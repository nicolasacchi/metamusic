# M2b — Duels, Ladder, Ears Tier A — Implementation Plan (DRAFT, revision 3)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, `docs/plans/2026-10-12-m0-skeleton.md` (contracts C1–C9 incl. the
> C3/C4/C6 addenda), the M1 plan (contracts C10–C24) and the M2a plan (contracts C25–C42,
> D-M2a-1…) first. M2b builds on those exact contracts and continues the numbering: M2b contracts are
> **C43–C56**. Where M2b changes an earlier contract, the task that makes the change records it in that
> plan document in the same commit (named in each task's **Files**). Amended contracts:
> - **M0:** C4 `api.Meta` (`mcp_image`, added to M2a's C28 form).
> - **M1:** C12 slug fold table (moved to the leaf `internal/textfold`), C14 analyzer (capability methods
>   `RequireClips`/`RequireTierA`; `ffmpeg.New` keeps its signature and requirements but retains the
>   `-filters`/`-encoders` listings it already reads), C16.7 orphan scan (`derived/clips/`),
>   C17 `Resolve` (`jpg`), C21 synthetic profiles. (Rev 3: C22.1 needs no change, the player store already
>   has `pause()`.)
> - **M2a:** **C27** (the one hard-coded schema-version assertion becomes 4), **C28** `Meta`/`Deps` (M2b ops, `mcp_image`,
>   concrete M2b services), **C33** media handler (ext `jpg`; the clips resolver appended to the
>   `serve.Chain`), **C35** `/context` (`judgments.ladder`, `lineage.children[].ladder`,
>   `_links.spectrogram`, `perception` from Ears, one blind-spots source), **C39** `httpserver.Options`
>   and `internal/app.Build` (duels, ears, clips), **C40** web (routes in `lib/router.ts`, nav in
>   `App.svelte`, Track page Ears panel and lineage duels), **C41** (`tools/list` ceiling, D-M2b-22,
>   Playwright project lists), **C42** test harness (`servertest.Options` M2b fields, parity cases for every M2b op, M2b rows
>   in the registry-derived acceptance tests).
>
> **Public/private split.** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` is public-safe (no real hostnames, titles, artists, lyrics, archive counts
> or personal data; `mm.example.com` and synthetic examples only). Only that part is copied to
> `docs/plans/<date>-m2b-duels-ears.md` (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`),
> and `scripts/public-gate.sh --staged` runs on the copy (staged) before it is committed (rev 3: `make gate`
> checks tracked files only).
>
> **Task ids** carry the `M2b-` prefix. Lane labels (`MM_LANE`): `m2b-found`, `m2b-judge`, `m2b-media`,
> `m2b-ears`, `m2b-web`, `m2b-infra`, `m2b-int`.
>
> **Revision 2** applies the review of revision 1: M2a is a hard dependency (no conditional branches
> remain); contracts renumbered C43–C56; migration `0004_duels.sql`; M2a's principal model and a
> schema-level source isolation for verdicts; human-source semantics across human and device principals;
> M2a gate integration (idempotent `create_duel`, heavy clip ops, `NewID`, `NoteCreated`); M2a's error,
> stub and `Deps` conventions; M2a's parity runner and media chain; the clip ↔ duels contract gaps; split
> ffmpeg capability checks; `*bool` body defaults; real nullable objects in OpenAPI; the blindness
> scope; `/context` wiring as a binding deliverable; a fixed `TestGETNeverWritesDB_M2b`; the
> `compare_tracks` relation rule; the orphan-scan code change; a re-measured spectrogram budget; CI time;
> isolated e2e duel specs; web-lane fixtures; scope trims (no duel-level note, attenuate-only
> `get_clip`); file ownership; the earlier tests that change; the privacy-gate globs.
>
> **Revision 3** reconciles the plan with the merged M0 + M1 + M2a code (branch `m2b` starts at `main`
> after the M2a merge). Every symbol, file, table, column, migration number, contract number, op id,
> Makefile target, script, test helper, CI job, config field and contract this plan references or amends
> was checked against the code; the corrections are indexed in "Baseline reconciliation" below and applied
> in place, each marked "(rev 3)". Numbering was checked as well: M2a ends at C42 / D-M2a-22 and M2b uses
> C43–C56 / D-M2b-1…22, so nothing collides with M0 (C1–C9), M1 (C10–C24, D-M1-*) or M2a (C25–C42,
> D-M2a-*).

**Goal:** the human judges takes by ear in loudness-matched, sample-synced **blind duels** (A/B on
desktop keys or a big mobile toggle, 8 ms crossfade), and every human blind verdict — from the operator's
browser or a paired device — feeds a personal **Bradley-Terry-Davidson ladder** (per lineage and global,
with uncertainty). Agents (PAT over REST or MCP, and `mcp-stdio`) can create duels, fetch clips and record
*critic* verdicts that are stored but never move the ladder. Every take gets an **Ears Tier A dossier** —
silence spans, clipping, token-loop metrics from `semantic.npy`, duration vs the score plan, explicit
`blind_spots` — and a **spectrogram JPEG ≤ 100 000 bytes** with section boundaries and lyric lines drawn
in, which MCP clients receive as `ImageContent`. `compare_tracks` gives an agent the metric, recipe and
verdict deltas between two takes, and M2a's `/context` bundle gains the ladder and the Ears perception.
Everything is one registry: REST, MCP and the SPA, behind M2a's gate.

**Architecture:** new packages, each behind a small interface so lanes build in parallel:
- `internal/media/clip` — window rules, input-addressed clip key, loudness-match gains (attenuate only),
  ffmpeg cut of a hard-linked master into a 48 kHz/24-bit FLAC clip (rendered detached from the calling
  request), a bounded cache under `derived/clips/` whose open-duel keys are pinned, and a
  `serve.Resolver` for `/media/<key>.flac` appended to M2a's media `serve.Chain`.
- `internal/ladder` — pure math: BTD fit by MM iterations with a Gaussian prior on log-strengths, a
  Gamma prior on the tie parameter, a fixed reject anchor, and Laplace standard deviations.
- `internal/duels` — duels and verdicts (migration `0004_duels.sql`), blind slot randomisation, reveal
  rules, the per-source queue (`next_duel`), verdict submission (the human source is every
  `ActsAsHuman` principal), ladder assembly (human blind verdicts only), a fit cache, and the open-duel
  clip keys the clip cache pins.
- `internal/analysis/{npy,tokens,spectro}` + new files in `internal/analysis` (Tier A result types, the
  one blind-spots list) and `internal/analysis/ffmpeg` (capability checks, the Tier A analyzers: one
  ffmpeg pass for `astats` + `silencedetect`; `showspectrumpic`; Go `.npy` reader, token loop metrics,
  overlay and JPEG budget).
- `internal/textfold` — leaf: the NFKD + fold-table text folding shared by M1's `yue2.Slug` and the
  spectrogram overlay.
- `internal/ears` — runs Tier A per track (post-ingest, CLI `metamusic analyze`), writes `analyses` and
  a `spectrogram` rendition (M1 tables, no schema change), builds the dossier, the spectrogram view,
  `compare_tracks` and the `/context` perception block.
- M2a's `internal/trackctx` fills `judgments.ladder`, `lineage.children[].ladder`, `_links.spectrogram`
  and `perception` from duels and Ears (M2b-B3).
- `internal/mcpgen` learns one thing: operations marked `x-mm.mcp_image` return `ImageContent`.
- Every M2b operation passes M2a's gate **as built** (audit recorder → rate limit → authorize →
  idempotency, C29; rev 3: the code charges the rate limiter before `Authorize`): `create_duel` is an
  idempotent create op and, like `get_clip`, uses the heavy rate bucket.
- The SPA gains `/duels`, `/duels/:id`, `/ladder`, `/compare` and an Ears panel + lineage duels on the
  track page. Clip bytes are fetched inside `web/src/api/` and played through Web Audio (never through
  THE `<audio>` element, which is paused while a duel plays).

**Tech stack:** one new Go module dependency, `golang.org/x/image` (`font/basicfont` + `font` drawer for
the 7×13 bitmap labels, D-M2b-9). Everything else is stdlib Go (`image/png`, `image/jpeg`,
`compress/gzip`, `math`, `math/rand/v2` for tests) plus the ffmpeg already in the runtime image (filters
`astats`, `silencedetect`, `showspectrumpic`, `atrim`, `asetpts`, `afade`, `asetnsamples`, `aformat`,
`aresample`, `volume`, `ebur128`, `ametadata`; encoders `flac`, `png`). `ffmpeg.New` keeps M1's
requirements; the M2b filters are checked by `RequireClips()` and `RequireTierA()` (C51.7). The clip
loudness window uses `ebur128=peak=true+sample` like M1's `Loudness` so `ffmpeg.ParseLoudness` is reused. Tests
validate MCP structured content with `github.com/google/jsonschema-go` (a direct dependency since M2a,
already used by the `internal/mcpgen` tests; test-only use). Web: no new dependencies (Web Audio API is a browser API). Pin versions at
implementation time; use context7 for Svelte/Vite/Playwright/Web Audio questions.

**Out of scope (YAGNI, with owner):** section-locked clips and active pair selection (M5); Tier B
(Demucs/Whisper/SheetSage, M5) and Tier C (opt-in machine critique, later); MCP resources and prompts
(`metamusic://…/spectrogram`, M4a); a taste model / `get_taste_profile` (later); analysis as an API job
(`analyze` job kind, M3 — M2b analysis runs from the CLI and after import); duel cancellation or
re-judging (expiry and new duels instead); a duel-level creator note (D-M2b-19; verdict notes stay);
persisted ladder history; WAV clip fallback (only if DC-M2b-2 fails); a clip-cache prune CLI
(size-capped eviction suffices); loudness boosts of any clip (D-M2b-2); opaque per-duel clip URLs
(D-M2b-4); Tier A for drafts (no audio).

---

## Dependencies and sequencing

