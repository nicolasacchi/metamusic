"""The Terminator (C397.4): order, headers, secrecy, give-up."""

from __future__ import annotations

import json
from typing import Any

import pytest

from mmworker import terminate
from mmworker.terminate import Terminator

from .conftest import Clock

KEY = "-".join(["podkey", "dummy", "value"])  # a fake bearer


class Api:
    """request() fake: scripted statuses per (method, action); records every call."""

    def __init__(self, *statuses: int) -> None:
        self.statuses = list(statuses)
        self.calls: list[tuple[str, str, dict[str, str], bytes | None]] = []

    def __call__(self, method: str, url: str, headers: dict[str, str], body: bytes | None) -> int:
        self.calls.append((method, url, dict(headers), body))
        return self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]


def make(api: Api, clock: Clock, **kw: Any) -> tuple[Terminator, list[int], list[str]]:
    exits: list[int] = []
    logs: list[str] = []
    t = Terminator("pod123", KEY, request=api, clock=clock, sleep=clock.sleep, hard_exit=exits.append, log=logs.append,
                   **kw)
    return t, exits, logs


def test_terminator_order() -> None:
    clock = Clock()
    api = Api(204)
    t, exits, logs = make(api, clock)
    t.run()
    assert [(m, u) for m, u, _, _ in api.calls] == [("DELETE", "https://api.runpod.io/v2/pods/pod123")]
    assert logs == ["terminator: step 1 ok"]
    assert exits == [3]  # accepted but still alive after the give-up window: restart the container
    assert clock.now - 1_800_000_000.0 >= 60


def test_terminator_stop_when_terminate_refused() -> None:
    clock = Clock()
    api = Api(403, 403, 200)
    t, exits, logs = make(api, clock)
    t.run()
    assert [(m, u.rsplit("/v2/pods/", 1)[1], b) for m, u, _, b in api.calls] == [
        ("DELETE", "pod123", None),
        ("POST", "pod123/action", b'{"action": "terminate"}'),
        ("POST", "pod123/action", b'{"action": "stop"}'),
    ]
    assert logs[:3] == ["terminator: step 1 failed:403", "terminator: step 2 failed:403", "terminator: step 3 ok"]


def test_terminator_user_agent() -> None:
    clock = Clock()
    api = Api(403, 200)
    t, _, _ = make(api, clock)
    t.run()
    for _, _, h, _ in api.calls:
        assert h["User-Agent"].startswith("metamusic-worker/") and "urllib" not in h["User-Agent"].lower()
        assert h["Authorization"] == f"Bearer {KEY}"
    assert terminate.user_agent().startswith("metamusic-worker/")


def test_terminator_never_logs_key(capsys: pytest.CaptureFixture[str]) -> None:
    clock = Clock()
    api = Api(500, 500, 500, 204)
    t = Terminator("pod123", KEY, request=api, clock=clock, sleep=clock.sleep, hard_exit=lambda c: None)
    t.run()  # the default logger: stderr
    out = capsys.readouterr()
    assert KEY not in out.err + out.out and "terminator: step 1 failed:500" in out.err
    assert all(KEY not in line for line in capsys.readouterr().err.splitlines())


def test_terminator_exit_after_failures() -> None:
    clock = Clock()
    api = Api(0)  # a network failure is status 0
    t, exits, logs = make(api, clock)
    t.run()
    assert exits == [3]
    assert 60 <= clock.now - 1_800_000_000.0 < 70
    assert len(api.calls) % 3 == 0 and len(api.calls) >= 6, "every cycle is DELETE, terminate, stop"
    assert set(logs) == {f"terminator: step {n} failed:0" for n in (1, 2, 3)}


def test_terminator_is_idempotent() -> None:
    clock = Clock()
    api = Api(204)
    t, exits, _ = make(api, clock)
    t.run()
    t.run()
    assert len(api.calls) == 1 and exits == [3]


def test_terminator_request_body_and_failure_status() -> None:
    """The real request function maps a refused connection to status 0 (loopback, nothing listening)."""
    assert terminate.http_request("DELETE", "http://127.0.0.1:1/v2/pods/x", {}, None, timeout=2) == 0
    assert json.loads(b'{"action": "stop"}') == {"action": "stop"}
