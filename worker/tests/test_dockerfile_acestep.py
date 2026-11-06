"""Static checks of worker/Dockerfile.acestep and the notices tooling (C154.6); the real build and smoke run in CI."""

from __future__ import annotations

import re
import sys
from importlib import metadata
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parent
DOCKERFILE = (ROOT / "Dockerfile.acestep").read_text()
STAGES = DOCKERFILE.split("\nFROM ")
RUNTIME = STAGES[-1]

sys.path.insert(0, str(ROOT / "tools"))
import gen_notices  # noqa: E402


def test_dockerfile_acestep_pins() -> None:
    refs = re.findall(r"^(?:FROM|COPY --from=)\s*(?:--\S+\s+)*(\S+)", DOCKERFILE, re.M)
    external = [r for r in refs if r not in {"build", "base", "/uv"} and not r.startswith(("build", "base"))]
    assert external
    for ref in external:
        assert re.search(r"@sha256:[0-9a-f]{64}$", ref), ref
    assert all("python:3.11-slim@" in r for r in re.findall(r"^FROM (\S+)", DOCKERFILE, re.M))
    assert "ARG ACESTEP_COMMIT" in DOCKERFILE and "[0-9a-f]{40}" in DOCKERFILE
    assert 'test "$(git -C /src/acestep rev-parse HEAD)" = "${ACESTEP_COMMIT}"' in DOCKERFILE
    assert '"torch==2.10.0+cu128"' in DOCKERFILE and "download.pytorch.org/whl/cu128" in DOCKERFILE
    assert re.search(r"^ARG FLASH_ATTN_SHA256=[0-9a-f]{64}$", DOCKERFILE, re.M)
    assert "sha256sum -c" in DOCKERFILE and "cp311" in DOCKERFILE
    # uv checks the wheel's local version against the file name
    assert "/tmp/flash_attn-2.8.3+cu128torch2.10-cp311-cp311-linux_x86_64.whl" in DOCKERFILE


def test_dockerfile_acestep_runtime_contract() -> None:
    for needle in ("HF_HUB_OFFLINE=1", "MODE=serverless", "MM_ENGINE=acestep15", "ENGINE=acestep15", 'ENTRYPOINT ["python", "-m", "mmworker"]',
                   "MM_ACESTEP_COMMIT", "MM_WORKER_BUILD", "MM_ACESTEP_MODELS_DIR=/opt/mm/hf-cache/hub",
                   'io.metamusic.engine="acestep15"', "io.metamusic.code-commit", "io.metamusic.torch",
                   "/opt/mm/THIRD_PARTY_NOTICES"):
        assert needle in RUNTIME, needle


def test_dockerfile_acestep_no_weights() -> None:
    """Only the small base DiT is fetched (sha256-verified); never the main bundle, never a hub download."""
    for forbidden in ("huggingface-cli", "snapshot_download", "from_pretrained", "hf_hub_download"):
        assert forbidden not in DOCKERFILE, forbidden
    code = "\n".join(l for l in DOCKERFILE.splitlines() if not l.lstrip().startswith("#"))
    assert "Ace-Step1.5" not in code and code.count("ACE-Step/acestep-v15-base") == 1
    assert re.search(r"^ARG BASE_REVISION=[0-9a-f]{40}$", DOCKERFILE, re.M)
    assert "f.verify(dest, e)" in DOCKERFILE
    assert "COPY --from=base /opt/mm/hf-cache /opt/mm/hf-cache" in RUNTIME
    for line in DOCKERFILE.splitlines():
        if line.startswith("COPY ") and "--from=" not in line:
            assert line.split()[1] in {".", "tools/fetch_vae.py"}, line


def test_dockerfile_acestep_base_revision_matches_the_go_pin() -> None:
    go = REPO / "internal" / "engine" / "acestep" / "weights.go"
    if not go.exists():
        pytest.skip("not run from a full checkout")
    rev = re.search(r"^ARG BASE_REVISION=([0-9a-f]{40})$", DOCKERFILE, re.M).group(1)  # type: ignore[union-attr]
    assert re.search(r'WeightsRepoBase: "' + rev + '"', go.read_text())


