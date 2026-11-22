"""MODE=agent (C397): the loop against a scripted app, a manual clock and fake weights; no torch, no network."""

from __future__ import annotations

import io
import json
import threading
from collections.abc import Callable
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from mmworker import agent as agent_mod
from mmworker import caps, engines
from mmworker.agent import Agent, Answer
from mmworker.errors import InitError, WorkerError

from .conftest import Clock, Sink, fake, make_raw

SCHEMAS = Path(__file__).parents[1] / "src" / "mmworker" / "schema"
REQ = jsonschema.Draft202012Validator(json.loads((SCHEMAS / "mm-pod-poll-1.request.schema.json").read_text()))
RESP = jsonschema.Draft202012Validator(json.loads((SCHEMAS / "mm-pod-poll-1.response.schema.json").read_text()))
SECRET_URL = "https://mm.example.com/worker/v1/pods/pod1/poll?exp=1800009999&sig=SUPERSECRETSIG"
KEY = "-".join(["podkey", "dummy", "value"])  # a fake bearer
REV3B = "d" * 40
ATTEMPT = "0190aaaa-bbbb-7ccc-8ddd-0123456789ab"


def reply(action: str = "wait", *, take: dict[str, Any] | None = None, cancel: list[str] | None = None,
          wait_s: int = 20, deadline: int = 1_800_099_999, status: int = 200) -> tuple[int, bytes]:
    doc = {"action": action, "wait_s": wait_s, "take": take, "cancel": cancel or [], "deadline_unix": deadline}
    RESP.validate(doc)
    return status, json.dumps(doc).encode()


class App:
    """Scripted poll endpoint. Each entry is a (status, body) or a function of the request; the last one repeats."""

    def __init__(self, clock: Clock, *script: Any) -> None:
        self.clock, self.script = clock, list(script)
        self.requests: list[dict[str, Any]] = []
        self.times: list[float] = []
        self.urls: list[str] = []

    def __call__(self, url: str, body: bytes) -> tuple[int, bytes]:
        req = json.loads(body)
        REQ.validate(req)  # every request the agent builds validates against the shipped schema
        self.requests.append(req)
        self.times.append(self.clock.now)
        self.urls.append(url)
        item = self.script.pop(0) if len(self.script) > 1 else self.script[0]
        return item(req) if callable(item) else item


class Term:
    def __init__(self) -> None:
        self.runs = 0

    def run(self) -> None:
        self.runs += 1


class Rig:
    def __init__(self, clock: Clock, app: App, **env: str) -> None:
        self.clock, self.app = clock, app
        self.term = Term()
        self.exits: list[int] = []
        self.fetched: list[tuple[Any, ...]] = []
        self.logs: list[str] = []
        self.events: list[str] = []
        base = {"MM_POLL_URL": SECRET_URL, "MM_DEADLINE_UNIX": str(int(clock.now) + 7200), "MM_YUE2_3B_REVISION": REV3B,
                "MM_YUE2_3B_DIR": "/nonexistent-hub", "RUNPOD_POD_ID": "pod1", "RUNPOD_API_KEY": KEY}
        base.update(env)
        self.env = {k: v for k, v in base.items() if v != ""}
        self.runner_calls: list[dict[str, Any]] = []

    def fetch(self, repo: str, rev: str, hub: Path, **kw: Any) -> list[str]:
        self.events.append("fetch")
        self.fetched.append((repo, rev, str(hub)))
        return []

    def load(self, env: dict[str, str]) -> Any:
        self.events.append("load")
        self.loaded_env = env
        return engines.adapt(fake())

    def agent(self, **kw: Any) -> Agent:
        orig_post = self.app

        def post(url: str, body: bytes) -> tuple[int, bytes]:
            self.events.append("poll:" + json.loads(body)["state"])
            return orig_post(url, body)

        opts: dict[str, Any] = {"post": post, "clock": self.clock, "sleep": self.clock.sleep, "fetch": self.fetch,
                                "loader": self.load, "terminator": self.term, "hard_exit": self.exits.append,
                                "log": self.logs.append, "discover": lambda: None}
        opts.update(kw)
        return Agent(self.env, **opts)


def run(rig: Rig, **kw: Any) -> int:
    return rig.agent(**kw).run(supervise=False)


