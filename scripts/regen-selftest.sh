#!/usr/bin/env bash
# Self-test for scripts/regen.sh (G1 C329.1): a second run changes nothing, and a stale generated file is repaired.
# Runs the real generators (go test of internal/api and internal/mcpgen, bun gen:api), so it is a CI/lane check, not part of
# the light `make gate`.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }

gen=(openapi.json web/src/api/schema.d.ts internal/api/testdata/role_matrix.golden
  internal/mcpgen/testdata/tools_list.golden.json internal/mcpgen/testdata/prompts_list.golden.json internal/mcpgen/testdata/resource_templates.golden.json)
sums() { sha256sum "${gen[@]}"; }

bak=$(mktemp -d)
trap 'for f in "${gen[@]}"; do [ -f "$bak/$(echo "$f" | tr / _)" ] && cp "$bak/$(echo "$f" | tr / _)" "$f"; done; rm -rf "$bak"' EXIT
for f in "${gen[@]}"; do cp "$f" "$bak/$(echo "$f" | tr / _)"; done

scripts/regen.sh >/dev/null 2>&1
first=$(sums)
scripts/regen.sh >/dev/null 2>&1
chk "a second regen changes nothing" '[ "$first" = "$(sums)" ]'
chk "regen on a clean tree leaves the committed files as they were" '[ "$first" = "$(cd "$bak" && for f in "${gen[@]}"; do sha256sum "$(echo "$f" | tr / _)" | sed "s#  .*#  $f#"; done)" ]'

# a stale generated file is rewritten, not left behind
printf '\n' >>"$root/internal/api/testdata/role_matrix.golden"
scripts/regen.sh >/dev/null 2>&1
chk "a hand-edited golden is repaired" '[ "$first" = "$(sums)" ]'

[ "$fail" = 0 ] || exit 1
echo "regen-selftest ok"
