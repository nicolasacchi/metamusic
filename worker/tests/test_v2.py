"""mm.worker/2 in the Python worker (C321, C322, G1-F4): parser against the schema, kinds, stages, attach, deadlines,
caps, selftest and the artifact tables. /1 behaviour is pinned by the unchanged existing suites."""

from __future__ import annotations

import base64
import copy
import hashlib
import json
import re
import sys
from collections.abc import Callable, Iterator
from importlib import resources
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from mmworker import caps, contract, handler, selftest, stages
from mmworker.contract import CONTRACT_V2, artifacts_v2, parse_job, parse_v2
from mmworker.errors import WorkerError
from mmworker.handler import deadlines, process
from mmworker.start import StartClient
from mmworker.upload import Uploader

from .conftest import WEIGHTS, Clock, Post, Sink, fake, make_raw

BASE = "https://mm.example.com"
ROOT = Path(__file__).resolve().parents[1]


def schema(name: str) -> jsonschema.Draft202012Validator:
    return jsonschema.Draft202012Validator(json.loads(resources.files("mmworker").joinpath(f"schema/{name}").read_text()))


IN2, OUT2 = schema("mm-worker-2.input.schema.json"), schema("mm-worker-2.output.schema.json")
SHA = "a" * 64


def uploads(base: str, kind: str = "render", ears: bool = False, keep: bool = False, seeds: int = 0) -> dict[str, Any]:
    return {n: {"url": f"{base}/{n}?sig=x", "max_bytes": m} for n, m, _ in artifacts_v2(kind, ears, keep, seeds)}


def raw2(base: str = BASE, shape: str = "render", **over: Any) -> dict[str, Any]:
    ears = over.get("ears")
    seeds = min(len(over["plan"]["seeds"]), 16) if "plan" in over else 0
    raw: dict[str, Any] = {
        "contract": CONTRACT_V2, "take_id": "take-1", "attempt_id": "attempt-1", "engine": "yue2",
        "timeout_s": 296, "upload_budget_s": 60, "deadline_unix": 1_800_100_000,
        "start": {"url": f"{base}/start?sig=x"},
        "uploads": uploads(base, shape, ears is not None, bool(ears and ears.get("keep_vocals")), seeds),
        "expect": {"weights": dict(WEIGHTS)},
    }
    if shape != "analyze":
        raw["request"] = {"cot": "full", "id": "syn-a-s1", "lyrics": "[Verse]\nla la", "seed": 1102, "style": "pop"}
    if shape != "render":
        raw["kind"] = shape
    raw.update(over)
    return raw


EARS = {"lang": "auto", "budget_s": 110, "stages": ["separate", "asr", "transcribe"], "keep_vocals": False}
SRC = {"src_audio": {"url": f"{BASE}/src?sig=x", "sha256": SHA, "bytes": 1000}}


def analyze_raw(base: str = BASE, **over: Any) -> dict[str, Any]:
    return raw2(base, "analyze", ears=dict(EARS), inputs=SRC, **over)


def plan_raw(base: str = BASE, seeds: tuple[int, ...] = (1, 2), **over: Any) -> dict[str, Any]:
    return raw2(base, "plan", plan={"seeds": list(seeds), "s_per_seed": 30}, **over)


# ---- parser table -------------------------------------------------------------------------------------------------

def mut(f: Callable[[dict[str, Any]], Any], make: Callable[[], dict[str, Any]] = raw2) -> dict[str, Any]:
    d = make()
    f(d)
    return d


def _attach(n: int, size: int = 4) -> dict[str, Any]:
    return {f"f{i}.txt": {"sha256": SHA, "data_base64": base64.b64encode(b"x" * size).decode()} for i in range(n)}


