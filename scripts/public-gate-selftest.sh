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
contains "staged pdf blocked reason" "forbidden path: x.pdf"
reset

mkdir -p docs/private; echo x > docs/private/a.md; git add -f docs/private/a.md
expect "staged docs/private blocked" 1 --staged
contains "staged docs/private blocked reason" "forbidden path: docs/private/a.md"
reset

# token-shaped secret (built at runtime so this file holds no valid token)
body=$(printf 'a%.0s' $(seq 1 52))
printf 'token = "mmp_%s"\n' "$body" > conf.txt; git add conf.txt
expect "staged mmp_ token blocked" 1 --staged
contains "mmp_ reason" "gitleaks reported findings"
reset

printf 'token = "mmc_%s"\n' "$body" > conf.txt; git add conf.txt
expect "staged mmc_ token blocked" 1 --staged
contains "mmc_ reason" "metamusic-confirmation-token"
reset

# private pattern
echo "my-secret-hostname.internal" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS=$'nothing-here\nsecret-hostname' expect "private pattern blocked" 1 --staged
contains "private pattern blocked reason" "private pattern match in: notes.txt"
reset

# metrics validation (tracked)
mkdir -p metrics
echo '{"ts":"2026-01-01T00:00:00Z","target":"check","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"main","cache":"warm"}' > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "valid metrics row passes" 0 --staged
echo '{"ts":"2026-01-01T00:00:00Z","target":"check","cmd":"rm -rf /","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"main","cache":"warm"}' > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "metrics row with extra field blocked" 1 --staged
contains "metrics row with extra field blocked reason" "metrics row invalid"
reset

# the private pattern list itself must never be published (any directory, any case)
echo 'secret-host\.lan' > .public-gate-patterns; git add -f .public-gate-patterns
expect "staged .public-gate-patterns blocked" 1 --staged
contains "staged .public-gate-patterns reason" "forbidden path: .public-gate-patterns"
reset
mkdir -p sub; echo x > sub/.Public-Gate-Patterns; git add -f sub/.Public-Gate-Patterns
expect "nested mixed-case pattern list blocked" 1 --staged
contains "nested pattern list reason" "forbidden path: sub/.Public-Gate-Patterns"
reset
echo x > .public-gate-patterns.example; git add .public-gate-patterns.example
expect ".public-gate-patterns.example allowed" 0 --staged
reset
echo 'secret-host\.lan' > .public-gate-patterns; git add -f .public-gate-patterns; git commit -qm patterns
git rm -q .public-gate-patterns; git commit -qm unpatterns
expect "history finds committed pattern list" 1 --history
contains "history pattern list reason" "forbidden path: .public-gate-patterns"
git reset -q --hard HEAD~2

# metrics: duplicate keys must not smuggle text (row must equal its canonical form)
mkdir -p metrics
echo '{"ts":"2026-01-01T00:00:00Z","target":"check","wall_ms":1,"exit":0,"compile_errors":0,"failing_tests":0,"sha":"abc1234","lane":"private-host-name","lane":"main","cache":"warm"}' > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "metrics duplicate key blocked" 1 --staged
contains "duplicate key reason" "metrics row invalid"
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
contains "staged uppercase PDF blocked reason" "forbidden path: Lyrics.PDF"
reset
echo x > .env.local; git add -f .env.local
expect "staged .env blocked" 1 --staged
contains "staged .env blocked reason" "forbidden path: .env.local"
reset
echo x > a.opus; git add -f a.opus
expect "staged audio ext blocked" 1 --staged
contains "staged audio ext blocked reason" "forbidden path: a.opus"
reset

# invalid private regex must block (not silently disable the check)
echo "hello" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS=$'(unclosed' expect "invalid regex blocked" 1 --staged
contains "invalid regex blocked reason" "private pattern check failed"
reset
echo "My-Secret-Hostname.internal" > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS='secret-hostname' expect "private pattern is case-insensitive" 1 --staged
contains "private pattern is case-insensitive reason" "private pattern match in: notes.txt"
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
contains "metrics last row without newline validated reason" "metrics row invalid"
reset

# history: secret committed then removed is still caught
printf 'token = "mmd_%s"\n' "$body" > old.txt; git add old.txt; git commit -qm leak
git rm -q old.txt; git commit -qm unleak
expect "history scan finds removed token" 1 --history
git reset -q --hard HEAD~2

# metrics: two JSON values on one line must not pass; other jsonl files are validated too
mkdir -p metrics
printf '{"cmd":"curl https://private-host/x"} %s\n' "$good" > metrics/loop.jsonl
git add -f metrics/loop.jsonl
expect "metrics multi-value line blocked" 1 --staged
contains "multi-value reason" "metrics row invalid"
reset
mkdir -p metrics
echo '{"cmd":"x"}' > metrics/other.jsonl; git add -f metrics/other.jsonl
expect "metrics/other.jsonl arbitrary object blocked" 1 --staged
contains "other.jsonl reason" "metrics row invalid"
reset

# private patterns also apply to file names
echo x > notes-private-thing.md; git add notes-private-thing.md
PUBLIC_GATE_PATTERNS=private-thing expect "private pattern in file name blocked" 1 --staged
contains "path reason" "private pattern match in path: notes-private-thing.md"
reset

# history: commit message with private pattern
git commit -q --allow-empty -m "deploy to private-thing.lan"
PUBLIC_GATE_PATTERNS=private-thing expect "private pattern in commit message blocked" 1 --history
contains "commit message reason" "private pattern match in commit message"
git reset -q --hard HEAD~1

# history: file added only by a merge commit (and deleted afterwards) is caught via -m
git checkout -q -b side; echo s > side.txt; git add side.txt; git commit -qm side
git checkout -q -; echo m > main.txt; git add main.txt; git commit -qm main
git merge -q --no-ff --no-commit side
echo x > evil.pdf; git add -f evil.pdf; git commit -qm merge
git rm -q evil.pdf; git commit -qm cleanup
expect "history finds pdf added only in merge" 1 --history
contains "merge reason" "forbidden path: evil.pdf"

exit $fail
