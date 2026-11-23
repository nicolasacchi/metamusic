"""process(): one take, in the exact order of C83.2."""

from __future__ import annotations

import hashlib
import json
import os
import re
import tempfile
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import caps as caps_mod
from . import redact, stages
from . import inputs as inputs_mod
from .contract import (ANALYSIS, CONTRACT_V2, KIND_ANALYZE, KIND_PLAN, KIND_RENDER, RESULT, TakeInput,
                       artifacts_for, artifacts_v2, plan_name, required_for)
from .engines import Adapter, adapt
from .env import flac_seconds
from .errors import WorkerError
from .pipeline import SEMANTIC_TOKENS_PER_S, Pipeline, with_min_audio
from .start import CancelWatch, StartClient, _spawn_thread
from .upload import Uploader, sha256_file

# Errors after which no mm-result.json upload is attempted (the app said stop, or cannot be reached).
_NO_RESULT_UPLOAD = frozenset({"ATTEMPT_CLOSED", "START_FAILED", "UPLOAD_CLOSED"})
_FINISH_MARGIN_S = 10.0
_UPLOAD_LEAD_S = 60.0


def deadlines(take: TakeInput, handler_start: float, environ: Mapping[str, str]) -> tuple[float, float | None]:
    """(upload_deadline, generation_deadline). D-M3-13: uploads stop 10 s before RunPod's kill; generation
    stops upload_budget_s earlier. MM_WORKER_DEADLINE=provider disables the generation deadline. A /2 take with
    `ears` stops generation ears.budget_s earlier still (C321.6)."""
    upload = min(handler_start + take.timeout_s - _FINISH_MARGIN_S, take.deadline_unix - _UPLOAD_LEAD_S)
    if environ.get("MM_WORKER_DEADLINE") == "provider":
        return upload, None
    gen = ears_deadline(take, upload)
    if take.ears is not None:
        gen -= take.ears.budget_s
    return upload, gen


def ears_deadline(take: TakeInput, upload_deadline: float) -> float:
    """C321.6: the Tier B stage ends upload_budget_s before the upload deadline."""
    return upload_deadline - take.upload_budget_s


def _valid_truncated(t: Any) -> bool:
    return (isinstance(t, dict) and set(t) == {"abc", "semantic"}
            and all(type(v) is bool for v in t.values()))


def process(take: TakeInput, pipe: Pipeline | Adapter, uploader: Uploader, clock: Callable[[], float], *,
            start: StartClient, job_id: str, environ: Mapping[str, str] | None = None,
            spawn: Callable[[Callable[[], None]], None] = _spawn_thread,
            sleep: Callable[[float], None] = time.sleep) -> dict[str, Any]:
    """Returns the C73.5 output on success, else {"error": "<CODE>: <message>", "mm": <output>}."""
    env = os.environ if environ is None else environ
    adapter = adapt(pipe)  # a bare yue2 Pipeline (FakePipeline, the real one) is wrapped; an Adapter is used as is
    t0 = clock()
    upload_deadline, gen_deadline = deadlines(take, t0, env)
    timings: dict[str, float | None] = {"load_s": None, "generate_s": None, "save_s": None,
                                        "upload_s": None, "handler_s": None, "e2e_seconds": None}
    uploaded: dict[str, dict[str, Any]] = {}
    state: dict[str, Any] = {"truncated": None, "audio_seconds": None, "ears": None, "plans": None}

    def output(error: WorkerError | None) -> dict[str, Any]:
        timings["handler_s"] = round(clock() - t0, 3)
        gen, sav = timings["generate_s"], timings["save_s"]
        timings["e2e_seconds"] = None if gen is None else round(gen + (sav or 0.0), 3)
        ok = error is None
        doc = {
            "contract": take.contract, "take_id": take.take_id, "attempt_id": take.attempt_id,
            "status": "ok" if ok else "error",
            "error": None if ok else {"code": error.code, "message": redact.message(error.message)},
            "artifacts": dict(uploaded),
            "audio_seconds": state["audio_seconds"],
            "truncated": state["truncated"],
            "timings": dict(timings),
            "env": adapter.env_fingerprint(env),
        }
        if take.contract == CONTRACT_V2:  # C321.4: the /1 object plus kind, ears, plans, caps
            doc["kind"] = take.kind
            doc["caps"] = caps_mod.advertised()
            if state["ears"] is not None:
                doc["ears"] = state["ears"]
            if state["plans"] is not None:
                doc["plans"] = state["plans"]
        return doc

    def put_result(tmp: Path, out: dict[str, Any]) -> None:
        body = json.dumps(out, separators=(",", ":")).encode()
        target = take.uploads[RESULT]
        if len(body) > target.max_bytes:
            raise WorkerError("ARTIFACT_TOO_LARGE", f"{RESULT} exceeds {target.max_bytes} bytes")
        p = tmp / RESULT
        p.write_bytes(body)
        uploader.put(RESULT, p, target, upload_deadline)

    with tempfile.TemporaryDirectory(prefix="mmworker-") as td:
        tmp = Path(td)
        try:
            out = _run(take, adapter, uploader, clock, start, job_id, spawn, tmp, gen_deadline,
                       upload_deadline, timings, uploaded, state, output, put_result, sleep)
            return out
        except WorkerError as e:
            mm = output(e)
            if e.code not in _NO_RESULT_UPLOAD:
                try:
                    put_result(tmp, mm)
                except WorkerError:
                    pass  # best effort
            return {"error": f"{e.code}: {redact.message(e.message)}", "mm": mm}


