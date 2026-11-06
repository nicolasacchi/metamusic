#!/usr/bin/env bash
# Self-test for the test tiers and the timing-driven shard weights: scripts/ci-weights-refresh.sh (write/check),
# scripts/go-suite.sh (test / test-long / perf do not overlap, perf runs only uninstrumented), the CI wiring of the perf
# job and the weights artifacts, and scripts/check-stamp.sh. No network, no docker, no test run.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }
wf="$root/.github/workflows"
mod=$(cd "$root" && go list -m)
refresh="$root/scripts/ci-weights-refresh.sh"

# ---- weights refresh, on a synthetic go test -json stream
ev() { printf '{"Action":"%s","Package":"%s/%s","Elapsed":%s}\n' pass "$mod" "$1" "$2"; }
tev() { printf '{"Action":"pass","Package":"%s/%s","Test":"%s","Elapsed":%s}\n' "$mod" "$1" "$2" "$3"; }
mkdir -p "$tmp/t"
{ ev internal/bigpkg 300; tev internal/bigpkg TestBigA 120; tev internal/bigpkg TestBigB 100; tev internal/bigpkg TestBigC 40
  tev internal/bigpkg TestParent 0; tev internal/bigpkg TestParent/a 15; tev internal/bigpkg TestParent/b 15
  tev internal/bigpkg TestTiny 1; ev internal/slowunlisted 20; ev internal/quick 0.5; ev internal/filler 2000; echo 'not json'; } >"$tmp/t/a.jsonl"
{ ev internal/bigpkg 100; tev internal/bigpkg TestBigD 90; ev internal/listed 40; } >"$tmp/t/b.jsonl"
printf '# header\n./internal/listed 40\n' >"$tmp/w.txt"
chk "check fails on a package slower than 15 s without a weight line" '! MM_WEIGHTS_FILE="$tmp/w.txt" "$refresh" check "$tmp/t" >"$tmp/out" 2>&1; grep -q "slowunlisted" "$tmp/out"'
chk "check names only the unweighted slow package" '! grep -q "quick" "$tmp/out" && ! grep -q "internal/listed" "$tmp/out"'
printf '# header\n./internal/listed 40\n./internal/slowunlisted 20\n./internal/bigpkg 400 4\n./internal/filler 2000 4\n' >"$tmp/w.txt"
chk "check passes once every slow package has an entry" 'MM_WEIGHTS_FILE="$tmp/w.txt" "$refresh" check "$tmp/t" >/dev/null 2>&1'
chk "check threshold is an argument" '! MM_WEIGHTS_FILE="$tmp/w.txt" "$refresh" check "$tmp/t" 1 >/dev/null 2>&1; true'
"$refresh" table "$tmp/t" >"$tmp/table"
chk "table sums a package over its parts (300 + 100)" 'grep -q "^400.0 ./internal/bigpkg" "$tmp/table"'
chk "table counts a parent of parallel subtests by its subtests" 'grep -q "^30.0 ./internal/bigpkg TestParent" "$tmp/table"'
MM_WEIGHTS_FILE="$tmp/w2.txt" MM_HEAVY_FILE="$tmp/h2.txt" "$refresh" write "$tmp/t" 6 >/dev/null
chk "write lists the slow packages, splits the one above a third of a shard, skips the quick one" 'grep -q "^./internal/bigpkg 400 3$" "$tmp/w2.txt" && grep -q "^./internal/slowunlisted 20$" "$tmp/w2.txt" && ! grep -q quick "$tmp/w2.txt"'
chk "write pins the slow tests of a split package to distinct parts, longest first" 'awk "!/^#/ && \$2==\"TestBigA\"{a=\$4} !/^#/ && \$2==\"TestBigB\"{b=\$4} !/^#/ && \$2==\"TestBigD\"{d=\$4} END{exit !(a!=b && b!=d && a!=d)}" "$tmp/h2.txt"'
chk "write does not pin a test under 8 s" '! grep -q TestTiny "$tmp/h2.txt"'
chk "the written weights satisfy check" 'MM_WEIGHTS_FILE="$tmp/w2.txt" "$refresh" check "$tmp/t" >/dev/null 2>&1'
chk "refuses a directory without timings" '! "$refresh" check "$tmp" >/dev/null 2>&1 || mkdir -p "$tmp/empty" && ! "$refresh" check "$tmp/empty" >/dev/null 2>&1'

