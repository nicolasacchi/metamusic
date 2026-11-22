"""The agent's deadline and dead-man (C397.5/.6) on a manual clock."""

from __future__ import annotations

import pytest

from mmworker.watchdog import DEADLINE, DEADMAN, Watchdog

from .conftest import Clock


def test_watchdog_deadline() -> None:
    clock = Clock()
    w = Watchdog(clock.now + 1000, 100_000, clock)
    assert w.expired() is None and w.may_take()
    clock.now += 879  # 121 s left
    assert w.may_take() and w.expired() is None
    clock.now += 1  # 120 s left: no new take, still alive
    assert not w.may_take() and w.expired() is None
    clock.now += 120
    assert w.expired() == DEADLINE


def test_watchdog_deadman() -> None:
    clock = Clock()
    w = Watchdog(clock.now + 100_000, 600, clock)
    clock.now += 599
    assert w.expired() is None
    clock.now += 1
    assert w.expired() == DEADMAN


def test_watchdog_resets_only_on_2xx() -> None:
    clock = Clock()
    w = Watchdog(clock.now + 100_000, 600, clock)
    for _ in range(5):
        clock.now += 500
        assert w.fed(200) and w.expired() is None
    clock.now += 600
    assert w.expired() == DEADMAN


@pytest.mark.parametrize("status", [0, 301, 400, 403, 404, 413, 500, 503])
def test_watchdog_403_does_not_reset(status: int) -> None:
    clock = Clock()
    w = Watchdog(clock.now + 100_000, 600, clock)
    clock.now += 599
    assert w.fed(status) is False
    clock.now += 1
    assert w.expired() == DEADMAN


def test_watchdog_deadline_only_tightens() -> None:
    clock = Clock()
    w = Watchdog(clock.now + 1000, 600, clock)
    w.tighten(clock.now + 5000)
    assert w.deadline == clock.now + 1000
    w.tighten(0)
    assert w.deadline == clock.now + 1000
    w.tighten(clock.now + 300)
    assert w.deadline == clock.now + 300
