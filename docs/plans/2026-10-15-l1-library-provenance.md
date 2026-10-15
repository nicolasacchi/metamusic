# L1 — Library: Provenance — Implementation Plan (DRAFT, revision 3: aligned with the merged code)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, the **binding concept** `docs/private/design-src/library-concept.md`
> (operator-approved vocabulary and decisions of 2026-10-14: **Muse · Song · Take · Pick · Session ·
> Sound · Record · Crate**; do not rename a noun or change a decision), `docs/plans/2026-10-12-m0-skeleton.md`
> (C1–C9 incl. addenda), the M1 plan (C10–C24, D-M1-1…10), the M2a plan (C25–C42, D-M2a-1…22), the M2b
> plan (C43–C56, D-M2b-1…22), the M2c plan (C57–C67, D-M2c-1…14) and the M3 plan (C68–C86, D-M3-1…38)
> first. L1 builds on those exact contracts and continues the numbering after M3: **L1 contracts are
> C87–C101**, decisions **D-L1-1…**, migration **`0008_provenance.sql`** (schema version 8; `0006_listen_detail.sql` and M3's `0007_generate.sql` are the previous two). Where L1
> changes an earlier contract, the task that makes the change records it in that plan document in the
> same commit (named in each task's **Files**). L1 is the first library plan; the L2 (Curation) and L3 (Agents & map) drafts are bound by this one
> and renumber their migrations after it (see "Contract numbering and the later drafts").
> Amended contracts:
> - **M1/M2a (revision 2):** the contract that defines `DraftDetail.Reference` (humans-only today) is amended by D-L1-36
>   (`library/drafts.go`; the contracts are M1 C19 `get_draft` as moved by M2a C38/C32, pinned by M1's `TestGetDraft_ReferenceHumanOnly`, which L1-B1 turns into `TestDraftReference_AgentView`).
> - **M0:** C4 (`api.Meta` is unchanged; one new `exclude:` reason `human-judgment`, D-L1-11), C6
>   (`httpserver.Options` unchanged), C7 (CLI `library reconcile|verify`; `cmd/metamusic/main.go` dispatch gains `library`).
> - **M1:** C11 (no column changes on `tracks`/`recipes`/`recipe_drafts`; three new read-side joins), C17
>   (`library` read amendments: `song_id`, `is_pick`, filters; every one gated on table existence like
>   M2c's `lossy` and M3's `job_id`, D-M3-35 pattern), C19 (`search_tracks` filters, `TrackSummary`/
>   `TrackDetail` fields), C21 (the synthetic archive gets L1 index rows through the existing
>   `index.csv`; no generator change beyond the fixture builders).
> - **M2a:** C27 (`TestNoDeleteSQL` allowlist gains the derived/queue tables `provenance_dirty`,
>   `take_sounds`, `song_sounds`, `sound_state`, `song_unmapped_terms`), C35 (`/context` gains `song`,
>   `muse`, `sounds`), C38 (`get_stats` gains `library_model`), C40 (web routes), C41 (routing contract,
>   coverage, `tools/list` ceiling), C42 (`servertest.Options.L1`, `ParityCase` rows, fixtures).
> - **M2b:** C51 (`ears` gains the Tier A analyzer `tempo`; `earstest.TierA` gains `Tempo`), C48 (read
>   only: the ladder's `Strengths` is consumed by picks; no change).
> - **M3:** C81 (op count 81 → 99; `get_batch`/`list_jobs` unchanged), C84 (web: Track page Song strip,
>   Library Pick badge/filter, Backlog Board link), C85 (CI, routing, `tools/list` ceiling re-measured), C86 (fixtures, parity runner), D-M3-36 (`tools/list` budget pattern).
>
> **Binding lessons carried from M2b/M2c/M3:** never mutate process globals (`slog.SetDefault` etc.) in
> `run()` or handlers; e2e specs must not depend on counters other specs change; startup waits in tests
> tolerate instrumented (`-cover`) runs (generous deadline, poll for readiness, never a fixed sleep); never run Playwright outside `make e2e` (shared port); new tests
> are safe under `t.Parallel()` and `-shuffle` (no shared buffers, loggers or globals); a derived-state
> writer has exactly one writer (D-M3-3 pattern, here the Reconciler, D-L1-4).

**Goal:** give the library a model that fits how music is actually made here — many **takes** of
evolving **ideas** (Songs) derived from **inspirations** (Muses), in bursts of work (Sessions), judged
continuously, with a derived **best take** (Pick) and automatic **Sound** categories — without any manual
filing, and make it fully drivable by humans (web) and agents (REST + MCP) with strict parity. L1 is the
*Provenance* axis of the concept (§1): everything here is derived from facts the app already holds or is
a small intentional annotation (assign a Muse, name an angle, pin a Pick). The *Curation* axis (Records,
Crates) is L2 and builds on the stable ids and the Pick event stream L1 delivers. **Nothing in L1 spends
money; Muse Board "render" actions reuse the M3 estimate → confirm protocol unchanged.**

**Architecture:** one new migration (`0008_provenance.sql`: `muses`, `songs`, `song_roots` + `take_songs` view,
`song_picks`, `pick_events`, `pick_proposals`, `sessions`, `session_members`, `sound_taxonomy_versions`,
`sound_terms`, `take_sounds`, `song_sounds`, `sound_state`, `song_unmapped_terms`, `provenance_dirty`, `provenance_lease`,
plus triggers) and six small Go packages with a one-way dependency order: `internal/muses`,
`internal/songs`, `internal/sessions`, `internal/picks`, `internal/sounds` (leaves over `store`), and
`internal/provenance` (the **Reconciler**: the single writer of derived provenance rows, fed by a
trigger-maintained queue) with `internal/songview` (the Song view and shared read assembly, C87/C90.8) and `internal/board` (the Muse Board read model). Take→Song mapping is derived from
`tracks.root_track_id` through the stored `song_roots` link (a Song can own several roots, D-L1-3), not stored per take. Ears gains one Tier A analyzer (`tempo`, pure Go
over ffmpeg-decoded PCM) so Sounds can say "requested 76 BPM, measured 75". 18 new registry operations
(7 MCP tools, 9 generic, 2 excluded) feed REST, MCP and four new SPA routes (`/board`, `/muses/:id`,
`/songs/:id`, `/sounds`); `/context` gains `song`, `muse`, `sounds`. Backfill is not a separate program: the
migration seeds the reconcile queue from the existing rows, and the same code that keeps the library
consistent in steady state drains it.

**Tech stack:** Go 1.27 (stdlib first; no new dependency), Huma v2, modernc SQLite (FTS5 for Muses),
Svelte 5 runes + TypeScript strict + Bun/Vite, Playwright. No new Go module, npm package, binary or
config variable.

---

## Why this model (design thinking, public-safe)

**The album is the wrong unit at the wrong time.** An album is a *finished, ordered, released* set. The
work here is the opposite: many renders of one idea, many ideas per inspiration, judged continuously. If
the library asks the operator to file takes into albums by hand, every new batch creates filing work and
every judgement ("take 3 is better than take 1") has nowhere to live. The concept therefore splits the
problem into two axes and L1 builds only the first:

| Axis | Question it answers | Who does it | Nouns | Phase |
|---|---|---|---|---|
| **Provenance** | How was this made? Which idea is it a take of? What inspired the idea? When did we work on it? What does it sound like? Which take is best right now? | The app, automatically (a human or agent may *annotate*, never *file*) | Muse · Song · Take · Pick · Session · Sound | **L1 (this plan)** |
| **Curation** | What do I do with it? What belongs together to be listened to, shipped or judged? | The human (an agent may *propose*) | Record · Crate (+ existing Playlist, Tag) | L2 / L3 |

**Categories and lists, applied.** The operator asked for categories and lists "now and for the future".
The model gives each need a home so nothing has to be a folder:

| Need | Mechanism | Automatic? | Where |
|---|---|---|---|
| "what is this kind of music?" | **Sound facets** (genre family, era, tempo band, vocal, mood, language, engine): *facets, not folders* — a Song is in many at once; counts and drill-down | yes (deterministic parser over style + Ears measurement; versioned taxonomy extensible by ops) | L1 |
| "what inspired this?" | **Muse** (reference work or free-text brief); a Song has at most one | backfilled from the import index; humans/agents create more | L1 |
| "what did I do this week / this batch?" | **Session** (import run, JSONL upload collection, generation batch) | yes; renameable, never required | L1 |
| "which take do I listen to?" | **Pick** (deterministic, explainable, every change an event) | yes; human pins, agent proposes | L1 |
| "my own labels" | **Tags** (M2a, unchanged) | human/agent | exists |
| "an ordered finished sequence" | **Record** (living album: slots point to Songs, play the Pick) | human; agent proposes | L2 |
| "a working set / saved search" | **Crate** (manual) and **Smart Crate** (saved filter over the whole model) | human/agent | L2 |

**What L1 deliberately hands L2/L3:** stable `song_id`s (a Record slot points at a Song, never at a Take
that a new render could supersede); the append-only `pick_events` stream (the Record changelog is a
filter of it); `list_songs`/`search_sounds` filters with exactly the vocabulary a Smart Crate saves; the
Session ids; and the invariant that reference/lossy takes are first-class everywhere. L1 builds **no**
Record, Crate, cover art, tape export, Subsonic mapping, Atlas, Muse-aware `next_actions` or
`propose_record` — those are L2/L3 and are listed under D-L1-31 so they are not re-litigated here.

**Why Songs are derived from lineage and drafts, not filed.** The lineage root is already the app's
identity of "one idea" (M1 `root_track_id`, M3 Operator Ledger). A draft that is not yet rendered is the
same idea in waiting. Making `Song` = "lineage root, or a motherless draft that will become one" gives
every existing row a Song with zero manual work and makes the Muse Board possible: the 90s-style
backlog is a grid of *Songs that exist and are not yet rendered*, not a pile of drafts.

---

## Dependencies and sequencing

- **Hard:** M0–M3 contracts exactly as written, **after the M3 merge** (concept: L1 + L2 + L3 are built
  after M3 merges and before M4a). Branch `l1` from `main` (84b1ffc: M3, the X2 provisioning/probe work and the
  auto-advance hotfix are merged). Schema version after L1: **8** (`0008_provenance.sql`; the previous migrations are
  `0006_listen_detail.sql`, the auto-advance diagnostics hotfix that took number 6 on `main`, and M3's
  `0007_generate.sql`). A deployed database may be at schema 6 (before M3) or 7. A database at schema 6 migrates 0007 then 0008 in
  one boot; the 0008 seed statements read tables that 0007 created (`batches`, `jobs`), so the upgrade test starts at
  schema 6 as well as 7.
- **What L1 uses (binding):**
  - **M1:** `tracks` (`root_track_id`, `generation`, `origin`, `status`, `recorded_at`, `import_run_id`,
    `import_rule`), `recipes`, `recipe_refs` (the import index rows: `source_title`, `source_artist`,
    `variant_type`), `recipe_sources` (first `import_run_id` per recipe), `recipe_drafts` (`group_key`,
    `label`, `parent_track_id`, `status`, `fulfilled_by_track_id`, `revision`), `draft_collections`,
    `import_runs`, `scores` (planned BPM), `analyses`, `yue2.StyleTerms`, `yue2.DetectLanguage`,
    `textfold.Fold`, `library` read paths, FTS5 conventions.
  - **M2a:** `ratings` (human stars only count), `judgments` isolation (agent rating never moves a Pick),
    `Principal.ActsAsHuman()`, the gate, `api.Register`/`Meta`, `bodySmall`/`created` helpers,
    optimistic `revision`, `/context`, `get_stats`, `TestNoDeleteSQL`, audit (`args_sha256` only).
  - **M2b:** `duel_verdicts` (human blind verdicts), `duels.Service.Strengths` (global BTD fit, cached),
    `ears.AnalyzeTrack`, `analysis` FFT/Hann, the `earstest` fakes, `create_duel` (blind) from the Song page.
  - **M2c:** reference tracks (`import_rule = 'reference'`, `RecipeKnown`) and lossy masters — first-class
    in every L1 structure (D-L1-23).
  - **M3:** `batches`/`jobs` (sessions, the `queued` overlay), `estimate_generation` (`from_draft`) for
    Muse Board rendering, `genIntent.ts`, lineage page, `servertest.Options{M3, Backend, Runner}`, `librarytest.FixtureM3`, `internal/app` `Options.M3` and `m3.go` (the pattern `Options.L1` and `l1.go` follow).
- **Operator inputs (tracked in L1-X2/X3):** acceptance that the two demo Muses are created by an
  operator action (not guessed, D-L1-28); review of the top unmapped style terms; a spot check of measured
  tempo on the reference recordings. No secrets, keys or spend.
- **Gate before L1-B1 (D-L1-36):** the P.5 boxes for D-L1-6 (Muse text may reach agents), D-L1-11 (pin on `/api/`) and D-L1-36
  (draft `reference` visibility follows the Muse rule) are ticked in the private notes. The lane brief repeats this; B1 does not start without it.
- **Timing:** nothing date-bound. L1 merges before M4a (which is built in parallel on its own branch and takes migration
  `0009`); whichever of the two merges second renumbers its contracts after the first (L1 merges first: M4a's contracts start
  after C101 unless its aligned plan already avoids C87–C101). See "Contract numbering" below.

### Contract numbering and the later drafts

M3 ended at **C86** and, after its renumbering (`main` took `0006_listen_detail.sql` first), at migration **0007**. L1 takes
**C87–C101** and **0008**; decisions **D-L1-1…39** (no earlier plan uses the `D-L1` prefix; D-M3 ends at 38, D-M2b at 22).
The L2 draft (C102–C116, `0008_curation.sql`) and the L3 draft (C117–C130, `0009_agents_atlas.sql`) were written against
a plan that assumed `0007_provenance.sql`: their **contract numbers stay**, but their migration numbers move to the
next free numbers when they are aligned (L1-X1 appends this sentence to both headers; each aligns by reading the real
highest number in `internal/store/migrations/` instead of assuming a literal). M4a is built in parallel and takes **`0009`**;
it merges after L1, so L2/L3 take the first free numbers after M4a's merge. The drafts of M4a (C76–C91), M5 and M6
(also from C76, migration `0006`) were written before M3 took C68–C86; their X1 "renumber" tasks must start after the
last contract and migration of the L-plans (and after whatever M4a merged). L1-X1 appends this sentence to the M4a draft's header.

---

## Baseline reconciliation (verified against `main` at 84b1ffc, which contains M0–M3)

**Verified as the plan assumes.** `0001`–`0007` migrations (schema version 7: `0006_listen_detail`, `0007_generate`); `tracks.root_track_id` (= `id` for roots) with the `tracks_frozen`
trigger covering `root_track_id`; `recipe_drafts` columns above; `recipe_refs` keyed `(recipe_id, source)`
with `ref_json` carrying `source_title`, `source_artist`, `variant_type`, `track_n`, `variant`;
`draft_collections(draft_id, name, source)`; `import_runs`; `batches`/`jobs` with `jobs.draft_id`,
`jobs.source_track_id`, `jobs.track_id`; `ratings` (`source` human|agent, stars NULL = cleared);
`duel_verdicts` (`source` human|critic) and `duels.blind`; `duels.Service.Strengths(ctx, ids)` returning
`ladder.Strength{U, SD, NDuels}` from the global blind-human fit (critic rows never selected);
`ladder.Options` default `PriorSD = 1.5`; `yue2.StyleTerms` (≤ 20 terms, ≤ 40 runes each, BPM 40–250 from a
term that is exactly `NNbpm`/`NN bpm`/`bpm NN`); `yue2.DetectLanguage`; `analysis.FFT`/`Hann`; `api.Meta`
validation (`human_only` ⇒ `mcp: exclude:*` and `/human/api/`; `exclude:` reasons are free text);
`TestAcceptance_M2a_AgentPerformsEveryUIAction` derives its required set from the registry as "not
human-only, mcp not `exclude:*`, ui starts with `/`" (so a new op with a UI route and an MCP face needs a
scripted agent call, and an `exclude:` op does not); `TestRegistry_OpCount` (`internal/api/ops_m2c_test.go`) pins 81; `toolsCeilingBytes =
87300` (`internal/mcpgen/m2a_contract_test.go`); `auth.Authorize` refuses `KindDevice` on human-only ops (`p.Kind != KindHuman`) while
`ActsAsHuman()` is true for humans and paired devices; the importer sets `recipe_drafts.parent_track_id`
by UPDATE after insert (so a draft can gain a mother after its row exists) and `update_draft` may change
it while the draft is a revision-1 backlog row; M3's job ingest fulfils the draft with `UPDATE
recipe_drafts SET status='fulfilled', fulfilled_by_track_id=…`.

**Findings that shape the plan (the concept's sketch vs the code).**

| Area | Concept sketch / assumption | Code reality | Resolution (where) |
|---|---|---|---|
| Ears tempo | §2 Sound "confirmed by Ears measurements (requested 76 BPM, measured 75)" | Ears Tier A measures loudness, bandwidth, silence, clipping, token loops, spectrogram — **no tempo**; `scores.bpm` is the *planned* ABC tempo, not a measurement | New Tier A analyzer `tempo` (`mm-tempo/1`, C94, D-L1-17); reference tracks get Sounds only from it |
| Draft with a mother | "exactly one Song per lineage root and per not-yet-rendered draft" | a draft may gain or change `parent_track_id` after insert (importer, `update_draft`); a mothered draft renders as a child of an existing root | A mothered draft has no Song of its own; a Song created earlier is **retired** into the mother's Song (D-L1-3, C90.3) |
| Recipe/draft Muse links | §5 `recipe_muses`, `draft_muses` | the Muse of a Song fully determines both (recipe → draft → Song) | Dropped; `songs.muse_id` is the only link (D-L1-5) — the concept's sketch is refined, the nouns and decisions are untouched |
| Humans pin | §4 "humans `pin_pick`" | `/human/api/` ops need `KindHuman` (2FA edge rule) and refuse paired devices; pin is a light, phone-friendly act | `pin_pick` lives on `/api/` (scope write) with a principal check `ActsAsHuman()`; MCP face excluded (`exclude:human-judgment`), agents use `propose_pick` (D-L1-11) |
| Backfill | §7 backfill of existing data | the importer, drafts, jobs and CLI write the tables from several processes | Triggers enqueue, one Reconciler derives; the migration seeds the queue (D-L1-4) |
| Session membership "Songs" | §2 "membership for drafts, Songs and Takes" | Songs are derived | Membership rows are `draft` and `track` only; Song membership is derived (D-L1-13) |
| Muse Board scale | §3 a grid of every inspiration × its angles | payload cap 90 000 B per list (M1 C17) | Row-paged board with the page cap enforced in Go (C97); perf asserted on a synthetic scale fixture (C101) |
| `tools/list` | not considered | `toolsCeilingBytes = 87300` after M3 | L1-B2 measures and sets the new ceiling; demotion order in C96 |
| Subsonic album mapping (L2) | §7 L2 "per-Muse auto-albums" | §0 decision: Muses never reach Subsonic clients | **Conflict recorded for L2** (Risks): per-Muse auto-albums must use Session/Song names or be opt-in; L1 only guarantees no Muse text is reachable from any Subsonic-bound package (C99) |
| Older-schema databases | n/a | a database at schema 6 (or 5) has no `batches`/`jobs` | 0007 and 0008 apply in one boot; the 0008 seed reads 0007's tables; the backfill is exercised on a schema-6 copy as well as the schema-7 production copy (L1-X2) |
| `textfold.Fold` | the plan keyed Muses and taxonomy terms on `Fold` | `Fold` keeps case and does not collapse whitespace | new `textfold.Norm` = collapse(lower(Fold)) used everywhere (D-L1-33) |
| Timestamp widths | "fixed-width, strings compare" | pre-M3 columns are 20 characters (`…05Z`), M3/L1 columns 24 (`….000Z`); the old ones are frozen | compare parsed instants, normalise session times (D-L1-34) |
| Draft reference metadata | Muse text readable by read-scope agents | `DraftDetail.Reference` is humans-only (M1/M2a) | explicit amendment (D-L1-36) |
| Rendered drafts | "Song = lineage root" | `from_draft` n>1 yields `[draft seed] + n−1` motherless roots; re-rendering a fulfilled draft yields another root; `compose_new` n>1 likewise | a Song owns several roots through `song_roots` (D-L1-3, C90.1) |
| Contract/migration numbers | n/a | M4a/M5/M6 drafts claim C76–C99 and `0006`; the L2/L3 drafts claim `0008`/`0009`; M4a (in parallel) takes `0009` | renumbered later (see above) |

**Alignment with the merged code (revision 3).** Every symbol, file, contract, op id, test helper and CI job named below was checked
against `main`; these are the places where revision 2 had drifted and what this revision says now.

| Area | Revision 2 said | Merged code | Resolution (where) |
|---|---|---|---|
| Migration number and schema version | `0007_provenance.sql`, schema 7, previous `0006_generate.sql` | `0006_listen_detail.sql` (hotfix) and `0007_generate.sql` exist | `0008_provenance.sql`, schema 8; tests `TestMigration0008_*` (`TestMigration0007_Applies` and `…UpgradeFromM2d` already exist in `internal/store`); upgrade fixtures at schema 6 and 7 (header, C88, C101.1, B1, X2) |
| Revision-conflict error | 409 `STALE_REVISION` "as playlists" | playlists and drafts answer 409 `REVISION_CONFLICT` with `details.current_revision` | `REVISION_CONFLICT` everywhere (C89.1, C90.5, C92.2, C96) |
| Error construction | not stated | leaf packages return `*problem.Error` (`problem.E(status, code, msg, details)`, as `duels`); the api maps it; stubs return `errors.ErrUnsupported` ⇒ 501 `NOT_IMPLEMENTED` with `details.task` | C87 (leaves may import `problem`), B2 |
| `TestSchemaVersionAgnostic` | listed as a new B1 test | exists (`schema_m3_test.go`); the one remaining literal is `upgrade_m3_test.go` (`v != 7`) | B1 edits that literal to `v < 7`; the existing test stays green |
| `/context` reader name | `trackctx.ProvenanceReader` in `trackctx/provenance.go` | `Bundle.Provenance` is an existing section (`origin`, `import_rule`, `truncated`, `cost`, `env`, `job_id`); four goldens exist (`context`, `context_job`, `context_lossy`, `context_reference`) | renamed `trackctx.LibraryReader` / `trackctx/library.go`; all four goldens re-baselined (C98, B5) |
| `Rig` option | `servertest.Options.ManualReconcile` "default true" | Go bool default is false; M3's `Runner bool` (false ⇒ built but idle) is the pattern | `Options.Reconcile bool` (false ⇒ Reconciler built but idle; true starts `Run`); `app.Options.L1 L1Options` beside `M3` (C87, C101.2) |
| Readiness | "readiness waits for Settle" | no readiness flag exists: `serve()` (shared by `serve` and `dev`) opens the listener after building services; the image HEALTHCHECK is `--start-period=10s --interval=30s --retries=3` (≈ 100 s before unhealthy) | startup `Settle` runs in `serve()` before `net.Listen` (budget 60 s < the healthcheck window); `mcp-stdio` and rigs never start `Run`; test is `TestServe_SettlesBeforeListening` (C95.3) |
| Performance helper | "env-scaled budget helper of the earlier plans — `MM_PERF_FACTOR`" | no such helper exists; the only timing test (`TestFit_TimeBudget`) skips under `-short` and `-race` with a `raceEnabled` constant | new test-only `internal/perftest` (`Budget(t, d)`: `MM_PERF_FACTOR`, default 1, ×10 under `-race` because the transpiled SQLite stack costs ≈ 10×; perf tests skip under `-short`), added to the helper list of `cmd/metamusic/nohelpers_test.go` (C101.4, B1) |
| Synthetic names | `TestFixtures_L1_SyntheticOnly` accepts only `Synthetic Muse NNN` | the base fixtures and the synthetic archive already carry `Synthetic Reference Title`, `Synthetic Title N`, `Synthetic Artist N` | the check accepts the family `Synthetic <words>[ NNN]` (C99.5) |
| `every_ui_action` rig | script uses `FixtureL1` | `TestAcceptance_M2a_AgentPerformsEveryUIAction` runs on `archiveRig` (the imported synthetic archive, `uiState` + `uiOrder` + `uiScript`), not on a library fixture | L1 entries get their ids through `afterStep` from `list_songs`/`list_muses`/`create_muse`; the rig settles after import (C101.2) |
| Playwright projects | "`l1-*.spec.ts` registered in the config" | every project is `testMatch **/*.spec.ts` minus ignore lists; M3 specs run as a third phase because they change shared counts | the L1 specs mutate shared state (Muses, Picks, stars, duels): a fourth phase `l1-*` after every M3 project, ignored by phases 1-3 (C100.7, W4) |
| Web navigation | "entries in `lib/nav`" | `lib/nav.ts` only classifies `/human/*` paths; the menu is the `<nav>` in `App.svelte`; routes live in `lib/router.ts` `APP_ROUTES` (`{pattern, name}`) | W1 edits `router.ts` and `App.svelte`; Library Pick badge/filter go in `TrackBadges.svelte`, `TrackTable.svelte`, `LibraryFilters.svelte`, `libraryQuery.ts` (C100.1, W3) |
| CI tiers | "tempo/ffmpeg tests join the media list" | `scripts/ci-go-test.sh`: `internal/analysis/ffmpeg`, `internal/acceptance`, `internal/servertest`, `internal/jobs`, … are already in `media`; every other package is `fast` automatically | no list edit unless a new package needs real media tools; the weekly long run follows the `spend-property` job (`MM_PROPERTY_TIMELINES`) with `MM_PROPERTY_SEQS` (C101.3) |
| Parity runner | "gets 16 rows in `parity_test.go`" | per-milestone files append to `parityCases` in `init` (`parity_m2c_test.go`, `parity_m3_test.go`) | rows live in `parity_l1_test.go`, appended the same way (C101.2) |
| Import-graph test | `TestImportGraph_L1` "extending the M3 table" | `internal/archtest/import_graph_test.go` has `TestImportGraph_M3` over `m3Rules` and `TestImportGraph_RulesBite` | `l1Rules` table and `TestImportGraph_L1` added beside them; new helper packages join the `nohelpers_test.go` list (B1) |
| Decision/contract ranges | D-M2b-1…21, D-M3-1…37 | D-M2b-22 and D-M3-38 exist | header updated; no collision with `D-L1` |
| Base-fixture counts | "verified at planning" | re-measured on `FixtureM3` at schema 7: 8 tracks (6 roots, 2 non-roots, 1 lossy, 2 reference roots), 9 drafts (8 motherless backlog, 1 mothered, 2 `jsonl_upload`), 1 `recipe_refs` row (with a variant type), 1 import run, 3 batches (2 active/finished with jobs), 1 collection of 3 drafts, 2 human ratings | C101/acceptance integers confirmed unchanged (389 Songs etc.) |
| System principal | Reconciler rows carry `system:reconciler` | M3 already uses `system:reconciler` (jobs reconciler, audit rows, D-M3-22) and `system:runner`; two writers must not share a key | L1's derivation writes `system:provenance` (`created_by`, `actor`, `decided_by`; `api.RefFor` renders it as `{system, system:provenance, provenance}`); key-valued strings go through `api.KeyFor` (C89–C92, C96) |
| Lease helper | "the D-M3-3 fence pattern" | `store.LeaseFence`/`ErrLeaseLost` are hard-wired to `runner_lease` | `provenance` owns its predicate and error values (D-L1-35, C95.1) |
| `tools/list` budget | demote above 14 000 compact bytes; `TestToolsList_L1Ceiling` | the rule M2b-B2/M3-B2 measured against is 16 000 bytes; the constants are `toolsCeilingBytes` (`m2a_contract_test.go`) **and** `toolsListBaselineBytes` (`parity_test.go`, 1.5× golden budget); `TestToolsList_Ceiling` and `TestToolsListBudgetFires` exist; M3-B2 stopped at the operator and the ceiling became 87 300 | C96, D-L1-20, B2 use 16 000, re-baseline both constants with justification comments, add only `TestToolsList_L1Budget` |
| Fixture signatures | `FixtureL1(tb, st, data)`, `FixtureL1Mirror(tb, params)`, `FixtureL1Upgrade(tb)` | fixtures are `Fixture…(tb, st, now)`; the upgrade tests open `store.OpenRaw` + `MigrateTo` and pass that store to the fixture | `(tb, st, now[, params])` throughout (C101.1) |
| Ears types and `trackctx` ownership | dossier/perception fields in `internal/api/types_m2b.go`; E1 and B5 both touch `/context` | the types are `ears.Dossier`/`ears.Perception` (`ears/types.go`); `api/types_m2b.go` only wraps `ears.Dossier`; `internal/trackctx/**` is B5's and its goldens are shared | E1 edits `ears/**` only; B7 owns the single final `perception` `order` edit and golden `-update` (L1-E1, L1-B7 Files) |
| CLI import order | `import` and `seed-synthetic` `Settle` before exit | both already end with `tierAAfterIngest` (`cmd/metamusic/cmd_tiera.go`, `Ears.Backfill`), and the `tempo` analyzer inserts `analyses` rows that enqueue tracks | `Settle` runs after `tierAAfterIngest` (D-L1-27, C95.4) |
| Job-item key of `compose_new` | `(batch_id, sha256 of style + lyrics)` | `estimates.items_json` is an array of `planTake` (position = `take_index`, field `item_index`) | exact key `(batch_id, item_index)` (C90.1 step 2) |

---

## Decisions recorded by this plan

These refine the design of record; L1-X1 copies them into the private design document. Concept decisions
are inputs, not repeated.

| Id | Decision | Why |
|---|---|---|
| D-L1-1 | **Scope.** L1 = Muse, Song, Take mapping, Pick, Session, Sound facets, Muse Board, Song page, Sound browser, backfill, REST/MCP/SPA parity, `/context` additions. Nothing spends. No `album` word anywhere in code, API or UI copy (Records are L2). Branch `l1` from the merged `main`; migration 0008. | Concept §7; keeps L1 shippable alone, before L2/L3. |
| D-L1-2 | **Nouns are tables and API words:** `muses`, `songs`, `take` (= existing `track`; the API keeps `track_id`, the UI says "take" on the Song page and "track" elsewhere as today), `song_picks`/`pick_events`, `sessions`, `sound_*`. No synonyms. | Concept vocabulary is binding. |
| D-L1-3 | **Song identity.** A *live* Song exists for (a) every lineage root (`tracks.generation = 0`) that is not attached to another Song and (b) every **motherless** draft (`parent_track_id IS NULL`) that is not fulfilled by a non-root take and is not already part of another Song. A Song owns **one or more roots** through the stored link `song_roots(root_track_id PK, song_id, via)`; `songs.root_track_id` is its *primary* (first) root. A root produced by rendering a draft is attached to the draft's Song and never gets a Song of its own: `from_draft` with `n > 1` yields `[draft seed] + n−1 new seeds`, all motherless roots (M3 C71.2), and re-rendering an already fulfilled draft allocates a new recipe and another root; `compose_new` with `n > 1` behaves the same way and its takes share one Song (keyed by the job item, C90.1). A root with no draft or job-item link gets its own Song. A draft **with** a mother, or one fulfilled by a non-root take, has no Song of its own because its renders join that lineage. If a draft that already has a takes-less Song gains a mother, the Song is **retired** (`retired_at`, `retired_into` = the mother's Song; never deleted, excluded from every list and count; its Muse and angle are carried to the target when it has none, D-L1-37); if a mother is later removed a *new* Song is created. Take→Song is derived from the link: `take_songs` = `tracks JOIN song_roots ON song_roots.root_track_id = tracks.root_track_id`. | One idea = one Song however many seeds were rendered; reproduces "one Song per idea and per not-yet-rendered draft" without duplicate Songs on the Board, and Picks are computed over all the takes of the idea. No per-take link table means no drift. |
| D-L1-4 | **Eventual consistency by a queue and one writer.** SQL triggers on the source tables only enqueue `provenance_dirty(kind, id, at)`; the **Reconciler** (`internal/provenance`) is the only writer of derived rows (Song rows' structural columns, Muses created from refs, session rows and members, `song_picks`/`pick_events` derivation, `take_sounds`/`song_sounds`). `Settle(ctx)` drains the queue synchronously (tests, CLI, startup) under the cross-process lease (D-L1-35); `Run(ctx)` polls every 1 s while the queue is non-empty. The migration seeds the queue from the existing rows, so **backfill is the steady-state path**, not a separate script. Reads that can be stale expose `pending: true`. Human/agent edits (Muse text, Song title/angle/Muse, pins, proposals, taxonomy) are written by their ops in their own transactions on disjoint columns. | Every writer (importer CLI, drafts, M3 ingest, ratings, duels, Ears) is covered without call-site edits; invariants cannot be forgotten by a future writer; same pattern as D-M3-3 (single writer). |
| D-L1-5 | **Muse.** `kind` `reference` (title + optional artist) or `brief` (short title + free-text brief). A Song has at most one Muse (`songs.muse_id`); Many Songs per Muse. Reference Muses are unique by the normalised key `norm(title) + 0x1F + norm(artist)` (`textfold.Norm`, D-L1-33); brief Muses are never merged. Backfill creates one reference Muse per distinct normalised `(source_title, source_artist)` of a draft's `recipe_refs` row (`source = 'recipe_refs'`). The Muse of a Take is the Muse of its Song; the concept's `recipe_muses` and `draft_muses` are **not built**. A Muse reaches a Song under the never-overwrite rule of D-L1-37 (`muse_source`). Hiding a Muse sets `hidden_at`; there is no delete. | One link, one truth; the 3 variants of one inspiration land on one Muse by the key. |
| D-L1-6 | **Muse visibility is structural.** Muse text (`title`, `artist`, `brief`) is readable only through `list_muses`, `get_muse`, `get_song` (the `muse` object), `get_muse_board`, `get_track_context` (`muse`) and the echoes of `create_muse`/`update_muse` — all scope `read` or higher, i.e. visible to the operator's agents. Draft `reference` metadata follows the same rule (D-L1-36) and collection-derived Session names are treated like Muse-adjacent text (read by agents through `list_sessions`/`get_song`, never in exports, logs, audit rows or notifications; canary-tested, C99). Muse text is never in exports (JSONL, ZIP, backups' manifests), logs, audit rows (`args_sha256` only; error messages carry ids, never Muse text), job events, ntfy messages, Subsonic responses or any tracked file. Enforced by an import-graph rule, a SQL-table allowlist, a response allowlist test with a canary Muse and the existing leak scanners (C99). | Concept §0/§4 decision; a rule that is only a convention would be broken by the next exporter. |
| D-L1-7 | **Angle** is an open lowercase slug (`^[a-z][a-z0-9-]{0,31}$`) with `angle_source` `reference` (the index `variant_type`), `request_id` (see below), `manual`, or `cleared` (an operator removed the angle; never refilled, D-L1-37). A draft's angle comes from its `recipe_refs.variant_type`; if it has no ref row and its request id ends `_v<n>`, the angle is the variant type that **every** ref row whose request id ends `_v<n>` carries, only when all of them agree (computed once per reconcile pass from the ref rows; absent ⇒ no angle). Root Songs get no angle. Board columns are the distinct angles in view ordered by Song count then name, plus a `(none)` column. | Concept §2 "optional angle … backfilled from variant_type and from request ids where unambiguous". |
| D-L1-8 | **Song title** defaults never contain Muse text: a draft Song takes the draft's `label` unless the label is empty, equals its angle, or is a bare angle-like slug (`^[a-z][a-z0-9-]*$`), in which case it is the humanised `group_key` (`_` → space, title-cased) followed by ` · ` and the angle with `-` shown as a space when there is one (`P90s 001 · remake long`), or the humanised request id when the group key is empty (≤ 200 runes, as D-M3-27); a root Song copies the root track's title at creation and is **not** kept in sync (renaming is `update_song`). | The Board must be shareable in screenshots/exports without Muse leakage; a Song title is the operator's own working name; a bare request id (`P90s 001 V1`) is a weak Board label, the group key names the slot without naming the inspiration. |
| D-L1-9 | **Pick derivation** (pure, deterministic, explainable) over the Song's **eligible** takes (`status <> 'discarded'`): (1) the **pinned** take if set and eligible (a pinned take that is not eligible stays recorded as `pinned_take_id` and is skipped for display only: evidence `pin_ineligible`; the pin applies again when the take is eligible, D-L1-38); (2) **ladder**, with incumbent stickiness (D-L1-38): when the Song has `N ≥ 3` counted human blind verdicts on duels whose two slots are both takes of the Song, rank eligible takes by ladder `u` (a take absent from the fit is `u = 0, sd = 1.5`); a fresh leader wins iff `u₁ − u₂ ≥ 0.5 · √(sd₁² + sd₂²) − 1e-9` against the runner-up (`z ≥ 0.5`, ≈ 69 % one-sided); an incumbent ladder Pick is kept while `z ≥ 0.25` and is displaced only by a challenger with `z ≥ 0.5` over it; (3) **stars**: the highest *human* stars (agent/critic ratings never count), ties → newest; (4) **newest** by `(recorded_at, created_at, id)` descending, the two timestamps compared as parsed instants (D-L1-34). No eligible take ⇒ source `none`. Thresholds are constants (`LadderMinDuels = 3`, `LadderChallengeZ = 0.5`, `LadderKeepZ = 0.25`) beside the function, changed only by a decision. | Concept §2 priority, with the "σ-aware" and "≥ 3 human duels" clauses made executable and the Pick stable under global-fit drift. |
| D-L1-10 | **Recompute.** Triggers enqueue the affected takes on take insert, status change, human rating insert/stars change, human duel verdict, and Ears `tempo` insert; the Reconciler recomputes the picks of their Songs. Because the global ladder fit shifts every `u`, a **sweep** recomputes every Song with ≥ 2 eligible takes and ≥ 1 counted human verdict at startup and hourly (`Run`). Open proposals whose take became ineligible or is now the Pick expire in the same transaction (C91.5). `evidence_json` is refreshed in place on every recompute; an event is written only when `(take_id, source)` changes. | Cheap and complete; the sweep covers cross-Song ladder drift the triggers cannot see. |
| D-L1-11 | **Pin and propose.** `pin_pick` (`PUT /api/songs/{id}/pin`, scope write) is applied only for `ActsAsHuman()` principals (human or paired device); any other principal ⇒ 403 `HUMAN_ONLY`; its MCP face is `exclude:human-judgment` (so the "agent does every UI action" derivation skips it). Agents call `propose_pick` (one open proposal per agent per Song; a re-proposal supersedes). A human pin with `proposal_id` closes that proposal as `accepted`; `dismiss_pick_proposal` closes as `dismissed`. A pinned take that becomes `discarded` **stays pinned**: the Pick falls back for display (event reason `pin_ineligible`) and returns to the pin when the take is eligible again (`pin_restored`); only an explicit unpin clears a pin (D-L1-38). | Matches the rating-source isolation pattern (D-M2a-12) without the 2FA path for a light act on a phone; agents cannot move a Pick, only suggest; a mistaken discard plus undo never loses the human's decision. |
| D-L1-12 | **Every Pick change is an event, enforced by the schema.** `pick_events` is append-only (`seq` per Song, a global monotonic `cursor` = the table's `rowid`, `from_*`, `to_*`, `reason`, evidence); `song_picks` has a `last_event_id` and triggers that refuse any insert/update of `take_id`/`source` not accompanied by a matching new event. Events carry no Muse text and no principal other than the pinning key. | Concept §6: "Pick derivation is deterministic and explainable; every change is an event (for Record changelogs and agents)". |
| D-L1-13 | **Sessions.** Kinds `import`, `upload`, `batch`, `night` (`night` is reserved: no L1 writer). Auto-created with a `source_key` (`import:<import_run_id>`, `collection:<name>`, `batch:<batch_id>`), renameable (`update_session`, revision-locked), hideable, never required. Batch sessions exist only for batches in state `active` or `finished` with ≥ 1 job (rejected, expired and awaiting batches create none). Membership rows are `(session_id, member_kind IN ('draft','track'), member_id)`, append-only; a Song is "in" a session when its draft or any of its takes is a member (derived). A collection session's `kind` is decided **once, at first sync** (`upload` iff all its drafts then have origin `jsonl_upload`, else `import`) and is frozen like the column says. Session names derived from collection names can embed inspirations and are treated as Muse-adjacent text (D-L1-6). | Concept §2; derived Song membership avoids a third membership table that could drift; no empty-session spam. |
| D-L1-14 | **Sounds are facets, not folders.** Seven facets: `genre_family`, `era`, `tempo_band`, `vocal`, `mood`, `language`, `engine`. Stored per take (`take_sounds`, from the take's recipe, plan and Ears) and per Song (`song_sounds`, from the Song's **representative subject**: the Pick take, else the newest eligible take, else its draft). Row `origin` ∈ `style` · `lyrics` · `declared` · `plan` · `measured` · `recipe`. Reference takes (stub recipe) get **only** `tempo_band` with origin `measured` (and nothing else) — never an invented value. | Concept §2 Sound; counts and drill-down at Song level, details at take level. |
| D-L1-15 | **Taxonomy is versioned and append-only.** `sound_taxonomy_versions` (kind `seed` with a `seed_id` such as `seed/1`, or `extension`), `sound_terms(version, term, facet, value, op add|remove)`. The code seed (`internal/sounds/seed.go`) is applied at startup as one version per unapplied `seed_id`, idempotently. The effective mapping at version V is the fold over versions ≤ V (`add` adds a `(term, facet, value)`, `remove` removes it). `map_sound_term` (write scope; agents allowed) adds an `extension` version with ≤ 50 mappings and recomputes affected facets synchronously. Term-derived facets are `genre_family`, `era`, `vocal`, `mood`, `language`; `tempo_band` and `engine` are computed, not mappable. | Concept §5 "versioned … seeded in code, extended by agents/humans via ops"; L3 adds proposals on top. |
| D-L1-16 | **Matching** is phrase containment, not exact-term only: each comma/semicolon/pipe/newline-separated part of the style (`textfold.Norm`, ≤ 160 runes) is matched against the effective term set by whole-word n-grams (1–5 words), longest match first, non-overlapping; every matched mapping is emitted. Parts with no match (≤ 40 runes, not a BPM term) are the **unmapped terms** (cached in `song_unmapped_terms`, surfaced by `get_sound_taxonomy`). BPM of a take comes from `tracks.bpm_declared` when set, else `yue2.StyleTerms`; BPM parts are skipped with a local matcher pinned to `StyleTerms` by a table test (`yue2`'s matcher is unexported); language from an explicit language term, else `yue2.DetectLanguage(lyrics)`. | Real style prompts are descriptive phrases ("laid-back roots reggae groove"); exact-only matching would leave most Songs unclassified. |
| D-L1-17 | **Tempo.** `tempo_band` ∈ `slow` (< 80), `mid` (80–109), `upbeat` (110–129), `fast` (130–159), `rapid` (≥ 160). Requested BPM = style-declared, else the score's planned BPM; measured BPM = the Ears `tempo` analyzer (`mm-tempo/1`, Tier A). The Song-level `tempo_band` row uses measured if present, else declared, else plan, and its `detail_json` carries `{requested_bpm, plan_bpm, measured_bpm, basis, mismatch, octave}`: `octave` when the measured value is within 6 % of 2× or ½ the requested one; otherwise `mismatch` when `|measured − requested| > max(4, 5 % of requested)`. Measured tempo is an *estimate* and is labelled so everywhere. | Concept §3 "requested vs got" badges without crying wolf on octave errors. |
| D-L1-18 | **Song state** (for chips and filters): `draft` (no takes), `rendered` (≥ 1 take), `judged` (≥ 1 human star, counted human verdict, or non-`draft` take status), `picked` (the Pick's source is `pinned` or `ladder`); precedence `picked > judged > rendered > draft`. `queued_jobs` (M3 jobs in state `queued`, `submitting`, `starting`, `generating`, `transferring` or `ingesting` for the Song's draft or any take as parent; jobs of an unconfirmed batch do not count) is an overlay count, not a state. A Song whose only anchor is an `archived` draft carries `archived: true` and is hidden from lists unless `include_archived`. | Concept §3 chips (`draft · queued · rendered n takes · judged · picked ★`). |
| D-L1-19 | **Muse Board** is `get_muse_board`: rows = Muses (plus a trailing `Unassigned` pseudo-row), columns = angles, cells = Song chips; filters session, sound facets, angle, state, text; row-paged (keyset on `(title_fold, id)`) with the 90 000-byte page cap enforced in Go for every `detail` and every caller (no larger exception); the `Unassigned` row is capped at 200 chips with its own cursor (C97). Selecting `draft` chips and pressing "Estimate" builds a `from_draft` intent (≤ 50 drafts, M3 D-M3-7) and goes through `/human/generate`; **the Board never calls a spend operation**. | Concept §3; zero new money paths. |
| D-L1-20 | **Registry.** 18 operations: 7 MCP tools (`list_muses`, `get_muse`, `create_muse`, `list_songs`, `get_song`, `propose_pick`, `search_sounds`), 9 generic (`update_muse`, `assign_muse_songs`, `update_song`, `list_pick_events`, `list_sessions`, `update_session`, `get_sound_taxonomy`, `map_sound_term`, `get_muse_board`), 2 excluded (`pin_pick`, `dismiss_pick_proposal`: `exclude:human-judgment`). Registry 81 → 99. Parity cases: 16 (the ops with an MCP face); the two excluded ops have REST-only tests. `tools/list` ceiling re-measured by L1-B2; if the L1 tools alone exceed the 16 000 compact-byte rule (D-M2b-22, D-M3-36), demote in order `list_songs`, `search_sounds`, `create_muse`, `get_muse` to generic. **Measured (B5):** the L1 tools exceeded the rule, so `list_songs` was demoted to `mcp: generic` (first in the order above): 6 MCP tools, 10 generic; the `tools/list` ceiling constant is 103 500 compact bytes (was 102 600). | Keeps the MCP surface bounded (D-M3-36 pattern). |
| D-L1-21 | **Library read amendments tolerate old schemas** (D-M3-35 pattern): `TrackSummary`/`TrackDetail` gain `song_id` and `is_pick`; `search_tracks` gains `song_id`, `is_pick`, `session_id`; the joins appear only when `songs` exists (`hasTable`), so `FixtureV2`…`FixtureM3` still build and read. | Fixture-frozen row counts; no churn in older golden tests. |
| D-L1-22 | **`/context`** gains three sections after `track`, in this order: `song`, `muse`, `sounds` (C98). `next_actions` is unchanged in L1 (Muse-aware actions are L3). | Concept §4 minus the L3 item. |
| D-L1-23 | **Reference and lossy takes are first-class**: they belong to Songs (each original reference recording is its own Song; a pilot group with lineage edges is one Song), take part in Picks (stars, newest, pin, propose; ladder when duels exist), show on the Song page with a `lossy` badge, and contribute Sounds only from measurement. | Concept §6. |
| D-L1-24 | **Immutability.** Provenance tables follow the repo rules: no physical deletes, frozen columns by trigger, hide/retire flags. The only deletable tables are *derived caches and queues* (`provenance_dirty`, `take_sounds`, `song_sounds`, `sound_state`, `song_unmapped_terms`): they are rebuilt by delete-and-insert inside the Reconciler's transaction and are added to the `TestNoDeleteSQL` allowlist (as `idempotency` is). | Derived rows must be replaceable; history lives in `pick_events`, taxonomy versions and audit. |
| D-L1-25 | Every new timestamp column is written with `store.FormatTime` (`2006-01-02T15:04:05.000Z`); the trigger-written `at` uses `strftime('%Y-%m-%dT%H:%M:%fZ','now')`, which has the same fixed width (D-M3-23). New columns therefore order lexicographically **among themselves only**; anything that also reads a pre-M3 column compares parsed instants (D-L1-34). | Lexicographic order = time order for L1's own columns; pre-M3 rows are 20 characters wide and frozen. |
| D-L1-26 | The SPA reaches everything through `web/src/api/` (generated client); no private endpoint exists for any L1 screen; every op with a UI route is served by a Playwright spec (served-operation coverage). | Architecture rules 1–2. |
| D-L1-27 | CLI: `metamusic library reconcile [--dry-run]` (Settle; with `--dry-run` it reports the queue and what a pass would create), `metamusic library verify` (the invariants of C95.5, exit 1 on any anomaly). `metamusic import` and `seed-synthetic` call `Settle` before exiting, **after** their existing `tierAAfterIngest` step (`cmd_tiera.go` runs `Ears.Backfill`, whose new `tempo` rows enqueue their takes), **when they obtain the settle lease** (no server running) so a CLI-built database is consistent; when a server holds it they only enqueue and the server drains (D-L1-35); `library reconcile --wait` polls until the queue is empty. Existing `metamusic analyze` runs the new `tempo` analyzer for tracks that lack it (Backfill skip rule, `Force` off). | A hand-off database is never half-derived; no new maintenance concept. |
| D-L1-28 | **Demo Muses are an operator action, not a heuristic.** Songs without index rows (JSONL-uploaded drafts, reference recordings) stay unassigned until a human (or an agent through `create_muse` + `assign_muse_songs`) assigns them. The Board shows them in `Unassigned`, filterable by Session. No code guesses a Muse from a file name or a collection name. | A wrong guess would put wrong text in front of agents and is hard to notice. |
| D-L1-29 | **Money:** no L1 operation has `cost` other than `free`; `TestL1_NoSpendOps` asserts it; the Board's render path ends at the existing estimate/confirm UI. | CLAUDE.md rule 6; M3 protocol is the only money path. |
| D-L1-30 | **Contract numbers** C87–C101, migration 0008, decisions D-L1-1…, test files suffixed `_l1`; the M4a/M5/M6 drafts renumber later (see above). | Continuity. |
| D-L1-31 | **YAGNI (not in L1, not to be built "just in case"):** Song merge/split, several Muses per Song, manual categories or folders beyond tags, machine-learned genre classification or audio embeddings, cover art, Records, Crates, tape export, Subsonic mapping, Muse-aware `next_actions`, taxonomy *proposals* (L3), a Songs list page separate from the Sound browser, "final ⇒ suggest pin" UI, a Session create op, `night` sessions, `muse_id`/`angle` inputs on `create_draft` (agents find the Song with `list_songs?draft_id=` or `get_song?by=draft`). | Each is an L2/L3 or later decision with its own acceptance. |
| D-L1-32 | **Hidden and retired.** Hiding a Muse removes its row from the Board and `list_muses` (default) but leaves its Songs listable (`list_songs`); hiding a Song removes it from Board counts and lists (default); a retired Song is invisible in every list and count, but `get_song` on a retired id answers 200 with `retired: true` and `retired_into` (L2 slots may still hold the id) and it appears in `verify`. Un-hiding is `hidden: false`. | Reversible shelving without deletion; stable ids for L2. |
| D-L1-33 | **One normaliser.** `textfold.Norm(s)` = collapse-whitespace(lower(`textfold.Fold(s)`)), trimmed (`Fold` keeps case and does not collapse whitespace). It is the only definition behind `muses.key`, `muses.title_fold` (keyset order of the Board and `list_muses`), `sounds.Parse` (before the `-`/`/` replacement) and taxonomy terms; the golden tests use the same function. | Without lower-casing, uppercase titles sort before lowercase ones and one inspiration splits into two Muses. |
| D-L1-34 | **Timestamps are compared as instants.** Pre-M3 columns (`tracks.recorded_at`/`created_at`, `recipe_drafts.created_at`, `import_runs.*`) are 20-character `…05Z` and frozen; M3/L1 columns are 24-character `….000Z`. `store.ParseInstant` (tolerant RFC 3339, any fractional width) is the only reader: `picks.Derive` receives unix-ms instants, session `started_at`/`ended_at` are normalised through `ParseInstant` → `FormatTime` before storing. A string comparison across widths is a bug (`.` sorts before `Z`: a same-second take would look older than an import). | Production rows mix both widths. |
| D-L1-35 | **One settler across processes.** `provenance_lease` (one row: `owner`, `epoch`, `expires_at`; the D-M3-3 fence pattern, but `store.LeaseFence` is bound to `runner_lease` and `store.ErrLeaseLost` means the runner lease, so `provenance` writes its own `epoch` predicate and its own `ErrLeaseLost`/`ErrSettleBusy`) is taken by `Settle`/`Run` for `SettleLeaseTTL = 30 s`, renewed each pass and checked inside both phase transactions (our `epoch`, else `ErrLeaseLost`, nothing written). `Settle` without the lease returns `ErrSettleBusy`. CLI `library reconcile`, `import` and `seed-synthetic` settle only when no server holds the lease, otherwise they enqueue, print the queue and exit 0. Startup waits for the lease and an empty queue before readiness. | `MaxOpenConns(1)` serialises one process only; two processes would double-derive. |
| D-L1-36 | **Draft reference metadata and agents (amendment of M1/M2a).** Today `DraftDetail.Reference` (from `recipe_refs`) is humans-only (`null` for agents) while the backfill copies the same text into Muses that read-scope agents read (D-L1-6): incoherent. Decision, **pending the P.5 box**: `get_draft` shows `reference` to read-scope agents with exactly the text its Muse carries; the contract that defines it is amended in the same commit as L1-B1; `TestDraftReference_AgentView` pins the behaviour (a human sees it, an agent now sees it, an agent never sees more than the Muse holds). The alternative (hide backfilled Muse text from agents) contradicts D-L1-6 and is not planned. | One reading path must not undo another. |
| D-L1-37 | **Derived values never overwrite human ones.** `songs.muse_source` (`reference` \| `manual`, NULL = never set) and `angle_source` (`reference` \| `request_id` \| `manual` \| `cleared`, NULL = never set) mark provenance. The Reconciler fills `muse_id`/`angle` on any pass while the source is NULL: a late `recipe_refs` row reaches an existing Song through the `pd_refs_ai` queue trigger, and a root Song derives its Muse from the root recipe's refs (rendered `varianti`, no draft needed). A set or cleared value (`manual`/`cleared`) is never touched again. When a Song is retired into another, its Muse (and angle) is copied onto the target when the target has none (a logged update, `revision` bumped). | An operator's clear is distinguishable from "never derived"; a mother arriving late does not erase an assigned Muse. |
| D-L1-38 | **Picks are sticky and pins are kept.** A pinned take that becomes ineligible keeps `pinned_take_id`; the Pick falls back for display (reason `pin_ineligible`) and returns to the pin when the take is eligible again (`pin_restored`); only an explicit unpin clears a pin. Ladder Picks use incumbent stickiness (kept while `z ≥ LadderKeepZ = 0.25`, displaced only at `z ≥ LadderChallengeZ = 0.5` over the incumbent; a fresh ladder Pick needs 0.5). Open proposals expire when their take becomes ineligible or the Pick. | L2 Records log every Pick event; global-fit drift must not generate flip-flop events. |
| D-L1-39 | **Scope valve and the tempo lane.** The tempo analyzer (L1-E1) is the one unproven item (validated on synthetic signals only). It is a separable lane: if it slips or fails the real-music spot check, Sounds degrade to "requested only" (no `measured` rows), and **no L1 acceptance item, including `TestAcceptance_L1_BackfillMirror`, depends on it**. Cut candidates if time is short, in this order: the weekly 300-sequence CI job, the Sessions rename/hide panel (the ops stay), `include_hidden` on the Songs/Sessions/Muses lists (the Board keeps it), the `engine` facet. | Keeps L1 shippable on the stable core. |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C87. Packages, dependency order, constants

New packages (all under `internal/`; none may import `api`, `app`, `httpserver`, `generation`, `jobs`,
`library`, `drafts`, `duels`, `ears`, `judgments`, `export*`, `importer`, `notify` — enforced by
`TestImportGraph_L1`):

| Package | Role | May import |
|---|---|---|
| `muses` | Muse model, key (`textfold.Norm`), CRUD, FTS, `EnsureReferenceTx`, `Ref` | `store`, `auth`, `textfold`, `redact` |
| `songs` | Song model, titles, angles, `song_roots` mapping, structural reconcile functions, list/filter/update (the state SQL is `store.SongStateSQL`) | `store`, `auth`, `muses`, `textfold` |
| `sessions` | Session model, `EnsureTx`/`SyncTx`, list/rename | `store`, `auth` |
| `picks` | pure `Derive`, Recompute/Sweep, pin, proposals, events | `store`, `auth`, `ladder` |
| `sounds` | taxonomy (seed, versions, effective index), `Parse`, bands, tempo compare, facet compute, facet counts | `store`, `auth`, `songs`, `engine/yue2`, `textfold` |
| `provenance` | the **Reconciler** (`Settle`, `Run`, `Verify`), queue, step ordering | `store`, `muses`, `songs`, `sessions`, `picks`, `sounds` |
| `songview` | `get_song` view assembly, Muse summaries/coverage (the code that needs picks, sounds, sessions and muses together) | `store`, `auth`, `muses`, `songs`, `sessions`, `picks`, `sounds` |
| `board` | Muse Board read model | `store`, `muses`, `songs`, `sounds` |
| `analysis` (existing) | gains `tempo.go` | `analysis` only |

`api` consumes all of them through `Deps` fields (`Muses *muses.Service`, `Songs *songs.Service`, `SongView *songview.Service`,
`Sessions *sessions.Service`, `Picks *picks.Service`, `Sounds *sounds.Service`, `Board *board.Service`;
nil ⇒ 503 `UNAVAILABLE {component}`, the M2a/M3 convention). `trackctx` reads them through a small
interface it declares (`trackctx.LibraryReader`, C98) so it imports none of them. `app.Build`
constructs and wires everything (`Services` gains `Muses`, `Songs`, `SongView`, `Sessions`, `Picks`, `Sounds`, `Board`, `Provenance`; `app.Options` gains `L1 L1Options{Log, BeforeSettle, FaultAfterPhaseA}` beside `M3`; `internal/app/l1.go` like `m3.go`) and `serve()` — shared by `serve` and `dev` — runs the startup `Settle` and then
`provenance.Service.Run` (C95.3); `mcp-stdio` and the rigs never start it.

Import-graph rules (`TestImportGraph_L1` over a new `l1Rules` table in `internal/archtest/import_graph_test.go`, beside `m3Rules` and `TestImportGraph_RulesBite`; the helper-subtree exemption `helperSubtree` applies):
- the five leaves above, `songview`, `provenance` and `board` import only what the table lists (a leaf never imports `songview`, `provenance` or `board`). Revision 2 removed two cycles: `muses.Summary` needed the Song state SQL (now `store.SongStateSQL`, a constant) and `songs.Get` needed picks/sounds/sessions (now `songview.Get`);
- `export`, `exports`, `notify`, `audit`, `backup`, `importer`, `jobs`, `generation`, `lineage`, `library`,
  `redact` and every future `subsonic*` package **must not reach** `muses`, `songview` or `board` (transitively) — the
  Muse-visibility rule (C99);
- `picks` imports `ladder` (pure types) but not `duels`: the ladder arrives through the interface
  `picks.Ladder` (same method set as `ears.Judgments.Strengths`), wired in `app.Build`.

`store` gains one tiny shared type, `store.Env{Now time.Time; NewID func() string; Actor string; Log *slog.Logger}` (`Log` is the ids-only logger of the structural reconcile, nil = `slog.Default()`; added by L1-B3 for the `song_muse_carried` record)
(`internal/store/env.go`), which every Reconciler-called function takes; the packages alias it (`type Env =
store.Env`). It also gains `store.ParseInstant` (tolerant RFC 3339 parse, D-L1-34) and `store.SongStateSQL` (the shared Song state/queued-jobs SQL fragment, C90.4); `textfold` gains `Norm` (D-L1-33).

**Errors.** The leaves return `*problem.Error` (`problem.E(status, code, msg, details)`, the `duels` convention, ids only in messages and details, C99.4), so
`muses`, `songs`, `sessions`, `picks`, `sounds`, `songview` and `board` may also import `problem`; a stub answers `errors.ErrUnsupported`, which the api maps to 501 `NOT_IMPLEMENTED` with `details.task`
(`TestOps_StubsAnswer501L1` asserts the task name, as `TestOps_StubNotImplemented` does for M2b).

Constants (one file `internal/picks/params.go`, one `internal/sounds/params.go`):
`LadderMinDuels = 3`, `LadderChallengeZ = 0.5`, `LadderKeepZ = 0.25`, `LadderPriorSD = 1.5` (must equal `ladder.Options` default —
`TestPicks_PriorSDMatchesLadder`), `SweepInterval = 1h`, `PollInterval = 1s`, `BatchKeys = 100`,
`MaxPendingReport = 1000`, `SettleLeaseTTL = 30s`, `MaxQueueTries = 5`, `BoardUnassignedChips = 200`, `MaxSongIDsFilter = 200`; tempo band edges 80/110/130/160, `TempoOctaveTol = 0.06`, `TempoAbsTol = 4`,
`TempoRelTol = 0.05`; `MaxMappingsPerCall = 50`.

### C88. Migration `internal/store/migrations/0008_provenance.sql` (full DDL) and write rules

```sql
-- 0008_provenance.sql — L1: Muses, Songs, Picks, Sessions, Sounds, and the reconcile queue.

-- ---- Muses -------------------------------------------------------------------------------------
CREATE TABLE muses (
  id          TEXT PRIMARY KEY,                          -- UUIDv7
  kind        TEXT NOT NULL CHECK (kind IN ('reference','brief')),
  title       TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  title_fold  TEXT NOT NULL CHECK (length(title_fold) BETWEEN 1 AND 400),     -- textfold.Norm(title) (lower-case, whitespace-collapsed); keyset order
  artist      TEXT CHECK (artist IS NULL OR length(artist) BETWEEN 1 AND 200),
  brief       TEXT CHECK (brief IS NULL OR length(brief) BETWEEN 1 AND 2000),
  key         TEXT NOT NULL UNIQUE CHECK (length(key) BETWEEN 1 AND 820),     -- reference: norm(title) || x'1f' || norm(artist or ''); brief: 'brief:' || id
  source      TEXT NOT NULL CHECK (source IN ('recipe_refs','human','agent')),
  created_by  TEXT NOT NULL,                             -- Principal.Key() or 'system:provenance'
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK ((kind = 'brief') = (brief IS NOT NULL)),
  CHECK (kind = 'reference' OR artist IS NULL),
  CHECK (source <> 'recipe_refs' OR kind = 'reference'),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX muses_order ON muses(title_fold, id);
CREATE TRIGGER muses_no_delete BEFORE DELETE ON muses
BEGIN SELECT RAISE(ABORT, 'muses: no physical delete'); END;
CREATE TRIGGER muses_frozen BEFORE UPDATE OF id, kind, source, created_by, created_at ON muses
BEGIN SELECT RAISE(ABORT, 'muses: frozen column'); END;
CREATE TRIGGER muses_brief_key_frozen BEFORE UPDATE OF key ON muses WHEN OLD.kind = 'brief'
BEGIN SELECT RAISE(ABORT, 'muses: frozen column'); END;

CREATE VIRTUAL TABLE muses_fts USING fts5(
  muse_id UNINDEXED, title, artist, brief, tokenize = 'unicode61 remove_diacritics 2');
CREATE TRIGGER muses_fts_ai AFTER INSERT ON muses BEGIN
  INSERT INTO muses_fts(muse_id, title, artist, brief)
  VALUES (NEW.id, NEW.title, COALESCE(NEW.artist, ''), COALESCE(NEW.brief, ''));
END;
CREATE TRIGGER muses_fts_au AFTER UPDATE OF title, artist, brief ON muses BEGIN
  UPDATE muses_fts SET title = NEW.title, artist = COALESCE(NEW.artist, ''), brief = COALESCE(NEW.brief, '')
   WHERE muse_id = NEW.id;
END;

-- ---- Songs -------------------------------------------------------------------------------------
CREATE TABLE songs (
  id            TEXT PRIMARY KEY,                        -- UUIDv7
  root_track_id TEXT UNIQUE REFERENCES tracks(id),       -- the PRIMARY (first) root; NULL while an unrendered draft; always has a song_roots row (Verify I1)
  draft_id      TEXT REFERENCES recipe_drafts(id),
  muse_id       TEXT REFERENCES muses(id),
  muse_source   TEXT CHECK (muse_source IS NULL OR muse_source IN ('reference','manual')),   -- NULL = never set (the Reconciler may fill); 'manual' also marks an operator-cleared Muse (D-L1-37)
  angle         TEXT CHECK (angle IS NULL OR (length(angle) BETWEEN 1 AND 32
                  AND angle GLOB '[a-z]*' AND angle NOT GLOB '*[^a-z0-9-]*')),
  angle_source  TEXT CHECK (angle_source IS NULL OR angle_source IN ('reference','request_id','manual','cleared')),   -- NULL = never set; 'cleared' = angle NULL by an operator
  title         TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  origin        TEXT NOT NULL CHECK (origin IN ('draft','root')),    -- how the Song was first created
  revision      INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),    -- bumped by update_song / assignments only
  created_by    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_by    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  hidden_at     TEXT,
  hidden_by     TEXT,
  retired_at    TEXT,
  retired_into  TEXT REFERENCES songs(id),
  CHECK ((angle IS NULL) = (angle_source IS NULL OR angle_source = 'cleared')),
  CHECK (muse_id IS NULL OR muse_source IS NOT NULL),
  CHECK (root_track_id IS NOT NULL OR draft_id IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL)),
  CHECK ((retired_at IS NULL) = (retired_into IS NULL)),
  CHECK (retired_at IS NULL OR root_track_id IS NULL),
  CHECK (retired_into IS NULL OR retired_into <> id)
);
CREATE UNIQUE INDEX songs_draft_live ON songs(draft_id) WHERE draft_id IS NOT NULL AND retired_at IS NULL;
CREATE INDEX songs_muse   ON songs(muse_id) WHERE retired_at IS NULL;
CREATE INDEX songs_angle  ON songs(angle)   WHERE retired_at IS NULL;
CREATE INDEX songs_live   ON songs(id)      WHERE retired_at IS NULL AND hidden_at IS NULL;
CREATE TRIGGER songs_no_delete BEFORE DELETE ON songs
BEGIN SELECT RAISE(ABORT, 'songs: no physical delete'); END;
CREATE TRIGGER songs_frozen BEFORE UPDATE OF id, origin, created_by, created_at ON songs
BEGIN SELECT RAISE(ABORT, 'songs: frozen column'); END;
CREATE TRIGGER songs_root_once BEFORE UPDATE OF root_track_id ON songs
WHEN OLD.root_track_id IS NOT NULL AND NEW.root_track_id IS NOT OLD.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: root_track_id is set once'); END;
CREATE TRIGGER songs_draft_once BEFORE UPDATE OF draft_id ON songs
WHEN OLD.draft_id IS NOT NULL AND NEW.draft_id IS NOT OLD.draft_id
BEGIN SELECT RAISE(ABORT, 'songs: draft_id is set once'); END;
CREATE TRIGGER songs_retired_final BEFORE UPDATE ON songs WHEN OLD.retired_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'songs: retired songs are immutable'); END;
CREATE TRIGGER songs_root_is_root BEFORE INSERT ON songs
WHEN NEW.root_track_id IS NOT NULL
 AND (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: not a lineage root'); END;
CREATE TRIGGER songs_root_is_root_upd BEFORE UPDATE OF root_track_id ON songs
WHEN NEW.root_track_id IS NOT NULL
 AND (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: not a lineage root'); END;
CREATE TRIGGER songs_retire_target BEFORE UPDATE OF retired_into ON songs
WHEN NEW.retired_into IS NOT NULL
 AND (SELECT retired_at FROM songs WHERE id = NEW.retired_into) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'songs: retire target is retired'); END;
CREATE TRIGGER songs_retire_takeless BEFORE UPDATE OF retired_at ON songs
WHEN NEW.retired_at IS NOT NULL
 AND (OLD.root_track_id IS NOT NULL OR EXISTS (SELECT 1 FROM song_roots WHERE song_id = OLD.id))
BEGIN SELECT RAISE(ABORT, 'songs: a song with takes cannot retire'); END;

CREATE TABLE song_roots (                                -- every lineage root belongs to exactly one Song (D-L1-3)
  root_track_id TEXT PRIMARY KEY REFERENCES tracks(id),
  song_id       TEXT NOT NULL REFERENCES songs(id),
  via           TEXT NOT NULL CHECK (via IN ('own','draft','job_item')),  -- own: created/attached its Song; draft: rendered from the Song's draft; job_item: sibling take of one compose_new item
  attached_by   TEXT NOT NULL,
  attached_at   TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX song_roots_song ON song_roots(song_id);
CREATE TRIGGER song_roots_no_delete BEFORE DELETE ON song_roots
BEGIN SELECT RAISE(ABORT, 'song_roots: no physical delete'); END;
CREATE TRIGGER song_roots_immutable BEFORE UPDATE ON song_roots
BEGIN SELECT RAISE(ABORT, 'song_roots: append-only'); END;
CREATE TRIGGER song_roots_is_root BEFORE INSERT ON song_roots
WHEN (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'song_roots: not a lineage root'); END;
CREATE TRIGGER song_roots_live_song BEFORE INSERT ON song_roots
WHEN (SELECT retired_at FROM songs WHERE id = NEW.song_id) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'song_roots: song is retired'); END;

CREATE VIEW take_songs AS                                -- Take mapping (D-L1-3): derived from song_roots, never stored per take
  SELECT t.id AS track_id, r.song_id AS song_id
    FROM tracks t JOIN song_roots r ON r.root_track_id = t.root_track_id;

-- ---- Picks -------------------------------------------------------------------------------------
CREATE TABLE pick_events (                               -- append-only; the Record changelog source (L2)
  id           TEXT PRIMARY KEY,                         -- UUIDv7 (identity only; the feed cursor is the table's rowid, C91.8)
  song_id      TEXT NOT NULL REFERENCES songs(id),
  seq          INTEGER NOT NULL CHECK (seq >= 1),
  at           TEXT NOT NULL,
  from_take_id TEXT REFERENCES tracks(id),
  to_take_id   TEXT REFERENCES tracks(id),
  from_source  TEXT NOT NULL CHECK (from_source IN ('none','pinned','ladder','stars','newest')),
  to_source    TEXT NOT NULL CHECK (to_source   IN ('none','pinned','ladder','stars','newest')),
  reason       TEXT NOT NULL CHECK (reason IN ('initial','pin','unpin','pin_ineligible','pin_restored','ladder','stars','newest','emptied')),
  actor        TEXT NOT NULL,                            -- Principal.Key() for pin/unpin, else 'system:provenance'
  actor_kind   TEXT NOT NULL CHECK (actor_kind IN ('human','agent','system')),   -- with actor_name: the inputs api.RefFor(viewer, key, kind, name) needs
  actor_name   TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 120),         -- 'provenance' for system
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),     -- mm.pick/1 (C91.3)
  UNIQUE (song_id, seq),
  CHECK ((from_source = 'none') = (from_take_id IS NULL)),
  CHECK ((to_source   = 'none') = (to_take_id   IS NULL)),
  CHECK (from_take_id IS NOT to_take_id OR from_source <> to_source)
);
CREATE INDEX pick_events_song ON pick_events(song_id, seq);
CREATE TRIGGER pick_events_immutable BEFORE UPDATE ON pick_events
BEGIN SELECT RAISE(ABORT, 'pick_events: append-only'); END;
CREATE TRIGGER pick_events_no_delete BEFORE DELETE ON pick_events
BEGIN SELECT RAISE(ABORT, 'pick_events: append-only'); END;
CREATE TRIGGER pick_events_chain BEFORE INSERT ON pick_events
WHEN NEW.seq <> COALESCE((SELECT MAX(seq) FROM pick_events WHERE song_id = NEW.song_id), 0) + 1
  OR COALESCE((SELECT 1 FROM pick_events p WHERE p.song_id = NEW.song_id AND p.seq = NEW.seq - 1
                  AND p.to_source = NEW.from_source AND p.to_take_id IS NEW.from_take_id),
              NEW.seq = 1 AND NEW.from_source = 'none') = 0
BEGIN SELECT RAISE(ABORT, 'pick_events: broken chain'); END;
CREATE TRIGGER pick_events_take_in_song BEFORE INSERT ON pick_events
WHEN NEW.to_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.to_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'pick_events: take is not in the song'); END;

CREATE TABLE song_picks (                                -- current pick; one row per Song that ever had an eligible take
  song_id        TEXT PRIMARY KEY REFERENCES songs(id),
  take_id        TEXT REFERENCES tracks(id),
  source         TEXT NOT NULL CHECK (source IN ('none','pinned','ladder','stars','newest')),
  pinned_take_id TEXT REFERENCES tracks(id),             -- kept while the pinned take is ineligible (D-L1-38); cleared only by an explicit unpin
  pinned_by      TEXT,
  pinned_at      TEXT,
  evidence_json  TEXT NOT NULL CHECK (json_valid(evidence_json)),
  last_event_id  TEXT NOT NULL REFERENCES pick_events(id),
  changed_at     TEXT NOT NULL,                          -- time of last_event_id
  computed_at    TEXT NOT NULL,                          -- last derivation (evidence refresh)
  CHECK ((take_id IS NULL) = (source = 'none')),
  CHECK (source <> 'pinned' OR take_id = pinned_take_id),
  CHECK ((pinned_take_id IS NULL) = (pinned_by IS NULL)),
  CHECK ((pinned_take_id IS NULL) = (pinned_at IS NULL))
) WITHOUT ROWID;
CREATE INDEX song_picks_take ON song_picks(take_id);
CREATE TRIGGER song_picks_no_delete BEFORE DELETE ON song_picks
BEGIN SELECT RAISE(ABORT, 'song_picks: no physical delete'); END;
CREATE TRIGGER song_picks_frozen BEFORE UPDATE OF song_id ON song_picks
BEGIN SELECT RAISE(ABORT, 'song_picks: frozen column'); END;
CREATE TRIGGER song_picks_first_event BEFORE INSERT ON song_picks
WHEN COALESCE((SELECT 1 FROM pick_events e WHERE e.id = NEW.last_event_id AND e.song_id = NEW.song_id
                  AND e.seq = 1 AND e.from_source = 'none' AND e.to_source = NEW.source
                  AND e.to_take_id IS NEW.take_id), 0) = 0
BEGIN SELECT RAISE(ABORT, 'song_picks: a pick needs its first event'); END;
CREATE TRIGGER song_picks_event_required BEFORE UPDATE OF take_id, source, last_event_id ON song_picks
WHEN (NEW.take_id IS NOT OLD.take_id OR NEW.source <> OLD.source OR NEW.last_event_id IS NOT OLD.last_event_id)
 AND COALESCE((SELECT 1 FROM pick_events e WHERE e.id = NEW.last_event_id AND e.song_id = NEW.song_id
                  AND e.from_source = OLD.source AND e.from_take_id IS OLD.take_id
                  AND e.to_source = NEW.source AND e.to_take_id IS NEW.take_id
                  AND e.seq = (SELECT MAX(seq) FROM pick_events x WHERE x.song_id = NEW.song_id)), 0) = 0
BEGIN SELECT RAISE(ABORT, 'song_picks: change needs a matching new event'); END;
CREATE TRIGGER song_picks_take_in_song BEFORE INSERT ON song_picks
WHEN (NEW.take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
            IS NOT NEW.song_id)
  OR (NEW.pinned_take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
            IS NOT NEW.song_id)
BEGIN SELECT RAISE(ABORT, 'song_picks: take is not in the song'); END;
CREATE TRIGGER song_picks_take_in_song_upd BEFORE UPDATE OF take_id, pinned_take_id ON song_picks
WHEN (NEW.take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
            IS NOT NEW.song_id)
  OR (NEW.pinned_take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
            IS NOT NEW.song_id)
BEGIN SELECT RAISE(ABORT, 'song_picks: take is not in the song'); END;

CREATE TABLE pick_proposals (
  id             TEXT PRIMARY KEY,                       -- UUIDv7
  song_id        TEXT NOT NULL REFERENCES songs(id),
  take_id        TEXT NOT NULL REFERENCES tracks(id),
  rationale      TEXT NOT NULL CHECK (length(rationale) BETWEEN 1 AND 500),
  principal_id   TEXT NOT NULL,                          -- Principal.Key() (agents only)
  principal_name TEXT NOT NULL,
  state          TEXT NOT NULL CHECK (state IN ('open','accepted','dismissed','superseded','expired')),   -- expired: the take became ineligible or the Pick (C91.5)
  created_at     TEXT NOT NULL,
  decided_at     TEXT,
  decided_by     TEXT,                                   -- Principal.Key() of the human; the proposing key when superseded; 'system:provenance' when expired
  CHECK ((state = 'open') = (decided_at IS NULL)),
  CHECK ((decided_at IS NULL) = (decided_by IS NULL))
);
CREATE UNIQUE INDEX pick_proposals_one_open ON pick_proposals(song_id, principal_id) WHERE state = 'open';
CREATE INDEX pick_proposals_song ON pick_proposals(song_id, created_at);
CREATE TRIGGER pick_proposals_no_delete BEFORE DELETE ON pick_proposals
BEGIN SELECT RAISE(ABORT, 'pick_proposals: no physical delete'); END;
CREATE TRIGGER pick_proposals_frozen BEFORE UPDATE OF id, song_id, take_id, rationale, principal_id,
  principal_name, created_at ON pick_proposals
BEGIN SELECT RAISE(ABORT, 'pick_proposals: frozen column'); END;
CREATE TRIGGER pick_proposals_decided BEFORE UPDATE ON pick_proposals WHEN OLD.state <> 'open'
BEGIN SELECT RAISE(ABORT, 'pick_proposals: decided proposals are immutable'); END;
CREATE TRIGGER pick_proposals_take_in_song BEFORE INSERT ON pick_proposals
WHEN (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'pick_proposals: take is not in the song'); END;

-- ---- Sessions ----------------------------------------------------------------------------------
CREATE TABLE sessions (
  id          TEXT PRIMARY KEY,                          -- UUIDv7
  kind        TEXT NOT NULL CHECK (kind IN ('import','upload','batch','night')),   -- frozen: a collection's kind is decided once, at first sync (D-L1-13)
  source_key  TEXT UNIQUE CHECK (source_key IS NULL OR length(source_key) BETWEEN 1 AND 300),
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  started_at  TEXT NOT NULL,
  ended_at    TEXT,
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK (ended_at IS NULL OR ended_at >= started_at),    -- both written as FormatTime(ParseInstant(source)) so the string compare is valid (D-L1-34)
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX sessions_started ON sessions(started_at, id);
CREATE TRIGGER sessions_no_delete BEFORE DELETE ON sessions
BEGIN SELECT RAISE(ABORT, 'sessions: no physical delete'); END;
CREATE TRIGGER sessions_frozen BEFORE UPDATE OF id, kind, source_key, created_by, created_at ON sessions
BEGIN SELECT RAISE(ABORT, 'sessions: frozen column'); END;

CREATE TABLE session_members (
  session_id  TEXT NOT NULL REFERENCES sessions(id),
  member_kind TEXT NOT NULL CHECK (member_kind IN ('draft','track')),
  member_id   TEXT NOT NULL,
  added_at    TEXT NOT NULL,
  PRIMARY KEY (session_id, member_kind, member_id)
) WITHOUT ROWID;
CREATE INDEX session_members_member ON session_members(member_kind, member_id);
CREATE TRIGGER session_members_immutable BEFORE UPDATE ON session_members
BEGIN SELECT RAISE(ABORT, 'session_members: append-only'); END;
CREATE TRIGGER session_members_no_delete BEFORE DELETE ON session_members
BEGIN SELECT RAISE(ABORT, 'session_members: append-only'); END;
CREATE TRIGGER session_members_exists BEFORE INSERT ON session_members
WHEN (NEW.member_kind = 'draft' AND NOT EXISTS (SELECT 1 FROM recipe_drafts WHERE id = NEW.member_id))
  OR (NEW.member_kind = 'track' AND NOT EXISTS (SELECT 1 FROM tracks WHERE id = NEW.member_id))
BEGIN SELECT RAISE(ABORT, 'session_members: unknown member'); END;

-- ---- Sounds ------------------------------------------------------------------------------------
CREATE TABLE sound_taxonomy_versions (
  version    INTEGER PRIMARY KEY CHECK (version >= 1),
  kind       TEXT NOT NULL CHECK (kind IN ('seed','extension')),
  seed_id    TEXT UNIQUE,                                -- 'seed/1' … for code seeds; NULL for extensions
  created_by TEXT NOT NULL,                              -- 'system:seed' or Principal.Key()
  created_at TEXT NOT NULL,
  note       TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
  CHECK ((kind = 'seed') = (seed_id IS NOT NULL))
);
CREATE TRIGGER sound_versions_immutable BEFORE UPDATE ON sound_taxonomy_versions
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: append-only'); END;
CREATE TRIGGER sound_versions_no_delete BEFORE DELETE ON sound_taxonomy_versions
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: append-only'); END;
CREATE TRIGGER sound_versions_monotonic BEFORE INSERT ON sound_taxonomy_versions
WHEN NEW.version <> COALESCE((SELECT MAX(version) FROM sound_taxonomy_versions), 0) + 1
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: versions are consecutive'); END;

CREATE TABLE sound_terms (
  version INTEGER NOT NULL REFERENCES sound_taxonomy_versions(version),
  term    TEXT NOT NULL CHECK (length(term) BETWEEN 1 AND 60),      -- textfold.Norm (lower-case, whitespace-collapsed) with - and / replaced by a space
  facet   TEXT NOT NULL CHECK (facet IN ('genre_family','era','vocal','mood','language')),
  value   TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  op      TEXT NOT NULL CHECK (op IN ('add','remove')),
  PRIMARY KEY (version, term, facet, value)
) WITHOUT ROWID;
CREATE INDEX sound_terms_term ON sound_terms(term);
CREATE TRIGGER sound_terms_immutable BEFORE UPDATE ON sound_terms
BEGIN SELECT RAISE(ABORT, 'sound_terms: append-only'); END;
CREATE TRIGGER sound_terms_no_delete BEFORE DELETE ON sound_terms
BEGIN SELECT RAISE(ABORT, 'sound_terms: append-only'); END;
CREATE TRIGGER sound_terms_newest_only BEFORE INSERT ON sound_terms
WHEN NEW.version <> (SELECT MAX(version) FROM sound_taxonomy_versions)
BEGIN SELECT RAISE(ABORT, 'sound_terms: only the newest version accepts terms'); END;

-- Derived caches (rebuilt by the Reconciler; deletable, D-L1-24).
CREATE TABLE take_sounds (
  track_id    TEXT NOT NULL REFERENCES tracks(id),
  facet       TEXT NOT NULL CHECK (facet IN ('genre_family','era','tempo_band','vocal','mood','language','engine')),
  value       TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  origin      TEXT NOT NULL CHECK (origin IN ('style','lyrics','declared','plan','measured','recipe')),
  detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),
  PRIMARY KEY (track_id, facet, value, origin)
) WITHOUT ROWID;
CREATE INDEX take_sounds_facet ON take_sounds(facet, value);

CREATE TABLE song_sounds (
  song_id     TEXT NOT NULL REFERENCES songs(id),
  facet       TEXT NOT NULL CHECK (facet IN ('genre_family','era','tempo_band','vocal','mood','language','engine')),
  value       TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  origin      TEXT NOT NULL CHECK (origin IN ('style','lyrics','declared','plan','measured','recipe')),
  detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),
  PRIMARY KEY (song_id, facet, value)
) WITHOUT ROWID;
CREATE INDEX song_sounds_facet ON song_sounds(facet, value, song_id);

CREATE TABLE sound_state (                               -- freshness of every derived Sound subject
  subject_kind     TEXT NOT NULL CHECK (subject_kind IN ('take','song')),
  subject_id       TEXT NOT NULL,
  taxonomy_version INTEGER NOT NULL,
  input_digest     TEXT NOT NULL,                        -- sha256 over the inputs (C93.4)
  computed_at      TEXT NOT NULL,
  PRIMARY KEY (subject_kind, subject_id)
) WITHOUT ROWID;

CREATE TABLE song_unmapped_terms (                       -- style parts no taxonomy phrase matched (C93.3)
  song_id TEXT NOT NULL REFERENCES songs(id),
  term    TEXT NOT NULL CHECK (length(term) BETWEEN 1 AND 40),
  PRIMARY KEY (song_id, term)
) WITHOUT ROWID;
CREATE INDEX song_unmapped_term ON song_unmapped_terms(term);

-- ---- Reconcile queue (D-L1-4) ------------------------------------------------------------------
CREATE TABLE provenance_dirty (
  kind TEXT NOT NULL CHECK (kind IN ('track','draft','run','collection','batch')),
  id   TEXT NOT NULL,                                    -- track/draft/run/batch id, or the collection name
  at   TEXT NOT NULL,
  tries INTEGER NOT NULL DEFAULT 0 CHECK (tries >= 0),   -- Reconciler re-marks of a transient anomaly (≤ MaxQueueTries, C95.1); reset by every trigger mark
  PRIMARY KEY (kind, id)
) WITHOUT ROWID;

CREATE TABLE provenance_lease (                          -- the cross-process settle fence (D-L1-35); one row, never deleted
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  owner      TEXT NOT NULL,                              -- '' = free
  epoch      INTEGER NOT NULL CHECK (epoch >= 1),        -- bumped on every acquisition; phase transactions check it
  expires_at TEXT NOT NULL
);
CREATE TRIGGER provenance_lease_no_delete BEFORE DELETE ON provenance_lease
BEGIN SELECT RAISE(ABORT, 'provenance_lease: no physical delete'); END;
INSERT INTO provenance_lease(id, owner, epoch, expires_at) VALUES (1, '', 1, '1970-01-01T00:00:00.000Z');

-- Triggers on source tables only enqueue (never write derived rows).
CREATE TRIGGER pd_tracks_ai AFTER INSERT ON tracks BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_tracks_status AFTER UPDATE OF status ON tracks BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_ratings_ai AFTER INSERT ON ratings WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_ratings_au AFTER UPDATE OF stars ON ratings WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_verdicts_ai AFTER INSERT ON duel_verdicts WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', d.slot_a_track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM duels d WHERE d.id = NEW.duel_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', d.slot_b_track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM duels d WHERE d.id = NEW.duel_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_analyses_tempo AFTER INSERT ON analyses WHEN NEW.analyzer = 'tempo' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_drafts_ai AFTER INSERT ON recipe_drafts BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_drafts_au AFTER UPDATE OF parent_track_id, request_json, status, fulfilled_by_track_id, label ON recipe_drafts BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_collections_ai AFTER INSERT ON draft_collections BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('collection', NEW.name, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.draft_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_runs_ai AFTER INSERT ON import_runs BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('run', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_runs_au AFTER UPDATE OF state, finished_at ON import_runs BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('run', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_batches_ai AFTER INSERT ON batches BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_batches_au AFTER UPDATE OF state, finished_at ON batches BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_jobs_au AFTER UPDATE OF track_id ON jobs WHEN NEW.track_id IS NOT NULL BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.batch_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

CREATE TRIGGER pd_refs_ai AFTER INSERT ON recipe_refs BEGIN    -- a late index row reaches existing Songs (D-L1-37)
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'draft', d.id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM recipe_drafts d WHERE d.source_recipe_id = NEW.recipe_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', t.id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM tracks t WHERE t.recipe_id = NEW.recipe_id AND t.generation = 0
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

-- Backfill = the steady-state path: seed the queue from every existing source row (D-L1-4). The `at` values are incidental
-- (one strftime per row); the Reconciler orders by kind rank first, then `at` (C95.1).
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'run', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM import_runs;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'collection', name, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (SELECT DISTINCT name FROM draft_collections);
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'batch', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM batches;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'draft', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM recipe_drafts;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM tracks;
```

**C88.1 Notes on the DDL.** (1) The migration creates no Song, Muse, Session or Sound row: Go-computed ids
(UUIDv7), Muse keys, titles and taxonomy cannot be produced in SQL; the seed statements at the end (queue rows)
and the single free `provenance_lease` row are the only data it writes. (2) `PRAGMA foreign_key_check` is empty after 0008 on a database
upgraded from schema 6 (0007 and 0008 applied together) and from schema 7. (3) `song_picks_event_required`
fires on any change of `take_id`/`source`/`last_event_id`; a pure `evidence_json`/`computed_at` refresh
does not need an event. (4) `sound_terms.facet` excludes `tempo_band` and `engine` (computed, D-L1-15);
`take_sounds`/`song_sounds` include them. (5) The `strftime(...%f...)` width is the same as
`store.FormatTime` (24 chars). (6) Pre-M3 timestamp columns are 20 characters and frozen: L1 never compares them as strings with a new column (D-L1-34). (7) Revision 2 additions: `song_roots` and the three take-in-song triggers that read it (a Song has several roots), `songs.muse_source` and `angle_source = 'cleared'`, `provenance_dirty.tries`, `provenance_lease`, `pd_refs_ai`, `pick_events.actor_kind/actor_name`, reasons `pin_ineligible`/`pin_restored`, proposal state `expired`. (8) `pick_events` is a rowid table on purpose: its `rowid` is the monotonic feed cursor (rows are never deleted, and the single writer assigns `max(rowid)+1` inside the write transaction).

**C88.2 Write rules.** Only the owners below write each table (`TestSQL_WriteOwners_L1` greps non-test Go
for `INSERT INTO|UPDATE|DELETE FROM <table>` outside the owner packages):

| Table | Owner (writes) | Other writers |
|---|---|---|
| `muses`, `muses_fts` (trigger) | `muses` (ops), `muses.EnsureReferenceTx` (called by `songs` reconcile in the Reconciler's tx) | none |
| `songs` structural columns (`root_track_id`, `draft_id`, `retired_*`, `created`, never-set fills of `muse_id`/`muse_source`/`angle`/`angle_source`, initial `title`), `song_roots` | `songs` reconcile functions (Reconciler only) | none |
| `songs` edit columns (`title`, `angle`/`angle_source`, `muse_id`/`muse_source`, `hidden_*`, `revision`) | `songs.Service.Update`, `muses.Service.AssignSongs` | none |
| `song_picks`, `pick_events` | `picks` (Reconciler recompute; pin/unpin ops) | none |
| `pick_proposals` | `picks` | none |
| `sessions`, `session_members` | `sessions.EnsureTx`/`SyncTx` (Reconciler); `sessions.Service.Update` (rename/hide) | none |
| `sound_taxonomy_versions`, `sound_terms` | `sounds` (startup seed; `map_sound_term`) | none |
| `take_sounds`, `song_sounds`, `sound_state`, `song_unmapped_terms` | `sounds` (Reconciler) | none |
| `provenance_dirty` | triggers (enqueue); `provenance` (dequeue, `tries`) | none |
| `provenance_lease` | `provenance` (acquire, renew, release) | none |

**C88.3 `TestNoDeleteSQL` allowlist** gains `provenance_dirty`, `take_sounds`, `song_sounds`, `sound_state`,
`song_unmapped_terms` (the four derived caches and the queue; recorded in M2a C27, D-L1-24). Every other
L1 table is covered by no-delete triggers above.

### C89. Muses (`internal/muses`)

```go
type Kind string // "reference" | "brief"
type Muse struct {
	ID, Kind, Title string; Artist, Brief *string
	Source, CreatedBy, CreatedAt, UpdatedBy, UpdatedAt string
	Revision int; HiddenAt *string
}
// Key is the reference-Muse identity: textfold.Norm(title) + "\x1f" + textfold.Norm(artist) (artist "" when nil).
// Norm = collapse-whitespace(lower(Fold(s))), D-L1-33; title_fold is Norm(title).
func Key(title string, artist *string) string

type NewMuse struct{ Kind Kind; Title string; Artist, Brief *string }
type Patch struct{ Revision int; Title, Artist, Brief *string; Hidden *bool } // reference: title/artist; brief: title/brief
type ListFilter struct{ Q string; Kind Kind; IncludeHidden bool; Limit int; Cursor string }
type Page struct{ Items []Summary; NextCursor *string; Total int }
type Summary struct{ Muse } // list rows; the Song counts (live, non-hidden, non-retired) are added by songview.MuseSummaries (C90.8)

func New(st *store.Store, now func() time.Time, newID func() string) *Service
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewMuse) (Muse, error)
func (s *Service) Get(ctx context.Context, id string) (Muse, error)
func (s *Service) Ref(ctx context.Context, id string) (MuseRef, bool, error) // {id, kind, title, artist}: the only way other packages read Muse text
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Muse, error)
func (s *Service) AssignSongs(ctx context.Context, p auth.Principal, museID string, songIDs []string) (AssignResult, error)
// EnsureReferenceTx gets or creates the reference Muse for (title, artist); the Reconciler calls it inside its transaction.
func EnsureReferenceTx(ctx context.Context, tx *sql.Tx, now time.Time, newID func() string, title string, artist *string, createdBy string) (id string, created bool, err error)
```

**C89.1 Rules.**
- `Create`: `kind=reference` ⇒ `title` required (1..200 after trim/whitespace collapse), `artist` optional
  (1..200), `brief` forbidden; `kind=brief` ⇒ `title` (short label) and `brief` (1..2000) required, `artist`
  forbidden. `source` = `human` for human/device principals, `agent` otherwise. A reference whose key exists
  (hidden or not) ⇒ 409 `MUSE_EXISTS` with `details.muse_id` and `details.hidden`; the caller can read or
  un-hide it. `created_by`/`updated_by` store `Principal.Key()`.
- `Update`: optimistic `revision` (409 `REVISION_CONFLICT` with the current revision, as playlists); `kind` and
  `source` are immutable; changing a reference title/artist recomputes `title_fold` and `key` (collision ⇒
  409 `MUSE_EXISTS`); `hidden: true|false` sets/clears `hidden_at`/`hidden_by`; empty patch ⇒ 422.
- `AssignSongs`: all-or-nothing, 1..200 distinct Song ids, one transaction; every Song must be live (not
  retired; hidden Songs are allowed) else 422 `UNKNOWN_SONG` (`details.song_id`) or 409 `SONG_RETIRED`
  (`details.retired_into`); the Muse must not be hidden (409 `MUSE_HIDDEN`); each changed Song's
  `revision`/`updated_*` are bumped; unchanged Songs are reported, not rewritten (`AssignResult{Assigned,
  Unchanged int}`). `muse_id = NULL` (unassign) is `update_song`'s job. An assignment writes `muse_source = 'manual'` (D-L1-37).
- `List`: `q` is an FTS5 phrase-prefix match over `muses_fts` (user text is quoted token by token; no raw FTS
  syntax is accepted); default order `(title_fold, id)` keyset, page cap 90 000 B; `muses.List` returns rows only; the per-Muse counts
  come from `songview.MuseSummaries` (one grouped query over live non-hidden Songs using `store.SongStateSQL`, C90.4), not N+1.
- `key` and `title_fold` are `textfold.Norm` of the title (and artist): `Key("Mixed CASE  title", "Ärtist") == Key("mixed case title", "artist")`; the keyset order `(title_fold, id)` is case-insensitive.
- No Muse text appears in any returned error message or `details` (ids only), in `slog` records, or in any
  audit field except through the hash (C99).

---

### C90. Songs and the take mapping (`internal/songs`)

```go
type State string // "draft" | "rendered" | "judged" | "picked"   (D-L1-18)
type Song struct {
	ID string; RootTrackID, DraftID, MuseID *string; Angle, AngleSource *string
	Title, Origin string; Revision int; MuseSource *string
	CreatedBy, CreatedAt, UpdatedBy, UpdatedAt string; HiddenAt, RetiredAt, RetiredInto *string
}
type Summary struct {                       // list rows and Board chips share this core
	ID, Title string; Angle, MuseID, DraftID, RootTrackID *string
	State State; Takes int; Pick *PickRef; QueuedJobs, OpenProposals int; Hidden, Archived bool; UpdatedAt string
}
type PickRef struct{ TakeID, Source string }
type ListFilter struct {
	MuseID string; NoMuse bool; Angle string; States []State; Queued *bool; SessionID string
	Sounds map[string][]string // facet → values (OR inside a facet, AND across facets)
	DraftID, TrackID string    // the single Song of a draft / of a take (any generation): agents find a Song before they know its id
	HasOpenProposals *bool
	TempoMismatch *bool; Q string; HasPick *bool
	IncludeHidden, IncludeArchived bool; Limit int; Cursor string
}
type Page struct{ Items []Summary; NextCursor *string; Total int }
type Patch struct{ Revision int; Title *string; Angle OptString; MuseID OptString; Hidden *bool } // OptString: unset | null | value

func New(st *store.Store, now func() time.Time, newID func() string) *Service
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)
// Get (get_song) lives in songview (C90.8): it needs picks, sounds and sessions, which songs may not import.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Song, error)
func FilterSQL(f ListFilter, excludeFacet string) (where string, args []any) // reused by sounds facet counts
// Structural reconcile (Reconciler only, inside its transaction):
func ReconcileTracksTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs []string) (Changes, error)
func ReconcileDraftsTx(ctx context.Context, tx *sql.Tx, env Env, draftIDs []string) (Changes, error)
type Env = store.Env // {Now time.Time; NewID func() string; Actor string}; the Reconciler passes Actor "system:provenance"
func DefaultTitle(label, groupKey, requestID string, angle *string) string
func Humanise(requestID string) string
func AngleSlug(s string) (string, bool)
// Ladder and Flags (the Strengths / FlagCodes interfaces) are declared in songview, which is their only consumer.
```

**C90.1 `ReconcileTracksTx(trackIDs)`** — for each distinct lineage root `r` of the given takes (`root_track_id`) that has **no
`song_roots` row**, in `(generation, recorded_at instant, id)` order (so one root creates a Song and its siblings find it):
1. **The Song of the draft that produced `r`.** `d` = the draft with `fulfilled_by_track_id = r`, or the `jobs.draft_id` of the
   job with `jobs.track_id = r` (M3), when `d` is **motherless**. *The Song of `d`* is its live Song (`songs.draft_id = d`,
   `retired_at IS NULL`), else the Song that owns `d.fulfilled_by_track_id` through `song_roots`. Found ⇒ `INSERT song_roots(r, S,
   'draft')`; when `S.root_track_id IS NULL`, also `UPDATE songs SET root_track_id = r WHERE id = S AND root_track_id IS NULL`
   (becomes the primary root; revision unchanged; the Song keeps its Muse, angle and title). Not found but `d` exists, is motherless and has no
   live Song ⇒ **create it now** (`origin 'draft'`, `root_track_id = r`, `draft_id = d`, Muse/angle by C90.3) and insert
   `song_roots(r, new, 'draft')`, so the other roots of the same pass find it. This covers `from_draft` with `n > 1` (the
   draft seed plus `n−1` new seeds, all motherless roots), re-rendering an already fulfilled draft (new recipe, another root)
   and a draft whose Song was created before its first render. `songs.draft_id` is never changed once set.
2. **The Song of the job item (`compose_new`).** `r` produced by a job with `draft_id IS NULL AND source_track_id IS NULL`:
   its **job-item key** is `(jobs.batch_id, item_index)` where `item_index = json_extract(estimates.items_json, '$[' || jobs.take_index || '].item_index')` over the batch's estimate (`batches.estimate_id`; M3 persists the expanded plan as an array of `planTake` whose position is `take_index` and which carries `item_index`, C71.4) — exact, so no style/lyrics hash is needed (L1-B3 pins it with a `compose_new` n = 3 batch and with two compose items of identical style and lyrics, which must stay two Songs). A root of the same key that already has a Song ⇒ `INSERT
   song_roots(r, that Song, 'job_item')`; otherwise step 3 creates the Song and later roots of the key attach to it.
3. **Otherwise** `INSERT` a Song `(root_track_id = r, draft_id NULL, muse_id NULL, angle NULL, title =` the root track's title`,
   origin 'root', created_by = env.Actor)` and `song_roots(r, new, 'own')`.
4. **Muse of a root Song** (D-L1-37): for the Song of `r` with `muse_source IS NULL`, the first non-empty `source_title` among the
   `recipe_refs` of `tracks.recipe_id` of `r` (ordered by `source`) ⇒ `EnsureReferenceTx` and `SET muse_id, muse_source =
   'reference'` — a rendered-`varianti` root whose recipe carries an index row gets its Muse here, with or without a draft.
A non-root take never creates a Song. After a pass every track has exactly one `take_songs` row (Verify I1). In a pass,
tracks are reconciled before drafts, and the Reconciler selects `track` keys before `draft` keys across passes (C95.1).

**C90.2 `ReconcileDraftsTx(draftIDs)`** — Reconciler processes tracks first, then drafts. For each draft `d`
compute its **target**:
- `parent_track_id = M` not NULL ⇒ `lineage(root(M))`;
- else `fulfilled_by_track_id = t` with `t.generation > 0` ⇒ `lineage(root(t))`;
- else `own`.

`lineage(r)`: let `S_r` = the Song with `root_track_id = r` (it exists once tracks were reconciled; if not,
skip and report `anomaly: song_missing_for_root` — a **transient** anomaly: the draft key is re-marked, never dequeued, C95.1; `Verify` also reports it). If `d` has a live Song `S_d`
with `root_track_id IS NULL` ⇒ **retire** it: `retired_at = now, retired_into = S_r.id` (`S_d.id ≠ S_r.id`). In the same transaction, when `S_r.muse_source IS NULL` and `S_d.muse_source IS NOT NULL`, copy `muse_id`/`muse_source` onto `S_r` (and `angle`/`angle_source` when `S_r.angle_source IS NULL`), bump `S_r.revision` and `updated_*` (`updated_by = system:provenance`) and emit one `slog` record `song_muse_carried` (ids only, no Muse text). The retired row keeps its own values.
If `S_d` already has a root (cannot normally happen: fulfilled drafts are immutable) ⇒ leave it and report
`anomaly: draft_song_has_root`.

`own`:
- a live Song with `draft_id = d.id` exists ⇒ only the **never-set fill** of C90.3 (human-set and operator-cleared values stay);
- else, `d` is fulfilled by a **root** take `t`: if `S_t` (root = t) exists and `S_t.draft_id IS NULL` ⇒
  attach (`SET draft_id = d.id`) and fill `muse_id`/`muse_source`/`angle`/`angle_source`
  from the draft's refs by the never-set rule (C90.3); if `S_t` does not exist ⇒ `INSERT` a Song with both
  `root_track_id = t` and `draft_id = d.id` (origin `draft`); if `S_t.draft_id` is another draft ⇒ nothing (its roots already belong to that Song; the second draft is not a Song);
- else ⇒ `INSERT` a Song `(draft_id = d.id, root NULL, origin 'draft', title = DefaultTitle(label,
  request_id, angle), muse/angle from C90.3)`.

A draft that regained "own" after being retired gets a **new** Song (the retired row keeps the old
`draft_id`; the partial unique index only constrains live rows).

**C90.3 Muse and angle of a Song, never-set rule** (D-L1-37: written when the Song row is created or attached **and on any later pass while `muse_source` / `angle_source` is NULL**; a value an operator or agent set or cleared is never touched; a late `recipe_refs` insert re-queues the draft through `pd_refs_ai`):
- *Muse:* the draft's `source_recipe_id` → `recipe_refs` rows of that recipe ordered by `source`; the first
  whose `ref_json` has a non-empty `source_title` gives `(title, artist)` (whitespace collapsed; empty
  artist ⇒ NULL) → `muses.EnsureReferenceTx(..., createdBy = env.Actor)` and `muse_source = 'reference'`. No ref row ⇒ no Muse (D-L1-28).
- *Angle:* that ref row's `variant_type` (`angle_source = 'reference'`) when it satisfies `AngleSlug`; else
  when the draft's `request_id` matches `_v(\d{1,3})$` and the **suffix map** has an entry for the number
  (`angle_source = 'request_id'`); else none. The suffix map is built once per Reconciler pass from
  `recipe_refs JOIN recipe_drafts ON source_recipe_id = recipe_id`: for each numeric suffix the set of
  distinct non-empty `variant_type`s; an entry exists iff the set has exactly one member.
- `AngleSlug(s)` lowercases, replaces `_`/space with `-`, strips everything outside `[a-z0-9-]`, collapses
  `-`, trims `-`, cuts to 32 and reports ok iff the result matches `^[a-z][a-z0-9-]*$`.

**C90.4 Song state SQL** (one SQL fragment, `store.SongStateSQL`, shared by `songs.List`, `board` and `songview`; derived, never
stored): `takes` = count of `take_songs`; `judged` = EXISTS(a human `ratings` row with stars on a take of the
Song) OR EXISTS(a human `duel_verdicts` row on a duel with a slot in the Song) OR EXISTS(a take with
`status <> 'draft'`); `picked` = `song_picks.source IN ('pinned','ladder')`; `queued_jobs` = count of `jobs`
with `state IN ('queued','submitting','starting','generating','transferring','ingesting')` (an unconfirmed batch's jobs, state `awaiting_confirmation`, and terminal jobs do **not** count) and (`draft_id = song.draft_id` or
`source_track_id` ∈ the Song's takes); `open_proposals` = count of `pick_proposals` in state `open` — 0 when `jobs` is absent; `archived` = the Song has no takes and its
draft is `archived`. The state is the first of `picked`, `judged`, `rendered` (takes > 0), else `draft`.

**C90.5 `Update`** — optimistic `revision` (409 `REVISION_CONFLICT`); retired Song ⇒ 409 `SONG_RETIRED`
(`details.retired_into`); `title` 1..200 (trimmed); `angle` unset/null/value (value through `AngleSlug`
must equal the input after trim-lowercase, else 422 `INVALID_ANGLE`; setting it writes
`angle_source = 'manual'`; null sets `angle` NULL with `angle_source = 'cleared'`, which the Reconciler never refills); `muse_id` unset/null/id (unknown ⇒ 422 `UNKNOWN_MUSE`, hidden
Muse ⇒ 409 `MUSE_HIDDEN`; any set or clear writes `muse_source = 'manual'`); `hidden` sets/clears `hidden_at`/`hidden_by`; empty patch ⇒ 422. Bumps
`revision`, `updated_at`, `updated_by`.

**C90.6 `Get` (`get_song`, `songview.Service.Get`)** — `GetOptions{By (song|draft|track, default song), TakeLimit int (default 24, max 100),
IncludeEvents int (default 10, max 50)}`. `id` is a Song id, or with `by=draft` a draft id (its live Song, else the Song owning its
fulfilling root), or with `by=track` a take id (through `take_songs`). Unknown id ⇒ 404 `NOT_FOUND` (with `details.pending: true`
when the draft/take exists but its key is still queued, so an agent retries after a Reconciler pass). A **retired** Song answers
**200** with `song.retired = true` and `song.retired_into` and empty takes/pick/events (L2 slots may still hold the id); the 409
`SONG_RETIRED` stays on write ops only. `View`:
```
View {
  song: SongCore{id, title, angle, angle_source, origin, revision, state, hidden, archived, queued_jobs,
                 created_at, updated_at, root_track_id, root_track_ids, draft_id, retired, retired_into},
  muse: MuseRef | null,                               // {id, kind, title, artist}  (visible to read scope, D-L1-6)
  draft: {id, status, revision, label, request_id, group_key} | null,
  takes: TakeCard[] (≤ take_limit, order: pick first, then recorded_at desc as parsed instants, id desc), takes_total,
  pick: PickView | null,                              // C91.4
  proposals: PickProposal[] (open ones),
  events: PickEvent[] (newest first, ≤ include_events),
  sounds: {taxonomy_version, facets: {facet: [{value, origin}]}, tempo: TempoDetail | null, unmapped: string[] (≤ 10)},
  sessions: [{id, kind, name}] (≤ 10, newest first),
  lineage: {root_track_id, generations, children_total} | null,
  pending: bool                                       // the Song's picks/sounds are queued for recompute
}
TakeCard {
  id, title, status, generation, parent_track_id, origin, duration_s, recorded_at, lossy, recipe_known,
  stars (human), critic_stars, is_pick, pinned, has_ears: bool, flags: string[] (Ears flag codes),
  ladder: {u, sd, n_duels} | null, tempo: {requested_bpm, measured_bpm} | null, cost_display_usd | null
}
```
`flags` come from `Flags.FlagCodes` (an `ears.Service` method added by L1-E: batch, read-only, no
analysis is started), `ladder` from `Ladder.Strengths` (global blind-human fit; null for takes without a
verdict). A nil `Ladder`/`Flags` yields nulls/empty (never an error); both interfaces are declared in `songview`. Muse text appears only in `muse` (and collection-derived Session names, which are Muse-adjacent text, in `sessions`, C99).

**C90.7 `List`** — order `(created_at DESC, id DESC)` keyset over `songs.id` (UUIDv7); default excludes hidden,
retired and archived Songs; `Q` is a case-insensitive substring on `title` (`LIKE` with escaping); sound
facets via `FilterSQL` (`EXISTS` on `song_sounds`); `TempoMismatch` via `json_extract(detail_json,
'$.mismatch')` on the `tempo_band` row; `SessionID` via the derived membership (`session_members` draft/
track rows joined to the Song's draft or takes); page cap 90 000 B, `Total` is the unpaged count. `DraftID` / `TrackID` return the single Song of that draft (live, else the owner of its fulfilling root) or of that take (at most one item, so an agent finds the Song it must pass to `assign_muse_songs` without waiting for an id from elsewhere); `HasOpenProposals` = EXISTS(an `open` `pick_proposals` row).

**C90.8 `songview` (`internal/songview`)** — the read assembly that needs several leaves at once: `Service.Get` (C90.6; declares `Ladder` and `Flags`), `MuseSummaries(ctx, []muses.Muse) []MuseSummary` (per-Muse `songs, draft, rendered, judged, picked, queued_jobs` in one grouped query over `store.SongStateSQL`) and `MuseCoverage(ctx, museID)`. `songview` reads Muse text only through `muses.Service.Ref/Get`, never through SQL, so `TestSQL_MuseTables_Allowlist` needs no exception for it. Constructor: `songview.New(st, muses, songs, sessions, picks, sounds, ladder, flags, now)`.

### C91. Picks (`internal/picks`)

```go
type Source string // "none" | "pinned" | "ladder" | "stars" | "newest"
type TakeInput struct {
	ID, Status string
	RecordedAt, CreatedAt int64 // unix ms from store.ParseInstant (pre-M3 20-char and M3 24-char timestamps mix; D-L1-34)
	HumanStars *int
	U, SD *float64 // ladder strength; nil ⇒ not in the fit
}
type Input struct {
	Takes []TakeInput        // every take of the Song (eligibility is decided inside)
	PinnedTakeID *string         // kept even while ineligible (D-L1-38)
	IncumbentTakeID *string      // the current Pick take when its source is 'ladder' (stickiness), else nil
	IntraDuels int           // counted human blind verdicts on duels whose slots are both takes of the Song
}
type Result struct { TakeID *string; Source Source; Evidence Evidence; PinIneligible bool }
func Derive(in Input) Result   // pure; no I/O; deterministic

type Ladder interface{ Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error) }
func New(st *store.Store, l Ladder, now func() time.Time, newID func() string) *Service
func (s *Service) RecomputeTx(ctx context.Context, tx *sql.Tx, env Env, songIDs []string, st map[string]ladder.Strength) (Changes, error)
func (s *Service) Sweep(ctx context.Context) (Changes, error)            // C91.5
func (s *Service) Get(ctx context.Context, songID string) (PickView, error)
func (s *Service) Pin(ctx context.Context, p auth.Principal, songID string, takeID, proposalID *string) (PinResult, error)
func (s *Service) Propose(ctx context.Context, p auth.Principal, songID, takeID, rationale string) (Proposal, error)
func (s *Service) Dismiss(ctx context.Context, p auth.Principal, proposalID string) (Proposal, error)
func (s *Service) Events(ctx context.Context, f EventFilter) (EventPage, error)
```

**C91.1 `Derive`** (D-L1-9, D-L1-38). Eligible takes = `Status <> 'discarded'`. Steps:
1. `PinnedTakeID` set and eligible ⇒ `{pinned}`. Set but not eligible (discarded/absent) ⇒ **the pin is kept**; continue with the fallback
   and report `PinIneligible = true` (evidence `pin_ineligible{take_id, status}`); nothing is cleared. The pin applies again as soon as
   the take is eligible.
2. **Ladder with incumbent stickiness:** applies when `IntraDuels ≥ LadderMinDuels` and ≥ 2 eligible takes. Each eligible take's strength
   is `(u, sd)` when both set, else `(0, LadderPriorSD)`. Sort by `u` descending, ties by newest (step 4 order). Leader `a`, runner-up
   `b`; `z(x, y) = (x.u − y.u) / √(x.sd² + y.sd²)`, compared with a `1e-9` tolerance. Let `i` = `IncumbentTakeID` when that take is
   eligible. Then:
   - no `i` ⇒ ladder decides iff `z(a, b) ≥ LadderChallengeZ` ⇒ `{a, ladder}`;
   - `i = a` ⇒ the ladder keeps `a` iff `z(a, b) ≥ LadderKeepZ` (0.25) ⇒ `{a, ladder}`;
   - `i ≠ a` ⇒ `a` displaces `i` iff `z(a, i) ≥ LadderChallengeZ` **and** `z(a, b) ≥ LadderChallengeZ` ⇒ `{a, ladder}`; if `z(a, i) < LadderChallengeZ`
     the incumbent holds ⇒ `{i, ladder}` (evidence `incumbent{id, held: true, z_challenge}`); otherwise fall through.
   Failing cases fall through; the evidence records the failed margin.
3. **Stars:** among eligible takes with `HumanStars ≠ nil`, the maximum stars; several ⇒ the newest of them ⇒ `{that take, stars}`. No rated
   eligible take ⇒ continue.
4. **Newest:** the eligible take with the greatest `(RecordedAt, CreatedAt, ID)` (the two timestamps are instants; ids break ties) ⇒
   `{newest}`. No eligible take ⇒ `{nil, none}`.

**C91.2 Event reason.** Given the previous `(take, source)` (initial `(nil, none)`) and the new one: new source `pinned` ⇒ `pin_restored` when
`song_picks.pinned_take_id` already equalled that take before this recompute (the pin was kept while the take was ineligible), else `pin`; previous `pinned` and new not pinned ⇒ `unpin` (explicit unpin through
`Pin` with a nil take) or `pin_ineligible` (`Derive` reported `PinIneligible`; the pin itself stays); new `none` ⇒ `emptied`; previous
`none` ⇒ `initial`; otherwise the new source name (`ladder`/`stars`/`newest`). An event is written iff `(take, source)` differs. The
actor columns are `(actor, actor_kind, actor_name)` = the pinning principal for `pin`/`unpin`, else `('system:provenance', 'system',
'provenance')`.

**C91.3 Evidence (`mm.pick/1`).** Always `{"def":"mm.pick/1","rule":<source>,"eligible":n}` plus per rule:
`pinned` ⇒ `pinned_by`, `pinned_at`, optional `proposal_id`; `ladder` ⇒ `duels_n`, `leader{id,u,sd}`,
`runner_up{id,u,sd}`, `z`, `min_z`, `min_duels`; `stars` ⇒ `stars`, `tied` (how many takes shared it);
`newest` ⇒ `recorded_at`; when the ladder was considered and failed, `ladder_skipped{duels_n, z|null,
reason: "few_duels"|"margin"|"single_take"}`; `incumbent{id, held, z_keep|z_challenge}` when an incumbent was involved; `pin_ineligible{take_id, status}` when the pin is kept but skipped. Numbers are rounded to 4 decimals like `ladder.Strength`. Golden
files pin the JSON.

**C91.4 `PickView`** `{take_id|null, source, pinned_take_id|null, pin_ineligible: bool, pinned_by (api.RefFor), pinned_at, evidence,
changed_at, last_event_id, seq, pending: bool, open_proposals: int}`. `pending` is true when a dirty
`track` row exists for any take of the Song (a single indexed query), so a reader knows the pick may lag
by ≤ one Reconciler pass (D-L1-4).

**C91.5 Recompute and sweep.** `RecomputeTx` loads, for each Song, its takes (`take_songs` ⋈ `tracks`,
human stars from `ratings` where `source = 'human'`), `IntraDuels` from
`SELECT COUNT(*) FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id WHERE v.source = 'human' AND d.blind = 1
AND d.slot_a_track_id IN (…) AND d.slot_b_track_id IN (…)` (the ids are the Song's takes), `PinnedTakeID` from
`song_picks`, and the strengths the caller fetched **before** the transaction from `Ladder.Strengths`
(one call for every take of the batch's Songs; the ladder is cached by `duels`). It then runs `Derive`;
an unchanged `(take, source)` only refreshes `evidence_json`/`computed_at`; a change inserts the
`pick_events` row (`seq = max+1`, actor `system:provenance` or the pinning principal) and then updates or
inserts `song_picks` with `last_event_id` (the triggers of C88 verify the pair). `PinIneligible` clears nothing. In the same transaction **open proposals expire** (`state 'expired'`, `decided_by 'system:provenance'`) when
their take is now ineligible or is the current Pick, so the 20-open cap never fills with dead rows and Accept never meets
`TAKE_NOT_ELIGIBLE`. `IncumbentTakeID` is read from `song_picks` (source `ladder`). Songs with no eligible take and no `song_picks` row get no row. `Sweep` recomputes every live
Song with ≥ 2 eligible takes and ≥ 1 counted human verdict, in batches of `BatchKeys` Songs.

**C91.6 `Pin`** (one write transaction, strengths fetched before it): Song live (else 404/409 as C90.5);
`takeID` nil ⇒ clear the pin (no pin set ⇒ 200 unchanged, no event) and recompute; else the take must be
a take of the Song (422 `TAKE_NOT_IN_SONG`) and eligible (422 `TAKE_NOT_ELIGIBLE`, details status); set
`pinned_take_id/by/at` (creating the `song_picks` row through the first event when absent; an explicit unpin is the only operation that clears `pinned_*`), recompute with
the new pin, and — when `proposalID` is given — require it to be an **open** proposal of this Song for this
take (409 `PROPOSAL_MISMATCH`, 409 `PROPOSAL_CLOSED`) and close it `accepted` (`decided_by` = the human
key). The pin of a take that is already the pinned pick is a 200 no-op. A non-`ActsAsHuman` principal ⇒ 403
`HUMAN_ONLY` before any read (the handler's first statement).

**C91.7 `Propose`** — principal must not `ActsAsHuman` (humans pin; 403 `HUMAN_ONLY` with the hint
`use pin_pick`); take in Song and eligible (as above); the take already **is** the current pick ⇒ 409
`ALREADY_PICK`; rationale 1..500 (trimmed); an existing open proposal of the same principal for the Song
becomes `superseded` (`decided_by` = that key) in the same transaction; at most 20 open proposals per
Song (409 `TOO_MANY_PROPOSALS`). A proposal is also closed by the Reconciler as `expired` (C91.5), never by a request. `Dismiss` — `ActsAsHuman` only (403 `HUMAN_ONLY`), open proposal only
(409 `PROPOSAL_CLOSED`), closes it `dismissed`.

**C91.8 `Events`** — `EventFilter{SongID string, SongIDs []string (≤ MaxSongIDsFilter = 200; unioned with SongID, so an L2 Record filters by the Songs of its slots), After int64
(the previous page's `cursor`, exclusive), Limit 1..200 (default 100)}`; ascending by the integer **`cursor` = `pick_events.rowid`**
(monotonic: rows are never deleted and the single writer assigns `max(rowid)+1` inside the write transaction; the UUIDv7 `id` is identity
only and is **not** the cursor); item `{cursor, id, song_id, seq, at, from_take_id, to_take_id, from_source, to_source, reason, actor
(api.RefFor(viewer, actor, actor_kind, actor_name)), evidence}`; `next_cursor` = the last item's cursor when more remain. Event rows never
contain titles or Muse text.

---

### C92. Sessions (`internal/sessions`)

```go
type Key struct{ Kind, SourceKey, Name, StartedAt string; EndedAt *string }
func EnsureTx(ctx context.Context, tx *sql.Tx, env Env, k Key) (id string, created bool, err error)  // get-or-create by SourceKey; refreshes started/ended of auto sessions, never name
func AddMembersTx(ctx context.Context, tx *sql.Tx, env Env, sessionID, memberKind string, ids []string) (added int, err error)
type Stats struct{ Created, MembersAdded int }                      // what a sync wrote, for the Reconciler report (L1-B3)
func SyncRunsTx(ctx, tx, env, runIDs []string) (Stats, error)
func SyncCollectionsTx(ctx, tx, env, names []string) (Stats, error)
func SyncBatchesTx(ctx, tx, env, batchIDs []string) (Stats, error)
func New(st *store.Store, now func() time.Time) *Service
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)           // Kind, Q, IncludeHidden, Limit, Cursor
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Session, error) // Revision, Name, Hidden
```

**C92.1 Sync rules (D-L1-13).**
- *Import run `r`:* source key `import:<r>`, kind `import`, name `Import <YYYY-MM-DD HH:MMZ>` of `started_at`;
  started = run `started_at`; ended = `finished_at` (NULL while `running`); every stored session timestamp is `FormatTime(ParseInstant(source))` (D-L1-34: pre-M3 sources are 20 characters wide); members: `tracks` with
  `import_run_id = r` (kind `track`) and drafts whose **first** `recipe_sources` occurrence belongs to `r`
  (`(SELECT import_run_id FROM recipe_sources WHERE recipe_id = d.source_recipe_id ORDER BY id LIMIT 1) = r`,
  kind `draft`).
- *Collection `c`:* source key `collection:<c>`, name `c` cut to 120 runes, kind decided **once at first
  sync** (`upload` iff every draft of the collection at that moment has `origin = 'jsonl_upload'`, else `import`; frozen, never recomputed as drafts join); started/ended = the min/max draft `created_at` **by parsed instant**;
  members: the collection's drafts.
- *Batch `b` (M3):* **only when `state IN ('active','finished')` and the batch has ≥ 1 job** (rejected, expired and awaiting batches create no session; a batch that later becomes active gets one on its next dirty mark); source key `batch:<b>`, kind `batch`, name `Batch <YYYY-MM-DD HH:MMZ> · <n_takes> take(s)`;
  started = `created_at`; ended = `finished_at` (NULL while live); members: `jobs.draft_id` (draft) and
  `jobs.track_id` (track) of the batch, where not NULL.
- Collection names can embed inspirations: a session `name` is Muse-adjacent text (D-L1-6) — returned by `list_sessions`, `update_session` and `get_song`, never written to logs, audit text, job events, notifications or exports (canary-tested, C99).
- Membership inserts are `INSERT OR IGNORE`; sessions are never recreated; a renamed session keeps its name;
  a hidden session keeps collecting members.
- A dirty `track` also syncs its import run and its job's batch; a dirty `draft` also syncs its collections
  and its source run (each distinct session once per pass).

**C92.2 `Update`** — `revision` lock (409 `REVISION_CONFLICT`), `name` 1..120 trimmed, `hidden`; empty patch ⇒
422; there is no create/delete op (D-L1-31). **`List`** items `{id, kind, name, started_at, ended_at, hidden,
revision, drafts, takes, songs}` (`songs` = distinct live Songs reached by membership; one grouped query),
order `(started_at DESC, id DESC)` keyset, page cap 90 000 B.

---

### C93. Sounds (`internal/sounds`)

```go
// Facets and their allowed values (Go-validated; the DB CHECKs only constrain the facet name).
var Facets = []string{"genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"}
type Match struct{ Term, Facet, Value string }
type Parsed struct{ Matches []Match; Unmapped []string; BPM int; Language string }
type Index struct{ Version int; /* unexported: folded phrase → []{facet,value} */ }
func BuildIndex(version int, rows []TermRow) *Index              // fold of add/remove rows ≤ version
func Parse(ix *Index, style string) Parsed                        // pure
func TempoBand(bpm float64) string                                // slow|mid|upbeat|fast|rapid
func CompareTempo(requested, measured *float64) (mismatch, octave bool)
type TempoDetail struct {
	RequestedBPM, PlanBPM, MeasuredBPM *float64 `json:"…"`; Basis string `json:"basis" enum:"measured,declared,plan"`
	Mismatch, Octave bool
}
func New(st *store.Store, now func() time.Time, newID func() string) *Service
func (s *Service) EnsureSeed(ctx context.Context) error           // startup: applies every unapplied code seed as one version
func (s *Service) Index(ctx context.Context) (*Index, error)      // cached per version
func (s *Service) Facets(ctx context.Context, f songs.ListFilter) (FacetCounts, error)
func (s *Service) Taxonomy(ctx context.Context, q TaxonomyQuery) (TaxonomyPage, error)
func (s *Service) Map(ctx context.Context, p auth.Principal, req MapRequest) (MapResult, error)
// Reconciler steps (C95.1 phase B; Env = store.Env):
func (s *Service) ComputeTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs, songIDs []string) (Changes, error) // dirty takes + affected Songs; skips fresh subjects by digest
func (s *Service) SweepStaleTx(ctx context.Context, tx *sql.Tx, env Env, limit int) (n int, err error)               // stale digest or taxonomy version; startup, after Map, hourly
```

**C93.1 Seed (`seed/1`).** `internal/sounds/seed.go` holds the table `(term → facet → value)`; values per
facet: `genre_family` — `pop rock hip-hop electronic dance reggae funk-soul r-and-b jazz blues folk country
classical latin world metal punk ambient cinematic singer-songwriter` (20); `era` — `50s 60s 70s 80s 90s 00s
10s 20s`; `vocal` — `male female duet choir rap instrumental`; `mood` — `upbeat chill melancholic romantic
dark dreamy energetic nostalgic aggressive warm`; `language` — `en it es fr de pt`. The seed lists singular and
plural/variant forms as separate terms (`female vocal`, `female vocals`, `female vocalist`, `female singer`)
because matching is whole-word. Representative rows (the full table is ≥ 150 terms and pinned by
`testdata/seed_v1.golden.json`): `reggae`, `roots reggae`, `dub` → genre `reggae`; `eurodance`, `italo disco`,
`house` → genre `dance`; `synthpop`, `synth pop` → genre `pop` **and** `electronic`; `trap` → `hip-hop`;
`90s`, `nineties`, `1990s` → era `90s`; `male vocal(s)`, `male singer` → vocal `male`; `rap`, `rapping` → vocal
`rap` **and** genre `hip-hop`; `acoustic guitar` → genre `folk`; `melancholic`, `sad`, `wistful` → mood
`melancholic`; `italian` → language `it`. Terms are stored `textfold.Norm`-ed (D-L1-33) with `-` and `/` replaced by a space
and whitespace re-collapsed (so `hip-hop` ≡ `Hip Hop` ≡ `hip hop`). Adding a facet value requires a seed revision, never a
silent edit (the golden test fails).

**C93.2 Versions.** `EnsureSeed` inserts, for each code seed whose `seed_id` is absent, one
`sound_taxonomy_versions` row (`kind 'seed'`, `created_by 'system:seed'`) and its `sound_terms` (`op 'add'`) in one
transaction (`INSERT … ON CONFLICT DO NOTHING`; concurrent starters are safe). `Map` inserts an `extension`
version plus ≤ `MaxMappingsPerCall` rows (`op add|remove`; removing a mapping that is not effective ⇒ 422
`MAPPING_NOT_EFFECTIVE`; adding one that already is ⇒ 200 reported as `unchanged`; an all-unchanged call
creates **no** version). Validation: `term` 1..60 after folding and containing a letter (422 `INVALID_TERM`);
`facet` ∈ the five term-derived facets (`tempo_band`/`engine` ⇒ 422 `FACET_NOT_MAPPABLE`); `value` per facet —
`era` `^(5|6|7|8|9|0|1|2)0s$`, `language` `^[a-z]{2}$`, others `^[a-z][a-z0-9-]{0,39}$`. After the insert the
call recomputes every Sound subject whose input digest used an older taxonomy version **synchronously** (≤ 5 s
budget for 10 000 subjects; `TestPerf_TaxonomyRecompute`), and returns `{version, mappings_applied,
unchanged, recomputed: {takes, songs}}`. The call is rate class `heavy`.

**C93.3 `Parse`** (D-L1-16). Split `style` on `, ; |` and line breaks; per part: `textfold.Norm` (lower-case, whitespace-collapsed, D-L1-33), replace `-`/`/`
with a space, re-collapse, trim, cut at 160 runes; BPM parts are skipped with a **local** matcher (`bpmPartRe`: `yue2`'s `bpmRe` is unexported),
and `Parsed.BPM` is the BPM `yue2.StyleTerms` returns for the whole style; `TestParse_BPMSkipMatchesStyleTerms` pins the local matcher to `StyleTerms` on a 30-row table. Words are tokenised on spaces. Greedy left-to-right longest match:
at word `i` try n = min(5, remaining) … 1; the first phrase present in the index emits **all** its mappings
(`Term` = the matched phrase), consumes n words, continues at `i+n`; otherwise `i+1`. A part with zero matches
and ≤ 40 runes (and not only digits) is an **unmapped term** (first-occurrence order, deduplicated, at most 20).
`Language` is set when a `language` mapping matched (the first).

**C93.4 Compute and freshness.**
- *Take subject* (`ComputeTakeTx`): reference takes (`engine = 'unknown'`) get only `tempo_band` rows from the
  Ears `tempo` analysis (origin `measured`, `detail_json {"bpm":…,"confidence":…}`; none when the analysis
  is absent or reported no tempo). Known recipes: `engine` row (origin `recipe`, value the engine name),
  `Parse` rows (origin `style`; the recipe style is parsed, `track_style_terms` is only the fallback when the style text is empty), `language` row from `yue2.DetectLanguage(lyrics)` when no language term matched
  (origin `lyrics`), `tempo_band` rows for each available basis — `declared` (`tracks.bpm_declared` when set, else `yue2.StyleTerms` of the style), `plan` (`scores.bpm`; no row when `scores` has none),
  `measured` (analysis) — with `detail_json {"bpm":…}`. `take_sounds` is replaced for the take
  (delete + insert) and `sound_state('take', id)` written with `input_digest = sha256(canonical JSON {taxonomy
  version, engine, style, sha256(lyrics), declared bpm, score bpm, tempo analysis version + sha256(metrics_json)})`.
- *Song subject* (`ComputeSongTx`): representative subject = the Pick take (`song_picks.take_id`), else the
  newest eligible take, else the draft (its `request_json` style/lyrics; digest includes the draft `revision`).
  `song_sounds` = the subject's non-tempo rows plus **one** collapsed `tempo_band` row: value from the effective
  BPM (measured, else declared, else plan), origin = that basis, `detail_json` = `TempoDetail`. A reference
  Song has only the measured tempo row (or none). With the tempo lane absent or disabled (D-L1-39) there are no `measured` rows and nothing else changes. `song_unmapped_terms` is replaced from the subject's
  `Unmapped`. `sound_state('song', id)` digest = sha256 of `{subject kind, subject id, subject digest,
  taxonomy version}`.
- *Staleness:* a subject is stale when it has no `sound_state` row, its digest differs, or its
  `taxonomy_version < current`. The Reconciler recomputes the subjects of dirty takes/drafts, every Song whose
  Pick take changed, and — at startup, after `Map` and on `Sweep` — every stale subject, in batches of
  `BatchKeys`.

**C93.5 `Facets`** (`search_sounds` counts) — for each facet `f` of the seven: `SELECT value, COUNT(DISTINCT
song_id)` over `song_sounds` restricted to live non-hidden non-retired Songs that satisfy every *other*
selected facet and the non-facet filters (`songs.FilterSQL(filter, excludeFacet = f)`); standard faceted
search, so a selected value's siblings stay visible. Values ordered by count desc then value. Counts for
`tempo_band` also report `mismatches` (count whose collapsed row has `mismatch = true`). `FacetCounts{
taxonomy_version, selected, facets: {facet: [{value, count, mismatches?}]}, songs_matching}`.

**C93.6 `Taxonomy`** (`get_sound_taxonomy`) — `TaxonomyQuery{Facet, Q, Unmapped bool, Limit 1..200 (100), Cursor}`:
default returns the effective mapping `{term, facet, value, since_version}` (ordered by facet, value, term,
keyset), the version list (last 20 `{version, kind, seed_id, created_by (api.RefFor), created_at, note}`) and
`current_version`; `unmapped=true` returns instead the top unmapped terms `{term, songs}` (count of live Songs
with the term in `song_unmapped_terms`, ordered by count desc then term). Page cap 90 000 B.

### C94. Tempo analyzer (Ears Tier A, `internal/analysis/tempo.go`)

`analysis.Tempo{BPM *float64, Confidence float64, Candidates []TempoCandidate (≤ 3, strongest first),
OctaveAmbiguous bool, AnalyzedSeconds float64}`; `analysis.TempoDef = "mm-tempo/1"`.

**Algorithm (pure Go, deterministic).** Input: mono float32 at 11 025 Hz, at most 240 s taken from the start of
the file (`ffmpeg.Tempo` decodes `-ac 1 -ar 11025 -t 240 -f f32le`; shorter files use what exists; < 20 s ⇒ no
tempo). (1) STFT: Hann window 1024, hop **128** (≈ 86.13 frames/s; revision 2: hop 256 gave ≈ 3 BPM per half-frame at 140 BPM and made the jitter test flaky). (2) Onset strength: per frame the sum over bins
covering 40 Hz–5 kHz of the half-wave-rectified difference of `log(1 + 100·|X|)` against the previous frame;
subtract a 0.4 s moving mean, clamp ≥ 0. (3) Tempogram: normalised autocorrelation of the onset envelope for
lags covering 50–220 BPM, multiplied by a log-normal prior centred at 110 BPM with σ = 0.8 octave. (4) Peak
pick with parabolic interpolation (resolution ≤ 0.5 BPM); candidates = the top peak and its ½× and 2× lags
(when in range) with their strengths. (5) `Confidence` = top peak ÷ mean of the tempogram over the range;
`BPM` is omitted (nil) when `Confidence < 2.0`; `OctaveAmbiguous` when a ½×/2× candidate has strength ≥ 0.8 of
the top. The result is an **estimate**: it assumes a steady tempo; blind spots (listed in `blind_spots` of the
dossier): tempo changes, rubato, free-time intros, ambiguous half/double time.

`ears` runs it as analyzer `tempo` (version `mm-tempo/1 <tool>`, tier `A`) in `AnalyzeTrack` like the other Tier A
analyzers (stored in `analyses.metrics_json = {bpm, confidence, candidates, octave_ambiguous, analyzed_s}`;
skipped when present unless `Force`; failure is a warning, never fails the take). `earstest.TierA` gains
`Tempo(ctx, workPath, durationS) (analysis.Tempo, error)` returning a deterministic function of the path for
fakes. The Ears dossier and `/context` `perception` gain `tempo_bpm`, `tempo_confidence` (null when absent).
`ears.Service.FlagCodes(ctx, ids)` (C90.6) is added in the same task.

### C95. The Reconciler (`internal/provenance`), backfill, verify

```go
func New(o Options) *Service   // Options{Store, Muses, Songs, Sessions, Picks, Sounds, Ladder, Now, NewID, Log, Owner,
                               //   FaultAfterPhaseA func() error,         // test seam: returns an error right after phase A commits
                               //   BeforeSettle func(context.Context)}    // test seam: called after the lease is taken, before the first pass
func (s *Service) Settle(ctx context.Context) (Report, error)   // takes the lease (ErrSettleBusy when another live process holds it); drains until the queue is empty or ctx ends
func (s *Service) Run(ctx context.Context)                        // loop: Settle every PollInterval when pending; Sweep at start and hourly
func (s *Service) Pending(ctx context.Context) (n int, err error)
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error)  // read-only; C95.5
type Report struct {
	Passes, KeysProcessed, KeysRequeued, KeysDropped int
	SongsCreated, SongsAttached, SongsRetired, MusesCreated int
	SessionsCreated, MembersAdded int
	PickEvents, PickRefreshed, SoundsComputed int
	Anomalies []Anomaly
}
```

**C95.1 One pass** (a transaction per phase, ≤ `BatchKeys` keys per pass). **Selection order:** `kind` rank first — `run`, `collection`,
`batch`, `track`, `draft` — then `at`, then `id` (rank-first because the seed gives every key almost the same `at`: ordering by `at` alone
interleaves tracks and drafts across batches and strands a draft whose track key lands in a later pass).
0. **Lease:** `Settle`/`Run` acquire `provenance_lease` (take it when `owner = ''`, `expires_at` is past or `owner` is ours; bump `epoch`)
   and renew it every pass; both phase transactions first check `epoch` (else `ErrLeaseLost`, nothing written). Within a process a mutex
   serialises `Settle`.
1. Read up to `BatchKeys` rows of `provenance_dirty` in that order (remember each row's `(kind, id, at, tries)`).
2. **Phase A (write tx):** sessions for `run`/`collection`/`batch` keys (`SyncRunsTx`…); `songs.ReconcileTracksTx` for
   `track` keys; `songs.ReconcileDraftsTx` for `draft` keys; sessions for the runs/collections/batches reached
   from those tracks/drafts (C92.1); collect the **affected Songs** (Songs of the dirty tracks, Songs created,
   attached, retired-into, and Songs of dirty drafts). (`FaultAfterPhaseA` fires after this commit.)
3. Outside any write transaction: `Ladder.Strengths(takes of the affected Songs)`.
4. **Phase B (write tx):** `picks.RecomputeTx(affected Songs)` (incl. proposal expiry); Songs whose Pick take changed are added to the
   sound set; `sounds` recompute for the dirty takes (tempo/analyses changes, new takes) and for the
   affected Songs (`ComputeTakeTx`/`ComputeSongTx`, skipping fresh subjects by digest); then settle the queue rows by outcome:
   **clean key** ⇒ `DELETE … WHERE (kind, id) = ? AND at = ?` (a row re-marked meanwhile has a newer `at` and survives);
   **key that reported a transient anomaly** (`song_missing_for_root`, a lineage whose root Song is not yet reconciled) ⇒ **not dequeued**:
   `UPDATE … SET tries = tries + 1 WHERE (kind, id) = ? AND at = ?`; at `tries ≥ MaxQueueTries` (5) the key is dequeued and the anomaly
   stays reported for `Verify` (a trigger re-mark resets `tries` to 0).
Phases A and B are separate transactions so the single writer connection is never held across the ladder
fit; if phase B fails the queue rows stay and the next pass redoes both phases (every function is idempotent,
C95.2). `Settle` returns when a pass finds no keys, when every selected key was only re-marked (no progress), or when `ctx` ends — **there
is no fixed pass cap** (a first backfill of a large library simply takes more passes; the startup budget is C95.3). `Run` calls it every
`PollInterval` only when `Pending > 0` (one indexed `SELECT 1`), and runs `picks.Sweep` + the `sounds` stale sweep at start and every
`SweepInterval`; when another process holds the lease `Run` waits and retries.

**C95.2 Idempotence and crash safety.** Every phase function is a pure function of the database state plus
the dirty keys: running a pass twice yields the same rows (no new Song, Muse, member, event, version); a crash
between phases leaves queue rows that redo the work. `Verify` + `Settle` twice after a random write
sequence is the property test (C101).

**C95.3 Backfill = first Settle after the migration.** The migration seeded one dirty row per existing run,
collection name, batch, draft and track. Expected result on any database (counts given for the real library
in Appendix P.2): one reference Muse per distinct folded `(source_title, source_artist)` of the drafts' refs;
one live Song per idea — per root with no draft or job-item link and per motherless unfulfilled draft (roots rendered from a draft or a job item attach to its Song through `song_roots`); Songs of drafts carry the Muse and
angle of their index row; one session per import run and per draft collection (and per batch); a
`song_picks` row (event `initial`) for every Song with ≥ 1 eligible take; `song_sounds` for every live
Song (draft Songs from their draft request; reference Songs only after `analyze` produced tempo); no
`pick_events` beyond the `initial` ones. `metamusic library reconcile --dry-run` reports the queue
composition without writing; the server runs `Settle` once before accepting API traffic on its first
start after a schema change (the listener is not opened until it finishes: there is no separate readiness flag, `serve()` simply calls `net.Listen` after the startup `Settle`; ≤ 60 s budget, inside the image HEALTHCHECK window of `--start-period=10s --interval=30s --retries=3`; `TestServe_SettlesBeforeListening`). **Startup order (explicit):** open store and migrate (`boot`) → `app.Build` → `sounds.EnsureSeed` (the taxonomy exists before any Sound is computed) → acquire the lease and run the first `Settle` (waiting if another process holds it) → `net.Listen` → start `Run`. `serve()` is shared by `serve` and `dev`, so both settle and run the Reconciler; `mcp-stdio` and the test rigs build the services but never start `Run` (rigs call `Rig.Settle`).

**C95.4 Restore and import.** A restored database is at an older or equal schema; the startup `Settle`
plus the stale-subject sweep rebuild everything derived (nothing in a backup is trusted to be current).
`metamusic import` and `seed-synthetic` call `Settle` before exit **only when they obtain the lease** (D-L1-27, D-L1-35); with a server
up (lease held) they only enqueue — the CLI prints the queue by kind and exits 0, and the server drains within `PollInterval`
(`library reconcile --wait` polls until empty). (The writer pool's `MaxOpenConns(1)` serialises one process only; the lease is what makes the writer single across processes.)

**C95.5 `Verify` invariants** (read-only SQL; `Anomaly{Code, Subject, Detail}`; `library verify` prints and
exits 1 on any):
- I1 every track belongs to exactly one live Song (`take_songs` has exactly one row per track; none to a retired Song); every `songs.root_track_id` has a `song_roots` row of the same Song;
- I2 every live Song has `root_track_id` or a live draft; a Song with `root_track_id IS NULL` has a draft that
  is motherless, unfulfilled-by-non-root (else `draft_song_should_retire`);
- I3 no two live Songs share a draft or a root; no live Song's draft has a mother (`song_for_mothered_draft`);
- I4 `song_picks` exists exactly for Songs that have/had an eligible take; `take_id` is a take of the Song;
  `source = pinned ⇒ pinned_take_id = take_id` with the pinned take eligible, and a `pinned_take_id ≠ take_id` is legal only while the pinned take is ineligible (`pin_ineligible`); the stored pick equals `picks.Derive` recomputed now **unless**
  the Song has pending dirty rows (then reported as `pick_stale` only after the queue is empty);
- I5 per Song the `pick_events` sequence is `1..n` with each `from` equal to the previous `to`, and the last event
  equals `song_picks` (`last_event_id`, `take_id`, `source`);
- I6 every Muse referenced by a live Song exists; every reference Muse's `key` equals `muses.Key(title, artist)`;
- I7 session members exist; every import run, every collection and every `active`/`finished` batch with ≥ 1 job has its session;
- I8 `sound_state` freshness (no live Song subject stale after the queue is empty) and `take_sounds` ⊆ the
  allowed facet/value sets.
- I9 `provenance_lease` has exactly one row; after the queue is empty no `pick_proposals` row is `open` for an ineligible take or for the current Pick.
`Verify` is also the oracle of the property tests and of `TestAcceptance_L1_BackfillMirror`.

---

### C96. Operations (types in `internal/api/types_l1.go`; JSON snake_case)

Legend (M2a C38): **C** = `idempotency: create`; **H** = `rate: heavy`; all L1 operations are `cost: free`
(`TestL1_NoSpendOps`, D-L1-29). `exclude:human-judgment` ops keep a UI route (the SPA calls them) but have no
MCP face; the registry-derived "agent does every UI action" set skips them by the existing rule.

| OperationID | Method + path | Scope | Cost | MCP | UI | Flags | Output |
|---|---|---|---|---|---|---|---|
| `list_muses` | GET `/api/muses` | read | free | tool | `/board` | | `{items: MuseSummary[], next_cursor, total}` |
| `get_muse` | GET `/api/muses/{id}` | read | free | tool | `/muses/:id` | | `MuseDetail` |
| `create_muse` | POST `/api/muses` | write | free | tool | `/board` | C | 201 `Muse` |
| `update_muse` | PATCH `/api/muses/{id}` | write | free | generic | `/muses/:id` | | `Muse` |
| `assign_muse_songs` | POST `/api/muses/{id}/songs` | write | free | generic | `/board` | | `AssignResult` |
| `list_songs` | GET `/api/songs` | read | free | tool | `none:agent-face (the Sound browser lists Songs through search_sounds)` | | `{items: SongSummary[], next_cursor, total}` |
| `get_song` | GET `/api/songs/{id}` | read | free | tool | `/songs/:id` | | `SongView` (C90.6) |
| `update_song` | PATCH `/api/songs/{id}` | write | free | generic | `/songs/:id` | | `SongCore` |
| `propose_pick` | POST `/api/songs/{id}/pick-proposals` | write | free | tool | `none:agent-face (a human pins directly with pin_pick)` | C | 201 `PickProposal` |
| `pin_pick` | PUT `/api/songs/{id}/pin` | write | free | `exclude:human-judgment` | `/songs/:id` | | `PinResult` |
| `dismiss_pick_proposal` | POST `/api/pick-proposals/{id}/dismiss` | write | free | `exclude:human-judgment` | `/songs/:id` | | `PickProposal` |
| `list_pick_events` | GET `/api/pick-events` | read | free | generic | `/songs/:id` | | `{items: PickEvent[], next_cursor}` |
| `list_sessions` | GET `/api/sessions` | read | free | generic | `/board` | | `{items: SessionSummary[], next_cursor, total}` |
| `update_session` | PATCH `/api/sessions/{id}` | write | free | generic | `/board` | | `SessionSummary` |
| `search_sounds` | GET `/api/sounds/search` | read | free | tool | `/sounds` | | `SoundSearch` |
| `get_sound_taxonomy` | GET `/api/sounds/taxonomy` | read | free | generic | `/sounds` | | `TaxonomyPage` |
| `map_sound_term` | POST `/api/sound-terms` | write | free | generic | `/sounds` | H | `MapResult` |
| `get_muse_board` | GET `/api/board` | read | free | generic | `/board` | | `Board` (C97) |

18 operations: 7 first-class tools, 9 generic, 2 excluded (`TestOps_RegisteredWithMetaL1`). After L1 the
registry holds 81 + 18 = 99 operations; L1-B2 re-pins `TestRegistry_OpCount` at 99. Agent counterparts of
the human-judgment ops: `propose_pick` (for `pin_pick`/`dismiss_pick_proposal`) and `list_pick_events`/
`get_song` (to read the outcome). **`tools/list`:** L1-B2 measures the compact `tools/list` size with the 7
new tools, sets `toolsCeilingBytes` (`internal/mcpgen/m2a_contract_test.go`, today 87 300) to the measured value rounded up and re-baselines `toolsListBaselineBytes` (`internal/mcpgen/parity_test.go`, today 207 451, the 1.5× golden budget) and `testdata/tools_list.golden.json`, each a committed constant with a justification comment in the M2b/M3 style, never an `-update` side effect; if the L1 tools alone exceed
the **16 000 compact-byte rule** (the figure M2b-B2 and M3-B2 measured against) it demotes to `mcp: generic`, in order, `list_songs`, `search_sounds`, `create_muse`,
`get_muse` and re-measures; beyond that it stops and asks the operator (D-M3-36 pattern: M3-B2 did exactly that and the operator accepted 87 300). Output schemas of `get_song` (nested `TakeCard`, `PickView`) and `get_muse_board` are the likely bulk: shapes marked open objects where the plan allows it (C98 sections) keep them small.

**Body limits** (`MaxBodyBytes`): every L1 op with a body is `bodySmall` (64 KiB) — `map_sound_term` ≤ 50 mappings,
`assign_muse_songs` ≤ 200 ids (`TestOps_BodyLimitsL1`).

**Inputs:**
- `list_muses`: `q`, `kind` (reference|brief), `include_hidden`, `limit` 1..200 (100), `cursor`.
- `get_muse`: path `id`; `songs_limit` 0..200 (default 100).
- `create_muse`: `IdemHeader` + `{kind, title, artist?, brief?}` (C89.1).
- `update_muse`: `{revision, title?, artist?, brief?, hidden?}`. `assign_muse_songs`: `{song_ids: string[1..200]}`.
- `list_songs`: `muse_id`, `no_muse` (bool), `angle`, `state` (explode: draft|rendered|judged|picked),
  `queued` (bool), `session_id`, `sound` (explode, `facet:value`; unknown facet ⇒ 422 `UNKNOWN_FACET`),
  `tempo_mismatch` (bool), `has_pick` (bool), `draft_id`, `track_id` (the single Song of that draft/take, so an agent can find the Song id
  `assign_muse_songs` needs), `has_open_proposals` (bool), `q`, `include_hidden`, `include_archived`, `limit` 1..100 (50), `cursor`.
- `get_song`: path `id`; `by` (`song` default | `draft` | `track`: `id` is then a draft or take id); `take_limit` 1..100 (24); `include_events` 0..50 (10). A retired Song answers 200 with `retired_into` (C90.6).
- `update_song`: `{revision, title?, angle? (string|null), muse_id? (string|null), hidden?}`.
- `propose_pick`: `IdemHeader` + `{take_id, rationale: string 1..500}`. `pin_pick`: `{take_id: string|null,
  proposal_id?: string}`. `dismiss_pick_proposal`: no body.
- `list_pick_events`: `song_id?`, `song_ids?` (explode, ≤ 200, unioned with `song_id`), `after` (integer `cursor` of the previous page), `limit` 1..200 (100).
- `list_sessions`: `kind` (explode), `q`, `include_hidden`, `limit` 1..100 (50), `cursor`. `update_session`:
  `{revision, name?, hidden?}`.
- `search_sounds`: every `list_songs` filter plus `sound` selections; `limit` 0..100 (50; 0 = facet counts only).
- `get_sound_taxonomy`: `facet?`, `q?`, `unmapped` (bool), `limit` 1..200 (100), `cursor`.
- `map_sound_term`: `{mappings: [{term, facet, value, op: add|remove}] (1..50), note?: string 1..500}`.
- `get_muse_board`: `detail` (`chips` | `counts`, default `counts`), `muse_id?`, `session_id?`, `angle?`,
  `state` (explode), `sound` (explode), `q?`, `include_hidden`, `limit` 1..500 (200), `cursor`, `unassigned_after?` (cursor inside the `Unassigned` row, C97).

**Shapes** (abridged; full in `types_l1.go`; principals through `api.RefFor`; no Muse text outside the marked objects):
```
MuseRef      {id, kind, title, artist|null}
Muse         {id, kind, title, artist|null, brief|null, source, created_by, updated_at, revision, hidden}
MuseSummary  Muse + {songs, draft, rendered, judged, picked, queued_jobs}
MuseDetail   {muse: Muse, coverage: {songs, draft, rendered, judged, picked, queued_jobs},
              angles: [{angle|null, songs}], songs: SongSummary[], songs_total}
