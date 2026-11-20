"""Vocal pitch contour with torchcrepe `full` (C331.3). torch and torchcrepe are imported lazily."""

from __future__ import annotations

import math
import time
from collections.abc import Callable
from typing import Any

import numpy as np

from .output import MAX_PITCH_FRAMES, PITCH_HOP_S

CREPE_RATE = 16000
CREPE_HOP = 160  # 10 ms
VOICED_PERIODICITY = 0.21  # torchcrepe's own default threshold for voiced frames


def frame_count(duration_s: float) -> int:
    """ceil(duration / 0.02), capped at 36 000 frames (720 s). Rounded first so 10.0 s is 500, not 501."""
    return min(MAX_PITCH_FRAMES, max(0, math.ceil(round(duration_s / PITCH_HOP_S, 6))))


def to_frames(f0_hz: Any, periodicity: Any, duration_s: float) -> dict[str, Any]:
    """10 ms hop f0 + periodicity to the contract's 20 ms frames.

    cents = 100 x MIDI note number (0 = unvoiced: NaN/non-positive f0 or periodicity below 0.21),
    conf = periodicity in 0..100. Both arrays have frame_count(duration_s) entries.
    """
    n = frame_count(duration_s)
    f0 = np.asarray(f0_hz, dtype=np.float64).reshape(-1)[::2][:n]
    per = np.asarray(periodicity, dtype=np.float64).reshape(-1)[::2][:n]
    m = min(f0.size, per.size)
    f0, per = f0[:m], per[:m]
    per = np.nan_to_num(per, nan=0.0, posinf=1.0, neginf=0.0).clip(0.0, 1.0)
    voiced = np.isfinite(f0) & (f0 > 0) & (per >= VOICED_PERIODICITY)
    with np.errstate(divide="ignore", invalid="ignore"):
        midi = 69.0 + 12.0 * np.log2(np.where(voiced, f0, 440.0) / 440.0)
    cents = np.where(voiced, np.maximum(0, np.rint(midi * 100.0)), 0).astype(np.int64)
    conf = np.rint(per * 100.0).astype(np.int64)
    cents_l, conf_l = cents.tolist(), conf.tolist()
    pad = n - len(cents_l)
    if pad > 0:
        cents_l += [0] * pad
        conf_l += [0] * pad
    return {"hop_s": PITCH_HOP_S, "cents": cents_l, "conf": conf_l}


def predict(
    vocals: Any,
    device: str,
    add_load: Callable[[float], None],
) -> tuple[Any, Any]:
    """Run torchcrepe on 16 kHz mono vocals: Viterbi decoding, periodicity as confidence."""
    import torch
    import torchcrepe

    t0 = time.monotonic()
    audio = torch.from_numpy(np.ascontiguousarray(vocals, dtype=np.float32))[None]
    add_load(time.monotonic() - t0)
    with torch.no_grad():
        f0, per = torchcrepe.predict(
            audio,
            CREPE_RATE,
            hop_length=CREPE_HOP,
            fmin=50.0,
            fmax=1100.0,
            model="full",
            decoder=torchcrepe.decode.viterbi,
            return_periodicity=True,
            batch_size=512,
            device=device,
        )
    return f0[0].cpu().numpy(), per[0].cpu().numpy()