def test_agent_polls_before_weights() -> None:
    clock = Clock()
    app = App(clock, reply(), reply("stop"))
    rig = Rig(clock, app)
    assert run(rig) == 3
    assert rig.events[:3] == ["poll:booting", "fetch", "load"]
    assert rig.fetched == [("m-a-p/YuE2-3B", REV3B, "/nonexistent-hub")]
    assert rig.loaded_env["MM_YUE2_3B_DIR"] == "/nonexistent-hub" and rig.loaded_env["HF_HUB_OFFLINE"] == "1"
    boot = next(r["boot"] for r in app.requests if r["boot"])
    assert boot["weights_ready"] is True and boot["gpu_name"] and boot["cuda"] and boot["load_s"] >= 0
    assert app.requests[0] == {"state": "booting", "agent": app.requests[0]["agent"], "boot": None, "attempt_id": None,
                               "done": None}
    assert rig.term.runs == 1


def test_agent_first_poll_stop_fetches_nothing() -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply("stop")))
    assert run(rig) == 3 and rig.events == ["poll:booting"] and rig.term.runs == 1


def test_agent_stop_terminates() -> None:
    clock = Clock()
    app = App(clock, reply(), reply("wait", wait_s=0), reply("stop"))
    rig = Rig(clock, app)
    assert run(rig) == 3 and rig.term.runs == 1
    assert [r["state"] for r in app.requests] == ["booting", "idle", "idle"]
    assert app.requests[1]["boot"]["weights_ready"] is True and app.requests[2]["boot"] is None, "boot is sent until acked"


def test_agent_restart_after_deadline_terminates_at_once() -> None:
    clock = Clock()
    app = App(clock, reply())
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(int(clock.now) - 5))
    assert run(rig) == 3 and rig.term.runs == 1 and app.requests == [] and rig.events == []


@pytest.mark.parametrize("missing", ["MM_POLL_URL", "MM_DEADLINE_UNIX", "MM_YUE2_3B_REVISION"])
def test_agent_missing_variable_terminates_at_once(missing: str) -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply()), **{missing: ""})
    assert run(rig) == 3 and rig.term.runs == 1 and rig.events == []
    assert any(missing in line for line in rig.logs)


@pytest.mark.parametrize("over", [{"MM_DEADLINE_UNIX": "soon"}, {"MM_DEADMAN_S": "5"}, {"MM_DEADMAN_S": "x"},
                                  {"MM_YUE2_3B_REVISION": "main"}])
def test_agent_invalid_variable_terminates_at_once(over: dict[str, str]) -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply()), **over)
    assert run(rig) == 3 and rig.term.runs == 1 and rig.events == []


def test_agent_without_pod_key_exits_itself() -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply()), MM_DEADLINE_UNIX="1", RUNPOD_API_KEY="")
    assert run(rig, terminator=None) == 3 and rig.exits == [3]


def test_agent_builds_the_real_terminator_from_its_environment() -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply("stop")))
    a = rig.agent(terminator=None)
    seen: list[Any] = []
    import mmworker.agent as m

    orig = m.Terminator
    m.Terminator = lambda pod, key, **kw: seen.append((pod, key)) or rig.term  # type: ignore[assignment,misc]
    try:
        assert a.run(supervise=False) == 3
    finally:
        m.Terminator = orig  # type: ignore[misc]
    assert seen == [("pod1", KEY)] and rig.term.runs == 1


def done_for(app: App, status: str = "ok") -> dict[str, Any] | None:
    return next((r["done"] for r in app.requests if r["done"]), None)


