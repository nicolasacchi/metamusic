from __future__ import annotations

import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest

from mmworker import __main__ as entry, selftest, serverless

from .conftest import Clock, Post, Sink, fake, make_raw


def test_model_loaded_once(sink: Sink) -> None:
    loads: list[int] = []
    handlers: list[Any] = []

    def loader(env: dict[str, str]) -> Any:
        loads.append(1)
        return fake()

    assert serverless.main({"HF_HUB_OFFLINE": "1"}, loader, handlers.append) == 0
    (h,) = handlers
    for i in range(2):
        raw = make_raw(sink.base, take_id=f"t{i}")
        out = h({"id": f"job{i}", "input": raw})
        assert out["status"] == "ok"
    assert loads == [1]
    assert len(sink.puts("/mm-result.json")) == 2


def test_serverless_requires_hf_offline(capsys: pytest.CaptureFixture[str]) -> None:
    loads: list[int] = []
    for env in ({}, {"HF_HUB_OFFLINE": "0"}):
        code = serverless.main(env, lambda e: loads.append(1), lambda h: None)  # type: ignore[arg-type,return-value]
        assert code == 2
    assert loads == [] and "HF_HUB_OFFLINE" in capsys.readouterr().err


def test_handler_rejects_bad_input() -> None:
    h = serverless.make_handler(fake())
    out = h({"id": "j", "input": {"contract": "mm.worker/9"}})
    assert out["error"].startswith("CONTRACT_VERSION") and "mm" not in out
    assert h({"id": "j"})["error"].startswith("BAD_INPUT")


def test_output_matches_schema(sink: Sink) -> None:
    from .test_handler import validate_output

    h = serverless.make_handler(fake())
    validate_output(h({"id": "j", "input": make_raw(sink.base)}))


def test_mode_agent_refused(capsys: pytest.CaptureFixture[str]) -> None:
    assert entry.run("agent") == 2
    assert "M6" in capsys.readouterr().err
    assert entry.run("") == 2 and entry.run("bogus") == 2


def test_mode_agent_subprocess() -> None:
    src = Path(__file__).parents[1] / "src"
    p = subprocess.run([sys.executable, "-m", "mmworker"], env={"MODE": "agent", "PYTHONPATH": str(src)},
                       capture_output=True, text=True, timeout=30)
    assert p.returncode == 2


def test_mode_dispatch_serverless_needs_offline(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("HF_HUB_OFFLINE", raising=False)
    assert entry.run("serverless") == 2


def test_selftest_passes() -> None:
    assert selftest.run() is True
    assert selftest.main() == 0
    assert entry.run("selftest") == 0


def test_selftest_failure_path(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(selftest, "run", lambda: False)
    assert selftest.main() == 1


def test_clock_post_helper() -> None:
    c = Clock()
    assert Post(c, 200)("u", b"") == 200
