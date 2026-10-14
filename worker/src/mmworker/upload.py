"""Uploader (C83.3): streamed PUT with Content-Digest, retries with backoff, deadline-aware."""

from __future__ import annotations

import base64
import hashlib
import http.client
import socket
import ssl
import time
from collections.abc import Callable
from pathlib import Path
from urllib.parse import urlsplit

from . import __version__
from .contract import Target
from .errors import WorkerError

BACKOFF = (1.0, 2.0, 4.0, 8.0, 16.0, 30.0)
ATTEMPT_TIMEOUT_S = 60.0
MAX_422 = 3
_RETRY_STATUS = frozenset({408, 429})
_CHUNK = 1 << 20


def sha256_file(path: Path) -> tuple[str, int]:
    h = hashlib.sha256()
    n = 0
    with open(path, "rb") as f:
        while chunk := f.read(_CHUNK):
            h.update(chunk)
            n += len(chunk)
    return h.hexdigest(), n


class Uploader:
    def __init__(self, *, clock: Callable[[], float] = time.time,
                 sleep: Callable[[float], None] = time.sleep, timeout: float = ATTEMPT_TIMEOUT_S) -> None:
        self._clock = clock
        self._sleep = sleep
        self._timeout = timeout

    def _once(self, path: Path, target: Target, digest_b64: str, size: int) -> tuple[int, float | None]:
        u = urlsplit(target.url)
        if u.scheme == "https":
            conn: http.client.HTTPConnection = http.client.HTTPSConnection(
                u.netloc, timeout=self._timeout, context=ssl.create_default_context())
        else:
            conn = http.client.HTTPConnection(u.netloc, timeout=self._timeout)
        try:
            with open(path, "rb") as f:
                conn.request("PUT", u.path + (f"?{u.query}" if u.query else ""), body=f, headers={
                    "Content-Length": str(size),
                    "Content-Type": "application/octet-stream",
                    "Content-Digest": f"sha-256=:{digest_b64}:",
                    "User-Agent": f"metamusic-worker/{__version__}",
                })
                resp = conn.getresponse()
                resp.read()
                ra = resp.getheader("Retry-After")
                try:
                    retry_after = float(ra) if ra is not None else None
                except ValueError:
                    retry_after = None
                return resp.status, retry_after
        finally:
            conn.close()

    def put(self, name: str, path: Path, target: Target, deadline: float | None,
            sha256: str | None = None) -> None:
        """PUT path to target. Success on 200/201. Raises WorkerError (UPLOAD_FAILED, UPLOAD_CONFLICT,
        UPLOAD_CLOSED, DEADLINE); messages carry no URL query."""
        path = Path(path)
        if sha256 is None:
            sha256, size = sha256_file(path)
        else:
            size = path.stat().st_size
        digest_b64 = base64.b64encode(bytes.fromhex(sha256)).decode()
        attempt = 0
        n422 = 0
        while True:
            retry_after: float | None = None
            try:
                status, retry_after = self._once(path, target, digest_b64, size)
            except (OSError, http.client.HTTPException, socket.timeout, ssl.SSLError):
                status = 0
            if status in (200, 201):
                return
            if status == 403:
                raise WorkerError("UPLOAD_FAILED", f"{name}: upload link refused (403)")
            if status == 409:
                raise WorkerError("UPLOAD_CONFLICT", f"{name}: conflicting upload (409)")
            if status == 410:
                raise WorkerError("UPLOAD_CLOSED", f"{name}: upload closed (410)")
            if status == 422:
                n422 += 1
                if n422 >= MAX_422:
                    raise WorkerError("UPLOAD_FAILED", f"{name}: digest mismatch {n422} times")
            elif not (status == 0 or status >= 500 or status in _RETRY_STATUS):
                raise WorkerError("UPLOAD_FAILED", f"{name}: upload refused (status {status})")
            delay = BACKOFF[min(attempt, len(BACKOFF) - 1)]
            attempt += 1
            if retry_after is not None:
                delay = retry_after
            if deadline is not None and self._clock() + delay > deadline:
                raise WorkerError("DEADLINE", f"{name}: upload retries would pass the upload deadline")
            self._sleep(delay)

