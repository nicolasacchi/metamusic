"""Build-time fetch of the Tier B ears models into <out>/<role>/<path> (worker/Dockerfile, stage ears-models; C333.2).

    python fetch_ears_models.py LOCK OUT_DIR

LOCK is worker/ears/models.lock.json (format mm.ears-models/1). Every file is downloaded from the lock's pinned source:
`{"repo", "revision"}` is a Hugging Face repository at a 40-hex revision (https://huggingface.co/REPO/resolve/REVISION/PATH),
`{"url"}` is the exact URL of the model's single file. Each file is checked against its size and sha256 from the lock; any
mismatch deletes OUT_DIR and exits 1, which fails the image build. A licence outside MIT, Apache-2.0 and BSD-3-Clause is refused
before anything is fetched. Standard library only; it runs at build time and is never part of the runtime import path.
The runtime checks the same sizes at start (mmears.models) and the sha256 once per worker process.
"""

from __future__ import annotations

import hashlib
import json
import re
import shutil
import sys
import urllib.parse
import urllib.request
from collections.abc import Iterator
from pathlib import Path, PurePosixPath
from typing import Any

HOST = "https://huggingface.co"
FORMAT = "mm.ears-models/1"
ROLES = ("separator", "asr", "pitch")
LICENSE_ALLOW = frozenset({"MIT", "Apache-2.0", "BSD-3-Clause"})
REPO_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$")
REV_RE = re.compile(r"^[0-9a-f]{40}$")
SHA_RE = re.compile(r"^[0-9a-f]{64}$")
# URL sources may only come from the model's own upstream hosts.
URL_HOSTS = frozenset({"dl.fbaipublicfiles.com", "raw.githubusercontent.com", "github.com", "huggingface.co"})


class VerifyError(Exception):
    pass


def _open(url: str):
    req = urllib.request.Request(url, headers={"User-Agent": "metamusic-worker-build"})
    return urllib.request.urlopen(req, timeout=300)  # noqa: S310 - https URL built from validated parts


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def _safe_path(p: Any) -> bool:
    if not isinstance(p, str) or not p:
        return False
    pp = PurePosixPath(p)
    return not pp.is_absolute() and ".." not in pp.parts and "\\" not in p


def parse_lock(data: bytes) -> list[dict[str, Any]]:
    """Validate the lock and return its model entries; every defect is a VerifyError."""
    try:
        doc = json.loads(data)
    except ValueError as exc:
        raise VerifyError("the lock is not valid JSON") from exc
    if not isinstance(doc, dict) or doc.get("format") != FORMAT or not isinstance(doc.get("models"), list):
        raise VerifyError(f"the lock is not a {FORMAT} document")
    seen: set[str] = set()
    for m in doc["models"]:
        if not isinstance(m, dict):
            raise VerifyError("a model entry is not an object")
        role = m.get("role")
        if role not in ROLES or role in seen:
            raise VerifyError(f"role {role!r} is unknown or repeated")
        seen.add(role)
        if m.get("license") not in LICENSE_ALLOW:
            raise VerifyError(f"{role}: licence {m.get('license')!r} is not on the allow-list")
        src = m.get("source")
        if not isinstance(src, dict):
            raise VerifyError(f"{role}: no source")
        if "repo" in src:
            if not REPO_RE.match(str(src["repo"])) or not REV_RE.match(str(src.get("revision", ""))):
                raise VerifyError(f"{role}: repo must be ORG/NAME and revision 40 lowercase hex")
        elif "url" in src:
            u = urllib.parse.urlparse(str(src["url"]))
            if u.scheme != "https" or u.hostname not in URL_HOSTS or u.query or u.fragment:
                raise VerifyError(f"{role}: url must be a plain https URL on an allowed upstream host")
            if len(m.get("files") or []) != 1:
                raise VerifyError(f"{role}: a url source names exactly one file")
        else:
            raise VerifyError(f"{role}: source needs repo and revision, or url")
        files = m.get("files")
        if not isinstance(files, list) or not files:
            raise VerifyError(f"{role}: no files")
        for f in files:
            if (not isinstance(f, dict) or not _safe_path(f.get("path")) or not SHA_RE.match(str(f.get("sha256", "")))
                    or not isinstance(f.get("bytes"), int) or isinstance(f.get("bytes"), bool) or f["bytes"] < 1):
                raise VerifyError(f"{role}: bad file entry")
    return list(doc["models"])


def _urls(model: dict[str, Any]) -> Iterator[tuple[str, str]]:
    src = model["source"]
    for f in model["files"]:
        if "url" in src:
            yield f["path"], src["url"]
        else:
            yield f["path"], f"{HOST}/{src['repo']}/resolve/{src['revision']}/{urllib.parse.quote(f['path'])}"


def fetch_file(url: str, dest: Path, want_sha: str, want_bytes: int) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    with _open(url) as r, open(dest, "wb") as out:
        got = 0
        while chunk := r.read(1 << 20):
            got += len(chunk)
            if got > want_bytes:  # a host serving more than the lock names must not fill the build disk
                raise VerifyError(f"{dest.name}: body exceeds the locked {want_bytes} bytes")
            out.write(chunk)
    size = dest.stat().st_size
    if size != want_bytes:
        raise VerifyError(f"{dest.name}: size {size} != {want_bytes}")
    got = sha256_of(dest)
    if got != want_sha:
        raise VerifyError(f"{dest.name}: sha256 {got} != {want_sha}")


def fetch(lock_path: Path, out: Path) -> list[str]:
    models = parse_lock(lock_path.read_bytes())
    done: list[str] = []
    shutil.rmtree(out, ignore_errors=True)
    try:
        for m in models:
            want = {f["path"]: f for f in m["files"]}
            for path, url in _urls(m):
                fetch_file(url, out / m["role"] / path, want[path]["sha256"], want[path]["bytes"])
                done.append(f"{m['role']}/{path}")
                print(f"verified {m['role']}/{path} ({want[path]['bytes']} bytes)", flush=True)
    except BaseException:
        shutil.rmtree(out, ignore_errors=True)
        raise
    return done


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    try:
        fetch(Path(argv[1]), Path(argv[2]))
    except (VerifyError, OSError, ValueError, KeyError) as e:
        print(f"fetch_ears_models: FAILED: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
