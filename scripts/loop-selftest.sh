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

# heavy targets: machine-wide lock, wait_ms, nesting, scope fallback
export MM_HEAVY_LOCK_FILE="$tmp/heavy.lock" MM_HEAVY_SCOPE=0
unset MM_HEAVY_HELD MM_HEAVY_LOCK MM_HEAVY_SLOTS
now() { date +%s%N; }
par2() { # run two heavy 1s sleeps concurrently, print elapsed ms
  local t0 t1
  t0=$(now)
  "$root/scripts/loop" test -- sleep 1 >/dev/null 2>&1 &
  "$root/scripts/loop" race -- sleep 1 >/dev/null 2>&1 &
  wait
  t1=$(now)
  echo $(((t1 - t0) / 1000000))
}
: >"$MM_METRICS_FILE"
el=$(par2)
chk "1 slot serialises heavy runs (>=2s, got ${el}ms)" '[ "$el" -ge 1950 ]'
chk "wait_ms recorded for the waiter" '[ "$(jq -s "map(.wait_ms) | max" "$MM_METRICS_FILE")" -ge 700 ]'
chk "wall_ms excludes the wait" '[ "$(jq -s "map(.wall_ms) | max" "$MM_METRICS_FILE")" -lt 1700 ]'
chk "wait_ms rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "legacy row without wait_ms validates" 'echo "{\"ts\":\"2026-01-01T00:00:00Z\",\"target\":\"check\",\"wall_ms\":1,\"exit\":0,\"compile_errors\":0,\"failing_tests\":0,\"sha\":\"abc1234\",\"lane\":\"main\",\"cache\":\"warm\"}" | jq -e -f "$root/scripts/metrics-row.jq" >/dev/null'
: >"$MM_METRICS_FILE"
el=$(MM_HEAVY_SLOTS=2 par2)
chk "2 slots run in parallel (<1.8s, got ${el}ms)" '[ "$el" -lt 1800 ]'
el=$(MM_HEAVY_LOCK=0 par2)
chk "MM_HEAVY_LOCK=0 skips the lock (<1.8s, got ${el}ms)" '[ "$el" -lt 1800 ]'
# nested loop invocation under a held slot must not deadlock
rc=0
timeout 20 "$root/scripts/loop" check -- "$root/scripts/loop" test -- true >/dev/null 2>&1 || rc=$?
chk "nested heavy invocation does not deadlock" '[ "$rc" = 0 ]'
# systemd path: fake systemd-run/systemctl on PATH prove the scope command line
fake="$tmp/fakebin"
mkdir -p "$fake"
printf '#!/bin/sh\necho running\n' >"$fake/systemctl"
printf '#!/bin/sh\necho "$@" >"%s/sdrun.args"\nwhile [ "$1" != "--" ]; do shift; done; shift; exec "$@"\n' "$tmp" >"$fake/systemd-run"
chmod +x "$fake/systemctl" "$fake/systemd-run"
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 MM_HEAVY_MEM=7G "$root/scripts/loop" test -- true >/dev/null 2>&1
chk "scope applied with MemoryMax and no swap" 'grep -q -- "--user --scope --quiet --collect -p MemoryMax=7G -p MemorySwapMax=0" "$tmp/sdrun.args"'
rm -f "$tmp/sdrun.args"
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 "$root/scripts/loop" lint -- true >/dev/null 2>&1
chk "non-heavy target is not scoped" '[ ! -e "$tmp/sdrun.args" ]'
printf '#!/bin/sh\necho offline\nexit 1\n' >"$fake/systemctl"
rc=0
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 "$root/scripts/loop" test -- sh -c 'exit 5' >/dev/null 2>&1 || rc=$?
chk "no user manager: direct fallback runs, rc kept" '[ "$rc" = 5 ] && [ ! -e "$tmp/sdrun.args" ]'
printf '#!/bin/sh\nwhile [ "$1" != "--" ]; do shift; done; shift; "$@"; exit 137\n' >"$fake/systemd-run"
printf '#!/bin/sh\necho running\n' >"$fake/systemctl"
rc=0
out=$(PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 "$root/scripts/loop" test -- true 2>&1) || rc=$?
chk "OOM-kill keeps non-zero exit and prints hint" '[ "$rc" = 137 ] && echo "$out" | grep -q "killed by MemoryMax=20G; raise MM_HEAVY_MEM"'
exit $fail