def _run(take, adapter, uploader, clock, start, job_id, spawn, tmp, gen_deadline, upload_deadline,
         timings, uploaded, state, output, put_result, fetch_sleep) -> dict[str, Any]:
    v2 = take.contract == CONTRACT_V2
    # 1a. start handshake: 410 ⇒ ATTEMPT_CLOSED, persistent failure ⇒ START_FAILED, both before any render.
    start.announce(job_id)

    # 2. the job must be for the engine this worker loaded.
    if take.engine != adapter.engine:
        raise WorkerError("BAD_INPUT", f"unsupported engine {take.engine!r}")

    # 1c. (/2) the kind and every requested stage must exist in this image, before any fetch or GPU work.
    if v2:
        _check_kind(take)

    # 1b. fetch and verify `inputs` (acestep15, or the src_audio of a /2 analyze; before any GPU work, counted
    #     against the generation deadline). Verified files only; INPUT_FAILED / INPUT_MISMATCH otherwise.
    fetched: dict[str, Path] = {}
    if take.inputs:
        f0 = clock()
        fetched = inputs_mod.fetch(take.inputs, gen_deadline if gen_deadline is not None else upload_deadline,
                                   tmp, clock=clock, sleep=fetch_sleep)
        timings["fetch_s"] = round(clock() - f0, 3)

    # 2b. weights, cheap failure before any GPU work (compared as a set of repo -> revision).
    if dict(take.weights) != adapter.weights:
        raise WorkerError("WEIGHTS_MISMATCH", "the loaded weights differ from the expected revisions")

    # 3. request validation by the engine's own validator (render, plan). A WorkerError passes through.
    params: Any = None
    if take.kind != KIND_ANALYZE:
        try:
            params = adapter.validate(take.request)
            if adapter.engine == "yue2" and take.kind == KIND_RENDER:  # the forced minimum is yue2's min_tokens
                params = with_min_audio(params, take.min_audio_s)
        except WorkerError:
            raise
        except Exception as e:
            raise WorkerError("BAD_INPUT", redact.message(f"request rejected: {e}")) from None

    # 3b. (/2) write the attachments into <tmp>/attach/, digest checked. They are never collected as artifacts.
    attached = _write_attach(take, tmp) if v2 else {}

    ctx = _Ctx(take, adapter, params, fetched, attached, tmp, clock, start, job_id, spawn, gen_deadline,
               upload_deadline)
    if take.kind == KIND_ANALYZE:
        return _run_analyze(ctx, uploader, timings, uploaded, state, output, put_result)
    if take.kind == KIND_PLAN:
        return _run_plan(ctx, uploader, timings, uploaded, state, output, put_result)

    # 4. render, with the cancelled() callback (deadline or a 410 from the start URL).
    watch = CancelWatch(start, job_id, deadline=gen_deadline, clock=clock, spawn=spawn)
    g0 = clock()
    try:
        result = adapter.render(take, fetched, watch, tmp, params=params)
    except WorkerError:
        raise
    except Exception as e:
        _stopped(watch)
        raise WorkerError("PIPELINE_ERROR", redact.message(f"{type(e).__name__}: {e}")) from None
    timings["generate_s"] = round(clock() - g0, 3)
    _stopped(watch)
    # numeric extras an adapter measured (acestep15: lm_s, dit_s, peak_vram_mb_<component> ...); the schema's timings
    # object takes any number, and the fixed keys above are never overwritten.
    for k, v in (getattr(result, "timings", None) or {}).items():
        if k not in timings and isinstance(v, (int, float)) and not isinstance(v, bool):
            timings[k] = v

    # 5. save.
    s0 = clock()
    try:
        result.save_artifacts(tmp)
    except Exception as e:
        raise WorkerError("PIPELINE_ERROR", redact.message(f"save_artifacts: {type(e).__name__}: {e}")) from None
    timings["save_s"] = round(clock() - s0, 3)

    # 6. truncated is a dict {"abc": bool, "semantic": bool}; it is copied verbatim and the app applies
    #    any(values) (never `if result.truncated`, never `== False`: the spec bug, design 3.3). Other engines
    #    report null (state["truncated"] stays None).
    if adapter.engine == "yue2":
        truncated = getattr(result, "truncated", None)
        if not _valid_truncated(truncated):
            raise WorkerError("TRUNCATED_NOT_DICT", "truncated is not a dict of exactly abc and semantic booleans")
        truncated = dict(truncated)
        state["truncated"] = {"abc": truncated["abc"], "semantic": truncated["semantic"]}

    # 6a. the forced minimum must be what yue2 recorded: config.json is the re-run record of the take.
    if take.min_audio_s:
        _check_min_tokens(tmp / "config.json", take.min_audio_s * SEMANTIC_TOKENS_PER_S)

    # 6b. (/2) the Tier B post-stage. It never fails the take: any failure is the `ears` block of the output.
    if take.ears is not None:
        state["ears"] = _render_ears(ctx, tmp / "audio.flac")

    # 7. collect: allowlisted files only (never latent.npy), required present, sizes, hashes.
    if v2:
        table = tuple((n, m) for n, m, _ in artifacts_v2(KIND_RENDER, take.ears is not None,
                                                         bool(take.ears and take.ears.keep_vocals)))
        required = {n for n, _, r in artifacts_v2(KIND_RENDER) if r and n != RESULT}
        if state["ears"] and state["ears"]["status"] == "ok":
            required.add(ANALYSIS)
    else:
        table = artifacts_for(take.engine)
        required = set(required_for(take.engine, str(take.request.get("cot", ""))))
    found = _collect(take, table, required, tmp)
    secs = flac_seconds(found["audio.flac"][0])
    if secs is None:
        raise WorkerError("PIPELINE_ERROR", "audio.flac has no readable duration")
    state["audio_seconds"] = round(secs, 3)

    # 8. upload in table order, mm-result.json last.
    return _upload_all(take, tmp, table, found, uploader, upload_deadline, timings, uploaded, output, put_result, clock)


