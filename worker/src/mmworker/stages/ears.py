"""The Tier B ears stage (C331.4, C333.3): runs `mmears` as a subprocess and leaves `analysis.json` in the work dir.

One function serves both registrations: `ears` (step 6b of a render, on the take's own audio.flac) and `analyze` (the fetched input).
The parent never imports mmears or any heavy module of it (D-EARS-1, C331.5): the subprocess is this image's interpreter with
`MM_EARS_SITE` (the directory that holds mmears, demucs, faster-whisper and torchcrepe) on its own PYTHONPATH only.

The stage registers only when the model lock verifies by size against `MM_EARS_MODELS_DIR` at import (C333.3); the sha256 of every
file is checked by the subprocess, once per worker process. A dev machine or an image without the models registers nothing and the
`ears` and `analyze` capabilities stay closed.
"""

from __future__ import annotations

import gc
import json
import os
import subprocess
import sys
import time
from collections.abc import Callable, Mapping
from pathlib import Path, PurePosixPath
from typing import Any

from .. import stages
from ..errors import WorkerError

ENV_MODELS, ENV_LOCK, ENV_SITE, ENV_PYTHON = "MM_EARS_MODELS_DIR", "MM_EARS_LOCK", "MM_EARS_SITE", "MM_EARS_PYTHON"
LOCK_FORMAT = "mm.ears-models/1"
ROLES = ("separator", "asr", "pitch")
DOCUMENT = "analysis.json"
POLL_S = 0.25
KILL_GRACE_S = 5.0
# The stage sets the CLI accepts, in the CLI's own order (mmears.parse_stages).
_ORDER = ("separate", "asr", "transcribe")

# Seams for the tests: the subprocess factory, the wall clock used to wait for it and its sleep.
_spawn: Callable[..., Any] = subprocess.Popen
_now: Callable[[], float] = time.monotonic
_sleep: Callable[[float], None] = time.sleep


def lock_verifies(lock_path: str, models_dir: str) -> bool:
    """C333.3: the lock parses, names all three roles and every file exists with the locked size. Never hashes."""
    try:
        doc = json.loads(Path(lock_path).read_bytes())
        if not isinstance(doc, dict) or doc.get("format") != LOCK_FORMAT:
            return False
        have: set[str] = set()
        for m in doc["models"]:
            role = m["role"]
            if role not in ROLES or role in have:
                return False
            have.add(role)
            if not m["files"]:
                return False
            for f in m["files"]:
                rel = PurePosixPath(f["path"])
                if rel.is_absolute() or ".." in rel.parts or not isinstance(f["bytes"], int):
                    return False
                if (Path(models_dir) / role / rel).stat().st_size != f["bytes"]:
                    return False
        return have == set(ROLES)
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        return False


def _canonical_stages(requested: tuple[str, ...]) -> str:
    """`separate,asr[,transcribe]` for the requested set; anything else is not something the CLI can run."""
    want = tuple(s for s in _ORDER if s in requested)
    if want not in (_ORDER[:2], _ORDER):
        raise WorkerError("PIPELINE_ERROR", "ears stages must be separate,asr or separate,asr,transcribe")
    return ",".join(want)


