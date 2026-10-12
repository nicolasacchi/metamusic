#!/usr/bin/env bash
# Self-test for public-gate.sh: runs it in a throwaway repo.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
gl="${GITLEAKS:-}"
[ -n "$gl" ] || { [ -x "$root/bin/gitleaks" ] && gl="$root/bin/gitleaks"; } || gl=$(command -v gitleaks || true)
[ -n "$gl" ] || { echo "selftest: gitleaks not found (run make tools)" >&2; exit 1; }
export GITLEAKS="$gl"
unset PUBLIC_GATE_PATTERNS

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q .
git config user.email selftest@example.invalid
git config user.name selftest
git config commit.gpgsign false
mkdir scripts metrics
cp "$root/scripts/public-gate.sh" scripts/
cp "$root/.gitleaks.toml" .
echo "hello" > README.md
git add -A
git commit -qm base

fail=0
expect() { # name want(0|1) args...
  local name=$1 want=$2 rc=0
  shift 2
  scripts/public-gate.sh "$@" >"$tmp/out" 2>&1 || rc=$?
  if { [ "$want" = 0 ] && [ "$rc" -ne 0 ]; } || { [ "$want" = 1 ] && [ "$rc" -eq 0 ]; }; then
    echo "FAIL: $name (exit $rc)"
    sed 's/^/    /' "$tmp/out"
    fail=1
  else
    echo "ok:   $name"
  fi
}

reset() { git reset -q --hard; git clean -qfd; }

expect "clean tree passes (staged)" 0 --staged
expect "clean tree passes (tracked)" 0
expect "clean tree passes (history)" 0 --history

# forbidden path
echo x > x.pdf; git add -f x.pdf
expect "staged pdf blocked" 1 --staged
reset

mkdir -p docs/private; echo x > docs/private/a.md; git add -f docs/private/a.md
expect "staged docs/private blocked" 1 --staged
reset

# token-shaped secret (built at runtime so this file holds no valid token)
body=$(printf 'a%.0s' $(seq 1 52))
printf 'token = "mmp_%s"\n' "$body" > conf.txt; git add conf.txt
expect "staged mmp_ token blocked" 1 --staged
reset

printf 'token = "mmc_%s"\n' "$body" > conf.txt; git add conf.txt
expect "staged mmc_ token blocked" 1 --staged
reset

# private pattern
echo "my-secret-hostname.internal" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS=$'nothing-here\nsecret-hostname' expect "private pattern blocked" 1 --staged
reset

# metrics validation (tracked)
mkdir -p metrics
echo '{"ts":"2026-01-01T00:00:00Z","target":"check","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"main","cache":"warm"}' > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "valid metrics row passes" 0 --staged
echo '{"ts":"2026-01-01T00:00:00Z","target":"check","cmd":"rm -rf /","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"main","cache":"warm"}' > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "metrics row with extra field blocked" 1 --staged
reset

# history: secret committed then removed is still caught
printf 'token = "mmd_%s"\n' "$body" > old.txt; git add old.txt; git commit -qm leak
git rm -q old.txt; git commit -qm unleak
expect "history scan finds removed token" 1 --history

exit $fail
