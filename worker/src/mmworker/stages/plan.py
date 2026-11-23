"""The plan stage (DESK-P1, C361.3): yue2's planning phase alone, one `plan-NN.tar` bundle per seed.

Seeds run sequentially through `ctx.adapter.plan(...)` (the engine adapter; yue2's `YuE2Pipeline.plan`). Seed i starts only while at
least `plan.s_per_seed` seconds remain before the generation deadline (else `DEADLINE`, and so does every later seed); a running seed is
cancelled at the deadline through `cancelled` (yue2 honours it at token boundaries) and reports `DEADLINE`. A seed that fails reports
`error` with a code; the handler turns "every seed failed" into `PLANS_FAILED`.

A bundle is one uncompressed POSIX tar that the server's ingester (`internal/desk/bundle.go`) verifies member by member:
`score.abc`, `plan.json` (exactly the six keys seed, cot, truncated, abc_tokens, seconds, yue2: yue2's own plan.json, which is a
different and larger document, is replaced), then `abc_tokens.npy`, `prefix.npy` and `plan_manifest.json` while the sizes allow.
`plan_manifest.json` here is the sha256 of every member of the bundle (yue2's own manifest hashes yue2's plan.json, which is not shipped).

The stage registers the `plan` capability only when the installed yue2 exposes the staged API (`YuE2Pipeline.plan`,
`SymbolicPlan.save`, the `abc`/`abc_ids`/`truncated` fields). The check reads the source of yue2/pipeline.py and imports nothing heavy.
"""

from __future__ import annotations

import ast
import hashlib
import importlib.metadata
import importlib.util
import io
import json
import os
import re
import shutil
import tarfile
import tempfile
from collections.abc import Callable, Mapping
from pathlib import Path
from typing import Any

from .. import stages
from ..contract import KIB, MIB, plan_name
from ..errors import WorkerError

# The ingester's limits (internal/desk/bundle.go): 64 KiB for the two required members, 1 MiB for each optional one, 2 MiB per tar.
SCORE_MAX = PLAN_JSON_MAX = 64 * KIB
OPTIONAL_MAX = MIB
TAR_MAX = 2 * MIB
_TAR_HEADROOM = 16 * KIB  # tar headers and block padding of five members
OPTIONAL = ("abc_tokens.npy", "prefix.npy")
MANIFEST = "plan_manifest.json"
_CODE = re.compile(r"[A-Z][A-Z0-9_]*")

# Seams for the tests: the staged-API probe and the version string.
_probe: Callable[[], bool] | None = None


def staged_api_present() -> bool:
    """True when the installed yue2 has `YuE2Pipeline.plan` and a `SymbolicPlan` with `save` and the fields the stage reads.
    Reads yue2/pipeline.py as text: importing it would load torch (the worker imports yue2 only inside load_yue2)."""
    try:
        spec = importlib.util.find_spec("yue2")
        roots = list(spec.submodule_search_locations or ()) if spec else []
        src = next((Path(r) / "pipeline.py" for r in roots if (Path(r) / "pipeline.py").is_file()), None)
        if src is None:
            return False
        tree = ast.parse(src.read_text(encoding="utf-8"))
    except (OSError, ValueError, SyntaxError, ImportError):
        return False
    classes = {n.name: n for n in tree.body if isinstance(n, ast.ClassDef)}

    def methods(c: ast.ClassDef | None) -> set[str]:
        return {n.name for n in c.body if isinstance(n, ast.FunctionDef)} if c else set()

    def fields(c: ast.ClassDef | None) -> set[str]:
        return {n.target.id for n in c.body if isinstance(n, ast.AnnAssign) and isinstance(n.target, ast.Name)} if c else set()

    sym = classes.get("SymbolicPlan")
    return ("plan" in methods(classes.get("YuE2Pipeline")) and "save" in methods(sym)
            and {"abc", "abc_ids", "truncated"} <= fields(sym))


def yue2_version(environ: Mapping[str, str] | None = None) -> str:
    environ = os.environ if environ is None else environ
    for dist in ("yue2-infer", "yue2"):
        try:
            return importlib.metadata.version(dist)[:64] or "unknown"
        except importlib.metadata.PackageNotFoundError:
            continue
    commit = environ.get("MM_YUE2_COMMIT", "")
    return commit[:12] if commit else "unknown"


