"""The environment fingerprint of C73.5 ("env") and the audio duration probe."""

from __future__ import annotations

import os
import platform
import sys
from collections.abc import Mapping
from importlib import metadata
from pathlib import Path

WEIGHTS_3B = "m-a-p/YuE2-3B"
WEIGHTS_VAE = "m-a-p/YuE2-Vae"
ENGINE = "yue2"

ENV_KEYS = ("engine", "pkg_version", "code_commit", "decoder", "gpu_name", "cuda", "torch", "worker_build",
            "python", "weights")


def _pkg_version() -> str:
    try:
        return metadata.version("yue2-infer")
    except metadata.PackageNotFoundError:
        return "unknown"


def fingerprint(weights: Mapping[str, str], environ: Mapping[str, str] | None = None) -> dict[str, object]:
    """Every key of the output schema's env object. torch is only inspected when something already imported
    it (the real pipeline); the worker itself never imports torch."""
    e = os.environ if environ is None else environ
    torch = sys.modules.get("torch")
    gpu, cuda, tver = "none", "none", "none"
    if torch is not None:  # pragma: no cover - real GPU path
        tver = str(getattr(torch, "__version__", "unknown"))
        cuda = str(getattr(getattr(torch, "version", None), "cuda", None) or "none")
        try:
            if torch.cuda.is_available():
                gpu = str(torch.cuda.get_device_name(0))
        except Exception:
            gpu = "unknown"
    return {
        "engine": ENGINE,
        "pkg_version": _pkg_version(),
        "code_commit": e.get("MM_YUE2_COMMIT", "unknown"),
        "weights": dict(weights),
        "decoder": WEIGHTS_VAE,
        "gpu_name": gpu,
        "cuda": cuda,
        "torch": tver,
        "worker_build": e.get("MM_WORKER_BUILD", "unknown"),
        "python": platform.python_version(),
    }


def flac_seconds(path: Path) -> float | None:
    """Duration from the FLAC STREAMINFO block; None when it cannot be read."""
    try:
        with open(path, "rb") as f:
            head = f.read(42)
    except OSError:
        return None
    if len(head) < 42 or head[:4] != b"fLaC" or head[4] & 0x7F != 0:
        return None
    packed = int.from_bytes(head[18:26], "big")
    rate = packed >> 44
    total = packed & ((1 << 36) - 1)
    if rate == 0 or total == 0:
        return None
    return total / rate
