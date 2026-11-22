"""MODE=selftest: the full handler path against FakePipeline and a local sink. Exit 0 when it passes."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from . import agent, caps, stages
from .contract import ACESTEP, ARTIFACTS, CONTRACT_V2, MAX_BYTES, artifacts_for, artifacts_v2, parse_input, parse_job
from .handler import process
from .pipeline import FakePipeline
from .start import StartClient
from .upload import Uploader

_WEIGHTS = {"m-a-p/YuE2-3B": "a" * 40, "m-a-p/YuE2-Vae": "b" * 40}


class _Sink(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), _SinkHandler)
        self.puts: dict[str, bytes] = {}
        self.starts = 0
        self.bad_digest = 0


class _SinkHandler(BaseHTTPRequestHandler):
    server: _Sink

    def log_message(self, *a: Any) -> None:  # silence
        pass

    def _body(self) -> bytes:
        return self.rfile.read(int(self.headers.get("Content-Length", "0")))

    def do_POST(self) -> None:
        self._body()
        self.server.starts += 1
        self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_PUT(self) -> None:
        data = self._body()
        want = "sha-256=:" + base64.b64encode(hashlib.sha256(data).digest()).decode() + ":"
        if self.headers.get("Content-Digest") != want:
            self.server.bad_digest += 1
            self.send_response(422)
        else:
            self.server.puts[self.path.lstrip("/")] = data
            self.send_response(201)
        self.send_header("Content-Length", "0")
        self.end_headers()


def _acestep_job(base: str) -> tuple[dict[str, Any], Any]:
    """A text2music job with an LRC pass against FakeAceStep (no torch, no GPU): the same handler path as a render."""
    from .engines import acestep  # noqa: PLC0415 - only when selected (the yue2 image never imports it)

    weights = {acestep.REPO_MAIN: "c" * 40}
    uploads = {n: {"url": f"{base}/{n}", "max_bytes": m} for n, m in artifacts_for(ACESTEP)}
    raw = {
        "contract": "mm.worker/1", "take_id": "selftest-take", "attempt_id": "selftest-attempt", "engine": ACESTEP,
        "request": {"id": "selftest", "task": "text2music", "caption": "pop", "lyrics": "[Verse]\nla la",
                    "instrumental": False, "vocal_language": "en", "seed": 1, "thinking": False, "lrc": True,
                    "engine_params_version": acestep.params_file()["current"], "inference_steps": 8,
                    "duration_ds": 100},
        "timeout_s": 120, "upload_budget_s": 60, "deadline_unix": int(time.time()) + 600,
        "start": {"url": f"{base}/start"}, "uploads": uploads, "expect": {"weights": weights},
    }
    adapter = acestep.Adapter(acestep.FakeAceStep(("turbo",), max_audio_s=1.0), weights=weights,
                              classes=frozenset({"turbo"}))
    return raw, adapter


def _run_v2(base: str, sink: _Sink) -> bool:
    """A plain /2 render against the fake pipeline: the /2 output must carry kind render and this image's caps."""
    sink.puts.clear()
    sink.starts = 0
    raw = {
        "contract": CONTRACT_V2, "kind": "render", "take_id": "selftest-take-2", "attempt_id": "selftest-attempt-2",
        "engine": "yue2",
        "request": {"cot": "off", "id": "selftest", "lyrics": "[Verse]\nla la", "seed": 1, "style": "pop"},
        "timeout_s": 120, "upload_budget_s": 60, "deadline_unix": int(time.time()) + 600,
        "start": {"url": f"{base}/start"},
        "uploads": {n: {"url": f"{base}/{n}", "max_bytes": m} for n, m, _ in artifacts_v2("render")},
        "expect": {"weights": _WEIGHTS},
    }
    take = parse_job(raw)
    out = process(take, FakePipeline(weights=_WEIGHTS), Uploader(), time.time, start=StartClient(take.start_url),
                  job_id="selftest-job-2")
    doc = json.loads(sink.puts.get("mm-result.json", b"{}"))
    return (out.get("status") == "ok" and sink.starts == 1 and sink.bad_digest == 0 and doc.get("kind") == "render"
            and doc.get("contract") == CONTRACT_V2 and doc.get("caps") == caps.advertised())


