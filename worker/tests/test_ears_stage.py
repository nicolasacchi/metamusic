"""The Tier B ears stage module (EARS-P2, C331.4, C331.6 `worker/tests` list, C333.3): the subprocess is faked, never run."""

from __future__ import annotations

import json
import subprocess
import sys
from collections.abc import Callable, Iterator
from importlib import resources
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from mmworker import stages
from mmworker.contract import parse_job
from mmworker.errors import WorkerError
from mmworker.stages import ears

from .conftest import Clock, Sink, fake
from .test_v2 import EARS, OUT2, _fetcher, analyze_raw, err, raw2, run2

ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / "ears" / "models.lock.json"
SCHEMA = jsonschema.Draft202012Validator(
    json.loads(resources.files("mmworker").joinpath("schema/mm-ears-b-1.schema.json").read_text()))


def ears_models_tree(root: Path, lock: Path = LOCK) -> Path:
    """A models directory laid out as the image has it: every file of the lock as a sparse file of the locked size."""
    for m in json.loads(lock.read_text())["models"]:
        for f in m["files"]:
            p = root / m["role"] / f["path"]
            p.parent.mkdir(parents=True, exist_ok=True)
            with open(p, "wb") as fh:
                fh.truncate(f["bytes"])
    return root


def ok_document(duration: float = 1.0, stages_: tuple[str, ...] = ("separate", "asr", "transcribe")) -> dict[str, Any]:
    return {
        "contract": "mm.ears-b/1", "status": "ok", "code": None,
        "stages": {"separate": "ok", "asr": "ok", "transcribe": "ok" if "transcribe" in stages_ else "not_requested"},
        "models": {"separator": {"name": "htdemucs", "sha256": "a" * 64},
                   "asr": {"name": "faster-whisper-large-v3", "sha256": "b" * 64, "compute_type": "float16"},
                   "pitch": {"name": "torchcrepe-full", "sha256": "c" * 64} if "transcribe" in stages_ else None},
        "input": {"duration_s": duration, "sample_rate": 44100, "channels": 2, "codec": "flac"},
        "timings": {"load_s": 1, "separate_s": 1, "asr_s": 1, "transcribe_s": 1, "total_s": 4},
        "resources": {"device": "gpu", "peak_vram_mib": 6000, "parent_reserved_mib": None},
        "asr": {"language": "it", "forced": False, "segments": [], "words": []},
        "pitch": {"hop_s": 0.02, "cents": [0] * 50, "conf": [0] * 50} if "transcribe" in stages_ else None,
        "vocals": {"rms_dbfs": -20.0, "active_s": 0.5},
        "errors": [],
    }


class Proc:
    """A fake mmears process. `doc` is written to --out when it starts; `hang` never finishes by itself."""

    def __init__(self, cmd: list[str], env: dict[str, str], doc: Any, rc: int, hang: bool, stubborn: bool) -> None:
        self.cmd, self.env, self.rc, self.hang, self.stubborn = cmd, env, rc, hang, stubborn
        self.log: list[str] = []
        self.done = not hang
        if doc is not None:
            Path(cmd[cmd.index("--out") + 1]).write_text(doc if isinstance(doc, str) else json.dumps(doc))

    @property
    def returncode(self) -> int | None:
        return self.rc if self.done else None

    def poll(self) -> int | None:
        return self.returncode

    def terminate(self) -> None:
        self.log.append("terminate")
        self.done = not self.stubborn

    def kill(self) -> None:
        self.log.append("kill")
        self.done = True

    def wait(self, timeout: float | None = None) -> int:
        if not self.done:
            raise subprocess.TimeoutExpired("mmears", timeout or 0)
        return self.rc


