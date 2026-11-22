"""The agent's two clocks (C397.5/.6): the absolute deadline and the dead-man switch.

The dead-man is fed only by a 2xx poll answer (an app that answers 403, 5xx or not at all is an app that has
lost this pod). It is a pure state object on an injected clock; the agent decides what to do when it trips."""

from __future__ import annotations

import time
from collections.abc import Callable

NO_NEW_TAKE_S = 120  # deadline - 120 s: no new take
DEADLINE = "deadline"
DEADMAN = "deadman"


class Watchdog:
    def __init__(self, deadline_unix: float, deadman_s: float, clock: Callable[[], float] = time.time, *,
                 no_take_s: float = NO_NEW_TAKE_S) -> None:
        self._clock = clock
        self.deadline = float(deadline_unix)
        self.deadman_s = float(deadman_s)
        self.no_take_s = float(no_take_s)
        self.last_ok = clock()

    def fed(self, status: int) -> bool:
        """Record a poll answer; only a 2xx resets the dead-man. Returns whether it did."""
        if 200 <= status < 300:
            self.last_ok = self._clock()
            return True
        return False

    def tighten(self, deadline_unix: float) -> None:
        """The app's answer may bring the deadline forward, never push it back."""
        if 0 < deadline_unix < self.deadline:
            self.deadline = float(deadline_unix)

    def expired(self) -> str | None:
        """`deadline` or `deadman` when the Terminator must run now, else None."""
        now = self._clock()
        if now >= self.deadline:
            return DEADLINE
        if now - self.last_ok >= self.deadman_s:
            return DEADMAN
        return None

    def may_take(self) -> bool:
        return self._clock() < self.deadline - self.no_take_s
