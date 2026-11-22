"""Run-time fetch of the pinned 3B weights for MODE=agent (C397.3, D-BULK-15).

Same rules as tools/fetch_vae.py, which does this at image build time: list the repository tree at one 40-hex revision,
download every file of it at that revision and verify each against the listing (LFS files by SHA-256 and size, plain
git files by their git blob id). A mismatch deletes the file and fails WEIGHTS_MISMATCH. At most 16 GiB in total. Only
`m-a-p/YuE2-3B` can be fetched; no token is sent; the file layout is the Hugging Face hub cache that resolve_3b reads.
Standard library only."""

from __future__ import annotations

import hashlib
import json
import re
import urllib.parse
import urllib.request
from collections.abc import Callable
from pathlib import Path
from typing import Any

from .env import WEIGHTS_3B
from .errors import WorkerError

HOST = "https://huggingface.co"
MAX_TOTAL_BYTES = 16 << 30
REV_RE = re.compile(r"^[0-9a-f]{40}$")
_BLOCK = 1 << 20

# opener(url) -> a binary file-like object (context manager) with read()
Opener = Callable[[str], Any]


def _opener(user_agent: str) -> Opener:
    def open_url(url: str) -> Any:
        req = urllib.request.Request(url, headers={"User-Agent": user_agent})  # no Authorization, ever
        return urllib.request.urlopen(req, timeout=120)  # noqa: S310 - https URL built from validated parts
    return open_url


def _git_blob_id(path: Path, size: int) -> str:
    h = hashlib.sha1(f"blob {size}\0".encode())  # noqa: S324 - git object id, not a security digest
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(_BLOCK), b""):
            h.update(block)
    return h.hexdigest()


def _sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(_BLOCK), b""):
            h.update(block)
    return h.hexdigest()


def verify(path: Path, entry: dict[str, Any]) -> None:
    """Raises WorkerError(WEIGHTS_MISMATCH) unless the file is exactly what the listing says (fetch_vae.verify)."""
    size = path.stat().st_size
    if size != entry["size"]:
        raise WorkerError("WEIGHTS_MISMATCH", f"{entry['path']}: size {size} != {entry['size']}")
    lfs = entry.get("lfs")
    if lfs:
        got, want = _sha256(path), lfs["oid"]
        if got != want:
            raise WorkerError("WEIGHTS_MISMATCH", f"{entry['path']}: sha256 mismatch")
    else:
        got, want = _git_blob_id(path, size), entry["oid"]
        if got != want:
            raise WorkerError("WEIGHTS_MISMATCH", f"{entry['path']}: git blob id mismatch")


def _safe(rel: str) -> bool:
    parts = rel.split("/")
    return bool(rel) and not rel.startswith("/") and "\\" not in rel and all(p not in ("", ".", "..") for p in parts)


def _listing(open_url: Opener, host: str, repo: str, revision: str) -> list[dict[str, Any]]:
    with open_url(f"{host}/api/models/{repo}/tree/{revision}?recursive=true") as r:
        entries = json.load(r)
    if not isinstance(entries, list):
        raise WorkerError("WEIGHTS_MISMATCH", "the tree listing is not a list")
    out = [e for e in entries if isinstance(e, dict) and e.get("type") == "file"]
    for e in out:
        if not isinstance(e.get("path"), str) or not _safe(e["path"]) or type(e.get("size")) is not int or e["size"] < 0:
            raise WorkerError("WEIGHTS_MISMATCH", "the tree listing holds a malformed entry")
        if not (e.get("lfs") or isinstance(e.get("oid"), str)):
            raise WorkerError("WEIGHTS_MISMATCH", f"{e['path']}: the listing has no digest")
    return out


def _download(open_url: Opener, url: str, dest: Path, size: int) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    with open_url(url) as r, open(dest, "wb") as out:
        left = size + 1  # one byte over the listed size is enough to fail the size check without filling the disk
        while left > 0:
            block = r.read(min(_BLOCK, left))
            if not block:
                break
            out.write(block)
            left -= len(block)


def fetch(repo: str, revision: str, hub: Path, *, host: str = HOST, user_agent: str = "metamusic-worker",
          opener: Opener | None = None, log: Callable[[str], None] = lambda s: None) -> list[str]:
    """Fetch every file of repo@revision into hub/models--ORG--NAME/snapshots/<revision>/ and return their names.
    Files already present and verified are kept (a restarted container resumes); anything else is replaced."""
    if repo != WEIGHTS_3B:
        raise WorkerError("WEIGHTS_MISMATCH", f"only {WEIGHTS_3B} can be fetched")
    if not REV_RE.match(revision):
        raise WorkerError("WEIGHTS_MISMATCH", "the revision must be 40 lowercase hex")
    open_url = opener or _opener(user_agent)
    entries = _listing(open_url, host, repo, revision)
    names = {e["path"] for e in entries}
    if "config.json" not in names or not any(n.endswith(".safetensors") for n in names):
        raise WorkerError("WEIGHTS_MISMATCH", f"config.json or the safetensors are missing from {repo}@{revision[:12]}")
    if sum(e["size"] for e in entries) > MAX_TOTAL_BYTES:
        raise WorkerError("WEIGHTS_MISMATCH", "the listing is over 16 GiB")
    root = hub / ("models--" + repo.replace("/", "--"))
    snap = root / "snapshots" / revision
    for e in entries:
        dest = snap / e["path"]
        if dest.is_file():
            try:
                verify(dest, e)
                log(f"hubfetch: kept {e['path']}")
                continue
            except WorkerError:
                pass
        url = f"{host}/{repo}/resolve/{revision}/{urllib.parse.quote(e['path'])}"
        try:
            _download(open_url, url, dest, e["size"])
            verify(dest, e)
        except BaseException:
            dest.unlink(missing_ok=True)
            raise
        log(f"hubfetch: verified {e['path']} ({e['size']} bytes)")
    (root / "refs").mkdir(parents=True, exist_ok=True)
    (root / "refs" / "main").write_text(revision)
    return sorted(names)