def _parent_reserved_mib() -> int | None:
    """Free the parent's CUDA cache before the spawn and report what it held (C331.4). Never imports torch itself:
    an analyze job (no pipeline) has no torch in this process and reports null."""
    gc.collect()
    torch = sys.modules.get("torch")
    try:
        if torch is None or not torch.cuda.is_available():
            return None
        torch.cuda.empty_cache()
        return int(torch.cuda.max_memory_reserved() // (1 << 20))
    except Exception:  # a driver fault must not fail the take: the field is optional
        return None


def _env(environ: Mapping[str, str]) -> dict[str, str]:
    """The subprocess's environment: a copy of ours plus the site directory and offline switches. Our own os.environ and
    sys.path are never touched (the parent must not be able to import mmears)."""
    env = dict(environ)
    site = environ.get(ENV_SITE, "")
    if site:
        env["PYTHONPATH"] = site + (os.pathsep + env["PYTHONPATH"] if env.get("PYTHONPATH") else "")
    env["HF_HUB_OFFLINE"] = "1"
    env["TRANSFORMERS_OFFLINE"] = "1"
    env["PYTHONUNBUFFERED"] = "1"
    return env


def _stop(proc: Any) -> None:
    """Terminate the subprocess, then kill it after a grace period."""
    for kill, grace in ((proc.terminate, KILL_GRACE_S), (proc.kill, 30.0)):
        try:
            kill()
            proc.wait(timeout=grace)
            return
        except subprocess.TimeoutExpired:
            continue
        except Exception:  # already gone
            return


def _error_document(code: str, stage: str, requested: tuple[str, ...]) -> dict[str, Any]:
    """A schema-valid `status: error` document, written by the parent when the subprocess left none (C331.2)."""
    return {
        "contract": "mm.ears-b/1", "status": "error", "code": code,
        "stages": {"separate": "skipped", "asr": "skipped",
                   "transcribe": "skipped" if "transcribe" in requested else "not_requested"},
        "models": {"separator": None, "asr": None, "pitch": None},
        "input": None,
        "timings": {"load_s": 0, "separate_s": 0, "asr_s": 0, "transcribe_s": 0, "total_s": 0},
        "resources": {"device": "unknown", "peak_vram_mib": 0, "parent_reserved_mib": None},
        "asr": None, "pitch": None, "vocals": None,
        "errors": [{"stage": stage, "code": code, "message": "the ears subprocess left no document"}],
    }


def _write_error_document(path: Path, code: str, stage: str, requested: tuple[str, ...]) -> None:
    tmp = path.with_name(path.name + ".part")
    tmp.write_text(json.dumps(_error_document(code, stage, requested)))
    os.replace(tmp, path)


def run(ctx: stages.StageContext, *, environ: Mapping[str, str] | None = None) -> stages.EarsOutcome:
    """Run mmears on ctx.audio under ctx.timeout_s. A stage fault is an EarsOutcome error, never an exception (except a bad request)."""
    environ = os.environ if environ is None else environ
    take_ears = ctx.take.ears
    if take_ears is None or ctx.audio is None:
        raise WorkerError("PIPELINE_ERROR", "the ears stage needs an ears request and an audio file")
    requested = tuple(take_ears.stages)
    names = _canonical_stages(requested)
    out = ctx.tmp / DOCUMENT
    models = environ.get(ENV_MODELS, "")
    cmd = [environ.get(ENV_PYTHON) or sys.executable, "-m", "mmears", "--audio", str(ctx.audio), "--lang", take_ears.lang,
           "--stages", names, "--models", models, "--out", str(out)]
    if environ.get(ENV_LOCK):
        cmd += ["--lock", environ[ENV_LOCK]]
    reserved = _parent_reserved_mib()
    if reserved is not None:
        cmd += ["--parent-reserved-mib", str(reserved)]

    t0 = _now()
    try:
        proc = _spawn(cmd, env=_env(environ), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except OSError:
        _write_error_document(out, "SUBPROCESS_FAILED", "load", requested)
        return stages.EarsOutcome("error", "SUBPROCESS_FAILED", round(_now() - t0, 3))

    stopped = False
    while proc.poll() is None:
        if ctx.cancelled() or _now() - t0 > ctx.timeout_s:
            stopped = True
            _stop(proc)
            break
        _sleep(POLL_S)
    seconds = round(_now() - t0, 3)

    if stopped and getattr(ctx.cancelled, "closed", False):
        # the app closed the attempt: the handler ends the run (ATTEMPT_CLOSED); no document is kept
        out.unlink(missing_ok=True)
        return stages.EarsOutcome("error", "STAGE_FAILED", seconds)
    if stopped:
        _write_error_document(out, "DEADLINE", "load", requested)
        return stages.EarsOutcome("error", "DEADLINE", seconds)
    return _outcome(out, proc.returncode, seconds, requested)


def _outcome(out: Path, returncode: int, seconds: float, requested: tuple[str, ...]) -> stages.EarsOutcome:
    doc: Any = None
    try:
        doc = json.loads(out.read_bytes())
    except (OSError, ValueError):
        doc = None
    if not isinstance(doc, dict) or doc.get("contract") != "mm.ears-b/1":
        # exit 0 with no readable document, or any non-zero exit: the parent writes the error document (C331.2)
        _write_error_document(out, "SUBPROCESS_FAILED", "load", requested)
        return stages.EarsOutcome("error", "SUBPROCESS_FAILED", seconds)
    audio_s = (doc.get("input") or {}).get("duration_s")
    audio_s = float(audio_s) if isinstance(audio_s, (int, float)) and not isinstance(audio_s, bool) and audio_s > 0 else None
    if doc.get("status") == "ok":
        return stages.EarsOutcome("ok", None, seconds, audio_s)
    code = doc.get("code")
    return stages.EarsOutcome("error", code if isinstance(code, str) and code else "SUBPROCESS_FAILED", seconds, audio_s)


def register_stage(environ: Mapping[str, str] | None = None) -> bool:
    """Register `ears` and `analyze` when the lock verifies (C333.3). Returns whether it did."""
    environ = os.environ if environ is None else environ
    if not environ.get(ENV_MODELS) or not lock_verifies(environ.get(ENV_LOCK, ""), environ[ENV_MODELS]):
        return False
    stages.register(stages.EARS, run)
    stages.register(stages.ANALYZE, run)
    return True


register_stage()
