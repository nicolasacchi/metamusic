"""The mm.worker/1 input contract: allowlist and a hand-written parser (no jsonschema at runtime)."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any

from .errors import WorkerError

CONTRACT = "mm.worker/1"
RESULT = "mm-result.json"

KIB = 1 << 10
MIB = 1 << 20

# (name, max_bytes) in C83.2 step 8 upload order, mm-result.json last. Equal to the schema enum (tested).
_TABLE: tuple[tuple[str, int], ...] = (
    ("request.json", 4 * MIB),
    ("config.json", 4 * MIB),
    ("result.json", 4 * MIB),
    ("score.abc", 1 * MIB),
    ("plan.json", 4 * MIB),
    ("plan_manifest.json", 4 * MIB),
    ("abc_tokens.npy", 64 * MIB),
    ("prefix.npy", 64 * MIB),
    ("semantic.npy", 64 * MIB),
    ("audio.flac", 160 * MIB),
    (RESULT, 64 * KIB),
)
ARTIFACTS: tuple[str, ...] = tuple(n for n, _ in _TABLE)
MAX_BYTES: dict[str, int] = dict(_TABLE)
_ALWAYS_REQUIRED = ("request.json", "config.json", "result.json", "audio.flac")


def REQUIRED(cot: str) -> frozenset[str]:  # noqa: N802 - name fixed by the plan (C83.1)
    """Artifacts a render must produce. score.abc and the plan files stay optional-but-verified until the
    observed set (artifacts.observed.json, M3-X3) lists them on the Go side, so the worker never requires more
    than the unconditional set, whatever the cot."""
    return frozenset(_ALWAYS_REQUIRED)


_ID = re.compile(r"[A-Za-z0-9_-]{1,64}")
_ENGINE = re.compile(r"[a-z0-9_]{1,32}")
_URL = re.compile(r"https?://\S+")
_REV = re.compile(r"[0-9a-f]{40}")


@dataclass(frozen=True)
class Target:
    url: str
    max_bytes: int

    def __repr__(self) -> str:  # never print the capability URL
        return f"Target(max_bytes={self.max_bytes})"


@dataclass(frozen=True)
class TakeInput:
    take_id: str
    attempt_id: str
    engine: str
    request: dict[str, Any]
    timeout_s: int
    upload_budget_s: int
    deadline_unix: int
    start_url: str = field(repr=False)
    uploads: dict[str, Target] = field(repr=False)
    weights: dict[str, str]


def _bad(msg: str) -> WorkerError:
    return WorkerError("BAD_INPUT", msg)


def _int(v: Any, name: str, lo: int, hi: int | None = None) -> int:
    if isinstance(v, bool) or not isinstance(v, int):
        raise _bad(f"{name} must be an integer")
    if v < lo or (hi is not None and v > hi):
        raise _bad(f"{name} out of range")
    return v


def _url(v: Any, name: str) -> str:
    if not isinstance(v, str) or len(v) > 2048 or not _URL.fullmatch(v):
        raise _bad(f"{name} is not a valid URL")
    return v


def _only(d: dict[str, Any], allowed: set[str], required: set[str], name: str) -> None:
    extra = set(d) - allowed
    if extra:
        raise _bad(f"{name}: unknown property {sorted(extra)[0]}")
    missing = required - set(d)
    if missing:
        raise _bad(f"{name}: missing {sorted(missing)[0]}")


def parse_input(job_input: Any) -> TakeInput:
    """Validate a job input exactly like mm-worker-1.input.schema.json. Raises WorkerError BAD_INPUT, or
    CONTRACT_VERSION when only the version differs."""
    if not isinstance(job_input, dict):
        raise _bad("input must be an object")
    if "contract" in job_input and job_input["contract"] != CONTRACT:
        raise WorkerError("CONTRACT_VERSION", "unsupported contract version")
    keys = {
        "contract", "take_id", "attempt_id", "engine", "request", "timeout_s", "upload_budget_s",
        "deadline_unix", "start", "uploads", "expect",
    }
    _only(job_input, keys, keys, "input")
    for k in ("take_id", "attempt_id"):
        v = job_input[k]
        if not isinstance(v, str) or not _ID.fullmatch(v):
            raise _bad(f"{k} is invalid")
    eng = job_input["engine"]
    if not isinstance(eng, str) or not _ENGINE.fullmatch(eng):
        raise _bad("engine is invalid")
    req = job_input["request"]
    if not isinstance(req, dict) or not req:
        raise _bad("request must be a non-empty object")
    timeout_s = _int(job_input["timeout_s"], "timeout_s", 1, 2700)
    budget = _int(job_input["upload_budget_s"], "upload_budget_s", 1, 2700)
    deadline = _int(job_input["deadline_unix"], "deadline_unix", 1)

    start = job_input["start"]
    if not isinstance(start, dict):
        raise _bad("start must be an object")
    _only(start, {"url"}, {"url"}, "start")
    start_url = _url(start["url"], "start.url")

    ups = job_input["uploads"]
    if not isinstance(ups, dict):
        raise _bad("uploads must be an object")
    required = {"audio.flac", "request.json", "config.json", "result.json", RESULT}
    _only(ups, set(ARTIFACTS), required, "uploads")
    targets: dict[str, Target] = {}
    for name, t in ups.items():
        if not isinstance(t, dict):
            raise _bad(f"uploads.{name} must be an object")
        _only(t, {"url", "max_bytes"}, {"url", "max_bytes"}, f"uploads.{name}")
        targets[name] = Target(_url(t["url"], f"uploads.{name}.url"),
                               _int(t["max_bytes"], f"uploads.{name}.max_bytes", 1, MAX_BYTES[name]))

    expect = job_input["expect"]
    if not isinstance(expect, dict):
        raise _bad("expect must be an object")
    _only(expect, {"weights"}, {"weights"}, "expect")
    w = expect["weights"]
    if not isinstance(w, dict) or not w:
        raise _bad("expect.weights must be a non-empty object")
    for repo, rev in w.items():
        if not isinstance(rev, str) or not _REV.fullmatch(rev):
            raise _bad("expect.weights revision is invalid")
    return TakeInput(job_input["take_id"], job_input["attempt_id"], eng, req, timeout_s, budget, deadline,
                     start_url, targets, dict(w))
