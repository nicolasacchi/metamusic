"""The stage registry (C321.7, C322.2): optional worker stages that a later lane adds as one module each.

A stage module (`mmworker/stages/<name>.py`) calls `register(...)` at import. `discover()` imports every module
of this package, so adding a stage never edits this file. A module whose imports fail (the models or libraries are
not in this image) registers nothing, and the capability stays closed: a stage's cap exists only when its code and
its models are present.

Stage names are the capability names: `ears` (Tier B on a render), `analyze` (kind analyze), `plan` (kind plan).
All of them receive a StageContext and write their files into `ctx.tmp`; the handler collects them against the
kind's allowlist, so a stage never uploads anything itself.
"""

from __future__ import annotations

import importlib
import pkgutil
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ..contract import EARS_STAGES, TakeInput

EARS, ANALYZE, PLAN = "ears", "analyze", "plan"
NAMES = (EARS, ANALYZE, PLAN)


@dataclass(frozen=True)
class StageContext:
    take: TakeInput
    tmp: Path  # the work dir: stages write their artifacts here under their allowlisted names
    inputs: Mapping[str, Path]  # verified fetched inputs (analyze: src_audio)
    attach: Mapping[str, Path]  # verified attachment files (the work dir's attach/ subdirectory)
    cancelled: Callable[[], bool]
    clock: Callable[[], float]
    timeout_s: float  # wall budget of this stage (ears: min(ears.budget_s, time to the ears deadline))
    adapter: Any = None  # the loaded engine adapter (plan stages render with it); None for ears and analyze
    params: Any = None  # adapter.validate(request) (plan)
    audio: Path | None = None  # the audio to analyze: the render's audio.flac (ears) or the fetched input (analyze)


@dataclass(frozen=True)
class EarsOutcome:
    """Result of an ears or analyze stage. status is ok | error | skipped; code names an error (pattern A-Z0-9_)."""

    status: str
    code: str | None = None
    seconds: float = 0.0
    audio_seconds: float | None = None  # analyze: the input's duration when the stage knows it


@dataclass(frozen=True)
class PlanOutcome:
    """One seed of the plan kind. An ok seed has written plan-NN.tar (NN = index) into ctx.tmp."""

    index: int
    seed: int
    status: str  # ok | error
    code: str | None = None
    truncated: Mapping[str, bool] | None = None


Stage = Callable[[StageContext], Any]


@dataclass(frozen=True)
class Registration:
    name: str
    run: Stage
    ears_stages: frozenset[str] = field(default_factory=lambda: frozenset(EARS_STAGES))


_REGISTRY: dict[str, Registration] = {}


def register(name: str, run: Stage, *, ears_stages: frozenset[str] | None = None) -> None:
    """Register the stage `name`. `ears` and `analyze` return an EarsOutcome, `plan` a list of PlanOutcome.
    `ears_stages` is the subset of separate/asr/transcribe the stage can run (default all)."""
    if name not in NAMES:
        raise ValueError(f"unknown stage {name!r}")
    if name in _REGISTRY:
        raise ValueError(f"stage {name!r} is registered twice")
    _REGISTRY[name] = Registration(name, run, ears_stages if ears_stages is not None else frozenset(EARS_STAGES))


def get(name: str) -> Registration | None:
    return _REGISTRY.get(name)


def caps() -> list[str]:
    """The capability of every registered stage (cap name = stage name)."""
    return [n for n in NAMES if n in _REGISTRY]


def discover() -> None:
    """Import every stage module of this package; a module that cannot import registers nothing."""
    for m in pkgutil.iter_modules(__path__):
        if m.name.startswith("_"):
            continue
        try:
            importlib.import_module(f"{__name__}.{m.name}")
        except ImportError:
            continue
