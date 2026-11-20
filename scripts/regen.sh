#!/usr/bin/env bash
# make regen (G1 C329.1): regenerate every generated file of the repository in one idempotent pass, so a lane that rebases
# onto a moved main never hand-merges a generated file. Run through scripts/loop (make regen); `scripts/regen-selftest.sh` proves
# a second run changes nothing.
#
#   1. openapi.json and web/src/api/schema.d.ts          (the `make openapi` recipe)
#   2. internal/api/testdata/role_matrix.golden          (TestRoleMatrixComplete -update)
#   3. internal/mcpgen/testdata/*.golden.json            (tools, prompts and resource-template goldens, -update)
#   4. the generated jobs DDL block, checked for every profile (gen-jobs-ddl.go -check-all: every profile, so a new one is checked here)
#
# Later G1 lanes append their step where the marker says (g1-wire: TestWriteArtifactsV2 -update).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export GOAMD64=v1 CGO_ENABLED=0

step() { echo "regen: $*" >&2; }

[ -d web/node_modules ] || (cd web && bun install --frozen-lockfile >/dev/null)
step openapi
go run ./cmd/metamusic openapi >openapi.json.tmp
mv openapi.json.tmp openapi.json
(cd web && bun run gen:api)

step role matrix
go test ./internal/api -count=1 -run '^TestRoleMatrixComplete$' -update >/dev/null

step mcpgen goldens
go test ./internal/mcpgen -count=1 -run '^(TestToolsListGolden|TestPrompts_ListGolden|TestResources_TemplatesGolden)$' -update >/dev/null

step jobs ddl
go run scripts/gen-jobs-ddl.go -check-all

# REGEN-STEPS: lanes append above this line (one step each, `step <name>` first).
step done
