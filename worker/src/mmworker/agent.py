"""MODE=agent: the pod worker loop (C397). Poll the app over a signed link, run takes one at a time, kill the pod.

Boot order (D-BULK-15): poll `booting` first (the dead-man is fed from the first second), then fetch the pinned 3B
weights (hubfetch), then load, then report `boot.weights_ready` and ask for takes. A supervisor thread keeps the
dead-man fed with `booting` / `busy` polls every 30 s, reads `cancel` into the running take, enforces the absolute
deadline and the take overrun bound, and runs the Terminator when any of them trips.

The poll URL (it carries the signature) and RunPod's key are secrets: nothing here logs, prints or stores them, and
errors are reported by type and status only."""

from __future__ import annotations

import http.client
import json
import os
import re
import ssl
import sys
import threading
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

from . import engines, hubfetch, redact, stages
from .contract import TakeInput, parse_job
from .env import WEIGHTS_3B, WEIGHTS_VAE
from .errors import InitError, WorkerError
from .handler import process
from .start import StartClient
from .terminate import Terminator, user_agent, worker_version
from .upload import Uploader
from .watchdog import Watchdog

HEARTBEAT_S = 30.0
TICK_S = 1.0
POLL_TIMEOUT_S = 45.0  # the app holds an idle poll up to 20 s
IDLE_PACE_S = 5.0  # an answered-at-once `wait` (pod not ready yet) is not re-asked faster than this
MIN_PACE_S = 1.0  # no poll loop is tighter than this, whatever wait_s the app answered
TERM_WAIT_S = 150.0  # bound on waiting for a Terminator another thread is running
FINAL_DONE_TRIES = 3
_LOOPBACK = ("127.0.0.1", "localhost", "::1")
OVERRUN_GRACE_S = 60.0
DEFAULT_DEADMAN_S = 600
DEFAULT_3B_DIR = "/opt/mm/pod-cache/hub"
EXIT_TERMINATOR = 3
EXIT_OVERRUN = 4
_ATTEMPT_RE = re.compile(r"^[0-9a-f-]{36}$")
_CODE_RE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")

# post(url, body) -> (status, response body); status 0 on any network failure
Post = Callable[[str, bytes], tuple[int, bytes]]


def _log(line: str) -> None:
    print(redact.text(line), file=sys.stderr, flush=True)


def http_post(url: str, body: bytes, timeout: float = POLL_TIMEOUT_S) -> tuple[int, bytes]:
    u = urlsplit(url)
    conn: http.client.HTTPConnection
    if u.scheme == "https":
        conn = http.client.HTTPSConnection(u.netloc, timeout=timeout, context=ssl.create_default_context())
    else:
        conn = http.client.HTTPConnection(u.netloc, timeout=timeout)
    try:
        target = u.path + (f"?{u.query}" if u.query else "")
        conn.request("POST", target, body=body, headers={
            "Content-Type": "application/json", "Content-Length": str(len(body)), "User-Agent": user_agent()})
        resp = conn.getresponse()
        return resp.status, resp.read(1 << 20)
    except (OSError, http.client.HTTPException):
        return 0, b""
    finally:
        conn.close()


@dataclass
class Answer:
    status: int
    action: str = "wait"
    wait_s: int = 0
    take: dict[str, Any] | None = None
    cancel: list[str] = field(default_factory=list)
    deadline_unix: int = 0


def parse_answer(status: int, raw: bytes) -> Answer | None:
    """A 2xx body of the poll-1 response shape, else None (a garbled answer does not feed the dead-man)."""
    if not 200 <= status < 300:
        return None
    try:
        d = json.loads(raw)
    except ValueError:
        return None
    if not isinstance(d, dict) or d.get("action") not in ("wait", "take", "stop"):
        return None
    wait_s, dl, cancel, take = d.get("wait_s", 0), d.get("deadline_unix", 0), d.get("cancel", []), d.get("take")
    if type(wait_s) is not int or type(dl) is not int or not isinstance(cancel, list):
        return None
    if not all(isinstance(c, str) for c in cancel) or (d["action"] == "take") != isinstance(take, dict):
        return None
    return Answer(status, d["action"], max(0, min(wait_s, 60)), take, cancel, dl)


class _PodStart(StartClient):
    """The start handshake of a pod take. `closed` also answers True once a poll's `cancel` named the attempt."""

    def __init__(self, url: str, cancelled: threading.Event, **kw: Any) -> None:
        super().__init__(url, **kw)
        self._cancelled = cancelled

    def closed(self, job_id: str) -> bool:
        return self._cancelled.is_set() or super().closed(job_id)


