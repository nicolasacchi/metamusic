"""Device and VRAM facts. torch is only consulted when a stage already imported it (lazy, C331.1)."""

from __future__ import annotations

import sys


def pick_device() -> str:
    """'cuda' when torch is importable and sees a GPU, else 'cpu'. Imports torch on demand."""
    try:
        import torch

        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


def snapshot(device: str) -> tuple[str, int]:
    """(device name, peak VRAM MiB). Never imports torch itself."""
    torch = sys.modules.get("torch")
    if torch is None or device != "cuda":
        return ("cpu" if device != "cuda" else "cuda"), 0
    try:
        name = str(torch.cuda.get_device_name(0))[:128]
        peak = int(torch.cuda.max_memory_allocated() // (1 << 20))
        return name, max(0, peak)
    except Exception:
        return "cuda", 0


def free_cache() -> None:
    """Release cached CUDA memory between stages, if torch is loaded."""
    torch = sys.modules.get("torch")
    if torch is not None:
        try:
            torch.cuda.empty_cache()
        except Exception:
            pass
