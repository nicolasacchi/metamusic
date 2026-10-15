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
chk "go-media timeout is 20 min" 'awk "/^  go-media:/{f=1;next} /^  [a-z]/{f=0} f && /timeout-minutes:/{print \$2}" "$wf/ci.yml" | grep -qx 20'
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

chk "ci-go-test media tier lists jobs and provider/fake" 'grep -q "./internal/jobs" "$root/scripts/ci-go-test.sh" && grep -q "./internal/provider/fake" "$root/scripts/ci-go-test.sh"'
chk "root .dockerignore excludes worker/ and runpod*.env" 'grep -qx "worker/" "$root/.dockerignore" && grep -qx "\*\*/runpod\*.env" "$root/.dockerignore"'
chk "deploy/worker-image.digest exists" 'test -f "$root/deploy/worker-image.digest"'
chk "Makefile has worker, contract, canary targets" 'grep -Eq "^worker:" "$root/Makefile" && grep -Eq "^contract:" "$root/Makefile" && grep -Eq "^canary:" "$root/Makefile"'
chk "make gate runs actions-pinned.sh" 'grep -q "actions-pinned.sh" "$root/Makefile"'
chk "make canary only prints" 'r=$(awk "/^canary:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile"); grep -q "echo" <<<"$r" && ! grep -Eq "go run|bin/metamusic|docker" <<<"$r"'

# L1-I1 (C101.3): the L1 packages land in the fast tier (not the media list), the weekly long property run exists,
# the docs sections exist and every relative link in docs/*.md resolves.
fast_pkgs=$(cd "$root" && bash -c 'media=$(sed -n "/^media=(/,/^)/p" scripts/ci-go-test.sh); for p in picks provenance muses songs sessions sounds board textfold perftest; do if grep -q "internal/$p\b" <<<"$media"; then echo "$p"; fi; done')
chk "L1 packages are not in the media tier list" '[ -z "$fast_pkgs" ]'
chk "L1 tempo/ffmpeg and acceptance stay in the media tier" 'grep -q "./internal/analysis/ffmpeg" "$root/scripts/ci-go-test.sh" && grep -q "./internal/acceptance" "$root/scripts/ci-go-test.sh"'
chk "weekly.yml has provenance-property with MM_PROPERTY_SEQS=300" 'awk "/^  provenance-property:/{f=1;next} /^  [a-z]/{f=0} f" "$wf/weekly.yml" | grep -q "MM_PROPERTY_SEQS: \"300\"" && grep -q "TestProperty_.* ./internal/provenance" "$wf/weekly.yml"'
chk "architecture.md has Library model" 'grep -q "^## Library model" "$root/docs/architecture.md"'
for h in "Reconcile and verify" "Taxonomy edits" "After a restore"; do
  chk "runbook.md has $h" 'grep -q "^## $h" "$root/docs/runbook.md"'
done
chk "device-checklist has DC-L1-1 and DC-L1-2" 'grep -q "DC-L1-1" "$root/docs/device-checklist.md" && grep -q "DC-L1-2" "$root/docs/device-checklist.md"'
chk "docs relative links resolve" '"$root/scripts/docs-links.sh"'
mkdir -p "$tmp/docs-bad"
printf "[x](missing.md)\n" >"$tmp/docs-bad/a.md"
chk "docs-links refuses a dangling link" '! "$root/scripts/docs-links.sh" "$tmp/docs-bad" >/dev/null 2>&1'
exit $fail
