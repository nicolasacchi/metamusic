#!/usr/bin/env bash
# Prints a hash of the Go build inputs (non-test .go files, go.mod, go.sum) as they are in the
# working tree, tracked or not. `make e2e-cover` stores it next to covdata/e2e (covdata/e2e.stamp) and
# scripts/coverage-gate.sh compares it, so coverage from an older build is never merged silently.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
# Every //go:embed input is a build input too: the non-Go files of each package that embeds (web/ is excluded, its
# dist is covered by the SPA hash in scripts/e2e.sh).
embdirs=()
while IFS= read -r d; do embdirs+=("$d"); done < <(
  git ls-files -co --exclude-standard -z -- '*.go' ':!*_test.go' \
    | xargs -0 -r grep -l '^//go:embed ' 2>/dev/null | xargs -r -n1 dirname | sort -u | grep -vx 'web' || true)
{
  git ls-files -co --exclude-standard -z -- '*.go' go.mod go.sum ':!*_test.go'
  for d in ${embdirs[@]+"${embdirs[@]}"}; do
    git ls-files -co --exclude-standard -z -- "$d" ":!$d/**/*.go" ":!$d/*.go"
  done
} \
  | sort -zu \
  | while IFS= read -r -d '' f; do [ -f "$f" ] && printf '%s\0' "$f"; done \
  | xargs -0 -r sha256sum \
  | sha256sum | cut -c1-12