AssignResult {assigned, unchanged}
SongSummary  {id, title, angle|null, muse_id|null, state, takes, pick: {take_id, source}|null, draft_id|null,
              root_track_id|null, queued_jobs, open_proposals, hidden, archived, updated_at}
PickProposal {id, song_id, take_id, rationale, proposed_by (RefFor), state (open|accepted|dismissed|superseded|expired), created_at, decided_at|null, decided_by|null}
PinResult    {pick: PickView, proposal: PickProposal|null, changed: bool}
PickEvent    (C91.8)
SessionSummary (C92.2)
SoundSearch  {taxonomy_version, selected: {facet: [value]}, facets: {facet: [{value, count, mismatches?}]},
              songs: SongSummary[], total, next_cursor}
TaxonomyPage {current_version, versions: [...], items: [{term, facet, value, since_version}]
              | unmapped: [{term, songs}], next_cursor}
MapResult    {version|null, mappings_applied, unchanged, recomputed: {takes, songs}}
```

**Error codes (new, stable):** 404 `NOT_FOUND` (unknown muse/song/session/event/proposal ids — the generic code, message
carries only the id), 409 `MUSE_EXISTS`, `MUSE_HIDDEN`, `SONG_RETIRED` (write ops only; `get_song` answers 200 with `retired_into`), `REVISION_CONFLICT`, `ALREADY_PICK`,
`PROPOSAL_MISMATCH`, `PROPOSAL_CLOSED`, `TOO_MANY_PROPOSALS`; 403 `HUMAN_ONLY`; 422 `VALIDATION_FAILED` (field
errors), `UNKNOWN_MUSE`, `UNKNOWN_SONG`, `UNKNOWN_FACET`, `INVALID_ANGLE`, `INVALID_TERM`, `FACET_NOT_MAPPABLE`,
`MAPPING_NOT_EFFECTIVE`, `TAKE_NOT_IN_SONG`, `TAKE_NOT_ELIGIBLE`; 503 `UNAVAILABLE {component}`. `REVISION_CONFLICT`,
`VALIDATION_FAILED` and `HUMAN_ONLY` reuse the existing shapes (M2a). Every message names ids and fields, never
titles, artists or briefs (C99).

### C97. Muse Board (`internal/board`)

`board.Service.Get(ctx, Query) (Board, error)` — one read transaction (`ReadOnly`), five statements, no N+1:
(1) the Muse rows (live, non-hidden unless `include_hidden`; one `muse_id` when given) with keyset on
`(title_fold, id)`; (2) the live Songs of those Muses (and, on the **last page**, the first `BoardUnassignedChips = 200` Songs of no Muse in `(created_at, id)` order — the `Unassigned` pseudo-row, which has its own cursor `unassigned_after` and a `chips_more` flag; JSONL uploads accumulate there by design, D-L1-28) filtered by session / angle / state / sound / text through `songs.FilterSQL` with the C90.4 state
columns; (3) their picks (`song_picks`); (4) the `tempo_band` mismatch flags (`song_sounds`); (5) `totals` over the whole filter. Shapes:
```
Board {
  generated_at, taxonomy_version,
  columns: [{angle|null, songs}],                      // distinct angles in view, by count desc then name; null = "(none)"
  rows: [{ muse: MuseRef|null,                         // null = Unassigned
           songs_total, counts: {draft, rendered, judged, picked, queued},
           cells: [{angle|null, chips: SongChip[]}],    // non-empty cells only; chips only when detail=chips
           chips_more, next_unassigned_cursor }],     // only meaningful on the Unassigned row: counts stay complete, chips are capped at 200
  totals: {muses, songs, draft, rendered, judged, picked, queued},     // over the whole filter, not the page
  next_cursor
}
SongChip {id, title, state, takes, pick_source|null, pick_take_id|null, queued, draft_id|null (only when state=draft),
          tempo_mismatch}
