"""MODE=serverless: load the model once, then hand a handler to the RunPod SDK."""

from __future__ import annotations

import os
import sys
import threading
import time
from collections.abc import Callable, Mapping
from typing import Any

from . import engines, redact
from .contract import parse_input
from .errors import InitError, WorkerError
from .handler import process
from .models import init_line
from .pipeline import Pipeline
from .start import StartClient
from .upload import Uploader

Handler = Callable[[dict[str, Any]], dict[str, Any]]


def make_handler(pipe: Pipeline | engines.Adapter, *, environ: Mapping[str, str] | None = None,
                 clock: Callable[[], float] = time.time,
                 sleep: Callable[[float], None] = time.sleep) -> Handler:
    env = os.environ if environ is None else environ

    def handle(job: dict[str, Any]) -> dict[str, Any]:
        try:
            take = parse_input(job.get("input"))
        except WorkerError as e:
            return {"error": f"{e.code}: {redact.message(e.message)}"}
        job_id = str(job.get("id", ""))
        return process(take, pipe, Uploader(clock=clock, sleep=sleep), clock,
                       start=StartClient(take.start_url, clock=clock, sleep=sleep),
                       job_id=job_id, environ=env, sleep=sleep)

    return handle


def _runpod_start(handler: Handler) -> None:  # pragma: no cover - needs the runpod SDK and a worker
    import runpod  # noqa: PLC0415

    runpod.serverless.start({"handler": handler})


LOAD_DEADLINE_DEFAULT_S = 420
LOAD_DEADLINE_RANGE_S = (60, 1200)


def _load_deadline(env: Mapping[str, str]) -> float:
    raw = env.get("MM_LOAD_DEADLINE_S")
    if raw is None or raw == "":
        return float(LOAD_DEADLINE_DEFAULT_S)
    lo, hi = LOAD_DEADLINE_RANGE_S
    try:
        v = int(raw)
    except ValueError:
        v = lo - 1
    if not lo <= v <= hi:
        raise InitError("ENV_INVALID", f"MM_LOAD_DEADLINE_S must be an integer in {lo}..{hi}")
    return float(v)


def main(environ: Mapping[str, str] | None = None,
         loader: Callable[[dict[str, str]], Any] = engines.load,
         runner: Callable[[Handler], None] = _runpod_start,
         hard_exit: Callable[[int], Any] = os._exit) -> int:
    """C154.5: any failure to initialise prints exactly one stderr line `mmworker: init failed: <CODE>: <message>`
    and returns 1 before the SDK starts, so RunPod reports the worker unhealthy within seconds instead of a job
    never arriving. No retry, no sleep, no download. A watchdog (MM_LOAD_DEADLINE_S) exits 1 if a load hangs."""
    env = dict(os.environ if environ is None else environ)
    if env.get("HF_HUB_OFFLINE") != "1":
        print("mmworker: HF_HUB_OFFLINE=1 is required (a missing file must fail fast, not download on billed "
              "time)", file=sys.stderr)
        return 2
    lock = threading.Lock()
    state = {"printed": False}

    def fail(e: InitError) -> None:
        with lock:  # the watchdog and the loader may race; exactly one line ever
            if state["printed"]:
                return
            state["printed"] = True
            print(init_line(e), file=sys.stderr, flush=True)

    timer: threading.Timer | None = None
    try:
        deadline = _load_deadline(env)

        def expire() -> None:
            fail(InitError("LOAD_DEADLINE", f"the model did not load within {deadline:g} s"))
            hard_exit(1)

        timer = threading.Timer(deadline, expire)
        timer.daemon = True
        timer.start()
        adapter = loader(env)  # once per process, outside the handler
    except InitError as e:
        fail(e)
        return 1
    except FileNotFoundError as e:  # missing weights
        fail(InitError("MODELS_MISSING", str(e)))
        return 1
    except Exception as e:  # any other load failure
        fail(InitError("LOAD_ERROR", f"{type(e).__name__}: {e}"))
        return 1
    finally:
        if timer is not None:
            timer.cancel()
            timer.join(5)
    if state["printed"]:  # the watchdog fired while the loader was finishing: do not start serving
        return 1
    runner(make_handler(adapter, environ=env))
    return 0
