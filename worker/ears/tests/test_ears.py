from __future__ import annotations

import json
import math
import os
import subprocess
import sys
from pathlib import Path

import numpy as np
import pytest

from mmears import __main__ as cli
from mmears import asr, audio, cudalibs, models, output, pitch, vocals
from mmears.errors import EarsError, classify

from .conftest import FakeSeg, FakeWhisper, FakeWord, fake_stages, make_models, write_audio

SRC = Path(__file__).resolve().parents[1] / "src" / "mmears"


def run_fake(model_env, flac, stages=("separate", "asr"), **kw):
    lock, mdir = model_env
    return cli.run(str(flac), kw.pop("lang", "it"), stages, str(mdir), kw.pop("st", fake_stages()), str(lock), **kw)


# test_lazy_imports
def test_lazy_imports():
    code = (
        "import sys, mmears.__main__, mmears.asr, mmears.pitch, mmears.separate, mmears.cudalibs;"
        "heavy=[m for m in ('torch','demucs','julius','faster_whisper','ctranslate2','torchcrepe','torchaudio') if m in sys.modules];"
        "print(heavy)"
    )
    out = subprocess.run([sys.executable, "-I", "-c", code], capture_output=True, text=True, check=True,
                         env={"PYTHONPATH": str(SRC.parent)}).stdout
    assert out.strip() == "[]"


# test_output_schema
@pytest.mark.parametrize("stages", [("separate", "asr"), ("separate", "asr", "transcribe")])
def test_output_schema(model_env, flac, validate, stages):
    doc = run_fake(model_env, flac, stages)
    validate(doc)
    assert doc["status"] == "ok" and doc["code"] is None
    assert (doc["pitch"] is not None) == ("transcribe" in stages)
    assert doc["stages"]["transcribe"] == ("ok" if "transcribe" in stages else "not_requested")
    assert doc["models"]["asr"]["compute_type"] == "int8"



def test_error_document_all_codes_validate(validate):
    from mmears.errors import CODES

    for code in sorted(CODES):
        validate(output.error_document(code, "input", "boom" * 200, ("separate", "asr", "transcribe")))


# test_models_lock_missing
def test_models_lock_missing(tmp_path, flac, validate):
    doc = cli.run(str(flac), "it", ("separate", "asr"), str(tmp_path), fake_stages(), str(tmp_path / "nope.json"))
    validate(doc)
    assert doc["status"] == "error" and doc["code"] == "MODELS_MISSING"
    assert doc["errors"][0]["stage"] == "load"


def test_models_file_missing_size_and_sha(tmp_path, monkeypatch):
    monkeypatch.setattr(models.tempfile, "gettempdir", lambda: str(tmp_path))
    lock, mdir = make_models(tmp_path)
    lk = models.load_lock(lock)
    models.verify(lk, mdir)  # ok, writes the marker
    (mdir / "asr" / "w.bin").write_bytes(b"x")  # wrong size: caught even with the marker
    with pytest.raises(EarsError) as e:
        models.verify(lk, mdir)
    assert e.value.code == "MODELS_MISSING"
    (mdir / "asr" / "w.bin").unlink()
    with pytest.raises(EarsError):
        models.verify(lk, mdir)
    # same size, other bytes: only the sha256 check sees it (fresh marker dir)
    lock2, mdir2 = make_models(tmp_path / "b")
    body = (mdir2 / "pitch" / "w.bin").read_bytes()
    (mdir2 / "pitch" / "w.bin").write_bytes(b"z" * len(body))
    monkeypatch.setattr(models.tempfile, "gettempdir", lambda: str(tmp_path / "b"))
    with pytest.raises(EarsError):
        models.verify(models.load_lock(lock2), mdir2)


def test_models_sha_checked_once_per_process(tmp_path, monkeypatch):
    monkeypatch.setattr(models.tempfile, "gettempdir", lambda: str(tmp_path))
    lock, mdir = make_models(tmp_path)
    lk = models.load_lock(lock)
    calls = []
    real = models._sha256_file
    monkeypatch.setattr(models, "_sha256_file", lambda p: calls.append(p) or real(p))
    models.verify(lk, mdir)
    n = len(calls)
    assert n == 3
    models.verify(lk, mdir)
    assert len(calls) == n