```
**Rules.** With any filter active, rows with no matching Song are omitted; unfiltered, every non-hidden Muse is a
row (empty rows are real information: an inspiration with nothing made). Within a cell chips are ordered by
`(created_at, id)`. `Unassigned` is the final row on the last page and never counts as a Muse in `totals.muses`.
Page cap: **90 000 B for every `detail`, for every caller** (REST, MCP, SPA). Revision 2 drops the earlier 256 KiB `detail=chips` exception:
through MCP `call_operation` that is ≈ 65 k tokens; the SPA follows `next_cursor` ("Load more") instead. Rows are cut at whole-row boundaries and
the `Unassigned` row is bounded by its own 200-chip cap, so a single row can never exceed the cap (`TestBoard_PageCap`: a 400-row fixture splits
into pages at the cap, the cursor resumes, no row repeats; `TestBoard_UnassignedCapped`: 1 000 unassigned Songs ⇒ the first response has ≤ 200
chips, `chips_more`, complete `counts`, and `unassigned_after` pages through all 1 000 exactly once, each page ≤ 90 000 B). `next_cursor` is the last emitted
`(title_fold, id)`. **Rendering from the Board never calls a spend operation**: the SPA turns selected `draft`
chips into a `from_draft` intent (C100.2). `TestBoard_Equivalence` asserts that the Board's per-Muse counts equal
`get_muse`'s coverage and `list_songs` counts for the same filter.

### C98. `/context`, search, stats amendments (amends M2a C35/C38, M1 C19, M3 C81.1)

**`/context`** (`trackctx.Bundle` gains `Song`, `Muse`, `Sounds map[string]any`, serialised after `track` in the
order `song`, `muse`, `sounds`; `order` map gains the key lists; goldens re-baselined with a reviewed diff):
```
song   {id, title, angle|null, state, takes, pick: {take_id, source, is_this}|null, open_proposals, pending,
        siblings: [{id, title, stars|null, status, is_pick}]}        // ≤ 12, this take excluded, newest first
