"""The ACE-Step adapter (C154.4): validation, kwargs handed to ACE-Step, audio checks, LRC. No torch, no GPU:
`FakeAceStep` records the kwargs at the ACE-Step API boundary."""

from __future__ import annotations

import copy
import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest
import soundfile as sf

from mmworker.engines import acestep as ace
from mmworker.errors import WorkerError

WEIGHTS = {ace.REPO_MAIN: "a" * 40, ace.REPO_BASE: "b" * 40}
VERSION = ace.params_file()["current"]


def stereo(seconds: float, rate: int = 48000, level: float = 0.1) -> np.ndarray:
    n = int(round(seconds * rate))
    t = np.arange(n, dtype=np.float32) / rate
    w = (level * np.sin(2 * np.pi * 330 * t)).astype(np.float32)
    return np.stack([w, w], axis=1)


def write_audio(path: Path, seconds: float, rate: int = 48000, level: float = 0.1) -> Path:
    sf.write(str(path), stereo(seconds, rate, level), rate, format="FLAC", subtype="PCM_24")
    return path


def base_request(task: str = "text2music", **over: Any) -> dict[str, Any]:
    r: dict[str, Any] = {
        "id": "req-1", "task": task, "caption": "synthwave, female vocal", "lyrics": "[Verse]\nla la",
        "instrumental": False, "vocal_language": "en", "seed": 7, "thinking": False,
        "engine_params_version": VERSION, "inference_steps": 8,
    }
    src = {"track_id": "t1", "master_sha256": "1" * 64, "duration_ds": 100}
    if task == "text2music":
        r["duration_ds"] = 300
    elif task == "repaint":
        r.update(src=src, repaint_from_ds=20, repaint_to_ds=60)
    elif task == "extend":
        r.update(src=src, extend_ds=50)
    elif task in ("complete", "extract"):
        r.update(src=src, tracks=["drums"] if task == "extract" else ["drums", "bass"], inference_steps=50,
                 guidance_milli=7000, thinking=False)
    r.update(over)
    return r


def adapter(fake: ace.FakeAceStep | None = None, classes: tuple[str, ...] = ("turbo", "base"), *,
            max_seconds: int = 480) -> tuple[ace.Adapter, ace.FakeAceStep]:
    fake = fake or ace.FakeAceStep(classes)
    weights = {k: v for k, v in WEIGHTS.items() if k == ace.REPO_MAIN or "base" in classes}
    return ace.Adapter(fake, weights=weights, classes=frozenset(classes), max_seconds=max_seconds), fake


def render(ad: ace.Adapter, request: dict[str, Any], tmp: Path, *, inputs: dict[str, Path] | None = None,
           cancelled: Any = lambda: False) -> ace.AceResult:
    plan = ad.validate(request)
    return ad.render(None, inputs or {}, cancelled, tmp, params=plan)  # type: ignore[arg-type]


def src_input(tmp: Path, seconds: float = 10.0) -> dict[str, Path]:
    return {"src_audio": write_audio(tmp / "src_audio", seconds)}


# ---- kwargs handed to ACE-Step -----------------------------------------------------------------------------

