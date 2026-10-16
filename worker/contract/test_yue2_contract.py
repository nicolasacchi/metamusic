"""Worker-contract job (C83.4): the pinned yue2 still matches what the worker and the Go side assume.

Needs `yue2` (pinned commit in YUE2_COMMIT) and CPU torch; downloads no weights. Run via `make contract`.
Set MM_CONTRACT_UPDATE=1 to rewrite testdata/artifacts.json (review the diff: it is the contract).
"""

from __future__ import annotations

import inspect
import json
import os
import re
import time
from pathlib import Path

import numpy as np
import pytest

import yue2  # noqa: E402 - a missing yue2 must fail this job, never skip it

HERE = Path(__file__).parent
TESTDATA = HERE / "testdata"
PIN_3B = "1" * 40  # synthetic revisions: the test only checks they are forwarded unchanged
PIN_VAE = "2" * 40


def test_pin_file_is_a_full_commit() -> None:
    assert re.fullmatch(r"[0-9a-f]{40}", (HERE / "YUE2_COMMIT").read_text().strip())


def test_call_signature() -> None:
    from yue2 import YuE2Pipeline
    from yue2.protocol import SongRequest

    params = set(inspect.signature(YuE2Pipeline.__call__).parameters)
    # style, lyrics and cancelled are named; cot, seed, abc, cfg_scale and id travel in **kwargs into SongRequest.
    assert {"style", "lyrics", "cancelled"} <= params
    assert any(p.kind is inspect.Parameter.VAR_KEYWORD for p in inspect.signature(YuE2Pipeline.__call__).parameters.values())
    assert {"cot", "seed", "abc", "cfg_scale", "id"} <= set(inspect.signature(SongRequest).parameters)


def test_from_pretrained_signature() -> None:
    from yue2 import YuE2Pipeline

    fp = inspect.signature(YuE2Pipeline.from_pretrained).parameters
    assert {"model", "vae", "revision", "vae_revision", "local_files_only", "cache_dir"} <= set(fp)
    # `device` is not a named from_pretrained parameter at this pin: it travels in **kwargs to __init__.
    assert any(p.kind is inspect.Parameter.VAR_KEYWORD for p in fp.values())
    assert "device" in inspect.signature(YuE2Pipeline.__init__).parameters


def _song_result(cot: str, tmp_dir: Path):
    """A tiny SongResult (1 s of zeros at 48 kHz) built exactly as YuE2Pipeline.__call__ builds its return
    value: SongRequest -> SymbolicPlan -> SemanticResult -> SongResult. Only the object is hand built; the
    file set below is what save_artifacts really writes for it. With cot=off the plan carries no ABC text, so
    no score.abc is written."""
    from yue2.pipeline import SemanticResult, SongResult, SymbolicPlan
    from yue2.protocol import SongRequest

    abc = None if cot == "off" else "X:1\nK:C\nCDEF|"
    request = SongRequest(style="pop", lyrics="[verse]\nla", cot=cot, seed=1, id="contract")
    plan = SymbolicPlan(request, abc, [1, 2, 3], [4, 5, 6], {"abc_seconds": 0.0}, False)
    semantic = SemanticResult(plan, [7, 8, 9], {"semantic_seconds": 0.0}, False)
    return SongResult(np.zeros(48000, dtype=np.float32), 48000, semantic, np.zeros((4, 8), dtype=np.float32),
                      {"hand_built": True}, {"hand_built": "x"}, {"e2e_seconds": 0.0}, "0" * 64)


def _written(cot: str, tmp_path: Path) -> list[str]:
    out = tmp_path / cot
    result = _song_result(cot, out)
    result.save_artifacts(out)
    return sorted(p.name for p in out.iterdir())


def test_truncated_is_a_dict(tmp_path: Path) -> None:
    t = _song_result("full", tmp_path).truncated
    assert isinstance(t, dict) and set(t) == {"abc", "semantic"}
    assert all(isinstance(v, bool) for v in t.values())


