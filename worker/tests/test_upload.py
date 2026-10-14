from __future__ import annotations

import hashlib
from pathlib import Path

import pytest

from mmworker.contract import Target
from mmworker.errors import WorkerError
from mmworker.redact import message, text
from mmworker.upload import Uploader

from .conftest import Clock, Sink, sha_b64


def target(sink: Sink, path: str = "/a") -> Target:
    return Target(f"{sink.base}{path}?exp=1&sig=abc", 1 << 20)


@pytest.fixture
def data(tmp_path: Path) -> Path:
    p = tmp_path / "f.bin"
    p.write_bytes(b"hello world" * 100)
    return p


def test_upload_retry_then_ok(sink: Sink, data: Path) -> None:
    sink.on("/a", 503, 503, 201)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), c.now + 3600)
    assert len(sink.puts("/a")) == 3 and c.sleeps == [1.0, 2.0]


def test_upload_retry_after(sink: Sink, data: Path) -> None:
    sink.on("/a", (429, {"Retry-After": "7"}), 200)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), None)
    assert c.sleeps == [7.0]


def test_upload_backoff_sequence(sink: Sink, data: Path) -> None:
    sink.on("/a", 500, 500, 500, 500, 500, 500, 500, 408, 200)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), None)
    assert c.sleeps == [1, 2, 4, 8, 16, 30, 30, 30]


def test_upload_connection_error_retries(data: Path) -> None:
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep, timeout=1).put(
            "a", data, Target("http://127.0.0.1:1/a?exp=1&sig=s", 10), c.now + 10)
    assert e.value.code == "DEADLINE" and "sig=" not in str(e.value)


@pytest.mark.parametrize(("status", "code"), [
    (403, "UPLOAD_FAILED"), (409, "UPLOAD_CONFLICT"), (410, "UPLOAD_CLOSED"), (411, "UPLOAD_FAILED"),
    (413, "UPLOAD_FAILED"), (400, "UPLOAD_FAILED"), (404, "UPLOAD_FAILED")])
def test_upload_aborts(sink: Sink, data: Path, status: int, code: str) -> None:
    sink.on("/a", status)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), None)
    assert e.value.code == code and len(sink.puts("/a")) == 1 and c.sleeps == []
    assert "sig=" not in str(e.value)


def test_upload_conflict_aborts(sink: Sink, data: Path) -> None:
    test_upload_aborts(sink, data, 409, "UPLOAD_CONFLICT")


def test_upload_closed_aborts(sink: Sink, data: Path) -> None:
    test_upload_aborts(sink, data, 410, "UPLOAD_CLOSED")


def test_upload_422_at_most_three(sink: Sink, data: Path) -> None:
    sink.on("/a", 422)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), None)
    assert e.value.code == "UPLOAD_FAILED" and len(sink.puts("/a")) == 3


def test_upload_422_then_ok(sink: Sink, data: Path) -> None:
    sink.on("/a", 422, 201)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), None)
    assert len(sink.puts("/a")) == 2


def test_upload_digest_header(sink: Sink, data: Path) -> None:
    Uploader().put("a", data, target(sink), None)
    (req,) = sink.puts("/a")
    assert req.headers["Content-Digest"] == f"sha-256=:{sha_b64(req.body)}:"
    assert req.body == data.read_bytes()
    assert req.headers["Content-Length"] == str(len(req.body))
    assert req.headers["Content-Type"] == "application/octet-stream"
    assert req.headers["User-Agent"].startswith("metamusic-worker/")
    # a precomputed digest is used as is
    sink.requests.clear()
    Uploader().put("a", data, target(sink), None, sha256=hashlib.sha256(req.body).hexdigest())
    assert sink.puts("/a")[0].headers["Content-Digest"] == req.headers["Content-Digest"]


def test_upload_deadline_stops_retries(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, target(sink), c.now + 5)
    assert e.value.code == "DEADLINE"
    assert c.sleeps == [1.0, 2.0]  # the next 4 s sleep would pass the deadline
    assert len(sink.puts("/a")) == 3


# ---- redact ------------------------------------------------------------------------------------------

def test_redact() -> None:
    s = text("x https://mm.example.com/p/q?exp=1&sig=abc y; sig=zzz Bearer abcdefghij12 token=abc12345678")
    assert "?<redacted>" in s and "sig=" not in s and "exp=" not in s and "abcdefghij12" not in s
    assert text("plain") == "plain"
    assert len(message("x" * 1000)) == 500
