# Shared validator for a metrics/loop.jsonl row (see metrics/loop.schema.json).
# Used by scripts/public-gate.sh and scripts/loop-selftest.sh via `jq -e -f`.
def isint: type == "number" and . == floor and . >= 0;
type == "object"
and ((keys - ["wait_ms"]) | sort) == (["cache","compile_errors","exit","failing_tests","lane","sha","target","ts","wall_ms"])
and (.ts | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
and (.target | IN("check","test","test-long","perf","race","lint","web","e2e","build","openapi","gate","gate-full","verify","docker","fixture-db","cover","routing","seed","worker","contract","canary"))
and (.wall_ms | isint) and ((has("wait_ms") | not) or (.wait_ms | isint)) and (.exit | isint) and (.compile_errors | isint) and (.failing_tests | isint)
and (.sha | type == "string" and test("^([0-9a-f]{7,40}|unknown)$"))
and (.lane | type == "string" and test("^[a-z0-9][a-z0-9_-]{0,31}$"))
and (.cache | IN("warm","cold"))
