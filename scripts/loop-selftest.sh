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

# M1 targets (C23) are accepted and their rows pass the shared validator
for t in fixture-db cover routing seed; do
  "$root/scripts/loop" "$t" -- true >/dev/null
  chk "target $t accepted" 'jq -e "select(.target==\"$t\")" "$MM_METRICS_FILE" >/dev/null'
done
chk "new-target rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "schema enum has new targets" 'jq -e ".properties.target.enum | contains([\"cover\",\"routing\",\"seed\",\"fixture-db\"])" "$root/metrics/loop.schema.json" >/dev/null'

# lint-style findings are not compile errors; tsc-style are; subtests counted once
: >"$MM_METRICS_FILE"
"$root/scripts/loop" lint -- sh -c 'echo "foo.go:3:4: type assertion on error (errorlint)"; echo "bar.ts(3,5): error TS2322: bad"; echo "--- FAIL: TestP (0.0s)"; echo "    --- FAIL: TestP/a (0.0s)"; echo "    --- FAIL: TestP/b (0.0s)"' >/dev/null
chk "lint line not a compile error, tsc line is" 'jq -e ".compile_errors==1 and .failing_tests==2" "$MM_METRICS_FILE" >/dev/null'
# real go vet / golangci-lint typecheck / bun / playwright output shapes
: >"$MM_METRICS_FILE"
"$root/scripts/loop" check -- sh -c 'echo "# example.com/x/internal/p"; echo "vet: internal/p/p.go:3:23: undefined: x"; echo "vet: internal/p/p.go:9:2: undefined: y"' >/dev/null
chk "vet: prefixed errors counted under check" 'jq -e ".target==\"check\" and .compile_errors==2" "$MM_METRICS_FILE" >/dev/null'
: >"$MM_METRICS_FILE"
"$root/scripts/loop" lint -- sh -c 'echo "p.go:1: : # example.com/x/internal/p"; echo "p.go:3:23: undefined: x (typecheck)"; echo "p.go:4:1: bad (errorlint)"' >/dev/null
chk "typecheck counted, header and linters not" 'jq -e ".target==\"lint\" and .compile_errors==1" "$MM_METRICS_FILE" >/dev/null'
: >"$MM_METRICS_FILE"
"$root/scripts/loop" web -- sh -c 'echo "(fail) a > b [0.1ms]"; echo "(fail) c [0.1ms]"; echo "  3 failed"; echo "  5 passed"' >/dev/null
chk "bun and playwright failures counted" 'jq -e ".failing_tests==5" "$MM_METRICS_FILE" >/dev/null'
# svelte-check machine format and human format (stops svelte-check && tsc chain, so must count here)
: >"$MM_METRICS_FILE"
"$root/scripts/loop" web -- sh -c 'echo "1700000000 START \"/w\""; echo "1700000001 ERROR \"src/lib/router.ts\" 110:7 \"Type x is not assignable\""; echo "1700000001 ERROR \"src/App.svelte\" 3:1 \"bad\""; echo "1700000002 COMPLETED 10 FILES 2 ERRORS 0 WARNINGS"' >/dev/null
chk "svelte-check machine errors counted" 'jq -e ".compile_errors==2" "$MM_METRICS_FILE" >/dev/null'
: >"$MM_METRICS_FILE"
"$root/scripts/loop" web -- sh -c 'echo "/w/src/lib/router.ts:110:7"; echo "Error: Type x is not assignable"; echo "/w/src/lib/a.ts:1:1"; echo "Warn: unused"; echo "svelte-check found 1 error and 1 warning"' >/dev/null
chk "svelte-check human errors counted, warnings not" 'jq -e ".compile_errors==1" "$MM_METRICS_FILE" >/dev/null'
MM_LANE=Upper "$root/scripts/loop" check -- true >/dev/null
chk "lane lower-cased" 'tail -n1 "$MM_METRICS_FILE" | jq -e ".lane==\"upper\"" >/dev/null'
chk "rows pass the gate validator" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
exit $fail
