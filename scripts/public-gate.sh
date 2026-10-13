#!/usr/bin/env bash
# Public-repo privacy gate. Usage: public-gate.sh [--staged|--history] [--require-patterns]
#   (default)  tracked files in the index
#   --staged   paths/content staged for commit (pre-commit hook)
#   --history  every path, blob, ref and commit identity ever committed (pre-push hook, CI)
#   --require-patterns  fail (instead of warn) when no private patterns are configured
# Checks: forbidden paths, binary/archive content, gitleaks, built-in token regex,
# private regexes (as regex and as fixed strings), identities, refs, metrics row schema.
set -uo pipefail

mode=tracked
require=0
for a in "$@"; do
  case "$a" in
    --staged) mode=staged ;;
    --history) mode=history ;;
    --require-patterns) require=1 ;;
    *) echo "usage: $0 [--staged|--history] [--require-patterns]" >&2; exit 2 ;;
  esac
done

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
    # rev-list --objects also reaches trees/blobs that only a tag points at
    readz < <(git ls-files -z; git log --all -m -z --name-only --pretty=format:
      git rev-list --all --objects 2>/dev/null | sed -n 's/^[0-9a-f]* //p' | tr '\n' '\0') ;;
  *) readz < <(git ls-files -z) ;;
esac

# --- 1. forbidden paths (case-insensitive) ---
forbidden_path() {
  local p=$1 b=${1##*/} rc=1
  shopt -s nocasematch
  case "$b" in
    *.flac | *.mp3 | *.wav | *.ogg | *.opus | *.m4a | *.aac | *.aif | *.aiff | *.webm | *.mp4 | *.mka | *.wma | \
      *.zip | *.7z | *.tar | *.tar.* | *.tgz | *.gz | *.bz2 | *.xz | *.zst | *.rar | \
      *.db* | *.sqlite* | *.s3db | *.pdf | *.docx | songs.json | songs.json* | CLAUDE.local.md | .env* | *.env | \
      *public-gate-patterns* | .gitleaksignore) rc=0 ;;
  esac
  # the one tracked placeholder list (content is checked separately)
  [ "$p" = ".public-gate-patterns.example" ] && rc=1
  # root-anchored: a default local BackupDir; internal/backup/ is source code and stays allowed
  case "$p" in
    backup/*) rc=0 ;;
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
  glargs=(--no-banner --ignore-gitleaks-allow --redact -c "$cfg" --exit-code 1 -l error --report-format json --report-path "$report")
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

# --- 3. private patterns, tokens, binary content, identities, refs ---
pf=$(mktemp)
blobf=$(mktemp)
tmpfiles+=("$pf" "$pf.f" "$blobf")
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
havepf=0
if [ -s "$pf" ]; then
  havepf=1
elif [ "$require" = 1 ]; then
  add "no private patterns configured (PUBLIC_GATE_PATTERNS / .public-gate-patterns) and --require-patterns is set"
else
  echo "public-gate: warning: no private patterns configured (PUBLIC_GATE_PATTERNS / .public-gate-patterns)" >&2
fi

# Each private pattern is tried as an extended regex and as a fixed string, so a
# verbatim copy of the pattern list matches itself even when its lines have escapes.
badre=0
# pmatch FILE [grep-flags...]: 0 if FILE matches any pattern
pmatch() {
  local f=$1 rc
  shift
  [ "$havepf" = 1 ] || return 1
  grep -q -a -i "$@" -E -f "$pf" "$f" 2>/dev/null; rc=$?
  [ "$rc" -eq 0 ] && return 0
  [ "$rc" -ge 2 ] && badre=1
  grep -q -a -i "$@" -F -f "$pf" "$f" 2>/dev/null
}
# pmatch_records FILE: NUL-separated records of FILE (-z) matching a pattern, both ways
pmatch_records() {
  [ "$havepf" = 1 ] || return 0
  { grep -z -a -i -E -f "$pf" "$1" 2>/dev/null; grep -z -a -i -F -f "$pf" "$1" 2>/dev/null; } | sort -zu
}
if [ "$havepf" = 1 ]; then
  grep -q -E -f "$pf" /dev/null 2>/dev/null; [ $? -ge 2 ] && badre=1
fi

seenmsg=$'\n'
add_once() { case "$seenmsg" in *$'\n'"$1"$'\n'*) return ;; esac; seenmsg+="$1"$'\n'; add "$1"; }

tokre='mm[pcd]_[a-z2-7]{52}'
# lowercase hex of the first 16 bytes -> 0 when the content is a known audio/archive/db/doc format
bad_magic() {
  local hex
  hex=$(head -c 16 "$1" | od -An -tx1 | tr -d ' \n')
  case "$hex" in
    664c6143* | 4944330[234]00* | 4f676753* | 52494646* | ????????66747970* | 25504446* | 504b0304* | 504b0506* | 504b0708* | \
      53514c69746520666f726d6174203300* | 1f8b* | 377abcaf271c* | 526172211a07* | fd377a585a00* | 28b52ffd* | \
      425a683?314159265359* | fffe* | feff*) return 0 ;;
  esac
  return 1
}
has_nul() {
  [ "$(head -c 8000 "$1" | wc -c)" != "$(head -c 8000 "$1" | tr -d '\000' | wc -c)" ]
}
png_ok() { # name file: allow-listed icon that really is a PNG
  case "$1" in web/public/icons/*.png) ;; *) return 1 ;; esac
  [ "$(head -c 8 "$2" | od -An -tx1 | tr -d ' \n')" = 89504e470d0a1a0a ]
}

scan_blob() { # sha name
  local sha=$1 name=$2 label
  label=${name:-blob ${sha:0:8}}
  git cat-file blob "$sha" >"$blobf" 2>/dev/null || return 0
  if ! png_ok "$name" "$blobf"; then
    if bad_magic "$blobf"; then add_once "binary/archive/audio/db/pdf content in: $label"
    elif has_nul "$blobf"; then add_once "binary content (NUL bytes) in: $label"; fi
  fi
  grep -q -a -i -E "$tokre" "$blobf" && add_once "metamusic token pattern in: $label"
  pmatch "$blobf" && add_once "private pattern match in: $label"
  case "${name##*/}" in
    .gitattributes)
      grep -q -a -E '(^|[[:space:]])(-diff|binary|-text|-merge)([[:space:]]|$)|diff=' "$blobf" &&
        add_once "gitattributes disables diff/text handling: $label" ;;
    .public-gate-patterns.example)
      grep -v -E '^[[:space:]]*(#|$)' "$blobf" | grep -q -v -x -F 'my-private-hostname\.example\.invalid' &&
        add_once "pattern list example holds more than the placeholder: $label" ;;
  esac
  return 0
}

