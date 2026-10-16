#!/usr/bin/env bash
# Prints a hash of the Go build inputs (non-test .go files, go.mod, go.sum) as they are in the
# working tree, tracked or not. `make e2e-cover` stores it next to covdata/e2e (covdata/e2e.stamp) and
# scripts/coverage-gate.sh compares it, so coverage from an older build is never merged silently.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
git ls-files -co --exclude-standard -z -- '*.go' go.mod go.sum ':!*_test.go' \
  | sort -z \
  | while IFS= read -r -d '' f; do [ -f "$f" ] && printf '%s\0' "$f"; done \
  | xargs -0 -r sha256sum \
  | sha256sum | cut -c1-12
