SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := check

LOOP   := scripts/loop
GOLINT = $(or $(wildcard bin/golangci-lint),golangci-lint)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build web openapi test race lint check _check e2e gate hooks tools dev docker metrics clean fixture-db cover seed

build:
	$(LOOP) build -- sh -c 'mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/metamusic ./cmd/metamusic'

web:
	$(LOOP) web -- sh -c 'cd web && bun install --frozen-lockfile && bun run build'

openapi:
	$(LOOP) openapi -- sh -c 'go run ./cmd/metamusic openapi > openapi.json.tmp && mv openapi.json.tmp openapi.json && cd web && bun run gen:api'

test:
	$(LOOP) test -- env CGO_ENABLED=1 go test -race -shuffle=on ./...

race:
	$(LOOP) race -- env CGO_ENABLED=1 go test -race -shuffle=on ./...

lint:
	$(LOOP) lint -- $(GOLINT) run ./...

check:
	$(LOOP) check -- $(MAKE) --no-print-directory _check

# Internal: steps of `check` (run inside one metrics row). -short skips the heavy media-tool/acceptance
# tiers (testtools.Require, TestAcceptance_*); `make test` and CI run them.
_check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	$(GOLINT) run ./...
	CGO_ENABLED=1 go test -race -short -shuffle=on ./...
	@if [ -d web/node_modules ]; then cd web && bun run lint && bun run check && bun test; else echo "web/node_modules missing: skipping web checks"; fi

e2e:
	$(LOOP) e2e -- scripts/e2e.sh

# Unit coverage (race, no -short) into covdata/unit, merged with covdata/e2e when `make e2e` ran;
# scripts/coverage-gate.sh enforces coverage.thresholds (C23).
cover:
	$(LOOP) cover -- sh -c 'rm -rf covdata/unit && mkdir -p covdata/unit && CGO_ENABLED=1 go test -race -shuffle=on -cover ./... -args -test.gocoverdir="$$PWD/covdata/unit" && scripts/coverage-gate-selftest.sh && scripts/coverage-gate.sh covdata/unit $$(test -d covdata/e2e && echo covdata/e2e)'

# Seeds the dev data dir with the synthetic archive (needs the seed-synthetic command, M1-B9).
seed:
	$(LOOP) seed -- env MM_AUTH=local MM_DATA_DIR=./data/dev go run ./cmd/metamusic seed-synthetic --profile dev --force

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
	$(LOOP) fixture-db -- sh -c 'rm -rf data/fixture && MM_FIXTURE_DB="$$PWD/data/fixture/metamusic.db" go test ./internal/library/librarytest -run TestWriteFixtureDB -count=1'

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
