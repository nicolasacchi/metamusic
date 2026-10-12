#!/usr/bin/env bash
# Render metrics/REPORT.md from metrics/loop.jsonl (per-target aggregates).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
common=$(git -C "$root" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
src=${MM_METRICS_FILE:-${common:+$common/../metrics/loop.jsonl}}
src=${src:-$root/metrics/loop.jsonl}
out=$root/metrics/REPORT.md
[ -s "$src" ] || { echo "metrics: no rows in $src" >&2; exit 1; }
tmpout=$(mktemp)
rows=$(mktemp)
trap 'rm -f "$tmpout" "$rows"' EXIT
# keep only well-formed rows; torn or foreign lines are skipped and counted
total=$(grep -c '' "$src" || true)
jq -R -c --from-file <(printf '%s\n' 'fromjson? // empty | select(. as $r | $r | type == "object" and (.target|type)=="string" and (.exit|type)=="number" and (.wall_ms|type)=="number" and (.compile_errors|type)=="number" and (.failing_tests|type)=="number" and (.cache|type)=="string")') "$src" >"$rows"
kept=$(grep -c '' "$rows" || true)
skipped=$((total - kept))
[ "$kept" -gt 0 ] || { echo "metrics: no valid rows in $src" >&2; exit 1; }
[ "$skipped" -eq 0 ] || echo "metrics: skipped $skipped malformed line(s)" >&2
{
  echo "# Loop metrics report"
  echo
  echo "Generated $(date -u +%Y-%m-%dT%H:%M:%SZ) from $kept rows ($skipped malformed skipped)."
  echo
  echo "| target | runs | failures | median ms (warm) | p90 ms (warm) | compile errors | failing tests |"
  echo "|---|---:|---:|---:|---:|---:|---:|"
  jq -rs '
    
    def pct($p): if length==0 then "-" else (sort | .[(((length-1)*$p)|floor)]) end;
    group_by(.target)[] |
    "| \(.[0].target) | \(length) | \(map(select(.exit!=0))|length) | \([.[]|select(.cache=="warm")|.wall_ms]|pct(0.5)) | \([.[]|select(.cache=="warm")|.wall_ms]|pct(0.9)) | \(map(.compile_errors)|add) | \(map(.failing_tests)|add) |"
  ' "$rows"
} >"$tmpout"
mv "$tmpout" "$out"
echo "wrote metrics/REPORT.md"