def test_models_sha_marker_per_role_subset(tmp_path, monkeypatch):
    monkeypatch.setattr(models.tempfile, "gettempdir", lambda: str(tmp_path))
    lock, mdir = make_models(tmp_path)
    lk = models.load_lock(lock)
    calls = []
    real = models._sha256_file
    monkeypatch.setattr(models, "_sha256_file", lambda p: calls.append(p) or real(p))
    models.verify(lk, mdir, roles=("separator", "asr"))
    n = len(calls)
    assert n >= 1
    models.verify(lk, mdir, roles=("separator", "asr"))
    assert len(calls) == n  # second run re-hashes nothing
    models.verify(lk, mdir)  # only the pitch role is new
    assert len(calls) == n + 1


# test_models_lock_licenses
@pytest.mark.parametrize("lic", ["MIT", "Apache-2.0", "BSD-3-Clause"])
def test_models_lock_licenses_allowed(tmp_path, lic):
    lock, _ = make_models(tmp_path, license_=lic)
    assert set(models.load_lock(lock).models) == {"separator", "asr", "pitch"}


@pytest.mark.parametrize("lic", ["GPL-3.0", "CC-BY-NC-4.0", "", None, "unknown"])
def test_models_lock_licenses_refused(tmp_path, lic):
    lock, _ = make_models(tmp_path, license_=lic)
    with pytest.raises(EarsError) as e:
        models.load_lock(lock)
    assert e.value.code == "MODELS_MISSING" and "licence" in e.value.message


@pytest.mark.parametrize(
    "mutate",
    [
        lambda d: d.update(format="x"),
        lambda d: d["models"][0].update(role="other"),
        lambda d: d["models"][0]["files"][0].update(path="../evil"),
        lambda d: d["models"][0]["files"][0].update(path="/abs"),
        lambda d: d["models"][0]["files"][0].update(sha256="ABC"),
        lambda d: d["models"][0]["files"][0].update(bytes=-1),
        lambda d: d["models"][0].update(files=[]),
        lambda d: d["models"][0].update(source={}),
        lambda d: d["models"].append(dict(d["models"][0])),
    ],
)
def test_models_lock_malformed(tmp_path, mutate):
    lock, _ = make_models(tmp_path)
    doc = json.loads(lock.read_text())
    mutate(doc)
    with pytest.raises(EarsError):
        models.parse_lock(json.dumps(doc).encode())
    with pytest.raises(EarsError):
        models.parse_lock(b"not json")


def test_model_digest_multi_file_is_stable():
    f = models.ModelFile
    a = models.Model("asr", "n", "MIT", (f("b", "1" * 64, 1), f("a", "2" * 64, 1)))
    b = models.Model("asr", "n", "MIT", (f("a", "2" * 64, 1), f("b", "1" * 64, 1)))
    assert a.digest() == b.digest() and len(a.digest()) == 64


def test_shipped_lock_if_present_is_valid():
    p = models.DEFAULT_LOCK
    if p.exists():
        models.load_lock(p)


# test_stage_errors_collected
def test_stage_errors_collected(model_env, flac, validate):
    # transcribe fails: asr still ok, status ok, pitch null, error recorded
    doc = run_fake(model_env, flac, ("separate", "asr", "transcribe"),
                   st=fake_stages(fail={"transcribe": RuntimeError("boom")}))
    validate(doc)
    assert doc["status"] == "ok" and doc["stages"]["transcribe"] == "error" and doc["pitch"] is None
    assert [e["stage"] for e in doc["errors"]] == ["transcribe"]
    # asr fails: transcribe still runs, status error ASR_FAILED
    doc = run_fake(model_env, flac, ("separate", "asr", "transcribe"),
                   st=fake_stages(fail={"asr": RuntimeError("bad")}))
    validate(doc)
    assert doc["status"] == "error" and doc["code"] == "ASR_FAILED"
    assert doc["stages"] == {"separate": "ok", "asr": "error", "transcribe": "ok"}
    assert doc["pitch"] is not None and doc["models"]["asr"] is None
    # separation fails: dependants are skipped
    doc = run_fake(model_env, flac, ("separate", "asr", "transcribe"),
                   st=fake_stages(fail={"separate": RuntimeError("bad")}))
    validate(doc)
    assert doc["code"] == "SEPARATION_FAILED"
    assert doc["stages"] == {"separate": "error", "asr": "skipped", "transcribe": "skipped"}
    assert doc["vocals"] is None


