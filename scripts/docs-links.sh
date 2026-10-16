#!/usr/bin/env bash
# Usage: scripts/docs-links.sh [dir]   (default: docs)
# Fails when a relative markdown link [text](target) in <dir>/*.md points at a file that does not exist.
# External (scheme://, mailto:) and pure-anchor links are ignored; #fragments are stripped. No network.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:-$root/docs}
rc=0
shopt -s nullglob
for f in "$dir"/*.md; do
  while IFS= read -r target; do
    case "$target" in ''|'#'*|*://*|mailto:*) continue ;; esac
    path=${target%%#*}
    [ -n "$path" ] || continue
    if [ ! -e "$(dirname "$f")/$path" ]; then
      echo "docs-links: $(basename "$f"): dangling link $target" >&2
      rc=1
    fi
  done < <(grep -oE '\]\([^)[:space:]]+\)' "$f" | sed -E 's/^\]\((.*)\)$/\1/' || true)
done
exit $rc
