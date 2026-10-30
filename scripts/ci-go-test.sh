#!/usr/bin/env bash
# Usage: scripts/ci-go-test.sh run <shard 1..N> <N> <covdata-dir>
#        scripts/ci-go-test.sh plan <shard 1..N> <N>      print the shard's units ("<pkg>" or "<pkg> <i>/<K>")
#        scripts/ci-go-test.sh tests <pkg> <i> <K>        print the top-level tests of <pkg> in test-shard i of K
# CI runs the Go tests as N parallel shards (M3 debts): every package of `go list ./...` lands in exactly one
# shard, except the few giants listed with "split K" in scripts/ci-shard-weights.txt, whose top-level tests are
# partitioned K ways by a stable hash of the test name. Units are balanced greedily (longest first) by the
# weights in that file; an unlisted package weighs 3, so a new package is covered with no edit here.
# scripts/ci-selftest.sh asserts that the union of all shards is the whole module, once. Every shard runs
# -race -shuffle=on with coverage data written to <covdata-dir> (the coverage job merges them).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
weights="$root/scripts/ci-shard-weights.txt"

# unit lines: "<weight> <pkg> <i>/<K>" for split packages, "<weight> <pkg>" otherwise (pkg is ./-relative).
# A split package's parts are not equal: the tests pinned to part i in scripts/ci-heavy-tests.txt weigh what they weigh
# (a single 180 s test makes its part >= 180), the rest of the package is spread evenly over the K parts.
units() {
  go list ./... | sed "s|^$(go list -m)|.|" | awk -v wf="$weights" -v hf="$root/scripts/ci-heavy-tests.txt" '
    BEGIN { while ((getline l < wf) > 0) { if (l ~ /^#/ || l ~ /^[[:space:]]*$/) continue; split(l, f, " "); w[f[1]] = f[2]; k[f[1]] = (f[3] ? f[3] : 1) }
            while ((getline l < hf) > 0) { if (l ~ /^#/ || l ~ /^[[:space:]]*$/) continue; split(l, f, " "); if (f[1] in k && k[f[1]] > 1) { q = f[4] % k[f[1]]; pin[f[1], q] += f[3]; pt[f[1]] += f[3] } } }
    { p = $0; wt = (p in w) ? w[p] : 3; n = (p in k) ? k[p] : 1
      if (n > 1) {
        sc = (pt[p] > 0.9 * wt) ? 0.9 * wt / pt[p] : 1; rest = wt - pt[p] * sc
        for (i = 0; i < n; i++) printf "%.3f %s %d/%d\n", rest / n + pin[p, i] * sc, p, i, n
      } else printf "%.3f %s\n", wt, p }'
}

plan_all() { # prints "<shard> <unit>" for shard 1..n
  local n=$1
  units | sort -k1,1nr -k2,2 -k3,3 | awk -v n="$n" '
    { best = 1; for (s = 2; s <= n; s++) if (load[s] < load[best]) best = s
      load[best] += $1; $1 = ""; sub(/^ /, ""); print best, $0 }'
}

tests_of() { # all top-level tests of a package
  go test -list '.*' "$1" | grep -E '^(Test|Example|Fuzz)[^[:space:]]*$' || true
}

tests_in_shard() { # <pkg> <i> <K>
  local pkg=$1 i=$2 k=$3 t h p
  while read -r t; do
    # a listed heavy test goes to its pinned part (mod K), any other test by name hash
    p=$(awk -v pkg="$pkg" -v t="$t" -v k="$k" '$1 == pkg && $1 !~ /^#/ && $2 == t { print $4 % k; exit }' "$root/scripts/ci-heavy-tests.txt")
    if [ -z "$p" ]; then
      h=$(printf '%s' "$t" | cksum | cut -d' ' -f1)
      p=$((h % k))
    fi
    if [ "$p" -eq "$i" ]; then echo "$t"; fi
  done < <(tests_of "$pkg")
}

# The suites fsync heavily (SQLite, the storage-fault chaos tests): on the runner's disk TestChaos_StorageFaults alone
# took about 5 minutes; on tmpfs it takes seconds (as in `make test`, see TEST_TMPDIR in the Makefile).
if [ -d /dev/shm ] && [ -w /dev/shm ]; then export TMPDIR=/dev/shm; fi

cmd=${1:?run|plan|tests}
case "$cmd" in
  plan)
    s=${2:?shard}; n=${3:?count}
    plan_all "$n" | awk -v s="$s" '$1 == s { $1 = ""; sub(/^ /, ""); print }'
    ;;
  tests)
    tests_in_shard "${2:?pkg}" "${3:?i}" "${4:?K}"
    ;;
  run)
    s=${2:?shard}; n=${3:?count}; cov=${4:?covdata dir}
    mkdir -p "$cov"; cov=$(cd "$cov" && pwd)
    mapfile -t plan < <("$0" plan "$s" "$n")
    [ "${#plan[@]}" -gt 0 ] || { echo "ci-go-test: shard $s/$n is empty" >&2; exit 2; }
    whole=(); pids=(); fail=0
    # TestPerf_* budget tests run in the perf job (no race, no cover), not under -race -cover here (scripts/go-suite.sh).
    # modernc.org/* (the transpiled SQLite stack, no project logic) is compiled without race instrumentation, as in
    # `make check`; everything else, ours included, stays under -race. The nightly sets MM_FULL_RACE=1 for full instrumentation.
    flags=(-timeout 12m -race -shuffle=on -cover -skip '^TestPerf_')
    [ "${MM_FULL_RACE:-0}" = 1 ] || flags+=(-gcflags=modernc.org/...=-race=false)
    # With MM_GO_TIMINGS=<dir> every go test run also writes its `-json` event stream to <dir>/<name>.jsonl (the weights
    # source: scripts/ci-weights-refresh.sh); the log shows the usual text.
    tdir=${MM_GO_TIMINGS:-}
    [ -z "$tdir" ] || { mkdir -p "$tdir"; tdir=$(cd "$tdir" && pwd); flags+=(-json); }
    emit() { # emit <name>: pass a go test stream through; with -json keep it in <name>.jsonl and print the plain text
      if [ -z "$tdir" ]; then cat; return; fi
      tee "$tdir/$1.jsonl" | jq -rRj '(try fromjson catch {Action:"output",Output:(.+"\n")}) | select(.Action=="output" or .Action=="build-output") | .Output'
    }
    for u in "${plan[@]}"; do
      read -r pkg part <<<"$u"
      if [ -z "$part" ]; then whole+=("$pkg"); continue; fi
      names=$(tests_in_shard "$pkg" "${part%/*}" "${part#*/}" | paste -sd'|')
      [ -n "$names" ] || { echo "ci-go-test: $u has no tests" >&2; continue; }
      echo "== $pkg tests $part"
      tag=$(printf '%s' "${pkg#./}-${part/\//of}" | tr -c 'A-Za-z0-9_.-' '_')
      { go test "${flags[@]}" -run "^($names)\$" "$pkg" -args -test.gocoverdir="$cov" 2>&1 | emit "s$s-$tag" | sed "s|^|[$pkg $part] |"; } &
      pids+=($!)
    done
    if [ "${#whole[@]}" -gt 0 ]; then
      echo "== ${#whole[@]} whole packages"
      go test "${flags[@]}" "${whole[@]}" -args -test.gocoverdir="$cov" 2>&1 | emit "s$s-whole" || fail=1
    fi
    for p in "${pids[@]}"; do wait "$p" || fail=1; done
    exit $fail
    ;;
  *) echo "ci-go-test: unknown command $cmd" >&2; exit 2 ;;
esac
