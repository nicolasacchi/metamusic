from __future__ import annotations

from typing import Any

import pytest

from mmworker.errors import WorkerError
from mmworker.start import CancelWatch, StartClient, http_post

from .conftest import Clock, Post, Sink, fake, make_rig


def test_start_announce_first_and_body(sink: Sink) -> None:
    StartClient(sink.base + "/start?exp=1&sig=s").announce("job-1")
    first = sink.requests[0]
    assert first.method == "POST" and first.path == "/start"
    import json

    assert json.loads(first.body) == {"runpod_job_id": "job-1"}


def test_start_410_skips_render(sink: Sink) -> None:
    rig = make_rig(sink, 410)
    pipe = fake()
    res = rig.run(pipe)
    assert res["error"].startswith("ATTEMPT_CLOSED") and res["mm"]["error"]["code"] == "ATTEMPT_CLOSED"
    assert pipe.calls == []
    assert sink.puts() == []


def test_start_failure_skips_render(sink: Sink) -> None:
    rig = make_rig(sink, 503)
    pipe = fake()
    res = rig.run(pipe)
    assert res["mm"]["error"]["code"] == "START_FAILED"
    assert pipe.calls == [] and sink.puts() == []
    assert 40 <= sum(rig.clock.sleeps) <= 60


def test_start_network_errors_then_ok(sink: Sink) -> None:
    rig = make_rig(sink, 0, 0, 200)
    out = rig.run(fake())
    assert out["status"] == "ok" and rig.clock.sleeps == [1.0, 2.0]


def test_start_other_4xx_fails_at_once(sink: Sink) -> None:
    rig = make_rig(sink, 403)
    res = rig.run(fake())
    assert res["mm"]["error"]["code"] == "START_FAILED" and rig.clock.sleeps == []


def test_start_conflict_continues(sink: Sink) -> None:
    assert make_rig(sink, 409).run(fake())["status"] == "ok"


def test_start_closed_poll() -> None:
    c = Clock()
    assert StartClient("http://x/y", post=Post(c, 410)).closed("j") is True
    assert StartClient("http://x/y", post=Post(c, 200)).closed("j") is False
    assert StartClient("http://x/y", post=Post(c, 0)).closed("j") is False


def test_http_post_real(sink: Sink) -> None:
    sink.on("/p", 410)
    assert http_post(sink.base + "/p?exp=1", b"{}") == 410
    with pytest.raises(OSError):
        http_post("http://127.0.0.1:1/x", b"{}", timeout=1)


def test_cancel_poll_stops_pipeline(sink: Sink) -> None:
    clock = Clock()
    t0 = clock.now
    rig = make_rig(sink)
    rig.clock = clock
    rig.post = Post(clock, 200, lambda now: 410 if now - t0 >= 20 else 200)
    rig.take = make_rig(sink).take
    stopped: list[float] = []

    def loop(kwargs: Any, cancelled: Any) -> None:
        for _ in range(500):
            if cancelled():
                stopped.append(clock.now)
                return
            clock.advance(1)

    res = rig.run(fake(on_call=loop))
    assert res["mm"]["error"]["code"] == "ATTEMPT_CLOSED"
    assert stopped and stopped[0] <= t0 + 20 + 15 + 1
    polls = rig.post.calls[1:]  # [0] is the announce
    assert polls and all(b - a >= 15 for a, b in zip(polls, polls[1:], strict=False))
    assert polls[0] - t0 >= 15
    assert sink.puts() == [], "no uploads after a 410"


def test_cancel_watch_never_overlaps_polls() -> None:
    c = Clock()
    pending: list[Any] = []
    w = CancelWatch(StartClient("http://x/y", post=Post(c, 200), clock=c), "j", deadline=None, clock=c,
                    spawn=pending.append)
    c.advance(16)
    assert w() is False and len(pending) == 1
    c.advance(16)
    assert w() is False and len(pending) == 1, "a poll is already in flight"
    pending.pop()()
    assert w() is False and len(pending) == 1, "more than 15 s since the last poll started"
    pending.pop()()
    c.advance(1)
    assert w() is False and len(pending) == 0, "rate limited to one poll per 15 s"


def test_cancel_watch_closed_is_sticky() -> None:
    c = Clock()
    pending: list[Any] = []
    w = CancelWatch(StartClient("http://x/y", post=Post(c, 410), clock=c), "j", deadline=None, clock=c,
                    spawn=pending.append)
    c.advance(16)
    w()
    pending.pop()()
    assert w() is True and w.closed and w.tripped


def test_start_client_error_type() -> None:
    c = Clock()
    with pytest.raises(WorkerError) as e:
        StartClient("http://x/y", post=Post(c, 410), clock=c, sleep=c.sleep).announce("j")
    assert e.value.code == "ATTEMPT_CLOSED"
