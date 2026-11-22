"""hubfetch: the run-time twin of tools/fetch_vae.py (C397.3)."""

from __future__ import annotations

import hashlib
import importlib.util
import io
import json
from pathlib import Path
from typing import Any

import pytest

from mmworker import hubfetch, pipeline
from mmworker.errors import WorkerError

REV = "c" * 40
REPO = "m-a-p/YuE2-3B"


def git_id(data: bytes) -> str:
    return hashlib.sha1(b"blob %d\0" % len(data) + data).hexdigest()  # noqa: S324


class Hub:
    """An in-memory Hugging Face: listing at tree/<rev>, files at resolve/<rev>/<path>; records every URL."""

    def __init__(self, files: dict[str, bytes], lfs: set[str] = frozenset(), rev: str = REV) -> None:  # type: ignore[assignment]
        self.files, self.lfs, self.rev = files, set(lfs), rev
        self.urls: list[str] = []
        self.corrupt: set[str] = set()
        self.listing: Any = None

    def entries(self) -> list[dict[str, Any]]:
        out = []
        for p, d in self.files.items():
            e: dict[str, Any] = {"type": "file", "path": p, "size": len(d), "oid": git_id(d)}
            if p in self.lfs:
                e["lfs"] = {"oid": hashlib.sha256(d).hexdigest(), "size": len(d)}
            out.append(e)
        return out

    def __call__(self, url: str) -> Any:
        self.urls.append(url)
        if f"/api/models/{REPO}/tree/{self.rev}?recursive=true" in url:
            doc = self.listing if self.listing is not None else self.entries() + [{"type": "directory", "path": "sub"}]
            return io.BytesIO(json.dumps(doc).encode())
        prefix = f"https://hub.test/{REPO}/resolve/{self.rev}/"
        assert url.startswith(prefix), url
        path = url[len(prefix):]
        data = self.files[path]
        return io.BytesIO(data + b"X" if path in self.corrupt else data)


FILES = {"config.json": b'{"a":1}', "model.safetensors": b"w" * 5000, "tokenizer/tok.json": b"{}"}


def run(hub: Hub, tmp_path: Path, **kw: Any) -> list[str]:
    return hubfetch.fetch(REPO, REV, tmp_path / "hub", host="https://hub.test", opener=hub, **kw)


def test_fetch_layout_resolves_like_the_runtime(tmp_path: Path) -> None:
    hub = Hub(FILES, lfs={"model.safetensors"})
    names = run(hub, tmp_path)
    assert names == sorted(FILES)
    snap = tmp_path / "hub" / "models--m-a-p--YuE2-3B" / "snapshots" / REV
    assert (snap / "tokenizer" / "tok.json").read_bytes() == b"{}"
    assert (tmp_path / "hub" / "models--m-a-p--YuE2-3B" / "refs" / "main").read_text() == REV
    assert pipeline.resolve_3b({"MM_YUE2_3B_DIR": str(tmp_path / "hub")}, REPO, REV) == str(snap)


def test_agent_fetches_only_pinned_revision(tmp_path: Path) -> None:
    hub = Hub(FILES, lfs={"model.safetensors"})
    run(hub, tmp_path)
    assert hub.urls and all(f"/{REV}/" in u or f"/tree/{REV}?" in u for u in hub.urls)
    assert all(u.startswith("https://hub.test/") and "main" not in u for u in hub.urls)
    for bad in ("main", REV[:39], REV.upper(), "../" + REV):
        with pytest.raises(WorkerError) as e:
            hubfetch.fetch(REPO, bad, tmp_path / "x", opener=hub)
        assert e.value.code == "WEIGHTS_MISMATCH"
    with pytest.raises(WorkerError):
        hubfetch.fetch("evil/other", REV, tmp_path / "x", opener=hub)
    assert not (tmp_path / "x").exists()


def test_agent_weights_sha_mismatch_fails(tmp_path: Path) -> None:
    hub = Hub(FILES, lfs={"model.safetensors"})
    hub.files = dict(FILES)
    entries = hub.entries()
    hub.files["model.safetensors"] = b"v" * 5000  # same size, other bytes: the listing says otherwise
    hub.entries = lambda: entries  # type: ignore[method-assign]
    with pytest.raises(WorkerError) as e:
        run(hub, tmp_path)
    assert e.value.code == "WEIGHTS_MISMATCH" and "sha256" in e.value.message
    snap = tmp_path / "hub" / "models--m-a-p--YuE2-3B" / "snapshots" / REV
    assert not (snap / "model.safetensors").exists(), "a bad file is deleted"
    assert not (tmp_path / "hub" / "models--m-a-p--YuE2-3B" / "refs" / "main").exists()


