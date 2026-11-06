"""Contract tests for the E2b branches (E2-PW3): extend, complete, extract and the LRC pass against the pinned
ACE-Step. test_acestep_contract.py covers the E2a tasks."""

from __future__ import annotations

import dataclasses
import importlib
import inspect

import acestep.constants as upstream_constants  # noqa: E402 - a missing acestep must fail this job, never skip it
import acestep.inference as upstream_inference  # noqa: E402
from acestep.handler import AceStepHandler  # noqa: E402
from mmworker.engines import acestep as ace  # noqa: E402

from test_acestep_contract import VERSION  # noqa: E402 - a sibling module in worker/contract


def _e2b_calls() -> list[dict]:
    fake = ace.FakeAceStep()
    ad = ace.Adapter(fake, weights={}, classes=frozenset({"turbo", "base"}))
    src = {"track_id": "t", "master_sha256": "1" * 64, "duration_ds": 100}
    base = {"id": "c", "caption": "x", "lyrics": "", "instrumental": True, "vocal_language": "en", "seed": 1,
            "thinking": False, "engine_params_version": VERSION}
    reqs = [
        {**base, "task": "extend", "src": src, "extend_ds": 50, "inference_steps": 8},
        {**base, "task": "complete", "src": src, "tracks": ["drums", "bass"], "inference_steps": 50,
         "guidance_milli": 7000},
        {**base, "task": "extract", "src": src, "tracks": ["vocals"], "inference_steps": 50, "guidance_milli": 7000},
    ]
    for r in reqs:
        p = ad.validate(r)
        fake.make_params(**{**p.gen, "src_audio": "s.flac"})
    return fake.params


def test_e2b_kwargs_are_fields_of_the_pinned_dataclass() -> None:
    fields = {f.name for f in dataclasses.fields(upstream_inference.GenerationParams)}
    for kw in _e2b_calls():
        assert set(kw) <= fields, set(kw) - fields
        upstream_inference.GenerationParams(**kw)


def test_e2b_instructions_equal_the_helper() -> None:
    gen = AceStepHandler.generate_instruction
    assert ace._instruction("extract", ["backing_vocals"]) == gen(None, "extract", "backing_vocals", None)
    assert ace._instruction("complete", ["drums", "bass"]) == gen(None, "complete", None, ["drums", "bass"])
    assert ace._instruction("complete", ["drums", "bass"]) == "Complete the input track with DRUMS | BASS:"
    assert ace._instruction("extend", []) == gen(None, "repaint", None, None)
    assert ace.INSTRUCTIONS["extract"] == upstream_constants.TASK_INSTRUCTIONS["extract"]
    assert ace.INSTRUCTIONS["complete"] == upstream_constants.TASK_INSTRUCTIONS["complete"]


def test_complete_and_extract_are_base_only_upstream() -> None:
    turbo, base = set(upstream_constants.TASK_TYPES_TURBO), set(upstream_constants.TASK_TYPES_BASE)
    assert {"complete", "extract"} <= base - turbo and "repaint" in turbo  # extend is a turbo repaint


def test_complete_does_not_skip_the_lm_at_this_pin() -> None:
    """At v0.1.8 only cover, cover-nofsq, repaint and extract skip the LM: complete must carry thinking=False and
    every use_cot_* False (the adapter's workaround; if the pin moves past #1287 this may be relaxed)."""
    src = inspect.getsource(upstream_inference.generate_music)
    assert 'skip_lm_tasks = {"cover", "cover-nofsq", "repaint", "extract"}' in src
    complete = next(p for p in _e2b_calls() if p["task_type"] == "complete")
    assert complete["thinking"] is False and not any(complete[k] for k in ace.COT_KEYS)


def test_lyric_timestamp_inputs_are_in_extra_outputs() -> None:
    """The LRC pass needs these keys of GenerationResult.extra_outputs (not recomputable later)."""
    src = inspect.getsource(importlib.import_module("acestep.core.generation.handler.generate_music_payload"))
    for key in ("pred_latents", "encoder_hidden_states", "encoder_attention_mask", "context_latents",
                "lyric_token_idss"):
        assert f'"{key}"' in src, key
    lrc = inspect.signature(AceStepHandler.get_lyric_timestamp).parameters
    assert {"pred_latent", "encoder_hidden_states", "encoder_attention_mask", "context_latents", "lyric_token_ids",
            "total_duration_seconds", "vocal_language", "inference_steps", "seed"} <= set(lrc)