class SeedError(Exception):
    """A seed that cannot ship; `code` is the plans[].code the app sees (pattern A-Z0-9_)."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


def _code(e: BaseException) -> str:
    if isinstance(e, (WorkerError, SeedError)):
        return e.code
    if isinstance(e, InterruptedError):
        return "DEADLINE"
    return "PLAN_FAILED"


def build_bundle(plan: Any, request: Mapping[str, Any], seed: int, seconds: float, version: str, scratch: Path,
                 cot: str | None = None) -> bytes:
    """The tar bytes for one planned seed. Raises SeedError (NO_SCORE, SCORE_TOO_LARGE) for a plan that cannot ship."""
    abc = getattr(plan, "abc", None)
    if not isinstance(abc, str) or not abc.strip():
        raise SeedError("NO_SCORE", "the plan carries no ABC score")
    score = abc.encode("utf-8")
    if len(score) > SCORE_MAX or b"\x00" in score:
        raise SeedError("SCORE_TOO_LARGE", "the plan's score is over its size limit")
    ids = getattr(plan, "abc_ids", None)
    cot = cot or request.get("cot")
    if cot not in ("full", "melody"):  # the ingester accepts only these two
        raise SeedError("BAD_INPUT", "the plan request has no usable cot")
    doc = {"seed": seed, "cot": cot, "truncated": bool(getattr(plan, "truncated", False)),
           "abc_tokens": len(ids) if isinstance(ids, list) else None, "seconds": round(max(seconds, 0.0), 3), "yue2": version}
    plan_json = json.dumps(doc, sort_keys=True).encode()
    members: list[tuple[str, bytes]] = [("score.abc", score), ("plan.json", plan_json)]

    saved = scratch / "saved"
    saved.mkdir(parents=True, exist_ok=True)
    plan.save(saved)  # yue2's own file set; only the token arrays are taken from it
    budget = TAR_MAX - _TAR_HEADROOM - len(score) - len(plan_json) - 4 * KIB  # 4 KiB: the manifest
    for name in OPTIONAL:
        p = saved / name
        if p.is_file() and not p.is_symlink():
            data = p.read_bytes()
            if 0 < len(data) <= min(OPTIONAL_MAX, budget):
                members.append((name, data))
                budget -= len(data)
    if len(members) > 2:  # a manifest only says something about the optional files it sits beside
        members.append((MANIFEST, json.dumps({n: hashlib.sha256(d).hexdigest() for n, d in members}, sort_keys=True).encode()))

    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for name, data in members:
            ti = tarfile.TarInfo(name)
            ti.size, ti.mode, ti.mtime, ti.uid, ti.gid, ti.uname, ti.gname = len(data), 0o644, 0, 0, 0, "", ""
            tar.addfile(ti, io.BytesIO(data))
    return buf.getvalue()


def run(ctx: stages.StageContext) -> list[stages.PlanOutcome]:
    """Plan every seed of ctx.take.plan; one outcome per seed, in order. Never raises for a seed's own fault."""
    take = ctx.take
    spec = take.plan
    plan_fn = getattr(ctx.adapter, "plan", None)
    if spec is None or plan_fn is None:
        raise WorkerError("PIPELINE_ERROR", "the plan stage needs a plan request and a planning engine")
    deadline = getattr(ctx.cancelled, "deadline", None)
    version = yue2_version()
    out: list[stages.PlanOutcome] = []
    stop = False
    for i, seed in enumerate(spec.seeds):
        if not stop and (ctx.cancelled() or (deadline is not None and deadline - ctx.clock() < spec.s_per_seed)):
            stop = True
        if stop:
            out.append(stages.PlanOutcome(i, seed, "error", "DEADLINE"))
            continue
        out.append(_one(ctx, plan_fn, i, seed, version))
    return out


def _one(ctx: stages.StageContext, plan_fn: Callable[..., Any], i: int, seed: int, version: str) -> stages.PlanOutcome:
    request = {**ctx.take.request, "seed": seed}
    t0 = ctx.clock()
    scratch = Path(tempfile.mkdtemp(prefix="plan-", dir=ctx.tmp))
    try:
        try:
            kwargs = dict(ctx.adapter.validate(request))
        except ValueError as e:  # only a rejected request is the caller's fault; a ValueError from yue2 itself is not
            raise SeedError("BAD_INPUT", str(e)) from e
        kwargs.pop("semantic_sampling", None)  # a plan has no semantic phase; YuE2Pipeline.plan would pass it into SongRequest
        plan = plan_fn(cancelled=ctx.cancelled, **kwargs)
        if ctx.cancelled() and getattr(plan, "truncated", False):
            return stages.PlanOutcome(i, seed, "error", "DEADLINE")  # cut short by the deadline (or a closed attempt)
        data = build_bundle(plan, request, seed, ctx.clock() - t0, version, scratch, cot=kwargs.get("cot"))
        (ctx.tmp / plan_name(i)).write_bytes(data)
        return stages.PlanOutcome(i, seed, "ok", truncated={"abc": bool(plan.truncated), "semantic": False})
    except Exception as e:  # noqa: BLE001 - a seed's fault is that seed's error, never the job's
        if ctx.cancelled() and isinstance(e, InterruptedError):
            return stages.PlanOutcome(i, seed, "error", "DEADLINE")
        code = _code(e)
        return stages.PlanOutcome(i, seed, "error", code if _CODE.fullmatch(code) else "PLAN_FAILED")
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


def register_stage() -> bool:
    """Register `plan` when the staged API is present (G1 C322.2). Returns whether it did."""
    if not (_probe or staged_api_present)():
        return False
    stages.register(stages.PLAN, run)
    return True


register_stage()
