#!/usr/bin/env bash
# `make verify`: the inner-loop verifier. It checks what this checkout changed versus origin/main
# (scripts/changed.sh) and nothing else:
#   - gofmt on the changed .go files; go vet on the changed packages and their reverse dependencies;
#     golangci-lint on the changed packages; go test -race -short on the changed packages plus every
#     package that imports them (non-test edges transitively, then packages whose tests import any of those)
#   - web lint, svelte-check/tsc and unit tests when web/ changed (or openapi.json)
#   - the openapi drift check when the API surface changed
#   - the incremental privacy gate (scripts/gate.sh)
#   - Playwright, only the specs selected from the changed files (scripts/e2e-map.txt), via scripts/e2e.sh
# go.mod/go.sum/.golangci.yml, or a path this script cannot place, selects everything (whole module, web, every
# spec). `make check`, `make test` and `make e2e` stay the full local pass; CI is the full sharded verifier.
#
#   verify.sh            run
#   verify.sh --plan     read "<status><TAB><path>" lines on stdin, print the plan, run nothing
set -uo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
root=${MM_VERIFY_ROOT:-$here}   # the module to plan for (the selftest points it at a scratch module)
cd "$root" || exit 2
mode=run
[ "${1:-}" = --plan ] && mode=plan
mapfile=${MM_E2E_MAP:-$here/scripts/e2e-map.txt}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# ---------------------------------------------------------------- plan
plan() {
  local mod all=0 reason= web=0 openapi=0 st p d anc f
  local -A pkgdir=() chg=() e2e=() efile=() fmtf=() ancdone=()
  local core=0 none_e2e=0
  mod=$(go list -m 2>/dev/null) || mod=
  go list -e -f '{{.ImportPath}}|{{.Dir}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}' ./... >"$tmp/pkgs" 2>/dev/null
  local ip dir
  while IFS='|' read -r ip dir _; do
    [ -n "$ip" ] || continue
    d=${dir#"$PWD"}; d=${d#/}; [ -n "$d" ] || d=.
    pkgdir[$d]=$ip
  done <"$tmp/pkgs"

  # ascend from a path to its enclosing Go package; prints the import path
  owner() {
    local x=$1
    x=$(dirname "$x")
    while :; do
      [ -n "${pkgdir[$x]:-}" ] && { echo "${pkgdir[$x]}"; return 0; }
      [ "$x" = . ] && return 1
      x=$(dirname "$x")
    done
  }
  addspecs() { local s; for s in $1; do case "$s" in -) ;; core) core=1 ;; *) e2e[$s]=1 ;; esac; done; }
  # the e2e-map line for a path (first match wins); prints the spec list, returns 1 when no line matches
  mapped() {
    local pat specs
    while IFS=$'\t' read -r pat specs; do
      case "$pat" in '' | '#'*) continue ;; esac
      # shellcheck disable=SC2254
      case "$1" in $pat) echo "$specs"; return 0 ;; esac
    done <"$mapfile"
    return 1
  }
  local corespecs
  corespecs=$(awk -F'\t' '$1=="@core"{print $2}' "$mapfile")

  while IFS=$'\t' read -r st p; do
    [ -n "$p" ] || continue
    local goish=0 specs=
    case "$p" in
      go.mod | go.sum | go.work | go.work.sum | .golangci.yml | .golangci.yaml | .golangci.toml)
        all=1; reason="${reason:+$reason, }$p"; continue ;;
    esac
    case "$p" in
      web/e2e/*.spec.ts)
        web=1; efile[$(basename "$p" .spec.ts)]=1; continue ;;
      web/e2e/*)
        web=1
        local helper specsrc found=0
        helper=$(basename "$p"); helper=${helper%.*}
        for specsrc in web/e2e/*.spec.ts; do
          grep -q "from \"\./$helper\"" "$specsrc" 2>/dev/null && { efile[$(basename "$specsrc" .spec.ts)]=1; found=1; }
        done
        [ "$found" = 1 ] || core=1
        continue ;;
      web/playwright.config.ts | web/package.json | web/bun.lock | web/vite.config.ts | web/svelte.config.js | web/index.html | web/public/*)
        web=1; core=1; continue ;;
      web/embed.go) web=1; goish=1 ;;
      web/*) web=1
        case "$p" in web/src/*)
          if specs=$(mapped "$p"); then addspecs "$specs"; else core=1; fi ;;
        esac
        continue ;;
      openapi.json) web=1; openapi=1; continue ;;
      docs/* | *.md | LICENSE | worker/* | metrics/* | .githooks/* | .github/* | scripts/* | Makefile | .gitleaks.toml | .gitignore | \
        .dockerignore | Dockerfile | coverage.thresholds | .public-gate-patterns.example | deploy/*) continue ;;
    esac
    goish=1
    local o
    if o=$(owner "$p"); then
      chg[$o]=1
      [ "${p##*.}" = go ] && [ "$st" != D ] && fmtf[$p]=1
      if specs=$(mapped "$p"); then addspecs "$specs"; fi
      case "$p" in internal/api/* | cmd/*) openapi=1 ;; esac
    elif [ "$st" = D ]; then
      # a deleted package: whoever still imports it must be re-checked (the compile error is the point)
      anc=$(dirname "$p")
      while [ ! -d "$anc" ] && [ "$anc" != . ]; do
        [ -n "${ancdone[$anc]:-}" ] || {
          ancdone[$anc]=1
          while IFS= read -r f; do
            if o=$(owner "$f"); then chg[$o]=1; fmtf[$f]=1; fi
          done < <(grep -rlF --include='*.go' "\"$mod/$anc" . 2>/dev/null | sed 's|^\./||')
        }
        anc=$(dirname "$anc")
      done
      specs=$(mapped "$p") && addspecs "$specs"
    else
      all=1; reason="${reason:+$reason, }unplaced path $p"
    fi
  done

  # reverse dependencies: non-test imports transitively, then packages whose tests import any of those
  local seed
  seed=$(printf '%s\n' "${!chg[@]}")
  local closure
  closure=$(awk -F'|' -v seed="$seed" '
    BEGIN { n = split(seed, s, "\n"); for (i = 1; i <= n; i++) if (s[i] != "") in_[s[i]] = 1 }
    { pk[NR] = $1; k = split($3, im, " "); for (j = 1; j <= k; j++) imp[NR, j] = im[j]; ni[NR] = k
      k = split($4 " " $5, ti, " "); for (j = 1; j <= k; j++) tst[NR, j] = ti[j]; nt[NR] = k }
    END {
      do { ch = 0
        for (r = 1; r <= NR; r++) if (!(pk[r] in in_)) for (j = 1; j <= ni[r]; j++) if (imp[r, j] in in_) { in_[pk[r]] = 1; ch = 1; break }
      } while (ch)
      for (r = 1; r <= NR; r++) if (!(pk[r] in in_)) for (j = 1; j <= nt[r]; j++) if (tst[r, j] in in_) { out[pk[r]] = 1; break }
      for (x in in_) print x
      for (x in out) print x
    }' "$tmp/pkgs" | sort -u)

  local e2emode=none specs_out= files_out=
  if [ "$all" = 1 ]; then
    web=1; openapi=1; e2emode=all
    specs_out='*'
  else
    local k
    [ "$core" = 1 ] && for k in $corespecs; do e2e[$k]=1; done
    for k in "${!efile[@]}"; do e2e[$k]=1; done
    if [ "${#e2e[@]}" -gt 0 ]; then
      e2emode=specs
      specs_out=$(printf '%s\n' "${!e2e[@]}" | sort | tr '\n' ' ')
      specs_out=${specs_out% }
    fi
  fi
  local npk
  npk=$(printf '%s' "$closure" | grep -c . || true)
  # heavy = anything that runs go test -race or Playwright: those take the machine-wide suite lock like make check/e2e
  local heavy=0
  { [ "$all" = 1 ] || [ "$e2emode" != none ] || [ "${npk:-0}" -gt 0 ]; } && heavy=1
  echo "all=$all"
  echo "reason=${reason}"
  echo "web=$web"
  echo "openapi=$openapi"
  echo "changed_pkgs=$(printf '%s\n' "${!chg[@]}" | sed "s|^$mod/||; s|^$mod\$|.|" | sort | tr '\n' ' ' | sed 's/ $//')"
  echo "test_pkgs=$(printf '%s\n' "$closure" | sed "s|^$mod/||; s|^$mod\$|.|" | grep . | tr '\n' ' ' | sed 's/ $//')"
  echo "fmt_files=$(printf '%s\n' "${!fmtf[@]}" | sort | tr '\n' ' ' | sed 's/ $//')"
  echo "e2e=$e2emode"
  echo "e2e_specs=$specs_out"
  echo "heavy=$heavy"
}

get() { sed -n "s/^$1=//p" "$tmp/plan" | head -n1; }

if [ "$mode" = plan ]; then
  cat >"$tmp/changes"
  plan <"$tmp/changes"
  exit 0
fi

# ---------------------------------------------------------------- run
scripts/changed.sh >"$tmp/changes" || exit 2
plan <"$tmp/changes" >"$tmp/plan"

# The heavy classification depends on the plan (whole-module tests and e2e are heavy), so the work runs
# inside one loop row whose weight verify decides: re-exec under scripts/loop unless already inside it.
if [ -z "${MM_VERIFY_INNER:-}" ]; then
  export MM_VERIFY_INNER=1
  [ "$(get heavy)" = 1 ] && export MM_LOOP_HEAVY=1
  scripts/loop verify -- "$0"
  exit $?
fi

all=$(get all) web=$(get web) e2e=$(get e2e) openapi=$(get openapi)
read -r -a pkgs <<<"$(get test_pkgs)"
read -r -a fmtfiles <<<"$(get fmt_files)"
read -r -a changed <<<"$(get changed_pkgs)"
echo "verify: base=$(git merge-base origin/main HEAD 2>/dev/null | cut -c1-8) changed paths=$(wc -l <"$tmp/changes")"
reason=$(get reason)
echo "verify: all=$all${reason:+ ($(get reason))} go-changed=${#changed[@]} go-test=${#pkgs[@]} web=$web e2e=$e2e specs=[$(get e2e_specs)]"

GOLINT=$(ls bin/golangci-lint 2>/dev/null || command -v golangci-lint || echo golangci-lint)
export TMPDIR=${TEST_TMPDIR:-$([ -d /dev/shm ] && [ -w /dev/shm ] && echo /dev/shm || echo "${TMPDIR:-/tmp}")}
rc=0
step() { echo "verify: $*"; }

# web side in the background (independent of the Go side)
wp=
if [ "$web" = 1 ]; then
  [ -d web/node_modules ] || (cd web && bun install --frozen-lockfile) >/dev/null 2>&1
  scripts/web-check.sh >"$tmp/web.log" 2>&1 &
  wp=$!
fi

# Go side
if [ "$all" = 1 ]; then gopk=(./...); lintpk=(./...); else
  gopk=(); for x in "${pkgs[@]}"; do gopk+=("./$x"); done
  lintpk=(); for x in "${changed[@]}"; do lintpk+=("./$x"); done
fi
if [ ${#fmtfiles[@]} -gt 0 ] || [ "$all" = 1 ]; then
  step "gofmt"
  if [ "$all" = 1 ]; then out=$(gofmt -l $(git ls-files '*.go')); else out=$(gofmt -l "${fmtfiles[@]}" 2>&1); fi
  [ -z "$out" ] || { echo "gofmt needed:"; echo "$out"; rc=1; }
fi
if [ ${#gopk[@]} -gt 0 ]; then
  step "vet ${#gopk[@]} pkg(s); lint ${#lintpk[@]}; test -race -short ${#gopk[@]}"
  # vet and lint run beside the tests (all three are compile-bound; sequencing them only adds their latencies)
  go vet "${gopk[@]}" >"$tmp/vet.log" 2>&1 &
  vp=$!
  [ ${#lintpk[@]} -eq 0 ] || "$GOLINT" run "${lintpk[@]}" >"$tmp/lint.log" 2>&1 &
  lp=$!
  GORACE=atexit_sleep_ms=0 GOGC=400 CGO_ENABLED=1 go test -race -gcflags=modernc.org/...=-race=false -short "${gopk[@]}" || rc=1
  wait "$vp" || rc=1; cat "$tmp/vet.log"
  if [ -n "${lp:-}" ]; then wait "$lp" || rc=1; cat "$tmp/lint.log"; fi
fi
if [ "$openapi" = 1 ]; then
  step "openapi drift"
  CGO_ENABLED=0 go run ./cmd/metamusic openapi 2>/dev/null | diff -q - openapi.json >/dev/null || { echo "openapi.json is stale: run make openapi"; rc=1; }
fi
if [ -n "$wp" ]; then wait "$wp" || rc=1; cat "$tmp/web.log"; fi

step "incremental privacy gate"
scripts/gate.sh incremental || rc=1

# e2e: only the specs the plan selected; scripts/e2e.sh owns the build and the run
if [ "$e2e" != none ] && [ "$rc" = 0 ]; then
  specs=$(get e2e_specs)
  files=
  if [ "$specs" != '*' ]; then for s in $specs; do files="$files e2e/$s.spec.ts"; done; fi
  step "e2e specs:${files:- all}"
  MM_E2E_SPECS="${files# }" MM_E2E_COVERAGE=0 scripts/e2e.sh || rc=1
elif [ "$e2e" != none ]; then
  step "e2e skipped: an earlier step failed"
fi
[ "$rc" = 0 ] && echo "verify: OK" || echo "verify: FAILED"
exit "$rc"
