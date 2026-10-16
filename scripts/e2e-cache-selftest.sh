#!/usr/bin/env bash
# Self-test for the cache decisions in scripts/e2e-cache.sh and the e2e.sh / Makefile wiring.
# No network, no builds.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }
# shellcheck source=e2e-cache.sh
. "$root/scripts/e2e-cache.sh"

# --- SPA: hash changed -> rebuild, unchanged -> skip
mkdir -p "$tmp/r/web/src" "$tmp/r/web/dist"
cd "$tmp/r"
echo '<html>' >web/index.html; echo '{}' >web/package.json; echo lock1 >web/bun.lock
echo 'a' >web/src/a.ts; echo built >web/dist/index.html
h1=$(web_hash)
echo "$h1" >web/dist/.mm-hash
chk "spa unchanged -> skip" 'spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo 'b' >web/src/a.ts
chk "web/src edit -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo 'a' >web/src/a.ts
chk "revert -> skip again" 'spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo lock2 >web/bun.lock
chk "bun.lock edit -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo lock1 >web/bun.lock; echo '<html>x' >web/index.html
chk "index.html edit -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo '<html>' >web/index.html
echo 'c' >web/src/new.ts
chk "new src file -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
rm web/src/new.ts
rm web/dist/index.html
chk "missing dist output -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'
echo built >web/dist/index.html; rm web/dist/.mm-hash
chk "missing hash file -> rebuild" '! spa_fresh web/dist web/dist/.mm-hash "$(web_hash)"'

# --- node_modules vs bun.lock
mkdir web/node_modules
chk "no lock hash recorded -> install" '! deps_fresh web/node_modules web/node_modules/.mm-lock-hash web/bun.lock'
sha256sum <web/bun.lock | cut -c1-16 >web/node_modules/.mm-lock-hash
chk "lock unchanged -> skip install" 'deps_fresh web/node_modules web/node_modules/.mm-lock-hash web/bun.lock'
echo lock3 >web/bun.lock
chk "lock changed -> install" '! deps_fresh web/node_modules web/node_modules/.mm-lock-hash web/bun.lock'

# --- binary: stamp (and mode) mismatch -> rebuild
printf '#!/bin/sh\n' >bin.x; chmod +x bin.x
echo 'plain:abc' >bin.stamp
chk "stamp matches -> skip build" 'bin_fresh bin.x bin.stamp plain:abc'
chk "source stamp mismatch -> rebuild" '! bin_fresh bin.x bin.stamp plain:abd'
chk "plain binary is not reused for cover" '! bin_fresh bin.x bin.stamp cover:abc'
rm bin.stamp
chk "no stamp -> rebuild" '! bin_fresh bin.x bin.stamp plain:abc'
echo 'plain:abc' >bin.stamp; rm bin.x
chk "no binary -> rebuild" '! bin_fresh bin.x bin.stamp plain:abc'

# --- firefox: installed iff the pinned revision is in the browser cache
mkdir -p web/node_modules/playwright-core "$tmp/pw"
echo '{"browsers":[{"name":"chromium","revision":"1"},{"name":"firefox","revision":"4242"}]}' >web/node_modules/playwright-core/browsers.json
cd web
chk "pinned firefox revision absent -> install" '! PLAYWRIGHT_BROWSERS_PATH="$tmp/pw" firefox_installed'
mkdir "$tmp/pw/firefox-4241"; touch "$tmp/pw/firefox-4241/INSTALLATION_COMPLETE"
chk "other revision only -> install" '! PLAYWRIGHT_BROWSERS_PATH="$tmp/pw" firefox_installed'
mkdir "$tmp/pw/firefox-4242"
chk "incomplete install -> install" '! PLAYWRIGHT_BROWSERS_PATH="$tmp/pw" firefox_installed'
touch "$tmp/pw/firefox-4242/INSTALLATION_COMPLETE"
chk "pinned revision present -> skip install" 'PLAYWRIGHT_BROWSERS_PATH="$tmp/pw" firefox_installed'
cd "$root"

# --- wiring: plain by default, covdata only with MM_E2E_COVER=1, cover refusal text points at e2e-cover
chk "e2e.sh builds -cover only when MM_E2E_COVER=1" '[ "$(grep -B2 "go build -cover" "$root/scripts/e2e.sh" | grep -c "\$cover\" = 1")" = 1 ]'
chk "plain run drops covdata/e2e and unsets GOCOVERDIR" 'grep -q "rm -rf covdata/e2e covdata/e2e.stamp" "$root/scripts/e2e.sh" && grep -q "unset GOCOVERDIR" "$root/scripts/e2e.sh"'
chk "Makefile e2e-cover sets MM_E2E_COVER=1" 'awk "/^e2e-cover:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile" | grep -q "MM_E2E_COVER=1"'
chk "Makefile e2e does not" '! awk "/^e2e:/{f=1;next} /^[a-z.]/{f=0} f" "$root/Makefile" | grep -q "MM_E2E_COVER"'
chk "CI e2e job still builds the coverage binary and feeds the coverage job" 'grep -q "go build -cover -covermode=atomic -coverpkg=./..." "$root/.github/workflows/ci.yml" && grep -q "scripts/coverage-gate.sh covdata/unit covdata/e2e" "$root/.github/workflows/ci.yml"'
chk "CI builds the coverage binary once (e2e-bin job) and the e2e shards download it" '[ "$(grep -c "go build -cover" "$root/.github/workflows/ci.yml")" = 1 ] && awk "/^  e2e-bin:/{f=1;next} /^  [a-z]/{f=0} f" "$root/.github/workflows/ci.yml" | grep -q "go build -cover" && grep -q "name: e2e-bin" "$root/.github/workflows/ci.yml" && awk "/^  e2e:/{f=1;next} /^  [a-z]/{f=0} f && /needs:/" "$root/.github/workflows/ci.yml" | grep -q e2e-bin'
exit $fail
