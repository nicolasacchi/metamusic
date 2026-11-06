"""The ACE-Step adapter through the real handler (`process`): schemas both ways, uploads, error mapping.
ACE-Step itself is `FakeAceStep`; fetched inputs are local files (inputs.fetch is covered by test_inputs)."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest
import soundfile as sf

from mmworker import contract, handler
from mmworker.contract import InputRef
from mmworker.engines import acestep as ace
from mmworker.handler import process
from mmworker.start import StartClient
from mmworker.upload import Uploader

from .conftest import Clock, Post, Sink
from .test_acestep_adapter import REF, VERSION, WEIGHTS, adapter, base_request, write_audio

SCHEMAS = Path(contract.__file__).parent / "schema"


def schema(name: str) -> jsonschema.Draft202012Validator:
    return jsonschema.Draft202012Validator(json.loads((SCHEMAS / name).read_text()))


def raw_job(base: str, request: dict[str, Any], *, lrc: bool = False, weights: dict[str, str] | None = None,
            inputs: dict[str, Any] | None = None) -> dict[str, Any]:
    ups = {n: {"url": f"{base}/{n}?exp=1&sig=abc", "max_bytes": m} for n, m in contract.artifacts_for("acestep15")
           if lrc or n != "lyrics.lrc"}
    raw: dict[str, Any] = {
        "contract": "mm.worker/1", "take_id": "take-1", "attempt_id": "attempt-1", "engine": "acestep15",
        "request": request, "timeout_s": 600, "upload_budget_s": 60, "deadline_unix": 1_800_100_000,
        "start": {"url": f"{base}/start?exp=1&sig=abc"}, "uploads": ups,
        "expect": {"weights": weights or {ace.REPO_MAIN: WEIGHTS[ace.REPO_MAIN], ace.REPO_BASE: WEIGHTS[ace.REPO_BASE]}},
    }
    if inputs:
        raw["inputs"] = inputs
    return raw


def fake_inputs(monkeypatch: pytest.MonkeyPatch, tmp_path: Path, files: dict[str, Path]) -> dict[str, Any]:
    refs = {}
    for name, p in files.items():
        data = p.read_bytes()
        refs[name] = {"url": f"http://127.0.0.1:1/{name}?sig=SECRET", "sha256": hashlib.sha256(data).hexdigest(),
                      "bytes": len(data)}

    def fetch(inputs: dict[str, InputRef], deadline: float, tmp: Path, **kw: Any) -> dict[str, Path]:
        out = {}
        for name in inputs:
            dst = tmp / name
            dst.write_bytes(files[name].read_bytes())
            out[name] = dst
        return out

    monkeypatch.setattr(handler.inputs_mod, "fetch", fetch)
    return refs


def run(sink: Sink, request: dict[str, Any], ad: Any, *, check_schema: bool = True, **kw: Any) -> dict[str, Any]:
    clock = Clock()
    raw = raw_job(sink.base, request, **kw)
    if check_schema:  # the Go-side schema is the first line of defence; the adapter must hold without it
        schema("mm-worker-1.acestep15.input.schema.json").validate(raw)
    take = contract.parse_input(raw)
    return process(take, ad, Uploader(clock=clock, sleep=clock.sleep), clock,
                   start=StartClient(take.start_url, post=Post(clock, 200), clock=clock, sleep=clock.sleep),
                   job_id="job-1", spawn=lambda fn: fn(), sleep=clock.sleep)


def put_names(sink: Sink) -> list[str]:
    return [r.path.lstrip("/") for r in sink.puts()]


def test_output_matches_schema_acestep(sink: Sink) -> None:
    ad, _ = adapter(ace.FakeAceStep(("turbo", "base"), max_audio_s=2.0))
    out = run(sink, base_request(duration_ds=100), ad)
    assert out["status"] == "ok", out
    schema("mm-worker-1.acestep15.output.schema.json").validate(out)
    assert out["truncated"] is None and out["env"]["engine"] == "acestep15"
    assert sorted(put_names(sink)) == sorted(["audio.flac", "request.json", "result.json", "mm-result.json"])
    assert put_names(sink)[-1] == "mm-result.json"
    flac = next(r.body for r in sink.puts("/audio.flac"))
    assert flac[:4] == b"fLaC" and out["audio_seconds"] == pytest.approx(2.0, abs=0.01)
    uploaded = json.loads(next(r.body for r in sink.puts("/mm-result.json")))
    schema("mm-worker-1.acestep15.output.schema.json").validate(uploaded)
    assert set(out["artifacts"]) == {"audio.flac", "request.json", "result.json"}


def test_acestep_all_tasks_through_the_handler(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    src = write_audio(tmp_path / "src", 10.0)
    clip = write_audio(tmp_path / "clip", 30.0)
    cases = [
        ("repaint", {"src_audio": src}, {}),
        ("text2music", {"reference_audio": clip}, {"ref": REF, "duration_ds": 100}),
        ("repaint", {"src_audio": src, "reference_audio": clip}, {"ref": REF}),
    ]
    for task, files, extra in cases:
        sink.requests.clear()
        ad, _ = adapter(ace.FakeAceStep())
        req = base_request(task, **extra)
        inputs = fake_inputs(monkeypatch, tmp_path, files)
        out = run(sink, req, ad, inputs=inputs)
        assert out["status"] == "ok", (task, out)
        schema("mm-worker-1.acestep15.output.schema.json").validate(out)
        assert out["audio_seconds"] == pytest.approx(10.0, abs=0.01), task
        assert json.loads(next(r.body for r in sink.puts("/request.json"))) == json.loads(json.dumps(req))


def test_acestep_task_unavailable_through_the_handler(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    src = write_audio(tmp_path / "src", 10.0)
    ad, fake = adapter(classes=("turbo",))
    out = run(sink, base_request("complete"), ad, weights={ace.REPO_MAIN: WEIGHTS[ace.REPO_MAIN]},
              inputs=fake_inputs(monkeypatch, tmp_path, {"src_audio": src}))
    assert out["error"].startswith("TASK_UNAVAILABLE") and fake.calls == []
    assert out["mm"]["error"]["code"] == "TASK_UNAVAILABLE"
    schema("mm-worker-1.acestep15.output.schema.json").validate(out["mm"])


def test_acestep_bad_request_is_bad_input(sink: Sink) -> None:
    ad, fake = adapter()
    out = run(sink, base_request(inference_steps=16), ad, check_schema=False)
    assert out["error"].startswith("BAD_INPUT") and fake.calls == []


def test_acestep_duration_cap_is_bad_input(sink: Sink) -> None:
    ad, fake = adapter(max_seconds=120)
    out = run(sink, base_request(duration_ds=1300), ad)
    assert out["error"].startswith("BAD_INPUT") and "cap" in out["error"] and fake.calls == []


def test_acestep_weights_mismatch(sink: Sink) -> None:
    ad, fake = adapter()
    out = run(sink, base_request(), ad, weights={ace.REPO_MAIN: "f" * 40})
    assert out["error"].startswith("WEIGHTS_MISMATCH") and fake.calls == []


def test_acestep_short_reference_is_input_mismatch(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    clip = write_audio(tmp_path / "clip", 29.9)
    ad, fake = adapter()
    out = run(sink, base_request(ref=REF), ad, inputs=fake_inputs(monkeypatch, tmp_path, {"reference_audio": clip}))
    assert out["error"].startswith("INPUT_MISMATCH") and fake.calls == [] and sink.puts("/audio.flac") == []


def test_acestep_failed_generation_is_pipeline_error(sink: Sink) -> None:
    ad, _ = adapter(ace.FakeAceStep(success=False, error="CUDA out of memory"))
    out = run(sink, base_request(), ad)
    assert out["error"].startswith("PIPELINE_ERROR") and "CUDA out of memory" in out["error"]


@pytest.mark.parametrize(("advance", "statuses", "code"), [(31, (200, 410), "ATTEMPT_CLOSED"),
                                                          (10_000, (200,), "DEADLINE")])
@pytest.mark.parametrize("swallowed", [False, True])
def test_acestep_cancelled_render_is_never_a_pipeline_error(sink: Sink, advance: int, statuses: tuple[int, ...],
                                                            code: str, swallowed: bool) -> None:
    """A 410 from the start URL, or the generation deadline, during the render. The progress callback raises; real
    generate_music swallows that into success=False; either way the handler answers ATTEMPT_CLOSED / DEADLINE."""
    clock = Clock()

    def on_generate(progress: Any) -> None:
        clock.advance(advance)
        progress(0.5)

    fake = ace.FakeAceStep(on_generate=on_generate)
    if swallowed:
        inner = fake.on_generate

        def swallow(progress: Any) -> None:
            try:
                inner(progress)
            except ace.Cancelled:
                fake.success, fake.error = False, "Cancelled"

        fake.on_generate = swallow
    ad, _ = adapter(fake)
    take = contract.parse_input(raw_job(sink.base, base_request()))
    out = process(take, ad, Uploader(clock=clock, sleep=clock.sleep), clock,
                  start=StartClient(take.start_url, post=Post(clock, *statuses), clock=clock, sleep=clock.sleep),
                  job_id="job-1", spawn=lambda fn: fn(), sleep=clock.sleep)
    assert out["error"].split(":")[0] == code
    assert sink.puts("/audio.flac") == []


def test_acestep_render_runs_after_inputs_are_verified(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    src = write_audio(tmp_path / "src", 10.0)
    ad, fake = adapter()
    seen: dict[str, Any] = {}
    real = ace.check_source

    def spy(path: Path, *a: Any, **k: Any) -> Path:
        seen["name"] = path.name
        return real(path, *a, **k)

    monkeypatch.setattr(ace, "check_source", spy)
    out = run(sink, base_request("repaint"), ad, inputs=fake_inputs(monkeypatch, tmp_path, {"src_audio": src}))
    assert out["status"] == "ok" and seen["name"] == "src_audio"
    assert out["timings"]["fetch_s"] is not None


def test_acestep_flac_header_is_24_bit(sink: Sink) -> None:
    ad, _ = adapter(ace.FakeAceStep(max_audio_s=1.0))
    run(sink, base_request(duration_ds=100), ad)
    body = next(r.body for r in sink.puts("/audio.flac"))
    p = Path(__import__("tempfile").mkdtemp()) / "a.flac"
    p.write_bytes(body)
    assert sf.info(str(p)).subtype == "PCM_24"


def test_selftest_acestep(monkeypatch: pytest.MonkeyPatch) -> None:
    from mmworker import selftest

    assert selftest.run("acestep15") is True
    assert selftest.run("yue2") is True
    # MODE=selftest ENGINE=acestep15 (the image sets both ENGINE and MM_ENGINE)
    monkeypatch.delenv("MM_ENGINE", raising=False)
    monkeypatch.setenv("ENGINE", "acestep15")
    assert selftest.main() == 0
    from mmworker.engines import acestep

    ran: list[str] = []
    real = acestep.FakeAceStep
    monkeypatch.setattr(acestep, "FakeAceStep", lambda *a, **k: ran.append("ace") or real(*a, **k))
    assert selftest.run() is True and ran == ["ace"]


def test_acestep_timings_reach_mm_result(sink: Sink) -> None:
    """Adapter-measured numbers are merged into the output `timings` (any number is allowed by the schema)."""
    from .test_acestep_adapter import FakeGpu

    fake = ace.FakeAceStep(max_audio_s=1.0, progress_script=[0.1, 0.51, 0.52, 0.8], lm_util_milli=412)
    ad = ace.Adapter(fake, weights=WEIGHTS, classes=frozenset({"turbo", "base"}), gpu=FakeGpu())
    out = run(sink, base_request(duration_ds=100), ad)
    schema("mm-worker-1.acestep15.output.schema.json").validate(out)
    t = out["timings"]
    assert t["lm_gpu_memory_utilization_milli"] == 412 and t["vram_resident_mb"] == 7000.0
    assert {"peak_vram_mb_lm", "peak_vram_mb_encoder", "peak_vram_mb_dit", "peak_vram_mb_vae"} <= set(t)
    assert t["lm_s"] == 0.1 and t["generate_s"] is not None and t["handler_s"] is not None
    uploaded = json.loads(next(r.body for r in sink.puts("/mm-result.json")))
    assert uploaded["timings"]["lm_gpu_memory_utilization_milli"] == 412


def test_handler_never_overwrites_fixed_timings(sink: Sink) -> None:
    class Greedy(ace.FakeAceStep):
        pass

    ad, _ = adapter(Greedy(max_audio_s=1.0))
    real = ad.render

    def render(*a: Any, **k: Any) -> Any:
        res = real(*a, **k)
        res.timings.update({"generate_s": 999.0, "flag": True, "text": "x"})
        return res

    ad.render = render  # type: ignore[method-assign]
    out = run(sink, base_request(duration_ds=100), ad)
    assert out["timings"]["generate_s"] != 999.0 and "flag" not in out["timings"] and "text" not in out["timings"]
