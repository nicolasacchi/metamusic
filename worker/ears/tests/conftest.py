"""Fakes only: no torch, no GPU, no model download."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest
import soundfile as sf

from mmears import __main__ as cli
from mmears import models

SCHEMA = Path(__file__).resolve().parents[2] / "src" / "mmworker" / "schema" / "mm-ears-b-1.schema.json"


@pytest.fixture(scope="session")
def schema() -> dict[str, Any]:
    return json.loads(SCHEMA.read_text())


@pytest.fixture
def validate(schema: dict[str, Any]):
    import jsonschema

    def check(doc: dict[str, Any]) -> None:
        jsonschema.Draft202012Validator(schema).validate(json.loads(json.dumps(doc)))

    return check


def make_models(root: Path, roles=("separator", "asr", "pitch"), license_: str = "MIT") -> tuple[Path, Path]:
    """Write tiny model files and a matching lock; return (lock path, models dir)."""
    entries = []
    for role in roles:
        body = f"{role}-weights".encode()
        d = root / "models" / role
        d.mkdir(parents=True, exist_ok=True)
        (d / "w.bin").write_bytes(body)
        entries.append(
            {
                "role": role,
                "name": f"fake-{role}",
                "source": {"repo": "example/fake", "revision": "r1"},
                "license": license_,
                "files": [{"path": "w.bin", "sha256": hashlib.sha256(body).hexdigest(), "bytes": len(body)}],
            }
        )
    lock = root / "models.lock.json"
    lock.write_text(json.dumps({"format": models.FORMAT, "models": entries}))
    return lock, root / "models"


@pytest.fixture
def model_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> tuple[Path, Path]:
    monkeypatch.setattr(models.tempfile, "gettempdir", lambda: str(tmp_path))
    return make_models(tmp_path)


def write_audio(path: Path, seconds: float = 3.0, sr: int = 16000, channels: int = 2) -> None:
    n = int(seconds * sr)
    t = np.arange(n) / sr
    x = (0.3 * np.sin(2 * np.pi * 220 * t)).astype(np.float32)
    data = np.stack([x] * channels, axis=1) if channels > 1 else x
    sf.write(str(path), data, sr, format=path.suffix[1:].upper())


@pytest.fixture
def flac(tmp_path: Path) -> Path:
    p = tmp_path / "in.flac"
    write_audio(p)
    return p


class FakeWord:
    def __init__(self, word: str, start: float, end: float, p: float = 0.9) -> None:
        self.word, self.start, self.end, self.probability = word, start, end, p


class FakeSeg:
    def __init__(self, start: float, end: float, words: list[FakeWord]) -> None:
        self.start, self.end, self.words = start, end, words
        self.avg_logprob, self.no_speech_prob = -0.3, 0.02


class FakeWhisper:
    """Records the transcribe kwargs; returns two segments of Italian-looking words."""

    calls: list[dict[str, Any]] = []

    def __init__(self, segs: list[FakeSeg] | None = None, language: str = "it") -> None:
        self.segs = segs if segs is not None else [
            FakeSeg(0.1, 1.5, [FakeWord(" la", 0.1, 0.4), FakeWord(" luna", 0.5, 1.0)]),
            FakeSeg(1.6, 2.5, [FakeWord(" sole", 1.6, 2.0)]),
        ]
        self.language = language

    def transcribe(self, audio: Any, **kw: Any):
        FakeWhisper.calls.append(kw)

        class Info:
            language = self.language

        return iter(self.segs), Info()


def fake_stages(*, fail: dict[str, Exception] | None = None, lang_seen: list[str] | None = None) -> cli.Stages:
    fail = fail or {}

    def sep(dec: Any, ctx: cli.Ctx) -> Any:
        if "separate" in fail:
            raise fail["separate"]
        ctx.add_load(0.1)
        return np.full(int(dec.duration_s * 16000), 0.1, dtype=np.float32)

    def asr_stage(v: Any, lang: str, ctx: cli.Ctx):
        if "asr" in fail:
            raise fail["asr"]
        if lang_seen is not None:
            lang_seen.append(lang)
        from mmears import asr

        return asr.transcribe(v, lang, Path("unused"), "cpu", ctx.add_load, lambda *a: FakeWhisper())

    def pitch_stage(v: Any, dur: float, ctx: cli.Ctx):
        if "transcribe" in fail:
            raise fail["transcribe"]
        n = int(dur / 0.01)
        return np.full(n, 440.0), np.full(n, 0.8)

    return cli.Stages(sep, asr_stage, pitch_stage, pick_device=lambda: "cpu")
