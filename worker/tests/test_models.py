"""models.resolve_snapshot / resolve_all (C154.2) against fake directory trees. No network, no GPU."""

from __future__ import annotations

import socket
from pathlib import Path

import pytest

from mmworker import models
from mmworker.models import ModelSpec, resolve_all, resolve_snapshot

REV = "c" * 40
MAIN = ("vae/config.json", "acestep-v15-turbo/config.json", "acestep-v15-turbo/model.safetensors")


def spec(repo: str = "Org/Main", rev: str = REV, required: tuple[str, ...] = MAIN, **kw: str) -> ModelSpec:
    return ModelSpec(repo, rev, required, **kw)


def build(root: Path, files: tuple[str, ...] = MAIN) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    for f in files:
        p = root / f
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text("x")
    return root


def hub(root: Path, repo: str = "Org/Main", rev: str = REV) -> Path:
    return root / ("models--" + repo.replace("/", "--")) / "snapshots" / rev


@pytest.fixture
def roots(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> tuple[Path, Path]:
    vol = tmp_path / "vol" / "hub"
    store = tmp_path / "store" / "huggingface"
    monkeypatch.setattr(models, "VOLUME_HUB", vol)
    monkeypatch.setattr(models, "MODEL_STORE", store)
    return vol, store


def test_resolve_hub_layout(roots: tuple[Path, Path]) -> None:
    snap = build(hub(roots[0]))
    assert resolve_snapshot(spec(), {}) == snap


def test_resolve_model_store_layout(roots: tuple[Path, Path]) -> None:
    snap = build(roots[1] / "Org" / "Main" / REV)
    assert resolve_snapshot(spec(), {}) == snap


def test_resolve_env_override_first(roots: tuple[Path, Path], tmp_path: Path) -> None:
    snap = build(hub(tmp_path / "custom"))
    build(hub(roots[0]))
    assert resolve_snapshot(spec(env_var="MM_X_DIR"), {"MM_X_DIR": str(tmp_path / "custom")}) == snap
    # without the variable set the override is not consulted
    assert resolve_snapshot(spec(env_var="MM_X_DIR"), {}) == hub(roots[0])


def test_resolve_pinned_revision_only(roots: tuple[Path, Path]) -> None:
    build(hub(roots[0], rev="d" * 40))
    with pytest.raises(FileNotFoundError) as ei:
        resolve_snapshot(spec(), {})
    msg = str(ei.value)
    assert "revisions present (not used): " + "d" * 40 in msg
    assert "absent" in msg


def test_resolve_partial_snapshot_rejected(roots: tuple[Path, Path]) -> None:
    build(hub(roots[0]), MAIN[:2])  # the DiT safetensors are missing
    with pytest.raises(FileNotFoundError, match=r"no acestep-v15-turbo/model\.safetensors"):
        resolve_snapshot(spec(), {})


def test_resolve_glob_manifest(roots: tuple[Path, Path]) -> None:
    s = spec(required=("config.json", "*.safetensors"))
    build(hub(roots[0]), ("config.json",))
    with pytest.raises(FileNotFoundError, match=r"no \*\.safetensors"):
        resolve_snapshot(s, {})
    build(hub(roots[0]), ("config.json", "w-00001.safetensors"))
    assert resolve_snapshot(s, {}) == hub(roots[0])


def test_resolve_message_names_every_path(roots: tuple[Path, Path], tmp_path: Path) -> None:
    custom = tmp_path / "custom"
    build(hub(roots[0]), MAIN[:1])  # partial
    s = spec(env_var="MM_X_DIR", label="main bundle")
    with pytest.raises(FileNotFoundError) as ei:
        resolve_snapshot(s, {"MM_X_DIR": str(custom)})
    want = (f"main bundle Org/Main@{REV[:12]} not found; tried: {hub(custom)} (absent); "
            f"{hub(roots[0])} (no acestep-v15-turbo/config.json); {roots[1] / 'Org' / 'Main' / REV} (absent)")
    assert str(ei.value) == want
    assert "\n" not in want


def test_resolve_all_names_every_missing_model(roots: tuple[Path, Path]) -> None:
    build(hub(roots[0], "Org/Have"), MAIN)
    specs = [spec("Org/Have"), spec("Org/Gone1"), spec("Org/Gone2")]
    with pytest.raises(FileNotFoundError) as ei:
        resolve_all(specs, {})
    msg = str(ei.value)
    assert "Org/Gone1" in msg and "Org/Gone2" in msg and "Org/Have" not in msg


def test_resolve_all_returns_every_snapshot(roots: tuple[Path, Path]) -> None:
    a = build(hub(roots[0], "Org/A"))
    b = build(roots[1] / "Org" / "B" / REV)
    assert resolve_all([spec("Org/A"), spec("Org/B")], {}) == {"Org/A": a, "Org/B": b}


def test_resolve_never_downloads(roots: tuple[Path, Path], monkeypatch: pytest.MonkeyPatch) -> None:
    def boom(*a: object, **k: object) -> None:
        raise AssertionError("socket opened while resolving")

    monkeypatch.setattr(socket, "socket", boom)
    build(hub(roots[0]))
    resolve_snapshot(spec(), {})
    with pytest.raises(FileNotFoundError):
        resolve_snapshot(spec("Org/Nope"), {})


def test_resolve_symlinked_snapshot_ok(roots: tuple[Path, Path], tmp_path: Path) -> None:
    blobs = build(tmp_path / "blobs")
    snap = hub(roots[0])
    snap.mkdir(parents=True)
    for f in MAIN:  # hub snapshots are symlinks into blobs
        (snap / f).parent.mkdir(parents=True, exist_ok=True)
        (snap / f).symlink_to(blobs / f)
    assert resolve_snapshot(spec(), {}) == snap


def test_resolve_dangling_symlink_is_missing(roots: tuple[Path, Path], tmp_path: Path) -> None:
    snap = build(hub(roots[0]), MAIN[:2])
    (snap / MAIN[2]).symlink_to(tmp_path / "no-such-blob")
    with pytest.raises(FileNotFoundError, match="safetensors"):
        resolve_snapshot(spec(), {})


def test_exit_on_missing_prints_one_line(roots: tuple[Path, Path], capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as ei:
        models.exit_on_missing([spec("Org/Nope")], {})
    err = capsys.readouterr().err.strip().splitlines()
    assert ei.value.code == 1 and len(err) == 1
    assert err[0].startswith("mmworker: init failed: MODELS_MISSING: ")


def test_init_line_redacts_urls() -> None:
    from mmworker.errors import InitError

    line = models.init_line(InitError("LOAD_ERROR", "GET https://h.example.com/x?sig=abc&exp=1\nfailed"))
    assert "sig=" not in line and "\n" not in line and "<redacted>" in line
