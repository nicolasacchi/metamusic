#!/usr/bin/env bash
# Public-repo privacy gate. Usage: public-gate.sh [--staged|--history]
#   (default)  tracked files in the index
#   --staged   paths/content staged for commit (pre-commit hook)
#   --history  every path and blob ever committed (pre-push hook, CI)
# Checks: forbidden paths, gitleaks, private regexes, metrics row schema.
set -uo pipefail

mode=tracked
case "${1:-}" in
  "") ;;
  --staged) mode=staged ;;
  --history) mode=history ;;
  *) echo "usage: $0 [--staged|--history]" >&2; exit 2 ;;
esac

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$(git rev-parse --show-toplevel)" || exit 2
cfg=.gitleaks.toml
[ -f "$cfg" ] || cfg="$root/.gitleaks.toml"

problems=()
add() { problems+=("$1"); }

# --- path list for this mode ---
case "$mode" in
  staged) mapfile -t paths < <(git diff --cached --name-only --diff-filter=ACMR) ;;
  history) mapfile -t paths < <(git log --all --name-only --pretty=format: | sort -u | sed '/^$/d') ;;
  *) mapfile -t paths < <(git ls-files) ;;
esac

# --- 1. forbidden paths ---
forbidden_path() {
  local p=$1 b=${1##*/}
  case "$b" in
    *.flac | *.mp3 | *.wav | *.ogg | *.opus | *.m4a | *.zip | *.db* | *.sqlite* | *.pdf | *.docx | songs.jsonl | CLAUDE.local.md | .env*) return 0 ;;
  esac
  case "/$p" in
    */varianti/* | */docs/private/* | */secrets/*) return 0 ;;
  esac
  return 1
}
for p in "${paths[@]}"; do
  [ -n "$p" ] || continue
  forbidden_path "$p" && add "forbidden path: $p"
done

# --- 2. gitleaks ---
gl=${GITLEAKS:-}
[ -n "$gl" ] || { [ -x "$root/bin/gitleaks" ] && gl="$root/bin/gitleaks"; } || gl=$(command -v gitleaks || true)
if [ -z "$gl" ]; then
  add "gitleaks not found (run: make tools)"
else
  glargs=(--no-banner --redact -c "$cfg" --exit-code 1 -l error)
  if [ "$mode" = staged ]; then
    "$gl" git "${glargs[@]}" --staged . >/tmp/gate-gl.$$ 2>&1
  elif [ "$mode" = history ]; then
    "$gl" git "${glargs[@]}" --log-opts="--all" . >/tmp/gate-gl.$$ 2>&1
  else
    "$gl" git "${glargs[@]}" . >/tmp/gate-gl.$$ 2>&1
  fi
  rc=$?
  if [ $rc -ne 0 ]; then
    add "gitleaks reported findings (exit $rc):"
    while IFS= read -r l; do add "    $l"; done < <(grep -E 'RuleID|File|Commit|Line' /tmp/gate-gl.$$ | sort -u | head -40)
  fi
  rm -f /tmp/gate-gl.$$
fi

# --- 3. private regexes ---
pf=$(mktemp)
trap 'rm -f "$pf"' EXIT
if [ -n "${PUBLIC_GATE_PATTERNS:-}" ]; then
  printf '%s\n' "$PUBLIC_GATE_PATTERNS" >"$pf"
elif [ -f .public-gate-patterns ]; then
  cp .public-gate-patterns "$pf"
fi
grep -v -E '^[[:space:]]*(#|$)' "$pf" >"$pf.f" || true
mv "$pf.f" "$pf"
if [ -s "$pf" ]; then
  if [ "$mode" = history ]; then
    mapfile -t revs < <(git rev-list --all)
    if [ ${#revs[@]} -gt 0 ]; then
      hits=$(git grep -I -l -E -f "$pf" "${revs[@]}" 2>/dev/null | sed 's/^[0-9a-f]*://' | sort -u)
    fi
  else
    hits=$(git grep --cached -I -l -E -f "$pf" 2>/dev/null)
  fi
  # report file names only: never echo the matched (private) text
  while IFS= read -r h; do [ -n "$h" ] && add "private pattern match in: $h"; done <<<"${hits:-}"
fi

# --- 4. metrics rows ---
validate_row='
  def isint: type == "number" and . == floor and . >= 0;
  type == "object"
  and (keys | sort) == (["cache","compile_errors","exit","failing_tests","lane","sha","target","ts","wall_ms"])
  and (.ts | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
  and (.target | IN("check","test","race","lint","web","e2e","build","openapi","gate","docker"))
  and (.wall_ms | isint) and (.exit | isint) and (.compile_errors | isint) and (.failing_tests | isint)
  and (.sha | type == "string" and test("^([0-9a-f]{7,40}|unknown)$"))
  and (.lane | type == "string" and test("^[a-z0-9][a-z0-9_-]{0,31}$"))
  and (.cache | IN("warm","cold"))'
ref=:
[ "$mode" = history ] && ref=HEAD:
for p in "${paths[@]}"; do
  case "$p" in metrics/*.jsonl) ;; *) continue ;; esac
  git cat-file -e "$ref$p" 2>/dev/null || continue
  if ! command -v jq >/dev/null 2>&1; then add "jq not found; cannot validate $p"; continue; fi
  n=0
  while IFS= read -r line; do
    n=$((n + 1))
    [ -n "$line" ] || continue
    if [ "$(basename "$p")" = loop.jsonl ]; then
      printf '%s' "$line" | jq -e "$validate_row" >/dev/null 2>&1 || add "metrics row invalid: $p line $n"
    else
      printf '%s' "$line" | jq -e 'type == "object"' >/dev/null 2>&1 || add "metrics row not a JSON object: $p line $n"
    fi
  done < <(git show "$ref$p")
done

if [ ${#problems[@]} -gt 0 ]; then
  echo "public-gate: BLOCKED ($mode)" >&2
  printf '  - %s\n' "${problems[@]}" >&2
  exit 1
fi
echo "public-gate: ok ($mode)"
