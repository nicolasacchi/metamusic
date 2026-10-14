#!/usr/bin/env bash
# Worker contract tests against the real yue2 (CPU torch), C83.4/C85. Same steps as
# .github/workflows/worker-contract.yml, which calls this script. Infra owns the file; the Worker lane
# (M3-P2) supplies the pytest target (worker/contract). No GPU, no weights, no secrets.
# YUE2_COMMIT (40 hex) comes from the environment or from the pin file worker/contract/YUE2_COMMIT.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
TORCH_VERSION=${TORCH_VERSION:-2.10.0}
commit=${YUE2_COMMIT:-}
if [ -z "$commit" ] && [ -f worker/contract/YUE2_COMMIT ]; then
  commit=$(tr -d '[:space:]' < worker/contract/YUE2_COMMIT)
fi
printf '%s' "$commit" | grep -Eq '^[0-9a-f]{40}$' || {
  echo "worker-contract: YUE2_COMMIT must be a 40-hex commit (env or worker/contract/YUE2_COMMIT)" >&2
  exit 2
}
venv=${WORKER_CONTRACT_VENV:-$root/worker/.venv-contract}
uv venv "$venv"
export VIRTUAL_ENV="$venv" PATH="$venv/bin:$PATH"
uv pip install "torch==${TORCH_VERSION}" --index-url https://download.pytorch.org/whl/cpu
uv pip install "yue2-infer @ git+https://github.com/multimodal-art-projection/YuE@${commit}"
uv pip install -e worker --no-deps
uv pip install --group worker/pyproject.toml:dev
cd worker
python -m pytest contract
