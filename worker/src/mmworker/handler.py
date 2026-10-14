"""process(): one take, in the exact order of C83.2."""

from __future__ import annotations

import json
import os
import tempfile
from collections.abc import Callable, Mapping
from pathlib import Path
from typing import Any

from . import redact
from .contract import ARTIFACTS, CONTRACT, MAX_BYTES, REQUIRED, RESULT, TakeInput
from .env import ENGINE, fingerprint, flac_seconds
from .errors import WorkerError
from .pipeline import Pipeline
from .start import CancelWatch, StartClient, _spawn_thread
from .upload import Uploader, sha256_file

# Errors after which no mm-result.json upload is attempted (the app said stop, or cannot be reached).
_NO_RESULT_UPLOAD = frozenset({"ATTEMPT_CLOSED", "START_FAILED", "UPLOAD_CLOSED"})
_UPLOAD_ORDER = tuple(a for a in ARTIFACTS if a != RESULT)
_FINISH_MARGIN_S = 10.0
_UPLOAD_LEAD_S = 60.0


def deadlines(take: TakeInput, handler_start: float, environ: Mapping[str, str]) -> tuple[float, float | None]:
    """(upload_deadline, generation_deadline). D-M3-13: uploads stop 10 s before RunPod's kill; generation
    stops upload_budget_s earlier. MM_WORKER_DEADLINE=provider disables the generation deadline."""
    upload = min(handler_start + take.timeout_s - _FINISH_MARGIN_S, take.deadline_unix - _UPLOAD_LEAD_S)
    if environ.get("MM_WORKER_DEADLINE") == "provider":
        return upload, None
    return upload, upload - take.upload_budget_s


def _valid_truncated(t: Any) -> bool:
    return (isinstance(t, dict) and set(t) == {"abc", "semantic"}
            and all(type(v) is bool for v in t.values()))


def process(take: TakeInput, pipe: Pipeline, uploader: Uploader, clock: Callable[[], float], *,
            start: StartClient, job_id: str, environ: Mapping[str, str] | None = None,
            spawn: Callable[[Callable[[], None]], None] = _spawn_thread) -> dict[str, Any]:
    """Returns the C73.5 output on success, else {"error": "<CODE>: <message>", "mm": <output>}."""
    env = os.environ if environ is None else environ
    t0 = clock()
    upload_deadline, gen_deadline = deadlines(take, t0, env)
    timings: dict[str, float | None] = {"load_s": None, "generate_s": None, "save_s": None,
                                        "upload_s": None, "handler_s": None, "e2e_seconds": None}
    uploaded: dict[str, dict[str, Any]] = {}
    state: dict[str, Any] = {"truncated": None, "audio_seconds": None}

    def output(error: WorkerError | None) -> dict[str, Any]:
        timings["handler_s"] = round(clock() - t0, 3)
        gen, sav = timings["generate_s"], timings["save_s"]
        timings["e2e_seconds"] = None if gen is None else round(gen + (sav or 0.0), 3)
        ok = error is None
        return {
            "contract": CONTRACT, "take_id": take.take_id, "attempt_id": take.attempt_id,
            "status": "ok" if ok else "error",
            "error": None if ok else {"code": error.code, "message": redact.message(error.message)},
            "artifacts": dict(uploaded),
            "audio_seconds": state["audio_seconds"],
            "truncated": state["truncated"],
            "timings": dict(timings),
            "env": fingerprint(getattr(pipe, "weights", {}), env),
        }

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
            out = _run(take, pipe, uploader, clock, start, job_id, spawn, tmp, gen_deadline,
                       upload_deadline, timings, uploaded, state, output, put_result)
            return out
        except WorkerError as e:
            mm = output(e)
            if e.code not in _NO_RESULT_UPLOAD:
                try:
                    put_result(tmp, mm)
                except WorkerError:
                    pass  # best effort
            return {"error": f"{e.code}: {redact.message(e.message)}", "mm": mm}


