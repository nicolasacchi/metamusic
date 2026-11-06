"""The rendering seam. yue2 (and torch) are imported lazily, only inside load_yue2."""

from __future__ import annotations

import json
import struct
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from . import models


class Result(Protocol):
    truncated: Any

    def save_artifacts(self, directory: str | Path) -> None: ...


class Pipeline(Protocol):
    weights: dict[str, str]  # repo -> revision the pipeline was loaded with

    def request_kwargs(self, request: dict[str, Any]) -> dict[str, Any]:
        """yue2's own validation; raises ValueError on rejection."""

    def __call__(self, *, cancelled: Callable[[], bool], **kwargs: Any) -> Result: ...


SEMANTIC_TOKENS_PER_S = 25  # YuE2 semantic codec rate


def with_min_audio(kwargs: dict[str, Any], min_audio_s: int) -> dict[str, Any]:
    """The pipeline kwargs with yue2's own override for a forced minimum length: semantic_sampling.min_tokens
    (yue2.sampling.distribution suppresses the end-of-music token until that many tokens exist). The result is
    recorded by yue2 in config.json (generation.semantic, overrides.semantic), which is what makes the take
    re-runnable. A min_tokens the request itself set is kept when larger. ValueError on a malformed sampling."""
    if min_audio_s <= 0:
        return kwargs
    sampling = kwargs.get("semantic_sampling")
    if sampling is None:
        sampling = {}
    elif not isinstance(sampling, dict):
        raise ValueError("semantic_sampling must be an object")
    current = sampling.get("min_tokens", 0)
    if isinstance(current, bool) or not isinstance(current, int):
        raise ValueError("semantic_sampling.min_tokens must be an integer")
    out = dict(kwargs)
    out["semantic_sampling"] = {**sampling, "min_tokens": max(current, min_audio_s * SEMANTIC_TOKENS_PER_S)}
    return out


def resolve_vae(env: dict[str, str], repo: str, revision: str) -> str:
    """The local snapshot directory of the VAE at the pinned revision, from MM_YUE2_VAE_DIR.

    MM_YUE2_VAE_DIR is a Hugging Face hub cache (`models--ORG--NAME/snapshots/<revision>/`): the image's own
    (/opt/mm/hf-cache/hub, baked at build time and sha256-verified), RunPod's cached-model directory or a
    network volume. yue2 takes a local directory as the VAE source without touching the hub. Without the
    variable the repo id is returned and the 3B cache directory serves both. A missing snapshot is an error
    (fail closed: a worker must never render with unpinned weights)."""
    root = env.get("MM_YUE2_VAE_DIR")
    if not root:
        return repo
    snap = Path(root) / ("models--" + repo.replace("/", "--")) / "snapshots" / revision
    if not (snap / "config.json").is_file():
        raise FileNotFoundError(f"VAE snapshot for the pinned revision not found under MM_YUE2_VAE_DIR ({snap.name})")
    return str(snap)


# The names stay here (tests and the contract job patch/read pipeline.VOLUME_HUB); the walk itself is
# models.resolve_snapshot (C154.2), which resolve_3b feeds with these module globals at call time.
VOLUME_HUB = models.VOLUME_HUB
MODEL_STORE = models.MODEL_STORE


def resolve_3b(env: dict[str, str], repo: str, revision: str) -> str:
    """The local snapshot directory of the 3B model at the pinned revision (yue2 accepts a local directory).

    Tried in order: MM_YUE2_3B_DIR (a hub-cache root), RunPod's /runpod-volume/huggingface-cache/hub and
    RunPod's model store /runpod/model-store/huggingface/ORG/NAME/<revision>. Only the pinned revision is
    accepted. Nothing found raises FileNotFoundError with one line naming every path tried."""
    spec = models.ModelSpec(repo, revision, ("config.json", "*.safetensors"), env_var="MM_YUE2_3B_DIR",
                            label="3B weights")
    return str(models.resolve_snapshot(spec, env, volume_hub=VOLUME_HUB, model_store=MODEL_STORE,
                                       list_revisions=False))