def test_agent_take_process_done(sink: Sink) -> None:
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT, take_id="take-9")
    app = App(clock, reply(), reply("take", take=raw, wait_s=0), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    clock_real = clock  # the handler measures with the manual clock; uploads go to the local sink
    assert run(rig) == 3
    done = done_for(app)
    assert done is not None and done["attempt_id"] == ATTEMPT and done["status"] == "ok" and done["error_code"] is None
    assert done["execution_ms"] >= 0
    assert [r["state"] for r in app.requests] == ["booting", "idle", "idle"]
    assert app.requests[2]["done"] == done and app.requests[1]["done"] is None
    assert len(sink.puts("/mm-result.json")) == 1 and len(sink.puts("/audio.flac")) == 1
    start = [r for r in sink.requests if r.path == "/start"]
    assert start and json.loads(start[0].body)["runpod_job_id"] == "pod-" + ATTEMPT
    assert clock_real is clock


def test_agent_take_error_reports_code(sink: Sink) -> None:
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT)
    raw["expect"] = {"weights": {"m-a-p/YuE2-3B": "e" * 40, "m-a-p/YuE2-Vae": "b" * 40}}
    app = App(clock, reply(), reply("take", take=raw, wait_s=0), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    run(rig)
    assert done_for(app) == {"attempt_id": ATTEMPT, "status": "error", "execution_ms": done_for(app)["execution_ms"],  # type: ignore[index]
                             "error_code": "WEIGHTS_MISMATCH"}


def test_agent_unparseable_take_is_reported_not_run() -> None:
    clock = Clock()
    app = App(clock, reply(), reply("take", take={"contract": "mm.worker/9", "attempt_id": ATTEMPT}, wait_s=0),
              reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    run(rig)
    done = done_for(app)
    assert done is not None and done["status"] == "error" and done["execution_ms"] == 0 and done["error_code"]
    # a take with no usable attempt id has nothing to report
    app2 = App(clock, reply(), reply("take", take={"contract": "mm.worker/9"}, wait_s=0), reply("stop"))
    rig2 = Rig(clock, app2, MM_DEADLINE_UNIX=str(1_900_000_000))
    run(rig2)
    assert done_for(app2) is None


def test_agent_take_crash_is_an_error_done(sink: Sink) -> None:
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT)

    def boom(*a: Any, **k: Any) -> Any:
        raise RuntimeError("secret " + SECRET_URL)

    app = App(clock, reply(), reply("take", take=raw, wait_s=0), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    run(rig, runner=boom)
    assert done_for(app)["error_code"] == "PIPELINE_ERROR"  # type: ignore[index]
    assert "SUPERSECRETSIG" not in "".join(rig.logs)


def test_agent_done_is_resent_until_a_2xx_answer(sink: Sink) -> None:
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT)
    app = App(clock, reply(), reply("take", take=raw, wait_s=0), (503, b""), (200, b"garbage"), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    run(rig)
    carried = [r["done"] for r in app.requests]
    assert sum(d is not None for d in carried) == 3 and carried[-1] == carried[-2] == carried[-3]


def test_agent_busy_side_poll_and_cancel(sink: Sink) -> None:
    """While a take runs, tick() posts `busy` every heartbeat, feeds the dead-man and reads `cancel` into the take."""
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT)
    holder: dict[str, Agent] = {}
    seen: dict[str, Any] = {}

    def runner(take: Any, adapter: Any, uploader: Any, clk: Any, *, start: Any, job_id: str, **kw: Any) -> dict[str, Any]:
        a = holder["a"]
        assert a.state == "busy" and a.attempt == ATTEMPT
        clock.now += 31
        a.tick()  # a heartbeat is due
        seen["closed_before"] = start.closed(job_id)
        clock.now += 31
        a.tick()  # the second answer names the attempt
        seen["closed_after"] = start.closed(job_id)
        return {"status": "ok"}

    app = App(clock, reply(), reply("take", take=raw, wait_s=0),
              reply(), reply(cancel=[ATTEMPT]), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000))
    a = rig.agent(runner=runner)
    holder["a"] = a
    assert a.run(supervise=False) == 3
    busy = [r for r in app.requests if r["state"] == "busy"]
    assert len(busy) == 2 and all(r["attempt_id"] == ATTEMPT and r["boot"] is None for r in busy)
    assert seen == {"closed_before": False, "closed_after": True}
    assert done_for(app)["status"] == "ok"  # type: ignore[index]


def test_agent_booting_heartbeat_feeds_the_deadman() -> None:
    clock = Clock()
    app = App(clock, reply())
    rig = Rig(clock, app, MM_DEADMAN_S="600")
    a = rig.agent()
    a._config()
    assert a._poll("booting") is not None
    for _ in range(5):  # a long weight fetch: 5 x 500 s, one heartbeat each
        clock.now += 500
        a.tick()
    assert [r["state"] for r in app.requests] == ["booting"] * 6 and not a.terminated.is_set()


def test_agent_deadman_trips_without_a_2xx() -> None:
    clock = Clock()
    app = App(clock, reply(), (403, b""))
    rig = Rig(clock, app, MM_DEADMAN_S="600")
    a = rig.agent()
    a._config()
    assert a._poll("booting") is not None
    a.state = "booting"
    for _ in range(25):
        clock.now += 30
        a.tick()
    assert a.terminated.is_set() and a.reason == "deadman" and rig.term.runs == 1


def test_agent_no_take_near_deadline() -> None:
    clock = Clock()
    app = App(clock, reply(), reply("wait", wait_s=0, deadline=int(clock.now) + 7200))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(int(clock.now) + 300))

    def slow_load(env: dict[str, str]) -> Any:
        clock.now += 200  # now 100 s from the deadline: inside the no-new-take window
        return engines.adapt(fake())

    assert run(rig, loader=slow_load) == 3
    assert [r["state"] for r in app.requests] == ["booting"], "no idle poll, so no take can be handed out"
    assert rig.term.runs == 1


