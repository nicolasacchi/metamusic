#!/usr/bin/env bash
# Self-test for scripts/deploy-preflight.sh: a fake `status` with one live attempt refuses, none
# proceeds, --force proceeds anyway, and an unreadable status fails closed. No docker, no network.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
pre="$root/scripts/deploy-preflight.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
chk() { if ! eval "$2"; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi; }
fake() { printf '#!/usr/bin/env bash\ncat <<J\n%s\nJ\n' "$2" >"$tmp/$1"; chmod +x "$tmp/$1"; }
run() { MM_PREFLIGHT_STATUS_CMD="$tmp/$1" "$pre" "${@:2}"; }

fake live '{"live_attempts":1,"armed":true}'
fake livelist '{"live_attempts":[{"id":"a1","state":"submitted"}]}'
fake idle '{"live_attempts":0,"armed":false}'
fake idlelist '{"live_attempts":[]}'
fake nofield '{"armed":true}'
fake junk 'not json'
printf '#!/usr/bin/env bash\nexit 7\n' >"$tmp/broken"; chmod +x "$tmp/broken"

chk "one live attempt (count) refuses" '! run live >/dev/null 2>&1'
chk "one live attempt (list) refuses" '! run livelist >/dev/null 2>&1'
msg=$(run live 2>&1 || true)
chk "the refusal names the count and the disarm-drain order" 'grep -q "1 live attempt" <<<"$msg" && grep -qi "disarm" <<<"$msg"'
chk "--force proceeds with a live attempt" 'run live --force >/dev/null 2>&1'
chk "no live attempt (count) proceeds" 'run idle >/dev/null 2>&1'
chk "no live attempt (list) proceeds" 'run idlelist >/dev/null 2>&1'
chk "a status without live_attempts fails closed" '! run nofield >/dev/null 2>&1'
chk "unparseable status fails closed" '! run junk >/dev/null 2>&1'
chk "a failing status command fails closed" '! run broken >/dev/null 2>&1'
chk "--force proceeds (loudly) when the status command is broken" 'msg=$(run broken --force 2>&1) && grep -q "force" <<<"$msg"'
chk "unknown flag is a usage error" 'rc=0; run idle --bogus >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ]'
exit $fail
