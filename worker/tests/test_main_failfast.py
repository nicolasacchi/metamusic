"""serverless.main fail-fast (C154.5): every load failure is one stderr line and exit 1 before the SDK starts."""

from __future__ import annotations

import threading
from typing import Any

import pytest

from mmworker import models, serverless
from mmworker.errors import InitError

from .conftest import fake

ENV = {"HF_HUB_OFFLINE": "1"}


def run(loader: Any, env: dict[str, str] | None = None, **kw: Any) -> tuple[int, list[Any]]:
    started: list[Any] = []
    code = serverless.main(ENV if env is None else env, loader, started.append, **kw)
    return code, started


def test_main_missing_model_exits_1_before_sdk(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise FileNotFoundError("model Org/M@abc not found; tried: /a (absent); /b (absent)")

    code, started = run(loader)
    err = capsys.readouterr().err.splitlines()
    assert code == 1 and started == []
    assert err == ["mmworker: init failed: MODELS_MISSING: model Org/M@abc not found; tried: /a (absent); /b (absent)"]


@pytest.mark.parametrize("code_name", ["NO_GPU", "NO_MEMORY", "ENV_INVALID", "LOAD_ERROR"])
def test_main_init_error_codes(code_name: str, capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise InitError(code_name, "because")

    code, started = run(loader)
    assert code == 1 and started == []
    assert capsys.readouterr().err == f"mmworker: init failed: {code_name}: because\n"


def test_main_no_gpu_exits_1(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise InitError("NO_GPU", "torch.cuda.is_available() is false")

    assert run(loader)[0] == 1
    assert "NO_GPU" in capsys.readouterr().err


def test_main_unexpected_exception_is_load_error(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise RuntimeError("CUDA out of memory")

    code, started = run(loader)
    assert code == 1 and started == []
    assert capsys.readouterr().err == "mmworker: init failed: LOAD_ERROR: RuntimeError: CUDA out of memory\n"


def test_main_error_line_redacted(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise RuntimeError("GET https://h.example.com/m.bin?sig=SECRET123&exp=9 failed\nsecond line")

    run(loader)
    err = capsys.readouterr().err
    assert "SECRET123" not in err and "sig=" not in err and err.count("\n") == 1


def test_main_exactly_one_line(capsys: pytest.CaptureFixture[str]) -> None:
    def loader(env: dict[str, str]) -> Any:
        raise FileNotFoundError("a\nb\nc")

    run(loader)
    assert len(capsys.readouterr().err.splitlines()) == 1


def test_main_offline_required(capsys: pytest.CaptureFixture[str]) -> None:
    loads: list[int] = []
    for env in ({}, {"HF_HUB_OFFLINE": "0"}):
        code, started = run(lambda e: loads.append(1), env)
        assert code == 2 and started == []
    assert loads == [] and "HF_HUB_OFFLINE" in capsys.readouterr().err


def test_main_load_deadline_exits_1(monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    monkeypatch.setattr(serverless, "LOAD_DEADLINE_RANGE_S", (1, 1200))
    exited = threading.Event()
    codes: list[int] = []

    def hard_exit(c: int) -> None:
        codes.append(c)
        exited.set()

    def loader(env: dict[str, str]) -> Any:  # a hung load, released once the watchdog has fired
        assert exited.wait(10)
        return fake()

    code, started = run(loader, {**ENV, "MM_LOAD_DEADLINE_S": "1"}, hard_exit=hard_exit)
    err = capsys.readouterr().err.splitlines()
    assert codes == [1] and code == 1 and started == [], "the late loader result must not start the SDK"
    assert len(err) == 1 and err[0].startswith("mmworker: init failed: LOAD_DEADLINE: ")


def test_main_load_deadline_cancelled_after_load() -> None:
    exits: list[int] = []
    code, started = run(lambda e: fake(), {**ENV, "MM_LOAD_DEADLINE_S": "60"}, hard_exit=exits.append)
    assert code == 0 and len(started) == 1 and exits == []
    assert not [t for t in threading.enumerate() if isinstance(t, threading.Timer) and t.is_alive()]


@pytest.mark.parametrize("v", ["59", "1201", "soon", "-5", "4.5"])
def test_main_load_deadline_invalid(v: str, capsys: pytest.CaptureFixture[str]) -> None:
    loads: list[int] = []
    code, started = run(lambda e: loads.append(1), {**ENV, "MM_LOAD_DEADLINE_S": v})
    assert code == 1 and loads == [] and started == []
    assert "ENV_INVALID: MM_LOAD_DEADLINE_S" in capsys.readouterr().err


def test_yue2_missing_3b_exits_1(tmp_path: Any, monkeypatch: pytest.MonkeyPatch,
                                 capsys: pytest.CaptureFixture[str]) -> None:
    """The canary regression through the shared path: the real resolve_3b miss ends as one MODELS_MISSING line."""
    from mmworker import pipeline

    monkeypatch.setattr(pipeline, "VOLUME_HUB", tmp_path / "vol")
    monkeypatch.setattr(pipeline, "MODEL_STORE", tmp_path / "store")

    def loader(env: dict[str, str]) -> Any:
        return pipeline.resolve_3b(env, "m-a-p/YuE2-3B", "a" * 40)

    code, started = run(loader)
    err = capsys.readouterr().err.splitlines()
    assert code == 1 and started == [] and len(err) == 1
    assert err[0].startswith("mmworker: init failed: MODELS_MISSING: 3B weights m-a-p/YuE2-3B@")


def test_yue2_preflight_names_missing_revision(capsys: pytest.CaptureFixture[str]) -> None:
    code, started = run(serverless.engines.load, {**ENV})
    assert code == 1 and started == []
    assert capsys.readouterr().err.startswith("mmworker: init failed: ENV_INVALID: MM_YUE2_3B_REVISION")


def test_models_init_line_shape() -> None:
    assert models.init_line(InitError("NO_GPU", "x")) == "mmworker: init failed: NO_GPU: x"
