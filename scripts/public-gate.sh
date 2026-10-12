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
tmpfiles=()
add() { problems+=("$1"); }

# --- path list for this mode (NUL-separated: git quotes non-ASCII paths otherwise) ---
paths=()
readz() { local p; while IFS= read -r -d '' p; do [ -n "$p" ] && paths+=("$p"); done < <(sort -zu); }
case "$mode" in
  staged) readz < <(git diff --cached --name-only -z --diff-filter=ACMR) ;;
  history)
    # -m: merge commits list the paths they add relative to each parent
    readz < <(git ls-files -z; git log --all -m -z --name-only --pretty=format:) ;;
  *) readz < <(git ls-files -z) ;;
esac

# --- 1. forbidden paths (case-insensitive) ---
forbidden_path() {
  local p=$1 b=${1##*/} rc=1
  shopt -s nocasematch
  case "$b" in
    *.flac | *.mp3 | *.wav | *.ogg | *.opus | *.m4a | *.zip | *.db* | *.sqlite* | *.pdf | *.docx | songs.jsonl | CLAUDE.local.md | .env*) rc=0 ;;
  esac
  case "/$p" in
    */varianti/* | */docs/private/* | */secrets/*) rc=0 ;;
  esac
  shopt -u nocasematch
  return $rc
}
for p in ${paths[@]+"${paths[@]}"}; do
  forbidden_path "$p" && add "forbidden path: $p"
done

# --- 2. gitleaks ---
gl=${GITLEAKS:-}
[ -n "$gl" ] || { [ -x "$root/bin/gitleaks" ] && gl="$root/bin/gitleaks"; } || gl=$(command -v gitleaks || true)
if [ -z "$gl" ]; then
  add "gitleaks not found (run: make tools)"
else
  report=$(mktemp)
  tmpfiles=("$report")
  glargs=(--no-banner --redact -c "$cfg" --exit-code 1 -l error --report-format json --report-path "$report")
  if [ "$mode" = staged ]; then
    "$gl" git "${glargs[@]}" --staged . >/dev/null 2>&1
  elif [ "$mode" = history ]; then
    "$gl" git "${glargs[@]}" --log-opts="--all -m" . >/dev/null 2>&1
  else
    "$gl" git "${glargs[@]}" . >/dev/null 2>&1
  fi
  rc=$?
  if [ $rc -ne 0 ]; then
    add "gitleaks reported findings (exit $rc):"
    if command -v jq >/dev/null 2>&1 && [ -s "$report" ]; then
      # rule + location only; never the (redacted) secret or its line context
      while IFS= read -r l; do add "    $l"; done < <(jq -r '.[]? | "\(.RuleID) \(.File):\(.StartLine) \((.Commit // "")[0:8])"' "$report" 2>/dev/null | sort -u | head -40)
    fi
  fi
fi

# --- 3. private regexes ---
pf=$(mktemp)
tmpfiles+=("$pf" "$pf.f")
trap 'rm -f "${tmpfiles[@]}"' EXIT
common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
if [ -n "${PUBLIC_GATE_PATTERNS:-}" ]; then
  printf '%s\n' "$PUBLIC_GATE_PATTERNS" >"$pf"
elif [ -f .public-gate-patterns ]; then
  cp .public-gate-patterns "$pf"
elif [ -n "$common" ] && [ -f "$common/../.public-gate-patterns" ]; then
  cp "$common/../.public-gate-patterns" "$pf"   # worktrees: file lives in the main checkout
fi
grep -v -E '^[[:space:]]*(#|$)' "$pf" >"$pf.f" || true
mv "$pf.f" "$pf"
if [ -s "$pf" ]; then
  hits=()
  gout=$(mktemp)
  tmpfiles+=("$gout")
  if [ "$mode" = history ]; then
    revs=()
    while IFS= read -r r; do revs+=("$r"); done < <(git rev-list --all)
    git grep -z -I -i -l -E -f "$pf" ${revs[@]+"${revs[@]}"} >"$gout" 2>/dev/null
  else
    git grep -z --cached -I -i -l -E -f "$pf" >"$gout" 2>/dev/null
  fi
  grc=$?
  while IFS= read -r -d '' h; do
    [ "$mode" = history ] && h=${h#*:}
    hits+=("$h")
  done <"$gout"
  [ "$grc" -ge 2 ] && add "private pattern check failed (invalid regex?)"
  # report file names only: never echo the matched (private) text
  for h in ${hits[@]+"${hits[@]}"}; do add "private pattern match in: $h"; done
else
  echo "public-gate: warning: no private patterns configured (PUBLIC_GATE_PATTERNS / .public-gate-patterns)" >&2
fi

# --- 4. metrics rows ---
validate_row="$root/scripts/metrics-row.jq"
[ -f "$validate_row" ] || validate_row="$(dirname "$0")/metrics-row.jq"
check_blob() { # label content-ref path
  local label=$1 ref=$2 p=$3 n=0 line
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    [ -n "$line" ] || continue
    if [ "$(basename "$p")" = loop.jsonl ]; then
      printf '%s' "$line" | jq -e -f "$validate_row" >/dev/null 2>&1 || add "metrics row invalid: $label line $n"
    else
      printf '%s' "$line" | jq -e 'type == "object"' >/dev/null 2>&1 || add "metrics row not a JSON object: $label line $n"
    fi
  done < <(git show "$ref")
}
declare -a seenb=()
for p in ${paths[@]+"${paths[@]}"}; do
  case "$p" in metrics/*.jsonl) ;; *) continue ;; esac
  if ! command -v jq >/dev/null 2>&1; then add "jq not found; cannot validate $p"; continue; fi
  if [ "$mode" = history ]; then
    refs=()
    while IFS= read -r c; do refs+=("$c:$p"); done < <(git log --all -m --format=%H -- "$p")
    refs+=(":$p")
  else
    refs=(":$p")
  fi
  for r in "${refs[@]}"; do
    blob=$(git rev-parse -q --verify "$r" 2>/dev/null) || continue
    case " ${seenb[*]-} " in *" $blob "*) continue ;; esac
    seenb+=("$blob")
    check_blob "$p" "$blob" "$p"
  done
done

if [ ${#problems[@]} -gt 0 ]; then
  echo "public-gate: BLOCKED ($mode)" >&2
  printf '  - %s\n' "${problems[@]}" >&2
  exit 1
fi
echo "public-gate: ok ($mode)"
