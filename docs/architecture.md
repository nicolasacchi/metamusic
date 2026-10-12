# Architecture (public summary)

metamusic is a single Go binary that serves a REST API, an MCP endpoint and an embedded Svelte 5
SPA, backed by SQLite. This page summarises the structure; examples use `mm.example.com`.

## One registry, three faces

A capability exists if and only if it is a Huma operation registered through `api.Register(...)`
with metadata (`x-mm`): required scope, cost class, MCP exposure (`tool`, `generic` or
`exclude:<reason>`), UI route (or `none:<reason>`) and a human-only flag. From that single
registry the system derives:

- the REST API and `openapi.json` (OpenAPI 3.1),
- MCP tools plus generic `describe_operation` / `call_operation`,
- the generated TypeScript client used by the SPA (the only network path in the SPA).

CI checks REST/MCP parity, a `tools/list` golden file, and that the SPA actually calls every
operation that declares a UI route.

## Authentication

Two modes, selected by `MM_AUTH` (unset means refuse to start):

- `local`: built-in password login with session cookies (argon2id).
- `proxy`: a reverse proxy performs SSO and sets `Remote-User`.

Rules: an `Authorization` header means bearer-only. `Remote-User` is honoured only on UI-class
paths, from a trusted proxy peer, with a matching edge secret header, for exactly one allow-listed
user. Agents use scoped personal access tokens (stored hashed). Human-only operations live under
`/human/api/` and are refused for any non-human principal.

## Data and invariants

- SQLite (WAL), one writer connection plus a reader pool; embedded, versioned migrations.
- Errors are `application/problem+json` with stable UPPER_SNAKE codes. GET never mutates; there
  are no physical deletes through the API.
- Money is integer micro-USD, never floating point.
- Master audio is frozen: FLAC frames are never rewritten, tags are written once at ingest, files
  are read-only. Generation parameters are immutable; every change is a new track via a job.
- Secrets never appear in responses, logs, job events, audit rows or OpenAPI; errors from HTTP
  clients pass through a redaction layer.

## Library, import and listening (M1)

- **Library**: tracks, recipes (immutable generation parameters) and masters in SQLite; masters
  are FLAC files addressed by content hash, tagged once at ingest and then read-only.
- **Import**: `metamusic import --dry-run` inventories a read-only inbox and writes a report
  (nothing else is created); `--plan REPORT` applies exactly what the report showed and refuses if
  the inbox changed. Import runs are recorded, re-runs are idempotent, and one-shot and incremental
  imports converge to the same library.
- **Media tools**: ffmpeg, flac and metaflac are external binaries (`MM_FFMPEG`, `MM_FLAC`,
  `MM_METAFLAC`), invoked with fixed argument vectors; the container image verifies at build time
  that ffmpeg has the loudness filter and MP3 encoder and that metaflac runs.
- **Serving**: media is delivered through short-lived signed URLs with Range support; loudness
  normalisation is baked into served derivatives because iOS ignores `audio.volume`.
- **Player**: a plain `<audio>` element player with waveform seeking and lock-screen controls,
  using the same registry operations as agents.
- **Edge**: the reverse-proxy routing contract (media, agent, bearer-API and UI routers, identity
  header stripping) is documented in `deploy/labels.example.yml` and tested by `make routing`.

## GPU work

Rendering runs on a RunPod Serverless endpoint; the server submits jobs and ingests results.

## Build and delivery

`make check` (format, vet, lint, race tests, web checks), `make e2e` (Playwright on chrome and
firefox against a coverage-instrumented binary) and `make gate` (privacy gate) mirror the CI jobs
`go`, `lint`, `web`, `e2e` and `gate`. The release image is a multi-stage Docker build running as
a non-root user.

See [plans/2026-10-12-roadmap.md](plans/2026-10-12-roadmap.md) for milestones.
