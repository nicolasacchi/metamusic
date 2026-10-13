# M2a — Judge, Edit, Agent Parity — Implementation Plan (DRAFT, revision 3)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, `docs/plans/2026-10-12-m0-skeleton.md` (contracts C1–C9 incl. the C3
> and C6 addenda) and the M1 plan (contracts C10–C24, migration `0002_library.sql`, D-M1-1…10) first.
> M2a builds on those exact contracts and continues the numbering at **C25**. Where M2a changes an
> earlier contract (C1 Config, C3 auth, C4 `api.Meta`/`Deps`, C5 MCP adapter, C6 `httpserver.Options`,
> C11 store rules, C17 library, C19 ops, C22 web, C24 routing), the task that makes the change also
> records it in that document in the same commit (named in each task's **Files**).
>
> **Public/private split.** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` is public-safe (no real hostnames, IP addresses, titles, artists, lyrics,
> request-id prefixes, counts from the real archive or personal data; `mm.example.com`,
> `ntfy.example.com` and synthetic examples only). Only that part is copied to
> `docs/plans/<date>-m2a-judge-edit-parity.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`); the copy is `git add`ed and
> `scripts/public-gate.sh --staged` must pass before it is committed (the pre-commit hook runs the same
> script; `make gate` checks the tracked tree). The appendix is never copied.
>
> **Task ids** carry the `M2a-` prefix (M2a-B1, M2a-B2a, M2a-W1, M2a-I1, M2a-X1 …). Lane labels
> (`MM_LANE`): `m2a-found`, `m2a-domain`, `m2a-drafts`, `m2a-data`, `m2a-agent`, `m2a-web`, `m2a-infra`,
> `m2a-int`.
>
> **Revision 2** applies the review of revision 1: export validation vs the M1 fixtures, body limits and
> the 413 code, `json.RawMessage` instead of Huma `RawBody`, ModeApp = `ParseRequest`, Stage-1 type
> freeze (B2 and B4 split into B2a/B2b and B4a/B4b), `export.Source`, one service constructor
> (`internal/app`), idempotency leases, cross-process backup guard, stdio re-verification, agent-visible
> personal data, an E2E leak scanner instead of HARs, and the test-clock rules (C42).
>
> **Revision 3** reconciles the plan with the merged M0 + M1 code (branch `m2a` starts at `main` after
> the M1 merge): every M0/M1 symbol, file, table, op id, test name, Makefile target, script, CI job and
> config field this plan references or amends was checked against the code. The corrections are indexed
> in "Baseline reconciliation" below and applied in place, each marked "(rev 3)".

**Goal:** the operator and any agent with a token can judge and edit the imported library through the
same registry: human stars and status (agent ratings kept apart as "critic"), tags, general and
timestamped comments whose timestamps jump the player, a draft editor with `lint_recipe` and JSONL batch
upload into drafts, playlists with MP3 ZIP export carrying current tags, a `songs.jsonl` export that
re-emits an imported file byte for byte (reporting exactly the validator issues that file already had)
and whose library, draft and for-batch selections pass a mirror of the `yue2 batch` validator, nightly backups
with a proven restore into an empty directory, a bounded `/context` bundle for LLMs, an append-only audit
log, rate limits and idempotency on every create, `/human/` pages for tokens and device pairing, a
`mcp-stdio` face, and listen-event logging that survives page unloads and offline periods.

**Architecture:** every request that reaches the Huma mux — REST from the SPA or a script, MCP over
HTTP, and the new `mcp-stdio` — passes one **gate** (`internal/gate`, C29) wrapped around the inner mux:
audit recorder → authorize → rate limit → idempotency → Huma (validation → handler). The MCP adapter
derives idempotency keys and marks the transport; stdio builds the same API + gate in-process with the
token's agent principal, re-verified before every tool call. One constructor, `internal/app.Build`
(C39), wires every service for `serve`, `dev`, `mcp-stdio` and the test rig, so the faces cannot drift.
New domain services (`judgments`, `drafts`, `playlists`, `exports`, `backup`,
`trackctx`, `listening`) sit beside M1's `library`; migration `0003_judge.sql` adds their tables with
append-only / no-delete triggers. Device pairing adds a `device` principal (cookie `mm_dev`, scopes
read+write) resolved by the auth middleware, with public `/start` and `/pair` pages. Export artifacts
are content-addressed files served through M1's signed `/media/` handler. The SPA gains judging UI on the
player bar and track page, a draft editor, playlists, export, backups, audit and `/human/` pages, and a
listen-event recorder with an IndexedDB outbox inside `web/src/api/`.

**Tech stack:** no new Go module except promoting `golang.org/x/time` (already in the module graph via
the MCP SDK) to a direct dependency for `rate.Limiter` (D-M2a-10). ID3v2.4 tags, ZIP (`archive/zip`,
Store method), Python-compatible JSON encoding, language detection and the batch-validator mirror are
stdlib Go. Web: one new runtime dependency, a zero-dependency QR encoder (`uqr`, MIT — verify the
current release with context7 / `bun info` at implementation; fall back to a vendored MIT QR encoder if
it is unmaintained). No IndexedDB shim: the outbox is unit-tested against its in-memory store.
Pin versions at implementation time.

**Out of scope (YAGNI, with owner milestone):** duels, clips, ladder, spectrogram, Tier-A flags,
`compare_tracks` (M2b); estimates, confirmation tokens, spend, budgets per token, approvals,
`request_approval`, jobs, `wait_for_job`, env fingerprints (M3); OAuth connectors, Subsonic,
`derived/dl/` FLAC downloads with current tags, offline sets, cover art, MCP resources and prompts
(M4a); Song PRs, callbacks and the `addressed`/`regressed` comment transitions they drive, Receipts view,
walkie-talkie notes (M5); `purge` (not before Release 1; no-delete triggers stay unconditional);
`get_track_lyrics`, `get_track_analysis`, `get_songwriting_guide`, `get_style_vocabulary` (the context
bundle carries the data); rating history, tag renames/merges, comment threads; off-host backup (D7);
`listen.flac` (D11); API-triggered import (M3 runner).

---

## Baseline reconciliation (revision 3: merged M0 + M1 code)

Checked against `main` after the M1 merge (M1 plan revision 3 and the code it produced). Where the plan
and the code disagreed, the plan was fixed (not the code); each fix is marked "(rev 3)" at its place.

**Verified as the plan assumed.** Migration `0002_library.sql` and `user_version` 2; every M1 table,
trigger and column the plan reads or amends (`recipe_drafts_frozen` freezes exactly `id, origin,
source_recipe_id, created_at`; `tracks` keeps `title, slug, status, status_set_by` mutable;
`sections.source` is `lyrics|plan|measured|manual`; `recipe_sources` has `source_path, line_no, raw,
raw_kind, import_run_id`; `human_sessions.step_up_at` already exists in `0001_init.sql`);
`github.com/modelcontextprotocol/go-sdk` v1.8.0 (`RequestExtra.Header`, `ServerSession.InitializeParams`
exist), Huma v2.39.1, `golang.org/x/time` v0.15.0 as an indirect dependency; `PAYLOAD_TOO_LARGE`,
`UNAVAILABLE`, `NOT_FOUND`, `HUMAN_ONLY`, `FORBIDDEN_SCOPE` problem codes; `serve.Object{RelPath,
ContentType, ETag, ModTime}`, `serve.Resolver`, `serve.ErrNotFound`, `serve.Signer.Sign(sha, ext, ttl,
bucket)`; the 90 000-byte page cap (`maxPageBytes`); stdlib `uuid.NewV7`; the generated SPA client already
sends `X-MM-Client: spa` on every request and the served-operation recorder already accepts it or
`?mm_client=spa`; `TestNoReplaceSQL` (`internal/store/noreplace_test.go`); exactly three `DELETE FROM
human_sessions` statements (`internal/auth/local.go`) and no other physical delete; the synthetic
`acceptance` and `e2e` profiles (`music/songs.jsonl` with the `syn-song-01` seeds 1001 and 2001, one
`{not json` line, an `abc_path` row); `scripts/loop` allowlist already holds `cover`, `routing`, `seed`;
Makefile targets `tools hooks check test e2e gate cover routing fixture-db openapi seed`; CI jobs `go`,
`lint`, `web`, `e2e`, `coverage` (`ci.yml`), `routing` (`routing.yml`), `gate` (`gate.yml`); gitleaks
rules for `mmp_`, `mmc_` and `mmd_`.

**Corrections applied.**

| Area | Plan revision 2 said | Merged code has | Fix (where) |
|---|---|---|---|
| `tools/list` size | "M0's golden is about 4 KB", ceiling 40 000 compact bytes | M1 re-baselined `toolsListBaselineBytes` 4 412 → 42 997 (`internal/mcpgen/parity_test.go`); the golden is indented JSON of 43 683 bytes (21 011 compact, 11 tools); relative budget tests are `TestToolsListGolden` / `TestToolsListBudgetFires` | C41, B4a, DoD |
| Registry/parity test names | `TestRegisterPanics`, `TestParityEveryOperation` | `TestRegisterPanics`, `TestParityEveryOperation` with `parityArgs`/`parityFixture`/`needsFixture` | renamed; C42 runner extends them |
| Clocks | all stores read the rig clock | `TokenStore` calls a package-level `now()`; `LocalAuth.now` is unexported (tests assign it) | C26 (B1 adds `UseClock`) |
| Unauthenticated tail | row 7 clears/redirects | M0 tail: API 401 problem; local HTML 303 `/auth/login`; proxy HTML 401 plain text | C26 note |
| Device-aware M0/M1 code | only the middleware changes | `serve.TTLFor`, `get_draft` reference gate, `whoami` `kind` enum check `KindHuman`/`human,agent` only | C33, C38, B4a |
| Version pins in M1 tests | none | `TestMigration0002_Applies` and `TestMigration0002_UpgradeFromM0` assert `user_version == 2` | B1 updates them |
| Upgrade-test helper | new `storetest.OpenAtVersion` package | `store.OpenRaw` + `store.MigrateTo` in `internal/store/export_test.go` | `storetest` dropped (B1, B2b, C42) |
| Mother relink | amend `library.RelinkVariantiMothers` | the importer relinks in `internal/importer/apply.go` (step 6 and `relinkMothers`); `library.RelinkVariantiMothers` has no caller; warnings go through `s.issues.warn`; existing drafts reach the importer via `library.SnapDraft` | C27, B4b |
| Draft origins | new origins just work | `library.enumOrigin = import_backlog, varianti_import` is Go-validated | B4b |
| Fixture API | `WithFiles(data *os.Root)`, clock 2026-10-12 | `WriteFiles(tb, dataDir string)`, `FixedNow` = 2026-10-06T12:00:00Z, tracks `TrackLuna/Citta/TwinA/TwinB/Short`, drafts `Draft1…6` | C42 |
| Shared synthetic archive | `archivetest.SyntheticArchive` | `internal/importer` tests import `librarytest`, so `librarytest` cannot import `importer` (import cycle) | new test-only package `internal/archivetest` (C42) |
| Track search | `library.TrackQuery` | `library.SearchParams` builds the `WHERE` | C33 `MatchTrackIDs(ctx, SearchParams)` |
| Problem codes | `RATE_LIMITED` is new | `codeFor(429)` already returns it | C28 table note |
| MCP dispatch | `dispatch` reads `req.Session` | `dispatch(ctx, inner, spec, args)` gets no request; the HTTP handler is `Stateless: true` | C30 |
| ModeApp codes | "fixed code → key table" | the 17 `ParseError` codes and 8 warning codes are listed | C32 |
| BPM lint | `bpm_out_of_range` for any BPM | `StyleTerms` hides BPM tokens outside 40–250 | C32 |
| SPA error hooks | `api/session.ts` handles 401/403/429 | response hooks live in the `client.ts` middleware; `session.ts` is the loss detector (`IDEMPOTENT = GET/HEAD/PUT`) | C40, W1, W4 |
| Router | `/human/*` excluded by routing rules | `SERVER_PREFIXES` in `web/src/lib/router.ts` lists `/human/api` but not the pages | C40 (W1 adds `/human`, `/start`, `/pair`) |
| Player API | `seek` + `play()` when paused | `play(id, queue?, {startAt})`, `load`, `resume`, `seek`; no event emitter | C40.2, C40.3 |
| E2E teardown | new `web/e2e/global-teardown.ts` | served-operation check is the closure returned by `globalSetup`; projects `chrome`, `chrome-pixel7`, `chrome-iphone15`, `firefox`; the seed archive is `<DataDir>/inbox-synthetic` | C41, W5 |
| Traefik labels | middlewares `sec-ratelimit`, `sec-headers` | shared middlewares are `sec-ratelimit@docker`, `sec-headers@docker`; `mm-strip-identity`, `mm-compress` are local | C41 |
| Gate path globs | "public-gate path globs", `TestGitignore_BackupAnchored` | `forbidden_path` in `scripts/public-gate.sh`; shell self-test `scripts/public-gate-selftest.sh` | C25, I2 |
| CLI | `run(args, env)` | `run(ctx, args, getenv, in, out, errw)`; `boot()` in `cmd_serve.go`; `import`/`seed-synthetic` use `config.Load` | C25, C39 |
| Container layout | `MM_BACKUP_DIR` next to `MM_DATA_DIR` | `Dockerfile`: `MM_DATA_DIR=/data`, `VOLUME /data` (default backup dir `/backup` would be outside the volume); compose binds the inbox at `/data/inbox` | I2 |
| Docs | `docs/runbook.md` | file does not exist yet (new) | I2 |

**M1 deviations and measurements carried into M2a.**
- `make check` measured 33 s median and 37 s at M1-X1 (target 30 s was missed); `make test` 47 s; `make
  e2e` 1.2 min locally for 124 tests, CI e2e 3–6.3 min (the 5-minute figure below stays a goal, not a
  gate); coverage `internal/...` 89.3 % (gate 85), `internal/auth` 95.7 %, `internal/media/serve` 96.6 %.
- Two CI-only e2e failures were real bugs found only in CI (header grid overflow on narrow viewports with
  wide system fonts; a Firefox audio-sink error misread as a FLAC decode failure): new UI is checked on
  the `chrome-pixel7` and `chrome-iphone15` projects, and Firefox audio-sink errors stay ignored in specs.
- `Player` caches links until `expires_at - 10 min` but never more than half the TTL (short agent/test
  TTLs); the media Handler answers multi-range requests with `multipart/byteranges` (D-M1-9);
  `get_device_probe`/`put_device_profile`/`list_device_profiles` (`/api/device-profiles`) are the *player*
  profiles; M2a's `devices` (paired browsers) is a different concept and table.
- M1 left no git tag; `v0.2.0-m2a` is still the intended tag name for X2.

## Decisions recorded by this plan

These amend or refine the design of record; M2a-X1 copies them into the private design document.

| Id | Decision | Why |
|---|---|---|
| D-M2a-1 | A paired device is a new principal kind `device` (scopes `read`,`write`). `ActsAsHuman()` is true for `human` and `device` (ratings, tags and comments count as human; either may edit the other's comments), but `HumanOnly` ops accept only `human`, and the device cookie is never read under `/human/` (so it is never cleared there either). | Design §4.2 says devices "count as human" for judging but must never approve or mint credentials; a separate kind makes the refusal structural instead of path-dependent. |
| D-M2a-2 | One gate wraps the inner Huma mux in this order: **audit recorder → authorize → rate limit → idempotency → Huma**. Audit is outermost so refused calls (403, 428, 429, 409) are recorded too. | Design §4.1's chain lists audit after budget; recording refusals is what "every agent call" means. Both REST and MCP (and stdio) dispatch into the same inner handler, so one wrapper covers all faces. |
| D-M2a-3 | Create ops declare `Idempotency-Key` as a **required** header in OpenAPI (truthful for REST). The MCP adapter marks `idempotency_key` optional and derives a key when it is omitted. Only 2xx responses are stored; any other outcome releases the key. | REST callers must send keys (§4.4); LLM clients do not, so the adapter derives them (§4.1). Storing errors would make a corrected retry with the same key impossible. |
| D-M2a-4 | Associations are never physically deleted through the API: `track_tags` rows carry `removed_at`/`removed_by`; playlists keep their ordered track ids as a JSON array column with a `revision`. | CLAUDE.md rule 5 (status/hidden flags only) applies to associations too; a JSON list keeps reordering atomic without row churn. |
| D-M2a-5 | Backups also hardlink the **derived renditions referenced by `renditions`** (MP3, peaks), copied on `EXDEV`, and **copy** the operator-authored `import/mapping.csv`, `import/mapping.todo.csv` and `import/reports/*.json`; exports and the device probe are excluded. Amends design §7 ("derived files are excluded"). | A restore must be playable without a rebuild command and future re-imports depend on the mapping; hardlinks cost no space on the shared mount, renditions are ~15 % of master bytes when copied, and the mutable `import/` files are copied because a hardlink would share later in-place edits. |
| D-M2a-6 | `songs.jsonl` export emits an imported recipe's **raw source bytes** when they pass the batch-validator mirror, otherwise a regenerated line in the **detected style**. The style (key order, separators, `ensure_ascii`) is detected at export time from `recipe_sources` and not stored in `settings`. Amends §3.6 (`recipes.import_raw`, `settings` style row): M1 already keeps every raw occurrence in `recipe_sources`. | A raw line that YuE2 would reject (for example one carrying an extra key the importer dropped) must not be exported verbatim; detection from the immutable sources cannot go stale. |
| D-M2a-7 | Exports are **create** operations that write content-addressed files (`derived/exports/<aa>/<sha>.{jsonl,zip}`) and return signed `/media/` links (agent TTL 15 min). Playlist ZIPs hold the MP3 renditions with an ID3v2.4 tag written in Go at export time (current title, playlist album, stars) — no re-encode. A recipe export that needs `abc_path` targets becomes a ZIP bundle (`songs.jsonl` + ABC files). | Large bodies never travel through MCP results (~100k-character cap); content addressing makes repeated exports free; tags are "current" because they are written per export. |
| D-M2a-8 | Listen events carry client-generated UUIDv7 ids and are inserted with `ON CONFLICT(id) DO NOTHING` (natural idempotency, no `Idempotency-Key`). The beacon URL carries `?mm_client=spa`, read by the gate only and never declared as an op parameter. | `sendBeacon` cannot set headers; per-event ids make outbox re-sends harmless. |
| D-M2a-9 | Pairing: an authenticated human on `/human/devices` mints an 8-symbol Crockford-base32 code (5 min, single use, shown with a QR of `<PublicURL>/pair#<code>`); the new device redeems it on the public `/pair` page, which sets `mm_dev`. The PWA manifest `start_url` becomes `/start` (public chooser: continue with SSO / pair this device; skipped when a valid device cookie or a remembered SSO choice exists). New public routes `/start`, `/pair`, `/pair.js`, `/auth/pair` on a rate-limited `mm-pair` router; paired requests use the design's `mm-device` router. | An iOS standalone PWA may not share Safari's cookie jar and cannot type a URL, so it needs an Authelia-free entry point; the fragment keeps the code out of access logs. |
| D-M2a-10 | Rate limits are in-memory token buckets per principal key (`golang.org/x/time/rate`): agents 60/min (burst 60) plus a heavy bucket 6/min for exports, uploads and backups; humans and devices 600/min (burst 200) plus heavy 12/min; `/auth/pair` 10/min global. Each process (serve, mcp-stdio) has its own buckets. `MM_RATE_SCALE` (default 1) multiplies every rate and burst; the E2E setup sets 100. | Design §4.4 states 60/min per principal; the SPA legitimately bursts (peaks, links, beacons), so humans get a higher ceiling that still stops runaway loops. |
| D-M2a-11 | `create_draft` + `update_draft` replace the design's `save_recipe_draft`. | Create needs an idempotency key, update needs an optimistic `revision`; one upsert op would need both and be ambiguous for LLMs. |
| D-M2a-12 | One current rating row per source (`human`, `agent`); a DB `CHECK` ties `source='human'` to principal kinds `human`/`device`. No rating history in M2a (the audit row records who changed it, never the value). | Rating isolation becomes a schema invariant, not only a code path. |
| D-M2a-13 | Local-mode step-up for `/human/` is a server-rendered `/auth/step-up` form (password re-entry, 10 min); `/human/api/*` without a fresh step-up returns 403 `STEP_UP_REQUIRED` and the SPA navigates to the form. | Design §4.2 requires step-up in local mode; a server-rendered form avoids a private SPA endpoint (CLAUDE.md rule 1). |
| D-M2a-14 | `x-mm` gains `idempotency` (`create`), `rate` (`heavy`) and `secret_fields`; only human-only create ops may declare secret fields, and the idempotency store nulls those fields (a replay returns metadata only). | Design §4.4 ("responses that carry a secret are stored redacted") made machine-checkable; `TestNoSecretLeak` uses the same list. |
| D-M2a-15 | The gate writes the audit row after the handler returns and before the response is delivered (rev 5), in its own transaction (a crash between the op's commit and the audit insert loses that row). M3 spend operations will write their audit row inside the op transaction. | Keeps every M2a handler free of audit plumbing; the window is milliseconds and no money moves in M2a. |
| D-M2a-16 | A comment's section label is computed once, at creation, from timed sections only (`manual` > `measured` > `plan`) and stored; `section_source` has a `CHECK` that excludes `lyrics`. | §6: an LLM must never edit "chorus 1" on a guess; storing the label keeps old comments stable when M5 adds measured sections. |
| D-M2a-17 | Minimal push notifications over ntfy (`MM_NTFY_URL` + optional secret `ntfy_token`): backup failed, device paired, token created. Messages are published as JSON (no user-chosen text in HTTP headers). | The operator supplies the publisher token at M2a (roadmap inputs); silent backup failures are the main risk this milestone adds. |
| D-M2a-18 | When `search_tracks` has no `status` filter, `discarded` tracks are excluded (amends C19). | `update_track status=discarded` is the agent counterpart of purge; discarded takes must leave the default view. |
| D-M2a-19 | **Agents never see the operator's username or device names.** Every principal reference in a response (`author`, `by`, `principal`, `created_by`, `updated_by`, `status_set_by`) carries `Principal.Key()` as its id; for a caller that is not `ActsAsHuman`, a `human` reference renders as `{kind:"human", id:"human", name:"human"}` and a `device` reference as `{kind:"device", id:"device:<id>", name:"device"}` (key-valued string fields: `human:<x>` ⇒ `"human"`). `list_devices` is human-only (`/human/api/devices`). | Third-party MCP transcripts must not collect personal data (M1 C16.9 reasoning); one rendering function makes it testable. |
| D-M2a-20 | A `source` export **is** the original file: it equals the imported bytes and reports exactly the validator issues that file had (for example a `duplicate_id` between two seeds of one request). `tracks` and `drafts` exports always make ids unique (the for-batch rule) and must validate clean; `for_batch` applies that rule to a `source` export. | Byte identity and "passes `yue2 batch`" cannot both hold for a file that `yue2 batch` itself rejects (the M1 synthetic `songs.jsonl` and real seed variants share ids). |
| D-M2a-21 | An idempotency row in `pending` carries a 2-minute **lease**, refreshed while the handler runs; a retry with the same key and body after the lease lapsed reclaims the row. Creates are therefore **at-least-once after a crash** between the handler's commit and `Complete` (exactly-once otherwise). | Without a lease a crash would answer every correct retry with 409 for 24 h; M3 spend ops will commit their idempotency row inside the op transaction. |
| D-M2a-22 | Device-self-service ops (`revoke_own_device`) are `mcp: exclude:device-self-service`: no agent can ever be a device. | Listing it for MCP would make "every UI action over MCP" fail by construction (always 403 `NOT_A_DEVICE`). |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C25. Config, secrets, data layout, leaf packages

New env (added to `config.Config`; M2a-B1 amends M0 C1 / M1 C10):

```go
BackupDir string // MM_BACKUP_DIR, default filepath.Join(filepath.Dir(DataDir), "backup")
BackupAt  string // MM_BACKUP_AT, "HH:MM" in the process time zone (TZ), default "03:30"; "off" disables the schedule
NtfyURL   string // MM_NTFY_URL, default "" (notifications off); https:// required unless the host is loopback
RateScale int    // MM_RATE_SCALE, default 1, 1..1000: multiplies every rate-limit class (C29); the E2E setup sets 100
```
`Load` errors (naming the variable, never a value) when: `BackupAt` is neither `off` nor `^([01]\d|2[0-3]):[0-5]\d$`;
`BackupDir` equals, contains or is contained in `DataDir` (`filepath.Rel` check after `filepath.Abs`);
`NtfyURL` is not a valid absolute URL, carries a query string or fragment, or carries userinfo
(`https://user:pass@…` — credentials belong in `ntfy_token`); `RateScale` is not an integer in 1..1000.

With the default `MM_DATA_DIR=./data` the default `BackupDir` is `./backup` in the working directory:
M2a-I2 adds the **root-anchored** pattern `/backup/` to `.gitignore` (an unanchored `backup/` would also
ignore the `internal/backup/` package) and a root-anchored `backup/*` case to `forbidden_path` in
`scripts/public-gate.sh` (rev 3: that function is the gate's path list; hooks and CI run the same script).

**Offline loading.** M0's `config.Load` refuses to run without `MM_AUTH`. M2a-B1 adds
`config.LoadOffline(getenv func(string) string) (Config, error)`: the same parsing and checks except the
`AuthMode` check (`AuthMode` stays as given, possibly ""). Every non-serving command — `mcp-stdio`,
`export`, `backup`, `restore`, `validate-batch`, and M0's `token`/`user` commands — uses `LoadOffline`
plus `LoadSecrets(dir, "local")` (no `edge_secret` needed); only `serve` and `dev` use `Load`. (rev 3)
M0's `boot(ctx, getenv, ensureSecrets)` in `cmd/metamusic/cmd_serve.go` (shared by `serve`, `dev`, `token`,
`user`) gains an `offline` flag that selects `LoadOffline`; M1's `import` and `seed-synthetic` commands keep
`config.Load` (they already require `MM_AUTH`; `make seed` and the E2E setup set it).
`TestCLI_OfflineCommandsWithoutMMAuth` runs `mcp-stdio` (in-memory transport) and `restore` with
`MM_AUTH` unset.

`config.Secrets` gains `NtfyToken string` from the optional file `ntfy_token` (missing ⇒ empty; present
but empty ⇒ error naming the file).

Data layout additions:
```
<DataDir>/derived/exports/<aa>/<sha>.{jsonl,zip}  export artifacts, 0444, reproducible, never backed up; swept after 7 days (C33)
<DataDir>/incoming/export-<export_id>/            export work dir (0700; removed on success or failure)
<BackupDir>/                                      0700
  <yyyymmddThhmmssZ>[-N]/                         one complete snapshot (C34; -2, -3 … when the second is taken)
  .<yyyymmddThhmmssZ>[-N].partial/                snapshot in progress; removed when older than 24 h
```
Production keeps `DataDir` and `BackupDir` under one bind mount (design §3.5), so hardlinks work;
anything else falls back to verified copies (C34).

**Leaf packages** (import nothing from `internal/` unless stated):

- `internal/canonjson` — deterministic JSON for hashing:
  ```go
  func Canonical(raw []byte) ([]byte, error)
  // Objects: keys sorted by byte order; arrays in order; numbers copied as their exact JSON tokens
  // (json.Decoder.UseNumber); strings re-encoded with SetEscapeHTML(false); no whitespace.
  // Errors: invalid JSON, invalid UTF-8, duplicate object keys, depth > 64.
  ```
- `internal/reqinfo` — request facts carried in the context (never from client-controlled headers
  except the `spa` label, which the gate honours only for cookie/proxy principals — C29):
  ```go
  type Transport string // "rest" | "spa" | "mcp" | "stdio" | "cli" | "pair"
  func WithTransport(ctx context.Context, t Transport) context.Context
  func TransportFrom(ctx context.Context) (Transport, bool)
  func WithClient(ctx context.Context, label string) context.Context // sanitised on entry
  func Client(ctx context.Context) string
  func SanitizeClient(s string) string // first product token, printable ASCII only, ≤ 120 bytes, "" if none
  ```
- `internal/redact` — the only path for HTTP-client errors into logs or rows (CLAUDE.md rule 9):
  ```go
  func String(s string) string // masks: mmp_/mmd_/mmc_ credentials, "Bearer <x>", "Authorization: <x>",
                               // URL query strings ("?<redacted>"), runs of ≥ 32 hex or base64url chars
  func Error(err error) string // String(err.Error()); nil ⇒ ""
  ```
- `internal/notify` — C37.

**C25 addendum (M3-B1):** `internal/redact` also masks `Cookie:`/`Set-Cookie:` lines and the RunPod endpoint id in
job-API paths (`/v2/<endpoint>/run`, `/v2/endpoints/<endpoint>`); `redact.WithEndpoint(id)` returns a value
`Redactor` that also replaces the configured id literally anywhere (no process global). New leaf packages
`internal/money` and `internal/clock` (+ test-only `clock/clocktest`), `store.FormatTime`/`ParseTime` and
`store.LeaseFence`. See the M3 plan, C68 and C80.

### C26. Principals, devices, pairing and step-up (amends M0 C3)

```go
const KindDevice Kind = "device"
const KindSystem Kind = "system" // CLI actions in audit rows only; never produced by the middleware

type Principal struct {
    Kind     Kind
    ID       string    // human: username; agent: token id; device: device id; system: "cli"
    Name     string    // human: username; agent: token name; device: device name
    Scopes   []Scope   // human: all four; device: read, write; agent: token scopes
    Via      string    // "proxy" | "local" | "token" | "device" | "cli"
    StepUpAt time.Time // local sessions only (zero otherwise)
}
func (p Principal) Key() string       // "human:<id>" | "device:<id>" | "agent:<id>" | "system:cli"; "" for anonymous
func (p Principal) ActsAsHuman() bool // Kind == KindHuman || Kind == KindDevice
var CLI = Principal{Kind: KindSystem, ID: "cli", Name: "cli", Via: "cli"}
```
`Key()` is the value stored in every M2a `principal_id` column, rate-limit key and idempotency key, and
the `id` of every principal reference in API responses and the `get_audit principal_id` filter
(D-M2a-19). Rendering for agents goes through one function, `api.RefFor(viewer auth.Principal, key, kind,
name string) PrincipalRef` (and `api.KeyFor(viewer, key) string` for key-valued string fields), which
applies the D-M2a-19 masking; no handler builds a principal reference by hand.

**Token store** (amends M0, M2a-B1): `NewTokenStore(st *store.Store, key []byte, now func() time.Time)`
(every `time.Now` in `tokens.go` goes through `now`; callers pass `time.Now` or the test clock) and
`(*TokenStore).Get(ctx, id string) (TokenInfo, error)` (`ErrNotFound`), so `revoke_token` and
`revoke_own_token` can return `TokenInfo`. (rev 3) M0's `tokens.go` formats times with a package-level
`now() string` helper; it is replaced by the injected clock. `LocalAuth` keeps its unexported `now` field
(M0 tests assign it); M2a-B1 adds an exported `(*LocalAuth).UseClock(now func() time.Time)` so
`app.Build` can hand it the rig clock (`TestLocalAuth_UseClock`), and `DeviceStore` takes its clock in the
constructor.

**Devices** (`internal/auth/devices.go`, `internal/auth/pairing.go`):
```go
const (
    DeviceCookie   = "mm_dev"
    DeviceTTL      = 90 * 24 * time.Hour // sliding
    deviceSlide    = time.Hour           // server-side expiry slides at most hourly
    deviceTouch    = time.Minute         // last_seen_at written at most once a minute
    PairingTTL     = 5 * time.Minute
)
var (ErrInvalidDevice = errors.New("invalid device"); ErrInvalidCode = errors.New("invalid pairing code"))
type DeviceInfo struct{ ID, Name, Prefix, UserAgent, PairedBy, CreatedAt, LastSeenAt, ExpiresAt, RevokedAt string; Scopes []Scope }
type PairingCode struct{ ID, Name, CreatedBy, CreatedAt, ExpiresAt string }
type DeviceVerifier interface{ VerifyDevice(ctx context.Context, cookie string) (Principal, error) }

func NewDeviceStore(st *store.Store, key []byte, now func() time.Time, newID func() string) *DeviceStore
func (d *DeviceStore) CreatePairingCode(ctx context.Context, by Principal, name string) (code string, pc PairingCode, err error) // by.Kind must be KindHuman
func (d *DeviceStore) Redeem(ctx context.Context, code, userAgent string) (cookie string, dev DeviceInfo, err error)
func (d *DeviceStore) VerifyDevice(ctx context.Context, cookie string) (Principal, error) // slides expiry; touches last_seen_at
func (d *DeviceStore) List(ctx context.Context) ([]DeviceInfo, error)                   // never returns hashes
func (d *DeviceStore) Revoke(ctx context.Context, id string) (DeviceInfo, error)        // idempotent; ErrNotFound
```
- **Pairing code:** 8 symbols from the Crockford alphabet `0123456789ABCDEFGHJKMNPQRSTVWXYZ` (40 bits),
  displayed `XXXX-XXXX`. Input normalisation: uppercase, drop `-` and spaces, `I`/`L` → `1`, `O` → `0`;
  anything else ⇒ `ErrInvalidCode`. Stored as `hex(HMAC-SHA256(key, "pair\n" + normalised))`. `name`
  (1–80 chars, the device label chosen on the desktop) is stored with the code.
- **At most one live code:** `CreatePairingCode` sets `revoked_at = now` on every unredeemed, unexpired
  code in the same write transaction that inserts the new one (this is the only writer of
  `pairing_codes.revoked_at`).
- **Redeem** (one write tx): the code row must be unredeemed, unrevoked and unexpired ⇒ insert the
  `devices` row, mark the code redeemed with its `device_id`, commit. Any failure ⇒ `ErrInvalidCode`
  (one error for wrong/expired/used/superseded codes). `userAgent` is truncated to 512 bytes at a UTF-8
  boundary before the insert (the column `CHECK` must never turn a long header into a 500).
- **Device cookie value:** `mmd_` + 52 lowercase base32 chars of 32 random bytes (gitleaks rule
  `mmd_` exists since M0). Stored as `hex(HMAC-SHA256(key, cookie))`; `prefix` = first 8 chars after
  `mmd_`. Cookie attributes: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` iff `PublicURL` is https,
  `Max-Age` = 90 days; re-issued only when the server-side expiry slid.
- `VerifyDevice` rejects revoked or expired rows with `ErrInvalidDevice`; infrastructure errors are
  returned as-is (middleware ⇒ 500 `INTERNAL`, as for tokens).

**Middleware** (M2a-B4a changes the signature; M2a-B9 implements rows 6–7):
```go
func Middleware(cfg config.Config, sec config.Secrets, tokens TokenVerifier, sessions SessionVerifier, devices DeviceVerifier) func(http.Handler) http.Handler
// devices may be nil (device auth disabled). Callers pass a nil *interface*, never a typed-nil
// *DeviceStore: app.Build/httpserver convert `(*DeviceStore)(nil)` to a nil DeviceVerifier
// (TestHTTPServer_NilDevicesNoPanic), and VerifyDevice on a nil receiver returns ErrInvalidDevice.
```
Resolution order (C3 table amended; rows 1–3 unchanged):

| # | Condition | Result |
|---|---|---|
| 4 | ClassUI, mode=proxy, edge + `Remote-User` rules hold | human (unchanged) |
| 5 | ClassUI, mode=local, valid `mm_session` | human; `StepUpAt` from the session row |
| 6 | ClassUI, rows 4/5 did not match, path **not** under `/human/`, `mm_dev` cookie present ⇒ read it; valid | device principal |
| 7 | otherwise | anonymous. Only a cookie that was **read and rejected** in row 6 is cleared (`Max-Age=0`) — a cookie under `/human/` (or on ClassMachine/ClassPublic paths) is never read and therefore never cleared, so opening `/human/` on a paired phone does not unpair it. After a rejection, HTML routes answer **303 `/start?expired=1`** in both modes (API routes keep 401 problem `UNAUTHENTICATED`). |

`CrossOriginProtection` applies to device principals (cookie principal). A device cookie on ClassMachine
or ClassPublic paths is never read. (rev 3) In M0's code the UI-class tail answers an unauthenticated
request with a 401 problem for `/api/` and `/human/api/`, a 303 `/auth/login` for HTML in local mode and a
401 plain-text page for HTML in proxy mode; row 7 changes only the case where a device cookie was read and
rejected (303 `/start?expired=1` in both modes). A request without any device cookie keeps the M0 tail.
`TestUnauthenticatedResponses` (M0) stays green unchanged.

**Step-up (local mode, D-M2a-13).** `LocalAuth.StepUp(ctx, cookieValue, password string) error` verifies
the password (argon2id, bounded like `Login`) and sets `human_sessions.step_up_at = now`. `VerifySession`
fills `Principal.StepUpAt`. In local mode, a human request whose path starts with `/human/` and whose
`StepUpAt` is older than 10 minutes gets: `/human/api/*` ⇒ 403 problem `STEP_UP_REQUIRED`; HTML ⇒ 303
`/auth/step-up?next=<path>`. `LocalAuth.Handler()` adds `GET /auth/step-up` (form, frame-ancestors
none) and `POST /auth/step-up` (CrossOriginProtection, 1 s `FailDelay` on failure, redirect to `next`
only when it starts with `/human/` and contains no `//`, `\` or control characters, else `/human/tokens`).
Proxy mode: `/auth/step-up` is 404 (the edge's two-factor rule covers `/human/`).

**Classify** (amends M0 C3): `/start`, `/pair`, `/pair.js`, `/auth/pair` ⇒ ClassPublic (exact paths).
`/auth/step-up` stays ClassUI (it needs the session).

**Public pairing pages** (`auth.PairingHandler`, mounted by httpserver; server-rendered, no SPA):
```go
type PairingOptions struct {
    Cfg      config.Config
    Devices  *DeviceStore
    Sessions SessionVerifier // local mode: a valid mm_session on /start goes straight to "/"
    Allow    func() (ok bool, retryAfter time.Duration) // global "pair" bucket (C29)
    OnPaired func(ctx context.Context, d DeviceInfo)    // httpserver: audit row (transport "pair") + notify
    FailDelay time.Duration                             // default 1 s
}
func PairingHandler(o PairingOptions) http.Handler
```
- `GET /start`: valid `mm_dev` ⇒ 302 `/`; local mode with a valid `mm_session` ⇒ 302 `/`; cookie
  `mm_start=sso` present ⇒ 302 `/` (proxy) or `/auth/login` (local); else a chooser page with two
  buttons — "Continue with sign-in" (a form POST to `/start` that sets `mm_start=sso`, 400 days,
  `SameSite=Lax`, then 303 to `/` or `/auth/login`) and "Pair this device" (link to `/pair`). With
  `?expired=1` the page says the device session ended and clears `mm_start`. `POST /start` applies
  `CrossOriginProtection` (as `/auth/pair` does); a cross-site POST ⇒ 403 `CROSS_ORIGIN`.
- `GET /pair`: form (code input `autocomplete="one-time-code"`, `inputmode="text"`, maxlength 9) plus
  `<script src="/pair.js" defer>`. `GET /pair.js` (static, embedded in `internal/auth`): copies
  `location.hash` (sans `#`) into the input and calls `history.replaceState(null, "", "/pair")`.
- `POST /auth/pair` (CrossOriginProtection; body ≤ 1 KiB form): `Allow()` false ⇒ 429 page with
  `Retry-After`; `Redeem` ⇒ `ErrInvalidCode` ⇒ sleep `FailDelay`, 401 page "Code not valid or expired";
  success ⇒ `Set-Cookie: mm_dev=…`, `OnPaired`, 303 `/`.
- All pages: `Cache-Control: no-store`, `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`. Pages never echo the submitted code.

### C27. Migration `internal/store/migrations/0003_judge.sql` (full DDL) and store rules

After M2a, `PRAGMA user_version == 3`. As in C11, `CHECK` constraints encode only invariants that never
change; growing enumerations (comment kinds and sources, listen kinds, export kinds, draft origins,
audit transports, backup causes) are validated in Go.

```sql
-- 0003_judge.sql — M2a: draft revisions, ratings, tags, comments, playlists, listen events,
-- exports, audit log, idempotency, devices, pairing codes, backup runs.

-- Drafts become editable (C32). New columns only; M1's recipe_drafts_frozen keeps
-- id, origin, source_recipe_id and created_at frozen.
ALTER TABLE recipe_drafts ADD COLUMN revision   INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1);
ALTER TABLE recipe_drafts ADD COLUMN created_by TEXT NOT NULL DEFAULT 'import';
ALTER TABLE recipe_drafts ADD COLUMN updated_by TEXT NOT NULL DEFAULT 'import';
CREATE TRIGGER recipe_drafts_created_by_frozen BEFORE UPDATE OF created_by ON recipe_drafts
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: frozen column'); END;
CREATE TRIGGER recipe_drafts_fulfilled_frozen BEFORE UPDATE OF request_id, request_json ON recipe_drafts
WHEN OLD.status = 'fulfilled'
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: fulfilled drafts are immutable'); END;

CREATE TABLE ratings (                                 -- one current row per (track, source); D-M2a-12
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  source         TEXT NOT NULL CHECK (source IN ('human','agent')),
  stars          INTEGER CHECK (stars IS NULL OR stars BETWEEN 1 AND 5),      -- NULL = cleared
  verdict        TEXT CHECK (verdict IS NULL OR length(verdict) BETWEEN 1 AND 280),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id   TEXT NOT NULL,                        -- Principal.Key()
  principal_name TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (track_id, source),
  CHECK ((source = 'human') = (principal_kind IN ('human','device')))   -- rating isolation, at the schema level
) WITHOUT ROWID;
CREATE INDEX ratings_source_stars ON ratings(source, stars);
CREATE TRIGGER ratings_no_delete BEFORE DELETE ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: no physical delete'); END;
CREATE TRIGGER ratings_frozen BEFORE UPDATE OF track_id, source, created_at ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: frozen column'); END;

CREATE TABLE tags (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),  -- normalised (C31)
  created_at TEXT NOT NULL
);
CREATE TRIGGER tags_immutable BEFORE UPDATE ON tags
BEGIN SELECT RAISE(ABORT, 'tags: immutable'); END;
CREATE TRIGGER tags_no_delete BEFORE DELETE ON tags
BEGIN SELECT RAISE(ABORT, 'tags: no physical delete'); END;

CREATE TABLE track_tags (
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  tag_id         INTEGER NOT NULL REFERENCES tags(id),
  source         TEXT NOT NULL CHECK (source IN ('human','agent','inherited')),
  inherited_from TEXT REFERENCES tracks(id),           -- the parent track (source = 'inherited' only)
  principal_id   TEXT NOT NULL,                        -- who added or re-added it
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  removed_at     TEXT,                                 -- D-M2a-4: soft removal
  removed_by     TEXT,
  PRIMARY KEY (track_id, tag_id, source),
  CHECK ((source = 'inherited') = (inherited_from IS NOT NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
) WITHOUT ROWID;
CREATE INDEX track_tags_active ON track_tags(tag_id, track_id) WHERE removed_at IS NULL;
CREATE TRIGGER track_tags_no_delete BEFORE DELETE ON track_tags
BEGIN SELECT RAISE(ABORT, 'track_tags: no physical delete'); END;
CREATE TRIGGER track_tags_frozen BEFORE UPDATE OF track_id, tag_id, source, inherited_from, created_at ON track_tags
BEGIN SELECT RAISE(ABORT, 'track_tags: frozen column'); END;

CREATE TABLE comments (
  id                 TEXT PRIMARY KEY,                 -- UUIDv7
  track_id           TEXT NOT NULL REFERENCES tracks(id),
  body               TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
  at_second          REAL CHECK (at_second IS NULL OR at_second >= 0),
  section_label      TEXT,
  section_source     TEXT CHECK (section_source IS NULL OR section_source IN ('plan','measured','manual')),  -- never 'lyrics' (D-M2a-16)
  section_confidence REAL CHECK (section_confidence IS NULL OR section_confidence BETWEEN 0 AND 1),
  kind               TEXT NOT NULL,                    -- Go-validated: note|fix|keep|marker
  state              TEXT NOT NULL,                    -- Go-validated state machine (C31): open|addressed|resolved|regressed
  source             TEXT NOT NULL,                    -- Go-validated: web|api|mcp|stdio (subsonic, headset later)
  principal_kind     TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id       TEXT NOT NULL,
  principal_name     TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  edited_at          TEXT,
  state_changed_at   TEXT,
  state_changed_by   TEXT,
  hidden_at          TEXT,
  hidden_by          TEXT,
  CHECK ((section_label IS NULL) = (section_source IS NULL)),
  CHECK (section_label IS NULL OR at_second IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL)),
  CHECK ((state_changed_at IS NULL) = (state_changed_by IS NULL))
);
CREATE INDEX comments_track ON comments(track_id, created_at);
CREATE INDEX comments_open  ON comments(track_id) WHERE state = 'open' AND hidden_at IS NULL;
CREATE TRIGGER comments_no_delete BEFORE DELETE ON comments
BEGIN SELECT RAISE(ABORT, 'comments: no physical delete'); END;
CREATE TRIGGER comments_frozen BEFORE UPDATE OF
  id, track_id, at_second, section_label, section_source, section_confidence, source,
  principal_kind, principal_id, principal_name, created_at ON comments
BEGIN SELECT RAISE(ABORT, 'comments: frozen column'); END;
-- Mutable: body, kind, state, updated_at, edited_at, state_changed_*, hidden_*.

CREATE TABLE playlists (
  id          TEXT PRIMARY KEY,                        -- UUIDv7
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
  items_json  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(items_json) AND json_type(items_json) = 'array'),
  items_count INTEGER NOT NULL DEFAULT 0 CHECK (items_count = json_array_length(items_json)),
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  created_by  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  hidden_at   TEXT
);
CREATE TRIGGER playlists_no_delete BEFORE DELETE ON playlists
BEGIN SELECT RAISE(ABORT, 'playlists: no physical delete'); END;
CREATE TRIGGER playlists_frozen BEFORE UPDATE OF id, created_by, created_at ON playlists
BEGIN SELECT RAISE(ABORT, 'playlists: frozen column'); END;

CREATE TABLE listen_events (                           -- D-M2a-8; immutable telemetry
  id             TEXT PRIMARY KEY,                     -- client UUID (v7 recommended)
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  session_id     TEXT NOT NULL CHECK (length(session_id) BETWEEN 8 AND 64),
  kind           TEXT NOT NULL,                        -- Go-validated: start|segment|seek|complete|skip
  from_ms        INTEGER CHECK (from_ms IS NULL OR from_ms >= 0),
  to_ms          INTEGER CHECK (to_ms IS NULL OR to_ms >= 0),
  format         TEXT,                                 -- Go-validated: flac|mp3|NULL
  device_id      TEXT,                                 -- C22.3 device-profile id (not a credential)
  source         TEXT NOT NULL,                        -- Go-validated: web|api|mcp|stdio
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id   TEXT NOT NULL,
  client_at      TEXT NOT NULL,
  received_at    TEXT NOT NULL,
  CHECK (to_ms IS NULL OR from_ms IS NULL OR to_ms >= from_ms)
);
CREATE INDEX listen_events_track ON listen_events(track_id, client_at);
CREATE TRIGGER listen_events_immutable BEFORE UPDATE ON listen_events
BEGIN SELECT RAISE(ABORT, 'listen_events: immutable'); END;
CREATE TRIGGER listen_events_no_delete BEFORE DELETE ON listen_events
BEGIN SELECT RAISE(ABORT, 'listen_events: no physical delete'); END;

CREATE TABLE exports (                                 -- D-M2a-7
  id           TEXT PRIMARY KEY,                       -- UUIDv7
  kind         TEXT NOT NULL,                          -- Go-validated: songs_jsonl|songs_bundle|playlist_zip
  params_json  TEXT NOT NULL CHECK (json_valid(params_json)),
  sha256       TEXT NOT NULL,
  bytes        INTEGER NOT NULL CHECK (bytes > 0),
  path         TEXT NOT NULL,                          -- derived/exports/<aa>/<sha>.<ext> (DataDir-relative)
  filename     TEXT NOT NULL CHECK (length(filename) BETWEEN 1 AND 120),
  summary_json TEXT NOT NULL CHECK (json_valid(summary_json)),
  principal_id TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX exports_sha ON exports(sha256);
CREATE TRIGGER exports_immutable BEFORE UPDATE ON exports
BEGIN SELECT RAISE(ABORT, 'exports: immutable'); END;
CREATE TRIGGER exports_no_delete BEFORE DELETE ON exports
BEGIN SELECT RAISE(ABORT, 'exports: no physical delete'); END;

CREATE TABLE audit_log (                               -- design §4.4; append-only
  id               TEXT PRIMARY KEY,                   -- UUIDv7 (time-ordered; keyset cursor)
  at               TEXT NOT NULL,
  principal_kind   TEXT NOT NULL,                      -- human|device|agent|system|anonymous
  principal_id     TEXT NOT NULL,
  principal_name   TEXT NOT NULL,
  transport        TEXT NOT NULL,                      -- Go-validated: rest|spa|mcp|stdio|cli|pair
  client           TEXT NOT NULL DEFAULT '',           -- reqinfo.SanitizeClient
  op               TEXT NOT NULL,                      -- operationId, 'cli:<cmd>', 'auth:pair' or '-' (no op matched)
  method           TEXT NOT NULL,
  args_sha256      TEXT NOT NULL,                      -- hash of the canonical request; never the args
  status           INTEGER NOT NULL,
  error_code       TEXT,
  cost_micro_usd   INTEGER NOT NULL DEFAULT 0 CHECK (cost_micro_usd >= 0),
  idempotency      TEXT NOT NULL,                      -- none|stored|replayed|conflict|in_progress|missing
  created_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(created_ids_json)),
  duration_ms      INTEGER NOT NULL CHECK (duration_ms >= 0)
);
CREATE INDEX audit_log_at        ON audit_log(at);
CREATE INDEX audit_log_principal ON audit_log(principal_id, id);
CREATE INDEX audit_log_op        ON audit_log(op, id);
CREATE TRIGGER audit_log_immutable BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log: append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log: append-only'); END;

CREATE TABLE idempotency (                             -- cache table: pruned after expiry (no triggers)
  principal_key  TEXT NOT NULL,
  key            TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
  op             TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  derived        INTEGER NOT NULL CHECK (derived IN (0,1)),
  state          TEXT NOT NULL CHECK (state IN ('pending','done')),
  status         INTEGER,
  content_type   TEXT,
  response       BLOB,                                 -- secret fields nulled (C29)
  created_at     TEXT NOT NULL,
  lease_until    TEXT NOT NULL,                        -- pending rows: reclaimable after this (D-M2a-21)
  expires_at     TEXT NOT NULL,
  PRIMARY KEY (principal_key, key),
  CHECK ((state = 'done') = (status IS NOT NULL AND response IS NOT NULL))
) WITHOUT ROWID;
CREATE INDEX idempotency_expires ON idempotency(expires_at);

CREATE TABLE devices (                                 -- paired device sessions (C26)
  id           TEXT PRIMARY KEY,                       -- UUIDv7
  name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  prefix       TEXT NOT NULL,
  hmac_hash    TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL CHECK (scopes = 'read,write'),   -- never generate or admin
  user_agent   TEXT NOT NULL CHECK (length(user_agent) <= 512),
  paired_by    TEXT NOT NULL,                          -- Principal.Key() of the human who minted the code
  created_at   TEXT NOT NULL,
  last_seen_at TEXT,
  expires_at   TEXT NOT NULL,
  revoked_at   TEXT
);
CREATE TRIGGER devices_no_delete BEFORE DELETE ON devices
BEGIN SELECT RAISE(ABORT, 'devices: no physical delete'); END;
CREATE TRIGGER devices_frozen BEFORE UPDATE OF id, prefix, hmac_hash, scopes, paired_by, created_at ON devices
BEGIN SELECT RAISE(ABORT, 'devices: frozen column'); END;

CREATE TABLE pairing_codes (
  id          TEXT PRIMARY KEY,
  hmac_hash   TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  redeemed_at TEXT,
  device_id   TEXT REFERENCES devices(id),
  revoked_at  TEXT,
  CHECK ((redeemed_at IS NULL) = (device_id IS NULL))
);
CREATE TRIGGER pairing_codes_no_delete BEFORE DELETE ON pairing_codes
BEGIN SELECT RAISE(ABORT, 'pairing_codes: no physical delete'); END;
CREATE TRIGGER pairing_codes_frozen BEFORE UPDATE OF id, hmac_hash, name, created_by, created_at, expires_at ON pairing_codes
BEGIN SELECT RAISE(ABORT, 'pairing_codes: frozen column'); END;
CREATE TRIGGER pairing_codes_single_use BEFORE UPDATE OF redeemed_at, device_id ON pairing_codes
WHEN OLD.redeemed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'pairing_codes: already redeemed'); END;

CREATE TABLE backup_runs (
  id           TEXT PRIMARY KEY,                       -- UUIDv7 (= manifest id)
  cause        TEXT NOT NULL,                          -- Go-validated: schedule|api|cli
  principal_id TEXT NOT NULL,
  state        TEXT NOT NULL CHECK (state IN ('running','completed','failed')),
  snapshot     TEXT,                                   -- snapshot directory name (never an absolute path)
  started_at   TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,
  finished_at  TEXT,
  stats_json   TEXT CHECK (stats_json IS NULL OR json_valid(stats_json)),
  error        TEXT                                    -- redacted (internal/redact)
);
CREATE INDEX backup_runs_started ON backup_runs(started_at);
CREATE UNIQUE INDEX backup_runs_one_running ON backup_runs(state) WHERE state = 'running';  -- cross-process guard (C34)
CREATE TRIGGER backup_runs_no_delete BEFORE DELETE ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs: no physical delete'); END;
CREATE TRIGGER backup_runs_final BEFORE UPDATE ON backup_runs WHEN OLD.state <> 'running'
BEGIN SELECT RAISE(ABORT, 'backup_runs: finished runs are immutable'); END;
CREATE TRIGGER backup_runs_frozen BEFORE UPDATE OF id, cause, principal_id, started_at ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs: frozen column'); END;
```

**Write SQL (binding; services implement exactly these forms):**
- Rating: `INSERT INTO ratings(track_id,source,stars,verdict,principal_kind,principal_id,principal_name,created_at,updated_at)
  VALUES(…) ON CONFLICT(track_id,source) DO UPDATE SET stars=excluded.stars, verdict=excluded.verdict,
  principal_kind=excluded.principal_kind, principal_id=excluded.principal_id,
  principal_name=excluded.principal_name, updated_at=excluded.updated_at`. `source` is computed from the
  principal (`ActsAsHuman` ⇒ `human`, else `agent`) — no request field can select it.
- Tag name: `INSERT INTO tags(name,created_at) VALUES(?,?) ON CONFLICT(name) DO NOTHING` then
  `SELECT id FROM tags WHERE name=?`.
- Tag add: `INSERT INTO track_tags(track_id,tag_id,source,inherited_from,principal_id,created_at,updated_at)
  VALUES(…) ON CONFLICT(track_id,tag_id,source) DO UPDATE SET removed_at=NULL, removed_by=NULL,
  principal_id=excluded.principal_id, updated_at=excluded.updated_at WHERE track_tags.removed_at IS NOT NULL`.
- Tag remove: `UPDATE track_tags SET removed_at=?, removed_by=?, updated_at=? WHERE track_id=? AND tag_id=?
  AND source IN (…) AND removed_at IS NULL`.
- Inheritance (M2a-B4b adds it to M1's `library.InsertTake`, in the take's transaction, when
  `parent_track_id` is set): `INSERT INTO track_tags(…) SELECT ?, tag_id, 'inherited', ?, 'system:inherit', ?, ?
  FROM track_tags WHERE track_id=? AND source IN ('human','inherited') AND removed_at IS NULL
  GROUP BY tag_id ON CONFLICT DO NOTHING`.
- **Edited drafts are never touched by the importer** (M2a-B4b amends M1 C11/C16.4/C17). (rev 3) The real
  write sites are: `library.(*Service).fulfil` (the own-recipe lookup and its `UPDATE`, and the `core_hash`
  fallback's lookup and `UPDATE`), the importer's two mother writes in `internal/importer/apply.go` (the
  step-6 `UPDATE` and `relinkMothers`) and the unused `library.RelinkVariantiMothers`; all of them add
  `AND revision = 1` to their `recipe_drafts … WHERE` clauses. `library.Fulfilment` gains `EditedSkipped
  []string` (drafts whose recipe matched but whose `revision > 1`); `apply.go` turns it into the import
  warning `draft_edited_not_fulfilled` through `s.issues.warn` (as it does for `fulfilled_by_core_match`),
  and the two mother paths count skipped drafts and warn `draft_edited_not_relinked`, each naming the draft
  id. A draft with `revision > 1` whose recipe would have matched stays as edited (status, request and a
  `parent_track_id` the human cleared are kept). `library.SnapDraft` gains `Revision` so `planMothers` and
  the plan digest (`inventory.go`) ignore edited drafts. Tests: `TestInsertTake_EditedDraftNotFulfilled`,
  `TestRelink_SkipsEditedDrafts` (library and importer level).
- Draft update: `UPDATE recipe_drafts SET request_id=?, request_json=?, group_key=?, label=?, status=?,
  parent_track_id=?, revision=revision+1, updated_by=?, updated_at=? WHERE id=? AND revision=? AND
  status <> 'fulfilled'` — 0 rows ⇒ re-read to tell 404 / `DRAFT_NOT_EDITABLE` / `REVISION_CONFLICT`.
- Playlist update: `UPDATE playlists SET name=?, description=?, items_json=?, items_count=?, hidden_at=?,
  revision=revision+1, updated_by=?, updated_at=? WHERE id=? AND revision=?`.
- Listen events: `INSERT INTO listen_events(…) VALUES(…) ON CONFLICT(id) DO NOTHING` (affected rows = accepted).
- Device touch: `UPDATE devices SET last_seen_at=?, expires_at=? WHERE id=? AND revoked_at IS NULL`.
- M1 rules still hold: never `INSERT OR REPLACE`/`REPLACE INTO` (`TestNoReplaceSQL` covers the new
  files); a frozen column never appears in an UPDATE's SET list; every write path above has a
  run-it-twice test.
- Physical `DELETE` statements exist only on the two **cache tables**: `idempotency` (prune, release)
  and M0's `human_sessions` (`internal/auth/local.go`: logout, password reset, expired-session sweep).
  `TestNoDeleteSQL` greps `internal/**/*.go` (tests excluded) and fails on any `DELETE FROM` whose table
  is not in its allowlist `{idempotency, human_sessions}`; the allowlist is named in the test and in
  this paragraph only.
- Backup runs: `INSERT INTO backup_runs(…, state) VALUES(…, 'running')` fails on
  `backup_runs_one_running` while another process runs a backup ⇒ `BACKUP_IN_PROGRESS` (C34).
- Idempotency `Begin`, `Extend`, `Complete`, `Release`: the exact forms in C29.

**C27 addendum (M3-B1):** the two assertions that hard-coded the schema version (`internal/httpserver/server_test.go`
healthz `schema_version`, `internal/store/schema_m2c_test.go`) now compare with the store's `user_version` /
`>= 5`; migration `0006_generate.sql` (schema version 6) adds the M3 tables. See the M3 plan, C69.

### C28. Registry amendments (amends M0 C4, M1 C19)

> M2b-B2 addendum: `Meta` gains `MCPImage` (after `SecretFields`); `Deps` gains `Duels`, `Ears` and `Clips` (concrete pointers; a nil one
> makes its operations answer 503 `UNAVAILABLE {component}`). The M2b operations are registered by `registerDuels`, `registerClips`
> and `registerEars`; `internal/api/errors_m2b.go` holds `m2bErr`.

```go
type Meta struct {
    Scope        auth.Scope `json:"scope"`
    Cost         string     `json:"cost"`
    MCP          string     `json:"mcp"`
    UI           string     `json:"ui"`
    HumanOnly    bool       `json:"human_only"`
    Idempotency  string     `json:"idempotency,omitempty"`   // "" | "create" (D-M2a-3)
    Rate         string     `json:"rate,omitempty"`          // "" (default bucket) | "heavy"
    SecretFields []string   `json:"secret_fields,omitempty"` // top-level response keys that carry a secret (D-M2a-14)
}
func Authorize(ctx context.Context, m Meta) error // exported form of M0's authorize; the gate reuses it
```
`validate` additions (each a `TestRegisterPanics` row): `Idempotency` ∉ {"", "create"}; `create` with a
method other than POST; `Rate` ∉ {"", "heavy"}; `SecretFields` non-empty unless `HumanOnly` and
`Idempotency == "create"`; a secret field not matching `^[a-z][a-z0-9_]*$`.
`TestOps_CreateOpsDeclareIdempotencyKey` (walks the OpenAPI document): every `create` op has a required
header parameter `Idempotency-Key` (`maxLength` 255); no other op declares it. Create-op inputs embed:
```go
type IdemHeader struct {
    IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255" pattern:"^[!-~]+$" doc:"Client-chosen key; a retry with the same key and body replays the first response (24 h)."`
}
```
`api.Deps` gains (registration never dereferences any of them; a nil dependency answers 503
`UNAVAILABLE` with `details.component`, as in M1):
```go
Judgments *judgments.Service; Drafts *drafts.Service; Playlists *playlists.Service
Exports *exports.Service;     Backups *backup.Service;  Context *trackctx.Builder
Listening *listening.Service; Audit *audit.Store;       Devices *auth.DeviceStore
NewID func() string // nil ⇒ uuid.NewV7().String(); tests inject a deterministic sequence
```
`api.Deps` (M0's `Tokens` is reused by the token ops) is only ever built by `internal/app.Build` (C39). Ops that need the requester's public base
URL use `Cfg.PublicURL`. No new problem code exists for 413: every body cap answers M0's
`PAYLOAD_TOO_LARGE`.

**New problem codes** (all `application/problem+json`, `{"code","message","details"}`):

| Code | Status | When |
|---|---|---|
| `IDEMPOTENCY_KEY_REQUIRED` | 428 | REST create op without `Idempotency-Key` |
| `IDEMPOTENCY_CONFLICT` | 409 | same key, different request hash (`details.op`) |
| `IDEMPOTENCY_IN_PROGRESS` | 409 | same key still pending (`Retry-After: 1`) |
| `RATE_LIMITED` | 429 | bucket empty (`Retry-After`, `details.retry_after_s`, `details.bucket`); (rev 3) M0's `codeFor(429)` already names this code, M2a adds the details |
| `REVISION_CONFLICT` | 409 | stale `revision` (`details.current_revision`) |
| `DRAFT_NOT_EDITABLE` | 409 | draft is `fulfilled` |
| `INVALID_TRANSITION` | 409 | comment state change not allowed (`details.from`, `details.to`) |
| `NOT_AUTHOR` | 403 | body/kind edit or hide toggle not allowed for this principal (C31) |
| `LINT_FAILED` | 422 | protocol errors in a request or an all-or-nothing upload (`details.items`) |
| `AT_SECOND_OUT_OF_RANGE` | 422 | `at_second` > track duration |
| `INVALID_TAG` | 422 | tag fails normalisation (`details.tag_index`) |
| `UNKNOWN_TRACK` | 422 | playlist/export/listen input names a missing track (`details.track_ids`) |
| `PAYLOAD_TOO_LARGE` | 413 | **existing M0 code** (`codeFor(413)`, also what Huma's `MaxBodyBytes` refusal produces), listed for completeness: request body above the gate cap or an op limit; a playlist ZIP above its cap (C33, `details.tracks`, `details.bytes`, `details.limit_tracks`, `details.limit_bytes`) |
| `EXPORT_EMPTY` | 422 | export selection matched nothing |
| `EXPORT_EXPIRED` | 410 | export file swept (create a new export) |
| `BACKUP_IN_PROGRESS` | 409 | a backup is already running |
| `STEP_UP_REQUIRED` | 403 | local mode, `/human/` without a fresh step-up |
| `NOT_A_TOKEN` / `NOT_A_DEVICE` | 403 | `revoke_own_token` / `revoke_own_device` called by another principal kind |
| `NOT_IMPLEMENTED` | 501 | `STUB(M2a-…)` handlers during Stage 2 only (none at X1) |

**C28 addendum (M3-B1):** `api.Meta.AliasOf`, the `spend`/`approve`/alias registration rules and `ValidateAliases`
(see the M3 plan, C80).

### C29. The gate: audit, authorize, rate limit, idempotency (`internal/gate`, `internal/audit`, `internal/idem`, `internal/ratelimit`)

```go
// internal/ratelimit
type Class string // "agent" | "agent_heavy" | "human" | "human_heavy" | "pair"
type Limit struct{ PerMinute float64; Burst int }
type Config map[Class]Limit
var Default = Config{"agent": {60, 60}, "agent_heavy": {6, 6}, "human": {600, 200}, "human_heavy": {12, 6}, "pair": {10, 5}}
func (c Config) Scaled(n int) Config                       // every PerMinute and Burst × n (Cfg.RateScale; app.Build applies it)
func New(cfg Config, now func() time.Time) *Limiter         // golang.org/x/time/rate per (class, key); idle entries pruned after 10 min
// Uses rate.Limiter's explicit-time methods (ReserveN(now(), 1)) only, so tests drive it with a fake clock.
func (l *Limiter) Allow(key string, classes ...Class) (ok bool, retryAfter time.Duration, bucket Class)
// Reserves on every class; if any reservation is not immediately available, cancels all and reports the
// longest delay (rounded up to whole seconds, ≥ 1 s) and that class.

// internal/audit
type Entry struct {
    At time.Time; Principal auth.Principal; Transport reqinfo.Transport; Client, Op, Method, ArgsSHA256 string
    Status int; ErrorCode, Idempotency string; CreatedIDs []string; Duration time.Duration
}
func NewStore(st *store.Store, newID func() string) *Store
func (s *Store) Insert(ctx context.Context, e Entry) error
func (s *Store) List(ctx context.Context, f Filter) (Page, error) // newest first, keyset cursor on id
func WithCollector(ctx context.Context) context.Context
func NoteCreated(ctx context.Context, ids ...string)              // handlers call it for every row id they create
func Created(ctx context.Context) []string

// internal/idem
type Outcome int // Proceed | Replay | Conflict | InProgress
type Begun struct{ Outcome Outcome; Status int; ContentType string; Body []byte }
func NewStore(st *store.Store, now func() time.Time) *Store
func (s *Store) Begin(ctx context.Context, principalKey, key, op, reqSHA string, derived bool) (Begun, error)
func (s *Store) Extend(ctx context.Context, principalKey, key string) error   // pending row: lease_until = now + Lease
func (s *Store) Complete(ctx context.Context, principalKey, key string, status int, contentType string, body []byte) error
func (s *Store) Release(ctx context.Context, principalKey, key string) error // deletes a pending row
func (s *Store) Prune(ctx context.Context) (int, error)                      // expires_at < now − 1 h
func RequestSHA(op, method, escapedPath string, query url.Values, body []byte) (string, error)
func DerivedKey(principalKey, op string, args json.RawMessage) (string, error) // "mcp:" + hex sha256
func WithDerived(ctx context.Context, key string) context.Context
func DerivedFrom(ctx context.Context) (string, bool)
func Redact(body []byte, fields []string) []byte // top-level fields set to null; non-object bodies unchanged
const ExplicitTTL, DerivedTTL = 24 * time.Hour, 10 * time.Minute
const Lease = 2 * time.Minute // D-M2a-21

// internal/gate
type Options struct {
    API     huma.API; Inner http.Handler; Audit *audit.Store; Idem *idem.Store; Limiter *ratelimit.Limiter
    Now     func() time.Time
    Ticker  func(d time.Duration) (c <-chan time.Time, stop func()) // lease refresh; nil ⇒ time.NewTicker; tests tick by hand
    MaxBody int64 // default 8 MiB; must be ≥ the largest op MaxBodyBytes (C38; TestOps_BodyLimits)
}
func New(o Options) http.Handler
```

**Per request** (exact order; `TestGate_Order` asserts it with spies):
1. Resolve the operation: `_, pattern := mux.Handler(r)`; map `"<METHOD> <path>"` → `api.OpInfo`
   (as M0's served-operation recorder does). Principal = `auth.FromContext`. Transport = `reqinfo`
   value if present (`mcp`, `stdio`); else `spa` when `X-MM-Client: spa` or the (undeclared) query
   parameter `mm_client=spa` is present **and** the principal is a cookie/proxy principal (`human` or
   `device`) — an agent (bearer) is always `rest`, whatever it sends; else `rest`. (rev 3) The generated SPA client
   already sends `X-MM-Client: spa` on every request (`web/src/api/client.ts`) and M1's served-operation
   recorder (`isSPARequest` in `internal/httpserver/e2e.go`) already accepts the header or the query flag;
   the gate uses that same rule (the helper moves to `reqinfo`).
   Client label = `reqinfo.Client(ctx)` (MCP client name) or `SanitizeClient(User-Agent)`.
2. Start the **audit recorder** (`audit.WithCollector`, a status-capturing writer). After the response:
   audit iff the principal is an agent (every call, reads included), or the op is human-only, or the op's
   scope is not `read`, or no op matched and the principal is an agent (op `-`). `error_code` = the
   `code` of a problem body (first 4 KiB parsed); `args_sha256` = the idempotency request hash (computed
   for every audited call). **Audit contract (rev 5): audit row visible before the response; inserts retried; a persistent failure surfaces in health.**
   For audited registry ops (and create ops) the recorder buffers status, headers and body (cap 4 MiB; past it, or on a
   handler `Flush`, the gate inserts the row first and then streams the rest unbuffered), inserts the row, and only then
   releases the response, so a `get_audit` issued after any response always lists that call. Binary/streaming routes
   (`mcp: exclude:binary`) and unmatched paths are not buffered; their row is still inserted with the same retries. The insert uses
   `context.WithoutCancel` and retries with backoff (25 ms doubling to 500 ms) inside a 15 s total budget (not a 2 s timeout),
   so transient writer contention (a backup, `-race`) cannot lose it. If it still fails the op has already committed, so the
   original response is returned unchanged (never a fake failure or 503), the failure is logged at error level (no row
   content) and `audit.Failures()` is incremented; the counter is exposed as `audit_failures` in `get_system_info` and
   as `status:"degraded"` plus `audit_failures` on `/healthz` (still HTTP 200: a restart does not repair a gap). The same hold
   makes the idempotency record durable before the response of a create op is delivered.
2a. **Rate limit (charged first, rev 4):** for every non-anonymous principal, before the body is read, before the
   unmatched pass-through and before `Authorize`: classes = `agent` for agents, `human` for humans and devices,
   plus the `_heavy` class only when an op matched and `meta.Rate == "heavy"`. Refused ⇒ 429 `RATE_LIMITED` with
   `Retry-After`. Unknown paths and scope-refused calls therefore consume tokens and cannot grow the audit log unboundedly.
   **Body reading:** for every call that will be audited (decidable from principal and meta before the
   handler) and every create op, the gate reads the body through `http.MaxBytesReader(MaxBody)` — over
   ⇒ 413 `PAYLOAD_TOO_LARGE` before any later step — and restores it for Huma. `MaxBody` (8 MiB) is ≥
   every op's `MaxBodyBytes`, so Huma's per-op limit (C38) is what normally refuses; the gate cap only
   bounds what the gate itself buffers. Calls that are neither audited nor create ops are streamed
   untouched.
3. No op matched ⇒ pass through (the M0 `notFound` handler answers).
4. `api.Authorize(ctx, meta)` ⇒ 401/403 problem (defense in depth stays in `api.Register`).
6. **Idempotency** (only `meta.Idempotency == "create"`):
   - key = `Idempotency-Key` header; else `idem.DerivedFrom(ctx)` (MCP); else 428
     `IDEMPOTENCY_KEY_REQUIRED`. TTL 24 h for header keys, 10 min for derived keys. The MCP adapter also
     sets the header, so derived-ness comes **only** from the context marker.
   - the body was read in step 2 (cap ⇒ 413 `PAYLOAD_TOO_LARGE`);
     `reqSHA = RequestSHA(op, method, EscapedPath, query, canonjson(body) or raw bytes when not JSON)`.
   - `Begin` (one write tx, one statement decides):
     ```sql
     INSERT INTO idempotency(principal_key,key,op,request_sha256,derived,state,created_at,lease_until,expires_at)
     VALUES(?,?,?,?,?,'pending',?,?,?)
     ON CONFLICT(principal_key,key) DO UPDATE SET
       op=excluded.op, request_sha256=excluded.request_sha256, derived=excluded.derived, state='pending',
       status=NULL, content_type=NULL, response=NULL, created_at=excluded.created_at,
       lease_until=excluded.lease_until, expires_at=excluded.expires_at
     WHERE idempotency.expires_at <= excluded.created_at
        OR (idempotency.state = 'pending' AND idempotency.lease_until <= excluded.created_at
            AND idempotency.request_sha256 = excluded.request_sha256)
     RETURNING created_at
     ```
     A returned row ⇒ inserted or reclaimed ⇒ `Proceed` (SQLite returns no row when the upsert's `WHERE`
     is false). No row ⇒ `SELECT` the existing row in the same tx: `done` with the same `request_sha256` ⇒
     `Replay`; a different hash (done or pending) ⇒ `Conflict` (409 `IDEMPOTENCY_CONFLICT`); `pending`
     within its lease ⇒ `InProgress` (409, `Retry-After: 1`).
   - While the handler runs, the gate calls `Extend` every `Lease/2` (60 s) until it returns, so a long
     handler (ZIP export, backup start) never loses its lease; a crashed process stops extending and the
     row becomes reclaimable after at most 2 min (D-M2a-21, at-least-once after a crash).
   - `Replay` ⇒ write the stored status, content type and body plus `Idempotent-Replayed: true`; the
     handler never runs (so nothing inside it — e.g. a single-use token in M3 — is consumed).
   - `Proceed` ⇒ serve; 2xx ⇒ `Complete` with `idem.Redact(body, meta.SecretFields)`; any other status
     (or a panic) ⇒ `Release`.
7. Serve the inner mux (Huma validation, then the handler).

Idempotency rows are pruned hourly by `serve`. Gate logs one slog line per request: method, path
(never the query), op, status, principal kind, duration.

**C29 addendum (M3-B1):** `idem.Begun.Gen` (the lease generation), `idem.CompleteTx`, `idem.WithKey/KeyFrom`,
`idem.Marks` (`WithMarks`/`MarksFrom`: mutable flags the gate installs before the handler), `audit.Entry.CostMicroUSD`,
`audit.InsertTx`, `audit.WithPending/PendingFrom`, `audit.SystemEntry`/`SystemOps` (D-M3-22) and `reqinfo.Runner`,
`auth.System(name)`. The gate audits in `commit()` only `if audited && !marks.Audited()` and skips `Idem.Complete` when
`marks.Completed()`; replays are still audited by the gate. See the M3 plan, C80 and D-M3-21.

### C30. MCP adapter, transport marks and `mcp-stdio` (amends M0 C5)

- `httpserver` passes the **gated** inner handler to `MCPFactory` (amends the C6 addendum): MCP tools
  dispatch into `gate(inner)`, never into the bare mux.
- `dispatch` sets `reqinfo.WithTransport(ctx, "mcp")` (or `"stdio"`, see below). (rev 3) M0's
  `dispatch(ctx, inner, spec, args)` does not receive the MCP request, so the two tool-handler closures
  (in `NewServer` and `generic.go`) compute the transport mark and the client label from `req` and pass
  them in (B2b changes the signature accordingly). **Client label**
  (go-sdk v1.8.0, M0 `internal/mcpgen/dispatch.go`): `buildRequest` gives the inner request a fresh
  header set (`req.Header = hdr`), so the client's HTTP headers never reach the gate by themselves.
  Dispatch therefore sets `reqinfo.WithClient` explicitly:
  (1) `req.Session.InitializeParams()` non-nil ⇒ `ClientInfo.Name + "/" + ClientInfo.Version` (**stdio** and
  in-memory sessions; rev 3: the production HTTP handler is built with `Stateless: true`, so (2) is the
  HTTP path); (2) else (stateless HTTP) ⇒ `CallToolRequest.Extra.Header.Get("User-Agent")`
  (`RequestExtra.Header` carries the HTTP request headers); (3) else "". The gate's own User-Agent
  fallback applies only to non-MCP requests. Tests: `TestDispatch_ClientLabel_Stateless` (UA from
  `Extra.Header`) and `TestDispatch_ClientLabel_Initialized` (stdio in-memory session: `ClientInfo`).
- **Derived idempotency** for `x-mm.idempotency == "create"` tools (first-class and `call_operation`):
  the input schema keeps `idempotency_key` but drops it from `required` and replaces its description with
  "Optional. Omit it and the server derives a key from (caller, operation, arguments): an identical call
  within 10 minutes replays the first result instead of acting twice. Pass distinct keys to repeat an
  action on purpose; explicit keys are remembered for 24 hours." The header parameter is also marked
  **not required in `toolSpec.Params`** (M0's `buildRequest` rejects a missing parameter whose `Required`
  flag is set), and derivation runs **before** `buildRequest`'s required-parameter check: when the
  argument is absent, `dispatch` computes `idem.DerivedKey(principal.Key(), op,
  args-without-idempotency_key)` over `canonjson.Canonical(args)`, inserts it as the `idempotency_key`
  argument (so the header is set by the normal parameter path) and sets `idem.WithDerived(ctx, key)`.
  Test: `TestDispatch_DerivedKeyBeforeRequiredCheck` (a create tool called without the argument reaches
  the gate with the derived header instead of failing as a missing required parameter).
- `describe_operation` output gains `idempotency`, `rate` and `secret_fields` from `x-mm`.
- **Stdio:**
  ```go
  type StdioOptions struct {
      Principal func(ctx context.Context) (auth.Principal, error) // called before EVERY tool call
      Version   string
  }
  func NewStdioServer(a huma.API, gated http.Handler, o StdioOptions) (*mcp.Server, error)
  // Same tools as NewServer; every dispatch context carries the principal returned for that call and
  // transport "stdio". A Principal error ⇒ the tool result is IsError with problem code UNAUTHENTICATED
  // (never the token value); infrastructure errors ⇒ INTERNAL.
  ```
  `metamusic mcp-stdio` (`cmd_mcpstdio.go`): requires `MM_TOKEN` (missing ⇒ exit 2 "MM_TOKEN is
  required"); verifies it once at start with `auth.TokenStore.Verify` (unknown or revoked ⇒ exit 2
  "MM_TOKEN rejected", never echoing the value); then passes `Principal: func(ctx) { return
  tokens.Verify(ctx, MM_TOKEN) }`, so a token revoked mid-session (on `/human/tokens` or through its own
  `revoke_own_token`) is refused from the next tool call on — the same behaviour as REST and HTTP MCP
  (`TestMCPStdio_RevokedMidSession`). It loads config with `config.LoadOffline` (no `MM_AUTH` needed) and
  secrets in `local` mode (no edge secret), opens the store and DataDir, builds everything with
  `app.Build` (its own limiter), and runs `NewStdioServer(...).Run(ctx, &mcp.StdioTransport{})`. It never
  starts the backup scheduler or the export sweep, never binds a port and logs to stderr only. On exit it
  calls `Services.Close(ctx)`, which waits (≤ 10 min) for a backup it started (C34). The principal is
  always the token's agent principal — never human.

### C31. Judgments: ratings, status/title, tags, comments (`internal/judgments`)

```go
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service
func (s *Service) Rate(ctx context.Context, p auth.Principal, trackID string, stars *int, verdict *string) (TrackRating, error)
func (s *Service) Ratings(ctx context.Context, trackID string) (TrackRating, error)
func (s *Service) HumanStars(ctx context.Context, trackIDs []string) (map[string]int, error) // rated tracks only (stars IS NOT NULL); exports.RatingsReader
func (s *Service) UpdateTrack(ctx context.Context, p auth.Principal, trackID string, title, status *string) error
func (s *Service) SetTags(ctx context.Context, p auth.Principal, trackID string, add, remove []string) (TrackTags, error)
func (s *Service) Tags(ctx context.Context, trackID string) (TrackTags, error)
func (s *Service) TagCounts(ctx context.Context) ([]TagCount, error)
func (s *Service) AddComment(ctx context.Context, p auth.Principal, src string, trackID string, in NewComment) (Comment, error)
func (s *Service) UpdateComment(ctx context.Context, p auth.Principal, id string, in CommentPatch) (Comment, error)
func (s *Service) Comments(ctx context.Context, trackID string, f CommentFilter) (CommentPage, error)
func NormalizeTag(s string) (string, error)
func Transition(from, to string) error // C31 table; ErrInvalidTransition (the principal never changes the result)
```

**Ratings.** `stars` 1–5 or `null` (clear); `verdict` ≤ 280 chars or `null`. Source comes from the
principal only (C27). Reads return both rows: `human` is canonical everywhere (`search_tracks min_stars`,
facets, `TrackSummary.stars`, ZIP `POPM`, context `track.stars`); `agent` is shown as `critic`.

**Track status/title.** `status` ∈ draft|to_relisten|final|discarded (M1 CHECK); writes
`status_set_by = p.Key()`. `title` 1–200 chars after trimming (C0/C1 controls rejected, 422); the slug
and the frozen master are unchanged (masters are never rewritten; M1 FTS trigger updates search).

**Tags.** `NormalizeTag`: Unicode NFC, lowercase, trim, collapse whitespace runs to one space, 1–40 runes,
must match `^[\p{L}\p{N}][\p{L}\p{N} &'+/._-]*$`; else `INVALID_TAG`. Per call ≤ 20 `add` + 20 `remove`;
per track ≤ 30 active tags per source (422). Effective tags = active rows of any source, deduplicated by
name, with their `sources`. A human or device `remove` removes the name from the `human`, `inherited`
**and** `agent` sources (the human is canonical); an agent `remove` touches only `agent` rows; an agent
`add` never creates a `human` row. Children inherit the parent's active `human`+`inherited` tags at
creation (C27 inheritance SQL; in M2a only the importer creates children).

**Comments.**
- `NewComment{Body string; AtSecond *float64; Kind string}` — `kind` ∈ note|fix|keep|marker (default
  `note`); `body` 1–4000 chars (trimmed; NUL rejected); `at_second` ≥ 0 and ≤ `duration_s` (else 422
  `AT_SECOND_OUT_OF_RANGE`), rounded to 0.1 s.
- **Section label rule (D-M2a-16):** only when `at_second` is set. Candidates = the track's `sections`
  rows whose `source` ∈ {manual, measured, plan} and `start_s` is not NULL; the highest-precedence
  source present wins (manual > measured > plan); within it the section with `start_s ≤ t < end_s`
  (the last section also covers `t == end_s`). **No covering section** (a gap between sections, or
  `t` after the last `end_s` — e.g. a tail beyond the score) ⇒ no label (`section: null`); the rule
  never falls back to a lower-precedence source or the nearest section. Store `section_label`,
  `section_source` and `section_confidence` (`manual` ⇒ 1.0). Lyrics-only (untimed) tracks never get a
  label.
- `source` (comment origin) = transport: `spa` ⇒ `web`, `rest` ⇒ `api`, `mcp` ⇒ `mcp`, `stdio` ⇒ `stdio`.
- New comments start in `open`.
- **State machine** (`Transition`; `TestCommentTransitions` covers all 16 pairs, each through a human
  and an agent principal to show the result does not depend on who asks):

  | from \ to | open | addressed | resolved | regressed |
  |---|---|---|---|---|
  | open | no-op | M5 only | ✓ | M5 only |
  | addressed | ✓ (reopen) | no-op | ✓ | M5 only |
  | resolved | ✓ (reopen) | M5 only | no-op | M5 only |
  | regressed | ✓ | M5 only | ✓ | no-op |

  "M5 only" ⇒ 409 `INVALID_TRANSITION` from `update_comment` (Song PR / callback flows set them later).
  Any write principal may change state; `state_changed_at/by` are recorded.
- `CommentPatch{Body, Kind *string; State *string; Hidden *bool}`. **May edit** (`body`/`kind`, setting
  `edited_at`) and **may toggle `hidden`**: an `ActsAsHuman` principal (human or device — one person,
  D-M2a-1) on any comment authored by a human or a device, and on agent comments only for `hidden`;
  an agent only on its own comments (`principal_id` equal). Anything else ⇒ 403 `NOT_AUTHOR`. So a
  phone may fix a typo in a comment written on the desktop. An empty patch ⇒ 422.
- `CommentFilter{State, Kind []string; IncludeHidden bool; Order string /* newest (default) | time */; Limit int; Cursor string}`;
  `time` orders by `at_second` (NULLs last) then `created_at`. 90 000-byte page cap (M1 C17).
- Every created id is reported with `audit.NoteCreated`.

### C32. Drafts: protocol checker, lint, batch validator mirror, editor, upload

**Protocol checker** (`internal/engine/yue2/protocol.go`; M2a-B3). One entry point, two modes.
**ModeYuE2** mirrors yue2-infer 0.1.6 `src/yue2/protocol.py` (`SongRequest`, `request_kwargs`) and
`src/yue2/cli.py` (`batch`) — each rule cites the function it mirrors in a comment. **ModeApp is M1's
`ParseRequest`** mapped to issues — there is no second app rule set, so the editor's lint and
`create_draft` can never disagree:
```go
type Mode int // ModeYuE2 (exactly what `yue2 batch` accepts) | ModeApp (M1 ParseRequest's rules)
type Issue struct {
    Code     string `json:"code"`
    Field    string `json:"field,omitempty"`   // JSON key ("seed", "lyrics", …)
    Severity string `json:"severity"`          // "error" | "warning"
    Message  string `json:"message"`           // never contains field values
    Line     int    `json:"line,omitempty"`    // lyrics line (1-based) or JSONL line
    Section  string `json:"section,omitempty"` // lyrics section label
    Fatal    bool   `json:"fatal,omitempty"`   // ModeYuE2: aborts the whole batch in 0.1.6
}
type ABCResolver func(rel string) ([]byte, error) // nil ⇒ every abc_path is abc_path_unresolved
func CheckRequest(raw []byte, m Mode, abc ABCResolver) []Issue // raw = one JSON value (the request)
```
- **ModeApp** = `ParseRequest(raw, ParseOptions{ResolveABCPath: abc})`: the `ParseError` ⇒ one `error`
  issue with its `Code` (and `Field` from a fixed code → key table, e.g. `seed_out_of_range` ⇒ `seed`);
  every `Parsed.Warnings` entry ⇒ a `warning` issue with the same code. Unknown and ignored keys
  (`title`, `lang`, `eval_index`, `clip_id`, any other) are therefore **warnings `dropped_key:<k>`**
  (`Field: k`) — exactly what `create_draft` accepts — and the seed range is D-M1-7's [0, 2^53 − 1]. (rev 3) M1's `ParseError` codes are `abc_conflict,
  abc_path_unresolved, abc_requires_cot, bad_abc, bad_cfg_scale, bad_cot, bad_id, bad_utf8, empty_abc,
  missing_id, missing_lyrics, missing_style, not_object, prompt_mismatch, sampling_unsupported,
  seed_out_of_range, style_tags_mismatch`; `Parsed.Warnings` is a `[]string` of codes (`bom`, `crlf`,
  `tags_alias`, `cot_defaulted`, `seed_defaulted`, `cfg_scale_default_dropped`, `prompt_verified`,
  `dropped_key:<k>`); the code → `Field` table covers exactly these.
- **ModeYuE2** rules (errors unless noted; a non-object ⇒ `not_object`; duplicate keys: last wins, like
  Python's `json.loads`):
  1. Allowed keys: `id, style, tags, lyrics, cot, seed, abc, abc_path, cfg_scale, abc_sampling,
     semantic_sampling, lang, eval_index, clip_id, prompt`. Any other key (incl. `title`) ⇒
     `unknown_key`, **fatal** (0.1.6 raises outside the per-row try and aborts the remaining batch).
  2. `id` required string, `^[A-Za-z0-9][A-Za-z0-9_.-]{0,179}$`, not `.`/`..` ⇒ `missing_id` / `bad_id`.
  3. `style`/`tags`: at least one string ⇒ `missing_style`; both ⇒ must be equal ⇒ `style_tags_mismatch`.
  4. `lyrics` required string ⇒ `missing_lyrics`.
  5. `cot` default `full`; ∈ {full, melody, off} ⇒ `bad_cot`.
  6. `seed` default 831001; an integer JSON token (no fraction, exponent or bool) in [0, 2^63) ⇒
     `seed_out_of_range`.
  7. `abc`: non-null ⇒ string, non-empty after strip (`empty_abc`); with `cot=off` ⇒ `abc_requires_cot`;
     with `abc_path` ⇒ `abc_conflict`. `abc_path` ⇒ resolved relative to the JSONL file's directory
     (unresolvable ⇒ `abc_path_unresolved`) ⇒ content then obeys the `abc` rules.
  8. `cfg_scale`: null ok; finite number in [0, 20] ⇒ `bad_cfg_scale`.
  9. `prompt` (non-null) must equal `Instructions[cot] + "\n[Tags]\n" + style + "\n[Lyrics]\n" + lyrics + "\n"`
     ⇒ `prompt_mismatch`.
  10. `abc_sampling`, `semantic_sampling`: must be JSON objects ⇒ `bad_sampling` (contents not
      validated — documented gap, confirmed in M3's worker-contract job).
  11. Any string that is not valid UTF-8 ⇒ `bad_utf8`.
`TestCheckRequest_AgreesWithParseRequest` (property over the M1 `TestParseRequest` rows) guards the
identity: every row `ParseRequest` rejects yields exactly one ModeApp error with the same code, every
accepted row yields no error, and the ModeApp warnings equal `Parsed.Warnings`.

**Batch validator mirror** (`internal/engine/yue2/batch.go`; M2a-B3):
```go
type BatchReport struct {
    Lines, Requests int
    OK, Fatal       bool
    Issues          []Issue // Line set; sorted by line
}
func ValidateBatch(jsonl []byte, dir fs.FS) BatchReport // dir = the JSONL file's directory (abc_path targets)
```
Lines split on `\n`; a trailing `\r` is JSON whitespace; whitespace-only lines are skipped (verify the
0.1.6 iteration rule and cite it); each line must parse as a JSON value (`bad_json`) that is an object
(`not_object`); `CheckRequest(line, ModeYuE2, resolver over dir)`; ids unique across the file
(`duplicate_id` on the later line, `Field: "id"`). `OK` = no issues. Also exposed as
`metamusic validate-batch FILE` (exit 0 OK, 1 issues, 2 usage).

**Lint** (`internal/engine/yue2/lint.go`, `duration.go`, `lang.go`). **M2a-B3 declares every type and
signature below in Stage 1** (function bodies `// STUB(M2a-B5)` returning zero values; no Stage-1 test
asserts lint values), so `openapi.json` and `schema.d.ts` are frozen with them after M2a-B4a; M2a-B5
implements the bodies.
```go
type LintItem struct {
    Request        json.RawMessage // the request object to lint (see "what lint runs on")
    DraftID        string          // the draft being linted, if any
    SourceRecipeID string          // that draft's frozen source recipe, if any
    Carry          []Issue         // warnings of an earlier parse of the same item (upload lines)
}
type LintContext struct {
    // KnownHash reports an existing recipe or draft with that canonical hash, ignoring the item's own
    // draft and its own source recipe. Returns "recipe" | "draft:<id>". nil ⇒ no duplicate check.
    KnownHash func(hash, selfDraftID, selfRecipeID string) (where string, ok bool)
}
type LintResult struct { // JSON: index, request_id, hash, errors, warnings, estimated_duration_s, duration_basis, language
    Index int; RequestID, Hash string; Errors, Warnings []Issue
    EstimatedDurationS float64; DurationBasis string // "abc" | "lyrics_style_bpm" | "lyrics_default_bpm"
    Language struct{ Detected, Tagged string }       // ISO 639-1 or "" (unknown)
}
type LintCounts struct{ Errors, Warnings int }        // JSON: errors, warnings (DraftSummary.lint)
type LintReport struct{ OK bool; Items []LintResult; BatchErrors []Issue } // JSON: ok, items, batch_errors
func Lint(items []LintItem, batch bool, c LintContext) LintReport
func EstimateDuration(r Request) (seconds float64, basis string)
func DetectLanguage(lyrics string) string
```
**What lint runs on (binding).** Lint never sees a raw line that still needs an `abc_path` resolver:
- `lint_recipe`: each `items[].request` exactly as sent (ModeApp, nil resolver — an `abc_path` there is
  `abc_path_unresolved`, because drafts must inline their ABC);
- `upload_batch_jsonl`: each line is first parsed with `ParseRequest` and the `abc_files` resolver; a
  line that fails reports that parse error and is not linted further; otherwise lint runs on the line's
  **`Parsed.Canonical`** (ABC inlined) with the line's parse warnings in `Carry`;
- drafts (`get_draft`, `list_drafts`, and the `create_draft` / `update_draft` results): always on the
  stored **`request_json`** (canonical).
Protocol issues = `CheckRequest(item.Request, ModeApp, nil)` plus `Carry`; `batch` ⇒ `duplicate_id`
across items (`BatchErrors`). Then, only for protocol-clean items, **songwriting warnings** (§6 of the
design; severities fixed here):

| Code | Rule |
|---|---|
| `no_sections` | lyrics contain no `[Header]` line |
| `unknown_section_tag` | a header whose kind (C12 synonym table) is `other` |
| `non_sung_text` | a lyric line matching `(?i)\((repeat\|rit\.?\|bis\|x\s*\d+\|\d+\s*x\|instrumental\|chorus)[^)]*\)` or a standalone `x2`, `2x`, `×2`, `bis` token |
| `repeat_marker` | a header carrying a repeat annotation (`[Chorus x2]`, `[Chorus] (repeat)`) |
| `repeat_not_written_out` | a `chorus` section with zero lines after an earlier chorus with lines |
| `empty_section` | any other section with zero lines (intro/outro/instrumental/interlude/solo exempt) |
| `section_line_count` | verse, pre-chorus, chorus or bridge with fewer than 4 or more than 6 lines |
| `duration_over_cap` | `EstimateDuration` > 360 s |
| `bpm_out_of_range` | the BPM `StyleTerms` returns outside 60–180 (rev 3: `StyleTerms` only recognises BPM tokens in 40–250 and drops others silently, so a token outside that range is invisible to this rule) |
| `missing_language_tag` | no language term in the style (italian/italiano, english, spanish/español, french/français, german/deutsch, portuguese/português) |
| `language_mismatch` | tagged language ≠ detected language (both known) |
| `italian_unofficial` | tagged or detected Italian (YuE2 does not officially support it) |
| `duplicate_recipe` | the canonical hash equals another recipe or draft (`KnownHash`, never the item's own draft or source recipe) or an earlier item |

`EstimateDuration`: a timed `abc` (`ParseScore` ⇒ `SecondsPerBar > 0`) ⇒ `Bars × SecondsPerBar`, basis
`abc`. Otherwise BPM = the style BPM (basis `lyrics_style_bpm`) or 100 (`lyrics_default_bpm`); a bar is
`240 / BPM` s (4/4); each lyric line = 2 bars; each section without lines (intro, outro, instrumental,
interlude, solo) = 8 bars; plus 4 bars of tail. Example: 100 BPM, intro + 4 sections × 4 lines ⇒
(32 + 8 + 4) × 2.4 = 105.6 s. `DetectLanguage`: lowercase word tokens scored against fixed stop-word
lists (≈ 30 words each for it, en, es, fr, de, pt, embedded in Go); the top language wins when it has
≥ 5 hits and ≥ 1.5× the runner-up, else "" (synthetic invented-syllable lyrics detect as "").

**Draft lint fields (defined once).** `DraftSummary.lint` is a `LintCounts` and `DraftDetail.lint` a
`LintResult`, both of the **full** lint (protocol + songwriting, incl. `duplicate_recipe`) of the
draft's `request_json`. `library` cannot call the DB-backed `KnownHash`, so it leaves `lint` zero and
the op handlers (`list_drafts`, `get_draft`, `create_draft`, `update_draft`, all in
`ops_drafts_m2a.go`) always fill it through `drafts.Service.Decorate` / `DecorateDetail` (one
`KnownHash` query per page). M2a-B4a freezes the fields and wires the calls; until M2a-B5 the two
decorate methods are `STUB(M2a-B5)` no-ops; the semantics never change mid-milestone.

**Drafts service** (`internal/drafts`; constructors and signatures M2a-B4a, bodies M2a-B5):
```go
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service
func (s *Service) Lint(ctx context.Context, items []yue2.LintItem, batch bool) (yue2.LintReport, error) // KnownHash from DB
func (s *Service) Decorate(ctx context.Context, ds []library.DraftSummary) error  // fills .Lint counts
func (s *Service) DecorateDetail(ctx context.Context, d *library.DraftDetail) error // fills .Lint
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewDraft) (library.DraftDetail, error)
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in DraftPatch) (library.DraftDetail, error)
func (s *Service) Upload(ctx context.Context, p auth.Principal, in Upload) (UploadResult, error)
```
- (rev 3) M1's `library.Service` is documented as owning all library SQL; M2a's new services own the SQL of
  their new tables, and `drafts.Service` owns the `INSERT`/`UPDATE` on `recipe_drafts` for editor-origin
  work (reads and `DraftDetail` stay in `library`). `library.enumOrigin` (Go-validated, today
  `import_backlog`, `varianti_import`) gains `editor`, `agent`, `jsonl_upload` in B4b.
- `NewDraft{Request json.RawMessage; Label *string; ParentTrackID *string; Collection *string}` ⇒
  `yue2.ParseRequest` (no `abc_path` resolver; `AllowTitle` false) — a parse error ⇒ 422
  `LINT_FAILED` with the issues; dropped keys become warnings (`dropped_key:<k>`, the same issues ModeApp
  lint shows). Stored canonical (`request_json`), `request_id`, `group_key = GroupKey(id)`, `origin` =
  `editor` (human/device) or `agent`, `status backlog`, `revision 1`, `created_by`/`updated_by` =
  `p.Key()`. Drafts are therefore always protocol-valid; songwriting warnings are returned, never
  blocking.
- `DraftPatch{Revision int (required); Request json.RawMessage; Label *string /* "" clears */;
  Status *string /* backlog|archived */; ParentTrackID *string /* "" clears */}` — at least one field;
  same parse rules; C27 update SQL; `fulfilled` ⇒ 409 `DRAFT_NOT_EDITABLE`; stale ⇒ 409
  `REVISION_CONFLICT`. Imported drafts (`import_backlog`, `varianti_import`) are editable; their
  `source_recipe_id` (frozen raw bytes) is untouched, and once edited (`revision > 1`) later imports
  never fulfil or relink them (C27).
- `Upload{JSONL string; Collection *string; ParentTrackID *string; AllowPartial bool;
  ABCFiles map[string]string /* path → ABC text */}`. **Limits:** one total cap — the whole request
  body as sent (JSON-escaped JSONL plus ABC files plus the rest) ≤ **6 MiB**, enforced by the op's
  `MaxBodyBytes` (413 `PAYLOAD_TOO_LARGE`, before the handler); within that total, the handler checks
  ≤ 1000 non-blank lines, `jsonl` ≤ 4 MiB of UTF-8, ≤ 50 ABC files, each ≤ 1 MiB (422
  `VALIDATION_FAILED`, `details.field`). Each non-blank line ⇒ `ParseRequest` with a resolver over
  `ABCFiles` (keys must be `filepath.IsLocal`, slash-separated, no `..`; else the line gets
  `abc_path_unresolved`) ⇒ `Lint(batch=true)` over the parsed lines' `Parsed.Canonical` (see "what lint
  runs on"). Protocol errors (incl. `duplicate_id` within the upload) ⇒ without `AllowPartial` 422
  `LINT_FAILED` and nothing written; with it, only clean lines are created. Created drafts: origin
  `jsonl_upload`, label = the stripped `GroupKey` suffix (`v2`) or NULL, collection membership with
  `draft_collections.source = "upload:<first draft id>"`. One write transaction.
- `library.DraftSummary`/`DraftDetail` (M1 C19) gain `revision`, `editable`, `created_by`,
  `updated_by` (rendered with `api.KeyFor`, D-M2a-19), `lint` (above) and `request.abc`/
  `request.cfg_scale` when present.

### C33. Exports, `songs.jsonl`, playlists and MP3 ZIPs (`internal/export`, `internal/exports`, `internal/playlists`, `internal/media/id3`)

> M2b-B1 addendum: the media path regex also accepts `jpg` (spectrogram renditions); `get_system_info.schema_version` is 4 after M2b (C27/C34 tests that pinned 3 now expect 4).

**Python-compatible JSON writer** (`internal/export/pyjson.go`; M2a-B3, used by export and tests):
```go
type Style struct {
    KeyOrder    []string // nil ⇒ sorted keys; else this order, unknown keys appended in sorted order
    ItemSep     string   // ", " (Python default) | ","
    KeySep      string   // ": " (Python default) | ":"
    EnsureASCII bool
}
func EncodeRequest(r yue2.Request, s Style) []byte // one line, no trailing newline
```
Matches CPython `json.dumps(obj, ensure_ascii=…, separators=…)` byte for byte: strings escape `"` `\\`
`\n` `\r` `\t` `\b` `\f`, other C0 controls as `\u00xx` (lowercase hex); `ensure_ascii` escapes every code
point outside U+0020…U+007E as `\uxxxx` (DEL included; UTF-16 surrogate pairs above U+FFFF); without it,
non-ASCII (incl. U+2028/U+2029) is raw UTF-8 and `<`, `>`, `&`, `/` are never escaped; integers in
decimal; `cfg_scale` as Python `repr(float)` (shortest round-trip digits, `.0` for integral values,
exponent form `1e-05` below 1e-4). Golden file `internal/export/testdata/pyjson.golden.jsonl` was produced
once with CPython (command recorded in the test file header) over synthetic inputs covering accents,
`<>&`, controls, an emoji, both `ensure_ascii` values and both separator styles.

**Style detection** (`DetectStyle(raws [][]byte) (Style, StyleStats)`): over every `recipe_sources` row
of `raw_kind = 'jsonl_line'`, a token scanner reads the top-level key sequence, the item and key
separators and an `ensure_ascii` vote (a `\u` escape of a code ≥ 0x80 ⇒ true; a raw byte ≥ 0x80 ⇒ false;
otherwise abstain). Each attribute takes the majority; key order is `sorted` when every sequence is
sorted, else the most frequent sequence; ties and no data ⇒ sorted keys, `", "`, `": "`,
`ensure_ascii=false`.

**JSONL export** (`internal/export`, pure over the `Source` interface below). **M2a-B4a declares
`Source`, its row types, `Selection`, `Row` and `Result` in `internal/export/source.go` and a stub
`Build` in Stage 1; M2a-B6 implements `Build` and the only `Source` implementation,
`exports.DBSource` (SQL on the read pool, owned by B6 — the library does not implement it).**
```go
type TrackFilter struct{ Status []string; MinStars *int; Tags []string; PlaylistID string; TrackIDs []string }
type DraftFilter struct{ Status []string /* default backlog */; Collection, Origin, GroupKey string; DraftIDs []string }
type Selection struct {
    Kind       string      // "source" | "tracks" | "drafts"
    SourcePath string      // Kind=source: a recipe_sources.source_path of raw_kind jsonl_line
    Tracks     TrackFilter // Kind=tracks (default: all non-discarded tracks)
    Drafts     DraftFilter // Kind=drafts
    ForBatch   bool        // Kind=source only; tracks and drafts always apply the unique-id rule (D-M2a-20)
}

type Source interface {
    // Every recipe_sources row of raw_kind jsonl_line with that source_path, all import runs, ordered by
    // (line_no, import_run_id). ErrUnknownSource when the path has none.
    JSONLSourceRows(ctx context.Context, path string) ([]SourceRow, error)
    // One row per distinct recipe of the tracks matching f (track set resolved exactly like search_tracks
    // via library.(*Service).MatchTrackIDs; PlaylistID through playlists; TrackIDs as given; filters AND).
    SelectRecipes(ctx context.Context, f TrackFilter) ([]RecipeRow, error)
    SelectDrafts(ctx context.Context, f DraftFilter) ([]DraftRow, error)
    // Raw bytes of every jsonl_line source (DetectStyle input).
    StyleSamples(ctx context.Context) ([][]byte, error)
}
type SourceRow struct {
    LineNo      int
    Raw         []byte
    ImportRunID string // UUIDv7: time-ordered
    RecipeID    string
    ABC         string // the recipe's inlined abc when Raw carries abc_path, else ""
}
type RawSource struct{ Path string; LineNo int; Raw []byte; ImportRunID string } // jsonl_line sources only
type RecipeRow struct {
    RecipeID, Hash, RequestID string
    Request         yue2.Request // canonical request (abc inlined)
    FirstRecordedAt string       // earliest take's recorded_at
    JSONLSources    []RawSource  // ordered by (path, line_no)
}
type DraftRow struct{ DraftID, GroupKey, RequestID string; Request yue2.Request }

type Row struct{ Bytes []byte; RecipeHash, RequestID string; Raw bool; RenamedFrom string; ABCPath string }
type Result struct {
    JSONL      []byte            // rows joined with "\n", trailing "\n"
    ABCFiles   map[string][]byte // abc_path targets referenced by raw rows (relative to the JSONL)
    Rows       []Row
    Style      Style
    Validation yue2.BatchReport  // ValidateBatch(JSONL, ABCFiles as fs.FS)
    Warnings   []Issue           // raw_not_batch_valid, abc_missing, source_line_superseded, …
}
func Build(ctx context.Context, src Source, sel Selection) (Result, error) // EXPORT_EMPTY when no rows
```
`library.(*Service).MatchTrackIDs(ctx, p library.SearchParams) ([]string, error)` is added by M2a-B4b
(Stage 1) (rev 3: M1 has no `TrackQuery`; `SearchParams` gains `MinStars`, `Unrated`, `Tags`,
`HasOpenComments` and its `WHERE` builder is split out of `SearchTracks`): the same builder as
`search_tracks` (incl. the D-M2a-18 default), unpaged (`Limit`, `Cursor`, `Sort` ignored), ordered by id.
- **Kind `source`** (the byte-identical path): the rows of that path, taking **per `line_no` the row from
  the newest `import_run_id`** (a line changed between import runs leaves an older row with the same
  `line_no`; the older ones are skipped and listed in one warning `source_line_superseded` with
  `details.lines`), in `line_no` order, each emitted as its raw bytes, joined by `\n` with a trailing
  `\n`. The result equals the newest imported version of the file whenever that file had no invalid,
  blank or unterminated lines and no BOM; otherwise the differences are exactly the lines M1 did not
  record (the import report lists them). Known limit: lines removed from the end of a file, and a line
  reverted to an earlier version (its row keeps its first run id), cannot be told from `recipe_sources`
  alone; byte identity is claimed only for files that were not rewritten that way after import.
- **Kind `tracks`**: one row per distinct recipe of the selected tracks (default: all non-discarded
  tracks). Kind `drafts`: one row per selected draft (`request_json`, never raw). Order: recipes with a
  `jsonl_line` source by their canonical source `(source_path, line_no)`, then others by the earliest
  take's `recorded_at` (drafts: `group_key, request_id`), ties by hash.
- **Raw vs regenerated** (D-M2a-6): a recipe's canonical raw = its earliest `jsonl_line` source
  (ties: path, line). It is emitted verbatim iff `CheckRequest(ModeYuE2)` finds no issue on it (an
  `abc_path` must resolve to the recipe's inlined `abc`, which is then written to `ABCFiles[abc_path]`).
  Otherwise (`json_file` / `json_array_item` sources, drafts, raw with issues) the row is
  `EncodeRequest(canonical request, detected style)` with warning `raw_not_batch_valid` when a raw
  existed.
- **Unique-id rule** (always for kinds `tracks` and `drafts`; for kind `source` only with `ForBatch`):
  request ids must be unique (`yue2 batch` rejects duplicates and output directories would collide).
  Every row whose id occurs more than once in the selection is regenerated with id `<id>-s<seed>` (base
  truncated so the id stays ≤ 180 chars and matches the id regex); remaining collisions get `-2`, `-3`,
  … in row order; renamed rows record `RenamedFrom`. Rows with unique ids keep their raw bytes.
- **Validation expectations (D-M2a-20):** every `tracks` export, every `drafts` export and every
  `ForBatch` export must have `Validation.OK` — a non-OK report is returned (never hidden) and fails
  `TestAcceptance_M2a_ExportPassesBatchValidator`. A `source` export without `ForBatch` must equal the
  original bytes, and its `Validation` must report **exactly the issues `ValidateBatch` reports on the
  original file**, excluding issues on lines M1 did not record (compared as a multiset of
  `(code, id)`; line numbers refer to the exported file). With the M1 `acceptance` profile's
  `music/songs.jsonl` that is exactly one `duplicate_id` (`syn-song-01`, seeds 1001 and 2001); the
  invalid `{not json` line is not part of the export. The `export` CLI then exits 1 but still writes
  the bytes.

**Exports service** (`internal/exports`; files + rows + links; constructor and signatures M2a-B4a,
bodies M2a-B6):
```go
type RatingsReader interface { // *judgments.Service implements it (HumanStars, C31)
    HumanStars(ctx context.Context, trackIDs []string) (map[string]int, error)
}
type Options struct {
    Store     *store.Store
    Data      *os.Root
    Lib       *library.Service   // current titles, durations, renditions (mp3_320 paths)
    Playlists *playlists.Service // playlist name, order, updated_at
    Ratings   RatingsReader      // human stars ⇒ POPM
    Source    export.Source      // DBSource{R: st.R, Lib: lib, Playlists: playlists} unless a test injects one
    Signer    *serve.Signer
    Now       func() time.Time
    NewID     func() string
}
func New(o Options) *Service
func NewDBSource(st *store.Store, lib *library.Service, pl *playlists.Service) export.Source
func (s *Service) JSONL(ctx context.Context, p auth.Principal, sel export.Selection, filename string) (Export, error)
func (s *Service) PlaylistZIP(ctx context.Context, p auth.Principal, playlistID string) (Export, error)
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Export, error) // fresh link; EXPORT_EXPIRED if swept
func (s *Service) Sources(ctx context.Context) ([]ExportSource, error)                 // jsonl source paths with line/recipe counts
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error)  // ext jsonl|zip; swept/missing file ⇒ serve.ErrNotFound
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error)     // files only; rows stay
```
- Kind `songs_jsonl` (no ABC files needed: the `.jsonl` file itself) or `songs_bundle` (ABC files
  needed: a ZIP with `songs.jsonl` and the ABC files at their relative paths). Files are built in
  `incoming/export-<id>/`, hashed, chmod 0444 and claimed with `Root.Link` into
  `derived/exports/<aa>/<sha>.<ext>`; `EEXIST` ⇒ identical content already exists ⇒ reuse (same rule as
  M1 C15 step 14).
- `Export{id, kind, filename, sha256, bytes, created_at, download:{url, expires_at},
  jsonl?: {rows, raw_rows, generated_rows, renamed:[{from,to}], style, validation:{ok, fatal, issues[]},
  abc_files:[{path, sha256}], warnings[], content: string|null}, zip?: {entries, tracks:[{track_id,
  entry}], missing_mp3:[track_id]}}`. `content` is the JSONL text when it is ≤ 60 000 bytes (so agents
  rarely need the link), else `null`. Links come from M1's `Signer` with `TTLFor(principal)`. (rev 3) `serve.TTLFor` today gives the 12 h /
  1 h-bucket lifetime only to `KindHuman`; M2a-B4a changes it to `p.ActsAsHuman()` (devices count as
  human; `internal/media/serve/sign.go` joins B4a's files).
- `filename` default: `songs.jsonl` / `songs-bundle.zip` / `<playlist slug>.zip` (sanitised to
  `[a-z0-9._-]`, ≤ 120).
- **Media handler** (amends M1 C18): `ext ∈ {flac, mp3, jsonl, zip}`; `jsonl` ⇒ `application/x-ndjson`,
  `zip` ⇒ `application/zip`. M1's `serve.Object{RelPath, ContentType, ETag, ModTime}` has no file name,
  so **M2a-B4a (Stage 1) adds `DownloadName string` to `serve.Object`** in
  `internal/media/serve/types.go` (empty for library objects); the handler (M2a-B6) sends
  `Content-Disposition: attachment; filename="<DownloadName>"` (ASCII, `[a-z0-9._-]`, so no RFC 5987
  form is needed) whenever it is non-empty. `exports.Resolve` fills it from the newest `exports` row
  with that sha. Resolution order: library (flac, mp3, probe) then exports. **A swept export file:**
  `Resolve` checks the file with `Root.Stat` and returns `serve.ErrNotFound` (⇒ 404 `NOT_FOUND`) when it
  is gone, because M1 C18 answers 500 `INTERNAL` for a resolved object whose file is missing. All M1 C18
  checks (signature, expiry, Range, no compression, no query logging) apply unchanged.
- `Sweep` removes export files whose newest row is older than 7 days. `serve` runs it once at start and
  then from **its own daily ticker** (independent of the backup schedule, so `MM_BACKUP_AT=off` does not
  stop it); `mcp-stdio` and the CLI never sweep. `Get` on a swept export ⇒ 410 `EXPORT_EXPIRED`; its
  old media link ⇒ 404.

**Playlists** (`internal/playlists`):
```go
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewPlaylist) (Playlist, error)
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in PlaylistPatch) (Playlist, error)
func (s *Service) Get(ctx context.Context, id string) (Playlist, error)
func (s *Service) List(ctx context.Context, includeHidden bool) ([]PlaylistSummary, error)
```
`NewPlaylist{Name; Description; TrackIDs []string}`; `PlaylistPatch{Revision int (required); Name,
Description *string; TrackIDs []string /* full ordered replacement */; AddTrackIDs []string /* appended,
duplicates allowed */; Hidden *bool}` — `TrackIDs` and `AddTrackIDs` are mutually exclusive (422). ≤ 1000
items; every id must exist (422 `UNKNOWN_TRACK`); stale ⇒ 409 `REVISION_CONFLICT`. Discarded tracks may
stay in a playlist (shown with their status).

**MP3 ZIP** (`exports.PlaylistZIP`): **capped** at 200 tracks with an `mp3_320` rendition and 2 GiB of
rendition bytes (summed from the `renditions` rows before anything is written); above either ⇒ 413
`PAYLOAD_TOO_LARGE` with `details: {tracks, limit_tracks, bytes, limit_bytes}` and no work dir created
(the export work dir would otherwise double disk use in `incoming/`). `archive/zip` with `Store`
method, entries in playlist order: `NN - <title>.mp3` (`NN` zero-padded to the item count's width; title
folded with `yue2.Slug` rules but keeping spaces, ≤ 80 bytes, duplicates suffixed ` (2)`), then
`<playlist name>.m3u8` (`#EXTM3U`, `#EXTINF:<int seconds>,<title>`, relative entry names). **Entry
names** never contain `/` or `\`, never start with `.`, and contain no C0/C1 controls (each such
character ⇒ `_`; an empty result ⇒ `track`/`playlist`), so every entry lands flat in the extraction
directory. Every entry's `Modified` = the playlist's
`updated_at`, so an unchanged playlist always yields the same bytes and sha. Each MP3 entry =
`id3.Encode(tag) ‖ rendition bytes` (no re-encode). Tracks without an `mp3_320` rendition are skipped
and listed in `missing_mp3`.

`internal/media/id3`:
```go
type Tag struct{ Title, Artist, Album, Track string; Stars int /* 0 = none */; TXXX [][2]string }
func Encode(t Tag) []byte // ID3v2.4, UTF-8 text frames (encoding 0x03), synchsafe sizes, no padding, no unsynchronisation
```
Frames in order: `TIT2` (current title), `TPE1` "metamusic", `TALB` (playlist name), `TRCK` "n/N",
`POPM` (email "metamusic", rating 1→1, 2→64, 3→128, 4→196, 5→255; only when the human rated),
`TXXX` `MM_TAKE_ID`, `MM_REQUEST_ID`, `MM_SEED`, `MM_STATUS`. Values sanitised like C15 step 7 (controls →
space). Tested by a small reader in the test file.

### C34. Backup and restore (`internal/backup`)

```go
type Options struct {
    Store *store.Store; DataDir, BackupDir string; Exports export.Source
    Now func() time.Time; NewID func() string; Version string
    Link func(oldname, newname string) error // default os.Link; tests inject EXDEV
    Notify notify.Notifier
}
func New(o Options) *Service
func (s *Service) Start(ctx context.Context, cause string, p auth.Principal) (Run, error) // async; BACKUP_IN_PROGRESS
func (s *Service) RunSync(ctx context.Context, cause string, p auth.Principal) (Run, error) // CLI
func (s *Service) List(ctx context.Context) (Listing, error)        // runs + snapshots on disk + schedule
func (s *Service) Close(ctx context.Context) error                  // refuses new runs (503 UNAVAILABLE), waits for in-flight ones until ctx ends
func Restore(ctx context.Context, from, into string, o RestoreOptions) (RestoreReport, error)
type RestoreOptions struct{ Link func(oldname, newname string) error; Now func() time.Time; NewID func() string; Version string }
type RestoreReport struct{ Files, Linked, Copied int; SchemaFrom, SchemaTo int; Counts map[string]int }
func SnapshotName(t time.Time, exists func(name string) bool) string // "<yyyymmddThhmmssZ>", then "-2", "-3", … while taken (dir or .partial)
func Retain(names []string, now time.Time, loc *time.Location) (keep, drop []string) // pure; names that do not parse are in neither list
func NextRun(now time.Time, at string, loc *time.Location) time.Time // pure; first instant > now whose local wall clock is `at`
type Scheduler struct{ At string; Loc *time.Location; Run func(context.Context) }
func (s *Scheduler) Loop(ctx context.Context) // uses time.Now and time.NewTimer only: for { sleep until NextRun(time.Now(), …); Run }
```
`NextRun` is table-tested (`TestNextRun_Table`, `Europe/Berlin`): from 02:00 ⇒ 03:30 the same day; from
03:30 exactly ⇒ the next day; on the March change day (02:00 → 03:00) and the October change day
(03:00 → 02:00) `03:30` is 03:30 local; `at = 02:30` on the March day ⇒ 03:30 CEST (Go's `time.Date`
normalisation, documented), on the October day ⇒ the first 02:30 (CEST). `Loop` gets one minimal
synctest (`TestScheduler_LoopSynctest`: in a bubble, `At` one minute after the bubble's start ⇒ `Run`
called once after 1 min and again 24 h later; cancel ⇒ returns).

**Concurrency.** One backup at a time **across processes** (`serve`, `mcp-stdio`, CLI `backup` share one
DB): the run insert fails on the partial unique index `backup_runs_one_running` (C27) ⇒
`BACKUP_IN_PROGRESS`. Before inserting, a `running` row whose `heartbeat_at` is older than 2 min is
marked `failed` "interrupted" (same tx). An in-process mutex only avoids needless DB round trips.
`create_backup` is asynchronous on every face (202 with the `running` run, parity-equal); the process
that started a run owns it until it ends: `Close` (called by `serve` on shutdown with the M0 shutdown
deadline, by `mcp-stdio` on exit with a 10-minute deadline, and by the `servertest` cleanup before
`t.TempDir` removal) waits for it. A run abandoned by a killed process is marked interrupted by the next
start (heartbeat rule).

**Snapshot procedure:**
1. Insert `backup_runs` (`running`); name = `SnapshotName(now, …)` — a second snapshot in the same
   second (manual + scheduled, or a fixed test clock) gets `-2`, `-3`, …; create
   `<BackupDir>/.<name>.partial/` (0700). Heartbeat every 30 s.
2. `VACUUM INTO '<partial>/db/metamusic.db'` (bound parameter) on the writer pool; open the copy
   read-only and require `PRAGMA integrity_check` = `ok`; sha256 it.
3. From **the copy**, list `tracks.master_path` (+ `master_sha256`), `artifacts.path` (+ `sha256`) and
   `renditions.path` (+ `sha256`) (D-M2a-5). Each path must be `filepath.IsLocal` and start with
   `audio/masters/`, `takes/` or `derived/` (else the run fails). For each: `Link(DataDir/p, partial/p)`;
   on any link error (EXDEV, EPERM, EMLINK, …) copy instead (`io.Copy` into an `O_EXCL` file — on Linux
   `copy_file_range` reflinks where the filesystem supports it), fsync, chmod 0444 and verify sha256 equals
   the DB value. Linked files are verified with `os.SameFile`, not re-hashed (they are frozen 0444).
4. **Copy** (never link — they are mutable) `import/mapping.csv`, `import/mapping.todo.csv` and
   `import/reports/*.json` when present (D-M2a-5), sha256 recorded, kind `import`, mode 0600.
5. Export `songs.jsonl` (Selection `tracks`, all four statuses, so discarded tracks too; unique ids,
   D-M2a-20) into `<partial>/export/` (+ ABC files).
6. Write `manifest.json` (below) via temp file + rename, fsync the directory, then rename
   `.<name>.partial` → `<name>` (atomic completion). Finish the run (`completed`, stats) and apply
   retention; any failure ⇒ run `failed` with a redacted error, the partial dir removed, and a
   high-priority notification "backup failed".

```json
{"format":"mm.backup/1","id":"<run id>","created_at":"…","app_version":"…","schema_version":3, /* M2b-B1: the live schema is now 4; backups record the live version */
 "db":{"path":"db/metamusic.db","sha256":"…","bytes":0,"integrity":"ok"},
 "files":[{"path":"audio/masters/…","sha256":"…","bytes":0,"kind":"master|artifact|rendition|import|export","linked":true}],
 "counts":{"tracks":0,"masters":0,"artifacts":0,"renditions":0,"drafts":0,"comments":0},
 "link":{"linked":0,"copied":0,"fallback":"EXDEV"|null}}
```
`files` sorted by path; never absolute paths; no secrets dir, no `incoming/`, `inbox/`,
`derived/exports/`, and from `import/` only the files of step 4.

**What a snapshot protects against (stated, not implied).** Hardlinked files share inodes with the live
tree: a snapshot protects them against **deletion** (and against DB-side changes, via its own DB copy),
not against **in-place modification** of a live file — which the 0444 frozen-master rule (CLAUDE.md
rule 7) and the immutable renditions make a bug, not an operation. Restore re-hashes every file against
the manifest, so a corrupted shared inode is detected at restore time; periodic verification of
snapshots is left to a later decision (recorded with D7 in the private design).

**Retention** (`Retain`, pure, over snapshot directory names): keep the newest complete snapshot of each
of the last 14 calendar days and of each of the last 8 ISO weeks (in `loc`), and always the newest
snapshot; drop the rest (`RemoveAll` of the snapshot directory — hardlinked masters survive in the live
tree). Dot-prefixed `.partial` directories older than 24 h are removed. Only directories whose names
parse as snapshot names (`<yyyymmddThhmmssZ>` with an optional `-N`) are ever touched.

**Restore** (`metamusic restore --from <snapshot dir> --into <dir>`, CLI only — never an API op):
1. `<dir>` must not exist or be empty (else exit 2, nothing written).
2. Read `manifest.json` (format check), verify the DB sha256 and **every** file's sha256 (exit 1
   listing mismatching or missing paths, nothing written).
3. Create `<dir>` (0750); copy the DB to `<dir>/metamusic.db`; link-or-copy every file into place
   (verify copies), chmod 0444 (kind `import`: copied, 0600 under a 0700 `import/`).
4. Open with `store.Open` (migrates an older schema; a newer one ⇒ exit 2 `ErrSchemaMismatch`-style
   message), `PRAGMA integrity_check` = ok; mark the snapshot's own `backup_runs` row `completed` (it was
   `running` when the copy was taken); write an audit row (`system:cli`, op `cli:restore`).
5. Print the counts and the next step (`MM_DATA_DIR=<dir>`; secrets are not part of backups — the same
   `secret_key` keeps tokens, sessions and devices valid, a new one invalidates them).

**C34 addendum (M3-B1):** restore will disarm every `provider_state` row, turn live attempts into `unknown` and require a
billing reconciliation before re-arming (M3 plan, C76.7, D-M3-31). The table exists since migration 0006 (M3-B1); the
behaviour lands with M3-B6.

### C35. The `/context` bundle (`internal/trackctx`)

> M2b-B3 amendment (C51.8): `perception` is built by `ears.Perception` (same keys and order; `tier`, `flags` and
> `blind_spots` now come from Ears, so `blind_spots` grows by the Tier A, score-adherence and section-timing
> entries); `judgments.ladder` is `{u, sd, n_duels}` and `lineage.children[].ladder` is `{u, sd}` from the
> human-only global fit (`null` without counted duels); `_links` gains `spectrogram` after `stream`
> (`null` without a rendition). `trackctx.Builder.UseM2b` wires both sources; `trackctx` no longer computes
> perception itself.

```go
func New(lib *library.Service, j *judgments.Service, l *listening.Service, signer *serve.Signer, publicURL string, now func() time.Time) *Builder
func (b *Builder) Build(ctx context.Context, p auth.Principal, trackID string, include []string) (Bundle, error)
func Fit(b *Bundle, limit int, include []string) (chars int, truncated []string) // pure; exported for tests; sets _budget to its fixed point
func Measure(b *Bundle) int // UTF-16 units of the bundle as Huma's JSON encoder writes it (HTML escaping on), trailing newline excluded
const DefaultLimit, IncludeLimit = 24000, 96000
```
Shape (`format: "mm.context/1"`; keys in this order; unknown values `null`, never invented):
```json
{"format":"mm.context/1",
 "track":{"id":"…","title":"…","status":"to_relisten","stars":4,"critic_stars":3,"critic_verdict":"…",
          "tags":["ballad"],"duration_s":231.4,"recorded_at":"…","url":"https://mm.example.com/tracks/…"},
 "recipe":{"engine":"yue2","hash":"…","request":{"id":"…","style":"…","lyrics":"…","cot":"full","seed":1101,
           "abc_present":true,"cfg_scale":null}},
 "provenance":{"origin":"import","import_rule":"slug","truncated":{"abc":false,"semantic":false},"cost_usd":null,"env":null},
 "score":{"source":"import","bars":64,"meter":"4/4","q":"1/4=92","seconds_per_bar":2.6,"aligned":true},
 "lineage":{"path":[{"id":"…","title":"…"}],"operator":"inferred_sibling","inferred":true,"rule":"same_request_id",
            "rationale":null,"children":[{"id":"…","title":"…","stars":3,"ladder":null}],"siblings":2},
 "judgments":{"comments":[{"id":"…","at_second":72.0,"section":"chorus 1","section_confidence":0.7,"kind":"fix",
              "state":"open","by":"human","body":"…"}],"comments_total":5,"open_fix":1,"ladder":null},
 "perception":{"tier":"0","lufs_i":-11.2,"true_peak_db":-0.4,"lra_lu":6.1,"bandwidth_hz":15800,
               "sections":[{"label":"verse 1","start":0,"end":31.2,"source":"plan","confidence":0.7}],
               "timed":true,"flags":[],"blind_spots":["timbre","emotional delivery","mix balance beyond loudness",
               "hook catchiness","diction and intelligibility"]},
 "listening":{"plays":5,"completion_rate":0.8,"last_played_at":"…"},
 "_links":{"stream":{"mp3":"…signed…","flac":"…signed…","expires_at":"…"},
           "comments":"/api/tracks/…/comments","track":"/api/tracks/…"},
 "next_actions":["…"],
 "_budget":{"chars":0,"limit":24000,"truncated":[]}}
```
- `include` ∈ {`score`, `lyrics`, `comments`} (explode; unknown ⇒ 422). Any include raises the limit to
  96 000 and protects that part from trimming: `score` adds `score.abc` (the ABC text; when it alone
  exceeds the limit it is cut and `score.abc_truncated: true`), `lyrics` forbids lyric trimming,
  `comments` lists up to 500 non-hidden comments.
- Default comments: non-hidden, open `fix` first, then newest, max 20.
- **Budget (exact):** `chars` = UTF-16 code units of the bundle **as the REST body carries it**:
  `trackctx.Measure(b)` encodes with the same encoder Huma uses for responses (M0 `api.New`'s JSON
  format: `encoding/json` with HTML escaping on, so `<`, `>`, `&` count as the six characters of
  `\u003c` …; the encoder's trailing newline is not counted), **with the final `_budget` object
  included**. Because `_budget.chars` counts its own digits, `Fit` computes it to a fixed point: set
  `chars`, re-measure, repeat until stable (at most 3 rounds; the digit count changes at most once).
  `Fit` trims in this order until it fits, recording each step in `_budget.truncated`: `comments`
  (20 → 5 → 0, keeping `comments_total`), `children` (→ 10 → 0), `lyrics` (→ `null`; structure remains
  in `perception.sections`), `style` (→ 500 chars). The final size must be ≤ the limit
  (`TestFit_WorstCase`). `TestMeasure_MatchesRESTBody` (B8) pins `Measure` to the real REST body.
  Over MCP the measured text is the tool result's **`TextContent`** (the REST body bytes the adapter
  passes through), never `StructuredContent`, which the SDK re-marshals with sorted keys and therefore
  differs in length and order.
- **`next_actions`** (deterministic, max 5, in this order): no human stars ⇒ "ask the human to rate this
  take (rate_track)"; open fix comments ⇒ "address N open fix comments (call_operation list_comments
state=open kind=fix)" (`list_comments` is a generic op, so the hint names the generic tool);
  untimed ⇒ "section times need a score: refer to sections by lyrics, not by time"; truncated ⇒ "the
  take is truncated (abc/semantic): prefer a shorter arrangement"; always last ⇒ "record your critique as
  a comment (add_comment) — you cannot hear: see perception.blind_spots".
- Links: signed with `TTLFor(principal)` (15 min for agents), absolute with `PublicURL`.

**C35 addendum (M3-B2):** the `provenance` block of the bundle will carry `cost` (`{micro_usd, usd, basis}|null`, replacing `cost_usd`),
`env` and `job_id`, and `next_actions` gains a lowest-priority generation hint under a cap of five entries; the shapes are frozen
in M3-B2 and the bundle code and goldens change in M3-B6. See the M3 plan, C81.1.


### C36. Listen events (`internal/listening`)

```go
type Event struct{ ID, TrackID, SessionID, Kind string; FromMS, ToMS *int64; Format, DeviceID *string; ClientAt time.Time }
func New(st *store.Store, now func() time.Time) *Service
func (s *Service) Log(ctx context.Context, p auth.Principal, src string, evs []Event) (accepted, duplicates int, rejected []Rejected, err error)
func (s *Service) Stats(ctx context.Context, trackID string) (Listening, error)
```
Validation per event (rejected events are listed with a code; the rest are accepted): `id` a UUID
(`unknown`/`bad_id`); `kind` ∈ start|segment|seek|complete|skip; `track_id` exists (`unknown_track`);
`from_ms`/`to_ms` within `[0, duration_ms + 2000]` and `to ≥ from` (`bad_range`); `segment` and `seek`
need both; `client_at` within [now − 30 days, now + 5 min] (`bad_client_at`); ≤ 200 events per call
(schema `maxItems`, 422 `VALIDATION_FAILED`) and ≤ 256 KiB body (op `MaxBodyBytes`, 413
`PAYLOAD_TOO_LARGE`). `Stats` counts only `human`/`device` events: `plays` = `start` events,
`completion_rate` = `complete` / `start` (capped at 1, `null` with no plays), `last_played_at` = latest
`start.client_at`.

### C37. Notifications (`internal/notify`)

```go
type Message struct{ Title, Body string; Priority int /* 1..5 */; Tags []string; Click string }
type Notifier interface{ Notify(ctx context.Context, m Message) error }
func NewNtfy(url, token string, client *http.Client) Notifier
// Publishes as JSON: POST <server base> with {"topic","title","message","priority","tags","click"}, where
// MM_NTFY_URL = <server base>/<topic> (topic = last path segment). Only Content-Type and
// "Authorization: Bearer <token>" (when set) travel as headers — no user-chosen text ever does, so a
// device or token name cannot make Go's HTTP client reject the request. Title and message are also
// stripped of C0/C1 controls (→ space) and capped at 200 / 1000 runes.
type Nop struct{}
func Async(n Notifier, timeout time.Duration) Notifier // fire-and-forget with timeout; errors logged via redact.Error
```
Events (M2a): backup failed (priority 4, body = redacted error code only), device paired (3, "Device
paired: <name>"), token created (3, "Token created: <name> (<scopes>)"). Bodies never contain tokens,
codes, cookies or URLs with query strings (`TestNotify_NoSecretsInMessages`); a name containing
`\r\n`, NUL or U+0085 is delivered sanitised (`TestNotify_ControlCharsInNames`). `MM_NTFY_URL` empty ⇒
`Nop`.

**C37 addendum (M3-B1):** `notify.Message` gains `Actions []notify.Action` (`{Action: "view", Label, URL}`, at most
`notify.MaxActions` = 3), published as ntfy's JSON `actions` array; entries with a non-`view` action, an empty label or URL,
or control characters in the URL are dropped. See the M3 plan, C82.

### C38. Operations (all `Cost: "free"`; registered via `api.Register`; JSON snake_case; types in `internal/api/types_m2a.go`)

Legend: **C** = `idempotency: create` (REST requires `Idempotency-Key`; MCP derives it), **H** =
`rate: heavy`, **S[…]** = `secret_fields`. Human-only ops use `mcp: exclude:human-only`.

| OperationID | Method + path | Scope | MCP | UI | Flags | Output |
|---|---|---|---|---|---|---|
| `rate_track` | PUT `/api/tracks/{id}/rating` | write | tool | `/tracks/:id` | | `TrackRating` |
| `update_track` | PATCH `/api/tracks/{id}` | write | generic | `/tracks/:id` | | `TrackDetail` |
| `set_tags` | PATCH `/api/tracks/{id}/tags` | write | tool | `/tracks/:id` | | `TrackTags` |
| `list_tags` | GET `/api/tags` | read | generic | `/` | | `{items: TagCount[]}` |
| `add_comment` | POST `/api/tracks/{id}/comments` | write | tool | `/tracks/:id` | C | 201 `Comment` |
| `update_comment` | PATCH `/api/comments/{id}` | write | tool | `/tracks/:id` | | `Comment` |
| `list_comments` | GET `/api/tracks/{id}/comments` | read | generic | `/tracks/:id` | | `{items: Comment[], total, next_cursor}` |
| `get_track_context` | GET `/api/tracks/{id}/context` | read | tool | `/tracks/:id` | | `ContextBundle` (C35) |
| `log_listen_events` | POST `/api/listen-events` | write | generic | `/tracks/:id` | | `{accepted, duplicates, rejected: [{id, code}]}` |
| `lint_recipe` | POST `/api/recipes/lint` | read | tool | `/backlog/:id` | | `LintReport` |
| `create_draft` | POST `/api/drafts` | write | tool | `/backlog/new` | C | 201 `DraftDetail` |
| `update_draft` | PATCH `/api/drafts/{id}` | write | generic | `/backlog/:id` | | `DraftDetail` |
| `upload_batch_jsonl` | POST `/api/drafts/batch` | write | generic | `/backlog/upload` | C, H | 201 `UploadResult` |
| `list_playlists` | GET `/api/playlists` | read | tool | `/playlists` | | `{items: PlaylistSummary[]}` |
| `get_playlist` | GET `/api/playlists/{id}` | read | generic | `/playlists/:id` | | `Playlist` |
| `create_playlist` | POST `/api/playlists` | write | tool | `/playlists` | C | 201 `Playlist` |
| `update_playlist` | PATCH `/api/playlists/{id}` | write | generic | `/playlists/:id` | | `Playlist` |
| `export_playlist_zip` | POST `/api/playlists/{id}/exports` | write | generic | `/playlists/:id` | C, H | 201 `Export` |
| `list_export_sources` | GET `/api/exports/sources` | read | generic | `/export` | | `{items: ExportSource[]}` |
| `export_jsonl` | POST `/api/exports/jsonl` | write | tool | `/export` | C, H | 201 `Export` |
| `get_export` | GET `/api/exports/{id}` | read | generic | `/export` | | `Export` |
| `create_backup` | POST `/api/backups` | write | generic | `/settings/backups` | C, H | 202 `BackupRun` |
| `list_backups` | GET `/api/backups` | read | generic | `/settings/backups` | | `BackupListing` |
| `get_audit` | GET `/api/audit` | read | generic | `/settings/audit` | | `{items: AuditEntry[], next_cursor}` |
| `list_tokens` | GET `/api/tokens` | read | generic | `/human/tokens` | | `{items: TokenInfo[]}` |
| `revoke_own_token` | POST `/api/tokens/self/revoke` | read | generic | `none:agent-self-service` | | `TokenInfo` |
| `create_token` | POST `/human/api/tokens` | admin | exclude:human-only | `/human/tokens` | C, S[`token`] | 201 `CreatedToken` |
| `revoke_token` | POST `/human/api/tokens/{id}/revoke` | admin | exclude:human-only | `/human/tokens` | | `TokenInfo` |
| `list_devices` | GET `/human/api/devices` | read | exclude:human-only | `/human/devices` | | `{items: Device[]}` |
| `create_pairing_code` | POST `/human/api/devices/pairing-codes` | admin | exclude:human-only | `/human/devices` | C, S[`code`,`pair_url`] | 201 `PairingCodeOut` |
| `revoke_device` | POST `/human/api/devices/{id}/revoke` | admin | exclude:human-only | `/human/devices` | | `Device` |
| `revoke_own_device` | POST `/api/devices/self/revoke` | write | exclude:device-self-service | `/settings` | | `DeviceSelf` (+ `Set-Cookie` clearing `mm_dev`) |

10 new first-class tools, 16 generic, 6 excluded (5 human-only + `revoke_own_device`). (rev 3, M2a-B4a: the
C41 ceiling forced five demotions to generic: `get_audit`, `update_playlist`, `upload_batch_jsonl`,
`update_draft` (the planned order) and `update_track`; `tools/list` also advertises slim output schemas, see C41.) `revoke_own_token`
uses scope `read` so a read-only token can always revoke itself; human callers get 403 `NOT_A_TOKEN`.
`revoke_own_device` is `exclude:device-self-service` (D-M2a-22): no agent can be a device, so it has no
parity case; its REST tests cover a device (200, cookie cleared), a human (403 `NOT_A_DEVICE`) and an
agent bearer (403 `NOT_A_DEVICE`). `list_devices` is human-only (D-M2a-19: user agents and names are
personal data). Revocations are naturally idempotent (repeat ⇒ same 200).

**Body limits** (`huma.Operation.MaxBodyBytes`, set explicitly by every M2a op with a body; Huma's
default is 1 MiB; over ⇒ 413 `PAYLOAD_TOO_LARGE`): `upload_batch_jsonl` 6 MiB (the single total cap,
C32); `lint_recipe` 2 MiB (200 items with inline ABC); `create_draft`, `update_draft` 2 MiB;
`log_listen_events` 256 KiB; every other M2a op with a body 64 KiB. `TestOps_BodyLimits` (B4a) asserts
each value and that all are ≤ the gate's `MaxBody` (8 MiB).

**Inputs:**
- `rate_track`: `{stars: 1..5 | null, verdict?: string ≤ 280 | null}` (no `source` field exists).
- `update_track`: `{title?: string 1..200, status?: draft|to_relisten|final|discarded}`, ≥ 1 field.
- `set_tags`: `{add?: string[] ≤ 20, remove?: string[] ≤ 20}`, ≥ 1 non-empty.
- `add_comment`: `IdemHeader` + `{body, at_second?: number|null, kind?: note|fix|keep|marker}`.
- `update_comment`: `{body?, kind?, state?: open|resolved|addressed|regressed, hidden?: bool}` (C31).
- `list_comments`: `state` (explode), `kind` (explode), `include_hidden` (bool), `order` (newest|time),
  `limit` 1..200 (default 100), `cursor`.
- `get_track_context`: `include` (explode; score|lyrics|comments).
- `log_listen_events`: `{events: [{id, track_id, session_id, kind, from_ms?, to_ms?, format?, device_id?,
  client_at}] (1..200)}`. Beacons append `?mm_client=spa` (D-M2a-8); **`mm_client` is never declared
  as an op parameter** (it would leak into OpenAPI, the MCP input schema and `schema.d.ts`): Huma
  ignores undeclared query parameters, and the gate and the served-operation recorder read it from the
  URL.
- **Exact request tokens:** `request` fields are declared as `api.RawRequest` (`type RawRequest
  json.RawMessage` with a `huma.SchemaProvider` returning `{type: object}`; a bare `json.RawMessage`
  would get the empty schema `{}`). Huma validates the body against the schema, then decodes it into the
  input struct with `encoding/json`, which keeps a `json.RawMessage` as its exact bytes, so seeds and
  other numbers reach `ParseRequest`/`CheckRequest` as the client's JSON tokens. Huma's `RawBody` is not
  used: next to a `Body` it adds an `application/octet-stream` request content (`format: binary`) to the
  OpenAPI document, or with `contentType:"application/json"` it replaces the JSON body schema — either
  breaks the MCP input schema and the generated TS client (checked in huma v2.39.1,
  `setRequestBodyFromRawBody`).
- `lint_recipe`: `{items: [{request: RawRequest, draft_id?: string}] (1..200), batch?: bool (default
  true)}`. `TestLintRecipe_RawTokens` sends seeds `9007199254740992` (2^53) and `1e3` over REST and MCP
  and gets `seed_out_of_range` for both.
- `create_draft`: `IdemHeader` + `{request: RawRequest, label?: string ≤ 40, parent_track_id?: string,
  collection?: string 1..80}`.
- `update_draft`: `{revision: int ≥ 1, request?: RawRequest, label?: string|null, status?:
  backlog|archived, parent_track_id?: string|null}`.
- `upload_batch_jsonl`: `IdemHeader` + `{jsonl: string, collection?: string, parent_track_id?: string,
  allow_partial?: bool, abc_files?: {path: text}}` — limits as C32 (one 6 MiB total).
- `list_playlists`: `include_hidden` (bool). `create_playlist`: `IdemHeader` + `{name, description?,
  track_ids?: string[] ≤ 1000}`. `update_playlist`: `{revision, name?, description?, track_ids?,
  add_track_ids?, hidden?}`.
- `export_playlist_zip`: `IdemHeader` (no body; cap C33). `export_jsonl`: `IdemHeader` + `{selection:
  source|tracks|drafts, source_path?, tracks?: {status?: [], min_stars?: 1..5, tags?: [], playlist_id?,
  track_ids?: []}, drafts?: {status?, collection?, origin?, group_key?, draft_ids?: []}, for_batch?: bool
  (meaningful for selection `source` only; `tracks`/`drafts` always apply the unique-id rule and ignore
  it), filename?: string}`.
- `create_backup`: `IdemHeader` (no body). `get_audit`: `principal_kind` (explode), `principal_id` (a
  `Principal.Key()`; an agent may only pass `agent:`/`device:` keys — a `human:` key from an agent ⇒ 422
  `VALIDATION_FAILED`; agents filter human rows with `principal_kind=human`), `op` (explode),
  `exclude_op` (explode), `since`, `until` (RFC 3339), `status_min` (100..599), `limit` 1..200 (default
  50), `cursor`.
- `create_token`: `IdemHeader` + `{name: 1..80, scopes: (read|write|generate|admin)[] 1..4 unique}`.
  `create_pairing_code`: `IdemHeader` + `{name: 1..80}`.
- Every list obeys the 90 000-byte page cap (M1 C17).

**Shapes** (abridged; full types in `types_m2a.go`; every `PrincipalRef` and key-valued string field is
rendered through `api.RefFor`/`api.KeyFor` — D-M2a-19):
```
PrincipalRef {kind, id (Principal.Key(); "human" for a human seen by an agent), name ("human"/"device" for agents)}
RatingRow   {stars|null, verdict|null, by: PrincipalRef, updated_at}
TrackRating {track_id, human: RatingRow|null, critic: RatingRow|null}
TrackTags   {track_id, tags:[{name, sources:[human|agent|inherited]}]}
TagCount    {name, tracks, by_source:{human, agent, inherited}}
Comment     {id, track_id, body, at_second|null, link ("/tracks/<id>?t=72.0"), url (absolute),
             section:{label, source, confidence}|null, kind, state, source, author: PrincipalRef,
             created_at, updated_at, edited_at|null, state_changed_at|null, hidden}
LintReport  {ok, items:[{index, request_id, hash, errors: Issue[], warnings: Issue[], estimated_duration_s,
             duration_basis, language:{detected, tagged}}], batch_errors: Issue[]}
UploadResult{created, drafts:[{line, id, request_id, label}], errors:[{line, code, field}],
             warnings:[{line, code, field}], collection|null}
PlaylistSummary {id, name, items_count, duration_s, hidden, revision, updated_at}
Playlist    {id, name, description, revision, hidden, created_at, updated_at, created_by, updated_by,
             items:[{position, track: TrackSummary}]}
ExportSource{path, kind ("jsonl"), lines, recipes, first_seen_at}
Export      C33
BackupRun   {id, cause, state, snapshot|null, started_at, finished_at|null, stats|null, error|null}
BackupListing {runs: BackupRun[] (newest 30), snapshots:[{name, created_at, bytes, files, linked, copied}],
             schedule:{at|"off", next_run_at|null}, backup_dir_label ("backup")}
AuditEntry  {id, at, principal: PrincipalRef, transport, client, op, method, status, error_code|null,
             idempotency, created_ids[], args_sha256, duration_ms}
TokenInfo   {id, name, prefix, scopes[], created_at, last_used_at|null, revoked_at|null}
CreatedToken TokenInfo + {token: string|null}          (null on idempotent replay)
Device      {id, name, prefix, scopes[], user_agent, paired_by, created_at, last_seen_at|null, expires_at,
             revoked_at|null}                          (human-only ops only)
DeviceSelf  {id, name, revoked_at}                     (revoke_own_device: the caller's own device, nothing else)
PairingCodeOut {id, name, code: string|null, pair_url: string|null, expires_at}   (nulls on replay)
```

**M1 operations amended** (types and registrations M2a-B4a, behaviour M2a-B4b; records the changes in the M1 plan's C19):
- `search_tracks` gains `min_stars` (1..5, human only), `unrated` (bool: **no human `ratings` row, or
  one with `stars IS NULL`** (cleared); 422 together with `min_stars`), `tag` (repeatable, explode, AND, ≤ 5), `has_open_comments` (bool), `sort` value
  `stars_desc` (human stars desc, unrated last, then `recorded_desc`); without `status`, `discarded` is
  excluded (D-M2a-18).
- `TrackSummary` gains `stars|null`, `critic_stars|null`, `tags[]` (effective names), `comments`,
  `open_comments`. `TrackDetail` gains `rating: TrackRating`, `tags: TrackTags.tags`, `comments`,
  `open_comments`, `playlists:[{id, name}]`, `listening: {plays, completion_rate, last_played_at}`.
- `get_library_facets` gains `tags:[{tag, count}]` (top 50 effective) and `stars:[{stars, count}]` +
  `unrated` (human).
- `get_stats` gains `judgments:{rated_human, rated_critic, stars_hist:{1..5}, comments, open_fix, tags}`,
  `playlists`, `drafts_by_origin{}`, `backups:{last_completed_at, last_state}`.
- `whoami` (M0, `internal/api/ops_system.go`) (rev 3): the `kind` enum becomes `human,agent,device`, the
  `via` doc lists `device`, a device's scopes are `[read, write]`.
- `get_draft` (rev 3): private reference metadata is returned when `p.ActsAsHuman()` (M1 tests `Kind ==
  KindHuman`); `list_drafts` accepts `origin` values from the extended `enumOrigin`.
- `list_drafts` gains filter values `origin=jsonl_upload|editor|agent` and the summary fields of C32;
  `get_draft` gains the detail fields of C32. M2a-B4a **moves both registrations** from M1's
  `internal/api/ops_drafts.go` into `ops_drafts_m2a.go` (same operationIds, paths and meta), so the
  Stage-2 Drafts lane owns every draft op file and never edits an M1 file.

**C38 addendum (M3-B2):** the registry holds 81 operations after M3 (58 + the 23 of M3 C81; `TestRegistry_OpCount`).
`TestOps_CreateOpsDeclareIdempotencyKey` now also covers the three spend operations; the M3 operations live in
`ops_generation_m3.go`, `ops_budget_m3.go`, `ops_jobs_m3.go` and `ops_provider_m3.go` with types in `types_m3.go`.


### C39. Service wiring, HTTP server and CLI additions (amends M0 C6/C7, M1 C20)

> M2b-B2 addendum: `app.Options` gains `Media *MediaOptions` (injected clip/Tier A inputs; nil => probe `cfg.FFmpeg`), `SlotCoin func() bool`
> and `DuelClips func(duels.ClipRenderer) duels.ClipRenderer`; `app.Services` gains `Clips`, `Duels` and `Ears`. `Build` calls
> `app.NewMedia` (`internal/app/media.go`), builds `duels.New`, calls `ears.UseJudgments` and appends the clips resolver to the media chain.

- **One constructor** (`internal/app`; created by M2a-B2a, M2a services filled in by M2a-B4a). `serve`,
  `dev`, `mcp-stdio` and `servertest` all build their services through it — no other code constructs
  an M2a service, a gate, a limiter or `api.Deps`:
  ```go
  type Options struct {
      Now       func() time.Time      // nil ⇒ time.Now; servertest passes Rig.Clock.Now
      NewID     func() string         // nil ⇒ UUIDv7
      Limits    ratelimit.Config      // nil ⇒ ratelimit.Default.Scaled(cfg.RateScale)
      Notify    notify.Notifier       // nil ⇒ from cfg/sec (ntfy or Nop), wrapped in notify.Async
      HTTP      *http.Client          // ntfy client; nil ⇒ 10 s timeout client
      Probe     *serve.Object         // (rev 3) what cmd's ensureProbe returns for serve/dev; nil ⇒ probe ops answer 503
      Version, Commit string
  }
  type Services struct {
      Deps   api.Deps               // every field of C28 filled (nil only for disabled features)
      Server httpserver.Options     // the M2a fields of C39 (gate deps, devices as a nil interface when absent, …)
      Tokens *auth.TokenStore; Local *auth.LocalAuth; Devices *auth.DeviceStore
      Backups *backup.Service; Exports *exports.Service; Idem *idem.Store
  }
  func Build(ctx context.Context, cfg config.Config, sec config.Secrets, st *store.Store, data *os.Root, o Options) (*Services, error)
  func (s *Services) Close(ctx context.Context) error // backup.Close, notifier drain
  ```
  Every store and service receives `o.Now` (`TokenStore`, `LocalAuth`, `DeviceStore`, `idem`,
  `ratelimit`, `audit`, all domain services), which is what lets rig tests advance one clock (C42).
- `httpserver.Options` gains `Judgments, Drafts, Playlists, Exports, Backups, Context, Listening`,
  `Audit *audit.Store`, `Idem *idem.Store`, `Limiter *ratelimit.Limiter`, `Devices auth.DeviceVerifier`
  (+ `DeviceStore *auth.DeviceStore` for the pairing pages), `Notify notify.Notifier`,
  `NewID func() string` — all filled from `app.Services.Server`. `New` builds `gate.New(...)` around the
  inner mux; the served-operation recorder (E2E) wraps the gate; `/api/`, `/human/api/` and
  `MCPFactory` all receive the gated handler. Mounts: `/start`, `/pair`, `/pair.js`, `/auth/pair` →
  `auth.PairingHandler` (both modes); `/auth/` (local) → `LocalAuth.Handler()` (now with
  `/auth/step-up`). `OnPaired` writes an audit row (transport `pair`, op `auth:pair`, principal = the
  new device) and notifies.
- **E2E leak scanner** (`internal/httpserver/e2e.go`, beside M0's served-operation recorder; only when
  `Cfg.E2E`): wraps the **outer** handler and scans every response's headers and, for paths not under
  `/media/`, its body (streamed through a bounded window that also catches canaries split across
  writes) for the run's **static** canaries — `secret_key` in raw, hex, base64 std and base64url forms,
  `edge_secret`, `ntfy_token` — recording `{path, method, kind, where: header|body}` (never the value).
  `GET /__e2e/leaks` returns the JSON list; the Playwright teardown requires it to be `[]`. Dynamic
  secrets (PATs, pairing codes, device cookies) have legitimate issuing responses and stay covered by Go
  `TestNoSecretLeak` (C42, B10). There is no HAR recording.
- `serve` additionally: builds everything with `app.Build`; starts the backup `Scheduler` (unless
  `BackupAt=off`); runs `Exports.Sweep(7 days)` once at start and then from its own 24 h ticker
  (independent of the backup schedule); prunes idempotency rows hourly; marks stale `running` backup
  runs failed at start; calls `Services.Close` on shutdown.
- New subcommands (stdlib `flag` through `newFlags`, `run(ctx, args, getenv, in, out, errw)` testable (rev 3:
  M0's signature), exit codes via `usageError` (2) and `exitError`; config via `config.LoadOffline`, C25; CLI
  mutations write audit rows with principal `system:cli`, transport `cli`):
  - `mcp-stdio` (C30).
  - `export (--source PATH | --tracks [--status S]… [--min-stars N] [--tag T]… [--playlist ID] |
    --drafts [--collection C] [--origin O]) [--for-batch] [--out DIR]` — without `--out` the JSONL goes
    to stdout (refused with exit 2 when ABC files are needed); with `--out` writes `songs.jsonl` (+ ABC
    files) into an empty or new DIR. `--for-batch` matters for `--source` only (C33). Prints the
    validator result to stderr; exit 0 OK, 1 validator issues (a `--source` export of a file that had
    issues still writes the identical bytes, then exits 1), 2 usage.
  - `validate-batch FILE` (C32).
  - `backup [--to DIR]` (synchronous; default `BackupDir`; `BACKUP_IN_PROGRESS` ⇒ exit 1 when another
    process is running one) and `backup list`.
  - `restore --from SNAPSHOT --into DIR` (C34; exit 0 / 1 verification failure / 2 usage or non-empty target).
- `token create`, `token revoke` and `user set-password` (M0) write audit rows and switch to
  `config.LoadOffline`; output unchanged.

**C39 addendum (M3-B2):** `app.Options` gains `Rand` (confirmation tokens and runner instance ids) and `M3 M3Options` (clock, backend,
fake and RunPod overrides, ingester, shutdown context); `app.Services` gains `Lineage`, `Spend`, `Generation`, `Jobs`, `Provider`,
`Uploads`, `Backend`, `Fake`, `Gate`, `Runner`, `Reconciler`; `api.Deps` and `httpserver.Options` gain `Generation`, `Spend`, `Jobs`,
`Provider`, `Lineage` (and `Uploads`, `BaseContext` for the server). `Build` selects the backend from `MM_BACKEND`, writes the
`provider_state` default row once (the fake armed, the serverless backend disarmed, by `system:default`) and builds, never starts,
the runner and the reconciler; the generation-enabled state is `provider.Gate.State`, computed from local facts only. The worker
routes `/worker/v1/uploads/` and `/worker/v1/attempts/` are mounted by `httpserver` (ClassMachine). See the M3 plan, C68 and C74.


### C40. Web (`web/`; amends M1 C22)

> M2b-W2 (recorded): `APP_ROUTES` gains `/duels`, `/duels/:id`, `/ladder`, `/compare`; the nav gains "Duels"
> (badge from `list_duels?state=open&limit=1`) and "Ladder"; the Track page gains the Ears panel and the
> Lineage-duels block (M2b C54.4).

Routes (M0 router; specific before parametric): `/` Library · `/tracks/:id` Track · `/backlog` ·
`/backlog/new` · `/backlog/upload` · `/backlog/:id` Draft editor · `/playlists` · `/playlists/:id` ·
`/export` · `/import` · `/settings` · `/settings/backups` · `/settings/audit` · `/human/tokens` ·
`/human/devices` · 404. **M2a-W1 registers every route above** (with a placeholder component for each
page a later W task fills), so W2–W4 never edit the route table. **`/human/*` is never routed
client-side** (rev 3: M1's `SERVER_PREFIXES` in `web/src/lib/router.ts` lists `/human/api` but not the
pages; M2a-W1 adds `/human`, `/start` and `/pair`): links to it are plain anchors excluded from the router's click interception, and every
programmatic navigation to it uses `location.assign`, so the request reaches the server and the edge
(proxy: two-factor rule) or the step-up check (local) runs before the page loads; the server answers
the SPA shell, which then renders the page. Otherwise a one-factor session would reach `/human/tokens`
without the second factor, and the first `/human/api` call would get an edge 401 that M1's
session-loss logic misreads as "session lost".

```
web/src/
  api/idem.ts                      newIdempotencyKey(): uuidv7() from lib/id; one key per user action (kept across retries); never crypto.randomUUID (secure contexts only)
  api/outbox.ts                    listen-event outbox: IndexedDB store + flush via client + sendBeacon (C40.3)
  api/client.ts + api/session.ts   (M1; rev 3: the response hooks live in client.ts's middleware) + 403 STEP_UP_REQUIRED ⇒ location.assign("/auth/step-up?next=" + path);
                                   429 RATE_LIMITED ⇒ typed error carrying Retry-After (never "session lost")
  lib/id.ts                        (M0 randomId) + uuidv7(): RFC 9562 v7 from Date.now() + crypto.getRandomValues,
                                   monotonic within one millisecond (12-bit counter in rand_a); used for idempotency
                                   keys, listen-event ids and session_id
  lib/time.ts                      (M1) parseT also accepts decimals: "72.0" → 72, "1:12.5" → 72.5 (C22 amended)
  lib/judge.svelte.ts              per-track rating/status/tags state with optimistic update + rollback
  lib/jump.ts                      jumpTo(trackId, seconds) (C40.2)
  lib/comments.ts                  pure: formatAt, sortComments, pinPositions(comments, duration, width), sectionChip
  lib/listen.svelte.ts             listening recorder bound to the player store (C40.3)
  lib/draftForm.ts                 pure: form ⇄ request object, canonical preview (sorted keys), dirty check
  lib/exportView.ts                pure: selection ⇄ query, export summary formatting
  lib/components/StarRating.svelte     5 buttons + clear; keyboard 1–5 / 0; aria-pressed
  lib/components/StatusSelect.svelte
  lib/components/TagEditor.svelte      chips with source badges; add with suggestions from list_tags (fetched once per page); remove
  lib/components/CommentsPanel.svelte  list + filters (open/all, kind) + composer
  lib/components/CommentItem.svelte    timestamp button (jump), section chip "chorus 1 · ~70%", kind, state toggle, hide, author badge
  lib/components/CommentComposer.svelte  body, kind, "at current time m:ss" toggle (default on while playing)
  lib/components/Waveform.svelte       (M1) + props pins: {id, t, kind}[], onpin(id)
  lib/components/DraftEditor.svelte    form + preview + LintPanel + save/save-as-new/archive
  lib/components/LintPanel.svelte      errors/warnings; clicking an issue with `line` focuses that lyrics line
  lib/components/UploadForm.svelte
  lib/components/AddToPlaylist.svelte
  lib/components/PlaylistItems.svelte  reorder (move up/down buttons; drag optional), remove, play all
  lib/components/ExportForm.svelte
  lib/components/BackupList.svelte
  lib/components/AuditTable.svelte
  lib/components/TokenForm.svelte      name + scope checkboxes; shows the token ONCE with its own copy button, plus MCP command templates that use ${MM_TOKEN}, never the token
  lib/components/PairingPanel.svelte   name ⇒ code "ABCD-EFGH" + QR (uqr SVG of pair_url) + 5:00 countdown
                                   (QR library pinned at implementation: `uqr` 0.1.3, MIT, zero dependencies, published 2026-04; rendered from its boolean matrix as SVG rects, no `{@html}`)
  lib/components/ContextButton.svelte  "Copy agent context" ⇒ get_track_context ⇒ clipboard + a displayed char count (UTF-16 units of the copied text)
  routes/{Library,Track,Backlog,DraftNew,DraftUpload,Draft,Playlists,Playlist,Export,Import,Settings,
          SettingsBackups,SettingsAudit,HumanTokens,HumanDevices}.svelte
```

**C40.1 Judging surfaces.** Player bar (M1 `PlayerBar`): human `StarRating` and `StatusSelect` for the
current track plus a "Comment" button that opens the composer at the current time (design §5.4:
judgment is one click from play). Track page: rating (human editable; critic shown read-only as
"critic ★3 — <verdict>"), status, tags, comments panel, waveform pins, `AddToPlaylist`, `ContextButton`.
Library: `TrackTable` gains stars, status and tag columns; `LibraryFilters` gains min stars (incl.
"unrated"), status multi-select and tag multi-select from facets; all filters round-trip through the URL
(`min_stars=4&status=final&tag=a&tag=b`). Mutations use `rate_track` / `update_track` / `set_tags`
optimistically and roll back with an error toast on failure. **On 429 `RATE_LIMITED`** every mutation
rolls back (no optimistic state is committed) and shows a toast "Too many changes — try again in N s"
(N from `Retry-After`); nothing retries automatically.

**C40.2 Timestamp jumps.** Every comment timestamp is a link `/tracks/<id>?t=<s>` (the server's
`link` field, one decimal, e.g. `?t=72.0`; M2a-W1 amends M1's `parseT` to accept decimals and records it
in M1 C22) and a button.
`jumpTo(trackId, t)`: if `player.state.trackId === trackId` ⇒ `player.seek(t)` and `player.resume()` when
`player.el.paused`; otherwise navigate to the track page and call `player.play(trackId, undefined, {startAt:
t})` (rev 3: M1's `PlayOptions.startAt`, applied in `loadedmetadata`; the Track route already handles a
`?t=` deep link with `player.load`). Pins on the waveform call the same function. Section chips appear only when
`comment.section` is non-null.

**C40.3 Listen events.** `listen.svelte.ts` observes the player (rev 3: `Player` has no event emitter; the recorder reads
`player.state` in an `$effect` and adds listeners to `player.el` — `play`, `pause`, `seeked`, `ended`,
`loadedmetadata` — through an injectable `PlayerLike` so tests can fake it): `start` on each play of a
track (from its position), `segment` for each contiguous span heard (closed on pause, seek, ended, track
change, `visibilitychange: hidden`, and every 30 s), `seek` (from → to), `complete` on `ended`, `skip`
when the track changes before 90 % was heard. Each event gets a `uuidv7()` id from `lib/id`,
the tab's `session_id` (one `uuidv7()` per tab), the M1 `device_id` and the current format. `outbox.ts` (inside `src/api/`, the
only network layer): append to IndexedDB (`mm-outbox`, store `events`, key `id`; in-memory fallback when
IndexedDB is unavailable; cap 5000 events, oldest dropped with a counter); flush through the generated
client when ≥ 10 events are pending or 30 s passed, deleting events listed as accepted, duplicate or
rejected; on 429 `RATE_LIMITED` keep every event and wait `Retry-After` seconds (or 30 s if absent) before
the next flush; on `pagehide` / `visibilitychange: hidden` send pending events with
`navigator.sendBeacon("/api/listen-events?mm_client=spa", new Blob([json], {type: "application/json"}))`
and mark them `beaconed` (kept until a later client flush confirms them — ids make re-sends harmless). If
`sendBeacon` throws or returns false, use `fetch(…, {keepalive: true})` from the same module. Flush on
load and on `online`.

**C40.4 Drafts.** `/backlog/:id` becomes the editor: fields id, style, lyrics (monospace, line numbers),
cot, seed, cfg_scale, abc (collapsed), label; canonical JSON preview; `lint_recipe` debounced 400 ms on
change; Save = `update_draft` with `revision` (409 `REVISION_CONFLICT` ⇒ dialog: reload theirs / keep
mine as a new draft via `create_draft`); Archive/Restore; read-only for `fulfilled`. `/backlog/new`
(`create_draft`, optional "start from" an existing draft or track recipe), `/backlog/upload`
(`upload_batch_jsonl`: paste or pick a `.jsonl`, collection name, allow partial, per-line results).
Backlog list shows lint error/warning counts and the origin filter.

**C40.5 Playlists, export, backups, audit.** As listed; downloads are `<a href="<signed url>" download>`
anchors (navigation, not fetch). The Export page lists `list_export_sources`, builds the selection,
calls `export_jsonl`, and shows rows (raw vs regenerated), renamed ids, the validator result, sha256 and
the download. The Export page also opens an earlier export from `?export=<id>` through `get_export`
(the URL is set after each export so a reload keeps the result), and re-fetches it with `get_export`
when its download link is within 1 minute of `expires_at` (a fresh signed link; 410 `EXPORT_EXPIRED`
⇒ "export expired — create it again"). Settings shows the device section (`whoami.kind == "device"` ⇒
"Unpair this browser" = `revoke_own_device`, then `location.assign("/start")`: the cookie is gone, so
any other page would answer the M0 401 page in proxy mode).

**C40.6 `/human/` pages.** `/human/tokens`: `list_tokens`, `create_token` (token shown once with its own
copy button; copyable templates that never embed it, as in design §4.2:
`claude mcp add --transport http metamusic <PublicURL>/mcp --header "Authorization: Bearer ${MM_TOKEN}"`
and an `mcp-stdio` example reading `MM_TOKEN` from the environment), `revoke_token`. `/human/devices`: `list_devices`,
`create_pairing_code` (`PairingPanel`), `revoke_device`. Local mode: a 403 `STEP_UP_REQUIRED` navigates to
`/auth/step-up?next=<route>` (C26).

**Lint and tests.** ESLint bans (M0) unchanged; `sendBeacon`, `fetch` and `indexedDB` appear only under
`src/api/` (`no-restricted-globals` gains `indexedDB` outside `src/api/**`). `svelte/no-at-html-tags`
stays an error; comment bodies, tags, titles and verdicts render as text.

**C40 addendum (M3-B2):** `web/src/api/schema.d.ts` is regenerated with the 23 M3 operations and the amended shapes; the M3 routes
(`/human/generate`, `/human/confirm/:batch`, `/human/spend`, `/jobs`, `/jobs/:id`, `/batches/:id`, `/lineage/:id`) are built by the
Web lane (M3-W1..W3).


### C41. Tooling, CI, routing, coverage (amends M1 C23/C24)

- **Routing contract** (C24 table gains two routers; `deploy/labels.example.yml`):

  | Router | Rule (inside `Host(…) && (…)`) | Priority | Middlewares, in this order |
  |---|---|---|---|
  | `mm-pair` | `Path(/start) \|\| Path(/pair) \|\| Path(/pair.js) \|\| Path(/auth/pair)` | 120 | `mm-strip-identity`, `sec-ratelimit@docker`, `sec-headers@docker` |
  | `mm-device` | `HeaderRegexp(Cookie, (^\|;\s*)mm_dev=) && !PathPrefix(/human/) && !PathPrefix(/oauth/) && !PathPrefix(/auth/)` | 50 | `mm-strip-identity`, `sec-ratelimit@docker`, `sec-headers@docker`, `mm-compress` |

  `mm-device` never matches `/media/`, `/rest/`, `/mcp` precedence-wise (those routers have priority
  100). The manifest `start_url` becomes `/start` (C22.5 amended).
- **Coverage thresholds** (`coverage.thresholds`): add `internal/gate 95`, `internal/idem 95`,
  `internal/ratelimit 95`; `internal/auth` stays 95 (devices and pairing included).
- **MCP `tools/list` ceiling.** (rev 3) M1 re-baselined `toolsListBaselineBytes` from 4 412 to 42 997
  (`internal/mcpgen/parity_test.go`; the committed golden is indented JSON of 43 683 bytes, 21 011 bytes
  compact, 11 tools) and the budget is relative (`TestToolsListGolden` fails above 1.5× that constant).
  M2a-B4a re-baselines the constant (the golden roughly doubles with 15 more tools; the relative budget is
  re-set, not loosened). M2a adds an **absolute ceiling: the compact-JSON `tools` array ≤ 40 000 bytes (≈ 10 000
  tokens at ~4 bytes per token)**, asserted by `TestToolsList_Ceiling` and first measured right after
  M2a-B4a (all 32 registrations with final schemas); the measured bytes go into the B4a commit message
  and X1's `metrics/REPORT.md`. If the ceiling is exceeded, first-class tools are demoted to `generic`
  in this order until it holds — `get_audit`, `update_playlist`, `upload_batch_jsonl`, `update_draft` —
  and the demotion is recorded in C38 (and in `next_actions` texts, which then say
  `call_operation <op>`). The relative 1.5× budget stays as the drift alarm between re-baselines.
  **Measured at M2a-B4a (rev 3):** with all 32 registrations the compact `tools` array was 74 701 bytes; the four
  planned demotions alone left 62 386, so B4a (1) advertises **slim output schemas** in `tools/list` (no
  `description`, `additionalProperties` or `required`; the full schema stays in `describe_operation`) and
  (2) also demoted `update_track`. Result: 21 tools, **38 676 compact bytes** (golden 85 917 indented; the relative
  budget baseline is now 85 917).
- **CI e2e job:** the E2E leak scanner (C39) is checked in the Playwright teardown (`GET /__e2e/leaks`
  must be `[]`; rev 3: the teardown is the closure `globalSetup` returns in `web/e2e/global-setup.ts`, which
  already performs the served-operation check — there is no separate teardown file); no HAR recording, no HAR upload. The e2e global setup sets `MM_RATE_SCALE=100` (one
  `e2e` human principal across parallel projects and workers would otherwise exceed burst 200 and
  `human_heavy` 12/min) and `MM_BACKUP_AT=off`.
- **CI time budget:** the mobile projects (`chrome-pixel7`, `chrome-iphone15`) run only the `m2a-judge`, `m2a-comments`,
  `m2a-human` and `m2a-listen` specs; every other M2a spec runs on desktop Chrome only; Firefox runs only
  the player, seek, jump (`m2a-comments` timestamp tests) and `m2a-listen` specs. Heavy Go tests skip
  under `-short` (so `make check` stays < 30 s warm): `TestNoSecretLeak`, every `TestCLI_*`, every
  `TestAcceptance_*`, and `TestCLI_MCPStdio_Smoke` (which reuses `MM_E2E_BIN` when set instead of
  running `go build`). M2a-X1 records the measured e2e (target ≤ 5 min) and `make check` (target < 30 s
  warm) wall times. (rev 3) M1 baselines: `make check` 33 s median / 37 s at X1, CI e2e 3–6.3 min for 124
  tests; the targets stay goals, and X1 explains any regression against these numbers.
- `scripts/loop` allowlist and `metrics/loop.schema.json`: no new targets.

### C42. Test harness and fixtures

> M2b-B2 addendum: `servertest.Options` gains `M2b M2bOptions{Fixture, Media, Backfill, FakeClips, SlotCoin, WrapClips, Override}`;
> `servertest.Alternating()` is the default slot coin; the parity runner's rigs use `M2b{Fixture: true}`. New test-only helper packages:
> `media/clip/cliptest`, `duels/duelstest`, `ears/earstest` (asserted absent from the binary by `TestNoTestHelpersInBinary`).

> M2b-B3 addendum: `M2bOptions` also has `ClipCacheMB` (the clip cache cap, used to exercise eviction) and
> `archivetest.ImportInbox` imports a purpose-built inbox (signals the profiles do not hold).

- **Clock rules (binding).** `testing/synctest` is used **only** for code driven through
  `httptest.NewRecorder` or in-memory transports with no real socket, because network I/O is not
  durably blocking and a bubble around an `httptest.Server` or a go-sdk HTTP client never advances time.
  Concretely: `Scheduler.Loop` (C34) and pure timer code. Everything else uses an injected clock:
  rig tests advance `Rig.Clock`, and every store and service built by `app.Build` (`TokenStore` — which
  gains clock injection in M2a-B1 —, `LocalAuth`, `DeviceStore`, `idem`, `ratelimit`, `audit`, all
  domain services) reads that clock. TTL, lease, slide and expiry tests (`TestIdempotency_TTLs`,
  `TestIdempotency_PendingLeaseReclaimed`, `TestRateLimit_RetryAfter`, `TestDevice_*`,
  `TestPairing_CodeLifecycle`, `TestLocalStepUp_HumanPaths`, `TestMCP_DerivedIdempotency_AddComment`)
  therefore advance a fake clock, never a synctest bubble.
- `internal/servertest` (**test-only**; added to `TestNoTestHelpersInBinary`). **Ownership:** M2a-B2b
  creates it (rigs, clients, MCP/stdio sessions, all services through `app.Build`); M2a-B4b extends
  `Options.Fixture` with the M2a fixture rows; M2a-B9 implements `Rig.Device()` and the local-mode
  step-up inside `Rig.Human()`. Until B9, `Device()` calls `tb.Skip("M2a-B9")` and local-mode `Human()`
  performs no step-up (step-up enforcement itself arrives with B9). Other tasks add cases, not helpers.
  ```go
  type Options struct {
      Mode     string             // "proxy" (default) | "local"
      Fixture  bool               // librarytest.Fixture (M1 rows + the M2a additions from M2a-B4b on)
      Files    bool               // librarytest.WriteFiles: files whose sha256 match the rows
      Clock    *Clock             // fixed, advanceable; nil ⇒ `librarytest.FixedNow` (2026-10-06T12:00:00Z, rev 3); passed to app.Build
      IDSeed   string             // deterministic UUIDv7-shaped id sequence ("" ⇒ "0190")
      Limits   ratelimit.Config   // nil ⇒ effectively unlimited (1e6/min)
      Canary   bool               // canary secret_key / edge_secret / ntfy_token / human username; ntfy fake server
  }
  type Rig struct {
      URL, DataDir, BackupDir string; Store *store.Store; API huma.API
      Clock *Clock                // the clock every service of this rig reads
      Logs *bytes.Buffer          // captured slog JSON
      Ntfy *FakeNtfy
  }
  func New(tb testing.TB, o Options) *Rig // cleanup: Services.Close (waits for backups) before t.TempDir removal
  func (r *Rig) Human() *Client                                   // REST, proxy headers (or a local session; step-up from B9)
  func (r *Rig) Agent(tb testing.TB, scopes ...auth.Scope) (*Client, string /* token id */)
  func (r *Rig) Device(tb testing.TB) *Client                     // paired through /human/api + /auth/pair (B9; Skip before)
  func (r *Rig) MCP(tb testing.TB, scopes ...auth.Scope) *mcp.ClientSession   // streamable HTTP, bearer
  func (r *Rig) Stdio(tb testing.TB, scopes ...auth.Scope) *mcp.ClientSession // in-memory transports + NewStdioServer
  type Client struct{ /* records every response (status, headers, body) for TestNoSecretLeak */ }
  func Normalize(v any, masks ...string) any // replaces token ids, transport/source/client fields named in masks
  ```
- **Parity runner** (`internal/mcpgen/parity_test.go`, refactored in M2a-B2b from M1's `parityArgs` /
  `parityFixture` table and `TestParityEveryOperation`, which already runs every tool and generic op
  through the first-class tool and `call_operation`, mutating ops on fresh rigs; cases added by each task):
  three rigs with identical fixtures, clock and id sequence; an ordered case list `[]ParityCase{Op,
  Args func(*Rig) map[string]any, Masks []string}` is executed REST-on-A (agent bearer, explicit
  `Idempotency-Key: parity-<n>` for create ops), MCP-on-B (first-class tool or `call_operation`, no key ⇒
  derived) and stdio-on-C; normalised responses must be equal at every step.
  `TestParityEveryOperation` fails when a registered non-excluded op has no case. Human-only and
  device-self-service ops are covered by `TestHumanOnly_*` and `TestDeviceOps` (REST only). Cases that
  end a principal (`revoke_own_token`) run last on a dedicated token.
- **`librarytest`** (M2a-B1 / M2a-B4b): `FixtureV2(tb, st, now)` (M2a-B1) is M1's `Fixture(tb, st, now
  time.Time) Fixtures` body frozen as it is at the start of M2a — it writes only v2 tables and is never
  extended; `Fixture` = `FixtureV2` plus the M2a additions (M2a-B4b). (rev 3) Names in this plan map to
  M1's fixture constants: "track 1" = `TrackLuna` (aligned plan score, 12 s: verse 0–4, chorus 4–8, outro
  8–12), "track 2" = `TrackCitta` (no score), "track 3" = `TrackTwinA` with child `TrackTwinB`, "untimed
  track" = `TrackShort` (untimed score). Additions: ratings (Luna human 5 + agent 2; Citta human 3;
  TwinA agent 4 only), tags (TwinA human `ballad`, `night`, agent `upbeat`; the fixture inserts TwinB's
  `inherited` row for `ballad` only, directly, so the AND matrix distinguishes parent and child),
  comments (Luna: general note, `fix` at 5.0 s labelled `chorus` from the plan sections, `keep` at 1.0 s;
  Short: one timestamped comment without label; one hidden comment), playlist `fixture-mix` (3 tracks),
  one `varianti_import` draft and two `jsonl_upload` drafts, listen events (Luna: 2 starts, 1 complete),
  one paired device, one completed backup run. M1's `WriteFiles(tb, dataDir string)` already writes
  deterministic placeholder master bytes, MP3-like bytes and peaks JSON whose sha256 the fixture rows
  carry (backup, export and ZIP tests need matching hashes, not decodable FLAC); B4b extends it only for
  files the new rows need: M1's `Fixture` has no `artifacts` rows, so B4b adds one artifact row with its
  file under `takes/<TrackLuna>/` (backup manifests list artifacts). The upgrade test reuses M1's `store.OpenRaw` +
  `store.MigrateTo` hooks (`internal/store/export_test.go`, as `TestMigration0002_UpgradeFromM0` does) to
  build a real v2 DB with `FixtureV2`; there is no `storetest` package. Test sources never contain a
  literal token-shaped string (`mm[pcd]_[a-z2-7]{52}` is rejected by gitleaks and the gate; build them at
  runtime as `scripts/public-gate-selftest.sh` does) and no committed file may have a basename the gate
  forbids (`songs.json*`, `*.zip`, `*.mp3`, `*.db*`).
- **Shared synthetic archives** (M2a-B4b): `archivetest.SyntheticArchive(tb, profile string) Template`
  (rev 3: new test-only package `internal/archivetest`, added to `TestNoTestHelpersInBinary` — it cannot
  live in `librarytest`, because `internal/importer`'s own tests import `librarytest` and `librarytest`
  would then import `importer`) imports the M1 `acceptance` or `e2e` profile with real ingest (mirroring
  `seedSynthetic` in `cmd/metamusic/cmd_seed.go`) **once per test binary** (`sync.Once` per
  profile; heavy, skipped under `-short`) into a template directory; `Template.Copy(tb)` gives a test its
  own DB copy plus hardlinked files. Every M2a acceptance test that needs an imported archive uses it
  instead of importing again.
- `make fixture-db` (M1) keeps working and now includes the M2a rows.

**C42 addendum (M3-B2):** `servertest.Options` gains `Backend` (`none`|`fake`), `Runner`, `FakeSpeed`, `Fake`, `Caps`, `M3`
(`librarytest.FixtureM3`), `Rand` and `Ingest`; `Rig` gains `Fake`, `Backend`, `Runner`, `Reconciler`; `servertest.SeededReader(seed)`
gives every rig the same token sequence. `ParityCase.Args` is now `func(r *servertest.Rig, prev map[string]any) map[string]any` (`prev` is
the raw response of the previous case on the same rig; `fixedArgs` ignores it) and the parity rigs run `Backend: "fake"` with the
runner idle. New helpers: `librarytest.FixtureM3`/`ApplyM3`/`SeedJob`, `archivetest.GenerateArchive`, `providertest`, `synth.FLACUncalibrated`.


---

## Acceptance mapping (roadmap M2a → named tests)

| Roadmap item | Named test(s) |
|---|---|
| [S] timestamp comment jumps the player | `m2a-comments.spec.ts › jump: a timestamped comment seeks the paused player on the same track` (chrome + firefox: same track paused, same track playing, another track playing ⇒ `currentTime` within ±0.5 s of `at_second`) · `› waveform pin seeks` · `TestAcceptance_M2a_TimestampCommentLink` · `jump.test.ts` |
| section labels only from timed sections (none outside every section) | `TestAddComment_SectionLabelOnlyFromTimed` · `TestMigration0003_CommentSectionSourceNeverLyrics` · `m2a-comments.spec.ts › general comment on an untimed track has no timestamp and no section chip` |
| [S] rating / status / tag filters | `TestAcceptance_M2a_RatingStatusTagFilters` · `TestSearchTracks_JudgmentFilters` · `TestParity_MultiValueQuery` (tag) · `m2a-judge.spec.ts › library filters: status, two tags AND, min stars, back/forward` (URL carries `tag=a&tag=b`) |
| human stars + status; critic stored separately | `TestRate_SourceFromPrincipal` · `TestMigration0003_RatingSourceCheck` · `m2a-judge.spec.ts › rate 4 and set to_relisten from the player bar` |
| agent `rate_track` never changes the human rating | `TestAcceptance_M2a_RatingIsolation` (REST bearer, MCP tool, `call_operation`, stdio; checks `get_track`, `search_tracks min_stars`, facets, context, ZIP `POPM`) |
| tags (human/agent/inherited) | `TestSetTags_Sources` · `TestInheritTags_OnInsertTake` · `TestNormalizeTag` |
| general + timestamped comments, kind/state | `TestAddComment_*` · `TestCommentTransitions` · `TestUpdateComment_Permissions` · `m2a-comments.spec.ts › resolve, reopen and hide a comment` |
| [S] `songs.jsonl` export byte-identical | `TestAcceptance_M2a_ExportByteIdentical` (seeded synthetic archive: `--source` equals the original file bytes minus the lines M1 did not record; every raw row of a `tracks` export equals a `recipe_sources.raw`) · `TestExport_SourceSuperseded` · `TestPyJSON_Golden` · `TestDetectStyle` · `m2a-export.spec.ts › source export is the original file byte for byte and reports exactly its own issues` |
| [S] `yue2 batch` validator mirror accepts it unchanged + `--for-batch` lineage | `TestAcceptance_M2a_ExportPassesBatchValidator` (D-M2a-20: `tracks`, `drafts` and every for-batch export validate clean; a `--source` export reports exactly the issues of its original file — one `duplicate_id` for the acceptance `songs.jsonl`) · `TestExport_ForBatchSiblingsUniqueIDs` (3-sibling lineage) · `TestValidateBatch_Table` · `TestCheckRequest_AgreesWithParseRequest` |
| [S] v2 drafts visible, editable, lint-clean | `TestAcceptance_M2a_VariantiDraftsEditableLintClean` · `m2a-drafts.spec.ts › varianti drafts are listed under their origin filter` |
| draft editor + `lint_recipe` | `TestLint_ProtocolRules` · `TestLint_RunsOnCanonical` · `TestDrafts_LintDecoration` · `TestLintRecipe_RawTokens` · `TestLint_SongwritingRules` · `TestEstimateDuration` · `TestDetectLanguage` · `TestDrafts_CreateUpdateRevision` · `draftForm.test.ts` |
| JSONL batch upload into drafts | `TestUploadBatch_AllOrNothing` · `TestUploadBatch_Limits` · `TestUploadBatch_AllowPartial` · `TestUploadBatch_ABCFiles` · `m2a-drafts.spec.ts › upload of three synthetic lines into a new collection creates three drafts` |
| playlists + MP3 ZIP with current tags | `TestPlaylists_Revision` · `TestZipExport_CurrentTagsID3` (title changed after creation appears in TIT2) · `TestZipExport_Deterministic` · `TestZipExport_Caps` · `TestZipExport_EntryNamesFlat` · `TestID3_Encode` · `m2a-playlists.spec.ts` |
| [S] backup restore on an empty directory | `TestAcceptance_M2a_BackupRestoreEmptyDir` · `TestBackup_EXDEVFallbackCopiesAndVerifies` · `TestBackup_SnapshotSurvivesLiveMutation` · `TestBackup_ManifestComplete` · `TestBackup_CrossProcessGuard` · `TestBackup_SameSecondNamesDistinct` · `TestRestore_RefusesNonEmptyTarget` · `TestRestore_DetectsTamper` · `TestRetain_Table` · `TestNextRun_Table` · `TestScheduler_LoopSynctest` |
| `/context` bundle ≤ 24k, `include=` | `TestAcceptance_M2a_ContextBudget` (REST body and MCP `TextContent`) · `TestFit_WorstCase` · `TestFit_BudgetFixedPoint` · `TestMeasure_MatchesRESTBody` · `TestContext_Include` · `TestContext_NextActions` |
| audit log (every agent call + human mutation) | `TestAudit_CoverageMatrix` · `TestAudit_RefusalsRecorded` · `TestMigration0003_AuditAppendOnly` · `m2a-audit.spec.ts` |
| rate limits | `TestRateLimit_AgentBucket` · `TestRateLimit_HeavyBucket` · `TestRateLimit_RetryAfter` (fake clock) · `TestRateLimit_PairEndpoint` · `TestRateLimit_Scaled` · `judge.test.ts` / `outbox.test.ts` (429 handling) |
| idempotency: REST header required; MCP derived 10 min; explicit 24 h; checked before any consumption | `TestIdempotency_RESTRequiresKey` · `TestIdempotency_ReplayAndConflict` · `TestIdempotency_InProgress` · `TestIdempotency_PendingLeaseReclaimed` · `TestIdempotency_LeaseExtendedForLongHandler` · `TestIdempotency_TTLs` (fake clock) · `TestIdempotency_ErrorsNotStored` · `TestIdempotency_CheckedBeforeConsumption` · `TestMCP_DerivedIdempotency_AddComment` · `TestIdempotency_SecretFieldsRedacted` |
| `/human/` pages for tokens + devices; human-only refusals | `TestHumanOnly_RefusedForAgentsDevicesStdio` · `TestHumanOnly_NotFoundOffHumanPrefix` · `TestLocalStepUp_HumanPaths` · `nav.test.ts` (`/human/*` is a full-page navigation) · `m2a-human.spec.ts` |
| device pairing (QR / code, `mm_dev`, read+write only) | `TestPairing_CodeLifecycle` · `TestPairing_SingleUse` · `TestDevice_ScopesReadWriteOnly` · `TestTrustMatrix_DeviceCookie` · `TestHTTPServer_NilDevicesNoPanic` · `TestRoutingContract` (mm-pair, mm-device) · `m2a-human.spec.ts › pairing: a code makes a device that can rate but never reach /human; unpair returns to /start` · DC-M2a-1..3 |
| `mcp-stdio` (MM_TOKEN required, agent principal) | `TestMCPStdio_RequiresToken` · `TestMCPStdio_AgentPrincipal` · `TestMCPStdio_RevokedMidSession` · `TestCLI_OfflineCommandsWithoutMMAuth` · `TestCLI_MCPStdio_Smoke` · `TestParity_Stdio` |
| listen-event logging (beacon + IndexedDB outbox) | `TestListen_ValidationAndDedup` · `TestListen_Stats` · `outbox.test.ts` · `listen.test.ts` · `m2a-listen.spec.ts › offline: events stay in IndexedDB and are flushed when back online` |
| an MCP client with a token performs every M2a UI action | `TestAcceptance_M2a_AgentPerformsEveryUIAction` (derived from the registry: every op with a `ui` route that MCP exposes — not human-only, not `exclude:*` — is called over MCP and appears in `get_audit`) · parity runner · served-operation coverage (zero unserved ui ops) |
| agents never see the operator's username or device names (D-M2a-19) | `TestAgentsNeverSeeHumanNames` · `TestRefFor_Table` · `TestGetAudit_AgentHumanKeyFilter422` |
| `TestNoSecretLeak` | `TestNoSecretLeak` (Go; static and dynamic secrets) · E2E leak scanner (`GET /__e2e/leaks` = `[]` in the Playwright teardown; static canaries) |
| notifications | `TestNotify_Ntfy` · `TestNotify_NoSecretsInMessages` · `TestBackup_FailureNotifies` |
| [N] device checklist: a paired device survives a 13 h gap | `TestDevice_SurvivesThirteenHourGap` · DC-M2a-2 |
| coverage gates | `coverage` CI job (thresholds incl. gate/idem/ratelimit 95 %) |

---

## Lanes and tasks

Stage 1 (branch `m2a` from merged `main`): **M2a-B1** (main worktree, `m2a-found`) → in parallel
{**M2a-B2a** → **M2a-B2b**} (main worktree, `m2a-found`) and **M2a-B3** (worktree
`../metamusic-wt/m2a-domain`, touches only `internal/engine/yue2/` and `internal/export/{pyjson,style}*`)
→ merge → **M2a-B4a** (contract freeze) → **M2a-B4b** (judgments, library amendments, fixtures; stays in
Stage 1 because it edits `internal/library/**`), both `m2a-found`. Stage 1 lands every shared contract:
schema, gate, MCP/stdio, harness, `internal/app`, all 32 op registrations with final input/output types
(every type they reference, incl. the lint types declared by B3 and `export.Source`, exists by the end
of B4a; `STUB(M2a-Bn)` handlers answer 501 `NOT_IMPLEMENTED`), `serve.Object.DownloadName`, the
judgments service, all service constructors, `openapi.json`, `schema.d.ts`, the fixtures and the fixture
DB. **`openapi.json` and `schema.d.ts` are frozen after M2a-B4a.**
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from M2a-B4b):
**Drafts** (`m2a-drafts`: M2a-B5), **Data** (`m2a-data`: M2a-B6 → M2a-B7), **Agent** (`m2a-agent`:
M2a-B8 → M2a-B9), **Web** (`m2a-web`: M2a-W1 → M2a-W2 → M2a-W3 → M2a-W4), **Infra** (`m2a-infra`:
M2a-I1 → M2a-I2).
Stage 3 (`m2a-int`): **M2a-B10** (cross-cutting acceptance, `TestNoSecretLeak`) → **M2a-W5** (Playwright)
→ **M2a-X1** integration → final review → **M2a-X2** deploy (operator-assisted).
**File ownership:** in Stage 2 no lane edits `internal/api/api.go`, `internal/api/types_m2a.go`,
`internal/api/ops_drafts.go` (M1), `internal/app/**`, `internal/httpserver/server.go`,
`internal/library/**`, `internal/importer/apply.go`, `internal/media/serve/{types.go,sign.go}`,
`internal/api/ops_system.go`, `cmd/metamusic/{main.go,cmd_serve.go,
cmd_mcpstdio.go}`, `openapi.json` or `web/src/api/schema.d.ts` — a needed contract change goes back
through this document and a Stage-1-owner fix commit that every lane rebases onto. Each lane replaces
only the stubs in its own `internal/api/ops_*.go` files and owns its packages (B5 also owns
`internal/engine/yue2/{lint,duration,lang}*.go`, whose types B3 froze). `internal/servertest` follows
the ownership in C42 (B2b creates, B4b extends the fixture, B9 implements `Device()` and local
step-up; everyone else adds cases, not helpers). Exceptions to "only its own files" are named in each
task's **Files** (B6: `internal/media/serve/handler.go`; B9: `internal/auth/**`).

**C42 addendum (M3-B1):** `servertest.Clock` is a type alias of `clocktest.Manual` (`internal/clock/clocktest`, added to
`TestNoTestHelpersInBinary`); `servertest.NewClock(t time.Time)` is unchanged, `Now/Advance/Set` keep their meaning and
`Advance`/`Set` now also fire due `NewTimer` timers in deadline order. See the M3 plan, C68.

### Task M2a-B1: config, principals, leaf packages, migration 0003, registry meta

**Files:** `internal/config/{config.go,config_test.go}` (C25 incl. `LoadOffline`, `RateScale`);
`internal/auth/{principal.go,principal_test.go}` (C26 types only: `KindDevice`, `KindSystem`, `Key`,
`ActsAsHuman`, `StepUpAt`, `CLI`); `internal/auth/local.go` (`UseClock` only, rev 3); `internal/auth/{tokens.go,tokens_test.go}` (`NewTokenStore` clock
injection, `Get`; callers in `cmd/metamusic` and `internal/httpserver` updated);
`internal/{canonjson,reqinfo,redact}/*.go` + tests; `internal/api/errors.go` (5xx logging through
`redact.Error`, closing M0's TODO); `internal/store/migrations/0003_judge.sql`,
`internal/store/{triggers_m2a_test.go,schema_m2a_test.go,upgrade_m2a_test.go,nodelete_test.go}`,
`internal/store/{schema_test.go,upgrade_test.go}` (rev 3: M1's version pins `== 2` become "at least 2" /
`MigrateTo(…, 2)`; the upgrade test reuses `store.OpenRaw` + `store.MigrateTo` from `export_test.go`, so
there is no `storetest` package);
`internal/library/librarytest/fixture_v2.go` (`FixtureV2` = M1's current fixture body; `Fixture` calls
it); `internal/api/{registry.go,registry_test.go}` (Meta fields, validation, exported `Authorize`);
`internal/api/api.go` (`NewID` only); `docs/plans/2026-10-12-m0-skeleton.md` (C1, C3, C4 addenda incl.
`TokenStore` signature); the M1 plan (C11: `user_version` now 3).
**Tests first:**
- `TestConfig_BackupAndNtfy`: `MM_DATA_DIR=/var/lib/mm/data` ⇒ `BackupDir=/var/lib/mm/backup`,
  `BackupAt=03:30`; `MM_BACKUP_AT=3:30` ⇒ error naming `MM_BACKUP_AT`; `off` ⇒ ok;
  `MM_BACKUP_DIR=/var/lib/mm/data/b` ⇒ error; `MM_NTFY_URL=http://ntfy.example.com/t` ⇒ error;
  `http://127.0.0.1:8090/t` ⇒ ok; `https://ntfy.example.com/t?x=1` ⇒ error;
  `https://user:pass@ntfy.example.com/t` ⇒ error naming `MM_NTFY_URL` and not containing `pass`;
  `MM_RATE_SCALE=0` / `1001` / `x` ⇒ error, `100` ⇒ 100.
  `TestConfig_LoadOffline` (`MM_AUTH` unset ⇒ ok with `AuthMode ""`; every other check still applies;
  `Load` still refuses). `TestLoadSecrets_NtfyTokenOptional` (missing ⇒ ""; empty file ⇒ error naming
  `ntfy_token`, no bytes).
- `TestPrincipal_KeyAndActsAsHuman` (human `alice` ⇒ `human:alice`, true; device `d1` ⇒ `device:d1`,
  true; agent `t1` ⇒ `agent:t1`, false; anonymous ⇒ "", false; `CLI` ⇒ `system:cli`, false).
- `TestTokenStore_InjectedClock` (`created_at`/`last_used_at` come from the injected clock),
  `TestTokenStore_Get` (no hash fields; unknown ⇒ `ErrNotFound`), `TestLocalAuth_UseClock` (a session
  expires by the injected clock).
- `TestCanonical` (`{"b":1,"a":[2,{"d":1e3,"c":1.0}]}` ⇒ `{"a":[2,{"c":1.0,"d":1e3}],"b":1}`; `"<&>"` stays
  unescaped; duplicate key ⇒ error; depth 65 ⇒ error; invalid UTF-8 ⇒ error).
- `TestSanitizeClient` (`claude-code/2.1.80 (linux)` ⇒ `claude-code/2.1.80`; control bytes dropped;
  300-char token ⇒ 120 bytes; `""` ⇒ `""`).
- `TestRedact` (a syntactically valid `mmp_` token ⇒ `mmp_<redacted>`; `Bearer abc.def` ⇒
  `Bearer <redacted>`; `https://h.example/x?exp=1&sig=abc` ⇒ `https://h.example/x?<redacted>`; 64 hex
  chars ⇒ `<redacted>`; `dial tcp: connection refused` unchanged), `TestProblem5xxLogRedacted` (a handler
  error carrying a canary `mmp_` token is logged as `mmp_<redacted>`).
- `TestMigration0003_Applies` (user_version 3; every table, index and trigger name of C27 present),
  `TestMigration0003_UpgradeFromM1` (a v2 DB built with `store.OpenRaw` + `store.MigrateTo(…, 2)` +
  `librarytest.FixtureV2` migrates; drafts get `revision 1`, `created_by 'import'`; every M1 row
  unchanged; a token created before still verifies). The upgrade test never uses `Fixture`, which needs
  v3 tables from M2a-B4b on.
- `TestTriggers_M2a` table: DELETE on every new table except `idempotency` ⇒ error; `UPDATE audit_log` ⇒
  `append-only`; `UPDATE listen_events` ⇒ `immutable`; a second redemption of a pairing code ⇒ `already
  redeemed`; `UPDATE recipe_drafts SET request_json=…` on a fulfilled draft ⇒ error, on a backlog draft ⇒
  ok; `UPDATE recipe_drafts SET created_by=…` ⇒ frozen; frozen columns of ratings, track_tags, comments,
  playlists, devices, pairing codes, backup runs ⇒ `frozen column`; a finished backup run ⇒ immutable;
  a second `running` backup run ⇒ UNIQUE error (`backup_runs_one_running`).
- `TestMigration0003_RatingSourceCheck` (`source='human'` with `principal_kind='agent'` ⇒ CHECK error;
  `source='agent'` with `device` ⇒ CHECK error), `TestMigration0003_CommentSectionSourceNeverLyrics`
  (`section_source='lyrics'` ⇒ CHECK error; label without `at_second` ⇒ CHECK error),
  `TestMigration0003_DeviceScopes` (`'read,write,generate'` ⇒ CHECK error),
  `TestMigration0003_PlaylistCount` (`items_count` ≠ array length ⇒ CHECK error),
  `TestMigration0003_AuditAppendOnly`.
- `TestNoDeleteSQL` (C27: allowlist `{idempotency, human_sessions}` named in the test; M0's three
  `DELETE FROM human_sessions` in `internal/auth/local.go` pass; a planted `DELETE FROM comments` in a
  temp copy fails).
- `TestRegisterPanics` new rows (C28) and `TestAuthorize_Exported` (same results as M0's table).
Done: `make check` green; `openapi.json` byte-identical (no op changed; new `x-mm` keys are `omitempty`).

### Task M2a-B2a: rate limits, audit, idempotency, gate, server wiring, `internal/app`

**Files:** `internal/{ratelimit,audit,idem,gate}/*.go` + tests; `internal/app/{app.go,app_test.go}`
(C39: `Build` with the gate dependencies — limiter, audit, idem — and M0/M1 services; M2a services are
filled in by B4a); `internal/httpserver/{server.go,server_test.go}` (gate wiring; recorder wraps the
gate; `MCPFactory` receives the gated handler), `internal/httpserver/{e2e.go,e2e_test.go}` (the E2E leak
scanner and `GET /__e2e/leaks`, C39); `cmd/metamusic/cmd_serve.go` (`serve`/`dev` build through
`app.Build`; hourly idempotency prune); `go.mod` (`golang.org/x/time` direct);
`docs/plans/2026-10-12-m0-skeleton.md` (C6 addendum: gated handler, leak scanner).
Gate tests use a test-only API (`gate_test.go`) with six ops: `t_read` (GET), `t_write` (PATCH),
`t_create` (POST, create), `t_heavy` (POST, create + heavy), `t_secret` (human-only create with
`secret_fields: ["token"]`) and `t_consume` (create; consumes a single-use row then returns 201). All
time-dependent tests use injected fake clocks (C42), never synctest.
**Tests first:**
- `TestRateLimit_AgentBucket` (fake clock: 60 calls allowed at t0, the 61st ⇒ refused with retry 1 s; at
  t0+1 s one more allowed), `TestRateLimit_HeavyBucket` (7th heavy call refused with bucket `agent_heavy`;
  the refusal does not consume a default token), `TestRateLimit_HumanCeiling` (200 burst, then refused),
  `TestRateLimit_RetryAfter` (fake clock; header is whole seconds ≥ 1), `TestRateLimit_PruneIdle`,
  `TestRateLimit_Scaled` (`Default.Scaled(100)`: 6 000 agent calls at t0 allowed).
- `TestIdempotency_RESTRequiresKey` (no header ⇒ 428, audit `idempotency=missing`),
  `TestIdempotency_ReplayAndConflict` (same key + body ⇒ identical status/body + `Idempotent-Replayed:
  true`, handler ran once; other body ⇒ 409 `IDEMPOTENCY_CONFLICT`; another principal with the same key ⇒
  independent), `TestIdempotency_InProgress` (blocked handler; concurrent duplicate ⇒ 409 + `Retry-After:
  1`), `TestIdempotency_PendingLeaseReclaimed` (a row left `pending` by a simulated crash — `Begin`
  without `Complete`/`Release`: a retry at +1 min ⇒ 409 `IDEMPOTENCY_IN_PROGRESS`; at +2 min 01 s with
  the same body ⇒ the `Begin` upsert returns the row (reclaimed), the handler runs, 201; at +2 min 01 s
  with another body ⇒ 409 `IDEMPOTENCY_CONFLICT`), `TestIdempotency_LeaseExtendedForLongHandler` (a
  blocked handler; the test advances the fake clock 1 min and fires the injected `Ticker` three times ⇒
  `lease_until` moved each time; at +3 min a concurrent retry still gets 409
  `IDEMPOTENCY_IN_PROGRESS`), `TestIdempotency_TTLs` (fake clock: header key replays at 23 h 59 m and
  re-executes at 24 h 01 m; derived key replays at 9 m 59 s and re-executes at 10 m 01 s),
  `TestIdempotency_ErrorsNotStored` (422 then a corrected body with the same key ⇒ 201),
  `TestIdempotency_CheckedBeforeConsumption` (`t_consume` replay ⇒ stored 201, the single-use row
  consumed exactly once, no error), `TestIdempotency_SecretFieldsRedacted` (`t_secret`: first response
  carries `token`; the stored row has `"token":null`; the replay returns null), `TestIdempotency_Prune`,
  `TestRequestSHA` (query order and JSON key order irrelevant; path and op relevant).
- `TestAudit_CoverageMatrix` (principal {human, device, agent} × op {t_read, t_write, t_create, read-scope
  POST, human-only}: agent ⇒ always; human/device ⇒ only non-read scope or human-only; rows carry status,
  `error_code`, `args_sha256`, `created_ids` from `NoteCreated`, transport, client — never body bytes),
  `TestAudit_RefusalsRecorded` (agent 403 `FORBIDDEN_SCOPE`, 429, 428, 413 ⇒ rows with those codes),
  `TestAudit_UnknownOpAgent` (agent `GET /api/nope` ⇒ row with op `-`), `TestAudit_ListKeyset`.
- `TestGate_Order` (spies: limiter → authorize → idem → handler; a 403 consumes a rate token; a 429
  creates no idempotency row), `TestGate_BodyCap` (9 MiB to an audited op ⇒ 413 `PAYLOAD_TOO_LARGE`, the
  same code Huma's `MaxBodyBytes` refusal produces; an unaudited human read with a body is streamed
  untouched), `TestGate_TransportLabels` (human with `X-MM-Client: spa` ⇒ `spa`; device with
  `?mm_client=spa` ⇒ `spa`; **agent bearer with `X-MM-Client: spa` ⇒ `rest`**; MCP ⇒ `mcp`; stdio ⇒
  `stdio`; else `rest`; User-Agent sanitised into `client`).
- `TestE2ELeakScanner` (E2E on: a test handler that echoes the canary `edge_secret` in a header, the hex
  `secret_key` in a JSON body split across two writes, and the `ntfy_token` in a `/media/` body ⇒
  `/__e2e/leaks` lists the first two, not the third, and never contains a value; E2E off ⇒ the route is
  404 and the handler is not wrapped).
- `TestApp_BuildWiresGate` (the `httpserver.Options` and `api.Deps` returned by `Build` share one
  limiter, audit store and idem store; a nil `DeviceStore` yields a nil `DeviceVerifier` interface).
Done: `make check` green; `x/time` justified in the commit message.

### Task M2a-B2b: MCP derivation, `mcp-stdio`, `servertest`, parity refactor

**Files:** `internal/mcpgen/{dispatch.go,schema.go,generic.go,stdio.go}` + tests;
`cmd/metamusic/{cmd_mcpstdio.go,cmd_mcpstdio_test.go}` (+ the `mcp-stdio` entry in `main.go`; built
through `app.Build`, `config.LoadOffline`); `internal/servertest/*` (C42; services through `app.Build`
with the rig clock; `Device()` skips until B9); `internal/mcpgen/parity_test.go` (runner refactor, C42);
`cmd/metamusic/nohelpers_test.go` (adds `servertest`; B4b adds `archivetest`);
`docs/plans/2026-10-12-m0-skeleton.md` (C5 addendum: dispatch changes of C30).
**Tests first:**
- `TestSchema_IdempotencyKeyOptionalForMCP` (create op: `idempotency_key` present, not required in the
  input schema **and** `toolSpec.Params[...].Required == false`, with the C30 description),
  `TestDispatch_DerivedIdempotencyKey` (same args ⇒ same key; argument order irrelevant; explicit key
  passed through with `derived=0`), `TestDispatch_DerivedKeyBeforeRequiredCheck` (C30 (a)),
  `TestDispatch_ClientLabel_Stateless` (stateless streamable HTTP, `User-Agent: test-client/1.0
  (x)` ⇒ audit `client = test-client/1.0`, taken from `Extra.Header`), `TestDispatch_ClientLabel_Initialized`
  (stdio in-memory session with `ClientInfo{Name: "c", Version: "2"}` ⇒ `client = c/2`),
  `TestMCP_DerivedIdempotency` (`t_create` twice without key ⇒ handler once; two distinct explicit keys
  ⇒ twice), `TestHTTPServer_MCPDispatchesThroughGate` (agent MCP call ⇒ idempotency row `derived=1`,
  audit `transport=mcp`).
- `TestMCPStdio_RequiresToken` (no `MM_TOKEN` ⇒ exit 2 "MM_TOKEN is required"; revoked token ⇒ exit 2 and
  stderr does not contain the token), `TestMCPStdio_AgentPrincipal` (in-memory transports: `whoami` ⇒
  `kind: agent`, name = token name; a human-only op is not listed and `call_operation` refuses it),
  `TestMCPStdio_RevokedMidSession` (in-memory session: `whoami` ok; revoke the token through the store;
  the next tool call ⇒ `IsError` with code `UNAUTHENTICATED`, no handler ran, the result does not contain
  the token), `TestCLI_OfflineCommandsWithoutMMAuth` (`mcp-stdio` row: `MM_AUTH` unset, in-memory
  transport, `whoami` ok; M2a-B7 adds the `restore` row), `TestCLI_MCPStdio_Smoke` (heavy: runs
  `MM_E2E_BIN` when set, else builds the binary; over `mcp.CommandTransport`: `tools/list` equals the
  HTTP `tools/list`; `whoami` ⇒ agent).
- `TestServertest_DeterministicRigs` (two rigs, same ops ⇒ identical ids and timestamps;
  `Rig.Clock.Advance` moves every store's `now`), `TestParity_Stdio` (M0 + M1 cases equal over REST, MCP
  and stdio), `TestParityEveryOperation`.
Done: `make check` green; parity green on three faces.

### Task M2a-B3 (Domain, parallel with B2a/B2b): protocol checker, batch validator mirror, lint types, Python-compatible JSON

**Files:** `internal/engine/yue2/{protocol.go,protocol_test.go,batch.go,batch_test.go}`;
`internal/engine/yue2/{lint.go,duration.go,lang.go}` (**types and signatures of C32 only**, bodies
`// STUB(M2a-B5)` returning zero values, so B4a can freeze the op contract);
`internal/export/{pyjson.go,style.go,pyjson_test.go,style_test.go}`,
`internal/export/testdata/{pyjson.inputs.json,pyjson.golden.jsonl}` (synthetic only; the golden header
records the exact CPython command used to produce it).
**Tests first:**
- `TestCheckRequest_Table` (≥ 35 rows × both modes): `title` key ⇒ ModeYuE2 `unknown_key` fatal, ModeApp
  **warning** `dropped_key:title` and no error; an unknown key `x` ⇒ the same pattern; `lang` ⇒ ModeYuE2
  allowed, ModeApp warning `dropped_key:lang`; seed 2^53 ⇒ ModeYuE2 ok, ModeApp `seed_out_of_range`;
  seed 2^63, `true`, `1.0`, `1e3`, `"7"`, −1 ⇒ `seed_out_of_range`; abc `""`/`"  \n"` ⇒ `empty_abc`; abc +
  cot off ⇒ `abc_requires_cot`; abc + abc_path ⇒ `abc_conflict`; abc_path ⇒ resolved (resolver given) /
  nil resolver ⇒ `abc_path_unresolved`; cfg 20 ok, 20.5 ⇒ `bad_cfg_scale`; matching prompt ok, other ⇒
  `prompt_mismatch`; `semantic_sampling: {}` ⇒ ModeYuE2 ok, ModeApp `sampling_unsupported`;
  `semantic_sampling: "x"` ⇒ ModeYuE2 `bad_sampling`; style ≠ tags ⇒ `style_tags_mismatch`; tags only ⇒
  ok; missing lyrics; id `..`, 181-char id ⇒ `bad_id`; invalid UTF-8 ⇒ `bad_utf8`.
- `TestCheckRequest_AgreesWithParseRequest` (C32; over the M1 `TestParseRequest` rows: errors and
  warnings identical by construction).
- `TestValidateBatch_Table`: 3 valid lines ⇒ OK, Lines 3; duplicate id on line 3 ⇒ `duplicate_id` line 3;
  unknown key ⇒ `Fatal`; blank lines skipped; CRLF endings ok; `abc_path` resolved through an
  `fstest.MapFS` / missing ⇒ `abc_path_unresolved`; `[1]` ⇒ `not_object`; `{not json` ⇒ `bad_json`;
  duplicate keys ⇒ last wins.
- `TestPyJSON_Golden` (every input × {sorted, design order `id,lyrics,style,seed,cot`} × {python, compact
  separators} × {ensure_ascii true, false} equals the CPython golden: accents, `<b>&`, U+2028, an emoji,
  `\x01`, DEL, tabs/newlines in lyrics, cfg 1.5 / 2.0 / 0.00001), `TestPyJSON_ParsesBack`.
- `TestDetectStyle` (sorted + python + raw accents ⇒ that style; design order + compact + `è` escapes ⇒
  that style; majority wins; all-ASCII lines abstain on `ensure_ascii`; no data ⇒ defaults).
Done: both packages ≥ 90 % statements (the lint stubs excluded); no new dependency.

### Task M2a-B4a: contract freeze — types, 32 registrations, stubs, `app.Build`, OpenAPI

**Files:** `internal/api/{types_m2a.go,principals.go,ops_judgments.go,ops_drafts_m2a.go,ops_playlists.go,
ops_exports.go,ops_backups.go,ops_context.go,ops_listen.go,ops_audit.go,ops_tokens.go,ops_devices.go,
api.go}` + tests (every new handler a `STUB(M2a-Bn)` answering 501, the judgment ops `STUB(M2a-B4b)`;
the moved `get_draft`/`list_drafts` keep working and already call the decorate methods);
`internal/api/ops_drafts.go` (M1: `get_draft`/`list_drafts` registrations moved to
`ops_drafts_m2a.go`; the reference gate becomes `ActsAsHuman`); `internal/api/ops_system.go` (`whoami`
`kind` enum gains `device`, rev 3); compile-ready stubs (constructors and methods returning
`errors.ErrUnsupported`, marked `// STUB(M2a-Bn)`; `drafts.Decorate`/`DecorateDetail` are no-ops
returning nil) in `internal/{judgments,drafts,playlists,exports,
backup,trackctx,listening}` with the final C31–C36 signatures (incl. `drafts.Decorate`/`DecorateDetail`,
`exports.Options`/`RatingsReader`/`NewDBSource`, `backup.Close`/`NextRun`/`SnapshotName`);
`internal/export/{source.go,build.go}` (C33 `Source`, row types, `Selection`, `Row`, `Result`; stub
`Build`); `internal/auth/{devices.go,pairing.go}` (stubs); `internal/notify/{notify.go,nop.go}`
(interface + `Nop` + `Async`); `internal/auth/middleware.go` (signature with `DeviceVerifier`,
nil-safe — rows 6/7 belong to M2a-B9; callers `internal/httpserver/server.go` and the tests that call
`auth.Middleware` directly are updated); `internal/media/serve/{types.go,chain.go,sign.go}` (`Object.DownloadName`;
`Chain(resolvers ...Resolver) Resolver`; `sign.go`: `TTLFor` uses `ActsAsHuman`, rev 3); `internal/app/app.go` (every M2a service constructed);
`internal/httpserver/{server.go,server_test.go}` (Options, mounts, `OnPaired`);
`cmd/metamusic/{main.go,cmd_serve.go,cmd_export.go,cmd_backup.go}` (dispatch entries; export/backup/restore
as stubs, `validate-batch` real since B3; `serve` starts the backup `Scheduler`, the export-sweep ticker
and calls `Services.Close` on shutdown — all against stubs until B6/B7); `openapi.json`; `web/src/api/schema.d.ts`;
`internal/mcpgen/testdata/tools_list.golden.json`; M1 plan (C18 `DownloadName`, C19/C20 notes); M0 plan
(C4 `Deps`, C6 `Options` addenda).
**Tests first:**
- `TestOps_M2aRegisteredWithMeta` (all 32 ops with the C38 method, path, scope, mcp, ui, idempotency, rate
  and secret_fields; `list_devices` under `/human/api/`; `revoke_own_device` `exclude:device-self-service`),
  `TestOps_CreateOpsDeclareIdempotencyKey`, `TestOps_NilDepsUnavailable` (extended; stubs answer 501
  `NOT_IMPLEMENTED`), `TestCLI_OpenAPIWithoutDatabase` (still no DB needed).
- `TestOps_BodyLimits` (each op's `MaxBodyBytes` as C38; all ≤ the gate's `MaxBody`).
- `TestOps_RawRequestSchema` (every `request` field is `{type: object}` in `openapi.json` and in the MCP
  input schemas; no operation in `openapi.json` has an `application/octet-stream` request body).
- `TestRefFor_Table` (viewer human/device ⇒ refs unchanged with `Principal.Key()` ids; viewer agent ⇒
  `human:alice` ⇒ `{human, human, human}`, `device:d1` ⇒ `{device, device:d1, device}`, `agent:t1`
  unchanged; `KeyFor` likewise).
- `TestApp_BuildAllM2aServices` (a full config ⇒ every `api.Deps` and `httpserver.Options` M2a field
  non-nil; the same constructor serves `serve`, `mcp-stdio` and `servertest`).
- `TestToolsList_Ceiling` (C41: ≤ 40 000 compact bytes; the measured size is written into the commit message;
  B4a also re-baselines `toolsListBaselineBytes` and regenerates `tools_list.golden.json`),
  `TestToolResultsConformToOutputSchema` (M1, `internal/mcpgen/outputschema_test.go`) covers the new tools:
  nullable response fields are declared nullable, and keys such as `_budget` / `_links` pass the schema.
- Parity cases for every stub op expect equal 501s on the three faces.
Done: `make check` green; `make openapi` committed; `schema.d.ts` regenerated; **the op contract is
frozen** (every type referenced by an op, incl. `LintReport`/`LintResult`/`LintCounts`/`Issue`,
`Export`, `PrincipalRef`, `DeviceSelf`, exists with its final JSON shape).

### Task M2a-B4b: judgments, library amendments, fixtures

**Files:** `internal/judgments/*` + tests (incl. `HumanStars`); `internal/api/ops_judgments.go` (stubs
replaced); `internal/library/*` + tests (search filters incl. `unrated`, facets, detail/summary fields,
stats, draft fields, tag inheritance in `InsertTake`, the C27 "edited drafts" amendment to fulfilment
and the mother writes, `MatchTrackIDs`, `enumOrigin`); `internal/importer/{apply.go,apply_test.go}`
(rev 3: mother `UPDATE`s skip edited drafts; `draft_edited_*` warnings from `library.Fulfilment`);
`internal/library/librarytest/*` (`Fixture` M2a additions); `internal/archivetest/*`
(`SyntheticArchive`; test-only, added to `TestNoTestHelpersInBinary`); `internal/servertest/*` (fixture
option); `make fixture-db` runs `TestWriteFixtureDB`, which calls `Fixture`, so it picks up the M2a rows
with no Makefile change (rev 3); M1 plan (C11, C16.4, C17, C19, C20 notes).
This task stays in Stage 1 because it edits `internal/library/**`.
**Tests first:**
- `TestRate_SourceFromPrincipal` (human ⇒ human row; device ⇒ human row with `principal_kind=device`;
  agent ⇒ agent row; an agent body that adds `"source":"human"` ⇒ the human row is unchanged whatever Huma
  does with the extra field), `TestRate_ClearAndVerdict` (null stars clears; verdict 281 chars ⇒ 422).
- `TestUpdateTrack_TitleAndStatus` (status ⇒ `status_set_by` = principal key; title ⇒ FTS finds the new
  title; with `WriteFiles` the master's sha256, mode 0444 and mtime are unchanged), `TestUpdateTrack_EmptyPatch422`.
- `TestSetTags_Sources` (agent add ⇒ agent row; human remove of an agent tag ⇒ removed; agent remove of a
  human tag ⇒ no change; re-add clears `removed_at`; `"  Ballad   Rock "` ⇒ `ballad rock`; 31st active
  tag ⇒ 422), `TestNormalizeTag` table, `TestInheritTags_OnInsertTake`.
- `TestAddComment_SectionLabelOnlyFromTimed` (fixture timed track: each plan section boundary
  `t = start_s` gets that section; `t` = the last section's `end_s` ⇒ last section; on a timed track the
  test inserts whose last section ends 2 s before its duration, `t` in (last `end_s`, duration] ⇒
  `section: null`; untimed track ⇒ `section: null`; inserted `manual` sections beat `plan`; `t >
  duration` ⇒ 422 `AT_SECOND_OUT_OF_RANGE`; `at_second` 12.345 ⇒ 12.3),
  `TestAddComment_Validation` (empty, 4001 chars, NUL, kind `x` ⇒ 422; default kind `note`, state `open`;
  `source` = `web`/`api`/`mcp`/`stdio` by transport), `TestCommentTransitions` (16 pairs, each through a
  human and an agent caller with identical results), `TestUpdateComment_Permissions` (a device edits the
  body of a human's comment ⇒ ok; a human edits a device's ⇒ ok; an agent edits a human's ⇒
  `NOT_AUTHOR`; a human hides an agent comment ⇒ ok, edits its body ⇒ `NOT_AUTHOR`; an agent hides its
  own ⇒ ok, another agent's ⇒ `NOT_AUTHOR`), `TestListComments_OrderFiltersCap`,
  `TestComments_AuthorMaskedForAgents` (D-M2a-19 through `list_comments` and `get_track`).
- `TestSearchTracks_JudgmentFilters` (fixture, rev 3 — track 1/2/3 = `TrackLuna`/`TrackCitta`/`TrackTwinA`: `min_stars=4` ⇒ [track 1]; `min_stars=3` ⇒ [1, 2];
  `unrated` ⇒ tracks without a human row incl. the agent-only track 3, **and** a track whose human
  rating was cleared (`stars IS NULL`); `tag=ballad` ⇒ track 3 and its inherited child `TrackTwinB`;
  `tag=ballad&tag=night` ⇒ [3]; `has_open_comments`; a discarded track hidden by default and listed with
  `status=discarded`; `sort=stars_desc` order; `min_stars`+`unrated` ⇒ 422),
  `TestAcceptance_M2a_RatingStatusTagFilters` (the same matrix through REST human and MCP agent),
  `TestMatchTrackIDs_AgreesWithSearchTracks` (every filter combination of the matrix ⇒ the same id set as
  paging `search_tracks`), `TestTrackDetail_M2aGolden`, `TestFacets_TagsAndStars`, `TestGetStats_Judgments`,
  `TestDrafts_ListAndGetM2aFields` (revision, editable, created_by, updated_by, `request.abc`/`cfg_scale`;
  `lint` present with its frozen shape — values belong to B5).
- `TestInsertTake_EditedDraftNotFulfilled` (an `import_backlog` draft edited to revision 2, then the
  original recipe's take is ingested ⇒ the draft stays `backlog` with its edited request; report warning
  `draft_edited_not_fulfilled`; an unedited sibling draft is fulfilled as before; the `core_hash`
  fallback obeys the same rule), `TestRelink_SkipsEditedDrafts` (a `varianti_import` draft whose
  `parent_track_id` the human cleared (revision 2) stays NULL after the mother arrives; warning
  `draft_edited_not_relinked`).
- `TestLibrarytest_FixtureExtendsV2` (`Fixture` = `FixtureV2` rows + the C42 M2a rows),
  `TestSyntheticArchive_ImportedOnce` (two calls in one binary ⇒ one import).
- Parity cases for the 7 judgment ops.
Done: `make check` green; `make fixture-db` writes a DB with the M2a fixture; `openapi.json` and
`schema.d.ts` unchanged since B4a; `grep -rn 'STUB(M2a-' internal` lists only Stage-2 stubs (B5–B9).

### Task M2a-B5 (Drafts): lint rules, drafts service, editor and upload ops

**Files:** `internal/engine/yue2/{lint.go,duration.go,lang.go,lang_words.go}` + tests (bodies of the
types B3 froze; no signature changes); `internal/drafts/*` + tests (incl. `Decorate`/`DecorateDetail`);
`internal/api/ops_drafts_m2a.go` (stubs replaced; `get_draft`/`list_drafts` were moved here by B4a and
already call the decorate methods).
**Tests first:**
- `TestLint_ProtocolRules` (delegates to `CheckRequest(ModeApp)`: a `title` key ⇒ warning
  `dropped_key:title`, no error, `ok: true`; `batch` duplicate ids ⇒ `batch_errors`; `ok` false iff any
  error), `TestLint_SongwritingRules` (one row per C32 code with a minimal synthetic lyric and the
  expected `line`/`section`; a clean synthetic request with a language tag ⇒ zero warnings),
  `TestLint_DuplicateRecipe` (hash of an existing recipe ⇒ `duplicate_recipe` naming `recipe`; an
  unedited imported draft is **not** a duplicate of its own source recipe, nor of itself),
  `TestLint_RunsOnCanonical` (an upload line with `abc_path` + `abc_files` lints clean on its
  `Parsed.Canonical`; the same line sent to `lint_recipe` ⇒ `abc_path_unresolved`; a line's
  `dropped_key` warnings appear once, via `Carry`).
- `TestEstimateDuration` (timed ABC 8 bars, `Q:1/4=120`, 4/4 ⇒ 16.0 `abc`; default BPM example ⇒ 105.6
  `lyrics_default_bpm`; style `120 bpm`, 4 sections × 4 lines ⇒ (32 + 4) × 2.0 = 72.0 `lyrics_style_bpm`;
  60 sections ⇒ > 360 ⇒ `duration_over_cap`).
- `TestDetectLanguage` (sentences built from each stop-word list ⇒ that code; invented syllables ⇒ "";
  a 50/50 mix ⇒ "").
- `TestDrafts_CreateUpdateRevision` (create ⇒ revision 1, origin `editor` for a human and `agent` for an
  agent; update rev 1 ⇒ 2; stale ⇒ 409 `REVISION_CONFLICT` with `current_revision: 2`; fulfilled ⇒ 409
  `DRAFT_NOT_EDITABLE`; archived ⇒ backlog; parse error ⇒ 422 `LINT_FAILED` with issues; dropped key ⇒
  warning; id change ⇒ new `group_key`; `drafts_fts` finds the new lyrics),
  `TestDrafts_EditImportedKeepsSource` (an imported draft's `source_recipe_id` and its `recipe_sources`
  rows are untouched), `TestDrafts_LintDecoration` (`list_drafts` counts and `get_draft` result equal
  `Lint` of `request_json` for every fixture draft; one `KnownHash` query per page; `create_draft` and
  `update_draft` results carry the same `lint`).
- `TestUploadBatch_AllOrNothing` (3 lines, one `cot: "none"` ⇒ 422, zero drafts),
  `TestUploadBatch_AllowPartial` (⇒ 2 created, error on line 2), `TestUploadBatch_DuplicateIDs`,
  `TestUploadBatch_ABCFiles` (`abc_path` from `abc_files` ⇒ created, lint clean, `request_json` has the
  ABC inlined; `../x.abc` ⇒ `abc_path_unresolved`), `TestUploadBatch_Collection`
  (`draft_collections.source = upload:<first id>`), `TestUploadBatch_Limits` (1001 lines ⇒ 422
  `VALIDATION_FAILED`; 51 ABC files ⇒ 422; a 1 MiB + 1 byte ABC file ⇒ 422; a 7 MiB body ⇒ 413
  `PAYLOAD_TOO_LARGE` before the handler; 4 MiB of JSONL whose JSON escaping pushes the body past 6 MiB ⇒
  413).
- `TestLintRecipe_RawTokens` (seed `9007199254740992` (2^53) and `1e3` ⇒ `seed_out_of_range` through REST
  and MCP — the `RawRequest` field keeps the exact tokens), `TestLintRecipe_BodyLimit` (2 MiB + 1 ⇒ 413).
- `TestAcceptance_M2a_VariantiDraftsEditableLintClean` (the M1 `e2e` synthetic archive via
  `archivetest.SyntheticArchive`: every
  `varianti_import` draft is listed by `list_drafts origin=varianti_import`; each has `editable: true` and
  zero lint errors; an `update_draft` changing one lyric line increments `revision`; `lint_recipe` on the
  edited request ⇒ `ok: true`; repeated through MCP with an agent token).
- Parity cases for `lint_recipe`, `create_draft`, `update_draft`, `upload_batch_jsonl` (MCP without key ⇒
  derived; a second identical MCP `create_draft` within 10 min ⇒ same draft id).
Done: no `STUB(M2a-B5)` left; package coverage ≥ 90 %.

### Task M2a-B6 (Data): export, exports service, media extensions, playlists, ID3/ZIP, CLI export

**Files:** `internal/export/{build.go,select.go,forbatch.go}` + tests (the `Source` types of B4a are
not changed); `internal/exports/*` + tests (incl. `source.go`: `DBSource`, the only `export.Source`
implementation); `internal/playlists/*` + tests; `internal/media/id3/*` + tests;
`internal/media/serve/{handler.go,handler_test.go}` (ext `jsonl`/`zip`, `Content-Disposition` from
`Object.DownloadName`; records the change in the M1 plan's C18); `internal/api/{ops_playlists.go,
ops_exports.go}`; `cmd/metamusic/{cmd_export.go,cmd_export_test.go}`.
**Tests first:**
- `TestExport_SourceByteIdentical` (fixture source of 5 lines — accents, a CRLF line, `<b>&`, an emoji —
  ⇒ output bytes equal the original file), `TestExport_SourceReportsOriginalIssues` (a 4-line source
  whose lines 1 and 3 share an id ⇒ bytes identical, `Validation` = exactly one `duplicate_id` (line 3),
  `ForBatch` ⇒ renamed and OK), `TestExport_SourceSuperseded` (a 3-line file imported, line 2 then
  modified and the file re-imported ⇒ the export carries the new line 2 and equals the modified file;
  warning `source_line_superseded` with `lines: [2]`), `TestExport_TracksRawRows` (every raw row equals a
  `recipe_sources.raw`; a `json_file`-only recipe is regenerated in the detected style; a raw line with an
  extra `title` key ⇒ regenerated + `raw_not_batch_valid`), `TestExport_ABCPathBundle` (kind
  `songs_bundle`; validation OK), `TestExport_ForBatchSiblingsUniqueIDs` (three siblings `syn-a` with
  seeds 1, 2, 3 ⇒ `syn-a-s1`, `syn-a-s2`, `syn-a-s3`; a fourth recipe `syn-a` seed 1 ⇒ `syn-a-s1-2`;
  a 180-char base is truncated; validation OK; unique-id rows stay raw), `TestExport_TracksAlwaysUnique`
  (a `tracks` selection with two seeds of one id, `for_batch` false ⇒ renamed, OK),
  `TestExport_DraftsSelection`, `TestExport_Empty` (⇒ `EXPORT_EMPTY`).
- `TestDBSource_Contract` (each `Source` method over the M2a fixture: row order, `ImportRunID`,
  inlined `ABC` for `abc_path` rows, `TrackFilter` via `MatchTrackIDs`, playlist and explicit ids
  intersected).
- `TestExports_ContentAddressed` (identical exports ⇒ same sha and path, two rows, file 0444),
  `TestExports_InlineContentThreshold` (≤ 60 000 bytes ⇒ `content` string; more ⇒ null),
  `TestExports_LinkTTLByPrincipal`, `TestExports_Sweep` (8-day-old export ⇒ file removed, `get_export`
  ⇒ 410 `EXPORT_EXPIRED`, its old signed media link ⇒ 404 `NOT_FOUND` (not 500); 6-day-old kept).
- `TestHandler_ExportExtensions` (`.jsonl` ⇒ `application/x-ndjson` + `attachment; filename="songs.jsonl"`
  from `DownloadName`; library objects send no `Content-Disposition`; `.zip` + Range ⇒ 206; unsigned ⇒
  403; `.exe` ⇒ 404).
- `TestID3_Encode` (frame order; synchsafe size for a 200-byte title; UTF-8 `Città`; POPM mapping table;
  no POPM when unrated; round trip through the test reader).
- `TestZipExport_CurrentTagsID3` (a title changed with `update_track` after the playlist was created
  appears in TIT2; TALB = playlist name; TRCK `2/3`; entry `01 - <title>.mp3`; `.m3u8` lines; POPM from
  `RatingsReader`), `TestZipExport_Deterministic` (same playlist twice ⇒ same sha; after
  `update_playlist` ⇒ different), `TestZipExport_MissingMP3`, `TestZipExport_Caps` (201 tracks with
  renditions ⇒ 413 `PAYLOAD_TOO_LARGE` with `details.limit_tracks: 200` and no `incoming/export-*`
  dir; rendition rows summing to 2 GiB + 1 ⇒ 413 with `details.limit_bytes`), `TestZipExport_EntryNamesFlat`
  (titles `../x`, `a/b`, `a\b`, `.hidden`, a control character and a playlist named `../p` ⇒ no entry
  name contains `/` or `\` or starts with `.`).
- `TestPlaylists_Revision` (stale ⇒ 409; `add_track_ids` appends; `track_ids` + `add_track_ids` ⇒ 422;
  unknown track ⇒ 422 `UNKNOWN_TRACK`; 1001 items ⇒ 422; hidden excluded unless `include_hidden`).
- `TestCLI_Export` (`--source` to stdout equals the bytes; `--for-batch`; `--out DIR` writes ABC files;
  ABC needed without `--out` ⇒ exit 2; a source file with a duplicate id ⇒ identical bytes and exit 1),
  `TestCLI_ValidateBatch`.
- `TestAcceptance_M2a_ExportByteIdentical` (heavy; the M1 `acceptance` archive via
  `archivetest.SyntheticArchive`: `export --source music/songs.jsonl` equals the original bytes minus
  exactly the one invalid line M1 skips (rev 3: through `--out DIR` and the `songs.jsonl` inside it,
  because that file's `abc_path` row needs ABC files and the CLI refuses stdout then), and the e2e
  profile's `music/songs.jsonl` (two `syn-song-01` seeds, so it too reports one `duplicate_id`) equals its
  seed file byte for byte; a `tracks` export has `generated_rows` only for recipes without a JSONL source and for the rows
  renamed by the unique-id rule — the two `syn-song-01` recipes become `syn-song-01-s1001` and
  `syn-song-01-s2001`), `TestAcceptance_M2a_ExportPassesBatchValidator` (D-M2a-20: every `tracks` and
  `drafts` export above, `--for-batch` of `music/songs.jsonl`, of the syn-song-01 lineage and of the
  syn-song-02 seed family pass `ValidateBatch` with zero issues; `--source music/songs.jsonl` without
  `--for-batch` reports exactly one issue, `duplicate_id` for `syn-song-01`, which is what
  `ValidateBatch` reports on the original file minus its unrecorded `{not json` line; the e2e recipe
  JSONL's `--source` export reports exactly the issues of its seed file).
- Parity cases for the 5 playlist ops and 3 export ops (same sha, same link at the fixed clock).
Done: no `STUB(M2a-B6)` left.

### Task M2a-B7 (Data): backup, restore, retention, scheduler

**Files:** `internal/backup/*` + tests; `internal/api/ops_backups.go`; `cmd/metamusic/{cmd_backup.go,
cmd_backup_test.go}` (`backup`, `backup list`, `restore`; `config.LoadOffline`). `serve`'s scheduler
start and shutdown `Close` are already wired by B4a through `app.Build`.
**Tests first:**
- `TestRetain_Table` (30 daily snapshots ⇒ 14 days + up to 8 weeks kept; two snapshots on one day ⇒ the
  newer kept; `-2` suffixed names parse and order after their base; the newest always kept;
  non-snapshot directories never touched; a `.partial` older than 24 h dropped, a fresh one kept),
  `TestNextRun_Table` (C34 rows: 02:00 start, both DST change days, `at` inside the spring gap),
  `TestScheduler_LoopSynctest` (C34; the only synctest in this task).
- `TestBackup_ManifestComplete` (`WriteFiles` fixture: manifest lists every master, artifact and rendition
  with matching sha256, plus `import/mapping.csv` and the `import/reports/*.json` files (copied, kind
  `import`, `linked: false`); DB integrity `ok`; `songs.jsonl` present and `ValidateBatch` OK; no
  absolute paths, no `secrets`, `incoming`, `inbox`, other `import/` files or `derived/exports`
  entries),
  `TestBackup_HardlinksSameInode` (`os.SameFile`; `link.linked` = file count).
- `TestBackup_EXDEVFallbackCopiesAndVerifies` (injected `Link` returns `EXDEV` ⇒ every file copied,
  sha256 verified, 0444, `link.fallback = "EXDEV"`), `TestBackup_CopyCorruptionDetected` (a copy hook flips
  one byte ⇒ run `failed`, partial dir removed, fake notifier got priority 4 without secrets).
- `TestBackup_SnapshotSurvivesLiveMutation` (after the snapshot: add a comment, retitle a track, rate it,
  and remove a live master file via a test hook ⇒ the snapshot DB still has the old values and the
  snapshot master still hashes to its manifest sha).
- `TestBackup_InProgressRefused` (second `Start` ⇒ `BACKUP_IN_PROGRESS`), `TestBackup_CrossProcessGuard`
  (two `backup.Service`s on one DB — as `serve` and CLI `backup` would be — the second `RunSync` while the
  first runs ⇒ `BACKUP_IN_PROGRESS` from the unique index, not from a mutex), `TestBackup_StaleRunMarkedFailed`,
  `TestBackup_SameSecondNamesDistinct` (fixed clock: a manual and a scheduled run ⇒ `<name>` and
  `<name>-2`, both complete), `TestBackup_CloseWaits` (`Close` returns only after the in-flight run
  finished; a `Start` after `Close` ⇒ 503 `UNAVAILABLE`), `TestBackup_ExcludesExportsAndProbe`,
  `TestRestore_ImportFilesWritable` (restored `import/mapping.csv` is 0600 and equals the snapshot copy).
- `TestRestore_RefusesNonEmptyTarget` (exit 2; target untouched), `TestRestore_DetectsTamper` (one flipped
  byte in a snapshot master ⇒ exit 1 naming the path; nothing written), `TestRestore_MissingFile`,
  `TestRestore_NewerSchemaRefused`.
- `TestAcceptance_M2a_BackupRestoreEmptyDir` (servertest rig with files and M2a fixture: `create_backup`
  ⇒ 202, poll `list_backups` until `completed`; mutate the live library (comment, retitle, rating); run
  `restore --from <snapshot> --into <empty temp dir>`; a new rig on the restored dir answers `get_track`,
  `list_comments`, `search_tracks min_stars` and `get_playlist` with the **pre-mutation** responses
  recorded before the mutation; every master's sha256 equals its row; an MP3 media link answers 206 for a
  Range request; the snapshot's run row is `completed`; `get_audit` shows `cli:restore`).
- `TestCLI_BackupList`; `TestCLI_OfflineCommandsWithoutMMAuth` gains its `restore` row (`MM_AUTH`
  unset ⇒ restore succeeds).
- Parity cases for `create_backup` (equal 202 bodies; each rig's cleanup `Close` waits for its run) and
  `list_backups`.
Done: no `STUB(M2a-B7)` left; `internal/backup` ≥ 90 %.

### Task M2a-B8 (Agent): context bundle, listen events, audit op

**Files:** `internal/trackctx/*` + tests, `internal/trackctx/testdata/context.golden.json`;
`internal/listening/*` + tests; `internal/api/{ops_context.go,ops_listen.go,ops_audit.go}`.
**Tests first:**
- `TestContext_Golden` (fixture timed track, agent principal: shape and key order as C35; links expire
  15 min after the fixed clock; human principal ⇒ 12 h), `TestContext_NextActions` (table over the five
  rules), `TestContext_Include` (`score` adds `score.abc`; `lyrics` is never trimmed; `comments` lists up
  to 500; `include=foo` ⇒ 422), `TestContext_NeverInventsListening` (no events ⇒ `plays: 0`,
  `completion_rate: null`), `TestContext_StarsSplit` (`stars` = human, `critic_stars` = agent).
- `TestFit_WorstCase` (12 000-char lyrics, 300 comments of 4 000 chars full of `<`, `>` and `&`, 50
  children ⇒ ≤ 24 000 UTF-16 units as `Measure` counts them (HTML escapes included) with `truncated` =
  [comments, children, lyrics] in that order; with `include=lyrics` ⇒ ≤ 96 000 and lyrics intact),
  `TestFit_BudgetFixedPoint` (a bundle whose size crosses 9 999 → 10 000 when the digits of `chars` are
  written ⇒ `_budget.chars` equals the final measured length), `TestMeasure_MatchesRESTBody` (`Measure`
  equals the UTF-16 length of the REST body minus its trailing newline), `TestAcceptance_M2a_ContextBudget`
  (that worst-case track inserted into a rig: the REST body and the MCP tool result's `TextContent` text
  are ≤ 24 000 UTF-16 units and `_budget.chars` equals each measured length; `StructuredContent` is not
  measured).
- `TestListen_ValidationAndDedup` (a batch with a bad kind, an unknown track and a bad range ⇒ those
  rejected, others accepted; re-posting ⇒ all duplicates; 201 events ⇒ 422 `VALIDATION_FAILED`; a
  300 KiB body ⇒ 413 `PAYLOAD_TOO_LARGE`),
  `TestListen_Stats` (human/device events only; `completion_rate` capped at 1),
  `TestListen_BeaconQueryFlag` (`?mm_client=spa` accepted and counted by the served-operation recorder;
  `mm_client` appears nowhere in `openapi.json` or the MCP schemas).
- `TestGetAudit_FiltersAndCursor` (each filter; `exclude_op`; newest first; cursor without dup/miss;
  90 000-byte page cap), `TestGetAudit_AgentHumanKeyFilter422` (agent with `principal_id=human:…` ⇒ 422;
  with `principal_kind=human` ⇒ rows whose `principal` renders as `{human, human, human}`).
- Parity cases for `get_track_context`, `log_listen_events`, `get_audit` (masks `transport`, `client`).
Done: no `STUB(M2a-B8)` left.

### Task M2a-B9 (Agent): devices, pairing pages, step-up, token and device ops, notifications

**Files:** `internal/auth/{devices.go,pairing.go,pairpage.go,pair.js,middleware.go,classify.go,local.go}`
+ tests; `internal/notify/ntfy.go` + tests; `internal/api/{ops_tokens.go,ops_devices.go}`;
`internal/servertest/{device.go,stepup.go}` (`Rig.Device()` and local-mode step-up in `Rig.Human()`,
C42); `docs/plans/2026-10-12-m0-skeleton.md` (C3 addendum: rows 6–7, classify, step-up). All
time-dependent tests here advance an injected clock (C42), never synctest.
**Tests first:**
- `TestPairing_CodeLifecycle` (code matches `^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`; redeem with
  lowercase, without dash and with `I`/`O` substitutions ⇒ cookie `^mmd_[a-z2-7]{52}$`, device scopes
  `read,write`; fake clock +5 min 01 s ⇒ `ErrInvalidCode`; minting a second code revokes the first, which
  is then invalid; a 600-byte `User-Agent` is stored truncated to 512 bytes at a UTF-8 boundary, no
  error), `TestPairing_SingleUse`.
- `TestRateLimit_PairEndpoint` (`GET /pair` has the C26 headers and never echoes a code; wrong code ⇒
  401 page after `FailDelay`; the 11th POST in a minute ⇒ 429; cross-site POST ⇒ 403 `CROSS_ORIGIN`;
  success ⇒ cookie attributes as C26 and 303 `/`; `OnPaired` called once).
- `TestStart_Chooser` (valid device cookie ⇒ 302 `/`; `mm_start=sso` ⇒ 302 `/` in proxy, `/auth/login` in
  local; none ⇒ chooser; `POST /start` sets `mm_start`; a cross-site `POST /start` ⇒ 403 `CROSS_ORIGIN`;
  `?expired=1` clears it).
- `TestDevice_VerifySlidesExpiry` (fake clock: `last_seen_at` written at most once a minute; expiry slides
  at most hourly; 90 idle days ⇒ invalid), `TestDevice_SurvivesThirteenHourGap` (verify at t0; at
  t0 + 13 h the same cookie is still valid; the response re-issues `Set-Cookie` only because the
  server-side expiry slid (> 1 h since the last slide), and a second request a minute later re-issues
  nothing), `TestDevice_ScopesReadWriteOnly` (device `whoami` ⇒ scopes `[read, write]`; a
  generate-scope test op ⇒ 403 `FORBIDDEN_SCOPE`), `TestDevice_NilReceiverSafe` (`(*DeviceStore)(nil)
  .VerifyDevice` ⇒ `ErrInvalidDevice`), `TestHTTPServer_NilDevicesNoPanic`.
- `TestTrustMatrix_DeviceCookie` (generated matrix: path {`/`, `/api/tracks`, `/human/tokens`,
  `/human/api/tokens`, `/mcp`, `/media/x`, `/start`} × cookie {absent, valid, revoked, expired, garbage} ×
  mode {proxy, local} × spoofed `Remote-User`/`X-MM-Edge` {absent, present} ⇒ expected kind per C26;
  rejected cookies are cleared; HTML ⇒ 303 `/start?expired=1`; API ⇒ 401; **a valid cookie under
  `/human/tokens` and `/human/api/tokens` is ignored and never cleared** (no `Set-Cookie: mm_dev=`
  in either mode, and the same cookie still authenticates `/` afterwards); a revoked or garbage cookie
  under `/human/` is not read and therefore not cleared either; on `/mcp` and `/media/x` no cookie is
  ever read or cleared).
- `TestHumanOnly_RefusedForAgentsDevicesStdio` (bearer on `/human/api/tokens` ⇒ 403 `HUMAN_ONLY`; device
  cookie there ⇒ 401; stdio `call_operation` ⇒ refused; MCP `tools/list` lacks the 5 human-only ops and
  `revoke_own_device`),
  `TestHumanOnly_NotFoundOffHumanPrefix` (`POST /api/tokens` ⇒ 404/405 problem).
- `TestLocalStepUp_HumanPaths` (local mode: fresh login ⇒ `/human/api/tokens` 403 `STEP_UP_REQUIRED`;
  `POST /auth/step-up` with the password ⇒ 303 `next`, then 200; rig clock +10 min 01 s ⇒ 403 again; wrong
  password ⇒ 401 page after `FailDelay`; `next=//evil.example` ⇒ `/human/tokens`; proxy mode
  `/auth/step-up` ⇒ 404).
- `TestTokenOps` (`create_token` ⇒ plaintext matching `^mmp_[a-z2-7]{52}$` once, replay ⇒ `token: null`;
  `list_tokens` has no hash fields; `revoke_token` ⇒ that token gets 401; `revoke_own_token` by an agent
  ⇒ revoked, by a human ⇒ 403 `NOT_A_TOKEN`; `revoke_token`/`revoke_own_token` return `TokenInfo` via
  `TokenStore.Get`; ntfy fake gets "Token created" without the plaintext).
- `TestDeviceOps` (`create_pairing_code` replay ⇒ `code: null`, `pair_url: null`; `list_devices` (human
  only) lists `user_agent` and `paired_by`; a device or agent calling `/human/api/devices` ⇒ 401/403;
  `revoke_device` ⇒ the cookie is rejected next request (HTML ⇒ 303 `/start?expired=1`);
  `revoke_own_device` by a device ⇒ `DeviceSelf` + cookie cleared, by a human or an agent bearer ⇒ 403
  `NOT_A_DEVICE`).
- `TestNotify_Ntfy` (fake server receives one JSON `POST` to the server base with `topic`, `title`,
  `message`, `priority`, `tags`, `Authorization: Bearer <canary>` and no query string),
  `TestNotify_ControlCharsInNames` (a device named `a\r\nX-Evil: 1\u0085b` ⇒ delivered, sanitised, no extra
  header), `TestNotify_NoSecretsInMessages`, `TestNotify_ErrorsRedacted`.
- Parity cases for `list_tokens` and `revoke_own_token` (last, dedicated token). `list_devices` and
  `revoke_own_device` are not MCP-exposed and have no parity case.
Done: no `STUB(M2a-B9)` left; `internal/auth` ≥ 95 %.

### Task M2a-W1 (Web): routes, ids, judging UI, comments, jumps, library filters

**Files:** `web/src/App.svelte` (**every M2a route of C40 registered**, each page a later W task owns
as a placeholder component under `web/src/routes/`; `/human/*` excluded from client-side routing);
`web/src/lib/id.ts` + test (`uuidv7()`); `web/src/lib/time.ts` + test (M1, amended: `parseT` decimals;
records the change in the M1 plan's C22); `web/src/lib/{judge.svelte.ts,jump.ts,comments.ts}` + tests;
`web/src/api/idem.ts` + test; `web/src/api/client.ts` + `session.ts` (M1, amended; rev 3: the response
middleware lives in `client.ts`: 429 ⇒ typed `RateLimited` error with `retryAfterS`; `session.ts` keeps
session-loss detection and `IDEMPOTENT = GET/HEAD/PUT`, so `rate_track` (PUT) is retried once after a
network error while creates are not);
`web/src/lib/components/{StarRating,StatusSelect,TagEditor,CommentsPanel,CommentItem,CommentComposer}.svelte`;
`web/src/lib/components/{PlayerBar,Waveform,TrackTable,LibraryFilters}.svelte` (M1 files, amended);
`web/src/routes/{Track,Library}.svelte`; `web/src/lib/libraryQuery.ts` + test (M1, amended);
`web/src/lib/router.ts` + `router.test.ts` (M1, amended: `SERVER_PREFIXES` gains `/human`, `/start`, `/pair`).
Calls: `rate_track`, `update_track`, `set_tags`, `list_tags` (TagEditor suggestions), `add_comment`,
`update_comment`, `list_comments`, and the M1 reads with the new filters.
**Tests first (`bun test`):** `uuidv7` (matches
`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`; 1 000 ids generated within one
mocked millisecond are strictly increasing; works with `crypto.randomUUID` deleted from the global);
`parseT` (`72` → 72, `1:12` → 72, `72.0` → 72, `1:12.5` → 72.5, `bad` → null); judge store (optimistic
update; rollback and error toast on 500; **429 ⇒ rollback, toast naming the `Retry-After` seconds, no
automatic retry**; critic row is read-only); `jumpTo` (same track paused ⇒ `seek(t)` + `resume()`; same
track playing ⇒ seek only; other track ⇒ navigate `/tracks/<id>?t=<t>` + `play(id, undefined, {startAt: t})`; t clamped to
[0, duration]); `comments.ts` (`formatAt(72.04)` ⇒ `1:12`; `pinPositions` width 300, duration 120, t 60 ⇒
x 150; `time` order puts untimed comments last; `sectionChip` ⇒ `chorus 1 · ~70%`, null section ⇒ no
chip); `libraryQuery` round trip with `min_stars`, `unrated`, repeated `status` and `tag`;
`newIdempotencyKey` is a `uuidv7`, stable across retries of one action and fresh per action;
`TagEditor` suggestions come from one `list_tags` call (fake client); `StarRating` keyboard (1–5 set, 0
clears); the route table resolves every C40 route (placeholders included).
Done: lint + svelte-check clean; no `{@html}`; the M0/M1 `<audio>` identity checks still pass.

### Task M2a-W2 (Web): draft editor, upload, lint panel

**Files:** `web/src/routes/{Backlog,Draft,DraftNew,DraftUpload}.svelte` (placeholders replaced);
`web/src/lib/components/{DraftEditor,LintPanel,UploadForm}.svelte`; `web/src/lib/draftForm.ts` + test.
Calls: `lint_recipe`, `create_draft`, `update_draft`, `upload_batch_jsonl`, `list_drafts`, `get_draft`.
**Tests first:** `draftForm` (form ⇄ request round trip incl. optional abc/cfg_scale; preview has sorted
keys; dirty detection); lint debounce (fake timers: three edits within 400 ms ⇒ one call); revision
conflict flow with a fake client (409 ⇒ dialog ⇒ "keep mine" calls `create_draft` with a new key);
`LintPanel` maps an issue with `line` to the textarea line and shows `dropped_key:<k>` as a warning.
Done: a fulfilled draft renders read-only; upload shows per-line results.

### Task M2a-W3 (Web): playlists, export, backups, audit, context

**Files:** `web/src/routes/{Playlists,Playlist,Export,SettingsBackups,SettingsAudit,Settings}.svelte`
(placeholders replaced);
`web/src/lib/components/{AddToPlaylist,PlaylistItems,ExportForm,BackupList,AuditTable,ContextButton}.svelte`;
`web/src/lib/exportView.ts` + test.
Calls: `list_playlists`, `get_playlist`, `create_playlist`, `update_playlist`, `export_playlist_zip`,
`list_export_sources`, `export_jsonl`, `get_export`, `create_backup`, `list_backups`, `get_audit`,
`get_track_context`, `revoke_own_device`.
**Tests first:** `exportView` (selection ⇄ request body; summary strings: "12 rows · 11 raw · 1
regenerated"; a `source` result with issues is labelled "issues of the original file", not "failed";
`needsRefresh(expiresAt, now)` true within 1 min of expiry; `?export=<id>` ⇒ one `get_export` call);
move-up/down pure helper; audit filters ⇄ query with `exclude_op=log_listen_events` by default;
`ContextButton` displays the UTF-16 length of the copied text (also when the clipboard write is
refused); unpair calls `revoke_own_device` then `location.assign("/start")`.
Done: downloads are anchors to signed links; nothing fetches a signed URL through `fetch`.

### Task M2a-W4 (Web): `/human/` pages, listen recorder, outbox, step-up, navigation

**Files:** `web/src/routes/{HumanTokens,HumanDevices}.svelte` (placeholders replaced);
`web/src/lib/components/{TokenForm,PairingPanel}.svelte`; `web/src/api/{outbox.ts,outbox.test.ts,client.ts}`;
`web/src/lib/{listen.svelte.ts,listen.test.ts}`; `web/src/lib/{nav.ts,nav.test.ts}` (full-page
navigation for `/human/*`); `web/src/App.svelte` (nav links only — routes are W1's);
`web/public/manifest.webmanifest` (`start_url: "/start"`); `web/package.json` + `bun.lock` (`uqr`);
`web/eslint.config.js` (`indexedDB` restriction); this plan's C40 (record the QR library version).
Calls: `list_tokens`, `create_token`, `revoke_token`, `list_devices`, `create_pairing_code`,
`revoke_device`, `log_listen_events`.
**Tests first:** outbox with an in-memory store (append; flush at 10 events; delete accepted, duplicate
and rejected ids; keep on network error; **429 ⇒ keep every event, next flush not before `Retry-After`
(30 s when absent)**; cap 5000 drops oldest and counts; `pagehide` beacons pending events and marks
them, a later flush re-sends and then deletes them; `sendBeacon` throwing ⇒ keepalive fetch; event ids
and `session_id` are `uuidv7`); listen recorder with fake player events (play 0 → pause at 30 s ⇒
`start` + `segment [0, 30000]`; seek 10 s → 50 s ⇒ closed segment + `seek`; `ended` ⇒ `complete`; next
at 40 % ⇒ `skip`; hidden page closes the segment); `client.ts` 403 `STEP_UP_REQUIRED` ⇒
`location.assign("/auth/step-up?next=/human/tokens")`; `nav.test.ts` (clicking the Tokens or Devices
link calls `location.assign("/human/…")` and never the client router; `navigate("/human/devices")`
does the same); `TokenForm` shows the token once with its own copy button, its MCP templates contain
`${MM_TOKEN}` and never the token, and it clears the token on navigation; `PairingPanel` countdown
reaches 0:00 and disables the code.
Done: lint (fetch/beacon/indexedDB bans) + svelte-check clean.

### Task M2a-W5 (Stage 3, Web): Playwright M2a specs, leak check, coverage

**Files:** `web/e2e/{m2a-judge.spec.ts,m2a-comments.spec.ts,m2a-drafts.spec.ts,m2a-playlists.spec.ts,
m2a-export.spec.ts,m2a-backup.spec.ts,m2a-audit.spec.ts,m2a-human.spec.ts,m2a-listen.spec.ts,
m2a-context.spec.ts,zip.ts}`, `web/e2e/global-setup.ts` (canary `secret_key`,
`edge_secret` and `ntfy_token` files for the run; server env `MM_RATE_SCALE=100` and
`MM_BACKUP_AT=off`; exports `MM_E2E_SEED_DIR=<dataDir>/inbox-synthetic` so specs can read the seed files;
rev 3: the closure it already returns for teardown gains the `GET /__e2e/leaks` check — it must be `[]` —
next to the served-operation check; there is no separate teardown file), `web/playwright.config.ts`
(existing projects `chrome`, `chrome-pixel7`, `chrome-iphone15`, `firefox`; per C41: the two mobile
projects only for judge, comments, human and listen; Firefox only for the player/seek/jump tests and
listen; everything else desktop Chrome; no HAR).
Specs:
- judge: rate 4 from the player bar; set `to_relisten`; add/remove tags (suggestions from `list_tags`);
  library filters `min_stars=4`, `status`, two tags ⇒ the request URL carries `tag=a&tag=b` and the rows
  are their AND; back/forward restores filters.
- comments: general comment; "comment at current time" while playing ⇒ `at_second` ≈ `currentTime`;
  **clicking a timestamped comment seeks the player** (same track paused, same track playing, another
  track playing ⇒ `currentTime` within ±0.5 s of `at_second` within 3 s; the link carries `?t=<s>.<d>`);
  waveform pin seeks; timed track shows a section chip, untimed track none; resolve, reopen, hide.
- drafts: **new draft on `/backlog/new`** (`create_draft`) ⇒ lands on its editor; varianti drafts listed
  (origin filter); edit a lyric line ⇒ save ⇒ revision +1; lint shows zero errors; typing
  `(repeat chorus)` shows `non_sung_text`; upload of 3 synthetic lines into a new collection ⇒ 3 drafts;
  an invalid line ⇒ per-line error and nothing created.
- playlists: create, add two tracks from track pages, reorder, play all, export ZIP ⇒ download ⇒
  `zip.ts` reads the stored entries ⇒ 2 MP3s whose ID3 TIT2 equals the current titles.
- export: export the seed `music/songs.jsonl` source ⇒ the downloaded bytes equal the seed inbox file byte for
  byte; the validator card shows **exactly the issues of the seed file** (one `duplicate_id`: the e2e
  profile's two siblings share a request id) labelled as the original file's; the same source with
  `for_batch` ⇒ renamed ids and the card shows OK; a `tracks` export ⇒ OK; reloading
  `/export?export=<id>` shows the same result through `get_export`.
- backup: "Back up now" ⇒ appears as completed in the list.
- audit: the human mutations made in this spec are listed; filters work.
- human: the Tokens nav link is a full-page navigation (a document request for `/human/tokens` is
  observed); `/human/tokens` create ⇒ token shown once ⇒ the token authenticates `/api/me` as an agent ⇒
  revoke ⇒ 401; `/human/devices` create code ⇒ a **second browser context created with
  `browser.newContext({ extraHTTPHeaders: {}, baseURL })`** (contexts inherit the project's `use`
  options, including the proxy headers, unless overridden) opens `/pair#<code>` ⇒ redeems ⇒ `/` loads
  and the spec first asserts `whoami.kind == "device"`; rating works; `/human/tokens` is refused;
  "Unpair this browser" ⇒ the SPA calls `revoke_own_device` and `location.assign("/start")` ⇒ the
  `/start` chooser is shown. A third context (same creation rule) pairs with a new code; the desktop
  context **revokes it from `/human/devices`** (`revoke_device`) ⇒ the third context's next navigation
  lands on the `/start?expired=1` chooser.
- listen: play 3 s, navigate, pause ⇒ `get_track_context` shows plays ≥ 1; offline (`context.setOffline`)
  while playing ⇒ events stay in IndexedDB ⇒ back online ⇒ flushed; closing the page sends a beacon
  (`/api/listen-events?mm_client=spa` observed) and the next page load leaves no duplicates.
- context: "Copy agent context" ⇒ the **displayed** char count is ≤ 24000 (the clipboard is not read:
  Firefox grants Playwright no clipboard permission).
- Teardown: served-operation coverage lists zero unserved ui ops (all 31 M2a ui ops + M0/M1, incl.
  `create_draft`, `get_export`, `list_tags`, `revoke_device`, `revoke_own_device`); `/__e2e/leaks` is
  `[]`.
Done: `make e2e` green on chrome + firefox; CI e2e ≤ 5 min (X1 records the measured time).

### Task M2a-I1 (Infra): routing contract — `mm-pair`, `mm-device`

**Files:** `deploy/labels.example.yml` (C41 routers, `mm.example.com`), `deploy/traefik-contract/contract_test.go`
(+ fixtures), the M1 plan's C24 (two new rows).
**Tests first (`TestRoutingContract`, new rows):** `/start`, `/pair`, `/pair.js`, `/auth/pair` ⇒ `mm-pair`,
never reach the stub forward-auth, spoofed `Remote-*`/`X-MM-Edge` arrive empty; `Cookie: mm_dev=x` on `/`
and `/api/tracks` ⇒ `mm-device` (no forward-auth, identity headers stripped, compressed);
`Cookie: a=1; mm_dev=x` ⇒ `mm-device`; `Cookie: xmm_dev=1` ⇒ `mm-ui`; `mm_dev` on `/human/tokens` and
`/auth/step-up` ⇒ `mm-ui` (stub called; one factor rejects `/human/`); `mm_dev` on `/media/x` ⇒ `mm-media`,
on `/mcp` ⇒ `mm-agent`; middleware order parsed from the labels: `mm-strip-identity` first on both new
routers; `mm-compress` on `mm-device` only.
Done: `make routing` green locally; CI `routing` job green.

### Task M2a-I2 (Infra): compose, docs, checklist, coverage, CI, ignore rules

**Files:** `Dockerfile` and `deploy/compose.example.yml` (rev 3: M1's image sets `MM_DATA_DIR=/data` with
`VOLUME /data`, so the default backup dir `/backup` would sit outside the volume and be unwritable for
uid 1000 on a read-only root; both move to one parent volume `/state` holding `data/` and `backup/` —
`MM_DATA_DIR=/state/data`, `MM_BACKUP_DIR=/state/backup`, the M1 read-only inbox bind follows to
`/state/data/inbox`, and the README configuration table and M1 docs are updated; `MM_BACKUP_AT`, optional
`MM_NTFY_URL`; `ntfy_token` documented as an optional secret file), `docs/architecture.md` (M2a section, public-safe), `README.md` (connecting Claude Code over HTTP
with a token from `/human/tokens`; `mcp-stdio` with `MM_TOKEN`; export and backup/restore quick start),
`docs/runbook.md` (new file; restore drill; what snapshots do and do not protect, C34), `docs/device-checklist.md`
(DC-M2a items), `coverage.thresholds`, `.github/workflows/ci.yml` (e2e job: the leak check runs in the
Playwright teardown, no HAR recording or upload), `.gitignore` (root-anchored `/backup/` — never an
unanchored `backup/`, which would hide `internal/backup/`), `scripts/public-gate.sh` (rev 3: a root-anchored
`backup/*` case in `forbidden_path`, so a default local `BackupDir` can never be committed; hooks and CI
run the same script) and `scripts/public-gate-selftest.sh` (cases: staged `backup/x` blocked, staged
`internal/backup/x.go` allowed, `git check-ignore backup/x` ignored and `internal/backup/x.go` not —
shell cases, not a Go test).
Device checklist M2a:
- DC-M2a-1 iPhone, installed PWA (standalone): launch ⇒ `/start` chooser ⇒ pair with a code typed from
  `/human/devices` on the desktop ⇒ library, playback, rating and comments work without SSO.
- DC-M2a-2 the paired device survives a 13 h gap: open the app after ≥ 13 h ⇒ still signed in, no SSO
  prompt, playback starts.
- DC-M2a-3 Android: scan the QR in the camera ⇒ Chrome pairs ⇒ the installed PWA is paired too (shared
  jar) — record the result.
- DC-M2a-4 while playing on the phone, "comment here" then tap the timestamp ⇒ the player jumps.
- DC-M2a-5 airplane mode while playing ⇒ reconnect ⇒ the listen events appear in the track context.
- DC-M2a-6 playlist ZIP downloads on the phone; the MP3s show their current titles in the Files app.
- DC-M2a-7 revoking the device on the desktop ⇒ the phone shows the `/start` chooser on its next request.
Done: `docker build` green; inside the container, `metamusic backup` against a synthetic data dir whose
backup dir is on the same volume reports `linked > 0, copied = 0`; coverage selftest passes.

### Task M2a-B10 (Stage 3, Backend): cross-cutting acceptance and `TestNoSecretLeak`

**Files:** `internal/acceptance/{doc.go,main_test.go,m2a_test.go,nosecretleak_test.go,every_ui_action_test.go,
names_test.go}` (`TestMain` imports the synthetic `acceptance` archive once for the whole binary through
`archivetest.SyntheticArchive`; tests that mutate use `Template.Copy`). Every test here is heavy
(skipped under `-short`).
**Tests first:**
- `TestAcceptance_M2a_AgentPerformsEveryUIAction`: a go-sdk MCP client with a read+write PAT over
  streamable HTTP. The script is derived from the registry: for every op with `human_only == false`, an
  `mcp` value that is not `exclude:*` and a `ui` route starting with `/` (M0, M1 and M2a), a scripted call
  must exist (the test fails naming any op without one); each call succeeds (first-class tool or
  `call_operation`; create ops without explicit keys); finally `get_audit principal_id=agent:<id>` lists
  every call in order with transport `mcp`. `revoke_own_device` (`exclude:device-self-service`, D-M2a-22)
  is outside the derived set by construction; there is no hand-written exemption list.
- `TestAcceptance_M2a_RatingIsolation`: the human rates track T 5; an agent rates T 1 through REST bearer,
  the MCP tool, `call_operation` and stdio; after each: `get_track.rating.human.stars == 5`,
  `search_tracks min_stars=5` contains T, facets `stars` unchanged, `get_track_context.track.stars == 5`,
  `critic_stars == 1`, a playlist ZIP's POPM for T maps 5 stars; the agent clearing its rating leaves the
  human row untouched.
- `TestAcceptance_M2a_TimestampCommentLink`: a comment at 72.0 s on a timed track ⇒ `link ==
  "/tracks/<id>?t=72.0"`, `url` absolute with `PublicURL`, section label from the plan; the same over MCP.
- `TestMCP_DerivedIdempotency_AddComment`: the same `add_comment` called twice over MCP without a key ⇒
  one comment and identical results; with two distinct `idempotency_key`s ⇒ two comments; after
  `Rig.Clock.Advance(10 min + 1 s)` the derived key no longer replays (rig clock, not synctest — the MCP
  client talks over a real loopback socket, C42).
- `TestAgentsNeverSeeHumanNames` (rig with `Canary`: the human's username and a device name are
  canaries; every response an agent receives over REST, MCP and stdio — comments, ratings, tracks,
  drafts, playlists, exports, audit, context — contains neither; `principal.id`/`author.id` of human rows
  read `human`).
- `TestNoSecretLeak` (rig with `Canary`, plus a local-mode rig for step-up and an HTTP MCP and a stdio
  session): canaries = `secret_key` (raw, hex, base64 std/url), `edge_secret`, `ntfy_token`, every PAT
  plaintext created (REST, CLI, stdio `MM_TOKEN`), every pairing code (both forms) and device cookie.
  (The E2E leak scanner covers only the static ones in the browser run; this test is the authority for
  the dynamic ones.)
  Exercise every op through REST (human, device, agent), MCP and stdio, the pairing and step-up flows,
  the CLI (`token create/list/revoke`, `export`, `backup`, `restore`, `validate-batch`), and a failing
  ntfy server. Scan: every recorded response (status line, headers, body) and MCP result; `openapi.json`
  and `/api/docs`; DB dumps of `audit_log`, `idempotency`, `exports`, `listen_events`, `backup_runs`,
  `comments`, `devices`, `pairing_codes`; the backup manifest and snapshot listing; captured slog output
  of serve and stdio; ntfy request titles and bodies; CLI stdout/stderr. **Allowed hits only:** a PAT
  in its `create_token` response or `token create` stdout; a pairing code in its `create_pairing_code`
  response; a device cookie in the `/auth/pair` `Set-Cookie` and in sliding re-issue `Set-Cookie` headers
  of responses to requests that presented that same cookie. Failures name the canary kind and location,
  never the value.
- `TestParityEveryOperation` green with zero stubs.
Done: `go test ./...` (no `-short`) green with media tools; coverage gates met.

### Task M2a-X1 (Integration)

Merge lane branches into `m2a`; `make tools hooks check test e2e gate cover routing` green;
`grep -rn 'STUB(M2a-' internal` empty; regenerate `openapi.json`, `schema.d.ts`, `tools_list.golden.json`;
update `metrics/REPORT.md` (`make metrics` regenerates the table; the integration-notes section is
hand-written, as in M1; measured `make check` warm wall time against < 30 s (M1: 37 s), e2e wall time against
≤ 5 min, CI wall time, coverage, tools/list bytes against the 40 000-byte ceiling); review (spec +
quality + security: rating isolation, idempotency ordering and leases, secret fields, audit coverage,
transport labels, device cookie scope and router, pairing brute force, step-up, `/human/` full-page
navigation, agent-visible names, backup path confinement and cross-process guard, restore
verification, export raw-vs-regenerated and unique-id rules, no query logging, `TestNoDeleteSQL`); copy
D-M2a-1 … D-M2a-22 into the private design document (§3.6, §4.1, §4.2, §4.4, §5.3, §7 amendments);
write the public copy of this plan and run `make gate` on it.

### Task M2a-X2 (Deploy, operator-assisted; host details and results only in private notes)

1. **Prerequisites (operator):** the ntfy publisher token saved as the secret file `ntfy_token` (shell
   tools only, never echoed); `MM_NTFY_URL` set; the data and backup directories live under one bind
   mount (design §3.5) with `MM_BACKUP_DIR` pointing at the backup side.
2. **Deploy only via `deploy.sh`** from a clean detached worktree of `origin/main` (as in M1-X2). The
   new routers (`mm-pair`, `mm-device`) are labels on the metamusic service, so only that container is
   recreated; no forward-auth restart is needed (`^/human/` is already two-factor).
3. **Smoke tests:** `/start`, `/pair` and `/pair.js` answer 200 without SSO; `/auth/pair` is rate
   limited; a spoofed `Remote-User` + `X-MM-Edge` with a garbage `mm_dev` cookie ⇒ 401/303 `/start`;
   `mm_dev` on `/human/tokens` still goes through SSO and the cookie is not cleared there; an agent
   token's `tools/list` contains the 15 new tools (or fewer after a C41 demotion), no human-only op and
   no `revoke_own_device`; `POST /api/drafts` without `Idempotency-Key` ⇒ 428.
4. **Agent session:** mint a token on `/human/tokens`, connect Claude Code over HTTP, run search ⇒
   context ⇒ rate (critic) ⇒ comment ⇒ `get_audit`; confirm the human rating is unchanged.
5. **Backups:** `docker exec metamusic metamusic backup` ⇒ `linked > 0, copied = 0`; restore drill into
   a fresh empty directory on the same volume; compare counts; remove the drill directory; confirm the
   first scheduled 03:30 run and that a forced failure (unwritable backup dir) sends the notification.
6. **Exports on the real archive:** `metamusic export --source <each imported JSONL>` compared by sha256
   with the inbox files (read-only); the validator reports exactly the issues of each inbox file (none
   for files with unique ids); `tracks` and `--for-batch` exports validate clean; results in private
   notes (Appendix P.2).
7. **Drafts:** when the `varianti/` recipes are imported, all varianti drafts are lint-clean (zero
   errors); their warnings are recorded in private notes.
8. Device checklist DC-M2a-1…7 on both phones; tag `v0.2.0-m2a` per the Definition of done.

## Definition of done (M2a)

- All named tests above green; `make check` < 30 s warm (M1 measured 37 s; the target stays, a regression beyond M1's number needs a recorded reason; heavy tests under `-short` skip as in M1);
  `make test` green with media tools; `make e2e` green (chrome + firefox) with **zero unserved ui ops**;
  `make gate`, `make routing`, coverage gates (incl. gate/idem/ratelimit/auth ≥ 95 %) green.
- Parity: every non-excluded op has a parity case and REST = MCP = stdio after normalisation; the
  `tools/list` golden is re-baselined and stays under the C41 ceiling (40 000 bytes).
- Every roadmap M2a acceptance item maps to a green named test or a signed-off device-checklist item.
- `TestNoSecretLeak` green in Go, `TestAgentsNeverSeeHumanNames` green, and `/__e2e/leaks` empty after
  the Playwright run.
- No real titles, artists, lyrics, hostnames, id prefixes or personal data in any tracked file; the
  public plan copy contains nothing below the private-appendix marker.
- Release 1 progress recorded: M2a [S] items green (M1 and M3 items tracked separately).

## Risks / open questions

- **`sendBeacon` with an `application/json` Blob:** same-origin beacons with a non-safelisted type are
  allowed by the Beacon spec, but browser behaviour has varied; W4 implements the keepalive-fetch fallback
  and W5 verifies the beacon on chrome + firefox. If both fail, the outbox still delivers on the next load.
- **iOS standalone cookie jar and the `/start` flow** are unverified on current iOS; DC-M2a-1/2 decide.
  If the standalone app shares Safari's jar after all, pairing remains the 90-day session fallback.
- **MCP client identity in stateless mode:** go-sdk v1.8.0 exposes no initialize params per stateless
  call; dispatch uses `RequestExtra.Header`'s `User-Agent` instead (C30, tested both ways). An SDK
  upgrade that changes either path must keep both tests green.
- **Exact number tokens** rely on Huma decoding the body into the input struct with `encoding/json`,
  which keeps `json.RawMessage` bytes verbatim; a Huma upgrade that changes its decoder would surface in
  `TestLintRecipe_RawTokens` and `TestOps_RawRequestSchema`.
- **At-least-once creates after a crash** (D-M2a-21): a process killed between a handler's commit and
  `Complete` can repeat that create on a retry after the 2-minute lease; acceptable for M2a (no money
  moves), M3 spend ops commit their idempotency row in the op transaction.
- **Batch-validator drift:** the mirror is hand-written from yue2-infer 0.1.6; M3's worker-contract job
  must run real `request_kwargs` on the exported files to confirm it, and a new yue2 release goes through
  the upgrade gate.
- **CPython golden:** `pyjson.golden.jsonl` is generated once with CPython and committed; regenerate it
  only with the recorded command (no CPython in CI).
- **VACUUM INTO on the writer pool** blocks writes for its duration (seconds at this size). If it grows,
  run it on a dedicated reader connection (verify modernc allows VACUUM INTO there).
- **EXDEV in production** would turn every backup into a full copy; X2 step 5 checks `copied = 0`.
- **Audit volume:** agent reads are audited; at 60 req/min the table grows ≤ 90k rows/day per busy agent
  — fine for SQLite; a retention policy can come later (append-only stays).
- **Rate limits are per process:** a busy `mcp-stdio` session has its own buckets (acceptable: stdio runs
  only on the host). Backups, by contrast, are guarded across processes by the DB (C34).
- **Language detection is crude** (stop words); it only drives warnings.
- **tools/list growth** (+15 tools): bounded by the C41 ceiling and its fixed demotion order; rarely used
  ops are already `generic`.
- **Snapshots share inodes** with the live tree (C34): they protect against deletion, not in-place
  modification of a frozen file; restore re-hashes everything; periodic snapshot verification is a later
  decision (with D7).

---

## Amendments by M2c (reference and lossy imports)

M2c amends this plan additively: C32 drafts refuse a stub recipe's request id and a reference track as
mother (`RECIPE_UNKNOWN`); C33 `songs.jsonl` export always excludes stub recipes (note
`recipe_unknown_excluded`) and the playlist ZIP carries lossy masters unchanged; C34 backups need no
change; C35 `/context` gains `track.recipe_known`/`lossy` and `recipe.known`/`note`; C40 the library
gains the recipe and audio filters and badges; C41 the `tools/list` ceiling is re-baselined to 56 200
bytes (measured 56 122); C42 `librarytest.FixtureM2c` adds a reference FLAC and a lossy reference.