def test_acestep_text2music_params(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request(bpm=96, keyscale="A Minor", timesignature="4", thinking=True), tmp_path)
    p, c = fake.params[0], fake.configs[0]
    assert p == {
        "shift": 3.0, "use_adg": False, "cfg_interval_start": 0.0, "cfg_interval_end": 1.0, "timesteps": None,
        "chunk_mask_mode": "auto", "infer_method": "ode", "sampler_mode": "euler", "velocity_norm_threshold": 0.0,
        "velocity_ema_factor": 0.0, "dcw_enabled": True, "dcw_mode": "double", "dcw_scaler": 0.05,
        "dcw_high_scaler": 0.02, "dcw_wavelet": "haar", "enable_normalization": True, "normalization_db": -1.0,
        "fade_in_duration": 0.0, "fade_out_duration": 0.0, "latent_shift": 0.0, "latent_rescale": 1.0,
        "lm_temperature": 0.85, "lm_top_p": 0.9, "lm_top_k": 0, "lm_cfg_scale": 2.0,
        "lm_negative_prompt": "NO USER INPUT", "use_cot_metas": True, "use_cot_caption": True,
        "use_cot_language": True, "use_constrained_decoding": True, "thinking": True,
        "task_type": "text2music", "instruction": "Fill the audio semantic mask based on the given conditions:",
        "caption": "synthwave, female vocal", "lyrics": "[Verse]\nla la", "instrumental": False,
        "vocal_language": "en", "seed": 7, "inference_steps": 8, "guidance_scale": 1.0, "bpm": 96,
        "keyscale": "A Minor", "timesignature": "4", "duration": 30.0,
    }
    assert c == {"batch_size": 1, "use_random_seed": False, "seeds": [7], "audio_format": "flac"}
    assert "src_audio" not in p and "reference_audio" not in p and "audio_codes" not in p


def test_acestep_instrumental_uses_the_documented_switch(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request(instrumental=True, lyrics=""), tmp_path)
    assert fake.params[0]["lyrics"] == "[Instrumental]" and fake.params[0]["instrumental"] is True


def test_acestep_reference_param(tmp_path: Path) -> None:
    ad, fake = adapter()
    ref = {"track_id": "t2", "clip_sha256": "2" * 64, "from_ds": 0, "to_ds": 200}
    clip = write_audio(tmp_path / "reference_audio", 30.0)
    render(ad, base_request(ref=ref), tmp_path, inputs={"reference_audio": clip})
    got = fake.params[0]["reference_audio"]
    assert Path(got).suffix == ".flac" and Path(got).read_bytes() == clip.read_bytes()
    render(ad, base_request("repaint", ref=ref), tmp_path, inputs={**src_input(tmp_path), "reference_audio": clip})
    assert fake.params[1]["reference_audio"] and fake.params[1]["task_type"] == "repaint"


def test_acestep_repaint_params(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request("repaint"), tmp_path, inputs=src_input(tmp_path))
    p = fake.params[0]
    assert p["task_type"] == "repaint"
    assert p["instruction"] == "Repaint the mask area based on the given conditions:"
    assert (p["repainting_start"], p["repainting_end"]) == (2.0, 6.0)
    assert p["chunk_mask_mode"] == "explicit"
    assert Path(p["src_audio"]).suffix == ".flac" and "duration" not in p
    assert p["thinking"] is False and not any(p[k] for k in ace.COT_KEYS)
    assert p["dcw_enabled"] is True and p["guidance_scale"] == 1.0 and p["inference_steps"] == 8


def test_acestep_batch_size_one(tmp_path: Path) -> None:
    ad, fake = adapter()
    for task, kw in (("text2music", {}), ("repaint", src_input(tmp_path))):
        render(ad, base_request(task), tmp_path, inputs=kw)
    assert [c["batch_size"] for c in fake.configs] == [1, 1]
    assert all(c["use_random_seed"] is False and c["seeds"] == [7] for c in fake.configs)


@pytest.mark.parametrize("task", ["text2music", "repaint"])
def test_acestep_all_params_explicit(task: str, tmp_path: Path) -> None:
    """Every key of engine_params.json reaches ACE-Step as an explicit kwarg (none is left to an upstream default)."""
    ad, fake = adapter()
    render(ad, base_request(task), tmp_path, inputs=src_input(tmp_path) if task != "text2music" else {})
    p, c = fake.params[0], fake.configs[0]
    ver = ace.params_version(VERSION)
    for key in ver["dit"]:
        assert (ace.DIT_FIELDS.get(key) in p) or (ace.CONFIG_FIELDS.get(key) in c), key
    for key in ver["lm"]:
        assert ace.LM_FIELDS.get(key) in p or key in ace.LOAD_KEYS["lm"], key
    assert ver["dit"]["shift"] == 3.0 == p["shift"] and ver["dit"]["batch_size"] == 1 == c["batch_size"]