@dataclass
class _Ctx:
    take: TakeInput
    adapter: Any
    params: Any
    fetched: dict[str, Path]
    attached: dict[str, Path]
    tmp: Path
    clock: Callable[[], float]
    start: StartClient
    job_id: str
    spawn: Callable[[Callable[[], None]], None]
    gen_deadline: float | None
    upload_deadline: float

    @property
    def ears_deadline(self) -> float:
        return ears_deadline(self.take, self.upload_deadline)

    def stage_ctx(self, timeout_s: float, watch: CancelWatch, audio: Path | None) -> stages.StageContext:
        return stages.StageContext(self.take, self.tmp, dict(self.fetched), dict(self.attached), watch, self.clock,
                                   timeout_s, self.adapter, self.params, audio)


def _check_kind(take: TakeInput) -> None:
    """Step 1c: KIND_UNAVAILABLE names what is missing and nothing else."""
    need = {KIND_ANALYZE: stages.ANALYZE, KIND_PLAN: stages.PLAN}.get(take.kind)
    if need and stages.get(need) is None:
        raise WorkerError("KIND_UNAVAILABLE", f"kind {take.kind} is not available in this image")
    if take.ears is not None:
        reg = stages.get(stages.EARS)
        if take.kind == KIND_RENDER and reg is None:
            raise WorkerError("KIND_UNAVAILABLE", "stage ears is not available in this image")
        if take.kind == KIND_ANALYZE:
            reg = stages.get(stages.ANALYZE)
        missing = sorted(set(take.ears.stages) - reg.ears_stages) if reg else []
        if missing:
            raise WorkerError("KIND_UNAVAILABLE", f"ears stage {missing[0]} is not available in this image")


