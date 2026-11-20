"""Input decoding with soundfile (FLAC or MP3). No torchaudio I/O anywhere (C331.3)."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import numpy as np

from .errors import EarsError


@dataclass(frozen=True)
class Decoded:
    samples: Any  # float32 ndarray, shape (frames, channels)
    sample_rate: int
    channels: int
    codec: str
    duration_s: float


def decode(path: str) -> Decoded:
    """Decode FLAC/MP3 to float32. Any failure is INPUT_DECODE."""
    try:
        import soundfile as sf

        info = sf.info(path)
        data, sr = sf.read(path, dtype="float32", always_2d=True)
    except Exception as exc:  # soundfile raises RuntimeError/LibsndfileError/OSError
        raise EarsError("INPUT_DECODE", f"cannot decode input: {type(exc).__name__}") from exc
    frames = int(data.shape[0])
    if frames == 0 or sr < 1:
        raise EarsError("INPUT_DECODE", "input has no audio")
    codec = "mp3" if "MPEG" in info.format else info.format.lower()
    return Decoded(
        samples=np.ascontiguousarray(data),
        sample_rate=int(sr),
        channels=int(data.shape[1]),
        codec=codec,
        duration_s=frames / float(sr),
    )


def to_mono(samples: Any) -> Any:
    """Average the channels of a (frames, channels) array."""
    return samples.mean(axis=1).astype(np.float32, copy=False)
