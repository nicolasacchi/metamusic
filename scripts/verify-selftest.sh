#!/usr/bin/env bash
# Self-test for the changed-scope selection: scripts/changed.sh, scripts/verify.sh --plan,
# scripts/gate.sh --select and scripts/e2e-map.txt.
set -uo pipefail
# A git hook (pre-push) runs with GIT_DIR/GIT_INDEX_FILE/... exported; scratch repos created below would then
# silently operate on the real repository (commits, tags, user.name/email). Never inherit that.
unset $(git rev-parse --local-env-vars)
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }
tab=$'\t'
plan() { printf '%b' "$1" | "$root/scripts/verify.sh" --plan; }          # on the real repo
val() { sed -n "s/^$1=//p" <<<"$2" | head -n1; }
sel() { printf '%b' "$1" | "$root/scripts/gate.sh" --select | tr '\n' ' '; }

# ---- gate.sh --select: a selftest runs only when what it tests changed
chk "docs-only change selects no selftest" '[ -z "$(sel "M\tdocs/architecture.md\nM\tCLAUDE.md\n")" ]'
chk "web-only change selects no selftest" '[ -z "$(sel "M\tweb/src/routes/Library.svelte\n")" ]'
chk "plain Go edit selects no selftest" '[ -z "$(sel "M\tinternal/redact/redact.go\n")" ]'
chk "public-gate.sh selects public-gate-selftest" '[[ "$(sel "M\tscripts/public-gate.sh\n")" == *public-gate-selftest* ]]'
chk "scripts/loop selects loop-selftest and ci-selftest" 's=$(sel "M\tscripts/loop\n"); [[ $s == *loop-selftest* && $s == *ci-selftest* ]]'
chk "metrics schema selects loop-selftest" '[[ "$(sel "M\tmetrics/loop.schema.json\n")" == *loop-selftest* ]]'
chk "workflow change selects ci-selftest and actions-pinned" 's=$(sel "M\t.github/workflows/ci.yml\n"); [[ $s == *ci-selftest* && $s == *actions-pinned* ]]'
chk "Makefile selects ci-selftest" '[[ "$(sel "M\tMakefile\n")" == *ci-selftest* ]]'
chk "a new Go file selects ci-selftest (package/test set moved)" '[[ "$(sel "A\tinternal/x/x_test.go\n")" == *ci-selftest* ]]'
chk "a deleted Go package selects ci-selftest" '[[ "$(sel "D\tinternal/x/x.go\n")" == *ci-selftest* ]]'
chk "deploy/ selects deploy-preflight-selftest" '[[ "$(sel "M\tdeploy/worker-image.digest\n")" == *deploy-preflight-selftest* ]]'
chk "selection scripts select verify-selftest" '[[ "$(sel "M\tscripts/verify.sh\n")" == *verify-selftest* ]]'

