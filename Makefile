SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := check

LOOP   := scripts/loop
GOLINT = $(or $(wildcard bin/golangci-lint),golangci-lint)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
# Go test scratch (t.TempDir) goes to tmpfs when available: the suites fsync heavily and a
# disk-backed TMPDIR adds ~10 s to `make check`. Override with TEST_TMPDIR=<dir>.
# Per-package `go test` timeout for the full-instrumentation targets (test, race, cover). Go's 10 min default is a
# hang detector, but internal/jobs (property + chaos suites) needs ~7.5 min of wall on an idle 8-core host because the
# transpiled SQLite stack under -race costs ~10x (schema parse per connection), and every package of `./...` runs at once,
# so a busy host pushes it past 10 min with nothing wrong. 30 min still catches a real hang.
TEST_TIMEOUT ?= 30m
TEST_TMPDIR ?= $(shell [ -d /dev/shm ] && [ -w /dev/shm ] && echo /dev/shm || echo $${TMPDIR:-/tmp})

.PHONY: build web openapi test race lint check _check e2e e2e-cover verify gate gate-full hooks tools dev docker metrics clean fixture-db cover seed routing worker contract canary

build:
	$(LOOP) build -- sh -c 'mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/metamusic ./cmd/metamusic'

web:
	$(LOOP) web -- sh -c 'cd web && bun install --frozen-lockfile && bun run build'

openapi:
	$(LOOP) openapi -- sh -c 'go run ./cmd/metamusic openapi > openapi.json.tmp && mv openapi.json.tmp openapi.json && cd web && bun run gen:api'

test:
	$(LOOP) test -- env TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on -timeout $(TEST_TIMEOUT) ./...

race:
	$(LOOP) race -- env TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on -timeout $(TEST_TIMEOUT) ./...

lint:
	$(LOOP) lint -- $(GOLINT) run ./...

check:
	$(LOOP) check -- $(MAKE) --no-print-directory _check

# Fast-loop only: the transpiled SQLite stack (modernc.org/*: sqlite, libc, memory) is compiled without
# race/checkptr instrumentation. It is generated C-to-Go code with no project logic, and instrumenting it
# cost about two thirds of suite CPU. Everything else (all of our code, database/sql, net/http) stays
# under -race, and `make test` / `make cover` / CI keep full instrumentation everywhere.
CHECK_GCFLAGS = -gcflags=modernc.org/...=-race=false
# Also fast-loop only: no -shuffle. A shuffle flag disables Go's test result cache, so with it every `make check` re-ran all
# ~70 packages (about 47 s) even when nothing changed; without it an unchanged package is "(cached)" and an edit re-runs only
# its dependents (a no-change run is about 1 s). Order independence stays enforced where it counts: `make test`, `make race`,
# `make cover` and CI all run -shuffle=on (asserted by scripts/ci-selftest.sh).
# GORACE=atexit_sleep_ms=0 drops the race runtime's 1 s sleep at the exit of each of the ~45
# test binaries (about 2 s of wall time on 8 cores) and GOGC=400 trades memory for fewer collections.

# Internal: steps of `check` (run inside one metrics row). -short skips the heavy media-tool/acceptance
# tiers (testtools.Require, TestAcceptance_*); `make test` and CI run them.
_check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@# The web checks (bun, ~7 s) run beside vet/lint/go test: the Go side saturates the cores only while
	@# it links and runs the big packages, and the two sides are independent. Output is buffered and printed
	@# after the Go output; both must pass.
	@log=$$(mktemp); trap 'rm -f "$$log"' EXIT; scripts/web-check.sh >"$$log" 2>&1 & wp=$$!; \
	rc=0; \
	{ go vet ./... && $(GOLINT) run ./... && TMPDIR=$(TEST_TMPDIR) GORACE=atexit_sleep_ms=0 GOGC=400 CGO_ENABLED=1 go test -race $(CHECK_GCFLAGS) -short ./...; } || rc=1; \
	wait $$wp || rc=1; cat "$$log"; exit $$rc

# Plain server binary, cached SPA/binary builds (scripts/e2e-cache.sh); writes no covdata.
e2e:
	$(LOOP) e2e -- scripts/e2e.sh

