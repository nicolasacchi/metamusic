"""Uploader (C83.3): streamed PUT with Content-Digest, retries with backoff, deadline-aware."""

from __future__ import annotations

import base64
import hashlib
import http.client
import json
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
_PROBLEM_MAX = 4096
IN_PROGRESS = "UPLOAD_IN_PROGRESS"


def sha256_file(path: Path) -> tuple[str, int]:
    h = hashlib.sha256()
    n = 0
    with open(path, "rb") as f:
        while chunk := f.read(_CHUNK):
            h.update(chunk)
            n += len(chunk)
    return h.hexdigest(), n


def _problem_code(raw: bytes) -> str | None:
    """The `code` of a problem+json body, or None when the body is not one."""
    try:
        doc = json.loads(raw)
    except ValueError:
        return None
    code = doc.get("code") if isinstance(doc, dict) else None
    return code if isinstance(code, str) else None


class Uploader:
    def __init__(self, *, clock: Callable[[], float] = time.time,
                 sleep: Callable[[float], None] = time.sleep, timeout: float = ATTEMPT_TIMEOUT_S) -> None:
        self._clock = clock
        self._sleep = sleep
        self._timeout = timeout
        # Per Uploader, and serverless builds one per attempt (C399.7): when the primary route first failed at the
        # transport level (None = healthy), and whether this attempt has switched to the fallback for good.
        self._first_failure: float | None = None
        self._on_fallback = False

    def _once(self, path: Path, target: Target, digest_b64: str, size: int, *, fallback: bool = False,
              ) -> tuple[int, float | None, str | None]:
        """One PUT. The fallback route is a plain presigned PUT: no Content-Digest (it is not in the signature)."""
        u = urlsplit(target.fallback_url if fallback else target.url)
        if u.scheme == "https":
            conn: http.client.HTTPConnection = http.client.HTTPSConnection(
                u.netloc, timeout=self._timeout, context=ssl.create_default_context())
        else:
            conn = http.client.HTTPConnection(u.netloc, timeout=self._timeout)
        try:
            with open(path, "rb") as f:
                headers = {
                    "Content-Length": str(size),
                    "Content-Type": "application/octet-stream",
                    "User-Agent": f"metamusic-worker/{__version__}",
                }
                if not fallback:
                    headers["Content-Digest"] = f"sha-256=:{digest_b64}:"
                conn.request("PUT", u.path + (f"?{u.query}" if u.query else ""), body=f, headers=headers)
                resp = conn.getresponse()
                raw = resp.read(_PROBLEM_MAX)
                ra = resp.getheader("Retry-After")
                try:
                    retry_after = float(ra) if ra is not None else None
                except ValueError:
                    retry_after = None
                return resp.status, retry_after, _problem_code(raw) if resp.status == 409 else None
        finally:
            conn.close()

    def _should_switch(self, target: Target) -> bool:
        """C399.7: use the fallback once the primary has failed at the transport level for fallback_after_s."""
        if not target.fallback_url:
            return False
        if self._on_fallback:
            return True
        return (self._first_failure is not None and target.fallback_after_s > 0
                and self._clock() - self._first_failure >= target.fallback_after_s)

    def _put_fallback(self, name: str, path: Path, target: Target, deadline: float | None, size: int) -> None:
        """PUT to the fallback until it answers 2xx. Retried with backoff until the upload deadline; an answered
        4xx (other than 408/429) is final: a presigned URL that is refused will not become valid. Messages never
        carry the URL."""
        attempt = 0
        while True:
            try:
                status, _, _ = self._once(path, target, "", size, fallback=True)
            except (OSError, http.client.HTTPException, socket.timeout, ssl.SSLError):
                status = 0
            if status in (200, 201):
                return
            if not (status == 0 or status >= 500 or status in _RETRY_STATUS):
                raise WorkerError("UPLOAD_FAILED", f"{name}: fallback upload refused (status {status})")
            delay = BACKOFF[min(attempt, len(BACKOFF) - 1)]
            attempt += 1
            if deadline is not None and self._clock() + delay > deadline:
                raise WorkerError("DEADLINE", f"{name}: fallback upload retries would pass the upload deadline")
            self._sleep(delay)

    def put(self, name: str, path: Path, target: Target, deadline: float | None,
            sha256: str | None = None) -> None:
        """PUT path to target. Success on 200/201. Retries network errors, 5xx, 408, 429 and 409
        UPLOAD_IN_PROGRESS. Raises WorkerError (UPLOAD_FAILED, UPLOAD_CONFLICT, UPLOAD_CLOSED, DEADLINE); messages carry no URL query."""
        path = Path(path)
        if sha256 is None:
            sha256, size = sha256_file(path)
        else:
            size = path.stat().st_size
        digest_b64 = base64.b64encode(bytes.fromhex(sha256)).decode()
        attempt = 0
        n422 = 0
        while True:
            if self._should_switch(target):
                self._on_fallback = True  # sticky: this artifact and every later one of the attempt
                self._put_fallback(name, path, target, deadline, size)
                return
            retry_after: float | None = None
            problem_code: str | None = None
            try:
                status, retry_after, problem_code = self._once(path, target, digest_b64, size)
            except (OSError, http.client.HTTPException, socket.timeout, ssl.SSLError):
                status = 0
            if status in (200, 201):
                self._first_failure = None
                return
            if status == 0 or status >= 500 or status in _RETRY_STATUS:
                if self._first_failure is None:
                    self._first_failure = self._clock()
                if self._should_switch(target):
                    continue  # no backoff sleep: the window is over, go straight to the fallback
            if status == 403:
                raise WorkerError("UPLOAD_FAILED", f"{name}: upload link refused (403)")
            # 409 UPLOAD_IN_PROGRESS: the server still holds the lock of an earlier, stalled PUT of this same
            # artifact; it frees itself, so wait and retry. Any other 409 (UPLOAD_CONFLICT: a different body
            # was already received) can never succeed.
            if status == 409 and problem_code != IN_PROGRESS:
                raise WorkerError("UPLOAD_CONFLICT", f"{name}: conflicting upload (409)")
            if status == 410:
                raise WorkerError("UPLOAD_CLOSED", f"{name}: upload closed (410)")
            if status == 422:
                n422 += 1
                if n422 >= MAX_422:
                    raise WorkerError("UPLOAD_FAILED", f"{name}: digest mismatch {n422} times")
            elif not (status == 0 or status >= 500 or status in _RETRY_STATUS or status == 409):
                raise WorkerError("UPLOAD_FAILED", f"{name}: upload refused (status {status})")
            delay = BACKOFF[min(attempt, len(BACKOFF) - 1)]
            attempt += 1
            if retry_after is not None:
                delay = retry_after
            if deadline is not None and self._clock() + delay > deadline:
                raise WorkerError("DEADLINE", f"{name}: upload retries would pass the upload deadline")
            self._sleep(delay)

