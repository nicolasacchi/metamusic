"""inputs.fetch (C154.3): verified fetch of signed URLs against loopback servers. No external network."""

from __future__ import annotations

import hashlib
import threading
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from mmworker import inputs
from mmworker.contract import InputRef
from mmworker.errors import WorkerError

from .conftest import Clock

DATA = b"RIFF-not-really-audio" * 50
SHA = hashlib.sha256(DATA).hexdigest()
SECRET = "sig=TOPSECRET123&exp=999"


@dataclass
class Origin:
    base: str
    script: dict[str, list[Callable[[BaseHTTPRequestHandler], None]]] = field(default_factory=dict)
    hits: list[tuple[str, dict[str, str]]] = field(default_factory=list)

    def on(self, path: str, *steps: Callable[[BaseHTTPRequestHandler], None]) -> None:
        self.script[path] = list(steps)

    def ref(self, path: str = "/a", data: bytes = DATA, **kw: object) -> InputRef:
        return InputRef(f"{self.base}{path}?{SECRET}", kw.get("sha", hashlib.sha256(data).hexdigest()),  # type: ignore[arg-type]
                        kw.get("bytes", len(data)))  # type: ignore[arg-type]


def body(data: bytes = DATA, status: int = 200) -> Callable[[BaseHTTPRequestHandler], None]:
    def step(h: BaseHTTPRequestHandler) -> None:
        h.send_response(status)
        h.send_header("Content-Length", str(len(data)))
        h.end_headers()
        h.wfile.write(data)

    return step


def status(code: int, **headers: str) -> Callable[[BaseHTTPRequestHandler], None]:
    def step(h: BaseHTTPRequestHandler) -> None:
        h.send_response(code)
        for k, v in headers.items():
            h.send_header(k.replace("_", "-"), v)
        h.send_header("Content-Length", "0")
        h.end_headers()

    return step


