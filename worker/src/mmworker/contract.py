"""The mm.worker/1 input contract: allowlist and a hand-written parser (no jsonschema at runtime)."""

from __future__ import annotations

import base64
import re
from dataclasses import dataclass, field
from typing import Any

from .errors import WorkerError

CONTRACT_V1 = "mm.worker/1"
CONTRACT_V2 = "mm.worker/2"
CONTRACTS = (CONTRACT_V1, CONTRACT_V2)
CONTRACT = CONTRACT_V1  # the /1 name callers already use
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
class EarsSpec:
    lang: str
    budget_s: int
    stages: tuple[str, ...]
    keep_vocals: bool


@dataclass(frozen=True)
class PlanSpec:
    seeds: tuple[int, ...]
    s_per_seed: int


@dataclass(frozen=True)
class AttachFile:
    sha256: str
    data: bytes = field(repr=False)


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
    inputs: dict[str, InputRef] = field(default_factory=dict, repr=False)  # acestep15, or src_audio of a /2 analyze
    # mm.worker/2 only (C321.3); the /1 parser leaves the defaults.
    contract: str = CONTRACT_V1
    kind: str = "render"
    ears: EarsSpec | None = None
    plan: PlanSpec | None = None
    attach: dict[str, AttachFile] = field(default_factory=dict, repr=False)
    fallback_after_s: int = 0  # reserved: ignored until the worker advertises capability fallback


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


# ---------------------------------------------------------------------------------------------------------------------
# mm.worker/2 (C321). Same allowlist idea as /1, per kind; mirrored by artifacts-v2.json (exported by the Go table) and
# by mm-worker-2.{input,output}.schema.json (all three are tested against each other).

KIND_RENDER, KIND_ANALYZE, KIND_PLAN = "render", "analyze", "plan"
KINDS = (KIND_RENDER, KIND_ANALYZE, KIND_PLAN)
EARS_STAGES = ("separate", "asr", "transcribe")
MAX_PLAN_SEEDS = 16
MAX_ATTACH_FILES = 16
MAX_ATTACH_BYTES = 1 * MIB
ANALYSIS = "analysis.json"
STEM_VOCALS = "stem-vocals.flac"
_PLAN_BUNDLE_MAX = 2 * MIB  # Revision 2, desk A1
_RENDER_OPTIONAL = ("score.abc", "plan.json", "plan_manifest.json", "abc_tokens.npy", "prefix.npy", "semantic.npy")


def plan_name(i: int) -> str:
    return f"plan-{i:02d}.tar"


MAX_BYTES_V2: dict[str, int] = {
    **MAX_BYTES, ANALYSIS: 4 * MIB, STEM_VOCALS: 200 * MIB, **{plan_name(i): _PLAN_BUNDLE_MAX for i in range(16)},
}
_ATTACH_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
_B64 = re.compile(r"(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?")
_LANG = re.compile(r"auto|[a-z]{2,3}")


def artifacts_v2(kind: str, ears: bool = False, keep_vocals: bool = False,
                 seeds: int = 0) -> tuple[tuple[str, int, bool], ...]:
    """(name, max_bytes, required) in upload order, mm-result.json last, for one /2 shape (C321.5). `required` is the
    document's: every name here must have an upload target. What the worker must *produce* is narrower: analysis.json
    only once ears.status is ok, stem-vocals.flac never (the handler decides)."""
    if kind == KIND_PLAN:
        names = [(plan_name(i), True) for i in range(seeds)]
    elif kind == KIND_ANALYZE:
        names = [(ANALYSIS, True)] + ([(STEM_VOCALS, True)] if keep_vocals else [])
    else:
        names = [(n, n in _ALWAYS_REQUIRED) for n, _ in _TABLE if n in _ALWAYS_REQUIRED or n in _RENDER_OPTIONAL]
        names = [x for x in names if x[0] != "audio.flac"] + [("audio.flac", True)]
        if ears:
            names += ([(STEM_VOCALS, True)] if keep_vocals else []) + [(ANALYSIS, True)]
    return tuple((n, MAX_BYTES_V2[n], req) for n, req in names) + ((RESULT, MAX_BYTES_V2[RESULT], True),)


def upload_rules_v2(kind: str, ears: bool, keep_vocals: bool, seeds: int) -> tuple[frozenset[str], frozenset[str]]:
    """(required, allowed) upload names of a /2 document. Required names must be present in `uploads`."""
    table = artifacts_v2(kind, ears, keep_vocals, seeds)
    allowed = frozenset(n for n, _, _ in table)
    required = {n for n, _, r in table if r}
    return frozenset(required), allowed


