"""Checkpoint tree, manifests, preflight, memory budget and the params copy (C154.2, C154.4, C154.5)."""

from __future__ import annotations

import copy
import hashlib
import json
import os
from pathlib import Path
from typing import Any

import pytest

from mmworker import engines, models, serverless
from mmworker.engines import acestep as ace
from mmworker.errors import InitError

REPO_ROOT = Path(__file__).resolve().parents[2]
MAIN_REV, BASE_REV = "a" * 40, "b" * 40


def touch_all(root: Path, paths: list[str]) -> None:
    for p in paths:
        f = root / p
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_bytes(b"x")


def fake_snapshots(tmp: Path) -> dict[str, Path]:
    """Hub-style snapshots built from the manifests only, files being symlinks into a blob dir as a real hub is."""
    man = ace.layout()["manifests"]
    out = {}
    for repo, key in ((ace.REPO_MAIN, "main"), (ace.REPO_BASE, "base")):
        snap = tmp / "hub" / ("models--" + repo.replace("/", "--")) / "snapshots" / (MAIN_REV if key == "main" else BASE_REV)
        blobs = tmp / "blobs" / key
        touch_all(blobs, man[key])
        for rel in man[key]:
            (snap / rel).parent.mkdir(parents=True, exist_ok=True)
            (snap / rel).symlink_to(blobs / rel)
        out[repo] = snap
    return out


def fake_code(tmp: Path) -> Path:
    code = tmp / "acestep" / "models"
    for variant, files in (("turbo", ["configuration_acestep_v15.py", "modeling_acestep_v15_turbo.py"]),
                           ("base", ["apg_guidance.py", "configuration_acestep_v15.py", "modeling_acestep_v15_base.py"])):
        (code / variant).mkdir(parents=True)
        (code / variant / "__init__.py").write_text("")
        for f in files:
            (code / variant / f).write_text(f"# {variant} {f}\n")
    return code


def test_acestep_layout_symlinks(tmp_path: Path) -> None:
    snaps, code = fake_snapshots(tmp_path), fake_code(tmp_path)
    root = tmp_path / "ckpt"
    made = ace.build_checkpoints(root, snaps, ("turbo", "base"), code)
    assert made == ["vae", "Qwen3-Embedding-0.6B", "acestep-5Hz-lm-1.7B", "acestep-v15-turbo", "acestep-v15-base"]
    main = snaps[ace.REPO_MAIN]
    for name in ("vae", "Qwen3-Embedding-0.6B", "acestep-5Hz-lm-1.7B"):
        assert (root / name).is_symlink() and os.readlink(root / name) == str(main / name)
    for cls, src in (("acestep-v15-turbo", main / "acestep-v15-turbo"), ("acestep-v15-base", snaps[ace.REPO_BASE])):
        d = root / cls
        assert d.is_dir() and not d.is_symlink()
        for f in ("config.json", "model.safetensors", "silence_latent.pt"):
            assert (d / f).is_symlink() and os.readlink(d / f) == str(src / f)
    # nothing but the pinned entries
    assert sorted(p.name for p in root.iterdir()) == sorted(made)


def test_acestep_layout_turbo_only_has_no_base_dir(tmp_path: Path) -> None:
    root = tmp_path / "ckpt"
    ace.build_checkpoints(root, {ace.REPO_MAIN: fake_snapshots(tmp_path)[ace.REPO_MAIN]}, ("turbo",), fake_code(tmp_path))
    assert not (root / "acestep-v15-base").exists()


def test_acestep_layout_code_presynced(tmp_path: Path) -> None:
    """initialize_service copies the pinned code over the DiT dir when it differs: it must find nothing to write."""
    snaps, code = fake_snapshots(tmp_path), fake_code(tmp_path)
    root = tmp_path / "ckpt"
    ace.build_checkpoints(root, snaps, ("turbo", "base"), code)
    for variant, d in (("turbo", "acestep-v15-turbo"), ("base", "acestep-v15-base")):
        src_files = sorted(p for p in (code / variant).glob("*.py") if p.name != "__init__.py")
        assert src_files
        for p in src_files:  # upstream _check_code_mismatch: hash(src) == hash(dst)
            dst = root / d / p.name
            assert dst.is_file() and not dst.is_symlink()
            assert hashlib.sha256(dst.read_bytes()).digest() == hashlib.sha256(p.read_bytes()).digest()
        assert not (root / d / "__init__.py").exists()
    # the code lives in the real directory, not behind a symlink into the (read-only) snapshot
    assert (root / "acestep-v15-base" / "modeling_acestep_v15_base.py").resolve().parent == (root / "acestep-v15-base").resolve()