# ---- tiers: perf / long / the rest never overlap and cover every name
eval "$("$root/scripts/go-suite.sh" --regexes)"
cls() { local n=$1 c=rest; if [[ $n =~ $PERF_RE ]]; then c=perf; fi; if grep -Eq "$LONG_RE" <<<"$n"; then c="$c+long"; fi; echo "$c"; }
chk "TestPerf_X is perf only" '[ "$(cls TestPerf_SearchSounds)" = perf ]'
chk "property, chaos, acceptance and fault-sweep tests are long" '[ "$(cls TestProperty_SpendNeverExceedsCap)" = rest+long ] && [ "$(cls TestChaos_StorageFaults)" = rest+long ] && [ "$(cls TestAcceptance_L1_BackfillMirror)" = rest+long ] && [ "$(cls TestRunner_EveryDatabaseCallMayFail)" = rest+long ]'
chk "an ordinary test is in neither tier" '[ "$(cls TestRunner_HappyPath)" = rest ] && [ "$(cls TestPerfectly)" = rest ]'
chk "test skips perf and long, test-long runs long and skips perf, perf is not instrumented" \
  'g=$(cat "$root/scripts/go-suite.sh"); grep -q -- "-skip \"\$PERF_RE|\${LONG_RE}\"" <<<"$g" && grep -q -- "-run \"\$LONG_RE\" -skip \"\$PERF_RE\"" <<<"$g" && ! grep -E "perf\)" -A4 <<<"$g" | grep -Eq -- "-race|-cover"'
chk "every TestPerf_ lives in a package the perf tier runs" 'want=$(cd "$root" && grep -rl --include="*_test.go" "^func TestPerf_" . | xargs -r -n1 dirname | sort -u); [ -n "$want" ] && [ "$want" = "$("$root/scripts/go-suite.sh" --perf-pkgs | sort -u)" ]'
chk "the Makefile runs test, test-long and perf through scripts/go-suite.sh and the loop" '( for t in test test-long perf; do grep -q "LOOP) $t -- .*scripts/go-suite.sh $t" "$root/Makefile" || exit 1; done )'
chk "the race shards skip TestPerf_ and CI has a perf job without race or cover" 'grep -q -- "-skip .\^TestPerf_." "$root/scripts/ci-go-test.sh" && awk "/^  perf:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/ci.yml" | grep -q "scripts/go-suite.sh perf" && ! awk "/^  perf:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/ci.yml" | grep -Eq -- "-race|-cover"'
chk "CI runs both race tiers: the go job runs every test of every package (no -run filter on the shards besides the split)" '! grep -E "go test.*-short" "$root/scripts/ci-go-test.sh"'
chk "the go jobs upload their go test -json timings and the coverage job checks the weights" 'grep -q "MM_GO_TIMINGS" "$wf/ci.yml" && grep -q "name: gotimings-" "$wf/ci.yml" && grep -q "ci-weights-refresh.sh check" "$wf/ci.yml"'
chk "a nightly job runs the shards with full race instrumentation" 'grep -q "MM_FULL_RACE" "$wf/nightly.yml" && grep -q "schedule:" "$wf/nightly.yml"'