def test_save_artifacts_file_set(tmp_path: Path) -> None:
    got = {cot: _written(cot, tmp_path) for cot in ("full", "melody", "off")}
    path = TESTDATA / "artifacts.json"
    if os.environ.get("MM_CONTRACT_UPDATE") == "1":
        path.write_text(json.dumps(got, indent=2, sort_keys=True) + "\n")
    want = json.loads(path.read_text())
    assert got == want, "save_artifacts changed what it writes; review and regenerate artifacts.json"
    # The hand-built object proves only what save_artifacts writes for it; it never decides the required set.
    from mmworker.contract import ARTIFACTS

    for cot, names in want.items():
        assert "latent.npy" in names, "latent.npy is written locally but must never be uploaded"
        assert "latent.npy" not in ARTIFACTS
        assert {n for n in names if n != "latent.npy"} <= set(ARTIFACTS), cot


class _Recorded(Exception):
    pass


def test_hub_allowlist_only_the_two_repos(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                          socket_guard: list) -> None:
    import huggingface_hub
    from yue2 import YuE2Pipeline

    calls: list[tuple[str, str | None]] = []

    def record(repo_id, *args, revision=None, **kwargs):  # noqa: ANN001, ANN002, ANN003
        calls.append((repo_id, revision))
        if len(calls) == 2:
            raise _Recorded
        # a local directory stands in for the first repo so that the VAE resolution is reached
        d = tmp_path / "fake-model"
        d.mkdir(exist_ok=True)
        return str(d)

    monkeypatch.setattr(huggingface_hub, "snapshot_download", record)
    with pytest.raises(_Recorded):
        YuE2Pipeline.from_pretrained(model="m-a-p/YuE2-3B", vae="m-a-p/YuE2-Vae", revision=PIN_3B,
                                     vae_revision=PIN_VAE, device="cpu", local_files_only=True,
                                     cache_dir=str(tmp_path / "cache"), progress=False)
    assert calls == [("m-a-p/YuE2-3B", PIN_3B), ("m-a-p/YuE2-Vae", PIN_VAE)]
    assert socket_guard == []


def test_vae_from_the_image_snapshot_never_asks_the_hub(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                                         socket_guard: list) -> None:
    """The VAE baked into the image (MM_YUE2_VAE_DIR) is passed as a local directory: the hub is asked for the
    3B model only, and yue2 receives exactly the snapshot directory of the pinned revision."""
    import huggingface_hub
    from yue2 import YuE2Pipeline

    from mmworker.pipeline import resolve_vae

    snap = tmp_path / "hub" / "models--m-a-p--YuE2-Vae" / "snapshots" / PIN_VAE
    snap.mkdir(parents=True)
    (snap / "config.json").write_text("{}")
    vae = resolve_vae({"MM_YUE2_VAE_DIR": str(tmp_path / "hub")}, "m-a-p/YuE2-Vae", PIN_VAE)
    calls: list[tuple[str, str | None]] = []
    built: list[tuple[Path, Path]] = []

    def record(repo_id, *args, revision=None, **kwargs):  # noqa: ANN001, ANN002, ANN003
        calls.append((repo_id, revision))
        d = tmp_path / "fake-model"
        d.mkdir(exist_ok=True)
        return str(d)

    def init(self, model_dir, vae_dir, **kwargs):  # noqa: ANN001, ANN003
        built.append((Path(model_dir), Path(vae_dir)))
        self.load_timing = {}

    monkeypatch.setattr(huggingface_hub, "snapshot_download", record)
    monkeypatch.setattr(YuE2Pipeline, "__init__", init)
    YuE2Pipeline.from_pretrained(model="m-a-p/YuE2-3B", vae=vae, revision=PIN_3B, vae_revision=PIN_VAE,
                                 device="cpu", local_files_only=True, cache_dir=str(tmp_path / "cache"),
                                 progress=False)
    assert calls == [("m-a-p/YuE2-3B", PIN_3B)]
    assert built == [((tmp_path / "fake-model").resolve(), snap.resolve())]
    assert socket_guard == []


