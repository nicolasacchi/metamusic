#!/usr/bin/env bash
# Cut the public copy of the L1 plan from the private draft (L1-X1).
# Usage: l1-public-copy.sh SRC DST [--stage]
#   Copies SRC up to (excluding) the private-appendix marker line into DST.
#   Refuses unless exactly ONE line of SRC equals the marker comment.
#   --stage: also `git add DST` and run scripts/public-gate.sh --staged.
set -euo pipefail

src=${1:-}
dst=${2:-}
[ -n "$src" ] && [ -n "$dst" ] || { echo "usage: $0 SRC DST [--stage]" >&2; exit 2; }
[ -f "$src" ] || { echo "l1-public-copy: no such draft: $src" >&2; exit 2; }
marker='<!-- PRIVATE APPENDIX -->'

n=$(grep -c -x -F -- "$marker" "$src" || true)
if [ "$n" != 1 ]; then
  echo "l1-public-copy: expected exactly one marker line in $src, found $n" >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
sed '/^<!-- PRIVATE APPENDIX -->$/,$d' "$src" >"$tmp"
if grep -q -x -F -- "$marker" "$tmp" || grep -q '^## Appendix P' "$tmp"; then
  echo "l1-public-copy: the cut still holds the marker or Appendix P" >&2
  exit 1
fi
cp "$tmp" "$dst"

if [ "${3:-}" = "--stage" ]; then
  root=$(cd "$(dirname "$0")/.." && pwd)
  git -C "$root" add -- "$dst"
  (cd "$root" && scripts/public-gate.sh --staged)
fi
