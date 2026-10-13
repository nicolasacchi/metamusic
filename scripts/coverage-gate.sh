#!/usr/bin/env bash
# Usage: scripts/coverage-gate.sh [--thresholds FILE] <covdata-dir>...
#        scripts/coverage-gate.sh [--thresholds FILE] --profile <textfmt-file>
# Merges Go binary coverage data dirs (unit + e2e) with `go tool covdata textfmt`, computes
# statement coverage per package from the merged profile and enforces coverage.thresholds.
# Only packages under internal/ are measured. Exit: 0 ok, 1 below threshold, 2 usage/config error, 4 stale covdata (see D.stamp).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
thresholds="$root/coverage.thresholds"
profile=""
dirs=()
die() { echo "coverage-gate: $*" >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --thresholds) [ $# -ge 2 ] || die "--thresholds needs a file"; thresholds=$2; shift 2 ;;
    --profile) [ $# -ge 2 ] || die "--profile needs a file"; profile=$2; shift 2 ;;
    -*) die "unknown flag: $1" ;;
    *) dirs+=("$1"); shift ;;
  esac
done
[ -f "$thresholds" ] || die "thresholds file not found: $thresholds"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# --- parse thresholds: "<pattern> <percent>", '#' comments and blank lines ignored.
# pattern = internal[/seg]... or internal[/seg]...[/...] ; percent = 0..100 (decimals ok)
rules="$tmp/rules"
: >"$rules"
n=0
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%%#*}
  # shellcheck disable=SC2086
  set -- $line
  [ $# -eq 0 ] && continue
  [ $# -eq 2 ] || die "bad thresholds line (want '<pattern> <percent>'): $line"
  pat=$1 pct=$2
  [[ "$pat" =~ ^internal(/[a-z0-9_]+)*(/\.\.\.)?$ ]] || die "unknown threshold pattern: $pat"
  [[ "$pct" =~ ^[0-9]+(\.[0-9]+)?$ ]] || die "bad threshold percent for $pat: $pct"
  awk -v p="$pct" 'BEGIN { exit !(p <= 100) }' || die "threshold above 100 for $pat: $pct"
  printf '%s %s\n' "$pat" "$pct" >>"$rules"
  n=$((n + 1))
done <"$thresholds"
[ "$n" -gt 0 ] || die "no thresholds in $thresholds"

# --- staleness: a covdata dir D with a sibling D.stamp (written by `make e2e`) was built from the
# sources hashed in it; if the sources changed since, its blocks are for code that no longer exists
# and would be counted as uncovered. No stamp (CI artifacts, fresh checkouts) => no check.
# COVERAGE_SOURCE_STAMP overrides the current hash (selftest).
if [ -z "$profile" ]; then
  now=""
  for d in "${dirs[@]}"; do
    [ -f "${d%/}.stamp" ] || continue
    built=$(tr -d '[:space:]' <"${d%/}.stamp")
    [ -n "$now" ] || now=${COVERAGE_SOURCE_STAMP:-$("$root/scripts/source-stamp.sh")}
    if [ "$built" != "$now" ]; then
      echo "coverage-gate: $d is stale (built from $built, sources now $now): run make e2e" >&2
      exit 4
    fi
  done
fi

# --- build the merged profile
if [ -z "$profile" ]; then
  [ ${#dirs[@]} -gt 0 ] || die "need covdata dirs or --profile"
  for d in "${dirs[@]}"; do [ -d "$d" ] || die "covdata dir not found: $d"; done
  profile="$tmp/merged.txt"
  (IFS=,; go tool covdata textfmt -i="${dirs[*]}" -o "$profile") || die "covdata textfmt failed"
fi
[ -f "$profile" ] || die "profile not found: $profile"

# --- per-package statements: identical blocks merge (covered if any run covered them)
# output lines: "<pkg> <covered> <total>"
awk -v mod="$(sed -n 's/^module //p' "$root/go.mod")/" '
  NR == 1 && /^mode:/ { next }
  {
    split($0, f, " ")
    blk = f[1]; stm = f[2] + 0; cnt = f[3] + 0
    if (!(blk in seen)) { seen[blk] = 1; st[blk] = stm; cv[blk] = 0 }
    if (cnt > 0) cv[blk] = 1
  }
  END {
    for (b in seen) {
      file = b; sub(/:.*/, "", file)
      if (index(file, mod) == 1) file = substr(file, length(mod) + 1)
      if (file !~ /^internal\//) continue
      pkg = file; sub(/\/[^\/]*$/, "", pkg)
      tot[pkg] += st[b]; if (cv[b]) cov[pkg] += st[b]
    }
    for (p in tot) printf "%s %d %d\n", p, cov[p], tot[p]
  }' "$profile" | sort >"$tmp/pkgs"
[ -s "$tmp/pkgs" ] || die "profile has no internal/ packages"

fails=0
while read -r pat pct; do
  base=${pat%/...}
  if [ "$base" = "$pat" ]; then
    sel=$(awk -v b="$base" '$1 == b' "$tmp/pkgs")
  else
    sel=$(awk -v b="$base" '$1 == b || index($1, b "/") == 1' "$tmp/pkgs")
  fi
  [ -n "$sel" ] || die "threshold pattern matches no package in the profile: $pat"
  read -r got c t < <(awk '{c += $2; t += $3} END { printf "%.1f %d %d\n", (t ? 100 * c / t : 100), c, t }' <<<"$sel")
  if awk -v g="$got" -v p="$pct" 'BEGIN { exit !(g + 0 < p + 0) }'; then
    printf 'FAIL %-28s %5.1f%% < %s%% (%d/%d statements)\n' "$pat" "$got" "$pct" "$c" "$t" >&2
    fails=$((fails + 1))
  else
    printf 'ok   %-28s %5.1f%% >= %s%%\n' "$pat" "$got" "$pct"
  fi
done <"$rules"
[ "$fails" -eq 0 ] || { echo "coverage-gate: $fails threshold(s) not met" >&2; exit 1; }