def __getattr__(name: str) -> Any:
    """load_yue2 and _Yue2Adapter moved behind the engines protocol (engines/yue2.py); the old import path
    keeps working. Lazy, because engines.yue2 imports this module."""
    if name in {"load_yue2", "_Yue2Adapter"}:
        from .engines import yue2  # noqa: PLC0415

        return getattr(yue2, name)
    raise AttributeError(name)


# FakePipeline: tests and selftest (no soundfile, no torch).

def _flac_bytes(seconds: float = 1.0, rate: int = 8000) -> bytes:
    """A FLAC stream holding only STREAMINFO (sample rate, total samples): enough for the duration probe, no
    encoder needed. The unit tests that want real audio use soundfile."""
    n = int(seconds * rate)
    block = 4096
    info = struct.pack(">HH", block, block) + (0).to_bytes(3, "big") + (0).to_bytes(3, "big")
    packed = (rate << 44) | (15 << 36) | n  # rate(20) channels-1(3)=0 bps-1(5)=15 total(36)
    info += packed.to_bytes(8, "big") + bytes(16)
    out = b"fLaC" + bytes([0x80]) + len(info).to_bytes(3, "big") + info
    return out


def _fake_config(semantic_sampling: Any) -> bytes:
    """What yue2's effective_config records for a semantic_sampling override (generation + overrides)."""
    if not semantic_sampling:
        return b'{"fake":true}'
    sem = {"min_tokens": 200, "max_tokens": 9000, **semantic_sampling}
    return json.dumps({"fake": True, "generation": {"semantic": sem}, "overrides": {"semantic": sem}}).encode()


class _FakeResult:
    def __init__(self, truncated: Any, files: dict[str, bytes]) -> None:
        self.truncated = truncated
        self._files = files

    def save_artifacts(self, directory: str | Path) -> None:
        d = Path(directory)
        for name, data in self._files.items():
            (d / name).write_bytes(data)


class FakePipeline:
    """Deterministic stand-in. Options let tests inject truncation shapes, extra files, slow runs."""

    def __init__(self, *, weights: dict[str, str] | None = None, truncated: Any = None,
                 files: dict[str, bytes] | None = None, drop: tuple[str, ...] = (),
                 on_call: Callable[[dict[str, Any], Callable[[], bool]], None] | None = None,
                 error: BaseException | None = None) -> None:
        self.weights = dict(weights or {})
        self._truncated = {"abc": False, "semantic": False} if truncated is None else truncated
        self._files = files or {}
        self._drop = drop
        self._on_call = on_call
        self._error = error
        self.calls: list[dict[str, Any]] = []

    def request_kwargs(self, request: dict[str, Any]) -> dict[str, Any]:
        allowed = {"style", "lyrics", "cot", "seed", "id", "abc", "cfg_scale", "abc_sampling", "semantic_sampling"}
        extra = set(request) - allowed
        if extra:
            raise ValueError(f"unknown request key {sorted(extra)[0]}")
        for k in ("style", "lyrics", "cot", "seed", "id"):
            if k not in request:
                raise ValueError(f"missing {k}")
        return dict(request)

    def __call__(self, *, cancelled: Callable[[], bool], **kwargs: Any) -> _FakeResult:
        self.calls.append(kwargs)
        if self._on_call is not None:
            self._on_call(kwargs, cancelled)
        if self._error is not None:
            raise self._error
        files: dict[str, bytes] = {
            "request.json": b'{"fake":true}',
            "config.json": _fake_config(kwargs.get("semantic_sampling")),
            "result.json": b'{"fake":true}',
            "audio.flac": _flac_bytes(),
        }
        files.update(self._files)
        for name in self._drop:
            files.pop(name, None)
        return _FakeResult(self._truncated, files)
