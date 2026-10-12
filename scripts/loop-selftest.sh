#!/usr/bin/env bash
# Self-test for scripts/loop.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export MM_METRICS_FILE="$tmp/m.jsonl" MM_LANE=selftest
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }

"$root/scripts/loop" check -- true >/dev/null
rc=0
"$root/scripts/loop" test -- sh -c 'echo "--- FAIL: TestX (0.00s)"; echo "foo.go:3:4: undefined: secretpath"; exit 3' >/dev/null || rc=$?
chk "exit code propagated" '[ "$rc" = 3 ]'
chk "two rows" '[ "$(wc -l <"$MM_METRICS_FILE")" = 2 ]'
chk "row 1 exit 0" 'jq -e "select(.target==\"check\") | .exit==0 and .lane==\"selftest\"" "$MM_METRICS_FILE" >/dev/null'
chk "row 2 counts" 'jq -e "select(.target==\"test\") | .exit==3 and .failing_tests==1 and .compile_errors==1" "$MM_METRICS_FILE" >/dev/null'
chk "no command text leaked" '! grep -q "secretpath" "$MM_METRICS_FILE"'
rc=0
"$root/scripts/loop" bogus -- true >/dev/null 2>&1 || rc=$?
chk "non-allowlisted target refused" '[ "$rc" = 2 ]'
exit $fail
