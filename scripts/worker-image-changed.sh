#!/usr/bin/env bash
# Usage: scripts/worker-image-changed.sh <yue2|acestep15> <base-sha>
# Prints "true" when the worker image of that engine must be rebuilt for the changes between <base-sha> and HEAD, else "false".
# Used by .github/workflows/worker-image.yml (E2-I1). A missing or unknown base prints "true" (build when in doubt).
#  - acestep15: true when a changed file is one the ACE-Step image is built from (its Dockerfile and dockerignore, NOTICE.acestep,
#    licenses/, tools/, src/, pyproject.toml, uv.lock, third_party_allow.txt, the ACESTEP_COMMIT pin) or the workflow file.
#  - yue2: true when ANY changed file under worker/ (or the workflow file) is not ACE-only. ACE-only is a closed list; everything
#    else, including shared sources and unknown files, rebuilds the yue2 image.
set -euo pipefail
engine=${1:-}
base=${2:-}
case "$engine" in yue2 | acestep15) ;; *) echo "usage: $0 <yue2|acestep15> <base-sha>" >&2; exit 2 ;; esac
if [ -z "$base" ] || [ "$base" = 0000000000000000000000000000000000000000 ] || ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  echo true
  exit 0
fi
ace_only() {
  case "$1" in
    worker/Dockerfile.acestep | worker/Dockerfile.acestep.dockerignore | worker/NOTICE.acestep | worker/licenses/* | \
      worker/third_party_allow.txt | worker/contract/ACESTEP_COMMIT | worker/contract/test_acestep* | worker/contract/acestep-* | \
      worker/src/mmworker/engines/acestep* | worker/tests/test_acestep*) return 0 ;;
  esac
  return 1
}
ace_image_input() {
  case "$1" in
    worker/Dockerfile.acestep | worker/Dockerfile.acestep.dockerignore | worker/NOTICE.acestep | worker/licenses/* | worker/tools/* | \
      worker/src/* | worker/pyproject.toml | worker/uv.lock | worker/third_party_allow.txt | worker/contract/ACESTEP_COMMIT | \
      .github/workflows/worker-image.yml) return 0 ;;
  esac
  return 1
}
out=true
res=false
while IFS= read -r f; do
  [ -n "$f" ] || continue
  if [ "$engine" = acestep15 ]; then
    ace_image_input "$f" && res=true
  else
    case "$f" in
      worker/* | .github/workflows/worker-image.yml) ace_only "$f" || res=true ;;
    esac
  fi
done < <(git diff --name-only "$base" HEAD)
echo "$res"
