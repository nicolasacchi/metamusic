"""Demucs htdemucs vocal separation (C331.3). torch, demucs and julius are imported lazily."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import numpy as np

from .audio import Decoded
from .errors import EarsError

DEMUCS_RATE = 44100
ASR_RATE = 16000


def separate(dec: Decoded, model_dir: Path, device: str, add_load: Any) -> Any:
    """Return the vocals stem as mono float32 at 16 kHz. Audio stays in memory (no torchaudio I/O)."""
    import time

    import julius
    import torch
    from demucs.apply import apply_model
    from demucs.states import load_model

    t0 = time.monotonic()
    ckpts = sorted(Path(model_dir).glob("*.th"))
    if len(ckpts) != 1:
        raise EarsError("MODELS_MISSING", "separator needs exactly one .th checkpoint")
    pkg = torch.load(ckpts[0], map_location="cpu", weights_only=False)  # trusted: sha256-pinned lock
    model = load_model(pkg).to(device).eval()
    add_load(time.monotonic() - t0)

    wav = torch.from_numpy(np.ascontiguousarray(dec.samples.T))  # (channels, frames)
    if wav.shape[0] == 1:
        wav = wav.repeat(2, 1)
    wav = wav[:2]
    if dec.sample_rate != DEMUCS_RATE:
        wav = julius.resample_frac(wav, dec.sample_rate, DEMUCS_RATE)
    ref = wav.mean(0)
    norm = (wav - ref.mean()) / (ref.std() + 1e-8)
    with torch.no_grad():
        out = apply_model(model, norm[None], device=device, split=True, overlap=0.25, shifts=0)[0]
    vocals = out[model.sources.index("vocals")] * (ref.std() + 1e-8) + ref.mean()
    mono = julius.resample_frac(vocals.mean(0, keepdim=True), DEMUCS_RATE, ASR_RATE)[0]
    return mono.detach().cpu().numpy().astype(np.float32)
