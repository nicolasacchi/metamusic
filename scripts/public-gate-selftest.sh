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
echo 'my-private-hostname\.example\.invalid' > .public-gate-patterns.example; git add .public-gate-patterns.example
expect ".public-gate-patterns.example allowed" 0 --staged
reset
printf 'real-host\\.lan\n' > .public-gate-patterns.example; git add .public-gate-patterns.example
expect "pattern example with extra lines blocked" 1 --staged
contains "example reason" "pattern list example holds more than the placeholder"
reset
# a copy of the (escaped) pattern list under another name or inside a file matches itself as fixed strings
printf 'secret-host\\.lan\n' > copy.txt; git add copy.txt
PUBLIC_GATE_PATTERNS='secret-host\.lan' expect "verbatim copy of escaped pattern list blocked" 1 --staged
contains "copy reason" "private pattern match in: copy.txt"
reset
echo x > my.public-gate-patterns.bak; git add my.public-gate-patterns.bak
expect "renamed pattern list blocked" 1 --staged
contains "renamed pattern list reason" "forbidden path: my.public-gate-patterns.bak"
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

# M2b: spectrogram JPEGs and token-loop arrays are derived data and never belong in the repo
for f in x.jpg x.JPEG x.npy; do
  echo x > "$f"; git add -f "$f"
  expect "staged $f blocked" 1 --staged
  contains "staged $f blocked reason" "forbidden path: $f"
  reset
done
echo x > icon.png; git add icon.png
expect "staged icon.png allowed" 0 --staged
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

# ---- hardening regressions ----------------------------------------------------
reset
# restart from the first commit: earlier history cases leave blocked paths behind
git reset -q --hard "$(git rev-list --max-parents=0 HEAD | tail -1)"
git branch -D side >/dev/null 2>&1 || true
git tag -l | xargs -r git tag -d >/dev/null
base=$(git rev-parse HEAD)

# commit identity (history and staged)
git -c user.email=x@private-thing.lan commit -q --allow-empty -m ident
PUBLIC_GATE_PATTERNS=private-thing expect "private identity in history blocked" 1 --history
contains "identity reason" "private pattern match in commit identity"
git reset -q --hard "$base"
echo y > id.txt; git add id.txt
GIT_AUTHOR_EMAIL=x@private-thing.lan PUBLIC_GATE_PATTERNS=private-thing expect "private identity staged blocked" 1 --staged
contains "staged identity reason" "git author/committer identity"
reset
# the real pre-commit hook refuses the commit before it exists
mkdir -p .githooks
printf '#!/usr/bin/env bash\nPUBLIC_GATE_PATTERNS=private-thing exec scripts/public-gate.sh --staged\n' > .githooks/pre-commit
chmod +x .githooks/pre-commit
git add .githooks; git commit -qm hook
git config core.hooksPath .githooks
echo z > z.txt; git add z.txt
if git -c user.email=x@private-thing.lan commit -qm blocked >/dev/null 2>&1; then echo "FAIL: hook let private identity commit through"; fail=1; else echo "ok:   hook refuses private identity"; fi
git config --unset core.hooksPath
reset
base=$(git rev-parse HEAD)

# gitleaks bypasses: the built-in token scan must still catch them
tok="mmp_$body"
printf '* -diff\n' > .gitattributes; printf 'token = "%s"\n' "$tok" > conf.txt; git add .gitattributes conf.txt
expect "token behind '* -diff' blocked (staged)" 1 --staged
contains "token pattern reason" "metamusic token pattern in: conf.txt"
contains "gitattributes reason" "gitattributes disables diff/text handling"
git commit -qm attrs
expect "token behind '* -diff' blocked (history)" 1 --history
git reset -q --hard "$base"
printf 'token = "%s" // gitleaks:allow\n' "$tok" > conf.txt; git add conf.txt
expect "gitleaks:allow comment ignored" 1 --staged
reset
for f in icon.svg docs/x.png blob.bin vendor/modules.txt node_modules/x/a.js go.sum my.gitleaks.toml.md; do
  mkdir -p "$(dirname "$f")"; printf 'token = "%s"\n' "$tok" > "$f"; git add -f "$f"
  expect "token in allowlisted path $f blocked" 1 --staged
  reset
done
printf 'token = "%s"\n\0nul\n' "$tok" > nul.txt; git add nul.txt
expect "token in NUL-containing file blocked" 1 --staged
reset
printf '.gitleaksignore\n' > /dev/null; echo "x" > .gitleaksignore; git add -f .gitleaksignore
expect ".gitleaksignore blocked" 1 --staged
reset

# default local BackupDir (root-anchored backup/) must never be committed; internal/backup/ is fine
mkdir -p backup; echo x > backup/x; git add -f backup/x
expect "staged backup/x blocked" 1 --staged
contains "staged backup/x reason" "forbidden path: backup/x"
reset
mkdir -p internal/backup; echo 'package backup' > internal/backup/x.go; git add internal/backup/x.go
expect "staged internal/backup/x.go allowed" 0 --staged
reset
cp "$root/.gitignore" .gitignore; mkdir -p backup internal/backup; echo x > backup/x; echo x > internal/backup/x.go
if git check-ignore -q backup/x; then echo "ok:   backup/x ignored"; else echo "FAIL: backup/x not ignored"; fail=1; fi
if git check-ignore -q internal/backup/x.go; then echo "FAIL: internal/backup/x.go ignored"; fail=1; else echo "ok:   internal/backup/x.go not ignored"; fi
reset

