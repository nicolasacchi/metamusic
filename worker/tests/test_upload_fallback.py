"""C399.7: the worker's upload fallback channel (BULK-R3)."""

from __future__ import annotations

import socket
from pathlib import Path
from typing import Any

import pytest

from mmworker import caps, handler, selftest
from mmworker.contract import Target, parse_job, parse_v2
from mmworker.errors import WorkerError
from mmworker.upload import Uploader

from .conftest import Clock, Sink, make_raw
from .test_v2 import raw2

SECRET = "-".join(["fb", "canary", "value"])


@pytest.fixture
def data(tmp_path: Path) -> Path:
    p = tmp_path / "f.bin"
    p.write_bytes(b"hello world" * 100)
    return p


def dead_url() -> str:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return f"http://127.0.0.1:{port}/a?sig=primary"


def tgt(sink: Sink, primary: str | None = None, after: int = 60, fb: bool = True) -> Target:
    return Target(primary or f"{sink.base}/a?sig=primary", 1 << 20,
                  f"{sink.base}/fb?X-Amz-Signature={SECRET}" if fb else "", after if fb else 0)


def test_fallback_after_primary_down(sink: Sink, data: Path) -> None:
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink, dead_url()), c.now + 3600)
    (req,) = sink.puts("/fb")
    assert req.body == data.read_bytes()
    assert req.headers["Content-Length"] == str(data.stat().st_size)
    assert req.headers["Content-Type"] == "application/octet-stream"
    assert req.headers["User-Agent"].startswith("metamusic-worker/")
    assert "Content-Digest" not in req.headers
    assert sum(c.sleeps) >= 60 and sum(c.sleeps[:-1]) < 60  # switched as soon as the window was over, no extra sleep


def test_fallback_on_5xx_after_window(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert len(sink.puts("/a")) > 1 and len(sink.puts("/fb")) == 1


@pytest.mark.parametrize("status", [408, 429])
def test_fallback_on_retryable_4xx_after_window(sink: Sink, data: Path, status: int) -> None:
    sink.on("/a", status)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert len(sink.puts("/fb")) == 1


def test_no_fallback_before_window(sink: Sink, data: Path) -> None:
    sink.on("/a", 503, 503, 201)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert sink.puts("/fb") == [] and len(sink.puts("/a")) == 3


@pytest.mark.parametrize("status,code", [(403, "UPLOAD_FAILED"), (410, "UPLOAD_CLOSED"), (413, "UPLOAD_FAILED"),
                                         (411, "UPLOAD_FAILED")])
def test_no_fallback_on_answered_4xx(sink: Sink, data: Path, status: int, code: str) -> None:
    sink.on("/a", status)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert e.value.code == code and sink.puts("/fb") == []


def test_no_fallback_on_403(sink: Sink, data: Path) -> None:
    test_no_fallback_on_answered_4xx(sink, data, 403, "UPLOAD_FAILED")


def test_no_fallback_on_410(sink: Sink, data: Path) -> None:
    test_no_fallback_on_answered_4xx(sink, data, 410, "UPLOAD_CLOSED")


def test_no_fallback_on_409_conflict(sink: Sink, data: Path) -> None:
    sink.on("/a", (409, {"_body": '{"code":"UPLOAD_CONFLICT"}'}))
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert e.value.code == "UPLOAD_CONFLICT" and sink.puts("/fb") == []


def test_no_fallback_on_422_x3_even_after_transport_failure(sink: Sink, data: Path) -> None:
    sink.on("/a", 503, 422)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink, after=600), c.now + 3600)
    assert e.value.code == "UPLOAD_FAILED" and sink.puts("/fb") == []


def test_no_fallback_target_keeps_retrying_primary(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink, fb=False), c.now + 200)
    assert e.value.code == "DEADLINE" and sink.puts("/fb") == []


def test_primary_success_resets_window(sink: Sink, data: Path) -> None:
    sink.on("/a", 503, 201)
    sink.on("/b", 503, 201)
    c = Clock()
    up = Uploader(clock=c, sleep=c.sleep)
    up.put("a", data, tgt(sink), c.now + 3600)
    c.advance(500)  # a long time later: the earlier failure must not count against the next artifact
    up.put("b", data, tgt(sink, f"{sink.base}/b?sig=p"), c.now + 3600)
    assert sink.puts("/fb") == []


