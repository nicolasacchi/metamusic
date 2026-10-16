# CLAUDE.md — metamusic

metamusic is a self-hosted, single-user "music lab" around YuE2 (open lyrics→song model):
generate → listen → judge → regenerate, with **strict parity** between the web/mobile UI and
LLM agents (REST + MCP). One Go binary embeds a Svelte 5 SPA. GPU work runs on RunPod Serverless.

The design of record lives outside this public repo. Public-safe summaries: `docs/architecture.md`,
`docs/plans/*.md`. If you are an agent on the build host, the full design is at the absolute path
given in your brief; never copy private content (hostnames, names, balances, lyrics) into the repo.

## Commands

```bash
make verify     # THE ITERATION TARGET: only what changed vs origin/main (changed Go pkgs + reverse deps: gofmt, vet,
                # lint, test -race -short; web checks if web/ changed; Playwright specs mapped from changed files
                # (scripts/e2e-map.txt); incremental gate). Target <3 min. go.mod/.golangci.yml/unplaceable paths = all.
make gate       # incremental privacy gate: origin/main..HEAD + working tree; selftests only if their scripts changed
make gate-full  # complete gate: every selftest + full-history scan (CI and pre-push run this; run it before `git push`,
                # the hook then accepts the green run for the same HEAD/scripts/patterns instead of repeating ~minutes)
make check      # FINAL PASS (or leave to CI): gofmt + vet + golangci-lint + go test -race -short + web lint/check/unit
make test       # FINAL PASS: go test -race -shuffle=on ./...  (no -short: media-tool + acceptance tiers; CI sets MM_REQUIRE_MEDIA_TOOLS=1)
make e2e        # FINAL PASS: build SPA + cover binary, run Playwright (chrome + firefox) in proxy auth mode
make openapi    # regenerate openapi.json and web/src/api/schema.d.ts (commit both)
make dev        # run locally: MM_AUTH=local, data in ./data/dev
make hooks      # install .githooks (pre-commit: staged gate; pre-push: gate-full)
make tools      # download pinned golangci-lint, gitleaks, ffmpeg (LGPL, linux/amd64) into ./bin (sha256-verified);
                # metaflac comes from the system flac package, else is built from the pinned xiph source
```

Iterate with `make verify`; run `make check`/`make test`/`make e2e` once at the end, or leave them to CI (full
sharded verifier: all packages, no -short, every Playwright project, coverage, worker, history gate).

Heavy suites (check, test, race, cover, e2e, and `verify` whenever its plan runs `go test -race` or Playwright) are serialised machine-wide (flock, `MM_HEAVY_SLOTS`
slots, default 1; `MM_HEAVY_LOCK=0` disables) and memory-capped (`MM_HEAVY_MEM`, default 20G, via
`systemd-run --user --scope`) by `scripts/loop`, because concurrent race suites triggered systemd-oomd.
Agents must run whole-repo race suites only through `make`; single-package
`go test -race ./internal/x/` is fine.

Every `make` target runs through `scripts/loop`, which appends a metrics row (no command lines,
paths or secrets) to the shared `metrics/loop.jsonl` (gitignored). This project is also an
experiment in how well Go suits LLM-driven development — do not bypass the wrapper.

## Architecture rules (non-negotiable)

1. **A capability exists iff it is a Huma operation** registered via `api.Register(...)` with an
   `api.Meta` (`x-mm` extension): scope, cost, mcp (`tool` | `generic` | `exclude:<reason>`),
   ui (`/route` | `none:<reason>`), human_only. REST, MCP tools and the SPA client are all derived
   from it. Never add a private endpoint for the SPA; never hand-write an MCP tool that bypasses the
   registry. Binary/streaming routes are the documented exceptions (`mcp: exclude:binary`).
2. **SPA network access only through `web/src/api/`** (generated openapi-fetch client). ESLint bans
   `fetch`/`XMLHttpRequest`/`EventSource`/`sendBeacon` elsewhere.