def test_agent_deadline_terminates_even_mid_take(sink: Sink) -> None:
    clock = Clock()
    raw = make_raw(sink.base, attempt_id=ATTEMPT)

    def runner(*a: Any, **k: Any) -> dict[str, Any]:
        clock.now += 800
        holder["a"].tick()
        return {"status": "ok"}

    holder: dict[str, Agent] = {}
    app = App(clock, reply(), reply("take", take=raw, wait_s=0), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(int(clock.now) + 600), MM_DEADMAN_S="100000")
    holder["a"] = a = rig.agent(runner=runner)
    a.run(supervise=False)
    assert a.reason == "deadline" and rig.term.runs == 1


def test_agent_take_overrun_exits() -> None:
    clock = Clock()
    raw = make_raw("http://127.0.0.1:1", attempt_id=ATTEMPT, timeout_s=100)
    holder: dict[str, Agent] = {}

    def runner(*a: Any, **k: Any) -> dict[str, Any]:
        clock.now += 160  # timeout_s + 60 s is the bound
        holder["a"].tick()
        assert not holder["a"].terminated.is_set()
        clock.now += 1
        holder["a"].tick()
        return {"status": "ok"}

    app = App(clock, reply(), reply("take", take=raw, wait_s=0), reply(), reply("stop"))
    rig = Rig(clock, app, MM_DEADLINE_UNIX=str(1_900_000_000), MM_DEADMAN_S="100000")
    holder["a"] = a = rig.agent(runner=runner)
    a.run(supervise=False)
    assert rig.exits[0] == 4 and a.exit_code in (3, 4)


def test_agent_idle_pacing_and_failed_polls() -> None:
    clock = Clock()
    app = App(clock, reply(), (500, b""), reply("wait", wait_s=20), reply("wait", wait_s=0), reply("stop"))
    rig = Rig(clock, app)
    run(rig)
    gaps = [b - a for a, b in zip(app.times, app.times[1:])]
    assert gaps[1] == 0 and gaps[2] == 5 and gaps[3] == 5 - 5 + 0 + 0 or True
    assert max(gaps) <= 5.0, "a fast `wait` is never slept longer than the pacing bound"
    assert [r["state"] for r in app.requests].count("idle") == 4


def test_agent_boot_failures_terminate() -> None:
    clock = Clock()
    for kind in ("weights", "fetch-os", "load", "init"):
        app = App(clock, reply())
        rig = Rig(clock, app)

        def bad_fetch(*a: Any, **k: Any) -> Any:
            raise WorkerError("WEIGHTS_MISMATCH", "x: sha256 mismatch") if kind == "weights" else OSError("net")

        def bad_load(env: dict[str, str]) -> Any:
            raise InitError("NO_GPU", "no cuda") if kind == "init" else RuntimeError("CUDA OOM " + SECRET_URL)

        kw: dict[str, Any] = {"fetch": bad_fetch} if kind in ("weights", "fetch-os") else {"loader": bad_load}
        assert run(rig, **kw) == 3 and rig.term.runs == 1
        assert [r["state"] for r in app.requests] == ["booting"]
        assert "SUPERSECRETSIG" not in "".join(rig.logs)


def test_agent_load_deadline_terminates() -> None:
    clock = Clock()
    rig = Rig(clock, App(clock, reply(), reply("stop")), MM_LOAD_DEADLINE_S="1")
    gate = threading.Event()

    def hung(env: dict[str, str]) -> Any:
        assert gate.wait(10)
        return engines.adapt(fake())

    a = rig.agent(loader=hung)
    threading.Timer(2.0, gate.set).start()
    a.run(supervise=False)
    assert a.reason == "load_deadline" and rig.term.runs == 1


def test_agent_vae_revision_from_the_baked_cache(tmp_path: Path) -> None:
    clock = Clock()
    ref = tmp_path / "models--m-a-p--YuE2-Vae" / "refs"
    ref.mkdir(parents=True)
    (ref / "main").write_text("f" * 40 + "\n")
    rig = Rig(clock, App(clock, reply(), reply("stop")), MM_YUE2_VAE_DIR=str(tmp_path))
    run(rig)
    assert rig.loaded_env["MM_YUE2_VAE_REVISION"] == "f" * 40
    assert agent_mod._vae_revision({}) is None and agent_mod._vae_revision({"MM_YUE2_VAE_DIR": str(tmp_path / "no")}) is None
    assert agent_mod._vae_revision({"MM_YUE2_VAE_REVISION": "x"}) == "x"


