#!/usr/bin/env bash
# Cache decisions for scripts/e2e.sh (sourced; also exercised by scripts/e2e-cache-selftest.sh).
# Each *_fresh function exits 0 when the cached artefact may be reused, non-zero when it must be rebuilt.

# Hash of the SPA build inputs, run from the repo root.
web_hash() {
  { for f in web/index.html web/package.json web/bun.lock web/vite.config.ts web/svelte.config.js \
      web/svelte-bun.ts web/tsconfig.json; do [ -f "$f" ] && printf '%s\0' "$f"; done
    find web/src web/public -type f -print0 2>/dev/null || true
  } | xargs -0 -r sha256sum | LC_ALL=C sort -k2 | sha256sum | cut -c1-16
}

# spa_fresh <dist-dir> <hash-file> <current-hash>: dist has built output and its recorded hash matches.
spa_fresh() {
  [ -f "$1/index.html" ] && [ -f "$2" ] && [ "$(cat "$2")" = "$3" ]
}

# deps_fresh <node_modules> <hash-file> <bun.lock>: node_modules exists and was installed from this lockfile.
deps_fresh() {
  [ -d "$1" ] && [ -f "$2" ] && [ "$(cat "$2")" = "$(sha256sum <"$3" | cut -c1-16)" ]
}

# bin_fresh <binary> <stamp-file> <want>: binary exists and was built for <want> ("<mode>:<source stamp>").
bin_fresh() {
  [ -x "$1" ] && [ -f "$2" ] && [ "$(cat "$2")" = "$3" ]
}

# bin_want <mode> <go-source-stamp> <web-hash>: the stamp the server binary must carry. The binary embeds web/dist
# (//go:embed all:dist), so the SPA hash is part of it; decide it only after the SPA is built or confirmed fresh.
bin_want() { printf '%s:%s:%s' "$1" "$2" "$3"; }

# firefox_installed: the firefox revision pinned by the installed Playwright is in the browser cache.
# Run from web/. Override the cache dir with PLAYWRIGHT_BROWSERS_PATH.
firefox_installed() {
  local json rev dir
  json=$(ls node_modules/playwright-core/browsers.json 2>/dev/null) || return 1
  rev=$(jq -r '.browsers[] | select(.name=="firefox") | .revision' "$json") || return 1
  [ -n "$rev" ] && [ "$rev" != null ] || return 1
  dir=${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}
  [ -f "$dir/firefox-$rev/INSTALLATION_COMPLETE" ]
}