3. **Human-only ops** live under `/human/api/` and are refused (403) for any non-human principal.
4. **Auth precedence:** an `Authorization` header means bearer-only (Remote-* and cookies ignored).
   `Remote-User` is honoured only on UI-class paths, from a trusted proxy peer, with a matching
   `X-MM-Edge` secret, and for exactly one allow-listed user. `MM_AUTH` unset ⇒ refuse to start.
5. **Errors** are `application/problem+json` bodies `{"code","message","details"}` with stable
   UPPER_SNAKE codes. GET never mutates. No physical deletes via API (status/hidden flags only).
6. **Money** is integer micro-USD (`int64`). Never floats for money.
7. **Masters are frozen**: FLAC audio frames never rewritten; tags written once at ingest; mode 0444.
8. **Generation params are immutable**: recipes table rows never change; every change = new track via job.
9. **Secrets** never appear in responses, logs, job events, audit rows or OpenAPI. Errors from HTTP
   clients pass through `internal/redact` before logging/storing.

## Go conventions

- Go 1.27, module `github.com/nicolasacchi/metamusic`. stdlib first; deps must be justified.
- SQLite: `modernc.org/sqlite`, driver name `"sqlite"`, pragmas via `_pragma=` DSN params
  (`_pragma=busy_timeout(5000)`, not `_busy_timeout`). Writer pool `SetMaxOpenConns(1)` + reader pool.
  Migrations: embedded `internal/store/migrations/NNNN_name.sql`, applied by `PRAGMA user_version`.
- UUIDs: stdlib `uuid` (v7) if available in the toolchain, else `github.com/google/uuid` — check once.
- No generic **methods** (generic functions are fine). No `encoding/json/v2`.
- `-race` needs `CGO_ENABLED=1` (tests only); release builds are `CGO_ENABLED=0`.
- Tests: stdlib `testing` + `github.com/google/go-cmp`; table tests; real SQLite in `t.TempDir()`;
  golden files under `testdata/` with an `-update` flag; `testing/synctest` for time.
- TDD: write the failing test first, see it fail, then implement. Every new op gets a REST test,
  and the generic MCP parity test must keep passing.
- Logging: `log/slog` JSON. Never log request bodies, tokens, cookies, URLs with query strings.

## Web conventions

- Svelte 5 **runes mode only** (`$state`, `$derived`, `$effect`, `$props`); no Svelte 4 syntax
  (`export let`, `$:`), no SvelteKit, no htmx. TypeScript strict.
- Bun is the package manager / script runner / unit test runner; Vite builds; Playwright for E2E.
- The SPA is embedded in the Go binary via `web/embed.go` (`//go:embed all:dist`). `web/dist/.gitkeep`
  keeps `go build` working without Node; the server shows a "UI not built" page in that case.
- Use context7 (MCP) for library docs — several majors (Vite, TS, ESLint) postdate model cutoffs.

## Public repo hygiene

This repository is PUBLIC. Never commit: audio, databases, PDFs, `songs.jsonl`, `varianti/`,
`docs/private/`, `.env*`, `secrets/`, real hostnames of the deployment, personal names or emails.
Use `mm.example.com` in docs/examples. `make gate` enforces this (also in hooks and CI).
Never read `.env` files or anything under `secrets/`.

## Known traps

- YuE2 `truncated` is a dict `{"abc": bool, "semantic": bool}` — check `any(values)`.
- RunPod REST v1 retires 2026-11-15 — use `api.runpod.io/v2` (management) and
  `api.runpod.ai/v2/{endpoint}` (jobs). Cloudflare 1010 blocks Python-urllib's User-Agent.
- iOS ignores `audio.volume`; loudness normalisation must be baked into served files.
- Traefik forwardAuth only strips `Remote-*` on routers that use it — other routers must strip.