def test_acestep_unmapped_param_key_fails() -> None:
    p = copy.deepcopy(ace.params_version(VERSION))
    ace.check_params(p)
    p["dit"]["brand_new_knob"] = 1
    with pytest.raises(ValueError, match="brand_new_knob"):
        ace.check_params(p)
    p = copy.deepcopy(ace.params_version(VERSION))
    del p["lm"]["top_p"]
    with pytest.raises(ValueError, match="top_p"):
        ace.check_params(p)


# ---- availability, limits, refusals ----------------------------------------------------------------------


def test_acestep_turbo_tasks_unavailable_on_a_base_only_worker() -> None:
    ad, _ = adapter(classes=("base",))
    with pytest.raises(WorkerError) as e:
        ad.validate(base_request("repaint"))
    assert e.value.code == "TASK_UNAVAILABLE"


@pytest.mark.parametrize("task", ["cover", "lego", "cover-nofsq", "", None, 3])
def test_acestep_refuses_other_tasks(task: Any) -> None:
    ad, fake = adapter()
    with pytest.raises(ValueError, match="unknown task"):
        ad.validate(base_request(task="text2music") | {"task": task})
    assert fake.calls == []


def _bad(**over: Any) -> dict[str, Any]:
    return over


BAD_REQUESTS = [
    ("unknown key", "text2music", {"colour": "blue"}, "unknown or forbidden key colour"),
    ("missing seed", "text2music", {"seed": None}, "missing seed"),
    ("seed negative", "text2music", {"seed": -1}, "seed"),
    ("seed bool", "text2music", {"seed": True}, "seed"),
    ("bad id", "text2music", {"id": "../x"}, "id is invalid"),
    ("empty caption", "text2music", {"caption": ""}, "caption"),
    ("long caption", "text2music", {"caption": "x" * 1001}, "caption"),
    ("lyrics not str", "text2music", {"lyrics": 3}, "lyrics must be a string"),
    ("instrumental with lyrics", "text2music", {"instrumental": True}, "instrumental"),
    ("language", "text2music", {"vocal_language": "EN"}, "vocal_language"),
    ("bpm low", "text2music", {"bpm": 10}, "bpm"),
    ("keyscale", "text2music", {"keyscale": "H Major"}, "keyscale"),
    ("timesignature", "text2music", {"timesignature": "5"}, "timesignature"),
    ("turbo steps", "text2music", {"inference_steps": 16}, "exactly 8"),
    ("turbo guidance", "text2music", {"guidance_milli": 7000}, "unknown or forbidden|base-class"),
    ("duration missing", "text2music", {"duration_ds": None}, "missing duration_ds"),
    ("duration short", "text2music", {"duration_ds": 50}, "duration_ds"),
    ("t2m with src", "text2music", {"repaint_from_ds": 1}, "forbidden key repaint_from_ds"),
    ("thinking on repaint", "repaint", {"thinking": True}, "thinking"),
    ("repaint range inverted", "repaint", {"repaint_from_ds": 60, "repaint_to_ds": 20}, "repaint range"),
    ("repaint past source", "repaint", {"repaint_to_ds": 101}, "repaint range"),
    ("repaint no src", "repaint", {"src": None}, "missing src"),
    ("src shape", "repaint", {"src": {"track_id": "t"}}, "src must be an object"),
    ("params version", "text2music", {"engine_params_version": "ace0"}, "not known"),
]


@pytest.mark.parametrize(("name", "task", "over", "match"), BAD_REQUESTS, ids=[r[0] for r in BAD_REQUESTS])
def test_acestep_validate_rejects(name: str, task: str, over: dict[str, Any], match: str) -> None:
    ad, fake = adapter()
    r = base_request(task)
    for k, v in over.items():
        if v is None:
            r.pop(k, None)
        else:
            r[k] = v
    with pytest.raises(ValueError, match=match):
        ad.validate(r)
    assert fake.calls == []


