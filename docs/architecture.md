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

## Judging, editing and parity (M2a)

- **Judgments**: ratings, comments (with optional timestamps), listen events and track context are
  registry operations like everything else. Human and agent ratings are stored separately and never
  overwrite each other.
- **Drafts and exports**: drafts are revisioned; `songs.jsonl` and playlist exports are
  reproducible artifacts under `derived/exports/`, never backed up and swept after seven days.
- **Devices**: a phone is paired once with a short code from `/human/devices`; the resulting
  device cookie is scoped to UI paths and revocable. Human-only pages still require the full
  sign-in.
- **Safety rails**: in-memory per-principal rate limits, an append-only audit log (principal,
  transport, operation), idempotency keys on create operations, and a gate that enforces
  scope, step-up and human-only rules for every face.
- **Backup**: a daily (and on-demand) snapshot holds a verified database copy, hardlinked or copied
  media, import mappings and a `songs.jsonl` export, written atomically with a manifest; restore is
  CLI-only and re-verifies every hash. Data and backup directories live on one volume.
- **Notifications**: optional ntfy messages for backup failures, device pairing and token creation.

## Duels and ears (M2b)

- **Blind duels**: two takes are rendered as short, loudness-matched clips (attenuation only) and judged
  A/B in the browser; the web client plays both on one Web Audio clock so toggling is sample-synced.
  Which slot holds which take is hidden until the human verdict exists. Verdicts are append-only; a
  second human-source verdict for a duel is refused.
- **Ladder**: human verdicts on blind duels feed a Bradley-Terry-Davidson ranking (per lineage and
  global, with uncertainty), computed on read. Agent (critic) verdicts are stored but never move it.
- **Clips**: addressed by their inputs and cached under `derived/clips/` (size-capped, not backed up).
  A GET may fill the cache but never writes the database.
- **Ears Tier A**: silence, clipping, token-loop metrics, duration against the plan, explicit blind
  spots, and a spectrogram JPEG of at most 100 000 bytes with sections and lyric lines drawn in. MCP
  clients receive it as an image result. Tier A runs from the CLI after a take is committed.
- **Compare**: `compare_tracks` reports metric, recipe and verdict deltas between two takes.

## Reference tracks and lossy masters (M2c)

- **Reference tracks**: audio that has no recipe can be imported on purpose (`--reference-unmatched`,
  `--reference-lossy` or a mapping row), never by a plain `import`. Such a track is an ordinary track
  linked to a stub recipe with `engine = 'unknown'`; the API, MCP and `/context` show `recipe_known:
  false`, and the library filters on it.
- **Lossy masters**: a standalone MP3 imports byte-for-byte as an MP3 master (`lossy: true`): frozen
  like a FLAC master, no tags, no MP3 rendition, never a lossless link (`get_media_links` returns
  `flac: null`). Only imports can create one; generated tracks stay FLAC.
- **Refusals**: stub recipes are excluded from `songs.jsonl` exports and refused by lint, draft
  creation and as mothers, in Go and in the database (`RECIPE_UNKNOWN`).

## GPU work

Rendering runs on a RunPod Serverless endpoint; the server submits jobs and ingests results.

## Build and delivery

`make check` (format, vet, lint, race tests, web checks), `make e2e` (Playwright on chrome and
firefox against a coverage-instrumented binary) and `make gate` (privacy gate) mirror the CI jobs
`go-fast` and `go-media` (the module split in two parallel jobs), `lint`, `web`, `e2e` and `gate`. The E2E teardown fails the job if a secret-shaped value
appeared in any served response; no HAR files are recorded or uploaded. The release image is a multi-stage Docker build running as
a non-root user.

See [plans/2026-10-12-roadmap.md](plans/2026-10-12-roadmap.md) for milestones.