# ---- verify --plan on the real module
p=$(plan "M\tdocs/architecture.md\nM\tREADME.md\nM\tworker/mmworker/x.py\nM\tscripts/gate.sh\n")
chk "docs-only: no Go, no web, no e2e, light" '[ "$(val all "$p")" = 0 ] && [ -z "$(val test_pkgs "$p")" ] && [ "$(val web "$p")" = 0 ] && [ "$(val e2e "$p")" = none ] && [ "$(val heavy "$p")" = 0 ]'
p=$(plan "M\tweb/src/lib/money.test.ts\n")
chk "web unit-test-only change: web checks, no Go, no e2e" '[ "$(val web "$p")" = 1 ] && [ -z "$(val test_pkgs "$p")" ] && [ "$(val e2e "$p")" = none ] && [ "$(val heavy "$p")" = 0 ]'
p=$(plan "M\tweb/src/routes/Duels.svelte\n")
chk "web-only change: web checks, no Go, mapped spec only" '[ "$(val web "$p")" = 1 ] && [ -z "$(val test_pkgs "$p")" ] && [ "$(val e2e "$p")" = specs ] && [ "$(val e2e_specs "$p")" = m2b-duels ] && [ "$(val heavy "$p")" = 1 ]'
p=$(plan "M\tweb/src/App.svelte\n")
chk "unmapped web source falls back to the core specs" '[ "$(val e2e_specs "$p")" = "m1-library m1-player smoke" ]'
p=$(plan "M\tweb/e2e/m3-spend.spec.ts\n")
chk "changed spec runs itself" '[ "$(val e2e_specs "$p")" = m3-spend ] && [ "$(val web "$p")" = 1 ]'
p=$(plan "M\tweb/e2e/m3Api.ts\n")
chk "changed e2e helper runs the specs that import it" '[[ "$(val e2e_specs "$p")" == *m3-batch* ]] && [[ "$(val e2e_specs "$p")" != *smoke* ]]'
p=$(plan "M\tgo.mod\n")
chk "go.mod change runs everything" '[ "$(val all "$p")" = 1 ] && [ "$(val web "$p")" = 1 ] && [ "$(val e2e "$p")" = all ] && [ "$(val openapi "$p")" = 1 ] && [ "$(val heavy "$p")" = 1 ]'
p=$(plan "M\tgo.sum\n")
chk "go.sum change runs everything" '[ "$(val all "$p")" = 1 ]'
p=$(plan "M\t.golangci.yml\n")
chk ".golangci.yml change runs everything" '[ "$(val all "$p")" = 1 ]'
p=$(plan "A\tsomething/unknown.bin\n")
chk "a path nothing can place selects everything (fail closed)" '[ "$(val all "$p")" = 1 ]'
p=$(plan "M\tinternal/redact/redact.go\n")
chk "Go edit: the package and its reverse dependencies, tests -race" 'tp=" $(val test_pkgs "$p") "; [[ $tp == *" internal/redact "* ]] && [ "$(wc -w <<<"$tp")" -gt 3 ] && [ "$(val changed_pkgs "$p")" = internal/redact ] && [ "$(val web "$p")" = 0 ]'
chk "any Go test run takes the heavy-suite lock" '[ "$(val heavy "$p")" = 1 ]'
p=$(plan "M\tweb/e2e/fixtures.ts\n")
chk "the shared e2e fixtures re-run the specs that import them (all of them)" '[ "$(val e2e "$p")" = specs ] && [ "$(wc -w <<<"$(val e2e_specs "$p")")" -ge 25 ]'
chk "selection does not depend on the retired e2e sets" '! grep -Eq "sets\.ts|E2E_SETS|MM_E2E_SET" "$root/scripts/verify.sh" "$root/scripts/e2e-map.txt" "$root/scripts/gate.sh" "$root/scripts/e2e.sh"'
p=$(plan "M\tinternal/duels/x.go\n")
chk "Go edit in a mapped package selects its spec" '[ "$(val e2e_specs "$p")" = m2b-duels ]'
p=$(plan "M\tinternal/money/money.go\n")
chk "Go edit in an unmapped package runs no e2e" '[ "$(val e2e "$p")" = none ]'
p=$(plan "M\tinternal/api/api.go\n")
chk "API package change turns on the openapi drift check" '[ "$(val openapi "$p")" = 1 ]'

# ---- scratch module: reverse deps, test-only edges, a package deleted on the branch
m=$tmp/mod
mkdir -p "$m"/a "$m"/b "$m"/c "$m"/d "$m"/e
cat >"$m/go.mod" <<'M'
module example.com/m

go 1.21
M
printf 'package a\n\nfunc A() int { return 1 }\n' >"$m/a/a.go"
printf 'package b\n\nimport "example.com/m/a"\n\nfunc B() int { return a.A() }\n' >"$m/b/b.go"
printf 'package c\n\nfunc C() int { return 3 }\n' >"$m/c/c.go"
printf 'package d\n' >"$m/d/d.go"
printf 'package d\n\nimport (\n\t"testing"\n\n\t"example.com/m/a"\n)\n\nfunc TestD(t *testing.T) { _ = a.A() }\n' >"$m/d/d_test.go"
printf 'package e\n\nimport "example.com/m/b"\n\nfunc E() int { return b.B() }\n' >"$m/e/e.go"
mplan() { printf '%b' "$1" | MM_VERIFY_ROOT="$m" "$root/scripts/verify.sh" --plan; }
p=$(mplan "M\ta/a.go\n")
chk "scratch: edit a -> a, b (import), e (via b), d (test import); not c" '[ "$(val test_pkgs "$p")" = "a b d e" ]'
p=$(mplan "M\tc/c.go\n")
chk "scratch: edit leaf c -> only c, heavy (go test -race)" '[ "$(val test_pkgs "$p")" = c ] && [ "$(val heavy "$p")" = 1 ]'
p=$(mplan "M\td/d_test.go\n")
chk "scratch: edit d's test -> only d (nothing imports d)" '[ "$(val test_pkgs "$p")" = d ]'
p=$(mplan "M\tb/b.go\n")
chk "scratch: edit b -> b, e; a is not re-tested" '[ "$(val test_pkgs "$p")" = "b e" ]'
rm -rf "$m/a"
p=$(mplan "D\ta/a.go\n")
chk "scratch: package a deleted on the branch -> its importers b, d (and e through b) are re-checked, c is not, no 'all'" '[ "$(val all "$p")" = 0 ] && [ "$(val test_pkgs "$p")" = "b d e" ] && [ "$(val changed_pkgs "$p")" = "b d" ]'
p=$(mplan "D\ta/testdata/x.json\nD\ta/a.go\n")
chk "scratch: deleted package's non-Go files do not force 'all'" '[ "$(val all "$p")" = 0 ]'
p=$(mplan "M\tgo.mod\n")
chk "scratch: go.mod change selects everything" '[ "$(val all "$p")" = 1 ]'