def test_input_decode_failure(model_env, tmp_path, validate):
    bad = tmp_path / "bad.flac"
    bad.write_bytes(b"not audio at all")
    doc = run_fake(model_env, bad)
    validate(doc)
    assert doc["code"] == "INPUT_DECODE" and doc["errors"][0]["stage"] == "input" and doc["input"] is None


# test_oom_recorded
def test_oom_recorded(model_env, flac, validate):
    class OutOfMemoryError(RuntimeError):
        pass

    doc = run_fake(model_env, flac, st=fake_stages(fail={"asr": OutOfMemoryError("CUDA out of memory")}))
    validate(doc)
    assert doc["code"] == "OOM" and doc["errors"][0]["code"] == "OOM"
    doc = run_fake(model_env, flac, st=fake_stages(fail={"separate": MemoryError()}))
    assert doc["code"] == "OOM"
    assert classify(ValueError("x"), "ASR_FAILED").code == "ASR_FAILED"


# test_lang_forced_and_auto
def test_lang_forced_and_auto(model_env, flac, validate):
    seen: list[str] = []
    d = run_fake(model_env, flac, lang="it", st=fake_stages(lang_seen=seen))
    assert d["asr"]["forced"] is True and d["asr"]["language"] == "it"
    FakeWhisper.calls.clear()
    d = run_fake(model_env, flac, lang="auto", st=fake_stages(lang_seen=seen))
    validate(d)
    assert d["asr"]["forced"] is False and d["asr"]["language"] == "it"  # detected
    assert seen == ["it", "auto"]
    assert FakeWhisper.calls[-1]["language"] is None


# test_no_prompt_passed
def test_no_prompt_passed():
    FakeWhisper.calls.clear()
    asr.transcribe(np.zeros(16000, np.float32), "it", Path("m"), "cpu", lambda s: None, lambda *a: FakeWhisper())
    kw = FakeWhisper.calls[0]
    assert kw["initial_prompt"] is None and "prompt" not in kw and "hotwords" not in kw
    assert kw["word_timestamps"] is True and kw["vad_filter"] is False
    assert kw["condition_on_previous_text"] is False and kw["beam_size"] == 5 and kw["temperature"] == 0
    assert kw["language"] == "it"


# test_words_capped_20000
def test_words_capped_20000(validate):
    segs = [
        {"start": float(i), "end": i + 1.0, "avg_logprob": -0.1, "no_speech_p": 0.0,
         "words": [{"w": f"w{i}_{j}", "start": i + j / 20, "end": i + j / 20 + 0.04, "p": 0.5} for j in range(20)]}
        for i in range(1100)
    ]
    block = output.normalise_asr("it", True, segs)
    assert len(block["words"]) == output.MAX_WORDS == 20000
    assert len(block["segments"]) == 1000
    assert block["segments"][-1]["w1"] == 20000
    doc = output.error_document("ASR_FAILED", "asr", "x")
    doc.update(status="ok", code=None, asr=block)
    doc["stages"]["asr"] = "ok"
    doc["errors"] = []
    validate(doc)
    # a segment cut mid-way keeps a consistent half-open range
    big = {**segs[0], "words": [{"w": f"b{j}", "start": 5000 + j, "end": 5001 + j, "p": 0.5} for j in range(30)]}
    b2 = output.normalise_asr("it", True, segs[:999] + [big, segs[1000]])
    assert len(b2["words"]) == 20000 and len(b2["segments"]) == 1000  # the 1001st segment is dropped
    assert b2["segments"][-1]["w0"] == 19980 and b2["segments"][-1]["w1"] == 20000
    assert all(0 <= s["w0"] <= s["w1"] <= len(b2["words"]) for s in b2["segments"])


