#!/usr/bin/env bash
# Usage: scripts/apt-install.sh <package>...
# CI apt install that cannot hang a job: first try the cached archives only (no mirror contact, no `apt-get update`), and only when
# that fails run update + install, each attempt under `timeout 180`, up to 3 attempts. APT_TIMEOUT / APT_ATTEMPTS override (tests).
set -uo pipefail
[ $# -gt 0 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
t=${APT_TIMEOUT:-180}
n=${APT_ATTEMPTS:-3}
apt=(sudo apt-get install -y --no-install-recommends)
if timeout "$t" "${apt[@]}" --no-download "$@"; then
  echo "apt-install: satisfied from cached archives"
  exit 0
fi
echo "apt-install: cache cannot satisfy the install, falling back to update + install"
for ((i = 1; i <= n; i++)); do
  if timeout "$t" sudo apt-get update && timeout "$t" "${apt[@]}" "$@"; then exit 0; fi
  echo "apt-install: attempt $i/$n failed or timed out" >&2
done
exit 1