def parse_job(job_input: Any) -> TakeInput:
    """Dispatch on `contract`: /1 (unchanged parse_input), /2 (parse_v2). Any other version is CONTRACT_VERSION."""
    if isinstance(job_input, dict) and job_input.get("contract") == CONTRACT_V2:
        return parse_v2(job_input)
    return parse_input(job_input)


def _bool(v: Any, name: str) -> bool:
    if not isinstance(v, bool):
        raise _bad(f"{name} must be a boolean")
    return v


def _obj(v: Any, name: str) -> dict[str, Any]:
    if not isinstance(v, dict):
        raise _bad(f"{name} must be an object")
    return v


def _parse_ears(raw: Any) -> EarsSpec:
    e = _obj(raw, "ears")
    _only(e, {"lang", "budget_s", "stages", "keep_vocals"}, {"budget_s", "stages"}, "ears")
    lang = e.get("lang", "auto")
    if not isinstance(lang, str) or not _LANG.fullmatch(lang):
        raise _bad("ears.lang is invalid")
    stages = e["stages"]
    if (not isinstance(stages, list) or not 1 <= len(stages) <= 3 or len(set(map(str, stages))) != len(stages)
            or any(not isinstance(x, str) or x not in EARS_STAGES for x in stages)):
        raise _bad("ears.stages must be 1 to 3 distinct names of separate, asr, transcribe")
    keep = _bool(e["keep_vocals"], "ears.keep_vocals") if "keep_vocals" in e else False
    return EarsSpec(lang, _int(e["budget_s"], "ears.budget_s", 1, 2700), tuple(stages), keep)


def _parse_plan(raw: Any) -> PlanSpec:
    p = _obj(raw, "plan")
    _only(p, {"seeds", "s_per_seed"}, {"seeds", "s_per_seed"}, "plan")
    seeds = p["seeds"]
    if not isinstance(seeds, list) or not 1 <= len(seeds) <= MAX_PLAN_SEEDS:
        raise _bad("plan.seeds must hold 1 to 16 integers")
    if any(isinstance(x, bool) or not isinstance(x, int) for x in seeds) or len(set(seeds)) != len(seeds):
        raise _bad("plan.seeds must be distinct integers")
    return PlanSpec(tuple(seeds), _int(p["s_per_seed"], "plan.s_per_seed", 1, 360))


def _parse_attach(raw: Any) -> dict[str, AttachFile]:
    a = _obj(raw, "attach")
    if len(a) > MAX_ATTACH_FILES:
        raise _bad("attach holds more than 16 files")
    out: dict[str, AttachFile] = {}
    total = 0
    for name, f in a.items():
        if not _ATTACH_NAME.fullmatch(name):
            raise _bad("attach: invalid file name")
        f = _obj(f, f"attach.{name}")
        _only(f, {"sha256", "data_base64"}, {"sha256", "data_base64"}, f"attach.{name}")
        sha, b64 = f["sha256"], f["data_base64"]
        if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
            raise _bad(f"attach.{name}.sha256 is invalid")
        if not isinstance(b64, str) or not _B64.fullmatch(b64):
            raise _bad(f"attach.{name}.data_base64 is invalid")
        data = base64.b64decode(b64, validate=True)
        total += len(data)
        if total > MAX_ATTACH_BYTES:
            raise _bad("attach exceeds 1 MiB decoded")
        out[name] = AttachFile(sha, data)  # the digest is checked when the file is written (ATTACH_MISMATCH)
    return out


def _parse_inputs_v2(raw: Any) -> dict[str, InputRef]:
    i = _obj(raw, "inputs")
    _only(i, {"src_audio"}, {"src_audio"}, "inputs")
    r = _obj(i["src_audio"], "inputs.src_audio")
    _only(r, {"url", "sha256", "bytes"}, {"url", "sha256", "bytes"}, "inputs.src_audio")
    sha = r["sha256"]
    if not isinstance(sha, str) or not _SHA256.fullmatch(sha):
        raise _bad("inputs.src_audio.sha256 is invalid")
    return {"src_audio": InputRef(_url(r["url"], "inputs.src_audio.url"), sha,
                                  _int(r["bytes"], "inputs.src_audio.bytes", 1, MAX_INPUT_BYTES))}