def test_dockerfile_acestep_apt_pinned() -> None:
    """apt installs exactly the recorded set, without recommends, lists removed, on a digest-pinned base."""
    installs = re.findall(r"apt-get install -y --no-install-recommends ([^\\\n]+)", DOCKERFILE)
    assert [i.split() for i in installs] == [["git", "ca-certificates", "curl"], ["libsndfile1"]]
    assert DOCKERFILE.count("rm -rf /var/lib/apt/lists/*") == 2


def test_dockerfile_acestep_size_trims_run_before_the_import_check() -> None:
    build = next(s for s in STAGES if s.startswith("python:3.11-slim") and " AS build" in s.split("\n")[0])
    trim = build.index("-name tests -prune -exec rm -rf")
    assert "torch/include" in build[trim:trim + 200]
    assert trim < build.index("assert not bad") < build.index("gen_notices.py")
    assert "ffmpeg" not in "\n".join(l for l in DOCKERFILE.splitlines() if "apt-get install" in l)


def test_dockerfile_acestep_builder_stage_not_in_runtime() -> None:
    for tool in ("git", "curl", "uv ", "gcc", "build-essential", "ACESTEP_COMMIT}\"", "fetch_vae"):
        assert tool not in "\n".join(l for l in RUNTIME.splitlines() if l.startswith(("RUN", "COPY ."))), tool
    assert "COPY --from=build /opt/venv /opt/venv" in RUNTIME
    assert "/src/acestep" not in RUNTIME  # the source tree stays in the build stage


def test_dockerfile_acestep_inference_only_deps() -> None:
    installs = " ".join(re.findall(r"RUN (?:uv pip install|curl)(?:[^\n]*\\\n)*[^\n]*", DOCKERFILE))
    assert "transformers" in installs
    for extra in ("gradio", "lightning", "tensorboard", "peft", "modelscope", "torchcodec", "torchao", "lycoris"):
        assert extra not in installs, extra
    assert '"pytorch-wavelets' in DOCKERFILE and "pywavelets" in DOCKERFILE  # DCW silently no-ops without them
    assert "assert not bad" in DOCKERFILE


def test_contract_requirements_match_the_dockerfile() -> None:
    """The CPU contract job and the image install the same non-torch dependency set."""
    req = [l.strip() for l in (ROOT / "contract" / "acestep-requirements.txt").read_text().splitlines()
           if l.strip() and not l.startswith("#")]
    block = re.search(r"RUN uv pip install \\\n((?:\s+\"[^\n]*\\\n)*\s+\"[^\n]*)", DOCKERFILE)
    assert block, "dependency block not found"
    image = re.findall(r'"([^"]+)"', block.group(1))
    assert sorted(req) == sorted(x for x in image if not x.startswith("runpod")), (req, image)
    assert not any(r.startswith("torch") for r in req) and '"torchaudio==2.10.0+cu128"' in DOCKERFILE


def test_dockerfile_acestep_dockerignore_applies() -> None:
    ign = [l.strip() for l in (ROOT / "Dockerfile.acestep.dockerignore").read_text().splitlines()
           if l.strip() and not l.startswith("#")]
    for entry in (".venv", "tests", "contract", "**/*.env", "Dockerfile.acestep"):
        assert entry in ign, entry
    assert "tools" not in ign and "licenses" not in ign  # the build needs both


def test_uv_lock_has_no_torch() -> None:
    lock = (ROOT / "uv.lock").read_text()
    assert not re.search(r'^name = "(torch|torchaudio|torchvision|nvidia-|triton|transformers)', lock, re.M)


def test_acestep_commit_file_is_a_full_commit() -> None:
    assert re.fullmatch(r"[0-9a-f]{40}", (ROOT / "contract" / "ACESTEP_COMMIT").read_text().strip())


def test_acestep_commit_matches_engine_params() -> None:
    from mmworker.engines import acestep

    pin = (ROOT / "contract" / "ACESTEP_COMMIT").read_text().strip()
    assert acestep.params_version(acestep.params_file()["current"])["acestep_commit"] == pin


