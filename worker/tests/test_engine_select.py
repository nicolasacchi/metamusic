"""Engine selection (MM_ENGINE) and the adapter protocol seams (C154.1): handler paths for a non-yue2 engine."""

from __future__ import annotations

import contextlib
import hashlib
import json
import sys
import threading
from collections.abc import Callable, Iterator, Mapping
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest

from mmworker import contract, engines, serverless
from mmworker.contract import InputRef, TakeInput
from mmworker.engines import yue2 as yue2_engine
from mmworker.errors import InitError, WorkerError
from mmworker.pipeline import FakePipeline, _flac_bytes

from .conftest import Clock, Post, Sink, make_raw
from .conftest import fake as fake_yue2

ACE_WEIGHTS = {"Org/Ace-Main": "a" * 40, "Org/Ace-Base": "b" * 40}
SRC = b"source-audio-bytes" * 20


class FakeAceResult:
    truncated = None

    def __init__(self, files: dict[str, bytes]) -> None:
        self.files = files

    def save_artifacts(self, directory: str | Path) -> None:
        for n, b in self.files.items():
            (Path(directory) / n).write_bytes(b)


class FakeAce:
    """An acestep15-shaped adapter: records what the handler hands it."""

    engine = "acestep15"
    classes = frozenset({"turbo"})

    def __init__(self, weights: Mapping[str, str] = ACE_WEIGHTS, *, files: dict[str, bytes] | None = None) -> None:
        self.weights = dict(weights)
        self.files = files
        self.validated: list[dict[str, Any]] = []
        self.rendered: list[tuple[TakeInput, dict[str, bytes], Any]] = []
        self.fail_validate: BaseException | None = None

    def validate(self, request: dict[str, Any]) -> Any:
        self.validated.append(request)
        if self.fail_validate is not None:
            raise self.fail_validate
        return {"seed": request["seed"]}

    def render(self, take: TakeInput, inputs: Mapping[str, Path], cancelled: Callable[[], bool], tmp: Path, *,
               params: Any) -> FakeAceResult:
        self.rendered.append((take, {k: v.read_bytes() for k, v in inputs.items()}, params))
        files = {"audio.flac": _flac_bytes(2.0), "request.json": b"{}", "result.json": b"{}"}
        files.update(self.files or {})
        return FakeAceResult(files)

    def env_fingerprint(self, environ: Mapping[str, str]) -> dict[str, object]:
        return {"engine": "acestep15", "weights": dict(self.weights)}


def ace_raw(base: str, **over: Any) -> dict[str, Any]:
    ups = {"audio.flac": 192 << 20, "request.json": 4 << 20, "result.json": 4 << 20, "mm-result.json": 64 << 10}
    raw: dict[str, Any] = {
        "contract": "mm.worker/1", "take_id": "take-1", "attempt_id": "attempt-1", "engine": "acestep15",
        "request": {"id": "x", "task": "text2music", "seed": 7, "caption": "pop"},
        "timeout_s": 186, "upload_budget_s": 60, "deadline_unix": 1_800_100_000,
        "start": {"url": f"{base}/start?exp=1&sig=abc"},
        "uploads": {n: {"url": f"{base}/{n}?exp=1&sig=abc", "max_bytes": m} for n, m in ups.items()},
        "expect": {"weights": dict(ACE_WEIGHTS)},
    }
    raw.update(over)
    return raw


def with_src(base: str, data: bytes = SRC, **over: Any) -> dict[str, Any]:
    raw = ace_raw(base)
    raw["request"] = {**raw["request"], "task": "repaint", "src": {"track_id": "t", "master_sha256": "0" * 64,
                                                                   "duration_ds": 10}}
    raw["inputs"] = {"src_audio": {"url": f"{base}/in/src?sig=SECRET", "sha256": hashlib.sha256(data).hexdigest(),
                                   "bytes": len(data)}}
    raw.update(over)
    return raw


def process(take_raw: dict[str, Any], adapter: Any, clock: Clock | None = None) -> dict[str, Any]:
    from mmworker.handler import process as proc
    from mmworker.start import StartClient
    from mmworker.upload import Uploader

    c = clock or Clock()
    take = contract.parse_input(take_raw)
    return proc(take, adapter, Uploader(clock=c, sleep=c.sleep), c,
                start=StartClient(take.start_url, post=Post(c, 200), clock=c, sleep=c.sleep),
                job_id="job-1", spawn=lambda fn: fn(), sleep=c.sleep)


