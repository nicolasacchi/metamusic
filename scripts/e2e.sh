#!/usr/bin/env bash
# Build SPA + coverage-instrumented binary, run Playwright (chrome + firefox).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ ! -d web/node_modules ]; then
  (cd web && bun install --frozen-lockfile)
fi
(cd web && bun run build)

mkdir -p bin covdata
rm -rf covdata/*
CGO_ENABLED=0 go build -cover -covermode=atomic -o bin/metamusic-e2e ./cmd/metamusic

# chrome comes from the system install; firefox is fetched on demand.
(cd web && bunx playwright install firefox)

export MM_E2E_BIN="${MM_E2E_BIN:-$root/bin/metamusic-e2e}"
export GOCOVERDIR="$root/covdata"
(cd web && bun run e2e)

go tool covdata percent -i="$root/covdata" || true