def _write_attach(take: TakeInput, tmp: Path) -> dict[str, Path]:
    out: dict[str, Path] = {}
    if not take.attach:
        return out
    d = tmp / "attach"
    d.mkdir()
    for name, f in take.attach.items():
        if hashlib.sha256(f.data).hexdigest() != f.sha256:
            raise WorkerError("ATTACH_MISMATCH", f"attachment {name} does not match its sha256")
        (d / name).write_bytes(f.data)
        out[name] = d / name
    return out


def _run_stage(ctx: _Ctx, name: str, audio: Path | None, budget_s: int) -> tuple[stages.EarsOutcome, bool]:
    """Run an ears-type stage under min(budget, time left to the ears deadline). Never raises for a stage fault:
    returns (outcome, closed) where closed says the app stopped the attempt."""
    reg = stages.get(name)
    left = ctx.ears_deadline - ctx.clock()
    timeout = min(float(budget_s), left)
    watch = CancelWatch(ctx.start, ctx.job_id, deadline=ctx.clock() + timeout, clock=ctx.clock, spawn=ctx.spawn)
    t0 = ctx.clock()
    try:
        if timeout <= 0 or reg is None:
            raise WorkerError("DEADLINE", "no time left for the stage")
        got = reg.run(ctx.stage_ctx(timeout, watch, audio))
        if not isinstance(got, stages.EarsOutcome) or got.status not in ("ok", "error", "skipped"):
            raise WorkerError("PIPELINE_ERROR", "the stage returned no outcome")
        out = got
    except WorkerError as e:
        out = stages.EarsOutcome("error", e.code)
    except Exception:
        out = stages.EarsOutcome("error", "STAGE_FAILED")
    if watch.deadline_hit and out.status == "error" and out.code in (None, "STAGE_FAILED"):
        out = stages.EarsOutcome("error", "DEADLINE")
    if out.status == "error" and not out.code:
        out = stages.EarsOutcome("error", "STAGE_FAILED", out.seconds, out.audio_seconds)
    seconds = out.seconds if out.seconds else round(ctx.clock() - t0, 3)
    return stages.EarsOutcome(out.status, out.code, seconds, out.audio_seconds), watch.closed


_CODE_RE = re.compile(r"^[A-Z][A-Z0-9_]*$")


def _code(code: Any, default: str) -> str:
    """A stage-supplied error code that the output schema accepts (UPPER_SNAKE), else the fallback."""
    return code if isinstance(code, str) and len(code) <= 64 and _CODE_RE.match(code) else default


def _ears_block(o: stages.EarsOutcome) -> dict[str, Any]:
    b: dict[str, Any] = {"status": o.status, "seconds": round(max(o.seconds, 0.0), 3)}
    if o.code:
        b["code"] = _code(o.code, "STAGE_FAILED")
    return b


