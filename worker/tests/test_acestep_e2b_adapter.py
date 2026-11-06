"""The E2b branches of the ACE-Step adapter (E2-PW3): extend, complete, extract and the LRC pass. Same rules as
test_acestep_adapter.py: no torch, no GPU, ACE-Step mocked at the boundary by `FakeAceStep`."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
import soundfile as sf

from mmworker.engines import acestep as ace
from mmworker.errors import WorkerError

from .test_acestep_adapter import adapter, base_request, render, src_input


def test_acestep_extend_pads_source(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request("extend", extend_ds=50), tmp_path, inputs=src_input(tmp_path, 10.0))
    p = fake.params[0]
    padded = sf.info(p["src_audio"])
    assert padded.frames / padded.samplerate == pytest.approx(15.0, abs=1e-3)
    assert padded.channels == 2 and padded.samplerate == 48000
    data, _ = sf.read(p["src_audio"], dtype="float32")
    assert float(abs(data[: 10 * 48000]).max()) > 0 and float(abs(data[10 * 48000 :]).max()) == 0.0
    assert (p["task_type"], p["repainting_start"], p["repainting_end"]) == ("repaint", 10.0, 15.0)
    assert p["chunk_mask_mode"] == "explicit" and "duration" not in p


def test_acestep_complete_params(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request("complete"), tmp_path, inputs=src_input(tmp_path))
    p = fake.params[0]
    assert p["task_type"] == "complete"
    assert p["instruction"] == "Complete the input track with DRUMS | BASS:"
    assert p["inference_steps"] == 50 and p["guidance_scale"] == 7.0 and p["duration"] == 10.0
    # v0.1.8 does not skip the LM for complete: every LM switch is off
    assert p["thinking"] is False and not any(p[k] for k in ace.COT_KEYS)
    assert p["dcw_enabled"] is False  # base class
    assert fake.calls[0][0] is fake.dits["base"]


def test_acestep_extract_params(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request("extract", tracks=["backing_vocals"], inference_steps=32, guidance_milli=4500),
           tmp_path, inputs=src_input(tmp_path))
    p = fake.params[0]
    assert p["task_type"] == "extract"
    assert p["instruction"] == "Extract the BACKING_VOCALS track from the audio:"
    assert p["inference_steps"] == 32 and p["guidance_scale"] == 4.5 and "duration" not in p
    assert not any(p[k] for k in ace.COT_KEYS) and p["thinking"] is False


@pytest.mark.parametrize("task", ["complete", "extract"])
def test_acestep_task_unavailable_without_base(task: str, tmp_path: Path) -> None:
    ad, fake = adapter(classes=("turbo",))
    with pytest.raises(WorkerError) as e:
        ad.validate(base_request(task))
    assert e.value.code == "TASK_UNAVAILABLE" and fake.calls == []
    ad.validate(base_request("text2music"))  # turbo tasks are fine


def test_acestep_base_models_in_result(tmp_path: Path) -> None:
    res = render(adapter()[0], base_request("complete"), tmp_path, inputs=src_input(tmp_path))
    assert res.info["models"]["dit"] == "ACE-Step/acestep-v15-base:acestep-v15-base"


def test_acestep_lrc_pass_separate(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request(), tmp_path)
    assert fake.dits["turbo"].lrc_calls == []  # absent unless request.lrc
    res = render(ad, base_request(lrc=True), tmp_path)
    (call,) = fake.dits["turbo"].lrc_calls
    assert call["seed"] == 42 and call["vocal_language"] == "en" and call["inference_steps"] == 8
    assert call["total_duration_seconds"] == pytest.approx(30.0) and call["lyric_token_ids"] == [[1]]
    assert res.lrc == "[00:00.00]la la\n[00:04.00]la\n"
    out = tmp_path / "o"
    out.mkdir()
    res.save_artifacts(out)
    assert (out / "lyrics.lrc").read_text() == res.lrc
    assert json.loads((out / "result.json").read_text())["lrc"] == {"requested": True, "produced": True}
    assert "lrc_s" in res.info["time_costs"]


def test_acestep_lrc_unknown_language_asks_for_english(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request(lrc=True, vocal_language="unknown"), tmp_path)
    assert fake.dits["turbo"].lrc_calls[0]["vocal_language"] == "en"


@pytest.mark.parametrize("text", [None, "", "no timestamps here", "[00:01.00]ok\nnot ok", "[ar:x]\n[00:01.00]a"])
def test_acestep_lrc_malformed_is_dropped_never_fabricated(text: str | None, tmp_path: Path) -> None:
    ad, _ = adapter(ace.FakeAceStep(lrc_text=text))
    res = render(ad, base_request(lrc=True), tmp_path)
    assert res.lrc is None
    out = tmp_path / "o"
    out.mkdir()
    res.save_artifacts(out)  # the take is unaffected
    assert not (out / "lyrics.lrc").exists()
    assert json.loads((out / "result.json").read_text())["lrc"] == {"requested": True, "produced": False}


def test_acestep_lrc_failure_does_not_fail_the_take(tmp_path: Path) -> None:
    res = render(adapter(ace.FakeAceStep(lrc_error=True))[0], base_request(lrc=True), tmp_path)
    assert res.lrc is None and res.audio.shape[1] == 2


def test_acestep_lrc_needs_the_extra_outputs(tmp_path: Path) -> None:
    class NoExtra(ace.FakeAceStep):
        def generate(self, *a: Any, **k: Any) -> Any:
            r = super().generate(*a, **k)
            r.extra_outputs.pop("pred_latents")
            return r

    ad, fake = adapter(NoExtra())
    assert render(ad, base_request(lrc=True), tmp_path).lrc is None
    assert fake.dits["turbo"].lrc_calls == []


def test_acestep_lrc_too_large_is_dropped() -> None:
    assert ace.valid_lrc("[00:01.00]a\n" * 10) == "[00:01.00]a\n" * 10
    assert ace.valid_lrc(("[00:01.00]" + "x" * 1000 + "\n") * 2000) is None
    assert ace.valid_lrc(7) is None  # type: ignore[arg-type]


def test_acestep_lrc_not_run_after_cancel(tmp_path: Path) -> None:
    calls = {"n": 0}

    def cancelled() -> bool:
        calls["n"] += 1
        return calls["n"] > 1  # false right after the render, true before the LRC pass

    ad, fake = adapter()
    with pytest.raises(ace.Cancelled):
        render(ad, base_request(lrc=True), tmp_path, cancelled=cancelled)
    assert fake.dits["turbo"].lrc_calls == []


BAD_E2B_REQUESTS = [
    ("lrc on repaint", "repaint", {"lrc": True}, "lrc"),
    ("lrc without lyrics", "text2music", {"lrc": True, "lyrics": "", "instrumental": True}, "lrc"),
    ("lrc not bool", "text2music", {"lrc": 1}, "lrc must be a boolean"),
    ("extend short", "extend", {"extend_ds": 10}, "extend_ds"),
    ("extend with ref", "extend", {"ref": {"track_id": "t", "clip_sha256": "1" * 64, "from_ds": 0, "to_ds": 5}},
     "forbidden key ref"),
    ("complete steps", "complete", {"inference_steps": 8}, "base steps"),
    ("complete guidance", "complete", {"guidance_milli": 99999}, "guidance_milli"),
    ("complete no tracks", "complete", {"tracks": []}, "tracks"),
    ("complete bad track", "complete", {"tracks": ["kazoo"]}, "tracks"),
    ("complete dup track", "complete", {"tracks": ["bass", "bass"]}, "tracks"),
    ("extract two tracks", "extract", {"tracks": ["bass", "drums"]}, "tracks"),
    ("complete with duration", "complete", {"duration_ds": 100}, "forbidden key duration_ds"),
]


@pytest.mark.parametrize(("name", "task", "over", "match"), BAD_E2B_REQUESTS, ids=[r[0] for r in BAD_E2B_REQUESTS])
def test_acestep_validate_rejects_e2b(name: str, task: str, over: dict[str, Any], match: str) -> None:
    ad, fake = adapter()
    with pytest.raises(ValueError, match=match):
        ad.validate(base_request(task, **over))
    assert fake.calls == []


def test_acestep_e2b_is_enabled() -> None:
    assert ace.ENABLED_TASKS == frozenset(ace.TASK_CLASS) and ace.LRC_ENABLED is True


def test_acestep_extend_cap_counts_the_extension() -> None:
    ad, _ = adapter(max_seconds=480)
    with pytest.raises(ValueError, match="cap"):
        ad.validate(base_request("extend", src={"track_id": "t", "master_sha256": "1" * 64, "duration_ds": 4790},
                                 extend_ds=50))
    ad.validate(base_request("extend", src={"track_id": "t", "master_sha256": "1" * 64, "duration_ds": 4750},
                             extend_ds=50))


def test_acestep_lrc_seconds_are_reported(tmp_path: Path) -> None:
    res = render(adapter()[0], base_request(lrc=True), tmp_path)
    assert "lrc_s" in res.timings and res.info["time_costs"]["lrc_s"] >= 0


def test_acestep_extend_source_is_not_modified(tmp_path: Path) -> None:
    inputs = src_input(tmp_path, 10.0)
    before = inputs["src_audio"].read_bytes()
    render(adapter()[0], base_request("extend"), tmp_path, inputs=inputs)
    assert inputs["src_audio"].read_bytes() == before
    assert sf.info(str(tmp_path / "src_padded.flac")).subtype == "PCM_24"


def test_acestep_complete_and_extract_use_the_base_model(tmp_path: Path) -> None:
    ad, fake = adapter()
    render(ad, base_request("complete"), tmp_path, inputs=src_input(tmp_path))
    render(ad, base_request("extract"), tmp_path, inputs=src_input(tmp_path))
    assert [c[0] for c in fake.calls] == [fake.dits["base"], fake.dits["base"]]


def test_acestep_task_unavailable_when_only_turbo_is_loaded(tmp_path: Path) -> None:
    ad, fake = adapter(classes=("turbo",))
    for task in ("complete", "extract"):
        with pytest.raises(WorkerError) as e:
            ad.validate(base_request(task))
        assert e.value.code == "TASK_UNAVAILABLE" and "base model" in e.value.message
    ad.validate(base_request("extend"))  # extend is a turbo-class repaint
    assert fake.calls == []
