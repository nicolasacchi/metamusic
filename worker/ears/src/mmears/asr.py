"""faster-whisper word timings on the vocals (C331.3, D-EARS-5). No prompt is ever given."""

from __future__ import annotations

import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

from .output import normalise_asr

# Fixed decoding options. initial_prompt stays None: prompting with the intended lyrics would hide the
# errors Tier B exists to find (D-M5-21).
TRANSCRIBE_KWARGS: dict[str, Any] = {
    "word_timestamps": True,
    "vad_filter": False,
    "condition_on_previous_text": False,
    "initial_prompt": None,
    "beam_size": 5,
    "temperature": 0,
}


def _default_factory(model_dir: str, device: str, compute_type: str) -> Any:
    from faster_whisper import WhisperModel

    return WhisperModel(model_dir, device=device, compute_type=compute_type, local_files_only=True)


def compute_type_for(device: str) -> str:
    return "float16" if device == "cuda" else "int8"


def transcribe(
    vocals: Any,
    lang: str,
    model_dir: Path,
    device: str,
    add_load: Callable[[float], None],
    model_factory: Callable[[str, str, str], Any] | None = None,
) -> tuple[dict[str, Any], str]:
    """Return (asr block, compute_type). `lang` is a code, forced, or "auto" (detected, forced=false)."""
    ctype = compute_type_for(device)
    t0 = time.monotonic()
    model = (model_factory or _default_factory)(str(model_dir), device, ctype)
    add_load(time.monotonic() - t0)

    forced = lang != "auto"
    segments, info = model.transcribe(vocals, language=lang if forced else None, **TRANSCRIBE_KWARGS)
    segs = []
    for s in segments:  # lazy generator: decoding happens here
        segs.append(
            {
                "start": s.start,
                "end": s.end,
                "avg_logprob": s.avg_logprob,
                "no_speech_p": s.no_speech_prob,
                "words": [
                    {"w": w.word, "start": w.start, "end": w.end, "p": w.probability}
                    for w in (s.words or [])
                ],
            }
        )
    language = lang if forced else str(getattr(info, "language", "") or "und")
    if len(language) < 2:
        language = "und"
    return normalise_asr(language[:8], forced, segs), ctype
