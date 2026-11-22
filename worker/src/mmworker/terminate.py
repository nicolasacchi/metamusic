"""The Terminator (C397.4): how a pod kills itself. Fixed order, each step logged, never the key.

(1) DELETE /v2/pods/<id>; (2) POST .../action {"action":"terminate"}; (3) POST .../action {"action":"stop"} (a stopped
pod bills no compute); after 60 s of failing, or of waiting for a death that does not come, os._exit(3): a restarted
container sees the deadline and starts again at step 1. The bearer is the pod-scoped key RunPod injects."""

from __future__ import annotations

import http.client
import json
import os
import ssl
import sys
import threading
import time
from collections.abc import Callable
from importlib import metadata
from urllib.parse import urlsplit

API = "https://api.runpod.io/v2"
GIVE_UP_S = 60.0
RETRY_S = 5.0

# request(method, url, headers, body) -> status (0 on a network failure)
Request = Callable[[str, str, dict[str, str], bytes | None], int]


def worker_version() -> str:
    try:
        return metadata.version("metamusic-worker")
    except metadata.PackageNotFoundError:
        return "0.0.0"


def user_agent() -> str:
    return f"metamusic-worker/{worker_version()}"  # never urllib's: Cloudflare 1010 blocks it


def http_request(method: str, url: str, headers: dict[str, str], body: bytes | None, timeout: float = 15.0) -> int:
    u = urlsplit(url)
    conn: http.client.HTTPConnection
    if u.scheme == "https":
        conn = http.client.HTTPSConnection(u.netloc, timeout=timeout, context=ssl.create_default_context())
    else:
        conn = http.client.HTTPConnection(u.netloc, timeout=timeout)
    try:
        conn.request(method, u.path, body=body, headers=headers)
        resp = conn.getresponse()
        resp.read()
        return resp.status
    except (OSError, http.client.HTTPException):
        return 0
    finally:
        conn.close()


def _log(line: str) -> None:
    print(line, file=sys.stderr, flush=True)


class Terminator:
    def __init__(self, pod_id: str, key: str, *, request: Request = http_request,
                 clock: Callable[[], float] = time.monotonic, sleep: Callable[[float], None] = time.sleep,
                 hard_exit: Callable[[int], object] = os._exit, log: Callable[[str], None] = _log,
                 api: str = API, give_up_s: float = GIVE_UP_S, retry_s: float = RETRY_S) -> None:
        self._pod = pod_id
        self._key = key
        self._request, self._clock, self._sleep = request, clock, sleep
        self._exit, self._log = hard_exit, log
        self._api, self._give_up, self._retry = api, give_up_s, retry_s
        self._lock = threading.Lock()
        self.ran = False

    def _step(self, n: int) -> bool:
        base = f"{self._api}/pods/{self._pod}"
        headers = {"Authorization": f"Bearer {self._key}", "User-Agent": user_agent(), "Accept": "application/json"}
        if n == 1:
            status = self._request("DELETE", base, headers, None)
        else:
            headers["Content-Type"] = "application/json"
            body = json.dumps({"action": "terminate" if n == 2 else "stop"}).encode()
            status = self._request("POST", base + "/action", headers, body)
        ok = 200 <= status < 300
        self._log(f"terminator: step {n} " + ("ok" if ok else f"failed:{status}"))
        return ok

    def run(self) -> None:
        """Idempotent: a second caller (the main loop and the supervisor may both decide) returns at once."""
        with self._lock:
            if self.ran:
                return
            self.ran = True
        t0 = self._clock()
        while True:
            if any(self._step(n) for n in (1, 2, 3)):
                break
            if self._clock() - t0 >= self._give_up:
                self._exit(3)
                return
            self._sleep(self._retry)
        # accepted: the platform kills the container; if it has not within the give-up window, restart it ourselves.
        while self._clock() - t0 < self._give_up:
            self._sleep(self._retry)
        self._exit(3)