def _vae_revision(env: Mapping[str, str]) -> str | None:
    """The baked VAE revision: MM_YUE2_VAE_REVISION, else what tools/fetch_vae.py recorded in refs/main."""
    if env.get("MM_YUE2_VAE_REVISION"):
        return env["MM_YUE2_VAE_REVISION"]
    root = env.get("MM_YUE2_VAE_DIR")
    if not root:
        return None
    try:
        return (Path(root) / ("models--" + WEIGHTS_VAE.replace("/", "--")) / "refs" / "main").read_text().strip() or None
    except OSError:
        return None


class Agent:
    def __init__(self, environ: Mapping[str, str], *, post: Post = http_post, clock: Callable[[], float] = time.time,
                 sleep: Callable[[float], None] = time.sleep, fetch: Callable[..., Any] = hubfetch.fetch,
                 loader: Callable[[dict[str, str]], Any] = engines.load, runner: Callable[..., dict[str, Any]] = process,
                 terminator: Terminator | None = None, hard_exit: Callable[[int], Any] = os._exit,
                 heartbeat_s: float = HEARTBEAT_S, tick_s: float = TICK_S, log: Callable[[str], None] = _log,
                 discover: Callable[[], None] = stages.discover) -> None:
        self.env = dict(environ)
        self._post, self._clock, self._sleep = post, clock, sleep
        self._fetch, self._loader, self._runner = fetch, loader, runner
        self._terminator = terminator
        self._exit, self._heartbeat_s, self._tick_s, self._log = hard_exit, heartbeat_s, tick_s, log
        self._discover = discover
        self._lock = threading.Lock()  # one poll in flight at a time
        self._term_lock = threading.Lock()
        self._term_started = False
        self._term_done = threading.Event()
        self.terminated = threading.Event()
        self.state = "booting"
        self.attempt: str | None = None
        self._take_deadline: float | None = None
        self._cancel = threading.Event()
        self._last_poll = 0.0
        self._url = ""
        self._watch: Watchdog | None = None
        self.exit_code = 0
        self.reason = ""

    # ---- configuration ------------------------------------------------------------------------------------------

    def _config(self) -> None:
        e = self.env
        missing = [k for k in ("MM_POLL_URL", "MM_DEADLINE_UNIX", "MM_YUE2_3B_REVISION") if not e.get(k)]
        if missing:
            raise InitError("ENV_INVALID", f"{missing[0]} is not set")
        try:
            deadline = int(e["MM_DEADLINE_UNIX"])
            deadman = int(e.get("MM_DEADMAN_S") or DEFAULT_DEADMAN_S)
        except ValueError:
            raise InitError("ENV_INVALID", "MM_DEADLINE_UNIX and MM_DEADMAN_S must be integers") from None
        if deadman < 30:
            raise InitError("ENV_INVALID", "MM_DEADMAN_S must be at least 30")
        if not hubfetch.REV_RE.match(e["MM_YUE2_3B_REVISION"]):
            raise InitError("ENV_INVALID", "MM_YUE2_3B_REVISION must be 40 lowercase hex")
        u = urlsplit(e["MM_POLL_URL"])
        if u.scheme != "https" and not (u.scheme == "http" and u.hostname in _LOOPBACK):
            raise InitError("ENV_INVALID", "MM_POLL_URL must be an https URL")
        self._url = e["MM_POLL_URL"]
        self._watch = Watchdog(deadline, deadman, self._clock)
        if self._clock() >= deadline:
            raise InitError("ENV_INVALID", "the deadline has passed")

    # ---- polling ------------------------------------------------------------------------------------------------

    def _body(self, state: str, boot: dict[str, Any] | None, done: dict[str, Any] | None) -> bytes:
        return json.dumps({
            "state": state, "agent": {"version": worker_version(), "build": self.env.get("MM_WORKER_BUILD") or "unknown"},
            "boot": boot, "attempt_id": self.attempt if state == "busy" else None, "done": done,
        }, separators=(",", ":")).encode()

    def _poll(self, state: str, boot: dict[str, Any] | None = None, done: dict[str, Any] | None = None) -> Answer | None:
        """One poll. Feeds the dead-man on a 2xx answer, tightens the deadline, queues cancels. None when it failed."""
        assert self._watch is not None
        with self._lock:
            status, raw = self._post(self._url, self._body(state, boot, done))
            self._last_poll = self._clock()
        ans = parse_answer(status, raw)
        if ans is None:
            self._log(f"agent: poll failed status {status}")
            return None
        self._watch.fed(status)
        self._watch.tighten(ans.deadline_unix)
        if self.attempt and self.attempt in ans.cancel:
            self._cancel.set()
        return ans

    # ---- terminating --------------------------------------------------------------------------------------------

    def terminate(self, reason: str) -> None:
        with self._term_lock:
            if self._term_started:
                first = False
            elif self.terminated.is_set():
                return  # the overrun exit already ran
            else:
                first = self._term_started = True
                self.terminated.set()
                self.reason = reason
        if not first:
            self._term_done.wait(TERM_WAIT_S)  # never return (and let the process exit) while it is running
            return
        self.exit_code = EXIT_TERMINATOR
        self._log(f"agent: terminating ({reason})")
        try:
            self._terminate_now()
        finally:
            self._term_done.set()

    def _terminate_now(self) -> None:
        if self._terminator is None:
            pod, key = self.env.get("RUNPOD_POD_ID"), self.env.get("RUNPOD_API_KEY")
            if not pod or not key:
                self._log("agent: RUNPOD_POD_ID or RUNPOD_API_KEY is missing; exiting")
                self._exit(EXIT_TERMINATOR)
                return
            self._terminator = Terminator(pod, key, hard_exit=self._exit)
        self._terminator.run()

    def tick(self) -> None:
        """The supervisor's work, callable without its thread: deadline, dead-man, overrun, heartbeat."""
        if self.terminated.is_set() or self._watch is None:
            return
        why = self._watch.expired()
        if why:
            self.terminate(why)
            return
        now = self._clock()
        if self.state == "busy" and self._take_deadline is not None and now > self._take_deadline:
            self._log("agent: take overran its timeout; restarting the container")
            self.terminated.set()
            self.exit_code = EXIT_OVERRUN
            self._exit(EXIT_OVERRUN)
            return
        if self.state in ("booting", "busy") and now - self._last_poll >= self._heartbeat_s:
            if self._lock.acquire(blocking=False):
                self._lock.release()
                ans = self._poll(self.state)
                if ans is not None and ans.action == "stop":
                    self.terminate("stop")

    def _supervise(self) -> None:  # pragma: no cover - thread wrapper around tick()
        while not self.terminated.wait(self._tick_s):
            try:
                self.tick()
            except Exception as e:  # the supervisor must outlive a bug in one tick
                self._log(f"agent: supervisor error {type(e).__name__}")

    # ---- boot ---------------------------------------------------------------------------------------------------

    def _boot(self) -> tuple[Any, dict[str, Any]] | None:
        e = self.env
        t0 = self._clock()
        hub = Path(e.get("MM_YUE2_3B_DIR") or DEFAULT_3B_DIR)
        env = dict(e)
        env["MM_YUE2_3B_DIR"] = str(hub)
        env.setdefault("HF_HUB_OFFLINE", "1")
        vae = _vae_revision(env)
        if vae:
            env["MM_YUE2_VAE_REVISION"] = vae
        try:
            self._fetch(WEIGHTS_3B, e["MM_YUE2_3B_REVISION"], hub, user_agent=user_agent(), log=self._log)
        except WorkerError as err:
            self._log(f"agent: weights failed: {err.code}: {redact.message(err.message)}")
            return None
        except Exception as err:  # noqa: BLE001 - OSError, a garbled listing (ValueError), IncompleteRead, ...
            self._log(f"agent: weights failed: WEIGHTS_FETCH {type(err).__name__}")
            return None
        if self.terminated.is_set():
            return None
        try:
            lock_s = int(env.get("MM_LOAD_DEADLINE_S") or 420)
        except ValueError:
            lock_s = 420
        timer = threading.Timer(lock_s, lambda: self.terminate("load_deadline"))
        timer.daemon = True
        timer.start()
        try:
            adapter = self._loader(env)
        except Exception as err:  # noqa: BLE001 - any load failure ends the pod
            self._log(f"agent: load failed: {type(err).__name__}: {redact.message(err)}")
            return None
        finally:
            timer.cancel()
        self._discover()
        fp = adapter.env_fingerprint(env)
        boot = {"gpu_name": str(fp.get("gpu_name", "unknown"))[:128], "cuda": str(fp.get("cuda", "unknown"))[:32],
                "weights_ready": True, "load_s": round(max(0.0, self._clock() - t0), 3)}
        return adapter, boot

    # ---- takes --------------------------------------------------------------------------------------------------

    def _run_take(self, doc: dict[str, Any], adapter: Any) -> dict[str, Any] | None:
        """Run one take; returns the `done` object to report (None when the document names no valid attempt)."""
        attempt = doc.get("attempt_id")
        attempt = attempt if isinstance(attempt, str) and _ATTEMPT_RE.match(attempt) else None
        if attempt is None:
            self._log("agent: take without a valid attempt id dropped; nothing to report")
        try:
            take: TakeInput = parse_job(doc)
        except WorkerError as err:
            self._log(f"agent: take refused: {err.code}")
            return self._done(attempt, "error", 0, err.code)
        self.attempt = take.attempt_id
        self._cancel.clear()
        t0 = self._clock()
        self._take_deadline = t0 + take.timeout_s + OVERRUN_GRACE_S
        self.state = "busy"
        try:
            start = _PodStart(take.start_url, self._cancel, clock=self._clock, sleep=self._sleep)
            out = self._runner(take, adapter, Uploader(clock=self._clock, sleep=self._sleep), self._clock,
                               start=start, job_id="pod-" + take.attempt_id, environ=self.env, sleep=self._sleep)
        except Exception as err:  # noqa: BLE001 - a bug in one take must not stop the pod
            self._log(f"agent: take crashed: {type(err).__name__}")
            out = {"error": "PIPELINE_ERROR: the take crashed"}
        finally:
            self.state = "idle"
            self._take_deadline = None
        ms = max(0, int((self._clock() - t0) * 1000))
        ok = out.get("error") is None and out.get("status") == "ok"
        code = None if ok else _error_code(out)
        return self._done(take.attempt_id, "ok" if ok else "error", ms, code)

    @staticmethod
    def _done(attempt: str | None, status: str, ms: int, code: str | None) -> dict[str, Any] | None:
        if attempt is None:
            return None
        return {"attempt_id": attempt, "status": status, "execution_ms": ms, "error_code": code}

    # ---- the loop -----------------------------------------------------------------------------------------------

    def run(self, *, supervise: bool = True) -> int:
        try:
            self._config()
        except InitError as err:
            self._log(f"agent: init failed: {err.code}: {redact.message(err.message)}")
            self.terminate("config")
            return self.exit_code
        first = self._poll("booting")  # poll first: the dead-man is fed before the long fetch
        if first is not None and first.action == "stop":
            self.terminate("stop")
            return self.exit_code
        self._last_poll = self._clock()
        if supervise:
            threading.Thread(target=self._supervise, daemon=True, name="mm-agent-supervisor").start()
        booted = self._boot()
        if booted is None or self.terminated.is_set():
            self.terminate("boot_failed")
            return self.exit_code
        adapter, boot = booted
        self.state = "idle"
        pending_boot: dict[str, Any] | None = boot
        pending_done: dict[str, Any] | None = None
        assert self._watch is not None
        while not self.terminated.is_set():
            self.tick()
            if self.terminated.is_set():
                break
            if not self._watch.may_take():
                self._final_report(pending_done)
                self.terminate("deadline_near")
                break
            t0 = self._clock()
            ans = self._poll("idle", pending_boot, pending_done)
            if ans is None:
                self._sleep(IDLE_PACE_S)
                continue
            pending_boot = pending_done = None  # a 2xx answer carried both
            if ans.action == "stop":
                self.terminate("stop")
            elif ans.action == "take" and ans.take is not None:
                pending_done = self._run_take(ans.take, adapter)
            else:
                pace = max(min(float(ans.wait_s), IDLE_PACE_S), MIN_PACE_S)
                self._sleep(max(0.0, pace - (self._clock() - t0)))
        if self._term_started:
            self._term_done.wait(TERM_WAIT_S)  # the Terminator may be running on the supervisor thread
        return self.exit_code

    def _final_report(self, done: dict[str, Any] | None) -> None:
        """A take finished inside the no-take window still reports `done`; any `take` answer is ignored.
        Only a pending `done` is worth a poll: an idle poll could be answered with a take nobody will run."""
        if done is None:
            return
        for i in range(FINAL_DONE_TRIES):
            if self.terminated.is_set() or self._poll("idle", None, done) is not None:
                return
            if i + 1 < FINAL_DONE_TRIES:
                self._sleep(IDLE_PACE_S)


def _error_code(out: Mapping[str, Any]) -> str:
    mm = out.get("mm")
    if isinstance(mm, dict) and isinstance(mm.get("error"), dict):
        code = mm["error"].get("code")
        if isinstance(code, str) and _CODE_RE.match(code):
            return code
    head = str(out.get("error", "")).split(":", 1)[0]
    return head if _CODE_RE.match(head) else "PIPELINE_ERROR"


def main(environ: Mapping[str, str] | None = None) -> int:
    return Agent(os.environ if environ is None else environ).run()
