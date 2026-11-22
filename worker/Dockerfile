# syntax=docker/dockerfile:1
# metamusic worker image (C83.6). Build context is worker/ ONLY:
#   docker build -f worker/Dockerfile --build-arg YUE2_COMMIT=<40 hex> worker/
# so worker/Dockerfile.dockerignore applies and nothing outside worker/ can enter the image.
# The 3B weights are NOT in the image: they come from RunPod's cached-model directory at run time (RunPod
# allows one cached model per endpoint). The small VAE (m-a-p/YuE2-Vae, CC BY-NC 4.0, see NOTICE) is baked in at
# build time at a pinned revision, sha256-verified against the Hugging Face listing; the build fails on mismatch.
# VAE_REVISION must equal yue2.Weights[m-a-p/YuE2-Vae] (internal/engine/yue2/weights.go; Go test pins it).
#
# Tier B ears (EARS-P2, plan C333): the stage's heavy packages go into /opt/ears-site, a directory that only the mmears subprocess
# puts on PYTHONPATH (layout "S-prime", see docs/plans/2026-11-20-m5b-ears.md C333.5): the subprocess runs this same interpreter and
# the torch 2.10.0 and CUDA libraries of /opt/venv, so nothing is installed twice, torchaudio stays out of /opt/venv (torchcrepe imports
# it at import time) and the yue2 environment is untouched. The three models (Demucs htdemucs, Whisper large-v3 as a CTranslate2
# conversion, torchcrepe full) are baked at pinned revisions and sha256 sums from worker/ears/models.lock.json; nothing is fetched at run time.

FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1 AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=ghcr.io/astral-sh/uv:0.11.7@sha256:240fb85ab0f263ef12f492d8476aa3a2e4e1e333f7d67fbdd923d00a506a516a /uv /bin/uv
# The pinned yue2 commit (not older than the Apache-2.0 relicensing); validated before anything is fetched.
ARG YUE2_COMMIT
RUN test -n "${YUE2_COMMIT}" && printf '%s' "${YUE2_COMMIT}" | grep -Eq '^[0-9a-f]{40}$' \
 || { echo "YUE2_COMMIT must be a 40-hex commit (--build-arg YUE2_COMMIT=...)" >&2; exit 1; }
RUN uv venv /opt/venv
ENV VIRTUAL_ENV=/opt/venv
RUN uv pip install \
      "yue2-infer @ git+https://github.com/multimodal-art-projection/YuE@${YUE2_COMMIT}" \
      "runpod==1.12.0"
# The worker needs neither; "not installed" is fine.
RUN uv pip uninstall torchvision torchaudio || true
# Tier B ears packages (hash-pinned, --no-deps: the shared ones are already in /opt/venv). torchaudio must match the torch the yue2 pin brings.
COPY requirements-ears.txt /tmp/requirements-ears.txt
RUN /opt/venv/bin/python -c "import importlib.metadata as m, sys; v = m.version('torch').split('+')[0]; sys.exit(0 if v == '2.10.0' else 'the ears pins need torch 2.10.0, found ' + v)" \
 && uv pip install --python /opt/venv/bin/python --target /opt/ears-site --no-deps --require-hashes -r /tmp/requirements-ears.txt
COPY . /src/worker
RUN uv pip install --no-deps /src/worker \
 && uv pip install --python /opt/venv/bin/python --target /opt/ears-site --no-deps /src/worker/ears \
 && uv pip freeze > /opt/venv/freeze.txt

FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1 AS vae
ARG VAE_REVISION=152733a19ad43aa67e367f9b5503ef8075bb5126
COPY tools/fetch_vae.py /opt/fetch_vae.py
RUN python /opt/fetch_vae.py m-a-p/YuE2-Vae "${VAE_REVISION}" /opt/mm/hf-cache/hub

FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1 AS ears-models
COPY tools/fetch_ears_models.py /opt/fetch_ears_models.py
COPY ears/models.lock.json /opt/mm/ears-models.lock.json
RUN python /opt/fetch_ears_models.py /opt/mm/ears-models.lock.json /opt/mm/ears-models

FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
COPY --from=vae /opt/mm/hf-cache /opt/mm/hf-cache
COPY --from=ears-models /opt/mm/ears-models /opt/mm/ears-models
COPY --from=ears-models /opt/mm/ears-models.lock.json /opt/mm/ears-models.lock.json
COPY --from=build /src/worker/licenses/ears /opt/mm/licenses/ears
COPY --from=build /src/worker/NOTICE /opt/mm/NOTICE
COPY --from=build /opt/venv /opt/venv
COPY --from=build /opt/ears-site /opt/ears-site
# libsndfile is a soundfile wheel dependency on slim images; ffmpeg is not needed.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsndfile1 \
 && rm -rf /var/lib/apt/lists/*
ENV PATH=/opt/venv/bin:$PATH HF_HUB_OFFLINE=1 MODE=serverless PYTHONUNBUFFERED=1 MM_YUE2_VAE_DIR=/opt/mm/hf-cache/hub
# Tier B ears: models and their lock (C333.2), and the directory only the mmears subprocess puts on its PYTHONPATH (the worker never does).
ENV MM_EARS_MODELS_DIR=/opt/mm/ears-models MM_EARS_LOCK=/opt/mm/ears-models.lock.json MM_EARS_SITE=/opt/ears-site
ARG YUE2_COMMIT GIT_SHA
ENV MM_YUE2_COMMIT=$YUE2_COMMIT MM_WORKER_BUILD=$GIT_SHA
# C322.2: what this image speaks. io.metamusic.caps must equal the `caps:` line of MODE=selftest (CI asserts it;
# a stage lane adds its cap here in the same commit that adds the stage module and its models). Sorted, as caps.compose prints it:
# the ears stage registers `ears` and `analyze`, only when its lock verifies against /opt/mm/ears-models; `fallback` is the
# upload fallback channel of upload.py (C399.7) and `agent` the pod loop of agent.py (C397), both always built in.
LABEL io.metamusic.contracts="mm.worker/1 mm.worker/2" io.metamusic.caps="agent analyze ears fallback v2"
ENTRYPOINT ["python", "-m", "mmworker"]