def test_segments_capped_and_words_sane():
    segs = [{"start": 0.0, "end": 1.0, "avg_logprob": 0.5, "no_speech_p": 2.0, "words": []} for _ in range(4100)]
    b = output.normalise_asr("it", True, segs)
    assert len(b["segments"]) == 4000
    assert b["segments"][0]["avg_logprob"] == 0.0 and b["segments"][0]["no_speech_p"] == 1.0
    seg = {"start": 1.0, "end": 0.5, "avg_logprob": -1.0, "no_speech_p": 0.1,
           "words": [{"w": " b ", "start": 2.0, "end": 1.0, "p": 1.5}, {"w": "a", "start": 1.0, "end": 1.2, "p": 0.1},
                     {"w": "  ", "start": 3, "end": 4, "p": 1}, {"w": "x" * 100, "start": 3, "end": 4, "p": 1}]}
    b = output.normalise_asr("it", True, [seg])
    w = b["words"]
    assert [x["w"] for x in w] == ["b", "a", "x" * 64]
    assert w[0]["end"] >= w[0]["start"] and w[1]["start"] >= w[0]["start"] and w[0]["p"] == 1.0
    assert b["segments"][0]["end"] >= b["segments"][0]["start"]


# test_pitch_frames_shape
@pytest.mark.parametrize("duration", [0.01, 1.0, 10.0, 215.8, 3.0001])
def test_pitch_frames_shape(duration):
    n10 = int(duration / 0.01) + 1
    p = pitch.to_frames(np.full(n10, 440.0), np.full(n10, 0.9), duration)
    assert len(p["cents"]) == len(p["conf"]) == math.ceil(round(duration / 0.02, 6))
    assert p["hop_s"] == 0.02
    assert set(p["cents"]) == {6900} and set(p["conf"]) == {90}


def test_pitch_frame_values_and_padding():
    f0 = np.array([440.0, 440.0, np.nan, np.nan, 220.0, 220.0, 0.0, 0.0, 880.0, 880.0])
    per = np.array([0.9, 0.9, 0.9, 0.9, 0.1, 0.1, 0.9, 0.9, 1.0, 1.0])
    p = pitch.to_frames(f0, per, 0.2)  # 10 frames wanted, 5 available -> padded
    assert len(p["cents"]) == 10
    assert p["cents"][:5] == [6900, 0, 0, 0, 8100]  # nan, low periodicity, f0=0 -> unvoiced
    assert p["cents"][5:] == [0] * 5 and p["conf"][5:] == [0] * 5
    assert p["conf"][:5] == [90, 90, 10, 90, 100]
    # very low pitch never goes negative
    assert pitch.to_frames([1.0, 1.0], [1.0, 1.0], 0.02)["cents"] == [0]


def test_pitch_frame_cap():
    n = 36000 * 2 + 100
    p = pitch.to_frames(np.full(n, 300.0), np.full(n, 1.0), 1000.0)
    assert len(p["cents"]) == 36000 == len(p["conf"])


def test_frame_count_floating_point():
    assert pitch.frame_count(10.0) == 500
    assert pitch.frame_count(215.8) == 10790


# test_mp3_input_decodes
def test_mp3_input_decodes(tmp_path):
    import soundfile as sf

    def no_mp3(why):
        if os.environ.get("CI"):
            pytest.fail(why)  # never vacuous in CI
        pytest.skip(why)

    if "MP3" not in sf.available_formats():
        no_mp3("libsndfile without MP3")
    p = tmp_path / "in.mp3"
    try:
        write_audio(p, 2.0, 44100)
    except Exception:
        no_mp3("libsndfile cannot encode MP3 here")
    d = audio.decode(str(p))
    assert d.codec == "mp3" and d.channels == 2 and d.sample_rate == 44100
    assert 1.9 < d.duration_s < 2.2


def test_flac_decodes_and_mono(flac):
    d = audio.decode(str(flac))
    assert d.codec == "flac" and d.sample_rate == 16000 and d.channels == 2
    assert d.duration_s == pytest.approx(3.0)
    assert audio.to_mono(d.samples).shape == (48000,)


