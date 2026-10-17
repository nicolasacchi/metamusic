#!/usr/bin/env bash
# Self-test for scripts/loop.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export MM_METRICS_FILE="$tmp/m.jsonl" MM_LANE=selftest
# Own lock file and no systemd scope from the first call on: the selftest's heavy targets (check, test, race) must
# never queue behind the machine-wide lock a real `make check`/`make e2e` holds (that made `make gate` wait for
# as long as any suite ran, up to 44 min).
export MM_HEAVY_LOCK_FILE="$tmp/heavy.lock" MM_HEAVY_SCOPE=0
# `make verify` runs the gate with MM_LOOP_HEAVY=1 (and a held lock) in its environment; the selftest must not inherit either.
unset MM_LOOP_HEAVY MM_HEAVY_HELD MM_HEAVY_MAX_LOAD MM_HEAVY_MAX_WAIT MM_LOADAVG_FILE
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

# M3 targets (C85) are accepted, validate, and are in the schema enum
for t in worker contract canary; do
  "$root/scripts/loop" "$t" -- true >/dev/null
  chk "target $t accepted" 'jq -e "select(.target==\"$t\")" "$MM_METRICS_FILE" >/dev/null'
done
chk "M3 rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "schema enum has M3 targets" 'jq -e ".properties.target.enum | contains([\"worker\",\"contract\",\"canary\"])" "$root/metrics/loop.schema.json" >/dev/null'

# make verify / gate-full (dev-loop): accepted, validated, in the schema enum
for t in verify gate-full; do
  "$root/scripts/loop" "$t" -- true >/dev/null
  chk "target $t accepted" 'jq -e "select(.target==\"$t\")" "$MM_METRICS_FILE" >/dev/null'
done
chk "verify rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "schema enum has verify and gate-full" 'jq -e ".properties.target.enum | contains([\"verify\",\"gate-full\"])" "$root/metrics/loop.schema.json" >/dev/null'

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
# Overlap is measured by timestamps taken inside the commands, not by wall time, so a loaded host cannot
# flip the result (spawn overhead varies by seconds at load 100; the order of start/end does not).
par2() { # run two heavy 1s jobs concurrently; print "overlap" if their run windows intersected, else "serial"
  local ts="$tmp/ts"
  : >"$ts"
  "$root/scripts/loop" test -- sh -c "echo s \$(date +%s%N) >>$ts; sleep 1; echo e \$(date +%s%N) >>$ts" >/dev/null 2>&1 &
  "$root/scripts/loop" race -- sh -c "echo s \$(date +%s%N) >>$ts; sleep 1; echo e \$(date +%s%N) >>$ts" >/dev/null 2>&1 &
  wait
  awk '$1 == "s" { s[++ns] = $2 } $1 == "e" { e[++ne] = $2 }
    END { min_e = (e[1] < e[2]) ? e[1] : e[2]; max_s = (s[1] > s[2]) ? s[1] : s[2]; print (max_s < min_e) ? "overlap" : "serial" }' "$ts"
}
: >"$MM_METRICS_FILE"
el=$(par2)
chk "1 slot serialises heavy runs ($el)" '[ "$el" = serial ]'
chk "wait_ms recorded for the waiter" '[ "$(jq -s "map(.wait_ms) | max" "$MM_METRICS_FILE")" -ge 700 ]'
chk "wall_ms excludes the wait" 'jq -s -e "(map(.wall_ms) | max) < (map(.wait_ms) | max) + 700" "$MM_METRICS_FILE" >/dev/null'
chk "wait_ms rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "legacy row without wait_ms validates" 'echo "{\"ts\":\"2026-01-01T00:00:00Z\",\"target\":\"check\",\"wall_ms\":1,\"exit\":0,\"compile_errors\":0,\"failing_tests\":0,\"sha\":\"abc1234\",\"lane\":\"main\",\"cache\":\"warm\"}" | jq -e -f "$root/scripts/metrics-row.jq" >/dev/null'
: >"$MM_METRICS_FILE"
el=$(MM_HEAVY_SLOTS=2 par2)
chk "2 slots run in parallel ($el)" '[ "$el" = overlap ]'
el=$(MM_HEAVY_LOCK=0 par2)
chk "MM_HEAVY_LOCK=0 skips the lock ($el)" '[ "$el" = overlap ]'
# nested loop invocation under a held slot must not deadlock
rc=0
timeout 20 "$root/scripts/loop" check -- "$root/scripts/loop" test -- true >/dev/null 2>&1 || rc=$?
chk "nested heavy invocation does not deadlock" '[ "$rc" = 0 ]'
# ---- FIFO ticket queue, max wait, holder info, load admission
q="$tmp/fifo"
mkdir -p "$q"
export MM_HEAVY_LOCK_FILE="$tmp/fifo.lock"
rm -rf "$MM_HEAVY_LOCK_FILE"*
: >"$MM_METRICS_FILE"
waitfor() { # path... : wait (bounded) until every path exists
  local n=0 p
  for p in "$@"; do
    while [ ! -e "$p" ]; do
      n=$((n + 1))
      [ "$n" -lt 600 ] || return 1
      sleep 0.1
    done
  done
}
nq() { find "$MM_HEAVY_LOCK_FILE.q" -type f 2>/dev/null | wc -l; }
waitq() { # n : wait (bounded) until n tickets are queued
  local n=0
  while [ "$(nq)" -lt "$1" ]; do
    n=$((n + 1))
    [ "$n" -lt 600 ] || return 1
    sleep 0.1
  done
}
MM_LANE=holder "$root/scripts/loop" test -- sh -c "touch $q/a.up; while [ ! -e $q/a.go ]; do sleep 0.1; done" >/dev/null 2>&1 &
pa=$!
waitfor "$q/a.up"
chk "holder info written next to the lock" 'grep -q "lane=holder" "$MM_HEAVY_LOCK_FILE.holder" && grep -q "target=test" "$MM_HEAVY_LOCK_FILE.holder" && grep -qE "pid=[0-9]+ .*since=20" "$MM_HEAVY_LOCK_FILE.holder"'
"$root/scripts/loop" race -- sh -c "echo B >>$q/order" >/dev/null 2>"$q/b.err" &
pb=$!
waitq 1
"$root/scripts/loop" e2e -- sh -c "echo C >>$q/order" >/dev/null 2>"$q/c.err" &
pc=$!
waitq 2
# a stale ticket (its owner died: nobody holds a lock on it) ahead of everyone is reaped, not waited for
: >"$MM_HEAVY_LOCK_FILE.q/000000000000"
sleep 0.5
touch "$q/a.go"
wait "$pa" "$pb" "$pc"
chk "waiters run in arrival order (FIFO)" '[ "$(tr -d "\n" <"$q/order")" = BC ]'
chk "waiter prints queue position and the holder" 'grep -q "queue position 1" "$q/b.err" && grep -q "queue position 2" "$q/c.err" && grep -q "lane=holder" "$q/b.err"'
chk "stale ticket reaped" '[ ! -e "$MM_HEAVY_LOCK_FILE.q/000000000000" ] && [ "$(nq)" = 0 ]'
chk "holder file removed when the holder exits" '[ ! -e "$MM_HEAVY_LOCK_FILE.holder" ]'

: >"$MM_METRICS_FILE"
rm -f "$q"/a.up "$q"/a.go
MM_LANE=holder "$root/scripts/loop" test -- sh -c "touch $q/a.up; while [ ! -e $q/a.go ]; do sleep 0.1; done" >/dev/null 2>&1 &
pa=$!
waitfor "$q/a.up"
rc=0
MM_HEAVY_MAX_WAIT=1s "$root/scripts/loop" race -- sh -c "touch $q/ran" >/dev/null 2>"$q/t.err" || rc=$?
touch "$q/a.go"
wait "$pa"
chk "max wait: fails with 75 and does not run the command" '[ "$rc" = 75 ] && [ ! -e "$q/ran" ]'
chk "max wait: message names the holder (lane, target, start)" 'grep -q "gave up" "$q/t.err" && grep -q "lane=holder" "$q/t.err" && grep -q "target=test" "$q/t.err" && grep -q "since=20" "$q/t.err"'
chk "max wait: a row is recorded with exit 75 and the wait" 'jq -s -e "map(select(.target==\"race\")) | length == 1 and .[0].exit == 75 and .[0].wait_ms >= 900" "$MM_METRICS_FILE" >/dev/null'
chk "max wait: queue left clean" '[ "$(nq)" = 0 ]'
chk "max wait accepts minutes and plain seconds" 'rc=0; MM_HEAVY_MAX_WAIT=2m "$root/scripts/loop" test -- true >/dev/null 2>&1 || rc=$?; [ "$rc" = 0 ] && MM_HEAVY_MAX_WAIT=30 "$root/scripts/loop" test -- true >/dev/null 2>&1'

# load admission: off by default; with MM_HEAVY_MAX_LOAD it refuses above the limit; the load at start is recorded
printf '7.50 3.00 1.00 1/100 1\n' >"$tmp/loadavg"
: >"$MM_METRICS_FILE"
rc=0
MM_LOADAVG_FILE="$tmp/loadavg" "$root/scripts/loop" check -- true >/dev/null 2>&1 || rc=$?
chk "load admission is off by default" '[ "$rc" = 0 ] && jq -e ".load1_start == 7.5" "$MM_METRICS_FILE" >/dev/null'
rc=0
MM_LOADAVG_FILE="$tmp/loadavg" MM_HEAVY_MAX_LOAD=4 "$root/scripts/loop" check -- sh -c "touch $q/ran2" >/dev/null 2>"$q/l.err" || rc=$?
chk "load above MM_HEAVY_MAX_LOAD refuses with 75" '[ "$rc" = 75 ] && [ ! -e "$q/ran2" ] && grep -q "refusing to start" "$q/l.err"'
rc=0
MM_LOADAVG_FILE="$tmp/loadavg" MM_HEAVY_MAX_LOAD=8 "$root/scripts/loop" check -- true >/dev/null 2>&1 || rc=$?
chk "load below MM_HEAVY_MAX_LOAD runs" '[ "$rc" = 0 ]'
chk "refused run released the lock" '[ ! -e "$MM_HEAVY_LOCK_FILE.holder" ]'
chk "load rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'

# failing test names (bounded, sanitised) and retry passes
: >"$MM_METRICS_FILE"
"$root/scripts/loop" test -- sh -c 'echo "--- FAIL: TestP (0.0s)"; echo "    --- FAIL: TestP/sub_a (0.0s)"; echo "--- FAIL: TestQ (0.0s)"; echo "(fail) a > b [0.1ms]"; echo "  1) [chromium] › e2e/x.spec.ts:70:5 › saves the draft ─────"; echo "  2 flaky"; echo "  1 failed"; for i in 1 2 3 4 5 6 7; do echo "--- FAIL: TestMany$i (0.0s)"; done; echo "--- FAIL: Test\"quote\\\\back (0.0s)"' >/dev/null || true
chk "failing_names: leaf names only, bounded to 5" 'jq -e "(.failing_names | length) == 5 and .failing_names[0] == \"TestP/sub_a\" and (.failing_names | index(\"TestP\") | not)" "$MM_METRICS_FILE" >/dev/null'
: >"$MM_METRICS_FILE"
"$root/scripts/loop" e2e -- sh -c 'echo "(fail) a > b [0.1ms]"; echo "  1) [chromium] › e2e/x.spec.ts:70:5 › saves the draft ─────"; echo "  2 flaky"' >/dev/null || true
chk "failing_names: bun and Playwright names, no spec path; retry_passes from flaky" 'jq -e ".failing_names == [\"a > b\", \"chromium: saves the draft\"] and .retry_passes == 2" "$MM_METRICS_FILE" >/dev/null'
chk "names rows validate" 'jq -e -f "$root/scripts/metrics-row.jq" "$MM_METRICS_FILE" >/dev/null'
chk "no failures: empty names, zero retry passes" '"$root/scripts/loop" lint -- true >/dev/null && tail -n1 "$MM_METRICS_FILE" | jq -e ".failing_names == [] and .retry_passes == 0" >/dev/null'
chk "bad failing_names rejected by the validator" '! echo "{\"ts\":\"2026-01-01T00:00:00Z\",\"target\":\"check\",\"wall_ms\":1,\"exit\":0,\"compile_errors\":0,\"failing_tests\":0,\"sha\":\"abc1234\",\"lane\":\"main\",\"cache\":\"warm\",\"failing_names\":[\"/home/x/y\\\\\"z\"]}" | jq -e -f "$root/scripts/metrics-row.jq" >/dev/null 2>&1'
chk "schema documents the new fields" 'jq -e ".properties | has(\"load1_start\") and has(\"failing_names\") and has(\"retry_passes\")" "$root/metrics/loop.schema.json" >/dev/null'

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
rm -f "$tmp/sdrun.args"
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 "$root/scripts/loop" verify -- true >/dev/null 2>&1
chk "verify is light by default (not scoped)" '[ ! -e "$tmp/sdrun.args" ]'
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 MM_LOOP_HEAVY=1 "$root/scripts/loop" verify -- true >/dev/null 2>&1
chk "verify with MM_LOOP_HEAVY=1 is heavy (scoped)" 'grep -q "MemoryMax=" "$tmp/sdrun.args"'
rm -f "$tmp/sdrun.args"
PATH="$fake:$PATH" MM_HEAVY_SCOPE=1 MM_LOOP_HEAVY=1 "$root/scripts/loop" lint -- true >/dev/null 2>&1
chk "MM_LOOP_HEAVY does not make other targets heavy" '[ ! -e "$tmp/sdrun.args" ]'
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
