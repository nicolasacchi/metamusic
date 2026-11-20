"""CLI and orchestration: python -m mmears --audio F --lang it|auto --stages separate,asr[,transcribe] ...

Exit 0 whenever a document was written (also status:error); non-zero only when none could be written, in
which case the parent writes the error document (C331.2). Each stage records its own failure and the next
stages that do not need it still run.
"""

from __future__ import annotations

import argparse
import gc
import os
import re
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import audio, models, output, pitch, resources, vocals
from .errors import EarsError, classify


@dataclass
class Ctx:
    models_dir: Path
    device: str
    load_s: float = 0.0

    def add_load(self, dt: float) -> None:
        self.load_s += dt


@dataclass
class Stages:
    """The heavy stage functions; tests substitute fakes (nothing here imports torch)."""

    separate: Callable[[audio.Decoded, Ctx], Any]  # -> mono 16 kHz vocals
    asr: Callable[[Any, str, Ctx], tuple[dict[str, Any], str]]  # -> (asr block, compute_type)
    pitch: Callable[[Any, float, Ctx], tuple[Any, Any]]  # -> (f0 hz at 10 ms, periodicity)
    pick_device: Callable[[], str] = resources.pick_device
    preload_cuda: Callable[[], Any] | None = None


def real_stages() -> Stages:
    def sep(dec: audio.Decoded, ctx: Ctx) -> Any:
        from . import separate

        return separate.separate(dec, models.model_dir(ctx.models_dir, "separator"), ctx.device, ctx.add_load)

    def asr_stage(v: Any, lang: str, ctx: Ctx) -> tuple[dict[str, Any], str]:
        from . import asr

        return asr.transcribe(v, lang, models.model_dir(ctx.models_dir, "asr"), ctx.device, ctx.add_load)

    def pitch_stage(v: Any, dur: float, ctx: Ctx) -> tuple[Any, Any]:
        return pitch.predict(v, ctx.device, ctx.add_load)

    def preload() -> Any:
        from . import cudalibs

        return cudalibs.preload()

    return Stages(sep, asr_stage, pitch_stage, preload_cuda=preload)


def parse_stages(spec: str) -> tuple[str, ...]:
    names = tuple(s for s in spec.split(",") if s)
    if names[:2] != ("separate", "asr") or names[2:] not in ((), ("transcribe",)):
        raise ValueError("stages must be separate,asr or separate,asr,transcribe")
    return names