- **Hard:** M0, M1 and M2a contracts as written **and as merged** (C1–C42 incl. addenda and the M2a
  decisions D-M2a-1…22). Where this plan and the merged code disagreed, the plan was fixed (see "Baseline
  reconciliation"). M2b starts from `main` **with M2a merged**; branch `m2b`.
- **What M2b uses from M2a** (binding; nothing below is conditional):
  - **Principals (C26):** `Principal.Key()` is stored in every M2b principal column
    (`duels.created_by`, `duel_verdicts.principal_id`, the critic unique index); `ActsAsHuman()` (human
    and paired device) selects the human verdict source; `KindSystem` and anonymous principals never
    judge. M2b responses carry principal *kinds* only, never keys or names, so D-M2a-19 masking needs no
    M2b rendering code (a future principal reference goes through `api.RefFor`).
  - **Schema (C27):** M2a ships `0003_judge.sql`; M2b ships `0004_duels.sql` and `PRAGMA user_version ==
    4`. Rev 3: the only test that hard-codes version 3 is `internal/httpserver/server_test.go`
    (`get_system_info.schema_version`); M2b-B1 updates it. M2a's `TestMigration0003_*` and the backup tests
    assert `>= 3` or compare with the live version and need no change. Duel verdicts adopt D-M2a-12's
    schema-level source isolation.
  - **Registry and gate (C28, C29, C30):** M2b ops declare `Idempotency`/`Rate` in `api.Meta`;
    `create_duel` embeds `api.IdemHeader`; handlers call `audit.NoteCreated` for every row they create;
    ids come from `Deps.NewID` (read through `Deps.NewIDFunc()`). MCP derives idempotency keys (C30);
    `mcp-stdio` agents are critics. The gate as built (`internal/gate/gate.go`): audit recorder (deferred,
    outermost) → rate limit → body read → authorize → idempotency → Huma; a call is audited when the
    caller is an agent or the op is non-read/human-only. **Errors (rev 3):** M2a has no shared
    `fromService` and no 501 path any more (its last stub was removed; `codeFor` returns `INTERNAL` for
    every status ≥ 500, so a 501 needs an explicit `api.Err`). Services map errors per domain (`judgErr`,
    `backupErr`, `libErr`, …); M2b adds `m2bErr` in `internal/api/errors_m2b.go` in the same style (C53).
    A nil service answers 503 `UNAVAILABLE` `{component}` through the existing `unavailable(component)`;
    5xx details reach logs through `redact.Error` in the Huma error hook (`newProblem`,
    `internal/api/errors.go`). Stage-1 stubs answer 501 `NOT_IMPLEMENTED` via `api.Err(501,
    "NOT_IMPLEMENTED", …)`, which M2b-B2 re-introduces for stubs only.
  - **Media (C33):** M2a's `serve.Chain` (`internal/media/serve/chain.go`, nil resolvers skipped; today
    `serve.Chain(lib, exp)` in `app.Build`) and media extensions `{flac, mp3, jsonl, zip}`; M2b adds `jpg`
    and appends the clips resolver (which, like `exports.Resolve`, checks its file and answers
    `serve.ErrNotFound` when it is gone, so an evicted clip is a 404, not M1's 500). Link lifetimes come
    from `serve.TTLFor`: `ActsAsHuman()` principals (human **and device**) 12 h in 1 h buckets, agents
    15 min in 1 min buckets (rev 3: devices are *not* short-lived).
  - **`/context` (C35):** M2a ships the bundle with `judgments.ladder` and `children[].ladder` set to
    `null`, `perception.tier "0"` and a five-entry `blindSpots` package variable in
    `internal/trackctx/trackctx.go`; M2b-B3 fills them (C51.8) and owns the resulting golden and budget
    updates.
  - **Wiring (C39):** `internal/app.Build(ctx, cfg, sec, st, data, app.Options)` returns `*app.Services`
    (`Deps`, `Server`, `Tokens`, `Local`, `Devices`, `Backups`, `Exports`, `Idem`, `Audit`, `Limiter`);
    `httpserver.Options` repeats the service fields and `httpserver.New` builds `api.Deps` from them. M2b
    adds duels, ears and clips to `Services`, `api.Deps` and `httpserver.Options`. Rev 3: `Build` has no
    ffmpeg access and the `import`/`seed-synthetic` commands do not call it (they use `newRuntime` in
    `cmd/metamusic/cmd_import.go`), so media services come from a shared `app.NewMedia` (C51.7).
  - **Web (C40):** `newIdempotencyKey()` / `idempotentAction()` (`web/src/api/idem.ts`), `RateLimited`
    (`web/src/api/client.ts`, thrown for 429 with `retryAfterS`), `jumpTo(trackId, t)`
    (`web/src/lib/jump.ts`), routes in `APP_ROUTES` (`web/src/lib/router.ts`), nav links in
    `web/src/App.svelte`, the Track page (M2a judging surfaces stay; M2b adds panels), and a player store
    that already has `pause()` (user pause that clears playback intent).
  - **Harness (C42):** `internal/servertest` (`Options{Mode, Fixture, Archive, Files, Clock, IDSeed,
    Limits, Canary}`; `Rig{URL, DataDir, BackupDir, Store, API, Clock, Services, Ntfy}` with clients
    `Human`, `Device`, `DeviceWithCookie`, `Agent`, `MCP`, `Stdio`, `Bearer`), the parity runner in
    `internal/mcpgen/parity_test.go` (`ParityCase`, `parityCases`, `stubCases`, `TestParityEveryOperation`,
    `TestParity_Stdio`; `runParity` builds three identically seeded rigs from one `servertest.Options`),
    `librarytest.Fixture(tb, st, now)` / `Fixtures` / `FixedNow` / `WriteFiles`, `archivetest.SyntheticArchive`
    (templates imported once per binary, no Tier A), the test clock and deterministic ids; M2a's E2E leak
    scanner (C39) already skips `/media/` bodies.

## Baseline reconciliation (revision 3: merged M0 + M1 + M2a code)

Checked against `main` after the M2a merge. Where the plan and the code disagreed, the plan was fixed
(not the code); each fix is marked "(rev 3)" at its place.

**Verified as the plan assumed.** Migration numbering (`0001`–`0003`, `loadMigrations` requires gap-free
numbers, so `0004_duels.sql` is next); tables `analyses` (PK `track_id, analyzer, version`, immutable,
free-text `analyzer`/`tier`), `renditions` (PK `track_id, kind`, UPDATE allowed, no delete),
`artifacts` (names include `semantic.npy`), `sections` (source `lyrics|plan|measured|manual`), `mutations`
(one row per child, operator `inferred_sibling`); `auth.Principal{Kind, ID}` with `Key()`,
`ActsAsHuman()`, `auth.CLI` (key `system:cli`), kinds `human|agent|device|system|anonymous`;
`audit.NoteCreated`, `api.IdemHeader`, `api.Meta` fields `Idempotency`/`Rate`/`SecretFields`,
`api.Register`/`Operations`, `withBody`/`created`, `Deps.NewID`/`NewIDFunc`, `unavailable(component)`,
`RefFor`; rate classes `agent_heavy` {6/min, burst 6} and `human_heavy` {12/min, burst 6}; MCP `explode`
arrays; `serve.Resolver`, `serve.Object`, `serve.ErrNotFound`, `Signer.Sign(sha, ext, ttl, bucket)`,
`exports.Resolve` answering `ErrNotFound` for a missing file, the media path regex
`(flac|mp3|jsonl|zip)`; `flacmeta.Read(r, size)` with `Info.StreamInfo`, `flactest.WriteFile`,
`mediatool.Tools`, `analysis.ErrToolMissing`, `ffmpeg.ParseLoudness`, `store.OpenReadOnly`,
`testtools.Require` (skips under `-short`); `TestNoReplaceSQL`, `TestNoDeleteSQL` (allowlist
`idempotency`, `human_sessions`), `TestNoTestHelpersInBinary` (one `go list -deps` call);
`importer.sweepWorkDirs` only touches `ingest-*` dirs older than 24 h (so `clip-*`/`ears-*` dirs need
their own sweep); `make check` runs `-short` with the modernc race exemption; Makefile targets `check
test e2e gate cover seed routing fixture-db openapi dev`; CI jobs `go lint web e2e coverage`; `scripts/loop`
lane names `m2b-*` are valid; `coverage.thresholds` format; Playwright projects `chrome chrome-pixel7
chrome-iphone15 firefox`, `fullyParallel`; `web/src/api/idem.ts`, `jump.ts`, `client.ts` `RateLimited`;
the ESLint network ban is off under `web/src/api/**`; synthetic totals 19 recipes / 6 tracks / 19 drafts
(13 backlog) / 1 run; Huma v2.39.1 refuses `nullable:"true"` on an object `$ref` field (re-verified) and
`setDefaults` overwrites zero values.

**Corrections applied.**

| Area | Plan revision 2 said | Merged code has | Fix (where) |
|---|---|---|---|
| Gate order | audit → authorize → rate limit → idempotency | `internal/gate/gate.go`: audit recorder → rate limit → body read → authorize → idempotency; audited = agent callers and non-read/human-only ops | Architecture, Dependencies |
| Link TTL of devices | devices and agents get 15 min / 1 min | `serve.TTLFor`: `ActsAsHuman()` (human **and** device) 12 h / 1 h, agents 15 min / 1 min | C45, C46, M2b-M1 tests, acceptance map |
| Service error mapping | one `api.fromService` in `errors_service.go`, logging through `redact` (C25) | per-domain mappers (`judgErr`, `backupErr`, `libErr`, …); redaction in the Huma error hook `newProblem`; no 501 path left | C53 (`m2bErr`, `errors_m2b.go`), M2b-B2 |
| Optional numeric query parameter | non-generic `api.OptFloat` in `params.go` | `optParam[T]` with `.ptr()` in `internal/api/types_m1.go` | C53, M2b-B2 |
| Nullable objects | `…OrNull` types with a `_ struct{} nullable:"true"` marker | the repo convention: `huma.SchemaProvider` `Schema()` on a method-less shadow type via `nullableObject[T]` (`internal/library/schema.go`, `judgments`, `backup`) | C53 |
| `internal/problem` | already holds the shared service `Error` | leaf with `Body` and `Write` only; services own their error types | C45, M2b-B1 |
| Schema-version tests | `TestMigration0003_Applies` and the backup manifest tests assert `== len(migrations)` | they assert `>= 3` / the live version; only `internal/httpserver/server_test.go` hard-codes 3 | Dependencies, C44, M2b-B1 |
| Upgrade-test helper | `storetest.OpenAtVersion` | `store.OpenRaw` + `store.MigrateTo` (`internal/store/export_test.go`); tests named `TestMigration0003_*`, files `schema_m2a_test.go` / `triggers_m2a_test.go` / `upgrade_m2a_test.go` | C44, M2b-B1 (`TestMigration0004_*`, `*_m2b_test.go`) |
| `ffmpeg.New` | "keeps the filter and encoder lists it already parses" | it runs `-filters`/`-encoders`, matches `ebur128`/`libmp3lame` and discards the output | C51.7, M2b-B2 (retain the listings) |
| Window loudness argv | `ebur128=peak=true` | `ParseLoudness` needs `sample_peak` and the `LRA*` keys, i.e. `peak=true+sample` | C46 |
| Player `pause()` | M1 C22.1 has none, M2b-W1 adds it | `Player.pause()` exists (clears playback intent) | C54.1, M2b-W1 |
| Routes and nav | `web/src/App.svelte` registers routes | `APP_ROUTES` in `web/src/lib/router.ts` (+ `router.test.ts`); nav links and route components in `App.svelte` | C54, M2b-W2 |
| 429 handling in the SPA | ad hoc | `RateLimited` / `retryAfterS` in `web/src/api/client.ts` | C54.4 |
| mcpgen files | `image.go`, `dispatch.go`, `generic.go`, `stdio.go` | `dispatch.go` (`serve`), `generic.go`, `schema.go` (`toolSpec`), `server.go`; stdio is `cmd_mcpstdio.go` + `mcpgen.NewStdioServer` (same `dispatch`) | C52, M2b-B2 |
| Output-schema test | new `TestMCP_StructuredContentMatchesOutputSchema` | `TestToolResultsConformToOutputSchema` (`outputschema_test.go`, `jsonschema-go`) covers M1 tools; M2b adds `TestToolResultsConformToOutputSchema_M2b` | C52, acceptance map |
| Parity runner names | `TestParity_EveryOperationHasACase` | `TestParityEveryOperation`, `TestParity_Stdio`, `stubCases`; `runParity` takes one `servertest.Options` for its three rigs | C52, C55, M2b-B2 |
| `tools/list` budget | not considered | `TestToolsList_Ceiling`: 40 000 compact bytes, **38 676 used by 21 tools**; golden 85 917 bytes with a 1.5× budget | D-M2b-22, M2b-B2 |
| Audit coverage test | `TestAudit_CoverageMatrix` is registry-derived | it drives synthetic ops `t_read`, `t_write`, … in `internal/gate/gate_test.go` | new `TestAcceptance_M2b_GateCoverage` (M2b-B3) |
| Nil-deps test | `TestOps_NilDepsUnavailable` extended | hand-written list (library, signer, probe) | new `TestOps_M2bNilDepsUnavailable` (M2b-B2) |
| Registry-derived UI test | scripted calls "accept 501" | `uiScript`/`uiOrder` in `internal/acceptance/every_ui_action_test.go`; `callTool` fails on any error and the audit trail must be all 2xx; runs on the `acceptance` archive without Tier A | M2b-B2, M2b-B3 (`servertest` `Backfill`) |
| Rig shape | `Rig` gains `Duels`, `Ears`, `Clips` | `Rig.Services *app.Services` carries every service | C55 |
| `app.Build` and media tools | builds ffmpeg-backed services | no analyzer/tools; CLI import/seed use `newRuntime` | C51.7 (`app.NewMedia`, `Options.Media`) |
| CLI shape | `run(args, env)`, `cmd_dev.go` | `cmdX(ctx, rest, getenv, s streams)` dispatched from `run()` in `main.go` (which also holds the `usage` text); `cmdDev` lives in `cmd_serve.go`; CLI audit rows follow `backup/restore.go` | C51.7, M2b-B2, M2b-B3 |
| Section lines | lyric lines of the matching lyrics section, only when aligned | `sections.lines_json` is written on `lyrics` **and** `plan` rows; selection is `sectionRank` in `library.trackExtras` | C45 |
| Library file names | `resolve.go` | `Service.Resolve` is in `tracks.go` | C45, M2b-B1 |
| Orphan scan | importer C16.7 | `Importer.libraryOrphans` in `internal/importer/report.go`; skips `derived/probe` only (so M2a's `derived/exports/` files are reported as orphans too) | M2b-B1 |
| `/context` internals | `trackctx.WithM2b`; `TestFit_WorstCase` re-run with ladders | setter style `UseScoreABC`; `trackctxtest.WorstCase` seeds 50 children with `ladder: null`; key order lives in the `order` map | C51.8 (`UseM2b`, new worst-case test) |
| `make fixture-db` | extended with duels and Tier A rows | runs `librarytest` `TestWriteFixtureDB` (metadata only; `librarytest` cannot import `duelstest`) | C55, M2b-B2 |
| Shared archive | `librarytest.SyntheticArchive` | `archivetest.SyntheticArchive(tb, "acceptance"\|"e2e")` | M2b-B3 |
| Synthetic `semantic.npy` | not mentioned | the `acceptance` archive writes the text `npy-semantic` | C55 |
| e2e seed library | counts 6 → 9 | also `ownTracks` (`web/e2e/helpers.ts`) slices the *sorted* titles, so "Syn ears …" would displace the seed pairs; `TITLES` in `m1-library.spec.ts`; `m1-import`/`m1-pwa` counts | M2b-B3 |
| Playwright budget | none | `ignoreM2a` project lists in `web/playwright.config.ts` | M2b-W3 |
| `tools-selftest.sh` | asserts ffmpeg capabilities | URL and tamper checks only | M2b-I1 |
| Release tag | `v0.<next>.0-m2b` after the latest milestone tag | the repository has no tags | M2b-X2 |
| Config literals | `ClipCacheMB` always set | `servertest` builds `config.Config` literals, so it is 0 | C43 |

---

## Decisions recorded by this plan

These refine the design of record; M2b-X1 copies them into the private design document.

| Id | Decision | Why |
|---|---|---|
| D-M2b-1 | Clips are **input-addressed**: `key = sha256(canonical {def, master sha, start/end sample, out rate, channels, gain, fade, tool version})`, file `derived/clips/<aa>/<key>.flac`, served at `/media/<key>.flac`. A GET (`get_clip`, `next_duel`, `get_duel`) may **fill this file cache** but never writes the database. Renders run detached from the calling request (`context.WithoutCancel` + timeout) so one cancelled caller never fails others waiting on the same key. The cache is size-capped (`MM_CLIP_CACHE_MB`), keys of open duels are pinned, and it is excluded from `library_orphans` (M2b-B1 changes the scan) and from backups (M2a backs up only files referenced by `renditions`, D-M2a-5). Refines design §3.5 (`derived/<aa>/<sha>.clip.flac`) and §5.5. | A content sha is only known after rendering, but links must be mintable before; the inputs fully determine the bytes for a given ffmpeg build because masters are frozen. A cache fill is not a state change (RFC 9110 safe-method semantics); keeping it out of the DB keeps "GET never mutates" true for everything an API client can observe except latency. A separate directory makes eviction and the orphan exclusion trivial. |
| D-M2b-2 | Clip loudness match **attenuates only**, for duels and for standalone `get_clip?match_lufs=`: gains ≤ 0 dB, at most −24 dB; a duel's target is the quieter window's integrated loudness; a window at or below −60 LUFS disables matching (`window_silent`); a `get_clip` window already quieter than the requested target is returned at gain 0 with `match: "limited"`. | Boosting a quiet clip risks inter-sample clipping that would itself become a judged artifact; gating at −60 LUFS avoids burying a real take to match a near-silent window. One rule for every clip keeps the renderer simple (no true-peak-capped boost path). |
| D-M2b-3 | Verdicts live in an append-only `duel_verdicts` table: at most one **human-source** verdict per duel (whichever `ActsAsHuman` principal — human or paired device — cast it) and one per **critic principal key**; `duels` rows are immutable. The schema ties `source` to `principal_kind` (`human` ⇔ `human`/`device`), as D-M2a-12 does for ratings. Refines design §3.6 (verdict columns on `duels`). | The future Critic Bench (M6) needs human and critic verdicts on the same duel; immutability makes the ladder's input auditable; a CHECK makes rating isolation a schema invariant rather than a code path. |
| D-M2b-4 | **Reveal rules:** a duel is revealed to a caller iff it is non-blind, or the human verdict exists, or the caller is a critic whose principal key has judged it. Other sources' verdicts are shown to a human only after the human verdict exists. **Server-side blindness protects humans only:** an agent can de-blind a duel itself (clip URLs are input-addressed, so `get_clip` with the duel window and gain 0 returns the quieter slot's key, and identical inputs give identical bytes), so a critic verdict's `saw_reveal` means "the server had revealed the duel to this principal", not "the agent could not know". **Pair membership is not secret:** `list_duels` filters by `track_id`/`root_track_id` include unrevealed duels; blindness hides only which slot holds which track (and everything that would tell). Opaque per-duel clip URLs were rejected: an agent that can download clips can compare their bytes anyway. | Prevents anchoring the human on machine opinions and slot identity; keeps critic data honest about what the server disclosed; avoids security theatre against principals that can hear nothing but can compare bytes. A human who started a "Blind round" already knows the pairs. |
| D-M2b-5 | The ladder uses **human-source verdicts on blind duels only** by default (`include_sighted=true` adds non-blind human verdicts). Critic verdicts are never used. | The ladder's value is unbiased preference; non-blind "compare with parent" (design F9) is stored and shown in `compare_tracks` but is knowingly biased. |
| D-M2b-6 | **Model:** Davidson ties (ν shared); prior θ ~ N(0, 1.5²) per track; prior ν ~ Gamma(shape 2, rate 2); `both_bad` = two forced-choice BT losses (no tie term) against a **fixed** reject anchor θ_R ≡ 0. The anchor contributes only losses, so `u` is a ranking score on the reject-anchored scale, **not** an acceptance probability; no `p_accept` is published. | Without a ν prior an all-tie history sends ν → ∞ (the MM update diverges); with it ν = (T+1)/(S+2). A free anchor or "winner beats anchor" variants were simulated: the first only moves the divergence to the anchor, the second double-counts wins and worsens relative recovery (centered RMSE 0.35–0.52 vs ≤ 0.20). |
| D-M2b-7 | The ladder is **computed on read** (no table) with an in-process cache of `ladder.Fit` results (and their item order) keyed by `(scope, root, include_sighted, count and max rowid of human verdicts)`; titles, seeds and roots are joined on every read. | Verdicts are append-only, so the key changes on every insert; fits for personal-scale data take milliseconds, the cache bounds pathological cases; titles change through `update_track` (M2a) and must never be served stale. |
| D-M2b-8 | **Clipping** is measured per 100 ms frame with `astats` (`reset=1`): a frame is *clipped* iff `Peak_level ≥ −0.01 dBFS` and `Flat_factor` is finite and > 0 (two or more consecutive samples at the extreme level); a frame that only reaches full scale is a *full-scale touch*. `Peak_count`/`Abs_Peak_count` are not used. **Silence** is `silencedetect=noise=-50dB:duration=1`. Both run in **one** ffmpeg pass. | Measured with ffmpeg n9.0.1: a hard-clipped sine gives Peak_level 0.000000 and Flat_factor 24.6 dB; a −0.087 dBFS sine and a single full-scale sample both give Flat_factor 0. `Peak_count` counts occasions at the signal's own min/max (averaged over channels), not full scale. Silent frames give `Flat_factor=-inf`, which the parser accepts. |
| D-M2b-9 | **Spectrogram** = ffmpeg `showspectrumpic` (`legend=1`, `fscale=log`, `scale=log`, `color=magma`, mono downmix) for the plot and axes, plus a Go-drawn annotation band on top (header, section labels, lyric lines) and section boundary lines over the plot. Lyric lines are **listed inside their timed section**; no per-line timing is claimed. Text is ASCII-folded (`internal/textfold` + `?` for the rest) and drawn with `golang.org/x/image/font/basicfont`. JPEG via `image/jpeg` with a quality/size ladder; hard budget **100 000 bytes**. The plot rectangle is computed from ffmpeg's legend layout (`x0 = int((log10(sr)+1)·25)`, `y0 = 64`) and **verified at runtime** (image size) and by a calibration test. | Measured on n9.0.1 with Go 1.27 `image/jpeg` (`s=1024x384` → 1308×512 image, plus a 110-row black band): `showspectrumpic` averages `consumed/spf` FFT frames per column, so long noise is smoother and compresses better and **short full-band noise is the worst case**. White noise at −6 dBFS, mono: 6 s → q85 130.8 KB, q75 99.4 KB, q65 82.7 KB; 16 s → 132.4 / 101.1 / 84.0 KB; 60 s → 99.6 / 74.5 KB; 360 s → 73.6 KB at q85 (69.3 KB without the band; revision 1's "≈ 66 KB" was this case). Short noise therefore needs q65 at the first size; q45 there is ≈ 63 KB and `768×288` at q85 ≈ 75 KB, so the ladder always fits. Our own axes would duplicate ffmpeg's log-frequency labelling; checking the geometry makes a layout change fail closed instead of mis-drawing boundaries. A hand-rolled bitmap font is not worth ~100 lines of glyph data; `x/image` is maintained by the Go team (like `x/text` and `x/crypto`). |
| D-M2b-10 | **Tier A runs after the take is committed**, from the CLI (`import` and `seed-synthetic` call `ears.Backfill` after apply; `metamusic analyze` runs it on demand). Failures are warnings and never change the import exit code. Ingest (C15) is unchanged; the importer (C16) changes only its orphan scan (`derived/clips/`, C43). The CLI builds its Ears service through `app.NewMedia` (C51.7), not through `app.Build`. **Capabilities are checked separately:** `ffmpeg.New` keeps M1's requirements; a missing Tier A filter makes Tier A report `TOOL_MISSING` once (import and seed still succeed), a missing clip filter makes clip-dependent ops answer 503 `clips`. | The spectrogram overlay needs the sections the importer writes in the take transaction; a post-commit step keeps the frozen-master pipeline untouched; an older ffmpeg must not break `import` because of a filter only Tier A uses. |
| D-M2b-11 | **Token loops:** the `.npy` time axis is the **longest** axis (ties: the last); other axes are streams; tokens per second = tokens / track duration. Metrics are deterministic; the analyzer version records the Go version because `compress/flate` output is only stable per Go release. Flags say "suspected". | YuE2's `semantic.npy` shape is not documented; the heuristic is robust for (T), (1, T), (2, T) and (T, 2). Thresholds are uncalibrated on real data, so wording stays honest; changing a threshold bumps the definition version. |
| D-M2b-12 | Analyzer **presence** = a row whose `version` starts with the current definition token (`mm-silence/1 `, …). A different ffmpeg/Go build does not trigger a re-run; `--force` does. When several rows match, the newest `created_at` wins (C51.4). | Avoids re-analysing the library after every image rebuild while keeping provenance per row. |
| D-M2b-13 | The SPA fetches clip bytes only through `web/src/api/media.ts` (the one place `fetch` is allowed for non-registry URLs), decodes them with `decodeAudioData` in **one shared `AudioContext`**, and plays duels through Web Audio; Play calls `ctx.resume()` synchronously in the click handler, before any `await`. THE `<audio>` element is paused when a duel starts; M1's "no Web Audio in the playback path" stays true for the player. | Sample sync needs two `AudioBufferSourceNode`s on one clock (design §5.5); iOS limits the number of contexts and only resumes one inside the user gesture. |
| D-M2b-14 | Duels **expire 30 days** after creation: `next_duel` skips them and `submit_duel` refuses them (`DUEL_EXPIRED`); no state column changes. | Keeps the queue from silting up with stale agent-created duels without a mutating cancel operation. |
| D-M2b-15 | MCP image results: an operation with `x-mm.mcp_image: true` returns a body with `image: {mime_type, data_base64}`; the MCP adapter turns it into `ImageContent` and strips `data_base64` from the structured and text content. | One registry op serves REST (base64 or a signed link) and MCP (`ImageContent`) without duplicating ~133 k characters of base64 in the tool result. |
| D-M2b-16 | The stored and API value of a non-human verdict source is **`critic`** (design §5.5), not M2a's `ratings.source` value `agent`; `principal_kind` stores `agent`. | "Critic" is the vocabulary of the API descriptions, the UI and the M6 Critic Bench; M2a already renders `ratings.source='agent'` as "critic" in its API. Cross-table queries (ratings vs verdicts) join on `principal_kind`, which is identical in both tables. |
| D-M2b-17 | `create_duel` is an M2a **create** op (`Idempotency-Key` required over REST, derived over MCP; D-M2a-3) on the **heavy** rate bucket, like `get_clip`. `submit_duel` is **naturally idempotent** (the replay rule, C48.4) and takes no `Idempotency-Key`, like `log_listen_events` (D-M2a-8). | `create_duel` renders up to four ffmpeg passes and a retried POST would otherwise create a second duel; `get_clip` renders arbitrary windows. A verdict is unique per source/principal by schema, so a replay is already harmless and a key would add a second, conflicting replay mechanism. |
| D-M2b-18 | **Human-source semantics:** the unique index allows one human-source verdict per duel whichever `ActsAsHuman` principal cast it. A later human-equivalent submit with the same verdict and normalised note (trimmed; empty ⇒ null) returns 200 `replayed` with the stored verdict (whose author may differ from the caller); anything else returns 409 `DUEL_ALREADY_JUDGED` with that verdict's id. For an `ActsAsHuman` caller `my_verdict` is the human verdict. | The operator's browser and phone are one human judge (design §4.2); a second "human" opinion on the same duel would double-count the ladder. |
| D-M2b-19 | Duels carry **no creator note** (revision 1's `duels.note` is dropped). | It was shown before the verdict and could name the tracks, breaking blindness; nothing reads it (YAGNI). Verdict notes stay. |
| D-M2b-20 | `compare_tracks.relation` comes from the Operator Ledger: a parent edge whose `mutations.operator` is `inferred_sibling` means **`siblings`**; any other operator gives `parent_child`/`child_parent`. Values: `siblings`, `parent_child`, `child_parent`, `same_lineage`, `unrelated`. | M1 stores seed siblings as root → child with an `inferred_sibling` row (C16.5), so a parent-pointer rule would call two seeds of one recipe "parent and child"; "same recipe" would be wrong too (different seeds are different recipes). |
| D-M2b-21 | **One blind-spots source:** `analysis.BlindSpots` (C51.3) produces the list for every Ears payload **and** M2a's `/context` `perception.blind_spots`; its fixed part keeps M2a's wording. `/context` `perception` is built by `ears.Perception` with M2a's exact keys. | Two hand-maintained lists drifted already (revision 1 vs M2a C35); an LLM must see the same limits wherever it reads perception. |
| D-M2b-22 | **`tools/list` budget:** the merged tree exposes 21 tools at 38 676 compact bytes against `toolsCeilingBytes = 40 000` (`TestToolsList_Ceiling`), so seven first-class M2b tools cannot fit. M2b-B2 measures the real size and sets the ceiling to the measured value rounded up (at most 56 000 bytes, about 14 000 tokens) with a one-line justification next to the constant; if the M2b tools alone would exceed 16 000 bytes, B2 first demotes `get_ladder`, then `compare_tracks`, then `create_duel` to `mcp: generic` (reachable through `call_operation`, documented by `describe_operation`). `toolsListBaselineBytes` and the golden are re-baselined; `TestToolsListBudgetFires` stays. | The ceiling guards agent context cost (C41) and is a committed constant, never an `-update` side effect; the tools agents call every session (`next_duel`, `submit_duel`, `get_track_analysis`, `get_track_spectrogram`) stay first-class. |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C43. Config, data layout, packages

New env (added to `config.Config` by M2b-B1; M0 C1 / M1 C10 / M2a C25 amended):
```go
ClipCacheMB int64 // MM_CLIP_CACHE_MB, default 4096, minimum 256 (smaller ⇒ config error naming the variable)
```
`internal/app.NewMedia` (C51.7) passes `ClipCacheMB << 20` to `clip.Options.CacheBytes` (bytes; tests set
tiny byte caps directly); a `config.Config` literal that leaves `ClipCacheMB` at 0 (the `servertest` rig)
gets the 4096 default there. The 256 minimum is validated in `config.load`, so `Load` and `LoadOffline`
both enforce it.

Data layout additions under `DataDir` (all writes through the M1 `*os.Root` on `DataDir`):
```
derived/clips/<aa>/<key>.flac     clip cache, input-addressed (D-M2b-1), 0444, evicted by size cap, not backed up
derived/<aa>/<sha>.spectro.jpg    spectrogram, content-addressed (its own sha256), 0444, rendition kind 'spectrogram' (backed up like every rendition, D-M2a-5)
incoming/clip-<uuid>/             clip work dir (0700): master.flac (hard link), clip.flac; removed on return
incoming/ears-<uuid>/             Tier A work dir (0700): master.flac (hard link), spectro-<w>x<h>.png, spectro.jpg
```
`<aa>` = first two hex chars of the key/sha. Work dirs older than 1 h are swept at `serve` start and at
`analyze` start (never by a GET). Rev 3: M1's `importer.sweepWorkDirs` only removes `ingest-*` dirs older
than 24 h, so the `clip-*`/`ears-*` sweep is new code in `cmd/metamusic` (M2b-B3). **The M1 argv rule is unchanged:** every ffmpeg argument is a literal
or an absolute path under `<DataDir>/incoming/`; masters are reached through a hard link
(`Root.Link(master, work/master.flac)`) inside the work dir, never by their `audio/masters/` path.
Masters keep mode 0444, sha and mtime (`TestClip_MasterUntouched`, `TestAnalyze_MasterUntouched`).

M1 C16.7 `library_orphans` additionally excludes `derived/clips/` (cache): **M2b-B1** changes
`Importer.libraryOrphans` (`internal/importer/report.go`, which today skips only `derived/probe`), adds
`TestOrphans_IgnoreClipCache` as a new test next to `TestReport_LibraryOrphans`, and records the amendment
in the M1 plan. The same function reports M2a's `derived/exports/` cache files as orphans; B1 skips that
directory too and extends the test (an M2a omission found during reconciliation).

**Packages** (new): `internal/media/clip`, `internal/ladder`, `internal/duels`, `internal/ears`,
`internal/analysis/npy`, `internal/analysis/tokens`, `internal/analysis/spectro`, `internal/textfold`
(leaf; imports only `golang.org/x/text`).
**Test-only helpers** (owner M2b-B2; added to M1's `TestNoTestHelpersInBinary` list):
`internal/media/clip/cliptest`, `internal/duels/duelstest`, `internal/ears/earstest`. There is no
`apitest` package (M2b uses concrete services, C53).

Import graph (Go rejects import cycles at compile time; the forbidden edges below are asserted by
M1's `TestNoTestHelpersInBinary`, whose single `go list -deps` call M2b-B2 extends — no second
`go list` run):
`api → {duels, ears, clip, ladder, library, serve, problem}`;
`app → {api, duels, ears, clip, trackctx, …}`; `duels → {clip, ladder, library, serve, store, auth,
problem}`; `ears → {analysis, analysis/*, ladder, library, serve, store, mediatool, auth, problem}`;
`analysis/spectro → {textfold}`; `clip → {library, serve, mediatool, flacmeta, auth, problem}`;
`trackctx → {ears, ladder}` (types of its two interfaces, C51.8); `ladder → stdlib only`. Forbidden: `duels → ears`, `ears → duels`,
`clip → {duels, ears}`, `library → {clip, duels, ears, trackctx}`, `{ears, duels} → trackctx`,
`{clip, ladder, duels, ears, analysis/*, textfold} → api`.

### C44. Migration `internal/store/migrations/0004_duels.sql` (full DDL) and write SQL

M2a ships `0003_judge.sql`; after M2b, `PRAGMA user_version == 4` (= `len(migrations)`). Rev 3: M2a's
`TestMigration0003_Applies` asserts the 0003 objects and `user_version >= 3`, `TestMigration0003_UpgradeFromM1`
asserts `>= 3` after upgrading, and the backup tests compare the manifest with the live version, so none
of them changes; the one test that hard-codes 3 is `internal/httpserver/server_test.go` (`schema_version`
of `get_system_info`), which M2b-B1 updates to 4 and records in the M2a plan (C27). Tier A analyses and spectrograms need **no** schema change: `analyses.analyzer` (`silence`,
`clipping`, `token_loops`), `analyses.tier` (`A`) and `renditions.kind` (`spectrogram`) are Go-validated
enums by M1's design (C11).

```sql
-- 0004_duels.sql — blind duels and their verdicts (M2b).

CREATE TABLE duels (
  id               TEXT PRIMARY KEY,                        -- UUIDv7 (Deps.NewID)
  slot_a_track_id  TEXT NOT NULL REFERENCES tracks(id),
  slot_b_track_id  TEXT NOT NULL REFERENCES tracks(id),
  swapped          INTEGER NOT NULL CHECK (swapped IN (0,1)), -- 1 ⇔ slot A holds the *requested* b track
  blind            INTEGER NOT NULL CHECK (blind IN (0,1)),
  from_ms          INTEGER NOT NULL CHECK (from_ms >= 0),
  to_ms            INTEGER NOT NULL,
  clip_def         TEXT NOT NULL,                           -- 'mm-clip/1'
  tool_version     TEXT NOT NULL,                           -- clip renderer ToolVersion() at creation ('ffmpeg <token>')
  match            TEXT NOT NULL,                           -- Go-validated: 'off'|'matched'|'limited'|'window_silent'
  target_lufs      REAL,                                    -- NULL unless match IN ('matched','limited')
  gain_a_cdb       INTEGER NOT NULL CHECK (gain_a_cdb BETWEEN -6000 AND 2400),  -- centi-dB applied to slot A (Go: ≤ 0, D-M2b-2)
  gain_b_cdb       INTEGER NOT NULL CHECK (gain_b_cdb BETWEEN -6000 AND 2400),
  window_lufs_a    REAL,                                    -- measured window loudness (NULL when match='off')
  window_lufs_b    REAL,
  window_tp_a      REAL,                                    -- measured window true peak, dBTP
  window_tp_b      REAL,
  created_by       TEXT NOT NULL,                           -- Principal.Key() (M2a C26)
  created_by_kind  TEXT NOT NULL CHECK (created_by_kind IN ('human','device','agent')),
  created_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL,                           -- created_at + 30 days (D-M2b-14)
  CHECK (slot_a_track_id <> slot_b_track_id),
  CHECK (to_ms > from_ms),
  CHECK (blind = 1 OR swapped = 0)
);
CREATE INDEX duels_created ON duels(created_at, id);
CREATE INDEX duels_slot_a  ON duels(slot_a_track_id);
CREATE INDEX duels_slot_b  ON duels(slot_b_track_id);
CREATE TRIGGER duels_immutable BEFORE UPDATE ON duels
BEGIN SELECT RAISE(ABORT, 'duels: immutable'); END;
CREATE TRIGGER duels_no_delete BEFORE DELETE ON duels
BEGIN SELECT RAISE(ABORT, 'duels: no physical delete'); END;

CREATE TABLE duel_verdicts (
  id              TEXT PRIMARY KEY,                         -- UUIDv7 (Deps.NewID)
  duel_id         TEXT NOT NULL REFERENCES duels(id),
  source          TEXT NOT NULL CHECK (source IN ('human','critic')),             -- D-M2b-16
  principal_kind  TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id    TEXT NOT NULL,                            -- Principal.Key() (M2a C26)
  verdict         TEXT NOT NULL CHECK (verdict IN ('a','b','tie','both_bad')),
  winner_track_id TEXT REFERENCES tracks(id),               -- the slot's track for 'a'/'b', else NULL
  note            TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000),  -- trimmed; empty ⇒ NULL (Go)
  latency_ms      INTEGER CHECK (latency_ms IS NULL OR latency_ms BETWEEN 0 AND 86400000),
  switches        INTEGER CHECK (switches IS NULL OR switches BETWEEN 0 AND 100000),
  saw_reveal      INTEGER NOT NULL CHECK (saw_reveal IN (0,1)),
  created_at      TEXT NOT NULL,
  CHECK ((verdict IN ('a','b')) = (winner_track_id IS NOT NULL)),
  CHECK ((source = 'human') = (principal_kind IN ('human','device')))   -- source isolation (D-M2b-3, as D-M2a-12)
);
CREATE UNIQUE INDEX duel_verdicts_one_human  ON duel_verdicts(duel_id) WHERE source = 'human';
CREATE UNIQUE INDEX duel_verdicts_one_critic ON duel_verdicts(duel_id, principal_id) WHERE source = 'critic';
CREATE INDEX duel_verdicts_source ON duel_verdicts(source, created_at);
CREATE INDEX duel_verdicts_duel   ON duel_verdicts(duel_id);
CREATE TRIGGER duel_verdicts_winner BEFORE INSERT ON duel_verdicts
WHEN NEW.verdict IN ('a','b') AND NEW.winner_track_id IS NOT (
  SELECT CASE NEW.verdict WHEN 'a' THEN slot_a_track_id ELSE slot_b_track_id END
  FROM duels WHERE id = NEW.duel_id)
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: winner does not match slot'); END;
CREATE TRIGGER duel_verdicts_immutable BEFORE UPDATE ON duel_verdicts
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: immutable'); END;
CREATE TRIGGER duel_verdicts_no_delete BEFORE DELETE ON duel_verdicts
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: no physical delete'); END;
```
The 100 ms window grid, the reveal rule and the gain limits are Go rules (C46, C48.3), not CHECKs: CHECK
constraints encode only invariants that will never change (M1 C11). The principal-kind enums mirror
M2a's `ratings` DDL; `system` and anonymous principals can never write a duel or a verdict.

**Write SQL (binding; owners in brackets):**
- Duels [duels]: plain `INSERT INTO duels(…) VALUES (…)` in one write transaction; `id` from `NewID`.
- Verdicts [duels]: plain `INSERT INTO duel_verdicts(…) VALUES (…)`; `id` from `NewID`; a unique-index
  violation (SQLite extended code `SQLITE_CONSTRAINT_UNIQUE`) ⇒ read the existing row and apply the
  replay rule (C48.4).
- Tier A analyses [ears]: `INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json,
  created_at) VALUES (…) ON CONFLICT(track_id, analyzer, version) DO NOTHING`.
- Spectrogram [ears]: `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json,
  created_at) VALUES (…, 'spectrogram', …) ON CONFLICT(track_id, kind) DO UPDATE SET
  sha256=excluded.sha256, bytes=excluded.bytes, path=excluded.path, params_json=excluded.params_json,
  created_at=excluded.created_at` (M1 allows rendition UPDATE; the old file becomes a reported orphan,
  D-M1-10). Never `INSERT OR REPLACE` (`TestNoReplaceSQL`, M1); no `DELETE FROM` (M2a `TestNoDeleteSQL`).
- Every write path above has a test that runs it twice.

### C45. Library, serve, problem and textfold additions (M2b-B1)

> **M4a amendment (B2):** `serve.Chain` receives the cover and download resolvers after the clip resolver, and the media extension `png` is accepted (M4a C110.3).

```go
// internal/library — read helpers used by clip, duels, ears and trackctx
type TrackRef struct {
    ID, Title, RequestID, RootTrackID, ParentTrackID string // ParentTrackID "" for roots
    ParentOperator           string // operator of the track's mutations row (one per child; "inferred_sibling", …); "" for roots
    Seed                     int64
    DurationS                float64
    SampleRate, Channels     int
    TotalSamples             int64
    MasterSHA256, MasterPath string // MasterPath is DataDir-relative
    Status                   string
    RecordedAt               time.Time
}
func (s *Service) TrackRefs(ctx context.Context, ids []string) (map[string]TrackRef, error) // unknown ids are absent
func (s *Service) LineageTracks(ctx context.Context, rootID string) ([]TrackRef, error)    // root first, then recorded_at, id; ErrNotFound if root unknown
type TimedSection struct {
    Idx int; Kind, Label string; StartS, EndS float64
    Lines []string // `sections.lines_json` of the selected source's row (rev 3: the importer writes it on lyrics and plan rows), else nil
}
type Sections struct {
    Timed             bool
    Source            string   // 'manual' > 'measured' > 'plan' (first present, all rows timed); "" when untimed
    Confidence        *float64
    Timeline          []TimedSection // empty when untimed
    Structure         []string       // lyrics labels in order ("verse 1", "chorus 1", …), always present
    ExpectedDurationS *float64       // score bars × seconds_per_bar when timed, else nil
}
func (s *Service) Sections(ctx context.Context, trackID string) (Sections, error)
// Rev 3: the selection (`sectionRank`: manual 4 > measured 3 > plan 2 > lyrics 1; Timed ⇔ every row of the
// winner has start_s) already exists inline in `trackExtras` (tracks.go) for `Track().Sections/Timed`.
// M2b-B1 factors it into one helper that both `trackExtras` and `Sections` call, so ears.Perception and
// M2a's /context cannot diverge (`TestSections_SelectionRule` also compares with `Track()`).
type ArtifactRef struct{ Name, Path, SHA256 string; Bytes int64 }
func (s *Service) Artifact(ctx context.Context, trackID, name string) (ArtifactRef, error) // ErrNotFound
// Resolve (M1 C17; `Service.Resolve` in tracks.go) gains ext "jpg" ⇒ renditions(kind='spectrogram').sha256 → image/jpeg.
```
**Media handler** (M1 C18 as amended by M2a C33): the path regex becomes
`^/media/([0-9a-f]{64})\.(flac|mp3|jsonl|zip|jpg)$`; content types come from the resolver
(`jpg` ⇒ `image/jpeg`). M2b reuses M2a's `serve.Chain` (no new chain code) with the order **library →
exports → clips** (the clips resolver is appended by M2b-B2 in `internal/app.Build`; it answers only
`flac` keys under `derived/clips/`). Everything else in C18/C33 is unchanged (signature, expiry,
headers, Range, `ServeContent`, `Content-Disposition` for exports); M2a's jsonl/zip handler tests stay
green unmodified. `serve.TTLFor` is **not** amended: `ActsAsHuman()` principals (humans and paired devices) get 12 h / 1 h
links, agents 15 min / 1 min (rev 3: the device case was wrong in revision 2); the SPA re-mints clip links
on 403/404 (C54.3).
```go
// internal/problem — shared service error (leaf package, M1 C10; rev 3: today it holds only `Body` and
// `Write`; M2b-B1 adds this in `error.go`, and M2b services return it instead of declaring their own type)
type Error struct {
    Status  int
    Code    string
    Message string
    Details map[string]any
}
func (e *Error) Error() string
func E(status int, code, msg string, details map[string]any) *Error
```
```go
// internal/textfold — leaf (M1 C12 fold table moved here; yue2.Slug calls it, its tests unchanged)
func Fold(s string) string // NFKD (x/text) + strip Mn marks + the D-M1-3 table (ß→ss æ→ae œ→oe ø→o ł→l đ→d þ→th, upper-case forms too); no case change
```

### C46. Clips: `internal/media/clip`

```go
const (
    Def        = "mm-clip/1"
    OutRate    = 48000
    FadeMS     = 10
    GridMS     = 100
    MinClipMS  = 30000
    MaxClipMS  = 45000
    SilentLUFS = -60.0
    MaxCutCDB  = -2400 // −24 dB
)
type Window struct{ StartMS, EndMS int64 }
func (w Window) DurationS() float64
// ResolveWindow validates a requested window against a duration (the shorter track's for a duel).
// D = floor(duration to 100 ms). Allowed length L: min(30 s, D) ≤ L ≤ min(45 s, D).
// from/to are rounded to the nearest 100 ms (half away from zero). nil from ⇒ DefaultWindow;
// nil to ⇒ from + min(45 s, D − from) (then validated).
func ResolveWindow(durationS float64, fromS, toS *float64) (Window, error) // *problem.Error 422 CLIP_WINDOW_INVALID {duration_s,min_s,max_s}
// DefaultWindow: L = min(45 s, D); start = min(floor100(0.30·D), D − L); i.e. whole track when D ≤ 45 s.
func DefaultWindow(durationS float64) Window

type Measure struct{ IntegratedLUFS, TruePeakDBTP float64 }
// MatchGains (D-M2b-2): both > SilentLUFS ⇒ target = min(a,b); gain = round_cdb(target − x), clamped ≥ MaxCutCDB
// (clamp ⇒ status "limited", target as measured); either ≤ SilentLUFS ⇒ (0, 0, nil, "window_silent").
func MatchGains(a, b Measure) (ga, gb int, target *float64, status string)
// TargetGain (standalone get_clip, attenuate only): I ≤ SilentLUFS ⇒ (0, "window_silent");
// want = round_cdb(target − I); want > 0 ⇒ (0, "limited"); want < MaxCutCDB ⇒ (MaxCutCDB, "limited");
// else (want, "matched").
func TargetGain(m Measure, targetLUFS float64) (gcdb int, status string)

type Spec struct {
    MasterSHA256, MasterPath string // from TrackRef
    SampleRate, Channels     int    // source; Channels must be 1 or 2 (else 422 CLIP_UNSUPPORTED_CHANNELS)
    W                        Window
    GainCDB                  int    // ≤ 0
}
// Key = hex sha256 of compact JSON with sorted keys:
// {"channels":C,"def":"mm-clip/1","end_sample":E,"fade_ms":10,"gain_cdb":G,"master_sha256":"…",
//  "out_rate":48000,"start_sample":S,"tool":"<ToolVersion(), e.g. ffmpeg n9.0.1>"}, S = StartMS·SampleRate/1000,
// E likewise (exact integers for 44.1/48 kHz because windows are on the 100 ms grid).
func Key(s Spec, toolVersion string) string
func (s Spec) Samples() (start, end int64)

type Object struct {
    Key, RelPath string
    Bytes        int64
    DurationS    float64 // (E−S)/SampleRate
    Channels     int     // output channels (= source, 1 or 2)
}
type WindowAnalyzer interface { // *ffmpeg.FFmpeg; cliptest.Analyzer in tests
    ToolVersion() string
    WindowLoudness(ctx context.Context, workPath string, startSample, endSample int64) (Measure, error)
}
type RenderFunc func(ctx context.Context, workMaster, outPath string, sp Spec) error
type Options struct {
    CacheBytes int64                                                  // > 0; app.Build: ClipCacheMB << 20
    Now        func() time.Time
    Pinned     func(ctx context.Context) (map[string]struct{}, error) // keys never evicted; B3 wires duels.OpenClipKeys; nil ⇒ none
    Render     RenderFunc                                             // nil ⇒ the ffmpeg render below; cliptest.Render in rigs without ffmpeg
}
type Service struct{ /* data *os.Root, dataDir, tools, an, lib, signer, opts, inflight map, sem */ }
func New(data *os.Root, dataDir string, tools mediatool.Tools, an WindowAnalyzer, lib *library.Service,
    signer *serve.Signer, o Options) *Service
func (s *Service) ToolVersion() string                                  // an.ToolVersion(); makes *Service a duels.ClipRenderer
func (s *Service) Measure(ctx context.Context, sp Spec) (Measure, error)
func (s *Service) Ensure(ctx context.Context, sp Spec) (Object, error)  // render if absent (cache fill)
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) // "flac" only
func (s *Service) Link(ctx context.Context, p auth.Principal, trackID string, q Query) (Link, error) // get_clip
type Query struct{ FromS, ToS, MatchLUFS *float64 }
type Link struct {
    TrackID, URL, Key, ContentType string // ContentType "audio/flac"
    ExpiresAt time.Time; TTLS int64
    Bytes int64; SampleRate, BitsPerSample, Channels int // 48000, 24
    FromS, ToS, DurationS float64
    GainDB float64; WindowLUFS, WindowTPDBTP, TargetLUFS *float64; Match string
}
```
Wiring (M2b-B3): `clip.New` cannot see duels (C43 forbids `clip → duels`), so `internal/app.Build`
(through `app.NewMedia`, C51.7, which takes the `Pinned` func as a parameter) closes over the duels service: `var ds *duels.Service; cs := clip.New(…, clip.Options{Pinned: func(ctx)
(map[string]struct{}, error) { return ds.OpenClipKeys(ctx) }}); ds = duels.New(duels.Options{Clips: cs,
…})`. If `ffmpeg.RequireClips()` fails, `app.Build` logs one warning naming the missing capability,
leaves `Deps.Clips` nil and does not set `duels.Options.Clips` (it never assigns a nil `*clip.Service`
to the interface, which would be a non-nil typed nil; C51.7). Rev 3: `app.Build` has no ffmpeg access today;
`app.Options.Media` (C51.7) is how the rigs inject fakes and how production probes `cfg.FFmpeg`.

**Measure** (`analysis/ffmpeg.WindowLoudness`, file `internal/analysis/ffmpeg/window.go`, timeout 60 s,
stdout cap 32 MiB; parsed with M1's exported `ffmpeg.ParseLoudness` (last frame; rev 3: it requires
`sample_peak` and the `LRA*` keys, hence `peak=true+sample`); `Measure{IntegratedLUFS, TruePeakDBTP}` is
copied from the returned `analysis.Loudness`):
```
ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<work>/master.flac -map 0:a:0 \
  -af atrim=start_sample=<S>:end_sample=<E>,ebur128=peak=true+sample:framelog=quiet:metadata=1,ametadata=mode=print:file=- -f null -
```

**Render** (`Ensure`; the default `RenderFunc`):
```
ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<work>/master.flac -map 0:a:0 \
  -map_metadata -1 -fflags +bitexact -flags:a +bitexact \
  -af atrim=start_sample=<S>:end_sample=<E>,asetpts=PTS-STARTPTS[,aresample=48000:resampler=swr][,volume=<G/100>dB:precision=double],afade=t=in:st=0:d=0.010,afade=t=out:st=<L−0.010>:d=0.010 \
  -c:a flac -sample_fmt s32 -bits_per_raw_sample 24 -compression_level 5 -f flac file:<work>/clip.flac
```
`aresample` only when `SampleRate ≠ 48000`; `volume` only when `GainCDB ≠ 0` (formatted `%.2f`); `L` =
window length in seconds formatted `%.3f`. Mono stays mono (no upmix, which would change loudness).

`Ensure` steps: (1) key; `Lstat derived/clips/<aa>/<key>.flac` regular ⇒ return (no tool runs);
(2) per-key in-flight guard (a mutex-protected map of done channels): the first caller starts the render
in a goroutine under `context.WithoutCancel(ctx)` with a **60 s timeout**; every caller (the first
included) waits on the done channel **or its own** `ctx.Done()` (a cancelled caller returns
`ctx.Err()`; the render continues and fills the cache for the others); (3) inside the render: a render
semaphore of 2; (4) cache cap: when the total size of `derived/clips/` (one walk per render) exceeds
`CacheBytes`, call `Pinned` and delete oldest-mtime clip files whose key is not pinned until ≤ 90 % of
the cap (a `Pinned` error skips eviction for this render and is logged; if pinned files alone exceed the
cap, the render proceeds — the cap is soft for open duels); (5) work dir `incoming/clip-<uuidv7>/`,
`Root.Link(MasterPath, work/master.flac)`; (6) render (`Options.Render`); (7) verify with
`flacmeta.Read(f, size)` (`Info.StreamInfo`): 24 bits, 48000 Hz, channels as expected, `TotalSamples` =
expected ± 1 (resampling);
else 500 `INTERNAL` (logged `clip_invalid`); (8) `Chmod 0444`; `Root.Link` into place (`EEXIST` ⇒
another render won; reuse); (9) `RemoveAll(work)`. Errors: tool failure ⇒ 500 `INTERNAL` (stderr tail
logged, never argv).

`Resolve(sha, "flac")`: `derived/clips/<aa>/<sha>.flac` is a regular file ⇒ `serve.Object{RelPath,
ContentType:"audio/flac", ETag: sha, ModTime}`; else `serve.ErrNotFound`. It is the last resolver of the
media chain (C45).

`Link` (the `get_clip` operation): `TrackRefs` (404) → `ResolveWindow(track duration, …)` →
`MatchLUFS` nil ⇒ gain 0, `match:"off"`; else `Measure` + `TargetGain` (`match_lufs` must be in
[−40, −6], else 422 `VALIDATION_FAILED`) → `Ensure` → signed URL `ttl, bucket := serve.TTLFor(p)`; `Signer.Sign(key, "flac", ttl, bucket)` (humans and paired
devices 12 h, agents 15 min; rev 3). `Link.URL` is the relative `/media/…` form `Signer.Sign` returns, like
`get_media_links`; the SPA fetches it same-origin.

### C47. Ladder: `internal/ladder` (pure math)

```go
const Anchor = -1 // the virtual reject item; θ_R ≡ 0
type Comparison struct {
    Winner, Loser int  // item indices 0..n−1; Winner may be Anchor (both_bad); Loser never is
    Tie           bool // Davidson tie between Winner and Loser (never with the Anchor)
}
type Options struct {
    PriorSD float64  // default 1.5 (prior θ_i ~ N(0, PriorSD²))
    NuShape float64  // default 2 (prior ν ~ Gamma(shape, rate))
    NuRate  float64  // default 2
    FixedNu *float64 // tests: hold ν fixed (0 ⇒ plain Bradley-Terry; 0 with any Tie comparison is an error)
    Tol     float64  // default 1e-9 (max |Δθ_i| and |Δν| per iteration)
    MaxIter int      // default 50000
}
type Fit struct {
    Theta, SD    []float64 // per item; items without comparisons: θ = 0, SD = PriorSD
    Nu           float64
    Iterations   int
    Converged    bool
    LogPosterior float64
}
func FitBTD(n int, cmps []Comparison, o Options) (Fit, error) // errors: index out of range, Winner==Loser, Anchor as loser or in a tie, FixedNu=0 with ties
func LogPosterior(n int, cmps []Comparison, theta []float64, nu float64, o Options) float64
func PBeats(thI, thJ, nu float64) (win, tie, loss float64) // Davidson; sums to 1

// Shared result types (stdlib-only so duels, ears and trackctx can all use them; explicit JSON tags):
type Strength struct {
    U      float64 `json:"u"`
    SD     float64 `json:"sd"`
    NDuels int     `json:"n_duels"`
}
type Counts struct {
    AWins   int `json:"a_wins"`
    BWins   int `json:"b_wins"`
    Ties    int `json:"ties"`
    BothBad int `json:"both_bad"`
    N       int `json:"n"`
}
type PairStats struct {
    Human       Counts `json:"human"`         // blind + sighted human-source verdicts on duels whose slots are exactly {a, b}
    HumanBlindN int    `json:"human_blind_n"` // how many of Human.N were blind
    Critic      Counts `json:"critic"`
}
```

**Model.** π_i = e^{θ_i}; the anchor has π_R = 1. For a pair (i, j) of items: P(i≻j) = π_i/D,
P(j≻i) = π_j/D, P(tie) = ν√(π_iπ_j)/D with D = π_i + π_j + ν√(π_iπ_j). Anchor comparisons are plain
Bradley-Terry: P(R≻i) = 1/(1 + π_i) (no tie term). Log-posterior (up to constants):
```
LP(θ, ν) = Σ_pairs [ w_ij θ_i + w_ji θ_j + t_ij (log ν + (θ_i+θ_j)/2) − n_ij log D_ij ]
         − Σ_i n_iR log(1 + π_i)  − Σ_i θ_i² / (2·PriorSD²)  + (NuShape−1) log ν − NuRate·ν
```
(ν terms omitted when `FixedNu` is set.) Aggregate comparisons into pair counts `w_ij, w_ji, t_ij`,
`n_ij = w_ij + w_ji + t_ij`, anchor losses `n_iR`; iterate items and pairs in index order (determinism).

**MM iteration k** (each step provably does not decrease LP):
1. θ-step (ν fixed at ν^k). For each item i: `A_i = W_i + T_i/2` (wins + half the ties);
   `B_i = Σ_j n_ij (1 + (ν/2)√(π_j/π_i)) / D_ij + n_iR / (1 + π_i)` (all π at iteration k). The
   minorizer is separable; θ_i^{k+1} maximises `A θ − B e^θ − θ²/(2·PriorSD²)`: Newton on
   `h(θ) = A − B e^θ − θ/PriorSD²` (`h' < 0`), start at θ_i^k, stop at |Δ| < 1e-13 or 100 steps. (Without
   the prior this is the classic `π_i = A_i / B_i`.) All items update from the same π^k.
2. ν-step (θ^{k+1} fixed): `S = Σ_pairs n_ij √(π_iπ_j) / D_ij` evaluated at (θ^{k+1}, ν^k);
   `ν^{k+1} = (T + NuShape − 1) / (S + NuRate)` where T = total ties (default: `(T+1)/(S+2)`).
3. Stop when `max_i |Δθ_i| < Tol` and `|Δν| < Tol`; `Converged=false` after `MaxIter`.

**Laplace SD.** At the MAP (ν held at ν̂), H = Hessian of −LP over the m items that have ≥ 1 comparison:
for a pair with `p_i = π_i/D, p_j = π_j/D, p_t = ν√(π_iπ_j)/D, q_i = p_i + p_t/2, q_j = p_j + p_t/2`:
`H_ii += n(p_i + p_t/4 − q_i²)`, `H_jj += n(p_j + p_t/4 − q_j²)`, `H_ij = H_ji += n(p_t/4 − q_i q_j)`;
for an anchor loss: `H_ii += n_iR · σ(θ_i)(1 − σ(θ_i))`; plus `1/PriorSD²` on the diagonal. Cholesky,
invert, `SD_i = √(H⁻¹)_ii`. ν's uncertainty is ignored (documented).

**Determinism and precision.** Same input ⇒ identical output bits on one platform. Go may fuse
multiply-adds differently across architectures, so tests compare with tolerances and API values are
rounded to 1e-4.

### C48. Duels service: `internal/duels`

```go
type ClipRenderer interface { // implemented by *clip.Service (C46 ToolVersion); cliptest.Renderer in tests
    ToolVersion() string
    Measure(ctx context.Context, sp clip.Spec) (clip.Measure, error)
    Ensure(ctx context.Context, sp clip.Spec) (clip.Object, error)
}
type Options struct {
    Store   *store.Store
    Library *library.Service
    Clips   ClipRenderer   // nil ⇒ create/next/get answer 503 UNAVAILABLE {component:"clips"}; list/submit/ladder still work
    Signer  *serve.Signer
    Now     func() time.Time
    NewID   func() string  // Deps.NewID (M2a C28); nil ⇒ uuid v7
    Rand    func() bool    // slot coin; nil ⇒ crypto/rand
    Expiry  time.Duration  // 0 ⇒ 30 days
    Ladder  ladder.Options // zero ⇒ defaults
}
func New(o Options) *Service
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Duel, error)
func (s *Service) Next(ctx context.Context, p auth.Principal, exclude []string) (NextResult, error)
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Duel, error)
func (s *Service) List(ctx context.Context, p auth.Principal, f ListFilter) (Page, error)
func (s *Service) Submit(ctx context.Context, p auth.Principal, id string, in VerdictInput) (SubmitResult, error)
func (s *Service) Ladder(ctx context.Context, q LadderQuery) (LadderPage, error)
func (s *Service) PairStats(ctx context.Context, a, b string) (ladder.PairStats, error)                     // compare_tracks
func (s *Service) Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error) // global fit; ν
func (s *Service) OpenClipKeys(ctx context.Context) (map[string]struct{}, error) // clip cache pins (C46)
type CreateInput struct {
    ATrackID, BTrackID string
    Blind, MatchLoudness *bool    // nil ⇒ true (C53: plain bools with Huma defaults would turn an explicit false into true)
    FromS, ToS           *float64
}
type VerdictInput struct {
    Verdict    string // a | b | tie | both_bad
    Note       *string
    LatencyMS  *int64
    Switches   *int
}
var ErrNoSource = errors.New("duels: principal cannot judge")
func SourceFor(p auth.Principal) (string, error) // C48.2
```
The api handlers (not the service) call `audit.NoteCreated(ctx, id)` for a created duel and for a
verdict that was inserted (201), never for a replay.

**C48.1 Create** (`create_duel`): `a_track_id ≠ b_track_id` (else 422 `DUEL_SAME_TRACK`); both exist
(404 `NOT_FOUND` `{track_id}`); window = `clip.ResolveWindow(min(dur_a, dur_b), from, to)`; if
`MatchLoudness` (nil ⇒ true): `Measure` both windows, `clip.MatchGains`; else gains 0, `match:"off"`
(no `Measure` call). Slots: blind (nil ⇒ true) ⇒ `swapped = Rand()`; non-blind ⇒ `swapped = false`
(slot A = requested a). `Ensure` both clips **before** the insert (a render failure leaves no duel).
Insert one row (`id = NewID()`, `created_by = p.Key()`, `created_by_kind = p.Kind`, `tool_version =
Clips.ToolVersion()`, `expires_at = now + Expiry`). Returns the creator's view (blind ⇒ unrevealed,
C48.3). Retries are the gate's business (D-M2b-17): a replayed `create_duel` returns the stored 201
body without running the handler, so its clip links may have expired — clients call `get_duel` for
fresh links (the SPA re-mints on 403/404, C54.3).