def test_acestep_layout_missing_dir(tmp_path: Path) -> None:
    snaps, code = fake_snapshots(tmp_path), fake_code(tmp_path)
    (snaps[ace.REPO_MAIN] / "vae").rename(snaps[ace.REPO_MAIN] / "vae-gone")
    with pytest.raises(FileNotFoundError, match="checkpoint layout target is missing"):
        ace.build_checkpoints(tmp_path / "ckpt", snaps, ("turbo",), code)


def test_acestep_layout_missing_dit_file(tmp_path: Path) -> None:
    snaps, code = fake_snapshots(tmp_path), fake_code(tmp_path)
    (snaps[ace.REPO_BASE] / "silence_latent.pt").unlink()
    with pytest.raises(FileNotFoundError, match="silence_latent.pt"):
        ace.build_checkpoints(tmp_path / "ckpt", snaps, ("turbo", "base"), code)


def test_acestep_layout_missing_code(tmp_path: Path) -> None:
    snaps = fake_snapshots(tmp_path)
    with pytest.raises(RuntimeError, match="no model code for variant turbo"):
        ace.build_checkpoints(tmp_path / "ckpt", snaps, ("turbo",), tmp_path / "nothing")


def test_acestep_layout_is_rebuilt_not_appended(tmp_path: Path) -> None:
    snaps, code = fake_snapshots(tmp_path), fake_code(tmp_path)
    root = tmp_path / "ckpt"
    ace.build_checkpoints(root, snaps, ("turbo", "base"), code)
    ace.build_checkpoints(root, snaps, ("turbo",), code)
    assert not (root / "acestep-v15-base").exists()


def test_acestep_manifest_matches_layout() -> None:
    lay = ace.layout()
    main = set(lay["manifests"]["main"])
    for name in lay["main_links"]:
        assert any(m.startswith(name + "/") for m in main), name  # every linked directory is in the manifest
    for cls, spec in lay["dits"].items():
        listed = set(lay["manifests"]["base"]) if spec["repo"] == "base" else main
        for f in spec["files"]:
            assert (f"{spec['src']}/{f}" if spec["src"] else f) in listed, (cls, f)
    # the manifest names the files the loaders open that the plan's first list missed
    for must in ("vae/diffusion_pytorch_model.safetensors", "Qwen3-Embedding-0.6B/model.safetensors",
                 "Qwen3-Embedding-0.6B/tokenizer.json", "acestep-v15-turbo/silence_latent.pt",
                 "acestep-5Hz-lm-1.7B/tokenizer.json", "acestep-5Hz-lm-1.7B/added_tokens.json"):
        assert must in main
    assert "silence_latent.pt" in lay["manifests"]["base"]


def test_acestep_partial_snapshot_is_models_missing(tmp_path: Path) -> None:
    snaps = fake_snapshots(tmp_path)
    (snaps[ace.REPO_MAIN] / "acestep-v15-turbo" / "silence_latent.pt").unlink()
    env = {ace.ENV_MODELS_DIR: str(tmp_path / "hub"), ace.ENV_MAIN_REV: MAIN_REV}
    specs = ace.specs_for(("turbo",), env)
    with pytest.raises(FileNotFoundError, match="silence_latent.pt"):
        models.resolve_all(specs, env)


def test_acestep_specs_resolve_through_the_env_root(tmp_path: Path) -> None:
    snaps = fake_snapshots(tmp_path)
    env = {ace.ENV_MODELS_DIR: str(tmp_path / "hub"), ace.ENV_MAIN_REV: MAIN_REV, ace.ENV_BASE_REV: BASE_REV}
    got = models.resolve_all(ace.specs_for(("turbo", "base"), env), env)
    assert got == snaps


