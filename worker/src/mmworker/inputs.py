"""Verified fetch of signed input URLs (C154.3, D-E2-9): the source audio and the reference clip of an ACE-Step job.

The URLs are capabilities (a signature rides in the query string): they are never logged, never put in an
exception message and never kept; every message here names the input and the host-free reason only. Bytes are
streamed to `<tmp>/<name>.part`, counted against the declared size, hashed on the way, and renamed to
`<tmp>/<name>` only after the length and the sha256 match, so nothing unverified ever sits under the final name.
"""

from __future__ import annotations

import hashlib
import http.client
import re
import ssl
import time
from collections.abc import Callable, Mapping
from pathlib import Path
from urllib.parse import urljoin, urlsplit

from . import __version__, redact
from .contract import InputRef
from .errors import WorkerError

TOTAL_BUDGET_S = 300.0  # all attempts of one input (retries included)
BACKOFF_S = (1.0, 2.0, 4.0, 8.0, 16.0, 32.0, 64.0)
SOCKET_TIMEOUT_S = 30.0
MAX_REDIRECTS = 3
CHUNK = 1 << 20
_RETRY_STATUS = frozenset({408, 425, 429, 500, 502, 503, 504})
_REDIRECT_STATUS = frozenset({301, 302, 303, 307, 308})
_NAME = re.compile(r"[a-z][a-z0-9_]{0,31}")


class _Retry(Exception):
    """A transient failure of one attempt (reason is redacted, URL-free text)."""


def _origin(url: str) -> tuple[str, str, int]:
    u = urlsplit(url)
    return (u.scheme, (u.hostname or "").lower(), u.port or (443 if u.scheme == "https" else 80))


def _connect(url: str, timeout: float) -> http.client.HTTPConnection:
    u = urlsplit(url)
    if u.scheme == "https":
        return http.client.HTTPSConnection(u.netloc, timeout=timeout, context=ssl.create_default_context())
    return http.client.HTTPConnection(u.netloc, timeout=timeout)


def _attempt(name: str, ref: InputRef, part: Path, deadline: float, clock: Callable[[], float]) -> None:
    """One GET (following same-origin redirects only) into `part`. Raises _Retry for transient failures,
    WorkerError for terminal ones."""
    url = ref.url
    for _hop in range(MAX_REDIRECTS + 1):
        left = deadline - clock()
        if left <= 0:
            raise WorkerError("INPUT_FAILED", f"{name}: the deadline passed while fetching")
        conn = _connect(url, min(SOCKET_TIMEOUT_S, left))
        try:
            u = urlsplit(url)
            target = (u.path or "/") + (f"?{u.query}" if u.query else "")
            try:
                conn.request("GET", target, headers={"User-Agent": f"metamusic-worker/{__version__}",
                                                     "Accept-Encoding": "identity"})
                resp = conn.getresponse()
            except (OSError, http.client.HTTPException) as e:  # includes timeouts and TLS errors
                raise _Retry(f"{type(e).__name__}") from None
            if resp.status in _REDIRECT_STATUS:
                loc = resp.getheader("Location")
                resp.read(0)
                if not loc:
                    raise WorkerError("INPUT_FAILED", f"{name}: redirect without a location")
                nxt = urljoin(url, loc)
                if _origin(nxt) != _origin(url):
                    raise WorkerError("INPUT_FAILED", f"{name}: refused a redirect to another host")
                url = nxt
                continue
            if resp.status != 200:
                msg = f"{name}: HTTP {resp.status}"
                if resp.status in _RETRY_STATUS:
                    raise _Retry(msg)
                raise WorkerError("INPUT_FAILED", msg)
            _stream(name, ref, resp, part, deadline, clock)
            return
        finally:
            conn.close()
    raise WorkerError("INPUT_FAILED", f"{name}: too many redirects")


def _stream(name: str, ref: InputRef, resp: http.client.HTTPResponse, part: Path, deadline: float,
            clock: Callable[[], float]) -> None:
    h = hashlib.sha256()
    n = 0
    declared = resp.getheader("Content-Length")
    if declared is not None and declared.isdigit() and int(declared) > ref.bytes:
        raise WorkerError("INPUT_MISMATCH", f"{name}: the server announces more than the {ref.bytes} bytes expected")
    try:
        with open(part, "wb") as f:
            while True:
                if clock() >= deadline:
                    raise WorkerError("INPUT_FAILED", f"{name}: the deadline passed while fetching")
                chunk = resp.read(min(CHUNK, ref.bytes + 1 - n))
                if not chunk:
                    break
                n += len(chunk)
                if n > ref.bytes:  # cut: never keep reading a server that sends more than declared
                    raise WorkerError("INPUT_MISMATCH", f"{name}: more than the {ref.bytes} bytes expected")
                h.update(chunk)
                f.write(chunk)
    except (OSError, http.client.HTTPException) as e:
        raise _Retry(f"{type(e).__name__}") from None
    if declared is not None and declared.isdigit() and n < int(declared):
        raise _Retry("IncompleteRead")  # the connection dropped mid-body: transient, not a wrong file
    if n != ref.bytes:
        raise WorkerError("INPUT_MISMATCH", f"{name}: received {n} bytes, expected {ref.bytes}")
    if h.hexdigest() != ref.sha256:
        raise WorkerError("INPUT_MISMATCH", f"{name}: sha256 mismatch")


def fetch(inputs: Mapping[str, InputRef], deadline: float, tmp: Path, *,
          clock: Callable[[], float] = time.time,
          sleep: Callable[[float], None] = time.sleep) -> dict[str, Path]:
    """Download and verify every input into `tmp`; returns {name: path}. `deadline` is a clock() time (the
    generation deadline): no attempt starts, and no backoff sleeps, past it. Errors are WorkerError
    INPUT_FAILED (network, status, redirect to another host, deadline) or INPUT_MISMATCH (length, sha256)."""
    out: dict[str, Path] = {}
    for name, ref in inputs.items():
        if not _NAME.fullmatch(name):
            raise WorkerError("BAD_INPUT", "input name is invalid")
        out[name] = _fetch_one(name, ref, deadline, tmp, clock, sleep)
    return out


def _fetch_one(name: str, ref: InputRef, deadline: float, tmp: Path, clock: Callable[[], float],
               sleep: Callable[[float], None]) -> Path:
    start = clock()
    limit = min(deadline, start + TOTAL_BUDGET_S)
    part = tmp / f"{name}.part"
    final = tmp / name
    last = "no attempt"
    for i in range(len(BACKOFF_S) + 1):
        try:
            _attempt(name, ref, part, limit, clock)
            part.replace(final)
            return final
        except _Retry as e:
            last = redact.message(str(e))
        except WorkerError:
            part.unlink(missing_ok=True)
            raise
        part.unlink(missing_ok=True)
        if i == len(BACKOFF_S):
            break
        wait = BACKOFF_S[i]
        if clock() + wait >= limit:
            break
        sleep(wait)
    raise WorkerError("INPUT_FAILED", f"{name}: fetch failed after retries ({last})")
