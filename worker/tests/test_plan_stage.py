"""The plan stage (DESK-P1, C361.3): seeds, deadlines, bundles. The bundle checks restate the ingester's rules
(internal/desk/bundle.go: allowlisted names, sizes, exactly the six plan.json keys) so a drift fails here first."""

from __future__ import annotations

import hashlib
import io
import json
import sys
import tarfile
from collections.abc import Callable, Iterator
from pathlib import Path
from typing import Any

import pytest

from mmworker import caps, stages
from mmworker.contract import parse_job
from mmworker.engines import yue2 as yue2_engine
from mmworker.stages import plan as plan_stage

from .conftest import Clock, Sink, fake
from .test_v2 import OUT2, err, plan_raw, raw2, run2

PLAN_JSON_KEYS = {"seed", "cot", "truncated", "abc_tokens", "seconds", "yue2"}
NAMES = {"score.abc", "plan.json", "abc_tokens.npy", "prefix.npy", "plan_manifest.json"}
LIMIT = {"score.abc": 64 << 10, "plan.json": 64 << 10}


@pytest.fixture(autouse=True)
def _registry(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    monkeypatch.setattr(stages, "_REGISTRY", {})
    stages.register(stages.PLAN, plan_stage.run)
    yield


def members(path: Path) -> dict[str, bytes]:
    """Read a bundle the way the ingester does: regular files only, allowlisted names, header sizes within limits."""
    out: dict[str, bytes] = {}
    with tarfile.open(path, "r:") as tar:  # "r:" = uncompressed only
        for m in tar.getmembers():
            assert m.isreg() and m.name in NAMES and m.name not in out, m.name
            assert 0 < m.size <= LIMIT.get(m.name, 1 << 20), (m.name, m.size)
            f = tar.extractfile(m)
            assert f is not None
            data = f.read()
            assert len(data) == m.size
            out[m.name] = data
    assert path.stat().st_size <= 2 << 20
    return out


def uploaded(sink: Sink, tmp_path: Path, name: str) -> Path:
    body = next(r.body for r in sink.puts() if r.path.lstrip("/") == name)
    p = tmp_path / name
    p.write_bytes(body)
    return p


def test_plan_stage_sequential_seeds(sink: Sink, tmp_path: Path) -> None:
    seen: list[int] = []
    pipe = fake(on_plan=lambda kw, _c: seen.append(kw["seed"]))
    out = run2(sink, plan_raw(sink.base, seeds=(7, 3, 9)), pipe)
    OUT2.validate(out)
    assert seen == [7, 3, 9] and [c["seed"] for c in pipe.plan_calls] == [7, 3, 9]
    assert [(p["index"], p["seed"], p["status"], p["bundle"]) for p in out["plans"]] == [
        (0, 7, "ok", "plan-00.tar"), (1, 3, "ok", "plan-01.tar"), (2, 9, "ok", "plan-02.tar")]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["plan-00.tar", "plan-01.tar", "plan-02.tar", "mm-result.json"]
    # the request reached the engine without a semantic phase, and the bundle's plan.json carries the seed
    assert all("semantic_sampling" not in c for c in pipe.plan_calls)
    doc = json.loads(members(uploaded(sink, tmp_path, "plan-01.tar"))["plan.json"])
    assert doc["seed"] == 3


def test_plan_stage_drops_semantic_sampling_before_the_engine(sink: Sink) -> None:
    pipe = fake()
    raw = plan_raw(sink.base, seeds=(1,))
    raw["request"]["semantic_sampling"] = {"temperature": 0.9}
    raw["request"]["abc_sampling"] = {"temperature": 0.8}
    out = run2(sink, raw, pipe)
    assert out["status"] == "ok" or out.get("plans")
    assert pipe.plan_calls[0]["abc_sampling"] == {"temperature": 0.8} and "semantic_sampling" not in pipe.plan_calls[0]


class Watch:
    """A cancelled() callback that has a deadline, like start.CancelWatch."""

    def __init__(self, deadline: float | None, clock: Clock) -> None:
        self.deadline, self.clock, self.closed = deadline, clock, False

    def __call__(self) -> bool:
        return self.closed or (self.deadline is not None and self.clock() > self.deadline)


def stage_ctx(tmp_path: Path, pipe: Any, clock: Clock, watch: Callable[[], bool], seeds: tuple[int, ...] = (1, 2, 3),
              s_per_seed: int = 30) -> stages.StageContext:
    take = parse_job(plan_raw("http://127.0.0.1:1", seeds=seeds))
    object.__setattr__(take, "plan", type(take.plan)(seeds, s_per_seed))  # type: ignore[arg-type]
    return stages.StageContext(take, tmp_path, {}, {}, watch, clock, float("inf"), yue2_engine.Adapter(pipe), None, None)


def test_plan_stage_deadline_skips_unstarted(tmp_path: Path) -> None:
    clock = Clock(1000.0)
    # each seed takes 40 s; the deadline leaves room for the first (100 s left) and the second (60 s left), not the third (20 s < 30)
    pipe = fake(on_plan=lambda _kw, _c: clock.advance(40))
    out = plan_stage.run(stage_ctx(tmp_path, pipe, clock, Watch(1100.0, clock)))
    assert [(o.index, o.status, o.code) for o in out] == [(0, "ok", None), (1, "ok", None), (2, "error", "DEADLINE")]
    assert len(pipe.plan_calls) == 2
    assert not (tmp_path / "plan-02.tar").exists() and (tmp_path / "plan-01.tar").is_file()


def test_plan_stage_cancels_running_seed(tmp_path: Path) -> None:
    clock = Clock(1000.0)

    def slow(_kw: dict[str, Any], cancelled: Callable[[], bool]) -> None:
        clock.advance(500)  # runs past the deadline
        assert cancelled()
        raise InterruptedError("cancelled")  # what yue2 does at a token boundary

    pipe = fake(on_plan=slow)
    out = plan_stage.run(stage_ctx(tmp_path, pipe, clock, Watch(1100.0, clock)))
    assert [(o.status, o.code) for o in out] == [("error", "DEADLINE")] * 3
    assert len(pipe.plan_calls) == 1  # the later seeds never start
    assert not list(tmp_path.glob("plan-*"))  # no scratch left behind


def test_plan_stage_cancelled_partial_plan_is_deadline(tmp_path: Path) -> None:
    clock = Clock(1000.0)
    pipe = fake(plan_truncated=True, on_plan=lambda _kw, _c: clock.advance(500))
    out = plan_stage.run(stage_ctx(tmp_path, pipe, clock, Watch(1100.0, clock), seeds=(1,)))
    assert [(o.status, o.code) for o in out] == [("error", "DEADLINE")]


def test_plan_stage_closed_attempt_stops_every_seed(tmp_path: Path) -> None:
    clock = Clock()
    watch = Watch(None, clock)
    watch.closed = True
    pipe = fake()
    out = plan_stage.run(stage_ctx(tmp_path, pipe, clock, watch))
    assert [o.code for o in out] == ["DEADLINE"] * 3 and not pipe.plan_calls


def test_plan_stage_no_deadline_runs_everything(tmp_path: Path) -> None:
    out = plan_stage.run(stage_ctx(tmp_path, fake(), Clock(), Watch(None, Clock())))
    assert [o.status for o in out] == ["ok"] * 3


def test_plan_stage_all_failed_is_plans_failed(sink: Sink) -> None:
    out = run2(sink, plan_raw(sink.base), fake(plan_error=RuntimeError("boom")))
    assert err(out) == "PLANS_FAILED"
    assert [p["code"] for p in out["mm"]["plans"]] == ["PLAN_FAILED", "PLAN_FAILED"]
    assert "boom" not in json.dumps(out)


def test_plan_stage_one_failed_seed_keeps_the_others(sink: Sink) -> None:
    calls = {"n": 0}

    def sometimes(_kw: dict[str, Any], _c: Callable[[], bool]) -> None:
        calls["n"] += 1
        if calls["n"] == 1:
            raise ValueError("bad sampling")

    out = run2(sink, plan_raw(sink.base), fake(on_plan=sometimes))
    OUT2.validate(out)
    assert [(p["status"], p.get("code")) for p in out["plans"]] == [("error", "BAD_INPUT"), ("ok", None)]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["plan-01.tar", "mm-result.json"]


@pytest.mark.parametrize(("abc", "code"), [(None, "NO_SCORE"), ("  \n", "NO_SCORE"), ("X:" + "a" * (64 << 10), "SCORE_TOO_LARGE"),
                                           ("X:1\x00", "SCORE_TOO_LARGE")])
def test_plan_stage_unshippable_scores(tmp_path: Path, abc: str | None, code: str) -> None:
    out = plan_stage.run(stage_ctx(tmp_path, fake(plan_abc=abc), Clock(), Watch(None, Clock()), seeds=(1,)))
    assert [(o.status, o.code) for o in out] == [("error", code)]


def test_plan_bundle_names_and_sizes(sink: Sink, tmp_path: Path) -> None:
    out = run2(sink, plan_raw(sink.base, seeds=(1102,)), fake(plan_truncated=True))
    OUT2.validate(out)
    assert out["plans"][0]["truncated"] == {"abc": True, "semantic": False}
    got = members(uploaded(sink, tmp_path, "plan-00.tar"))
    assert list(got) == ["score.abc", "plan.json", "abc_tokens.npy", "prefix.npy", "plan_manifest.json"]
    assert got["score.abc"] == b"X:1\nK:C\nCDEF|\n"
    doc = json.loads(got["plan.json"])
    assert set(doc) == PLAN_JSON_KEYS  # the ingester refuses any other key
    assert doc["seed"] == 1102 and doc["cot"] == "full" and doc["truncated"] is True and doc["abc_tokens"] == 4
    assert isinstance(doc["seconds"], float) and doc["yue2"] and len(doc["yue2"]) <= 64
    assert got["abc_tokens.npy"].startswith(b"\x93NUMPY") and got["prefix.npy"].startswith(b"\x93NUMPY")
    assert json.loads(got["plan_manifest.json"]) == {n: hashlib.sha256(d).hexdigest() for n, d in got.items()
                                                     if n != "plan_manifest.json"}


def test_plan_bundle_is_a_plain_posix_tar(sink: Sink, tmp_path: Path) -> None:
    run2(sink, plan_raw(sink.base, seeds=(5,)), fake())
    p = uploaded(sink, tmp_path, "plan-00.tar")
    with tarfile.open(p, "r:") as tar:
        for m in tar.getmembers():
            assert (m.type, m.mode, m.mtime, m.uid, m.gid, m.pax_headers) == (tarfile.REGTYPE, 0o644, 0, 0, 0, {})


def test_plan_bundle_drops_oversize_optionals(tmp_path: Path) -> None:
    class Big:
        abc, abc_ids, truncated = "X:1\nK:C\nC|\n", [1], False

        def save(self, directory: Path) -> None:
            d = Path(directory)
            d.mkdir(parents=True)
            (d / "abc_tokens.npy").write_bytes(b"\x93NUMPY" + bytes(2 << 20))  # over 1 MiB: left out
            (d / "prefix.npy").write_bytes(b"")  # empty: left out
    data = plan_stage.build_bundle(Big(), {"cot": "melody"}, 1, 0.5, "v", tmp_path)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as tar:
        assert tar.getnames() == ["score.abc", "plan.json"]  # no manifest without an optional beside it


def test_plan_bundle_stays_under_the_tar_limit(tmp_path: Path) -> None:
    class Heavy:
        abc, abc_ids, truncated = "X:" + "a" * (60 << 10), None, False

        def save(self, directory: Path) -> None:
            d = Path(directory)
            d.mkdir(parents=True)
            for n in ("abc_tokens.npy", "prefix.npy"):
                (d / n).write_bytes(b"\x93NUMPY" + bytes((1 << 20) - 6))  # exactly 1 MiB each

    data = plan_stage.build_bundle(Heavy(), {"cot": "full"}, 1, 1.0, "v", tmp_path)
    assert len(data) <= 2 << 20
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as tar:
        assert json.loads(tar.extractfile("plan.json").read())["abc_tokens"] is None  # type: ignore[union-attr]


def test_plan_cap_only_with_staged_api(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(stages, "_REGISTRY", {})
    monkeypatch.setattr(plan_stage, "_probe", lambda: False)
    assert plan_stage.register_stage() is False and "plan" not in caps.advertised()
    monkeypatch.setattr(plan_stage, "_probe", lambda: True)
    assert plan_stage.register_stage() is True and "plan" in caps.advertised()
    assert stages.get(stages.PLAN) is not None


YUE2 = '''
from dataclasses import dataclass
@dataclass
class SymbolicPlan:
    request: object
    abc: str | None
    abc_ids: list
    prefix: list
    timing: dict
    truncated: bool = False
    def save(self, directory): pass
class YuE2Pipeline:
    {plan}
'''


def install_staged_yue2(site: Path, monkeypatch: pytest.MonkeyPatch, plan: str = "def plan(self, style=None): pass") -> None:
    """A yue2 package on sys.path that only the probe reads (the worker never imports it)."""
    pkg = site / "yue2"
    pkg.mkdir(parents=True)
    (pkg / "__init__.py").write_text("")
    (pkg / "pipeline.py").write_text(YUE2.format(plan=plan))
    monkeypatch.syspath_prepend(str(site))
    monkeypatch.delitem(sys.modules, "yue2", raising=False)


@pytest.mark.parametrize(("plan", "want"), [("def plan(self, style=None): pass", True), ("def other(self): pass", False)])
def test_staged_api_probe_reads_the_installed_source(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, plan: str, want: bool) -> None:
    install_staged_yue2(tmp_path, monkeypatch, plan)
    assert plan_stage.staged_api_present() is want


def test_staged_api_probe_refuses_a_missing_or_broken_yue2(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    assert plan_stage.staged_api_present() is False  # not installed in the unit-test environment
    pkg = tmp_path / "yue2"
    pkg.mkdir()
    (pkg / "__init__.py").write_text("")
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.delitem(sys.modules, "yue2", raising=False)
    try:
        assert plan_stage.staged_api_present() is False  # no pipeline.py
        (pkg / "pipeline.py").write_text("def broken(:\n")
        assert plan_stage.staged_api_present() is False  # unparsable
        (pkg / "pipeline.py").write_text("class SymbolicPlan:\n    abc: str\n")
        assert plan_stage.staged_api_present() is False  # no YuE2Pipeline.plan, no save
    finally:
        sys.modules.pop("yue2", None)


def test_yue2_version_falls_back_to_the_commit(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(plan_stage.importlib.metadata, "version",
                        lambda _d: (_ for _ in ()).throw(plan_stage.importlib.metadata.PackageNotFoundError()))
    assert plan_stage.yue2_version({"MM_YUE2_COMMIT": "a" * 40}) == "a" * 12
    assert plan_stage.yue2_version({}) == "unknown"
    monkeypatch.setattr(plan_stage.importlib.metadata, "version", lambda _d: "0.1.6")
    assert plan_stage.yue2_version({}) == "0.1.6"


def test_plan_stage_needs_a_planning_engine(tmp_path: Path) -> None:
    ctx = stage_ctx(tmp_path, fake(), Clock(), Watch(None, Clock()))
    bare = stages.StageContext(ctx.take, tmp_path, {}, {}, ctx.cancelled, ctx.clock, 1.0, object(), None, None)
    with pytest.raises(Exception, match="planning engine"):
        plan_stage.run(bare)


def test_a_render_is_unchanged_by_the_plan_methods(sink: Sink) -> None:
    pipe = fake()
    out = run2(sink, raw2(sink.base), pipe)
    OUT2.validate(out)
    assert not pipe.plan_calls and len(pipe.calls) == 1
