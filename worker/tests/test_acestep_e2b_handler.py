"""The E2b branches through the real handler: extend, complete, extract and the LRC pass (E2-PW3)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from mmworker.engines import acestep as ace

from .test_acestep_adapter import adapter, base_request, write_audio
from .test_acestep_handler import fake_inputs, put_names, run, schema


def test_output_matches_schema_with_lrc(sink) -> None:  # type: ignore[no-untyped-def]
    ad, _ = adapter(ace.FakeAceStep(("turbo", "base"), max_audio_s=2.0))
    out = run(sink, base_request(duration_ds=100, lrc=True), ad, lrc=True)
    assert out["status"] == "ok", out
    schema("mm-worker-1.acestep15.output.schema.json").validate(out)
    assert sorted(put_names(sink)) == sorted(["audio.flac", "request.json", "result.json", "lyrics.lrc",
                                              "mm-result.json"])
    assert put_names(sink)[-1] == "mm-result.json"
    assert set(out["artifacts"]) == {"audio.flac", "request.json", "result.json", "lyrics.lrc"}
    assert next(r.body for r in sink.puts("/lyrics.lrc")).startswith(b"[00:00.00]")
    assert out["timings"]["lrc_s"] >= 0


def test_acestep_no_lrc_unless_requested(sink) -> None:  # type: ignore[no-untyped-def]
    ad, fake = adapter(ace.FakeAceStep(max_audio_s=1.0))
    out = run(sink, base_request(duration_ds=100), ad)
    assert out["status"] == "ok" and "lyrics.lrc" not in out["artifacts"] and fake.dits["turbo"].lrc_calls == []


def test_acestep_malformed_lrc_never_fails_the_take(sink) -> None:  # type: ignore[no-untyped-def]
    ad, _ = adapter(ace.FakeAceStep(max_audio_s=1.0, lrc_text="no stamps here"))
    out = run(sink, base_request(duration_ds=100, lrc=True), ad, lrc=True)
    assert out["status"] == "ok" and "lyrics.lrc" not in out["artifacts"] and sink.puts("/lyrics.lrc") == []


@pytest.mark.parametrize(("task", "seconds"), [("extend", 15.0), ("complete", 10.0), ("extract", 10.0)])
def test_acestep_e2b_tasks_through_the_handler(sink, monkeypatch, tmp_path: Path, task: str,  # type: ignore[no-untyped-def]
                                               seconds: float) -> None:
    src = write_audio(tmp_path / "src", 10.0)
    ad, _ = adapter()
    req = base_request(task)
    out = run(sink, req, ad, inputs=fake_inputs(monkeypatch, tmp_path, {"src_audio": src}))
    assert out["status"] == "ok", out
    schema("mm-worker-1.acestep15.output.schema.json").validate(out)
    assert out["audio_seconds"] == pytest.approx(seconds, abs=0.01)
    assert json.loads(next(r.body for r in sink.puts("/request.json"))) == json.loads(json.dumps(req))


def test_acestep_extract_stem_is_the_source_length(sink, monkeypatch, tmp_path: Path) -> None:  # type: ignore[no-untyped-def]
    src = write_audio(tmp_path / "src", 10.0)
    ad, fake = adapter()
    out = run(sink, base_request("extract", tracks=["vocals"]), ad,
              inputs=fake_inputs(monkeypatch, tmp_path, {"src_audio": src}))
    assert out["status"] == "ok" and out["audio_seconds"] == pytest.approx(10.0, abs=0.01)
    assert fake.params[0]["instruction"] == "Extract the VOCALS track from the audio:"


def test_acestep_complete_is_unavailable_on_a_turbo_only_worker(sink, monkeypatch, tmp_path: Path) -> None:  # type: ignore[no-untyped-def]
    src = write_audio(tmp_path / "src", 10.0)
    ad, fake = adapter(classes=("turbo",))
    out = run(sink, base_request("complete"), ad, weights={ace.REPO_MAIN: "a" * 40},
              inputs=fake_inputs(monkeypatch, tmp_path, {"src_audio": src}))
    assert out["error"].startswith("TASK_UNAVAILABLE") and "base model" in out["error"] and fake.calls == []