# ---- configuration / preflight ---------------------------------------------------------------------------

GOOD = {"MM_ENGINE": "acestep15", ace.ENV_MAIN_REV: MAIN_REV, ace.ENV_BASE_REV: BASE_REV}


@pytest.fixture
def gpu(monkeypatch: pytest.MonkeyPatch) -> dict[str, Any]:
    state: dict[str, Any] = {"cuda": "12.8", "gib": 23.5, "name": "RTX 4090"}

    def info() -> tuple[str, float, str]:
        if state.get("error"):
            raise InitError("NO_GPU", state["error"])
        return state["cuda"], state["gib"], state["name"]

    monkeypatch.setattr(ace, "_cuda_info", info)
    return state


@pytest.mark.parametrize("tasks,expect", [(None, ("turbo",)), ("turbo", ("turbo",)), ("turbo,base", ("turbo", "base")),
                                          ("base, turbo", ("turbo", "base"))])
def test_acestep_tasks_of(tasks: str | None, expect: tuple[str, ...]) -> None:
    assert ace.tasks_of({} if tasks is None else {ace.ENV_TASKS: tasks}) == expect


@pytest.mark.parametrize("tasks", ["base", "sft", "turbo,sft", "turbo,,base", "turbo,base,base2"])
def test_acestep_tasks_invalid(tasks: str) -> None:
    with pytest.raises(InitError) as e:
        ace.tasks_of({ace.ENV_TASKS: tasks})
    assert e.value.code == "ENV_INVALID"


@pytest.mark.parametrize("env,match", [
    ({ace.ENV_MAIN_REV: ""}, ace.ENV_MAIN_REV),
    ({ace.ENV_MAIN_REV: "main"}, ace.ENV_MAIN_REV),
    ({ace.ENV_TASKS: "turbo,base", ace.ENV_BASE_REV: ""}, ace.ENV_BASE_REV),
    ({ace.ENV_TASKS: "base"}, ace.ENV_TASKS),
    ({ace.ENV_MAX_SECONDS: "abc"}, ace.ENV_MAX_SECONDS),
    ({ace.ENV_MAX_SECONDS: "10"}, ace.ENV_MAX_SECONDS),
    ({ace.ENV_MAX_SECONDS: "601"}, ace.ENV_MAX_SECONDS),
])
def test_acestep_preflight_env_invalid(env: dict[str, str], match: str, gpu: dict[str, Any]) -> None:
    with pytest.raises(InitError, match=match) as e:
        ace.preflight({**GOOD, **env})
    assert e.value.code == "ENV_INVALID"


def test_acestep_preflight_ok_and_logs_unmeasured_budget(gpu: dict[str, Any], capsys: pytest.CaptureFixture[str]) -> None:
    ace.preflight(GOOD)
    err = capsys.readouterr().err
    assert "memory budget is not measured" in err and "23.5 GiB" in err and "max 480 s" in err


def test_acestep_memory_budget_file_ships_unmeasured() -> None:
    b = ace.memory_budget()
    assert b["measured"] is False and b["vram_fraction"] == 0.9


def test_acestep_preflight_no_gpu(gpu: dict[str, Any]) -> None:
    gpu["error"] = "no CUDA device is visible"
    with pytest.raises(InitError) as e:
        ace.preflight(GOOD)
    assert e.value.code == "NO_GPU"


@pytest.mark.parametrize("cuda", ["12.4", "11.8", "", "weird"])
def test_acestep_preflight_old_cuda(cuda: str, gpu: dict[str, Any]) -> None:
    gpu["cuda"] = cuda
    with pytest.raises(InitError, match="older than 12.8") as e:
        ace.preflight(GOOD)
    assert e.value.code == "NO_GPU"


def test_acestep_preflight_600s_needs_a_big_card(gpu: dict[str, Any]) -> None:
    env = {**GOOD, ace.ENV_MAX_SECONDS: "600"}
    with pytest.raises(InitError) as e:
        ace.preflight(env)
    assert e.value.code == "NO_MEMORY" and "above 24 GB" in e.value.message
    gpu["gib"] = 44.0
    ace.preflight(env)
    ace.preflight({**GOOD, ace.ENV_MAX_SECONDS: "480"})