def test_digest_file_is_a_placeholder_or_a_digest() -> None:
    f = REPO / "deploy" / "worker-acestep-image.digest"
    if not f.exists():
        pytest.skip("not run from a full checkout")
    lines = [l for l in f.read_text().splitlines() if l.strip() and not l.startswith("#")]
    assert all(re.fullmatch(r"sha256:[0-9a-f]{64}", l.strip()) for l in lines) and len(lines) <= 1


# ---- notices ---------------------------------------------------------------------------------------------

def make_dist(root: Path, name: str, **meta: str | list[str]) -> None:
    d = root / f"{name.replace('-', '_')}-1.0.dist-info"
    d.mkdir(parents=True)
    lines = [f"Metadata-Version: 2.4", f"Name: {name}", "Version: 1.0"]
    for k, v in meta.items():
        for item in v if isinstance(v, list) else [v]:
            lines.append(f"{k}: {item}")
    (d / "METADATA").write_text("\n".join(lines) + "\n")


def dists(root: Path) -> list[metadata.Distribution]:
    return list(metadata.distributions(path=[str(root)]))


ALLOW = gen_notices.read_allow(ROOT / "third_party_allow.txt")


def test_third_party_notices_generated(tmp_path: Path) -> None:
    site = tmp_path / "site"
    make_dist(site, "alpha", **{"License-Expression": "MIT"})
    make_dist(site, "beta", Classifier=["License :: OSI Approved :: Apache Software License"])
    make_dist(site, "gamma", License="BSD-3-Clause")
    make_dist(site, "delta", Classifier=["License :: OSI Approved :: BSD License", "License :: OSI Approved :: MIT License"])
    make_dist(site, "metamusic-worker")  # our own code: no third-party notice
    out = tmp_path / "out" / "THIRD_PARTY_NOTICES"
    rows, problems = gen_notices.collect(dists(site), ALLOW)
    assert problems == [] and [r[0] for r in rows] == ["alpha", "beta", "delta", "gamma"]
    assert dict((n, lic) for n, _, lic in rows) == {"alpha": "MIT", "beta": "Apache-2.0", "gamma": "BSD-3-Clause",
                                                    "delta": "BSD-3-Clause OR MIT"}
    # the real tool: static notice + licence texts + the table
    import importlib.metadata as md

    real = md.distributions
    md.distributions = lambda *a, **k: real(path=[str(site)])  # type: ignore[assignment]
    try:
        rc = gen_notices.main(["gen_notices", str(ROOT / "third_party_allow.txt"), str(ROOT / "NOTICE.acestep"),
                               str(ROOT / "licenses"), str(out)])
    finally:
        md.distributions = real  # type: ignore[assignment]
    text = out.read_text()
    assert rc == 0
    for needle in ("Qwen3", "Apache License", "Copyright 2024 Alibaba Cloud", "Qwen/Qwen3-1.7B @ 70d244cc",
                   "Qwen/Qwen3-Embedding-0.6B @ 97b0c614", "License: [MIT]", "alpha 1.0: MIT", "ACEStep"):
        assert needle in text, needle


@pytest.mark.parametrize("meta,why", [
    ({}, "no recognisable licence"),
    ({"License": "UNKNOWN"}, "no recognisable licence"),
    ({"License": "Some Custom Terms 1.0"}, "not in third_party_allow.txt"),
    ({"License-Expression": "GPL-3.0-only"}, "not in third_party_allow.txt"),
    ({"Classifier": ["License :: OSI Approved :: GNU General Public License v3 (GPLv3)"]}, "not in third_party_allow"),
    ({"License-Expression": "MIT AND GPL-2.0-only"}, "not in third_party_allow.txt"),
])
def test_third_party_notices_fail_on_unknown_or_disallowed(tmp_path: Path, meta: dict[str, str | list[str]], why: str) -> None:
    site = tmp_path / "site"
    make_dist(site, "badpkg", **meta)
    make_dist(site, "okpkg", License="MIT")
    _, problems = gen_notices.collect(dists(site), ALLOW)
    assert len(problems) == 1 and problems[0].startswith("badpkg:") and why in problems[0]
    assert gen_notices.main(["x"]) == 2


