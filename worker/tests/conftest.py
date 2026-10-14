"""Shared fixtures: socket guard (loopback only), a scriptable local sink, a manual clock."""

from __future__ import annotations

import base64
import hashlib
import socket
import threading
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import pytest

from mmworker.contract import ARTIFACTS, MAX_BYTES, TakeInput, parse_input
from mmworker.handler import process
from mmworker.pipeline import FakePipeline
from mmworker.start import StartClient
from mmworker.upload import Uploader

WEIGHTS = {"m-a-p/YuE2-3B": "a" * 40, "m-a-p/YuE2-Vae": "b" * 40}


@pytest.fixture(autouse=True)
def _socket_guard(monkeypatch: pytest.MonkeyPatch) -> None:
    """Unit tests never leave the machine: only loopback connects are allowed."""
    real = socket.socket.connect

    def guarded(self: socket.socket, address: Any) -> None:
        host = address[0] if isinstance(address, tuple) else address
        if host not in ("127.0.0.1", "::1", "localhost") and not str(host).startswith("/"):
            raise AssertionError(f"network access in a unit test: {host!r}")
        return real(self, address)

    monkeypatch.setattr(socket.socket, "connect", guarded)
    real_gai = socket.getaddrinfo

    def gai(host: Any, *a: Any, **k: Any) -> Any:
        if host not in ("127.0.0.1", "localhost", "::1", None):
            raise AssertionError(f"DNS lookup in a unit test: {host!r}")
        return real_gai(host, *a, **k)

    monkeypatch.setattr(socket, "getaddrinfo", gai)


class Clock:
    """Manual clock; sleep advances it and records the request."""

    def __init__(self, now: float = 1_800_000_000.0) -> None:
        self.now = now
        self.sleeps: list[float] = []

    def __call__(self) -> float:
        return self.now

    def sleep(self, s: float) -> None:
        self.sleeps.append(s)
        self.now += s

    def advance(self, s: float) -> None:
        self.now += s


@dataclass
class Request:
    method: str
    path: str
    headers: dict[str, str]
    body: bytes


@dataclass
class Sink:
    base: str
    script: dict[str, list[tuple[int, dict[str, str]]]] = field(default_factory=dict)
    requests: list[Request] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def on(self, path: str, *statuses: int | tuple[int, dict[str, str]]) -> None:
        self.script[path] = [s if isinstance(s, tuple) else (s, {}) for s in statuses]

    def puts(self, path: str | None = None) -> list[Request]:
        return [r for r in self.requests if r.method == "PUT" and (path is None or r.path == path)]


@pytest.fixture
def sink() -> Iterator[Sink]:
    holder: dict[str, Sink] = {}

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a: Any) -> None:
            pass

        def _handle(self) -> None:
            s = holder["s"]
            n = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(n) if n else b""
            path = self.path.split("?")[0]
            with s.lock:
                s.requests.append(Request(self.command, path, {k: v for k, v in self.headers.items()}, body))
                queue = s.script.get(path)
                if queue is None:
                    status, hdrs = (201 if self.command == "PUT" else 200), {}
                elif len(queue) > 1:
                    status, hdrs = queue.pop(0)
                else:
                    status, hdrs = queue[0]
            self.send_response(status)
            for k, v in hdrs.items():
                self.send_header(k, v)
            self.send_header("Content-Length", "0")
            self.end_headers()

        do_PUT = do_POST = _handle

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    srv.daemon_threads = True
    holder["s"] = Sink(f"http://127.0.0.1:{srv.server_address[1]}")
    t = threading.Thread(target=srv.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    t.start()
    try:
        yield holder["s"]
    finally:
        srv.shutdown()
        srv.server_close()
        t.join(5)


def make_raw(base: str, **over: Any) -> dict[str, Any]:
    raw: dict[str, Any] = {
        "contract": "mm.worker/1", "take_id": "take-1", "attempt_id": "attempt-1", "engine": "yue2",
        "request": {"cot": "full", "id": "syn-a-s1", "lyrics": "[Verse]\nla la", "seed": 1102, "style": "pop"},
        "timeout_s": 186, "upload_budget_s": 60, "deadline_unix": 1_800_100_000,
        "start": {"url": f"{base}/start?exp=1&sig=abc"},
        "uploads": {n: {"url": f"{base}/{n}?exp=1&sig=abc", "max_bytes": MAX_BYTES[n]} for n in ARTIFACTS},
        "expect": {"weights": dict(WEIGHTS)},
    }
    raw.update(over)
    return raw


def sha_b64(data: bytes) -> str:
    return base64.b64encode(hashlib.sha256(data).digest()).decode()


class Post:
    """Scripted start-URL POST: statuses consumed in order, last repeats. Records (clock, status)."""

    def __init__(self, clock: Clock, *statuses: int | Callable[[float], int]) -> None:
        self.clock = clock
        self.statuses = list(statuses)
        self.calls: list[float] = []

    def __call__(self, url: str, body: bytes) -> int:
        self.calls.append(self.clock())
        s = self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]
        status = s(self.clock()) if callable(s) else s
        if status == 0:
            raise OSError("boom")
        return status


@dataclass
class Rig:
    sink: Sink
    clock: Clock
    take: TakeInput
    post: Post

    def run(self, pipe: FakePipeline | Any, **kw: Any) -> dict[str, Any]:
        return process(self.take, pipe, Uploader(clock=self.clock, sleep=self.clock.sleep), self.clock,
                       start=StartClient(self.take.start_url, post=self.post, clock=self.clock,
                                         sleep=self.clock.sleep),
                       job_id="job-1", spawn=lambda fn: fn(), **kw)


@pytest.fixture
def rig(sink: Sink) -> Rig:
    clock = Clock()
    return Rig(sink, clock, parse_input(make_raw(sink.base)), Post(clock, 200))


def make_rig(sink: Sink, *statuses: int | Callable[[float], int], **over: Any) -> Rig:
    clock = Clock()
    return Rig(sink, clock, parse_input(make_raw(sink.base, **over)), Post(clock, *(statuses or (200,))))


def fake(**kw: Any) -> FakePipeline:
    return FakePipeline(weights=dict(WEIGHTS), **kw)
