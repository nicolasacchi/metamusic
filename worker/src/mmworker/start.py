"""Start handshake client (D-M3-28, C74.3): announce the RunPod job id, then re-poll for a 410."""

from __future__ import annotations

import http.client
import json
import ssl
import threading
import time
from collections.abc import Callable
from urllib.parse import urlsplit

from . import redact
from .errors import WorkerError

ANNOUNCE_BUDGET_S = 60.0
POLL_EVERY_S = 15.0
_BACKOFF = (1.0, 2.0, 4.0, 8.0, 16.0)

# post(url, body_bytes) -> status; raises OSError on network failure.
Post = Callable[[str, bytes], int]


def http_post(url: str, body: bytes, timeout: float = 20.0) -> int:
    """POST a small JSON body; returns the status. Network failures raise OSError."""
    u = urlsplit(url)
    if u.scheme == "https":
        conn: http.client.HTTPConnection = http.client.HTTPSConnection(
            u.netloc, timeout=timeout, context=ssl.create_default_context())
    else:
        conn = http.client.HTTPConnection(u.netloc, timeout=timeout)
    try:
        target = u.path + (f"?{u.query}" if u.query else "")
        conn.request("POST", target, body=body, headers={
            "Content-Type": "application/json", "Content-Length": str(len(body)),
            "User-Agent": "metamusic-worker"})
        resp = conn.getresponse()
        resp.read()
        return resp.status
    except (http.client.HTTPException, ssl.SSLError) as e:
        raise OSError(redact.text(e)) from None
    finally:
        conn.close()


class StartClient:
    def __init__(self, url: str, *, post: Post = http_post, clock: Callable[[], float] = time.time,
                 sleep: Callable[[float], None] = time.sleep) -> None:
        self._url = url
        self._post = post
        self._clock = clock
        self._sleep = sleep

    def _body(self, job_id: str) -> bytes:
        return json.dumps({"runpod_job_id": job_id}).encode()

    def announce(self, job_id: str) -> None:
        """200 (or 409, both ids are then live app-side) ⇒ return; 410 ⇒ ATTEMPT_CLOSED; network errors, 5xx,
        408 and 429 are retried for up to 60 s ⇒ START_FAILED; any other status ⇒ START_FAILED at once."""
        body = self._body(job_id)
        t0 = self._clock()
        i = 0
        while True:
            try:
                status = self._post(self._url, body)
            except OSError:
                status = 0
            if status in (200, 409):
                return
            if status == 410:
                raise WorkerError("ATTEMPT_CLOSED", "the app closed this attempt")
            if status not in (0, 408, 429) and status < 500:
                raise WorkerError("START_FAILED", f"start call refused with status {status}")
            delay = _BACKOFF[min(i, len(_BACKOFF) - 1)]
            i += 1
            if self._clock() + delay - t0 > ANNOUNCE_BUDGET_S:
                raise WorkerError("START_FAILED", "the start call kept failing for 60 s")
            self._sleep(delay)

    def closed(self, job_id: str) -> bool:
        """One poll: True only when the app answered 410. Failures count as not closed."""
        try:
            return self._post(self._url, self._body(job_id)) == 410
        except OSError:
            return False


def _spawn_thread(fn: Callable[[], None]) -> None:  # pragma: no cover - trivial
    threading.Thread(target=fn, daemon=True, name="mm-start-poll").start()


class CancelWatch:
    """The `cancelled()` callback handed to the pipeline. It never blocks the GPU loop: the 410 poll runs on a
    background thread, at most once per POLL_EVERY_S and never concurrently."""

    def __init__(self, start: StartClient, job_id: str, *, deadline: float | None,
                 clock: Callable[[], float] = time.time, every: float = POLL_EVERY_S,
                 spawn: Callable[[Callable[[], None]], None] = _spawn_thread) -> None:
        self._start = start
        self._job_id = job_id
        self.deadline = deadline
        self._clock = clock
        self._every = every
        self._spawn = spawn
        self._lock = threading.Lock()
        self._last = clock()  # the announce just succeeded
        self._inflight = False
        self.closed = False
        self.deadline_hit = False

    @property
    def tripped(self) -> bool:
        return self.closed or self.deadline_hit

    def _poll(self) -> None:
        try:
            if self._start.closed(self._job_id):
                self.closed = True
        finally:
            with self._lock:
                self._inflight = False

    def __call__(self) -> bool:
        now = self._clock()
        if self.deadline is not None and now > self.deadline:
            self.deadline_hit = True
        if self.closed:
            return True
        with self._lock:
            due = not self._inflight and now - self._last >= self._every
            if due:
                self._inflight = True
                self._last = now
        if due:
            self._spawn(self._poll)
        return self.tripped