# renamed forbidden types and encodings
printf '%%PDF-1.4\nhello\n' > lyrics.txt; git add lyrics.txt
expect "renamed pdf blocked" 1 --staged
contains "renamed pdf reason" "binary/archive/audio/db/pdf content in: lyrics.txt"
reset
printf 'SQLite format 3\0rest' > data.dat; git add data.dat
expect "renamed sqlite blocked" 1 --staged
reset
printf 'fLaC\0\0\0\042' > take1.bin; git add take1.bin
expect "renamed flac blocked" 1 --staged
reset
echo 'secret-hostname.internal' | gzip > notes.txt.gz; git add notes.txt.gz
PUBLIC_GATE_PATTERNS=secret-hostname expect "gzip blocked" 1 --staged
reset
echo 'secret-hostname.internal' | gzip > notes.txt; git add notes.txt
PUBLIC_GATE_PATTERNS=secret-hostname expect "renamed gzip blocked by magic" 1 --staged
contains "gzip magic reason" "binary/archive/audio/db/pdf content in: notes.txt"
reset
printf 'secret-hostname\n' | iconv -f UTF-8 -t UTF-16LE > u16.txt; git add u16.txt
PUBLIC_GATE_PATTERNS=secret-hostname expect "UTF-16 text blocked" 1 --staged
reset
for f in a.aac a.aif a.aiff a.webm a.mp4 a.mka a.wma a.7z a.tar a.tar.gz a.rar a.s3db songs.json songs.jsonl.bak prod.env a.xz a.zst a.tgz; do
  echo x > "$f"; git add -f "$f"
  expect "forbidden name $f blocked" 1 --staged
  reset
done
# M3: RunPod key files and key assignments (values built at runtime; this file holds no such literal)
for f in runpod.env runpod-admin.env secrets/runpod.env; do
  mkdir -p "$(dirname "$f")"; echo x > "$f"; git add -f "$f"
  expect "staged $f blocked" 1 --staged
  contains "staged $f blocked reason" "forbidden path: $f"
  reset
done
kv="RUNPOD_""JOBS_""KEY"
printf '%s=%s\n' "$kv" "abcdefghijk" > notes.txt; git add notes.txt
expect "RunPod key assignment (11 chars) blocked" 1 --staged
contains "RunPod key rule" "metamusic-runpod-key"
reset
printf '%s: %s\n' "runpod_admin_key" "abcdefghijk" > notes.txt; git add notes.txt
expect "RunPod key assignment (yaml, lowercase) blocked" 1 --staged
reset
printf '%s=%s\n' "$kv" "short" > notes.txt; git add notes.txt
expect "RunPod key name with a short value passes" 0 --staged
reset
printf 'the %s variable names the jobs key\n' "$kv" > notes.txt; git add notes.txt
expect "RunPod key name in prose passes" 0 --staged
reset

# an allow-listed icon is a real PNG; a fake one is not
mkdir -p web/public/icons
printf '\211PNG\r\n\032\n\0\0\0\rIHDR' > web/public/icons/ok.png; git add web/public/icons/ok.png
expect "real png icon allowed" 0 --staged
reset
mkdir -p web/public/icons; printf '%%PDF-1.4\n\0' > web/public/icons/fake.png; git add -f web/public/icons/fake.png
expect "fake png icon blocked" 1 --staged
reset

# refs: tag messages, ref names, and tags pointing at blobs/trees
git tag -a v-priv -m "deploy at private-thing.lan"
PUBLIC_GATE_PATTERNS=private-thing expect "private tag message blocked" 1 --history
contains "tag message reason" "ref name or tag metadata"
git tag -d v-priv >/dev/null
git tag release-private-thing
PUBLIC_GATE_PATTERNS=private-thing expect "private tag name blocked" 1 --history
git tag -d release-private-thing >/dev/null
git branch feature-private-thing
PUBLIC_GATE_PATTERNS=private-thing expect "private branch name blocked" 1 --history
git branch -qD feature-private-thing
blobsha=$(printf 'token = "%s"\n' "$tok" | git hash-object -w --stdin)
git tag blobtag "$blobsha"
expect "tag pointing at a blob blocked" 1 --history
contains "blob tag reason" "ref points directly at a blob"
git tag -d blobtag >/dev/null
treesha=$(git rev-parse HEAD^{tree})
git tag treetag "$treesha"
expect "tag pointing at a tree blocked" 1 --history
git tag -d treetag >/dev/null
# annotated tag whose target is a blob holding a token
git tag -a -m t blobann "$blobsha"
expect "annotated tag at a blob blocked" 1 --history
git tag -d blobann >/dev/null
# private content reachable only through a tag-pointed tree
mkdir -p tt/docs/private; echo hi > tt/docs/private/design.md
tsha=$(cd tt && GIT_INDEX_FILE=$tmp/idx git add -A . 2>/dev/null; GIT_INDEX_FILE=$tmp/idx git write-tree)
rm -rf tt "$tmp/idx"
git tag -a -m t treeann "$tsha"
expect "annotated tag at a tree blocked" 1 --history
git tag -d treeann >/dev/null
expect "history clean again after tag cleanup" 0 --history

# --require-patterns fails closed when no patterns are configured
expect "no patterns: warn only by default" 0 --staged
expect "no patterns: --require-patterns fails" 1 --staged --require-patterns
contains "require reason" "--require-patterns"
PUBLIC_GATE_PATTERNS=nothing-here expect "patterns present: --require-patterns passes" 0 --staged --require-patterns


exit $fail