# test_no_torchaudio_io
def test_no_torchaudio_io():
    for f in SRC.glob("*.py"):
        text = f.read_text()
        assert "import torchaudio" not in text and "from torchaudio" not in text, f.name


def test_vocals_measure():
    sr = 16000
    x = np.concatenate([0.5 * np.sin(np.linspace(0, 2000, sr)), np.zeros(sr)]).astype(np.float32)
    v = vocals.measure(x, sr)
    assert v["rms_dbfs"] <= 0 and v["active_s"] == pytest.approx(1.0, abs=0.1)
    assert vocals.measure(np.zeros(100), sr) == {"rms_dbfs": -200.0, "active_s": 0.0}
    assert vocals.measure([], sr) == {"rms_dbfs": 0.0, "active_s": 0.0}


def test_cudalibs(tmp_path):
    a = tmp_path / "nvidia" / "cublas" / "lib"
    b = tmp_path / "nvidia" / "cudnn" / "lib"
    a.mkdir(parents=True)
    b.mkdir(parents=True)
    assert cudalibs.lib_dirs([tmp_path]) == [b.parent.parent / "cublas" / "lib", b]
    assert cudalibs.library_path([tmp_path], "/x") == f"{a}:{b}:/x"
    assert cudalibs.library_path([tmp_path / "none"]) == ""
    assert cudalibs.preload([tmp_path]) == []


def test_cli_writes_document_and_self_check(model_env, flac, tmp_path, monkeypatch, validate):
    lock, mdir = model_env
    monkeypatch.setattr(cli, "real_stages", lambda: fake_stages())
    out = tmp_path / "analysis.json"
    rc = cli.main(["--audio", str(flac), "--lang", "it", "--stages", "separate,asr,transcribe",
                   "--models", str(mdir), "--out", str(out), "--lock", str(lock), "--parent-reserved-mib", "13210"])
    assert rc == 0
    doc = json.loads(out.read_text())
    validate(doc)
    assert doc["resources"]["parent_reserved_mib"] == 13210
    assert cli.main(["--self-check", "--models", str(mdir), "--lock", str(lock)]) == 0
    assert cli.main(["--self-check", "--models", str(tmp_path / "x"), "--lock", str(lock)]) == 1


def test_cli_models_missing_exits_zero_with_document(flac, tmp_path, monkeypatch, validate):
    out = tmp_path / "o.json"
    rc = cli.main(["--audio", str(flac), "--models", str(tmp_path), "--out", str(out), "--lock", str(tmp_path / "no")])
    assert rc == 0
    d = json.loads(out.read_text())
    validate(d)
    assert d["code"] == "MODELS_MISSING"


def test_cli_unexpected_crash_writes_error_document(flac, tmp_path, monkeypatch, validate):
    def boom(*a, **k):
        raise RuntimeError("x")

    monkeypatch.setattr(cli, "run", boom)
    out = tmp_path / "o.json"
    assert cli.main(["--audio", str(flac), "--models", str(tmp_path), "--out", str(out)]) == 0
    d = json.loads(out.read_text())
    validate(d)
    assert d["code"] == "SUBPROCESS_FAILED"


@pytest.mark.parametrize("spec", ["asr", "separate", "separate,asr,pitch", "asr,separate", "separate,asr,transcribe,x"])
def test_bad_stages_refused(spec):
    with pytest.raises(ValueError):
        cli.parse_stages(spec)
    assert cli.parse_stages("separate,asr,transcribe") == ("separate", "asr", "transcribe")


def test_output_write_is_atomic_and_bounded(tmp_path):
    out = tmp_path / "a.json"
    output.write(out, output.error_document("OOM", "asr", "m"))
    assert json.loads(out.read_text())["code"] == "OOM"
    assert [p.name for p in tmp_path.iterdir()] == ["a.json"]
    doc = output.error_document("OOM", "asr", "m")
    doc["errors"][0]["message"] = "x" * (5 << 20)
    doc["asr"] = {"blob": "y" * (5 << 20)}
    with pytest.raises(ValueError):
        output.dumps(doc)