# (label, document, accepted, schema_can_say)
ROWS: list[tuple[str, dict[str, Any], bool, bool]] = [
    ("plain render", raw2(), True, True),
    ("explicit kind render", raw2(**{"kind": "render"}), True, True),
    ("render with ears", raw2(ears=dict(EARS)), True, True),
    ("render with ears keep", raw2(ears={**EARS, "keep_vocals": True}), True, True),
    ("render with attach", raw2(attach=_attach(2)), True, True),
    ("render min_audio_s", raw2(min_audio_s=60), True, True),
    ("analyze", analyze_raw(), True, True),
    ("analyze keep", raw2(shape="analyze", ears={**EARS, "keep_vocals": True}, inputs=SRC), True, True),
    ("plan 2 seeds", plan_raw(), True, True),
    ("plan 16 seeds", plan_raw(seeds=tuple(range(16))), True, True),
    ("unknown top key", mut(lambda d: d.update(zzz=1)), False, True),
    ("unknown kind", raw2(**{"kind": "mix"}), False, True),
    ("wrong contract", mut(lambda d: d.update(contract="mm.worker/3")), False, True),
    ("engine acestep15 on /2", mut(lambda d: d.update(engine="acestep15")), False, True),
    ("unknown key in start", mut(lambda d: d["start"].update(zzz=1)), False, True),
    ("unknown key in ears", raw2(ears={**EARS, "zzz": 1}), False, True),
    ("ears without stages", raw2(ears={"budget_s": 10}), False, True),
    ("ears stage duplicate", raw2(ears={**EARS, "stages": ["asr", "asr"]}), False, True),
    ("ears stage unknown", raw2(ears={**EARS, "stages": ["mix"]}), False, True),
    ("ears budget 0", raw2(ears={**EARS, "budget_s": 0}), False, True),
    ("ears lang bad", raw2(ears={**EARS, "lang": "English"}), False, True),
    ("analyze without inputs", mut(lambda d: d.pop("inputs"), analyze_raw), False, True),
    ("analyze without ears", mut(lambda d: d.pop("ears"), analyze_raw), False, True),
    ("analyze with request", mut(lambda d: d.update(request={"a": 1}), analyze_raw), False, True),
    ("analyze with plan", mut(lambda d: d.update(plan={"seeds": [1], "s_per_seed": 5}), analyze_raw), False, True),
    ("analyze with audio.flac upload", mut(lambda d: d["uploads"].update({"audio.flac": uploads(BASE)["audio.flac"]}),
                                           analyze_raw), False, True),
    ("plan without seeds", plan_raw(seeds=()), False, True),
    ("plan 17 seeds", plan_raw(seeds=tuple(range(17))), False, True),
    ("plan duplicate seeds", plan_raw(seeds=(3, 3)), False, True),
    ("plan seed bool", mut(lambda d: d["plan"].update(seeds=[True]), plan_raw), False, False),
    ("plan s_per_seed 0", mut(lambda d: d["plan"].update(s_per_seed=0), plan_raw), False, True),
    ("plan with ears", mut(lambda d: d.update(ears=dict(EARS)), plan_raw), False, True),
    ("plan without request", mut(lambda d: d.pop("request"), plan_raw), False, True),
    ("plan upload missing a bundle", mut(lambda d: d["uploads"].pop("plan-01.tar"), plan_raw), False, True),
    ("plan upload extra bundle", mut(lambda d: d["uploads"].update({"plan-05.tar": d["uploads"]["plan-00.tar"]}),
                                     plan_raw), False, True),
    ("inputs on a yue2 render", mut(lambda d: d.update(inputs=SRC)), False, True),
    ("render without request", mut(lambda d: d.pop("request")), False, True),
    ("attach 17 files", raw2(attach=_attach(17)), False, True),
    ("attach bad name", raw2(attach={"../x": _attach(1)["f0.txt"]}), False, True),
    ("attach bad base64", raw2(attach={"a.txt": {"sha256": SHA, "data_base64": "!!!"}}), False, True),
    ("attach bad sha", raw2(attach={"a.txt": {"sha256": "zz", "data_base64": ""}}), False, True),
    ("attach over 1 MiB decoded", raw2(attach=_attach(2, (1 << 19) + 1)), False, False),
    ("analysis.json upload without ears", mut(lambda d: d["uploads"].update(
        {"analysis.json": {"url": f"{BASE}/a", "max_bytes": 10}})), False, True),
    ("ears without analysis.json upload", mut(lambda d: d["uploads"].pop("analysis.json"),
                                              lambda: raw2(ears=dict(EARS))), False, True),
    ("stem upload without keep_vocals", mut(lambda d: d["uploads"].update(
        {"stem-vocals.flac": {"url": f"{BASE}/s", "max_bytes": 10}}), lambda: raw2(ears=dict(EARS))), False, True),
    ("keep_vocals without stem upload", mut(lambda d: d["uploads"].pop("stem-vocals.flac"),
                                            lambda: raw2(ears={**EARS, "keep_vocals": True})), False, True),
    ("missing mm-result.json", mut(lambda d: d["uploads"].pop("mm-result.json")), False, True),
    ("upload over its cap", mut(lambda d: d["uploads"]["score.abc"].update(max_bytes=(1 << 20) + 1)), False, True),
    ("fallback without fallback_after_s", mut(lambda d: d["uploads"]["audio.flac"].update(
        fallback={"url": f"{BASE}/fb"})), False, True),
    ("fallback_after_s without fallback", raw2(fallback_after_s=120), False, True),
    ("fallback both", mut(lambda d: (d.update(fallback_after_s=120),
                                     d["uploads"]["audio.flac"].update(fallback={"url": f"{BASE}/fb"}))), True, True),
    ("fallback_after_s too small", mut(lambda d: (d.update(fallback_after_s=59),
                                                  d["uploads"]["audio.flac"].update(fallback={"url": f"{BASE}/fb"}))),
     False, True),
    ("bad start url", mut(lambda d: d["start"].update(url="ftp://x")), False, True),
    ("empty weights", mut(lambda d: d["expect"].update(weights={})), False, False),
    ("request empty", mut(lambda d: d.update(request={})), False, True),
]


def test_parse_v2_table() -> None:
    assert len(ROWS) >= 25
    for label, doc, accepted, schema_says in ROWS:
        try:
            parse_v2(copy.deepcopy(doc))
            got = True
        except WorkerError as e:
            assert e.code in ("BAD_INPUT", "CONTRACT_VERSION"), label
            got = False
        assert got is accepted, label
        if schema_says:
            assert IN2.is_valid(doc) is accepted, f"schema disagrees: {label}"


def test_parse_v2_fields() -> None:
    t = parse_job(raw2(ears=dict(EARS), attach=_attach(1), min_audio_s=30))
    assert t.contract == CONTRACT_V2 and t.kind == "render" and t.min_audio_s == 30
    assert t.ears == contract.EarsSpec("auto", 110, ("separate", "asr", "transcribe"), False)
    assert t.attach["f0.txt"].data == b"xxxx"
    assert "sig" not in repr(t)
    a = parse_job(analyze_raw())
    assert a.kind == "analyze" and a.request == {} and a.inputs["src_audio"].bytes == 1000
    p = parse_job(plan_raw())
    assert p.plan == contract.PlanSpec((1, 2), 30)
    assert parse_job(raw2(ears={"budget_s": 5, "stages": ["asr"]})).ears.lang == "auto"  # type: ignore[union-attr]