def _measured(monkeypatch: pytest.MonkeyPatch, **static: float) -> None:
    b = {"measured": True, "vram_fraction": 0.9,
         "static_gib": {"turbo_dit": 5.0, "base_dit": 5.0, "lm": 4.0, "qwen3_encoder": 1.5, "vae": 0.5,
                        "lm_kv_reservation": 3.0, **static}, "activation_gib_per_s": 0.01}
    monkeypatch.setattr(ace, "memory_budget", lambda: b)


def test_acestep_budget_arithmetic(monkeypatch: pytest.MonkeyPatch) -> None:
    _measured(monkeypatch)
    b = ace.memory_budget()
    assert ace.budget_gib(b, ("turbo",), 480) == pytest.approx(5 + 4 + 1.5 + 0.5 + 3 + 4.8)
    assert ace.budget_gib(b, ("turbo", "base"), 480) == pytest.approx(5 + 4 + 1.5 + 0.5 + 3 + 4.8 + 5)


def test_acestep_preflight_memory_budget_exceeded(gpu: dict[str, Any], monkeypatch: pytest.MonkeyPatch) -> None:
    _measured(monkeypatch)
    ace.preflight(GOOD)  # 18.8 GiB <= 0.9 x 23.5
    with pytest.raises(InitError) as e:  # turbo + base: 23.8 GiB > 21.15
        ace.preflight({**GOOD, ace.ENV_TASKS: "turbo,base"})
    assert e.value.code == "NO_MEMORY" and "turbo,base" in e.value.message
    gpu["gib"] = 47.0
    ace.preflight({**GOOD, ace.ENV_TASKS: "turbo,base"})


def test_acestep_main_memory_budget_exceeded_exits_1(gpu: dict[str, Any], monkeypatch: pytest.MonkeyPatch,
                                                      capsys: pytest.CaptureFixture[str]) -> None:
    _measured(monkeypatch)
    started: list[Any] = []
    rc = serverless.main({**GOOD, "HF_HUB_OFFLINE": "1", ace.ENV_TASKS: "turbo,base"}, runner=started.append)
    err = capsys.readouterr().err.strip().splitlines()
    assert rc == 1 and started == [] and len(err) == 1 and err[0].startswith("mmworker: init failed: NO_MEMORY:")


def test_acestep_engine_selection_runs_this_preflight(gpu: dict[str, Any]) -> None:
    assert engines.preflight(GOOD) == "acestep15"
    with pytest.raises(InitError):
        engines.preflight({**GOOD, ace.ENV_MAIN_REV: ""})


# ---- params copy ---------------------------------------------------------------------------------------

def test_acestep_params_copy_equals_the_go_file() -> None:
    go = REPO_ROOT / "internal" / "engine" / "acestep" / "engine_params.json"
    if not go.exists():
        pytest.skip("not run from a full checkout")
    assert (Path(ace.__file__).parent / "acestep_params.json").read_bytes() == go.read_bytes()


def test_acestep_params_current_is_audited_and_mapped() -> None:
    f = ace.params_file()
    assert f["audited"] is True
    for v, p in f["versions"].items():
        ace.check_params(p)
    assert ace.params_version(f["current"])["dit"]["shift"] == 3.0
    with pytest.raises(ValueError, match="not known"):
        ace.params_version("nope")


def test_acestep_pick() -> None:
    assert ace._pick(3, "x") == 3
    assert ace._pick({"turbo": 1, "base": 2}, "text2music", "base") == 2
    assert ace._pick({"default": "auto", "repaint": "explicit"}, "repaint", "turbo") == "explicit"
    assert ace._pick({"default": "auto", "repaint": "explicit"}, "complete", "base") == "auto"
    with pytest.raises(ValueError):
        ace._pick({"turbo": 1}, "x", "base")


def test_acestep_layout_json_is_valid_and_pinned_to_the_commit_file() -> None:
    lay = json.loads((Path(ace.__file__).parent / "acestep_layout.json").read_text())
    assert lay["pinned_commit_file"] == "worker/contract/ACESTEP_COMMIT"
    assert set(lay["dits"]) == {"turbo", "base"}
    assert copy.deepcopy(lay) == lay
