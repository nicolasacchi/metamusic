#!/usr/bin/env bash
# Build SPA + coverage-instrumented binary, run Playwright (chrome + firefox).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ ! -d web/node_modules ]; then
  (cd web && bun install --frozen-lockfile)
fi
(cd web && bun run build)

mkdir -p bin covdata/e2e
rm -rf covdata/e2e/*
CGO_ENABLED=0 go build -cover -covermode=atomic -coverpkg=./... -o bin/metamusic-e2e ./cmd/metamusic

# chrome comes from the system install; firefox is fetched on demand.
(cd web && bunx playwright install firefox)

export MM_E2E_BIN="${MM_E2E_BIN:-$root/bin/metamusic-e2e}"
export GOCOVERDIR="$root/covdata/e2e"
# the seeded e2e server finds the pinned ffmpeg/metaflac in bin/
export PATH="$root/bin:$PATH"
(cd web && bun run e2e)

go tool covdata percent -i="$root/covdata/e2e" || true