def test_parse_job_not_object_and_version() -> None:
    with pytest.raises(WorkerError):
        parse_job("x")
    with pytest.raises(WorkerError) as e:
        parse_v2("x")
    assert e.value.code == "BAD_INPUT"
    with pytest.raises(WorkerError) as e2:
        parse_v2({"contract": "mm.worker/1"})
    assert e2.value.code == "CONTRACT_VERSION"
    with pytest.raises(WorkerError) as e3:
        parse_job(mut(lambda d: d.update(contract="mm.worker/3")))
    assert e3.value.code == "CONTRACT_VERSION"


@pytest.mark.parametrize("patch", [
    lambda d: d.update(attach={"a.txt": "x"}),
    lambda d: d.update(attach="x"),
    lambda d: d.update(ears="x"),
    lambda d: d.update(ears={**EARS, "keep_vocals": "yes"}),
    lambda d: d.update(ears={**EARS, "stages": "asr"}),
    lambda d: d.update(take_id="a b"),
    lambda d: d.update(min_audio_s=361),
    lambda d: d.update(uploads={"audio.flac": "x"}),
    lambda d: d["uploads"]["audio.flac"].update(fallback="x"),
    lambda d: d.update(expect="x"),
    lambda d: d["expect"]["weights"].update({"a": "short"}),
    lambda d: d.update(start="x"),
])
def test_parse_v2_type_errors(patch: Callable[[dict[str, Any]], None]) -> None:
    with pytest.raises(WorkerError) as e:
        parse_v2(mut(patch))
    assert e.value.code == "BAD_INPUT"


def test_parse_v2_plan_and_input_type_errors() -> None:
    for bad in ({"seeds": "x", "s_per_seed": 1}, {"seeds": [1.5], "s_per_seed": 1}, "x", {"seeds": [1]}):
        with pytest.raises(WorkerError):
            parse_v2(mut(lambda d: d.update(plan=bad), plan_raw))
    for bad in ("x", {"src_audio": "x"}, {"src_audio": {"url": f"{BASE}/x", "sha256": "no", "bytes": 1}}):
        with pytest.raises(WorkerError):
            parse_v2(mut(lambda d: d.update(inputs=bad), analyze_raw))


def test_v1_documents_unchanged() -> None:
    """A /1 document parses through parse_job exactly as through parse_input; a /1 parser still refuses /2."""
    raw = make_raw(BASE)
    assert parse_job(raw) == contract.parse_input(raw)
    t = parse_job(raw)
    assert (t.contract, t.kind, t.ears, t.plan, t.attach) == (contract.CONTRACT_V1, "render", None, None, {})
    with pytest.raises(WorkerError) as e:
        contract.parse_input(raw2())
    assert e.value.code == "CONTRACT_VERSION"
    assert contract.CONTRACTS == ("mm.worker/1", "mm.worker/2") and contract.CONTRACT == "mm.worker/1"
    with pytest.raises(WorkerError):  # /1 stays strict about the /2 keys
        contract.parse_input({**make_raw(BASE), "kind": "render"})


# ---- the artifact tables ------------------------------------------------------------------------------------------

def test_artifacts_v2_equal_go_table() -> None:
    doc = json.loads((ROOT / "contract/testdata/artifacts-v2.json").read_text())
    assert doc["contract"] == CONTRACT_V2
    for s in doc["shapes"]:
        got = artifacts_v2(s["kind"], s["ears"], s["keep_vocals"], s["seeds"])
        want = tuple((a["name"], a["max_bytes"], a["required"]) for a in s["artifacts"])
        assert got == want, s["name"]


def test_artifacts_v2_schema_enums() -> None:
    names = set(IN2.schema["properties"]["uploads"]["properties"])
    out = set(OUT2.schema["properties"]["artifacts"]["properties"])
    assert names - {"mm-result.json"} == out and names == set(contract.MAX_BYTES_V2)
    for n, m in contract.MAX_BYTES_V2.items():
        assert IN2.schema["properties"]["uploads"]["properties"][n]["properties"]["max_bytes"]["maximum"] == m


def test_error_codes_equal_output_schema() -> None:
    from mmworker.errors import CODES

    enum = set(OUT2.schema["properties"]["error"]["oneOf"][1]["properties"]["code"]["enum"])
    assert enum == set(CODES) - {"TASK_UNAVAILABLE"}  # ACE-Step (engine 2) never speaks /2


# ---- running a /2 take ---------------------------------------------------------------------------------------------

