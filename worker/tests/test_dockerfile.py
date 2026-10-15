"""Static checks of worker/Dockerfile (C83.6); the real build and smoke run in CI."""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCKERFILE = (ROOT / "Dockerfile").read_text()


def test_base_images_pinned_by_digest() -> None:
    refs = re.findall(r"^(?:FROM|COPY --from=)\s*(?:--\S+\s+)*(\S+)", DOCKERFILE, re.M)
    external = [r for r in refs if r not in {"build", "vae", "/uv"} and not r.startswith("build")]
    assert external, "no images found"
    for ref in external:
        assert re.search(r"@sha256:[0-9a-f]{64}$", ref), ref


def test_yue2_commit_is_validated_and_required() -> None:
    assert "ARG YUE2_COMMIT" in DOCKERFILE
    assert "[0-9a-f]{40}" in DOCKERFILE
    assert "@${YUE2_COMMIT}" in DOCKERFILE


def test_runtime_stage_contract() -> None:
    last = DOCKERFILE.split("\nFROM ")[-1]
    for needle in ("HF_HUB_OFFLINE=1", "MODE=serverless", 'ENTRYPOINT ["python", "-m", "mmworker"]',
                   "MM_YUE2_COMMIT", "MM_WORKER_BUILD"):
        assert needle in last, needle


def test_no_context_escape_and_no_weights() -> None:
    for line in DOCKERFILE.splitlines():
        if line.startswith("COPY ") and "--from=" not in line:
            # only the worker/ context is copied; the build-time fetcher is the one other file
            assert line.split()[1] in {".", "tools/fetch_vae.py"}, line
    assert ".." not in re.findall(r"^COPY .*", DOCKERFILE, re.M).__str__()
    assert "huggingface-cli" not in DOCKERFILE and "snapshot_download" not in DOCKERFILE
    assert "torchvision torchaudio" in DOCKERFILE


def test_dockerignore_excludes_dev_and_secrets() -> None:
    ign = (ROOT / "Dockerfile.dockerignore").read_text().split()
    for entry in (".venv", "tests", "contract", "**/*.env"):
        assert entry in ign, entry


def test_vae_baked_in_and_3b_is_not() -> None:
    assert re.search(r"^ARG VAE_REVISION=[0-9a-f]{40}$", DOCKERFILE, re.M)
    assert "fetch_vae.py m-a-p/YuE2-Vae" in DOCKERFILE and "/opt/mm/hf-cache/hub" in DOCKERFILE
    assert "YuE2-3B" not in DOCKERFILE, "the 3B weights must not be baked in"
    last = DOCKERFILE.split("\nFROM ")[-1]
    assert "MM_YUE2_VAE_DIR=/opt/mm/hf-cache/hub" in last and "HF_HUB_OFFLINE=1" in last
    assert "COPY --from=vae /opt/mm/hf-cache /opt/mm/hf-cache" in last
    assert "/opt/mm/NOTICE" in last


def test_notice_attributes_the_vae() -> None:
    notice = (ROOT / "NOTICE").read_text()
    for needle in ("m-a-p/YuE2-Vae", "CC BY-NC 4.0", "creativecommons.org/licenses/by-nc/4.0",
                   "MODEL_LICENSE"):
        assert needle in notice, needle
