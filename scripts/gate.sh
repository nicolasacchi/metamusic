#!/usr/bin/env bash
# The privacy/repo gate, in two strengths.
#   gate.sh incremental   (make gate)       origin/main..HEAD plus the working tree: the incremental public
#                         gate (scripts/public-gate.sh --range) and only the selftests whose subject changed.
#   gate.sh full          (make gate-full, pre-push hook, CI)   every selftest, then the public gate over
#                         the whole history (public-gate.sh --history). Nothing is skipped or narrowed.
#   gate.sh prepush       (pre-push hook) reads git's "<ref> <sha> <ref> <sha>" lines on stdin. If every pushed tip was
#                         already cleared by a green `gate.sh full` with the private patterns required (stamp below,
#                         same scripts, same patterns) it passes at once; otherwise it runs `full`. A push opens its ssh
#                         connection before the hook runs, and a ~10 min hook gets the connection dropped by the remote,
#                         so run `make gate-full` first and the hook costs nothing. CI never uses the stamp.
#   gate.sh --select      read "<status><TAB><path>" lines on stdin, print the selftests that apply
# GATE_REQUIRE_PATTERNS=1 adds --require-patterns to the history scan (CI and the pre-push hook set it).
set -uo pipefail
# Run from a git hook (pre-push) this inherits GIT_DIR and friends; the selftests build scratch repos and must never
# touch the real one (they once did: commits, tags and user.name/email landed in the real repository).
unset $(git rev-parse --local-env-vars)
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root" || exit 2
mode=${1:-incremental}

ALL_SELFTESTS="public-gate-selftest loop-selftest coverage-gate-selftest ci-selftest e2e-cache-selftest deploy-preflight-selftest verify-selftest actions-pinned ci-tiers-selftest"

run_one() {
  case "$1" in
    actions-pinned) scripts/actions-pinned.sh ;;
    *) "scripts/$1.sh" ;;
  esac
}

