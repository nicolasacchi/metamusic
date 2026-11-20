"""Vocal level measurements on the separated stem."""

from __future__ import annotations

import math
from typing import Any

import numpy as np

ACTIVE_WINDOW_S = 0.05
ACTIVE_FLOOR_DBFS = -50.0
_FLOOR = 1e-10


def measure(mono: Any, sample_rate: int) -> dict[str, float]:
    """RMS in dBFS (<= 0) and seconds of activity (50 ms windows above -50 dBFS)."""
    x = np.asarray(mono, dtype=np.float64)
    if x.size == 0:
        return {"rms_dbfs": 0.0, "active_s": 0.0}
    rms = math.sqrt(float(np.mean(x * x)))
    rms_db = min(0.0, 20.0 * math.log10(max(rms, _FLOOR)))
    win = max(1, int(round(ACTIVE_WINDOW_S * sample_rate)))
    n = x.size // win
    active = 0
    if n:
        w = x[: n * win].reshape(n, win)
        wr = np.sqrt((w * w).mean(axis=1))
        active = int(np.sum(20.0 * np.log10(np.maximum(wr, _FLOOR)) > ACTIVE_FLOOR_DBFS))
    return {"rms_dbfs": round(rms_db, 2), "active_s": round(active * ACTIVE_WINDOW_S, 2)}