# enumerate "type sha name" for every blob this mode covers (deduplicated by sha)
list_blobs() {
  case "$mode" in
    staged)
      local meta path m1 m2 o n st
      while IFS= read -r -d '' meta && IFS= read -r -d '' path; do
        read -r m1 m2 o n st <<<"$meta"
        [ "$m2" = 160000 ] && continue
        printf 'blob %s %s\n' "$n" "$path"
      done < <(git diff --cached --raw -z --no-abbrev --no-renames --diff-filter=ACMR) ;;
    history)
      { git ls-files -s -z | tr '\0' '\n' | awk -F'[ \t]' '$1 != 160000 {print $2" "substr($0, index($0,"\t")+1)}'
        git rev-list --all --objects 2>/dev/null; } |
        git cat-file --batch-check='%(objecttype) %(objectname) %(rest)' 2>/dev/null ;;
    *)
      git ls-files -s -z | tr '\0' '\n' | awk -F'[ \t]' '$1 != 160000 {print "blob "$2" "substr($0, index($0,"\t")+1)}' ;;
  esac | awk '$1 == "blob" && !seen[$2]++'
}
while read -r _ sha name; do
  scan_blob "$sha" "$name"
done < <(list_blobs)
[ "$badre" = 1 ] && add "private pattern check failed (invalid regex?)"

# file names are published too (report the path only)
if [ "$havepf" = 1 ] && [ ${#paths[@]} -gt 0 ]; then
  printf '%s\0' "${paths[@]}" >"$pf.p"
  tmpfiles+=("$pf.p")
  while IFS= read -r -d '' p; do add_once "private pattern match in path: $p"; done < <(pmatch_records "$pf.p")
fi

if [ "$mode" = history ]; then
  if [ "$havepf" = 1 ]; then
    # commit messages and author/committer identities are published too (short SHA only)
    git log --all -z --format='%H %B' >"$pf.m"
    git log --all -z --format='%h %an <%ae> %cn <%ce>' >"$pf.i"
    tmpfiles+=("$pf.m" "$pf.i")
    while IFS= read -r -d '' r; do add_once "private pattern match in commit message: ${r:0:8}"; done < <(pmatch_records "$pf.m")
    while IFS= read -r -d '' r; do add_once "private pattern match in commit identity: ${r:0:8}"; done < <(pmatch_records "$pf.i")
  fi
  # refs: names, tag messages/taggers, and refs that point at non-commits
  while read -r otype oname ptype; do
    case "$otype" in
      blob | tree) add_once "ref points directly at a $otype: ${oname:0:8}" ;;
      tag) [ "$ptype" = commit ] || add_once "tag points at a ${ptype:-?} object, not a commit: ${oname:0:8}" ;;
    esac
  done < <(git for-each-ref --format='%(objecttype) %(objectname) %(*objecttype)')
  if [ "$havepf" = 1 ]; then
    while IFS= read -r ref; do
      git for-each-ref --format='%(refname)%0a%(taggername) %(taggeremail)%0a%(contents)' "$ref" >"$blobf"
      pmatch "$blobf" && add_once "private pattern match in ref name or tag metadata: $(git rev-parse --short "$ref" 2>/dev/null)"
    done < <(git for-each-ref --format='%(refname)')
  fi
elif [ "$mode" = staged ] && [ "$havepf" = 1 ]; then
  { git var GIT_AUTHOR_IDENT; git var GIT_COMMITTER_IDENT; } >"$blobf" 2>/dev/null
  pmatch "$blobf" && add "private pattern match in your git author/committer identity (set a noreply user.email)"
fi

# --- 4. metrics rows ---
validate_row="$root/scripts/metrics-row.jq"
[ -f "$validate_row" ] || validate_row="$(dirname "$0")/metrics-row.jq"
row_prog=$(cat "$validate_row")
check_blob() { # label content-ref path
  local label=$1 ref=$2 p=$3 n=0 line
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    [ -n "$line" ] || continue
    # canonical form only: rejects duplicate keys / odd spacing that jq would normalise away
    [ "$line" = "$(printf '%s' "$line" | jq -c . 2>/dev/null)" ] || { add "metrics row invalid: $label line $n"; continue; }
    printf '%s' "$line" | jq -e -s "length == 1 and (.[0] | $row_prog)" >/dev/null 2>&1 || add "metrics row invalid: $label line $n"
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