class _StubTerminator:
    """Stands in for the Terminator: the selftest never calls the RunPod API."""

    def __init__(self) -> None:
        self.runs = 0

    def run(self) -> None:
        self.runs += 1


def _run_agent() -> bool:
    """MODE=agent against a local fake app that answers `stop` to the first poll: the agent must terminate at once,
    having fetched and loaded nothing (poll first, D-BULK-15)."""
    polls: list[dict[str, Any]] = []

    class App(BaseHTTPRequestHandler):
        def log_message(self, *a: Any) -> None:
            pass

        def do_POST(self) -> None:
            polls.append(json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0")))))
            body = json.dumps({"action": "stop", "wait_s": 0, "take": None, "cancel": [],
                               "deadline_unix": int(time.time()) + 600}).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), App)
    threading.Thread(target=srv.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True).start()
    try:
        env = {"MM_POLL_URL": f"http://127.0.0.1:{srv.server_address[1]}/worker/v1/pods/selftest/poll?exp=1&sig=x",
               "MM_DEADLINE_UNIX": str(int(time.time()) + 600), "MM_YUE2_3B_REVISION": "a" * 40}
        term = _StubTerminator()

        def never(*a: Any, **k: Any) -> Any:
            raise AssertionError("weights fetched before the first poll answered")

        code = agent.Agent(env, terminator=term, fetch=never, loader=never,  # type: ignore[arg-type]
                           hard_exit=lambda c: None).run(supervise=False)
        return code == agent.EXIT_TERMINATOR and term.runs == 1 and len(polls) == 1 and polls[0]["state"] == "booting"
    finally:
        srv.shutdown()
        srv.server_close()


def run(engine: str | None = None) -> bool:
    engine = engine or os.environ.get("MM_ENGINE") or os.environ.get("ENGINE") or "yue2"
    sink = _Sink()
    port = sink.server_address[1]
    t = threading.Thread(target=sink.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    t.start()
    try:
        base = f"http://127.0.0.1:{port}"
        if engine == ACESTEP:
            raw, pipe = _acestep_job(base)
            take = parse_input(raw)
            out = process(take, pipe, Uploader(), time.time, start=StartClient(take.start_url),
                          job_id="selftest-job")
            return (out.get("status") == "ok" and sink.starts == 1 and sink.bad_digest == 0
                    and {"audio.flac", "lyrics.lrc", "mm-result.json"} <= set(sink.puts)
                    and json.loads(sink.puts["mm-result.json"])["env"]["engine"] == ACESTEP)
        uploads = {n: {"url": f"{base}/{n}", "max_bytes": MAX_BYTES[n]} for n in ARTIFACTS}
        raw = {
            "contract": "mm.worker/1", "take_id": "selftest-take", "attempt_id": "selftest-attempt",
            "engine": "yue2",
            "request": {"cot": "off", "id": "selftest", "lyrics": "[Verse]\nla la", "seed": 1, "style": "pop"},
            "timeout_s": 120, "upload_budget_s": 60, "deadline_unix": int(time.time()) + 600,
            "start": {"url": f"{base}/start"}, "uploads": uploads, "expect": {"weights": _WEIGHTS},
        }
        take = parse_input(raw)
        out = process(take, FakePipeline(weights=_WEIGHTS), Uploader(), time.time,
                      start=StartClient(take.start_url), job_id="selftest-job")
        ok = (out.get("status") == "ok" and sink.starts == 1 and sink.bad_digest == 0
              and "mm-result.json" in sink.puts and "audio.flac" in sink.puts
              and json.loads(sink.puts["mm-result.json"])["take_id"] == "selftest-take")
        return ok and _run_v2(base, sink) and _run_agent()
    finally:
        sink.shutdown()
        sink.server_close()


def main() -> int:
    stages.discover()
    ok = run()
    if ok and (os.environ.get("MM_ENGINE") or os.environ.get("ENGINE") or "yue2") != ACESTEP:
        print("caps: " + caps.label(caps.advertised()))  # CI compares this line with LABEL io.metamusic.caps
    print("selftest: " + ("ok" if ok else "FAILED"), file=sys.stderr)
    return 0 if ok else 1