def test_acestep_validate_not_an_object() -> None:
    with pytest.raises(ValueError):
        adapter()[0].validate([])  # type: ignore[arg-type]


def test_acestep_duration_cap_is_the_guard() -> None:
    ad, _ = adapter(max_seconds=480)
    ad.validate(base_request(duration_ds=4800))
    with pytest.raises(ValueError, match="over this worker's 480 s cap"):
        ad.validate(base_request(duration_ds=4810))
    big, _ = adapter(max_seconds=600)
    big.validate(base_request(duration_ds=6000))
    # an edit's output length counts too: the source
    with pytest.raises(ValueError, match="cap"):
        ad.validate(base_request("repaint", src={"track_id": "t", "master_sha256": "1" * 64, "duration_ds": 4810},
                                 repaint_to_ds=100))


# ---- inputs ------------------------------------------------------------------------------------------------

REF = {"track_id": "t2", "clip_sha256": "2" * 64, "from_ds": 0, "to_ds": 200}


def test_acestep_reference_clip_30s_asserted(tmp_path: Path) -> None:
    ad, fake = adapter()
    for seconds in (29.9, 30.1):
        clip = write_audio(tmp_path / f"r{seconds}", seconds)
        with pytest.raises(WorkerError) as e:
            render(ad, base_request(ref=REF), tmp_path, inputs={"reference_audio": clip})
        assert e.value.code == "INPUT_MISMATCH" and "exactly 30.000 s" in e.value.message
    assert fake.calls == []


def test_acestep_reference_must_be_48k(tmp_path: Path) -> None:
    ad, fake = adapter()
    clip = write_audio(tmp_path / "r44", 30.0, rate=44100)
    with pytest.raises(WorkerError, match="48 kHz"):
        render(ad, base_request(ref=REF), tmp_path, inputs={"reference_audio": clip})
    assert fake.calls == []


def test_acestep_silent_reference_refused(tmp_path: Path) -> None:
    ad, fake = adapter()
    clip = write_audio(tmp_path / "silent", 30.0, level=0.0)
    with pytest.raises(WorkerError) as e:
        render(ad, base_request(ref=REF), tmp_path, inputs={"reference_audio": clip})
    assert e.value.code == "INPUT_MISMATCH" and "silent" in e.value.message and fake.calls == []


def test_acestep_unreadable_inputs(tmp_path: Path) -> None:
    ad, fake = adapter()
    junk = tmp_path / "junk"
    junk.write_bytes(b"not audio at all")
    for task, inputs in (("text2music", {"reference_audio": junk}), ("repaint", {"src_audio": junk})):
        extra = {"ref": REF} if task == "text2music" else {}
        with pytest.raises(WorkerError) as e:
            render(ad, base_request(task, **extra), tmp_path, inputs=inputs)
        assert e.value.code == "INPUT_MISMATCH"
    assert fake.calls == []


def test_acestep_source_length_must_match_the_request(tmp_path: Path) -> None:
    ad, fake = adapter()
    with pytest.raises(WorkerError) as e:
        render(ad, base_request("repaint"), tmp_path, inputs=src_input(tmp_path, 12.0))
    assert e.value.code == "INPUT_MISMATCH" and fake.calls == []


# ---- output ------------------------------------------------------------------------------------------------

def test_acestep_no_truncation_in_output(tmp_path: Path) -> None:
    res = render(adapter()[0], base_request(), tmp_path)
    assert res.truncated is None


def test_acestep_audio_is_encoded_as_24_bit_flac(tmp_path: Path) -> None:
    res = render(adapter()[0], base_request(), tmp_path)
    out = tmp_path / "out"
    out.mkdir()
    res.save_artifacts(out)
    info = sf.info(str(out / "audio.flac"))
    assert (info.format, info.subtype, info.samplerate, info.channels) == ("FLAC", "PCM_24", 48000, 2)
    assert info.frames / info.samplerate == pytest.approx(30.0, abs=1e-3)
    # float input is kept at 24-bit precision, not quantised to 16 bit
    data, _ = sf.read(str(out / "audio.flac"), dtype="float64")
    assert len(np.unique(np.round(data[:, 0] * 2**23))) > 2**9