**C48.2 Sources.** `SourceFor(p)`: `p.ActsAsHuman()` (kinds `human`, `device`) ⇒ `"human"`; `KindAgent`
(PAT over REST/MCP, and `mcp-stdio`) ⇒ `"critic"`; `system` and anonymous ⇒ `ErrNoSource` (never
`critic`; unreachable through the API — anonymous gets 401 at the gate, `system` exists only in CLI audit
rows — and mapped to 500 `INTERNAL` if it ever occurs). `principal_kind` stores `p.Kind`,
`principal_id` stores `p.Key()`. No request field can select the source.

**C48.3 Views and reveal (D-M2b-4).** `state` ∈ `open` (no human verdict, not expired), `decided`
(human verdict exists), `expired` (no human verdict, `now ≥ expires_at`). `revealed(p)` = `!blind` ∨
a human verdict exists ∨ (`SourceFor(p) = critic` and a critic verdict with `principal_id = p.Key()`
exists). Unrevealed views never contain track ids, titles, request ids, root ids, seeds, gains, window
loudness, target loudness, `swapped` or winner ids (their fields are `null`; `swapped` is never
serialised). `TestDuel_BlindRedaction` checks this structurally (C48 tests, M2b-J2). Verdict visibility:
`my_verdict` — for an `ActsAsHuman` caller the human verdict (whoever cast it, D-M2b-18), for a critic
its own verdict; `human_verdict` whenever the duel is revealed to the caller; `critic_verdicts[]` only
after the caller has judged this duel (for a human-source caller: after the human verdict exists — a
human therefore never sees machine opinions before deciding).

**C48.4 Submit** (`submit_duel`): 404; `now ≥ expires_at` ⇒ 409 `DUEL_EXPIRED`; source/principal from
`p` (C48.2); `note` normalised (trim Unicode space; empty ⇒ null; ≤ 2000 runes after trimming, else
422); `saw_reveal = revealed(p)` before the insert (for a human-source verdict this is 1 only for
non-blind duels); winner = slot A/B track for `a`/`b`; insert. **Replay rule:** if the caller's source
already has a verdict on this duel — for `human`, the one human-source verdict whichever human-equivalent
principal cast it; for `critic`, this principal key's verdict — then identical `verdict` and normalised
`note` ⇒ 200 with the stored verdict and `replayed: true` (`latency_ms`/`switches` are not compared;
the stored values win); otherwise 409 `DUEL_ALREADY_JUDGED` `{verdict_id}` of that existing verdict (for
the human source its author may differ from the caller). First insert ⇒ 201. The response is the
caller's (now revealed) view plus the verdict. Concurrent submits hit the unique index and resolve
through the same rule. `submit_duel` takes no `Idempotency-Key` (D-M2b-17).

**C48.5 Next** (`next_duel`): human source ⇒ the oldest (`created_at, id`) unexpired duel without a
human verdict; critic ⇒ the oldest unexpired, **open** duel without a verdict from this principal key;
both skip `exclude` (≤ 50 ids). `open_total` = how many such duels exist (ignoring `exclude`). The
returned duel has clip links (`Ensure` both — a cache fill, D-M2b-1). No DB writes. The SPA calls
`next_duel` only on the Duels page (C54).

**C48.6 Ladder** (`get_ladder`):
- Input rows: `SELECT d.slot_a_track_id, d.slot_b_track_id, v.verdict, v.winner_track_id FROM
  duel_verdicts v JOIN duels d ON d.id = v.duel_id WHERE v.source = 'human' AND (d.blind = 1 OR
  :include_sighted) [AND both slots ∈ lineage(:root)] ORDER BY v.created_at, v.id`. Critic rows are never
  selected (`TestLadderInputs_HumanOnly`); device verdicts are human-source and counted.
- Mapping: `a`/`b` ⇒ `{Winner, Loser}`; `tie` ⇒ `{a, b, Tie:true}`; `both_bad` ⇒ `{Anchor, a}` and
  `{Anchor, b}`.
- Items: scope `global` ⇒ tracks with ≥ 1 counted verdict; scope `lineage` ⇒ every track of
  `LineageTracks(root)` (zero-duel tracks shown with θ 0, SD 1.5, `provisional`).
- Output per item: `u` = θ (4 decimals), `sd`, `ci95 = [u − 1.96 sd, u + 1.96 sd]`, `wins`, `losses`,
  `ties`, `both_bad` (counted verdicts), `n_duels`, `provisional` = `n_duels < 3 ∨ sd > 1.0`, `rank` by
  `u` desc then `track_id`; `title`, `seed`, `root_track_id` read from `TrackRefs` on every request.
- `counts`: `verdicts_human_counted`, `verdicts_critic_excluded`, `verdicts_sighted_excluded`,
  `both_bad`, `ties`, `tracks`; `diagnostics.slot_a_win_rate` = share of `a` among counted `a`/`b`
  verdicts (null when none) with `n_ab`.
- Fit cache (D-M2b-7): LRU of 32 entries holding only `ladder.Fit` plus the item-id order, keyed by
  `(scope, root, include_sighted, count(*), max(rowid))` of human-source verdicts.
- `Strengths(ids)` = the global fit restricted to `ids` (`{U, SD, NDuels}`; tracks without counted duels
  absent) and ν — used by `compare_tracks` and by `/context` (`judgments.ladder = {u, sd, n_duels}`,
  `lineage.children[].ladder = {u, sd}`, C51.8).
- `OpenClipKeys` = the clip keys of both slots of every open duel (unexpired, no human verdict),
  recomputed from the duel rows with the **current** `Clips.ToolVersion()` (clips cached under an older
  tool are re-rendered on demand and may be evicted).

**C48.7 PairStats** (`compare_tracks`): verdict counts on duels whose two slots are exactly `{a, b}`,
oriented so `a_wins` counts wins of the requested `a`: `human` (blind and sighted; `human_blind_n`
separately) and `critic`.

**`list_duels` filters** (D-M2b-4): `track_id` matches either slot and `root_track_id` matches either
slot's root **for every duel, revealed or not** (pair membership is not secret); unrevealed rows still
render `slots.{a,b}.track: null`. `mode` = `any` (default) | `blind` | `sighted`.

### C49. Tier A signal analyzers

**C49.1 One ffmpeg pass for silence + clipping** (`internal/analysis/ffmpeg/signal.go`; timeout 300 s;
stdout cap 64 MiB):
```
ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<work>/master.flac -map 0:a:0 \
  -af asetnsamples=n=<round(sr/10)>:p=0,astats=metadata=1:reset=1:measure_perchannel=none:measure_overall=Peak_level+Flat_factor,silencedetect=noise=-50dB:duration=1,ametadata=mode=print:file=- \
  -f null -
```
The result types live in **`internal/analysis`** (`types_m2b.go`, M2b-B2), with explicit JSON tags:
```go
type Span struct {
    StartS float64 `json:"start_s"`
    EndS   float64 `json:"end_s"`
}
type Silence struct {
    ThresholdDB    float64 `json:"threshold_db"`    // -50
    MinDurationS   float64 `json:"min_duration_s"`  // 1.0
    Spans          []Span  `json:"spans"`           // ≤ 100, then SpansTruncated
    SpansTruncated bool    `json:"spans_truncated"`
    LeadingS       float64 `json:"leading_s"`       // duration of the leading span (0 if none)
    TrailingS      float64 `json:"trailing_s"`      // duration of the trailing span (0 if none)
    InternalS      float64 `json:"internal_s"`      // Σ durations of internal spans, over ALL spans (before the 100-span cap)
    TotalS         float64 `json:"total_s"`         // leading + internal + trailing
    DurationS      float64 `json:"duration_s"`      // the track duration used for closing spans
}
type Clipping struct {
    FrameS          float64 `json:"frame_s"`           // 0.1 (nominal)
    PeakDBFS        float64 `json:"peak_dbfs"`         // max over frames (−120 floor)
    FullScaleFrames int     `json:"full_scale_frames"` // Peak_level ≥ −0.01
    ClippedFrames   int     `json:"clipped_frames"`    // and Flat_factor finite > 0 (D-M2b-8)
    ClippedS        float64 `json:"clipped_s"`         // Σ clipped frame durations
    Spans           []Span  `json:"spans"`             // clipped frames merged across gaps ≤ 0.5 s; ≤ 100
    SpansTruncated  bool    `json:"spans_truncated"`
}
// internal/analysis/ffmpeg
func (f *FFmpeg) Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error) // sampleRate sets asetnsamples n
func ParseSignal(r io.Reader, durationS float64) (analysis.Silence, analysis.Clipping, error)                // pure; golden-tested
```
Parsing (same metadata-stream grammar as M1 C14): `frame:N pts:P pts_time:T` headers; per frame
`lavfi.astats.Overall.Peak_level`, `lavfi.astats.Overall.Flat_factor` (values may be `-inf`, `inf`,
`nan`); `lavfi.silence_start`, `lavfi.silence_end` (the value, not the frame time, is the boundary). A
`silence_start` without an end closes at `durationS`. Leading = a span starting at < 0.05 s; trailing =
a span ending at > duration − 0.05 s; internal = the others. **Frame durations come from `pts_time`
deltas** (frame k lasts `T_{k+1} − T_k`); the last frame closes at `durationS` (`ParseSignal` has no
sample-rate parameter). Versions: `mm-silence/1 ffmpeg <token>`, `mm-clipping/1 ffmpeg <token>`.