def _render_ears(ctx: _Ctx, audio: Path) -> dict[str, Any]:
    """Step 6b. Skipped (code DEADLINE) when generation ended after ears_deadline - budget/3 (C321.6)."""
    ears = ctx.take.ears
    assert ears is not None
    if ctx.clock() > ctx.ears_deadline - ears.budget_s / 3:
        return {"status": "skipped", "code": "DEADLINE", "seconds": 0.0}
    out, closed = _run_stage(ctx, stages.EARS, audio, ears.budget_s)
    if closed:
        raise WorkerError("ATTEMPT_CLOSED", "the app closed this attempt during the stage")
    if out.status == "ok" and not (ctx.tmp / ANALYSIS).is_file():
        out = stages.EarsOutcome("error", "NO_DOCUMENT", out.seconds)
    return _ears_block(out)


def _run_analyze(ctx: _Ctx, uploader, timings, uploaded, state, output, put_result) -> dict[str, Any]:
    take = ctx.take
    assert take.ears is not None
    src = ctx.fetched["src_audio"]
    out, closed = _run_stage(ctx, stages.ANALYZE, src, take.ears.budget_s)
    if closed:
        raise WorkerError("ATTEMPT_CLOSED", "the app closed this attempt during the stage")
    timings["generate_s"] = out.seconds
    state["ears"] = _ears_block(out)
    secs = out.audio_seconds if out.audio_seconds else flac_seconds(src)
    state["audio_seconds"] = round(secs, 3) if secs else None
    table = tuple((n, m) for n, m, _ in artifacts_v2(KIND_ANALYZE, True, take.ears.keep_vocals))
    required = {ANALYSIS} if out.status == "ok" else set()
    found = _collect(take, table, required, ctx.tmp)
    if out.status == "ok" and state["audio_seconds"] is None:
        raise WorkerError("PIPELINE_ERROR", "the input has no readable duration")
    if out.status != "ok":
        # the document (when the stage wrote one) still goes up, then the run ends in error (C321.4)
        _upload_all(take, ctx.tmp, table, found, uploader, ctx.upload_deadline, timings, uploaded, output, put_result,
                    ctx.clock, final=False)
        raise WorkerError("ANALYZE_FAILED", f"the analysis ended {out.status} ({out.code})")
    return _upload_all(take, ctx.tmp, table, found, uploader, ctx.upload_deadline, timings, uploaded, output, put_result,
                       ctx.clock)


