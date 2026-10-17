#!/usr/bin/env bash
# Usage: scripts/check-stamp.sh key|match|write
# A green `make check` is remembered by a hash of the whole working tree (tracked and untracked-not-ignored files,
# their contents) plus the tool versions that judge it (go, golangci-lint, bun). `match` succeeds, printing which green
# run it reuses, when the current tree already passed; `write` records a pass. The stamp lives in this worktree's git
# dir (never committed, never shared between worktrees), keeps the last 20 trees, and every reuse is appended to
# check-stamp.log next to it (scripts/loop's metrics schema has no "reused" field, and is not touched for this).
# MM_CHECK_STAMP=0 or `make check FORCE=1` bypasses it.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
gitdir=$(git rev-parse --absolute-git-dir)
stamp="$gitdir/check-stamp"

tools() {
  go version 2>/dev/null || true
  { bin/golangci-lint --version || golangci-lint --version; } 2>/dev/null | head -n1 || true
  bun --version 2>/dev/null || true
}

key() {
  { tools; git ls-files -co --exclude-standard -z | sort -z \
      | while IFS= read -r -d '' f; do [ -f "$f" ] && printf '%s\0' "$f"; done \
      | xargs -0 -r sha256sum; } | sha256sum | cut -c1-16
}

case "${1:?key|match|write}" in
  key) key ;;
  match)
    [ "${MM_CHECK_STAMP:-1}" = 0 ] && exit 1
    k=$(key)
    line=$(grep "^$k " "$stamp" 2>/dev/null | tail -n1) || exit 1
    echo "check: tree $k is unchanged since the green run of ${line#* }; reused it, nothing re-run (make check FORCE=1 to run anyway)"
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) reused $k" >>"$stamp.log"
    ;;
  write)
    k=$(key)
    { echo "$k $(date -u +%Y-%m-%dT%H:%M:%SZ)"; grep -v "^$k " "$stamp" 2>/dev/null | head -n 19; } >"$stamp.new" || true
    mv "$stamp.new" "$stamp"
    ;;
  *) echo "usage: $0 key|match|write" >&2; exit 2 ;;
esac
