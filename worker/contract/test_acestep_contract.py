"""Worker-contract job for ACE-Step (C154.7): the pinned ACE-Step still matches what the adapter and the Go side assume.
This file covers the E2a tasks; test_acestep_e2b_contract.py (E2-PW3) covers extend, complete, extract and the LRC pass.

Needs `acestep` at the commit in ACESTEP_COMMIT (installed without its UI/training extras) and CPU torch; downloads
no weights and needs no GPU. Run via the ACE-Step contract job (see the PR text for the exact steps).
"""

from __future__ import annotations

import dataclasses
import re
import sys
import types
from pathlib import Path
from typing import Any

import pytest

try:  # the CPU job installs CPU torch; PyPI's torchaudio is a CUDA build that cannot load beside it
    import torchaudio  # noqa: F401
except (ImportError, OSError):
    for _name in [m for m in sys.modules if m == "torchaudio" or m.startswith("torchaudio.")]:
        del sys.modules[_name]
    sys.modules["torchaudio"] = types.ModuleType("torchaudio")  # acestep.audio_utils only imports it at module level

import acestep.constants as upstream_constants  # noqa: E402 - a missing acestep must fail this job, never skip it
import acestep.inference as upstream_inference  # noqa: E402
import acestep.model_downloader as upstream_downloader  # noqa: E402
from acestep.handler import AceStepHandler  # noqa: E402
from acestep.llm_inference import LLMHandler  # noqa: E402
from mmworker.engines import acestep as ace  # noqa: E402

HERE = Path(__file__).parent
REPO = HERE.parent.parent
GO_PARAMS = REPO / "internal" / "engine" / "acestep" / "engine_params.json"
VERSION = ace.params_file()["current"]

# Fields of GenerationParams the adapter deliberately leaves at the pinned commit's default. Reviewed: each is
# neutral for the tasks this worker runs, or an output of the LM / a request-local value set elsewhere.
IGNORED_PARAMS = {
    "audio_codes": "never passed: a text2music call with codes silently becomes cover (E2-P0a)",
    "global_caption": "lego/stems song-level caption: lego is not offered",
    "use_cot_lyrics": "declared but not consumed by inference (E2-P0a)",
    "cot_bpm": "output of the LM, written by generate_music", "cot_keyscale": "output of the LM",
    "cot_timesignature": "output of the LM", "cot_duration": "output of the LM",
    "cot_vocal_language": "output of the LM", "cot_caption": "output of the LM", "cot_lyrics": "output of the LM",
    "repaint_latent_crossfade_frames": "repaint seam blend: upstream default at the pinned commit (candidate for ace2)",
    "repaint_wav_crossfade_sec": "repaint seam blend: upstream default at the pinned commit (candidate for ace2)",
    "repaint_mode": "repaint strength mode: upstream default 'balanced' at the pinned commit (candidate for ace2)",
    "repaint_strength": "repaint strength: upstream default 0.5 at the pinned commit (candidate for ace2)",
    "retake_seed": "variation mixing, off at retake_variance=0", "retake_variance": "0 = no-op",
    "flow_edit_morph": "cover-only overlay, off", "flow_edit_source_caption": "flow edit, off",
    "flow_edit_source_lyrics": "flow edit, off", "flow_edit_n_min": "flow edit, off",
    "flow_edit_n_max": "flow edit, off", "flow_edit_n_avg": "flow edit, off",
    "audio_cover_strength": "cover-only; cover is not offered", "cover_noise_strength": "cover-only",
}
IGNORED_CONFIG = {
    "allow_lm_batch": "batch_size is 1", "lm_batch_chunk_size": "batch_size is 1",
    "constrained_decoding_debug": "logging only", "mp3_bitrate": "mp3 is not produced",
    "mp3_sample_rate": "mp3 is not produced",
}


def test_pin_file_is_a_full_commit() -> None:
    assert re.fullmatch(r"[0-9a-f]{40}", (HERE / "ACESTEP_COMMIT").read_text().strip())


def test_params_version_matches_commit() -> None:
    """D-E2-31: the audited parameter file and the pin describe the same ACE-Step commit."""
    pin = (HERE / "ACESTEP_COMMIT").read_text().strip()
    assert ace.params_version(VERSION)["acestep_commit"] == pin
    if GO_PARAMS.exists():
        assert (Path(ace.__file__).parent / "acestep_params.json").read_bytes() == GO_PARAMS.read_bytes()