@pytest.fixture(autouse=True)
def _clean_registry(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    monkeypatch.setattr(stages, "_REGISTRY", {})
    yield


def ok_ears(ctx: stages.StageContext) -> stages.EarsOutcome:
    (ctx.tmp / "analysis.json").write_bytes(b'{"contract":"mm.ears-b/1"}')
    if ctx.take.ears and ctx.take.ears.keep_vocals:
        (ctx.tmp / "stem-vocals.flac").write_bytes(b"stem")
    return stages.EarsOutcome("ok", seconds=1.5)


def run2(sink: Sink, raw: dict[str, Any], pipe: Any = None, *, clock: Clock | None = None,
         fetch: Callable[..., dict[str, Path]] | None = None, monkeypatch: pytest.MonkeyPatch | None = None) -> Any:
    clock = clock or Clock()
    take = parse_job(raw)
    if fetch is not None and monkeypatch is not None:
        monkeypatch.setattr(handler.inputs_mod, "fetch", fetch)
    return process(take, pipe if pipe is not None else fake(), Uploader(clock=clock, sleep=clock.sleep), clock,
                   start=StartClient(take.start_url, post=Post(clock, 200), clock=clock, sleep=clock.sleep),
                   job_id="job-1", spawn=lambda fn: fn(), sleep=clock.sleep)


def err(out: dict[str, Any]) -> str:
    assert "error" in out and "mm" in out
    OUT2.validate(out["mm"])
    return str(out["mm"]["error"]["code"])


def test_v2_render_plain_same_artifacts_as_v1(sink: Sink) -> None:
    out2 = run2(sink, raw2(sink.base))
    puts2 = [r.path.lstrip("/") for r in sink.puts()]
    sink.requests.clear()
    from .conftest import make_rig

    out1 = make_rig(sink).run(fake())
    puts1 = [r.path.lstrip("/") for r in sink.puts()]
    OUT2.validate(out2)
    assert puts1 == puts2 and puts2[-1] == "mm-result.json"
    assert set(out1["artifacts"]) == set(out2["artifacts"])
    assert out2["kind"] == "render" and out2["contract"] == CONTRACT_V2 and out2["caps"] == ["agent", "fallback", "v2"]
    assert "ears" not in out2 and "plans" not in out2 and out1["contract"] == "mm.worker/1"
    assert "kind" not in out1 and "caps" not in out1
    assert json.loads(sink.puts("/mm-result.json")[0].body) == out1


def test_v2_kind_unavailable_before_gpu(sink: Sink, monkeypatch: pytest.MonkeyPatch) -> None:
    fetched: list[str] = []
    monkeypatch.setattr(handler.inputs_mod, "fetch", lambda *a, **k: fetched.append("x") or {})
    cases = [
        (analyze_raw(sink.base), "analyze"),
        (plan_raw(sink.base), "plan"),
        (raw2(sink.base, ears=dict(EARS)), "ears"),
    ]
    for raw, what in cases:
        pipe = fake()
        out = process(parse_job(raw), pipe, Uploader(), Clock(), start=StartClient(
            f"{sink.base}/start", post=Post(Clock(), 200)), job_id="j", spawn=lambda fn: fn())
        assert err(out) == "KIND_UNAVAILABLE", what
        assert pipe.calls == [] and fetched == []
        assert what in out["error"]
    # the start handshake ran, nothing was rendered, and only mm-result.json went up
    assert {r.path.lstrip("/") for r in sink.puts()} == {"mm-result.json"}


def test_v2_requested_ears_stage_must_be_available(sink: Sink) -> None:
    stages.register("ears", ok_ears, ears_stages=frozenset({"asr"}))
    pipe = fake()
    out = run2(sink, raw2(sink.base, ears=dict(EARS)), pipe)
    assert err(out) == "KIND_UNAVAILABLE" and "separate" in out["error"] and pipe.calls == []
    assert run2(sink, raw2(sink.base, ears={**EARS, "stages": ["asr"]}))["status"] == "ok"


def test_v2_render_with_ears(sink: Sink) -> None:
    stages.register("ears", ok_ears)
    out = run2(sink, raw2(sink.base, ears={**EARS, "keep_vocals": True}))
    OUT2.validate(out)
    assert out["ears"] == {"status": "ok", "seconds": 1.5} and out["caps"] == ["agent", "ears", "fallback", "v2"]
    assert {"analysis.json", "stem-vocals.flac", "audio.flac"} <= set(out["artifacts"])
    puts = [r.path.lstrip("/") for r in sink.puts()]
    assert puts.index("audio.flac") < puts.index("stem-vocals.flac") < puts.index("analysis.json") < puts.index(
        "mm-result.json")


def test_v2_ears_runs_after_save_and_gets_the_audio(sink: Sink) -> None:
    seen: dict[str, Any] = {}

    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        seen["audio"] = ctx.audio and ctx.audio.is_file()
        seen["timeout"] = ctx.timeout_s
        seen["cancelled"] = ctx.cancelled()
        return ok_ears(ctx)

    stages.register("ears", stage)
    assert run2(sink, raw2(sink.base, ears=dict(EARS)))["ears"]["status"] == "ok"
    assert seen == {"audio": True, "timeout": 110.0, "cancelled": False}


@pytest.mark.parametrize(("stage", "status", "code"), [
    (lambda ctx: (_ for _ in ()).throw(RuntimeError("boom with /secret/path")), "error", "STAGE_FAILED"),
    (lambda ctx: (_ for _ in ()).throw(WorkerError("PIPELINE_ERROR", "x")), "error", "PIPELINE_ERROR"),
    (lambda ctx: stages.EarsOutcome("error", "OOM", 2.0), "error", "OOM"),
    (lambda ctx: stages.EarsOutcome("error"), "error", "STAGE_FAILED"),
    (lambda ctx: "garbage", "error", "PIPELINE_ERROR"),
    (lambda ctx: stages.EarsOutcome("ok"), "error", "NO_DOCUMENT"),
])
def test_v2_ears_failure_never_fails_the_take(sink: Sink, stage: Any, status: str, code: str) -> None:
    stages.register("ears", stage)
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    OUT2.validate(out)
    assert out["status"] == "ok" and out["ears"]["status"] == status and out["ears"]["code"] == code
    assert "audio.flac" in out["artifacts"] and "analysis.json" not in out["artifacts"]
    assert "secret" not in json.dumps(out)


def test_v2_ears_error_with_partial_document_still_uploads_it(sink: Sink) -> None:
    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        (ctx.tmp / "analysis.json").write_bytes(b"{}")
        (ctx.tmp / "stem-vocals.flac").write_bytes(b"x")  # keep_vocals is off: never uploaded
        return stages.EarsOutcome("error", "ASR_FAILED")

    stages.register("ears", stage)
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    assert out["ears"]["code"] == "ASR_FAILED" and "analysis.json" in out["artifacts"]
    assert "stem-vocals.flac" not in out["artifacts"]


def test_v2_ears_deadline_skips_and_take_uploads(sink: Sink) -> None:
    clock = Clock()
    calls: list[int] = []
    stages.register("ears", lambda ctx: calls.append(1) or ok_ears(ctx))
    pipe = fake(on_call=lambda kw, c: clock.advance(3000))  # generation ends long after ears_deadline - budget/3
    raw = raw2(sink.base, ears=dict(EARS), timeout_s=2700, upload_budget_s=60, deadline_unix=int(clock()) + 99999)
    out = run2(sink, raw, pipe, clock=clock)
    assert out["status"] == "ok" and out["ears"] == {"status": "skipped", "code": "DEADLINE", "seconds": 0.0}
    assert calls == [] and "audio.flac" in out["artifacts"] and "analysis.json" not in out["artifacts"]
    OUT2.validate(out)


def test_v2_ears_budget_capped_by_time_left(sink: Sink) -> None:
    clock = Clock()
    seen: list[float] = []
    stages.register("ears", lambda ctx: seen.append(ctx.timeout_s) or ok_ears(ctx))
    t0 = clock()
    # upload_deadline = t0 + 296 - 10 = t0 + 286; ears_deadline = t0 + 226; generation ends at t0 + 185 (< 226 - 36.7)
    pipe = fake(on_call=lambda kw, c: clock.advance(185))
    run2(sink, raw2(sink.base, ears=dict(EARS), deadline_unix=int(t0) + 99999), pipe, clock=clock)
    assert seen == [pytest.approx(41.0)]


def test_v2_ears_closed_attempt_stops(sink: Sink) -> None:
    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        ctx.cancelled.closed = True  # type: ignore[attr-defined]
        return ok_ears(ctx)

    stages.register("ears", stage)
    assert err(run2(sink, raw2(sink.base, ears=dict(EARS)))) == "ATTEMPT_CLOSED"


def test_v2_deadlines_ears_budget(sink: Sink) -> None:
    t0 = 1_800_000_000.0
    env: dict[str, str] = {}
    plain = parse_v2(raw2(timeout_s=296, upload_budget_s=60, deadline_unix=int(t0) + 99999))
    assert deadlines(plain, t0, env) == (t0 + 286, t0 + 226)  # exactly /1
    ears = parse_v2(raw2(ears=dict(EARS), timeout_s=296, upload_budget_s=60, deadline_unix=int(t0) + 99999))
    up, gen = deadlines(ears, t0, env)
    assert (gen - t0, handler.ears_deadline(ears, up) - t0, up - t0) == (116, 226, 286)
    assert deadlines(ears, t0, {"MM_WORKER_DEADLINE": "provider"}) == (up, None)
    near = parse_v2(raw2(timeout_s=2700, upload_budget_s=60, deadline_unix=int(t0) + 200))
    assert deadlines(near, t0, env)[0] == t0 + 140  # the provider deadline wins, as in /1


def test_v2_analyze_fetches_before_stage(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    order: list[str] = []
    audio = _wav(tmp_path / "in.flac")

    def fetch(inputs: Any, deadline: float, tmp: Path, **kw: Any) -> dict[str, Path]:
        order.append("fetch")
        dst = tmp / "src_audio"
        dst.write_bytes(audio.read_bytes())
        return {"src_audio": dst}

    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        order.append("stage")
        assert ctx.audio == ctx.inputs["src_audio"] and ctx.adapter is not None
        (ctx.tmp / "analysis.json").write_bytes(b"{}")
        return stages.EarsOutcome("ok", seconds=2.0)

    stages.register("analyze", stage)
    pipe = fake()
    out = run2(sink, analyze_raw(sink.base), pipe, fetch=fetch, monkeypatch=monkeypatch)
    OUT2.validate(out)
    assert order == ["fetch", "stage"] and pipe.calls == []
    assert out["kind"] == "analyze" and out["truncated"] is None and out["audio_seconds"] == pytest.approx(1.0)
    assert out["ears"] == {"status": "ok", "seconds": 2.0} and out["caps"] == ["agent", "analyze", "fallback", "v2"]
    assert set(out["artifacts"]) == {"analysis.json"}
    assert [r.path.lstrip("/") for r in sink.puts()] == ["analysis.json", "mm-result.json"]


def _wav(path: Path) -> Path:
    from mmworker.pipeline import _flac_bytes

    path.write_bytes(_flac_bytes())
    return path


def _fetcher(tmp_path: Path) -> Callable[..., dict[str, Path]]:
    audio = _wav(tmp_path / "in.flac")

    def fetch(inputs: Any, deadline: float, tmp: Path, **kw: Any) -> dict[str, Path]:
        dst = tmp / "src_audio"
        dst.write_bytes(audio.read_bytes())
        return {"src_audio": dst}

    return fetch


def test_v2_analyze_failure_uploads_document_and_errors(sink: Sink, monkeypatch: pytest.MonkeyPatch,
                                                        tmp_path: Path) -> None:
    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        (ctx.tmp / "analysis.json").write_bytes(b'{"status":"error"}')
        return stages.EarsOutcome("error", "OOM")

    stages.register("analyze", stage)
    out = run2(sink, analyze_raw(sink.base), fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)
    assert err(out) == "ANALYZE_FAILED"
    assert out["mm"]["ears"]["code"] == "OOM" and "analysis.json" in out["mm"]["artifacts"]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["analysis.json", "mm-result.json"]


def test_v2_analyze_missing_document_and_duration(sink: Sink, monkeypatch: pytest.MonkeyPatch,
                                                  tmp_path: Path) -> None:
    stages.register("analyze", lambda ctx: stages.EarsOutcome("ok"))
    assert err(run2(sink, analyze_raw(sink.base), fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)) == \
        "ARTIFACT_MISSING"

    def no_audio(inputs: Any, deadline: float, tmp: Path, **kw: Any) -> dict[str, Path]:
        (tmp / "src_audio").write_bytes(b"not audio")
        return {"src_audio": tmp / "src_audio"}

    def stage(ctx: stages.StageContext) -> stages.EarsOutcome:
        (ctx.tmp / "analysis.json").write_bytes(b"{}")
        return stages.EarsOutcome("ok")

    monkeypatch.setattr(stages, "_REGISTRY", {})
    stages.register("analyze", stage)
    assert err(run2(sink, analyze_raw(sink.base), fetch=no_audio, monkeypatch=monkeypatch)) == "PIPELINE_ERROR"
    # a stage that knows the duration (an mp3 reference) supplies it
    monkeypatch.setattr(stages, "_REGISTRY", {})
    stages.register("analyze", lambda ctx: (stage(ctx), stages.EarsOutcome("ok", audio_seconds=12.5))[1])
    out = run2(sink, analyze_raw(sink.base), fetch=no_audio, monkeypatch=monkeypatch)
    assert out["audio_seconds"] == 12.5


def test_v2_analyze_input_failure_passes_through(sink: Sink, monkeypatch: pytest.MonkeyPatch) -> None:
    stages.register("analyze", lambda ctx: stages.EarsOutcome("ok"))

    def fetch(*a: Any, **k: Any) -> dict[str, Path]:
        raise WorkerError("INPUT_MISMATCH", "src_audio: sha256 differs")

    assert err(run2(sink, analyze_raw(sink.base), fetch=fetch, monkeypatch=monkeypatch)) == "INPUT_MISMATCH"


def test_v2_analyze_stage_timeout_and_closed(sink: Sink, monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    def slow(ctx: stages.StageContext) -> stages.EarsOutcome:
        ctx.cancelled()
        raise RuntimeError("late")

    stages.register("analyze", slow)
    clock = Clock()
    raw = analyze_raw(sink.base, timeout_s=2700, deadline_unix=int(clock()) + 99999)
    out = run2(sink, raw, clock=clock, fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)
    assert err(out) == "ANALYZE_FAILED" and out["mm"]["ears"]["code"] == "STAGE_FAILED"
    # no time left for the stage: DEADLINE without calling it
    monkeypatch.setattr(stages, "_REGISTRY", {})
    called: list[int] = []
    stages.register("analyze", lambda ctx: called.append(1) or stages.EarsOutcome("ok"))
    raw = analyze_raw(sink.base, timeout_s=2700, deadline_unix=int(clock()) + 99999)
    clock2 = Clock()

    def fetch_slow(inputs: Any, deadline: float, tmp: Path, **kw: Any) -> dict[str, Path]:
        clock2.advance(5000)
        return _fetcher(tmp_path)(inputs, deadline, tmp)

    out = run2(sink, raw, clock=clock2, fetch=fetch_slow, monkeypatch=monkeypatch)
    assert err(out) == "ANALYZE_FAILED" and out["mm"]["ears"]["code"] == "DEADLINE" and called == []

    monkeypatch.setattr(stages, "_REGISTRY", {})

    def closing(ctx: stages.StageContext) -> stages.EarsOutcome:
        ctx.cancelled.closed = True  # type: ignore[attr-defined]
        return stages.EarsOutcome("ok")

    stages.register("analyze", closing)
    out = run2(sink, analyze_raw(sink.base), fetch=_fetcher(tmp_path), monkeypatch=monkeypatch)
    assert err(out) == "ATTEMPT_CLOSED"


def test_v2_attach_written_and_verified(sink: Sink) -> None:
    body = b"X:1\nK:C\nCDEF|\n"
    att = {"score.abc": {"sha256": hashlib.sha256(body).hexdigest(), "data_base64": base64.b64encode(body).decode()}}
    seen: dict[str, Any] = {}
    stages.register("ears", lambda ctx: seen.update(files={k: v.read_bytes() for k, v in ctx.attach.items()},
                                                    names=sorted(p.name for p in ctx.tmp.iterdir())) or ok_ears(ctx))
    out = run2(sink, raw2(sink.base, attach=att, ears=dict(EARS)))
    assert out["status"] == "ok" and seen["files"] == {"score.abc": body}
    assert "attach" in seen["names"]
    assert "score.abc" not in out["artifacts"], "an attachment is never collected as an artifact"
    assert not sink.puts("/score.abc")
    sink.requests.clear()
    bad = {"score.abc": {**att["score.abc"], "sha256": "0" * 64}}
    pipe = fake()
    res = run2(sink, raw2(sink.base, attach=bad), pipe)
    assert err(res) == "ATTACH_MISMATCH" and pipe.calls == []
    assert "0" * 64 not in res["error"]


def test_v2_plan_kind(sink: Sink) -> None:
    def stage(ctx: stages.StageContext) -> list[stages.PlanOutcome]:
        assert ctx.take.plan and ctx.adapter is not None and ctx.params is not None
        (ctx.tmp / "plan-00.tar").write_bytes(b"tar0")
        return [stages.PlanOutcome(1, 2, "error", "OOM"),
                stages.PlanOutcome(0, 1, "ok", truncated={"abc": False, "semantic": True})]

    stages.register("plan", stage)
    pipe = fake()
    out = run2(sink, plan_raw(sink.base), pipe)
    OUT2.validate(out)
    assert out["kind"] == "plan" and out["audio_seconds"] is None and out["truncated"] is None
    assert out["plans"] == [{"index": 0, "seed": 1, "status": "ok", "bundle": "plan-00.tar",
                             "truncated": {"abc": False, "semantic": True}},
                            {"index": 1, "seed": 2, "status": "error", "code": "OOM"}]
    assert set(out["artifacts"]) == {"plan-00.tar"} and out["caps"] == ["agent", "fallback", "plan", "v2"]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["plan-00.tar", "mm-result.json"]


def test_v2_plan_all_failed(sink: Sink) -> None:
    stages.register("plan", lambda ctx: [stages.PlanOutcome(0, 1, "error"), stages.PlanOutcome(1, 2, "error", "X")])
    out = run2(sink, plan_raw(sink.base))
    assert err(out) == "PLANS_FAILED"
    assert [p["code"] for p in out["mm"]["plans"]] == ["PLAN_FAILED", "X"]
    assert [r.path.lstrip("/") for r in sink.puts()] == ["mm-result.json"]


@pytest.mark.parametrize("outcomes", [
    [stages.PlanOutcome(0, 1, "ok")],  # seed 2 not reported
    [stages.PlanOutcome(0, 9, "ok"), stages.PlanOutcome(1, 2, "ok")],  # wrong seed
    [stages.PlanOutcome(0, 1, "ok"), stages.PlanOutcome(0, 1, "ok")],  # duplicate index
    [stages.PlanOutcome(5, 1, "ok"), stages.PlanOutcome(1, 2, "ok")],  # index out of range
    None,  # type: ignore[list-item]
    "junk",  # type: ignore[list-item]
    [object(), object()],  # type: ignore[list-item]
    [stages.PlanOutcome(0, 1, "weird"), stages.PlanOutcome(1, 2, "ok")],  # unknown status
])
def test_v2_plan_bad_stage_report(sink: Sink, outcomes: list[stages.PlanOutcome]) -> None:
    def stage(ctx: stages.StageContext) -> list[stages.PlanOutcome]:
        (ctx.tmp / "plan-00.tar").write_bytes(b"t")
        (ctx.tmp / "plan-01.tar").write_bytes(b"t")
        return outcomes

    stages.register("plan", stage)
    assert err(run2(sink, plan_raw(sink.base))) == "PIPELINE_ERROR"


def test_v2_stage_codes_are_normalised_to_the_schema(sink: Sink) -> None:
    stages.register("ears", lambda ctx: stages.EarsOutcome("error", "boom: /x y"))
    out = run2(sink, raw2(sink.base, ears=dict(EARS)))
    OUT2.validate(out)
    assert out["ears"]["code"] == "STAGE_FAILED"
    stages._REGISTRY.clear()
    stages.register("plan", lambda ctx: [stages.PlanOutcome(0, 1, "error", "lower case"),
                                         stages.PlanOutcome(1, 2, "error", "OOM")])
    out = run2(sink, plan_raw(sink.base))
    assert [p["code"] for p in out["mm"]["plans"]] == ["PLAN_FAILED", "OOM"]


def test_v2_plan_missing_bundle_and_crash(sink: Sink) -> None:
    stages.register("plan", lambda ctx: [stages.PlanOutcome(0, 1, "ok"), stages.PlanOutcome(1, 2, "ok")])
    assert err(run2(sink, plan_raw(sink.base))) == "ARTIFACT_MISSING"
    stages._REGISTRY.clear()
    stages.register("plan", lambda ctx: (_ for _ in ()).throw(RuntimeError("x")))
    assert err(run2(sink, plan_raw(sink.base))) == "PIPELINE_ERROR"
    stages._REGISTRY.clear()
    stages.register("plan", lambda ctx: (_ for _ in ()).throw(WorkerError("DEADLINE", "x")))
    assert err(run2(sink, plan_raw(sink.base))) == "DEADLINE"


def test_v2_plan_bundle_too_large(sink: Sink) -> None:
    def stage(ctx: stages.StageContext) -> list[stages.PlanOutcome]:
        (ctx.tmp / "plan-00.tar").write_bytes(b"x" * ((2 << 20) + 1))
        return [stages.PlanOutcome(0, 1, "ok")]

    stages.register("plan", stage)
    assert err(run2(sink, plan_raw(sink.base, seeds=(1,)))) == "ARTIFACT_TOO_LARGE"


def test_v2_wrong_engine_and_weights(sink: Sink) -> None:
    pipe = fake()
    pipe.weights = {**WEIGHTS, "m-a-p/YuE2-3B": "c" * 40}
    assert err(run2(sink, raw2(sink.base), pipe)) == "WEIGHTS_MISMATCH"
    assert err(run2(sink, raw2(sink.base, request={"id": "x", "zzz": 1}))) == "BAD_INPUT"


def test_acestep_stays_v1() -> None:
    raw = raw2()
    raw["engine"] = "acestep15"
    with pytest.raises(WorkerError) as e:
        parse_job(raw)
    assert e.value.code == "BAD_INPUT"
    schema_ok = IN2.is_valid(raw)
    assert not schema_ok
    assert "acestep15" not in json.dumps(IN2.schema["properties"]["engine"])


# ---- caps, registry, selftest, image label -------------------------------------------------------------------------

def test_caps_vocabulary_and_compose() -> None:
    assert caps.compose([]) == ["v2"]
    assert caps.compose(["plan", "ears"], ["agent", "fallback"]) == ["agent", "ears", "fallback", "plan", "v2"]
    assert caps.label(["a", "b"]) == "a b"
    for bad, kw in ((["nope"], {}), (["attach"], {}), ([], {"transport": ["ears"]}), ([], {"transport": ["x"]})):
        with pytest.raises(ValueError):
            caps.compose(bad, **kw)
    assert caps.image_caps() == ["v2"]
    stages.register("analyze", lambda c: None)
    assert caps.image_caps(["agent"]) == ["agent", "analyze", "v2"]
    out_schema = OUT2.schema["properties"]["caps"]["items"]["enum"]
    assert set(out_schema) == caps.VOCABULARY


def test_stage_registry() -> None:
    with pytest.raises(ValueError):
        stages.register("mix", lambda c: None)
    stages.register("ears", lambda c: None)
    with pytest.raises(ValueError):
        stages.register("ears", lambda c: None)
    assert stages.caps() == ["ears"] and stages.get("plan") is None and stages.get("ears") is not None


def test_stage_discover_imports_modules_and_skips_broken(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    d = tmp_path / "stages_pkg"
    d.mkdir()
    (d / "good.py").write_text("from mmworker import stages\nstages.register('plan', lambda c: [])\n")
    (d / "broken.py").write_text("import a_library_that_is_not_in_this_image\n")
    (d / "_private.py").write_text("raise SystemExit('never imported')\n")
    monkeypatch.setattr(stages, "__path__", [str(d)])
    monkeypatch.setattr(stages, "__name__", "stages_pkg")
    monkeypatch.syspath_prepend(str(tmp_path))
    stages.discover()
    assert stages.caps() == ["plan"]


def test_stage_discover_survives_any_import_error_and_drops_partial_registration(
        tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    d = tmp_path / "stages_pkg2"
    d.mkdir()
    (d / "half.py").write_text("from mmworker import stages\nstages.register('ears', lambda c: None)\n"
                               "raise RuntimeError('cuda init failed')\n")
    (d / "ok.py").write_text("from mmworker import stages\nstages.register('plan', lambda c: [])\n")
    monkeypatch.setattr(stages, "__path__", [str(d)])
    monkeypatch.setattr(stages, "__name__", "stages_pkg2")
    monkeypatch.syspath_prepend(str(tmp_path))
    stages.discover()
    assert stages.caps() == ["plan"]


def test_serverless_handler_dispatches_v2(sink: Sink) -> None:
    from mmworker import serverless

    clock = Clock()
    h = serverless.make_handler(fake(), environ={}, clock=clock, sleep=clock.sleep)
    bad = h({"id": "j", "input": {**raw2(sink.base), "zzz": 1}})
    assert bad["error"].startswith("BAD_INPUT")
    old = h({"id": "j", "input": {**raw2(sink.base), "contract": "mm.worker/3"}})
    assert old["error"].startswith("CONTRACT_VERSION")
    # the sink answers real HTTP; clock is fake, so deadlines are in the far past of deadline_unix: use real time
    out = serverless.make_handler(fake(), environ={})({"id": "j", "input": raw2(
        sink.base, deadline_unix=2_000_000_000_000)})
    assert out["status"] == "ok" and out["kind"] == "render"


def test_selftest_prints_caps_and_runs_v2(capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("MM_ENGINE", raising=False)
    monkeypatch.delenv("ENGINE", raising=False)
    assert selftest.main() == 0
    got = capsys.readouterr()
    assert got.out.splitlines() == ["caps: agent fallback v2"]
    monkeypatch.setattr(selftest, "_run_v2", lambda base, sink: False)
    assert selftest.main() == 1
    assert "caps:" not in capsys.readouterr().out


def test_selftest_acestep_prints_no_caps(capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("MM_ENGINE", "acestep15")
    assert selftest.main() == 0
    assert capsys.readouterr().out == ""


def test_dockerfile_labels_equal_selftest_caps(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """C322.2: the label a stage lane must edit equals what the selftest prints, for the stages in this tree.
    The ears stage registers only next to its models (C333.3), so the test lays out the image's models directory."""
    from .test_ears_stage import LOCK, ears_models_tree

    monkeypatch.setenv("MM_EARS_MODELS_DIR", str(ears_models_tree(tmp_path / "models")))
    monkeypatch.setenv("MM_EARS_LOCK", str(LOCK))
    monkeypatch.delitem(sys.modules, "mmworker.stages.ears", raising=False)  # discover() re-imports it under the new environment
    text = (ROOT / "Dockerfile").read_text()
    m = re.search(r'^LABEL io\.metamusic\.contracts="([^"]*)" io\.metamusic\.caps="([^"]*)"$', text, re.M)
    assert m, "the worker Dockerfile must carry both labels"
    assert m.group(1).split() == list(contract.CONTRACTS)
    stages._REGISTRY.clear()  # a clean registry plus exactly the stage modules of this tree
    stages.discover()
    assert m.group(2).split() == caps.compose(stages.caps(), caps.BUILT_IN_TRANSPORT)
    assert Path(ROOT / "../.github/workflows/worker-image.yml").read_text().count("io.metamusic.caps") >= 1