def run(
    audio_path: str,
    lang: str,
    stage_names: tuple[str, ...],
    models_dir: str,
    stages: Stages,
    lock_path: str | None = None,
    parent_reserved_mib: int | None = None,
) -> dict[str, Any]:
    """Run the pipeline and return the document; never raises for a stage failure."""
    t_start = time.monotonic()
    want_pitch = "transcribe" in stage_names
    status = {"separate": "skipped", "asr": "skipped", "transcribe": "skipped" if want_pitch else "not_requested"}
    errors: list[dict[str, str]] = []
    timings = {"load_s": 0.0, "separate_s": 0.0, "asr_s": 0.0, "transcribe_s": 0.0}
    ctx = Ctx(Path(models_dir), "cpu")

    def finish(**kw: Any) -> dict[str, Any]:
        timings["load_s"] = ctx.load_s + timings["load_s"]
        timings["total_s"] = time.monotonic() - t_start
        name, peak = resources.snapshot(ctx.device)
        return output.build(
            stages=status,
            timings=timings,
            resources={"device": name, "peak_vram_mib": peak, "parent_reserved_mib": parent_reserved_mib},
            errors=errors,
            **{"models": {}, "input_info": None, "asr": None, "pitch": None, "vocals": None, **kw},
        )

    # 1. models: sizes (and sha256 once per process); never a download
    t0 = time.monotonic()
    roles = ("separator", "asr") + (("pitch",) if want_pitch else ())
    try:
        lock = models.load_lock(lock_path)
        verified = models.verify(lock, models_dir, roles)
    except EarsError as e:
        errors.append({"stage": "load", "code": e.code, "message": e.message})
        return finish()
    timings["load_s"] += time.monotonic() - t0
    used: dict[str, Any] = {}

    def model_entry(role: str) -> dict[str, str]:
        m = verified[role]
        return {"name": m.name, "sha256": m.digest()}

    # 2. input
    try:
        dec = audio.decode(audio_path)
    except EarsError as e:
        errors.append({"stage": "input", "code": e.code, "message": e.message})
        return finish()
    input_info = {
        "duration_s": output.r2(dec.duration_s),
        "sample_rate": dec.sample_rate,
        "channels": dec.channels,
        "codec": dec.codec,
    }

    ctx.device = stages.pick_device()
    if ctx.device == "cuda" and stages.preload_cuda is not None:
        try:
            stages.preload_cuda()
        except Exception:  # missing nvidia wheels: faster-whisper reports it as an ASR failure
            pass

    def fail(stage: str, default: str, exc: BaseException) -> None:
        e = classify(exc, default)
        status[stage] = "error"
        errors.append({"stage": stage, "code": e.code, "message": e.message})

    # 3. separate
    voc = None
    t0 = time.monotonic()
    try:
        voc = stages.separate(dec, ctx)
        used["separator"] = model_entry("separator")
        status["separate"] = "ok"
    except Exception as exc:
        fail("separate", "SEPARATION_FAILED", exc)
    timings["separate_s"] = time.monotonic() - t0
    gc.collect()
    resources.free_cache()

    asr_block = pitch_block = voc_block = None
    if voc is not None:
        try:
            voc_block = vocals.measure(voc, 16000)
        except Exception:
            voc_block = None  # a measurement failure never fails the stage

        # 4. asr
        t0 = time.monotonic()
        try:
            asr_block, ctype = stages.asr(voc, lang, ctx)
            used["asr"] = {**model_entry("asr"), "compute_type": ctype}
            status["asr"] = "ok"
        except Exception as exc:
            fail("asr", "ASR_FAILED", exc)
        timings["asr_s"] = time.monotonic() - t0
        gc.collect()
        resources.free_cache()

        # 5. transcribe (vocal pitch contour)
        if want_pitch:
            t0 = time.monotonic()
            try:
                f0, per = stages.pitch(voc, dec.duration_s, ctx)
                pitch_block = pitch.to_frames(f0, per, dec.duration_s)
                used["pitch"] = model_entry("pitch")
                status["transcribe"] = "ok"
            except Exception as exc:
                fail("transcribe", "SUBPROCESS_FAILED", exc)
            timings["transcribe_s"] = time.monotonic() - t0
    return finish(models=used, input_info=input_info, asr=asr_block, pitch=pitch_block, vocals=voc_block)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="mmears")
    ap.add_argument("--audio")
    ap.add_argument("--lang", default="auto", help="language code or auto")
    ap.add_argument("--stages", default="separate,asr")
    ap.add_argument("--models", required=True)
    ap.add_argument("--out")
    ap.add_argument("--lock", default=None)
    ap.add_argument("--parent-reserved-mib", type=int, default=None)
    ap.add_argument("--self-check", action="store_true")
    a = ap.parse_args(argv)

    os.environ.setdefault("HF_HUB_OFFLINE", "1")
    os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")

    if a.self_check:
        try:
            lock = models.load_lock(a.lock)
            models.verify(lock, a.models)
        except EarsError as e:
            print(f"self-check failed: {e.message}", file=sys.stderr)
            return 1
        print("self-check ok")
        return 0

    if not a.audio or not a.out:
        ap.error("--audio and --out are required")
    if not re.fullmatch(r"auto|[a-z]{2,3}", a.lang):
        ap.error("--lang must be auto or a language code")
    try:
        names = parse_stages(a.stages)
    except ValueError as e:
        ap.error(str(e))

    try:
        doc = run(a.audio, a.lang, names, a.models, real_stages(), a.lock, a.parent_reserved_mib)
        output.write(a.out, doc)
    except Exception as exc:
        try:
            output.write(a.out, output.error_document("SUBPROCESS_FAILED", "load", type(exc).__name__, names))
        except Exception:
            return 2
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
