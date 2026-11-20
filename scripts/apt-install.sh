#!/usr/bin/env bash
# Usage: scripts/apt-install.sh <package>...
# CI apt install that cannot hang a job and survives mirror flakes: first try the cached archives only (no mirror contact, no
# `apt-get update`), and only when that fails run update + install, each attempt under `timeout 180`, up to 3 attempts with a
# growing pause between them (APT_BACKOFF seconds x attempt number, default 10). A deterministic error (E: Unable to locate
# package / has no installation candidate) is NOT retried: it fails at once with apt's own message, so a real missing package
# is never hidden behind retries. APT_TIMEOUT / APT_ATTEMPTS / APT_BACKOFF override (tests).
set -uo pipefail
[ $# -gt 0 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
t=${APT_TIMEOUT:-180}
n=${APT_ATTEMPTS:-3}
b=${APT_BACKOFF:-10}
apt=(sudo apt-get install -y --no-install-recommends)
if timeout "$t" "${apt[@]}" --no-download "$@"; then
  echo "apt-install: satisfied from cached archives"
  exit 0
fi
echo "apt-install: cache cannot satisfy the install, falling back to update + install"
out=$(mktemp); trap 'rm -f "$out"' EXIT
for ((i = 1; i <= n; i++)); do
  if timeout "$t" sudo apt-get update; then
    timeout "$t" "${apt[@]}" "$@" 2>&1 | tee "$out"
    [ "${PIPESTATUS[0]}" = 0 ] && exit 0
    if grep -qE '^E: (Unable to locate package|Package .* has no installation candidate|Unable to correct problems)' "$out"; then
      echo "apt-install: deterministic apt error, not retrying" >&2
      exit 1
    fi
  fi
  echo "apt-install: attempt $i/$n failed or timed out" >&2
  ((i < n)) && sleep $((b * i))
done
exit 1
