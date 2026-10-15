"""Build-time fetch of the YuE2 VAE weights into a Hugging Face hub-cache layout (worker/Dockerfile).

    python fetch_vae.py REPO REVISION HUB_DIR

Lists the repository tree at the 40-hex REVISION (https://huggingface.co/api/models/REPO/tree/REVISION),
downloads each wanted file at that revision and verifies it against the listing: LFS files by SHA-256
(`lfs.oid`) and size, plain git files by their git blob id. Any mismatch deletes the output and exits 1, which
fails the image build. Writes HUB_DIR/models--ORG--NAME/snapshots/REVISION/<path> and refs/main. Standard
library only; it runs at build time and is never part of the runtime import path.
"""

from __future__ import annotations

import hashlib
import json
import re
import shutil
import sys
import urllib.parse
import urllib.request
from pathlib import Path

HOST = "https://huggingface.co"
# What yue2's resolve_model would fetch for a VAE (storage.py MODEL_FILES + licenses/), nothing else.
WANTED = {
    "config.json", "weights_manifest.json", "model.safetensors", "modeling_vae.py", "LICENSE",
    "THIRD_PARTY_NOTICES.md",
}
WANTED_PREFIX = "licenses/"
REPO_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$")
REV_RE = re.compile(r"^[0-9a-f]{40}$")


class VerifyError(Exception):
    pass


def _open(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "metamusic-worker-build"})
    return urllib.request.urlopen(req, timeout=120)  # noqa: S310 - https URL built from validated parts


def listing(repo: str, revision: str) -> list[dict]:
    with _open(f"{HOST}/api/models/{repo}/tree/{revision}?recursive=true") as r:
        entries = json.load(r)
    return [e for e in entries if e.get("type") == "file"
            and (e["path"] in WANTED or e["path"].startswith(WANTED_PREFIX))]


def git_blob_id(path: Path, size: int) -> str:
    h = hashlib.sha1(f"blob {size}\0".encode())  # noqa: S324 - git object id, not a security digest
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def verify(path: Path, entry: dict) -> None:
    size = path.stat().st_size
    if size != entry["size"]:
        raise VerifyError(f"{entry['path']}: size {size} != {entry['size']}")
    lfs = entry.get("lfs")
    if lfs:
        got, want = sha256_of(path), lfs["oid"]
        if got != want:
            raise VerifyError(f"{entry['path']}: sha256 {got} != {want}")
    else:
        got, want = git_blob_id(path, size), entry["oid"]
        if got != want:
            raise VerifyError(f"{entry['path']}: git blob id {got} != {want}")


def fetch(repo: str, revision: str, hub: Path) -> list[str]:
    if not REPO_RE.match(repo) or not REV_RE.match(revision):
        raise VerifyError("repo must be ORG/NAME and revision 40 lowercase hex")
    entries = listing(repo, revision)
    names = {e["path"] for e in entries}
    for required in ("config.json", "model.safetensors", "weights_manifest.json", "modeling_vae.py"):
        if required not in names:
            raise VerifyError(f"{required} missing from the tree of {repo}@{revision}")
    root = hub / ("models--" + repo.replace("/", "--"))
    snap = root / "snapshots" / revision
    shutil.rmtree(root, ignore_errors=True)
    try:
        for e in entries:
            dest = snap / e["path"]
            dest.parent.mkdir(parents=True, exist_ok=True)
            url = f"{HOST}/{repo}/resolve/{revision}/{urllib.parse.quote(e['path'])}"
            with _open(url) as r, open(dest, "wb") as out:
                shutil.copyfileobj(r, out, 1 << 20)
            verify(dest, e)
            print(f"verified {e['path']} ({e['size']} bytes)", flush=True)
        (root / "refs").mkdir(parents=True, exist_ok=True)
        (root / "refs" / "main").write_text(revision)
    except BaseException:
        shutil.rmtree(root, ignore_errors=True)
        raise
    return sorted(names)


def main(argv: list[str]) -> int:
    if len(argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    try:
        fetch(argv[1], argv[2], Path(argv[3]))
    except (VerifyError, OSError, ValueError, KeyError) as e:
        print(f"fetch_vae: FAILED: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
