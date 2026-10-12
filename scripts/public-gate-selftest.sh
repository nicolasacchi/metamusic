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
cp "$root/scripts/public-gate.sh" "$root/scripts/metrics-row.jq" scripts/
cp "$root/.gitleaks.toml" .
echo "hello" > README.md
git add -A
git commit -qm base

fail=0
contains() { # name needle: last gate output must contain needle
  if grep -qF -- "$2" "$tmp/out"; then echo "ok:   $1"; else echo "FAIL: $1"; sed 's/^/    /' "$tmp/out"; fail=1; fi
}
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

# non-ASCII and uppercase paths
echo x > "canzone perché.flac"; git add -f "canzone perché.flac"
expect "staged non-ASCII flac blocked" 1 --staged
reset
mkdir -p varianti; echo x > varianti/città.txt; git add -f varianti/città.txt
expect "staged non-ASCII varianti dir blocked" 1 --staged
reset
echo x > Lyrics.PDF; git add -f Lyrics.PDF
expect "staged uppercase PDF blocked" 1 --staged
reset
echo x > .env.local; git add -f .env.local
expect "staged .env blocked" 1 --staged
reset
echo x > a.opus; git add -f a.opus
expect "staged audio ext blocked" 1 --staged
reset

# invalid private regex must block (not silently disable the check)
echo "hello" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS=$'(unclosed' expect "invalid regex blocked" 1 --staged
reset
echo "My-Secret-Hostname.internal" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS='secret-hostname' expect "private pattern is case-insensitive" 1 --staged
reset

# gitleaks output names rule and file
printf 'token = "mmp_%s"\n' "$body" > conf.txt; git add conf.txt
expect "gitleaks token blocked (detail)" 1 --staged
contains "gitleaks output names file" "conf.txt"
contains "gitleaks output names rule" "personal-access-token"
reset

# missing gitleaks blocks
echo x > a.txt; git add a.txt
rc=0; env -u GITLEAKS PATH="$(dirname "$(command -v bash)"):$(dirname "$(command -v git)")" bash scripts/public-gate.sh --staged >"$tmp/out" 2>&1 || rc=$?
if command -v gitleaks >/dev/null 2>&1 && PATH="$(dirname "$(command -v bash)")" command -v gitleaks >/dev/null 2>&1; then
  echo "note: gitleaks shares a dir with bash/git, skipping missing-tool case"
else
  [ "$rc" -ne 0 ] && echo "ok:   missing gitleaks blocked" || { echo "FAIL: missing gitleaks (rc 0)"; fail=1; }
  contains "missing gitleaks message" "gitleaks not found"
fi
reset

# metrics: last row without trailing newline is validated
good='{"ts":"2026-01-01T00:00:00Z","target":"check","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"main","cache":"warm"}'
mkdir -p metrics
printf '%s\n{"cmd":"curl https://private-host/secret"}' "$good" > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "metrics last row without newline validated" 1 --staged
reset

# history: secret committed then removed is still caught
printf 'token = "mmd_%s"\n' "$body" > old.txt; git add old.txt; git commit -qm leak
git rm -q old.txt; git commit -qm unleak
expect "history scan finds removed token" 1 --history
git reset -q --hard HEAD~2

# history: file added only by a merge commit is caught
git checkout -q -b side; echo s > side.txt; git add side.txt; git commit -qm side
git checkout -q -; echo m > main.txt; git add main.txt; git commit -qm main
git merge -q --no-ff --no-commit side
echo x > evil.pdf; git add -f evil.pdf; git commit -qm merge
expect "history finds pdf added in merge" 1 --history

exit $fail
