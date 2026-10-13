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
TEST_TMPDIR ?= $(shell [ -d /dev/shm ] && [ -w /dev/shm ] && echo /dev/shm || echo $${TMPDIR:-/tmp})

.PHONY: build web openapi test race lint check _check e2e gate hooks tools dev docker metrics clean fixture-db cover seed routing

build:
	$(LOOP) build -- sh -c 'mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/metamusic ./cmd/metamusic'

web:
	$(LOOP) web -- sh -c 'cd web && bun install --frozen-lockfile && bun run build'

openapi:
	$(LOOP) openapi -- sh -c 'go run ./cmd/metamusic openapi > openapi.json.tmp && mv openapi.json.tmp openapi.json && cd web && bun run gen:api'

test:
	$(LOOP) test -- env TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on ./...

race:
	$(LOOP) race -- env TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on ./...

lint:
	$(LOOP) lint -- $(GOLINT) run ./...

check:
	$(LOOP) check -- $(MAKE) --no-print-directory _check

# Fast-loop only: the transpiled SQLite stack (modernc.org/*: sqlite, libc, memory) is compiled without
# race/checkptr instrumentation. It is generated C-to-Go code with no project logic, and instrumenting it
# cost about two thirds of suite CPU. Everything else (all of our code, database/sql, net/http) stays
# under -race, and `make test` / `make cover` / CI keep full instrumentation everywhere.
CHECK_GCFLAGS = -gcflags=modernc.org/...=-race=false
# Also fast-loop only: GORACE=atexit_sleep_ms=0 drops the race runtime's 1 s sleep at the exit of each of the ~45
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
	{ go vet ./... && $(GOLINT) run ./... && TMPDIR=$(TEST_TMPDIR) GORACE=atexit_sleep_ms=0 GOGC=400 CGO_ENABLED=1 go test -race $(CHECK_GCFLAGS) -short -shuffle=on ./...; } || rc=1; \
	wait $$wp || rc=1; cat "$$log"; exit $$rc

e2e:
	$(LOOP) e2e -- scripts/e2e.sh

# Unit coverage (race, no -short) into covdata/unit, merged with covdata/e2e when `make e2e` ran
# (refused as stale if sources changed since: covdata/e2e.stamp);
# scripts/coverage-gate.sh enforces coverage.thresholds (C23).
cover:
	$(LOOP) cover -- sh -c 'rm -rf covdata/unit && mkdir -p covdata/unit && TMPDIR=$(TEST_TMPDIR) CGO_ENABLED=1 go test -race -shuffle=on -cover ./... -args -test.gocoverdir="$$PWD/covdata/unit" && scripts/coverage-gate-selftest.sh && scripts/coverage-gate.sh covdata/unit $$(test -d covdata/e2e && echo covdata/e2e)'

# Seeds the dev data dir with the synthetic archive (needs the seed-synthetic command, M1-B9).
seed:
	$(LOOP) seed -- env MM_AUTH=local MM_DATA_DIR=./data/dev go run ./cmd/metamusic seed-synthetic --profile dev --force

# Edge routing contract (C24): real Traefik + stub forward-auth in docker, labels from
# deploy/labels.example.yml. Needs docker compose; fails (does not skip) without it.
routing:
	$(LOOP) routing -- env MM_REQUIRE_ROUTING=1 CGO_ENABLED=0 go test -tags routing -count=1 ./deploy/traefik-contract/

gate:
	$(LOOP) gate -- sh -c 'scripts/public-gate-selftest.sh && scripts/loop-selftest.sh && scripts/coverage-gate-selftest.sh && scripts/public-gate.sh'

hooks:
	git config core.hooksPath .githooks
	@echo "hooks installed (.githooks)"

tools:
	scripts/tools.sh

# Writes a fixture library (metadata + peaks files) the web lane can serve:
#   go run ./cmd/metamusic dev --data-dir ./data/fixture
fixture-db:
	$(LOOP) fixture-db -- sh -c 'rm -rf data/fixture && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/library/librarytest -run TestWriteFixtureDB -count=1 && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/servertest -run TestWriteFixtureDB_M2b -count=1 && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/servertest -run TestWriteFixtureDB_M3 -count=1'

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
