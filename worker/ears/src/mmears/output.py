"""Build and write analysis.json (contract mm.ears-b/1, C332). Pure; no heavy imports."""

from __future__ import annotations

import json
import math
import os
import tempfile
from typing import Any

from . import CONTRACT
from .errors import CODES

MAX_WORDS = 20000
MAX_SEGMENTS = 4000
MAX_PITCH_FRAMES = 36000
MAX_DOC_BYTES = 4 << 20
MAX_ERRORS = 20
MAX_MESSAGE = 300
PITCH_HOP_S = 0.02
STAGE_NAMES = ("separate", "asr", "transcribe")


def r2(x: float) -> float:
    return round(float(x), 2)


def _clamp(x: float, lo: float, hi: float) -> float:
    if math.isnan(x):
        return lo
    return max(lo, min(hi, x))


def normalise_asr(language: str, forced: bool, segments: list[dict[str, Any]]) -> dict[str, Any]:
    """Flatten segments (each with a `words` list) into the schema's segments + words arrays.

    Words are capped at 20 000 and segments at 4 000; a segment cut by the word cap keeps only the words
    that fit and later segments are dropped. Times have two decimals, are non-negative, end >= start, and
    word starts never decrease within a segment.
    """
    words: list[dict[str, Any]] = []
    segs: list[dict[str, Any]] = []
    for seg in segments:
        if len(segs) >= MAX_SEGMENTS or len(words) >= MAX_WORDS:
            break
        w0 = len(words)
        last = 0.0
        for w in seg.get("words", []):
            text = str(w["w"]).strip()[:64]
            if not text or len(words) >= MAX_WORDS:
                continue
            start = max(r2(max(0.0, w["start"])), last)
            end = max(r2(max(0.0, w["end"])), start)
            last = start
            words.append({"w": text, "start": start, "end": end, "p": round(_clamp(w["p"], 0.0, 1.0), 3)})
        w1 = len(words)
        start = r2(max(0.0, seg["start"]))
        segs.append(
            {
                "start": start,
                "end": max(r2(max(0.0, seg["end"])), start),
                "avg_logprob": round(min(0.0, float(seg["avg_logprob"])), 3),
                "no_speech_p": round(_clamp(seg["no_speech_p"], 0.0, 1.0), 3),
                "w0": w0,
                "w1": w1,
            }
        )
    return {"language": language, "forced": forced, "segments": segs, "words": words}


def build(
    *,
    stages: dict[str, str],
    models: dict[str, Any],
    input_info: dict[str, Any] | None,
    timings: dict[str, float],
    resources: dict[str, Any],
    asr: dict[str, Any] | None,
    pitch: dict[str, Any] | None,
    vocals: dict[str, float] | None,
    errors: list[dict[str, str]],
) -> dict[str, Any]:
    """Assemble the document. status is ok iff stages.asr is ok; the code is the first error's."""
    ok = stages["asr"] == "ok"
    errs = [
        {
            "stage": e["stage"],
            "code": e["code"] if e["code"] in CODES else "SUBPROCESS_FAILED",
            "message": e["message"][:MAX_MESSAGE],
        }
        for e in errors[:MAX_ERRORS]
    ]
    code = None
    if not ok:
        code = errs[0]["code"] if errs else "SUBPROCESS_FAILED"
    t = {k: r2(timings.get(k, 0.0)) for k in ("load_s", "separate_s", "asr_s", "transcribe_s", "total_s")}
    return {
        "contract": CONTRACT,
        "status": "ok" if ok else "error",
        "code": code,
        "stages": {k: stages[k] for k in STAGE_NAMES},
        "models": {
            "separator": models.get("separator"),
            "asr": models.get("asr"),
            "pitch": models.get("pitch"),
        },
        "input": input_info,
        "timings": t,
        "resources": resources,
        "asr": asr,
        "pitch": pitch,
        "vocals": vocals,
        "errors": errs,
    }


def error_document(code: str, stage: str, message: str, stages_requested: tuple[str, ...] = ("separate", "asr")) -> dict[str, Any]:
    """A minimal valid status:error document (also what a parent writes when the subprocess died)."""
    stages = {
        s: ("skipped" if s in stages_requested else "not_requested") for s in STAGE_NAMES
    }
    return build(
        stages=stages,
        models={},
        input_info=None,
        timings={},
        resources={"device": "none", "peak_vram_mib": 0, "parent_reserved_mib": None},
        asr=None,
        pitch=None,
        vocals=None,
        errors=[{"stage": stage, "code": code, "message": message}],
    )


def dumps(doc: dict[str, Any]) -> bytes:
    data = json.dumps(doc, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode()
    if len(data) > MAX_DOC_BYTES:
        raise ValueError(f"analysis.json is {len(data)} bytes, over {MAX_DOC_BYTES}")
    return data


def write(path: str | os.PathLike[str], doc: dict[str, Any]) -> None:
    """Atomic write: temp file in the target directory, then rename."""
    data = dumps(doc)
    d = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".analysis-", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