def _run(take, pipe, uploader, clock, start, job_id, spawn, tmp, gen_deadline, upload_deadline,
         timings, uploaded, state, output, put_result) -> dict[str, Any]:
    # 1a. start handshake: 410 ⇒ ATTEMPT_CLOSED, persistent failure ⇒ START_FAILED, both before any render.
    start.announce(job_id)

    # 2. weights, cheap failure before any GPU work.
    if take.engine != ENGINE:
        raise WorkerError("BAD_INPUT", f"unsupported engine {take.engine!r}")
    if dict(take.weights) != dict(getattr(pipe, "weights", {})):
        raise WorkerError("WEIGHTS_MISMATCH", "the loaded weights differ from the expected revisions")

    # 3. request validation by the engine's own validator.
    try:
        kwargs = pipe.request_kwargs(take.request)
    except Exception as e:
        raise WorkerError("BAD_INPUT", redact.message(f"request rejected: {e}")) from None

    # 4. render, with the cancelled() callback (deadline or a 410 from the start URL).
    watch = CancelWatch(start, job_id, deadline=gen_deadline, clock=clock, spawn=spawn)
    g0 = clock()
    try:
        result = pipe(cancelled=watch, **kwargs)
    except WorkerError:
        raise
    except Exception as e:
        _stopped(watch)
        raise WorkerError("PIPELINE_ERROR", redact.message(f"{type(e).__name__}: {e}")) from None
    timings["generate_s"] = round(clock() - g0, 3)
    _stopped(watch)

    # 5. save.
    s0 = clock()
    try:
        result.save_artifacts(tmp)
    except Exception as e:
        raise WorkerError("PIPELINE_ERROR", redact.message(f"save_artifacts: {type(e).__name__}: {e}")) from None
    timings["save_s"] = round(clock() - s0, 3)

    # 6. truncated is a dict {"abc": bool, "semantic": bool}; it is copied verbatim and the app applies
    #    any(values) (never `if result.truncated`, never `== False`: the spec bug, design 3.3).
    truncated = getattr(result, "truncated", None)
    if not _valid_truncated(truncated):
        raise WorkerError("TRUNCATED_NOT_DICT", "truncated is not a dict of exactly abc and semantic booleans")
    truncated = dict(truncated)
    state["truncated"] = {"abc": truncated["abc"], "semantic": truncated["semantic"]}

    # 7. collect: allowlisted files only (never latent.npy), required present, sizes, hashes.
    found: dict[str, tuple[Path, str, int]] = {}
    for name in _UPLOAD_ORDER:
        p = tmp / name
        if not p.is_file():
            continue
        limit = MAX_BYTES[name]
        if name in take.uploads:
            limit = min(limit, take.uploads[name].max_bytes)
        if p.stat().st_size > limit:
            raise WorkerError("ARTIFACT_TOO_LARGE", f"{name} exceeds {limit} bytes")
        sha, n = sha256_file(p)
        found[name] = (p, sha, n)
    missing = sorted(REQUIRED(str(take.request.get("cot", ""))) - set(found))
    if missing:
        raise WorkerError("ARTIFACT_MISSING", f"required artifact missing: {missing[0]}")
    secs = flac_seconds(found["audio.flac"][0])
    if secs is None:
        raise WorkerError("PIPELINE_ERROR", "audio.flac has no readable duration")
    state["audio_seconds"] = round(secs, 3)

    # 8. upload in C83.2 order, mm-result.json last.
    u0 = clock()
    for name in _UPLOAD_ORDER:
        if name not in found or name not in take.uploads:
            continue
        p, sha, n = found[name]
        uploader.put(name, p, take.uploads[name], upload_deadline, sha256=sha)
        uploaded[name] = {"sha256": sha, "bytes": n}
    timings["upload_s"] = round(clock() - u0, 3)
    final = output(None)
    put_result(tmp, final)
    return final


def _stopped(watch: CancelWatch) -> None:
    if watch.closed:
        raise WorkerError("ATTEMPT_CLOSED", "the app closed this attempt during the render")
    if watch.deadline_hit:
        raise WorkerError("DEADLINE", "the generation deadline passed")
