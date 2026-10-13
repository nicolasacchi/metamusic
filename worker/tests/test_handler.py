from __future__ import annotations

import json
import logging
import sys
from importlib import resources
from typing import Any

import jsonschema
import numpy as np
import pytest
import soundfile

from mmworker import contract, env as envmod
from mmworker.contract import ARTIFACTS
from mmworker.handler import deadlines

from .conftest import WEIGHTS, Sink, fake, make_raw, make_rig, sha_b64

OUT_SCHEMA = json.loads(resources.files("mmworker").joinpath("schema/mm-worker-1.output.schema.json").read_text())


def validate_output(out: dict[str, Any]) -> None:
    jsonschema.Draft202012Validator(OUT_SCHEMA).validate(out)


def err_code(res: dict[str, Any]) -> str:
    assert "error" in res and "mm" in res
    assert res["error"].split(":")[0] == res["mm"]["error"]["code"]
    validate_output(res["mm"])
    return res["mm"]["error"]["code"]


FULL = {"score.abc": b"abc", "plan.json": b"{}", "plan_manifest.json": b"{}", "abc_tokens.npy": b"n1",
        "prefix.npy": b"n2", "semantic.npy": b"n3", "latent.npy": b"never"}


def test_handler_happy_path(sink: Sink) -> None:
    rig = make_rig(sink)
    out = rig.run(fake(files=FULL, truncated={"abc": False, "semantic": True}))
    validate_output(out)
    assert out["status"] == "ok" and out["truncated"] == {"abc": False, "semantic": True}
    assert "mm-result.json" not in out["artifacts"]
    assert len(rig.post.calls) == 1, "the start handshake is announced once, before any upload"
    puts = [r.path.lstrip("/") for r in sink.requests]
    expected = [a for a in ARTIFACTS if a != "latent.npy"]
    assert puts == expected and puts[-1] == "mm-result.json"
    assert set(out["artifacts"]) == set(expected) - {"mm-result.json"}
    assert json.loads(sink.puts("/mm-result.json")[0].body) == out
    assert out["audio_seconds"] == pytest.approx(1.0)
    assert rig.take.take_id == out["take_id"]


def test_artifacts_exclude_latent(sink: Sink) -> None:
    rig = make_rig(sink)
    out = rig.run(fake(files=FULL))
    assert "latent.npy" not in out["artifacts"]
    assert not sink.puts("/latent.npy")


def test_upload_order_result_last(sink: Sink) -> None:
    rig = make_rig(sink)
    rig.run(fake(files=FULL))
    order = [r.path.lstrip("/") for r in sink.puts()]
    assert order[-1] == "mm-result.json"
    assert order.index("request.json") < order.index("config.json") < order.index("result.json")
    assert order.index("semantic.npy") < order.index("audio.flac") < order.index("mm-result.json")


@pytest.mark.parametrize(("trunc", "flag"), [
    ({"abc": False, "semantic": True}, True),
    ({"abc": True, "semantic": False}, True),
    ({"abc": False, "semantic": False}, False),
])
def test_truncation_any(sink: Sink, trunc: dict[str, bool], flag: bool) -> None:
    out = make_rig(sink).run(fake(truncated=trunc))
    assert out["status"] == "ok" and out["truncated"] == trunc
    assert any(out["truncated"].values()) is flag


@pytest.mark.parametrize("bad", [True, False, "none", {"abc": False}, {"abc": 0, "semantic": 0},
                                 {"abc": False, "semantic": False, "extra": False}, [False, False]])
def test_truncated_not_dict(sink: Sink, bad: Any) -> None:
    pipe = fake(truncated=bad)
    if bad == "none":
        pipe._truncated = None
    assert err_code(make_rig(sink).run(pipe)) == "TRUNCATED_NOT_DICT"


def test_required_missing(sink: Sink) -> None:
    res = make_rig(sink).run(fake(drop=("config.json",)))
    assert err_code(res) == "ARTIFACT_MISSING"
    assert not sink.puts("/audio.flac")


def test_too_large(sink: Sink) -> None:
    rig = make_rig(sink)
    raw_uploads = rig.take.uploads["request.json"]
    assert raw_uploads.max_bytes == 4 * 1024 * 1024
    res = rig.run(fake(files={"score.abc": b"x" * (1024 * 1024 + 1)}))
    assert err_code(res) == "ARTIFACT_TOO_LARGE"
    assert not sink.puts("/audio.flac")


