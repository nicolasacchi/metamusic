"""The build-time VAE fetcher (tools/fetch_vae.py) against a local fake Hugging Face, and resolve_vae."""

from __future__ import annotations

import hashlib
import importlib.util
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest

from mmworker.pipeline import resolve_vae

TOOL = Path(__file__).resolve().parents[1] / "tools" / "fetch_vae.py"
REPO = "m-a-p/YuE2-Vae"
REV = "c" * 40

spec = importlib.util.spec_from_file_location("fetch_vae", TOOL)
assert spec and spec.loader
fetch_vae = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetch_vae)


def blob_id(data: bytes) -> str:
    return hashlib.sha1(f"blob {len(data)}\0".encode() + data).hexdigest()  # noqa: S324


FILES: dict[str, bytes] = {
    "config.json": b'{"model_type":"yue2_vae"}',
    "weights_manifest.json": b"{}",
    "modeling_vae.py": b"# code\n",
    "model.safetensors": b"W" * 5000,
    "LICENSE": b"licence",
    "licenses/x-MIT.txt": b"mit",
    "README.md": b"not wanted",
    "assets/figure.png": b"not wanted",
}


def tree(files: dict[str, bytes]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for path, data in files.items():
        e: dict[str, Any] = {"type": "file", "path": path, "size": len(data), "oid": blob_id(data)}
        if path == "model.safetensors":
            e["lfs"] = {"oid": hashlib.sha256(data).hexdigest(), "size": len(data)}
        out.append(e)
    out.append({"type": "directory", "path": "licenses", "size": 0, "oid": "d" * 40})
    return out


@pytest.fixture
def hf(monkeypatch: pytest.MonkeyPatch) -> dict[str, Any]:
    state: dict[str, Any] = {"served": dict(FILES), "listed": dict(FILES)}

    class H(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            if self.path == f"/api/models/{REPO}/tree/{REV}?recursive=true":
                body = json.dumps(tree(state["listed"])).encode()
            elif self.path.startswith(f"/{REPO}/resolve/{REV}/"):
                name = self.path.split(f"/resolve/{REV}/", 1)[1]
                if name not in state["served"]:
                    self.send_error(404)
                    return
                body = state["served"][name]
            else:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a: Any) -> None:
            pass

    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    monkeypatch.setattr(fetch_vae, "HOST", f"http://127.0.0.1:{srv.server_address[1]}")
    yield state
    srv.shutdown()
    srv.server_close()


def test_fetch_writes_hub_layout_and_only_wanted_files(hf: dict[str, Any], tmp_path: Path) -> None:
    names = fetch_vae.fetch(REPO, REV, tmp_path)
    snap = tmp_path / "models--m-a-p--YuE2-Vae" / "snapshots" / REV
    assert sorted(names) == sorted(set(FILES) - {"README.md", "assets/figure.png"})
    assert (snap / "model.safetensors").read_bytes() == FILES["model.safetensors"]
    assert (snap / "licenses" / "x-MIT.txt").read_bytes() == b"mit"
    assert not (snap / "README.md").exists() and not (snap / "assets").exists()
    assert (tmp_path / "models--m-a-p--YuE2-Vae" / "refs" / "main").read_text() == REV


def test_lfs_sha256_mismatch_fails_and_leaves_nothing(hf: dict[str, Any], tmp_path: Path) -> None:
    hf["served"]["model.safetensors"] = b"X" * 5000  # same size, different bytes
    with pytest.raises(fetch_vae.VerifyError, match="sha256"):
        fetch_vae.fetch(REPO, REV, tmp_path)
    assert not (tmp_path / "models--m-a-p--YuE2-Vae").exists()


def test_git_blob_mismatch_fails(hf: dict[str, Any], tmp_path: Path) -> None:
    hf["served"]["config.json"] = b'{"model_type":"evil__"}'
    with pytest.raises(fetch_vae.VerifyError, match="config.json"):
        fetch_vae.fetch(REPO, REV, tmp_path)
    assert not (tmp_path / "models--m-a-p--YuE2-Vae").exists()


def test_size_mismatch_and_missing_required_file(hf: dict[str, Any], tmp_path: Path) -> None:
    hf["served"]["LICENSE"] = b"short"
    with pytest.raises(fetch_vae.VerifyError, match="size"):
        fetch_vae.fetch(REPO, REV, tmp_path)
    hf["served"]["LICENSE"] = FILES["LICENSE"]
    del hf["listed"]["model.safetensors"]
    with pytest.raises(fetch_vae.VerifyError, match="missing"):
        fetch_vae.fetch(REPO, REV, tmp_path)


@pytest.mark.parametrize("repo,rev", [("not-a-repo", REV), (REPO, "main"), (REPO, "C" * 40)])
def test_bad_arguments_refused(repo: str, rev: str, tmp_path: Path) -> None:
    with pytest.raises(fetch_vae.VerifyError):
        fetch_vae.fetch(repo, rev, tmp_path)


def test_main_exit_codes(hf: dict[str, Any], tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    assert fetch_vae.main(["x"]) == 2
    assert fetch_vae.main(["x", REPO, REV, str(tmp_path)]) == 0
    hf["served"]["model.safetensors"] = b"X" * 5000
    assert fetch_vae.main(["x", REPO, REV, str(tmp_path / "again")]) == 1
    assert "FAILED" in capsys.readouterr().err


def test_resolve_vae(tmp_path: Path) -> None:
    assert resolve_vae({}, REPO, REV) == REPO  # no directory configured: the repo id (3B cache serves both)
    snap = tmp_path / "models--m-a-p--YuE2-Vae" / "snapshots" / REV
    snap.mkdir(parents=True)
    (snap / "config.json").write_text("{}")
    assert resolve_vae({"MM_YUE2_VAE_DIR": str(tmp_path)}, REPO, REV) == str(snap)
    with pytest.raises(FileNotFoundError):  # another revision is not silently accepted
        resolve_vae({"MM_YUE2_VAE_DIR": str(tmp_path)}, REPO, "d" * 40)
