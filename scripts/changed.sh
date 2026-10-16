#!/usr/bin/env bash
# Prints what this checkout changed relative to origin/main, one "<status><TAB><path>" line per path:
# commits since the merge-base, staged and unstaged edits, and untracked files (status A). Statuses are
# A, M, D, T (renames are reported as D + A). Used by scripts/verify.sh and scripts/gate.sh.
# MM_BASE overrides the base commit. If no base can be found it fails closed by printing "M<TAB>go.mod",
# which every consumer treats as "everything changed".
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
base=${MM_BASE:-}
[ -n "$base" ] || base=$(git merge-base origin/main HEAD 2>/dev/null || true)
if [ -z "$base" ] || ! git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1; then
  echo "changed: no base (origin/main missing?): treating everything as changed" >&2
  printf 'M\tgo.mod\n'
  exit 0
fi
{
  git diff --name-status --no-renames -z "$base" --
  git ls-files -z -o --exclude-standard | while IFS= read -r -d '' f; do printf 'A\0%s\0' "$f"; done
} | while IFS= read -r -d '' st && IFS= read -r -d '' p; do printf '%s\t%s\n' "${st:0:1}" "$p"; done | LC_ALL=C sort -u