# ---- the check stamp, in a scratch repo
r="$tmp/repo"; mkdir -p "$r/scripts"; cp "$root/scripts/check-stamp.sh" "$r/scripts/"
(
  cd "$r"; unset $(git rev-parse --local-env-vars); git init -q .
  . "$root/scripts/selftest-lib.sh"; assert_scratch_repo "$r" "$root"
  echo a >a.txt; git add a.txt
  s=scripts/check-stamp.sh
  ! $s match >/dev/null 2>&1 || { echo "FAIL: match before any write"; exit 1; }
  $s write
  $s match >"$tmp/m1" 2>&1 || { echo "FAIL: match after write"; exit 1; }
  grep -q "reused" "$tmp/m1" || { echo "FAIL: reuse message"; exit 1; }
  [ -s "$(git rev-parse --absolute-git-dir)/check-stamp.log" ] || { echo "FAIL: reuse log"; exit 1; }
  echo b >a.txt
  ! $s match >/dev/null 2>&1 || { echo "FAIL: edited file still matches"; exit 1; }
  $s write; echo new >untracked.txt
  ! $s match >/dev/null 2>&1 || { echo "FAIL: untracked file still matches"; exit 1; }
  $s write; echo junk >.gitignore; echo ignored.txt >>.gitignore; $s write; echo x >ignored.txt
  $s match >/dev/null 2>&1 || { echo "FAIL: ignored file changed the key"; exit 1; }
  MM_CHECK_STAMP=0 $s match >/dev/null 2>&1 && { echo "FAIL: MM_CHECK_STAMP=0 not honoured"; exit 1; }
  echo "ok:   check stamp: matches an unchanged tree, not an edited, extended or bypassed one; ignores ignored files"
) || fail=1
chk "make check consults the stamp and writes it after a green run" 'awk "/^check:/{f=1;next} /^[a-z_.]/{f=0} f" "$root/Makefile" | grep -q "check-stamp.sh match" && awk "/^check:/{f=1;next} /^[a-z_.]/{f=0} f" "$root/Makefile" | grep -q "check-stamp.sh write"'
chk "CLAUDE.md documents make test-long" 'grep -q "make test-long" "$root/CLAUDE.md"'
# BEGIN T1-I1 toolchain pin and dsp/score tiers (D-T1-3d, C215)
chk "GOAMD64=v1 is pinned in the Makefile, the Dockerfile and every Go workflow env" 'grep -q "^export GOAMD64 := v1" "$root/Makefile" && grep -q "^ENV GOAMD64=v1" "$root/Dockerfile" && for w in ci weekly nightly e2e-extra; do grep -q "^  GOAMD64: v1" "$wf/$w.yml" || exit 1; done'
chk "CI proves the release binary records GOAMD64=v1, and the image build asserts it too" 'grep -q "release binary is built with GOAMD64=v1" "$wf/ci.yml" && grep -q "go version -m /out/metamusic" "$root/Dockerfile"'
chk "make dsp runs through the loop without -race, and make check keeps dsp/score out of the -race run" 'grep -qF "\$(LOOP) dsp -- " "$root/Makefile" && d=$(awk "/^dsp:/{f=1;next} /^[a-z_.]/{f=0} f" "$root/Makefile") && grep -qF "go test -short \$(NORACE_PKGS)" <<<"$d" && ! grep -v "^#" <<<"$d" | grep -q -- "-race" && c=$(awk "/^_check:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile") && grep -qF -- "-race \$(CHECK_GCFLAGS) -short \$(RACE_PKGS)" <<<"$c" && grep -qF "go test -short \$(NORACE_PKGS)" <<<"$c"'
chk "the plain dsp/score shard runs use covermode atomic like the race runs (covdata cannot merge modes)" 'grep -q "flags_nr=(-covermode=atomic)" "$root/scripts/ci-go-test.sh"'
chk "the CI shards run internal/dsp and internal/score without -race (nightly full race keeps them)" 'grep -q "noracere=" "$root/scripts/ci-go-test.sh" && grep -q "flags_nr\[@\]" "$root/scripts/ci-go-test.sh" && grep -q "MM_FULL_RACE:-0.\" != 1 \] && \[\[ \"\$pkg\" =~ \$noracere" "$root/scripts/ci-go-test.sh"'
chk "weekly.yml has the determinism, fast-budget and bench jobs" 'for j in t1-determinism t1-fast-budget t1-transform-bench; do grep -q "^  $j:" "$wf/weekly.yml" || exit 1; done'
# END T1-I1
# BEGIN E2-I1 ACE-Step worker image, contract and floors (C162.1)
chk "worker-image.yml builds worker/Dockerfile.acestep (build-smoke + publish), tolerant of the file not existing, never publishing from a PR" 'grep -q "^  acestep-build-smoke:" "$wf/worker-image.yml" && grep -q "^  acestep-publish:" "$wf/worker-image.yml" && grep -q "file: worker/Dockerfile.acestep" "$wf/worker-image.yml" && grep -q "! -f worker/Dockerfile.acestep" "$wf/worker-image.yml" && awk "/^  acestep-publish:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/worker-image.yml" | grep -q "github.event_name != .pull_request."'
chk "the ACE-Step image smoke asserts Python 3.11, torch 2.10.0 with CUDA >= 12.8, the 14 GB bound and the acestep15 selftest" 'a=$(awk "/^  acestep-build-smoke:/{f=1;next} /^  acestep-publish:/{f=0} f" "$wf/worker-image.yml"); grep -q "(3, 11)" <<<"$a" && grep -q "startswith(.2.10.0.)" <<<"$a" && grep -q "(12, 8)" <<<"$a" && grep -q "14000000000" <<<"$a" && grep -q "ENGINE=acestep15" <<<"$a"'
chk "worker-contract.yml has a contract-acestep job with the ACE-Step path filters on pull_request and push" '[ "$(grep -c "internal/engine/acestep/\*\*" "$wf/worker-contract.yml")" = 2 ] && grep -q "^  contract-acestep:" "$wf/worker-contract.yml" && grep -q "worker-contract.sh acestep15" "$wf/worker-contract.yml"'
chk "worker-contract.sh: yue2 default, acestep15 by argument or ENGINE, unknown engine and pin-less acestep15 refused, --check downloads nothing" '( cd "$root" && scripts/worker-contract.sh --check | grep -q "yue2 pins ok" && ! scripts/worker-contract.sh bogus >/dev/null 2>&1 && ! ENGINE=bogus scripts/worker-contract.sh --check >/dev/null 2>&1 && if [ -f worker/contract/ACESTEP_COMMIT ]; then scripts/worker-contract.sh acestep15 --check >/dev/null 2>&1 || true; else ! scripts/worker-contract.sh acestep15 --check >/dev/null 2>&1; fi )'
chk "the Makefile has worker-acestep and contract-acestep through the loop, outside make check" 'grep -qF "uv run pytest tests --cov=mmworker.engines.acestep" "$root/Makefile" && grep -qF "\$(LOOP) worker -- " "$root/Makefile" && grep -qF "\$(LOOP) contract -- scripts/worker-contract.sh acestep15" "$root/Makefile" && ! awk "/^_check:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile" | grep -q acestep'
chk "coverage.thresholds and shard weights carry E2-I1 blocks and keep the T1-I1 ones" 'for f in coverage.thresholds scripts/ci-shard-weights.txt scripts/e2e-map.txt; do grep -q "BEGIN E2-I1" "$root/$f" && grep -q "END E2-I1" "$root/$f" && grep -q "BEGIN T1-I1" "$root/$f" && grep -q "END T1-I1" "$root/$f" || exit 1; done'
# END E2-I1
# BEGIN E2-I1 image build cache is a registry cache, not the 10 GB Actions cache (buildkit blobs evicted go/apt/playwright)
chk "no workflow uses the type=gha buildx cache" '! grep -rq "type=gha" "$wf"'
chk "image builds read ghcr :buildcache and only the publish jobs write it" 'w="$wf/worker-image.yml"; [ "$(grep -c "cache-from: type=registry,ref=.*:buildcache" "$w")" = 4 ] && [ "$(grep -c "cache-to: type=registry" "$w")" = 2 ] && for j in publish acestep-publish; do awk "/^  $j:/{f=1;next} /^  [a-z]/{f=0} f" "$w" | grep "cache-to: type=registry" >/dev/null || exit 1; done'
# END E2-I1
exit $fail
