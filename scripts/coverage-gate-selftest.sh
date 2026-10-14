#!/usr/bin/env bash
# Self-test for scripts/coverage-gate.sh using synthetic textfmt profiles (no go test run).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
gate="$root/scripts/coverage-gate.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "coverage-gate-selftest: FAIL: $*" >&2; exit 1; }

m=github.com/nicolasacchi/metamusic
# mode line + blocks "file:l.c,l.c numStmts count"
cat >"$tmp/low.txt" <<P
mode: atomic
$m/internal/auth/a.go:1.1,2.2 8 1
$m/internal/auth/a.go:3.1,4.2 2 0
$m/internal/media/serve/s.go:1.1,2.2 10 1
$m/internal/store/t.go:1.1,2.2 10 1
$m/cmd/metamusic/main.go:1.1,2.2 50 0
P
cat >"$tmp/high.txt" <<P
mode: atomic
$m/internal/auth/a.go:1.1,2.2 96 1
$m/internal/auth/a.go:3.1,4.2 4 0
$m/internal/media/serve/s.go:1.1,2.2 10 1
$m/internal/store/t.go:1.1,2.2 10 1
$m/cmd/metamusic/main.go:1.1,2.2 50 0
P
cat >"$tmp/merge.txt" <<P
mode: atomic
$m/internal/auth/a.go:1.1,2.2 96 0
$m/internal/auth/a.go:1.1,2.2 96 3
$m/internal/auth/a.go:3.1,4.2 4 0
$m/internal/media/serve/s.go:1.1,2.2 10 1
$m/internal/store/t.go:1.1,2.2 10 1
P
cat >"$tmp/th" <<'T'
# comment
internal/...            85
internal/auth           95
internal/media/serve    95
T

# below threshold: exit 1, names package and percentages
rc=0; out=$("$gate" --profile "$tmp/low.txt" --thresholds "$tmp/th" 2>&1) || rc=$?
[ "$rc" = 1 ] || fail "below threshold: want exit 1, got $rc: $out"
grep -q 'internal/auth' <<<"$out" || fail "output does not name internal/auth: $out"
grep -q '80\.0' <<<"$out" || fail "output lacks measured 80.0: $out"
grep -q '95' <<<"$out" || fail "output lacks threshold 95: $out"
# above threshold: 0
"$gate" --profile "$tmp/high.txt" --thresholds "$tmp/th" >/dev/null 2>&1 || fail "above threshold should pass"
# duplicate blocks (unit + e2e merge) count once, covered if any count > 0
"$gate" --profile "$tmp/merge.txt" --thresholds "$tmp/th" >/dev/null 2>&1 || fail "merged duplicate blocks should pass"

# parser rejects unknown/malformed patterns and thresholds
expect_reject() {
  printf '%s\n' "$1" >"$tmp/bad"
  rc=0; "$gate" --profile "$tmp/high.txt" --thresholds "$tmp/bad" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "thresholds '$1': want exit 2, got $rc"
}
expect_reject 'cmd/... 50'
expect_reject 'internal/.../x 50'
expect_reject 'internal/auth/ 50'
expect_reject 'internal/auth abc'
expect_reject 'internal/auth 101'
expect_reject 'internal/auth'
expect_reject 'internal/auth 90 extra'
expect_reject '../x 50'
expect_reject ''
# a well-formed pattern that matches no package in the profile fails loudly
expect_reject 'internal/nosuchpkg 10'

# the missing-profile and unknown-flag cases are usage errors
rc=0; "$gate" --bogus >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "unknown flag: want 2, got $rc"
# stale covdata: a dir whose sibling .stamp differs from the current sources is refused (exit 4)
mkdir -p "$tmp/cov/e2e"
echo aaaaaaaaaaaa >"$tmp/cov/e2e.stamp"
rc=0; out=$(COVERAGE_SOURCE_STAMP=bbbbbbbbbbbb "$gate" --thresholds "$tmp/th" "$tmp/cov/e2e" 2>&1) || rc=$?
[ "$rc" = 4 ] || fail "stale covdata: want exit 4, got $rc: $out"
grep -q 'is stale (built from aaaaaaaaaaaa, sources now bbbbbbbbbbbb): run make e2e' <<<"$out" || fail "stale message: $out"
# matching stamp, or no stamp at all (CI artifact): not refused as stale
rc=0; out=$(COVERAGE_SOURCE_STAMP=aaaaaaaaaaaa "$gate" --thresholds "$tmp/th" "$tmp/cov/e2e" 2>&1) || rc=$?
[ "$rc" != 4 ] || fail "matching stamp refused as stale: $out"
rm "$tmp/cov/e2e.stamp"
rc=0; out=$(COVERAGE_SOURCE_STAMP=bbbbbbbbbbbb "$gate" --thresholds "$tmp/th" "$tmp/cov/e2e" 2>&1) || rc=$?
[ "$rc" != 4 ] || fail "unstamped dir refused as stale: $out"
# --profile is never stamp-checked
COVERAGE_SOURCE_STAMP=x "$gate" --profile "$tmp/high.txt" --thresholds "$tmp/th" >/dev/null 2>&1 || fail "profile mode should skip the stamp check"
# M3 patterns (nested path, decimals-free) are accepted by the grammar and enforced per package
cat >"$tmp/th3" <<'T'
internal/spend             95
internal/provider/runpod   90
internal/provider/fake     85
T
cat >"$tmp/m3.txt" <<P
mode: atomic
$m/internal/spend/a.go:1.1,2.2 96 1
$m/internal/spend/a.go:3.1,4.2 4 0
$m/internal/provider/runpod/r.go:1.1,2.2 90 1
$m/internal/provider/runpod/r.go:3.1,4.2 10 0
$m/internal/provider/fake/f.go:1.1,2.2 85 1
$m/internal/provider/fake/f.go:3.1,4.2 15 0
P
"$gate" --profile "$tmp/m3.txt" --thresholds "$tmp/th3" >/dev/null 2>&1 || fail "M3 patterns at threshold should pass"
sed -i 's/ 96 1/ 90 1/; s/ 4 0/ 10 0/' "$tmp/m3.txt"
rc=0; out=$("$gate" --profile "$tmp/m3.txt" --thresholds "$tmp/th3" 2>&1) || rc=$?
[ "$rc" = 1 ] || fail "M3 spend below 95: want exit 1, got $rc: $out"
grep -q 'internal/spend' <<<"$out" || fail "output does not name internal/spend: $out"
# the committed thresholds file parses in full (patterns exist or the gate says so by name, never a grammar error)
rc=0; out=$("$gate" --profile "$tmp/high.txt" --thresholds "$root/coverage.thresholds" 2>&1) || rc=$?
! grep -q 'unknown threshold pattern\|bad thresholds line' <<<"$out" || fail "coverage.thresholds grammar: $out"

echo "coverage-gate-selftest: ok"