muse   {id, kind, title, artist|null, brief|null}                    // brief ≤ 500 chars; visible to agents (D-L1-6); null without a Muse
sounds {taxonomy_version, facets: {facet: [value]}, tempo: {requested_bpm|null, measured_bpm|null, mismatch, octave}|null}
```
All three are `null` when the take has no Song yet (queue pending) or the provenance services are absent
(`trackctx.LibraryReader` nil). Trimming (`Fit`): `song.siblings` 12 → 5 → 0 before comments, then `muse.brief`
to 200 chars; names recorded in `_budget.truncated`. `trackctx` declares
`type LibraryReader interface{ ForTrack(ctx, trackID string) (LibrarySections, error) }` (`LibrarySections{Song, Muse, Sounds map[string]any}`; the name avoids the existing `Bundle.Provenance` section, which keeps its keys `origin, import_rule, truncated, cost, env, job_id`) and `app.Build` adapts the
L1 services to it. The `order` table of `trackctx.go` gains `song`, `muse`, `sounds`; the four goldens (`context`, `context_job`, `context_lossy`, `context_reference`) are re-baselined with a reviewed diff; `web/e2e/m2a-context.spec.ts` is checked for key-order assumptions. **`next_actions` is unchanged** (D-L1-22).

**`search_tracks` / `TrackSummary` / `TrackDetail`** (`library`, SQL joins gated by `hasTable("songs")`):
`TrackSummary` gains `song_id` (string|null) and `is_pick` (bool: the take is its Song's current Pick);
`TrackDetail` gains `song: {id, title, angle|null, muse_id|null, pick_source|null}|null`. New filters `song_id`,
`is_pick` (bool), `session_id` (derived membership). Old-schema fixtures return `null`/`false` and ignore the filters
(422 `UNSUPPORTED_FILTER` is **not** used: the filters simply match nothing on a pre-7 schema, which cannot occur in a
migrated database).

**`get_stats`** gains `library_model: {songs: {total, draft, rendered, judged, picked}, muses, sessions,
taxonomy_version, pending}` (counts of live, non-hidden rows; `pending` = queue length).

### C99. Muse visibility and privacy enforcement (D-L1-6)

1. **Import graph** (`TestImportGraph_L1`): `export`, `exports`, `notify`, `audit`, `backup`, `importer`, `jobs`,
   `generation`, `lineage`, `library`, `redact` and every package whose path contains `subsonic` must not reach
   `internal/muses`, `internal/songview` or `internal/board` transitively. (`trackctx` reaches Muses only through its own interface;
   `api` and `app` are the composition roots and are exempt.)
2. **SQL allowlist** (`TestSQL_MuseTables_Allowlist`): non-test Go outside `internal/muses`, `internal/board` and
   `internal/store/migrations` never mentions `muses` or `muses_fts` in SQL text. `songview` reads Muse text through `muses.Service.Ref/Get` (C90.8), so **no exception is needed**; the test asserts the allowlist is exactly those three locations.
3. **Response allowlist** (`TestMuseText_OnlyInAllowlistedOps`): the parity runner's rig holds a canary Muse (unique
   title, artist and brief strings, built at runtime) assigned to the Song of the fixture take; every registered op
   is called with its parity case over REST and MCP and the response bytes are scanned; the canary may appear only
   in `{list_muses, get_muse, create_muse, update_muse, get_song, get_muse_board, get_track_context, get_draft}` (`get_draft` per D-L1-36; `TestDraftReference_AgentView` pins the decision). **More canaries:** a *collection-derived Session name* (readable in `list_sessions`, `update_session`, `get_song.sessions`; never elsewhere), an *agent-authored* `pick_proposals.rationale` and an agent-authored `sound_terms.term` that contain Muse text (stored and readable by design through `get_song` / `get_sound_taxonomy` / `map_sound_term`; never in logs, exports, audit text, job events, ntfy or `problem+json`). The same test
   scans the bytes of `export_jsonl`, `export_playlist_zip`, a backup snapshot's **manifest and export listings**
   (the snapshot's database copy contains Muses and `muses_fts` by construction — backups are operator-private, like lyrics — and is
   not scanned), the captured `slog` output, every text column of `audit_log`, `job_events`, `notify` messages sent to
   the ntfy fake, and `problem+json` bodies of every refusal case. `idempotency.response` rows of `create_muse` are a
   cache that expires (24 h for an explicit key, 10 min for the key MCP derives: `idem.ExplicitTTL`, `idem.DerivedTTL`) and are exempt; `TestIdem_CreateMuseCacheExpires` asserts the expiry (clock +24 h ⇒ no replay, and the purge removes the row).
4. **Errors and logs:** no Muse text in `Problem.Message`/`Details`, in `slog` attributes or in `audit` (args are hashed);
   `TestMuseOps_ErrorsCarryIDsOnly` drives every error code of C96 with a canary Muse.
5. **Fixtures and docs:** L1 fixtures use synthetic Muse names (`Synthetic Muse 001`, artist `Synthetic Artist 001`); the earlier fixtures and the synthetic archive already carry `Synthetic Reference Title`, `Synthetic Title N` and `Synthetic Artist N`;
   `TestFixtures_L1_SyntheticOnly` fails on any Muse title, artist or brief outside the family `Synthetic <words>[ NNN]` (checked over `FixtureL1`, `FixtureL1Mirror` and the archive-built Muses); docs/examples use `mm.example.com` and
   invented titles. Nothing in this plan's public part names a real inspiration; `make gate` stays unchanged.
6. **Draft reference metadata (D-L1-36):** `get_draft` shows `reference` to read-scope agents exactly as its Muse carries it (amending the M1/M2a contract in the same commit as B1); `TestDraftReference_AgentView` covers human, agent and no-reference drafts.
7. **Subsonic (M4a)** is not built: the graph rule above is the guard the M4a plan must keep green; the L2 plan
   must resolve the "per-Muse auto-albums" conflict (Risks).

### C100. Web (`web/`; amends M2a C40, M3 C84)

**C100.1 Routes** (`APP_ROUTES` in `web/src/lib/router.ts`, `{pattern, name}`, specific before parametric: `board`, `muse`, `song`, `sounds`; the menu is the `<nav>` of `App.svelte`, which gains "Board" and "Sounds" links with the same `aria-current` rule; `lib/nav.ts` only classifies `/human/*` paths and is unchanged): `/board` (Board),
`/muses/:id`, `/songs/:id`, `/sounds`. `web/src/api/schema.d.ts` is regenerated by `make openapi`.
Existing pages (`ProvenancePanel.svelte` on the Track page keeps showing origin, rule and cost; it is unrelated to L1's `provenance` package): **Track** gains a "Song" strip (title link to `/songs/:id`, `Pick` badge when `is_pick`, "Pick
of this Song: <title>" link when not); **Library** gains the `Pick` badge on rows (`TrackBadges.svelte`, `TrackTable.svelte`) and a "Picks only" filter
(`LibraryFilters.svelte`, `libraryQuery.ts`: `is_pick=true`); **Backlog** gains a "Muse Board" link in its header (no new op call).

**C100.2 Board (`routes/Board.svelte`).** Loads `get_muse_board(detail=chips)` with the URL state
(`?session=&angle=&state=&sound=facet:value&sound=…&q=&muse=`; repeated keys read from the raw search string as
Library does). Layout: toolbar (session select from `list_sessions`, state toggles, facet pills from
`search_sounds(limit=0)` counts, search box), a column header row of angles, one row per Muse (label links to
`/muses/:id`, counts line), cells of `SongChip` buttons, a trailing `Unassigned` row (200 chips, then **Show more** through `unassigned_after`), a collapsible **Sessions**
panel (`list_sessions`, rename inline and hide through `update_session`). Interaction: a chip click toggles
selection (shift-click selects the range within a row; "Select all drafts in view" caps at 50 = `MAX_TAKES`);
the action bar shows the count with: **Estimate & render** (enabled when ≥ 1 selected chip is `draft` and
`mayGenerate(viewer)`; non-draft chips are skipped with a note; builds `draftsIntent(draft_ids)` and calls
`startGeneration` — the M3 full-page navigation to `/human/generate`; the Board never calls an `/api/` create op),
**Assign to Muse…** (`MusePicker`: typeahead over `list_muses?q=`, "Create Muse…" form → `create_muse`, then
`assign_muse_songs`), **Open** (one selected ⇒ `/songs/:id`). Chip: state colour + glyph (`draft` outline,
`rendered` dot, `judged` check, `picked` ★), `×n` takes, a queued badge, a tempo-mismatch dot; a tooltip with
title, state, pick source. Rows beyond the first page load with the cursor ("Load more"). All interactive
elements keyboard-operable (`role="grid"`, chips are `button[aria-pressed]`), tap targets ≥ 44 px on
touch viewports.

**C100.3 Song (`routes/Song.svelte`).** Header: title (inline edit → `update_song` with `revision`), angle (input with a
datalist of the Board's angles), Muse (`MusePicker`; clearing sends `muse_id: null`), hide toggle, state chip,
sessions chips, sounds chips with `TempoBadge` ("requested 76 · measured 75 ✓", mismatch ⚠, octave ≈, "estimate").
Body: **take cards** (`TakeCard`: lazy waveform from `get_track_peaks` overview via the existing `Waveform` with a
play button on the player store, `StarRating`/`StatusSelect` (existing ops), Ears flag chips from `flags`, ladder
position "u 1.20 ± 0.55 · 4 duels", `lossy` and `no recipe` badges, parent link, cost); the Pick card is first with
`PickBadge` (source word + one-line evidence, `pickEvidenceText`); per card **Pin as Pick** / **Unpin**
(`pin_pick`; a pinned take that is discarded shows "pinned, not eligible" next to the fallback Pick), **Duel…** (select two cards ⇒ `createDuel(api, a, b, key, {blind: true})` ⇒ `/duels/:id`);
**Pick history** (`list_pick_events`, lazy); **Proposals** (`ProposalList`: agent, take, rationale, Accept =
`pin_pick` with `proposal_id`, Dismiss = `dismiss_pick_proposal`); **Lineage** link to `/lineage/:root`. A draft
Song (no takes) shows the draft summary and **Generate** (`draftsIntent([draft.id])`). When `pending` is true the
page refetches `get_song` after 1.5 s, at most three times.

**C100.4 Muse (`routes/Muse.svelte`).** `get_muse`: header with inline edit (`update_muse` + `revision`; reference
shows title/artist, brief shows title/brief), hide toggle, coverage chips, angle breakdown, the Songs as chips
(shared `SongChip`), link "Show on the Board" (`/board?muse=<id>`).

**C100.5 Sounds (`routes/Sounds.svelte`).** Tab **Songs**: `FacetPanel` per facet (counts from `search_sounds`,
click toggles; selected pills on top; "tempo mismatch only" toggle), the results list (SongChips with their sound
chips; paging by cursor). Tab **Terms**: effective mapping table (`get_sound_taxonomy`, filter by facet/query), the
top unmapped terms (`unmapped=true`) each with an inline "Map…" form (`map_sound_term`: facet select, value
select/free input per facet rules), the taxonomy version history. A successful map shows the recompute counts
and re-queries.

**C100.6 View-model modules (`web/src/lib/`, each with a bun test):** `boardView.ts` (columns/rows → grid model,
selection with the 50 cap, range select, filter ⇄ query string), `museApi.ts` (typed wrappers over the generated
client; idempotency keys for `create_muse` through `idempotentAction`), `songView.ts` (take ordering, pending
refetch policy, duel selection), `pickView.ts` (`pickEvidenceText(evidence)`: ladder ⇒ "ahead after N blind
duels", stars ⇒ "N★ (newest of K tied)", newest ⇒ "newest take", pinned ⇒ "pinned by you", none ⇒ "no take"),
`soundsView.ts` (facet selection ⇄ `sound=` params, tempo badge text). No `fetch` outside `web/src/api/`
(ESLint ban stays).

**C100.7 Playwright projects.** The L1 specs mutate shared state (Muses, ratings, Picks, proposals, duels) and the Reconciler keeps deriving Songs
while they run, so they must not share a phase with count-based specs. `web/playwright.config.ts` gains `L1 = ["board","song","sounds","muse"]`,
`ignoreL1` and a **fourth phase**: projects `l1-chrome` (desktop Chrome) and `l1-firefox` (all four specs: layout and data only), `l1-pixel7` (board and song: the phone
requirement), each `dependencies` on phase 1, the listen projects and every M3 project (including `m3-spend`, which changes caps and arming), run one after the
other; phases 1–3 gain `ignoreL1` so no earlier project picks up an `l1-*` spec (`testMatch` is `**/*.spec.ts`). Specs use per-run unique Muse titles and assert
relative effects (a created Muse appears, a pin moves the badge); nothing deletes, so no spec depends on a total another spec changes. `l1Api.ts` follows `m3Api.ts`.

### C101. Tooling, fixtures, CI, performance, docs (amends M2a C41/C42, M3 C85/C86)

**C101.1 Fixtures.** `librarytest.FixtureL1(tb, st, now)` (the `FixtureM3(tb, st, now)` signature; rows only, stub rows for the reference FLACs as in `FixtureM2c` unless `WriteFiles` is used) adds, on top of `FixtureM3`, a **mirror of the real
structure with synthetic text**: `L1Params{Muses: 6, Angles: 3, DemoDrafts: 4, Originals: 3, Groups: 2,
GroupSize: 3}` ⇒ 18 index-backed motherless drafts (3 per Muse, `recipe_refs` rows with `variant_type`
`variant-a`/`variant-b`/`variant-c`, request ids `ex_NNN_v1..3`), one draft collection of those 18 and one of 4
`jsonl_upload` drafts, 3 single-take reference FLACs, 2 lossy reference groups of 3 takes (`p1` root, `p2`/`p3`
children with inferred-sibling mutations), plus one known-recipe lineage with a mothered draft. `FixtureL1Mirror(
tb, st, now, params)` builds the same shape at any parameters (the acceptance mirror uses `Muses: 120, DemoDrafts: 5,
Originals: 4, Groups: 5` — values chosen different from the real library on purpose; it adds two import runs, the index import and the reference import, and two collections, so the session count is fixed, see the acceptance mapping). `FixtureL1Scale(tb, st, now, muses,
angles)` writes drafts/refs/Songs-to-be only (no audio) for the performance tests (`Muses: 200, Angles: 3`). All
three write rows through SQL (triggers fill the queue) and **do not run `Settle`**; tests call `Settle` or
`rig.Settle(t)`. `FixtureL1Upgrade(tb, st, now)` writes the same data into a store opened with `store.OpenRaw` + `store.MigrateTo(…, 7)` (the `upgrade_m3_test.go` pattern; schema 7, before 0008) for
`TestMigration0008_UpgradeFromM3`. Existing fixtures keep their row counts.

**C101.2 Rigs.** `servertest.Options` gains `L1 bool` (adds `FixtureL1`; requires `Fixture`, `M2c`, `M3`) and
`Reconcile bool` (the `Runner` pattern: false, the default, builds the Reconciler but leaves it idle, tests call `Rig.Settle(t)`, which also
runs `picks.Sweep`; true starts `Run`; `serve()` always starts it). Rigs built from `Archive` (the imported synthetic archive) also get the Reconciler, so `Rig.Settle` works there. `Rig.Provenance` and `Rig.Settle` exist; the parity runner
gets **16** `ParityCase` rows (`Args(r, prev)`; the 16 ops with an MCP face) in `internal/mcpgen/parity_l1_test.go`, appended to `parityCases` in `init` like `parity_m2c_test.go` and `parity_m3_test.go`; `pin_pick` and `dismiss_pick_proposal` have no MCP face and are covered by REST-only tests (`TestPin_*`, `TestDismiss_HumanOnly`, `TestOps_HumanJudgment_NoMCP`); the
`every_ui_action` script (`internal/acceptance/every_ui_action_test.go`; it runs on `archiveRig`, the imported synthetic archive, not on a library fixture: `uiState` gains `song`, `muse`, `session` and `uiOrder` the new steps, `afterStep` records ids from `list_songs`, `list_muses`, `create_muse`, `list_sessions`; the rig settles right after import; the archive's index rows give it four Muses with angles `type1`–`type3`) gets entries for the 14 ops that have a UI
route **and** an MCP face (`list_muses, get_muse, create_muse, update_muse, assign_muse_songs, get_song,
update_song, list_pick_events, list_sessions, update_session, search_sounds, get_sound_taxonomy, map_sound_term,
get_muse_board`).

**C101.3 CI and coverage.** New packages fall into the `fast` tier of `scripts/ci-go-test.sh` automatically (it is every package outside the media list); `internal/analysis/ffmpeg` (the tempo decode) and `internal/acceptance`/`internal/servertest` are already in the `media` list, so the script changes only if a new package needs the real media tools; Playwright gains `l1-*.spec.ts`; `coverage.thresholds`: `internal/picks`
and `internal/provenance` ≥ 95 %, `internal/muses`, `internal/songs`, `internal/sessions`, `internal/sounds`,
`internal/board` ≥ 90 %, `internal/analysis` unchanged. `weekly.yml` gains a `provenance-property` job modelled on `spend-property` (`MM_PROPERTY_SEQS=300 go test -race -shuffle=on -run 'TestProperty_' ./internal/provenance`; 60 sequences per CI run) — a scope-valve cut candidate (D-L1-39).

**C101.4 Performance targets** (asserted through `perftest.Budget(t, d)` of the new test-only package `internal/perftest`, added by L1-B1 and listed in the helper list of `cmd/metamusic/nohelpers_test.go`: `MM_PERF_FACTOR` (default 1, raised on shared runners) and ×10 under `-race`, because the transpiled SQLite stack costs ≈ 10× there (Makefile note); perf tests skip under `-short`, so `make check` stays under 30 s; a perf test never hard-codes a wall-clock literal):
`TestPerf_MuseBoard` — 200 Muses × 3 angles (600 Songs) `detail=chips`, following every page of the cursor, server time p95 over 20 runs **< 1 s**
(target 250 ms); `TestPerf_Settle_Backfill` — scale fixture + demo/reference rows: first `Settle` **< 5 s**,
second `Settle` < 50 ms; `TestPerf_PickRecompute` — 1 000 Songs × 5 takes < 2 s; `TestPerf_SearchSounds` —
10 000 Songs, one facet selection, `limit 50` < 300 ms; `TestPerf_TaxonomyRecompute` — 10 000 subjects < 5 s;
`TestPerf_Tempo` — 240 s of audio < 2 s CPU (hop 128); a bun test builds the 600-chip Board view model in < 50 ms.

**C101.5 Docs.** `docs/architecture.md` gains "Library model" (nouns, derivation, visibility rule, reconcile
queue); `docs/runbook.md` gains "Reconcile and verify", "Taxonomy edits", "After a restore"; `docs/device-checklist.md`
gains DC-L1-1 (pin a Pick from a paired phone) and DC-L1-2 (the Board is usable on a phone: horizontal scroll,
44 px targets, selection and Estimate hand-off); the public plan copy `docs/plans/<date>-l1-library-provenance.md`
is the text above the private marker (L1-X1).

---

## Acceptance mapping (concept §7 L1 and design items → named tests)

| Concept / design item | Named test(s) |
|---|---|
| backfill counts on real data match expectations (reference Muses, draft Songs incl. demo, reference Songs) | `TestAcceptance_L1_BackfillMirror` — literal integers, each computed once by independent SQL over the fixture's *source* tables and written into the test. `FixtureL1Mirror` at `Muses 120, DemoDrafts 5, Originals 4, Groups 5` on top of `FixtureM3`, whose base was re-measured at alignment (schema 7): 6 roots, 8 motherless backlog drafts (one carries an index row with a variant type), 1 mothered draft, 1 import run, 3 batches (2 `active`/`finished` with jobs), 1 collection of 3 drafts. Expected after the first `Settle`: **Muses 121** (120 + the base index row); **live Songs 389** = 373 draft Songs (360 index-backed + 5 demo + 8 base) + 16 root Songs (6 base + 4 originals + 5 lossy groups + 1 known-recipe lineage); **Songs with a Muse 361**, unassigned 28; angles `variant-a`/`variant-b`/`variant-c` 120 each plus 1 for the base row (the builder fails if it collides); retired 0; `song_roots` 16; **sessions 8** = 3 import runs + 3 collections + 2 batches; `song_picks` 16 and 16 `initial` events; `Verify` empty. No tempo row is asserted (the analyzer is not needed, D-L1-39) · `TestBackfill_UpgradePath` (schema-7 `FixtureL1Upgrade` → migrate → `Settle` ⇒ same integers) · `TestBackfill_Idempotent` (second `Settle` writes nothing) · real library: L1-X2 (counts in Appendix P.2) |
| Song = lineage root or motherless draft; drafts that render attach, no duplicate Song | `TestSongs_ReconcileTracks` · `TestSongs_ReconcileDrafts_Table` (rows: backlog draft; draft fulfilled by a root; fulfilled by a child; mothered at insert; gains a mother later ⇒ retired; mother removed ⇒ new Song; fulfilled by core hash of an imported root; two drafts one root) · `TestSongs_NoDuplicateOnRender` (job ingest of a draft attaches to its Song for **n = 1 and n = 3**: `from_draft` n = 3 yields the draft seed plus two new motherless roots ⇒ one Song, three `song_roots`, one Pick row, no extra "Unassigned" Song; re-rendering an already fulfilled draft adds a root to the same Song; `compose_new` n = 3 ⇒ one Song) · `TestSongs_RootsOrderIndependent` (the same roots reconciled in any order and across passes ⇒ identical Songs) · `TestSongs_RetireCarriesMuse` (a Muse assigned to a draft Song that later gains a mother appears on the target Song when it has none; an existing target Muse is kept; logged, `revision` bumped) |
| each reference recording is its own Song; a pilot group is one Song with its takes | `TestAcceptance_L1_ReferenceSongs` (FLAC originals ⇒ 1 Song each, 1 take; lossy group p1/p2/p3 ⇒ 1 Song, 3 takes, root = p1) · `TestSongs_TakeMapping_View` |
| Muses from the index; one per distinct normalised (title, artist); Many Songs per Muse | `TestMuses_EnsureReferenceTx` (case/diacritic/whitespace variants ⇒ one Muse, via `textfold.Norm`) · `TestNorm_Table` · `TestBackfill_MuseFromRefs` · `TestMuses_CreateRules` · `TestMuses_AssignSongs` · `TestMuses_KeyGolden` · `TestMuses_OrderCaseInsensitive` (`title_fold` keyset puts `apple` before `Zebra`) · `TestMuse_LateRefReachesSong` (a `recipe_refs` row inserted after the Song exists fills a never-set Muse through `pd_refs_ai`) · `TestMuse_ClearedNeverRefilled` (an operator-cleared Muse/angle stays empty) · `TestMuse_RootSongFromRecipeRefs` (rendered-`varianti` root, no draft) |
| angle from variant_type and unambiguous `_vN` | `TestAngle_FromRef` · `TestAngle_RequestIDSuffix_Unambiguous` (all refs agree ⇒ applied; two types for `v2` ⇒ none) · `TestAngleSlug_Table` |
| Pick derivation table-tested (pinned → ladder → stars → newest; σ-aware; ≥ 3 human duels; sticky) | `TestPick_DerivationTable` (32 rows, C91.1) · `TestPick_LadderThresholds` · `TestPick_LadderStickiness` (hysteresis band 0.25–0.5) · `TestPicks_NoFlipFlopUnderDrift` (a margin drifting 0.5 → 0.4 → 0.3 → 0.5 across sweeps writes ≤ 1 event) · `TestPick_MixedTimestampWidths` (20- vs 24-character timestamps compared as instants) · `TestPicks_IntraDuelsMatchesLadderCounting` (fails if the M2b ladder's counted-verdict definition changes) · `TestPick_AgentRatingsNeverCount` · `TestPick_ReferenceAndLossyFirstClass` · `TestPick_EvidenceGolden` |
| every Pick change is an event; chain and schema guards | `TestPickEvents_ChainAndTriggers` · `TestPickEvents_ReasonTable` · `TestPicks_RecomputeWritesEventOnlyOnChange` · `TestSchema_SongPicksNeedsEvent` · `TestPick_PinIneligibleThenRestored` (discard ⇒ `pin_ineligible`, pin kept; un-discard ⇒ `pin_restored`) · `TestEvents_CursorMonotonic` (ids generated out of order, the integer cursor still strictly increases; `song_ids` filter ≤ 200) |
| pin by human/device, propose by agent, proposals lifecycle | `TestPin_Rules` · `TestPin_AgentRefusedREST` · `TestPin_NotInMCP` · `TestPropose_Rules` · `TestProposals_Lifecycle` · `TestDismiss_HumanOnly` · `TestProposals_ExpireOnDiscardOrPick` |
| recompute on rating/duel/status/new take; cross-Song drift by sweep | `TestPicks_TriggerEnqueue_Table` · `TestPicks_RecomputeAfterVerdict` · `TestPicks_SweepCatchesLadderDrift` |
| sessions from import runs, collections, batches; renameable, never required | `TestSessions_SyncRun` · `TestSessions_SyncCollection_Kind` · `TestSessions_SyncBatch` · `TestSessions_RenameSurvivesSync` · `TestSessions_DerivedSongMembership` · `TestSessions_KindFrozenAtFirstSync` · `TestSessions_NoEmptyBatchSessions` (rejected/expired/awaiting or job-less batches create none) · `TestSessions_TimestampsNormalised` (20-character sources stored as 24) |
| Sound facets from style/lyrics/measurement; versioned taxonomy; reference tracks measured-only | `TestParse_Table` (24 rows incl. longest match, plural forms, hyphen/space, BPM skipped, unmapped) · `TestSeed_Golden` · `TestSeed_Invariants` · `TestTempoBand_Table` · `TestCompareTempo_Table` (76/75 ok, 76/152 octave, 76/95 mismatch, 120/125 ok, 120/126 ok, 120/127 mismatch) · `TestCompute_KnownRecipe` · `TestCompute_ReferenceMeasuredOnly` · `TestCompute_SongSubjectFollowsPick` · `TestTaxonomy_EffectiveFold` · `TestMap_Rules` · `TestMap_RecomputesAffected` · `TestParse_BPMSkipMatchesStyleTerms` · `TestSeed_CoverageSynthetic` (≥ 85 % of a 40-style synthetic corpus get a `genre_family`) · real-copy coverage gate (L1-X2: ≥ 85 % of the real draft Songs, top-50 unmapped report) |
| "requested vs got" | `TestSongView_TempoRequestedVsMeasured` · `l1-song.spec.ts` (badge) · `l1-sounds.spec.ts` (mismatch toggle) |
| Ears tempo (separable lane; no other acceptance item depends on it, D-L1-39) | `TestTempo_SyntheticClicks` (70/90/120/140 BPM exact clicks ± 1.5 BPM at hop 128, octave-equivalence rule; the 120 BPM ±20 ms jitter case ± 3 BPM) · `TestTempo_NoiseNoBPM` · `TestTempo_ShortFile` · `TestEars_AnalyzeRunsTempo` · `TestEars_FlagCodes` · `TestFFmpeg_TempoDecodeArgv` · `TestAcceptance_L1_NoTempoNeeded` (the mirror acceptance with a `TierA.Tempo` that reports nothing) |
| Muse Board renders the backlog grid in < 1 s (server) | `TestPerf_MuseBoard` (200×3) · L1-X2 on the production copy (real size, Appendix P.2) · `TestBoard_Equivalence` · `TestBoard_PageCap` · `TestBoard_FiltersTable` · `TestBoard_UnassignedRow` · `TestBoard_UnassignedCapped` (1 000 unassigned Songs) |
| Song page, Sound browser, Muse page | `l1-song.spec.ts` · `l1-sounds.spec.ts` · `l1-board.spec.ts` · `l1-muse.spec.ts` · `songView.test.ts` · `soundsView.test.ts` · `boardView.test.ts` · `pickView.test.ts` |
| selecting Board cells → estimate/render in one step, no new money path | `l1-board.spec.ts › estimate hands off to /human/generate` · `TestL1_NoSpendOps` · `boardView.test.ts › selection cap 50` |
| MCP/REST/SPA parity for every new op; served-operation coverage | parity runner (16 cases: the ops with an MCP face; `pin_pick` and `dismiss_pick_proposal` are REST-only: `TestPin_*`, `TestDismiss_HumanOnly`) · `TestParityEveryOperation` · `TestAcceptance_M2a_AgentPerformsEveryUIAction` (14 new entries) · `TestOps_RegisteredWithMetaL1` · `TestRegistry_OpCount` (99) · `TestOps_HumanJudgment_NoMCP` · served-operation coverage (zero unserved ui ops) |
| `/context` gains song, muse, sounds | `TestContext_L1Sections` · `TestContext_L1Trim` · `TestContext_NoSongYet` · golden re-baseline |
| search/read amendments | `TestSearchTracks_SongFilters` · `TestTrackSummary_SongFields` · `TestGetStats_LibraryModel` · `TestLibrary_OldSchemaFixturesStillRead` |
| Muses never exported, logged, sent to Subsonic clients or committed | `TestMuseText_OnlyInAllowlistedOps` · `TestImportGraph_L1` · `TestSQL_MuseTables_Allowlist` · `TestMuseOps_ErrorsCarryIDsOnly` · `TestFixtures_L1_SyntheticOnly` · `TestDraftReference_AgentView` (D-L1-36) · `TestIdem_CreateMuseCacheExpires` · canaries for Session names, agent `rationale` and agent `sound_terms.term` (C99.3) · E2E leak scanner (`/__e2e/leaks` = `[]`) · `make gate` |
| one writer, eventual consistency, crash safety, restore | `TestReconciler_SingleWriterOwners` (`TestSQL_WriteOwners_L1`) · `TestReconciler_PassIdempotent` · `TestReconciler_CrashBetweenPhases` · `TestReconciler_RemarkedKeySurvives` · `TestReconciler_DraftBeforeTrackAcrossBatches` (150 tracks + 150 drafts, `BatchKeys 100`) · `TestReconciler_TransientAnomalyRequeues` · `TestSettle_TwoSettlers` (two services on one DB file: exactly one settles, the other gets `ErrSettleBusy`; takeover after expiry; a stale `epoch` writes nothing) · `TestServe_SettlesBeforeListening` · `TestRestore_RebuildsDerived` · `TestProperty_ProvenanceInvariants` (60 sequences; 300 weekly) · `TestVerify_DetectsEachAnomaly` (one planted fault per I1–I9, triggers dropped in a scratch DB where they forbid the fault) |
| agents find Songs of drafts/takes; retired ids stay readable | `TestListSongs_ByDraftAndTrack` · `TestListSongs_HasOpenProposals` · `TestGetSong_ByDraftAndTrack` (404 `details.pending` while queued) · `TestGetSong_RetiredAnswers200` |
| schema | `TestMigration0008_Applies` · `TestMigration0008_UpgradeFromM3` · `TestMigration0008_SeedsQueue` · `TestTriggers_L1` · `TestSchemaVersionAgnostic` |
| invariants of the existing system still hold | `TestRatingIsolation_StillHolds` (agent rating never moves a Pick) · `TestImmutableMastersUntouched` · `make test` |
| performance targets | `TestPerf_*` (C101.4) |
| coverage gates | `coverage` CI job (C101.3) |

---

## Lanes and tasks

Stage 1 (branch `l1` from merged `main`, its own root worktree): **L1-B1 → L1-B2 → L1-B3**.
Stage 1 lands every shared contract: migration `0008`, pure functions (key folding, titles, angles, `picks.Derive`,
`sounds.Parse`/seed/bands), `Deps`/service constructors with frozen method sets, the 18 registrations with final
types (`STUB(L1-Bn)` handlers answer 501 `NOT_IMPLEMENTED`), the Reconciler skeleton with the Song/Muse/Session
phase fully implemented (the structural core every lane tests against), fixtures and rigs, `openapi.json` and
`schema.d.ts`. **`openapi.json` and `schema.d.ts` are frozen after L1-B2.**
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from L1-B3):
**Picks** (`l1-picks`: L1-B4), **Library** (`l1-lib`: L1-B5), **Sounds** (`l1-sounds`: L1-B6), **Ears** (`l1-ears`:
L1-E1), **Web** (`l1-web`: L1-W1 → L1-W2 → L1-W3), **Infra** (`l1-infra`: L1-I1).
Stage 3 (`l1-int`): **L1-B7** (wiring, acceptance, property, leaks, parity) → **L1-W4** (Playwright) → **L1-X1**
(integration) → final review → **L1-X2** (production-copy backfill and deploy, operator-assisted) → **L1-X3**
(operator review).
**File ownership:** in Stage 2 no lane edits `internal/api/api.go`, `internal/api/types_l1.go`, `internal/app/**`,
`internal/httpserver/server.go`, `internal/store/migrations/**`, `internal/provenance/**`, `internal/muses/{muse,key,ensure}.go`,
`internal/songs/{song,reconcile,title,angle,mapping}.go`, `internal/sessions/{session,ensure,sync}.go`,
`internal/picks/{derive,params,types}.go`, `internal/sounds/{parse,seed,bands,taxonomy,params}.go`,
`internal/library/{write.go,tracks.go}` (read amendments are in L1-B5's list), `internal/{idem,audit,gate}/**`,
`cmd/metamusic/{main.go,cmd_serve.go,cmd_mcpstdio.go}`, `openapi.json`, `web/src/api/schema.d.ts` (Stage-1 owned; a
needed change goes back through this document and a Stage-1-owner fix commit every lane rebases onto). Each lane
replaces only the stubs in its own `internal/api/ops_*_l1.go` files and owns its packages: Picks —
`internal/picks/{service,recompute,pin,propose,events,evidence}.go`, `ops_picks_l1.go`; Library —
`internal/{board}/**`, `internal/muses/service.go`, `internal/songs/{service,list,get,state,update}.go`,
`internal/sessions/service.go`, `ops_muses_l1.go`, `ops_songs_l1.go`, `ops_sessions_l1.go`, `ops_board_l1.go`, the
`library`/`trackctx` read amendments named in L1-B5; Sounds — `internal/sounds/{service,compute,facets,map,
taxonomy_ops}.go`, `ops_sounds_l1.go`; Ears — `internal/analysis/tempo*.go`, `internal/analysis/ffmpeg/tempo*.go`,
`internal/ears/**` (tempo, `FlagCodes`), `internal/ears/earstest/**`, `cmd/metamusic/cmd_analyze.go`; Web — `web/**`
except `schema.d.ts`; Infra — `.github/**`, `scripts/**`, `docs/**` (except the plan files), `Makefile`,
`coverage.thresholds`, `.gitleaks.toml`, `metrics/loop.schema.json`, `internal/archtest/**`.

### Task L1-B1: migration 0008, pure functions, import-graph test, schema tests

**Files:** `internal/store/migrations/0008_provenance.sql` (C88);
`internal/store/{env.go,instant.go,instant_test.go,songstate.go,triggers_l1_test.go,schema_l1_test.go,upgrade_l1_test.go}`;
`internal/textfold/{norm.go,norm_test.go}`; `library/drafts.go` + the M1/M2a contract of `DraftDetail.Reference` (D-L1-36, edited in the same commit);
`internal/store/upgrade_m3_test.go` (its one remaining literal `v != 7` becomes `v < 7`; `schema_m2c_test.go`, `schema_m3_test.go`, `store_test.go` and `httpserver/server_test.go` already compare with `len(migrations)` or a minimum);
`internal/perftest/{perftest.go,race.go,norace.go}` (C101.4; `race.go` is `//go:build race`, the `internal/jobs` `raceEnabled` pattern) and the helper list of `cmd/metamusic/nohelpers_test.go`; `internal/muses/{muse.go,key.go,key_test.go}`;
`internal/songs/{song.go,title.go,angle.go,title_test.go,angle_test.go}`; `internal/picks/{derive.go,params.go,types.go,
derive_test.go,evidence_test.go,testdata/evidence_*.golden.json}`; `internal/sounds/{parse.go,seed.go,bands.go,
taxonomy.go,params.go,parse_test.go,seed_test.go,bands_test.go,testdata/seed_v1.golden.json}`;
`internal/archtest/import_graph_test.go` (`l1Rules`, `TestImportGraph_L1`, `TestSQL_MuseTables_Allowlist`,
`TestSQL_WriteOwners_L1`); `internal/store/nodelete_test.go` (`deleteAllowlist`, C88.3); earlier plans: M2a
C27 addendum, M1/M2a `Reference` amendment.
**Gate (blocking):** the P.5 boxes for D-L1-6, D-L1-11 and D-L1-36 are ticked before this task starts.
**Tests first:**
- `TestMigration0008_Applies` (user_version 8 on top of 0007; every C88 table, index, view, FTS table and trigger
  exists; `pick_events`, `song_picks`, `songs` empty; `provenance_dirty` holds one row per tracks/drafts/runs/batches/
  collections row of the fixture; `PRAGMA foreign_key_check` empty; `sound_taxonomy_versions` empty — the seed is applied by
  the app, not the migration), `TestMigration0008_UpgradeFromM3` (`store.OpenRaw` + `store.MigrateTo(…, 7)` + `FixtureL1Upgrade`
  ⇒ `store.Open` migrates; every earlier row unchanged; queue rows = 1 per source row; from a schema-6 `FixtureM2c` (the
  `upgrade_m3_test.go` pattern) 0007 and 0008 apply together), `TestMigration0008_SeedsQueue` (counts per kind equal `SELECT COUNT(*)` of each source;
  distinct collection names); the existing `TestSchemaVersionAgnostic` (M3) stays green and `upgrade_m3_test.go`'s `v != 7` literal becomes `v < 7`.
- `TestTriggers_L1` table: DELETE on every new non-cache table ⇒ error (`muses`, `songs`, `pick_events`, `song_picks`,
  `pick_proposals`, `sessions`, `session_members`, `sound_taxonomy_versions`, `sound_terms`); UPDATE on `pick_events`/
  `session_members`/`sound_terms`/`sound_taxonomy_versions` ⇒ append-only; frozen columns (`muses.kind`,
  `songs.origin`, `sessions.source_key`, `song_picks.song_id`, `pick_proposals.take_id`) ⇒ `frozen column`; `songs`:
  `root_track_id` set twice ⇒ error, a non-root track ⇒ `not a lineage root`, retiring a Song with a root ⇒ error, update
  of a retired Song ⇒ `immutable`, two live Songs for one draft ⇒ UNIQUE error but a retired + live pair ⇒ ok, retire target
  retired ⇒ error; `pick_events`: seq gap ⇒ `broken chain`, wrong `from` ⇒ `broken chain`, `to_take_id` outside the Song ⇒
  error, no-op event ⇒ CHECK; `song_picks`: insert without a matching first event ⇒ error, update of `take_id` without a
  new event ⇒ `needs a matching new event`, with an event whose `from` is stale ⇒ error, evidence-only update ⇒ ok,
  `source = 'pinned'` with `take_id ≠ pinned_take_id` ⇒ CHECK; `pick_proposals`: take outside the Song ⇒ error, second open
  proposal of one principal ⇒ UNIQUE, decided proposal ⇒ immutable; `sound_terms`: insert into an old version ⇒ `only the
  newest version`, `sound_taxonomy_versions` gap ⇒ `consecutive`; `muses`: brief with artist ⇒ CHECK, reference with brief
  ⇒ CHECK, duplicate key ⇒ UNIQUE; `session_members`: unknown member ⇒ error.
- `TestQueueTriggers_Table`: insert track ⇒ `track` row; update status ⇒ row `at` advances; human rating insert/stars update ⇒
  row, **agent** rating ⇒ none; human verdict ⇒ two rows (both slots), critic verdict ⇒ none; analyses `tempo` ⇒ row, `loudness`
  ⇒ none; draft insert/`parent_track_id` update/`status` update ⇒ `draft` row; collection insert ⇒ `collection` + `draft` rows;
  run insert/state update, batch insert/state update, job `track_id` set ⇒ rows; a re-mark overwrites `at` (one row per key).
- `TestNorm_Table` (`textfold`: case, runs of spaces/tabs/NBSP collapsed, diacritics via `Fold`, `Norm("Hip-Hop")` keeps the hyphen, `Norm("Zebra")` sorts after `Norm("apple")`), `TestParseInstant_Table` (20-character `…05Z`, 24-character `….000Z`, other fractional widths, invalid ⇒ error; a same-second 20-character value is earlier than the same second at `.250Z`), `TestTriggers_L1_Rev2` (`song_roots`: update/delete refused, non-root ⇒ `not a lineage root`, retired Song ⇒ `song is retired`; retiring a Song that owns a `song_roots` row ⇒ error; `songs`: `muse_id` set with `muse_source` NULL ⇒ CHECK, `angle_source 'cleared'` with an angle ⇒ CHECK; the take-in-song triggers of `pick_events`, `song_picks` and `pick_proposals` accept a second root of the same Song and refuse a take of another Song; reason `pin_lost` ⇒ CHECK, `pin_ineligible`/`pin_restored` ok; proposal state `expired` ok; a second `provenance_lease` row ⇒ CHECK), and the queue additions of `TestQueueTriggers_Table` (a `recipe_refs` insert ⇒ the drafts of that recipe and its generation-0 tracks; every trigger re-mark resets `tries` to 0).
- `TestKey_Golden` (`Key("Mixed CASE  title", ptr("Ärtist"))` equals `Key("mixed case title", ptr("artist"))` through `textfold.Norm`; nil artist ≡
  empty artist; golden hex of three keys), `TestDefaultTitle_Table` (label `Title Words` ⇒ itself; label `variant-a` with angle `variant-a` and group key
  `ex_001` ⇒ `Ex 001 · variant a`; the same without an angle ⇒ `Ex 001`; empty label and empty group key ⇒ humanised request id `Ex 001 V1`; 250-rune title
  cut at a rune boundary), `TestHumanise_Table`, `TestAngleSlug_Table` (`Remake Long` ⇒ `remake-long`, `_x_` ⇒ ok `x`, `123` ⇒ not ok, `éé`
  ⇒ not ok, 40 chars ⇒ cut at 32).
- `TestPick_DerivationTable` — 32 rows, each `Input → Result{take, source, evidence.rule}` (timestamps given as instants): P01 no takes ⇒ none; P02 one `draft`
  take ⇒ newest; P03 one discarded ⇒ none; P04 A, newer B, no stars ⇒ B newest; P05 A 4★, newer B unrated ⇒ A stars; P06 A 4★, B 4★
  newer ⇒ B stars (`tied 2`); P07 A 5★, B 3★ newer ⇒ A; P08 pinned A, B 5★ ⇒ A pinned; P09 pinned A discarded, B 5★ ⇒ B stars with
  `PinIneligible` (the pin stays recorded); P10 intra 3, A `u 1.2 sd 0.5`, B `u 0.3 sd 0.6` (z 1.15), B 5★ ⇒ A ladder; P11 same with intra 2 ⇒ B stars
  (`ladder_skipped few_duels`); P12 intra 3, diff 0.3, sds 0.5/0.6 (z 0.38) ⇒ stars/newest with `ladder_skipped margin`; P13 intra
  3, diff 0.5, sds 0.6/0.8 (z exactly 0.5, epsilon rule) ⇒ ladder; P14 intra 3, A `u 1.30 sd 0.5` discarded, B `u 0.90 sd 0.5`, C `u 0.20 sd 0.6` ⇒ B ladder
  (z 0.90, computed over B and C only); P15 ladder leader A, pinned B ⇒ B pinned; P16 take C absent from the fit (`0, 1.5`) vs A `1.5/0.4` and B `−0.2/0.5`
  ⇒ A ladder (z 0.97); P17 critic (agent) stars only on the older A, B newer ⇒ B newest (critic stars never count); P18 lossy A 3★ with a newer
  recipe-unknown B ⇒ A stars; P19 equal `recorded_at`, B's id greater ⇒ B newest; P20 all takes discarded after a pick ⇒ none
  (`emptied` in C91.2); P21 ladder tie in `u` ⇒ fallback; P22 intra ≥ 3 but one eligible take ⇒ stars/newest with `single_take`;
  P23 stars cleared (nil) ⇒ not counted; P24 star 5 on a discarded take ⇒ ignored. **Stickiness (intra 3 throughout):** P25 incumbent A, A `u 0.70 sd 0.5`,
  B `u 0.46 sd 0.6` (z 0.31 ≥ 0.25) ⇒ A ladder (held); P26 the same without an incumbent ⇒ stars/newest (`ladder_skipped margin`); P27 incumbent A, B `u 0.54`
  (z 0.20 < 0.25) ⇒ stars/newest; P28 incumbent B `u 0.60 sd 0.6`, challenger A `u 1.00 sd 0.5` (z 0.51 ≥ 0.5) ⇒ A ladder (displaces); P29 incumbent B, A `u 0.90 sd 0.5`
  (z 0.38 < 0.5) ⇒ B ladder (`incumbent held`); P30 incumbent B discarded ⇒ the incumbent is ignored, fresh rule among the rest; P31 as P14 with B `u 0.45`
  (z 0.32) ⇒ stars/newest. **Mixed widths:** P32 A `recorded_at 2026-03-01T10:00:05Z` (20 chars), B `2026-03-01T10:00:05.250Z` (24 chars), no stars ⇒ B newest (a string compare
  would pick A: `.` sorts before `Z`). `TestPick_MixedTimestampWidths` repeats P32 through `store.ParseInstant` on real 20/24-character rows. `TestPick_EvidenceGolden` pins the JSON of
  each rule. `TestPicks_PriorSDMatchesLadder` (`LadderPriorSD == ladder.Options{}.PriorSD default 1.5`).
- `TestParse_Table` (24 rows): `"reggae, male vocal, warm, 76bpm"` ⇒ genre `reggae`, vocal `male`, mood `warm`, BPM 76, no
  unmapped; `"laid-back roots reggae groove"` ⇒ `roots reggae` (longest) ⇒ genre `reggae`, unmapped none (the part matched);
  `"Hip-Hop"` ≡ `"hip hop"`; `"female vocals"` and `"female vocal"` both ⇒ `female`; `"synthpop"` ⇒ pop **and** electronic;
  `"90s eurodance"` ⇒ era `90s` + genre `dance` (two phrases in one part); `"popular"` ⇒ no `pop` (whole words); `"italian"` ⇒
  language `it`; `"smoky late-night jazz bar ambience"` ⇒ `jazz` match; a 60-rune unknown part ⇒ not unmapped (> 40), a 12-rune
  unknown part ⇒ unmapped; `"120 bpm"` skipped; `";"`/newline separators; empty ⇒ empty; dedupe; ≥ 5-word phrase cut.
- `TestParse_BPMSkipMatchesStyleTerms` (30-row table: every part form `yue2.StyleTerms` accepts as a BPM term is skipped by the local matcher, and nothing else is), `TestParse_CaseAndSpace` (`"Hip-Hop"`, `"hip  hop"`, `"HIP HOP"` ⇒ the same match via `Norm`).
- `TestSeed_Golden` (the effective seed table equals `seed_v1.golden.json`; changing a value fails until the golden is
  regenerated deliberately), `TestSeed_Invariants` (every value in its facet's allowed set; every term folded; no duplicate
  `(term, facet, value)`; ≥ 150 terms; each `genre_family` value has ≥ 3 terms; every `era` term mapped), `TestTempoBand_Table`
  (79.9 ⇒ slow, 80 ⇒ mid, 109.9 ⇒ mid, 110 ⇒ upbeat, 129.9, 130 ⇒ fast, 159.9, 160 ⇒ rapid), `TestCompareTempo_Table`.
- `TestPerftest_Budget` (factor 1 by default, `MM_PERF_FACTOR` scales it, ×10 under `-race`, a perf test skips under `-short`), `TestNoTestHelpersInBinary` (existing; the helper list gains `internal/perftest`).
- `TestImportGraph_L1` (C87/C99 edges incl. `songview`; packages that do not exist yet skipped; neither revision-2 cycle — `muses`→`songs`→`muses`, `songs`→`picks`/`sounds` — can reappear), `TestSQL_MuseTables_Allowlist`,
  `TestSQL_WriteOwners_L1` (scaffold: the owner table of C88.2; fails when a non-owner package writes a table).
Done: `make check` green; `openapi.json` unchanged.

### Task L1-B2: contract freeze — types, 18 registrations, stubs, services, wiring, rigs, fixtures

**Files:** `internal/api/{api.go (Deps, New), types_l1.go, ops_muses_l1.go, ops_songs_l1.go, ops_picks_l1.go,
ops_sessions_l1.go, ops_sounds_l1.go, ops_board_l1.go}` (stubs answer 501); `internal/api/{ops_m2c_test.go
(TestRegistry_OpCount 99), ops_l1_test.go}`; `internal/muses/service.go`, `internal/songs/service.go`,
`internal/sessions/service.go`, `internal/picks/service.go`, `internal/sounds/service.go`, `internal/board/board.go`
(constructors and the frozen method sets of C89–C93/C97; bodies return `errors.ErrUnsupported` until their lane);
`internal/provenance/{service.go,queue.go,lease.go,report.go}` (constructor with the test seams `FaultAfterPhaseA`/`BeforeSettle`, `Pending`, the queue read/dequeue SQL, the settle lease; `Settle`
calls the phases that exist); `internal/songview/{service.go}` (frozen method set of C90.6/C90.8); `internal/app/{app.go,l1.go}` (`Options.L1 L1Options`, `Services` fields; `Build` constructs the services, wires `picks.Ladder` to
`duels.Service`, `songs.Flags` to `ears.Service`, `trackctx.LibraryReader`; `serve` starts `Run`);
`internal/trackctx/library.go` (interface + nil-safe adapter); `internal/servertest/{rig.go,rig_l1_test.go}`
(`Options.L1`, `Reconcile`, `Rig.Settle`); `internal/library/librarytest/{fixture_l1.go,fixture_l1_test.go}`
(C101.1); `internal/mcpgen/{parity_test.go,parity_l1_test.go,m2a_contract_test.go (ceiling)}`;
`internal/mcpgen/testdata/tools_list.golden.json`; `openapi.json`; `web/src/api/schema.d.ts`; earlier plans: M3
C81/C85/C86 and M2a C35/C38/C41/C42 addenda.
**Tests first:**
- `TestRegistry_OpCount` (99, "81 + 18 of L1"), `TestOps_RegisteredWithMetaL1` (exact Meta of the C96 table: scopes, costs,
  `mcp` faces — 7 tool, 9 generic, 2 `exclude:human-judgment` — UI routes, `C`/`H` flags, `human_only` false for all),
  `TestL1_NoSpendOps` (no op has cost ≠ free), `TestOps_BodyLimitsL1`, `TestOps_StubsAnswer501L1` (every stub ⇒ 501
  `NOT_IMPLEMENTED`, removed per lane), `TestToolsList_L1Budget` (the existing `TestToolsList_Ceiling` and `TestToolsListBudgetFires` stay; this adds: L1 tools ≤ 16 000 B or the demotion order applied — the test prints the measurement), `TestOps_HumanJudgment_NoMCP`
  (`pin_pick`, `dismiss_pick_proposal` absent from `tools/list` and from `call_operation`'s catalogue).
- `TestFixtureL1_Shape` (row counts of the L1 rows by SQL: 6 Muses' worth of refs ⇒ 18 refs rows; 18 + 4 drafts; 3 + 6 reference
  tracks; 2 groups with `p2`/`p3` parented to `p1`; one mothered draft; two collections; `Settle` is **not** run:
  `songs` is empty and `provenance_dirty` is non-empty), `TestFixtureL1Mirror_Params` (parameters ⇒ counts for three parameter
  sets), `TestFixtureL1Upgrade_IsSchema7`, `TestFixtures_L1_SyntheticOnly` (names match `Synthetic Muse NNN`/`Synthetic Artist
  NNN`), `TestRig_L1Options` (requires Fixture/M2c/M3; `Rig.Settle` drains; default `Reconcile` leaves the queue).
- `TestParity_L1Cases` scaffold: a case exists for each of the **16** ops with an MCP face (the runner fails on a missing case by name); `pin_pick` and `dismiss_pick_proposal` are checked absent from MCP by `TestOps_HumanJudgment_NoMCP`.
Done: `make check` green; `make openapi` committed; every stub reachable over REST and MCP (501); `TestImportGraph_L1` green.

### Task L1-B3: the Reconciler core — Songs, Muses from refs, Sessions, backfill, `Verify`, CLI

**Files:** `internal/muses/{ensure.go,ensure_test.go}`; `internal/songs/{reconcile.go,mapping.go,reconcile_test.go,
reconcile_table_test.go}`; `internal/sessions/{ensure.go,sync.go,sync_test.go}`; `internal/provenance/{reconcile.go,lease.go,lease_test.go,
verify.go,steps.go,reconcile_test.go,verify_test.go,property_test.go,backfill_test.go}` (phase A and the dequeue; phase B
calls `picks`/`sounds` through the two small interfaces `provenance.PickStep{RecomputeTx}` and `provenance.SoundStep{ComputeTx,
SweepStaleTx}` frozen in L1-B2, with `nopPicks`/`nopSounds` implementations until L1-B7 wires the real ones); `cmd/metamusic/{cmd_library.go,cmd_library_test.go,main.go (dispatch), cmd_import.go, cmd_seed.go}` (D-L1-27);
`internal/app/l1.go` (`L1Options`, wiring), `cmd/metamusic/cmd_serve.go` (startup `Settle` before `net.Listen`, then `Run`); earlier plans: M0 C7 addendum.
**Tests first:**
- `TestMuses_EnsureReferenceTx`: `("Mixed  Title","Ärtist")` creates; `("mixed title","artist")` returns the same id,
  `created=false`; `("Mixed Title", nil)` creates a second Muse (no artist ≠ artist); title over 200 runes ⇒ error; the row's
  `source = 'recipe_refs'`, `created_by = 'system:provenance'`, `title_fold` set; FTS finds it.
- `TestSongs_ReconcileTracks`: a fresh root ⇒ 1 Song (`origin 'root'`, title = the track's title); a child of it ⇒ no new Song;
  `take_songs` maps both to the Song; a root with a rootless draft Song whose draft it fulfilled ⇒ the Song gains
  `root_track_id` (id and Muse unchanged, `revision` unchanged); a second pass ⇒ no change.
- `TestSongs_ReconcileDrafts_Table` (the rows of the acceptance mapping): backlog motherless draft ⇒ Song `origin 'draft'`, title
  per D-L1-8, angle/Muse from its ref; draft fulfilled by a root ⇒ attaches (or creates with both); fulfilled by a child ⇒ no
  Song / retires an existing takes-less one into the child's root Song; draft inserted with a mother ⇒ no Song; draft that gains
  a mother after its Song exists ⇒ `retired_at`, `retired_into` = the mother's Song, Muse of the retired Song untouched; mother
  later removed ⇒ a **new** live Song, the retired row stays; two drafts fulfilled by one root ⇒ the first attaches, the second
  gets nothing and `Verify` is empty; a draft Song edited by a human (`muse_id` set to another Muse, then a later dirty of the
  draft) ⇒ the human edit survives (**never re-derived**); muse/angle are filled only in the creating/attaching statement.
- `TestAngle_RequestIDSuffix_Unambiguous`: refs whose `_v2` rows all say `variant-b` ⇒ a draft without a ref and id
  `x_v2` gets that angle with `angle_source 'request_id'`; two different types for `_v2` ⇒ none; `_v10` suffix parsed.
- `TestSessions_SyncRun/Collection/Batch`: names, kinds (`upload` iff all drafts `jsonl_upload`), `started_at`/`ended_at`
  (NULL while the run runs/the batch is live), members (tracks by `import_run_id`, drafts by first `recipe_sources` run, batch
  jobs' drafts and completed tracks), idempotence (second sync adds 0), rename survives, hidden still collects.
- `TestReconciler_PassIdempotent` (two `Settle`s: identical table contents by a hash over every L1 table except
  `computed_at`/`at`), `TestReconciler_RemarkedKeySurvives` (a key re-marked between read and delete stays queued),
  `TestReconciler_CrashBetweenPhases` (`Options.FaultAfterPhaseA` returns an error after phase A commits: queue intact, `Settle` converges), `TestReconciler_BatchBoundary`
  (250 dirty drafts ⇒ 3 passes), `TestServe_SettlesBeforeListening` (`cmd/metamusic`, the `serve_test` pattern: the schema-7 upgrade fixture with `app.Options.L1.BeforeSettle` holding the first `Settle`: the address does not accept a connection while it is held and the queue is non-empty; released ⇒ order EnsureSeed → Settle → listening, then 200), `TestBackfill_UpgradePath`, `TestBackfill_Idempotent`, `TestBackfill_MuseFromRefs`,
  `TestAcceptance_L1_BackfillMirror` (C95.3 with the mirror parameters; `Verify` empty), `TestAcceptance_L1_ReferenceSongs`.
- **Revision-2 tests:** `TestSongs_RootsAttach_FromDraft_N3` (draft seed + 2 new motherless roots ⇒ one Song, three `song_roots`, the draft Song's Muse/angle/title untouched), `TestSongs_RerenderFulfilledDraft` (a new root of the same draft joins the Song), `TestSongs_ComposeNew_N3_OneSong` (job-item key; plus the two-identical-items case ⇒ two Songs), `TestSongs_RootsOrderIndependent`, `TestSongs_RetireCarriesMuse` (target without a Muse gains the retired Song's Muse and angle, `revision` bumped, one `song_muse_carried` log record with ids only; a target with a Muse keeps it), `TestMuse_LateRefReachesSong`, `TestMuse_ClearedNeverRefilled`, `TestMuse_RootSongFromRecipeRefs`; `TestReconciler_DraftBeforeTrackAcrossBatches` (`BatchKeys 100`, 150 tracks + 150 drafts, the drafts older by `at` than the tracks and some fulfilled by child takes or given mothers: selection is kind-rank-first, `Verify` empty, no draft Song left that should be retired), `TestReconciler_TransientAnomalyRequeues` (a draft whose root Song is missing keeps its queue row with `tries` incremented, converges once the track arrives, and is dropped with the anomaly reported at `MaxQueueTries`), `TestReconciler_NoPassCap` (1 000 keys settle in one call); `TestSettle_TwoSettlers` (two `provenance.Service` values on one DB file with different owners: concurrent `Settle` ⇒ exactly one processes and the other returns `ErrSettleBusy`; a lease past its `expires_at` is taken over; a settler whose `epoch` went stale writes nothing: `ErrLeaseLost`), `TestCLI_ReconcileWhileLeaseHeld` (prints the queue, exits 0, writes nothing; `--wait` returns once the other settler drains); `TestSessions_KindFrozenAtFirstSync`, `TestSessions_NoEmptyBatchSessions`, `TestSessions_TimestampsNormalised`.
- `TestVerify_DetectsEachAnomaly` (one planted fault per I1–I9 in a **scratch DB**; faults the new triggers forbid (`songs_draft_once`, `song_roots_*`, `pick_events_chain`, …) are planted after the test drops the guarding trigger there with `dropTrigger(t, name)`; each
  yields the named code), `TestProperty_ProvenanceInvariants` (seeded `math/rand` sequences of: insert draft/ref, set/clear mother,
  insert root/child take, fulfil draft, rate, delete-free status change, duel verdict; `Settle` every few steps and at the end;
  oracle = `Verify` empty and `Settle` idempotent; 60 sequences, `MM_PROPERTY_SEQS` overrides), `TestCLI_LibraryReconcile`
  (`--dry-run` prints the queue by kind and writes nothing; real run drains), `TestCLI_LibraryVerify` (exit 1 on a planted fault),
  `TestCLI_ImportSettles` (a CLI import leaves an empty queue).
Done: `make check` green; the mirror backfill passes with `nopPicks`/`nopSounds`.

### Task L1-B4 (Picks): derivation wiring, events, pins, proposals, ops

**Files:** `internal/picks/{service.go,recompute.go,pin.go,propose.go,events.go,evidence.go,*_test.go}`;
`internal/api/ops_picks_l1.go`; `internal/api/ops_picks_l1_test.go`; `internal/acceptance/…` parity scripts for `list_pick_events`.
**Tests first:**
- `TestPicks_RecomputeWritesEventOnlyOnChange`: first recompute of a Song with 2 takes ⇒ event `initial` (seq 1) and a
  `song_picks` row; unchanged inputs ⇒ no event, `computed_at` refreshed; a human 5★ on the older take ⇒ event `stars`
  (seq 2) with `from`/`to` set; discard the picked take ⇒ event to the next (`newest`/`stars`); discard every take ⇒ event
  `emptied`, source `none`; un-discard ⇒ an event from none with reason `initial` (C91.2: the previous source was `none`; `TestPickEvents_ReasonTable`
  pins the whole reason table).
- `TestPicks_RecomputeAfterVerdict` (rig with a fake `Ladder`; three blind human verdicts between A and B with strengths
  `A 1.2±0.5`, `B 0.3±0.6` ⇒ pick flips to A with reason `ladder` and the evidence of C91.3; a critic verdict never does),
  `TestPicks_SweepCatchesLadderDrift` (the fake ladder shifts strengths so that a Song's margin crosses 0.5 with no trigger
  row ⇒ `Sweep` writes the event), `TestPicks_TriggerEnqueue_Table` (every source of C88's queue marks the take).
- `TestPicks_NoFlipFlopUnderDrift` (a fake ladder drifting a Song's margin 0.5 → 0.4 → 0.3 → 0.5 → 0.2 across sweeps: one `ladder` event at 0.5, held through 0.3, one fallback event at 0.2), `TestPick_LadderStickiness` (P25–P31 through the service with a stored incumbent), `TestProposals_ExpireOnDiscardOrPick` (discarding a proposed take, or it becoming the Pick, expires the proposal with `decided_by system:provenance`; the 20-open cap counts only `open`; Accept of an expired proposal ⇒ 409 `PROPOSAL_CLOSED`), `TestPicks_IntraDuelsMatchesLadderCounting` (a two-take Song with every combination of blind/non-blind, human/critic and each verdict kind: `IntraDuels` equals the `n_duels` the `duels` fit reports for either take — fails if the M2b counted-verdict definition changes).
- `TestPin_Rules`: human pins an eligible take ⇒ source `pinned`, event `pin`, `pinned_by` = the human key, `PinResult.changed`;
  pinning the same take again ⇒ 200 `changed false`; pin a take of another Song ⇒ 422 `TAKE_NOT_IN_SONG`; a discarded take ⇒ 422
  `TAKE_NOT_ELIGIBLE`; unpin ⇒ event `unpin` and the derivation resumes; unpin with no pin ⇒ 200 unchanged; pin on a Song without
  a `song_picks` row ⇒ creates it through a `pin` first event; a device principal may pin; `TestPin_AgentRefusedREST` (PAT agent ⇒
  403 `HUMAN_ONLY`, no state change, audit row with status 403); `TestPick_PinIneligibleThenRestored` (pinned take discarded ⇒ next
  Settle writes `pin_ineligible` and keeps `pinned_*`; un-discard ⇒ `pin_restored` back to the pin; explicit unpin ⇒ `unpin`).
- `TestPropose_Rules` (agent proposes ⇒ 201 open; human ⇒ 403 `HUMAN_ONLY` hint `pin_pick`; the current Pick ⇒ 409 `ALREADY_PICK`;
  re-proposal supersedes the previous open one; 21st open ⇒ 409 `TOO_MANY_PROPOSALS`; take outside the Song ⇒ 422),
  `TestProposals_Lifecycle` (pin with `proposal_id` closes it `accepted`; wrong take ⇒ 409 `PROPOSAL_MISMATCH`; closed ⇒ 409
  `PROPOSAL_CLOSED`; dismiss closes `dismissed`; `TestDismiss_HumanOnly`), `TestEvents_Feed` (ascending by integer `cursor`, `after` cursor, `song_id` and `song_ids` (≤ 200) filters, page size, `actor` rendered with `api.RefFor(viewer, actor, actor_kind, actor_name)`), `TestEvents_CursorMonotonic` (events inserted with out-of-order UUIDv7 ids still page strictly in cursor order, none skipped or repeated), `TestPick_AgentRatingsNeverCount`, `TestRatingIsolation_StillHolds`.
- REST tests for the four ops (`ops_picks_l1_test.go`): shapes, status codes, the audit rows, `Idempotency-Key` replay of
  `propose_pick` (same key ⇒ stored 201, one proposal).
Done: the Picks stubs removed from `TestOps_StubsAnswer501L1`; `TestPick_*` green; `make check` green.

### Task L1-B5 (Library): Muses, Songs, Sessions, Board, read amendments, `/context`

**Files:** `internal/muses/{service.go,list.go,ref.go,*_test.go}` (C89); `internal/songs/{service.go,list.go,update.go,filter.go,*_test.go}` (C90.5, C90.7); `internal/songview/{view.go,muse_summary.go,*_test.go}` (C90.6, C90.8); `internal/store/songstate.go` (C90.4 SQL, owned by B1); `internal/sessions/{service.go,*_test.go}` (C92.2); `internal/board/{board.go,query.go,
*_test.go}` (C97); `internal/api/{ops_muses_l1.go,ops_songs_l1.go,ops_sessions_l1.go,ops_board_l1.go}` + tests;
`internal/library/{tracks.go,drafts.go,…}` read amendments (`drafts.go`: the D-L1-36 `Reference` visibility)  (C98: `song_id`, `is_pick`, filters, `TrackDetail.song`; D-M3-35 gating) and
`internal/library/read_l1_test.go`; `internal/library/stats.go` + `internal/api/ops_stats.go` (`library_model`);
`internal/trackctx/{trackctx.go,library.go,trackctx_l1_test.go,testdata/*}` (C98 sections, trimming, goldens);
`internal/api/ops_context.go` (wire the reader); earlier plans: M1 C17/C19, M2a C35/C38 addenda.
**Tests first:**
- `TestMuses_CreateRules` (reference/brief validity table: 12 rows incl. over-long, artist on a brief, brief on a reference,
  duplicate key ⇒ 409 `MUSE_EXISTS` with `details.muse_id`/`hidden`; agent ⇒ `source 'agent'`, human ⇒ `human`),
  `TestMuses_UpdateRules` (revision lock; title change recomputes `key`/`title_fold`; collision ⇒ 409; hide/unhide; `kind` immutable;
  empty patch ⇒ 422), `TestMuses_AssignSongs` (all-or-nothing; unknown Song ⇒ 422 `UNKNOWN_SONG`; retired ⇒ 409 `SONG_RETIRED`; hidden Muse ⇒
  409 `MUSE_HIDDEN`; `assigned`/`unchanged` counts; `revision` bumped only on change), `TestMuses_ListFTS` (quoted tokens, prefix, a raw
  `NEAR(` or `"` in `q` cannot break the query), `TestMuses_SummaryCounts` (in `songview`: equals independent SQL per state), `TestMuses_KeyGolden`.
- `TestSongs_StateTable` (rows: no takes ⇒ draft; one take ⇒ rendered; human star ⇒ judged; status `final` ⇒ judged; human duel
  verdict ⇒ judged; pick source `ladder` ⇒ picked; pick source `stars` ⇒ judged not picked; pinned ⇒ picked; a live job ⇒ `queued_jobs 1` with the
  base state unchanged; archived draft ⇒ `archived`), `TestSongs_ListFilters_Table` (every filter alone and combined on the L1 fixture;
  hidden/archived defaults; `session_id` via derived membership; sound filters; `has_pick`; keyset pagination stable across pages),
  `TestSongs_Update` (C90.5 table), `TestSongView_Takes` (order pick first; `take_limit`; `ladder`/`flags` null with nil collaborators;
  lossy/reference cards), `TestSongView_TempoRequestedVsMeasured` (with a fake `tempo` analysis row), `TestSongView_PendingFlag`.
- `TestSessions_List` (counts `drafts/takes/songs`, filters, keyset), `TestSessions_Update`.
- **Revision-2 tests:** `TestListSongs_ByDraftAndTrack` (`draft_id` of a backlog draft, of a fulfilled draft, `track_id` of a child take ⇒ the one Song; unknown ⇒ empty), `TestListSongs_HasOpenProposals`, `TestGetSong_ByDraftAndTrack` (404 `NOT_FOUND` with `details.pending` while the key is queued, 200 after `Settle`), `TestGetSong_RetiredAnswers200` (`retired`, `retired_into`, empty takes; `update_song` on it ⇒ 409 `SONG_RETIRED`), `TestSongs_StateTable_LiveJobs` (`queued_jobs` counts exactly `queued`/`submitting`/`starting`/`generating`/`transferring`/`ingesting`; a job of an `awaiting_confirmation` batch and terminal jobs do not), `TestBoard_UnassignedCapped` (1 000 unassigned Songs: first response ≤ 200 chips, `chips_more`, complete counts, `unassigned_after` pages through all exactly once, every page ≤ 90 000 B), `TestBoard_FiveStatements` (a statement counter: ≤ 5 per call), `TestDraftReference_AgentView` (a human reads `reference`; a read-scope agent reads the same text, equal to the Muse's title/artist; a draft with no ref ⇒ null for both).
- `TestBoard_Equivalence`, `TestBoard_PageCap`, `TestBoard_FiltersTable`, `TestBoard_UnassignedRow`, `TestBoard_CountsVsChips` (`detail=counts`
  and `chips` agree), `TestBoard_DraftIDOnlyForDrafts`, `TestPerf_MuseBoard` (200×3 `FixtureL1Scale` + `Settle`).
- Op REST tests: shapes, status codes, 404/409/422 codes and **ids-only messages** (`TestMuseOps_ErrorsCarryIDsOnly` is finished in B7),
  idempotent replay of `create_muse`, rate classes, `UNAVAILABLE` when a service is nil.
- `TestSearchTracks_SongFilters`, `TestTrackSummary_SongFields`, `TestTrackDetail_Song`, `TestLibrary_OldSchemaFixturesStillRead`
  (`FixtureV2`, `Fixture`, `FixtureM2c`, `FixtureM3` unchanged output), `TestGetStats_LibraryModel`.
- `TestContext_L1Sections` (song/muse/sounds present and ordered after `track`; a take without a Song ⇒ three nulls; `muse.brief` cut at 500),
  `TestContext_L1Trim` (siblings 12 → 5 → 0, then brief to 200; `_budget.truncated` names), `TestContext_NoLibraryReader`.
Done: Library stubs removed from `TestOps_StubsAnswer501L1`; `make check` green.

### Task L1-B6 (Sounds): taxonomy service, compute, facets, ops

**Files:** `internal/sounds/{service.go,compute.go,facets.go,map.go,taxonomy_ops.go,unmapped.go,*_test.go}`;
`internal/songs/filter.go` (only `FilterSQL`'s sound clauses, coordinated with B5 through the frozen signature);
`internal/api/ops_sounds_l1.go` + tests; `cmd/metamusic/cmd_serve.go` is Stage-1
owned (the startup `EnsureSeed` call lives in `app/l1.go`).
**Tests first:**
- `TestEnsureSeed_Idempotent` (two calls ⇒ one version with `seed_id seed/1`; concurrent calls ⇒ one version; `sound_terms` count = the
  seed table's), `TestTaxonomy_EffectiveFold` (seed + extension add + extension remove ⇒ index at each version; `BuildIndex(v)` of an old version is
  unaffected by later rows), `TestIndex_CachedPerVersion`.
- `TestCompute_KnownRecipe` (a known recipe with style `reggae, male vocal, 76bpm`, Italian lyrics, a score with `Q:` ⇒ take rows: engine
  `yue2/recipe`, genre `reggae/style`, vocal `male/style`, tempo `slow/declared` (76 BPM < 80 ⇒ `slow`; the test pins the band edge), tempo
  plan row when the score differs, language `it/lyrics`), `TestCompute_ReferenceMeasuredOnly` (stub recipe ⇒ no rows until a tempo analysis
  exists; with `{bpm: 95}` ⇒ exactly one row `tempo_band mid/measured`; never engine/genre), `TestCompute_SongSubjectFollowsPick` (picked take
  changes ⇒ `song_sounds` recomputed from the new subject; draft Song uses its draft request and recomputes when the draft's `revision`
  changes; collapsed tempo row prefers measured, `detail` `{requested_bpm, measured_bpm, basis, mismatch, octave}`), `TestCompute_Freshness`
  (digest unchanged ⇒ no rewrite; taxonomy version bump ⇒ rewrite), `TestUnmapped_Cache`.
- `TestMap_Rules` (validation table: 14 rows incl. `tempo_band` ⇒ `FACET_NOT_MAPPABLE`, bad era value, term without a letter, 51 mappings ⇒ 422,
  removing a non-effective mapping ⇒ `MAPPING_NOT_EFFECTIVE`, all-unchanged ⇒ no version), `TestMap_RecomputesAffected` (adding `skank` → genre
  `reggae` makes a draft Song with that term appear under `reggae`; `recomputed.songs ≥ 1`; the unmapped list loses the term), `TestMap_AgentAllowed`
  (write scope), `TestFacets_CountsExcludeOwnSelection` (standard faceted semantics on a 40-Song table), `TestFacets_TempoMismatches`,
  `TestSearchSounds_Equivalence` (`songs_matching` = `list_songs` total for the same filter), `TestTaxonomy_Unmapped_Ordering`,
  `TestPerf_SearchSounds`, `TestPerf_TaxonomyRecompute`.
- `TestSeed_CoverageSynthetic` (a committed 40-style synthetic corpus: ≥ 85 % of the Songs get a `genre_family`, ≥ 95 % a term-derived facet; the unmapped list is printed), `TestCompute_DeclaredPrefersBpmDeclared` (`tracks.bpm_declared` wins over re-parsing; absent `scores` ⇒ no `plan` row), `TestCompute_NoTempoLane` (no `tempo` analyses at all ⇒ every non-tempo facet unchanged, no `measured` row).
- REST tests for `search_sounds`, `get_sound_taxonomy`, `map_sound_term` (+ `heavy` rate class, audit row, no secret).
Done: Sounds stubs removed from `TestOps_StubsAnswer501L1`; `make check` green.

### Task L1-E1 (Ears): tempo analyzer, `FlagCodes`, CLI

**Separable lane (D-L1-39).** Tempo is the one unproven item. This lane merges independently: if it slips or fails the real-music spot check, L1 ships with Sounds degrading to "requested only"; no other task, test or acceptance item (including `TestAcceptance_L1_BackfillMirror`) depends on it. `FlagCodes` is not part of the risk and lands first.

**Files:** `internal/analysis/{tempo.go,tempo_test.go,testdata/tempo_*.golden.json}`; `internal/analysis/ffmpeg/{tempo.go,
argv_tempo_test.go,tempo_test.go}`; `internal/ears/{analyze.go (plan + compute + store), types.go (TierA.Tempo, Perception fields),
flags_codes.go,dossier.go,perception.go,*_test.go}`; `internal/ears/earstest/**` (the dossier and perception types are `ears.Dossier` and `ears.Perception` in `ears/types.go`; `internal/api/types_m2b.go` only wraps `ears.Dossier` and needs no edit; `internal/trackctx/**` is B5's, see B7 for the `perception` keys); `cmd/metamusic/cmd_analyze.go` (no new flag; the new analyzer runs by the Backfill rule);
`internal/mcpgen/testdata/*` (only if the dossier shape is in a golden); earlier plans: M2b C49/C51 addenda.
**Tests first:**
- `TestTempo_SyntheticClicks`: generated mono 11 025 Hz signals at hop 128 — a kick+hat pattern at 70, 90, 120 and 140 BPM, 60 s each, exact timing — return `BPM` within ±1.5 of the
  truth **or** of its octave (when `OctaveAmbiguous`, the truth appears among `Candidates`); `Confidence ≥ 2.0`. A 120 BPM click track with ±20 ms jitter is asserted at ±3 BPM
  (one frame at 140 BPM is ≈ 1.9 BPM, a jitter case at ±1.5 is plain-flaky). `TestTempo_NoiseNoBPM` (white noise and a sustained chord ⇒ `BPM nil`), `TestTempo_ShortFile` (< 20 s ⇒ nil, no error),
  `TestTempo_Deterministic` (two runs ⇒ identical JSON), `TestTempo_Golden` (pinned metrics JSON for one fixed signal),
  `TestPerf_Tempo` (240 s < 2 s CPU under the env-scaled budget).
- `TestFFmpeg_TempoDecodeArgv` (the exact argv: `-ac 1 -ar 11025 -t 240 -f f32le`), `TestFFmpeg_Tempo` (media tier: a synthetic 120 BPM FLAC ⇒ within ±1.5).
- `TestEars_AnalyzeRunsTempo` (the fake `TierA.Tempo` ⇒ an `analyses` row `tempo`, version `mm-tempo/1 <tool>`; skipped on the second run; `Force` re-runs; a
  failing tempo is a warning in `Outcome.Failed` and the other analyzers still store), `TestEars_TempoBlindSpots` (the dossier lists the C94 blind spots),
  `TestEars_FlagCodes` (batch of 3 takes ⇒ codes per take; takes without analyses ⇒ empty; > 100 ids ⇒ error), `TestPerception_TempoFields`.
Done: `make test` (media tier) green for the tempo tests; `make check` green.

### Task L1-W1 (Web): routes, view models, Board

**Files:** `web/src/lib/router.ts` (+ `router.test.ts`), `web/src/App.svelte` (the `<nav>` links; `lib/nav.ts` is unchanged); `web/src/lib/{boardView.ts,museApi.ts,
pickView.ts}` + `.test.ts`; `web/src/lib/components/{BoardGrid.svelte,SongChip.svelte,BoardToolbar.svelte,MusePicker.svelte,SessionPanel.svelte,
PickBadge.svelte}`; `web/src/routes/{Board.svelte,Board.test.ts}`; `web/src/lib/fixtures/*`.
**Tests first (bun):** `boardView.test.ts` — the grid model from a `Board` fixture (columns order, `(none)` column last, cells by angle, Unassigned row last);
selection toggle, shift-range inside a row, "select all drafts in view" capped at 50 (`MAX_TAKES`) with a note, non-draft chips skipped by Estimate;
filter ⇄ query string round trip (repeated `sound=` keys, `session`, `angle`, `state`, `q`, `muse`); `draftsIntent` of the selected draft ids; a 600-chip
model builds in < 50 ms. `pickView.test.ts` — `pickEvidenceText` for each rule and for `none`. `museApi.test.ts` — wrappers send the idempotency header
for `create_muse`, map 409 `MUSE_EXISTS` to the existing Muse. `router.test.ts` — the four new routes match, specific before parametric.
Done: `make check` (web lint/check/unit) green; no `fetch` outside `web/src/api/`.

### Task L1-W2 (Web): Song page and Muse page

**Files:** `web/src/lib/{songView.ts,songView.test.ts}`; `web/src/lib/components/{TakeCard.svelte,PickHistory.svelte,ProposalList.svelte,
TempoBadge.svelte}`; `web/src/routes/{Song.svelte,Muse.svelte,Song.test.ts}`.
**Tests first (bun):** `songView.test.ts` — take ordering (pick first), duel selection (exactly two, a third replaces the oldest), pending refetch policy (1.5 s,
max three, stops when `pending` is false), pin availability (hidden for a take that is discarded), proposals actions map to `pin_pick{proposal_id}` /
`dismiss_pick_proposal`; `TempoBadge` text table (`requested 76 · measured 75 ✓`, mismatch, octave `≈`, requested-only, measured-only, none).
Done: `make check` green.

### Task L1-W3 (Web): Sound browser and entry points

**Files:** `web/src/lib/{soundsView.ts,soundsView.test.ts}`; `web/src/lib/components/{FacetPanel.svelte,TaxonomyTable.svelte}`;
`web/src/routes/{Sounds.svelte,Track.svelte (Song strip),Library.svelte,Backlog.svelte (Board link)}`, `web/src/lib/components/{TrackBadges,TrackTable,LibraryFilters}.svelte` (Pick badge + Picks only);
`web/src/lib/libraryQuery.ts` (+ test) for the `is_pick` filter.
**Tests first (bun):** `soundsView.test.ts` — facet selection ⇄ `sound=` params, selected pills, counts rendering order, the "Map…" form validation per facet
(`era` regex, `language` two letters, `tempo_band` not offered), tempo mismatch toggle; `libraryQuery.test.ts` — `is_pick=true` round trip; `Track.test.ts`
extended — Song strip shows the Pick badge when `is_pick` and the "Pick of this Song" link otherwise.
Done: `make check` green.

### Task L1-I1 (Infra): CI, coverage, docs, gate, performance harness

**Files:** `.github/workflows/*` (L1 packages in `go-fast`; tempo tests in `go-media` via `scripts/ci-go-test.sh`; weekly 300-sequence property run);
`coverage.thresholds`; `scripts/ci-go-test.sh`; `docs/{architecture.md,runbook.md,device-checklist.md}`; `Makefile` (no new target; `make e2e`
picks up the specs); `internal/archtest/**` (rules land with B1; this task only wires them into the CI tier); `metrics/loop.schema.json` (no change expected).
**Tests first:** `scripts/ci-go-test.sh` selftest lists the new packages in the right tier; `TestCoverageThresholds_L1` (the file names every L1 package
with its minimum); a docs link check for the new sections.
Done: CI green on a branch; coverage job reports the L1 packages.

### Task L1-B7 (Stage 3, Backend): wiring, acceptance, property, leaks, parity

**Files:** `internal/app/{app.go,l1.go}` (final wiring: real `picks`/`sounds` steps in the Reconciler, `Run` in `serve`); `internal/provenance/steps.go`
(remove `nop*`); `internal/acceptance/{l1_test.go,l1_rig_test.go,every_ui_action_test.go,nosecretleak_test.go,names_test.go}`;
`internal/mcpgen/parity_l1_test.go`; `internal/trackctx/{trackctx.go (order["perception"] gains `tempo_bpm`, `tempo_confidence`),testdata/*.golden.json}` (re-baselined once, after the Ears lane merged: B5 owns the song/muse/sounds baseline, E1 owns the fields, B7 runs the single final `-update`); `internal/api/ops_l1_leaks_test.go`; `internal/servertest/**` adjustments; `internal/store/*_l1_test.go` completions.
**Tests first:** `TestAcceptance_L1_*` (the acceptance mapping), `TestMuseText_OnlyInAllowlistedOps`, `TestMuseOps_ErrorsCarryIDsOnly`,
`TestNoSecretLeak_L1` (extends `TestNoSecretLeak`: confirmation tokens, upload signatures and the L1 canaries — Muse, collection-derived Session name, agent `rationale`, agent `sound_terms.term` — appear nowhere they must not),
`TestAcceptance_M2a_AgentPerformsEveryUIAction` (14 new script entries), the parity cases with REST = MCP = stdio after normalisation,
`TestProperty_ProvenanceInvariants` with picks and sounds enabled (events chain, sound freshness), `TestRestore_RebuildsDerived` (restore a snapshot of the
mirror, drop the derived rows, `Settle` ⇒ `Verify` empty), `TestAcceptance_L1_EndToEnd_Agent` (go-sdk MCP client with a read+write PAT: `list_muses` ⇒
`get_muse` ⇒ `create_muse` + `assign_muse_songs` ⇒ `get_song` ⇒ `propose_pick` ⇒ `search_sounds`; the human pin is applied over REST with a human principal and
`list_pick_events` shows pin and proposal in order; `get_audit` lists every step and no Muse text).
Done: all named tests green; `make check` and `make test` green.

### Task L1-W4 (Stage 3, Web): Playwright L1 specs

**Files:** `web/e2e/{l1-board.spec.ts,l1-song.spec.ts,l1-sounds.spec.ts,l1-muse.spec.ts,l1Api.ts}`; `web/playwright.config.ts` (the fourth phase, C100.7);
`internal/synth/archive.go` only if the e2e profile needs one more index row (no).
**Tests first (chrome + firefox):** `l1-board.spec.ts` — the Board shows the e2e archive's Muses with chips by angle; create a Muse and assign two selected chips; filter by
session and by a facet; select drafts and press Estimate ⇒ the page lands on `/human/generate` with the draft count (the spec stops there: it never confirms spend); the
50-selection cap note; `l1-song.spec.ts` — a rendered Song shows its cards, rate a take, pin it (Pick badge moves, history shows `pin`), unpin, pending refetch, start a blind duel
from two cards, an agent proposal (created through the API helper) appears and Accept pins it; `l1-sounds.spec.ts` — facet counts and drill-down, the tempo mismatch toggle,
map an unmapped term and see the Song appear under the facet; `l1-muse.spec.ts` — edit title/artist with the revision lock (a stale edit shows the conflict), hide ⇒ gone from the Board.
Every spec resets its own data and does not depend on counters other specs change; served-operation coverage reports zero unserved ui ops; `/__e2e/leaks` is `[]`.
Done: `make e2e` green.

### Task L1-X1 (Integration)

Merge all lanes onto `l1-int`; regenerate `openapi.json` and `schema.d.ts` (must be unchanged after B2); copy the **public** part of this plan to
`docs/plans/<date>-l1-library-provenance.md` with `scripts/l1-public-copy.sh` (the same cut as the alignment step: `sed '/^<!-- PRIVATE APPENDIX -->$/,$d'`, then `git add` and `scripts/public-gate.sh --staged`), which cuts at the private-appendix marker comment (the only line holding that exact comment; no other text may spell it) and fails when the marker is missing; `TestPublicCopy_StopsAtMarker` (archtest) asserts the marker, `## Appendix P` and every P.4 pattern string (the demo Muse titles and artists) are absent from the copy and that `make gate` fails on a copy that contains one; append decisions D-L1-1…39 to the private design document; update
`docs/plans/2026-10-12-roadmap.md` (insert L1/L2/L3 between M3 and M4a, record that M4a/M5/M6 renumber after the L-plans) and the M4a draft header
(the renumber sentence of "Contract numbering"); update the earlier plan documents named in each task's **Files**; record `metrics/` rows; run the final
review (spec, quality, security: the Muse-visibility tests are the security focus).
Done: `make check`, `make test`, `make e2e`, `make gate` green on the merged tree.

### Task L1-X2 (Deploy and production-copy backfill; operator-assisted; host details only in private notes)

1. Copy the production database **read-only** snapshot to a scratch directory (never the live file); also build a schema-6 copy by restoring the oldest pre-M3 backup if one exists (0007 and 0008 apply together); run the L1 binary's migration and `metamusic library
   reconcile --dry-run` then `library reconcile` and `library verify`; compare the counts with Appendix P.2; time the first `Settle` and one
   `get_muse_board(detail=chips)` call (budget: < 1 s, C101.4).
2. *(Only when the tempo lane L1-E1 merged, D-L1-39.)* Run `metamusic analyze` on the scratch copy for the reference recordings (tempo) and spot-check three measured tempi with the operator (Appendix P.3).
3. Deploy the binary (the schema migration is the first boot step; the listener opens after the startup `Settle`); confirm `healthz` reports schema 8 and
   `library_model.pending = 0`.
4. Operator action D-L1-28: create the demo Muses and assign their Songs (an agent may do it through `create_muse` + `assign_muse_songs`).
5. **Coverage gate (measured on the real copy, not an automated test):** ≥ 85 % of the real draft Songs have ≥ 1 `genre_family` row and ≥ 95 % have ≥ 1 term-derived facet; the **top-50 unmapped terms** report is saved as an artifact in the private notes. Below the gate ⇒ ship a `seed/2` (a seed revision, never a silent edit) before deploy.
Done: production reports the expected counts; `library verify` is clean; rollback = restore the pre-migration backup (documented in the runbook).

### Task L1-X3 (Operator review)

Review the Board on the real data (phone and desktop), the top unmapped terms (map the ones worth mapping through `map_sound_term`), the first Pick events after real
rating/duelling, and the `Unassigned` row. Record outcomes and any taxonomy additions in the private notes; open L2 inputs (Record naming, Crate defaults).

---

## Definition of done (L1)

- All named tests above green; `make check` < 30 s warm; `make test` green with media tools; `make e2e` green (chrome + firefox) with zero unserved ui ops and
  `/__e2e/leaks` empty; `make gate`, `make routing` green; coverage thresholds (C101.3) met on merged data.
- Parity: every L1 op with an MCP face (16) has a parity case (REST = MCP = stdio after normalisation) and the two excluded ops (`pin_pick`, `dismiss_pick_proposal`) have REST-only tests; the `tools/list` golden is re-baselined under the new ceiling
  (C96); `TestAcceptance_M2a_AgentPerformsEveryUIAction` covers the 14 new UI+MCP operations; `list_songs` and `propose_pick` (no UI route) are covered by their parity cases and `TestAcceptance_L1_EndToEnd_Agent`.
- Invariants: `library verify` clean on every fixture and on the production copy; `TestProperty_ProvenanceInvariants` (60 per CI run, 300 weekly) green; no derived
  row has a second writer (`TestSQL_WriteOwners_L1`); provenance tables follow the no-delete/frozen rules.
- Concurrency: `TestSettle_TwoSettlers` and `TestReconciler_DraftBeforeTrackAcrossBatches` green; a CLI run while a server holds the lease only enqueues (D-L1-35).
- The tempo lane (L1-E1) is not on this list: L1 is complete with Sounds degraded to "requested only" if it is cut (D-L1-39).
- Privacy: the Muse-visibility tests of C99 green; no real titles, artists, inspirations, lyrics, hostnames, balances or personal data in any tracked file; the
  public plan copy contains nothing below the private-appendix marker.
- Money: no L1 op spends; the Board's render path ends at the existing estimate/confirm page.
- Performance targets of C101.4 met on the CI runner and (Board, backfill) on the production copy.
- Every concept §7 L1 acceptance item maps to a green named test or a signed-off X2/X3 item; release tag `v0.4.0-l1` recorded.

## Risks / open questions

- **Eventual consistency is visible.** A take ingested a moment ago may show no Song for ≤ 1 s and a stale Pick for ≤ one pass. Mitigations: `pending` flags, the Song
  page's bounded refetch, `Settle` in tests/CLI/startup. If real use shows the lag is noticeable, `Run` can additionally be woken in-process after commits of
  ratings/verdicts (a callback seam, not a design change).
- **Ladder-driven Picks and global-fit drift.** `u` values are on a global scale shared by all Songs; adding duels elsewhere shifts margins slightly. Revision 2 builds the
  hysteresis now (D-L1-38: an incumbent ladder Pick is kept while `z ≥ 0.25` and displaced only at `z ≥ 0.5`), because L2 Records will log every Pick event and a flip-flop
  would appear in their changelogs. The constants are provisional and tuned after the first real duels; every event's evidence says whether the incumbent held.
- **Tempo estimation quality is unproven on real music.** The estimator is validated on synthetic signals only; octave errors are handled by tolerance, not eliminated; lossy
  reference MP3s may be noisier. L1-X2/X3 spot-check real tracks; if the estimator is poor, the badge degrades to "requested" (the rest of Sounds is unaffected) and
  the analyzer is revisited with Tier B (M5) rather than blocking L1. It is a separable lane (D-L1-39): no acceptance item depends on it.
- **Taxonomy coverage.** Style prompts are descriptive; the seed will miss phrases. The unmapped report plus `map_sound_term` is the loop; the first review is L1-X3.
  Agents may extend the taxonomy (write scope): a bad mapping is reversible (`remove`) and versioned, but it changes browse counts until reverted; L3 adds
  proposals with human acceptance.
- **Song retire/attach corner cases** (draft gains a mother, draft fulfilled by a non-root take, two drafts one root) are covered by the table and the property test,
  but the importer's real `varianti/` arrival (drafts that gain mothers after their Songs exist) is the first real exercise; `Verify` after the import is part of the
  runbook.
- **Muse text reaches agents by decision.** The Board payload, `get_muse`, `get_song` and `/context` carry Muse titles/artists/briefs to whichever LLM the operator's
  agent uses. The decision stands (concept §0); the structural guards stop *accidental* spread (exports, logs, Subsonic). If the operator later wants an "agent-blind"
  Muse, it is a per-Muse flag filtered in `muses` reads — not built.
- **L2 conflict to resolve:** "Subsonic album mapping (Records + per-Muse auto-albums)" would expose Muse titles to Subsonic clients, contradicting §0. L2 must map auto-albums
  from Sessions or Song titles, or make Muse-named albums an explicit opt-in. L1's import-graph rule makes the conflict impossible to ship by accident.
- **`final` status is not a Pick input** (the concept's priority list omits it). A human who marks a take `final` expects it to be the Pick; today that needs a pin. A
  "mark final ⇒ offer to pin" affordance is the cheapest fix (D-L1-31 lists it as not built); revisit after the first real use.
- **`tools/list` growth** (+7 tools) is bounded by the ceiling and the demotion order (C96), which ends with "stop and ask the operator".
- **Board page cap.** One 90 000-byte cap for every caller (revision 2 dropped the earlier 256 KiB `detail=chips` exception: ≈ 65 k tokens through MCP); the SPA follows `next_cursor`, so the real library's Board takes a few requests; the `Unassigned` row is capped at 200 chips with its own cursor (C97).
- **Derived-table deletes** (caches) widen the `TestNoDeleteSQL` allowlist by five names; the write-owner test confines them to their packages.
- **Mid-flight migration size.** The 0008 queue seed writes one row per track/draft; on a library with 10⁵ rows the first `Settle` takes minutes (batches of 100 keys); the startup
  budget is 60 s for the real library (≈ 500 keys) — larger libraries should run `library reconcile` before the first serve (documented in the runbook).
- **Scope valve.** If time is short, cut in this order (D-L1-39): the weekly 300-sequence CI job, the Sessions rename/hide panel (the ops stay), `include_hidden` on the Songs/Sessions/Muses lists, the `engine` facet. The tempo lane is separable by design.
- **Draft `reference` becomes agent-visible (D-L1-36).** This reverses the M1/M2a humans-only rule and needs the P.5 box; the alternative (hiding backfilled Muse text from agents) contradicts D-L1-6. `TestDraftReference_AgentView` pins whichever is chosen.
- **Two processes, one writer.** The lease (D-L1-35) makes a CLI run next to a live server safe by making it enqueue-only; a crashed settler blocks others for at most `SettleLeaseTTL` (30 s).