def test_weights_mismatch_fails_fast(sink: Sink) -> None:
    rig = make_rig(sink)
    pipe = fake()
    pipe.weights = {**WEIGHTS, "m-a-p/YuE2-3B": "c" * 40}
    res = rig.run(pipe)
    assert err_code(res) == "WEIGHTS_MISMATCH"
    assert pipe.calls == []


def test_bad_request_rejected_before_render(sink: Sink) -> None:
    rig = make_rig(sink, request={"id": "x", "lyrics": "l", "style": "s", "seed": 1, "cot": "off", "zzz": 1})
    pipe = fake()
    assert err_code(rig.run(pipe)) == "BAD_INPUT"
    assert pipe.calls == []


def test_wrong_engine(sink: Sink) -> None:
    rig = make_rig(sink, engine="other")
    assert err_code(rig.run(fake())) == "BAD_INPUT"


def test_pipeline_error_and_redaction(sink: Sink, caplog: pytest.LogCaptureFixture) -> None:
    secret = "https://mm.example.com/worker/v1/uploads/x?exp=1&sig=abc"
    rig = make_rig(sink)
    with caplog.at_level(logging.DEBUG):
        res = rig.run(fake(error=RuntimeError(f"boom {secret} Bearer abcdefghijkl1234")))
    assert err_code(res) == "PIPELINE_ERROR"
    blob = json.dumps(res) + caplog.text
    sent = b"".join(r.body for r in sink.puts("/mm-result.json")).decode()
    for text in (blob, sent):
        assert "sig=" not in text and "exp=" not in text and "abcdefghijkl1234" not in text
    assert "?<redacted>" in res["error"]
    assert sink.puts("/mm-result.json"), "a best-effort mm-result.json is uploaded"


test_errors_redacted = test_pipeline_error_and_redaction


def test_error_message_clipped(sink: Sink) -> None:
    res = make_rig(sink).run(fake(error=RuntimeError("x" * 900)))
    assert len(res["mm"]["error"]["message"]) <= 500
    validate_output(res["mm"])


def test_save_artifacts_failure(sink: Sink) -> None:
    class Boom:
        truncated = {"abc": False, "semantic": False}

        def save_artifacts(self, d: Any) -> None:
            raise OSError("disk full")

    pipe = fake()
    pipe.__class__ = type("P", (type(pipe),), {"__call__": lambda self, **k: Boom()})
    assert err_code(make_rig(sink).run(pipe)) == "PIPELINE_ERROR"


def test_unreadable_audio(sink: Sink) -> None:
    res = make_rig(sink).run(fake(files={"audio.flac": b"not flac"}))
    assert err_code(res) == "PIPELINE_ERROR"


def test_result_too_large(sink: Sink) -> None:
    rig = make_rig(sink)
    raw = rig.take
    from dataclasses import replace

    from mmworker.contract import Target
    ups = dict(raw.uploads)
    ups["mm-result.json"] = Target(ups["mm-result.json"].url, 10)
    rig.take = replace(raw, uploads=ups)
    assert err_code(rig.run(fake())) == "ARTIFACT_TOO_LARGE"


def test_optional_without_target_is_skipped(sink: Sink) -> None:
    raw = make_raw(sink.base)
    del raw["uploads"]["score.abc"]
    rig = make_rig(sink, uploads=raw["uploads"])
    out = rig.run(fake(files={"score.abc": b"abc"}))
    assert "score.abc" not in out["artifacts"] and not sink.puts("/score.abc")


def test_upload_failure_returns_error(sink: Sink) -> None:
    sink.on("/config.json", 403)
    res = make_rig(sink).run(fake())
    assert err_code(res) == "UPLOAD_FAILED"


def test_upload_closed_skips_result_upload(sink: Sink) -> None:
    sink.on("/config.json", 410)
    res = make_rig(sink).run(fake())
    assert err_code(res) == "UPLOAD_CLOSED"
    assert not sink.puts("/mm-result.json")


def test_result_upload_failure(sink: Sink) -> None:
    sink.on("/mm-result.json", 409)
    res = make_rig(sink).run(fake())
    assert err_code(res) == "UPLOAD_CONFLICT"


