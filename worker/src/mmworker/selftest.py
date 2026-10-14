"""MODE=selftest: the full handler path against FakePipeline and a local sink. Exit 0 when it passes."""

from __future__ import annotations

import base64
import hashlib
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .contract import ARTIFACTS, MAX_BYTES, parse_input
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


def run() -> bool:
    sink = _Sink()
    port = sink.server_address[1]
    t = threading.Thread(target=sink.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    t.start()
    try:
        base = f"http://127.0.0.1:{port}"
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
        return ok
    finally:
        sink.shutdown()
        sink.server_close()


def main() -> int:
    ok = run()
    print("selftest: " + ("ok" if ok else "FAILED"), file=sys.stderr)
    return 0 if ok else 1
