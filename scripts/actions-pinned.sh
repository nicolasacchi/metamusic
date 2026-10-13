#!/usr/bin/env bash
# Usage: scripts/actions-pinned.sh [workflow-dir]   (default .github/workflows)
# Every `uses:` in a workflow must pin a 40-hex commit SHA (supply-chain rule, C85). Local actions
# (./path) and docker:// references are not accepted either: the repo has none and a mutable tag is
# the thing this gate exists to refuse. Run by `make gate`; self-tested by scripts/ci-selftest.sh.
set -euo pipefail
dir=${1:-.github/workflows}
[ -d "$dir" ] || { echo "actions-pinned: no such directory: $dir" >&2; exit 2; }
bad=0
shopt -s nullglob
files=("$dir"/*.yml "$dir"/*.yaml)
[ "${#files[@]}" -gt 0 ] || { echo "actions-pinned: no workflow files in $dir" >&2; exit 2; }
for f in "${files[@]}"; do
  while IFS= read -r line; do
    ref=$(printf '%s' "$line" | sed -E 's/^[[:space:]-]*uses:[[:space:]]*//; s/[[:space:]]*#.*$//; s/^["'"'"']//; s/["'"'"']$//')
    if ! printf '%s' "$ref" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$'; then
      echo "actions-pinned: $f: not pinned to a 40-hex SHA: $ref" >&2
      bad=1
    fi
  done < <(grep -E '^[[:space:]-]*uses:' "$f" || true)
done
exit $bad
