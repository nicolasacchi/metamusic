"""The rendering seam. yue2 (and torch) are imported lazily, only inside load_yue2."""

from __future__ import annotations

import struct
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol


class Result(Protocol):
    truncated: Any

    def save_artifacts(self, directory: str | Path) -> None: ...


class Pipeline(Protocol):
    weights: dict[str, str]  # repo -> revision the pipeline was loaded with

    def request_kwargs(self, request: dict[str, Any]) -> dict[str, Any]:
        """yue2's own validation; raises ValueError on rejection."""

    def __call__(self, *, cancelled: Callable[[], bool], **kwargs: Any) -> Result: ...


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


def load_yue2(env: dict[str, str]) -> Pipeline:  # pragma: no cover - needs yue2, torch and weights
    """Load the model once per worker process (called from serverless.main, outside the handler)."""
    import yue2  # noqa: PLC0415 - lazy by contract (test_yue2_imported_lazily)

    from .env import WEIGHTS_3B, WEIGHTS_VAE  # noqa: PLC0415

    rev3b = env["MM_YUE2_3B_REVISION"]
    revvae = env["MM_YUE2_VAE_REVISION"]
    cache = env.get("HF_HOME") or env.get("MM_MODEL_CACHE")
    pipe = yue2.YuE2Pipeline.from_pretrained(
        model=WEIGHTS_3B, vae=resolve_vae(env, WEIGHTS_VAE, revvae), revision=rev3b, vae_revision=revvae,
        local_files_only=True, device="cuda", cache_dir=cache)
    return _Yue2Adapter(pipe, {WEIGHTS_3B: rev3b, WEIGHTS_VAE: revvae})


class _Yue2Adapter:  # pragma: no cover - exercised by the contract job and the canary
    def __init__(self, pipe: Any, weights: dict[str, str]) -> None:
        self._pipe = pipe
        self.weights = weights

    def request_kwargs(self, request: dict[str, Any]) -> dict[str, Any]:
        # request_kwargs lives in yue2.cli (it is what `yue2 batch` calls); SongRequest construction inside
        # _request is what actually validates the values, and it does not touch self (contract-tested).
        from pathlib import Path  # noqa: PLC0415

        from yue2.cli import request_kwargs  # noqa: PLC0415
        from yue2.pipeline import YuE2Pipeline  # noqa: PLC0415

        kwargs = dict(request_kwargs(request, Path("/nonexistent-request-dir")))
        YuE2Pipeline._request(None, **{k: v for k, v in kwargs.items()
                                       if k not in {"abc_sampling", "semantic_sampling"}})
        return kwargs

    def __call__(self, *, cancelled: Callable[[], bool], **kwargs: Any) -> Result:
        return self._pipe(cancelled=cancelled, **kwargs)


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
        allowed = {"style", "lyrics", "cot", "seed", "id", "abc", "cfg_scale"}
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
            "config.json": b'{"fake":true}',
            "result.json": b'{"fake":true}',
            "audio.flac": _flac_bytes(),
        }
        files.update(self._files)
        for name in self._drop:
            files.pop(name, None)
        return _FakeResult(self._truncated, files)
