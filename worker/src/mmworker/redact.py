"""Strip secrets from any text that leaves the worker (logs, errors, outputs)."""

from __future__ import annotations

import re

_URL_QUERY = re.compile(r"(https?://[^\s?#\"'<>]*)\?[^\s\"'<>]*")
_BEARER = re.compile(r"(?i)\b(bearer|token)\s+[A-Za-z0-9._~+/=-]{8,}")
_PARAM = re.compile(r"(?i)\b(sig|exp|signature|api_key|apikey|key)=[^\s&\"'<>]+")

MAX_MESSAGE = 500


def text(s: object) -> str:
    """Return s with URL query strings replaced by ?<redacted> and bearer-looking tokens removed."""
    out = str(s)
    out = _URL_QUERY.sub(r"\1?<redacted>", out)
    out = _BEARER.sub(lambda m: m.group(1) + " <redacted>", out)
    out = _PARAM.sub("<redacted>", out)
    return out


def message(s: object) -> str:
    """Redacted and clipped to the 500 characters the output schema allows."""
    return text(s)[:MAX_MESSAGE]
