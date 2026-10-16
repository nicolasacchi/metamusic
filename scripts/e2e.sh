#!/usr/bin/env bash
# Build SPA + server binary, run Playwright (chrome + firefox). Builds are cached (scripts/e2e-cache.sh):
# the SPA by a content hash, the binary by scripts/source-stamp.sh, node_modules by bun.lock, firefox by revision.
# Default: plain binary, no covdata. MM_E2E_COVER=1 (make e2e-cover): coverage-instrumented binary whose data
# feeds `make cover`.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
# shellcheck source=e2e-cache.sh
. scripts/e2e-cache.sh

cover=${MM_E2E_COVER:-0}
stamp=$(scripts/source-stamp.sh) # sources the binary is built from

lock_hash=$(sha256sum <web/bun.lock | cut -c1-16)
if ! deps_fresh web/node_modules web/node_modules/.mm-lock-hash web/bun.lock; then
  (cd web && bun install --frozen-lockfile)
  echo "$lock_hash" >web/node_modules/.mm-lock-hash
fi

wh=$(web_hash)
if ! spa_fresh web/dist web/dist/.mm-hash "$wh"; then
  (cd web && bun run build)
  echo "$wh" >web/dist/.mm-hash
else
  echo "e2e: SPA build up to date ($wh)"
fi

mkdir -p bin
want="plain:$stamp"
[ "$cover" = 1 ] && want="cover:$stamp"
if ! bin_fresh bin/metamusic-e2e bin/metamusic-e2e.stamp "$want"; then
  rm -f bin/metamusic-e2e.stamp
  if [ "$cover" = 1 ]; then
    CGO_ENABLED=0 go build -cover -covermode=atomic -coverpkg=./... -o bin/metamusic-e2e ./cmd/metamusic
  else
    CGO_ENABLED=0 go build -o bin/metamusic-e2e ./cmd/metamusic
  fi
  echo "$want" >bin/metamusic-e2e.stamp
else
  echo "e2e: server binary up to date ($want)"
fi

# chrome comes from the system install; firefox is fetched on demand.
(cd web && { firefox_installed || bunx playwright install firefox; })

if [ "${MM_E2E_BUILD_ONLY:-0}" = 1 ]; then exit 0; fi # timing aid: stop before Playwright

export MM_E2E_BIN="${MM_E2E_BIN:-$root/bin/metamusic-e2e}"
if [ "$cover" = 1 ]; then
  mkdir -p covdata/e2e
  rm -rf covdata/e2e/* covdata/e2e.stamp
  export GOCOVERDIR="$root/covdata/e2e"
else
  # a plain run writes no coverage; drop covdata/e2e from an earlier cover run so `make cover` cannot merge it
  rm -rf covdata/e2e covdata/e2e.stamp
  unset GOCOVERDIR
fi
# the seeded e2e server finds the pinned ffmpeg/metaflac in bin/
export PATH="$root/bin:$PATH"
rm -rf web/test-results # each run writes its own subdirectory (web/e2e/env.ts)
# MM_E2E_SPECS (space separated spec paths relative to web/, set by `make verify`) runs just those specs; a partial
# run never records coverage data (no stamp), so `make cover` cannot merge it as if it were the whole suite.
# shellcheck disable=SC2086
(cd web && bun run e2e ${MM_E2E_SPECS:+--pass-with-no-tests $MM_E2E_SPECS})

if [ "$cover" = 1 ] && [ -z "${MM_E2E_SPECS:-}" ]; then
  go tool covdata percent -i="$root/covdata/e2e" || true
  echo "$stamp" >covdata/e2e.stamp # scripts/coverage-gate.sh refuses covdata/e2e once sources move on
elif [ "$cover" = 1 ]; then
  rm -rf covdata/e2e covdata/e2e.stamp # a partial run is not the suite: `make cover` must not merge it
fi