def test_fallback_sticky(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    c = Clock()
    up = Uploader(clock=c, sleep=c.sleep)
    up.put("a", data, tgt(sink), c.now + 3600)
    n_primary = len(sink.puts("/a"))
    sink.on("/a", 201)  # the primary is back, the attempt stays on the fallback
    up.put("b", data, tgt(sink), c.now + 3600)
    up.put("c", data, tgt(sink), c.now + 3600)
    assert len(sink.puts("/fb")) == 3 and len(sink.puts("/a")) == n_primary


def test_fallback_retried_until_ok(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    sink.on("/fb", 503, 500, 200)
    c = Clock()
    Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert len(sink.puts("/fb")) == 3


def test_fallback_respects_upload_deadline(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    sink.on("/fb", 503)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 100)
    assert e.value.code == "DEADLINE" and len(sink.puts("/fb")) >= 1
    assert c.now <= 1_800_000_000.0 + 100
    assert SECRET not in str(e.value)


def test_fallback_refused_is_final_and_redacted(sink: Sink, data: Path) -> None:
    sink.on("/a", 503)
    sink.on("/fb", 403)
    c = Clock()
    with pytest.raises(WorkerError) as e:
        Uploader(clock=c, sleep=c.sleep).put("a", data, tgt(sink), c.now + 3600)
    assert e.value.code == "UPLOAD_FAILED" and len(sink.puts("/fb")) == 1
    assert SECRET not in str(e.value) and "X-Amz" not in str(e.value) and "sig=" not in str(e.value)


def test_fallback_url_redacted(sink: Sink) -> None:
    t = tgt(sink)
    assert SECRET not in repr(t) and "fb" not in repr(t) and "sig=" not in repr(t)
    assert SECRET not in str(parse_v2(_doc_with_fallback(sink.base)))


def _doc_with_fallback(base: str) -> dict[str, Any]:
    d = raw2(fallback_after_s=120)
    for t in d["uploads"].values():
        t["fallback"] = {"url": f"{base}/fb?X-Amz-Signature={SECRET}"}
    return d


def test_fallback_fields_only_in_v2(sink: Sink) -> None:
    take = parse_job(_doc_with_fallback(sink.base))
    assert take.fallback_after_s == 120
    assert all(t.fallback_url.endswith(SECRET) and t.fallback_after_s == 120 for t in take.uploads.values())
    plain = parse_job(raw2())
    assert all(t.fallback_url == "" and t.fallback_after_s == 0 for t in plain.uploads.values())
    # /1 has no fallback key: the document is refused, and its targets never carry one
    v1 = make_raw(sink.base)
    assert all(t.fallback_url == "" for t in parse_job(v1).uploads.values())
    v1["uploads"]["audio.flac"]["fallback"] = {"url": f"{sink.base}/fb"}
    with pytest.raises(WorkerError):
        parse_job(v1)


def test_caps_fallback_registered(monkeypatch: pytest.MonkeyPatch) -> None:
    assert "fallback" in caps.TRANSPORT_CAPS and caps.BUILT_IN_TRANSPORT == ("agent", "fallback")
    assert "fallback" in caps.advertised() and "agent" in caps.advertised()
    assert caps.advertised() == caps.image_caps(["agent", "fallback"])
    assert handler.caps_mod.advertised is caps.advertised and selftest.caps is caps
    # Dockerfile label equals the advertised list (the stage lane's label test covers the stage part)
    text = (Path(__file__).resolve().parents[1] / "Dockerfile").read_text()
    assert 'io.metamusic.caps="agent analyze ears fallback v2"' in text


def test_window_clamps_long_retry_after(sink: Sink, data: Path) -> None:
    c = Clock()
    up = Uploader(clock=c, sleep=c.sleep)
    real = up._once

    def once(*a: Any, **k: Any) -> tuple[int, float | None, str | None]:
        return real(*a, **k) if k.get("fallback") else (429, 900.0, None)

    up._once = once  # type: ignore[method-assign]
    up.put("a", data, tgt(sink, after=60), c.now + 100)  # Retry-After 900 would pass the deadline; the window ends first
    assert len(sink.puts("/fb")) == 1 and c.sleeps == [60]


def test_answered_422_restarts_window(sink: Sink, data: Path) -> None:
    c = Clock()
    up = Uploader(clock=c, sleep=c.sleep)
    seq = iter([(503, None, None), (422, 100.0, None), (422, 100.0, None), (422, 100.0, None)])
    real = up._once
    up._once = lambda *a, **k: real(*a, **k) if k.get("fallback") else next(seq)  # type: ignore[method-assign]
    with pytest.raises(WorkerError) as e:
        up.put("a", data, tgt(sink, after=60), c.now + 3600)
    assert e.value.code == "UPLOAD_FAILED" and sink.puts("/fb") == []


def test_process_v2_dead_primary_uses_fallback(sink: Sink) -> None:
    from .test_v2 import run2

    raw = raw2(dead_url().rsplit("/", 1)[0])
    raw["fallback_after_s"] = 60
    for n, u in raw["uploads"].items():
        u["fallback"] = {"url": f"{sink.base}/fb/{n}?X-Amz-Signature={SECRET}"}
    out = run2(sink, raw)
    assert out["status"] == "ok", out.get("error")
    paths = [r.path for r in sink.puts()]
    assert paths and all(p.startswith("/fb/") for p in paths)
    assert "/fb/mm-result.json" in paths
    assert SECRET not in repr(out)