def test_3b_from_the_runpod_cache_is_a_local_directory_and_never_asks_the_hub(
        tmp_path: Path, monkeypatch: pytest.MonkeyPatch, socket_guard: list) -> None:
    """resolve_3b's output (RunPod's cached-model layouts) is accepted by yue2's loader as `model=`: a local
    directory short-circuits the hub entirely (storage.resolve_model), so no download call and no socket."""
    import huggingface_hub
    from yue2 import YuE2Pipeline

    from mmworker import pipeline
    from mmworker.pipeline import resolve_3b

    store = tmp_path / "store"
    snap = store / "m-a-p" / "YuE2-3B" / PIN_3B
    snap.mkdir(parents=True)
    (snap / "config.json").write_text("{}")
    (snap / "model-00001-of-00001.safetensors").write_bytes(b"w")
    vol = tmp_path / "vol"
    monkeypatch.setattr(pipeline, "MODEL_STORE", store)
    monkeypatch.setattr(pipeline, "VOLUME_HUB", vol)
    vae = tmp_path / "vae"
    vae.mkdir()
    model = resolve_3b({}, "m-a-p/YuE2-3B", PIN_3B)
    built: list[Path] = []

    def never(*a, **k):  # noqa: ANN002, ANN003, ANN202
        raise AssertionError("the hub was asked")

    def init(self, model_dir, vae_dir, **kwargs):  # noqa: ANN001, ANN003
        built.append(Path(model_dir))
        self.load_timing = {}

    monkeypatch.setattr(huggingface_hub, "snapshot_download", never)
    monkeypatch.setattr(YuE2Pipeline, "__init__", init)
    YuE2Pipeline.from_pretrained(model=model, vae=str(vae), revision=PIN_3B, vae_revision=PIN_VAE,
                                 device="cpu", local_files_only=True, progress=False)
    assert built == [snap.resolve()]
    assert socket_guard == []


def test_offline_raises_fast_and_opens_no_socket(tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
                                                 socket_guard: list) -> None:
    import huggingface_hub.constants as hc
    from yue2 import YuE2Pipeline

    monkeypatch.setenv("HF_HUB_OFFLINE", "1")
    monkeypatch.setattr(hc, "HF_HUB_OFFLINE", True)
    t0 = time.monotonic()
    with pytest.raises(Exception) as ei:  # noqa: PT011 - the exact hub exception type varies by version
        YuE2Pipeline.from_pretrained(model="m-a-p/YuE2-3B", vae="m-a-p/YuE2-Vae", revision=PIN_3B,
                                     vae_revision=PIN_VAE, device="cpu", local_files_only=True,
                                     cache_dir=str(tmp_path / "cache"), progress=False)
    assert time.monotonic() - t0 < 5
    assert not isinstance(ei.value, AssertionError), "a socket was opened"
    assert socket_guard == []


def _batch_verdict(line: str) -> bool:
    """What `yue2 batch` does with one row (cli.py): json.loads, request_kwargs, then pipe._request(**kwargs)
    (which builds SongRequest and so validates). _request does not touch self, so no model is needed."""
    from yue2.cli import request_kwargs
    from yue2.pipeline import YuE2Pipeline

    try:
        row = json.loads(line)
        kwargs = request_kwargs(row, Path("/nonexistent-contract-dir"))
        YuE2Pipeline._request(None, **{k: v for k, v in kwargs.items() if k not in {"abc_sampling", "semantic_sampling"}})
    except Exception:  # noqa: BLE001 - any rejection counts as rejected
        return False
    return True


def test_batch_validator_agreement() -> None:
    lines = (TESTDATA / "batch_cases.jsonl").read_text().split("\n")
    assert lines[-1] == ""
    lines = lines[:-1]
    expect = json.loads((TESTDATA / "batch_cases.expect.json").read_text())["cases"]
    assert len(lines) == len(expect) > 0
    bad = [e["name"] for line, e in zip(lines, expect) if _batch_verdict(line) != e["accept"]]
    assert not bad, f"Go ModeYuE2 and yue2 disagree on: {bad}"


def test_versions_summary(record_property, capsys) -> None:  # noqa: ANN001
    import torch

    line = f"yue2 {yue2.__version__} commit {(HERE / 'YUE2_COMMIT').read_text().strip()} torch {torch.__version__}"
    record_property("summary", line)
    with capsys.disabled():
        print("\n" + line)
        summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if summary:
            with open(summary, "a", encoding="utf-8") as f:
                f.write(line + "\n")


def test_adapter_request_kwargs_uses_real_yue2() -> None:
    """The worker's adapter (mmworker.pipeline) validates exactly like `yue2 batch`: same verdicts as Go."""
    from mmworker.pipeline import _Yue2Adapter

    adapter = _Yue2Adapter(None, {})
    ok = adapter.request_kwargs({"id": "a", "style": "s", "lyrics": "l", "cot": "off", "seed": 3})
    assert ok["seed"] == 3
    for bad in ({"id": "a", "style": "s"}, {"id": "a", "style": "s", "lyrics": "l", "cot": "half"},
                {"id": "a", "style": "s", "lyrics": "l", "extra": 1}):
        with pytest.raises(Exception):  # noqa: B017, PT011
            adapter.request_kwargs(bad)