def parse_v2(job_input: Any) -> TakeInput:
    """Validate a job input like mm-worker-2.input.schema.json (engine yue2 only; acestep15 stays on /1).
    Raises WorkerError BAD_INPUT (CONTRACT_VERSION when the version is not /2)."""
    if not isinstance(job_input, dict):
        raise _bad("input must be an object")
    if job_input.get("contract") != CONTRACT_V2:
        raise WorkerError("CONTRACT_VERSION", "unsupported contract version")
    allowed = {"contract", "kind", "take_id", "attempt_id", "engine", "request", "ears", "plan", "attach", "inputs",
               "timeout_s", "upload_budget_s", "deadline_unix", "min_audio_s", "fallback_after_s", "start",
               "uploads", "expect"}
    required = {"contract", "take_id", "attempt_id", "engine", "timeout_s", "upload_budget_s", "deadline_unix",
                "start", "uploads", "expect"}
    _only(job_input, allowed, required, "input")
    kind = job_input.get("kind", KIND_RENDER)
    if not isinstance(kind, str) or kind not in KINDS:
        raise _bad("kind is invalid")
    for k in ("take_id", "attempt_id"):
        v = job_input[k]
        if not isinstance(v, str) or not _ID.fullmatch(v):
            raise _bad(f"{k} is invalid")
    if job_input["engine"] != "yue2":
        raise _bad("engine must be yue2 on mm.worker/2")
    # per-kind key rules (the schema's allOf)
    need = {KIND_RENDER: {"request"}, KIND_ANALYZE: {"ears", "inputs"}, KIND_PLAN: {"request", "plan"}}[kind]
    banned = {KIND_RENDER: {"plan", "inputs"}, KIND_ANALYZE: {"request", "plan", "min_audio_s"},
              KIND_PLAN: {"ears", "inputs", "attach", "min_audio_s"}}[kind]
    for k in sorted(need - set(job_input)):
        raise _bad(f"{kind}: missing {k}")
    for k in sorted(banned & set(job_input)):
        raise _bad(f"{kind}: {k} is not allowed")
    req: dict[str, Any] = {}
    if "request" in job_input:
        req = job_input["request"]
        if not isinstance(req, dict) or not req:
            raise _bad("request must be a non-empty object")
    ears = _parse_ears(job_input["ears"]) if "ears" in job_input else None
    plan = _parse_plan(job_input["plan"]) if "plan" in job_input else None
    attach = _parse_attach(job_input["attach"]) if "attach" in job_input else {}
    inputs = _parse_inputs_v2(job_input["inputs"]) if "inputs" in job_input else {}
    timeout_s = _int(job_input["timeout_s"], "timeout_s", 1, 2700)
    budget = _int(job_input["upload_budget_s"], "upload_budget_s", 1, 2700)
    deadline = _int(job_input["deadline_unix"], "deadline_unix", 1)
    min_audio_s = _int(job_input["min_audio_s"], "min_audio_s", 0, MAX_MIN_AUDIO_S) if "min_audio_s" in job_input else 0
    fb_after = _int(job_input["fallback_after_s"], "fallback_after_s", 60, 600) if "fallback_after_s" in job_input else 0

    start = _obj(job_input["start"], "start")
    _only(start, {"url"}, {"url"}, "start")
    start_url = _url(start["url"], "start.url")

    ups = _obj(job_input["uploads"], "uploads")
    req_names, allowed_names = upload_rules_v2(kind, ears is not None, bool(ears and ears.keep_vocals),
                                               len(plan.seeds) if plan else 0)
    _only(ups, set(allowed_names), set(req_names), "uploads")
    targets: dict[str, Target] = {}
    any_fallback = False
    for name, t in ups.items():
        t = _obj(t, f"uploads.{name}")
        _only(t, {"url", "max_bytes", "fallback"}, {"url", "max_bytes"}, f"uploads.{name}")
        if "fallback" in t:  # reserved (C321.3): validated, then ignored
            f = _obj(t["fallback"], f"uploads.{name}.fallback")
            _only(f, {"url"}, {"url"}, f"uploads.{name}.fallback")
            _url(f["url"], f"uploads.{name}.fallback.url")
            any_fallback = True
        targets[name] = Target(_url(t["url"], f"uploads.{name}.url"),
                               _int(t["max_bytes"], f"uploads.{name}.max_bytes", 1, MAX_BYTES_V2[name]))
    if any_fallback != bool(fb_after):
        raise _bad("fallback_after_s and uploads.*.fallback must appear together")

    expect = _obj(job_input["expect"], "expect")
    _only(expect, {"weights"}, {"weights"}, "expect")
    w = expect["weights"]
    if not isinstance(w, dict) or not w:
        raise _bad("expect.weights must be a non-empty object")
    for rev in w.values():
        if not isinstance(rev, str) or not _REV.fullmatch(rev):
            raise _bad("expect.weights revision is invalid")
    return TakeInput(job_input["take_id"], job_input["attempt_id"], "yue2", req, timeout_s, budget, deadline,
                     start_url, targets, dict(w), min_audio_s, inputs, CONTRACT_V2, kind, ears, plan, attach, fb_after)
