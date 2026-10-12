#!/usr/bin/env bash
# Self-test for scripts/tools.sh (needs network):
#  1. every pinned URL answers 2xx to HEAD (ffmpeg: release URL or its upstream fallback);
#  2. a tampered sha256 makes tools.sh exit non-zero and leaves no binary;
#  3. a second run is a no-op (nothing is downloaded again).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "tools-selftest: FAIL: $*" >&2; exit 1; }

# 1. URLs. Every printed URL must answer 2xx, including the repo release copy of ffmpeg.
# Non-default escape hatch while the release is not uploaded yet: TOOLS_SELFTEST_ALLOW_UPSTREAM_ONLY=1
# tolerates a failing ffmpeg release URL (loud warning) as long as another ffmpeg URL answers.
n=0
declare -A fb=() fok=()
while read -r name url; do
  [ -n "$name" ] || continue
  n=$((n + 1))
  code=$(curl -sIL -o /dev/null -w '%{http_code}' --retry 2 -m 60 "$url" || true)
  case "$code" in
    2??) fok[$name]=1 ;;
    *)
      echo "tools-selftest: $name: HEAD $url -> $code" >&2
      if [ "${TOOLS_SELFTEST_ALLOW_UPSTREAM_ONLY:-}" = 1 ] && [ "$name" = ffmpeg ] && [[ "$url" == *"/nicolasacchi/metamusic/releases/"* ]]; then
        echo "tools-selftest: WARNING: ffmpeg release asset missing; CI depends on the non-durable upstream tag. Upload it before merge." >&2
        fb[$name]=1
      else
        fail "pinned URL does not answer 2xx: $name $url"
      fi
      ;;
  esac
done < <("$root/scripts/tools.sh" --print-urls)
[ "$n" -gt 0 ] || fail "tools.sh --print-urls printed nothing"
for k in "${!fb[@]}"; do [ -n "${fok[$k]:-}" ] || fail "no fallback URL for $k answers 2xx"; done
echo "tools-selftest: all pinned URLs reachable"

# Work on a copy so bin/ of the real tree is untouched (tools.sh derives its root from its own path).
mkdir -p "$tmp/scripts"
cp "$root/scripts/tools.sh" "$tmp/scripts/tools.sh"

# 2. tampered sha256 => non-zero exit and no binary.
sed -E 's/^(  \[ffmpeg-linux-amd64\]=)[0-9a-f]{64}/\1'"$(printf '0%.0s' $(seq 64))"'/' \
  "$root/scripts/tools.sh" >"$tmp/scripts/tools-tampered.sh"
chmod +x "$tmp/scripts/tools-tampered.sh" "$tmp/scripts/tools.sh"
if grep -q "^  \[ffmpeg-linux-amd64\]=0\{64\}" "$tmp/scripts/tools-tampered.sh"; then
  if [ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ]; then
    if "$tmp/scripts/tools-tampered.sh" ffmpeg >"$tmp/tamper.log" 2>&1; then fail "tampered sha256 was accepted"; fi
    grep -q "sha256 mismatch" "$tmp/tamper.log" || fail "no mismatch message: $(cat "$tmp/tamper.log")"
    [ ! -e "$tmp/bin/ffmpeg" ] || fail "binary left behind after sha256 mismatch"
    echo "tools-selftest: tampered sha256 rejected, no binary left"
  else
    echo "tools-selftest: skipping tamper check (ffmpeg is pinned for linux-amd64 only)"
  fi
else
  fail "could not tamper the ffmpeg pin"
fi

# 3. idempotent: the second run downloads nothing.
"$tmp/scripts/tools.sh" >"$tmp/run1.log" 2>&1 || fail "first run failed: $(cat "$tmp/run1.log")"
"$tmp/scripts/tools.sh" >"$tmp/run2.log" 2>&1 || fail "second run failed: $(cat "$tmp/run2.log")"
if grep -Eq 'tools.sh: (downloading|building flac|unavailable)' "$tmp/run2.log"; then fail "second run was not a no-op: $(cat "$tmp/run2.log")"; fi
echo "tools-selftest: second run is a no-op"
echo "tools-selftest: OK"
