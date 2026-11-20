"""CUDA 12 / cuDNN 9 library discovery for ctranslate2 (C331.1).

The nvidia-* wheels put their .so files under site-packages/nvidia/<pkg>/lib. ctranslate2 loads cuBLAS and
cuDNN with dlopen, so the directories are put on LD_LIBRARY_PATH for child processes and the libraries are
preloaded with ctypes in this one. Nothing here imports torch.
"""

from __future__ import annotations

import ctypes
import os
import sysconfig
from collections.abc import Iterable
from pathlib import Path

WANTED = ("libcublas.so.12", "libcublasLt.so.12", "libcudnn.so.9")


def site_roots() -> list[Path]:
    roots = []
    for key in ("purelib", "platlib"):
        p = sysconfig.get_paths().get(key)
        if p and Path(p) not in roots:
            roots.append(Path(p))
    return roots


def lib_dirs(roots: Iterable[Path] | None = None) -> list[Path]:
    """Every site-packages/nvidia/*/lib directory, sorted, deduplicated."""
    found: list[Path] = []
    for root in roots if roots is not None else site_roots():
        base = Path(root) / "nvidia"
        if not base.is_dir():
            continue
        for d in sorted(base.glob("*/lib")):
            if d.is_dir() and d not in found:
                found.append(d)
    return found


def library_path(roots: Iterable[Path] | None = None, existing: str = "") -> str:
    """LD_LIBRARY_PATH value: the nvidia lib dirs first, then the existing value."""
    parts = [str(d) for d in lib_dirs(roots)]
    if existing:
        parts.append(existing)
    return os.pathsep.join(parts)


def preload(roots: Iterable[Path] | None = None) -> list[str]:
    """dlopen the wanted libraries (RTLD_GLOBAL) found under the nvidia dirs; returns the loaded names."""
    loaded = []
    for d in lib_dirs(roots):
        for name in WANTED:
            p = d / name
            if p.exists():
                ctypes.CDLL(str(p), mode=ctypes.RTLD_GLOBAL)
                loaded.append(name)
    return loaded
