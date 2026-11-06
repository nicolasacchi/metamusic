"""The mm.worker/1 input contract: allowlist and a hand-written parser (no jsonschema at runtime)."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any

from .errors import WorkerError

CONTRACT = "mm.worker/1"
RESULT = "mm-result.json"

# yue2's semantic max_tokens is 9000 = 360 s at 25 semantic tokens per second of audio; min_tokens may not exceed it.
MAX_MIN_AUDIO_S = 360

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


# acestep15 (C153.2): the engine-specific allowlist; audio.flac is 192 MiB, no config.json, lyrics.lrc optional.
ACESTEP = "acestep15"
_TABLE_ACESTEP: tuple[tuple[str, int], ...] = (
    ("request.json", 4 * MIB),
    ("result.json", 4 * MIB),
    ("lyrics.lrc", 1 * MIB),
    ("audio.flac", 192 * MIB),
    (RESULT, 64 * KIB),
)
_ARTIFACT_TABLES = {ACESTEP: _TABLE_ACESTEP}
_REQUIRED_ACESTEP = frozenset({"audio.flac", "request.json", "result.json"})
INPUT_NAMES = ("src_audio", "reference_audio")
MAX_INPUT_BYTES = 192 * MIB
MAX_WEIGHTS = {ACESTEP: 2}


def artifacts_for(engine: str) -> tuple[tuple[str, int], ...]:
    """(name, max_bytes) in upload order, mm-result.json last, for the engine's allowlist (yue2 = ARTIFACTS)."""
    return _ARTIFACT_TABLES.get(engine, _TABLE)


def required_for(engine: str, cot: str = "") -> frozenset[str]:
    """Artifacts a render must produce for the engine."""
    return _REQUIRED_ACESTEP if engine == ACESTEP else REQUIRED(cot)


_ID = re.compile(r"[A-Za-z0-9_-]{1,64}")
_ENGINE = re.compile(r"[a-z0-9_]{1,32}")
_URL = re.compile(r"https?://\S+")
_REV = re.compile(r"[0-9a-f]{40}")
_SHA256 = re.compile(r"[0-9a-f]{64}")


@dataclass(frozen=True)
class Target:
    url: str
    max_bytes: int

    def __repr__(self) -> str:  # never print the capability URL
        return f"Target(max_bytes={self.max_bytes})"


@dataclass(frozen=True)
class InputRef:
    """A signed URL to fetch, with the digest and exact size it must have (inputs.fetch verifies both)."""

    url: str
    sha256: str
    bytes: int

    def __repr__(self) -> str:  # never print the capability URL
        return f"InputRef(sha256={self.sha256[:12]}, bytes={self.bytes})"


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
    min_audio_s: int = 0  # forced minimum render length in seconds (X4); 0 = none (yue2 only)
    inputs: dict[str, InputRef] = field(default_factory=dict, repr=False)  # acestep15 only


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
    """Validate a job input like mm-worker-1.input.schema.json (engine yue2 or absent) or, for engine acestep15,
    mm-worker-1.acestep15.input.schema.json minus the request body, which the adapter validates (C153.2). Raises WorkerError BAD_INPUT, or
    CONTRACT_VERSION when only the version differs."""
    if not isinstance(job_input, dict):
        raise _bad("input must be an object")
    if "contract" in job_input and job_input["contract"] != CONTRACT:
        raise WorkerError("CONTRACT_VERSION", "unsupported contract version")
    ace = job_input.get("engine") == ACESTEP
    keys = {
        "contract", "take_id", "attempt_id", "engine", "request", "timeout_s", "upload_budget_s",
        "deadline_unix", "start", "uploads", "expect",
    }
    _only(job_input, keys | ({"inputs"} if ace else {"min_audio_s"}), keys, "input")
    table = artifacts_for(ACESTEP if ace else "")
    max_bytes = dict(table)
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
    min_audio_s = _int(job_input["min_audio_s"], "min_audio_s", 0, MAX_MIN_AUDIO_S) if "min_audio_s" in job_input else 0

    start = job_input["start"]
    if not isinstance(start, dict):
        raise _bad("start must be an object")
    _only(start, {"url"}, {"url"}, "start")
    start_url = _url(start["url"], "start.url")

    ups = job_input["uploads"]
    if not isinstance(ups, dict):
        raise _bad("uploads must be an object")
    required = ({"audio.flac", "request.json", "result.json", RESULT} if ace
                else {"audio.flac", "request.json", "config.json", "result.json", RESULT})
    _only(ups, set(max_bytes), required, "uploads")
    targets: dict[str, Target] = {}
    for name, t in ups.items():
        if not isinstance(t, dict):
            raise _bad(f"uploads.{name} must be an object")
        _only(t, {"url", "max_bytes"}, {"url", "max_bytes"}, f"uploads.{name}")
        targets[name] = Target(_url(t["url"], f"uploads.{name}.url"),
                               _int(t["max_bytes"], f"uploads.{name}.max_bytes", 1, max_bytes[name]))

    expect = job_input["expect"]
    if not isinstance(expect, dict):
        raise _bad("expect must be an object")
    _only(expect, {"weights"}, {"weights"}, "expect")
    w = expect["weights"]
    if not isinstance(w, dict) or not w or (ace and len(w) > MAX_WEIGHTS[ACESTEP]):
        raise _bad("expect.weights must be a non-empty object" + (" of at most two repos" if ace else ""))
    for repo, rev in w.items():
        if not isinstance(rev, str) or not _REV.fullmatch(rev):
            raise _bad("expect.weights revision is invalid")
    inputs = _parse_inputs(job_input.get("inputs"), req) if ace else {}
    return TakeInput(job_input["take_id"], job_input["attempt_id"], eng, req, timeout_s, budget, deadline,
                     start_url, targets, dict(w), min_audio_s, inputs)


def _parse_inputs(raw: Any, request: dict[str, Any]) -> dict[str, InputRef]:
    """acestep15 `inputs`: keys match the request (`src` <=> src_audio, `ref` <=> reference_audio)."""
    if raw is None:
        raw = {}
    if not isinstance(raw, dict):
        raise _bad("inputs must be an object")
    _only(raw, set(INPUT_NAMES), set(), "inputs")
    out: dict[str, InputRef] = {}
    for name, r in raw.items():
        if not isinstance(r, dict):
            raise _bad(f"inputs.{name} must be an object")
        _only(r, {"url", "sha256", "bytes"}, {"url", "sha256", "bytes"}, f"inputs.{name}")
        sha = r["sha256"]
        if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
            raise _bad(f"inputs.{name}.sha256 is invalid")
        out[name] = InputRef(_url(r["url"], f"inputs.{name}.url"), sha,
                             _int(r["bytes"], f"inputs.{name}.bytes", 1, MAX_INPUT_BYTES))
    for key, name in (("src", "src_audio"), ("ref", "reference_audio")):
        if (key in request) != (name in out):
            raise _bad(f"inputs.{name} must be present exactly when request.{key} is")
    return out
