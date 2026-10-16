#!/usr/bin/env bash
# Self-test for the CI wiring added in M3-I1 (C85): no path filter on ci.yml, the worker jobs and
# workflows exist with the documented triggers, every `uses:` is SHA-pinned, the Go tiers include the
# M3 packages, and the helper scripts refuse what they must. No network, no docker.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }
wf="$root/.github/workflows"

chk "ci.yml has no paths: key" '! grep -Eq "^[[:space:]]*paths(-ignore)?:" "$wf/ci.yml"'
chk "ci.yml has the worker-py job" 'grep -Eq "^  worker-py:" "$wf/ci.yml"'
chk "worker-py runs pytest with the 90 gate" 'grep -q -- "--cov-fail-under=90" "$wf/ci.yml"'
chk "go job timeout is 20 min" 'awk "/^  go:/{f=1;next} /^  [a-z]/{f=0} f && /timeout-minutes:/{print \$2}" "$wf/ci.yml" | grep -qx 20'
chk "worker-contract.yml triggers on PR and push with worker paths" \
  'grep -q "pull_request:" "$wf/worker-contract.yml" && grep -q "worker/\*\*" "$wf/worker-contract.yml" && grep -q "schedule:" "$wf/worker-contract.yml" && grep -q "workflow_dispatch:" "$wf/worker-contract.yml"'
chk "worker-contract.yml delegates to scripts/worker-contract.sh" 'grep -q "scripts/worker-contract.sh" "$wf/worker-contract.yml"'
chk "worker-image.yml runs on pull_request (dry run)" 'grep -q "pull_request:" "$wf/worker-image.yml"'
chk "worker-image.yml publish is skipped on pull_request" 'grep -q "github.event_name != .pull_request." "$wf/worker-image.yml"'
chk "worker-image.yml publish only on upstream main" 'grep -q "github.repository == .nicolasacchi/metamusic. && github.ref == .refs/heads/main. && github.event_name != .pull_request." "$wf/worker-image.yml"'
chk "worker-image.yml build-smoke has a repository guard" 'awk "/^  build-smoke:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/worker-image.yml" | grep -q "github.repository =="'
chk "worker-image.yml passes YUE2_COMMIT build-arg" '[ "$(grep -c "YUE2_COMMIT=" "$wf/worker-image.yml")" -ge 2 ]'
chk "worker/contract/YUE2_COMMIT is a 40-hex commit" 'grep -Eq "^[0-9a-f]{40}$" "$root/worker/contract/YUE2_COMMIT"'
chk "worker-contract.sh resolves the pin file" 'grep -q "worker/contract/YUE2_COMMIT" "$root/scripts/worker-contract.sh"'
chk "worker-image.yml build-smoke has read-only contents" 'awk "/^  build-smoke:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/worker-image.yml" | grep -q "contents: read" && ! awk "/^  build-smoke:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/worker-image.yml" | grep -q "write"'
chk "weekly.yml exists with a schedule" 'grep -q "schedule:" "$wf/weekly.yml"'
chk "every uses: is pinned to a 40-hex SHA" '"$root/scripts/actions-pinned.sh"'

# actions-pinned.sh itself refuses tags and branches, accepts SHAs
mkdir -p "$tmp/good" "$tmp/bad"
printf 'jobs:\n  a:\n    steps:\n      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n' >"$tmp/good/a.yml"
printf 'jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n' >"$tmp/bad/a.yml"
printf 'jobs:\n  a:\n    steps:\n      - uses: actions/checkout@main # 3d3c42e5aac5ba805825da76410c181273ba90b1\n' >"$tmp/bad/b.yml"
chk "actions-pinned accepts a SHA" '"$root/scripts/actions-pinned.sh" "$tmp/good" >/dev/null 2>&1'
chk "actions-pinned refuses tags and branches" '! "$root/scripts/actions-pinned.sh" "$tmp/bad" >/dev/null 2>&1'

