"""Build-time third-party notices for worker/Dockerfile.acestep (D-E2-29, C154.6).

    python gen_notices.py ALLOW_FILE STATIC_NOTICE LICENSES_DIR OUT

Walks every installed distribution, works out its licence from the package metadata (License-Expression, the
OSI classifiers, a short License field, else a sniff of an embedded licence text), and FAILS (exit 1) when a
distribution has no recognisable licence or one outside ALLOW_FILE (one SPDX identifier per line, `#` comments).
On success writes OUT: STATIC_NOTICE (ACE-Step, the model cards, Qwen3 ...), the licence texts of LICENSES_DIR and
a table of every distribution with its licence. Standard library only; it runs at build time inside the image's
virtualenv and is never part of the runtime import path.
"""

from __future__ import annotations

import re
import sys
from collections.abc import Iterable
from importlib import metadata
from pathlib import Path

OWN = "metamusic-worker"

# classifier / free-text spelling -> SPDX identifier
_ALIASES = {
    "mit": "MIT", "mit license": "MIT", "expat": "MIT",
    "bsd": "BSD-3-Clause", "bsd license": "BSD-3-Clause", "new bsd": "BSD-3-Clause", "bsd 3-clause": "BSD-3-Clause",
    "bsd-3-clause": "BSD-3-Clause", "bsd 3-clause license": "BSD-3-Clause", "3-clause bsd license": "BSD-3-Clause",
    "bsd-2-clause": "BSD-2-Clause", "bsd 2-clause": "BSD-2-Clause", "simplified bsd": "BSD-2-Clause",
    "apache": "Apache-2.0", "apache 2.0": "Apache-2.0", "apache-2.0": "Apache-2.0", "apache license 2.0": "Apache-2.0",
    "apache software license": "Apache-2.0", "apache license, version 2.0": "Apache-2.0",
    "isc": "ISC", "isc license (iscl)": "ISC", "mit-0": "MIT-0", "mit no attribution": "MIT-0", "isc license (iscl)": "ISC", "mpl-2.0": "MPL-2.0", "mozilla public license 2.0 (mpl 2.0)": "MPL-2.0",
    "psf": "PSF-2.0", "python software foundation license": "PSF-2.0", "psf-2.0": "PSF-2.0",
    "the unlicense (unlicense)": "Unlicense", "unlicense": "Unlicense", "0bsd": "0BSD",
    "zlib": "Zlib", "zlib/libpng license": "Zlib", "hpnd": "HPND", "cc0-1.0": "CC0-1.0",
    "nvidia proprietary software": "LicenseRef-NVIDIA-Proprietary", "nvidia": "LicenseRef-NVIDIA-Proprietary",
}
_SNIFF = (
    (re.compile(r"Apache License[\s\S]{0,40}Version 2\.0", re.I), "Apache-2.0"),
    (re.compile(r"Permission is hereby granted, free of charge", re.I), "MIT"),
    (re.compile(r"Redistribution and use in source and binary forms[\s\S]{0,1500}Neither the name", re.I), "BSD-3-Clause"),
    (re.compile(r"Redistribution and use in source and binary forms", re.I), "BSD-2-Clause"),
    (re.compile(r"Mozilla Public License,? (v\.|version) ?2\.0", re.I), "MPL-2.0"),
    (re.compile(r"Permission to use, copy, modify, and/or distribute this software", re.I), "ISC"),
    (re.compile(r"PYTHON SOFTWARE FOUNDATION LICENSE", re.I), "PSF-2.0"),
    (re.compile(r"NVIDIA (Software )?License|NVIDIA Proprietary", re.I), "LicenseRef-NVIDIA-Proprietary"),
)


def norm(token: str) -> str:
    t = token.strip()
    while t.startswith("(") and t.endswith(")") and t.count("(") == t.count(")"):
        t = t[1:-1].strip()
    return _ALIASES.get(t.lower(), t)


def licence_of(md: metadata.PackageMetadata) -> str | None:
    """The distribution's licence expression, or None when nothing recognisable is declared."""
    expr = md.get("License-Expression")
    if expr:
        return " ".join(norm(x) if x not in ("AND", "OR", "WITH") else x for x in re.split(r"\s+|(?=[()])|(?<=[()])", expr) if x)
    ids = []
    for c in md.get_all("Classifier") or []:
        if c.startswith("License :: ") and c != "License :: OSI Approved":
            ids.append(norm(c.split("::")[-1]))
    if ids:
        return " OR ".join(dict.fromkeys(ids))
    text = (md.get("License") or "").strip()
    if text and "\n" not in text and len(text) <= 120 and text.upper() != "UNKNOWN":
        return norm(text)
    body = text or ""
    for rx, spdx in _SNIFF:
        if rx.search(body):
            return spdx
    return None


def allowed(expr: str, allow: set[str]) -> bool:
    """`A OR B` needs one alternative to be fully allowed; `A AND B` needs both."""
    for alt in re.split(r"\s+OR\s+", expr):
        toks = [norm(t) for t in re.split(r"\s+AND\s+|\s+WITH\s+", alt.strip().strip("()")) if t.strip()]
        if toks and all(t in allow for t in toks):
            return True
    return False


def read_allow(path: Path) -> set[str]:
    out = set()
    for line in path.read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            out.add(line)
    return out


def collect(dists: Iterable[metadata.Distribution], allow: set[str]) -> tuple[list[tuple[str, str, str]], list[str]]:
    rows, problems = [], []
    for d in dists:
        md = d.metadata
        name = md.get("Name") or "?"
        if name == OWN:  # this worker's own code (the repository's licence applies); no third-party notice
            continue
        lic = licence_of(md)
        if lic is None:
            problems.append(f"{name}: no recognisable licence in the package metadata")
        elif not allowed(lic, allow):
            problems.append(f"{name}: licence {lic!r} is not in third_party_allow.txt")
        rows.append((name, md.get("Version") or "?", lic or "UNKNOWN"))
    return sorted(set(rows), key=lambda r: r[0].lower()), problems


def main(argv: list[str]) -> int:
    if len(argv) != 5:
        print(__doc__, file=sys.stderr)
        return 2
    allow_f, static_f, lic_dir, out_f = (Path(a) for a in argv[1:])
    rows, problems = collect(metadata.distributions(), read_allow(allow_f))
    if problems:
        print("gen_notices: FAILED:\n  " + "\n  ".join(problems), file=sys.stderr)
        return 1
    parts = [static_f.read_text().rstrip(), ""]
    for p in sorted(lic_dir.glob("*.txt")):
        parts += ["=" * 78, f"Licence text: {p.name}", "=" * 78, p.read_text().rstrip(), ""]
    parts += ["=" * 78, "Python distributions installed in this image", "=" * 78]
    parts += [f"{n} {v}: {lic}" for n, v, lic in rows]
    out_f.parent.mkdir(parents=True, exist_ok=True)
    out_f.write_text("\n".join(parts) + "\n")
    print(f"gen_notices: {len(rows)} distributions, all licences allowed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