def test_acestep_tensor_like_objects_are_accepted(tmp_path: Path) -> None:
    class T:  # the shape of a torch tensor: detach().cpu().float().numpy()
        def __init__(self, a: np.ndarray) -> None:
            self.a = a

        def detach(self) -> T:
            return self

        def cpu(self) -> T:
            return self

        def float(self) -> T:
            return self

        def numpy(self) -> np.ndarray:
            return self.a

    assert ace._np(T(np.zeros((2, 4)))).dtype == np.float32


def test_acestep_sample_rate_must_be_48k(tmp_path: Path) -> None:
    ad, _ = adapter(ace.FakeAceStep(rate=44100))
    with pytest.raises(RuntimeError, match="44100 Hz"):
        render(ad, base_request(), tmp_path)


def test_acestep_bad_tensor_shape(tmp_path: Path) -> None:
    class Flat(ace.FakeAceStep):
        def generate(self, *a: Any, **k: Any) -> Any:
            r = super().generate(*a, **k)
            r.audios[0]["tensor"] = np.zeros(10, dtype=np.float32)
            return r

    with pytest.raises(RuntimeError, match=r"\[channels, samples\]"):
        render(adapter(Flat())[0], base_request(), tmp_path)


def test_acestep_failed_generation_is_an_error(tmp_path: Path) -> None:
    ad, _ = adapter(ace.FakeAceStep(success=False, error="CUDA out of memory"))
    with pytest.raises(RuntimeError, match="CUDA out of memory"):
        render(ad, base_request(), tmp_path)


def test_acestep_cancel_via_progress(tmp_path: Path) -> None:
    state = {"stop": False}

    def on_generate(progress: Any) -> None:
        progress(0.1, desc="x")  # not cancelled yet
        state["stop"] = True
        progress(0.5, desc="y")  # raises Cancelled, as the handler's CancelWatch would trigger

    ad, _ = adapter(ace.FakeAceStep(on_generate=on_generate))
    with pytest.raises(ace.Cancelled):
        render(ad, base_request(), tmp_path, cancelled=lambda: state["stop"])


def test_acestep_cancel_swallowed_by_generate_music(tmp_path: Path) -> None:
    """generate_music catches the callback's exception and returns success=False: the adapter still reports the
    cancellation (so the handler answers DEADLINE / ATTEMPT_CLOSED, not PIPELINE_ERROR)."""
    ad, _ = adapter(ace.FakeAceStep(success=False, error="Cancelled"))
    with pytest.raises(ace.Cancelled):
        render(ad, base_request(), tmp_path, cancelled=lambda: True)


def test_acestep_result_json_and_request_json(tmp_path: Path) -> None:
    req = base_request(bpm=100)
    res = render(adapter()[0], req, tmp_path)
    out = tmp_path / "o"
    out.mkdir()
    res.save_artifacts(out)
    assert (out / "request.json").read_bytes() == json.dumps(req, sort_keys=True, separators=(",", ":")).encode()
    r = json.loads((out / "result.json").read_text())
    assert r["success"] is True and r["error"] is None and r["task"] == "text2music" and r["seed"] == 7
    assert r["sample_rate"] == 48000 and r["audio"] == {"channels": 2, "frames": 1440000, "subtype": "PCM_24",
                                                          "sample_rate": 48000}
    assert r["models"] == {"dit": "ACE-Step/Ace-Step1.5:acestep-v15-turbo",
                           "lm": "ACE-Step/Ace-Step1.5:acestep-5Hz-lm-1.7B"}
    assert set(r["lm_metadata"]) == {"bpm", "keyscale", "duration", "vocal_language"}
    assert r["time_costs"]["pipeline_total_time"] == 0.3 and "generate_s" in r["time_costs"]
    assert r["params"]["dit"]["shift"] == 3.0 and r["params"]["engine_params_version"] == VERSION
    assert r["params"]["load"]["backend"] == "vllm" and r["params"]["batch_size"] == 1
    assert not (out / "lyrics.lrc").exists()