# Go sharding (debts): the matrix shard list, the shard count passed to ci-go-test.sh and the e2e --shard
# count agree, and the shards' union is every package exactly once, split packages' tests exactly once.
n=$(grep -oE "ci-go-test.sh run .* [0-9]+ covdata" "$wf/ci.yml" | awk '{print $(NF-1)}' | sort -u)
gn=$(grep -c . <<<"$n" || true)
chk "ci.yml runs ci-go-test.sh with one shard count" '[ "$gn" = 1 ]'
mx=$(awk "/^  go:/{f=1;next} /^  [a-z]/{f=0} f && /shard: \[/{print}" "$wf/ci.yml" | grep -oE "[0-9]+" | sort -n | tr "\n" " ")
chk "go matrix is shards 1..$n" '[ "$mx" = "$(seq 1 "$n" | tr "\n" " ")" ]'
chk "ci.yml go tier does not use -short" '! grep -q -- "-short" "$wf/ci.yml"'
en=$(grep -oE -- "--shard=\\$\{\{ matrix.shard \}\}/[0-9]+" "$wf/ci.yml" | grep -oE "[0-9]+$" | sort -u)
chk "e2e runs Playwright --shard=i/N with one N" '[ "$(grep -c . <<<"$en")" = 1 ]'
ex=$(awk "/^  e2e:/{f=1;next} /^  [a-z]/{f=0} f && /shard: \\[/{print}" "$wf/ci.yml" | grep -oE "[0-9]+" | sort -n | tr "\n" " ")
chk "e2e matrix is shards 1..$en" '[ "$ex" = "$(seq 1 "$en" | tr "\n" " ")" ]'
chk "e2e has no hand-made project sets or phase dependencies" '! grep -q "MM_E2E_SET" "$wf/ci.yml" && ! grep -q "dependencies:" "$root/web/playwright.config.ts"'
chk "the coverage job merges every shard" 'grep -q "pattern: covdata-e2e-\*" "$wf/ci.yml" && grep -q "pattern: covdata-unit-\*" "$wf/ci.yml"'
chk "each e2e shard writes its served ops and the coverage job checks the union over all shards" 'grep -q "MM_E2E_SERVED_OUT" "$wf/ci.yml" && grep -q "served-check.ts $en " "$wf/ci.yml" && grep -q "pattern: e2e-served-\*" "$wf/ci.yml"'
chk "ci-go-test.sh runs the suites on tmpfs scratch (fsync-heavy)" 'grep -q "export TMPDIR=/dev/shm" "$root/scripts/ci-go-test.sh"'
chk "shard weights file exists" 'test -f "$root/scripts/ci-shard-weights.txt"'
go list ./... | sed "s|^$(go list -m)|.|" | sort >"$tmp/allpkgs"
: >"$tmp/planned"
for s in $(seq 1 "$n"); do "$root/scripts/ci-go-test.sh" plan "$s" "$n" >>"$tmp/planned"; done
awk '{print $1}' "$tmp/planned" | sort -u >"$tmp/plannedpkgs"
chk "sharding covers every package" 'diff "$tmp/allpkgs" "$tmp/plannedpkgs"'
chk "no whole package or split part is planned twice" '[ -z "$(sort "$tmp/planned" | uniq -d)" ]'
chk "a package is either whole or fully split, never both" 'awk "NF==1{w[\$1]=1} NF==2{s[\$1]=1} END{for(p in w) if(p in s) exit 1}" "$tmp/planned"'
chk "every split package has all its parts planned" 'awk "NF==2{split(\$2,a,\"/\"); c[\$1]++; k[\$1]=a[2]} END{for(p in c) if(c[p]!=k[p]) exit 1}" "$tmp/planned"'
for pk in $(awk 'NF==2{print $1}' "$tmp/planned" | sort -u); do
  k=$(awk -v p="$pk" '$1==p{split($2,a,"/");print a[2];exit}' "$tmp/planned")
  go test -list ".*" "$pk" | grep -E "^(Test|Example|Fuzz)[^[:space:]]*$" | sort >"$tmp/alltests"
  : >"$tmp/parts"
  for i in $(seq 0 $((k-1))); do "$root/scripts/ci-go-test.sh" tests "$pk" "$i" "$k" >>"$tmp/parts"; done
  sort "$tmp/parts" >"$tmp/partsorted"
  chk "split $pk: its $k test parts are every test exactly once" 'test -s "$tmp/alltests" && diff "$tmp/alltests" "$tmp/partsorted"'
done
chk "root .dockerignore excludes worker/ and runpod*.env" 'grep -qx "worker/" "$root/.dockerignore" && grep -qx "\*\*/runpod\*.env" "$root/.dockerignore"'
chk "deploy/worker-image.digest exists" 'test -f "$root/deploy/worker-image.digest"'
chk "Makefile has worker, contract, canary targets" 'grep -Eq "^worker:" "$root/Makefile" && grep -Eq "^contract:" "$root/Makefile" && grep -Eq "^canary:" "$root/Makefile"'
chk "make check keeps go test cacheable (no -shuffle)" '! awk "/^_check:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile" | grep -q -- "-shuffle"'
chk "make test, race and cover keep -shuffle=on" '[ "$(grep -c -E "go test .*-shuffle=on" "$root/Makefile")" -ge 3 ]'
chk "make gate is incremental and make gate-full is complete" 'grep -q "gate.sh incremental" "$root/Makefile" && grep -q "gate.sh full" "$root/Makefile"'
chk "gate-full runs every selftest, actions-pinned and the full-history scan" 'g=$(awk "/^  full\\)/{f=1} f" "$root/scripts/gate.sh"); grep -q "^ALL_SELFTESTS=.*public-gate-selftest.*loop-selftest.*coverage-gate-selftest.*ci-selftest.*deploy-preflight-selftest.*actions-pinned" "$root/scripts/gate.sh" && grep -q -- "--history" <<<"$g"'
chk "pre-push runs the complete gate (gate.sh prepush -> gate-full with the patterns required)" 'grep -q "gate.sh\" prepush" "$root/.githooks/pre-push" && grep -q "GATE_REQUIRE_PATTERNS=1 exec scripts/loop gate-full -- scripts/gate.sh full" "$root/scripts/gate.sh"'
chk "CI gate workflow runs gate.sh full (selftests plus full history)" 'grep -q "scripts/gate.sh full" "$wf/gate.yml" && grep -q "GATE_REQUIRE_PATTERNS" "$wf/gate.yml"'
chk "make canary only prints" 'r=$(awk "/^canary:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile"); grep -q "echo" <<<"$r" && ! grep -Eq "go run|bin/metamusic|docker" <<<"$r"'
exit $fail