# Coverage-instrumented binary: writes covdata/e2e (+ .stamp) for `make cover`.
e2e-cover:
	MM_E2E_COVER=1 $(LOOP) e2e -- scripts/e2e.sh

# Unit coverage (race, no -short) into covdata/unit, merged with covdata/e2e when `make e2e-cover` ran
# (refused as stale if sources changed since: covdata/e2e.stamp);
# scripts/coverage-gate.sh enforces coverage.thresholds (C23).
cover:
	$(LOOP) cover -- sh -c 'rm -rf covdata/unit && mkdir -p covdata/unit && TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on -timeout $(TEST_TIMEOUT) -cover ./... -args -test.gocoverdir="$$PWD/covdata/unit" && scripts/coverage-gate-selftest.sh && scripts/coverage-gate.sh covdata/unit $$(test -d covdata/e2e && echo covdata/e2e)'

# Seeds the dev data dir with the synthetic archive (needs the seed-synthetic command, M1-B9).
seed:
	$(LOOP) seed -- env MM_AUTH=local MM_DATA_DIR=./data/dev go run ./cmd/metamusic seed-synthetic --profile dev --force

# Edge routing contract (C24): real Traefik + stub forward-auth in docker, labels from
# deploy/labels.example.yml. Needs docker compose; fails (does not skip) without it.
routing:
	$(LOOP) routing -- env MM_REQUIRE_ROUTING=1 CGO_ENABLED=0 go test -tags routing -count=1 ./deploy/traefik-contract/

# The inner loop: only what changed versus origin/main (changed Go packages + reverse deps, web if web/ changed, the
# Playwright specs mapped from the changed files, the incremental gate). scripts/verify.sh runs itself under
# scripts/loop (target verify), heavy only when its plan runs the whole module or any spec. See scripts/verify.sh.
verify:
	scripts/verify.sh

# Incremental: origin/main..HEAD plus the working tree; a selftest runs only when the script it tests changed.
gate:
	$(LOOP) gate -- scripts/gate.sh incremental

# Complete: every selftest and the full-history scan with the private patterns required (what CI and the pre-push hook
# run). A green run clears HEAD for the pre-push hook, which then does not repeat it (scripts/gate.sh prepush).
# GATE_REQUIRE_PATTERNS=0 make gate-full runs it without the pattern requirement (no stamp is written).
gate-full:
	$(LOOP) gate-full -- env GATE_REQUIRE_PATTERNS=$(or $(GATE_REQUIRE_PATTERNS),1) scripts/gate.sh full

# Worker (Python, C83): unit tests with the 90 % coverage gate, no torch, no network.
worker:
	$(LOOP) worker -- sh -c 'cd worker && uv run pytest --cov=mmworker --cov-fail-under=90'

# Worker contract tests against the real yue2 (CPU torch), same steps as the worker-contract workflow.
contract:
	$(LOOP) contract -- scripts/worker-contract.sh

# PRINT-ONLY: the canary spends real money and is typed by the operator (M3-X3, after explicit approval).
# This target never runs it; it prints the documented command.
canary:
	$(LOOP) canary -- echo 'operator step (M3-X3): metamusic runpod canary --url <PUBLIC_URL> --case main --max-usd 0.25 -- read docs/runbook.md first; never run without approval'

hooks:
	git config core.hooksPath .githooks
	@echo "hooks installed (.githooks)"

tools:
	scripts/tools.sh

# Writes a fixture library (metadata + peaks files) the web lane can serve:
#   go run ./cmd/metamusic dev --data-dir ./data/fixture
fixture-db:
	$(LOOP) fixture-db -- sh -c 'rm -rf data/fixture && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/library/librarytest -run TestWriteFixtureDB -count=1 && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/servertest -run TestWriteFixtureDB_M2b -count=1 && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/servertest -run TestWriteFixtureDB_M3 -count=1 && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/servertest -run TestWriteFixtureDB_M4a -count=1'

dev:
	go run ./cmd/metamusic dev --data-dir ./data/dev

docker:
	$(LOOP) docker -- docker build -t metamusic:dev --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .

metrics:
	scripts/metrics-report.sh

clean:
	rm -f bin/metamusic bin/metamusic.exe bin/metamusic-e2e openapi.json.tmp
	rm -rf covdata coverage web/test-results web/playwright-report
	find web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
