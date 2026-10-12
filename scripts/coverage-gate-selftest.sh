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
echo "coverage-gate-selftest: ok"