def test_env_fields(sink: Sink) -> None:
    out = make_rig(sink).run(fake(), environ={"MM_WORKER_BUILD": "deadbeef", "MM_YUE2_COMMIT": "c" * 40})
    e = out["env"]
    assert set(e) == set(envmod.ENV_KEYS)
    assert e["worker_build"] == "deadbeef" and e["code_commit"] == "c" * 40
    assert e["engine"] == "yue2" and e["decoder"] == "m-a-p/YuE2-Vae" and e["weights"] == WEIGHTS
    assert e["python"].startswith("3.12")
    assert set(OUT_SCHEMA["properties"]["env"]["required"]) == set(e)


def test_env_defaults() -> None:
    e = envmod.fingerprint({}, {})
    assert e["worker_build"] == "unknown" and e["code_commit"] == "unknown"


# ---- deadlines ---------------------------------------------------------------------------------------

def test_deadlines_split(sink: Sink) -> None:
    rig = make_rig(sink, timeout_s=200, upload_budget_s=60)
    up, gen = deadlines(rig.take, 1000.0, {})
    assert up == 1190.0 and gen == 1130.0
    rig2 = make_rig(sink, timeout_s=200, upload_budget_s=60, deadline_unix=1100)
    up, gen = deadlines(rig2.take, 1000.0, {})
    assert up == 1040.0 and gen == 980.0


def test_worker_deadline_provider_mode(sink: Sink) -> None:
    rig = make_rig(sink)
    assert deadlines(rig.take, 1000.0, {"MM_WORKER_DEADLINE": "provider"})[1] is None
    seen: list[bool] = []

    def slow(kwargs: Any, cancelled: Any) -> None:
        rig.clock.advance(1000)  # far past what would have been the generation deadline
        seen.append(cancelled())

    out = rig.run(fake(on_call=slow), environ={"MM_WORKER_DEADLINE": "provider"})
    assert seen == [False] and out["status"] == "ok"


def test_cancelled_callback_honours_deadline(sink: Sink) -> None:
    rig = make_rig(sink, timeout_s=200, upload_budget_s=60)
    t0 = rig.clock.now
    stopped_at: list[float] = []

    def loop(kwargs: Any, cancelled: Any) -> None:
        for _ in range(1000):
            if cancelled():
                stopped_at.append(rig.clock.now)
                return
            rig.clock.advance(1)

    res = rig.run(fake(on_call=loop))
    assert err_code(res) == "DEADLINE"
    assert stopped_at and t0 + 130 < stopped_at[0] <= t0 + 131
    assert not sink.puts("/audio.flac")


def test_generation_deadline_leaves_upload_window(sink: Sink) -> None:
    rig = make_rig(sink, timeout_s=200, upload_budget_s=60)
    t0 = rig.clock.now

    def almost(kwargs: Any, cancelled: Any) -> None:
        while rig.clock.now < t0 + 129:
            assert not cancelled()
            rig.clock.advance(1)

    sink.on("/audio.flac", 503, 503, 201)  # retries cost 1 s + 2 s of the window
    out = rig.run(fake(on_call=almost))
    assert out["status"] == "ok"
    assert rig.clock.now <= t0 + 200 - 10
    assert rig.clock.sleeps == [1.0, 2.0]


def test_real_flac_duration(sink: Sink, tmp_path: Any) -> None:
    p = tmp_path / "a.flac"
    soundfile.write(p, np.sin(np.linspace(0, 440, 16000)).astype("float32"), 16000, format="FLAC")
    assert envmod.flac_seconds(p) == pytest.approx(1.0)
    assert envmod.flac_seconds(tmp_path / "missing") is None
    (tmp_path / "short").write_bytes(b"fLaC")
    assert envmod.flac_seconds(tmp_path / "short") is None
    out = make_rig(sink).run(fake(files={"audio.flac": p.read_bytes()}))
    assert out["audio_seconds"] == pytest.approx(1.0)
    assert sink.puts("/audio.flac")[0].headers["Content-Digest"] == f"sha-256=:{sha_b64(p.read_bytes())}:"


def test_yue2_imported_lazily() -> None:
    import mmworker.handler, mmworker.serverless, mmworker.selftest  # noqa: F401,E401

    assert "yue2" not in sys.modules and "torch" not in sys.modules
