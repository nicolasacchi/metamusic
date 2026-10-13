#!/bin/sh
# Web lint, svelte-check/tsc and unit tests, run side by side (they are independent); every one must pass.
# Output is buffered per step and printed in a fixed order so logs stay readable.
set -u
cd "$(dirname "$0")/../web" || exit 1
if [ ! -d node_modules ]; then
	echo "web/node_modules missing: skipping web checks"
	exit 0
fi
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
bun run lint >"$d/lint" 2>&1 & p1=$!
bun run check >"$d/check" 2>&1 & p2=$!
bun test >"$d/test" 2>&1 & p3=$!
rc=0
wait "$p1" || rc=1
wait "$p2" || rc=1
wait "$p3" || rc=1
cat "$d/lint" "$d/check" "$d/test"
exit "$rc"
