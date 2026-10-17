#!/usr/bin/env bash
# Usage: scripts/go-suite.sh test|test-long|perf
# The local full Go suites, split by tier so no test runs under two instrumentations:
#   test       -race -shuffle=on, everything except the long tier and the perf tests
#   test-long  -race -shuffle=on, the long tier only: the whole acceptance package, and in every other package the
#              tests named by LONG_RE (property, chaos, "every database call may fail" fault sweeps, TestAcceptance_*)
#   perf       the TestPerf_* budget tests, WITHOUT -race and -cover (budgets are real numbers only uninstrumented).
#              `make test` runs it after the race tier, so perf tests run in exactly one mode: plain.
# CI runs every tier: the sharded race job runs test + test-long (scripts/ci-go-test.sh, perf skipped), the perf job
# runs `perf`. A name is in exactly one of the three tiers by construction (the same two regexes select and skip);
# scripts/ci-tiers-selftest.sh asserts that on the real test list.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

PERF_RE='^TestPerf_'
LONG_RE='^(TestAcceptance_|TestProperty_|TestChaos_|Test[A-Za-z0-9]+_EveryDatabaseCallMayFail$)'
ACCEPTANCE=./internal/acceptance
TIMEOUT=${TEST_TIMEOUT:-30m}

pkgs_without_acceptance() { go list ./... | grep -vxF "$(go list -m)/internal/acceptance" | sed "s|^$(go list -m)|.|"; }
perf_pkgs() { grep -rl --include='*_test.go' '^func TestPerf_' internal cmd 2>/dev/null | xargs -r -n1 dirname | sort -u | sed 's|^|./|'; }

if [ "${1:-}" = --perf-pkgs ]; then perf_pkgs; exit 0; fi
if [ "${1:-}" = --regexes ]; then printf 'PERF_RE=%q\nLONG_RE=%q\n' "$PERF_RE" "$LONG_RE"; exit 0; fi

race=(go test -race -shuffle=on -timeout "$TIMEOUT")
case "${1:?test|test-long|perf}" in
  test)
    CGO_ENABLED=1 "${race[@]}" -skip "$PERF_RE|${LONG_RE}" $(pkgs_without_acceptance)
    "$0" perf
    ;;
  test-long)
    CGO_ENABLED=1 "${race[@]}" -run "$LONG_RE" -skip "$PERF_RE" $(pkgs_without_acceptance)
    CGO_ENABLED=1 "${race[@]}" -skip "$PERF_RE" "$ACCEPTANCE"
    ;;
  perf)
    # one process at a time (-p 1, -parallel 1): the stopwatch counts the process CPU, so concurrent tests would inflate it
    # shellcheck disable=SC2046
    CGO_ENABLED=0 go test -count=1 -p 1 -parallel 1 -timeout "$TIMEOUT" -run "$PERF_RE" $(perf_pkgs)
    ;;
  *) echo "usage: $0 test|test-long|perf" >&2; exit 2 ;;
esac