class Spawner:
    def __init__(self) -> None:
        self.doc: Any = ok_document()
        self.rc, self.hang, self.stubborn, self.fail = 0, False, False, False
        self.procs: list[Proc] = []
        self.on_spawn: Callable[[list[str]], None] = lambda cmd: None

    def __call__(self, cmd: list[str], env: dict[str, str], **kw: Any) -> Proc:
        assert kw["stdin"] == subprocess.DEVNULL
        if self.fail:
            raise OSError("no interpreter")
        self.on_spawn(cmd)
        p = Proc(cmd, env, self.doc, self.rc, self.hang, self.stubborn)
        self.procs.append(p)
        return p


@pytest.fixture(autouse=True)
def _clean_registry(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    monkeypatch.setattr(stages, "_REGISTRY", {})
    yield


@pytest.fixture
def env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    models = ears_models_tree(tmp_path / "models")
    monkeypatch.setenv(ears.ENV_MODELS, str(models))
    monkeypatch.setenv(ears.ENV_LOCK, str(LOCK))
    monkeypatch.setenv(ears.ENV_SITE, "/opt/ears-site")
    monkeypatch.delenv(ears.ENV_PYTHON, raising=False)
    return models


@pytest.fixture
def spawn(monkeypatch: pytest.MonkeyPatch) -> Spawner:
    s = Spawner()
    monkeypatch.setattr(ears, "_spawn", s)
    return s


def ctx_for(tmp: Path, *, stages_: list[str] | None = None, timeout_s: float = 30.0,
            cancelled: Callable[[], bool] = lambda: False, lang: str = "auto") -> stages.StageContext:
    take = parse_job(raw2(ears={**EARS, "lang": lang, "stages": stages_ or EARS["stages"]}))
    audio = tmp / "audio.flac"
    audio.write_bytes(b"x")
    return stages.StageContext(take, tmp, {}, {}, cancelled, lambda: 0.0, timeout_s, audio=audio)


# ---- the lock gate (C333.3) ---------------------------------------------------------------------------------------

def test_shipped_lock_pins_exact_revisions_and_hashes() -> None:
    doc = json.loads(LOCK.read_text())
    assert doc["format"] == "mm.ears-models/1" and [m["role"] for m in doc["models"]] == ["separator", "asr", "pitch"]
    for m in doc["models"]:
        assert m["license"] == "MIT"
        assert "revision" not in m["source"] or len(m["source"]["revision"]) == 40
        for f in m["files"]:
            assert len(f["sha256"]) == 64 and set(f["sha256"]) <= set("0123456789abcdef") and f["bytes"] > 0
    assert "revision" in doc["models"][1]["source"]
    assert "/" + "0" * 40 not in LOCK.read_text()


def test_lock_verifies_sizes_only(env: Path) -> None:
    assert ears.lock_verifies(str(LOCK), str(env))
    victim = env / "pitch" / "full.pth"
    with open(victim, "r+b") as f:
        f.truncate(10)
    assert not ears.lock_verifies(str(LOCK), str(env))  # wrong size
    victim.unlink()
    assert not ears.lock_verifies(str(LOCK), str(env))  # missing file


def test_lock_verifies_rejects_a_bad_lock(tmp_path: Path) -> None:
    models = tmp_path / "m"
    models.mkdir()
    bad = tmp_path / "bad.json"
    for text in ("not json", "[]", json.dumps({"format": "other", "models": []}), json.dumps({"format": ears.LOCK_FORMAT,
                 "models": [{"role": "separator", "files": []}]})):
        bad.write_text(text)
        assert not ears.lock_verifies(str(bad), str(models))
    assert not ears.lock_verifies(str(tmp_path / "missing.json"), str(models))
    doc = json.loads(LOCK.read_text())
    for mutate in (lambda d: d["models"].pop(), lambda d: d["models"].append(dict(d["models"][0])),
                   lambda d: d["models"][0]["files"][0].update(path="../x"), lambda d: d["models"][0]["files"][0].update(
                       path="/abs"), lambda d: d["models"][0]["files"][0].update(bytes="9"),
                   lambda d: d["models"][0].update(role="other")):
        d = json.loads(json.dumps(doc))
        mutate(d)
        bad.write_text(json.dumps(d))
        assert not ears.lock_verifies(str(bad), str(ears_models_tree(tmp_path / "t")))


def test_stage_registers_ears_and_analyze_only_when_the_lock_verifies(env: Path, tmp_path: Path) -> None:
    assert not ears.register_stage({})  # no models dir: dev machine
    assert not ears.register_stage({ears.ENV_MODELS: str(tmp_path / "empty"), ears.ENV_LOCK: str(LOCK)})
    assert stages.caps() == []
    assert ears.register_stage({ears.ENV_MODELS: str(env), ears.ENV_LOCK: str(LOCK)})
    assert stages.caps() == ["ears", "analyze"]
    assert stages.get("ears").ears_stages == frozenset({"separate", "asr", "transcribe"})


def test_discover_registers_the_stage_in_an_image_like_environment(env: Path) -> None:
    import importlib

    importlib.reload(ears)  # module import is the registration point
    assert sorted(stages.caps()) == ["analyze", "ears"]


# ---- render (step 6b) ---------------------------------------------------------------------------------------------

def test_render_ears_runs_after_save(env: Path, spawn: Spawner, sink: Sink) -> None:
    stages.register("ears", ears.run)
    seen: dict[str, Any] = {}

    def on_spawn(cmd: list[str]) -> None:
        seen["cmd"] = cmd
        seen["audio_exists"] = Path(cmd[cmd.index("--audio") + 1]).is_file()

    spawn.on_spawn = on_spawn
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    OUT2.validate(out)
    assert out["ears"]["status"] == "ok" and "audio.flac" in out["artifacts"] and "analysis.json" in out["artifacts"]
    cmd = seen["cmd"]
    assert seen["audio_exists"] and cmd[1:3] == ["-m", "mmears"]
    assert cmd[cmd.index("--stages") + 1] == "separate,asr,transcribe" and cmd[cmd.index("--lang") + 1] == "auto"
    assert cmd[cmd.index("--models") + 1] == str(env) and cmd[cmd.index("--lock") + 1] == str(LOCK)


def test_ears_upload_order(env: Path, spawn: Spawner, sink: Sink) -> None:
    """The artifact table of the kind decides: audio.flac, analysis.json, then mm-result.json last (G1-F4 as built)."""
    stages.register("ears", ears.run)
    run2(sink, raw2(sink.base, ears=dict(EARS)))
    puts = [r.path.lstrip("/") for r in sink.puts()]
    assert puts.index("audio.flac") < puts.index("analysis.json") < puts.index("mm-result.json") and puts[-1] == "mm-result.json"


def test_ears_deadline_skips_and_take_uploads(env: Path, spawn: Spawner, sink: Sink) -> None:
    stages.register("ears", ears.run)
    clock = Clock()
    pipe = fake(on_call=lambda kw, c: clock.advance(3000))
    raw = raw2(sink.base, ears=dict(EARS), timeout_s=2700, upload_budget_s=60, deadline_unix=int(clock()) + 99999)
    out = run2(sink, raw, pipe, clock=clock)
    assert out["status"] == "ok" and out["ears"] == {"status": "skipped", "code": "DEADLINE", "seconds": 0.0}
    assert spawn.procs == [] and "audio.flac" in out["artifacts"] and "analysis.json" not in out["artifacts"]


def test_ears_error_keeps_take(env: Path, spawn: Spawner, sink: Sink) -> None:
    stages.register("ears", ears.run)
    spawn.doc, spawn.rc = None, 1  # the process died without writing a document
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    OUT2.validate(out)
    assert out["status"] == "ok" and out["ears"]["status"] == "error" and out["ears"]["code"] == "SUBPROCESS_FAILED"
    assert "audio.flac" in out["artifacts"]
    doc = json.loads(next(r.body for r in sink.puts("/analysis.json")))  # the parent's own error document, schema-valid
    SCHEMA.validate(doc)
    assert doc["status"] == "error" and doc["code"] == "SUBPROCESS_FAILED"


def test_ears_error_document_from_the_subprocess_is_kept(env: Path, spawn: Spawner, sink: Sink) -> None:
    stages.register("ears", ears.run)
    spawn.doc = {**ok_document(), "status": "error", "code": "MODELS_MISSING"}
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    assert out["ears"]["code"] == "MODELS_MISSING" and "analysis.json" in out["artifacts"]


def test_ears_frees_cache_before_spawn(env: Path, spawn: Spawner, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    calls: list[str] = []

    class Cuda:
        @staticmethod
        def is_available() -> bool:
            return True

        @staticmethod
        def empty_cache() -> None:
            calls.append("empty_cache")

        @staticmethod
        def max_memory_reserved() -> int:
            calls.append("max_memory_reserved")
            return 13210 * (1 << 20) + 5

    class Torch:
        cuda = Cuda

    monkeypatch.setitem(sys.modules, "torch", Torch)
    spawn.on_spawn = lambda cmd: calls.append("spawn")
    out = ears.run(ctx_for(tmp_path))
    assert out.status == "ok" and calls == ["empty_cache", "max_memory_reserved", "spawn"]
    cmd = spawn.procs[0].cmd
    assert cmd[cmd.index("--parent-reserved-mib") + 1] == "13210"


@pytest.mark.parametrize("torch", [None, "nocuda", "broken"])
def test_parent_reserved_is_omitted_without_cuda(env: Path, spawn: Spawner, monkeypatch: pytest.MonkeyPatch,
                                                 tmp_path: Path, torch: str | None) -> None:
    if torch is None:
        monkeypatch.delitem(sys.modules, "torch", raising=False)
    else:
        class Cuda:
            @staticmethod
            def is_available() -> bool:
                if torch == "broken":
                    raise RuntimeError("driver")
                return False

        monkeypatch.setitem(sys.modules, "torch", type("T", (), {"cuda": Cuda}))
    ears.run(ctx_for(tmp_path))
    assert "--parent-reserved-mib" not in spawn.procs[0].cmd


def test_ears_env_only_in_subprocess(env: Path, spawn: Spawner, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PYTHONPATH", raising=False)
    monkeypatch.delenv("HF_HUB_OFFLINE", raising=False)
    before_env, before_path = dict(__import__("os").environ), list(sys.path)
    ears.run(ctx_for(tmp_path))
    child = spawn.procs[0].env
    assert child["PYTHONPATH"] == "/opt/ears-site" and child["HF_HUB_OFFLINE"] == "1" and child["TRANSFORMERS_OFFLINE"] == "1"
    assert dict(__import__("os").environ) == before_env and sys.path == before_path
    assert "/opt/ears-site" not in sys.path and not any(m == "mmears" or m.startswith("mmears.") for m in sys.modules)
    monkeypatch.setenv("PYTHONPATH", "/x")
    ears.run(ctx_for(tmp_path))
    assert spawn.procs[1].env["PYTHONPATH"] == "/opt/ears-site:/x"


def test_ears_env_is_an_allow_list(env: Path, spawn: Spawner, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    for k in ("RUNPOD_API_KEY", "AWS_SECRET_ACCESS_KEY", "UPLOAD_TOKEN", "MM_WORKER_SECRET"):
        monkeypatch.setenv(k, "sentinel")
    monkeypatch.setenv("PATH", "/usr/bin")
    monkeypatch.setenv("CUDA_VISIBLE_DEVICES", "0")
    ears.run(ctx_for(tmp_path))
    child = spawn.procs[0].env
    assert not any(v == "sentinel" for v in child.values()) and not any(k.startswith("RUNPOD_") for k in child)
    assert child["PATH"] == "/usr/bin" and child["CUDA_VISIBLE_DEVICES"] == "0" and child[ears.ENV_MODELS]


def test_ok_document_is_accepted_even_when_the_process_exits_nonzero(env: Path, spawn: Spawner, tmp_path: Path) -> None:
    """A crash at interpreter teardown after the document was written does not discard a valid result."""
    spawn.rc = 139
    outcome = ears.run(ctx_for(tmp_path))
    assert outcome.status == "ok"


def test_python_interpreter_is_overridable(env: Path, spawn: Spawner, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ears.ENV_PYTHON, "/opt/venv/bin/python")
    ears.run(ctx_for(tmp_path))
    assert spawn.procs[0].cmd[0] == "/opt/venv/bin/python"


@pytest.mark.parametrize(("requested", "want"), [(["asr", "separate"], "separate,asr"),
                                                 (["transcribe", "asr", "separate"], "separate,asr,transcribe")])
def test_stages_are_passed_in_cli_order(env: Path, spawn: Spawner, tmp_path: Path, requested: list[str], want: str) -> None:
    ears.run(ctx_for(tmp_path, stages_=requested))
    cmd = spawn.procs[0].cmd
    assert cmd[cmd.index("--stages") + 1] == want


@pytest.mark.parametrize("requested", [["asr"], ["separate"], ["separate", "transcribe"]])
def test_unrunnable_stage_sets_are_refused_before_spawn(env: Path, spawn: Spawner, tmp_path: Path, requested: list[str]) -> None:
    with pytest.raises(WorkerError) as e:
        ears.run(ctx_for(tmp_path, stages_=requested))
    assert e.value.code == "PIPELINE_ERROR" and spawn.procs == []


def test_run_needs_an_ears_request_and_audio(env: Path, spawn: Spawner, tmp_path: Path) -> None:
    c = ctx_for(tmp_path)
    with pytest.raises(WorkerError):
        ears.run(stages.StageContext(c.take, tmp_path, {}, {}, c.cancelled, c.clock, 5.0, audio=None))
    with pytest.raises(WorkerError):
        ears.run(stages.StageContext(parse_job(raw2()), tmp_path, {}, {}, c.cancelled, c.clock, 5.0, audio=c.audio))


# ---- waiting, deadline and cancel ---------------------------------------------------------------------------------

@pytest.fixture
def ticking(monkeypatch: pytest.MonkeyPatch) -> list[float]:
    now = [0.0]
    monkeypatch.setattr(ears, "_now", lambda: now[0])
    monkeypatch.setattr(ears, "_sleep", lambda s: now.__setitem__(0, now[0] + s))
    return now


def test_timeout_kills_the_process_and_writes_a_deadline_document(env: Path, spawn: Spawner, ticking: list[float],
                                                                   tmp_path: Path) -> None:
    spawn.hang, spawn.doc = True, None
    out = ears.run(ctx_for(tmp_path, timeout_s=2.0))
    assert out.status == "error" and out.code == "DEADLINE" and 2.0 < out.seconds < 3.0
    assert spawn.procs[0].log == ["terminate"]
    doc = json.loads((tmp_path / "analysis.json").read_text())
    SCHEMA.validate(doc)
    assert doc["code"] == "DEADLINE"


def test_a_process_that_ignores_terminate_is_killed(env: Path, spawn: Spawner, ticking: list[float], tmp_path: Path) -> None:
    spawn.hang, spawn.stubborn, spawn.doc = True, True, None
    out = ears.run(ctx_for(tmp_path, timeout_s=1.0))
    assert out.code == "DEADLINE" and spawn.procs[0].log == ["terminate", "kill"]


def test_the_stage_deadline_watch_stops_the_process(env: Path, spawn: Spawner, ticking: list[float], tmp_path: Path) -> None:
    """The handler's CancelWatch trips at its deadline without being `closed`: the stage writes the DEADLINE document."""
    spawn.hang, spawn.doc = True, None
    out = ears.run(ctx_for(tmp_path, timeout_s=999.0, cancelled=lambda: ticking[0] > 1.0))
    assert out.code == "DEADLINE" and (tmp_path / "analysis.json").is_file()


def test_a_closed_attempt_stops_the_process_and_keeps_no_document(env: Path, spawn: Spawner, ticking: list[float],
                                                                  tmp_path: Path) -> None:
    class Watch:
        closed = True

        def __call__(self) -> bool:
            return True

    spawn.hang, spawn.doc = True, None
    out = ears.run(ctx_for(tmp_path, cancelled=Watch()))
    assert out.status == "error" and spawn.procs[0].log == ["terminate"] and not (tmp_path / "analysis.json").exists()


def test_spawn_failure_is_a_subprocess_error(env: Path, spawn: Spawner, tmp_path: Path) -> None:
    spawn.fail = True
    out = ears.run(ctx_for(tmp_path))
    assert (out.status, out.code) == ("error", "SUBPROCESS_FAILED")
    SCHEMA.validate(json.loads((tmp_path / "analysis.json").read_text()))


@pytest.mark.parametrize("doc", ["not json", {"contract": "other"}, [], None])
def test_an_unreadable_document_is_replaced_by_an_error_document(env: Path, spawn: Spawner, tmp_path: Path, doc: Any) -> None:
    spawn.doc = doc
    out = ears.run(ctx_for(tmp_path))
    assert (out.status, out.code) == ("error", "SUBPROCESS_FAILED")
    SCHEMA.validate(json.loads((tmp_path / "analysis.json").read_text()))


@pytest.mark.parametrize("requested", [("separate", "asr"), ("separate", "asr", "transcribe")])
def test_the_parent_error_document_matches_the_schema(requested: tuple[str, ...]) -> None:
    for code in ("DEADLINE", "SUBPROCESS_FAILED"):
        SCHEMA.validate(ears._error_document(code, "load", requested))


def test_an_error_document_without_a_code_is_subprocess_failed(env: Path, spawn: Spawner, tmp_path: Path) -> None:
    spawn.doc = {**ok_document(), "status": "error", "code": None}
    out = ears.run(ctx_for(tmp_path))
    assert (out.status, out.code) == ("error", "SUBPROCESS_FAILED")


def test_ok_document_reports_the_input_duration(env: Path, spawn: Spawner, tmp_path: Path) -> None:
    spawn.doc = ok_document(duration=215.8)
    out = ears.run(ctx_for(tmp_path))
    assert (out.status, out.code, out.audio_seconds) == ("ok", None, 215.8)
    spawn.doc = {**ok_document(), "input": None}
    assert ears.run(ctx_for(tmp_path)).audio_seconds is None


def test_the_fake_ok_document_is_schema_valid() -> None:
    SCHEMA.validate(ok_document())
    SCHEMA.validate(ok_document(stages_=("separate", "asr")))


# ---- analyze ------------------------------------------------------------------------------------------------------

def test_analyze_runs_ears_only(env: Path, spawn: Spawner, sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    stages.register("analyze", ears.run)
    spawn.doc = ok_document(duration=1.0)
    pipe = fake()
    out = run2(sink, analyze_raw(sink.base), pipe, fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)
    OUT2.validate(out)
    assert pipe.calls == [] and out["kind"] == "analyze" and out["ears"]["status"] == "ok"
    cmd = spawn.procs[0].cmd
    assert Path(cmd[cmd.index("--audio") + 1]).name == "src_audio"
    assert [r.path.lstrip("/") for r in sink.puts()] == ["analysis.json", "mm-result.json"]


def test_analyze_error_still_uploads_document(env: Path, spawn: Spawner, sink: Sink, monkeypatch: pytest.MonkeyPatch,
                                              tmp_path: Path) -> None:
    stages.register("analyze", ears.run)
    spawn.doc = {**ok_document(), "status": "error", "code": "MODELS_MISSING"}
    out = run2(sink, analyze_raw(sink.base), fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)
    assert err(out) == "ANALYZE_FAILED"
    assert out["mm"]["ears"]["code"] == "MODELS_MISSING" and "analysis.json" in out["mm"]["artifacts"]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["analysis.json", "mm-result.json"]
