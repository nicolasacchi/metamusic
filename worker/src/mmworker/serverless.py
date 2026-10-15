"""MODE=serverless: load the model once, then hand a handler to the RunPod SDK."""

from __future__ import annotations

import os
import sys
import time
from collections.abc import Callable, Mapping
from typing import Any

from . import redact
from .contract import parse_input
from .errors import WorkerError
from .handler import process
from .pipeline import Pipeline, load_yue2
from .start import StartClient
from .upload import Uploader

Handler = Callable[[dict[str, Any]], dict[str, Any]]


def make_handler(pipe: Pipeline, *, environ: Mapping[str, str] | None = None,
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
                       job_id=job_id, environ=env)

    return handle


def _runpod_start(handler: Handler) -> None:  # pragma: no cover - needs the runpod SDK and a worker
    import runpod  # noqa: PLC0415

    runpod.serverless.start({"handler": handler})


def main(environ: Mapping[str, str] | None = None,
         loader: Callable[[dict[str, str]], Pipeline] = load_yue2,
         runner: Callable[[Handler], None] = _runpod_start) -> int:
    env = dict(os.environ if environ is None else environ)
    if env.get("HF_HUB_OFFLINE") != "1":
        print("mmworker: HF_HUB_OFFLINE=1 is required (a missing file must fail fast, not download on billed "
              "time)", file=sys.stderr)
        return 2
    try:
        pipe = loader(env)  # once per process, outside the handler
    except FileNotFoundError as e:  # missing weights: one clear line, non-zero exit, no retry loop in here
        print(f"mmworker: cannot load the model: {redact.text(e)}", file=sys.stderr)
        return 1
    runner(make_handler(pipe, environ=env))
    return 0