def test_size_overflow_stops_the_download(tmp_path: Path) -> None:
    hub = Hub(FILES, lfs={"model.safetensors"})
    hub.corrupt = {"model.safetensors"}
    with pytest.raises(WorkerError) as e:
        run(hub, tmp_path)
    assert "size" in e.value.message


def test_resume_keeps_verified_files(tmp_path: Path) -> None:
    hub = Hub(FILES, lfs={"model.safetensors"})
    run(hub, tmp_path)
    hub.urls.clear()
    snap = tmp_path / "hub" / "models--m-a-p--YuE2-3B" / "snapshots" / REV
    (snap / "config.json").write_bytes(b"torn")
    run(hub, tmp_path)
    assert [u.rsplit("/", 1)[1] for u in hub.urls if "/resolve/" in u] == ["config.json"]
    assert (snap / "config.json").read_bytes() == FILES["config.json"]


@pytest.mark.parametrize("path", ["../escape", "/abs", "a//b", "a/../b", "a\\b", ""])
def test_unsafe_paths_refused(tmp_path: Path, path: str) -> None:
    hub = Hub({**FILES, path: b"x"})
    with pytest.raises(WorkerError):
        run(hub, tmp_path)
    assert not (tmp_path / "escape").exists()


def test_required_files_and_total_bound(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    with pytest.raises(WorkerError, match="missing"):
        run(Hub({"config.json": b"{}"}), tmp_path)
    monkeypatch.setattr(hubfetch, "MAX_TOTAL_BYTES", 100)
    with pytest.raises(WorkerError, match="16 GiB"):
        run(Hub(FILES, lfs={"model.safetensors"}), tmp_path)


def test_listing_without_digest_refused(tmp_path: Path) -> None:
    hub = Hub(FILES)
    hub.listing = [{"type": "file", "path": "config.json", "size": 3}]
    with pytest.raises(WorkerError, match="digest"):
        run(hub, tmp_path)
    hub.listing = {"not": "a list"}
    with pytest.raises(WorkerError, match="not a list"):
        run(hub, tmp_path)


def test_default_opener_sends_user_agent_and_no_token() -> None:
    seen: dict[str, Any] = {}

    class R:
        def __init__(self, req: Any) -> None:
            seen["req"] = req

    import urllib.request

    orig = urllib.request.urlopen
    urllib.request.urlopen = lambda req, timeout: R(req)  # type: ignore[assignment]
    try:
        hubfetch._opener("metamusic-worker/9")("https://hub.test/x")
    finally:
        urllib.request.urlopen = orig  # type: ignore[assignment]
    headers = {k.lower(): v for k, v in seen["req"].header_items()}
    assert headers == {"user-agent": "metamusic-worker/9"}


def _fetch_vae() -> Any:
    spec = importlib.util.spec_from_file_location("fetch_vae", Path(__file__).parents[1] / "tools" / "fetch_vae.py")
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


@pytest.mark.parametrize("case", ["ok-lfs", "ok-git", "bad-size", "bad-sha", "bad-blob"])
def test_hubfetch_matches_fetch_vae_rules(tmp_path: Path, case: str) -> None:
    """The same listing entry and file are judged identically by the build-time tool and the runtime fetch."""
    vae = _fetch_vae()
    data = b"hello world" * 10
    entry: dict[str, Any] = {"path": "f.bin", "size": len(data), "oid": git_id(data)}
    if case in ("ok-lfs", "bad-sha"):
        entry["lfs"] = {"oid": hashlib.sha256(data if case == "ok-lfs" else b"other").hexdigest()}
    if case == "bad-size":
        entry["size"] += 1
    if case == "bad-blob":
        entry["oid"] = "0" * 40
    f = tmp_path / "f.bin"
    f.write_bytes(data)
    expected_ok = case.startswith("ok")
    for fn, exc in ((vae.verify, vae.VerifyError), (hubfetch.verify, WorkerError)):
        if expected_ok:
            fn(f, entry)
        else:
            with pytest.raises(exc):
                fn(f, entry)
    assert hubfetch.MAX_TOTAL_BYTES == 16 << 30 and hubfetch.REV_RE.pattern == vae.REV_RE.pattern