# ---- changed.sh in a scratch repo: committed, staged, unstaged, untracked, deleted
r=$tmp/repo
mkdir -p "$r"
(
  cd "$r" && git init -q -b main && . "$root/scripts/selftest-lib.sh" && assert_scratch_repo "$r" "$root" && git config user.email t@example.invalid && git config user.name t
  mkdir -p src && echo 1 >src/keep && echo 1 >src/gone && echo 1 >src/edit && echo 1 >src/staged
  git add -A && git commit -qm base && git tag base
  echo 2 >src/edit && echo 3 >src/staged && git add src/staged && rm src/gone
  mkdir other && echo 5 >other/file && git add other/file && git commit -qm second
  echo 6 >src/untracked
) >/dev/null 2>&1
out=$(cd "$r" && MM_BASE=base "$root/scripts/changed.sh" 2>/dev/null)
chk "changed.sh sees commits, deletions, edits and untracked files" '[ "$out" = "$(printf "A\tother/file\nA\tsrc/untracked\nD\tsrc/gone\nM\tsrc/edit\nM\tsrc/staged\n")" ]'
out=$(cd "$r" && MM_BASE=nonexistent-ref "$root/scripts/changed.sh" 2>/dev/null)
chk "changed.sh fails closed on an unknown base (reports go.mod)" '[ "$out" = "$(printf "M\tgo.mod\n")" ]'

# ---- gate.sh prepush: skips only for a tip cleared by a green full run with the same scripts and patterns
g=$tmp/grepo
mkdir -p "$g/scripts"
cp "$root/scripts/gate.sh" "$root/scripts/public-gate.sh" "$root/scripts/metrics-row.jq" "$g/scripts/"
cp "$root/.gitleaks.toml" "$g/"
(
  cd "$g" && git init -q -b main && . "$root/scripts/selftest-lib.sh" && assert_scratch_repo "$g" "$root" && git config user.email t@example.invalid && git config user.name t
  echo a >f && git add -A && git commit -qm one && echo b >f && git commit -qam two
) >/dev/null 2>&1
h1=$(cd "$g" && git rev-parse HEAD~1) h2=$(cd "$g" && git rev-parse HEAD)
# a stand-in for scripts/loop and the selftests: the hook must run "gate-full" here only when the stamp misses
mkdir -p "$g/bin"
cat >"$g/scripts/loop" <<'L'
#!/usr/bin/env bash
echo "RAN-FULL" >>"$GATE_TEST_LOG"
exit 0
L
chmod +x "$g/scripts/loop"
pp() { (cd "$g" && GATE_TEST_LOG="$tmp/gl" PUBLIC_GATE_PATTERNS=x-pattern scripts/gate.sh prepush <<<"refs/heads/main $1 refs/heads/main 0000000000000000000000000000000000000000" 2>&1); }
: >"$tmp/gl"
pp "$h2" >/dev/null
chk "prepush with no stamp runs the full gate" '[ "$(wc -l <"$tmp/gl")" = 1 ]'
sf=$(cd "$g" && git rev-parse --path-format=absolute --git-common-dir)/gate-full.ok
(cd "$g" && PUBLIC_GATE_PATTERNS=x-pattern scripts/gate.sh --stamp-write)
: >"$tmp/gl"
chk "stamp-write wrote a stamp for HEAD" 'grep -q "^$h2 " "$sf"'
out=$(pp "$h2")
chk "prepush skips a tip with a matching stamp" '[ ! -s "$tmp/gl" ] && grep -q "already cleared" <<<"$out"'
: >"$tmp/gl"; pp "$h1" >/dev/null
chk "prepush reruns for a different tip" '[ "$(wc -l <"$tmp/gl")" = 1 ]'
: >"$tmp/gl"; (cd "$g" && GATE_TEST_LOG="$tmp/gl" PUBLIC_GATE_PATTERNS=other-pattern scripts/gate.sh prepush <<<"refs/heads/main $h2 refs/heads/main 0000000000000000000000000000000000000000" >/dev/null 2>&1)
chk "prepush reruns when the private patterns changed" '[ "$(wc -l <"$tmp/gl")" = 1 ]'
echo dirty >>"$g/f"
rm -f "$sf"; (cd "$g" && PUBLIC_GATE_PATTERNS=x-pattern scripts/gate.sh --stamp-write)
chk "no stamp is written while tracked files differ from HEAD" '[ ! -e "$sf" ]'
(cd "$g" && git checkout -q f)
echo "# edited" >>"$g/scripts/public-gate.sh"
: >"$tmp/gl"; pp "$h2" >/dev/null
chk "prepush reruns when the gate scripts changed" '[ "$(wc -l <"$tmp/gl")" = 1 ]'
: >"$tmp/gl"; (cd "$g" && GATE_TEST_LOG="$tmp/gl" scripts/gate.sh prepush <<<"refs/heads/gone 0000000000000000000000000000000000000000 refs/heads/gone $h2" >/dev/null 2>&1)
chk "prepush: deleting a remote ref needs no scan" '[ ! -s "$tmp/gl" ]'