def test_third_party_notices_expressions() -> None:
    a = gen_notices.allowed
    assert a("MIT", ALLOW) and a("Apache-2.0 OR GPL-3.0-only", ALLOW) and a("MIT AND BSD-3-Clause", ALLOW)
    assert not a("MIT AND GPL-3.0-only", ALLOW) and not a("GPL-3.0-only", ALLOW) and not a("", ALLOW)
    assert gen_notices.norm("(MIT)") == "MIT" and gen_notices.norm("The Unlicense (Unlicense)") == "Unlicense"


def test_third_party_sniff_embedded_licence_text(tmp_path: Path) -> None:
    site = tmp_path / "site"
    make_dist(site, "texty", License="MIT License\n\nPermission is hereby granted, free of charge, to any person")
    make_dist(site, "apachey", License="Apache License\n Version 2.0, January 2004\n ...")
    rows, problems = gen_notices.collect(dists(site), ALLOW)
    assert problems == [] and {r[0]: r[2] for r in rows} == {"texty": "MIT", "apachey": "Apache-2.0"}


def test_third_party_nvidia_and_exceptions(tmp_path: Path) -> None:
    site = tmp_path / "site"
    make_dist(site, "nvidia-cublas-cu12", Classifier=["License :: Other/Proprietary License"])
    make_dist(site, "cuda-bindings", **{"License-Expression": "LicenseRef-NVIDIA-SOFTWARE-LICENSE"})
    make_dist(site, "llvmlite", **{"License-Expression": "BSD-2-Clause AND Apache-2.0 WITH LLVM-exception"})
    make_dist(site, "regex", **{"License-Expression": "Apache-2.0 AND CNRI-Python"})
    make_dist(site, "pytorch-wavelets", Classifier=["License :: Free To Use But Restricted"])
    make_dist(site, "pillow", **{"License-Expression": "MIT-CMU"})
    make_dist(site, "frozendict", **{"License-Expression": "LGPL-3.0-only"})
    rows, problems = gen_notices.collect(dists(site), ALLOW)
    assert problems == [], problems
    lic = {n: l for n, _, l in rows}
    assert lic["nvidia-cublas-cu12"] == lic["cuda-bindings"] == "LicenseRef-NVIDIA-Proprietary"
    assert "accepted by name" in lic["pytorch-wavelets"]
    # proprietary or restricted licences anywhere else still fail the build
    other = tmp_path / "other"
    make_dist(other, "somelib", Classifier=["License :: Other/Proprietary License"])
    make_dist(other, "pytorch-wavelets2", Classifier=["License :: Free To Use But Restricted"])
    _, problems = gen_notices.collect(dists(other), ALLOW)
    assert sorted(p.split(":")[0] for p in problems) == ["pytorch-wavelets2", "somelib"]


def test_allow_list_is_permissive_only() -> None:
    assert not any(re.search(r"GPL|SSPL|NC|Proprietary", a) and a != "LicenseRef-NVIDIA-Proprietary"
                   and not a.startswith(("LGPL-2.1", "LGPL-3.0-only")) for a in ALLOW)
    assert "LicenseRef-NVIDIA-Proprietary" in ALLOW and "MIT" in ALLOW


def test_notice_quotes_the_licences() -> None:
    n = " ".join((ROOT / "NOTICE.acestep").read_text().split())
    for needle in ("acestep-5Hz-lm-1.7B", "Qwen3-Embedding-0.6B", "Apache License, Version 2.0", "Alibaba Cloud",
                   "license: apache-2.0", "code MIT; weights MIT per model card", "commercial purposes",
                   "70d244cc86ccca08cf5af4e1e306ecf908b1ad5e", "97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3"):
        assert needle in n, needle
    lic = (ROOT / "licenses" / "Qwen3-LICENSE-Apache-2.0.txt").read_text()
    assert lic.lstrip().startswith("Apache License") and "Copyright 2024 Alibaba Cloud" in lic