def test_acestep_importable_without_training_extras() -> None:
    """The inference path imports neither the Gradio UI nor the training stack (the image installs neither)."""
    for extra in ("gradio", "lightning", "tensorboard", "peft", "modelscope", "torchcodec", "torchao"):
        assert extra not in sys.modules, extra
    import pytorch_wavelets  # noqa: F401 - DCW silently becomes a no-op without it
    # nano-vllm ships as source inside the ACE-Step tree and is installed as its own package in the image; the LM
    # imports it lazily (it needs triton and flash-attn), so the CPU job only checks the source is there
    import acestep

    assert (Path(acestep.__file__).parent / "third_parts" / "nano-vllm" / "nanovllm" / "__init__.py").is_file()


def _recorded_calls() -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """The kwargs the adapter would hand to ACE-Step for every task/class, via the test double."""
    fake = ace.FakeAceStep()
    ad = ace.Adapter(fake, weights={}, classes=frozenset({"turbo", "base"}))
    src = {"track_id": "t", "master_sha256": "1" * 64, "duration_ds": 100}
    ref = {"track_id": "r", "clip_sha256": "2" * 64, "from_ds": 0, "to_ds": 200}
    base = {"id": "c", "caption": "x", "lyrics": "[Verse]\nla", "instrumental": False, "vocal_language": "en",
            "seed": 1, "thinking": True, "engine_params_version": VERSION, "inference_steps": 8}
    reqs = [
        {**base, "task": "text2music", "duration_ds": 300, "ref": ref, "bpm": 100, "keyscale": "C Major",
         "timesignature": "4"},
        {**base, "task": "repaint", "thinking": False, "src": src, "repaint_from_ds": 10, "repaint_to_ds": 50},
    ]
    for r in reqs:
        p = ad.validate(r)
        fake.make_params(**{**p.gen, "src_audio": "s.flac", "reference_audio": "r.flac"})
        fake.make_config(**p.config)
    return fake.params, fake.configs


def test_generation_params_accept_kwargs() -> None:
    """Every kwarg the adapter passes is a field of the pinned GenerationParams / GenerationConfig."""
    params, configs = _recorded_calls()
    pf = {f.name for f in dataclasses.fields(upstream_inference.GenerationParams)}
    cf = {f.name for f in dataclasses.fields(upstream_inference.GenerationConfig)}
    for kw in params:
        assert set(kw) <= pf, set(kw) - pf
        upstream_inference.GenerationParams(**kw)  # constructible
    for kw in configs:
        assert set(kw) <= cf, set(kw) - cf
        upstream_inference.GenerationConfig(**kw)
    assert upstream_inference.GenerationConfig().batch_size == 2  # the default the adapter must not rely on
    assert {c["batch_size"] for c in configs} == {1}


def test_explicit_params_cover_output_affecting_fields() -> None:
    """Every GenerationParams / GenerationConfig field is passed by the adapter or on the reviewed ignore list, so a
    new upstream knob (a pin bump) cannot slip in with an unreviewed default."""
    params, configs = _recorded_calls()
    passed = set().union(*map(set, params))
    for f in dataclasses.fields(upstream_inference.GenerationParams):
        assert f.name in passed or f.name in IGNORED_PARAMS, f.name
    passed_c = set().union(*map(set, configs))
    for f in dataclasses.fields(upstream_inference.GenerationConfig):
        assert f.name in passed_c or f.name in IGNORED_CONFIG, f.name
    assert not (set(IGNORED_PARAMS) & passed)  # a field is either ignored or passed, never both
    assert set(IGNORED_PARAMS) <= {f.name for f in dataclasses.fields(upstream_inference.GenerationParams)}
    # the file's own keys are all mapped (nothing in engine_params.json is dropped)
    ace.check_params(ace.params_version(VERSION))
    defaults = {f.name: f.default for f in dataclasses.fields(upstream_inference.GenerationParams)}
    assert defaults["shift"] == 1.0  # the dataclass default the explicit shift=3.0 overrides (E2-P0a)