# ---- selftests that build scratch repos must not inherit a hook's GIT_DIR (they would write to the real repo)
for f in "$root"/scripts/*selftest.sh; do
  grep -q "git init" "$f" || continue
  chk "$(basename "$f") drops inherited GIT_* variables" 'grep -q "git rev-parse --local-env-vars" "$f"'
  chk "$(basename "$f") asserts it works inside its own scratch repository" 'grep -q "assert_scratch_repo " "$f"'
done
# the assertion itself: refuses when git resolves to a different repository than the scratch dir
chk "assert_scratch_repo passes inside a scratch repo" '(cd "$r" && . "$root/scripts/selftest-lib.sh" && assert_scratch_repo "$r" "$root")'
chk "assert_scratch_repo refuses a hook-like GIT_DIR pointing at the real repo" '! (cd "$r" && . "$root/scripts/selftest-lib.sh" && GIT_DIR=$(git -C "$root" rev-parse --path-format=absolute --git-dir) assert_scratch_repo "$r" "$root") 2>/dev/null'
chk "assert_scratch_repo refuses a scratch dir inside the real repo" '! (cd "$root" && . "$root/scripts/selftest-lib.sh" && assert_scratch_repo "$root" "$root") 2>/dev/null'
chk "gate.sh bounds every selftest with a timeout" 'grep -q "timeout -k" "$root/scripts/gate.sh" && grep -q "GATE_SELFTEST_TIMEOUT" "$root/scripts/gate.sh"'
chk "gate.sh drops inherited GIT_* variables" 'grep -q "git rev-parse --local-env-vars" "$root/scripts/gate.sh"'
# the dangerous case itself: run the changed.sh scratch-repo block with a hook-like environment pointing at the real repo
real_git=$(git -C "$root" rev-parse --path-format=absolute --git-dir)
head_before=$(git -C "$root" rev-parse HEAD)
(cd "$tmp" && GIT_DIR=$real_git "$root/scripts/gate.sh" --select </dev/null >/dev/null 2>&1)
chk "gate.sh under a hook-like GIT_DIR leaves the real repo alone" '[ "$(git -C "$root" rev-parse HEAD)" = "$head_before" ]'

# ---- e2e-map.txt integrity: every spec it names exists; every Go dir it names exists
map=$root/scripts/e2e-map.txt
bad=
while IFS="$tab" read -r pat specs; do
  case "$pat" in '' | '#'*) continue ;; esac
  for s in $specs; do
    case "$s" in - | core) continue ;; esac
    [ -f "$root/web/e2e/$s.spec.ts" ] || bad="$bad $s"
  done
  case "$pat" in internal/*/\* | cmd/\*) [ -d "$root/${pat%/\*}" ] || bad="$bad ${pat%/\*}" ;; esac
done <"$map"
chk "e2e-map names only existing specs and Go dirs (${bad:-none missing})" '[ -z "$bad" ]'
chk "e2e-map has a @core line of existing specs" '[ -n "$(awk -F"\t" "\$1==\"@core\"{print \$2}" "$map")" ]'
exit $fail
