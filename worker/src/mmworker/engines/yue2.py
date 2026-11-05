"""The yue2 engine behind the adapter protocol (C154.1). Behaviour is unchanged from the M3 pipeline; yue2 and
torch are imported lazily, only inside load_yue2."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from pathlib import Path
from typing import TYPE_CHECKING, Any

from .. import env as envmod
from ..errors import InitError
from ..pipeline import Pipeline, Result, resolve_3b, resolve_vae

if TYPE_CHECKING:
    from ..contract import TakeInput

ENGINE = "yue2"


class Adapter:
    """Wraps a yue2 Pipeline (the real one or FakePipeline) as an engines.Adapter."""

    engine = ENGINE
    classes: frozenset[str] = frozenset()

    def __init__(self, pipe: Pipeline) -> None:
        self._pipe = pipe

    @property
    def weights(self) -> dict[str, str]:
        return dict(getattr(self._pipe, "weights", {}))

    def validate(self, request: dict[str, Any]) -> dict[str, Any]:
        return self._pipe.request_kwargs(request)

    def render(self, take: TakeInput, inputs: Mapping[str, Path], cancelled: Callable[[], bool], tmp: Path, *,
               params: dict[str, Any]) -> Result:
        return self._pipe(cancelled=cancelled, **params)

    def env_fingerprint(self, environ: Mapping[str, str]) -> dict[str, object]:
        return envmod.fingerprint(self.weights, environ)


def preflight(env: Mapping[str, str]) -> None:
    """The pinned revisions must be in the environment (before this, a missing one was a bare KeyError)."""
    for k in ("MM_YUE2_3B_REVISION", "MM_YUE2_VAE_REVISION"):
        if not env.get(k):
            raise InitError("ENV_INVALID", f"{k} is not set")


def load(env: dict[str, str]) -> Adapter:  # pragma: no cover - needs yue2, torch and weights
    return Adapter(load_yue2(env))


def load_yue2(env: dict[str, str]) -> Pipeline:  # pragma: no cover - needs yue2, torch and weights
    """Load the model once per worker process (called from serverless.main, outside the handler)."""
    import yue2  # noqa: PLC0415 - lazy by contract (test_yue2_imported_lazily)

    from ..env import WEIGHTS_3B, WEIGHTS_VAE  # noqa: PLC0415

    rev3b = env["MM_YUE2_3B_REVISION"]
    revvae = env["MM_YUE2_VAE_REVISION"]
    pipe = yue2.YuE2Pipeline.from_pretrained(
        model=resolve_3b(env, WEIGHTS_3B, rev3b), vae=resolve_vae(env, WEIGHTS_VAE, revvae), revision=rev3b,
        vae_revision=revvae, local_files_only=True, device="cuda")
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