**C49.2 `.npy` reader** (`internal/analysis/npy`, pure Go):
```go
type Array struct{ DType string; Shape []int; Data []int64 } // C-order values widened to int64
func Read(r io.Reader, maxBytes int64) (Array, error)
// Accepts format versions 1.0/2.0/3.0; dtypes '<i1','<i2','<i4','<i8','<u1','<u2','<u4','|i1','|u1'
// ('<u8' only if every value ≤ MaxInt64); fortran_order False only; ndim 1..3; header ≤ 64 KiB;
// product(shape) × itemsize == remaining bytes exactly; any other case ⇒ ErrUnsupported / ErrCorrupt.
// Never allocates more than maxBytes (checked before allocation). Fuzzed.
```

**C49.3 Token-loop metrics** (`internal/analysis/tokens`, pure Go; definition `mm-token-loops/1`; named
types with explicit JSON tags — no anonymous structs):
```go
type Repeat struct {
    Tokens    int     `json:"tokens"`
    S         float64 `json:"s"`
    FirstAtS  float64 `json:"first_at_s"`
    SecondAtS float64 `json:"second_at_s"`
}
type Periodic struct {
    PeriodTokens int     `json:"period_tokens"`
    PeriodS      float64 `json:"period_s"`
    SpanTokens   int     `json:"span_tokens"`
    SpanS        float64 `json:"span_s"`
    AtS          float64 `json:"at_s"`
    Repeats      float64 `json:"repeats"`
}
type Stream struct {
    Index              int       `json:"index"`
    NTokens            int       `json:"n_tokens"`
    DistinctTokens     int       `json:"distinct_tokens"`
    TokensPerS         float64   `json:"tokens_per_s"`         // NTokens / durationS, 2 decimals
    Distinct4GramRatio float64   `json:"distinct_4gram_ratio"` // |distinct 4-grams| / max(1, n−3)
    LongestRepeat      Repeat    `json:"longest_repeat"`       // longest substring occurring twice without overlap
    PeriodicRun        Periodic  `json:"periodic_run"`
    GzipRatio10s       []float64 `json:"gzip_ratio_10s"`       // per full 10 s window (a final partial window counts if ≥ 50 % full)
    GzipRatioMin       *float64  `json:"gzip_ratio_min"`
    GzipRatioMedian    *float64  `json:"gzip_ratio_median"`
    Encoding           string    `json:"encoding"`             // "u16le" if max token < 65536 else "u32le"
}
type Report struct {
    DType    string   `json:"dtype"`
    Shape    []int    `json:"shape"`
    TimeAxis int      `json:"time_axis"`
    Streams  []Stream `json:"streams"`
}
func Analyze(a npy.Array, durationS float64) (Report, error)
```
- Time axis = the longest axis (ties: last); streams = the other axes flattened (≤ 16, else
  `ErrUnsupported`).
- `LongestRepeat`: binary search on length L with a 64-bit polynomial rolling hash; a candidate L is
  accepted iff two windows with equal hash **and equal tokens** start at p1 < p2 with p2 − p1 ≥ L.
- `PeriodicRun`: for periods p = 1 … clamp(round(2·tps), 1, 400): longest run of i with
  `t[i] == t[i−p]`; span = run + p; choose the largest span (ties: smaller p); `Repeats = span/p`,
  `AtS` = span start / tps.
- Gzip: tokens encoded per `Encoding`, `compress/gzip` at `BestCompression` with a zero header
  (deterministic per Go release), ratio = compressed/raw.
- Version string `mm-token-loops/1 <runtime.Version()>` (D-M2b-11). Input cap: `semantic.npy` ≤ 64 MiB.

### C50. Spectrogram: `internal/analysis/spectro` + ffmpeg picture

**C50.1 ffmpeg** (`internal/analysis/ffmpeg/spectrumpic.go`; timeout 120 s; output file ≤ 8 MiB):
```
ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<work>/master.flac \
  -filter_complex [0:a:0]aformat=channel_layouts=mono,showspectrumpic=s=<W>x<H>:legend=1:fscale=log:scale=log:color=magma[v] \
  -map [v] -frames:v 1 -update 1 -c:v png -f image2 file:<work>/spectro-<W>x<H>.png
```
**C50.2 Geometry** (`mm-spectro/1`): plot origin `x0 = int((log10(sr) + 1) · 25)` (142 at 48 kHz, 141 at
44.1 kHz), `y0 = 64`, plot `W × H`; the PNG must be exactly `(W + 2·x0) × (H + 128)`, else
`SPECTROGRAM_GEOMETRY` (fail closed). Time maps linearly: `x = x0 + t/duration · W`.

**C50.3 Overlay** (pure Go, `spectro.Compose`):
```go
type Section struct{ Label string; StartS, EndS float64; Lines []string }
type Overlay struct {
    Header    string    // ASCII, e.g. "mm-spectro/1 | 4:00 | sections: plan (confidence 0.7); lyric lines listed per section, not timed"
    Timed     []Section // empty when untimed
    Structure []string  // used only when untimed: "structure: verse 1, chorus 1, …"
}
type Geometry struct{ X0, Y0, W, H int; DurationS float64 }
func Compose(plot image.Image, g Geometry, o Overlay) (*image.RGBA, Geometry, error) // returned Geometry has Y0 += band height
func FoldASCII(s string) string // textfold.Fold (C45), then other non-ASCII ⇒ '?', controls ⇒ ' '
func EncodeBudget(img image.Image, budget int, qualities []int) (jpg []byte, quality int, err error)
```
- Canvas: an annotation band of height `6 + 13·(2 + Lmax)` (Lmax = min(6, max lines in a timed section);
  0 when untimed) **above** the ffmpeg image; band background black, text white, `basicfont.Face7x13`.
- Row 1: header. Row 2: for each timed section, its label at `x0 + start/duration·W + 2`, truncated to the
  section's pixel width (labels narrower than 3 characters are omitted; the boundary line remains);
  alternating amber `#F2A65A` / light blue `#9AD0EC` per section. Rows 3…: the section's lyric lines
  (folded, truncated to the width; a last row `+N more` when lines exceed Lmax). Untimed: row 2 shows
  the structure line; no boundary lines.
- Boundary lines: 1 px, white at 55 % alpha, at every timed section start > 0, from the band's row 2 to
  the bottom of the plot.
- **Budget ladder:** plot sizes `1024×384`, `768×288`, `512×192`; JPEG qualities `85, 75, 65, 55, 45`;
  the first (size, quality) whose JPEG is ≤ **100 000 bytes** wins; none ⇒ `SPECTROGRAM_TOO_LARGE`
  (nothing stored). Measured worst case (D-M2b-9): short (≤ ~16 s) full-band noise needs q65 at the
  first size. Rendition params: `{"def":"mm-spectro/1","ffmpeg":"<token>","plot":"1024x384",
  "quality":85,"width":1308,"height":<h>,"geometry":{"x0":142,"y0":<band+64>,"w":1024,"h":384,
  "duration_s":…},"overlay_sha256":"<sha of canonical overlay JSON>","freq_scale":"log"}`.

### C51. Ears service: `internal/ears`

```go
type TierA interface { // *ffmpeg.FFmpeg; earstest.TierA in tests
    ToolVersion() string
    Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error)
    SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error
}
type Judgments interface { // *duels.Service; earstest.Judgments in tests
    PairStats(ctx context.Context, a, b string) (ladder.PairStats, error)
    Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}
```
`PairStats` and `Strength` live in the stdlib-only `internal/ladder` (C47) so that `ears` never imports
`duels` (C43).

```go
type Options struct {
    Store *store.Store; Library *library.Service; Data *os.Root; DataDir string
    Analyzer TierA
    Missing  error // non-nil (from ffmpeg.RequireTierA) ⇒ AnalyzeTrack/Backfill run nothing and report TOOL_MISSING once (C51.7)
    Signer *serve.Signer; Judgments Judgments; Now func() time.Time
}
func New(o Options) *Service
func (s *Service) UseJudgments(j Judgments) // setter (rev 3: like trackctx.UseScoreABC); app wiring creates ears before duels, then calls it
func (s *Service) AnalyzeTrack(ctx context.Context, trackID string, o AnalyzeOptions) (Outcome, error)
type AnalyzeOptions struct{ Force bool }
type Outcome struct{ TrackID string; Ran, Skipped []string; Failed map[string]string } // analyzer ⇒ error code
func (s *Service) Backfill(ctx context.Context, o BackfillOptions) (BackfillReport, error)
type BackfillOptions struct{ TrackIDs []string; Force, DryRun bool; Jobs int } // Jobs default 1
type BackfillReport struct{ Tracks, Analysed, Failed int; Ran map[string]int; Failures []Outcome; ToolMissing string } // ToolMissing names the capability
func (s *Service) Dossier(ctx context.Context, trackID string) (Dossier, error)
func (s *Service) Spectrogram(ctx context.Context, p auth.Principal, trackID string, inline bool) (SpectrogramView, error)
func (s *Service) Compare(ctx context.Context, a, b string) (Comparison, error)
func (s *Service) Perception(ctx context.Context, trackID string) (Perception, error) // /context (C51.8)
```

**C51.1 AnalyzeTrack.** `Options.Missing` set ⇒ returns `Outcome{Failed: {"tier_a": "TOOL_MISSING"}}`
without touching the track (Backfill checks once and returns `ToolMissing` without iterating). Needed
analyzers: `silence` + `clipping` (one pass), `token_loops` (only if the track has a `semantic.npy`
artifact), `spectrogram` (missing, different `def`, or `overlay_sha256` ≠ the current overlay's).
Presence per D-M2b-12; `Force` runs everything. Steps: work dir `incoming/ears-<uuidv7>/`, hard-link the
master; signal pass; token loops (artifact read through the Root, cap 64 MiB); spectrogram ladder (C50)
→ `Chmod 0444` → `Root.Link` to `derived/<aa>/<sha>.spectro.jpg` (`EEXIST` ⇒ identical content, reuse);
one write transaction with the C44 statements for everything that succeeded; `RemoveAll(work)`. A
failing analyzer is recorded in `Outcome.Failed` (`TOOL_FAILED`, `NPY_UNSUPPORTED`,
`SPECTROGRAM_GEOMETRY`, `SPECTROGRAM_TOO_LARGE`, `IO`) and does not block the others. Never modifies the
master.

**C51.2 Flags** (computed on read; each `{code, severity, evidence}`; order as listed):

| code | severity | rule | evidence |
|---|---|---|---|
| `truncated` | warn | `truncated_abc` or `truncated_semantic` = 1 | `{abc, semantic}` |
| `clipping` | warn | `clipping.clipped_frames ≥ 1` | `{clipped_s, spans (≤ 5)}` |
| `near_clipping` | info | no `clipping` and loudness `true_peak_dbtp > −1.0` | `{true_peak_dbtp}` |
| `internal_silence` | warn | an internal silence span ≥ 2.0 s | `{spans}` |
| `long_leading_silence` | info | `leading_s ≥ 3.0` | `{leading_s}` |
| `long_trailing_silence` | info | `trailing_s ≥ 5.0` | `{trailing_s}` |
| `band_limited` | info | `bandwidth_hz < 16000` (M1 spectrum) | `{bandwidth_hz}` |
| `token_loop_suspected` | warn | a stream with `periodic_run.span_s ≥ 6.0` and `repeats ≥ 3`, or ≥ 3 gzip windows and `gzip_ratio_min ≤ 0.5 · gzip_ratio_median` | `{stream, at_s, span_s, period_s}` or `{stream, window_at_s, ratio, median}` |
| `duration_mismatch` | info | timed and `abs(duration / expected − 1) > 0.15` | `{duration_s, expected_s}` |

**C51.3 Blind spots** (D-M2b-21; every analysis payload and `/context` `perception` carries
`blind_spots: string[]` from this one function):
```go
// internal/analysis/blindspots.go (M2b-B2)
type BlindSpotFacts struct {
    TierA         bool     // every applicable Tier A analyzer present (C51.4)
    HasSemantic   bool     // the take has a semantic.npy artifact
    Timed         bool
    SectionSource string   // "plan" | "measured" | "manual" | "" (untimed)
    Confidence    *float64
    Spectrogram   bool     // the payload is or embeds a spectrogram view
}
func BlindSpots(f BlindSpotFacts) []string
```
Always, in this order (the first five are M2a C35's wording, unchanged): `"timbre"`, `"emotional
delivery"`, `"mix balance beyond loudness"`, `"hook catchiness"`, `"diction and intelligibility"`,
`"melody and harmony adherence to the score"`. Then, when applicable, in this order: `"silence, clipping
and token loops (Tier A not run yet)"` (`!TierA`); `"token loops (no semantic.npy for this take)"`
(`TierA && !HasSemantic`); `"section times are score-plan estimates (confidence <c>)"` (timed from
`plan`, `<c>` formatted `%.1f`); `"section times unknown (no aligned score)"` (untimed); `"a spectrogram
shows energy over time and frequency, not pitch accuracy or words"` (`Spectrogram`).

**C51.4 Dossier** (`get_track_analysis`). **Row selection:** for each analyzer (M1 `loudness`,
`spectrum`; M2b `silence`, `clipping`, `token_loops`) use the row with the newest `created_at` among
those whose `version` starts with the analyzer's **current** definition prefix (ties: the greatest
`version`); rows of other definitions are ignored. (Rev 3: `library.trackExtras` — M2a's `perception` input
— takes the last row by `created_at, version` per analyzer *without* the prefix test; the two agree
whenever only the current definition is stored, which `TestPerception_MatchesM2aContext` pins.) **Tier:** the applicable Tier A analyzers are
`silence`, `clipping`, `spectrogram` (a rendition with the current `def`) and `token_loops` iff the take
has a `semantic.npy` artifact; `tier` is `"A"` iff all of them are present, else `"0"`; `complete` =
`tier == "A"`; `missing` lists the absent ones.
```
Dossier {track_id, tier: "A"|"0", complete, missing: [analyzer],
  summary (≤ 300 chars, deterministic: "−11.2 LUFS, true peak −0.4 dBTP, bandwidth 15.8 kHz; 2 warnings: clipping 2.1 s at 1:12; …"),
  loudness: M1 Loudness + {replaygain_track_gain_db, version} | null,
  spectrum: {bandwidth_hz, version} | null,
  silence: Silence + {version} | null,
  clipping: Clipping + {version} | null,
  token_loops: {artifact_sha256, dtype, shape, time_axis, streams: Stream[], version} | null,
  structure: {duration_s, timed, source, confidence, expected_duration_s|null, ratio|null,
              sections: [{idx, label, kind, start_s|null, end_s|null}]},
  flags: Flag[], blind_spots: string[],
  spectrogram: {available, bytes|null, width|null, height|null, quality|null},
  provenance: {analyzers: {name: version}}}
```
Every `| null` object above is a nullable object type in OpenAPI (C53).

**C51.5 Spectrogram view** (`get_track_spectrogram`): `{track_id, content_type:"image/jpeg", sha256,
bytes, width, height, quality, plot:{x0, y0, width, height, duration_s, freq_scale:"log"}, overlay:
{timed, source, confidence, line_timing:"none"}, url, expires_at, ttl_s, blind_spots, image?:
{mime_type:"image/jpeg", data_base64}}`; `image` only when `inline=true` (default). Missing rendition ⇒
409 `ANALYSIS_UNAVAILABLE` `{analyzer:"spectrogram", hint:"ask the operator to run analysis"}` (the same
hint wherever `ANALYSIS_UNAVAILABLE` appears); the file is missing ⇒ 500 `INTERNAL`.

**C51.6 Compare** (`compare_tracks`):
```
Comparison {a: Side, b: Side,
  relation: "siblings"|"parent_child"|"child_parent"|"same_lineage"|"unrelated",
  deltas: {integrated_lufs, true_peak_dbtp, lra_lu, bandwidth_hz, duration_s, clipped_s,
           internal_silence_s, token_loop_span_s} (b − a; null when either side lacks the metric),
  flags: {only_a: [code], only_b: [code], both: [code]},
  recipe_diff: {same_hash, changed: ["seed"|"style"|"lyrics"|"cot"], seed: {a, b},
                style_terms: {added, removed}},
  duels: PairStats, ladder: {a: {u, sd, n_duels}|null, b: {u, sd, n_duels}|null, nu, p_a_beats_b|null, p_tie|null},
  blind_spots}
Side {track_id, title, seed, request_id, root_track_id, duration_s, tier, flags: [code]}
```
- **Relation** (D-M2b-20, from `TrackRef.ParentTrackID`/`ParentOperator`/`RootTrackID`, first match):
  `b.parent = a` ⇒ `siblings` if `b`'s operator is `inferred_sibling`, else `parent_child`;
  `a.parent = b` ⇒ `siblings` if `a`'s operator is `inferred_sibling`, else `child_parent`;
  `a.parent = b.parent ≠ ""` and both operators `inferred_sibling` ⇒ `siblings`; same root ⇒
  `same_lineage`; else `unrelated`.
- **Delta definitions:** `integrated_lufs`, `true_peak_dbtp`, `lra_lu` from M1 loudness; `bandwidth_hz`
  from M1 spectrum; `duration_s` from the track; `clipped_s` = `clipping.clipped_s`;
  `internal_silence_s` = `silence.internal_s` (C49.1); `token_loop_span_s` = the maximum over streams of
  `periodic_run.span_s` (null when either side has no `token_loops` row). All rounded to 2 decimals.
- `a == b` ⇒ 422 `COMPARE_SAME_TRACK`. `p_a_beats_b`/`p_tie` = `ladder.PBeats(u_a, u_b, ν)` from the
  global fit (null unless both have counted duels).

**C51.7 CLI and capabilities.** `cmd/metamusic/cmd_analyze.go` (rev 3: the repo's CLI shape is
`func cmdAnalyze(ctx context.Context, args []string, getenv func(string) string, s streams) error`, parsed
with a `newFlags` FlagSet, dispatched from the `switch` in `run()` in `main.go`, errors as `usageError` /
`exitError{code, err}`; config via `config.LoadOffline` so no `MM_AUTH` is needed in `docker exec`; the
`main.go` dispatch case, the `usage` text and a stub are added by M2b-B2, M2b-E3 replaces only
`cmd_analyze.go`): `metamusic analyze [--track ID]… [--force] [--dry-run] [--jobs N] [--json]` —
default: all tracks, missing/stale analyzers only. `--dry-run` opens the DB with `store.OpenReadOnly`
(M1) and prints what would run. Exit codes: 0 ok; 1 any track failed; 2 usage/fatal (incl. a missing
Tier A capability, named). A run that wrote anything appends one audit row through `audit.Store.Insert`
exactly like `cli:restore` in `internal/backup/restore.go` (`Principal: auth.CLI` ⇒ key `system:cli`,
`Transport: reqinfo.Transport("cli")`, `Op: "cli:analyze"`, `Method: "CLI"`, `ArgsSHA256` of the flags);
`--dry-run` writes none. `import` and `seed-synthetic` call `Backfill` (missing only) after a successful
apply and print `Tier A: N analysed, M failed (run "metamusic analyze" to retry)` — or `Tier A:
unavailable (ffmpeg lacks <capability>)` — and their exit codes are unchanged (D-M2b-10). `serve` logs
one info line `tier_a_missing=<n>` at start (and `tier_a_unavailable=<capability>` when Tier A cannot
run).

**Shared media constructor (rev 3).** `app.Build` has no access to ffmpeg, and the CLI commands never
call it, so the clip and Ears services come from one function in `internal/app/media.go`:
```go
type MediaOptions struct { // nil in production ⇒ NewMedia probes cfg.FFmpeg itself
    Window       clip.WindowAnalyzer; Render clip.RenderFunc; ClipsMissing error // clip inputs
    TierA        ears.TierA;                                  TierAMissing error // ears inputs
}
type Media struct{ Clips *clip.Service; Ears *ears.Service; ClipsMissing, TierAMissing error }
func NewMedia(ctx context.Context, cfg config.Config, st *store.Store, data *os.Root, lib *library.Service,
    signer *serve.Signer, now func() time.Time, pinned func(context.Context) (map[string]struct{}, error),
    mo *MediaOptions) (*Media, error)
// app.Options gains `Media *MediaOptions` (passed through; servertest sets it for Media:false rigs).
// app.Services gains `Clips *clip.Service; Duels *duels.Service; Ears *ears.Service`.
```
With `mo == nil`, `NewMedia` runs `ffmpeg.New(ctx, mediatool.Tools{cfg.FFmpeg, cfg.FLAC, cfg.Metaflac})`:
a failure (binary absent, M1 requirements missing) is **not** an error — both `…Missing` fields carry it
and the services are nil/`Missing`-guarded; otherwise `RequireClips()` / `RequireTierA()` each run once.
`signer` may be nil (the CLI): Ears' link-minting ops then answer 503. `Build` calls `NewMedia`, builds
`duels.New`, calls `ears.UseJudgments(duels)` and `trackctx.UseM2b(ears, duels)`; `cmd_import.go`,
`cmd_seed.go` and `cmd_analyze.go` call `NewMedia` with `pinned == nil` and no judgments.

`ffmpeg.New` (M1 C14) keeps its signature and requirements (it still requires only `ebur128` and
`libmp3lame`). Rev 3: it currently runs `-filters` and `-encoders`, matches the two names and **discards**
the output; M2b-B2 makes it retain both listings in unexported fields and adds
(`internal/analysis/ffmpeg/capabilities.go`, recorded in the M1 plan):
```go
func (f *FFmpeg) RequireClips() error // filters atrim, asetpts, afade, aresample, volume, ebur128, ametadata; encoder flac
func (f *FFmpeg) RequireTierA() error // filters asetnsamples, astats, silencedetect, ametadata, aformat, showspectrumpic; encoder png
// ErrToolMissing naming the first missing capability (wraps analysis.ErrToolMissing).
```
`app.NewMedia` calls both once: `RequireClips` failing ⇒ no clip service (clip-dependent ops 503
`clips`); `RequireTierA` failing ⇒ `ears.Options.Missing` set (Tier A reports `TOOL_MISSING` once; the
read-only Ears ops keep working from the DB). Both are logged once at start through `slog` (never
`slog.SetDefault`).

**C51.8 `/context` integration (M2a C35 amended; M2b-B3 deliverable).**
```go
// internal/ears
type Perception struct { // keys and order exactly M2a C35's perception block
    Tier        string              `json:"tier"`         // C51.4
    LUFSI       *float64            `json:"lufs_i"`
    TruePeakDB  *float64            `json:"true_peak_db"`
    LRALU       *float64            `json:"lra_lu"`
    BandwidthHz *float64            `json:"bandwidth_hz"`
    Sections    []PerceptionSection `json:"sections"`
    Timed       bool                `json:"timed"`
    Flags       []string            `json:"flags"`        // C51.2 codes, in order
    BlindSpots  []string            `json:"blind_spots"`  // C51.3
}
type PerceptionSection struct {
    Label      string   `json:"label"`
    Start      *float64 `json:"start"`
    End        *float64 `json:"end"`
    Source     string   `json:"source"`
    Confidence *float64 `json:"confidence"`
}

// internal/trackctx (small interfaces; trackctx imports only ears and ladder types)
type PerceptionSource interface {
    Perception(ctx context.Context, trackID string) (ears.Perception, error) // *ears.Service
}
type StrengthSource interface {
    Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error) // *duels.Service
}
func (b *Builder) UseM2b(p PerceptionSource, s StrengthSource) // setter in the style of UseScoreABC; app.Build wires both (nil ⇒ M2a behaviour)
```
- `perception` = `ears.Perception(track)`: `lufs_i`, `true_peak_db`, `lra_lu`, `bandwidth_hz`,
  `sections` and `timed` are computed exactly as M2a's trackctx computed them (M2b-E3 reproduces that
  code in `ears`, checked by `TestPerception_MatchesM2aContext`; M2b-B3 switches trackctx to
  `ears.Perception` and deletes trackctx's copy), so those keys' golden values do not change; `tier`,
  `flags` and `blind_spots` now come from Ears. M2a's own `blindSpots` package variable in `trackctx.go` is deleted.
- `judgments.ladder` = `{u, sd, n_duels}` of the track from `Strengths` (global fit, human-source
  verdicts only), `null` without counted duels; `lineage.children[].ladder` = `{u, sd}` likewise.
- `_links.spectrogram` = the absolute signed `/media/<sha>.jpg` URL (`TTLFor(principal)`, `PublicURL`)
  of the track's spectrogram rendition, or `null`; placed after `_links.stream` (rev 3: M2b-B3 adds
  `"spectrogram"` to `order["_links"]` in `trackctx.go`: `stream, spectrogram, comments, track`). Signing:
  `ttl, bucket := serve.TTLFor(p)`; `Signer.Sign(sha, "jpg", ttl, bucket)`.
- Budget: `Fit`'s trimming order is unchanged (children drop with their ladders). Rev 3:
  `trackctxtest.WorstCase` seeds 50 children whose `ladder` is `null`, and `TestFit_WorstCase` (which also
  pins `Budget.Truncated == [comments children lyrics]`) stays unmodified and green; M2b-B3 adds
  `TestFit_WorstCaseWithLadders` in a new file (a fake `StrengthSource` gives every child a ladder; still
  ≤ 24 000 UTF-16 units) and updates `testdata/context.golden.json` (`TestContext_Golden`: new
  `blind_spots` entry, `_links.spectrogram`, `perception.tier`/`flags`).

### C52. Registry and MCP image results