# ---- LRC ---------------------------------------------------------------------------------------------------


# ---- fingerprint -------------------------------------------------------------------------------------------

def test_acestep_env_fields() -> None:
    ad, _ = adapter()
    fp = ad.env_fingerprint({"MM_ACESTEP_COMMIT": "d" * 40, "MM_WORKER_BUILD": "abc"})
    assert set(fp) == set(__import__("mmworker.env", fromlist=["ENV_KEYS"]).ENV_KEYS)
    assert fp["engine"] == "acestep15" and fp["decoder"] == "ACE-Step/Ace-Step1.5:vae"
    assert fp["code_commit"] == "d" * 40 and fp["worker_build"] == "abc" and fp["weights"] == WEIGHTS
    assert ad.engine == "acestep15" and ad.weights == WEIGHTS


def test_acestep_import_needs_no_torch() -> None:
    import sys

    assert "torch" not in sys.modules and "acestep" not in sys.modules


# ---- timings: per-component VRAM peaks, LM utilisation, stage costs (the P0b fold-in) -----------------------

MIB = 1 << 20


class FakeGpu:
    """torch.cuda stand-in: the test sets what the 'peak' will be before each milestone."""

    def __init__(self, resident: int = 7000 * MIB) -> None:
        self.resident, self.next_peak, self.resets = resident, 0, 0
        self.peak = 0

    def reset_peak(self) -> None:
        self.resets += 1
        self.peak = self.resident

    def peak_bytes(self) -> int:
        return self.peak

    def allocated_bytes(self) -> int:
        return self.resident


def metered(script: list[tuple[float, int]], **kw: Any) -> tuple[ace.Adapter, FakeGpu, ace.FakeAceStep]:
    gpu = FakeGpu()

    class Scripted(ace.FakeAceStep):
        def generate(self, dit: Any, llm: Any, params: Any, config: Any, progress: Any) -> Any:
            for value, peak in script:
                progress(value, desc="x")
                gpu.peak = max(gpu.peak, peak * MIB)  # the work after this milestone allocates up to `peak`
            return super().generate(dit, llm, params, config, progress)

    fake = Scripted(**kw)
    return ace.Adapter(fake, weights=WEIGHTS, classes=frozenset({"turbo", "base"}), gpu=gpu), gpu, fake


def test_acestep_timings_peak_vram_per_component(tmp_path: Path) -> None:
    ad, gpu, _ = metered([(0.1, 9000), (0.5, 11000), (0.51, 8000), (0.52, 14500), (0.6, 14500), (0.8, 12500),
                          (0.99, 7000)], lm_util_milli=412)
    res = render(ad, base_request(thinking=True), tmp_path)
    t = res.timings
    assert t["peak_vram_mb_lm"] == 11000.0 and t["peak_vram_mb_encoder"] == 8000.0
    assert t["peak_vram_mb_dit"] == 14500.0 and t["peak_vram_mb_vae"] == 12500.0
    assert t["vram_resident_mb"] == 7000.0 and t["lm_gpu_memory_utilization_milli"] == 412
    assert gpu.resets >= 5  # the counters are reset between phases
    assert all(isinstance(v, float | int) and not isinstance(v, bool) for v in t.values())
    assert "seed_reproducible" not in t  # only when the canary asks for a same-seed re-render


def test_acestep_timings_without_the_lm_phase(tmp_path: Path) -> None:
    ad, _, _ = metered([(0.51, 8000), (0.52, 13000), (0.8, 12000)])
    t = render(ad, base_request("repaint"), tmp_path, inputs=src_input(tmp_path)).timings
    assert "peak_vram_mb_lm" not in t and t["peak_vram_mb_dit"] == 13000.0 and t["peak_vram_mb_vae"] == 12000.0
    assert "lm_gpu_memory_utilization_milli" not in t  # not reported by this backend