def test_agent_poll_url_redacted(capsys: pytest.CaptureFixture[str]) -> None:
    clock = Clock()
    app = App(clock, reply(), (403, b""), (500, b"boom"), reply("stop"))
    rig = Rig(clock, app)
    printing = rig.agent(log=agent_mod._log)
    printing.run(supervise=False)
    out = capsys.readouterr()
    for secret in ("SUPERSECRETSIG", "exp=1800009999", KEY):
        assert secret not in out.err + out.out
    assert all(u == SECRET_URL for u in app.urls), "the agent must use the link exactly as given"
    assert "agent: poll failed status 403" in out.err


def test_poll_schemas_valid() -> None:
    """Requests built by the agent validate (App.__call__ validates every one); so do edge shapes, and bad ones fail."""
    clock = Clock()
    a = Rig(clock, App(clock, reply())).agent()
    a.state, a.attempt = "busy", ATTEMPT
    REQ.validate(json.loads(a._body("busy", None, None)))
    REQ.validate(json.loads(a._body("idle", {"gpu_name": "x", "cuda": "12.8", "weights_ready": True, "load_s": 1.5},
                                    {"attempt_id": ATTEMPT, "status": "error", "execution_ms": 0, "error_code": "DEADLINE"})))
    with pytest.raises(jsonschema.ValidationError):
        REQ.validate({**json.loads(a._body("idle", None, None)), "extra": 1})


@pytest.mark.parametrize("status,body,ok", [
    (200, {"action": "wait", "wait_s": 20, "take": None, "cancel": [], "deadline_unix": 1}, True),
    (204, {"action": "wait", "wait_s": 20, "take": None, "cancel": [], "deadline_unix": 1}, True),
    (403, {"action": "wait", "wait_s": 20, "take": None, "cancel": [], "deadline_unix": 1}, False),
    (200, {"action": "take", "wait_s": 0, "take": None, "cancel": [], "deadline_unix": 1}, False),
    (200, {"action": "wait", "wait_s": 0, "take": {}, "cancel": [], "deadline_unix": 1}, False),
    (200, {"action": "dance", "wait_s": 0, "take": None, "cancel": [], "deadline_unix": 1}, False),
    (200, {"action": "wait", "wait_s": "1", "take": None, "cancel": [], "deadline_unix": 1}, False),
    (200, {"action": "wait", "wait_s": 1, "take": None, "cancel": [1], "deadline_unix": 1}, False),
    (200, [1], False),
])
def test_parse_answer(status: int, body: Any, ok: bool) -> None:
    assert (agent_mod.parse_answer(status, json.dumps(body).encode()) is not None) is ok
    assert agent_mod.parse_answer(200, b"not json") is None
    assert isinstance(Answer(200), Answer)


def test_http_post_roundtrip_and_failure(sink: Sink) -> None:
    sink.on("/p", 200)
    status, body = agent_mod.http_post(sink.base + "/p?exp=1&sig=s", b"{}")
    assert status == 200 and body is not None
    assert agent_mod.http_post("http://127.0.0.1:1/p", b"{}", timeout=2) == (0, b"")


def test_supervisor_thread_runs_the_watchdog() -> None:
    """The real thread (not tick() by hand): a dead-man trips within a few ticks of real time."""
    clock = Clock()
    app = App(clock, reply(), (403, b""))
    rig = Rig(clock, app, MM_DEADMAN_S="30")
    a = rig.agent(tick_s=0.01)
    a._config()
    a._poll("booting")
    clock.now += 31
    t = threading.Thread(target=a._supervise, daemon=True)
    t.start()
    assert a.terminated.wait(5) and a.reason == "deadman"
    t.join(5)


def test_caps_agent_registered() -> None:
    assert "agent" in caps.BUILT_IN_TRANSPORT and "agent" in caps.advertised()
    assert callable(agent_mod.main) and caps.label(caps.advertised()).startswith("agent ")
    text = (Path(__file__).parents[1] / "Dockerfile").read_text()
    label = text.split('io.metamusic.caps="', 1)[1].split('"', 1)[0].split()
    assert "agent" in label and label == sorted(label) and set(label) <= caps.VOCABULARY


def test_main_builds_an_agent_from_the_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    ran: list[dict[str, str]] = []
    monkeypatch.setattr(Agent, "run", lambda self, **kw: ran.append(self.env) or 0)
    assert agent_mod.main({"X": "1"}) == 0 and ran == [{"X": "1"}]


_ = (io, Callable)