`api.Meta` (M2a C28 form, `internal/api/registry.go`; M0 C4 amended by M2b-B2) gains, after
`SecretFields`:
```go
MCPImage bool `json:"mcp_image,omitempty"` // the 2xx body has image {mime_type, data_base64 (optional)}
```
```go
type ImageData struct { // internal/api
    MIMEType   string `json:"mime_type"`
    DataBase64 string `json:"data_base64,omitempty"` // not required in the schema
}
```
`Register[I, O]` validates `MCPImage` **by reflection on `O` at Register time** and panics when it is set
on a non-GET op, on an op with `MCP` = `exclude:*`, or when `O` has no `Body` field whose struct carries
a field with JSON name `image` of type `*api.ImageData` (`TestRegister_MCPImageValidation`).
`TestRegister_MCPImageShape` checks the generated schema: `image.mime_type` required,
`image.data_base64` present and not required.

`internal/mcpgen` (rev 3: `toolSpec` in `schema.go` gains `Image bool` from `x-mm.mcp_image`; `serve()` in
`dispatch.go` converts the result, so both the first-class tool and `call_operation` — over HTTP and
stdio, which share `dispatch` — get it; the new code lives in `image.go`): for an op with
`mcp_image`, after a 2xx response whose `image` object has `mime_type` and `data_base64`: decode
(StdEncoding; failure ⇒ `IsError` `INTERNAL`); decoded size > 100 000 ⇒ `IsError` `IMAGE_TOO_LARGE`;
result `Content = [ImageContent{Data, MIMEType}, TextContent(JSON without data_base64)]`,
`StructuredContent` = body without `image.data_base64`. Without `image` the normal path applies.
`describe_operation` adds `"image_result": true`.

**Parity rules for image ops** (part of the M2a parity runner, on the REST, MCP and **stdio** rigs):
REST JSON minus `image.data_base64` equals `StructuredContent`; `ImageContent.Data` equals the decoded
REST base64; the `TextContent` JSON equals `StructuredContent`; the serialized tool result is ≤ 140 000
bytes. For every op (image or not), every MCP `StructuredContent` must validate against the tool's
`outputSchema` (`github.com/google/jsonschema-go`; MCP clients such as the TypeScript SDK reject results
that do not). Rev 3: `runParity` keeps its signature but its `servertest.Options` gains `M2b:
servertest.M2bOptions{Fixture: true}`; `submit_duel` is not replayable through `call_operation` (a replay is
200 `replayed:true`, not the first 201), so its case sets `SkipGeneric`; ids drawn from the rig sequence
(`id`, `created_by`, verdict ids) are `Masks`; the stub-phase cases are `stubCases` entries with `Status: 501`.

### C53. Operations (all `Cost: "free"`; JSON snake_case; types in `internal/api/types_m2b.go`)

Legend (M2a C38): **C** = `idempotency: create` (REST requires `Idempotency-Key`; MCP derives it),
**H** = `rate: heavy`. The `MCP` column is the target; D-M2b-22 may demote some `tool` entries to `generic`
when M2b-B2 measures `tools/list`.

| OperationID | Method + path | Scope | MCP | UI | Flags | Output |
|---|---|---|---|---|---|---|
| `get_clip` | GET `/api/tracks/{id}/clip` | read | generic | `none:clip links reach the SPA inside duel responses` | H | `ClipLink` |
| `create_duel` | POST `/api/duels` | write | tool | `/duels` | C, H | 201 `Duel` |
| `next_duel` | GET `/api/duels/next` | read | tool | `/duels` | | `{duel: Duel\|null, open_total}` |
| `get_duel` | GET `/api/duels/{id}` | read | generic | `/duels/:id` | | `Duel` |
| `list_duels` | GET `/api/duels` | read | generic | `/duels` | | `{items: DuelSummary[], total, next_cursor}` |
| `submit_duel` | POST `/api/duels/{id}/verdicts` | write | tool | `/duels` | | 201/200 `{duel: Duel, verdict: Verdict, replayed}` |
| `get_ladder` | GET `/api/ladder` | read | tool | `/ladder` | | `LadderPage` |
| `get_track_analysis` | GET `/api/tracks/{id}/analysis` | read | tool | `/tracks/:id` | | `Dossier` |
| `get_track_spectrogram` | GET `/api/tracks/{id}/spectrogram` | read | tool (`mcp_image`) | `/tracks/:id` | | `SpectrogramView` |
| `compare_tracks` | GET `/api/compare` | read | tool | `/compare` | | `Comparison` |