def _run_plan(ctx: _Ctx, uploader, timings, uploaded, state, output, put_result) -> dict[str, Any]:
    take = ctx.take
    assert take.plan is not None
    reg = stages.get(stages.PLAN)
    watch = CancelWatch(ctx.start, ctx.job_id, deadline=ctx.gen_deadline, clock=ctx.clock, spawn=ctx.spawn)
    g0 = ctx.clock()
    try:
        outcomes = reg.run(ctx.stage_ctx(float("inf"), watch, None)) if reg else []
    except WorkerError:
        raise
    except Exception as e:
        _stopped(watch)
        raise WorkerError("PIPELINE_ERROR", redact.message(f"{type(e).__name__}: {e}")) from None
    timings["generate_s"] = round(ctx.clock() - g0, 3)
    if watch.closed:
        _stopped(watch)
    seeds = take.plan.seeds
    if not isinstance(outcomes, list) or not all(
            isinstance(o, stages.PlanOutcome) and isinstance(o.index, int) and isinstance(o.seed, int)
            and o.status in ("ok", "error") for o in outcomes):
        raise WorkerError("PIPELINE_ERROR", "the plan stage returned no valid report")
    plans: list[dict[str, Any]] = []
    for o in sorted(outcomes, key=lambda x: x.index):
        if not (0 <= o.index < len(seeds)) or o.seed != seeds[o.index] or any(p["index"] == o.index for p in plans):
            raise WorkerError("PIPELINE_ERROR", "the plan stage returned an unknown seed index")
        p: dict[str, Any] = {"index": o.index, "seed": o.seed, "status": "ok" if o.status == "ok" else "error"}
        if p["status"] == "ok":
            p["bundle"] = plan_name(o.index)
            if o.truncated:
                p["truncated"] = {str(k): bool(v) for k, v in o.truncated.items()}
        else:
            p["code"] = _code(o.code, "PLAN_FAILED")
        plans.append(p)
    if len(plans) != len(seeds):
        raise WorkerError("PIPELINE_ERROR", "the plan stage did not report every seed")
    if watch.deadline_hit and not any(p["status"] == "ok" for p in plans):
        raise WorkerError("DEADLINE", "the generation deadline passed")
    # a deadline that fired mid-run keeps the seeds that finished: their bundles ship and the rest report DEADLINE
    state["plans"] = plans
    table = tuple((n, m) for n, m, _ in artifacts_v2(KIND_PLAN, seeds=len(seeds)))
    required = {p["bundle"] for p in plans if p["status"] == "ok"}
    found = _collect(take, table, required, ctx.tmp)
    if not required:
        _upload_all(take, ctx.tmp, table, found, uploader, ctx.upload_deadline, timings, uploaded, output, put_result,
                    ctx.clock, final=False)
        raise WorkerError("PLANS_FAILED", "every plan seed failed")
    return _upload_all(take, ctx.tmp, table, found, uploader, ctx.upload_deadline, timings, uploaded, output, put_result,
                       ctx.clock)


def _collect(take: TakeInput, table: tuple[tuple[str, int], ...], required: set[str] | frozenset[str],
             tmp: Path) -> dict[str, tuple[Path, str, int]]:
    """Step 7: allowlisted files only, required present, sizes, hashes."""
    max_bytes = dict(table)
    found: dict[str, tuple[Path, str, int]] = {}
    for name in (n for n, _ in table if n != RESULT):
        p = tmp / name
        if not p.is_file():
            continue
        limit = max_bytes[name]
        if name in take.uploads:
            limit = min(limit, take.uploads[name].max_bytes)
        if p.stat().st_size > limit:
            raise WorkerError("ARTIFACT_TOO_LARGE", f"{name} exceeds {limit} bytes")
        sha, n = sha256_file(p)
        found[name] = (p, sha, n)
    missing = sorted(set(required) - set(found))
    if missing:
        raise WorkerError("ARTIFACT_MISSING", f"required artifact missing: {missing[0]}")
    return found


def _upload_all(take, tmp, table, found, uploader, upload_deadline, timings, uploaded, output, put_result, clock,
                *, final: bool = True) -> dict[str, Any]:
    """Step 8: table order, mm-result.json last. With final=False the files go up but mm-result.json is left to
    the error path of process() (which uploads the error document)."""
    u0 = clock()
    for name in (n for n, _ in table if n != RESULT):
        if name not in found or name not in take.uploads:
            continue
        p, sha, n = found[name]
        uploader.put(name, p, take.uploads[name], upload_deadline, sha256=sha)
        uploaded[name] = {"sha256": sha, "bytes": n}
    timings["upload_s"] = round(clock() - u0, 3)
    if not final:
        return {}
    doc = output(None)
    put_result(tmp, doc)
    return doc


def _check_min_tokens(config: Path, want: int) -> None:
    try:
        got = json.loads(config.read_text())["generation"]["semantic"]["min_tokens"]
    except (OSError, ValueError, KeyError, TypeError):
        got = None
    if got is None or isinstance(got, bool) or not isinstance(got, int) or got < want:
        raise WorkerError("PIPELINE_ERROR", f"config.json does not record semantic min_tokens >= {want}")


def _stopped(watch: CancelWatch) -> None:
    if watch.closed:
        raise WorkerError("ATTEMPT_CLOSED", "the app closed this attempt during the render")
    if watch.deadline_hit:
        raise WorkerError("DEADLINE", "the generation deadline passed")