def test_acestep_timings_without_a_gpu_have_no_vram_keys(tmp_path: Path) -> None:
    ad, _ = adapter(ace.FakeAceStep(progress_script=[0.51, 0.52, 0.8]))
    assert not [k for k in render(ad, base_request(), tmp_path).timings if "vram" in k]


def test_acestep_timings_stage_costs(tmp_path: Path) -> None:
    class Costs(ace.FakeAceStep):
        def generate(self, *a: Any, **k: Any) -> Any:
            r = super().generate(*a, **k)
            r.extra_outputs["time_costs"] = {"lm_total_time": 5.0, "dit_total_time_cost": 40.0,
                                             "dit_vae_decode_time_cost": 6.0, "dit_text": "x"}
            return r

    t = render(adapter(Costs())[0], base_request(), tmp_path).timings
    assert (t["lm_s"], t["dit_s"], t["decode_s"]) == (5.0, 34.0, 6.0) and "lrc_s" not in t


def test_acestep_phase_boundaries() -> None:
    assert [ace.phase_of(v) for v in (0.0, 0.1, 0.5, 0.51, 0.519, 0.52, 0.79, 0.8, 0.98, 0.99, 1.0)] == [
        "lm", "lm", "lm", "encoder", "encoder", "dit", "dit", "vae", "vae", None, None]


def test_acestep_meter_does_not_blame_a_phase_for_earlier_work() -> None:
    gpu = FakeGpu()
    m = ace.PhaseMeter(gpu)
    gpu.peak = 20000 * MIB  # something allocated before the first milestone, which is the DiT's
    m.on_progress(0.52)
    gpu.peak = 13000 * MIB
    first = m.finish()
    assert first["peak_vram_mb_dit"] == 13000.0
    assert m.finish() == first  # idempotent


def test_acestep_timings_survive_a_failed_render(tmp_path: Path) -> None:
    ad, gpu, _ = metered([(0.52, 9000)], success=False, error="CUDA out of memory")
    with pytest.raises(RuntimeError):
        render(ad, base_request(), tmp_path)


def test_acestep_lm_utilization_from_the_init_status() -> None:
    assert ace.lm_utilization_milli("OK\nDevice: cuda\nGPU Memory Utilization: 0.412\nLow GPU Memory Mode: False") == 412
    assert ace.lm_utilization_milli("GPU Memory Utilization: 0.9") == 900
    assert ace.lm_utilization_milli("5Hz LM initialized") is None and ace.lm_utilization_milli("") is None


# ---- what this build does not render yet (E2-PW3 enables extend, complete, extract and the LRC pass) -----------

@pytest.mark.parametrize("task", ["extend", "complete", "extract"])
def test_acestep_e2b_tasks_unavailable_in_this_build(task: str, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(ace, "ENABLED_TASKS", frozenset({"text2music", "repaint"}))  # the E2a build
    ad, fake = adapter()
    with pytest.raises(WorkerError) as e:
        ad.validate(base_request(task))
    assert e.value.code == "TASK_UNAVAILABLE" and "not supported by this worker build" in e.value.message
    assert fake.calls == []


def test_acestep_lrc_is_unavailable_in_this_build(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(ace, "LRC_ENABLED", False)  # the E2a build
    ad, _ = adapter()
    with pytest.raises(WorkerError) as e:
        ad.validate(base_request(lrc=True))
    assert e.value.code == "TASK_UNAVAILABLE" and "lrc" in e.value.message
    ad.validate(base_request(lrc=False))


def test_acestep_task_unavailable_for_an_unloaded_class(tmp_path: Path) -> None:
    """Upstream never refuses a task for a DiT: a worker that loaded only the base model refuses turbo-class tasks."""
    ad, fake = adapter(classes=("base",))
    with pytest.raises(WorkerError) as e:
        ad.validate(base_request("text2music"))
    assert e.value.code == "TASK_UNAVAILABLE" and "turbo model" in e.value.message and fake.calls == []
