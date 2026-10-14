"""Static checks of worker/Dockerfile (C83.6); the real build and smoke run in CI."""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCKERFILE = (ROOT / "Dockerfile").read_text()


def test_base_images_pinned_by_digest() -> None:
    refs = re.findall(r"^(?:FROM|COPY --from=)\s*(?:--\S+\s+)*(\S+)", DOCKERFILE, re.M)
    external = [r for r in refs if r not in {"build", "/uv"} and not r.startswith("build")]
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
            assert line.split()[1] == ".", line  # only the worker/ context is copied
    assert ".." not in re.findall(r"^COPY .*", DOCKERFILE, re.M).__str__()
    assert "huggingface-cli" not in DOCKERFILE and "snapshot_download" not in DOCKERFILE
    assert "torchvision torchaudio" in DOCKERFILE


def test_dockerignore_excludes_dev_and_secrets() -> None:
    ign = (ROOT / "Dockerfile.dockerignore").read_text().split()
    for entry in (".venv", "tests", "contract", "**/*.env"):
        assert entry in ign, entry