# ---- X4: forced minimum render length ----

def _bare_pipeline(tmp_path: Path):
    """A YuE2Pipeline with only what effective_config reads: no weights, no loading."""
    from yue2 import YuE2Pipeline
    from yue2.protocol import GenerationConfig

    vae = tmp_path / "vae"
    vae.mkdir(exist_ok=True)
    (vae / "config.json").write_text('{"release_variant": "contract"}')
    p = YuE2Pipeline.__new__(YuE2Pipeline)
    p.generation_config, p.backend, p.quantization = GenerationConfig(), "torch", "none"
    p.memory_budget_gib, p.offload_ar, p.runtime_sha256 = 24, False, "0" * 64
    p.vae_core_frames, p.device, p.vae_dir = 64, "cpu", vae
    return p


def test_semantic_sampling_travels_through_request_kwargs_into_the_pipeline_call(tmp_path: Path) -> None:
    """The worker's only lever: request_kwargs keeps semantic_sampling, __call__ takes it, and yue2 records it
    in config.json under generation.semantic AND overrides.semantic (the take's re-run record)."""
    from yue2 import YuE2Pipeline
    from yue2.cli import request_kwargs

    from mmworker.pipeline import with_min_audio

    assert "semantic_sampling" in inspect.signature(YuE2Pipeline.__call__).parameters
    req = {"cot": "melody", "id": "x", "lyrics": "[verse]\nla", "seed": 2, "style": "pop"}
    kwargs = with_min_audio(request_kwargs(req, tmp_path), 252)
    assert kwargs["semantic_sampling"] == {"min_tokens": 6300}
    pipe = _bare_pipeline(tmp_path)
    request = pipe._request(**{k: v for k, v in kwargs.items() if k not in {"abc_sampling", "semantic_sampling"}})
    cfg = pipe.effective_config(request, kwargs.get("abc_sampling"), kwargs.get("semantic_sampling"))
    assert cfg["generation"]["semantic"]["min_tokens"] == 6300
    assert cfg["overrides"]["semantic"]["min_tokens"] == 6300
    assert cfg["generation"]["semantic"]["max_tokens"] == 9000
    plain = pipe.effective_config(request, None, None)
    assert plain["overrides"] == {} and plain["generation"]["semantic"]["min_tokens"] == 200
    # the take's identity covers the config (pipeline.__call__): another min_tokens is another identity
    from yue2.storage import identity

    ident = lambda c: identity({"request": request.to_dict(), "config": c, "weights": {}})  # noqa: E731
    assert ident(cfg) != ident(plain)
    # the request itself (request.json, the recipe's hash input) and the runtime hash do not change
    assert request.to_dict() == pipe._request(**{k: v for k, v in kwargs.items()
                                                 if k not in {"abc_sampling", "semantic_sampling"}}).to_dict()
    assert cfg["runtime_sha256"] == plain["runtime_sha256"]


def test_min_tokens_suppresses_the_end_token_until_reached_and_is_bounded() -> None:
    import torch
    from yue2.protocol import MUSIC_END, VOCAB_SIZE, Sampling
    from yue2.sampling import distribution

    from mmworker.contract import MAX_MIN_AUDIO_S
    from mmworker.pipeline import SEMANTIC_TOKENS_PER_S

    sampling = Sampling(min_tokens=6300)
    logits = torch.zeros(1, VOCAB_SIZE)
    logits[..., MUSIC_END] = 50.0  # the model wants to stop
    assert distribution(logits, sampling, [], 6299, "semantic")[..., MUSIC_END].item() == float("-inf")
    assert distribution(logits, sampling, [], 6300, "semantic")[..., MUSIC_END].item() > float("-inf")
    # the schema's 360 s ceiling is exactly max_tokens; one more token is refused by yue2 itself
    assert MAX_MIN_AUDIO_S * SEMANTIC_TOKENS_PER_S == Sampling().max_tokens
    Sampling(min_tokens=MAX_MIN_AUDIO_S * SEMANTIC_TOKENS_PER_S)
    with pytest.raises(ValueError):
        Sampling(min_tokens=MAX_MIN_AUDIO_S * SEMANTIC_TOKENS_PER_S + 1)
