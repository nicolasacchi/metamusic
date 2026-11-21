"""Static checks of worker/Dockerfile (C83.6); the real build and smoke run in CI."""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOCKERFILE = (ROOT / "Dockerfile").read_text()


def test_base_images_pinned_by_digest() -> None:
    refs = re.findall(r"^(?:FROM|COPY --from=)\s*(?:--\S+\s+)*(\S+)", DOCKERFILE, re.M)
    external = [r for r in refs if r not in {"build", "vae", "ears-models", "/uv"} and not r.startswith("build")]
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
            assert line.split()[1] in {".", "tools/fetch_vae.py", "tools/fetch_ears_models.py", "requirements-ears.txt",
                                       "ears/models.lock.json"}, line
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


def test_ears_stage_layout() -> None:
    """EARS-P2 (C333.2/.3/.5): shared interpreter, ears-only site dir, models baked from the lock, cap label."""
    assert "AS ears-models" in DOCKERFILE and "fetch_ears_models.py /opt/mm/ears-models.lock.json /opt/mm/ears-models" in DOCKERFILE
    assert "--target /opt/ears-site --no-deps --require-hashes -r /tmp/requirements-ears.txt" in DOCKERFILE
    last = DOCKERFILE.split("\nFROM ")[-1]
    for needle in ("COPY --from=ears-models /opt/mm/ears-models /opt/mm/ears-models", "COPY --from=build /opt/ears-site /opt/ears-site",
                   "MM_EARS_MODELS_DIR=/opt/mm/ears-models", "MM_EARS_LOCK=/opt/mm/ears-models.lock.json",
                   "MM_EARS_SITE=/opt/ears-site", 'io.metamusic.caps="analyze ears v2"'):
        assert needle in last, needle
    # the worker's own interpreter path never sees the site dir, and torchaudio stays out of /opt/venv
    env_lines = [ln for ln in DOCKERFILE.splitlines() if ln.startswith("ENV ")]
    assert not any("PYTHONPATH" in ln for ln in env_lines) and not any("ears-site" in ln and "PATH=" in ln.replace("MM_EARS_SITE=", "")
                                                                       for ln in env_lines)


def test_ears_requirements_are_hash_pinned_and_torch_free() -> None:
    text = (ROOT / "requirements-ears.txt").read_text()
    body = "\n".join(line for line in text.splitlines() if not line.startswith("#"))
    blocks = [b for b in re.split(r"\n(?=[A-Za-z0-9_.-]+==)", body.strip()) if b]
    names = [b.split("==")[0] for b in blocks]
    assert len(names) >= 30 and "torch" not in names, "the torch of /opt/venv is shared, never reinstalled"
    for must in ("demucs", "julius", "faster-whisper", "ctranslate2", "torchcrepe", "torchaudio"):
        assert must in names, must
    for b in blocks:
        assert re.match(r"[A-Za-z0-9_.-]+==[^\s=]+ \\\n", b) and re.search(r"--hash=sha256:[0-9a-f]{64}", b), b.split("\n")[0]


def test_notice_covers_the_ears_models_and_lgpl_libraries() -> None:
    notice = (ROOT / "NOTICE").read_text()
    for needle in ("htdemucs", "faster-whisper-large-v3", "torchcrepe", "lameenc", "soxr", "/opt/mm/licenses/ears"):
        assert needle in notice, needle
    for name in ("demucs", "whisper", "faster-whisper", "ctranslate2", "torchcrepe"):
        assert (ROOT / "licenses" / "ears" / f"{name}-LICENSE-MIT.txt").read_text().startswith("MIT License"), name
        assert f"{name}-LICENSE-MIT.txt" in notice, name
    assert "dist:lameenc" in (ROOT / "third_party_allow.txt").read_text().split()