def short(data: bytes = DATA) -> Callable[[BaseHTTPRequestHandler], None]:
    """Announces len(data) but closes after half of it: a dropped connection."""

    def step(h: BaseHTTPRequestHandler) -> None:
        h.send_response(200)
        h.send_header("Content-Length", str(len(data)))
        h.end_headers()
        h.wfile.write(data[: len(data) // 2])
        h.close_connection = True

    return step


def chunked_no_length(data: bytes) -> Callable[[BaseHTTPRequestHandler], None]:
    """No Content-Length: the body ends when the connection closes (so a too-long body is only cut by the cap)."""

    def step(h: BaseHTTPRequestHandler) -> None:
        h.send_response(200)
        h.end_headers()
        h.wfile.write(data)
        h.close_connection = True

    return step


@pytest.fixture
def origin() -> Iterator[Origin]:
    holder: dict[str, Origin] = {}

    class H(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.0"

        def log_message(self, *a: object) -> None:
            pass

        def do_GET(self) -> None:
            o = holder["o"]
            path = self.path.split("?")[0]
            o.hits.append((self.path, {k: v for k, v in self.headers.items()}))
            steps = o.script.get(path)
            if not steps:
                body()(self)
            else:
                (steps.pop(0) if len(steps) > 1 else steps[0])(self)

    class Quiet(ThreadingHTTPServer):
        def handle_error(self, request: object, client_address: object) -> None:  # clients cut on purpose
            pass

    srv = Quiet(("127.0.0.1", 0), H)
    srv.daemon_threads = True
    holder["o"] = Origin(f"http://127.0.0.1:{srv.server_address[1]}")
    t = threading.Thread(target=srv.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    t.start()
    try:
        yield holder["o"]
    finally:
        srv.shutdown()
        srv.server_close()
        t.join(5)


def run(o: Origin, refs: dict[str, InputRef], tmp: Path, clock: Clock | None = None, *,
        left: float = 600.0) -> dict[str, Path]:
    c = clock or Clock()
    return inputs.fetch(refs, c() + left, tmp, clock=c, sleep=c.sleep)


def test_inputs_verified_before_use(origin: Origin, tmp_path: Path) -> None:
    out = run(origin, {"src_audio": origin.ref()}, tmp_path)
    assert out == {"src_audio": tmp_path / "src_audio"}
    assert out["src_audio"].read_bytes() == DATA
    assert not (tmp_path / "src_audio.part").exists()
    ua = origin.hits[0][1]["User-Agent"]
    assert ua.startswith("metamusic-worker/")


@pytest.mark.parametrize("kind", ["sha", "short_declared", "long_declared"])
def test_inputs_mismatch_no_render(origin: Origin, tmp_path: Path, kind: str) -> None:
    ref = {"sha": origin.ref(sha="0" * 64),
           "short_declared": origin.ref(bytes=len(DATA) + 1),
           "long_declared": origin.ref(bytes=len(DATA) - 1)}[kind]
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": ref}, tmp_path)
    assert ei.value.code == "INPUT_MISMATCH"
    # nothing unverified is left under the final name or as a partial file
    assert list(tmp_path.iterdir()) == []
    assert len(origin.hits) == 1, "a mismatch is terminal, never retried"


def test_inputs_length_cap(origin: Origin, tmp_path: Path) -> None:
    big = b"z" * (4 << 20)
    origin.on("/big", chunked_no_length(big))
    ref = InputRef(f"{origin.base}/big?{SECRET}", hashlib.sha256(big).hexdigest(), 1000)
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": ref}, tmp_path)
    assert ei.value.code == "INPUT_MISMATCH" and "more than" in ei.value.message
    assert list(tmp_path.iterdir()) == []


def test_inputs_url_redacted(origin: Origin, tmp_path: Path) -> None:
    origin.on("/gone", status(403))
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/gone")}, tmp_path)
    for text in (str(ei.value), ei.value.message, repr(ei.value), repr(origin.ref())):
        assert "TOPSECRET" not in text and "sig=" not in text and origin.base not in text
    assert ei.value.__cause__ is None and ei.value.__context__ is None or "TOPSECRET" not in str(ei.value.__context__)


def test_inputs_url_redacted_after_retries(origin: Origin, tmp_path: Path) -> None:
    origin.on("/flaky", status(503))
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/flaky")}, tmp_path)
    assert ei.value.code == "INPUT_FAILED" and "TOPSECRET" not in str(ei.value)


def test_inputs_retry_then_success(origin: Origin, tmp_path: Path) -> None:
    origin.on("/r", status(503), short(), body())
    c = Clock()
    out = run(origin, {"src_audio": origin.ref("/r")}, tmp_path, c)
    assert out["src_audio"].read_bytes() == DATA
    assert c.sleeps == [1.0, 2.0] and len(origin.hits) == 3


def test_inputs_retry_bounded(origin: Origin, tmp_path: Path) -> None:
    origin.on("/down", status(500))
    c = Clock()
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/down")}, tmp_path, c)
    assert ei.value.code == "INPUT_FAILED"
    assert c.sleeps == list(inputs.BACKOFF_S[: len(c.sleeps)])
    assert len(origin.hits) == len(c.sleeps) + 1 <= len(inputs.BACKOFF_S) + 1
    assert sum(c.sleeps) < inputs.TOTAL_BUDGET_S


def test_inputs_client_error_not_retried(origin: Origin, tmp_path: Path) -> None:
    origin.on("/denied", status(403))
    c = Clock()
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/denied")}, tmp_path, c)
    assert ei.value.code == "INPUT_FAILED" and c.sleeps == [] and len(origin.hits) == 1


def test_inputs_respect_deadline(origin: Origin, tmp_path: Path) -> None:
    origin.on("/down", status(500))
    c = Clock()
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/down")}, tmp_path, c, left=2.5)
    assert ei.value.code == "INPUT_FAILED"
    assert sum(c.sleeps) < 2.5, "a backoff never sleeps past the deadline"


def test_inputs_deadline_already_passed(origin: Origin, tmp_path: Path) -> None:
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref()}, tmp_path, left=-1)
    assert ei.value.code == "INPUT_FAILED" and origin.hits == []


def test_inputs_no_cross_host_redirect(origin: Origin, tmp_path: Path) -> None:
    other = origin.base.replace("127.0.0.1", "localhost")  # same server, another host name
    origin.on("/hop", status(302, Location=f"{other}/a?{SECRET}"))
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/hop")}, tmp_path)
    assert ei.value.code == "INPUT_FAILED" and "another host" in ei.value.message
    assert [h[0].split("?")[0] for h in origin.hits] == ["/hop"], "the foreign location is never requested"


def test_inputs_same_host_redirect_followed(origin: Origin, tmp_path: Path) -> None:
    origin.on("/hop", status(302, Location=f"/a?{SECRET}"))
    out = run(origin, {"src_audio": origin.ref("/hop")}, tmp_path)
    assert out["src_audio"].read_bytes() == DATA


def test_inputs_redirect_loop_bounded(origin: Origin, tmp_path: Path) -> None:
    origin.on("/loop", status(302, Location="/loop"))
    with pytest.raises(WorkerError) as ei:
        run(origin, {"src_audio": origin.ref("/loop")}, tmp_path)
    assert ei.value.code == "INPUT_FAILED" and len(origin.hits) == inputs.MAX_REDIRECTS + 1


def test_inputs_two_inputs_and_bad_name(origin: Origin, tmp_path: Path) -> None:
    ref = origin.ref()
    out = run(origin, {"src_audio": ref, "reference_audio": ref}, tmp_path)
    assert set(out) == {"src_audio", "reference_audio"}
    with pytest.raises(WorkerError) as ei:
        run(origin, {"../evil": ref}, tmp_path)
    assert ei.value.code == "BAD_INPUT"
