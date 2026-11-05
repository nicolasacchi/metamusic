"""Engine adapters (C154.1). One worker image serves one engine, chosen by MM_ENGINE (default yue2).

An engine module (`mmworker.engines.<name>`) provides:

    ENGINE: str                      the engine id, equal to the job input's `engine`
    preflight(env) -> None           optional; raises InitError in seconds, before any model I/O
    load(env) -> Adapter             once per process, outside the handler; raises InitError / FileNotFoundError

The modules are imported by name, only when selected, so an image never needs another engine's dependencies
and adding an engine is one entry in ENGINES (module `mmworker.engines.acestep` arrives with E2-PW2).
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from importlib import import_module
from pathlib import Path
from typing import TYPE_CHECKING, Any, Protocol

from ..errors import InitError

if TYPE_CHECKING:
    from ..contract import TakeInput
    from ..pipeline import Result

ENGINES: dict[str, str] = {
    "yue2": "mmworker.engines.yue2",
    "acestep15": "mmworker.engines.acestep",
}
DEFAULT_ENGINE = "yue2"
ENV_VAR = "MM_ENGINE"


class Adapter(Protocol):
    engine: str
    weights: dict[str, str]  # repo -> revision loaded; compared as a set with expect.weights
    classes: frozenset[str]  # task classes loaded (acestep15: turbo, base); empty for yue2

    def validate(self, request: dict[str, Any]) -> Any:
        """The engine's own request validation; returns opaque render parameters. ValueError means BAD_INPUT;
        a WorkerError (e.g. TASK_UNAVAILABLE) passes through unchanged."""

    def render(self, take: TakeInput, inputs: Mapping[str, Path], cancelled: Callable[[], bool], tmp: Path, *,
               params: Any) -> Result:
        """Render. `inputs` are the verified fetched files (name -> path); `params` is validate()'s result."""

    def env_fingerprint(self, environ: Mapping[str, str]) -> dict[str, object]:
        """The output schema's env object (engine id included)."""


def engine_name(env: Mapping[str, str]) -> str:
    name = env.get(ENV_VAR) or DEFAULT_ENGINE
    if name not in ENGINES:
        raise InitError("ENV_INVALID", f"{ENV_VAR}={name!r} is not a known engine ({', '.join(sorted(ENGINES))})")
    return name


def _module(name: str) -> Any:
    try:
        return import_module(ENGINES[name])
    except ModuleNotFoundError as e:
        if e.name != ENGINES[name]:
            raise  # a missing dependency of the adapter: reported as a load failure by the caller
        raise InitError("ENV_INVALID", f"engine {name!r} is not available in this worker image") from None


def preflight(env: Mapping[str, str]) -> str:
    """Select the engine and run its cheap checks; returns the engine id."""
    name = engine_name(env)
    check = getattr(_module(name), "preflight", None)
    if check is not None:
        check(env)
    return name


def load(env: Mapping[str, str]) -> Adapter:
    """The default loader of serverless.main: preflight, then load the selected engine's adapter."""
    name = preflight(env)
    adapter: Adapter = _module(name).load(dict(env))
    return adapter


def adapt(pipe: Any) -> Adapter:
    """An Adapter for `pipe`: already one, or a bare yue2 Pipeline (request_kwargs + __call__) wrapped."""
    if hasattr(pipe, "render") and hasattr(pipe, "validate") and hasattr(pipe, "env_fingerprint"):
        return pipe  # type: ignore[no-any-return]
    from . import yue2  # noqa: PLC0415

    return yue2.Adapter(pipe)
