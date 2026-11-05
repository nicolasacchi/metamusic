"""Explicit model-path resolution (C154.2). The worker never asks the Hugging Face API where a model is and never
assumes HF_HOME: RunPod's cached models live at paths of their own, so every snapshot directory is found here,
at the pinned revision only, and a miss is an error (never a download)."""

from __future__ import annotations

import sys
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path

from . import redact
from .errors import InitError

VOLUME_HUB = Path("/runpod-volume/huggingface-cache/hub")  # RunPod cached model, hub-cache layout
MODEL_STORE = Path("/runpod/model-store/huggingface")  # RunPod cached model, ORG/NAME/<revision> layout


@dataclass(frozen=True)
class ModelSpec:
    repo: str  # "ACE-Step/Ace-Step1.5"
    revision: str  # 40-hex pinned sha
    required: tuple[str, ...] = ("config.json", "*.safetensors")  # completeness manifest: paths or globs
    env_var: str = ""  # optional hub-cache root override, tried first (e.g. MM_ACESTEP_MODELS_DIR)
    label: str = "model"  # the first words of a "not found" message


def candidates(spec: ModelSpec, env: Mapping[str, str], *, volume_hub: Path | None = None,
               model_store: Path | None = None) -> list[Path]:
    """Directories to try, in order: the env override (a hub-cache root), RunPod's volume hub, RunPod's model store."""
    hub_name = "models--" + spec.repo.replace("/", "--")
    out: list[Path] = []
    root = env.get(spec.env_var) if spec.env_var else None
    if root:
        out.append(Path(root) / hub_name / "snapshots" / spec.revision)
    out.append((VOLUME_HUB if volume_hub is None else volume_hub) / hub_name / "snapshots" / spec.revision)
    out.append((MODEL_STORE if model_store is None else model_store) / spec.repo / spec.revision)
    return out


def _unsatisfied(snap: Path, required: Sequence[str]) -> str | None:
    """Why a candidate is unusable, or None when every manifest entry exists. Symlinks (hub snapshots point into
    blobs) are followed; a partial directory is never partially used."""
    if not snap.is_dir():
        return "absent"
    for pattern in required:
        if any(c.exists() for c in snap.glob(pattern)):
            continue
        return f"no {pattern}"
    return None


def _revisions_present(spec: ModelSpec, paths: Sequence[Path]) -> list[str]:
    seen: set[str] = set()
    for p in paths:
        parent = p.parent  # .../snapshots (hub layout) or .../ORG/NAME (model store)
        try:
            seen.update(c.name for c in parent.iterdir() if c.is_dir() and c.name != spec.revision)
        except OSError:
            continue
    return sorted(seen)


def resolve_snapshot(spec: ModelSpec, env: Mapping[str, str], *, volume_hub: Path | None = None,
                     model_store: Path | None = None, list_revisions: bool = True) -> Path:
    """The first candidate whose manifest is satisfied. Only the pinned revision is accepted; nothing found raises
    FileNotFoundError with one line naming every path tried (and the other revisions present, never used)."""
    paths = candidates(spec, env, volume_hub=volume_hub, model_store=model_store)
    tried: list[str] = []
    for snap in paths:
        why = _unsatisfied(snap, spec.required)
        if why is None:
            return snap
        tried.append(f"{snap} ({why})")
    msg = f"{spec.label} {spec.repo}@{spec.revision[:12]} not found; tried: " + "; ".join(tried)
    if list_revisions:
        present = _revisions_present(spec, paths)
        if present:
            msg += "; revisions present (not used): " + ", ".join(present)
    raise FileNotFoundError(msg)


def resolve_all(specs: Sequence[ModelSpec], env: Mapping[str, str]) -> dict[str, Path]:
    """Every spec or none: the error names every missing model, not only the first."""
    found: dict[str, Path] = {}
    missing: list[str] = []
    for spec in specs:
        try:
            found[spec.repo] = resolve_snapshot(spec, env)
        except FileNotFoundError as e:
            missing.append(str(e))
    if missing:
        raise FileNotFoundError(" | ".join(missing))
    return found


def exit_on_missing(specs: Sequence[ModelSpec], env: Mapping[str, str]) -> dict[str, Path]:
    """resolve_all, but a miss prints the one init-failed line and exits 1 (for adapters resolving at import time)."""
    try:
        return resolve_all(specs, env)
    except FileNotFoundError as e:
        print(init_line(InitError("MODELS_MISSING", str(e))), file=sys.stderr)
        raise SystemExit(1) from None


def init_line(e: InitError) -> str:
    """`mmworker: init failed: <CODE>: <redacted message>`, always one line."""
    return f"mmworker: init failed: {e.code}: {' '.join(redact.text(e.message).split())}"
