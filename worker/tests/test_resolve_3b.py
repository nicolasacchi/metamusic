"""resolve_3b: the 3B snapshot directory for the pinned revision, from fake directory trees."""

from __future__ import annotations

from pathlib import Path

import pytest

from mmworker import pipeline, serverless
from mmworker.pipeline import resolve_3b

REPO = "m-a-p/YuE2-3B"
REV = "8" * 40


def snapshot(d: Path, *, config: bool = True, weights: bool = True) -> Path:
    d.mkdir(parents=True)
    if config:
        (d / "config.json").write_text("{}")
    if weights:
        (d / "model-00001-of-00002.safetensors").write_bytes(b"w")
    return d


@pytest.fixture
def roots(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> tuple[Path, Path]:
    vol = tmp_path / "runpod-volume" / "huggingface-cache" / "hub"
    store = tmp_path / "model-store" / "huggingface"
    monkeypatch.setattr(pipeline, "VOLUME_HUB", vol)
    monkeypatch.setattr(pipeline, "MODEL_STORE", store)
    return vol, store


def hub(root: Path, rev: str = REV) -> Path:
    return root / "models--m-a-p--YuE2-3B" / "snapshots" / rev


def test_env_dir_first(roots: tuple[Path, Path], tmp_path: Path) -> None:
    snap = snapshot(hub(tmp_path / "custom"))
    snapshot(hub(roots[0]))
    assert resolve_3b({"MM_YUE2_3B_DIR": str(tmp_path / "custom")}, REPO, REV) == str(snap)


def test_runpod_volume_layout(roots: tuple[Path, Path]) -> None:
    snap = snapshot(hub(roots[0]))
    assert resolve_3b({}, REPO, REV) == str(snap)


def test_model_store_layout(roots: tuple[Path, Path]) -> None:
    snap = snapshot(roots[1] / "m-a-p" / "YuE2-3B" / REV)
    assert resolve_3b({}, REPO, REV) == str(snap)


def test_env_dir_without_snapshot_falls_through(roots: tuple[Path, Path], tmp_path: Path) -> None:
    snap = snapshot(hub(roots[0]))
    assert resolve_3b({"MM_YUE2_3B_DIR": str(tmp_path / "empty")}, REPO, REV) == str(snap)


def test_wrong_revision_is_not_accepted(roots: tuple[Path, Path]) -> None:
    snapshot(hub(roots[0], "d" * 40))
    snapshot(roots[1] / "m-a-p" / "YuE2-3B" / ("e" * 40))
    with pytest.raises(FileNotFoundError):
        resolve_3b({}, REPO, REV)


@pytest.mark.parametrize("kw", [{"config": False}, {"weights": False}])
def test_incomplete_snapshot_is_skipped(roots: tuple[Path, Path], kw: dict[str, bool]) -> None:
    snapshot(hub(roots[0]), **kw)
    with pytest.raises(FileNotFoundError, match="config.json|safetensors"):
        resolve_3b({}, REPO, REV)


def test_nothing_present_names_every_path(roots: tuple[Path, Path], tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError) as ei:
        resolve_3b({"MM_YUE2_3B_DIR": str(tmp_path / "custom")}, REPO, REV)
    msg = str(ei.value)
    assert "\n" not in msg
    for p in (hub(tmp_path / "custom"), hub(roots[0]), roots[1] / "m-a-p" / "YuE2-3B" / REV):
        assert str(p) in msg


def test_main_fails_fast_with_one_line(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> object:
        raise FileNotFoundError("3B snapshot not found; tried /a, /b")

    ran: list[int] = []
    code = serverless.main({"HF_HUB_OFFLINE": "1"}, loader, lambda h: ran.append(1))  # type: ignore[arg-type]
    err = capsys.readouterr().err.strip().splitlines()
    assert code == 1 and not ran
    assert len(err) == 1 and "/a, /b" in err[0]