# ---- selection -------------------------------------------------------------------------------------------

def test_engine_env_selects_adapter(monkeypatch: pytest.MonkeyPatch) -> None:
    assert engines.engine_name({}) == "yue2"
    assert engines.engine_name({"MM_ENGINE": ""}) == "yue2"
    assert engines.engine_name({"MM_ENGINE": "acestep15"}) == "acestep15"

    calls: list[str] = []

    class Mod:
        @staticmethod
        def preflight(env: Mapping[str, str]) -> None:
            calls.append("preflight")

        @staticmethod
        def load(env: Mapping[str, str]) -> Any:
            calls.append("load")
            return FakeAce()

    monkeypatch.setattr(engines, "_module", lambda name: Mod if name == "acestep15" else pytest.fail(name))
    a = engines.load({"MM_ENGINE": "acestep15"})
    assert a.engine == "acestep15" and calls == ["preflight", "load"], "preflight runs before any load"


def test_engine_unknown_is_env_invalid() -> None:
    with pytest.raises(InitError) as ei:
        engines.engine_name({"MM_ENGINE": "musicgen"})
    assert ei.value.code == "ENV_INVALID" and "musicgen" in ei.value.message


def test_engine_not_in_image_is_env_invalid(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setitem(engines.ENGINES, "acestep15", "mmworker.engines.not_there")
    with pytest.raises(InitError) as ei:
        engines.load({"MM_ENGINE": "acestep15"})
    assert ei.value.code == "ENV_INVALID" and "not available" in ei.value.message


def test_engine_dependency_error_is_not_masked(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    pkg = tmp_path / "brokenmod.py"
    pkg.write_text("import a_dependency_that_is_absent\n")
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.setitem(engines.ENGINES, "acestep15", "brokenmod")
    with pytest.raises(ModuleNotFoundError):  # main reports it as LOAD_ERROR, with the real cause
        engines.load({"MM_ENGINE": "acestep15"})


def test_main_selected_engine_failure_is_one_line(monkeypatch: pytest.MonkeyPatch,
                                                  capsys: pytest.CaptureFixture[str]) -> None:
    class Mod:
        @staticmethod
        def preflight(env: Mapping[str, str]) -> None:
            raise InitError("NO_GPU", "no cuda")

    monkeypatch.setattr(engines, "_module", lambda name: Mod)
    started: list[Any] = []
    assert serverless.main({"HF_HUB_OFFLINE": "1", "MM_ENGINE": "acestep15"}, runner=started.append) == 1
    assert started == [] and capsys.readouterr().err == "mmworker: init failed: NO_GPU: no cuda\n"


def test_adapt_wraps_bare_pipeline_and_keeps_adapters() -> None:
    p = fake_yue2()
    a = engines.adapt(p)
    assert isinstance(a, yue2_engine.Adapter) and a.engine == "yue2" and a.weights == p.weights
    ace = FakeAce()
    assert engines.adapt(ace) is ace
    assert engines.adapt(a) is a


def test_yue2_adapter_env_fingerprint_and_validate() -> None:
    a = yue2_engine.Adapter(fake_yue2())
    fp = a.env_fingerprint({})
    assert fp["engine"] == "yue2" and fp["weights"] == a.weights
    with pytest.raises(ValueError):
        a.validate({"style": "x"})


def test_yue2_modules_stay_lazy() -> None:
    import mmworker.engines.yue2  # noqa: F401

    assert "yue2" not in sys.modules and "torch" not in sys.modules


def test_old_pipeline_import_paths_still_work() -> None:
    from mmworker import pipeline

    assert pipeline.load_yue2 is yue2_engine.load_yue2 and pipeline._Yue2Adapter is yue2_engine._Yue2Adapter
    with pytest.raises(AttributeError):
        pipeline.no_such_name  # noqa: B018


def test_yue2_preflight_requires_revisions() -> None:
    with pytest.raises(InitError):
        yue2_engine.preflight({"MM_YUE2_3B_REVISION": "a" * 40})
    yue2_engine.preflight({"MM_YUE2_3B_REVISION": "a" * 40, "MM_YUE2_VAE_REVISION": "b" * 40})


# ---- handler with a non-yue2 adapter ---------------------------------------------------------------------

def test_job_engine_mismatch_bad_input(sink: Sink) -> None:
    out = process(ace_raw(sink.base), fake_yue2())  # an acestep15 job on a yue2 worker
    assert out["mm"]["error"]["code"] == "BAD_INPUT" and "unsupported engine" in out["error"]
    out = process(make_raw(sink.base), FakeAce())  # and the other way round
    assert out["mm"]["error"]["code"] == "BAD_INPUT"


def test_truncated_checked_only_for_yue2(sink: Sink) -> None:
    ace = FakeAce()
    out = process(ace_raw(sink.base), ace)
    assert out["status"] == "ok" and out["truncated"] is None
    assert out["env"] == {"engine": "acestep15", "weights": ACE_WEIGHTS}
    # yue2 still refuses a non-dict truncated
    bad = process(make_raw(sink.base), FakePipeline(weights=dict(__import__("tests.conftest", fromlist=["x"]).WEIGHTS),
                                                    truncated=False))
    assert bad["mm"]["error"]["code"] == "TRUNCATED_NOT_DICT"


def test_acestep_artifact_allowlist_and_order(sink: Sink) -> None:
    raw = ace_raw(sink.base)
    raw["uploads"]["lyrics.lrc"] = {"url": f"{sink.base}/lyrics.lrc?sig=1", "max_bytes": 1 << 20}
    ace = FakeAce(files={"lyrics.lrc": b"[00:00.00]la", "config.json": b"{}", "latent.npy": b"never"})
    out = process(raw, ace)
    assert out["status"] == "ok"
    assert set(out["artifacts"]) == {"request.json", "result.json", "lyrics.lrc", "audio.flac"}
    order = [r.path.lstrip("/") for r in sink.requests if r.method == "PUT"]
    assert order[-1] == "mm-result.json" and "config.json" not in order and "latent.npy" not in order


def test_acestep_required_artifacts(sink: Sink) -> None:
    class NoAudio(FakeAce):
        def render(self, take: Any, inputs: Any, cancelled: Any, tmp: Path, *, params: Any) -> FakeAceResult:
            return FakeAceResult({"request.json": b"{}", "result.json": b"{}"})

    out = process(ace_raw(sink.base), NoAudio())
    assert out["mm"]["error"]["code"] == "ARTIFACT_MISSING"


def test_task_unavailable_passes_through_validate(sink: Sink) -> None:
    ace = FakeAce()
    ace.fail_validate = WorkerError("TASK_UNAVAILABLE", "base class not loaded")
    out = process(ace_raw(sink.base), ace)
    assert out["mm"]["error"]["code"] == "TASK_UNAVAILABLE" and ace.rendered == []
    ace.fail_validate = ValueError("steps out of range")
    out = process(ace_raw(sink.base), ace)
    assert out["mm"]["error"]["code"] == "BAD_INPUT" and ace.rendered == []


def test_weights_compared_as_a_set(sink: Sink) -> None:
    other = FakeAce({"Org/Ace-Main": "c" * 40})
    assert process(ace_raw(sink.base), other)["mm"]["error"]["code"] == "WEIGHTS_MISMATCH"
    reordered = FakeAce(dict(reversed(list(ACE_WEIGHTS.items()))))
    assert process(ace_raw(sink.base), reordered)["status"] == "ok"


@contextlib.contextmanager
def serve(data: bytes) -> Iterator[str]:
    """A loopback origin answering every GET with `data` (the sink in conftest only takes PUT/POST)."""

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a: object) -> None:
            pass

        def do_GET(self) -> None:
            self.send_response(200)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    t = threading.Thread(target=srv.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    t.start()
    try:
        yield f"http://127.0.0.1:{srv.server_address[1]}"
    finally:
        srv.shutdown()
        srv.server_close()
        t.join(5)


def test_inputs_fetched_and_handed_to_render(sink: Sink) -> None:
    ace = FakeAce()
    with serve(SRC) as origin:
        raw = with_src(origin)
        base = ace_raw(sink.base)
        raw["uploads"], raw["start"] = base["uploads"], base["start"]
        out = process(raw, ace)
    assert out["status"] == "ok"
    ((_, fetched, _),) = ace.rendered
    assert fetched == {"src_audio": SRC}
    assert isinstance(out["timings"]["fetch_s"], float)
    assert "SECRET" not in json.dumps(out)


def test_inputs_mismatch_no_render_in_handler(sink: Sink) -> None:
    ace = FakeAce()
    with serve(b"not the source") as origin:
        raw = with_src(origin)
        base = ace_raw(sink.base)
        raw["uploads"], raw["start"] = base["uploads"], base["start"]
        out = process(raw, ace)
    assert out["mm"]["error"]["code"] == "INPUT_MISMATCH" and ace.rendered == []
    assert "SECRET" not in json.dumps(out)
    assert sink.puts("/mm-result.json"), "the failure is still reported through mm-result.json"


def test_yue2_has_no_fetch_timing(sink: Sink) -> None:
    out = process(make_raw(sink.base), fake_yue2())
    assert "fetch_s" not in out["timings"]


# ---- contract parsing for acestep15 ----------------------------------------------------------------------

def test_parse_acestep_ok() -> None:
    t = contract.parse_input(with_src("https://mm.example.com"))
    assert t.engine == "acestep15" and t.min_audio_s == 0
    assert set(t.inputs) == {"src_audio"} and t.inputs["src_audio"].bytes == len(SRC)
    assert "SECRET" not in repr(t) and "SECRET" not in repr(t.inputs["src_audio"])
    assert t.inputs["src_audio"].url.endswith("sig=SECRET")


def _bad(mut: Callable[[dict[str, Any]], None]) -> None:
    raw = with_src("https://mm.example.com")
    mut(raw)
    with pytest.raises(WorkerError) as ei:
        contract.parse_input(raw)
    assert ei.value.code == "BAD_INPUT"


@pytest.mark.parametrize("name,mut", [
    ("min_audio_s", lambda r: r.update(min_audio_s=30)),
    ("config.json upload", lambda r: r["uploads"].update({"config.json": {"url": "https://x/y", "max_bytes": 5}})),
    ("audio over 192MiB", lambda r: r["uploads"]["audio.flac"].update(max_bytes=(192 << 20) + 1)),
    ("src without src_audio", lambda r: r["inputs"].clear()),
    ("src_audio without src", lambda r: r["request"].pop("src")),
    ("reference_audio without ref", lambda r: r["inputs"].update(
        reference_audio={"url": "https://x/y", "sha256": "a" * 64, "bytes": 5})),
    ("unknown input", lambda r: r["inputs"].update(other={"url": "https://x/y", "sha256": "a" * 64, "bytes": 5})),
    ("bad sha", lambda r: r["inputs"]["src_audio"].update(sha256="ABC")),
    ("zero bytes", lambda r: r["inputs"]["src_audio"].update(bytes=0)),
    ("bytes over cap", lambda r: r["inputs"]["src_audio"].update(bytes=(192 << 20) + 1)),
    ("bytes bool", lambda r: r["inputs"]["src_audio"].update(bytes=True)),
    ("input extra key", lambda r: r["inputs"]["src_audio"].update(z=1)),
    ("three weights", lambda r: r["expect"]["weights"].update({"a/b": "c" * 40, "d/e": "f" * 40})),
    ("inputs list", lambda r: r.update(inputs=[])),
])
def test_parse_acestep_rejects(name: str, mut: Callable[[dict[str, Any]], None]) -> None:
    _bad(mut)


def test_parse_yue2_still_rejects_inputs(sink: Sink) -> None:
    raw = make_raw("https://mm.example.com", inputs={})
    with pytest.raises(WorkerError):
        contract.parse_input(raw)


def test_artifacts_for_tables() -> None:
    assert contract.artifacts_for("yue2") == contract.artifacts_for("") == contract._TABLE
    ace = dict(contract.artifacts_for("acestep15"))
    assert ace == {"request.json": 4 << 20, "result.json": 4 << 20, "lyrics.lrc": 1 << 20,
                   "audio.flac": 192 << 20, "mm-result.json": 64 << 10}
    assert contract.required_for("acestep15") == {"audio.flac", "request.json", "result.json"}
    assert contract.required_for("yue2", "full") == {"audio.flac", "request.json", "config.json", "result.json"}