def test_task_types_present() -> None:
    """The adapter's task -> class split is the pinned model-card matrix."""
    turbo, base = set(upstream_constants.TASK_TYPES_TURBO), set(upstream_constants.TASK_TYPES_BASE)
    for task, cls in ace.TASK_CLASS.items():
        up = ace.UPSTREAM_TASK[task]
        assert up in (turbo if cls == "turbo" else base), (task, cls, up)
    assert {"complete", "extract"} <= base - turbo and {"text2music", "repaint"} <= turbo
    assert set(ace.TRACK_NAMES) == set(upstream_constants.TRACK_NAMES)


def test_instruction_templates() -> None:
    t = upstream_constants.TASK_INSTRUCTIONS
    for key, ours in ace.INSTRUCTIONS.items():
        assert ours == t[key], key
    # and the strings the adapter sends are what upstream's own helper would produce (UPPERCASE names, " | ")
    gen = AceStepHandler.generate_instruction
    assert ace._instruction("text2music", []) == gen(None, "text2music", None, None)
    assert ace._instruction("repaint", []) == gen(None, "repaint", None, None)
    assert ace._instruction("repaint", []) == "Repaint the mask area based on the given conditions:"


def _stub_tree(root: Path) -> dict[str, Path]:
    man = ace.layout()["manifests"]
    snaps = {}
    for repo, key in ((ace.REPO_MAIN, "main"), (ace.REPO_BASE, "base")):
        snap = root / "snap" / key
        for rel in man[key]:
            (snap / rel).parent.mkdir(parents=True, exist_ok=True)
            (snap / rel).write_bytes(b"")
        snaps[repo] = snap
    return snaps


def test_layout_matches_loader(tmp_path: Path) -> None:
    """The pinned loader is satisfied by the tree built from a stub repo of empty files: the main model check,
    both DiT checks and a code sync that finds nothing to write."""
    code = upstream_downloader._get_models_source_dir()
    snaps = _stub_tree(tmp_path)
    ckpt = tmp_path / "checkpoints"
    ace.build_checkpoints(ckpt, snaps, ("turbo", "base"), code)
    assert upstream_downloader.check_main_model_exists(ckpt)
    for name in ("acestep-v15-turbo", "acestep-v15-base"):
        assert upstream_downloader.check_model_exists(name, ckpt), name
        assert upstream_downloader._check_code_mismatch(name, ckpt) == [], name  # the sync would write nothing
        for p in (ckpt / name).glob("*.py"):
            assert not p.is_symlink()
    assert (ckpt / ace.layout()["main_links"][2] / "model.safetensors").exists()


def test_manifest_matches_layout() -> None:
    lay = ace.layout()
    main = set(lay["manifests"]["main"])
    for name in upstream_downloader.MAIN_MODEL_COMPONENTS:
        assert any(m.startswith(name + "/") for m in main), name
    assert lay["dits"]["turbo"]["dir"] in upstream_downloader.MAIN_MODEL_COMPONENTS
    assert lay["dits"]["base"]["dir"] in upstream_downloader.SUBMODEL_REGISTRY
    assert upstream_downloader.MAIN_MODEL_REPO == ace.REPO_MAIN
    assert upstream_downloader.SUBMODEL_REGISTRY[lay["dits"]["base"]["dir"]] == ace.REPO_BASE
    assert upstream_downloader.DEFAULT_LM_MODEL == ace.params_version(VERSION)["lm"]["model"]


def test_apis_the_adapter_calls_exist() -> None:
    import inspect

    init = inspect.signature(AceStepHandler.initialize_service).parameters
    assert {"project_root", "config_path", "device", "use_flash_attention", "compile_model", "offload_to_cpu",
            "offload_dit_to_cpu", "quantization"} <= set(init)
    li = inspect.signature(LLMHandler.initialize).parameters
    assert {"checkpoint_dir", "lm_model_path", "backend", "device", "offload_to_cpu"} <= set(li)
    gm = inspect.signature(upstream_inference.generate_music).parameters
    assert {"dit_handler", "llm_handler", "params", "config", "save_dir", "progress"} <= set(gm)



def test_instruction_default_and_codes_trap() -> None:
    """generate_music does not derive the instruction from task_type (the default is the text2music sentence)."""
    d = {f.name: f.default for f in dataclasses.fields(upstream_inference.GenerationParams)}
    assert d["instruction"] == upstream_constants.TASK_INSTRUCTIONS["text2music"]
    assert d["audio_codes"] == ""