(`/api/duels/next` is a literal segment; Go 1.22+ `ServeMux` prefers it over `/api/duels/{id}`.)
Rev 3: `submit_duel` answers 201 for a new verdict and 200 for a replay. No M1/M2a handler does this yet;
Huma v2.39.1 supports it through a `Status int` field on the output struct (read in `huma.go` next to
`Body`), with `created()` giving the documented default 201 and the replay path setting `Status: 200`
(M2b-B2 also adds the 200 response to the operation's `Responses` so `openapi.json` is truthful).
`submit_duel` is not a create op (D-M2b-17); M2a's `TestOps_CreateOpsDeclareIdempotencyKey` therefore
expects the header on `create_duel` only.

Summaries/descriptions are written for LLM readers; binding sentences: `submit_duel` — "Verdicts from
agents are stored as critic opinions and never change the human ladder."; `get_ladder` — "u is a
log-strength on a scale anchored at the virtual 'reject' item (0); compare u values, not
probabilities of acceptance."; `get_track_spectrogram` — "Returns the image as MCP ImageContent; never
audio."; `create_duel` — "A replayed call returns the first result; call get_duel for fresh clip
links."; every Ears op mentions `blind_spots`.

**Inputs.**
- `get_clip` query: `from_s`, `to_s` (seconds; optional), `match_lufs` (optional, −40…−6; attenuates
  only, D-M2b-2). Huma v2.39 panics on pointer query parameters, so optional numbers use the existing
  `optParam[float64]` (`internal/api/types_m1.go`: `Value`, `IsSet`, `.ptr()`; rev 3: no `OptFloat` and no
  `params.go`; it is a generic *type*, not a generic method).
- `create_duel`: `api.IdemHeader` (M2a C28) + body `{a_track_id, b_track_id, blind?: boolean,
  from_s?: number, to_s?: number, match_loudness?: boolean}`. **`blind` and `match_loudness` are
  `*bool` without a Huma `default` tag** (nil ⇒ true in the service; the `doc` text says "default
  true"): verified in Huma v2.39.1, `setDefaults` overwrites zero values, so a plain `bool` with
  `default:"true"` would turn an explicit `false` into `true` and silently break non-blind "A/B with
  parent" and `match_loudness=false` (`TestCreateDuel_ExplicitFalseHonoured`). Optional body fields
  are pointers with `required:"false"`. There is no `note` field (D-M2b-19).
- `next_duel` query: `exclude` (`query:"exclude,explode"`, ≤ 50 ids).
- `list_duels` query: `state` (explode; `open|decided|expired`), `track_id`, `root_track_id`, `mode`
  (`any` (default) | `blind` | `sighted`), `limit` 1…100 (default 50), `cursor`. Order `created_at DESC,
  id DESC`. Filters include unrevealed duels (D-M2b-4).
- `submit_duel` body: `{verdict: "a"|"b"|"tie"|"both_bad", note?: ≤ 2000 (trimmed; empty ⇒ null),
  latency_ms?: 0…86 400 000, switches?: 0…100 000}`. No `source` field exists.
- `get_ladder` query: `scope` = `global` (default) | `lineage`; `root_track_id` (required for lineage,
  else 422); `include_sighted` (default false); `limit` 1…500 (default 200); `cursor`.
- `get_track_spectrogram` query: `inline` (default true). Query defaults apply only when the parameter
  is absent, so `inline=false` is honoured (covered by `TestGetTrackSpectrogram_InlineAndLink`).
- `compare_tracks` query: `a`, `b` (required track ids).
- Every list response obeys M1's 90 000-byte page cap.

**Shapes.**
```
ClipLink {track_id, url, key, content_type:"audio/flac", expires_at, ttl_s, bytes, sample_rate:48000,
  bits_per_sample:24, channels, from_s, to_s, duration_s, gain_db, window_lufs|null, window_tp_dbtp|null,
  target_lufs|null, match: "off"|"matched"|"limited"|"window_silent"}
Duel {id, state, blind, revealed, created_at, expires_at, created_by_kind,
  window: {from_s, to_s, duration_s}, loudness_matched (match ∈ {matched, limited}),
  slots: {a: Slot, b: Slot},
  my_verdict: Verdict|null, human_verdict: Verdict|null, critic_verdicts: Verdict[]}   (visibility: C48.3)
Slot {clip: {url, expires_at, ttl_s, content_type:"audio/flac", bytes, duration_s},
  track: {id, title, seed, request_id, root_track_id, duration_s}|null,      (null while unrevealed)
  gain_db|null, window_lufs|null}                                           (null while unrevealed)
Verdict {id, source: "human"|"critic", principal_kind: "human"|"device"|"agent", verdict,
  winner_track_id|null (null while unrevealed), note|null, latency_ms|null, switches|null, saw_reveal,
  created_at}
DuelSummary {id, state, blind, revealed, created_at, expires_at, window, loudness_matched,
  slots: {a: {track|null}, b: {track|null}}, my_verdict: string|null,
  human_verdict: {verdict, winner_track_id}|null (only when revealed), critic_verdicts: int}
LadderPage {scope, root_track_id|null, include_sighted, generated_at,
  model: {kind:"btd", prior_sd:1.5, nu_prior:"gamma(2,2)", anchor:"reject", anchor_u:0, nu, iterations, converged},
  counts: {verdicts_human_counted, verdicts_critic_excluded, verdicts_sighted_excluded, both_bad, ties, tracks},
  diagnostics: {slot_a_win_rate|null, n_ab},
  items: [{rank, track_id, title, root_track_id, seed, u, sd, ci95:[lo,hi], n_duels, wins, losses, ties,
           both_bad, provisional}],
  total, next_cursor}
NextDuel {duel: Duel|null, open_total}
```
**Nullable objects in OpenAPI.** Huma does not mark pointer-to-struct fields nullable, and
`nullable:"true"` on an object field panics (re-verified on v2.39.1). Rev 3: the repo's convention is a
`huma.SchemaProvider`: a method-less shadow type renders the object inline with `Nullable = true` through a
generic helper `nullableObject[T]` (copies in `internal/library/schema.go`, `judgments`, `backup`; M2b-B2
adds one in `internal/api`). Every object marked `|null` above and in C51 (`Slot.track`,
`my_verdict`, `human_verdict`, `DuelSummary.slots.*.track`, `DuelSummary.human_verdict`, `NextDuel.duel`,
`Comparison.ladder.a`/`.b`, the dossier blocks `loudness`, `spectrum`, `silence`, `clipping`,
`token_loops`) uses a dedicated `…OrNull` type in `types_m2b.go` that embeds nothing and implements
`Schema(r huma.Registry)` that way, referenced through a pointer without `omitempty` (always present,
required) — the same shape as `library.Truncation` / `ScoreInfo`. Scalar `|null`
fields are pointers with `nullable:"true"` where Huma does not already mark them.
`TestOpenAPI_M2bNullableObjects` walks `openapi.json` and asserts every `|null` of C51/C53 is nullable
and every other field is not; otherwise `schema.d.ts` would lie and MCP clients that validate
`structuredContent` against `outputSchema` would reject results.

**JSON tags.** Every exported field of every struct reachable from an M2b response type (including
`analysis`, `tokens` and `ladder` types) has an explicit `json` tag (`TestJSONTags_M2bPayloads`,
reflection); Go field names never reach REST or MCP.

Errors: 404 `NOT_FOUND`; 409 `DUEL_EXPIRED`, `DUEL_ALREADY_JUDGED`, `ANALYSIS_UNAVAILABLE` (plus the
gate's `IDEMPOTENCY_CONFLICT`/`IDEMPOTENCY_IN_PROGRESS` on `create_duel`); 422 `VALIDATION_FAILED`,
`DUEL_SAME_TRACK`, `CLIP_WINDOW_INVALID`, `CLIP_UNSUPPORTED_CHANNELS`, `COMPARE_SAME_TRACK`; 428
`IDEMPOTENCY_KEY_REQUIRED` (REST `create_duel` without a key); 429 `RATE_LIMITED`; 501
`NOT_IMPLEMENTED` (`STUB(M2b-…)` services during Stage 1–2 only; none at X1); 503 `UNAVAILABLE`
`{component: "duels"|"ears"|"clips"}`.

**Deps and wiring (M2a C28/C39 pattern).** `api.Deps` gains concrete pointers; registration never
dereferences them; a nil dependency answers 503 `UNAVAILABLE` `{component}`:
```go
Duels *duels.Service; Ears *ears.Service; Clips *clip.Service
```
`m2bErr(err)` (`internal/api/errors_m2b.go`; rev 3: the M2a-style per-domain mapper, falling back to
`libErr`) maps service errors in one place: `*problem.Error` ⇒ `api.Err` with the same fields;
`library.ErrNotFound` ⇒ 404 `NOT_FOUND` (via `libErr`); `errors.ErrUnsupported` (a Stage-1 stub) ⇒
`api.Err(501, "NOT_IMPLEMENTED", …, {"task": …})`; anything else is returned unchanged and becomes a 500
`INTERNAL` whose cause the Huma error hook (`newProblem`) logs through `redact.Error`. `internal/app.Build` builds the three
services (C46 wiring, C51.7 `NewMedia` and capabilities) and fills `Deps` and `httpserver.Options` (which
gain the same three fields; `httpserver.New` copies them into `api.Deps`); the media resolver chain
becomes `serve.Chain(lib, exp, clips)` (today `serve.Chain(lib, exp)`; `Chain` skips a nil *interface*, so
`Build` passes `clips` only when the service exists — never a typed-nil `*clip.Service`). M0 C4/C6, M1
C20 and M2a C28/C39 are amended by M2b-B2.

**GET never mutates.** `TestGETNeverWritesDB_M2b` runs every M2b GET op over REST **as the proxy
human** (whose reads write nothing under M2a: no audit row for human reads, no token or device touch)
and asserts that `PRAGMA data_version` seen from a second connection and a checksum of every table are
unchanged (clip cache files may appear, D-M2b-1). It then repeats the GETs as a PAT agent and as a paired
device and asserts the table checksum excluding exactly the M2a side effects of those principals —
`audit_log` rows, `api_tokens.last_used_at` (agent), `devices.last_seen_at`/`devices.expires_at`
(device); `data_version` is not used for those runs.

### C54. Web (`web/`)

Routes (rev 3: entries in `APP_ROUTES`, `web/src/lib/router.ts`, with `router.test.ts` updated; components
imported in `web/src/App.svelte`, which also holds the nav links): `/duels` Duels · `/duels/:id` Duel (plays an open duel; shows the reveal of a
decided one and an expired notice otherwise) · `/ladder` Ladder (query `root`) · `/compare` Compare
(query `a`, `b`); `/tracks/:id` (M2a C40) gains an Ears panel and a Lineage-duels block. Nav gains
"Duels" (badge = `list_duels?state=open&limit=1` → `total`; never `next_duel`, which renders clips) and
"Ladder".

```
web/src/
  api/media.ts                      fetchClip(url, signal) → ArrayBuffer (same-origin /media/ only; cache:"force-cache"; 403 ⇒ ClipLinkExpired, 404 ⇒ ClipGone; the ESLint network ban is already off under src/api/**)
  lib/ab.ts                         A/B engine (C54.1), DOM-free
  lib/audioContext.ts               one shared AudioContext (lazy; latencyHint "interactive", sampleRate 48000 with fallback)
  lib/duelKeys.ts                   keyboard map (C54.2)
  lib/duelRound.ts                  pure: blind-round pairs and order (injected RNG)
  lib/duelView.ts                   pure: Duel/DuelSummary → view model (slot labels, reveal rows, state)
  lib/ladderView.ts                 pure: LadderPage → rows (rank order as served)
  lib/ladderScale.ts                pure: u/ci → pixel positions
  lib/earsView.ts                   pure: Dossier/SpectrogramView → panel model (flags first, metrics, blind spots)
  lib/compareView.ts                pure: Comparison → delta rows (sign), flag diff, recipe diff
  lib/spectroSeek.ts                pure: click (x, rendered width, natural width, plot) → seconds
  lib/fixtures/m2b/*.json + index.ts  typed fixtures for Duel, LadderPage, Dossier, SpectrogramView, Comparison (M2b-B2)
  lib/components/ABPlayer.svelte    load/decode progress, Play/Pause, big A/B toggle, position bar, key help
  lib/components/VerdictBar.svelte  A better · B better · Tie · Both bad · note · Submit
  lib/components/DuelCard.svelte    ABPlayer + VerdictBar + skip; Reveal after submit
  lib/components/DuelReveal.svelte  titles/seeds per slot, winner, gains, links (compare, ladder, next)
  lib/components/NewDuelForm.svelte two track pickers (search_tracks), blind, window, match
  lib/components/LadderTable.svelte rank, title, u ± 1.96 sd whisker around the reject line, W-L-T-BB, provisional
  lib/components/EarsPanel.svelte   flags (severity), metrics, blind spots, spectrogram
  lib/components/Spectrogram.svelte <img> from the signed URL; click ⇒ jumpTo(trackId, t) (M2a C40.2)
  lib/components/LineageDuels.svelte lineage ladder + "Blind round" + "A/B with parent" + "Compare with parent"
  routes/{Duels,Duel,Ladder,Compare}.svelte
```

**C54.1 A/B engine** (`lib/ab.ts`):
```ts
export const CROSSFADE_S = 0.008;
export const START_LEAD_S = 0.05;
export type Slot = "a" | "b";
export interface AB {
  readonly active: Slot; readonly playing: boolean; readonly switches: number;
  play(offsetS?: number): void;   // start both sources
  pause(): Promise<void>;         // ctx.suspend() — both stop on the same clock
  resume(): Promise<void>;        // ctx.resume()
  select(s: Slot): void; toggle(): void;
  seek(offsetS: number): void;    // restart both at the same when/offset
  position(): number;             // seconds within the loop
  dispose(): void;                // stop + disconnect sources (the shared context stays open)
}
export function createAB(ctx: BaseAudioContextLike, a: AudioBufferLike, b: AudioBufferLike): AB;
```
- `|a.duration − b.duration| > 0.001` ⇒ throws `ClipLengthMismatch`; `loopEnd = min(a.duration,
  b.duration)`.
- `play(o)`: new `AudioBufferSourceNode` per slot (`loop = true`, `loopStart = 0`, `loopEnd`), each
  through its own `GainNode` to `destination`; active gain 1, other 0; `when = ctx.currentTime +
  START_LEAD_S`; `srcA.start(when, o)` and `srcB.start(when, o)` with **identical** arguments.
- `select(s)` (s ≠ active): `t = ctx.currentTime`; for both gains `cancelScheduledValues(t)` then
  `setValueAtTime(gain.value, t)`; outgoing `linearRampToValueAtTime(0, t + CROSSFADE_S)`, incoming
  `linearRampToValueAtTime(1, t + CROSSFADE_S)`; `switches++`. (No `cancelAndHoldAtTime`: Firefox lacks
  it.)
- **iOS gesture rule:** the Play click handler calls `ctx.resume()` **synchronously, before any
  `await`**, then `ab.play()`; no code path resumes the context outside a user gesture.
- The Duel page pauses THE `<audio>` through the M1 player store's existing `pause()` (rev 3: it is there
  already — "User pause: the only place playback intent is cleared"; `toggle()` resumes) before `play()`,
  and calls `dispose()` on navigation. M2b-W1 only adds a test.

**C54.2 Keyboard and touch.** `keyAction(e)` maps `a`→select A, `b`→select B, `Space`→toggle, `p`→
play/pause, `r`→restart at 0, `1`/`2`/`3`/`4`→choose A better/B better/Tie/Both bad (highlight only),
`Enter`→submit the chosen verdict. Ignored when `e.repeat`, a modifier is held, or the target is
`input`, `textarea`, `select` or contenteditable; handled keys call `preventDefault`. Touch: a
full-width toggle (`data-testid="ab-toggle"`, `aria-pressed`, shows "A"/"B"), explicit A and B buttons,
verdict buttons ≥ 48 px high. `latency_ms` = time from the first `play()` to submit; `switches` from
the engine.

**C54.3 Duels page flow.** On load: `next_duel` → DuelCard (clips fetched and decoded in the shared
context while suspended; Play resumes). Blind duels show "A"/"B" only. Submit → `submit_duel` → Reveal
(from the response). "Skip" → `next_duel?exclude=…` (ids kept in page state). "Next" → `next_duel`.
History: `list_duels` (paged) with links to `/duels/:id`. `/duels/:id` uses `get_duel` and the same
DuelCard for an open duel. A clip fetch answered **403 (expired link) or 404 (evicted clip)** ⇒
`get_duel` once to re-mint (re-rendering if needed), then retry once.

**C54.4 Track page.** Ears panel: `get_track_analysis` (flags first, then metrics, then blind spots) and
`get_track_spectrogram?inline=false` (image from `url`; a click maps x → t with `plot` via
`spectroSeek` and calls M2a's `jumpTo(trackId, t)`). Lineage duels:
`get_ladder?scope=lineage&root_track_id=<root>`. **"Blind round"** (disabled, with a hint, for lineages
with fewer than 2 tracks) creates blind duels for every pair of lineage tracks (≤ 6 tracks ⇒ all pairs;
more ⇒ 15 random pairs), in random order (`crypto.getRandomValues`), as sequential `create_duel` calls
with progress; each pair gets **one** `newIdempotencyKey()` (M2a C40, `idempotentAction` wraps exactly this)
that is reused on every retry of that pair (network error, 5xx, or a `RateLimited` rejection from
`web/src/api/client.ts` — which waits `retryAfterS`, shown as "waiting"; the human heavy bucket allows 6 at
once, then 12/min); then navigates to `/duels/<first created id>`. "A/B with
parent" = non-blind `create_duel(parent, this)` with its own key → `/duels/:id`; "Compare with parent"
→ `/compare?a=<parent>&b=<this>`.

**C54.5 Ladder and Compare pages.** Ladder: `get_ladder` (global, or lineage when `root` is set); the
whisker scale is centred on the reject line (u = 0); footer shows ν, iterations, converged, counts and
the slot-A win rate (a warning chip when outside [0.4, 0.6] with `n_ab ≥ 20`). Compare: `compare_tracks`
as a delta table (sign-coloured), flag diff, recipe diff, head-to-head and ladder line.

No raw HTML (`svelte/no-at-html-tags`, M1): titles, verdict notes and lyric lines render as text.

**C54 addendum (M3-B2):** the lineage page (`/lineage/:id`) is fed by `get_lineage` and reuses the "A/B with parent" and "Compare" actions;
`LineageNode.cost`, `operator`, `inferred`, `rationale` and the `recipe_known`/`lossy` flags are in the frozen schema (M3 C81).


### C55. Synthetic data and fixtures

`internal/synth` additions (production code, `mediatool.Tools`):
```go
type SpecialKind int // SpecialClipBursts | SpecialGap
func Special(ctx context.Context, t mediatool.Tools, dst string, kind SpecialKind, seconds float64) error
// ClipBursts: stereo 1 kHz sine at −12 dBFS; in [2.0, 4.0) s the amplitude is 2.0 and hard-clipped at ±1.0
//   (aevalsrc clip()); 24-bit/48 kHz FLAC. Gap: stereo 440 Hz sine at −14 dBFS with exact zeros in [4.0, 6.5).
type NPYOptions struct{ TokensPerS, Seconds float64; Vocab int; Seed uint64; LoopAtS float64; Period, Repeats int } // Repeats 0 ⇒ no loop
func SemanticNPY(dst string, o NPYOptions) error // '<i8', shape (1, n); uniform tokens from PCG(Seed); loop region repeats one random pattern
```
**Profiles e2e/dev** (C21 amended by M2b-B3) gain an `ears` group: three recipe lines appended to the
e2e `music/songs.jsonl` (same key order, separators and `ensure_ascii` as M1 C21; all three pass M2a's
batch-validator mirror) for `syn-ears-clip` (8 s, `music/flac/syn_ears_clip.flac`, ClipBursts),
`syn-ears-gap` (10 s, Gap) and `syn-ears-loop` (result dir `music/results/syn-ears-loop/` with a 12 s
tone, request.json (same request ⇒ same hash), result.json (truncated false/false), an aligned
`score.abc` of 3 sections × 2 bars at `Q:1/4=120` (12 s) and `semantic.npy` with 50 tokens/s, loop at
3.0 s, period 25, 12 repeats). New totals (M1 C21's 19/6/19 plus three): **22 recipes, 9 tracks, 22
drafts (13 backlog, 9 fulfilled), 1 sibling edge.** Rev 3: `synth.Expect` for the e2e profile in
`internal/synth/archive.go` (checked by `TestSynthArchive_E2EProfile`, `len(m.Audio)`) changes to Recipes /
RecipeSources 22, Tracks 9, Drafts 22, DraftsFulfilled 9, AudioFiles / AudioIdentities 9, Scores /
TimedScores 2 and a recomputed `LoudnessSpreadLU`; `ProfileDev` is the same archive. The `acceptance`
profile's `syn-song-05/semantic.npy` currently holds the **text** `npy-semantic` (not a valid array); it
becomes a loop-free `SemanticNPY` (counts and artifact names unchanged). `seed-synthetic` runs Tier A after
the import (C51.7).

**Expected Tier A results** (tolerances; used by `TestAcceptance_M2b_TierAFlags`):
`syn-ears-clip` → `clipping.clipped_s` 2.0 ± 0.2, one span [2.0, 4.0] ± 0.1, flag `clipping`;
`syn-ears-gap` → internal silence span [4.0, 6.5] ± 0.05, flag `internal_silence`; `syn-ears-loop` →
`periodic_run.period_tokens` 25, `period_s` 0.5, `span_s` ≥ 6.0, `at_s` 3.0 ± 0.1, `repeats` ≥ 12, flag
`token_loop_suspected`, structure timed with `expected_duration_s` 12.0 and `ratio` 1.00 ± 0.01; every
other synthetic take has none of `clipping`, `internal_silence`, `token_loop_suspected`.

**Test-only fixtures** (owner M2b-B2):
- `cliptest.Renderer` (a `duels.ClipRenderer`: deterministic `Measure` from a map; `Ensure` returns
  `Key(spec, "ffmpeg fake")` without a file; counts calls), `cliptest.Analyzer` (a `clip.WindowAnalyzer`:
  `ToolVersion` `"ffmpeg fake"`, `Measure` from a map) and `cliptest.Render` (a `clip.RenderFunc` writing
  a header-valid FLAC with the expected STREAMINFO through M1's `flactest`, so the real `*clip.Service`
  runs without ffmpeg).
- `duelstest.Fixture(tb, st, fx librarytest.Fixtures, now, newID) Duels` — inserts directly (after
  `librarytest.Fixture`/the cached template DB the rig copies; it is not part of the template), with
  **distinctive values** for redaction tests: two tracks `Blindtest Alpha`/`Blindtest Beta` (request ids
  `syn-blind`, `syn-blind-s7340022`, seeds 7340021/7340022, durations 61.3/66.6 s; inferred siblings),
  1 open blind duel between them (gains −737/0 cdB, window loudness −12.63/−20.00), 1 decided blind duel
  with a human `a` verdict (`human:` key) and a critic `b` verdict, 1 decided sighted duel with a device
  verdict, 1 expired duel; returns ids.
- `earstest.Fixture(tb, st, data *os.Root, fx, now) Ears` — Tier A analyses rows for two tracks (one with
  clipping + silence, one with token loops) and a spectrogram rendition whose file is a Go-generated
  1308×560 JPEG (gradient; < 100 000 bytes) written under `derived/`; `earstest.TierA` (fake analyzer:
  fixed `Signal` results; `SpectrumPicture` writes a Go-generated PNG of the exact C50.2 geometry) and
  `earstest.Judgments`.
- **`servertest` M2b options** (M2a C42 amended by M2b-B2; the rig builds the M2b services through
  `internal/app.Build` like `serve`):
  ```go
  type M2bOptions struct {
      Fixture   bool        // duelstest.Fixture + earstest.Fixture (+ the earstest JPEG); requires Options.Fixture
      Media     bool        // real ffmpeg for clips and Tier A (testtools.Require); false ⇒ app.Options.Media = &MediaOptions{cliptest.Analyzer, cliptest.Render, earstest.TierA} (no ffmpeg subprocess in the rig)
      Backfill  bool        // run ears.Backfill after the fixture/archive is loaded (rev 3: archivetest templates carry no Tier A rows; the registry-derived UI/leak tests need get_track_spectrogram to succeed)
      FakeClips bool        // duels use cliptest.Renderer instead of the clip service (Stage-2 lanes without a clip service)
      SlotCoin  func() bool // nil ⇒ servertest.Alternating(): false, true, false, … created fresh per rig, so parity rigs agree
      WrapClips func(duels.ClipRenderer) duels.ClipRenderer // optional spy between duels and its renderer (render counts)
      Override  func(*app.Services) // optional: replace the built Duels/Ears/Clips (or their Deps entries) with test instances before the server is mounted
  }
  // Options gains `M2b M2bOptions`. Rev 3: the rig already exposes `Rig.Services *app.Services`; Duels, Ears and
  // Clips are its new fields (`r.Services.Duels`, …), there are no separate Rig fields.
  ```
  Without a fixed slot coin the REST, MCP and stdio rigs would randomise slots differently and parity
  would diverge (`servertest.Alternating` is new code; `servertest` already has `Clock`, `IDSeed` and the
  `Limits` option the heavy-bucket tests use). M2a's clock rules (C42) apply: every M2b service built by `app.Build` reads `Rig.Clock`,
  and expiry, link-TTL and ladder `generated_at` tests advance that clock (never a synctest bubble).
- **Web fixtures** (M2b-B2): `TestWebFixtures_M2b` (`internal/api`, `-update`) serialises typed Go
  values of `Duel` (open blind and revealed), `LadderPage`, `Dossier`, `SpectrogramView` (without
  `image`) and `Comparison` into `web/src/lib/fixtures/m2b/*.json`, validating each against the op's
  response schema in `openapi.json`; `web/src/lib/fixtures/m2b/index.ts` exports them typed with the
  generated `components["schemas"]` types. Synthetic values only.
- `make fixture-db` writes a fixture **DataDir** (M1 + M2a fixture DB plus `duelstest` and `earstest`
  rows and the `earstest` JPEG under `derived/`) that `metamusic dev` can serve. Rev 3: today the target runs
  `go test ./internal/library/librarytest -run TestWriteFixtureDB` (metadata DB only) and `librarytest`
  cannot import `duelstest`/`earstest` (they import it). M2b-B2 adds `TestWriteFixtureDB_M2b` in
  `internal/servertest` (which may import all three) and changes the Makefile recipe to run both tests.

### C56. Tooling, CI, coverage, docs

- No new tools. `scripts/tools-selftest.sh` (M1; rev 3: today URL and tamper checks only) gains a section
  asserting the `RequireClips` and `RequireTierA` capabilities (C51.7) in the pinned ffmpeg; the Dockerfile
  `RUN` check (M1-I4, today `ebur128` + `libmp3lame` + `metaflac --version`) is extended for the runtime image.
- `coverage.thresholds` gains `internal/ladder 95`; `internal/...` stays 85.
- **Test tiers and CI time** (M1 C23; `make check` runs `go test -race -short` with the modernc race
  exemption): tests needing ffmpeg call `testtools.Require` (which skips under `-short`); the heavy
  `TestAcceptance_*` skip under `-short` through `testtools.Require` / `archiveRig` (there is no blanket rule),
  except two that need no tools: `TestAcceptance_M2b_BTParameterRecovery` (**3
  seeds per scenario under `-short`**, 20 otherwise — race instrumentation makes its float loops 5–10×
  slower under `make check`) and `TestAcceptance_M2b_CriticVerdictsExcluded` (fake renderer).
  `TestFit_TimeBudget` skips under `-short` and under the race detector (a build-tagged
  `raceEnabled` constant in `internal/ladder`). The slot coin is tested alone (`TestSlotCoin_Fair`:
  100 000 crypto draws, share within 0.5 ± 4σ = [0.4937, 0.5063]); duel-creation tests inject `Rand`.
  The spectrogram budget tests generate their white-noise inputs **once per package** (mono, 16-bit,
  48 kHz FLAC: 6 s, 16 s, 60 s, 360 s) instead of large stereo files per test.
- Forbidden import edges (C43) are asserted inside M1's `TestNoTestHelpersInBinary` (one `go list -deps`
  call).
- E2E: M2a's leak scanner (C39) already skips `/media/` bodies, so FLAC clips, MP3s and spectrograms are
  never buffered for scanning; there is no HAR recording, and any later `recordHar` must set a
  `urlFilter` excluding `/media/[0-9a-f]{64}\.(flac|mp3|jpg)`.
- **Privacy gate:** `scripts/public-gate.sh` forbids `*.npy`, `*.jpg`, `*.jpeg` (matched
  case-insensitively, like every forbidden glob) — real spectrograms carry lyric lines and `.npy` files are real model outputs; icons stay PNG.
  `scripts/public-gate-selftest.sh` gains rows (staged `x.jpg`, `x.JPEG`, `x.npy` blocked; `icon.png`
  passes).
- Docs: `docs/architecture.md` (M2b section, public-safe), `docs/device-checklist.md` (DC-M2b-1…5),
  `docs/runbook.md` (analyze + clip cache), README ("Duels and Ears" paragraph).
- **Golden files (rev 3):** the `-update` flag is declared per test package (`flag.Bool("update", …)` in
  `mcpgen`, `trackctx`, `library`, …); `internal/api` has none, so `TestWebFixtures_M2b` and the `duels` /
  `ears` golden tests declare their own in a new `_test.go` file.

---

## Acceptance mapping (roadmap M2b → named tests)

Each roadmap item in bold is proven by a `TestAcceptance_M2b_*` test that runs in CI (`make test` with
media tools; the first two also run under `make check`).

| Roadmap / brief item | Named test(s) |
|---|---|
| **BT parameter recovery on simulated voters** | `TestAcceptance_M2b_BTParameterRecovery` (scenarios a–d; 3 seeds per scenario under `-short`, 20 in `make test`) · `TestFit_*` unit tests |
| **Critic verdicts excluded from the ladder** | `TestAcceptance_M2b_CriticVerdictsExcluded` (PAT REST, PAT MCP and stdio critics; device verdicts counted) · `TestLadderInputs_HumanOnly` · `TestSourceFor` · `TestDuelsTriggers` (source/kind CHECK) · `TestContext_LadderHumanOnly` |
| **Spectrogram ≤ 100 KB** | `TestAcceptance_M2b_SpectrogramBudget` (REST, MCP, stdio; white noise 6/16/60/360 s among the signals) · `TestSpectrogram_BudgetWorstCase` · `m2b-ears.spec.ts › spectrogram is a JPEG ≤ 100 000 bytes` |
| parity for every op on REST, MCP and stdio | M2a parity runner cases for all 10 ops · `TestParityEveryOperation` (existing) · `TestParity_Stdio` (existing) · `TestParity_ImageOps` · `TestToolResultsConformToOutputSchema_M2b` · `TestOps_M2bRegisteredWithMeta` · `tools/list` golden and ceiling (D-M2b-22) |
| an agent performs every UI action; audit; no secret leak (M2a criteria extended) | `TestAcceptance_M2a_AgentPerformsEveryUIAction` (M2b `uiScript` rows) · `TestAcceptance_M2b_GateCoverage` (audit rows of `create_duel`/`submit_duel` for human, device and agent; M2b reads audited for agents only; replace of M2a's synthetic-op `TestAudit_CoverageMatrix`, which stays unchanged) · `TestNoSecretLeak` (M2b ops, `analyze` CLI) |
| idempotent `create_duel`, heavy clip ops, naturally idempotent `submit_duel` | `TestCreateDuel_ReplayDoesNotRender` (REST key and MCP derived key) · `TestCreateDuel_RESTWithoutKey428` · `TestOps_CreateOpsDeclareIdempotencyKey` (M2a) · `TestOps_M2bRegisteredWithMeta` (`rate`) · `TestClipLink_Op` (heavy bucket) · `TestSubmitDuel_ReplayAndConflict` |
| explicit `false` in bodies honoured | `TestCreateDuel_ExplicitFalseHonoured` (REST + MCP) |
| truthful OpenAPI / MCP schemas | `TestOpenAPI_M2bNullableObjects` · `TestJSONTags_M2bPayloads` · `TestToolResultsConformToOutputSchema_M2b` · `TestWebFixtures_M2b` |
| served-op coverage for every op | M0 teardown: all 9 M2b ui ops served (`get_clip` is `none:`) |
| clip endpoint: content-addressed 24-bit FLAC, 30–45 s, optional loudness match (attenuate only) | `TestAcceptance_M2b_ClipContract` · `TestClip_*` · `TestResolveWindow` · `TestMatchGains` · `TestTargetGain` · `TestClipLink_Op` (human and device 12 h, agent 15 min) |
| clip cache bounded, open duels pinned, renders detached, cache not an orphan | `TestClipCache_EvictsOldestUnpinned` · `TestOpenClipKeys` · `TestClip_CancelledCallerDoesNotFailWaiters` · `TestOrphans_IgnoreClipCache` |
| duel creation, blind/non-blind, slots randomised + logged | `TestAcceptance_M2b_DuelLifecycle` · `TestCreateDuel_BlindSlotsRandomisedAndLogged` · `TestSlotCoin_Fair` · `TestDuel_BlindRedaction` · `TestReveal_Rules` |
| human source = operator or paired device; source isolation in the schema | `TestSubmitDuel_SourceByPrincipal` (human, device, PAT, stdio, forged `source`) · `TestSubmitDuel_HumanThenDevice` · `TestDuelsTriggers` · `TestMigration0004_Applies` · `TestLadder_DeviceVerdictCounted` |
| next_duel / submit_duel (a/b/tie/both_bad + note + latency) | `TestNextDuel_QueuePerSource` · `TestSubmitDuel_*` |
| Web Audio synced A/B, 8 ms crossfade, keyboard + mobile toggle | `ab.test.ts` · `duelKeys.test.ts` · `m2b-duels.spec.ts › synced start and 8 ms crossfade` · `› keyboard flow` (desktop) · `› mobile toggle` (mobile) · DC-M2b-1 |
| e2e duel specs isolated under `fullyParallel` | every `m2b-duels.spec.ts` test creates its own duel; `› queue, skip and history` (serial, chrome); `› ladder` compares UI order with `get_ladder` |
| BTD ladder: weak Gaussian prior, MM, Laplace σ, per lineage + global, both_bad vs reject anchor | `TestFit_*` · `TestLaplace_MatchesNumericHessian` · `TestLadder_ScopeLineageVsGlobal` · `TestLadder_BothBadAnchor` · `TestLadder_TitlesNotCached` |
| Ears Tier A: silencedetect, astats clipping, token loops, spectrogram with overlay | `TestAcceptance_M2b_TierAFlags` · `TestParseSignal_Golden` · `TestTokenLoops_*` · `TestSpectrogram_*` |
| ffmpeg capability split (Tier A or clip filters missing never break import) | `TestFFmpeg_RequireCapabilities` · `TestFFmpeg_TierAMissingDoesNotBreakImport` · `TestAnalyze_TierAMissingReportedOnce` · `TestClipLink_Op` (clip capability missing ⇒ 503) |
| get_track_analysis / get_track_spectrogram (ImageContent) / compare_tracks | `TestDossier_*` · `TestGetTrackSpectrogram_*` · `TestMCP_ImageContent` · `TestCompareTracks_Deltas` · `TestCompareTracks_Relation` |
| blind_spots on every analysis payload, one source | `TestBlindSpots_Table` · `TestBlindSpots_EveryPayload` · `TestContext_PerceptionFromEars` |
| `/context` gains ladder, perception and spectrogram link | `TestContext_PerceptionFromEars` · `TestContext_LadderHumanOnly` · `TestContext_SpectrogramLink` · M2a `TestFit_WorstCase` (unchanged, green) · `TestFit_WorstCaseWithLadders` · M2a `TestContext_Golden` (updated) |
| GET never mutates | `TestGETNeverWritesDB_M2b` (proxy human + agent/device with M2a side effects excluded) |
| masters untouched by clips and analysis | `TestClip_MasterUntouched` · `TestAnalyze_MasterUntouched` |
| earlier milestones stay green with Tier A after import | `TestSeedSynthetic_E2EProfile` (updated) · `TestSynthEars_RecipesBatchValid` · M2a export acceptance (updated) · `internal/httpserver` `get_system_info` schema_version 4 |
| public repo never receives spectrograms or `.npy` files | `public-gate-selftest.sh` rows (`.jpg`, `.JPEG`, `.npy` blocked; `.png` passes) |
| [N] device checklist | `docs/device-checklist.md` DC-M2b-1…5 |

---

## Lanes and tasks

Stage 1 (sequential, branch `m2b` from `main` **with M2a merged**, main worktree,
`MM_LANE=m2b-found`): **M2b-B1 → M2b-B2**. Stage 1 lands every shared contract (schema,
library/serve/problem/textfold additions, service types and compile-ready stubs, capability methods,
operations, MCP image support, harness options and fixtures, `openapi.json`, `schema.d.ts`, web
fixtures), so the Stage 2 lanes compile and the web lane has the API types and data.
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from M2b-B2): **Judge** (`m2b-judge`:
M2b-J1 → M2b-J2), **Media** (`m2b-media`: M2b-M1), **Ears** (`m2b-ears`: M2b-E1 → M2b-E2 → M2b-E3), **Web**
(`m2b-web`: M2b-W1 → M2b-W2), **Infra** (`m2b-infra`: M2b-I1).
Stage 3 (`m2b-int`): **M2b-B3** (wiring, `/context`, synthetic profiles, cross-package acceptance) →
**M2b-W3** (Playwright) → **M2b-X1** integration → final review → **M2b-X2** deploy + backfill
(operator-assisted).
**File ownership** (M2a's rule): in Stage 2 no lane edits `internal/api/**` (M2b-B2 writes every M2b
handler complete; lanes replace service stubs only), `internal/app/**`, `internal/httpserver/**`,
`internal/servertest/**`, `internal/library/**`, `internal/mcpgen/**`, `internal/acceptance/**`,
`internal/trackctx/**`, `internal/store/**`, `internal/importer/**`, `internal/media/serve/**`,
`internal/config/**`, `internal/analysis/{types_m2b.go,blindspots.go}`,
`internal/analysis/tokens/types.go`, `internal/analysis/ffmpeg/{ffmpeg.go,capabilities.go}`,
`cmd/metamusic/{main.go,cmd_serve.go}`, `openapi.json`, `web/src/api/schema.d.ts` or
`web/src/lib/fixtures/**`; a needed contract change goes back through this document and a
Stage-1-owner fix commit that every lane rebases onto. A shared M1 test is extended by **a new test
function in a new file per lane** (Media: `TestArgv_OnlyWorkPaths_Clip`, `TestArgv_OnlyWorkPaths_Window`;
Ears: `TestArgv_OnlyWorkPaths_Signal`, `TestArgv_OnlyWorkPaths_Spectrum`), never by two lanes editing one
function. Stubs are marked `// STUB(M2b-<task>)` and answer 501 `NOT_IMPLEMENTED`;
`grep -rn 'STUB(M2b-' internal cmd` must be empty after M2b-B3.

### Task M2b-B1: schema, store, library/serve/problem/textfold additions, orphan scan
**Files:** `internal/store/migrations/0004_duels.sql`, `internal/store/{schema_m2b_test.go,
triggers_m2b_test.go,upgrade_m2b_test.go}` (rev 3: M2a's naming); the one hard-coded schema-version
assertion (`internal/httpserver/server_test.go`, 3 → 4); `internal/config/{config.go,config_test.go}`
(`ClipCacheMB`, validated in `load`);
`internal/library/{refs.go,sections.go,artifacts.go}` + tests, `Service.Resolve` in `tracks.go` (the
`jpg` case) and the factored section-selection helper used by `trackExtras`;
`internal/media/serve/{handler.go (regex + jpg),handler_test.go}`; `internal/problem/{error.go,
error_test.go}`; `internal/textfold/{fold.go,fold_test.go}` and `internal/engine/yue2/slug.go` (calls
it); the importer's orphan scan (`Importer.libraryOrphans` in `internal/importer/report.go`, C16.7) +
`TestOrphans_IgnoreClipCache` in a new test file; plan
records in `docs/plans/2026-10-12-m0-skeleton.md` (C1 env), the M1 plan (C10 env, C12 fold table, C16.7,
C17 `jpg`, C18 regex) and the M2a plan (C27/C34 schema version, C33 regex).
Tests first:
- `TestMigration0004_Applies` (`user_version == 4 == len(migrations)`; tables, indexes, triggers
  present; `duel_verdicts` has exactly two table CHECKs), `TestMigration0004_UpgradeFromM2a` (package
  `store_test`, like `TestMigration0003_UpgradeFromM1`: a real v3 DB built with `store.OpenRaw` +
  `store.MigrateTo(ctx, raw, 3)` + `librarytest.Fixture` migrates; M1 and
  M2a rows unchanged; an M0 token and the M2a fixture device still verify).
- `TestDuelsTriggers` table: `UPDATE duels SET match='x'` → `duels: immutable`; DELETE duels/verdicts →
  `no physical delete`; `UPDATE duel_verdicts` → immutable; insert verdict `a` with the slot-B track as
  winner → `winner does not match slot`; `a` without winner → CHECK; `principal_kind` `system`,
  `anonymous` or `x` → CHECK; `source='human'` with `principal_kind='agent'` → CHECK; `source='critic'`
  with `human` or `device` → CHECK; `created_by_kind='system'` → CHECK; `note=''` → CHECK; a human-source
  verdict by `human:op` then another by `device:d1` → unique violation; critic verdicts from `agent:t1`
  and `agent:t2` → ok, `agent:t1` twice → unique violation; `swapped=1, blind=0` → CHECK; `to_ms =
  from_ms` → CHECK; `slot_a = slot_b` → CHECK.
- `TestSchema_M2bEnumsGoValidated` (`duels.match='other'`, `analyses.analyzer='token_loops'`,
  `analyses.tier='A'`, `renditions.kind='spectrogram'` insert at the SQL level).
- M2a's `TestMigration0003_*` and the backup tests stay unchanged and green (they assert `>= 3` / the live
  version); `internal/httpserver`'s `get_system_info` test expects `schema_version` 4.
- `TestTrackRefs` (fixture: fields; `ParentOperator` is `inferred_sibling` for the sibling child and ""
  for roots; unknown ids absent), `TestLineageTracks_Order`, `TestSections_SelectionRule` (aligned plan
  → timed with lines from lyrics rows; unaligned → untimed, structure only; expected duration = bars ×
  spb), `TestArtifact_NotFound`, `TestResolve_JPG`.
- `TestHandler_JPGContentType` (`image/jpeg`, Range works), `TestHandler_Paths` (existing, in
  `handler_test.go`) extended (`.png`, `.JPG` → 404); M2a's `TestHandler_ExportExtensions` unchanged and green.
- `TestOrphans_IgnoreClipCache` (a file under `derived/clips/aa/` or `derived/exports/aa/` is never
  reported; an unreferenced `derived/<aa>/<sha>.spectro.jpg` is; M1's `TestReport_LibraryOrphans` expectations
  unchanged).
- `TestTextfold_Fold` (`Straße` → `Strasse`, `Città` → `Citta`, `Ørsted` → `Orsted`, `þ` → `th`; case
  kept); M1's slug tests unchanged and green.
- `TestProblemError_Fields`, `TestConfig_ClipCacheMB` (default 4096; 100 → error naming the variable).
Done: `make check` green; `TestNoReplaceSQL` (M1) and `TestNoDeleteSQL` (M2a) green.

### Task M2b-B2: contracts, stubs, operations, MCP image, harness, fixtures
**Files:** `internal/ladder/{types.go,stub.go}`; `internal/media/clip/{types.go,window.go,key.go,gains.go,
stub.go}` + tests for the pure files; `internal/duels/{types.go,stub.go}`; `internal/ears/{types.go,
stub.go}` (incl. `Perception`); `internal/analysis/{types_m2b.go,blindspots.go}` + tests;
`internal/analysis/tokens/types.go`; `internal/analysis/ffmpeg/{ffmpeg.go (retains the
`-filters`/`-encoders` listings `New` reads), capabilities.go}` + test (new file, reusing `fakeFFmpeg` from
`ffmpeg_test.go`); `internal/api/{api.go (Deps, New),types_m2b.go (incl. `nullableObject`),ops_duels.go,
ops_ears.go,ops_clips.go,errors_m2b.go,registry.go (Meta.MCPImage + reflection check in Register)}` +
tests (new files `ops_m2b_test.go`, `types_m2b_test.go`);
`internal/mcpgen/{image.go (new),dispatch.go (serve),generic.go (describe_operation),schema.go (toolSpec)}`
+ tests; `internal/mcpgen/parity_test.go`
(M2b cases in `stubCases`, the `M2b` option in `runParity`, image rules; the output-schema validation is a
new test in a new file next to `outputschema_test.go`); `internal/mcpgen/m2a_contract_test.go`
(`toolsCeilingBytes`, D-M2b-22); `internal/mcpgen/testdata/tools_list.golden.json`;
`internal/app/*` (`media.go` with `NewMedia`/`MediaOptions`/`Media`, `Options.Media`, `Services.{Clips,
Duels,Ears}`, stub services, the media chain, the compile-time assertion
`var _ duels.ClipRenderer = (*clip.Service)(nil)`); `internal/httpserver/{server.go,server_test.go}`
(Options); `internal/servertest/*` (`M2bOptions`); test helpers `internal/{media/clip/cliptest,
duels/duelstest,ears/earstest}/*.go`; `internal/acceptance/every_ui_action_test.go` (`uiScript`/`uiOrder`
rows for the 9 ui ops) and `nosecretleak_test.go` (M2b ops);
`cmd/metamusic/nohelpers_test.go` (helper list + forbidden edges); `cmd/metamusic/{main.go (analyze
case + usage text),cmd_analyze.go (stub)}`; `openapi.json`; `web/src/api/schema.d.ts`;
`web/src/lib/fixtures/m2b/*`; `Makefile` (`fixture-db` runs `TestWriteFixtureDB_M2b` too); `go.mod`/`go.sum` (`golang.org/x/image`, pinned
in `internal/deps/tools.go` until M2b-E2 imports it); plan records: M0 (C4 `Meta`, C6 Options), M1 (C14
capabilities, C20 Options) and M2a (C28 `Meta`/`Deps`, C39 `app.Build`/Options, C42 servertest).
Stubs: every service method returns `errors.ErrUnsupported` (⇒ 501 `NOT_IMPLEMENTED`) with
`// STUB(M2b-J1|J2|M1|E3)`; `cmd_analyze.go` exits 2 "not implemented" with `// STUB(M2b-E3)`.
Tests first:
- `TestResolveWindow` table: D=240 default → [72.0, 117.0]; D=40 default → [0, 40]; D=6 → [0, 6] and
  any other length → 422; from 10.04, to 40.06 → [10.0, 40.1]; length 29.9 → 422 `{min_s:30, max_s:45}`;
  length 45.1 → 422; to > D → 422; from < 0 → 422; to omitted with from 200 on D=240 → [200, 240].
- `TestMatchGains` (−10/−20 → gains −1000/0 cdB, target −20, `matched`; −20/−20 → 0/0; −10/−40 →
  −2400/0 `limited`; −65/−14 → 0/0 `window_silent`), `TestTargetGain` (attenuate only: I −20, target −30
  → −1000 `matched`; I −20, target −14 → 0 `limited`; I −12, target −40 → −2400 `limited`; I −12, target
  −20 → −800 `matched`; I −65 → 0 `window_silent`).
- `TestClipKey_Golden` (fixed spec + tool `ffmpeg n9.0.1` → golden hex; changing any field, the tool
  included, changes the key; 44.1 kHz window [1.0, 31.0] → start 44100, end 1367100).
- `TestFFmpeg_RequireCapabilities` (fake `ffmpeg` scripts: M1 capabilities only → `New` succeeds,
  `RequireClips` and `RequireTierA` name their first missing filter; only `asetpts` missing → only
  `RequireClips` fails, naming it; only `showspectrumpic` missing → only `RequireTierA` fails).
- `TestBlindSpots_Table` (every fact combination → the exact ordered list; the first five entries equal
  M2a C35's literal).
- `TestRegister_MCPImageValidation` (POST + mcp_image → panic; exclude + mcp_image → panic; an output
  type without an `image *api.ImageData` body field → panic at Register), `TestRegister_MCPImageShape`.
- `TestMCP_ImageContent` (fake image op: ImageContent first, bytes equal, structured content has no
  `data_base64`, text JSON equals structured; base64 garbage → `INTERNAL`; 100 001 bytes →
  `IMAGE_TOO_LARGE`; no `image` → plain result; `call_operation` and stdio identical).
- `TestOps_M2bRegisteredWithMeta` (the 10 ops with exactly the C53 method, path, scope, mcp, ui,
  `idempotency` (`create_duel` only), `rate` (`create_duel`, `get_clip`) and `mcp_image`;
  `exclude`/`state` carry `explode: true`; `list_duels` has `mode` and no `blind` parameter; the
  `create_duel` body has no `note`, and `blind`/`match_loudness` carry no `default`); M2a's
  `TestOps_CreateOpsDeclareIdempotencyKey` green; new `TestOps_M2bNilDepsUnavailable` (zero `Deps` →
  each M2b op 503 `UNAVAILABLE` naming its component; M2a's hand-written `TestOps_NilDepsUnavailable` is
  left alone); `TestOps_StubNotImplemented` (stub services → 501 `NOT_IMPLEMENTED`);
  `TestServiceErrors_Mapping` (`m2bErr`: `*problem.Error`, NotFound, `ErrUnsupported` → 501, anything else →
  500 whose log line went through `redact.Error`).
- `TestOptParam_Float` for `get_clip` (absent → IsSet false; `from_s=12.5` → 12.5 set; schema type number).
- `TestOpenAPI_M2bNullableObjects`, `TestJSONTags_M2bPayloads`, `TestWebFixtures_M2b`.
- Parity: one or more `ParityCase`s per M2b op (args from `duelstest`/`earstest` ids; `create_duel` once
  with `blind:false, match_loudness:false` and once with both omitted; `submit_duel` on the open fixture
  duel; `get_track_spectrogram` inline and not), run on the REST, MCP and stdio rigs with
  `M2b{Fixture:true}` and the fixed slot coin — at Stage 1 they compare equal 501s;
  `TestParityEveryOperation` green; `TestToolResultsConformToOutputSchema_M2b` (every MCP
  result of every case, HTTP and stdio, validates against the tool's `outputSchema`).
- M2a registry-derived tests (rev 3): `TestAcceptance_M2a_AgentPerformsEveryUIAction` derives the required
  set from the registry and fails by name for an op with a `ui` route and no `uiScript` entry, so B2 adds
  scripted calls (and `uiOrder` positions) for the 9 M2b ops with a `ui` route; until B3 the test accepts a
  501 `NOT_IMPLEMENTED` result and its 501 audit row only for an op whose service still carries a
  `STUB(M2b-` marker (B3 removes the allowance and sets `servertest` `M2b{Media:true, Backfill:true}` for
  that test, since the `acceptance` archive template has no Tier A rows). M2a's `TestAudit_CoverageMatrix`
  drives synthetic ops (`t_read`, `t_write`, …) in `internal/gate/gate_test.go` and is not touched; the M2b
  audit expectations live in B3's `TestAcceptance_M2b_GateCoverage`.
- `TestNoTestHelpersInBinary` (new helpers listed; the C43 forbidden edges asserted from the same
  `go list -deps` output).
- `tools/list` (D-M2b-22): `TestToolsList_Ceiling` measured, `toolsCeilingBytes` and `toolsListBaselineBytes`
  re-baselined with a one-line justification, golden regenerated, `TestToolsListBudgetFires` green.
Done: `make check` green; `make openapi` committed; `schema.d.ts` regenerated; web fixtures committed;
`make fixture-db` writes duels and Tier A rows; the only `STUB(M2b-` markers are the listed service stubs
and `cmd_analyze.go`.

**B2 outcome (recorded at implementation).** D-M2b-22 measured: the seven M2b tools were 28 198 compact bytes (66 881 in
total), so `get_ladder`, `compare_tracks` and `create_duel` became `mcp: generic`; `tools/list` is 25 tools / 55 308 bytes,
`toolsCeilingBytes` = 55 500, `toolsListBaselineBytes` = 130 984. Deviations from the file list, all additive: the
nullable-object `…OrNull` types and the `ImageData` struct live beside the service types (`internal/duels/schema.go`,
`internal/ears/schema.go`; `api.ImageData` is an alias of `ears.ImageData`) so services return wire shapes directly;
`duelstest.Fixture` takes the DataDir (`Fixture(tb, st, dataDir, fx, now, newID)`) because the real clip service links the
fixture masters; `app.Options` also carries `SlotCoin` and `DuelClips` (spy/fake renderer hook); `*ffmpeg.FFmpeg` implements
`clip.WindowAnalyzer` and `ears.TierA` through three stub files (`window_stub.go` STUB(M2b-M1), `signal_stub.go` STUB(M2b-E1),
`spectrum_stub.go` STUB(M2b-E2)) that their lanes delete; `internal/importer/privacy_test.go` wires the stub M2b services so
the privacy sweep keeps covering every GET; component names of generic Go types are fixed in `wireNames`
(`DuelPage`, `NextDuel`, `ClipWindow`, `TimeSpan`, `TokenStream`, ...).

### Task M2b-J1 (Judge): ladder math
**Files:** `internal/ladder/{fit.go,laplace.go,prob.go,race_on.go,race_off.go}` + `fit_test.go`,
`laplace_test.go`, `sim_test.go`, `acceptance_test.go` (replacing the stub).
Tests first (exact values computed with an independent reference implementation; tolerance 1e-4 unless
stated):
- `TestFit_Validation` (Winner == Loser, index out of range, Anchor as loser, Anchor in a tie, `FixedNu=0`
  with a tie → errors; n = 0 → empty fit).
- `TestFit_EmptyIsPrior` (3 items, no comparisons → θ = 0, SD = 1.5, iterations 0, converged).
- `TestFit_SingleWinFixedNu0` (`FixedNu=0`, item 0 beats item 1 once → θ0 = −θ1 = 0.556474, SD 1.318022
  for both).
- `TestFit_AllTies` (three ties between items 0 and 1 → θ0 = θ1 = 0, ν = (−3 + √73)/4 = 1.386001).
- `TestLadder_BothBadAnchor` (one `both_bad` on (0, 1) → θ0 = θ1 = −0.731130, SD 1.227386, ν unaffected
  by anchor pairs), `TestFit_BothBadLowersBoth` (property over 50 random problems: adding a both_bad on
  (i, j) strictly lowers θ_i and θ_j).
- `TestFit_MonotoneLogPosterior` (50 random problems with ties and anchor losses: LP never decreases by
  more than 1e-12·|LP| between iterations), `TestFit_StationaryPoint` (|∂LP/∂θ_i| < 1e-6 and
  |∂LP/∂ν| < 1e-6 at the result, by central differences), `TestLaplace_MatchesNumericHessian` (relative
  error < 1e-3 vs finite differences of LP), `TestFit_PermutationInvariant`,
  `TestFit_DisconnectedComponents` (two components fit independently given ν), `TestFit_Undefeated`
  (10–0 → θ finite, < 3), `TestFit_Deterministic` (bit-identical twice), `TestPBeats` (ν=0 logistic;
  sums to 1; symmetry), `TestFit_TimeBudget` (500 items, 5000 random comparisons converge in < 2 s on
  the runner; skipped under `-short` and under the race detector; `BenchmarkFit_500x5000` reported in
  the task summary).
- `TestAcceptance_M2b_BTParameterRecovery` — simulated voters with `math/rand/v2` PCG seeds 1…20 per
  scenario (**1…3 under `-short`**); a duel picks two distinct items uniformly; outcomes sample the
  model; metrics on **centered** θ (mean removed) because the absolute level is only weakly identified:

  | Scenario | Items, duels | Truth | Assertions (all replicates) |
  |---|---|---|---|
  | a pure BT | 10, 1500 | θ ~ N(0,1), no ties | RMSE ≤ 0.25; Spearman min ≥ 0.85, mean ≥ 0.94; ν̂ ≤ 0.01 |
  | b ties | 10, 2000 | ν = 0.5 | RMSE ≤ 0.20; Spearman min ≥ 0.85, mean ≥ 0.94; ν̂ ∈ [0.40, 0.60] |
  | c reject anchor | 8, 2000 | θ ~ N(0.5,1), ν = 0.3; per duel each item independently "fails the bar" w.p. 1/(1+e^θ); both fail ⇒ `both_bad`; **otherwise the outcome is sampled from Davidson `PBeats(θ_i, θ_j, ν)`** (a single failing item is not forced to lose) | RMSE ≤ 0.25; Spearman min ≥ 0.85, mean ≥ 0.93; ν̂ ∈ [0.20, 0.40] |
  | d mixed voters | 10, 2000 | ν = 0.3; three voters with temperatures 0.8 / 1.0 / 1.25 (θ/T) in rotation | RMSE ≤ 0.25; Spearman min ≥ 0.85, mean ≥ 0.93 |

  Coverage: across scenarios a, b and d, the share of items whose centered true θ lies within
  û ± 1.96·SD is ≥ 0.90. (A reference simulation of this exact fitter gave max RMSE 0.199/0.148/0.184/0.165,
  min Spearman 0.891/0.903/0.881/0.891, mean ≥ 0.964, coverage 1.0; M2b-J1 re-runs it with the scenario-c
  rule above and records the numbers in the test file.) The `-short` subset uses the same thresholds.
  If a threshold fails, the fitter is presumed wrong; a threshold may change only with a written
  justification in the test file.
Done: `internal/ladder` ≥ 95 % statements; no `STUB(M2b-J1)`; the test runs in < 3 s under
`make check` (`-race -short`).

### Task M2b-J2 (Judge): duels service
**Files:** `internal/duels/{service.go,create.go,view.go,next.go,list.go,submit.go,ladder.go,cache.go,
pairs.go,clipkeys.go}` + tests (replacing the stub); `internal/duels/rest_test.go` (package `duels_test`,
servertest rigs with `M2b: servertest.M2bOptions{Fixture: true, FakeClips: true}`). Uses `cliptest.Renderer`, `librarytest` and
`duelstest`.
Tests first:
- `TestCreateDuel_Validation` (same track → 422 `DUEL_SAME_TRACK`; unknown → 404 with `track_id`; window
  errors pass through).
- `TestCreateDuel_BlindSlotsRandomisedAndLogged` (injected `Rand` true → slot A = requested b,
  `swapped=1`; false → as requested; non-blind never calls `Rand`), `TestSlotCoin_Fair` (the crypto coin
  alone: 100 000 draws, share in [0.4937, 0.5063], i.e. ±4σ).
- `TestCreateDuel_LoudnessGains` (renderer measures −10/−20 → stored gains −1000/0, target −20,
  `match='matched'`, both clips `Ensure`d with those gains; silent window → `window_silent`, gains 0;
  `MatchLoudness` false → `off`, no Measure call; nil → matched).
- `TestCreateDuel_RenderFailureLeavesNoRow`, `TestCreateDuel_IDsAndPrincipal` (id from `NewID`;
  `created_by = p.Key()`; `created_by_kind` `device` for a device).
- REST/MCP (`rest_test.go`): `TestCreateDuel_ExplicitFalseHonoured` (REST and MCP: `blind:false` →
  non-blind, `swapped=0`, revealed at creation; `match_loudness:false` → `match:"off"`, no Measure call;
  both omitted → blind and matched), `TestCreateDuel_ReplayDoesNotRender` (same REST
  `Idempotency-Key` twice → identical 201 body with `Idempotent-Replayed: true`, one `duels` row, the
  wrapped renderer's `Ensure` called twice in total (once per slot); the same over MCP without a key
  (derived key); a different key → a second duel), `TestCreateDuel_RESTWithoutKey428` (428
  `IDEMPOTENCY_KEY_REQUIRED`, no row, no renderer call).
- `TestDuel_BlindRedaction` (structural JSON walk over the `duelstest` open blind duel as human, device
  and agent: `slots.*.track`, `slots.*.gain_db`, `slots.*.window_lufs` and every `winner_track_id` are
  `null`; no key named `swapped`, `seed`, `request_id`, `title`, `root_track_id` or `target_lufs` exists
  anywhere; no string leaf equals either track id, title, request id or root id; no number leaf equals
  either seed (7340021/7340022) or ±7.37; after the caller's verdict — human source, or the agent's own
  critic verdict — every value is present and correct; non-blind: present from creation).
- `TestReveal_Rules` (table over blind/sighted × human verdict present × caller human/device/critic ×
  caller judged).
- `TestNextDuel_QueuePerSource` (three duels: the human gets the oldest; a device gets the same; after a
  human-source verdict it is gone for human, device and critics; a critic gets only open duels its key
  has not judged; `exclude` skips; expired skipped; `open_total` ignores exclude).
- `TestSubmitDuel_SourceByPrincipal` (`rest_test.go`: proxy human → `source human`, `principal_kind
  human`; paired device → `human`/`device`; PAT agent → `critic`/`agent`; stdio agent →
  `critic`/`agent`; an agent body that adds `"source":"human"` → 422 or ignored, whatever Huma does with
  the extra field, and the stored row is `critic` and the human ladder is unchanged).
- `TestSubmitDuel_HumanThenDevice` (human submits `a` with note `"ok "`; the device submits `a` with
  `"ok"` → 200 `replayed` with the human's stored verdict (`principal_kind human`); the device submits `b`
  → 409 `DUEL_ALREADY_JUDGED` with the human verdict's id; the device's `my_verdict` is the human
  verdict; the device's `next_duel` skips the duel).
- `TestSubmitDuel_WinnerMapping` (swapped duel: verdict `a` stores the requested b track as winner),
  `TestSubmitDuel_ReplayAndConflict` (identical → 200 `replayed`; note `" x "` vs `"x"` and `""` vs
  absent → replay; different verdict or note → 409 `DUEL_ALREADY_JUDGED`; concurrent identical submits
  (`-race`) → one row, one 201 and one 200), `TestSubmitDuel_Expired` (409), `TestSubmitDuel_SawReveal`
  (critic on a decided duel → 1; on an open blind duel → 0), `TestSubmitDuel_NoteCreatedOnlyOn201`
  (audit `created_ids` holds the verdict id for the 201, nothing for the replay).
- `TestSourceFor` (human → human; device → human; agent → critic; `auth.CLI` (system) → `ErrNoSource`;
  anonymous → `ErrNoSource`).
- `TestListDuels_Filters` (state; `track_id` either slot, unrevealed duels included with `track: null`;
  root; `mode` any/blind/sighted; paging without dup/miss; page cap).
- `TestLadderInputs_HumanOnly` (critic rows of every verdict kind added → identical comparisons),
  `TestLadder_SightedExcludedByDefault`, `TestLadder_ScopeLineageVsGlobal` (cross-lineage duels only in
  global; lineage lists zero-duel tracks as provisional), `TestLadder_Counts` (counted/excluded counts,
  slot-A rate), `TestLadder_DeviceVerdictCounted`, `TestLadder_CacheInvalidatedByVerdict`,
  `TestLadder_TitlesNotCached` (a title changed through `update_track` appears on the next `get_ladder`
  without a new verdict), `TestLadder_Golden` (JSON golden, `-update`).
- `TestPairStats` (exact pair only; orientation; human blind/sighted and critic counted separately),
  `TestStrengths_GlobalSubset`, `TestOpenClipKeys` (both slots of open duels only; decided and expired
  duels excluded; keys use the current `ToolVersion()`).
- `TestAcceptance_M2b_CriticVerdictsExcluded` (servertest rig, `M2b{Fixture:true, FakeClips:true}`):
  the human (proxy) creates and blind-judges 6 duels among 4 tracks of one lineage and the paired device
  blind-judges 2 more → snapshot of `get_ladder` global and lineage (`verdicts_human_counted` = 8);
  agents then submit contradicting critic verdicts on all 8 — PAT over REST (3), PAT over MCP (3) and
  `mcp-stdio` (2) — and an agent creates + critic-judges 4 more duels → both ladders are byte-identical
  to the snapshots after removing `counts.verdicts_critic_excluded` (now 12) — `generated_at` is fixed by
  the rig clock; the rows exist with `source='critic'`, `principal_kind='agent'`; one more human verdict
  changes the ladder.
Done: package ≥ 90 % statements; no `STUB(M2b-J2)`.

### Task M2b-M1 (Media): clip service
**Files:** `internal/media/clip/{service.go,render.go,cache.go,resolve.go,link.go}` + tests,
`internal/media/clip/argv_test.go` (`TestArgv_OnlyWorkPaths_Clip`), `internal/analysis/ffmpeg/window.go`
+ `window_test.go` + `argv_window_test.go` (`TestArgv_OnlyWorkPaths_Window`).
Tests first (with tools unless marked pure):
- `TestWindowLoudness_Golden` (pure: captured ebur128 metadata text for a windowed tone → I and TP).
- `TestClip_SampleExactWindow` (40 s synthetic click track at 48 kHz with clicks every 0.5 s; window
  [5.0, 35.0] → `TotalSamples` 1 440 000; click k ≥ 1 of the decoded clip at sample 24 000·k ± 2 — click 0
  sits inside the 10 ms fade-in and is not asserted).
- `TestClip_24bit48kNoTags` (`flacmeta`: 24 bits, 48 000 Hz, 2 channels, zero Vorbis comments (vendor
  only), no `TITLE`/`MM_*`), `TestClip_Resample44k` (44.1 kHz master → 48 kHz clip, duration ± 1 sample),
  `TestClip_Mono` (mono stays mono), `TestClip_FadesApplied` (first and last decoded samples |x| <
  1e-3).
- `TestClip_Deterministic` (two renders into fresh caches → identical bytes and key),
  `TestClip_CacheHitRunsNoTool` (second `Ensure` runs no tool), `TestClip_ConcurrentSameKey` (8 goroutines
  → one render, same file), `TestClip_CancelledCallerDoesNotFailWaiters` (pure, blocking fake
  `RenderFunc`: the first caller's ctx is cancelled mid-render → it returns `context.Canceled`; a second
  waiter on the same key gets the object; the file exists; one render), `TestClip_LoudnessMatched` (−10
  and −20 LUFS sources, matched gains → both clips measure −20.0 ± 0.2 LUFS),
  `TestClip_TargetAttenuateOnly` (`match_lufs=-30` on a −20 LUFS window → the clip measures −30.0 ± 0.2;
  `match_lufs=-10` → gain 0, `match:"limited"`, `target_lufs` −10).
- `TestClipCache_EvictsOldestUnpinned` (pure, `cliptest.Render`, `CacheBytes` of a few KB: pinned keys
  survive; others are removed oldest-mtime first down to ≤ 90 %; a `Pinned` error evicts nothing; with
  every file pinned and the cap exceeded the render still succeeds).
- `TestClip_MasterUntouched` (master sha, mode 0444, mtime and link count 1 after renders),
  `TestClip_WorkDirRemoved` (success and failure), `TestArgv_OnlyWorkPaths_Clip`,
  `TestArgv_OnlyWorkPaths_Window`.
- `TestResolveClip` (existing key → object with `audio/flac`, ETag = key; unknown → NotFound; non-flac
  ext → NotFound), `TestMediaHandler_ServesClip` (full chain library → exports → clips: signed
  `/media/<key>.flac` → 200/206; a master sha still resolves through the library first; an export
  `.jsonl` through exports).
- `TestClipLink_Op` (REST `get_clip` on a servertest rig with `M2b{Fixture:true}`: URL verifies with the
  signer; link TTL human **and device** 12 h, agent 15 min (`serve.TTLFor`, unchanged; rev 3); 404; 422
  window; `match_lufs` −50 → 422; heavy bucket: with `Limits` set, the 7th agent call within a minute →
  429 with `details.bucket = agent_heavy` (`ratelimit.AgentHeavy` is 6/min, burst 6); an `app.Build` whose
  `MediaOptions.ClipsMissing` is set (the real-probe path is tested through a fake `ffmpeg` script lacking
  `asetpts`) → 503 `{component:"clips"}`).
- `TestAcceptance_M2b_ClipContract`: over REST for a 240 s synthetic master: default window [72, 117],
  45.0 s, 24-bit/48 kHz FLAC, `key` = sha in the URL, second call returns the same URL path and does not
  re-render; window [0, 30] → 30.0 s; [0, 29.9] → 422; 45.1 → 422; `match_lufs=-30` → measured −30 ± 0.2.
Done: no `STUB(M2b-M1)`; `internal/media/clip` ≥ 90 %.

### Task M2b-E1 (Ears): signal pass, `.npy`, token loops, synth helpers
**Files:** `internal/analysis/ffmpeg/{signal.go,parse_signal.go}` + tests,
`internal/analysis/ffmpeg/argv_signal_test.go` (`TestArgv_OnlyWorkPaths_Signal`),
`internal/analysis/ffmpeg/testdata/signal_*.txt` (captured from synthetic signals only),
`internal/analysis/npy/{npy.go,npy_test.go,fuzz_test.go}`, `internal/analysis/tokens/{tokens.go,
tokens_test.go}` (types from M2b-B2), `internal/synth/{special.go,npy.go}` + tests.
Tests first:
- `TestParseSignal_Golden` (captures: clipped 10 s sine → clipped_s 10.0, one span; −0.087 dBFS sine →
  0 clipped, full-scale frames 0; single full-scale sample → 0 clipped, 1 full-scale frame; gaps file →
  leading [0, 1.2], internal [3.0, 5.5], trailing [9.0, 10.0] closed at duration, `internal_s` 2.5;
  `-inf`/`nan` values accepted; frame durations from `pts_time` deltas, the last frame closed at
  `durationS`), `TestParseSignal_OtherFraming` (re-framed 50 ms capture → same spans ± 0.05),
  `TestParseSignal_Truncated` (no frames → error), `TestParseSignal_SpanCap` (150 spans → 100 +
  truncated; `internal_s` sums all internal spans, not only the first 100).
- With tools: `TestSignal_ClipBursts` (`synth.Special` → clipped_s 2.0 ± 0.2), `TestSignal_Gap`
  (internal span [4.0, 6.5] ± 0.05), `TestSignal_44k` (frame size 4410), `TestArgv_OnlyWorkPaths_Signal`.
- `TestNPY_Read` table (v1.0/2.0 headers; `<i2 <i4 <i8 <u2 |u1`; shapes (n), (1, n), (2, n), (n, 2);
  `fortran_order True` → unsupported; `>i4` → unsupported; header 70 KiB → corrupt; data short/long →
  corrupt; declared shape × itemsize > maxBytes → error before allocation, `TotalAlloc` growth < 1 MiB),
  `FuzzNPY` (no panic, ≤ 64 KiB inputs).
- `TestTokenLoops_Random` (uniform tokens, vocab 1024, 9000 tokens over 180 s: distinct 4-gram ratio
  ≥ 0.999, periodic span < 1 s, no flag), `TestTokenLoops_Injected` (period 25 × 12 repeats at 3.0 s
  in a 600-token/12 s stream → period 25, span ≥ 300 tokens, at_s 3.0 ± 0.1, repeats ≥ 12),
  `TestTokenLoops_ConstantRun` (period 1), `TestTokenLoops_LongestRepeatNonOverlapping` (a 40-token
  block copied 200 tokens later → 40 tokens, both positions), `TestTokenLoops_GzipWindows` (4 windows,
  one repetitive → its ratio ≤ 0.5 × median), `TestTokenLoops_TimeAxis` ((2, n) and (n, 2) → same
  streams), `TestTokenLoops_Deterministic`, `TestTokenLoops_JSONShape` (golden JSON of a `Report`: only
  the snake_case keys of C49.3).
- `TestSynthSpecial_*`, `TestSemanticNPY_ReadBack` (shape, dtype, loop region).
Done: no Go dependency added in this task; package tests < 10 s warm without `-short`.

### Task M2b-E2 (Ears): spectrogram renderer and overlay
**Files:** `internal/analysis/ffmpeg/spectrumpic.go` + test + `argv_spectrum_test.go`
(`TestArgv_OnlyWorkPaths_Spectrum`), `internal/analysis/spectro/{compose.go,fold.go,encode.go}` +
tests, `internal/analysis/spectro/noise_test.go` (white-noise inputs generated once per package),
`go.mod`/`go.sum` (`golang.org/x/image` becomes a direct import; remove the `internal/deps` pin).
Tests first:
- `TestFoldASCII` (`Città è <&>` → `Citta e <&>`; `Straße` → `Strasse`; emoji → `?`; tab/newline →
  space; letters folded exactly as `textfold.Fold`).
- `TestCompose_BandAndLines` (pure: synthetic plot image, 3 timed sections at 0/4/8 s of 12 s, W=1024 →
  boundary columns at x0 + 341 and x0 + 683 (± 1) are overlay-bright over the plot rows; label pixels exist
  in each section column of row 2; returned Geometry.Y0 = band + 64), `TestCompose_Untimed` (no boundary
  columns; structure row present), `TestCompose_Truncation` (a 300-character lyric line stays inside its
  section; `+N more` row when lines exceed 6).
- `TestEncodeBudget` (a noise image that needs quality 55 → returns 55 and ≤ budget; impossible budget
  → `SPECTROGRAM_TOO_LARGE`).
- With tools: `TestSpectrogram_Geometry` (48 kHz → 1308×512 PNG for 1024×384; 44.1 kHz → x0 141; a
  wrong declared size → `SPECTROGRAM_GEOMETRY`), `TestSpectrogram_TimeCalibration` (10 s file, silent
  except a white-noise burst at 2.0–2.5 s: the first plot column whose **mean luminance over all plot
  rows** exceeds the median column mean by 30 % is x0 + 205 ± 3), `TestSpectrogram_BudgetWorstCase`
  (white noise at −6 dBFS, mono 16-bit 48 kHz, **6 s, 16 s, 60 s and 360 s**, generated once per
  package, with a maximal 6-line overlay band: every JPEG ≤ 100 000 bytes; the 6 s and 16 s cases land at
  the first plot size with quality ≤ 65 as measured in D-M2b-9; the chosen size/quality per case is
  reported in the task summary), `TestSpectrogram_Deterministic` (same inputs → same bytes),
  `TestArgv_OnlyWorkPaths_Spectrum`.
Done: no `STUB` in these packages.

### Task M2b-E3 (Ears): ears service, compare, perception, CLI
**Files:** `internal/ears/{service.go,analyze.go,backfill.go,dossier.go,flags.go,spectrogram.go,
compare.go,summary.go,perception.go}` + tests (replacing the stub), `cmd/metamusic/{cmd_analyze.go,
cmd_analyze_test.go}` (replacing M2b-B2's stub; **`main.go` is not edited**; the tests use the package's
`do(t, env, in, args…)` / `testEnv` helpers like `cmd_seed_test.go`).
Tests first (fixtures + fake `TierA`/`Judgments`; tool-backed where named):
- `TestAnalyze_RunsMissingOnly` (fresh track → silence, clipping, spectrogram (+ token_loops when
  `semantic.npy` exists); second run → all skipped, zero tool calls; `Force` with the same tool version →
  inserts nothing new (PK), spectrogram unchanged), `TestAnalyze_StaleOverlayRerenders` (sections
  changed → new spectrogram sha, rendition row updated, old file left as an orphan),
  `TestAnalyze_PartialFailure` (spectrogram fails → silence/clipping still committed, `Outcome.Failed`
  names it), `TestAnalyze_TierAMissingReportedOnce` (`Options.Missing` set → `Backfill` over 3 tracks
  returns `ToolMissing` naming the capability, analyses nothing, zero tool calls),
  `TestAnalyze_MasterUntouched`, `TestAnalyze_WorkDirRemoved`, `TestAnalyze_WritesTwice` (the C44
  statements executed twice).
- `TestFlags_Table` (one row per C51.2 rule incl. boundaries: true peak −1.0 → no `near_clipping`,
  −0.99 → yes; internal span 1.99 s → no; ratio 0.15 → no, 0.151 → yes).
- `TestBlindSpots_EveryPayload` (dossier, spectrogram view, comparison and perception carry exactly
  `analysis.BlindSpots(facts)` for their facts).
- `TestDossier_Golden` (fixture with every analyzer; `-update`), `TestDossier_PartialWhenMissing` (no
  Tier A → 200, `tier "0"`, `missing` lists them, blind spot "silence, clipping and token loops (Tier A
  not run yet)"), `TestDossier_RowSelection` (two `silence` rows with the current prefix → the newest
  `created_at` wins; an `mm-silence/0 …` row is ignored), `TestDossier_TierRule` (`token_loops` required
  only with a `semantic.npy` artifact; a spectrogram rendition with an older `def` → tier `"0"`),
  `TestDossier_Summary` (deterministic text).
- `TestGetTrackSpectrogram_InlineAndLink` (inline → base64 decodes to the file and its sha;
  `inline=false` → no `image`; URL verifies; missing rendition → 409 `ANALYSIS_UNAVAILABLE` with hint
  "ask the operator to run analysis"; missing file → 500).
- `TestCompareTracks_Deltas` (fixture: exact b − a values incl. `internal_silence_s` = `internal_s`
  difference and `token_loop_span_s` = difference of the per-side maxima over streams; nulls for missing
  metrics; recipe_diff seed + style terms; `PairStats` and ladder line from the fake; same id → 422),
  `TestCompareTracks_Relation` (root vs its inferred sibling → `siblings` in both orders; two inferred
  siblings of one root → `siblings`; an edge with another operator, inserted directly → `parent_child` /
  `child_parent`; same root otherwise → `same_lineage`; different roots → `unrelated`).
- `TestPerception_Keys` (exact M2a key order; `tier`, `flags`, `blind_spots` per C51.8),
  `TestPerception_MatchesM2aContext` (on the M2a fixture tracks, `lufs_i`, `true_peak_db`, `lra_lu`,
  `bandwidth_hz`, `sections` and `timed` equal what M2a's `trackctx` produces — E3 reproduces that code
  in `ears`; M2b-B3 then switches `trackctx` to it; the test lives in package `ears_test` and may import
  `trackctx`, whose import of `ears` only arrives in B3).
- `TestCLI_Analyze` (exit 0/1/2; `--dry-run` writes nothing — no audit row — and opens read-only;
  `--json`; a missing Tier A capability is named with exit 2; a real run appends exactly one `cli:analyze`
  audit row with principal `system:cli`). `TestCLI_ImportRunsTierA` / `TestCLI_SeedRunsTierA` are added
  by M2b-B3.
Done: no `STUB(M2b-E3)`; `internal/ears` ≥ 85 %.

### Task M2b-W1 (Web): A/B engine, keys, media fetch, duel components, player `pause()`
**Files:** `web/src/lib/{ab.ts,audioContext.ts,duelKeys.ts,ladderScale.ts,spectroSeek.ts}` + `*.test.ts`,
`web/src/api/{media.ts,media.test.ts}`, `web/src/lib/components/{ABPlayer.svelte,VerdictBar.svelte,
DuelCard.svelte,DuelReveal.svelte}`; no change to `web/src/lib/player.svelte.ts` (rev 3: `pause()` exists).
Tests first (`bun test`, fake AudioContext recording every call):
- `ab.test.ts`: both `start` calls receive identical `(when, offset)` with `when = currentTime + 0.05`;
  sources loop with `loopEnd = min(durations)`; `select('b')` at t → A: cancel(t), setValueAtTime(v, t),
  linearRamp(0, t + 0.008); B: linearRamp(1, t + 0.008); `select` of the active slot is a no-op;
  toggling twice within 8 ms cancels and re-ramps from the current value; `seek(12)` stops the old pair
  and starts a new pair with identical args; durations 30.000 vs 30.002 → `ClipLengthMismatch`;
  `pause`/`resume` call suspend/resume; `switches` counts; `dispose` stops both; the Play handler calls
  `ctx.resume()` synchronously (recorded before the handler's first `await` yields).
- `duelKeys.test.ts`: each mapping; repeat, modifiers and form targets ignored; `Enter` without a
  chosen verdict does nothing.
- `media.test.ts`: refuses non-`/media/` and cross-origin URLs; passes `force-cache`; surfaces 403 as
  `ClipLinkExpired` and 404 as `ClipGone`.
- `duelPause.test.ts` (new file; the existing `player.test.ts` is not edited): `pause()` pauses THE element
  and sets `playing = false`; `toggle()` afterwards resumes.
- `ladderScale.test.ts`, `spectroSeek.test.ts` (click at x0 + W/2 on an image rendered at half size →
  duration/2).
Done: lint (fetch ban, no-at-html-tags) + svelte-check clean.

### Task M2b-W2 (Web): pages and track-page additions
**Files:** `web/src/routes/{Duels.svelte,Duel.svelte,Ladder.svelte,Compare.svelte,Track.svelte}`,
`web/src/lib/components/{NewDuelForm.svelte,LadderTable.svelte,EarsPanel.svelte,Spectrogram.svelte,
LineageDuels.svelte}`, `web/src/lib/router.ts` + `router.test.ts` (`APP_ROUTES` entries), `web/src/App.svelte` (imports + nav
links), `web/src/lib/{duelRound.ts,duelView.ts,
ladderView.ts,earsView.ts,compareView.ts}` + tests; the M2a plan (C40: routes, nav and Track page
additions recorded).
Calls (served-op coverage): `next_duel`, `create_duel`, `submit_duel`, `list_duels` (history and nav
badge), `get_duel`, `get_ladder`, `compare_tracks`, `get_track_analysis`, `get_track_spectrogram`.
Tests first (`bun test` against the M2b-B2 fixtures in `web/src/lib/fixtures/m2b/`):
- `duelRound.test.ts` (pairs for 2/3/6 tracks = 1/3/15; 8 tracks → 15 random pairs without duplicates;
  order randomised with an injected RNG; 1 track → no pairs and the button disabled),
  `duelRoundRun.test.ts` (fake client: one `newIdempotencyKey()` per pair, reused across a `RateLimited` error
  (`retryAfterS`) and across a network error; navigates to the first created id).
- `duelView.test.ts` (open blind fixture: the view model contains no title, seed or gain string; revealed
  fixture: titles, winner and gains; `/duels/:id` of an open duel yields the playable card, decided →
  reveal, expired → notice), `ladderView.test.ts`, `earsView.test.ts` (flags first, then metrics, then
  blind spots), `compareView.test.ts` (sign colouring, relation label `siblings`).
- `navBadge.test.ts` (the badge calls `list_duels` with `state=open&limit=1` and shows `total`;
  `next_duel` is called only by the Duels page — spy).
Done: lint + svelte-check clean; the bun tests above green. Pages are **not** rendered against a live
server in this lane (the services are stubs in this branch); real rendering is verified in M2b-W3.
`/duels/:id` plays open duels ("A/B with parent" and "Blind round" navigate there).

### Task M2b-I1 (Infra): tool capabilities, privacy gate, coverage, docs
**Files:** `scripts/tools-selftest.sh` (rev 3: today it only checks pinned URLs and the sha256 tamper
case; the capability assertions are a new section that runs the pinned `bin/ffmpeg` and a negative case
with a fake `ffmpeg` script), `Dockerfile` (extend the existing `RUN ffmpeg -hide_banner -filters | grep -q
' ebur128 ' && … -encoders | grep -q libmp3lame && metaflac --version` check with the M2b filters and the
`flac`/`png` encoders), `scripts/public-gate.sh` (the `forbidden_path()` case glob list),
`scripts/public-gate-selftest.sh`, `coverage.thresholds`, `docs/architecture.md`,
`docs/device-checklist.md` (new `## M2b` section after M2a's DC-M2a-7), `docs/runbook.md` (new "Tier A
analysis and the clip cache" section: `metamusic analyze`, `MM_CLIP_CACHE_MB`), `README.md`.
Device checklist M2b:
- DC-M2b-1 iPhone (ringer on silent) and Android: a blind duel plays both clips in sync — toggling on a
  drum-heavy passage gives no flam, echo, click or gap.
- DC-M2b-2 24-bit FLAC clips decode in Safari (browser and installed PWA) and Chrome on Android; ten
  consecutive duels without a reload or crash.
- DC-M2b-3 the big toggle and verdict buttons work one-handed; locking the screen during a duel and
  returning resumes correctly.
- DC-M2b-4 spectrogram readable on a phone; a tap seeks the player.
- DC-M2b-5 THE player pauses when a duel starts and can be resumed afterwards.
Tests first: `tools-selftest.sh` fails when a `RequireClips` or `RequireTierA` filter is missing (fake
ffmpeg script); `public-gate-selftest.sh` rows: staged `x.jpg`, `x.JPEG` and `x.npy` blocked with
"forbidden path", `icon.png` passes.
Done: `docker build .` green with the capability check; `make gate` green (it runs the selftests and
`scripts/public-gate.sh`); CI green.

### Task M2b-B3 (Stage 3, Backend): wiring, `/context`, synthetic profiles, cross-package acceptance
**Files:** `internal/app/*` (real services; the `Pinned` closure, C46; `NewMedia` and capability checks,
C51.7; `trackctx.UseM2b`), `cmd/metamusic/{cmd_serve.go (also hosts `cmdDev`; work-dir sweep,
`tier_a_missing`/`tier_a_unavailable` logs),cmd_import.go,cmd_seed.go}` + tests (Tier A after
import/seed through `NewMedia`; rev 3: there is no `cmd_dev.go`),
`internal/synth/{archive.go,synth_test.go}` (C55 profiles; `TestSynthArchive_E2EProfile` expectations),
`internal/trackctx/*` + `testdata/context.golden.json`
(C51.8; `trackctx`'s own perception and blind-spot code deleted), `internal/mcpgen/parity_test.go` (rigs
with real services), `internal/acceptance/m2b_test.go` (new file in M2a's `internal/acceptance`),
`web/e2e/{helpers.ts,m1-library.spec.ts,m1-import.spec.ts,m1-pwa.spec.ts}` and any other spec the
grep below finds,
`internal/acceptance/{every_ui_action_test.go,nosecretleak_test.go}` (M2b rows, stub allowance removed),
the earlier tests named below, and plan records: M1 (C21 totals), M2a (C35, C42).
Tests first:
- `TestSeedSynthetic_E2EProfile` (`cmd/metamusic/cmd_seed_test.go`, today `{19, 6, 19, 13, 1}`) updated to
  `{22, 9, 22, 13, 1}` plus Tier A rows for all 9 tracks and 9 spectrograms, `TestSynthEars_RecipesBatchValid` (the e2e `songs.jsonl` incl. the three ears lines
  passes M2a's `ValidateBatch` with zero issues), `TestCLI_ImportRunsTierA` (summary line; a forced Tier A
  failure keeps exit 0), `TestCLI_SeedRunsTierA`, `TestFFmpeg_TierAMissingDoesNotBreakImport` (a wrapper
  around the real ffmpeg that hides `showspectrumpic` from `-filters`: `ffmpeg.New` succeeds, `import`
  and `seed-synthetic` exit 0 and print `Tier A: unavailable (ffmpeg lacks showspectrumpic)`, `serve`
  logs it once, `get_track_analysis` answers `tier "0"`, `get_clip` still works),
  `TestServe_SweepsStaleWorkDirs`.
- **Earlier tests that change** once Tier A runs after `import`/`seed-synthetic` — B3 runs the M1 and
  M2a suites without `-short` plus `make e2e` and updates exactly these (naming any further one it finds
  in the commit message): M1 `TestSeedSynthetic_E2EProfile` (above) and `TestSynthArchive_E2EProfile`
  (`internal/synth`: Recipes 22, Tracks 9, Drafts 22, `len(m.Audio)` 9, …); M1 CLI import tests that count
  renditions or analyses per track; M2a `TestAcceptance_M2a_ExportByteIdentical` and
  `TestAcceptance_M2a_ExportPassesBatchValidator` (the e2e recipe JSONL gains three lines that must stay
  byte-identical and batch-valid); M2a backup tests over a seeded archive (spectrogram renditions are
  backed up, D-M2a-5; the clip cache is not); M1/M2a Playwright specs asserting e2e library counts (6 → 9
  tracks, 19 → 22 recipes and drafts): found with `grep -rn "toHaveCount(6)\|6 tracks\|TITLES\|ownTracks" web/e2e`
  — in particular `ownTracks` in `web/e2e/helpers.ts` slices the *sorted* titles per project and the new
  "Syn ears …" titles sort before "Syn song …", so it must be restricted to the `Syn song` tracks (the
  sibling pair "Syn song 01 s1001/s2001" stays the first pair), `TITLES` and the counts in
  `m1-library.spec.ts`, the audio-item count in `m1-import.spec.ts`, the counts in `m1-pwa.spec.ts`; the
  `acceptance`-profile archive tests are unaffected apart from the valid `semantic.npy`.
- `/context` (C51.8): `TestContext_PerceptionFromEars` (a track with `earstest` Tier A: `tier "A"`, flag
  codes, `lra_lu` and `timed` present, keys exactly in M2a's order; a track without Tier A: `tier "0"`,
  `flags: []`, blind spots from `analysis.BlindSpots` incl. "Tier A not run yet"; M2a's values of
  `lufs_i`, `true_peak_db`, `lra_lu`, `bandwidth_hz`, `sections` unchanged), `TestContext_LadderHumanOnly`
  (critic verdicts never change `judgments.ladder`; a device verdict does; `null` without counted duels;
  `children[].ladder` equals `get_ladder` global `u`/`sd`), `TestContext_SpectrogramLink` (signed
  `/media/<sha>.jpg`, agent TTL 15 min; `null` without a rendition); `TestFit_WorstCaseWithLadders` (50
  children with ladders, ≤ 24 000 UTF-16 units; M2a's `TestFit_WorstCase` stays as is) and
  `TestContext_Golden` updated.
- `TestAcceptance_M2b_TierAFlags` (C55 expectations over the `archivetest.SyntheticArchive(tb, "e2e")`
  template copy — now including the ears group — after `ears.Backfill`; rev 3: the template is imported
  once per test binary without Tier A rows).
- `TestAcceptance_M2b_SpectrogramBudget` (signals: white noise 6 s, 16 s, 60 s and 360 s (generated once
  per package, mono 16-bit), 240 s pink noise + tone, 6 s sine, 12 s silence, clip bursts; each: rendition
  ≤ 100 000 bytes, JPEG magic `FF D8 FF`, size equals params; REST inline base64 decodes to ≤ 100 000
  bytes; MCP and stdio `ImageContent` ≤ 100 000 bytes and the serialized result ≤ 140 000 bytes).
- `TestAcceptance_M2b_DuelLifecycle` (servertest `M2b: M2bOptions{Fixture: true, Media: true}`, real clips: the human
  creates a blind duel (explicit key) between siblings → `next_duel` returns it unrevealed with two
  playable signed FLAC links (206 on Range) whose decoded lengths are equal → `submit_duel b` with
  latency and switches → revealed, winner = slot B track → the ladder lists the winner first; the paired
  device sees it decided; an agent PAT over MCP: `next_duel` skips the decided duel, `create_duel`
  (derived key) + `submit_duel` stores `critic`; a non-blind duel reveals at creation; open-duel clips
  survive an eviction pass under a tiny cap while decided-duel clips go).
- Parity with real services: the M2a parity runner (C42) over `M2b{Fixture:true}` — all 10 ops on REST,
  MCP and stdio, the image rules and the output-schema validation (C52).
- `TestGETNeverWritesDB_M2b` (C53).
- M2a registry-derived tests with M2b rows and no stub allowance: `TestAcceptance_M2a_AgentPerformsEveryUIAction`
  (every M2b ui op succeeds over MCP and appears in `get_audit`; the rig runs `M2b{Media:true,
  Backfill:true}`), `TestNoSecretLeak` (every M2b op over REST human/device/agent, MCP and stdio, plus the
  `analyze` CLI output and its audit row; its `scriptRest`/`scriptMCP` iterate `api.Operations`).
- `TestAcceptance_M2b_GateCoverage` (new, `internal/acceptance/m2b_test.go`; rev 3: replaces the planned
  rows in M2a's synthetic `TestAudit_CoverageMatrix`): over a `servertest` rig, `create_duel` and
  `submit_duel` write exactly one audit row for the human, the device and an agent (`created_ids` holds the
  duel / verdict id for the 201, nothing for a replay); `get_ladder`, `get_duel`, `list_duels`,
  `next_duel`, `get_clip`, `compare_tracks` and the Ears reads add a row for the agent only; a heavy-bucket
  refusal (`get_clip` ×7 as an agent) is recorded with `RATE_LIMITED`; `create_duel` without a key over REST
  is recorded as `IDEMPOTENCY_KEY_REQUIRED` (428).
Done: `grep -rn 'STUB(M2b-' internal cmd` empty; `make check` < 30 s warm; `metamusic dev --seed-synthetic`
shows duels, ladder, Ears and the `/context` perception.

### Task M2b-W3 (Stage 3, Web): Playwright M2b specs
**Files:** `web/e2e/{m2b-duels.spec.ts,m2b-ears.spec.ts,m2b-compare.spec.ts}`, `web/e2e/abSpy.ts` (init
script wrapping `AudioBufferSourceNode.prototype.start`, `AudioParam.prototype.setValueAtTime`,
`linearRampToValueAtTime` and `cancelScheduledValues`, recording into `window.__ab`),
`web/e2e/m2bApi.ts` (creates duels through the API with the human headers and an `Idempotency-Key`; it
wraps the existing `apiJSON(request, "POST", …, body, true)` / `idemKey()` helpers in `web/e2e/helpers.ts`),
`web/playwright.config.ts` (rev 3: add M2b entries to the `ignore…` project lists like `ignoreM2a` —
keyboard specs desktop only, toggle specs mobile only, Firefox only the ears/compare specs unless its
headless `AudioContext` works — so the CI time budget holds).
Projects (`web/playwright.config.ts`, `fullyParallel: true`): `chrome`, `chrome-pixel7`,
`chrome-iphone15`, `firefox`, sharing one DataDir and one human principal. **Isolation rules:**
verdicts are immutable and `next_duel` returns the globally oldest open duel, so every duel spec creates
**its own** duel (API helper or UI) and opens `/duels/:id`; no spec asserts a global rank or the queue
head. Keyboard specs run on desktop projects only (`chrome`, `firefox`), toggle specs on mobile projects
only (`chrome-pixel7`, `chrome-iphone15`); `next_duel`, skip and history run in one serial `describe`
(`mode: "serial"`) on `chrome` only.
Specs:
- duels: **blind round from a lineage** (the sibling root's track page → "Blind round" → lands on
  `/duels/<id>` of the round's duel, which shows "A"/"B" and no title or seed text); **synced start and
  8 ms crossfade** (own duel; Play → exactly two `start` calls with identical `when` and `offset`; press
  `b` → for each gain the recorded ramp end time minus the recorded `setValueAtTime` time is 0.008 ± 1e-6,
  with targets 0 (A) and 1 (B); Space → back to A; desktop); **keyboard flow** (desktop; own duel; `2`,
  `Enter` → the reveal shows both titles and B as winner); **ladder** (`/ladder?root=…`: the UI row
  order equals the `get_ladder` API order fetched in the spec; a whisker per row; footer counts);
  **mobile toggle** (mobile; own duel; tap `ab-toggle` → ramps recorded; tap "Tie" → submit → reveal);
  **non-blind A/B with parent** (the child's track page → "A/B with parent" → `/duels/:id` shows titles
  before the verdict; slot A is the parent); **queue, skip and history** (serial, chrome: creates two
  duels; `/duels` shows an open duel; Skip shows a different id; skipping (≤ 20 times) reaches one of its
  own duels; after deciding it, the history lists it and `/duels/:id` shows the reveal via `get_duel`);
  **clip requests are signed FLAC** (`/media/[0-9a-f]{64}\.flac\?exp=` responses with `audio/flac`);
  **main player paused during a duel** (play a track, open the own duel, Play → THE `<audio>` is paused);
  **nav badge** (the badge request is `list_duels?state=open&limit=1`; no `next_duel` request before the
  Duels page opens).
- ears: the `syn-ears-clip`, `syn-ears-gap`, `syn-ears-loop` track pages show `clipping`,
  `internal_silence`, `token_loop_suspected` respectively and the blind-spot list; **spectrogram is a
  JPEG ≤ 100 000 bytes** (the `/media/*.jpg` response: `image/jpeg`, body length ≤ 100 000;
  `naturalWidth > 0`); **click seeks** (click at the plot's horizontal middle → `jumpTo` → THE player's
  `currentTime` within ± 0.5 s of duration/2).
- compare: `/compare?a=<sibling 1>&b=<sibling 2>` shows the LUFS delta row and relation `siblings`.
Served-operation coverage (M0 teardown) must list zero unserved ops; M2a's leak-scanner teardown
(`GET /__e2e/leaks`) stays `[]` (it skips `/media/` bodies; no HAR is recorded).
Done: `make e2e` green on all four projects; CI e2e ≤ 5 min.

### Task M2b-X1 (Integration)
Merge lanes into `m2b`; `make tools hooks check test e2e gate cover routing` green; regenerate
`openapi.json` + `schema.d.ts` + the web fixtures; `tools/list` golden within budget; update
`metrics/REPORT.md` (check/e2e/CI times, coverage, ladder benchmark, spectrogram qualities chosen); review
(spec + quality + security: blind redaction and its documented limit (D-M2b-4), critic exclusion incl.
the schema CHECK, human-source semantics, idempotent create, GET-no-DB-write, argv rule, master untouched,
cache cap and pins, image size cap, no audio over MCP, nullable schemas); copy D-M2b-1…22 into the
private design document (§3.5, §3.6, §5.5, §6 amendments); confirm every M0/M1/M2a plan amendment listed
in the header is recorded; create the public plan copy, stage it and run `scripts/public-gate.sh --staged`
(rev 3: `make gate` checks tracked files, so it also passes only after the copy is committed).

### Task M2b-X2 (Deploy + backfill, operator-assisted; host details only in private notes)
1. Deploy via the host's `deploy.sh` (not in this repository; private notes) exactly as M1-X2 step 2 (clean detached worktree of `origin/main`, backup
   first, no bare `docker compose up`); the `serve` log shows no `tier_a_unavailable` line.
2. `docker exec metamusic metamusic analyze --dry-run` → review → `docker exec metamusic metamusic
   analyze` (Jobs 1); record duration and the largest spectrogram size in the private notes; `get_audit`
   shows one `cli:analyze` row.
3. Check `derived/` growth and that `MM_CLIP_CACHE_MB` fits the host.
4. Smoke: `get_track_analysis` and `get_track_spectrogram` over MCP with a PAT (image arrives as
   ImageContent); `get_track_context` shows `perception.tier "A"` and `_links.spectrogram`; a signed
   `/media/<sha>.jpg` → 200 `image/jpeg`; a blind duel plays on both phones (DC-M2b-1…5).
5. First real blind round over sibling seeds; confirm the ladder and the slot-A diagnostic look sane.
6. Inspect the first real `semantic.npy` (shape and dtype only) and record it; if the time-axis rule
   (D-M2b-11) is wrong, open a fix before trusting token-loop flags.
7. Tag `v0.3.0-m2b` per the Definition of done (rev 3: the repository has no tags yet; M1 left none and
   M2a's intended `v0.2.0-m2a` must exist first — create it at the M2a merge commit if the operator has
   not).

## Definition of done (M2b)
- All named tests above green; `make check` < 30 s warm; `make test` green with media tools; `make e2e`
  green on all four projects with zero unserved ui ops and an empty leak list; `make gate` green;
  coverage gates met.
- BT recovery, critic exclusion and the 100 000-byte spectrogram budget are proven by the three
  `TestAcceptance_M2b_*` tests named in the roadmap mapping, all running in CI.
- Every M2b op is reachable over REST, MCP and stdio with equal JSON (image ops per C52), its MCP
  structured content validates against its output schema, `tools/list` stays inside the re-baselined
  ceiling (D-M2b-22), and every op with a UI route is served by the SPA in Playwright.
- M2a's gate covers every M2b op (audit, rate limits, idempotent `create_duel`); M2a's registry-derived
  acceptance tests include the M2b ops; `/context` carries the ladder, the Ears perception and the
  spectrogram link.
- No GET writes the database; masters are untouched by clips and analysis; the clip cache is bounded and
  never evicts an open duel's clips.
- No real titles, artists, lyrics, hostnames or personal data in any tracked file; no `.jpg`/`.jpeg`/`.npy`
  is tracked; the public plan copy contains nothing below the private-appendix marker.

## Risks / open questions
- **iOS `decodeAudioData` of 24-bit FLAC** is unverified on current iOS; DC-M2b-2 decides. Fallback (not
  built): a `format=wav` clip variant (one ffmpeg argument, same key scheme with `"format"` added).
- **Headless audio in CI.** The duel specs assert scheduling calls, not audible output, so they do not
  depend on `currentTime` advancing; if Firefox headless cannot create an `AudioContext`, run the
  duel specs on Chrome projects only and keep Firefox for ears/compare (record in the task summary).
- **ffmpeg drift** (CI LGPL static build vs Alpine runtime): parsers are frame-agnostic with golden
  captures; tolerances on time (± 0.05–0.1 s) and loudness (± 0.2 LU); the spectrogram geometry check
  fails closed; analyzer versions and clip keys record the tool token; capability checks are split so an
  older runtime ffmpeg degrades Tier A or clips instead of breaking import.
- **Spectrogram budget margin.** Short full-band material needs q65 at the first plot size (D-M2b-9);
  a future overlay with more text rows eats into that margin — the budget ladder still has q55/q45 and two
  smaller sizes.
- **Uncalibrated thresholds** (silence −50 dB/1 s, clipping rule, token-loop rules, band limit 16 kHz):
  flags are heuristics labelled as such; changing one bumps the definition version; real-data
  calibration is recorded privately (M2b-X2).
- **`semantic.npy` shape** unknown until a real result dir arrives (D-M2b-11).
- **Ladder semantics.** The anchor only ever wins, so `u` drifts down for often-rejected takes as data
  grows; it is a ranking score, documented in the API description and UI ("u is not a probability").
  Small data ⇒ `provisional`. Position bias is visible through `slot_a_win_rate`.
- **Agents can de-blind duels** (D-M2b-4): critic verdicts are labelled with `saw_reveal` (server
  disclosure only) and never reach the ladder; the M6 Critic Bench must treat critic blindness as
  unverified.
- **CPU and rate limits in the request path.** `create_duel` runs up to four short ffmpeg passes (≈ 1–2 s
  for a 4-minute master); renders are bounded to 2 concurrent and detached from callers; both clip ops use
  the heavy bucket, so a 15-pair "Blind round" takes ≈ 45 s for a human (6 at once, then 12/min) — the SPA
  shows the wait and reuses each pair's idempotency key.
- **Replayed `create_duel`** returns stored clip links that may have expired (agents: 15 min); the op
  description tells agents to call `get_duel`, and the SPA re-mints on 403/404.

---

## Amendments by M2c (reference and lossy imports)

M2c amends this plan additively: C44 the hard-coded schema-version assertion becomes 5; C45
`library.TrackRef` gains `Lossy` and `RecipeKnown`; C46/C51 clip and Ears work-dir links keep the
master's extension and Ears adds the `lossy_source` blind spot; C53 `duels.TrackBrief` and `ears.Side`
gain `recipe_known` and `lossy` (a duel's brief stays null until reveal, so the flags cannot leak slot
identity); C54 the reveal and compare views show the badges.