# Which selftests does a set of changes call for? Over-selecting is fine, under-selecting is not: anything
# that feeds a selftest's subject, the selftest itself, or the shared inputs (.gitleaks.toml, metrics schema,
# Makefile, workflows, tools pin) selects it. A path with no mapping selects nothing.
select_tests() {
  local st p want=" "
  add() { case "$want" in *" $1 "*) ;; *) want="$want$1 " ;; esac; }
  while IFS=$'\t' read -r st p; do
    [ -n "$p" ] || continue
    case "$p" in
      scripts/public-gate.sh | scripts/public-gate-selftest.sh | .gitleaks.toml | .githooks/* | scripts/tools.sh | scripts/metrics-row.jq) add public-gate-selftest ;;&
      scripts/loop | scripts/loop-selftest.sh | scripts/metrics-row.jq | metrics/loop.schema.json) add loop-selftest ;;&
      scripts/coverage-gate.sh | scripts/coverage-gate-selftest.sh | coverage.thresholds) add coverage-gate-selftest ;;&
      scripts/e2e-cache.sh | scripts/e2e-cache-selftest.sh | scripts/e2e.sh) add e2e-cache-selftest ;;&
      scripts/deploy-preflight.sh | scripts/deploy-preflight-selftest.sh | deploy/*) add deploy-preflight-selftest ;;&
      scripts/verify.sh | scripts/changed.sh | scripts/gate.sh | scripts/verify-selftest.sh | scripts/e2e-map.txt | web/e2e/*.spec.ts) add verify-selftest ;;&
      scripts/ci-* | Makefile | .github/* | go.mod | scripts/gate.sh | scripts/verify.sh | scripts/e2e.sh | scripts/loop | .githooks/* | scripts/tools.sh | scripts/actions-pinned.sh | scripts/deploy-preflight.sh) add ci-selftest ;;&
      scripts/actions-pinned.sh | .github/*) add actions-pinned ;;&
      scripts/ci-* | scripts/go-suite.sh | scripts/check-stamp.sh | Makefile | .github/*) add ci-tiers-selftest ;;&
      # ci-selftest asserts the Go shards cover every package once: a package or test file that appears or
      # disappears changes the package/test set
      *_test.go | *.go) case "$st" in A | D) add ci-selftest ;; esac ;;
    esac
  done
  # emit in the canonical order
  local t
  for t in $ALL_SELFTESTS; do case "$want" in *" $t "*) echo "$t" ;; esac; done
}

# ---- green-stamp for the pre-push hook: <commit> <patterns-hash> <gate-scripts-hash>, one line per cleared tip
stamp_file() { echo "$(git rev-parse --path-format=absolute --git-common-dir)/gate-full.ok"; }
patterns_hash() { # the private pattern list in force, as public-gate.sh resolves it
  local f
  if [ -n "${PUBLIC_GATE_PATTERNS:-}" ]; then printf '%s' "$PUBLIC_GATE_PATTERNS" | sha256sum | cut -c1-64; return; fi
  for f in .public-gate-patterns "$(git rev-parse --path-format=absolute --git-common-dir)/../.public-gate-patterns"; do
    if [ -f "$f" ]; then sha256sum <"$f" | cut -c1-64; return; fi
  done
  echo none
}
scripts_hash() { cat scripts/gate.sh scripts/public-gate.sh scripts/metrics-row.jq .gitleaks.toml 2>/dev/null | sha256sum | cut -c1-64; }
stamp_line() { echo "$1 $(patterns_hash) $(scripts_hash)"; }
stamp_write() { # clears HEAD, only when the index and the tracked files are exactly HEAD
  local sf
  git diff --cached --quiet 2>/dev/null && git diff --quiet 2>/dev/null || return 0
  sf=$(stamp_file)
  { stamp_line "$(git rev-parse HEAD)"; tail -n 19 "$sf" 2>/dev/null || true; } >"$sf.new" && mv "$sf.new" "$sf"
}

case "$mode" in
  --select) select_tests; exit 0 ;;
  incremental)
    changes=$(mktemp)
    trap 'rm -f "$changes"' EXIT
    scripts/changed.sh >"$changes" || exit 2
    base=${MM_BASE:-$(git merge-base origin/main HEAD 2>/dev/null || true)}
    rc=0
    tests=$(select_tests <"$changes")
    # no base means changed.sh already reported "everything changed" (go.mod): run every selftest too
    if [ -z "$base" ]; then tests=$ALL_SELFTESTS; fi
    for t in $tests; do
      echo "gate: selftest $t"
      run_one "$t" || { echo "gate: $t FAILED" >&2; rc=1; }
    done
    [ -n "$tests" ] || echo "gate: no gate-related script changed: selftests skipped (make gate-full runs them all)"
    if [ -n "$base" ]; then
      scripts/public-gate.sh --range="$base" || rc=1
    else
      echo "gate: origin/main not found: scanning the full history instead" >&2
      scripts/public-gate.sh --history || rc=1
    fi
    exit $rc ;;
  full)
    rc=0
    for t in $ALL_SELFTESTS; do
      echo "gate: selftest $t"
      run_one "$t" || { echo "gate: $t FAILED" >&2; rc=1; }
    done
    req=
    [ "${GATE_REQUIRE_PATTERNS:-0}" = 1 ] && req=--require-patterns
    scripts/public-gate.sh --history $req || rc=1
    # a green run with the patterns required clears this commit for the pre-push hook (index and tracked files must match HEAD)
    if [ $rc -eq 0 ] && [ "${GATE_REQUIRE_PATTERNS:-0}" = 1 ]; then stamp_write; fi
    exit $rc ;;
  --stamp-write) stamp_write; exit $? ;;
  prepush)
    ok=1
    sf=$(stamp_file)
    while read -r _ lsha _ _; do
      [ -n "$lsha" ] || continue
      case "$lsha" in *[!0]*) ;; *) continue ;; esac   # a deletion pushes no objects
      grep -qxF "$(stamp_line "$lsha")" "$sf" 2>/dev/null || ok=0
    done
    if [ "$ok" = 1 ]; then echo "gate: pushed tip(s) already cleared by gate-full (same scripts and patterns); not rerunning"; exit 0; fi
    GATE_REQUIRE_PATTERNS=1 exec scripts/loop gate-full -- scripts/gate.sh full ;;
  *) echo "usage: $0 incremental|full|prepush|--select|--stamp-write" >&2; exit 2 ;;
esac
