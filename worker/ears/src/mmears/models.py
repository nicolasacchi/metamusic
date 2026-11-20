"""The model lock (format mm.ears-models/1) and its verification (C333.1).

    {"format": "mm.ears-models/1",
     "models": [{"role": "separator|asr|pitch", "name": "...",
                 "source": {"repo": "...", "revision": "..."} | {"url": "..."},
                 "license": "MIT|Apache-2.0|BSD-3-Clause",
                 "files": [{"path": "...", "sha256": "<64 hex>", "bytes": 123}]}]}

Files live at <models dir>/<role>/<path>. Sizes are checked on every start, sha256 once per worker process
(a marker file under /tmp). A miss is MODELS_MISSING and never a download.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import tempfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

from .errors import EarsError

FORMAT = "mm.ears-models/1"
ROLES = ("separator", "asr", "pitch")
LICENSE_ALLOW = frozenset({"MIT", "Apache-2.0", "BSD-3-Clause"})
DEFAULT_LOCK = Path(__file__).resolve().parents[2] / "models.lock.json"
_SHA = re.compile(r"^[0-9a-f]{64}$")
_CHUNK = 1 << 20


@dataclass(frozen=True)
class ModelFile:
    path: str
    sha256: str
    bytes: int


@dataclass(frozen=True)
class Model:
    role: str
    name: str
    license: str
    files: tuple[ModelFile, ...]

    def digest(self) -> str:
        """One sha256 for the model: the file's own for a single file, else over sorted 'path\\0sha\\n' lines."""
        if len(self.files) == 1:
            return self.files[0].sha256
        h = hashlib.sha256()
        for f in sorted(self.files, key=lambda f: f.path):
            h.update(f"{f.path}\0{f.sha256}\n".encode())
        return h.hexdigest()


@dataclass(frozen=True)
class Lock:
    models: dict[str, Model]
    raw_sha256: str


def _missing(msg: str) -> EarsError:
    return EarsError("MODELS_MISSING", msg)


def _safe_path(p: Any) -> bool:
    if not isinstance(p, str) or not p:
        return False
    pp = PurePosixPath(p)
    return not pp.is_absolute() and ".." not in pp.parts and "\\" not in p


def parse_lock(data: bytes) -> Lock:
    """Parse and validate a lock document; every defect is MODELS_MISSING."""
    try:
        doc = json.loads(data)
    except ValueError as exc:
        raise _missing("models lock is not valid JSON") from exc
    if not isinstance(doc, dict) or doc.get("format") != FORMAT:
        raise _missing(f"models lock format is not {FORMAT}")
    entries = doc.get("models")
    if not isinstance(entries, list):
        raise _missing("models lock has no models list")
    models: dict[str, Model] = {}
    for e in entries:
        if not isinstance(e, dict):
            raise _missing("models lock entry is not an object")
        role, name, lic = e.get("role"), e.get("name"), e.get("license")
        if role not in ROLES or role in models:
            raise _missing(f"models lock role {role!r} is unknown or repeated")
        if not isinstance(name, str) or not name or len(name) > 64:
            raise _missing(f"models lock {role}: bad name")
        if lic not in LICENSE_ALLOW:
            raise _missing(f"models lock {role}: licence {lic!r} is not on the allow-list")
        src = e.get("source")
        if not isinstance(src, dict) or not (
            ("repo" in src and "revision" in src) or "url" in src
        ):
            raise _missing(f"models lock {role}: bad source")
        files = e.get("files")
        if not isinstance(files, list) or not files:
            raise _missing(f"models lock {role}: no files")
        parsed = []
        for f in files:
            if (
                not isinstance(f, dict)
                or not _safe_path(f.get("path"))
                or not isinstance(f.get("sha256"), str)
                or not _SHA.match(f["sha256"])
                or not isinstance(f.get("bytes"), int)
                or isinstance(f.get("bytes"), bool)
                or f["bytes"] < 0
            ):
                raise _missing(f"models lock {role}: bad file entry")
            parsed.append(ModelFile(f["path"], f["sha256"], f["bytes"]))
        models[role] = Model(role, name, lic, tuple(parsed))
    return Lock(models, hashlib.sha256(data).hexdigest())


def load_lock(path: str | os.PathLike[str] | None = None) -> Lock:
    p = Path(path or os.environ.get("MM_EARS_LOCK") or DEFAULT_LOCK)
    try:
        data = p.read_bytes()
    except OSError as exc:
        raise _missing("models lock cannot be read") from exc
    return parse_lock(data)


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        while chunk := f.read(_CHUNK):
            h.update(chunk)
    return h.hexdigest()


def _read_marker(marker: Path, base: Path) -> set[str]:
    """Roles already sha256-verified for this lock and models dir (per-role marker)."""
    try:
        first, *roles = marker.read_text().split("\n")
    except OSError:
        return set()
    return set(roles) if first == str(base.resolve()) else set()


def verify(
    lock: Lock,
    models_dir: str | os.PathLike[str],
    roles: tuple[str, ...] = ROLES,
    marker_dir: str | os.PathLike[str] | None = None,
) -> dict[str, Model]:
    """Check the files of `roles`: sizes always, sha256 unless a marker says this lock was verified here."""
    base = Path(models_dir)
    marker = Path(marker_dir or tempfile.gettempdir()) / f"mm-ears-verified-{lock.raw_sha256[:16]}"
    done = _read_marker(marker, base)
    out: dict[str, Model] = {}
    for role in roles:
        model = lock.models.get(role)
        if model is None:
            raise _missing(f"models lock has no {role} model")
        verified = role in done
        for f in model.files:
            p = base / role / f.path
            try:
                size = p.stat().st_size
            except OSError as exc:
                raise _missing(f"{role} file {f.path} is missing") from exc
            if size != f.bytes:
                raise _missing(f"{role} file {f.path} has {size} bytes, lock says {f.bytes}")
            if not verified and _sha256_file(p) != f.sha256:
                raise _missing(f"{role} file {f.path} does not match its sha256")
        out[role] = model
    if not set(roles) <= done:
        try:
            marker.write_text("\n".join([str(base.resolve()), *sorted(done | set(roles))]))
        except OSError:
            pass  # the marker is an optimisation only
    return out


def model_dir(models_dir: str | os.PathLike[str], role: str) -> Path:
    return Path(models_dir) / role
