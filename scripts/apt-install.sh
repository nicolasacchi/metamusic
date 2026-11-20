#!/usr/bin/env bash
# Usage: scripts/apt-install.sh <package>...
# CI apt install that cannot hang a job and survives mirror flakes: first try the cached archives only (no mirror contact, no
# `apt-get update`), and only when that fails run update + install, each attempt under `timeout 180`, up to 3 attempts with a
# growing pause between them (APT_BACKOFF seconds x attempt number, default 10). A missing package (E: Unable to locate
# package / has no installation candidate) is NOT retried, but only when the `apt-get update` just before it was clean: a flaky
# mirror can leave a partial index (update prints Err: / Failed to fetch / Some index files failed, maybe still exit 0) and the
# install then reports the same message, so that case retries. A real missing package after a clean update fails at once with
# apt's own message. APT_TIMEOUT / APT_ATTEMPTS / APT_BACKOFF override (tests).
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
out=$(mktemp); upd=$(mktemp); trap 'rm -f "$out" "$upd"' EXIT
for ((i = 1; i <= n; i++)); do
  if timeout "$t" sudo apt-get update 2>&1 | tee "$upd" && [ "${PIPESTATUS[0]}" = 0 ]; then
    timeout "$t" "${apt[@]}" "$@" 2>&1 | tee "$out"
    [ "${PIPESTATUS[0]}" = 0 ] && exit 0
    if grep -qE '^E: (Unable to locate package|Package .* has no installation candidate)' "$out" &&
      ! grep -qE '^(Err:|W: Failed to fetch|E: Failed to fetch|W: Some index files failed|E: Some index files failed)' "$upd"; then
      echo "apt-install: package missing after a clean apt-get update, not retrying" >&2
      exit 1
    fi
  fi
  echo "apt-install: attempt $i/$n failed or timed out" >&2
  ((i < n)) && sleep $((b * i))
done
exit 1
