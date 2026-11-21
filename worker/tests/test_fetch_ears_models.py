"""worker/tools/fetch_ears_models.py: lock validation, sources and verification (the real downloads happen only in the image build)."""

from __future__ import annotations

import hashlib
import importlib.util
import io
import json
from pathlib import Path
from typing import Any

import pytest

SPEC = importlib.util.spec_from_file_location("fetch_ears_models", Path(__file__).resolve().parents[1] / "tools" / "fetch_ears_models.py")
assert SPEC and SPEC.loader
fem = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fem)

LOCK = Path(__file__).resolve().parents[1] / "ears" / "models.lock.json"
REV = "e" * 40


def entry(role: str, files: dict[str, bytes], source: dict[str, str], license_: str = "MIT") -> dict[str, Any]:
    return {"role": role, "name": role + "-model", "source": source, "license": license_,
            "files": [{"path": p, "sha256": hashlib.sha256(b).hexdigest(), "bytes": len(b)} for p, b in files.items()]}


BLOBS = {"sep.th": b"separator", "cfg.json": b"{}", "model.bin": b"asr-weights", "full.pth": b"pitch"}


def make_lock(tmp: Path, **over: Any) -> Path:
    models = [
        entry("separator", {"sep.th": BLOBS["sep.th"]}, {"url": "https://dl.fbaipublicfiles.com/demucs/sep.th"}),
        entry("asr", {"cfg.json": BLOBS["cfg.json"], "model.bin": BLOBS["model.bin"]}, {"repo": "Org/name", "revision": REV}),
        entry("pitch", {"full.pth": BLOBS["full.pth"]}, {"url": "https://raw.githubusercontent.com/o/r/" + REV + "/full.pth"}),
    ]
    doc = {"format": "mm.ears-models/1", "models": models, **over}
    p = tmp / "lock.json"
    p.write_text(json.dumps(doc))
    return p


@pytest.fixture
def served(monkeypatch: pytest.MonkeyPatch) -> dict[str, bytes]:
    urls: dict[str, bytes] = {
        "https://dl.fbaipublicfiles.com/demucs/sep.th": BLOBS["sep.th"],
        f"https://huggingface.co/Org/name/resolve/{REV}/cfg.json": BLOBS["cfg.json"],
        f"https://huggingface.co/Org/name/resolve/{REV}/model.bin": BLOBS["model.bin"],
        f"https://raw.githubusercontent.com/o/r/{REV}/full.pth": BLOBS["full.pth"],
    }
    monkeypatch.setattr(fem, "_open", lambda url: io.BytesIO(urls[url]))
    return urls


def test_shipped_lock_parses_and_every_pin_is_complete() -> None:
    models = fem.parse_lock(LOCK.read_bytes())
    assert [m["role"] for m in models] == ["separator", "asr", "pitch"]
    asr = models[1]
    assert fem.REV_RE.match(asr["source"]["revision"]) and len(asr["files"]) == 5
    assert models[0]["source"]["url"].endswith("955717e8-8726e21a.th") and models[0]["files"][0]["sha256"].startswith("8726e21a")
    assert all(f["sha256"] != "0" * 64 for m in models for f in m["files"])


def test_fetch_writes_the_role_layout_and_verifies(tmp_path: Path, served: dict[str, bytes], capsys: pytest.CaptureFixture[str]) -> None:
    out = tmp_path / "out"
    assert fem.main(["x", str(make_lock(tmp_path)), str(out)]) == 0
    assert (out / "separator" / "sep.th").read_bytes() == b"separator" and (out / "asr" / "model.bin").read_bytes() == b"asr-weights"
    assert (out / "pitch" / "full.pth").is_file() and "verified asr/model.bin" in capsys.readouterr().out


@pytest.mark.parametrize("url", ["https://dl.fbaipublicfiles.com/demucs/sep.th", f"https://huggingface.co/Org/name/resolve/{REV}/model.bin"])
def test_a_hash_or_size_mismatch_fails_and_removes_the_output(tmp_path: Path, served: dict[str, bytes], url: str,
                                                              capsys: pytest.CaptureFixture[str]) -> None:
    served[url] = served[url][:-1] + b"X"  # same size, other bytes
    out = tmp_path / "out"
    assert fem.main(["x", str(make_lock(tmp_path)), str(out)]) == 1
    assert not out.exists() and "sha256" in capsys.readouterr().err
    served[url] = served[url] + b"+"
    assert fem.main(["x", str(make_lock(tmp_path)), str(out)]) == 1 and not out.exists()
    assert "exceeds" in capsys.readouterr().err  # the oversize body is cut off while reading


@pytest.mark.parametrize("mutate", [
    lambda d: d["models"][0].update(license="GPL-3.0-only"),
    lambda d: d["models"][0].update(license="CC-BY-NC-4.0"),
    lambda d: d["models"][1]["source"].update(revision="main"),
    lambda d: d["models"][1]["source"].update(repo="../evil"),
    lambda d: d["models"][0]["source"].update(url="http://dl.fbaipublicfiles.com/x.th"),
    lambda d: d["models"][0]["source"].update(url="https://evil.example.com/x.th"),
    lambda d: d["models"][0]["source"].update(url="https://dl.fbaipublicfiles.com/x.th?token=1"),
    lambda d: d["models"][0]["files"].append(dict(d["models"][0]["files"][0])),
    lambda d: d["models"][0].update(source={}),
    lambda d: d["models"][0]["files"][0].update(path="../x"),
    lambda d: d["models"][0]["files"][0].update(sha256="abc"),
    lambda d: d["models"][0]["files"][0].update(bytes=0),
    lambda d: d["models"][0]["files"].clear(),
    lambda d: d["models"].append(dict(d["models"][0])),
    lambda d: d["models"][0].update(role="video"),
    lambda d: d.update(format="mm.ears-models/2"),
    lambda d: d["models"].append("x"),
])
def test_parse_lock_refuses_bad_locks(mutate: Any) -> None:
    doc = json.loads(LOCK.read_text())
    mutate(doc)
    with pytest.raises(fem.VerifyError):
        fem.parse_lock(json.dumps(doc).encode())


def test_parse_lock_refuses_non_json_and_wrong_shape() -> None:
    for raw in (b"nope", b"[]", b'{"format":"mm.ears-models/1"}'):
        with pytest.raises(fem.VerifyError):
            fem.parse_lock(raw)


def test_main_usage_and_failures(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    assert fem.main(["x"]) == 2
    assert fem.main(["x", str(tmp_path / "missing.json"), str(tmp_path / "o")]) == 1
    assert "FAILED" in capsys.readouterr().err
