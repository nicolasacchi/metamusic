# L2 — Library: Curation — Implementation Plan (DRAFT, revision 3: re-baselined on main b6b221a)

> **Revision 3 (2026-10-27).** Written against `main` at `b6b221a` (L1, M3, M4a, the UI redesign PRs #32–#42 and the dev-loop
> infrastructure are all merged). Revision 2 was written against branch `m3` HEAD `f77ed3d` and the L1 *draft*; the unchanged draft is
> kept beside this file as `l2-library-curation.draft.md` for diffing. Every claim in "Baseline reconciliation" was checked in the
> worktree `metamusic-wt/l2` (code, migrations, `docs/plans/`, `web/src/`); nothing was checked against a running system.

## Revision 3 changelog (every material change)

1. **Numbering.** Migration `0008_curation.sql` → **`0010_curation.sql`** (schema / `PRAGMA user_version` **10**; `0008_provenance.sql` is L1's,
   `0009_remote.sql` is M4a's). Contracts **C102–C116 → C118–C132** (+16; M4a implemented C102–C117, so C117 is the highest in use). L3 will start at
   **C133** and migration **0011** (its draft's C117–C130 / 0009 is stale and renumbers in its own X1). Decisions stay `D-L2-n`; test names that embedded
   `0008` now embed `0010`. Op count arithmetic: the registry holds **109** operations on main (not 99): 109 + 17 = **126**.
2. **Amended-contract references re-verified** (heading and content checked): L1 C87/C89/C90/C96/C98/C99/C100/C101, M0 C7, M1 C16.7/C18/C19, M2a
   C27/C33/C35/C38/C40/C41/C42, M2b C51, M3 C81/C84/C85/C86 all exist with the content the draft assumes. The draft's "M4a C82/C83/C84.1/C84.2" used
   M4a's *old* numbers; they are **C108/C109/C110.1/C110.2** today.
3. **Baseline reconciliation rewritten** against main (about 60 rows, each *exists as assumed / exists differently / missing*). The consequential
   findings and their resolutions:
   - **M4a shipped the cover package** (`internal/media/cover`: `Render`, `Key`, `ClampSize`, integer palette, splitmix64, `Service` with
     `EnsureTrack`/`EnsureAlbum`/`Resolve`/`Sweep`, `png` already served). C107 becomes an *extension*: a mosaic renderer in new files, `EnsureMosaic`
     on the existing `Service`, constants renamed (`MosaicVersion`; `Version = "mm-cover/1"` is taken) (C123, D-L2-9, D-L2-39).
   - **M4a shipped the FLAC metadata writer inside `internal/media/tagged`** (unexported `vorbisBlock`, `pictureBlock`, `Write`). L2 *extracts* those into
     `flacmeta` (exported) and adds only the CUESHEET block and the rewrite-with-new-metadata function; `tagged`'s goldens keep pinning the bytes
     (C128.3, D-L2-40). Draft D-L2-32's "tagged reuses L2's writers" is reversed.
   - **M4a shipped the Subsonic server with `album = lineage` (`al-<root id>`)**, SQL-direct over `tracks`. Draft D-L2-34 ("L2 builds membership only, M4a
     builds the consumer") no longer makes sense. New lane **S1** adds Record albums (`rec-<id>`) to the shipped server *additively*; lineage albums and
     song `albumId`s do not change (D-L2-35). Muse auto-albums / `unfiled` / neutral names become the separable lanes **A2/S2** (scope valve).
   - **`has_pick` already exists with a different meaning** (`song_picks.take_id IS NOT NULL`, any source); the Board's `picked` is `source IN
     ('pinned','ladder')`. The draft's "L1 leaves has_pick undefined" is wrong. L2 does **not** change `has_pick`; it adds **`pick_decided`** (D-L2-36).
   - `muses.Service.Refs` (batch) **already exists**; `muses` has **no ordinal column**. Neutral-name stability is moved from "rank by `(created_at, id)`,
     verified by a test" to a frozen **`muses.ordinal`** column added by 0010 (backfilled by rank, assigned by an `AFTER INSERT` trigger) (D-L2-37).
   - `audit_log.id` is a UUIDv7 `TEXT`, not an integer (the change-token probe uses `MAX(id)`, which is still monotonic); the table is
     `idempotency` (package `idem`); the backup never walks `derived/` (it copies only DB-referenced files), so "excluded from backups" needs **no code**;
     `derived/covers` and `derived/dl` are already in the orphan skip list, `derived/tapes` must be added.
   - The Library has **no multi-select** today (only the Board does): the draft's "Library selection bar" is cut; entry points are per-item menus plus the
     Board selection (D-L2-41).
   - The single-`<audio>` rule (`singleAudio.test.ts`) and a track-id-only persisted queue shape "Play as tape" and the gain map (D-L2-42).
4. **Web contract rewritten (C131)** for the shipped shell: Records and Crates are **Library sub-views** (next to Takes · Sounds · Playlists), the two secure
   pages are in-app `SecureGate` routes under More → Secure, playback goes through `playerStore`/`player`/queue, scroll restoration via `READY_ROUTES` +
   `routeReady()`, 44 px targets, dark only, tokens and existing icons only (D-L2-38).
5. **Tooling/CI contract rewritten (C132)** for the shipped dev loop: agents iterate with `make verify` + focused package tests + only the affected
   Playwright specs (`scripts/e2e-map.txt`, `// matrix:` headers, per-worker e2e servers); whole-repo `make test`/`make e2e` run in GitHub CI (6 Go shards,
   perf job, 6 e2e shards, coverage job). `go-fast`/`go-media` tiers no longer exist.
6. **Lanes re-cut** into parallel worktree lanes with explicit file ownership, hot-spot rules and PR-size estimates (section "Lanes and tasks"): foundation
   **F1 → F2 → F3** first (migration + contract freeze + web scaffold), then **R-a, R-b, V1, V2, K, A, T1, T2, I1, W1–W3** in parallel, then **S1** (and the
   optional **A2/S2**), then **INT-B / INT-W**, X1–X3. Draft B1…B8/W1…W4 test lists are preserved inside the new lanes.
7. **Other contract corrections:** `FixtureL2(tb, st, now)` follows the real fixture signature and moves to F1 (SQL-only); `CoverRef` is replaced by the
   existing `api.CoverLinks`; the batch readers list drops `muses.Refs`; the `tools/list` ceiling is currently 104 800 bytes with 104 699 measured (39 tools),
   so five new tools force a re-baseline and the demotion order of D-L2-21 matters; the archtest guards go into **new** `*_l2_test.go` files (the L1/M4a files
   are not edited); the weekly property job follows the existing `provenance-property` pattern; "secure pages are full-page loads" is gone (they are in-app).
8. **Appendix P** is kept; its production facts carry a dated "stale since main b6b221a" note instead of new numbers (no production access).

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan lane-by-lane (TDD per task: write the named tests first, watch them fail, implement, review between tasks). Read `CLAUDE.md` (the worktree's
> copy is authoritative for commands: `make verify`, `scripts/go-test`, `scripts/pw`), the **binding concept** `docs/private/design-src/library-concept.md`
> (operator-approved vocabulary and decisions of 2026-10-14: **Muse · Song · Take · Pick · Session · Sound · Record · Crate**; do not rename a noun or
> change a decision), `docs/plans/2026-10-12-m0-skeleton.md` (C1–C9 incl. addenda), the M1 plan (C10–C24), the M2a plan (C25–C42), the M2b plan (C43–C56),
> the M2c plan (C57–C67), the M3 plan (C68–C86), the **L1 plan `docs/plans/2026-10-15-l1-library-provenance.md`** (C87–C101, D-L1-1…39; binding: L2 builds on
> its tables, ids, Pick event stream, filters and privacy rules exactly) and the **M4a plan `docs/plans/2026-10-15-m4a-remote-agents-mobile.md`** (C102–C117) first.
> L2 continues the numbering after M4a: **L2 contracts are C118–C132**, decisions **D-L2-1…**, migration **`0010_curation.sql`** (schema version 10).
> Where L2 changes an earlier contract, the lane that makes the change records it in that plan document in the same commit (named in each lane's **Files**;
> the plan documents under `docs/plans/` are edited by X1 only, so lanes list the addendum text in their PR description and X1 applies it).
> L3 (Agents & map) is written later and is bound by this one.
> Amended contracts (all verified to exist on main with this content):
> - **L1:** C87 (import-graph rules gain `recordview`, `tape`, `albums`, `liner`, `cover`, `crates`, `records`, `curation`; `MuseDirectory` methods
>   `Ordinals`/`Version`/`Labels` on `muses.Service`; `Refs` already exists), C90 (`songs.ListFilter` gains the L2 predicates; **`has_pick` is unchanged,
>   `pick_decided` is new**, D-L2-36; `SongView` gains `records`/`crates`), C96 (`list_songs` and `search_sounds` gain the same query parameters; `get_muse`
>   gains `catalog`), C98 (`/context` gains `curation`, `get_stats` gains `curation`, `search_tracks` gains `unheard`, `in_record`, `sound`, `crate_id`),
>   C99 (the Muse-text allowlist gains exactly one op, `get_record`), C100 (web: superseded for the shell by the redesign; L2's pages follow C131), C101
>   (`servertest.Options.L2`, rigs, fixtures).
> - **M0:** C7 (CLI `library verify` also runs the L2 invariants; `library reconcile` also settles the Record syncer); `mediatool.RunOpts` gains `Stdin io.Reader`.
> - **M1:** C16.7 (`library_orphans` skips `derived/tapes/` — `derived/covers/` and `derived/dl/` are already skipped), C18 (the tape resolver joins the chain;
>   `png` and the nil-safe variadic `serve.Chain` already exist), C19 (no change).
> - **M2a:** C27 (`TestNoDeleteSQL` allowlist unchanged), C33 (playlists unchanged; the Record is a playlist's richer sibling), C35/C38 (`/context`, `get_stats`),
>   C40 (web routes), C41 (routing contract, coverage, `tools/list` ceiling), C42 (`servertest`, parity rows, fixtures).
> - **M2b:** C51 (Ears: read-only use of the stored `loudness` analysis and its 1 Hz short-term series; no analyzer change).
> - **M3:** C81 (op count 109 → 126), C84 (web: entry points), C85 (CI, `tools/list` ceiling re-measured), C86 (fixtures).
> - **M4a:** C108/C109 (OpenSubsonic layer and domain mapping: Record albums are added, lineage albums unchanged, D-L2-35), C110.1 (the cover package gains the mosaic
>   renderer), C110.2 (`tagged`'s block writers move into `flacmeta`), C113 (op list), C115 (web), C116 (tooling), C117 (fixtures/harness).
>
> **Binding lessons carried from M2b/M2c/M3/L1/M4a and the dev-loop work:** never mutate process globals (`slog.SetDefault` etc.) in `run()` or handlers; e2e specs
> must not depend on counters other specs change (each worker has its own server, but a spec file shares it); startup waits in tests tolerate instrumented
> (`-cover`) runs (generous deadline, readiness poll, never a fixed sleep); new tests are safe under `t.Parallel()` and `-shuffle` (no shared buffers, loggers or
> globals); a derived-state writer has exactly one writer where a lease is needed (D-M3-3, D-L1-4) and an idempotent, uniquely-keyed writer where several may run
> (D-L2-7); a perf test never hard-codes a wall-clock literal (`MM_PERF_FACTOR`) and is named `TestPerf_*` (it runs in the CI perf job, not the race shards); a spec
> file starts with a `// matrix:` header; Muse text never leaves the app except by an explicit operator opt-in (D-L1-6, D-L2-16); the privacy gate is public-repo
> grade (no hostnames, personal names, lyrics or balances in any tracked file).

**Goal:** give the operator the *Curation* axis of the library (concept §1, §7 L2): a **Record** that is a
living, ordered, finished listening sequence (slots point at Songs and play the Song's current Pick unless
pinned; every Pick-driven upgrade is logged; cover art and liner notes are generated from provenance; only a
human publishes), **Crates** for working sets and saved filters (**smart Crates**), the **album mapping**
that lets Subsonic clients see published Records (and, in the optional A2/S2 lanes, automatic per-Muse albums) **without ever seeing a Muse
name unless the operator opts in**, and a **lossless tape** (one 24-bit FLAC with chapters, loudness-matched,
gapless, labelled when it contains lossy sources). It is fully drivable by humans (web) and agents (REST + MCP)
with strict parity, except publishing and the privacy opt-ins, which are human-only. **Nothing in L2 spends
money.**

**Architecture:** one new migration (`0010_curation.sql`: `records`, `record_slots`, `record_changes`, `crates`, `crate_items`, `tapes`, the `muses.ordinal`
column, plus triggers) and seven new small Go packages with a one-way dependency order: `internal/curation` (the two privacy opt-in settings), `internal/records`
(Record model, slot edits, slot resolution, the append-only changelog, the idempotent **Syncer** that turns L1 `pick_events` into changelog rows, `Verify`),
`internal/liner` (pure liner-note rendering), `internal/crates` (Crate model, the smart-filter vocabulary, validation, expansion), `internal/tape` (loudness plan,
pipeline, runner, cache, resolver), `internal/albums` (the catalog the Subsonic server consumes: Records first, auto-albums with neutral naming in the optional A2
lane, change token) and `internal/recordview` (the read assembly that needs Muses, Sessions, Sounds, loudness and tapes together — the only L2 package allowed to
read Muse text, as `songview` is in L1). The **existing** `internal/media/cover` package (M4a) gains a deterministic mosaic renderer for Records, and the existing
`internal/media/flacmeta` gains the exported block writers (the VORBIS_COMMENT and PICTURE builders are extracted from `internal/media/tagged`; CUESHEET and
rewrite-with-new-metadata are new). `mediatool.RunOpts` gains `Stdin`, so the tape is `ffmpeg` decode → Go gain stage → reference `flac` encoder → Go metadata
rewrite → verification. Records never store a Take for a floating slot: the take is derived from the L1 Pick at read time, so playback is never behind; the
changelog is written by the Syncer within about one second. The shipped Subsonic server (`internal/subsonic`) learns Record albums in lane S1 (additive,
D-L2-35). 17 new registry operations (5 MCP tools, 10 generic, 2 human-only; registry 109 → 126) feed REST, MCP and seven new pages, all **in-app SPA routes**
of the shipped shell: Library sub-views `/records`, `/records/:id`, `/crates`, `/crates/:id`, the More-page `/settings/curation`, and the two secure pages
`/human/records/:id/publish` and `/human/curation` behind `SecureGate` (C131).

**Tech stack:** Go 1.27 (stdlib first; no new dependency), Huma v2, modernc SQLite, the existing external tools
(`ffmpeg` for decode, the reference `flac` encoder for the tape — both already pinned by `make tools`), Svelte 5
runes + TypeScript strict + Bun/Vite, Playwright. No new Go module, npm package, binary or config variable.

---

## Why this model (design thinking, public-safe)

**Where L1 stopped, and the one question L2 answers.** L1 made the library self-organising: a Take belongs to a
Song, a Song has an inspiration (Muse), a best Take (Pick) and Sound facets, and none of it needs filing. What it
cannot answer is *"what do I do with all this?"* — hear a finished sequence, keep a short list of things to judge,
hand something to a phone, give a friend a file. L2 gives each of those a home, and the interesting design work is
that **each home has a different relationship to time**:

| Home | Time behaviour | Who decides membership | What it is for |
|---|---|---|---|
| **Crate** (manual) | static until edited | human or agent, any time | "to judge", "remake candidates": a pile with a purpose |
| **Smart Crate** | recomputed on every read | a saved filter (human or agent writes it) | "reggae · unheard · ≥ 4 min · no Pick yet": a question, not an answer |
| **Record** (draft/proposed) | living: floating slots follow Picks | human, or an agent that proposes | shaping a sequence while the work is still moving |
| **Record** (published) | living, but human-owned: only a human edits, every change is logged | human | the sequence a phone, a Subsonic client or a friend should see |
| **Tape** | **frozen**: one file, input-addressed | export of a published Record | the artifact you can copy, play gaplessly, archive |
| **Playlist** (M2a) | static, simple, unchanged | human or agent | quick ordered lists with no judgement semantics |

The album is therefore not "surpassed" so much as **split along that axis**: the *living* part is the Record (it
keeps meaning "the best version of this sequence right now"), the *frozen* part is the Tape (it keeps meaning "this
exact audio"). Collapsing them is the mistake the old album made: it demanded a finished set before there was one.

**Categories and lists, applied — one decision rule.** The operator asked for categories and lists "now and for the
future". The model answers with a rule instead of more folders: *if the grouping can be computed, it is a filter
(Sound facet, Muse, Session, smart Crate); if it needs a human decision about membership, it is a list (Crate,
Record, Playlist, Tag); if it needs a human decision about order and release, it is a Record.* That keeps every
future "category" request one of three moves — add a facet value (taxonomy op), save a filter (smart Crate), or
curate a list — and never a new table. Concretely:

| Need | Mechanism (L1 existing → L2 new) |
|---|---|
| "what kind of music is this?" | Sound facets (L1) — computed |
| "what inspired it / when did we work on it?" | Muse, Session (L1) — computed or annotated |
| "things to judge next" | Crate (manual, subject Take) — L2 |
| "remake candidates" | Crate (manual, subject Song) or smart Crate — L2 |
| "everything reggae I have not heard that is long enough" | smart Crate (saved L1/L2 filter) — L2 |
| "a sequence I want to hear in order" | Record — L2 |
| "ship it to my phone / Subsonic / a friend" | publish the Record (human), tape for a file — L2 |
| "my own labels" | Tags (M2a) — exists |
| "a quick ordered list" | Playlist (M2a) — exists; "promote playlist to Record" is deferred (D-L2-28), the slot model already represents it (every playlist item becomes a *pinned* slot) |

**Why a Record's slots point at Songs, not Takes — and what that costs.** A slot that pointed at a Take would go
stale the first time a better render arrived, which is exactly the event this system produces constantly. Pointing
at the Song makes the Record follow the Pick; the price is that a published Record can change under a listener.
L2 pays that price on purpose and bounds it three ways: (1) every change is an append-only, explained changelog
row ("slot 3 upgraded: take A → take B (won 4 blind duels)"); (2) a slot can be **pinned** and a pinned slot never
changes except by a human act; (3) a floating slot whose Pick is *undecided* (`newest` or `stars`: no human judged it) is flagged, counted and listed on the publish page, and `pin_all` freezes a whole Record in one step when "release" should mean "stop
moving" (D-L2-30). The Tape is the unconditional freeze.

**What the publish gate is, and what it is not.** Everything that leaves the app — Subsonic albums and the tape — is derived only from
**published** Records, and a published Record can be edited only by a human principal. So an agent can compose, sequence and propose
freely, but nothing it types **into a Record** (title, subtitle, liner note, rationale, slot set and order) can reach a Subsonic client
or an exported file without a human having looked at the Record and pressed publish. That is a structural guarantee and it is tested.
It is **not** a guarantee about catalog text an agent can already edit through the existing ops: Take titles and status
(`update_track`), Song titles (`update_song`) and Muse names and grouping (`update_muse`, `assign_muse_songs`) are read live by the tape
(chapter names, export liner) and by the catalog (auto-album grouping). L2 handles those by **review-by-display** (D-L2-27): the publish page
shows exactly what a tape would carry, the tape key includes those strings, and Muse *text* never travels either way by default. The same
gate is why the opt-ins for Muse names (D-L2-16) can be a pair of plain booleans.

**Why the album mapping needs neutral names.** The concept maps Records *and* per-Muse auto-albums to Subsonic, while
the operator's standing decision (concept §0) is that Muses are never sent to Subsonic clients. L1 left that conflict
open on purpose (L1 Risks). L2 resolves it without choosing one side: auto-albums are built from the Muse *grouping*
(ids and an ordinal) and named from a neutral family (`Inspiration 007`); only an explicit human opt-in swaps in the
Muse's own title. The package that builds the catalog is **structurally unable** to read Muse text (it receives a
directory interface that the composition root only wires to real names when the opt-in is on), so a future bug cannot
leak a name by accident. The operator's real way to give an album a real name is to make a Record of it.

---

## Dependencies and sequencing

- **Hard:** M0–M3, **L1** and **M4a** as merged on `main` (`b6b221a`), plus the UI redesign (tab shell, `playerStore`, in-app secure pages) and the dev-loop
  infrastructure. Branch every lane from `main` (see "Lanes and tasks" for the order). Schema version after L2: **10** (`0010_curation.sql`; L1's
  `0008_provenance.sql` and M4a's `0009_remote.sql` are the previous two). A database at schema 5–9 migrates the missing steps and 0010 in one boot; 0010 creates no
  row except the `muses.ordinal` backfill of existing Muses (a pure rank, no seed).
- **What L2 uses (binding; each item verified in the baseline table):**
  - **L1:** `songs` (live/hidden/retired, `retired_into`), `song_roots` and the `take_songs` view (a Take's Song), `song_picks` (the Pick and its `source`),
    `pick_events` (per-Song `seq`, the integer `cursor` = rowid, `from_*`, `to_*`, `reason`, `evidence_json` `mm.pick/1`), `picks.Service.Events` (`song_ids ≤ 200`),
    `songs.ListFilter`/`songs.FilterSQL` and `store.SongStateSQL`, `muses` (only through `muses.Service`; `Ref`/`Refs` exist), `sessions`/`session_members`,
    `song_sounds`/`take_sounds`/`sounds.TempoDetail`, `songview`, `api.RefFor`, `board` chips, `provenance_dirty` (read only, for `pending`), `store.Env`,
    `store.ParseInstant`/`FormatTime`, `textfold.Norm`, `Principal.ActsAsHuman()`/`Key()`.
  - **M1/M2a:** `tracks` (`master_path`, `master_sha256`, `master_bytes`, `sample_rate`, `bits_per_sample`, `channels`, `total_samples`, `lossy`, `status`, `duration_s`,
    and M4a's `status_set_at`), `analyses` (`loudness`), `renditions`, `ratings`, `listen_events` (human/device `start`: the definition of "heard"), `track_tags`,
    `playlists` (unchanged; M4a added `role`, which L2 never touches), `settings`, `audit_log`, `idempotency` (package `idem`), `gate`, `ratelimit` classes,
    `serve.Chain`/`Signer`/`Resolver`, `exports.Service` (pattern only), `library` read paths (`SearchParams`, `TrackRefs`).
  - **M2b/M2c:** `ears` (loudness read-only; `AnalyzeTrack` when a take lacks one), `clip` gain conventions (centi-dB integers, `OutRate = 48000`), `mediatool.Run`,
    `flacmeta.Read`/`FramesSHA256`, `flactest`, lossy masters (MP3, `lossy = 1`) and recipe-less reference takes — first-class everywhere (D-L1-23).
  - **M3:** `jobs.cost`/`library` cost fields (liner costs, app audience only), `preflight` disk-space pattern, `notify` unchanged.
  - **M4a:** `internal/media/cover` (`ClampSize`, palette, PRNG, `Service`, `png` serving), `internal/media/tagged` (the writers to extract), `internal/subsonic`
    (the server L2 extends), principals: connector principals are `KindAgent` with `Via oauth` (never `ActsAsHuman()`); Subsonic keys (`mms_`) are `KindDevice` principals with `Via subsonic` that authenticate only inside `/rest/`, so they never reach the L2 ops.
- **Operator inputs (tracked in X2/X3):** approval of the neutral naming default and the two opt-ins (D-L2-16), of the publish route (D-L2-4), of the publish-gate
  scope (D-L2-27), of the living-slot policy (D-L2-30), of the tape format and retention (D-L2-17, D-L2-19), of the Subsonic scope (D-L2-35); a listening check of one
  real tape in the operator's own players (chapters, loudness, seams); review of the first Records. No secrets, keys or spend.
- **Gate before F1 (P.5):** the boxes for D-L2-4, D-L2-15, D-L2-16, D-L2-17, D-L2-19, D-L2-27, D-L2-30 and D-L2-35 are ticked in the private notes (the last three
  change behaviour after review). The F1 brief repeats this; F1 does not start without it. (Revision 3 keeps A2/S2 behind their own P.5 box; F1 only needs the
  `muses.ordinal` column, which is harmless if A2 is never built.)
- **Timing:** nothing date-bound. L2 must merge before L3 starts; L3's X1 reads the real highest contract/migration numbers from `docs/plans/` at that time.

### Contract numbering and the later drafts

L1 ended at **C101** and migration **0008**; M4a took **C102–C117** and **0009**. L2 takes **C118–C132** and **0010**. L3 starts at **C133** and migration **0011** (its
own X1 renumbers; the L3 draft's C117–C130/0009 is stale). The M5/M6 drafts renumber in their X1 tasks after the highest in use. Decision ids `D-L2-1…43`. Test
files added by L2 are suffixed `_l2`.

---

## Baseline reconciliation (verified against `main` `b6b221a`, worktree `metamusic-wt/l2`; revision 2 was verified against `m3` `f77ed3d` and the L1 draft)

Status legend: **OK** = exists as the plan assumes; **DIFF** = exists differently (the contract is corrected); **MISSING** = does not exist (L2 builds it). Method:
`ls`/`grep` over `internal/`, `cmd/`, `web/`, `scripts/`, `.github/`; `internal/store/migrations/*.sql`; `openapi.json`; `docs/plans/*.md` headings. Nothing was run
against production, a container or the network.

### A. Numbering, plans and amended contracts

| Item | Revision 2 assumed | Main `b6b221a` | Status | Resolution |
|---|---|---|---|---|
| L1 contracts / migration | C87–C101, `0007_provenance.sql` | C87–C101 (`docs/plans/2026-10-15-l1-library-provenance.md`), `0008_provenance.sql` | DIFF (migration number) | L2 follows both |
| M4a contracts / migration | not built; C76–C99 / `0006` in the old draft | **built**: C102–C117, `0009_remote.sql` (connector grants, Subsonic clients/salts/bookmarks/play queue, `tracks.status_set_at`, `playlists.role`) | DIFF | L2 = **C118–C132**, **`0010_curation.sql`**; no column or table of 0008/0009 collides with L2's DDL |
| Highest contract in use | C101 | **C117** (M4a); the L3 draft claims C117–C130 | DIFF | L3 renumbers to C133+/0011 in its X1 |
| Amended: L1 C87 packages, C89 muses, C90 songs, C96 ops, C98 context/stats, C99 Muse visibility, C100 web, C101 tooling | as numbered | headings present with that content | OK | C100's shell parts are superseded by the redesign (see section C) |
| Amended: M0 C7 CLI; M1 C16.7 (`library_orphans`), C18 (media), C19 (ops); M2a C27, C33, C35, C38, C40, C41, C42; M2b C51; M3 C81, C84, C85, C86 | as numbered | all present | OK | none |
| Amended: "M4a C82/C83/C84.1/C84.2" | M4a draft numbers (album endpoints, cover, tagged) | M4a is built: **C108** (OpenSubsonic layer), **C109** (domain mapping and endpoints), **C110.1** (cover), **C110.2** (tagged downloads), C113 ops, C115 web, C116 tooling, C117 harness | DIFF | all references renumbered; D-L2-32 rewritten |
| Public plan copies | n/a | `docs/plans/` has M0…M4a, L1, roadmap (no L2/L3) | OK | L2's copy is made by X1 with `scripts/l1-public-copy.sh` |

### B. Schema and data

| Item | Revision 2 assumed | Main | Status | Resolution |
|---|---|---|---|---|
| Table/index/trigger names `records`, `record_slots`, `record_changes`, `crates`, `crate_items`, `tapes` and every index/trigger of C119 | free | no hit in `0001`–`0009` (grep for `records`, `crates`, `tapes`, `record_` excluding `recorded_at`) | OK | C119 DDL stands |
| `tracks` columns (`master_path/sha256/bytes`, rate, bits, channels, `total_samples`, `lossy`, `status`, `duration_s`) | as listed | all present (`0002`, `lossy` in `0005`); `status_set_at` added by `0009` | OK | `status_set_at` could also feed the change token; the audit probe is kept (it also sees titles) |
| `analyses` loudness (`integrated_lufs`, `true_peak_dbtp`, `short_term_1hz`, `replaygain_track_gain_db`) | as listed | PK `(track_id, analyzer, version)`; metrics read through `library.LoudnessOut` / `ears` | OK | batch reader `ears.Loudness` is new (V2) |
| `audit_log` | `audit_log.id` an integer; `audit_log_op(op, id)` index | table `audit_log`, **`id` is UUIDv7 TEXT**, index `audit_log_op(op, id)`, `status` integer | DIFF | token probe is `MAX(id)` (lexicographic = time order for UUIDv7); only the hash is used (C126.5) |
| `idem` | table "idem" | package `internal/idem`, table **`idempotency`** | DIFF (name) | tests say "the `idempotency` row" |
| `songs`, `song_roots`, `take_songs`, `song_picks`, `pick_events` (per-Song `seq`, rowid cursor, UUID `id`, `mm.pick/1` evidence) | as C119 reads them | present exactly (`0008_provenance.sql`); `song_picks.source` ∈ none/pinned/ladder/stars/newest; `song_picks` is `WITHOUT ROWID` | OK | `pick_events.reason` has no `retired`/`duplicate` (those are `record_changes` reasons only) |
| `muses` | no ordinal; neutral rank by `(created_at, id)` verified in a test | no `ordinal`; one backfill pass shares one `now` and orders by generated UUIDv7; `TestSQL_MuseTables_Allowlist` confines `muses` SQL to `internal/muses`, `internal/board`, migrations | DIFF | **frozen `muses.ordinal`** in 0010 (D-L2-37); `Ordinals` lives in `internal/muses/directory.go` |
| `listen_events` (kind `start`, `principal_kind IN ('human','device','agent')`), `ratings`, `track_tags`, `playlists` | as listed | present | OK | none |
| `exports` | synchronous, fixed kinds, 0444 input-addressed under `derived/exports/` | present | OK | tapes stay a separate table |
| `settings` | free keys | `settings` (key/value) present; no `curation.*` keys | OK | none |
| `provenance_dirty` and the Reconciler | read-only use for `pending` | present (`0008`), `provenance.Service.Settle/Run/Verify` | OK | none |
| `PRAGMA user_version` | 7 → 8 | migrations run `0001`–`0009` (user_version 9) | DIFF | 10 after L2 |

### C. Packages, ops, wiring

| Item | Revision 2 assumed | Main | Status | Resolution |
|---|---|---|---|---|
| `internal/records`, `crates`, `liner`, `tape`, `albums`, `recordview`, `curation` | new | none exists | MISSING | built by L2 |
| **`internal/media/cover`** | "L2 creates it; M4a adds the per-take renderer later" | **exists (M4a C110.1)**: `Version = "mm-cover/1"`, `ClampSize`, `Key(rootID, seed, size)`, `Render(rootID, seed, size)` (floating-point rings and bars, 2×2 sampling), integer `hsl`/`makePalette`/`blend`/`nearest`/`pixelIndex`, `splitmix64`, `Service{data, lib *library.Service}` with `EnsureTrack`/`EnsureAlbum`/`Resolve`/`Sweep`, `derived/covers/`, `cover.golden.json`; swept daily by `sweepM4aCaches` | DIFF | C123 extends: mosaic in new files, `MosaicVersion`, `MosaicKey`, `RenderMosaic`, `Service.EnsureMosaic`; `render.go` is untouched; the integer-only rule applies to the new file only |
| `internal/media/flacmeta` | read-only | read-only (`Read`, `FramesSHA256`, `AudioID`, `Block`, `Info`, `BlockType` consts) | OK | writers added by T1 |
| **FLAC block writers** | none anywhere | **`internal/media/tagged/write.go`** has unexported `blockHeader`, `vorbisBlock`, `pictureBlock` and `Write` (copy STREAMINFO+SEEKTABLE verbatim, new VORBIS_COMMENT, PICTURE, frames byte for byte) | DIFF | T1 extracts the builders into `flacmeta`, adds CUESHEET and `Rewrite`; `tagged` calls them; `tagged`'s goldens must stay green (D-L2-40) |
| `mediatool.Run` / `RunOpts` | no stdin | `RunOpts{Timeout, Stdout, MaxStdout, MaxStderr}`; no `Stdin` | OK | T1 adds `Stdin` |
| `serve`: `png`, `Chain` | `png` and nil-safe `Chain(library, exports, clips, tapes, covers)` are Stage 1 work | handler regexp already `flac\|mp3\|jsonl\|zip\|jpg\|png`; `Chain(resolvers ...Resolver)` is variadic and skips nil; `app.go` builds `resolvers` and appends `m4aResolvers(covers, downloads)`; `cue` is **not** an extension | DIFF | F2 only appends the tape resolver in `app/l2.go`; `cue` is added by T2 only if the spike needs it |
| Orphans / backup | `derived/covers`, `derived/tapes` excluded from orphans and backups | `importer/report.go` `cacheDirs` already has `derived/covers` and `derived/dl`, not `derived/tapes`; `backup` copies only files referenced by `tracks`/`artifacts`/`renditions` and never walks `derived/` | DIFF | F2 adds `derived/tapes` to `cacheDirs` (one line + test); **no backup code**; `TestBackup_ExcludesTapesAndCovers` is a pure assertion |
| **Subsonic** | "M4a not built"; L2 would hand it `albums.Service` | **built**: `internal/subsonic` (catalog SQL over `tracks`, `albumAgg` grouped by `root_track_id`, ids `al-<root>`/`tr-<track>`/`ar-metamusic`, folders 1/2/3 by status, all list types, `getCoverArt` via `cover.Service`, `getIndexes` token = `lastModified`); `docs/subsonic.md` | DIFF | lane S1 (C126.7); lineage albums unchanged (D-L2-35) |
| `muses.Service.Refs` | L2 adds the batch | **exists** (`internal/muses/ref.go`: `Ref`, `Refs`, 400-id chunks) | DIFF | dropped from the batch-reader list; `Ordinals`/`Version`/`Labels` still new |
| Other batch readers `sessions.Refs`, `sounds.FamiliesOf`, `songs.Briefs`, `picks.Current`, `ears.Loudness` | new | none exists (`songs.Service.Core`, `library.TrackRefs` exist) | MISSING | V2 builds them; cover parts use the existing `library.TrackRefs` (carries `RootTrackID` and recipe `Seed`) |
| `textfold.Slug` | new | missing (`yue2.Slug` and `exports.cleanFilename` exist) | MISSING | F2 |
| Registry size | 99 | **109** operations (pinned in `ops_m3_test.go` and `ops_m2c_test.go`) | DIFF | 109 + 17 = **126** (F2) |
| Op names | none of the 17 exist | none of `list_records … set_curation_settings` is registered (checked against `openapi.json` and the registration files); no `list_albums` | OK | none |
| `tools/list` | ceiling set by L1-B2 | `toolsCeilingBytes = 104800`, measured 104 699, 39 tools | DIFF | five tools need a re-baseline; D-L2-21's demotion order applies (F2) |
| `api` registration | `api.go`, files per area | `New()` calls `registerXxx(a, d)` per file (`ops_*.go`, `*_l1.go`, `*_m4a.go`); `Deps` fields; `Deps.Covers *cover.Service` exists | OK | F2 adds four `registerXxxL2` calls and Deps fields; lanes own their `ops_*_l2.go` |
| App wiring | `app.Build` | `internal/app/{app.go, l1.go, m3.go, m4a.go, media.go}`: each milestone has its own `buildXxx` file; `serve` goroutines (`Provenance.Run`, `sweepM4aCaches`) live in `cmd/metamusic/cmd_serve.go` | OK | F2 adds `app/l2.go` (+ one lane-owned file per lane), a hook in `cmd_serve.go` and `cmd_library.go` |
| CLI `library verify` / `reconcile` | exist (L1) | `cmd/metamusic/cmd_library.go` | OK | R-b adds the L2 steps through F2's hook |
| `list_songs` / `search_sounds` filters | L1 leaves `has_pick` undefined | `SongFilterIn` has `has_pick` = **`song_picks.take_id IS NOT NULL`** (any source); `store.SongStateSQL` `picked` = `source IN ('pinned','ladder')`; no `unheard`, durations, stars, tags, `lossy`, `recipe_known`, `in_record`, `crate_id` | DIFF | `has_pick` unchanged; **`pick_decided`** added (D-L2-36); the other predicates are new for Songs |
| `search_tracks` filters | has `min_stars`, `unrated`, `tag`, `lossy`, … | present; also `song_id`, `is_pick`, `session_id`; **no** `sound`, `unheard`, `in_record`, `crate_id` | OK/MISSING | those four are new |
| `library` schema gating | `hasTable` | `hasTable(ctx, tx, name)` in `write.go` and `hasTableDB` in `tracks.go` | OK | same D-M3-35 pattern |
| `songview` / `trackctx` paths | `songview/view.go`, `trackctx/curation.go` | `songview/{get.go, library_sections.go, muse_summary.go, service.go}`; `trackctx/{trackctx.go (Bundle, Fit), library.go (LibraryReader/UseLibrary)}` | DIFF (file names) | V2 edits `get.go`/`library_sections.go`; F2 adds `trackctx/curation.go` |
| `get_stats` | `library/stats.go` | present (`LibraryModel`) | OK | A adds `curation` |
| Archtest | extend `TestImportGraph_L1` etc. | `import_graph_test.go` (M3, L1), `import_graph_m4a_test.go`, `sql_owners_test.go` (`TestSQL_WriteOwners_L1`, `TestSQL_MuseTables_Allowlist`), `public_copy_test.go`, `coverage_thresholds_test.go` (L1) | DIFF | F1/I1 add **new** `*_l2_test.go` files; existing files are not edited |
| Fixtures / rigs | `FixtureL2(tb, st, data)`; `servertest.Options.L1` | `FixtureL1(tb, st, now)`, `ApplyL1`, `FixtureL1Mirror/Upgrade/Scale`; `servertest.Options{L1, M4a, …}`, `Rig.Settle` (provenance) | DIFF (signature) | `FixtureL2(tb, st, now)` + `ApplyL2` (F1, SQL-only); rig options in F2 |
| `weekly.yml` | add `property-l2` | has `provenance-property` with `MM_PROPERTY_SEQS: "300"` | OK | add `curation-property` the same way (I1) |

### D. Web (the shipped shell)

| Item | Revision 2 assumed | Main | Status | Resolution |
|---|---|---|---|---|
| Shell | top nav + `APP_ROUTES` with Library at `/` | tab shell **New · Library · Play queue · Judge · More** (`lib/nav.ts`: `TABS`, `TAB_OF`, `NAV_LINKS`, `subLinks`/`subActive`, `isTabRoot`, `TITLES`, `CONTEXTUAL_ROUTES`); New is `/`, Library `/library`; Library sub-views Takes · Sounds · Playlists; Make (Board, Backlog, Jobs, Generate) under More; `components/shell/{TopBar,TabBar,SubNav}.svelte` | DIFF | C131 |
| Secure pages | server-side full-page loads | **in-app SPA routes** (`/human`, `/human/tokens`, …) wrapped in `SecureGate`; only `/human/api/*` is server-owned (`SERVER_PREFIXES`) | DIFF | both L2 secure pages are SPA routes under `SecureGate`; Meta `UI` strings unchanged |
| Playback | `player.svelte.ts` with queue and prefetch | `Player` class: queue is **track ids** persisted by `playerStore` (v1, `isTrackId`, `MAX_QUEUE 500`, source label+href); `PlayOptions{source, more, undo, …}`; focus leases (`acquireFocus`) for duels; no GainNode; `audioContext.ts` is used only by `ABPlayer` | DIFF | D-L2-42 |
| One `<audio>` | not considered | `singleAudio.test.ts`: exactly one `<audio` in `web/src`, in `App.svelte`'s footer | DIFF | "Play as tape" plays through that element (queue item `tape:<id>`), never a second element |
| Covers in the SPA | `CoverImage` new | `CoverArt.svelte` takes a **trackId** (via `get_media_links.cover`) | DIFF | new `RecordCover.svelte` takes `CoverLinks` |
| Scroll restoration | not considered | router-owned: `READY_ROUTES` + `routeReady()`, one saved position per history entry | OK/new duty | new list routes call `routeReady()` and join `READY_ROUTES` |
| Library selection | "Library rows gain a selection action bar" | Library has **no** multi-select; Board has selection with a phone `selectMode` action bar | DIFF | cut (D-L2-41) |
| Track/Song pages | add menus | `Track.svelte` has `AddToPlaylist`; `Song.svelte`/`Muse.svelte` exist with Judge buttons | OK | `AddToCrate` follows the `AddToPlaylist` pattern |
| Tokens, targets, icons | not considered | `styles/tokens.css` (`--hit: 44px`, `--hit-lg`, spacing/radius/z/colour tokens, dark only), `components/icons/*` (Grip, Up, Down, Play, QueueAdd, Close, Lock, …) | OK | reuse; no new colour literals |
| e2e | `l2-*.spec.ts` | per-worker servers (`fixtures.ts`, `server.ts`), `// matrix:` header required (`matrix.ts`, `e2e-matrix.test.ts`), `scripts/e2e-map.txt`, `served-check.ts` (every UI op served by some shard), helpers `smallTargets`/`spaGoto` | DIFF | C132 |

### E. Overlaps and conflicts: how each is resolved

1. **L2 vs M4a cover (C110.1):** extend, do not re-create (C123, D-L2-39). **L2 vs M4a `tagged` writers:** extract into `flacmeta` (C128.3, D-L2-40).
2. **L2 vs M4a Subsonic albums:** additive Record albums; `al-<root>` unchanged; Muse auto-albums are the optional A2/S2 (D-L2-35).
3. **L2 vs L1 `has_pick`:** new `pick_decided`; `has_pick` untouched (D-L2-36).
4. **L2 vs L1 Muse ordinals:** frozen column (D-L2-37); `muses` SQL stays inside `internal/muses`.
5. **L2 vs the redesign:** Library sub-views, in-app secure pages, `playerStore`/single-audio constraints, no Library multi-select (D-L2-38/41/42, C131).
6. **L2 vs the dev loop:** lanes iterate with `make verify`; hot-spot files have single owners (C132, "Lanes and tasks").

---

## Decisions recorded by this plan

These refine the design of record; X1 copies them into the private design document. Concept and L1 decisions are inputs,
not repeated.

| Id | Decision | Why |
|---|---|---|
| D-L2-1 | **Scope.** L2 = living Records (slots floating/pinned, Pick-driven upgrades with changelog, cover art, liner notes, propose/publish), Crates + smart Crates, the album catalog (**Record albums** and the change token first; per-Muse auto-albums, the "Unfiled" album and neutral names in the separable A2 lane), the Subsonic consumer for Record albums (lane S1, D-L2-35), the tape export, and the two privacy opt-ins. Nothing spends money. Branch from `main` (`b6b221a` at revision 3); migration `0010`. **Not in L2:** the Subsonic protocol server itself (M4a, built), changing lineage albums or song `albumId`s, `propose_record` *auto-sequencing*, Atlas, Muse-aware `next_actions` (L3). | Concept §7: L2 builds on L1's stable ids and Pick stream; the protocol layer is M4a's and already exists. |
| D-L2-2 | **Vocabulary.** Tables and API words are `records`, `record_slots`, `record_changes`, `crates`, `crate_items`, `tapes`; the UI says Record, slot, Crate, Tape. The word *album* appears only in `internal/albums` (the Subsonic catalog), in the FLAC `ALBUM` tag writer and in docs. `TestNoAlbumWord_L2` fails on any other non-test Go or web source. This **refines D-L1-1** (L1 reserved the word entirely). | Protocol vocabulary must exist somewhere; confining it keeps the product vocabulary honest. |
| D-L2-3 | **Record lifecycle.** `status` ∈ `draft` → `proposed` → `published`; legal moves: draft→proposed (`propose_record`), draft|proposed→published (`publish_record`), proposed|published→draft (`publish_record` with `published: false`: "send back" / "unpublish"). `published → proposed` does not exist. A Record is never deleted; it is hidden (`hidden_at`) like Muses and Songs. Publishing needs ≥ 1 playable slot (a slot whose resolved Take exists). `first_published_at` is set once and never cleared. `publish_record{published:false}` on a `draft` Record is a 200 no-op (no row, no revision bump). | Concept §2/§4: agents propose, a human publishes; reversible without deletion. |
| D-L2-4 | **Published means human-owned.** Every mutating op on a **published** Record (`update_record`, `edit_record_slots`) requires `Principal.ActsAsHuman()` (a human or a paired device); any other principal ⇒ 403 `HUMAN_ONLY` with hint "published records are edited by a human". `publish_record` itself is a **human-only op** on `/human/api/` (KindHuman, 2FA edge; devices refused): publishing exposes a Record to Subsonic clients and the tape, which is a different act from pinning a Pick (D-L1-11 deliberately kept pin on `/api/`). Agents may create, edit and propose **draft/proposed** Records. An edit of a **proposed** Record by a non-human principal sends it back to `draft` (row `proposal_dismissed`, `detail {"reason":"edited"}`, `proposed_*` and `proposal_note` cleared), so a human never publishes something other than what was proposed; a human's edit keeps the proposal. A paired phone can edit but not publish (device checklist DC-L2-3). | Concept §4 "publish human-only"; the publish gate is what makes every export path safe (D-L2-27). |
| D-L2-5 | **Slots.** A slot is an immutable identity row (`id`, `record_id`, `song_id`, `added_*`) plus mutable `side`, `pinned_take_id/by/at`, `synced_seq`, soft-removal `removed_*`. **Order is `records.order_json`** (the live slot ids in order; rewritten by `Service` edits and, for a duplicate-follow removal only, by the Syncer in the same transaction, C122.2.1); a trigger (`records_order_consistent`) rejects any update whose list is not exactly the live slots. A Song appears at most once per Record (409 `SONG_ALREADY_IN_RECORD`); max `MaxSlots = 60` live slots. `side` is an optional display/chapter label `A`–`D` with no ordering rule (concept: `side NULL`). Positions are 1-based and derived from the order. | No physical deletes and stable slot identity for the changelog; one ordering column makes drag-and-drop one write. |
| D-L2-6 | **Resolution.** A slot's current Take is derived, never stored: pinned ⇒ `pinned_take_id` (even when that Take was later discarded: the slot reports `pin_ineligible` and keeps playing it **in the app** — *a Record never silently changes a pinned slot*; such a slot is not listed in the catalog and blocks a tape until a human accepts it, D-L2-33); floating ⇒ the Song's current Pick (`song_picks.take_id`; none ⇒ `empty`). `ResolveSlot` is a pure function with a table test. Playback, `get_record`, tape snapshots and the catalog all read the **live** `song_picks`, so none of them waits for the Syncer. Retired Songs are followed (D-L2-8). Warnings (`pin_ineligible`, `empty`, `retired_song`, `undecided_pick`) are returned, never errors; `undecided_pick` = a floating slot whose Pick source is `newest` or `stars` (D-L2-30). | Concept §6 invariants; readers never lag; the changelog may (≤ one Syncer pass). |
| D-L2-7 | **Changelog and the Syncer.** `record_changes` is append-only with a per-Record `seq` chain. Human/agent ops write their own rows in their transaction; **Pick-driven rows** (`slot_upgraded`, `slot_filled`, `slot_emptied`, `slot_song_followed`) are written by `records.Syncer`, which consumes L1 `pick_events` per slot: each live floating slot holds `synced_seq` (the highest `pick_events.seq` of its Song already accounted for), initialised to the Song's current max `seq` when the slot is added or becomes floating again. Rows are keyed `UNIQUE(slot_id, pick_event_id)`, so any number of Syncers (server, CLI) are safe and idempotent — **no lease**. Every slot edit first **catches up** that Record's slots inside its own write transaction, so history is never reordered around an edit. A Pick event whose `to_take` equals `from_take` (source change only) advances `synced_seq` and writes nothing. Each row copies the event's `mm.pick/1` evidence, so the changelog is self-contained. **Transactions:** every L2 write is one transaction on the writer pool (`BEGIN IMMEDIATE` via `_txlock=immediate`, M0), with Pick state read inside it; a cross-process `SQLITE_BUSY` (serve vs CLI) waits `busy_timeout` and is then retried `SyncRetries = 5` times (20 ms × 2ⁿ). | Exactly the explanation the concept asks for, with no new coordination primitive and no lost or duplicated rows. |
| D-L2-8 | **Retired and duplicate Songs.** A takes-less draft Song that is retired into another (L1 D-L1-3) is **followed**: the Syncer re-points the slot to `retired_into` (a trigger allows a `song_id` change only to that target), writes `slot_song_followed`, and resets `synced_seq`; if the target Song is already live in the same Record the slot is soft-removed instead (`slot_removed`, reason `duplicate`) and `records.order_json` is rewritten in the same transaction (C122.2.1). Adding a retired Song is 409 `SONG_RETIRED`; hidden Songs may be slotted and stay playable. | L1 promised "L2 slots may still hold the id" (D-L1-32). |
| D-L2-9 | **Cover art** is deterministic and **stable under Pick changes**: input = Record id (palette) + `cover_seed` (layout) + the ordered `(root_track_id, recipe seed)` of the first 12 slot Songs (their lineage, not their current Take). **It extends M4a's `internal/media/cover`** (D-L2-39): a mosaic renderer `mm-cover/record-1` (C123) in new files — integer-only (Q16 fixed-point) geometry so the pixel goldens hold on amd64 and arm64, no fonts, no text — reusing M4a's integer palette (`makePalette`), `splitmix64`, 2×2 sampling (`pixelIndex`/`nearest`), file cache (`derived/covers/`, `Root.Link` claim), `Resolve`/`Sweep` and signed `/media/<key>.png` serving. `update_record cover_reroll` changes only `cover_seed` (layout, not palette). The same renderer produces neutral covers for auto-albums (`Kind album`, A2). | Concept §2 "deterministic art from its Songs' seeds/lineage"; a cover that changed with every Pick would churn Subsonic art caches. |
| D-L2-10 | **Liner notes** are generated, never stored, by the pure `liner` package from a `liner.Input` that `recordview` assembles; the operator's own text (`liner_note` ≤ 4000 characters, writable by the same principals as the other text fields) is prepended. Two audiences: **`app`** (SPA, agents: titles, angles, sessions by name, engines, tempo, Pick reasons, **costs**, and Muse title/artist) and **`export`** (tape, anything leaving the app: titles, angle slugs, engine, duration, tempo, Sound families, Pick reasons; sessions only as `kind + date`; **no costs, no Muse text** unless `export_muse_names`, D-L2-16). Output is ≤ 8 KB Markdown; goldens pin both audiences. | Concept §2 liner notes from provenance; §4 tape metadata without Muse artist names by default; spend is private. |
| D-L2-11 | **Energy curve** (studio preview and the L3 sequencing input) is read-only data from Ears: each slot's `short_term_1hz` after its planned gain, downsampled to ≤ 120 points, plus mean LUFS and the Song's effective BPM (L1 `TempoDetail`). Returned only with `include=energy`. | Concept §3 "energy curve preview (Ears)"; no new analyzer. |
| D-L2-12 | **Crates.** `kind` `manual` (explicit items) or `smart` (saved filter); `subject` `song` (default) or `take`, **fixed at creation**; a Crate never mixes subjects. Manual items are append-only rows with soft removal (re-adding creates a new row); a Crate has ≤ 5 000 live items; item edits are commutative and do **not** bump `revision` (metadata edits do). Crates are not nested and do not contain other Crates or Records. A manual Crate's retired Songs are resolved through `retired_into` at read time (no writer). | Concept §2: "Crates hold Songs or Takes"; one subject keeps every smart filter well-typed. |
| D-L2-13 | **Smart filters are the list-op vocabulary.** A smart Crate stores `filter_json` (`mm.crate-filter/1`, ≤ 4 096 B, unknown keys rejected) whose keys are **exactly the query parameters** of `list_songs` (subject `song`) or `search_tracks` (subject `take`); expansion maps the JSON to the same Go filter struct the op uses, so a live count, a Crate view and an agent's ad-hoc query can never disagree (`TestCrateVocabulary_EqualsListOps`). L2 adds the missing predicates: for **both** ops `unheard` (no human/device `start` on the Take; for a Song: on any of its Takes), `in_record`, `crate_id`; for `search_tracks` also `sound`; for Songs `min_duration_s`, `max_duration_s`, `min_stars`, `unrated`, `tag`, `lossy`, `recipe_known`, evaluated on the Song's **subject take** (its Pick take, else its newest eligible take; a Song with no take never matches a duration, star or tag predicate), and **`pick_decided`** (D-L2-36): true iff the Pick source is `pinned` or `ladder` (the Board's `picked` chip, D-L1-18), so "no Pick yet" is `pick_decided=false`; a Song whose Pick is only the `newest` or `stars` fallback matches it. **`has_pick` keeps its shipped meaning** (a Pick row with a take, any source). | Concept §2 example filter becomes executable and unambiguous without changing a shipped filter; one implementation. |
| D-L2-14 | **Resolving a Crate.** `crate_id` on `list_songs` / `search_tracks`: manual ⇒ membership join; smart ⇒ `crates.Expand` merges the stored filter into the request filter — a field set by both with different values ⇒ 422 `FILTER_CONFLICT`, otherwise AND; a filter may not contain `crate_id` (no nesting). Counts are computed on read (never stored); `list_crates` returns `count` for smart Crates only when `counts=true`. A Crate whose Muse/Song/Session ids no longer exist matches nothing and reports a warning. | Saved questions stay live; cycles are impossible by construction. |
| D-L2-15 | **Album catalog.** `albums` exposes up to three kinds. **`record`** (id `rec-<record id>`; lane A): every **published, non-hidden** Record, name = its title, songs = its listable slots in order, disc = side letter index. **`muse`** (id `mus-<muse id>`) and **`unfiled`** (id `unf`, name `Unfiled`) — lane **A2**, separable (D-L2-29): the **Picks** of the Muse's live, non-hidden Songs that are in **no published Record**, ordered by Song creation; an empty album is not listed; a hidden Muse's album is not listed. Auto-album **names are neutral**: `Inspiration NNN` where NNN is the Muse's frozen **`muses.ordinal`** (D-L2-37, zero-padded to 3 digits), with `subsonic_muse_names` on a `muse` album is named by the Muse (`title`, or `title · artist`) instead. The package exposes **membership, neutral naming and a cheap change token**; the Subsonic consumer is lane S1 (Record albums) and S2 (auto-albums) (D-L2-35). The `unfiled` album is a plan invention (not in the concept) and needs the P.5 approval. Curation moves Songs *out of* the auto-albums: the pile shrinks as Records are published. | The only reading of "per-Muse auto-albums" that respects concept §0 by default; Records are the way to get a real album name. |
| D-L2-16 | **Two plain opt-ins, structurally enforced.** `settings` keys `curation.export_muse_names` and `curation.subsonic_muse_names` (default false) are written only by the human-only op `set_curation_settings` (admin scope, audited) and readable by everyone with read scope (`get_curation_settings`, so an agent knows what it may expect). `albums` and `tape` **cannot import `muses`**: `albums` takes a `MuseDirectory` interface (`Ordinals`, `Version`, and `Labels` that the composition root wires to real names **only** when the flag is on) and `tape` takes already-rendered metadata from `recordview`, the only L2 package that may read Muse text. The tape key includes the flag, so toggling produces a different artifact. The L1 rule "export, subsonic and exporter packages must not reach `muses`" stays green and is extended to the L2 packages. | A flag that is only a convention would be broken by the next exporter; this makes the unsafe path unrepresentable without the opt-in. |
| D-L2-17 | **Tape format.** One FLAC, **48 kHz · 24-bit · stereo** (sources at other rates are resampled by ffmpeg `swr`, mono is duplicated to stereo, >2 channels are downmixed — each flagged per item and in the manifest), **gapless** (Takes concatenated sample-exactly, nothing inserted, no fades, no crossfade), **loudness-matched** by a static per-Take gain (D-L2-18) applied **in Go** with round-to-nearest, no limiter, no compression, no dither. Encoded by the reference `flac` encoder from a raw stdin pipe (`-8`, seek table, no padding), then Go rewrites the metadata blocks (STREAMINFO/SEEKTABLE and the audio frames copied verbatim; frame-region sha256 asserted equal). Chapters are written **three consistent ways**: the binary CUESHEET block (sample-exact; lead-out track 255, `is_cd = 0`), `CHAPTERnnn`/`CHAPTERnnnNAME` Vorbis comments (millisecond), and a text `CUESHEET` comment (75 fps frames; the form foobar2000/mpv read). Tags: `TITLE`/`ALBUM` = Record title, `ARTIST` = `metamusic`, `DATE` (the Record's `first_published_at` day, never the export day), `GENRE` (top Sound families), `DESCRIPTION` (the export-audience liner summary ≤ 1 000 chars), `REPLAYGAIN_ALBUM_GAIN/PEAK`, `MM_*` provenance (`MM_RECORD_ID`, `MM_TAPE_VERSION=mm-tape/1` — no revision or tape id is written, they differ between otherwise identical exports and live in the database — `MM_TARGET_LUFS`, `MM_CONTAINS_LOSSY_SOURCES`), a front-cover PICTURE (the Record's 512 px cover). **Lossy label:** if any item is a lossy Take (decoded from MP3 — never upgraded, never presented as lossless) the file carries `MM_CONTAINS_LOSSY_SOURCES=true` and a `DESCRIPTION` line "Contains lossy sources (n of m tracks)", the chapter names of those items end in ` [lossy source]`, the filename ends `-lossy-sources.flac`, and the API says `contains_lossy: true`. Limits: ≤ 60 items, ≤ 7 200 s, ≤ 2 GiB. | Concept §7 L2 acceptance (chapters, loudness, gapless, lossy label); the reference encoder gives a trustworthy STREAMINFO MD5. |
| D-L2-18 | **Loudness plan** (pure `tape.PlanGains`, also used for the Record's playback `gain_db`): target `T` = the caller's value, else the **median** integrated LUFS of the measured, non-silent items clamped to [−23.0, −14.0] and rounded to 0.1 LU (no measurement at all ⇒ −14.0); per-item gain = `Tc − Lc` centi-dB, where `Tc`/`Lc` are the target and the item's integrated LUFS converted once to integer centi-units (`round(x·100)`, half away from zero), clamped to [−1 200, +600] (`cut_clamped` / `boost_clamped`), then **peak-guarded**: gain ≤ `−100 − TPc` with `TPc = round(truePeak·100)` (`peak_limited`; exact in integers — the float form `floor((−1.0 − (−1.9))·100)` yields 89 instead of 90); items at or below −60 LUFS are `silent` (gain 0, excluded from the median), items without a loudness analysis are `unmeasured` (gain 0; the tape export asks Ears to analyse them first and falls back to `unmeasured` with a warning). Integer arithmetic only; the plan stores the predicted post-gain LUFS (`L + gain/100`). A post-gain sample that would exceed 24-bit full scale aborts the export (`TAPE_CLIPPED`) — cannot happen when the guard holds; it is the proof that it does. | Static, reproducible, never clips, never pumps; same rule as the duel gains' integer discipline (D-M2b-2). |
| D-L2-19 | **Tape lifecycle.** Only a **published** Record can be exported (409 `RECORD_NOT_PUBLISHED`). `export_record_tape` snapshots the Record (resolved Takes, gains, tags, liner, cover) in one read transaction, computes the **input key** (`sha256` over version, ordered `(master sha256, gain cdB)`, the target rounded to 0.1 LU, liner/tag bytes incl. chapter names, cover key, `flac` version, binary version, `muse_names` flag), inserts a `queued` `tapes` row (unique per live key: a repeated export while the first is queued/running/ready **returns that row**, `reused: true`) and returns 201 immediately; one in-process runner (concurrency 1) claims, renders, verifies and marks it `ready`/`failed`; `running` rows with an expired lease are re-queued at startup. The file lives in `derived/tapes/<aa>/<sha>.flac` (0444), is served by a signed `/media/<sha>.flac` link with `Content-Disposition`, expires after **14 days** (row → `expired`, file swept; a re-export under the same binary and `flac` version re-renders a byte-identical file), is never backed up and is excluded from `library_orphans`. Export is refused (409 `TAPE_NEEDS_REVIEW`) while the snapshot has `pin_ineligible` or `undecided_pick` slots unless a **human** principal passes `accept_warnings: true`, and quotas bound the disk (≤ 3 live tapes per Record, ≤ 8 GiB of `ready` bytes: 409 `TAPE_QUOTA`), D-L2-33. | Large, CPU-heavy, reproducible artifact: async with reuse; retention bounds disk. |
| D-L2-20 | **Playback.** The SPA applies each slot's `gain_db` through a `GainNode` on the shared `AudioContext` (works on iOS where `audio.volume` is ignored) and hands off to the next slot with the player's existing prefetch; this is **best-effort gapless** and is measured, not promised (device checklist DC-L2-2), and the `MediaElementAudioSourceNode` path must also keep playing with the screen locked on iOS (DC-L2-2); **Play as tape is the supported iOS path** if it does not. **"Play as tape"** plays the Record's ready tape (rendering it first when absent) as a `tape:<id>` queue item through the one `<audio>` element of `App.svelte` (D-L2-42) and is the guaranteed-gapless, guaranteed-matched path. Subsonic gets `replayGain` metadata for Record albums from the same plan (S1). | Honest about what an HTML media element can do; the tape is the proof. |
| D-L2-21 | **Registry.** 17 operations: 5 MCP tools (`list_records`, `get_record`, `propose_record`, `create_crate`, `update_crate`; `create_record` is not in concept §4's agent tool list, so it is generic from the start and agents reach it through `call_operation`), 10 generic (`create_record`, `update_record`, `edit_record_slots`, `get_record_changes`, `export_record_tape`, `get_tape`, `list_crates`, `get_crate`, `edit_crate_items`, `get_curation_settings`), 2 human-only (`publish_record`, `set_curation_settings`). Registry **109 → 126**. Parity cases: 15 (every op with an MCP face has a UI route too). `tools/list` is at 104 699 of a 104 800 ceiling with 39 tools, so F2 re-measures with the five new tools and sets `toolsCeilingBytes` to the measured value rounded up with a one-line justification (the file's history comment pattern); if the L2 tools alone exceed 9 000 compact bytes demote in order `update_crate`, `create_crate`, `list_records` to generic and re-measure; beyond that stop and ask the operator (D-M3-36 pattern). | Bounded MCP surface; concept §4 names exactly these agent faces. |
| D-L2-22 | **Money.** No L2 op has a cost other than `free` (`TestL2_NoSpendOps`); no L2 code path calls a provider; liner notes read existing cost fields as integer micro-USD (rule 6) for the `app` audience only. | CLAUDE.md rule 6; spend stays M3's. |
| D-L2-23 | **Immutability.** Every L2 table follows the repo rules: no physical deletes (triggers), frozen identity columns, soft removal; there is **no derived cache table** (the Syncer's state lives on slots, the catalog is computed), so the `TestNoDeleteSQL` allowlist is unchanged. Tape *files* are the only deletable artifacts. | Rules 5, 7, 8: history is the product. |
| D-L2-24 | **Timestamps and ordering.** Every new timestamp is written with `store.FormatTime` (24-character, D-L1-25); lists order by `(updated_at DESC, id DESC)` keyset (Records, Crates) or `seq` (changelog); anything that mixes L2 and pre-M3 columns compares parsed instants (D-L1-34). | Same discipline as L1. |
| D-L2-25 | **Verify and CLI.** `records.Verify` implements invariants R1–R9 (C122.5); `metamusic library verify` runs L1 then L2 invariants (exit 1 on any anomaly); `metamusic library reconcile` runs the provenance `Settle` then the Syncer `Settle`; `--dry-run` reports slots with pending events. `metamusic tape sweep [--dry-run]` is not added (the daily `serve` ticker sweeps). | One maintenance concept (D-L1-27). |
| D-L2-26 | **Contract numbers** C118–C132, migration `0010`, decisions D-L2-1…43, test files suffixed `_l2`; L3 starts at C133/0011; the M5/M6 drafts renumber later. | Continuity. |
| D-L2-27 | **Privacy of curation text, and the real scope of the publish gate.** Record titles, subtitles, liner notes, proposal rationales and Crate names/descriptions are operator-visible content. (a) **Record-owned text and membership** (title, subtitle, liner note, rationale, slots, order) is written by agents on draft/proposed Records only and reaches Subsonic or a tape only after a human published that Record. (b) **Catalog text agents can already edit** — Take titles and status (`update_track`, scope `write`), Song titles (`update_song`), Muse names and grouping (`update_muse`, `assign_muse_songs`) — is **not** behind the Record gate: Take titles become tape chapter names and Song titles enter the export liner, both read at **snapshot time**; Muse changes alter auto-album grouping (names only with the opt-in). Rule for (b): the tape and the catalog read these fields live; the **publish page shows exactly what the tape would carry** (chapter names, export-liner Song titles, tags, warnings: `get_record include=preview`); the tape key includes those strings, so a later title edit never rewrites an existing tape; a title an agent edits after publishing is visible in the next preview and on the next tape's plan, and a human may unpublish. This is review-by-display, not a hard gate, and the plan claims no more. (c) Muse *text* never travels either path by default (D-L2-16). **Mutating ops return a liner-less `RecordView`**: only `get_record` may emit `liner` (C124.2), so the five mutating ops and the stored `idem` row carry no Muse text. Canaries (a Muse-text string in an agent-authored title/note/rationale/Crate name) are tested to appear **only** in the ops that return that field and never in logs, audit text, exports, job events, notifications or problem bodies (C127.4); a Muse-looking agent-written Take or Song title is tested to appear in the tape/preview only as documented (`TestL2_AgentTitleCanary`, `TestPublishPreview_EqualsSnapshot`). The generated `app` liner notes carry Muse title/artist and are the **one** new Muse-text surface (`get_record` only). | D-L1-6 extended without widening it; the guarantee is stated at the strength it actually has. |
| D-L2-28 | **YAGNI (not in L2, not to be built "just in case"):** promote-playlist-to-Record and Record-to-playlist, nested or mixed-subject Crates, Crate sharing, per-slot notes or per-slot gain overrides, crossfades or fades on the tape, ReplayGain per chapter, other tape formats (MP3/ALAC/ZIP bundle with `.cue`) unless the compatibility spike fails (then one fallback, D-L2-29), cover text or uploaded covers, Record versions/branches/diffs beyond the changelog, scheduled or automatic tapes, tape sharing links beyond the signed download link, Record-level privacy flags, Subsonic playlists for Records, `propose_record` auto-sequencing and any tempo/key/energy ordering (L3), Muse-aware `next_actions` (L3), a Record `final` flag. | Each is an L3 or later decision with its own acceptance. |
| D-L2-29 | **Scope valves.** Separable: (a) the **tape lanes** (T1, T2) — if they slip or fail the real-player spot check, L2 ships Records, Crates and the catalog and the tape op answers 503 `UNAVAILABLE {component: tape}`; no other acceptance item depends on them except `TestAcceptance_L2_TapeExport`. Cutting them removes only tape-lane code (`internal/tape` service/pipeline/runner, the `flacmeta` writers beyond the `tagged` extraction, `mediatool.Stdin`, `ops_tape_l2.go`); cover serving, the orphan skip and the Record/catalog code stay. Fallback if chapter compatibility fails in the operator's players: add a sidecar `.cue` as one extra download of the same tape (no new format). (b) **A2/S2** (Muse auto-albums, `unfiled`, neutral names, the `subsonic_muse_names` effect): if cut, the booleans stay in the frozen API but `subsonic_muse_names` has no effect, and the `muses.ordinal` column stays unused. (c) Cut candidates if time is short, in order: the Energy curve (`include=energy`), `cover_reroll`, the `side` label UI (the column and ops stay), `unfiled`, the weekly 300-sequence CI job. | Keeps L2 shippable on the stable core. |
| D-L2-30 | **Published Records keep floating; undecided Picks are flagged; `pin_all` freezes.** A published Record's floating slots follow Picks (that is what "living" means) and each change is logged. A Pick whose source is `newest` or `stars` is **undecided** (no human judged it as the Pick; `stars` may be set by agents or devices): the slot reports warning `undecided_pick`; the changelog row carries `detail_json {"undecided": true}` and its why-text ends "· undecided"; `RecordView.totals.undecided`, `get_stats.curation.review.undecided` (published Records only) and the publish page count and list them; a tape export needs a human to accept them (D-L2-33). They are **not held back** in the app or the catalog: holding a published slot at its last decided take would need a stored take per floating slot, which breaks "derived, never stored" (D-L2-6); the alternative is recorded for the operator in P.5. The human can freeze the whole Record with the `pin_all` slot op (every floating slot with a Take is pinned to its current Take; `unpin_all` reverses); `pin_all`/`unpin_all` are plan inventions and need the P.5 approval. The catalog change token changes when a floating upgrade lands, so Subsonic clients refresh. The Tape is the unconditional freeze. | Concept §2 "living record" without letting an undecided take enter a published Record unseen. |
| D-L2-31 | **Eventual consistency, bounded and visible.** The only lagging output is the changelog (≤ one Syncer pass, `PollInterval = 1 s`); `RecordView.pending` is true while any floating slot has unconsumed Pick events or a provenance queue row for its Song's Takes, and the studio refetches (1.5 s, ≤ 3 times) as the Song page does. `Rig.Settle` runs provenance `Settle` then the Syncer. | Same trade as D-L1-4, with no new daemon. |
| D-L2-32 | **M4a amendments (M4a is built; the amendments are code in lanes S1/T1/V1 and addenda recorded by X1 in the M4a plan document).** C124/C125 — the Subsonic server gains Record albums (`rec-<id>`) through `albums.Service` (S1); `al-<root>` albums, folder rules, `getSimilarSongs` and every existing id are unchanged. C126.1 — `internal/media/cover` gains `RenderMosaic`/`MosaicKey`/`EnsureMosaic` without renaming or changing any existing symbol or the M4a golden (V1). C126.2 — the VORBIS_COMMENT and PICTURE block builders move from `tagged` into `flacmeta`; `tagged.Write` calls them and its goldens stay byte-identical (T1). `subsonic*` must keep the L1/L2 import-graph rules (it may reach `albums` and `media/cover`, never `muses`, `recordview` or `songview`). | The catalog contract is L2's deliverable; the protocol layer exists. |
| D-L2-33 | **Tape safety.** (1) *Review gate:* `export_record_tape` returns 409 `TAPE_NEEDS_REVIEW` (`details.slots`) when the snapshot has `pin_ineligible` or `undecided_pick` slots; a **human** principal may pass `accept_warnings: true` (any other principal ⇒ 403 `HUMAN_ONLY`); the warnings and the acceptance are stored in `plan_json` (ids and codes only) and shown in `Tape`. (2) *Quotas:* `MaxLivePerRecord = 3` live (queued/running/ready) tapes per Record and `MaxReadyBytes = 8 GiB` over all `ready` rows ⇒ 409 `TAPE_QUOTA` (`details.scope` `record|total`). (3) `target_lufs` is rounded to 0.1 LU before the plan and the key, so float noise cannot multiply keys. | An agent holds `write` scope: without these, distinct `target_lufs`/`include_liner` values would each render a ~400 MB file, and a discarded or undecided take could be baked into a file nobody looked at. |
| D-L2-34 | **Catalog scope (revised in revision 3; the draft's "contract-level until M4a" is withdrawn because M4a is built).** Lane A builds Record-album membership and the change token and S1 serves them over Subsonic (D-L2-35). A2 builds the `muse`/`unfiled` kinds, neutral names and the Muse directory; S2 serves them. No op and no SPA page exposes albums (the Subsonic protocol is the only consumer). Sorts, genre, year, play counts and ReplayGain for Record albums are computed by S1 inside `internal/subsonic` from `albums.Get` plus its own SQL, never inside `albums`. | Smallest change to a shipped server that delivers the concept's Record albums; auto-albums are optional and later. |
| D-L2-35 | **Subsonic consumer (lane S1), additive.** The shipped server keeps `album = lineage` (`al-<root id>`) for every Take and keeps each song's `albumId`/`parent`. S1 adds one more album kind: **Record albums** `rec-<record id>` (published, non-hidden Records with ≥ 1 listable slot) to `getAlbumList2`/`getAlbumList` (every type), `getIndexes`/`getArtists`/`getArtist` (under the one artist `metamusic`, `lastModified` includes `albums.Token`), `getAlbum`, `getMusicDirectory`, `getAlbumInfo2`, `search2/3` (title match), `getCoverArt` (`rec-<id>` → `cover.Service.EnsureMosaic`) and `getSong`'s unchanged lineage ids; songs served *inside* a Record album carry that album's id as `albumId`/`parent`, `track` = position, `discNumber` = side; `replayGain` album/track gains from the plan; folder rule (status) applies to the resolved Takes. Unknown, unpublished or hidden Record id ⇒ error 70. The `primary-album` replacement of lineage ids (draft C110.3) is **not built**. S2 (optional) adds `mus-<id>`/`unf` albums with the same machinery. | M4a's shipped ids and tests stay valid; Records are the new value; replacing lineage albums would break clients for no L2 gain. |
| D-L2-36 | **`pick_decided`, not a redefined `has_pick`.** `list_songs`/`search_sounds` already ship `has_pick` = a Pick row with a take (any source). L2 leaves it and adds `pick_decided` (source `pinned` or `ladder`, the Board's `picked`). The concept's example filter reads `{"sound":["genre_family:reggae"],"unheard":true,"min_duration_s":240,"pick_decided":false}`; result set unchanged (`{S1, S6}`). | Never change the meaning of a shipped agent-visible parameter. |
| D-L2-37 | **`muses.ordinal`** (frozen, set once): 0010 adds the column, backfills existing Muses by rank `(created_at, id)`, creates a unique index and an `AFTER INSERT` trigger assigning `MAX(ordinal)+1`, plus a trigger forbidding changes. `MuseDirectory.Ordinals` (in `internal/muses/directory.go`, the only package allowed to read `muses`) returns it; the neutral name is `Inspiration NNN` from it. The draft's dependence on backfill ordering (and its test) is replaced by `TestMuses_OrdinalAssigned`/`TestMigration0010_MuseOrdinalBackfill`. Kept in 0010 (not in A2) because only F1 touches migrations; harmless if A2 is cut. | Names Subsonic clients cache must never reshuffle, whatever the clock or id generator did. |
| D-L2-38 | **Web placement.** Records and Crates are **Library sub-views** (segmented row Takes · Sounds · Records · Crates · Playlists) at `/records`, `/crates` (+ `:id` detail routes, contextual); the read-only `/settings/curation` is a More → Settings row; the two secure pages `/human/records/:id/publish` and `/human/curation` are in-app `SecureGate` routes listed under More → Secure. Rationale: a Record is a listening object ("hear a finished sequence") and a Crate is a working set of takes to judge or remake — both are things you browse beside Takes and Playlists, which is exactly what the Library tab and its sub-view row are for; Make (More) is about generating and the Board, a different job; Judge is one-at-a-time evaluation. A "to judge" Crate reaches the Judge tab only through the queue (Play queue), not a new tab. | Listen-first shell; no new tab; reuses `SubNav`. |
| D-L2-39 | **Cover: extend, do not fork.** New symbols only: `MosaicVersion = "mm-cover/record-1"`, `Input`, `Part`, `RenderMosaic`, `MosaicKey`, `(*Service).EnsureMosaic`; M4a's `Version`, `Key`, `Render`, `EnsureTrack`, `EnsureAlbum` and `testdata/cover.golden.json` do not change. The renderer file contains no `float32`/`float64`/`math.` (`TestCover_NoFloatInRenderer` greps `mosaic*.go` only; M4a's `render.go` stays floating-point and golden-pinned). | Zero risk to shipped covers. |
| D-L2-40 | **FLAC writers: extract, then extend.** T1 first moves `blockHeader`, the VORBIS_COMMENT builder and the PICTURE builder from `internal/media/tagged/write.go` into exported `flacmeta` functions (behaviour-preserving; `tagged`'s tests and goldens are the guard), then adds `CuesheetBlock` and `Rewrite`. `tagged` keeps its own `Tags` type and calls `flacmeta`. | One implementation of the block formats. |
| D-L2-41 | **No Library multi-select in L2.** The Library list has none and building one is a redesign task. Entry points: `AddToCrate` menu on the Track and Song pages (the `AddToPlaylist` pattern), "In Records / In Crates" chips, and the **Board** selection (which exists) gains "Add to Crate…" and "New Record from selection…". A Library multi-select is a later UX item. | YAGNI; keeps W2 inside its lane. |
| D-L2-42 | **Playback constraints.** The player queue is track ids persisted by `playerStore` (v1) and `App.svelte` holds the only `<audio>` (`singleAudio.test.ts`). Therefore: (1) a Record plays as an ordinary queue of take ids with `source {label: record title, href: /records/<id>}`; the per-slot **gain map is not persisted** — a restored queue whose source href is a Record route re-derives it from `get_record` (W3); (2) **Play as tape** enqueues one `tape:<tape id>` item resolved through `get_tape.download.url` (a signed `.flac`), shown with its chapter list; `isTrackId` still rejects it, so a tape item is not restored after a reload (documented, acceptable); (3) the gain `GainNode` is attached lazily to the single element (`MediaElementAudioSourceNode` once). Only W3 edits `player.svelte.ts` and `playerStore.ts`. | Keeps the player's invariants; the riskiest file has one owner. |
| D-L2-43 | **Lanes and merge rules** (see "Lanes and tasks"): foundation F1 → F2 → F3 land first and alone; each later lane owns a closed file set; shared hot spots have exactly one owner; generated API files are frozen after F2 and regenerated, never hand-merged; every lane PR targets < ~2 500 changed lines excluding generated files. | Parallel agents with minimal conflicts. |

---


## Contracts (all lanes build against these — do not change without updating this file)

### C118. Packages, dependency order, constants

New packages (all under `internal/`):

| Package | Role | May import |
|---|---|---|
| `curation` | the two opt-in settings (read/write, audit-friendly) | `store`, `auth` |
| `records` | Record model, slot edits, `ResolveSlot`, changelog, `Syncer`, `Verify`, `ChangeText` (reads the L1 tables through SQL; imports no L1 package) | `store`, `auth` |
| `liner` | pure liner-note rendering (`Input` → `Liner` → Markdown) | stdlib only |
| `crates` | Crate model, smart-filter vocabulary, validation, `Expand` | `store`, `auth`, `songs`, `sounds`, `library` |
| `media/cover` (existing, M4a) | gains the mosaic renderer and `EnsureMosaic` (C123); M4a's symbols are unchanged | `media/serve`, `library` (existing) |
| `tape` | loudness plan, pipeline, runner, cache, resolver | `store`, `auth`, `mediatool`, `media/flacmeta`, `media/serve`, `media/cover` (types), `liner` (types), `textfold` (`Slug`, tape filename) |
| `albums` | the catalog Subsonic consumes (Records + auto-albums, neutral names, change token) | `store`, `records`, `textfold` (`Norm` for `SortName`), `media/cover` (types) |
| `recordview` | `get_record` assembly, liner input, energy curve, **tape snapshot** (`tape.Source`), cover input | `store`, `auth`, `records`, `muses`, `songs`, `sessions`, `sounds`, `picks`, `ears` (batch loudness reader over `analyses`), `curation`, `liner`, `tape` (types), `media/cover` (types), `library` |
| `media/flacmeta` (existing) | gains the exported block writers (C128.3), extracted from `media/tagged` plus CUESHEET and `Rewrite` | stdlib |
| `mediatool` (existing) | `RunOpts.Stdin` | stdlib |

`api` consumes them through `Deps` fields (`Records *records.Service`, `RecordView *recordview.Service`, `Crates *crates.Service`,
`Tapes *tape.Service`, `Curation *curation.Service`, `Albums *albums.Service`; nil ⇒ 503 `UNAVAILABLE {component}`, the
M2a/M3/L1 convention; `Albums` has no op in L2 — it is wired for the Subsonic server (S1) and for the tests). `app.Build` constructs and wires everything,
adapts `muses.Service` into `albums.MuseDirectory` (names only when the flag is on, D-L2-16), `recordview.Service` into `tape.Source`,
`ears.Service` into `tape.Loudness`, and starts `records.Syncer.Run` and `tape.Service.Run` in `serve` (never in `dev`'s offline mode
without a store, never in `mcp-stdio` or the CLI).

Import-graph rules (`TestImportGraph_L2`, extending `TestImportGraph_L1`):
- the packages above import only what the table lists; `records`, `crates`, `liner`, `cover`, `tape`, `albums` are leaves with respect to
  `recordview` (a leaf never imports `recordview`);
- **`records`, `liner`, `media/cover`, `tape`, `albums`, `curation` must not reach `muses`, `songview`, `board` or `recordview`
  (transitively)** — the Muse-visibility rule (C127). This is why `records` imports no L1 package (`songs` imports `muses`, so any import of `songs` would let a
  Subsonic-bound package reach Muse text through `albums → records`): it reads `songs`, `song_roots`, `song_picks` and `pick_events` through SQL only. `crates` may import `songs`, `sounds` and
  `library` (it feeds the list ops and no export path); only `recordview` (and the composition roots `api`/`app`) may read Muse text;
- `export`, `exports`, `notify`, `audit`, `backup`, `importer`, `jobs`, `generation`, `lineage`, `library`, `redact` and every package whose
  path contains `subsonic` must not reach `recordview` or `muses` (L1 rule extended); they **may** reach `albums` and `media/cover`;
- `albums` reads `records` through its exported read functions only (`TestSQL_WriteOwners_L2` forbids any write); `tape` does not import `records` at all (it receives a `Snapshot`);
- `picks` and `provenance` never import any L2 package (the dependency points one way: L2 reads L1).

`store` gains nothing. `store.Env` (L1) is reused; `textfold` (L1) gains `Slug(s string, max int) string` (ASCII `[a-z0-9._-]`, the rules of the unexported `exports.cleanFilename`; F2-owned) for the tape filename.

Constants (`internal/records/params.go`, `internal/tape/params.go`, `internal/crates/params.go`, `internal/curation/params.go`):
`records.MaxSlots = 60`, `MaxEditOps = 50`, `MaxProposalNote = 500`, `MaxChangesPage = 200`, `SyncBatch = 200` (slots per pass),
`SyncPoll = 1s`, `CoverParts = 12`; `tape.MaxItems = 60`, `MaxDurationS = 7200`, `Rate = 48000`, `Bits = 24`, `TargetMinLUFS = -23.0`,
`TargetMaxLUFS = -14.0`, `SilentLUFS = -60.0`, `MaxCutCDB = -1200`, `MaxBoostCDB = 600`, `PeakCeilingDBTP = -1.0`, `ItemTimeout = 120s`,
`LeaseTTL = 60s`, `RetentionDays = 14`, `MaxBytes = 2 GiB`, `Chunk = 1 MiB`, `MaxLivePerRecord = 3`, `MaxReadyBytes = 8 GiB`; `records.SyncRetries = 5`; `crates.MaxManualItems = 5000`, `MaxEditBatch = 200`,
`MaxFilterBytes = 4096`, `MaxName = 80`, `MaxDescription = 500`; `curation.KeyExport = "curation.export_muse_names"`,
`KeySubsonic = "curation.subsonic_muse_names"`.

### C119. Migration `internal/store/migrations/0010_curation.sql` (full DDL) and write rules

```sql
-- 0010_curation.sql — L2: Records, slots, changelog, Crates, Tapes, and the frozen Muse ordinal. Creates no L2 row (no seed, no queue).

-- ---- Muse ordinal (D-L2-37): the stable rank behind the neutral album names ---------------------
ALTER TABLE muses ADD COLUMN ordinal INTEGER CHECK (ordinal IS NULL OR ordinal >= 1);
UPDATE muses SET ordinal = (SELECT COUNT(*) + 1 FROM muses m2
                             WHERE m2.created_at < muses.created_at OR (m2.created_at = muses.created_at AND m2.id < muses.id));
CREATE UNIQUE INDEX muses_ordinal ON muses(ordinal) WHERE ordinal IS NOT NULL;
CREATE TRIGGER muses_ordinal_assign AFTER INSERT ON muses WHEN NEW.ordinal IS NULL
BEGIN UPDATE muses SET ordinal = (SELECT COALESCE(MAX(ordinal), 0) + 1 FROM muses) WHERE id = NEW.id; END;
CREATE TRIGGER muses_ordinal_once BEFORE UPDATE OF ordinal ON muses
WHEN OLD.ordinal IS NOT NULL AND NEW.ordinal IS NOT OLD.ordinal
BEGIN SELECT RAISE(ABORT, 'muses: ordinal is set once'); END;

-- ---- Records -----------------------------------------------------------------------------------
CREATE TABLE records (
  id                 TEXT PRIMARY KEY,                       -- UUIDv7
  title              TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
  subtitle           TEXT NOT NULL DEFAULT '' CHECK (length(subtitle) <= 200),
  liner_note         TEXT NOT NULL DEFAULT '' CHECK (length(liner_note) <= 4000),   -- the operator's own text; generated notes are never stored
  cover_seed         INTEGER NOT NULL CHECK (cover_seed BETWEEN 0 AND 9007199254740991),   -- records.CoverSeed(id, cover_rerolls) (Verify R6)
  cover_rerolls      INTEGER NOT NULL DEFAULT 0 CHECK (cover_rerolls >= 0),
  status             TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','proposed','published')),
  order_json         TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(order_json) AND json_type(order_json) = 'array'),   -- live slot ids in order (D-L2-5)
  proposal_note      TEXT CHECK (proposal_note IS NULL OR length(proposal_note) BETWEEN 1 AND 500),
  proposed_by        TEXT,                                   -- Principal.Key()
  proposed_at        TEXT,
  published_by       TEXT,                                   -- Principal.Key() of a KindHuman
  published_at       TEXT,
  first_published_at TEXT,                                   -- set once, never cleared (album ordering, D-L2-15)
  revision           INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),   -- bumped by every human/agent edit; never by the Syncer (incl. its duplicate-removal order rewrite, C122.2.1)
  created_by         TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  updated_by         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  hidden_at          TEXT,
  hidden_by          TEXT,
  CHECK ((proposed_at IS NULL) = (proposed_by IS NULL)),
  CHECK ((published_at IS NULL) = (published_by IS NULL)),
  CHECK ((published_at IS NOT NULL) = (status = 'published')),
  CHECK (status <> 'draft' OR proposed_at IS NULL),
  CHECK (proposal_note IS NULL OR proposed_at IS NOT NULL),
  CHECK (published_at IS NULL OR first_published_at IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX records_updated ON records(updated_at, id);
CREATE INDEX records_published ON records(first_published_at, id) WHERE status = 'published' AND hidden_at IS NULL;
CREATE TRIGGER records_no_delete BEFORE DELETE ON records
BEGIN SELECT RAISE(ABORT, 'records: no physical delete'); END;
CREATE TRIGGER records_frozen BEFORE UPDATE OF id, created_by, created_at ON records
BEGIN SELECT RAISE(ABORT, 'records: frozen column'); END;
CREATE TRIGGER records_first_published_once BEFORE UPDATE OF first_published_at ON records
WHEN OLD.first_published_at IS NOT NULL AND NEW.first_published_at IS NOT OLD.first_published_at
BEGIN SELECT RAISE(ABORT, 'records: first_published_at is set once'); END;
CREATE TRIGGER records_status_path BEFORE UPDATE OF status ON records
WHEN NOT (NEW.status = OLD.status
       OR (OLD.status = 'draft'    AND NEW.status IN ('proposed','published'))
       OR (OLD.status = 'proposed' AND NEW.status IN ('published','draft'))
       OR (OLD.status = 'published' AND NEW.status = 'draft'))
BEGIN SELECT RAISE(ABORT, 'records: illegal status transition'); END;

CREATE TABLE record_slots (
  id             TEXT PRIMARY KEY,                           -- UUIDv7; stable across reorders (the changelog's subject)
  record_id      TEXT NOT NULL REFERENCES records(id),
  song_id        TEXT NOT NULL REFERENCES songs(id),         -- follows retired_into only (trigger)
  side           TEXT CHECK (side IS NULL OR side IN ('A','B','C','D')),
  pinned_take_id TEXT REFERENCES tracks(id),
  pinned_by      TEXT,
  pinned_at      TEXT,
  synced_seq     INTEGER NOT NULL DEFAULT 0 CHECK (synced_seq >= 0),    -- highest pick_events.seq of song_id accounted for (D-L2-7)
  added_by       TEXT NOT NULL,
  added_at       TEXT NOT NULL,
  removed_by     TEXT,
  removed_at     TEXT,
  CHECK ((pinned_take_id IS NULL) = (pinned_by IS NULL)),
  CHECK ((pinned_take_id IS NULL) = (pinned_at IS NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
);
CREATE UNIQUE INDEX record_slots_live_song ON record_slots(record_id, song_id) WHERE removed_at IS NULL;
CREATE INDEX record_slots_song   ON record_slots(song_id) WHERE removed_at IS NULL;
CREATE INDEX record_slots_record ON record_slots(record_id);
CREATE TRIGGER record_slots_no_delete BEFORE DELETE ON record_slots
BEGIN SELECT RAISE(ABORT, 'record_slots: no physical delete'); END;
CREATE TRIGGER record_slots_frozen BEFORE UPDATE OF id, record_id, added_by, added_at ON record_slots
BEGIN SELECT RAISE(ABORT, 'record_slots: frozen column'); END;
CREATE TRIGGER record_slots_removed_final BEFORE UPDATE ON record_slots WHEN OLD.removed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'record_slots: removed slots are immutable'); END;
CREATE TRIGGER record_slots_song_follow BEFORE UPDATE OF song_id ON record_slots
WHEN NEW.song_id IS NOT OLD.song_id
 AND (SELECT retired_into FROM songs WHERE id = OLD.song_id) IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: song_id follows retired_into only'); END;
CREATE TRIGGER record_slots_synced_monotonic BEFORE UPDATE OF synced_seq ON record_slots
WHEN NEW.synced_seq < OLD.synced_seq AND NEW.song_id IS OLD.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: synced_seq never decreases'); END;
CREATE TRIGGER record_slots_live_song_ins BEFORE INSERT ON record_slots
WHEN (SELECT retired_at FROM songs WHERE id = NEW.song_id) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'record_slots: song is retired'); END;
CREATE TRIGGER record_slots_pin_in_song BEFORE INSERT ON record_slots
WHEN NEW.pinned_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: pinned take is not in the song'); END;
CREATE TRIGGER record_slots_pin_in_song_upd BEFORE UPDATE OF pinned_take_id, song_id ON record_slots
WHEN NEW.pinned_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: pinned take is not in the song'); END;

CREATE TRIGGER records_order_consistent BEFORE UPDATE OF order_json ON records     -- defined after record_slots (it reads it)
WHEN json_array_length(NEW.order_json) <> (SELECT COUNT(*) FROM record_slots s WHERE s.record_id = NEW.id AND s.removed_at IS NULL)
  OR (SELECT COUNT(DISTINCT j.value) FROM json_each(NEW.order_json) j) <> json_array_length(NEW.order_json)
  OR EXISTS (SELECT 1 FROM json_each(NEW.order_json) j
              WHERE NOT EXISTS (SELECT 1 FROM record_slots s
                                 WHERE s.id = j.value AND s.record_id = NEW.id AND s.removed_at IS NULL))
BEGIN SELECT RAISE(ABORT, 'records: order must list exactly the live slots'); END;

CREATE TABLE record_changes (                                  -- append-only changelog (concept §2)
  id            TEXT PRIMARY KEY,                              -- UUIDv7
  record_id     TEXT NOT NULL REFERENCES records(id),
  seq           INTEGER NOT NULL CHECK (seq >= 1),             -- per-record chain 1..n
  at            TEXT NOT NULL,
  kind          TEXT NOT NULL CHECK (kind IN (
                  'created','renamed','retexted','cover_rerolled','hidden','unhidden',
                  'slot_added','slot_removed','slot_moved','slot_side','slot_pinned','slot_unpinned',
                  'slot_upgraded','slot_filled','slot_emptied','slot_song_followed',
                  'proposed','proposal_dismissed','published','unpublished')),
  slot_id       TEXT REFERENCES record_slots(id),
  position      INTEGER CHECK (position IS NULL OR position >= 1),          -- the slot's position when the row was written
  from_take_id  TEXT REFERENCES tracks(id),
  to_take_id    TEXT REFERENCES tracks(id),
  reason        TEXT CHECK (reason IS NULL OR reason IN
                  ('initial','pin','unpin','pin_ineligible','pin_restored','ladder','stars','newest','emptied','retired','duplicate')),
  pick_event_id TEXT REFERENCES pick_events(id),               -- the L1 event that caused a Syncer row
  evidence_json TEXT CHECK (evidence_json IS NULL OR json_valid(evidence_json)),   -- the event's mm.pick/1, copied
  detail_json   TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),       -- ids/numbers only, never titles or Muse text
  actor         TEXT NOT NULL,                                 -- Principal.Key(), or 'system:records-sync'
  actor_kind    TEXT NOT NULL CHECK (actor_kind IN ('human','agent','system')),
  actor_name    TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 120),
  UNIQUE (record_id, seq),
  CHECK ((kind LIKE 'slot\_%' ESCAPE '\') = (slot_id IS NOT NULL)),
  CHECK (kind NOT IN ('slot_upgraded','slot_filled','slot_emptied') OR pick_event_id IS NOT NULL),
  CHECK (kind <> 'slot_upgraded' OR (from_take_id IS NOT NULL AND to_take_id IS NOT NULL AND from_take_id <> to_take_id)),
  CHECK (kind <> 'slot_filled'   OR (from_take_id IS NULL AND to_take_id IS NOT NULL)),
  CHECK (kind <> 'slot_emptied'  OR (from_take_id IS NOT NULL AND to_take_id IS NULL))
);
CREATE UNIQUE INDEX record_changes_event ON record_changes(slot_id, pick_event_id) WHERE pick_event_id IS NOT NULL;   -- Syncer idempotence
CREATE INDEX record_changes_record ON record_changes(record_id, seq);
CREATE INDEX record_changes_slot   ON record_changes(slot_id, seq) WHERE slot_id IS NOT NULL;
CREATE TRIGGER record_changes_immutable BEFORE UPDATE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record_changes: append-only'); END;
CREATE TRIGGER record_changes_no_delete BEFORE DELETE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record_changes: append-only'); END;
CREATE TRIGGER record_changes_chain BEFORE INSERT ON record_changes
WHEN NEW.seq <> COALESCE((SELECT MAX(seq) FROM record_changes WHERE record_id = NEW.record_id), 0) + 1
BEGIN SELECT RAISE(ABORT, 'record_changes: broken chain'); END;
CREATE TRIGGER record_changes_slot_of_record BEFORE INSERT ON record_changes
WHEN NEW.slot_id IS NOT NULL
 AND (SELECT record_id FROM record_slots WHERE id = NEW.slot_id) IS NOT NEW.record_id
BEGIN SELECT RAISE(ABORT, 'record_changes: slot is not in the record'); END;

-- ---- Crates ------------------------------------------------------------------------------------
CREATE TABLE crates (
  id          TEXT PRIMARY KEY,                              -- UUIDv7
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  kind        TEXT NOT NULL CHECK (kind IN ('manual','smart')),
  subject     TEXT NOT NULL DEFAULT 'song' CHECK (subject IN ('song','take')),
  filter_json TEXT CHECK (filter_json IS NULL OR (json_valid(filter_json) AND json_type(filter_json) = 'object' AND length(filter_json) <= 4096)),
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),     -- bumped by name/description/filter/hidden edits, not by item edits
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK ((kind = 'smart') = (filter_json IS NOT NULL)),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX crates_updated ON crates(updated_at, id);
CREATE TRIGGER crates_no_delete BEFORE DELETE ON crates
BEGIN SELECT RAISE(ABORT, 'crates: no physical delete'); END;
CREATE TRIGGER crates_frozen BEFORE UPDATE OF id, kind, subject, created_by, created_at ON crates
BEGIN SELECT RAISE(ABORT, 'crates: frozen column'); END;

CREATE TABLE crate_items (                                     -- manual crates only; soft removal; re-adding creates a new row
  id         TEXT PRIMARY KEY,                                 -- UUIDv7
  crate_id   TEXT NOT NULL REFERENCES crates(id),
  song_id    TEXT REFERENCES songs(id),
  track_id   TEXT REFERENCES tracks(id),
  added_by   TEXT NOT NULL,
  added_at   TEXT NOT NULL,
  removed_by TEXT,
  removed_at TEXT,
  CHECK ((song_id IS NULL) <> (track_id IS NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
);
CREATE UNIQUE INDEX crate_items_live_song  ON crate_items(crate_id, song_id)  WHERE song_id  IS NOT NULL AND removed_at IS NULL;
CREATE UNIQUE INDEX crate_items_live_track ON crate_items(crate_id, track_id) WHERE track_id IS NOT NULL AND removed_at IS NULL;
CREATE INDEX crate_items_song  ON crate_items(song_id)  WHERE song_id  IS NOT NULL AND removed_at IS NULL;
CREATE INDEX crate_items_track ON crate_items(track_id) WHERE track_id IS NOT NULL AND removed_at IS NULL;
CREATE TRIGGER crate_items_no_delete BEFORE DELETE ON crate_items
BEGIN SELECT RAISE(ABORT, 'crate_items: no physical delete'); END;
CREATE TRIGGER crate_items_frozen BEFORE UPDATE OF id, crate_id, song_id, track_id, added_by, added_at ON crate_items
BEGIN SELECT RAISE(ABORT, 'crate_items: frozen column'); END;
CREATE TRIGGER crate_items_removed_final BEFORE UPDATE ON crate_items WHEN OLD.removed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'crate_items: removed items are immutable'); END;
CREATE TRIGGER crate_items_manual BEFORE INSERT ON crate_items
WHEN (SELECT kind FROM crates WHERE id = NEW.crate_id) <> 'manual'
BEGIN SELECT RAISE(ABORT, 'crate_items: only manual crates hold items'); END;
CREATE TRIGGER crate_items_subject BEFORE INSERT ON crate_items
WHEN (NEW.song_id  IS NOT NULL AND (SELECT subject FROM crates WHERE id = NEW.crate_id) <> 'song')
  OR (NEW.track_id IS NOT NULL AND (SELECT subject FROM crates WHERE id = NEW.crate_id) <> 'take')
BEGIN SELECT RAISE(ABORT, 'crate_items: item does not match the crate subject'); END;

-- ---- Tapes -------------------------------------------------------------------------------------
CREATE TABLE tapes (
  id              TEXT PRIMARY KEY,                           -- UUIDv7
  record_id       TEXT NOT NULL REFERENCES records(id),
  record_revision INTEGER NOT NULL CHECK (record_revision >= 1),
  key             TEXT NOT NULL CHECK (length(key) = 64),     -- input address (C128.5)
  state           TEXT NOT NULL CHECK (state IN ('queued','running','ready','failed','expired')),
  params_json     TEXT NOT NULL CHECK (json_valid(params_json)),     -- {target_lufs|null, include_liner, muse_names}
  plan_json       TEXT NOT NULL CHECK (json_valid(plan_json)),       -- items, gains, flags; chapters added when ready (C128.4)
  contains_lossy  INTEGER NOT NULL CHECK (contains_lossy IN (0,1)),
  lossy_sources   INTEGER NOT NULL CHECK (lossy_sources >= 0),
  total_samples   INTEGER CHECK (total_samples IS NULL OR total_samples > 0),
  bytes           INTEGER CHECK (bytes IS NULL OR bytes > 0),
  sha256          TEXT,
  path            TEXT,                                       -- derived/tapes/<aa>/<sha>.flac (DataDir-relative)
  filename        TEXT CHECK (filename IS NULL OR length(filename) BETWEEN 1 AND 120),
  progress_json   TEXT CHECK (progress_json IS NULL OR json_valid(progress_json)),
  attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),     -- recovery re-queues (C128.5); NOT in progress_json, which the runner overwrites about once a second
  error_code      TEXT,
  error_message   TEXT CHECK (error_message IS NULL OR length(error_message) <= 500),
  owner           TEXT,                                       -- runner id while running
  lease_expires_at TEXT,
  principal_id    TEXT NOT NULL,                              -- Principal.Key()
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  finished_at     TEXT,
  CHECK ((state IN ('ready','expired')) = (sha256 IS NOT NULL)),
  CHECK ((sha256 IS NULL) = (bytes IS NULL) AND (sha256 IS NULL) = (path IS NULL)
     AND (sha256 IS NULL) = (filename IS NULL) AND (sha256 IS NULL) = (total_samples IS NULL)),
  CHECK ((state = 'failed') = (error_code IS NOT NULL)),
  CHECK ((state = 'running') = (lease_expires_at IS NOT NULL AND owner IS NOT NULL)),
  CHECK (finished_at IS NULL OR state IN ('ready','failed','expired')),
  CHECK (contains_lossy = (lossy_sources > 0))
);
CREATE UNIQUE INDEX tapes_live_key ON tapes(key) WHERE state IN ('queued','running','ready');
CREATE INDEX tapes_record ON tapes(record_id, created_at);
CREATE INDEX tapes_state  ON tapes(state, created_at);
CREATE TRIGGER tapes_no_delete BEFORE DELETE ON tapes
BEGIN SELECT RAISE(ABORT, 'tapes: no physical delete'); END;
CREATE TRIGGER tapes_frozen BEFORE UPDATE OF id, record_id, record_revision, key, params_json, principal_id, created_at ON tapes
BEGIN SELECT RAISE(ABORT, 'tapes: frozen column'); END;
CREATE TRIGGER tapes_state_path BEFORE UPDATE OF state ON tapes
WHEN NOT (NEW.state = OLD.state
       OR (OLD.state = 'queued'  AND NEW.state IN ('running','failed'))
       OR (OLD.state = 'running' AND NEW.state IN ('ready','failed','queued'))
       OR (OLD.state = 'ready'   AND NEW.state = 'expired'))
BEGIN SELECT RAISE(ABORT, 'tapes: illegal state transition'); END;
CREATE TRIGGER tapes_ready_frozen BEFORE UPDATE OF sha256, bytes, path, filename, total_samples, plan_json, contains_lossy, lossy_sources ON tapes
WHEN OLD.state IN ('ready','expired')
BEGIN SELECT RAISE(ABORT, 'tapes: a finished tape is immutable'); END;
```

**C119.1 Notes on the DDL.** (1) 0010 writes no L2 row (its only data write is the `muses.ordinal` backfill, a rank): ids, covers and keys are Go-computed. (2) `PRAGMA foreign_key_check` is empty after 0010 on a
database upgraded from schema 5 through 9. (3) `records_order_consistent` runs on `UPDATE OF order_json`; the service (and the Syncer's duplicate removal) therefore inserts/soft-removes slot rows
**before** updating the order inside one transaction. (4) `record_slots_synced_monotonic` allows a decrease only when `song_id` changed in the same
statement (a followed Song restarts its cursor, D-L2-8). (5) `record_changes` has no foreign key to the *take's Song*: take membership is a `Verify` invariant
(R5), because a Song may follow another. (6) `crate_items` carries no trigger on retired Songs: a retired Song is resolved through `retired_into` at read time
(D-L2-12). (7) No table here is a cache: nothing is added to the `TestNoDeleteSQL` allowlist. (8) `tapes.plan_json` is replaced exactly once, on the
`running → ready` update, with the chapter offsets added (C128.4); after that the row is immutable. (9) The `LIKE 'slot\_%' ESCAPE '\'` pattern keeps `_` literal. (10) **Transaction mode:** every L2 write is one transaction on the writer pool (`BEGIN IMMEDIATE` through `_txlock=immediate`, M0 C3), never a read followed by a lock upgrade on the reader pool; `catchUpTx`, `syncSlotTx` and `EditSlots` read Pick state inside it, so `record_changes_chain` and `UNIQUE(record_id, seq)` never see a snapshot conflict. A cross-process `SQLITE_BUSY` (serve vs the CLI) waits `busy_timeout(5000)` and is then retried `SyncRetries = 5` times (20 ms × 2ⁿ) before the pass reports an error. (11) `tapes.attempts` is the one mutable counter and is not in `tapes_frozen`.

**C119.2 Write rules.** Only the owners below write each table (`TestSQL_WriteOwners_L2` greps non-test Go for `INSERT INTO|UPDATE|DELETE FROM <table>` outside the owner
packages and, for `record_changes`, checks the kind partition below, including `slot_removed` = `Service` except reason `duplicate` = Syncer):

| Table / column group | Owner (writes) | Other writers |
|---|---|---|
| `records` (all columns; `order_json` is also written by the Syncer for a duplicate removal, next row), `record_slots` structural + edit columns (`side`, `pinned_*`, `removed_*`), `record_changes` kinds `created`…`unpublished`, `slot_added`, `slot_removed`, `slot_moved`, `slot_side`, `slot_pinned`, `slot_unpinned`, `proposed`, `proposal_dismissed` | `records.Service` (ops) | none |
| `record_slots.synced_seq`, `record_slots.song_id` (follow), `record_slots.removed_*` and `records.order_json` **only for a `duplicate` removal**, `record_changes` kinds `slot_upgraded`, `slot_filled`, `slot_emptied`, `slot_song_followed` and `slot_removed` with reason `duplicate` | `records.Syncer` and `records.catchUpTx` (the same code, called inside `Service.EditSlots`) | none |
| `crates`, `crate_items` | `crates.Service` | none |
| `tapes` | `tape.Service` (insert, reuse, claim, progress, finish, expire) | none |
| `settings` keys `curation.*` | `curation.Service` | none |
| `muses.ordinal` | the database only: the 0010 backfill and the `muses_ordinal_assign` trigger; no Go code writes it | none |

### C120. Records (`internal/records`)

```go
type Status string // "draft" | "proposed" | "published"
type Record struct {
	ID, Title, Subtitle, LinerNote string
	CoverSeed int64; CoverRerolls int
	Status Status
	ProposalNote *string; ProposedBy, ProposedAt, PublishedBy, PublishedAt, FirstPublishedAt *string
	Revision int; CreatedBy, CreatedAt, UpdatedBy, UpdatedAt string; HiddenAt *string
}
type NewRecord struct{ Title, Subtitle, LinerNote string; SongIDs []string }       // SongIDs → floating slots, in order
type Patch struct{ Revision int; Title, Subtitle, LinerNote *string; CoverReroll bool; Hidden *bool }
type ListFilter struct{ Statuses []Status; Q string; SongID string; IncludeHidden bool; Limit int; Cursor string }
type Summary struct {                     // list rows
	ID, Title, Subtitle string; Status Status; Slots, Playable int; DurationS float64; Revision int
	UpdatedAt string; Hidden bool; Warnings int          // covers are built by recordview (CoverInputs), never by this package
}
type Page struct{ Items []Summary; NextCursor *string; Total int }

func CoverSeed(recordID string, rerolls int) int64     // first 53 bits of sha256("mm-cover-seed/1|"+id+"|"+rerolls)
func New(st *store.Store, now func() time.Time, newID func() string) *Service
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewRecord) (Record, error)
func (s *Service) Get(ctx context.Context, id string) (Record, error)                      // the row; views are recordview's
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Record, error)
func (s *Service) EditSlots(ctx context.Context, p auth.Principal, id string, revision int, ops []SlotOp) (EditResult, error)   // C121
func (s *Service) Propose(ctx context.Context, p auth.Principal, id string, revision int, rationale string) (Record, error)
func (s *Service) Publish(ctx context.Context, p auth.Principal, id string, revision int, published bool) (Record, error)       // p.Kind == KindHuman
func (s *Service) Slots(ctx context.Context, id string) ([]Slot, error)                    // live slots in order
func (s *Service) Resolved(ctx context.Context, id string) ([]ResolvedSlot, error)         // C121.2
func (s *Service) Changes(ctx context.Context, f ChangeFilter) (ChangePage, error)         // C122.4
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error)                           // C122.5
```

**C120.1 Rules.**
- `Create`: any `write`-scope principal; `title` 1..120 after trim/whitespace-collapse, control characters refused (the playlist rules), `subtitle` ≤ 200, `liner_note` ≤ 4000; `status draft`; `cover_seed = CoverSeed(id, 0)`; optional `song_ids` (0..60, distinct, every Song live and not retired: 422 `UNKNOWN_SONG` with `details.song_id`, 409 `SONG_RETIRED`); the slots are floating and get `slot_added` rows; first row `created`. One transaction.
- **Principal rules by status** (`TestRecords_PrincipalRules` is the full table):

| Operation | `draft` / `proposed` | `published` |
|---|---|---|
| `Update` (text, `cover_reroll`, `hidden`), `EditSlots` | any `write`-scope principal | `ActsAsHuman()` only, else 403 `HUMAN_ONLY` (hint "published records are edited by a human") |
| `Propose` | any `write`-scope principal; `draft` only (else 409 `INVALID_RECORD_STATE`); needs ≥ 1 live slot (422 `RECORD_EMPTY`); writes `proposal_note`, `proposed_by/at`, row `proposed` | 409 `INVALID_RECORD_STATE` |
| `Publish(true)` | `KindHuman` only (403 `HUMAN_ONLY` for every other principal, including devices, before any read); from `draft`|`proposed`; needs ≥ 1 *playable* slot (422 `RECORD_EMPTY`); sets `published_by/at`, `first_published_at` once; row `published` | already published ⇒ 200 unchanged |
| `Publish(false)` | `KindHuman` only; `draft` ⇒ 200 unchanged (no row, no revision bump); `proposed` ⇒ `draft` (row `proposal_dismissed`, clears `proposed_*`/`proposal_note`) | `published` ⇒ `draft` (row `unpublished`; `proposed_*` cleared) |

- **`Publish` check order** (either direction): (1) principal is `KindHuman` (403 `HUMAN_ONLY`, before any read); (2) the Record exists (404); (3) `revision` equals the current one (409 `STALE_REVISION`, `details` carry the current revision and `status`) — this applies **even to an already-published Record**, so a human who saw an older revision reloads before anything is confirmed; a lost-response retry therefore sees 409 with `status published` and the publish page treats that as success; (4) the no-op/legality rules of the table; (5) content rules (≥ 1 playable slot, 422 `RECORD_EMPTY`).
- **Edits of a `proposed` Record:** an `Update` or `EditSlots` by a non-human principal also moves it to `draft`, clears `proposed_*`/`proposal_note` and writes `proposal_dismissed` with `detail {"reason":"edited"}` (in the same transaction as the edit); a human's edit keeps the proposal; Syncer rows never dismiss.
- **Return shape:** `Create`/`Update`/`EditSlots`/`Propose`/`Publish` return the `Record` row only; the ops wrap it in a **liner-less** `RecordView` (C124.2).
- Every mutating call checks `revision` (409 `STALE_REVISION` with the current revision, as playlists) and bumps `revision`, `updated_at`, `updated_by`; **Syncer rows never bump `revision`** (they are not edits). An empty `Patch` ⇒ 422. `CoverReroll` increments `cover_rerolls`, recomputes `cover_seed`, row `cover_rerolled`.
- Text fields are operator-visible content (D-L2-27): the same control-character and length rules everywhere; nothing is HTML-interpreted anywhere (SPA renders text nodes).
- `List`: order `(updated_at DESC, id DESC)` keyset; `Q` is a case-insensitive substring on `title` (`LIKE` with escaping); `SongID` = Records with a live slot of that Song; hidden excluded by default; `Summary.Playable` = slots with a resolved Take; page cap 90 000 B.
- No Muse text anywhere in this package (it cannot import `muses`); error messages carry ids and field names only.

### C121. Slots: resolution and edits (`internal/records`)

```go
type Slot struct {
	ID, RecordID, SongID string; Position int; Side *string
	PinnedTakeID *string; PinnedBy, PinnedAt *string; SyncedSeq int; AddedBy, AddedAt string
}
type SlotMode string  // "floating" | "pinned"
type SlotState string // "ok" | "empty" | "pin_ineligible"
type TakeInfo struct{ ID, Status string; DurationS float64; Lossy bool }
type PickInput struct{ TakeID *string; Source string }   // the Song's song_picks row; nil take ⇒ none
type ResolvedSlot struct {
	Slot Slot; Take *TakeInfo; Mode SlotMode; State SlotState
	Pick PickInput; PinDiffers bool            // pinned take differs from the Song's current Pick
	SongRetired bool; Warnings []string        // "pin_ineligible" | "empty" | "retired_song" | "undecided_pick"
}
func ResolveSlot(s Slot, pinned *TakeInfo, pick PickInput, pickTake *TakeInfo) ResolvedSlot   // pure

type SlotOp struct {
	Op string                      // add | remove | move | reorder | pin | unpin | set_side | pin_all | unpin_all
	SongID, SlotID, TakeID string  // add: SongID, optional TakeID (pinned add) ; remove/move/pin/unpin/set_side: SlotID ; pin: TakeID
	Position int                   // add (optional, default end) / move
	Side OptString                 // set_side / add: unset | null | "A".."D"
	SlotIDs []string               // reorder: the complete new order
}
type EditResult struct{ Record Record; Changes []Change; Warnings []string }
```
`OptString` (package `records`; the Huma input type is `api.OptSide`, a nullable enum `["A","B","C","D"]`, not required) is `struct{ Set, Null bool; Value string }` with `UnmarshalJSON`: key **absent** ⇒ `Set=false`; JSON `null` ⇒ `Set=true, Null=true`; `"A"`–`"D"` ⇒ `Set=true, Value`; any other value ⇒ 422 `VALIDATION_FAILED` (field `side`). `add`: absent or null ⇒ no side. `set_side`: absent ⇒ 422 (`side` is required there), null clears the side, a letter sets it (`TestOptSide_Decode`).

**C121.1 `ResolveSlot`** (pure; D-L2-6): pinned slot ⇒ `Take = pinned`, `Mode pinned`, `State ok`, or `pin_ineligible` when the pinned Take is absent or `status = 'discarded'` (the slot **keeps** that Take; warning `pin_ineligible`); `PinDiffers = pick.take ≠ pinned`. Floating ⇒ `Take = pickTake`, `Mode floating`, `State ok`, or `empty` when the Pick is none; a floating slot whose Pick `Source` is `newest` or `stars` adds warning `undecided_pick` (D-L2-30; recordview and `get_stats` surface it for **published** Records only). A retired Song adds warning `retired_song` (the Syncer follows it, D-L2-8). Table `TestResolveSlot_Table` (C122 acceptance): R01 floating/no pick ⇒ empty; R02 floating/pick A (`newest`) ⇒ A ok; R03 floating/pick A (`pinned` source at Song level) ⇒ A ok, mode floating; R04 pinned P (draft)/pick A ⇒ P ok, differs; R05 pinned P (discarded)/pick A ⇒ P `pin_ineligible`, differs; R06 pinned P absent from the take table ⇒ P `pin_ineligible`; R07 pinned = pick ⇒ ok, not differs; R08 pinned P (discarded)/pick none ⇒ P `pin_ineligible`; R09 floating on a retired Song ⇒ warning `retired_song`; R10 lossy Take ⇒ `TakeInfo.Lossy` carried; R11 floating/pick whose Take is `to_relisten` ⇒ ok; R12 pinned P `final` ⇒ ok; R13 floating/pick (`stars`) ⇒ ok + `undecided_pick`; R14 floating/pick (`ladder`) ⇒ ok, no warning.

**C121.2 `Resolved`** loads, in three statements (no N+1): the live slots in `order_json` order, the Songs' `song_picks` rows (`LEFT JOIN`), and the `tracks` rows of every pinned/Pick Take (`status`, `duration_s`, `lossy`).

**C121.3 `EditSlots`** — one write transaction (`BEGIN IMMEDIATE`, C119.1 note 10):
1. Load the Record (404 `NOT_FOUND`), apply the principal rule (C120.1), compare `revision` (409 `STALE_REVISION`).
2. **Catch up:** `catchUpTx(tx, id)` runs the Syncer's per-slot step for this Record's floating slots (C122.2), so that every pending Pick event is logged *before* the edit is (history is never reordered around an edit).
3. Load the live slots into an ordered working list; apply `ops` in order (≤ `MaxEditOps = 50`, empty ⇒ 422), each against the working list **as left by the previous op**:
   - `add {song_id, position?, take_id?, side?}`: the Song must exist (422 `UNKNOWN_SONG`), be non-retired (409 `SONG_RETIRED` with `details.retired_into`) and not live in this Record (409 `SONG_ALREADY_IN_RECORD`); `position` 1..n+1 (default n+1; else 422 `INVALID_POSITION`); with `take_id` the slot is created **pinned** (the Take must be a Take of the Song, 422 `TAKE_NOT_IN_SONG`, and not discarded, 422 `TAKE_NOT_ELIGIBLE`), else floating with `synced_seq` = the Song's current max `pick_events.seq` (0 when none). Row `slot_added` (`to_take_id` = the resolved Take, possibly NULL).
   - `remove {slot_id}`: soft-removes the slot (`removed_by/at`), row `slot_removed` (`from_take_id` = the resolved Take). A removed Song may be added again later (a new slot, a new identity).
   - `move {slot_id, position}` / `reorder {slot_ids}`: `reorder` must be an exact permutation of the live slots (422 `SLOT_ORDER_MISMATCH` with `details.missing`/`extra` ids); one `slot_moved` row per slot whose position changed (`position` = new, `detail_json {"from": n}`); no change ⇒ no row.
   - `pin {slot_id, take_id}`: the Take must belong to the slot's Song (422 `TAKE_NOT_IN_SONG`) and not be discarded (422 `TAKE_NOT_ELIGIBLE`, `details.status`); sets `pinned_*`; row `slot_pinned` (`from` = the previously resolved Take, `to` = the pin, reason `pin`); pinning the Take already pinned is a no-op without a row.
   - `unpin {slot_id}`: no pin ⇒ no-op; else clears `pinned_*`, sets `synced_seq` = the Song's current max `seq`, row `slot_unpinned` (`from` = the pin, `to` = the Pick Take or NULL, reason `unpin`).
   - `set_side {slot_id, side}`: `A`–`D` or null; row `slot_side` when changed.
   - `pin_all`: every floating slot whose resolved Take exists is pinned (a `slot_pinned` row each; slots with no Take are skipped and reported in `warnings`); `unpin_all`: the reverse (D-L2-30).
   Unknown slot id (or a removed one) ⇒ 422 `UNKNOWN_SLOT` with `details.slot_id`; every error carries the op index (`details.op`) and rolls the whole call back.
4. Live slot count ≤ `MaxSlots` (else 422 `TOO_MANY_SLOTS`); write `order_json` (the trigger verifies it), bump `revision`/`updated_*`; when a non-human edited a `proposed` Record also move it to `draft` with a `proposal_dismissed` row (C120.1); commit; the call returns the new `Record`, the written `Change` rows and warnings.

**C121.4 Timing guarantee.** `EditSlots` reads the Pick state **inside its transaction** (`song_picks`, `pick_events`), so what the caller sees equals what the changelog records; a Pick that flips a millisecond later is logged by the Syncer after the edit.

### C122. Changelog, the Syncer, Verify (`internal/records`)

```go
type Change struct {
	ID string; Seq int; At, Kind string; SlotID *string; Position *int
	FromTakeID, ToTakeID *string; Reason *string; PickEventID *string
	Evidence, Detail json.RawMessage; Actor, ActorKind, ActorName string
}
type ChangeFilter struct{ RecordID string; Order string /* "desc" (default) | "asc" */; Cursor int; Kinds []string; Limit int }
type ChangePage struct{ Items []Change; NextCursor *int }
func ChangeText(c Change, names func(takeID string) string) string     // C122.3

type Syncer struct{ /* svc *Service; log *slog.Logger; now */ }
func NewSyncer(s *Service, log *slog.Logger) *Syncer
func (y *Syncer) Settle(ctx context.Context) (SyncReport, error)      // drains until no slot has unconsumed events; safe next to any other Settle
func (y *Syncer) Run(ctx context.Context)                             // loop: Settle every SyncPoll when Pending > 0
func (y *Syncer) Pending(ctx context.Context) (int, error)            // slots with events beyond synced_seq (one indexed query)
type SyncReport struct{ Slots, Rows, Skipped, Followed, Removed int }
type Anomaly struct{ Code, Severity, Subject, Detail string }          // Severity "error" | "warn"
```

**C122.1 What the Syncer does.** Pending work = every **live floating** slot (`removed_at IS NULL AND pinned_take_id IS NULL`) of a Record (hidden Records are synced too) with a `pick_events` row of its Song whose `seq > slot.synced_seq`; the query is `record_slots_song` ⋈ `pick_events_song(song_id, seq)` and costs one index probe per slot. Passes process ≤ `SyncBatch` slots, one transaction per slot (so a failure never blocks the others), using the per-slot step of C122.2.

**C122.2 The per-slot step (`syncSlotTx`; also `catchUpTx` for one Record).** Inside a write transaction: re-read the slot (skip if pinned/removed meanwhile); if its Song is **retired**, follow it (C122.2.1) and stop; else read the Song's events with `seq > synced_seq` in `seq` order; for each event `e`:
- `e.to_take_id IS NULL` ⇒ row `slot_emptied` (`from = e.from_take_id`, reason `emptied`);
- `e.from_take_id IS NULL` ⇒ row `slot_filled` (`to = e.to_take_id`, reason = `e.reason`);
- `e.from_take_id ≠ e.to_take_id` ⇒ row `slot_upgraded` (reason = `e.reason`: `ladder`, `stars`, `newest`, `pin`, `unpin`, `pin_ineligible`, `pin_restored`, `initial`);
- same Take, different source ⇒ no row (the Take the slot plays did not change);
each row carries `pick_event_id = e.id`, `evidence_json = e.evidence_json`, `position` = the slot's current position, actor `('system:records-sync', 'system', 'records-sync')`, and is inserted with `INSERT … ON CONFLICT(slot_id, pick_event_id) DO NOTHING` (so a concurrent Syncer or a re-run cannot duplicate). Rows whose event `reason` is `newest` or `stars` carry `detail_json {"undecided": true}` (D-L2-30). Then `UPDATE record_slots SET synced_seq = max(synced_seq, <last seq>)`. A slot of a **pinned** state is never processed; its `synced_seq` is refreshed only by `unpin`.
**C122.2.1 Follow.** Song `S` is retired into `T` (L1): if `T` is live in the same Record ⇒ soft-remove the slot (row `slot_removed`, reason `duplicate`, actor system) **and, in the same transaction, rewrite `records.order_json` without that slot** (the `records_order_consistent` trigger verifies it; `revision` and `updated_at` are **not** touched — the Syncer makes no edit). A client holding the old order sees `SLOT_ORDER_MISMATCH` (`details.extra` = the removed slot id) on its next `reorder`, and `UNKNOWN_SLOT` for an op naming the removed slot; both tell it to reload, and the studio's `pending` refetch normally reloads first; else `UPDATE record_slots SET song_id = T, synced_seq = <T's max seq>` (the trigger allows exactly this), row `slot_song_followed` (`from` = the resolved Take before, `to` = the Pick Take of `T` or NULL, reason `retired`). Followed slots never lose their pin (a retired Song has no Take, so a pinned slot cannot be on one).

**C122.3 `ChangeText`** (pure, deterministic; golden table `testdata/change_text.golden.json`, 24 rows; the web mirror `recordView.changeText` is tested against the same file). `{n}` = the change's `position`; titles are inserted by the caller's `names` lookup at read time (never stored): `created` "Record created"; `renamed` "Title changed"; `retexted` "Notes changed"; `cover_rerolled` "Cover re-rolled"; `hidden`/`unhidden`; `proposed` "Proposed for publishing"; `proposal_dismissed` "Sent back to draft"; `published` "Published"; `unpublished` "Unpublished"; `slot_added` "Slot {n} added: {to}" (or "… (no take yet)"); `slot_removed` "Slot {n} removed ({from})"; `slot_moved` "Slot moved {from} → {n}"; `slot_side` "Slot {n} side {side}"; `slot_pinned` "Slot {n} pinned to {to}"; `slot_unpinned` "Slot {n} unpinned: follows the Pick ({to})"; `slot_filled` "Slot {n} filled: {to} ({why})"; `slot_emptied` "Slot {n} emptied: no playable take (was {from})"; `slot_song_followed` "Slot {n} follows the merged song"; `slot_upgraded` **"Slot {n} upgraded: {from} → {to} ({why})"**. `{why}` is `records.WhyText(source, evidence)` — the **single why-vocabulary** shared by `ChangeText` (source = the event's reason), `SlotView.why` and `liner.Track.Why` (source = the Song's **current** `song_picks.source` with its `evidence_json`, never the changelog: a slot that has never changed has no change row; `pinned` reads as `pin`, `none` as an empty string; golden `testdata/why_text.golden.json`, 8 rows: ladder 1 duel, ladder n duels, pinned, stars untied, stars tied 3, newest, none, ladder with missing evidence): `ladder` "won {duels_n} blind duel(s)" (singular for 1); `stars` "{stars}★" plus " (newest of {tied} tied)" when `tied > 1`, always followed by " · undecided"; `newest` "newest take · undecided"; `pin` "pinned as the song's Pick"; `unpin` "pin removed"; `pin_ineligible` "pinned take discarded, fell back"; `pin_restored` "pinned take restored"; `initial` "first take"; `retired`/`duplicate` as the kind says. A row whose evidence lacks a field renders the generic form without it (never an error). No Muse text can reach it (ids and numbers only).

**C122.4 `Changes`** — `ChangeFilter.Cursor` is the `seq` of the last item of the previous page (exclusive; direction follows `Order`); `Limit` 1..200 (default 50); `Kinds` filters; page cap 90 000 B; `NextCursor` when more remain. `Change` JSON adds, in the op (not here), `from`/`to` `{id, title}` and `text`.

**C122.5 `Verify`** (read-only SQL; `library verify` prints and exits 1 on any `error`; `warn` is printed only):
- **R1** (error) `order_json` lists exactly the live slots, each once (also guarded by the trigger, and after a Syncer duplicate removal); no live slot is in a Record twice for one Song;
- **R2** (error) a live slot's Song exists; (warn `retired_song`) a live slot on a retired Song once the Syncer has nothing pending;
- **R3** (error) `pinned_take_id` belongs to the slot's Song (`take_songs`);
- **R4** (error) per Record `record_changes.seq` is `1..n` and the first row is `created`;
- **R5** (error) per live slot, folding its take-bearing rows (`slot_added`, `slot_pinned`, `slot_unpinned`, `slot_upgraded`, `slot_filled`, `slot_emptied`, `slot_song_followed`) in `seq` order: each `from_take_id` equals the previous `to_take_id` (the first row is `slot_added`), and — when the slot has no unconsumed events — the last `to_take_id` equals the slot's **current resolved Take** (the replay of the changelog reproduces the state; this is also the property-test oracle, C132);
- **R6** (error) `cover_seed = CoverSeed(id, cover_rerolls)`; `first_published_at` set whenever a Record was ever published; (warn `published_empty`) a published Record with no playable slot;
- **R7** (error) manual Crate items match the Crate's subject and are unique per live member; (warn `crate_filter_invalid`) a smart filter no longer valid under the current vocabulary;
- **R8** (error) at most one live `tapes` row per `key`; no `running` tape whose lease expired more than `LeaseTTL` ago once the runner recovered; (error, only with `library verify --files`) every `ready` tape's file exists with the recorded size;
- **R9** (error) `settings` keys under `curation.` are exactly the known ones with `true`/`false` values.

`Verify` is also the oracle of `TestProperty_RecordInvariants` and of `TestAcceptance_L2_Mirror`.

**C122.6 Pending.** `RecordView.pending` = (some floating slot has `pick_events.seq > synced_seq`) OR (`provenance_dirty` holds a `track` row for a Take of a slot Song — the L1 flag): a reader knows the changelog may lag by ≤ one pass; the *resolved Takes themselves are never stale* (D-L2-6).

---

### C123. Cover art — the mosaic extension of `internal/media/cover` (amends M4a C110.1)

**What exists (M4a, unchanged by L2):** `Version = "mm-cover/1"`, `ClampSize`, `Key(rootID, seed, size)`, `Render(rootID, seed, size)` (rings and bars, floating point, golden
`testdata/cover.golden.json`), the integer palette (`hsl`, `makePalette(hue)`, `blend`, `nearest`, `pixelIndex`), `splitmix64`, `Service{data, lib}` with `EnsureTrack`,
`EnsureAlbum(rootID)`, `Resolve(key, "png")`, `Sweep`, files under `derived/covers/<aa>/<key>.png`, served as signed `/media/<key>.png`. **L2 adds, in new files, only:**

```go
const MosaicVersion = "mm-cover/record-1"
type Part struct{ RootID string; Seed int64 }                 // a Song's lineage: its primary root id and that root's recipe seed
type Input struct{ Kind string /* "record" | "album" */; ID string; Seed int64; Parts []Part }
func RenderMosaic(in Input, size int) *image.Paletted         // pure, deterministic, integer-only (mosaic.go)
func MosaicKey(in Input, size int) string                     // hex sha256(MosaicVersion "\n" Kind "\n" ID "\n" Seed "\n" size "\n" then for each of the first CoverParts parts: RootID 0x1F Seed "\n")
func (s *Service) EnsureMosaic(ctx context.Context, in Input, size int) (serve.Object, string, error)   // renders on a cache miss; returns the key; size = ClampSize(size)
```
`Service.ensure` is split so that the write path (work dir `incoming/cover-<uuid>/`, chmod 0444, mtime stamp, `Root.Link` claim, EEXIST ⇒ identical content already there ⇒ reuse) is shared by
`EnsureTrack`/`EnsureAlbum` and `EnsureMosaic`; M4a's `service_test.go` and `cover.golden.json` are the regression guard (the split changes no byte). `Resolve` and `Sweep` already serve and age
mosaic files (the key space is distinct from `Key`'s by the version prefix; `isKey` accepts any 64-hex name).

**Algorithm (binding, so output stays identical across implementations and Go releases):**
- `h = sha256(MosaicVersion + "|" + Kind + "|" + ID)`; a `splitmix64` stream `r` seeded with `uint64(in.Seed) ^ binary.BigEndian.Uint64(h[8:16])` (the existing generator; `math/rand` is not used).
- **Palette from the identity only:** `hue = (uint16(h[0])<<8 | uint16(h[1])) mod 360` (the same formula as `Render`), `makePalette(hue)` (background HSL(H, .35, .12); primary H+150; secondary H+210; ink; 16 entries with the 20/40/60/80 % blends). A Record keeps its palette through every re-roll and every Pick change.
- **Layout from the seed and the lineage:** `n = min(len(Parts), CoverParts)`; `n = 0` ⇒ `n = 4` filler tiles driven by `r`. Grid side `g = 2` (n ≤ 4), `3` (n ≤ 9), else `4`; cell edge `size / g`; tiles row-major from the top-left; cells beyond `n` are background with one ink dot (radius 0.04 cell) at the centre. A tile with part `p` draws from its own stream `tr` seeded `binary.BigEndian.Uint64(sha256(p.RootID)[0:8]) ^ uint64(p.Seed)` (fillers use `r`): `k = 2 + tr%3` concentric rings centred at the cell centre shifted by `(u₁ − 0.5)·0.2` and `(u₂ − 0.5)·0.2` of a cell, outer radius `(0.30 + 0.18·u₃)` cell, ring width `(0.06 + 0.08·u₄)` cell, colours alternating primary/secondary (primary first when `tr&1 == 0`), then an ink dot of radius `(0.05 + 0.04·u₅)` cell at the ring centre; a 4 % gap separates cells. Each pixel takes **2×2 samples** (as `Render` does); the four counts per base slot go through the existing `pixelIndex` (uniform pixel keeps its base colour, a mixed one takes the nearest blend); no dithering. No text is ever drawn.
- **Integer-only geometry (binding):** every fraction above is a Q16 fixed-point constant (`round(x·65536)`); `u₁…u₅` are `tr.next() >> 48` (0..65 535); distances are compared as squared Q16 values in `int64`. `mosaic.go` contains no `float32`/`float64`/`math.` identifier (`TestCover_NoFloatInRenderer` greps `mosaic*.go`), so FMA contraction on arm64 cannot move a pixel and one golden file serves amd64 and arm64. (`unit()` of the existing PRNG is a float helper and is **not** used by the mosaic.)
- Encoding `png.Encoder{CompressionLevel: png.BestCompression}`, the existing cache and serving path.
- **Who builds the input:** `recordview` for a Record (`Kind record`, `ID` = Record id, `Seed` = `cover_seed`, `Parts` = the first 12 slot Songs' `(primary root id, that root's recipe seed)` read in one batch through the existing `library.TrackRefs`; a takes-less draft Song contributes `(draft id, 0)`); the A2 catalog for auto-albums (`Kind album`, `ID` = album id, `Seed` 0, `Parts` = the album's first 12 Songs the same way). Because a Part is a Song's *lineage*, not its current Take, **a Pick change never changes a cover**; adding/removing/reordering slots or `cover_reroll` does.

Tests (new, in `internal/media/cover`): `TestCover_Mosaic_DeterministicPixels` (two renders equal; sha256 of the raw pixel indices + palette equals `testdata/cover_mosaic.golden.json` for 4 inputs — 0, 3, 9 and 12 parts; `-update`), `TestCover_RerollChangesLayoutNotPalette`, `TestCover_PartsOrderMatters`, `TestCover_RecordsDiffer`, `TestCover_SizeBudget` (10 fixed inputs: 1024 px ≤ 96 KiB, 512 px ≤ 40 KiB), `TestCover_MosaicKeyTable`, `TestCover_EnsureMosaic_Idempotent` (two concurrent calls ⇒ one file, same key), `TestCover_MosaicResolveSigned` (a signed `/media/<key>.png` link resolves; GET never writes the DB), `TestCover_MosaicSweep` (existing 30-day/24 h rule applies), `TestCover_NoFloatInRenderer`, `TestCover_M4aGoldenUnchanged` (the M4a golden test still passes untouched), `TestPerf_CoverRender`; in `recordview`: `TestCover_PickChangeKeepsKey`. The types (`Input`, `Part`, `MosaicVersion`) are **F2**; the renderer, key and `EnsureMosaic` are **V1**.


### C124. Liner notes and the energy curve (`internal/liner`, `internal/recordview`)

```go
type Audience string // "app" | "export"
type Input struct {
	Audience Audience; Title, Subtitle, Note, Date string        // Date = the caller's YYYY-MM-DD (no time.Now inside)
	MuseNames bool                                               // honoured for "export" only (D-L2-16)
	Tracks []Track
}
type Track struct {
	Position int; Side, Title, Angle, Engine string; DurationS float64; BPM *int; BPMBasis string   // "measured"|"declared"|"plan"|""
	Families []string; Why, Mode string                          // Why = records.WhyText of the Song's current song_picks row (C122.3; no titles); Mode floating|pinned
	Lossy, Resampled, Upmixed bool; SessionLabel string          // export: "<kind> <date>" built by the caller, never a name
	CostMicroUSD *int64; SessionName string; Muse *MuseLine      // private: see Build
}
type MuseLine struct{ Title string; Artist *string }
type Liner struct{ Title string; Summary Summary; Lines []Line; Notices []string }
func Build(in Input) Liner                       // pure
func (l Liner) Markdown() string                 // ≤ 8 KB; the track list is cut at a whole line with a notice
func (l Liner) Description() string              // ≤ 1 000 chars: the tape DESCRIPTION (summary + notices)
```
**C124.1 Build rules.** `Summary` = songs, playable tracks, total duration (`m:ss` / `h:mm:ss`), distinct sorted engines, distinct sorted angles, distinct sorted Sound families (top 3), `contains_lossy`, `lossy_n`. Notices: "Contains lossy sources (n of m tracks)." when `lossy_n > 0`; "n track(s) resampled to 48 kHz" / "n track(s) mono, duplicated to stereo" when applicable; "n slot(s) have no playable take yet" (app only). A line is `{position}. {title} — {angle} · {engine} · {m:ss} · {bpm} BPM · {families} · {why}` omitting empty parts; a pinned slot's why starts "pinned · ". **Private fields are dropped by construction for `export`**: `Build` ignores `CostMicroUSD`, `SessionName` and (unless `MuseNames`) `Muse` even when the caller supplied them (`TestLiner_ExportIgnoresPrivateFields` with canary inputs); `app` renders cost per track and a total (integer micro-USD formatted without floats), the session name, and `Muse` as "inspired by {title}[ — {artist}]". Same input ⇒ same bytes; goldens `testdata/liner_app.golden.md`, `liner_export.golden.md`, `liner_export_muses.golden.md`.

**C124.2 `recordview.Service`** (read assembly; the only L2 package that reads Muse text, through the batch `muses.Service.Refs(ctx, ids)` — one statement for every slot Song — never SQL). **Batch readers** (additive files in L1/M2 packages, signatures frozen by F2 as `recordview.Options` interfaces, implemented by V2; each is **one statement however many ids**): `muses.Service.Refs` (already exists), `sessions.Service.Refs(ctx, ids)`, `sounds.Service.FamiliesOf(ctx, songIDs)`, `songs.Service.Briefs(ctx, songIDs)`, `picks.Service.Current(ctx, songIDs)` (the `song_picks` rows with evidence), `ears.Loudness(ctx, takeIDs)` (`TestBatchReaders_OneStatement`).
```go
type GetOptions struct{ Include []string /* liner, energy, tapes, changes, preview; empty ⇒ none of them */; ChangesLimit int }
func New(o Options) *Service      // Options{Store, Records, Muses, Songs, Sessions, Sounds, Picks, Curation, Library, Covers CoverSource, Signer, Tapes TapeBriefs, Now}
func (s *Service) Get(ctx context.Context, viewer auth.Principal, id string, o GetOptions) (RecordView, error)
func (s *Service) Snapshot(ctx context.Context, recordID string, o tape.SnapOptions) (tape.Snapshot, error)   // implements tape.Source
func (s *Service) CoverInput(ctx context.Context, recordID string) (cover.Input, error)
func (s *Service) CoverRefs(ctx context.Context, viewer auth.Principal, ids []string) (map[string]CoverRef, error)   // list rows: signed 512/1024 links, one batch
```
`RecordView{record, slots: SlotView[], totals {slots, playable, duration_s, lossy_n, warnings, undecided}, warnings, cover, liner? (get_record only), energy?, changes?, tapes?, preview?, pending}` (shapes in C129). `slots[].gain_db` = the **plan gain** (C128.2 with the auto target) so the SPA and Subsonic play matched; `null` when unmeasured. `liner` (audience `app`) is generated per request (≤ 8 KB) and **only when `include` contains `liner`, which only the `get_record` op ever passes** (the five mutating ops call `Get` with an empty `Include`, D-L2-27). `preview` (`include=preview`, the publish page's data) is the tape's chapter names, export-liner Song titles, tags and warnings exactly as `Snapshot` would build them, without writing anything. **`Snapshot`** builds the tape input: resolved Takes and masters (titles read **at snapshot time**), the slot warnings (`pin_ineligible`, `undecided_pick`), the loudness measures, the `export`-audience liner (Muse lines only when `curation.export_muse_names`), the tag set, the cover input; it reads Muse text **only** in that branch.

**C124.3 Energy curve (D-L2-11).** `energy: {target_lufs, total_duration_s, slots: [{slot_id, duration_s, gain_db|null, mean_lufs|null, bpm|null, lufs: number[] (≤ 120)}]}`: `lufs` is the take's stored `short_term_1hz` plus `gain_db`, floor −70.0, one decimal, bucket-mean downsampled when longer than 120 (bucket = ceil(len / 120)); `mean_lufs` = integrated + gain; `bpm` = the Song's effective tempo (measured, else declared, else plan; L1 C93) or null. Slots without an analysis carry `lufs: []`. Included only with `include=energy`; the payload stays < 90 000 B at 60 slots (`TestRecordView_EnergyBudget`).

### C125. Crates and smart Crates (`internal/crates`)

```go
type Kind string    // "manual" | "smart"
type Subject string // "song" | "take"
type Crate struct {
	ID, Name, Description string; Kind Kind; Subject Subject; Filter json.RawMessage
	Revision int; CreatedBy, CreatedAt, UpdatedBy, UpdatedAt string; HiddenAt *string
}
type Member struct{ SongID, TrackID string }                  // exactly one set; must match the subject
type NewCrate struct{ Name, Description string; Kind Kind; Subject Subject; Filter json.RawMessage; Items []Member }
type Patch struct{ Revision int; Name, Description *string; Filter json.RawMessage; Hidden *bool }     // Filter only for smart crates
type ItemsEdit struct{ Add, Remove []Member }
type ItemsResult struct{ Added, Already, Removed, NotMember, Count int }
type ListFilter struct{ Kind Kind; Subject Subject; Q string; IncludeHidden, Counts bool; Limit int; Cursor string }
type Page struct{ Items []Summary; NextCursor *string; Total int }
type Summary struct{ Crate; Count *int }                       // Count: manual always, smart only with Counts

func New(st *store.Store, lib Counter, now func() time.Time, newID func() string) *Service
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewCrate) (Crate, int, error)         // the count is returned (manual: items; smart: matches now)
func (s *Service) Get(ctx context.Context, id string) (Crate, int, error)
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error)
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Crate, int, error)
func (s *Service) EditItems(ctx context.Context, p auth.Principal, id string, e ItemsEdit) (ItemsResult, error)
func ParseFilter(subject Subject, raw json.RawMessage) (Filter, error)                                    // C125.2
func (f Filter) Songs() songs.ListFilter ; func (f Filter) Tracks() library.SearchParams
func (s *Service) ExpandSongs(ctx context.Context, crateID string, base songs.ListFilter) (songs.ListFilter, error)
func (s *Service) ExpandTracks(ctx context.Context, crateID string, base library.SearchParams) (library.SearchParams, error)
```
**C125.1 Rules.** `name` 1..80 (trim, whitespace-collapse, no control characters), `description` ≤ 500; any `write`-scope principal (agents included — a Crate is a working set, not a release). `kind`/`subject` immutable. Manual: `items` ≤ 200 on create, ≤ 5 000 live per Crate (409 `CRATE_FULL`), `EditItems` adds/removes ≤ 200 per call each; unknown Song/Take ⇒ 422 `UNKNOWN_SONG` / `UNKNOWN_TRACK`; a member of the wrong subject ⇒ 422 `SUBJECT_MISMATCH`; adding a retired Song ⇒ 409 `SONG_RETIRED`; adding a present member counts `already`, removing an absent one `not_member` (never an error); a removed member re-added creates a new row. Smart Crates reject items (409 `CRATE_IS_SMART`) and require `filter`. Metadata edits are revision-locked (409 `STALE_REVISION`) and bump `revision`; item edits are not (commutative) but bump `updated_at`. Empty patch ⇒ 422. `Create`/`Update` of a smart Crate return the **live match count** (one `COUNT` through the same filter structs as the list ops). No hidden-Crate deletion; hide only.

**C125.2 Filter vocabulary (`mm.crate-filter/1`, D-L2-13).** The filter is a JSON object whose keys are exactly the **query parameters of the matching list op**, minus the *non-saved* set `{limit, cursor, sort, include_hidden, include_archived, draft_id, track_id, song_id, root_track_id, crate_id}`; unknown key ⇒ 422 `UNKNOWN_FILTER_KEY` (`details.key`), a wrongly typed value or an out-of-range value ⇒ 422 `INVALID_FILTER` (field errors), > 4 096 bytes ⇒ 422 `FILTER_TOO_LARGE`. Values are JSON scalars/arrays of the parameter's type.

| Subject | Keys (all optional; AND across keys, OR inside `state`/`sound` per L1 rules) |
|---|---|
| `song` (= `list_songs`) | `muse_id`, `no_muse`, `angle`, `state[]`, `queued`, `session_id`, `sound[]` (`facet:value`), `tempo_mismatch`, `has_pick` (unchanged), `has_open_proposals`, `q`, **new:** `pick_decided`, `unheard`, `min_duration_s`, `max_duration_s`, `min_stars`, `unrated`, `tag[]`, `lossy`, `recipe_known`, `in_record` |
| `take` (= `search_tracks`) | `q`, `status[]`, `style[]`, `min_duration_s`, `max_duration_s`, `recorded_from`, `recorded_to`, `has_score`, `truncated`, `min_stars`, `unrated`, `tag[]`, `has_open_comments`, `recipe_known`, `lossy`, `origin[]`, `is_pick`, `session_id`, **new:** `unheard`, `in_record`, `sound[]` |

`TestCrateVocabulary_EqualsListOps` is the drift guard: the key sets above are derived by reflection from `searchTracksIn` / `listSongsIn` (minus the non-saved set) and compared with `ParseFilter`'s accepted keys; for each key, a fixture query through the op and through the stored filter returns the same ids.

**C125.3 The L2 predicates (added to `songs.ListFilter`, `library.SearchParams` and the two ops; SQL in the owner packages):**
- **Subject take of a Song** = its Pick take, else its newest eligible take (D-L1-14's representative subject without the draft fallback); a Song with no take never matches a duration, star or tag predicate.
- `unheard` (bool): `true` ⇒ **no** human/device `listen_events` row of kind `start` for the Take (Song: for any of its Takes) — the M2a `Stats` definition of a play (`principal_kind IN ('human','device')`); `false` ⇒ at least one. Index: `listen_events_track`.
- `min_duration_s` / `max_duration_s`, `min_stars`, `unrated`, `tag`, `lossy`, `recipe_known` for Songs: the M1/M2a take predicates evaluated on the subject take (same SQL fragments as `library.SearchParams`).
- `in_record` (bool): Song — a live slot in any non-hidden Record of any status; Take — the Take is the **resolved Take** of a live slot (`COALESCE(pinned_take_id, song_picks.take_id)`). `sound[]` for Takes filters `take_sounds` (L1).
- `crate_id`: manual ⇒ live membership (`crate_items`; for Song crates retired Songs match through `retired_into`); smart ⇒ `Expand` (below). A crate of the other subject ⇒ 422 `SUBJECT_MISMATCH`; a hidden Crate is usable.
- **`pick_decided` (D-L2-13, D-L2-36):** `pick_decided = true` ⇔ `song_picks.source IN ('pinned','ladder')` — the Board's `picked` chip (D-L1-18); `false` ⇔ the Song has no Pick row or its source is `stars`/`newest`/`none`. **`has_pick` is unchanged** (a Pick row with a take, any source; shipped by L1). `TestSongs_PickDecided` pins the first, `TestSongs_HasPick_Unchanged` the second.

**C125.4 `Expand`.** `ExpandSongs/Tracks(crateID, base)`: manual ⇒ `base` with `CrateID` set (membership join); smart ⇒ `ParseFilter` then **merge**: a field set in both the stored filter and `base` with different values ⇒ 422 `FILTER_CONFLICT` (`details.field`), equal values and fields set in only one are kept (AND semantics; when both set, the list fields `state`, `status`, `style`, `tag`, `sound`, `origin` are **intersected**; an empty intersection simply matches nothing; an **empty array inside a stored filter** is refused at create/update with 422 `INVALID_FILTER` — it is never read as "unset" or as "match nothing"; an absent or empty request parameter is "unset"). A filter containing `crate_id` is refused at create/update (422 `UNKNOWN_FILTER_KEY`), so Crates cannot nest. Counts are always computed on read.

**C125.5 Worked example (the concept's, executable).** Filter `{"sound":["genre_family:reggae"],"unheard":true,"min_duration_s":240,"pick_decided":false}` on subject `song`. Fixture (`FixtureL2`): S1 reggae, 300 s, unheard, Pick `newest` ⇒ **match**; S2 reggae, 300 s, played once ⇒ no; S3 reggae, 180 s ⇒ no; S4 dance, 300 s ⇒ no; S5 reggae, 300 s, unheard, human-pinned Pick ⇒ no; S6 reggae, exactly 240 s, unheard, Pick `stars` ⇒ **match** (`stars` is not a decided Pick); S7 reggae draft Song with no take ⇒ no (no duration). (Per-Song fixture data: C132.2 table.) Expected ids `{S1, S6}`, count 2, identical through `list_songs` with the same parameters and through `crate_id`.

### C126. The album catalog and the Subsonic Record albums (`internal/albums`, lanes A, A2, S1, S2)

**Scope (D-L2-34 revised, D-L2-35).** `internal/albums` is the membership, naming and change-token package. Lane **A** builds the `record` kind and the token; lane **A2** (separable) adds the
`muse` and `unfiled` kinds, neutral naming and the Muse directory; lane **S1** makes the shipped Subsonic server list Record albums (C126.7); **S2** (optional) serves the auto-albums.
No op, no SPA page. The package is observed through `albumstest` and `testdata/albums_fixture.golden.json` and, end to end, through the Subsonic protocol tests of S1.

```go
type Kind string // "record" | "muse" | "unfiled"
type Album struct {
	ID string; Kind Kind; Name, SortName string; SongCount int
	CoverInput cover.Input
	RecordID, MuseID *string                       // ids only; never text
}
type Song struct{ TakeID string; Position, Disc int; SlotID *string }
type MuseMeta struct{ Ordinal int; Hidden bool }
type MuseDirectory interface {                     // implemented in internal/muses/directory.go (A2); nil until then (record kind only)
	Ordinals(ctx context.Context) (map[string]MuseMeta, error)            // every Muse, neutral facts only (the frozen muses.ordinal), ONE statement; called by List/Get, never by Token
	Version(ctx context.Context) (string, error)                          // COUNT(*) and MAX(updated_at) of muses: two indexed probes; the only directory call Token makes
	Labels(ctx context.Context, ids []string) (map[string]string, error)  // real names; called ONLY when the opt-in is on
}
type Settings interface{ SubsonicMuseNames(ctx context.Context) (bool, error) }   // curation.Service

func New(st *store.Store, rec *records.Service, muses MuseDirectory, set Settings, now func() time.Time) *Service
func (s *Service) List(ctx context.Context, kinds ...Kind) ([]Album, error)             // all kinds when none given; ordered record (first_published_at, id), muse (ordinal), unfiled
func (s *Service) Get(ctx context.Context, id string) (Album, []Song, error)            // ErrNotFound for an unknown or unlisted id
func (s *Service) Token(ctx context.Context) (string, error)                            // C126.5
func NeutralName(ordinal int) string                                                    // "Inspiration 007"
```
**C126.1 Membership (D-L2-15).** `record` albums: every Record with `status = 'published'` and `hidden_at IS NULL` and ≥ 1 *listable* slot; songs = resolved playable slots in order (`records.ResolvedPublished`, one set-based query); a slot in state **`pin_ineligible` (a pinned Take that was later discarded) is not listed** — the Subsonic folder rule (status) hides discarded Takes and the catalog must agree; the Record keeps playing it in the app and reports the warning (D-L2-6), and a tape needs a human to accept it (D-L2-33); `Disc` = index of the slot's side (`A`=1…`D`=4), 1 when none. **A2:** `muse` albums: for every Muse that is not hidden (from `MuseDirectory.Ordinals`), the Picks (`song_picks.take_id`) of its **live, non-hidden** Songs that are **in no published Record**, ordered by Song `(created_at, id)`; an album with no Songs is not listed. `unfiled`: the same for live non-hidden Songs with **no Muse**, id `unf`, name `Unfiled`. Every Take is listed at most once per album; Takes of `discarded` status never are.

**C126.2 Names (the neutral rule; A2).** `record` ⇒ the Record title. `muse` ⇒ `NeutralName(ordinal)` = `Inspiration ` + the Muse's frozen **`muses.ordinal`** (D-L2-37; hidden Muses keep their number, so ranks never shift), zero-padded to 3 digits (more when needed); with `subsonic_muse_names` on **and only then** the catalog calls `MuseDirectory.Labels` and uses `title` (reference Muses: `title · artist` when an artist exists). `unfiled` ⇒ `Unfiled`. `SortName` = `textfold.Norm(Name)`. The catalog **never** receives a Muse's text when the flag is off: the fake directory used by `TestAlbums_MuseLabelsNotCalledWhenOff` panics on `Labels`. `muses` SQL stays in `internal/muses` (`TestSQL_MuseTables_Allowlist` unchanged and green).

**C126.3 Not built (recorded).** `Primary(take)`/`AlbumsOfTake` (draft C110.3: a take's primary album) is **not** built: lineage `albumId`s stay (D-L2-35). If the operator later wants Records to own their songs' album ids, the rule was: the Record that first published (`first_published_at`, then id), else the Muse album, else the Song's Pick's album.

**C126.4 Subsonic mapping table (the binding contract; field names are OpenSubsonic's). "L2" = provided by `albums`; "S1" = computed in `internal/subsonic` by lane S1 from `albums.Get` and its own SQL; "M4a" = already shipped.**

| Subsonic | From | Owner |
|---|---|---|
| `AlbumID3.id` | `Album.ID` (`rec-<id>`; `mus-<id>`, `unf` in S2) | L2 (ids); S1 serves |
| `coverArt` / `getCoverArt id=rec-…` | `Album.CoverInput` through `cover.Service.EnsureMosaic` | L2 (input); S1 serves |
| `name`, `sortName`, `songCount` | `Album.Name`, `SortName`, `SongCount` (neutral for auto-albums unless opted in) | L2 |
| `Child.track`, `discNumber` | `Song.Position`, `Song.Disc` | L2 |
| `Child.albumId`, `parent` (inside a Record album) | the Record album id; elsewhere the lineage id `al-<root>` unchanged | S1 / M4a |
| `artist`, `displayArtist`, `artistId` | constants `metamusic`, `ar-metamusic` | M4a |
| `duration`, `created`, `year`, `genre`/`genres`, `playCount`, `played`, `starred` | durations, `first_published_at`, its year, style terms of the members, human/device `start` events, status | S1 (same SQL expressions as M4a's `albumAgg`, over the Record's resolved Takes) |
| `replayGain` | `albumGain = −18.0 − T`, `trackGain = gain_db + albumGain` from `RecordView.slots[].gain_db` (C124.2) | S1 |
| `getAlbumList2 type=`, `getIndexes`/`getArtists` `lastModified` | list types over lineage + Record albums; `Token` mixed into the modification stamp | S1 (token from L2) |
| unknown, unpublished or hidden Record id | `ErrNotFound` ⇒ Subsonic error 70 (a Record leaves the catalog the moment it is unpublished or hidden) | L2 error, S1 mapping |

**C126.5 `Token`** = first 16 hex of sha256 over **cheap probes and the two flags**, no scan of Takes and no per-Muse data (it never calls `Ordinals` or `Labels`): the number of published listed Records, `MAX(records.updated_at)`, `MAX(record_changes.rowid)`, `MAX(pick_events.rowid)`, `MAX(songs.updated_at)`, `MuseDirectory.Version` (skipped while the directory is nil), **`MAX(audit_log.id)` over the catalog-affecting write ops that succeeded** (`op IN ('update_track','update_song','update_muse','assign_muse_songs') AND status < 400`, index `audit_log_op(op, id)`; `audit_log.id` is a UUIDv7 `TEXT`, so `MAX` is time-ordered; this catches Take title/status edits and Song titles, which no other probe sees — the M2a `mutations` table is the Operator Ledger and is **not** used; M4a's `tracks.status_set_at` sees status only), and the two opt-in flags. It changes on every publish/unpublish/hide/edit, every Pick event, every Song hide/retire/title edit, every Take title or status edit, every Muse change and every flag change; it does **not** change on unrelated writes (`TestAlbums_TokenStable`, `TestAlbums_TokenNoDirectoryScan`). The op list is the constant `albums.TokenOps`; a test edits a title/status through every op that can write `tracks.title`, `tracks.status`, `songs.title` or `muses` and asserts the token moved. **Contract tests:** `albumstest.Golden` and `testdata/albums_fixture.golden.json` (the full list and every album's songs for `FixtureL2`, neutral names) are public-safe; S1's Subsonic tests compare ids and counts against the same fixture.

**C126.6 Performance and cache.** `List` (all kinds) for 1 000 Songs, 200 Muses, 30 Records: p95 < 150 ms (`TestPerf_AlbumCatalog`); no per-album query (≤ 6 statements per call, `TestAlbums_StatementCount`); `Token` ≤ 9 statements; no cache — the Token is for clients.

**C126.7 The Subsonic consumer (lane S1; amends M4a C108/C109; `internal/subsonic` is S1-owned).** Binding behaviour (D-L2-35):
1. **Ids.** `ids.go` gains a `rec-` prefix kind (`kindRecord`); `parseID`, `albumID`-style helpers and the existing `al-`/`tr-`/`ar-` rules are otherwise unchanged. A malformed `rec-` id is `kindBad` (error 10), an unknown or unlisted one is error 70.
2. **Lists.** `getAlbumList2`/`getAlbumList` return lineage albums **and** Record albums merged under the same `type`, `size`, `offset`, `musicFolderId`, `fromYear/toYear`, `genre` semantics: `newest` by `created` (Record: `first_published_at`), `alphabeticalByName/Artist` by name, `byYear` by the year of `first_published_at`, `frequent/recent/highest/starred` from the aggregates over the Record's resolved Takes (the same expressions as `albumAgg`, applied to the Takes `albums.Get` returned), `random` through the existing seeded shuffle over the union of ids. `getIndexes`/`getArtists`/`getArtist` list Record albums under the one artist; `lastModified` = max(existing stamp, token-derived stamp) so a client refreshes when `Token` changes; `ifModifiedSince` honoured. `search2/3` `albumCount` includes Records whose title matches (plain case-insensitive substring; no FTS table is added).
3. **Browse.** `getAlbum`, `getMusicDirectory` and `getAlbumInfo2` accept `rec-<id>`: children are the listable slots in order (`track` = position, `discNumber` = side index), each `Child` as M4a builds it for a lineage song except `albumId`/`parent` = the Record album. The folder rule (status) filters the resolved Takes exactly as for lineage albums.
4. **Cover art.** `getCoverArt id=rec-<id>` → `albums.Get` → `cover.Service.EnsureMosaic(CoverInput, ClampSize(size))`; the cover id on an `AlbumID3` is the album id. Unknown ⇒ error 70.
5. **ReplayGain.** Record albums carry `replayGain {albumGain, trackGain}` from the plan; lineage songs are unchanged.
6. **What does not change:** every existing id, `getSong`, streaming, scrobble, stars/ratings, playlists, bookmarks, play queue, lyrics, `getSimilarSongs`; `TestImportGraph_L2` keeps `subsonic*` away from `muses`, `recordview` and `songview` (it may import `albums`, `records` read paths via `albums`, and `media/cover`).
7. **Tests (S1):** `TestSubsonic_RecordAlbum_List` (a published Record appears in `newest`, `alphabeticalByName`, `byYear`, `random`; a draft and an unpublished one never), `TestSubsonic_RecordAlbum_Get` (children order, `track`/`discNumber`, `albumId`), `TestSubsonic_RecordAlbum_Cover` (PNG, size clamp, 70 when unpublished), `TestSubsonic_RecordAlbum_ReplayGain`, `TestSubsonic_RecordAlbum_Token` (`getIndexes lastModified` moves on publish, unpublish and a Pick flip; `ifModifiedSince` short-circuit), `TestSubsonic_LineageUnchanged` (the M4a golden XML/JSON fixtures pass byte for byte on a rig with no Records), `TestSubsonic_NoMuseText` (canary Muse on every Song; flag off ⇒ no canary byte in any `/rest` response), `TestAcceptance_L2_SubsonicRecordAlbum` (go-level rig: human publishes ⇒ the Subsonic client sees `rec-<id>`; unpublish ⇒ error 70). **S2** adds the same tests for `mus-<id>`/`unf` and the opt-in names.


### C127. Curation settings and privacy enforcement (`internal/curation`, tests)

```go
type Settings struct{ ExportMuseNames, SubsonicMuseNames bool; UpdatedAt, UpdatedBy string }
type SettingsPatch struct{ ExportMuseNames, SubsonicMuseNames *bool }
func New(st *store.Store, now func() time.Time) *Service
func (s *Service) Get(ctx context.Context) (Settings, error)
func (s *Service) SubsonicMuseNames(ctx context.Context) (bool, error)
func (s *Service) ExportMuseNames(ctx context.Context) (bool, error)
func (s *Service) Set(ctx context.Context, p auth.Principal, patch SettingsPatch) (Settings, error)   // p.Kind == KindHuman, else 403 HUMAN_ONLY before any read
```
**C127.1** Four `settings` keys: `curation.export_muse_names`, `curation.subsonic_muse_names` (`true`/`false`, absent = false), `curation.updated_at`, `curation.updated_by`; written in one transaction; an empty patch ⇒ 422; unchanged values ⇒ 200 without writing. The op is human-only (admin scope, audited by the gate: `args_sha256` only) and an agent can read the result.
**C127.2 Meaning.** `export_muse_names` ⇒ the tape's DESCRIPTION/liner, tags and chapter names gain "inspired by {title}[ — {artist}]" lines and the tape key changes; `subsonic_muse_names` ⇒ `muse` albums are named by the Muse (C126.2). Neither changes anything inside the app (the `app` audience always shows Muses to the operator and agents, D-L1-6), neither exposes Muse `brief` text anywhere, neither affects Records' own text.
**C127.3 Structural guards.** `TestImportGraph_L2` (C118); `TestAlbums_MuseLabelsNotCalledWhenOff`; `TestTape_SnapshotNoMuseByDefault` (a canary Muse on every slot Song; the snapshot, tags, chapter names, DESCRIPTION, filename, plan and cover input contain no canary byte; with the flag on only the documented lines do); `TestLiner_ExportIgnoresPrivateFields`; `TestRecordView_SnapshotFlag`; `TestSQL_MuseTables_Allowlist` (L1) **unchanged and green** (`albums` mentions no `muses` SQL; `recordview` reads Muses through `muses.Service.Ref` only).
**C127.4 Response, log and canary tests (extends L1 C99).** The L1 allowlist `{list_muses, get_muse, create_muse, update_muse, get_song, get_muse_board, get_track_context, get_draft}` gains **exactly one** op, `get_record` (the `app` liner notes carry Muse title/artist to read-scope agents, D-L1-6); `TestMuseText_OnlyInAllowlistedOps` runs every L2 op over REST and MCP with a canary Muse assigned to the fixture Songs and fails on any other op's bytes. Because `RecordView.liner` carries Muse text, **only `get_record` may emit it**: `create_record`, `update_record`, `edit_record_slots`, `propose_record` and `publish_record` return a liner-less `RecordView` (C124.2), and `TestMuseText_MutatingOpsLinerless` runs those five over REST and MCP with the canary on the Songs and asserts no canary byte in any response, **in the `idem` row stored for a `create_record` replay** (and its replay body), in the audit row and in logs (the L1 `TestIdem_CreateMuseCacheExpires` pattern). Further canaries: an **agent-authored Record title, subtitle, liner note, proposal rationale and Crate name/description** containing Muse-looking text — each may appear only in the ops that return that field (`create_record`, `get_record`, `list_records`, `update_record`, `propose_record`, Crate ops) and **never** in `slog` output, `audit_log` text columns, `job_events`, `notify` messages, exports (`export_jsonl`, `export_playlist_zip`, backup manifests), `problem+json` bodies, the tape (draft Records cannot be exported) or the catalog (not published). `TestL2_PublishGate`: an agent drafts a Record with a canary title and slots; `albums.List`, `albums.Get` and `tape.Export` do not see it (404/409) until a human publishes; after a human **unpublishes** it vanishes again; an agent edit of a published Record is 403 `HUMAN_ONLY`. `TestL2_AgentTitleCanary`: an agent writes a Muse-looking Take title (`update_track`) and Song title (`update_song`) on a published Record's Songs — the catalog text is read live, so the string appears in the tape snapshot/preview chapter names exactly as documented (D-L2-27 b) and **nowhere else**; `TestPublishPreview_EqualsSnapshot` (the strings `get_record include=preview` returns equal the chapter names, export-liner titles and tags of `Snapshot`, byte for byte). `TestRecordOps_ErrorsCarryIDsOnly` drives every L2 error code with canaries. `TestFixtures_L2_SyntheticOnly` (names match the L1 synthetic family plus `Synthetic Record NNN`, `Synthetic Crate NNN`).
**C127.5 Settings visibility.** `get_stats.curation` carries the two booleans so the operator sees what is on (C130).

### C128. Tape export (`internal/tape`, `internal/media/flacmeta`, `internal/mediatool`)

```go
type Item struct {
	SlotID string; Position int; TakeID, Title, MasterRel, MasterSHA256 string; MasterBytes int64
	SampleRate, Bits, Channels int; Lossy bool; DurationS float64; Measure Measure
}
type Measure struct{ LUFS, TruePeakDBTP float64; OK bool }                // OK false ⇒ unmeasured
type Snapshot struct {
	RecordID string; Revision int; Status string; Title string
	Items []Item; Tags []flacmeta.Comment; Description string; CoverInput cover.Input; MuseNames bool
	Warnings []SlotWarning                                                  // pin_ineligible | undecided_pick, ids and codes only
}
type SlotWarning struct{ SlotID, Code string }
type SnapOptions struct{ IncludeLiner bool }
type Source interface{ Snapshot(ctx context.Context, recordID string, o SnapOptions) (Snapshot, error) }
type Loudness interface{ Ensure(ctx context.Context, takeIDs []string) (map[string]Measure, error) }   // ears; analyses missing takes
type Pipeline interface{ Render(ctx context.Context, j Job, progress func(Progress)) (Result, error) } // production: ffmpeg + flac; tests: fakes
type Options struct { Store *store.Store; Tools mediatool.Tools; Source Source; Loudness Loudness; Covers CoverRenderer
	Data *os.Root; Pipeline Pipeline; Signer *serve.Signer; Now func() time.Time; NewID func() string; Owner string; Log *slog.Logger
	FlacVersion, BuildVersion string }   // FlacVersion: first line of `flac --version`, computed ONCE by app.Build at startup (empty when exec.LookPath failed ⇒ 503); BuildVersion = the binary version (both are in the key)
func New(o Options) *Service
type ExportInput struct{ Revision *int; TargetLUFS *float64; IncludeLiner, AcceptWarnings bool }   // AcceptWarnings: human principals only (D-L2-33)
func (s *Service) Export(ctx context.Context, p auth.Principal, recordID string, in ExportInput) (Tape, error)
func (s *Service) Get(ctx context.Context, id string) (Tape, error)
func (s *Service) Brief(ctx context.Context, recordID string, limit int) ([]TapeBrief, error)
func (s *Service) Run(ctx context.Context)                              // runner loop (serve only)
func (s *Service) RunOne(ctx context.Context) (bool, error)             // claim and process one queued tape (tests)
func (s *Service) Recover(ctx context.Context) error                    // startup: re-queue running tapes with an expired lease
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error)   // "flac"
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error)      // RetentionDays
```
**C128.1 `Export`** (D-L2-19). Order of checks: Record exists (404), is `published` (409 `RECORD_NOT_PUBLISHED`), optional `revision` equals the current one (409 `STALE_REVISION`: "I reviewed revision N"), at least one resolved Take (422 `RECORD_EMPTY`), `MaxItems`/`MaxDurationS` (422 `TAPE_TOO_LONG`), `target_lufs` in [−30, −9] (422) and then **rounded to 0.1 LU**, `accept_warnings: true` from a non-human principal (403 `HUMAN_ONLY`), `flac` usable (503 `UNAVAILABLE {component: "flac"}` only when `exec.LookPath` of the configured binary failed at startup — `MM_FLAC` defaults to `"flac"`, M1, so an unset variable is not an error). Then: take the snapshot (one read transaction: Record, slots, resolved Takes, masters), ask `Loudness.Ensure` for missing analyses (a failure only marks the item `unmeasured`), compute the plan (C128.2) and the snapshot warnings (any `pin_ineligible`/`undecided_pick` without `accept_warnings` ⇒ 409 `TAPE_NEEDS_REVIEW` with `details.slots`), compute the **key** (C128.5), then one write transaction: if a live row with that key exists return it (`reused: true`; reuse is never blocked by a quota), else check the quotas (live tapes of this Record ≥ `MaxLivePerRecord`, or `Σ bytes` of `ready` rows ≥ `MaxReadyBytes` ⇒ 409 `TAPE_QUOTA`, `details.scope` `record|total`), else insert `queued` with `plan_json`, `params_json`, `record_revision`, `contains_lossy`; wake the runner; return 201. The audit row records the op and the Record id (no titles).

**C128.2 Loudness plan (`tape.PlanGains`, pure; D-L2-18).**
```go
type GainStatus string // matched | cut_clamped | boost_clamped | peak_limited | silent | unmeasured
type GainPlan struct{ GainCDB int; Status GainStatus; PredictedLUFS *float64 }
func AutoTarget(ms []Measure) float64
func PlanGains(ms []Measure, target *float64) (float64, []GainPlan)
func ApplyGain(dst []int32, src []int32, gainCDB int) (clipped int)   // 24-bit values in int32; round-to-nearest-away-from-zero of v·10^(cdB/2000)
```
`PlanGains` converts every LUFS and true-peak value **once** to integer centi-units (`round(x·100)`, half away from zero) and uses integer math from there on: `AutoTarget`: median (in centi-units) of the `OK`, non-silent (`LUFS > −60`) integrated values (mean of the two middle ones for an even count), clamped to [−23.0, −14.0], rounded to one decimal = a multiple of 10 centi-units (half away from zero); none ⇒ −14.0. A caller's `target` is first rounded to 0.1 LU. For each item: `silent` (`LUFS ≤ −60`) ⇒ gain 0; not `OK` ⇒ `unmeasured`, gain 0; else `want = Tc − Lc`; clamp to [−1200, +600] (a clamp ⇒ `cut_clamped`/`boost_clamped`); peak guard `limit = −100 − TPc` (exact: no `floor` of a float): if `gain > limit` ⇒ `gain = limit`, status `peak_limited` (then clamp again to [−1200, +600]); `PredictedLUFS = (Lc + gain)/100` (nil for `silent`/`unmeasured`). `ApplyGain` multiplies by the float64 factor `float64(math.Pow(10, float64(gainCDB)/2000))` (one product per sample, explicit `float64()` casts so FMA contraction cannot differ between amd64 and arm64; `TestApplyGain_Table` also pins seven factors as hex float literals), rounds, clamps to ±8 388 607 and **returns the number of samples that had to be clamped** (the export fails with `TAPE_CLIPPED` when it is > 0; the guard makes 0 the only reachable value for measured items). Table `TestPlanGains_Table` (17 rows, G01–G17; the expected values are in F1's tests): G01 one item −14.0/−3 ⇒ T −14.0, gain 0 matched; G02 `[-15.5/-2.0, -13.0/-1.5, -11.8/0.6]` ⇒ median −13.0, clamped to T −14.0; gains `+100` (`peak_limited`: wants +150, limit +100), `−100` matched, `−220` matched; G03 `[-30/-12, -14/-3]` ⇒ median −22.0 ⇒ T −22.0; `+600` `boost_clamped`, `−800` matched; G04 target −16.0, `−20/−5.0` ⇒ `+400` matched (limit exactly +400); G05 silent item −70 ⇒ gain 0 `silent`, excluded from the median; G06 unmeasured ⇒ gain 0, excluded; G07 all unmeasured ⇒ T −14.0, every gain 0; G08 `−1.0/−3` with T −14.0 ⇒ want −1300 ⇒ `−1200 cut_clamped`; G09 `−14.0/+0.6` T −14.0 ⇒ want 0, limit −160 ⇒ `−160 peak_limited`, predicted −15.6; G10 `[-16,-14]` ⇒ median −15.0 ⇒ T −15.0; G11 `−14.04/−3` T −14.0 ⇒ `+4` matched; G12 predicted LUFS = `LUFS + gain/100` for every non-silent measured item; G13 median at the clamp edges (−23.0 and −14.0 exactly); G14 an item whose `peak_limited` gain is below −1200 (true peak +11) ⇒ `−1200` (`cut_clamped` wins); G15 `−16.0/−1.9` T −14.0 ⇒ want +200, limit +90 ⇒ `+90 peak_limited` (the float form yields +89); G16 `−16.5/−2.2` T −14.0 ⇒ want +250, limit +120 ⇒ `+120 peak_limited`; G17 `−14.04/−1.0` T −14.0 ⇒ want +4, limit 0 ⇒ `0 peak_limited`, predicted −14.04. `TestApplyGain_Table` (−600 cdB on 1 000 000 ⇒ 501 187; +600 on 7 000 000 ⇒ clamped to 8 388 607, `clipped 1`; 0 cdB identity; negative samples round away from zero; −1200 on ±1 ⇒ 0).

**C128.3 `flacmeta` block writers and `mediatool.Stdin` (T1, tape lane: the VORBIS_COMMENT and PICTURE builders are **extracted from `internal/media/tagged/write.go`** (D-L2-40) and exported, CUESHEET and `Rewrite` are new; `flacmeta/types.go` with `Comment` is F2; pure Go; fuzz + golden tests with `flactest`).**
```go
type Comment struct{ Key, Value string }                 // Key: ASCII 0x20–0x7D without '='; Value: UTF-8
func VorbisComment(vendor string, c []Comment) []byte    // block type 4 payload
type CueTrack struct{ Offset uint64; Number uint8; Indexes []CueIndex }   // ISRC empty, audio track
type CueIndex struct{ Offset uint64; Number uint8 }
type Cuesheet struct{ Tracks []CueTrack; LeadOut uint64 } // media catalog empty, lead-in 0, is_cd 0
func CuesheetBlock(c Cuesheet) []byte                    // block type 5 payload; lead-out track number 255
type Picture struct{ Type uint32; MIME, Desc string; W, H, Depth, Colors uint32; Data []byte }
func PictureBlock(p Picture) []byte                      // block type 6 payload
type Replace struct{ Vorbis, Cuesheet, Picture []byte }  // nil ⇒ keep the source's block of that type, empty slice ⇒ drop it
func Rewrite(dst io.Writer, src io.ReaderAt, size int64, info Info, r Replace) (int64, error)   // fLaC + STREAMINFO + SEEKTABLE verbatim + the new blocks (last-block flag fixed) + the audio frames byte for byte
```
`Rewrite` never touches the frames region: it streams `src[framesOffset:]` with `io.Copy` and the caller asserts `FramesSHA256` before/after. `mediatool.RunOpts` gains `Stdin io.Reader` (`cmd.Stdin`; `nil` keeps today's `/dev/null`); `TestRun_Stdin` and the unchanged callers' tests cover it. `TestFlacmeta_Rewrite_Golden` (a `flactest` file: metadata replaced, STREAMINFO/SEEKTABLE/frames identical, `Read` returns the new comments, cuesheet tracks and picture), `TestCuesheet_Block_Golden`, `FuzzRewrite` (random block orders and sizes never panic and keep the frame region identical), `TestVorbisComment_Escapes` (UTF-8, `=` in values, empty values, key validation).

**C128.4 The pipeline (production `Pipeline`; every argv is literals plus app-created absolute paths — the M0 argv rule).**
**Concurrency (binding).** `Render` runs two goroutines under one cancellable context (`ctx2, cancel := context.WithCancelCause(ctx)`) joined by `pr, pw := io.Pipe()`. The **encoder goroutine** runs `mediatool.Run(ctx2, flac, args, RunOpts{Stdin: pr})` (it blocks until the encoder exits). The **feeder** (the caller's goroutine) loops over the items, running `mediatool.Run(ctx2, ffmpeg, …, Stdout: gainStage→pw)` per item. *ffmpeg fails* (or the gain stage aborts with `TAPE_CLIPPED`/`TAPE_SOURCE_CHANGED`): the feeder calls `pw.CloseWithError(err)` and `cancel(err)`, which kills the encoder through its context. *The encoder dies* (error, `ENOSPC`): its goroutine calls `pr.CloseWithError(err)` so the gain stage's next write fails, and `cancel(err)` stops the running ffmpeg. On success the feeder `pw.Close()`s and waits for the encoder goroutine; no goroutine outlives `Render`; the first error wins (`context.Cause`) and its code (`TAPE_DECODE_FAILED` vs `TAPE_ENCODE_FAILED`) is that of the side that failed first. Tests: `TestPipeline_FFmpegFailureKillsEncoder`, `TestPipeline_EncoderDeathStopsFFmpeg`, `TestPipeline_NoGoroutineLeak`.
1. **Work dir** `incoming/tape-<uuid>/` (0700), removed on return (success, failure, cancel). Disk preflight: free space ≥ `1.3 × Σ duration × 288 000 B + 64 MiB` — `tape.flac` and `final.flac` coexist (`Rewrite` copies every frame), so the peak is two FLAC files of about 0.65 × PCM each (else `TAPE_DISK_FULL`, nothing started).
2. **Encoder** (one process, started first, stdin fed from a pipe): `flac --silent --force --force-raw-format --endian=little --sign=signed --channels=2 --bps=24 --sample-rate=48000 -8 --no-padding -o <work>/tape.flac -` (`mediatool.Run` with `Stdin`, timeout `max(120 s, 0.5 × total duration)`).
3. **Per item, in order** (sequential): `ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<abs master> -map 0:a:0 -map_metadata -1 -vn -ac 2 -ar 48000 -f s32le pipe:1` (timeout `ItemTimeout`), `Stdout` = the **gain stage**: it accumulates whole 6-byte frames, converts each 32-bit little-endian sample `s` (the 24-bit value left-aligned; 16-bit sources are left-aligned too) to `v = s >> 8`, applies `ApplyGain(v, gain_cdb)`, writes 3 little-endian bytes per sample to the encoder pipe in `Chunk`-sized writes, and counts frames `n_i` (a clamp ⇒ abort `TAPE_CLIPPED`). A master whose size differs from `tracks.master_bytes` ⇒ `TAPE_SOURCE_CHANGED`. The first byte of item *k+1* follows the last byte of item *k* with nothing in between — **that is the gapless guarantee**; no resampling state is carried between items.
4. Close the pipe, wait for the encoder. `flacmeta.Read(tape.flac)`: `total_samples` must equal `Σ n_i` (else `TAPE_SAMPLE_MISMATCH`), 48 000 Hz, 24-bit, 2 channels.
5. **Chapters** from the cumulative `n_i`: chapter *i* starts at `Σ_{j<i} n_j` samples; `start_ms = floor(start · 1000 / 48000)`. Written three ways from the same list: the binary `CUESHEET` block (one TRACK per item, `INDEX 01` at the exact sample offset, lead-out track 255 at `total_samples`), `CHAPTERnnn=HH:MM:SS.mmm` + `CHAPTERnnnNAME=<take title>` comments, and a text `CUESHEET` comment (`FILE "tape.flac" WAVE`, per track `TRACK nn AUDIO`, `TITLE`, `PERFORMER "metamusic"`, `INDEX 01 mm:ss:ff` at 75 fps, double quotes in titles replaced by `'`). The names of lossy items end ` [lossy source]`; sided slots are prefixed `A · `.
6. **Tags:** `snapshot.Tags` (`TITLE`, `ALBUM` = Record title, `ARTIST` = `metamusic`, `DATE`, `GENRE`, and the export-audience extras) plus `DESCRIPTION` (the liner `Description()` when `include_liner`, with the lossy line), `REPLAYGAIN_ALBUM_GAIN` = `−18.00 − T` dB, `REPLAYGAIN_ALBUM_PEAK` (the largest post-gain sample peak, linear 6 decimals), `MM_RECORD_ID`, `MM_TAPE_VERSION=mm-tape/1`, `MM_TARGET_LUFS`, `MM_CONTAINS_LOSSY_SOURCES=true|false`, `MM_SOURCES` (`lossless=n;lossy=n;resampled=n;upmixed=n`). A front-cover `PICTURE` (type 3, `image/png`, the 512 px cover from `Covers.EnsureMosaic`). Vendor string `metamusic <version>` (in the key). **No tag varies between two exports of the same inputs:** `DATE` is the Record's `first_published_at` day, and neither the Record revision nor the tape id is written (both live in the database).
7. **Rewrite** with `flacmeta.Rewrite` into `<work>/final.flac`; assert `FramesSHA256(tape.flac) == FramesSHA256(final.flac)`.
8. **Verify** (D-L2-17, C128.6): `flac --silent --test <work>/final.flac` (decodes and checks the STREAMINFO MD5), then `flacmeta.Read(final.flac)` parse-back of the cuesheet, comments and picture against the plan. Then claim `derived/tapes/<aa>/<sha256>.flac` with `Root.Link` (0444; EEXIST ⇒ identical bytes already there) and finish the row (`ready`, `sha256`, `bytes`, `path`, `filename`, `total_samples`, `plan_json` + chapters, `finished_at`).
**`plan_json` (`mm.tape/1`):** `{"def":"mm.tape/1","target_lufs":-14.0,"spread_lu":{"before":3.7,"after":0.4},"items":[{"slot_id","take_id","position","master_sha256","gain_cdb","status","measured":{"lufs","true_peak_dbtp"}|null,"predicted_lufs"|null,"lossy","resampled","upmixed","downmixed","duration_s"}],"chapters":[{"index","slot_id","take_id","start_sample","samples","start_ms"}],"warnings":[{"slot_id","code"}],"accepted_warnings":false}` — `chapters` and `total_samples` are filled when the tape is ready; **no titles and no Muse text** are stored (the chapter names live only in the file and the key).

**C128.5 Key, lifecycle, errors.** `key = hex sha256("mm-tape/1\n" + flac --version line + "\n" + canonical JSON of {items: [(master_sha256, gain_cdb, resampled/upmixed/downmixed flags)], target_lufs (rounded to 0.1), include_liner, muse_names, tags (sorted), chapter names, description, cover key, vendor (`metamusic <version>`)})`; the key excludes the Record revision (two revisions with identical audio and metadata are the same file), the tape id and the principal. States: `queued → running → ready | failed`; `running → queued` (recovery increments the **`tapes.attempts` column** — never `progress_json`, which the runner overwrites about once a second; an expired lease with `attempts ≥ 2` ⇒ `failed TAPE_INTERRUPTED`, i.e. the third expiry fails); `ready → expired` (sweep). The runner is one goroutine (`concurrency 1`), claims with `UPDATE … SET state='running', owner=?, lease_expires_at=? WHERE id=? AND state='queued'`, renews the lease every 10 s, writes `progress_json {phase: decode|encode|rewrite|verify, item, items}` at most once per second. **Error codes** (`tapes.error_code`, Go-validated, ids only in messages): `TAPE_DECODE_FAILED` (`item`), `TAPE_ENCODE_FAILED`, `TAPE_CLIPPED`, `TAPE_SAMPLE_MISMATCH`, `TAPE_VERIFY_FAILED`, `TAPE_DISK_FULL`, `TAPE_SOURCE_CHANGED`, `TAPE_INTERRUPTED`; request-time refusals (never stored on a row): 409 `TAPE_NEEDS_REVIEW`, 409 `TAPE_QUOTA`. A failed tape never leaves a file; a re-export after a failure creates a new row (the partial unique index only constrains queued/running/ready). `Sweep(olderThan)` moves `ready` rows older than `RetentionDays` to `expired` and removes the file after the commit, and deletes files under `derived/tapes/` that no `ready` row names and that are older than one day; the daily `serve` ticker calls it next to the export sweep.

**C128.6 What "verified" means (the acceptance, all of it asserted in `TestAcceptance_L2_TapeExport` on synthetic sources of known level, media tier):**
- **V1 container:** `flac --test` passes and the STREAMINFO MD5 matches the decode;
- **V2 samples:** `total_samples = Σ n_i` = the last chapter's end; 48 000 Hz, 24-bit, 2 channels; for a lossless 48 kHz source `n_i` equals `tracks.total_samples` exactly;
- **V3 gapless:** the decoded PCM of the tape equals, sample for sample, the independently computed concatenation of the gain-applied decodes (compared by sha256 of the raw `s32le` decode), and the first sample of every item sits exactly at its chapter offset (no inserted silence, no overlap);
- **V4 chapters:** the three encodings parse and agree (block offsets exact; comments within 1 ms of `floor(offset·1000/48000)`; text cuesheet within 1/75 s); the count equals the item count; the names equal the take titles with the lossy suffix;
- **V5 loudness:** each chapter range, measured with the Ears window measurement, is within ±0.5 LU of the item's `predicted_lufs` (clamped items are excluded and listed), and the post-gain spread of the unclamped items is ≤ 0.5 LU;
- **V6 lossy label:** with one MP3 source the `MM_CONTAINS_LOSSY_SOURCES` tag, the `DESCRIPTION` line, the chapter-name suffix, the filename suffix and `contains_lossy` in the API agree; with none they are all absent; a lossy item is never relabelled lossless;
- **V7 tags and cover:** the PICTURE block is the 512 px PNG of the cover key, every tag of C128.4 is present, no tag contains the canary Muse text unless the flag is on;
- **V8 determinism:** exporting the same snapshot twice returns the same row (`reused`); rendering it again after a sweep yields a byte-identical file (equal sha256) **under the same binary and `flac` version** (both are in the key; the file carries no tape id, revision or export date).
`TestTape_ChaptersVisibleToFFmpeg` has **no assertion**: it is a *spike log* (`t.Log` of what `-f ffmetadata` reports; skipped without ffmpeg). The **T1 step-0 spike result is a binding input** to the tape lanes: if at least one of the operator's players (or a desktop player in the spike) reads one of the three encodings, no sidecar is built and DC-L2-1 names the working encoding; if none does, T2 adds the `.cue` sidecar of D-L2-29 before the lane is done — `get_tape.download.cue_url` (a signed `/media/<sha>.cue`, text generated from the plan's chapters, extension `cue` added to `serve` by T2) and a "Download cue sheet" link, with `TestTape_CueSidecar`; the three chapter encodings stay either way.

**C128.7 Serving and housekeeping.** `serve.Chain(library, exports, clips, tapes, covers)` (the nil-safe variadic `Chain` and the `png` extension already exist from M4a; F2 appends the `tapes` resolver in `app/l2.go`; the tape lane supplies the resolver); `tape.Service.Resolve(sha, "flac")` looks up a `ready` row by `sha256` and returns `serve.Object{RelPath, ContentType: "audio/flac", ETag: sha, DownloadName: filename}` (a sha not owned by a `ready` tape is `ErrNotFound`, so the master resolver and the tape resolver never shadow each other: shas are distinct by construction). `derived/tapes/` is skipped by `library_orphans` (F2 adds it to the importer's cache-dir list) and is never in a backup (the backup copies only files the database references; `TestBackup_ExcludesTapesAndCovers` asserts it); `Filename` = `<record title slug>-<YYYYMMDD>-tape[-lossy-sources].flac` (via `textfold.Slug`: ASCII `[a-z0-9._-]`, ≤ 120, the rules of the unexported `exports.cleanFilename`).

---

### C129. Operations (types in `internal/api/types_l2.go`; JSON snake_case)

Legend (M2a C38): **C** = `idempotency: create`; **H** = `rate: heavy`; all L2 operations are `cost: free` (`TestL2_NoSpendOps`, D-L2-22). `exclude:human-only` ops are
`human_only: true` and live under `/human/api/` (the registry rule); their UI routes are in-app `SecureGate` routes (C131).

| OperationID | Method + path | Scope | Cost | MCP | UI | Flags | Output |
|---|---|---|---|---|---|---|---|
| `list_records` | GET `/api/records` | read | free | tool | `/records` | | `{items: RecordSummary[], next_cursor, total}` |
| `get_record` | GET `/api/records/{id}` | read | free | tool | `/records/:id` | | `RecordView` |
| `create_record` | POST `/api/records` | write | free | generic | `/records` | C | 201 `RecordView` (liner-less) |
| `update_record` | PATCH `/api/records/{id}` | write | free | generic | `/records/:id` | | `RecordView` (liner-less) |
| `edit_record_slots` | POST `/api/records/{id}/slots` | write | free | generic | `/records/:id` | | `RecordView` (liner-less) |
| `get_record_changes` | GET `/api/records/{id}/changes` | read | free | generic | `/records/:id` | | `{items: ChangeView[], next_cursor}` |
| `propose_record` | POST `/api/records/{id}/proposal` | write | free | tool | `/records/:id` | | `RecordView` (liner-less) |
| `publish_record` | POST `/human/api/records/{id}/publish` | write | free | `exclude:human-only` | `/human/records/:id/publish` | human_only | `RecordView` (liner-less) |
| `export_record_tape` | POST `/api/records/{id}/tapes` | write | free | generic | `/records/:id` | C, H | 201 `Tape` |
| `get_tape` | GET `/api/tapes/{id}` | read | free | generic | `/records/:id` | | `Tape` |
| `list_crates` | GET `/api/crates` | read | free | generic | `/crates` | | `{items: CrateSummary[], next_cursor, total}` |
| `get_crate` | GET `/api/crates/{id}` | read | free | generic | `/crates/:id` | | `CrateDetail` |
| `create_crate` | POST `/api/crates` | write | free | tool | `/crates` | C | 201 `CrateDetail` |
| `update_crate` | PATCH `/api/crates/{id}` | write | free | tool | `/crates/:id` | | `CrateDetail` |
| `edit_crate_items` | POST `/api/crates/{id}/items` | write | free | generic | `/crates/:id` | | `ItemsResult` |
| `get_curation_settings` | GET `/api/curation/settings` | read | free | generic | `/settings/curation` | | `CurationSettings` |
| `set_curation_settings` | PUT `/human/api/curation/settings` | admin | free | `exclude:human-only` | `/human/curation` | human_only | `CurationSettings` |

17 operations: 5 first-class tools, 10 generic, 2 human-only (`TestOps_RegisteredWithMetaL2`). After L2 the registry holds 109 + 17 = **126** operations; F2 re-pins
`TestRegistry_OpCount` (both pins) at 126. **Agent counterparts of the human-only ops:** `propose_record` (for `publish_record`) and `get_curation_settings` (to read the opt-ins).
**`tools/list`:** F2 measures the compact `tools/list` size with the 5 new tools, sets `toolsCeilingBytes` to the measured value rounded up (a committed constant with a
one-line justification, never an `-update` side effect) and re-baselines the golden; if the L2 tools alone exceed **9 000 compact bytes** it demotes to `mcp: generic`, in
order, `update_crate`, `create_crate`, `list_records`, and re-measures (`create_record` is already generic, D-L2-21); beyond that it stops and asks the operator (D-M3-36 pattern).
**Body limits** (`MaxBodyBytes`): every L2 op with a body is `bodySmall` (64 KiB): `edit_record_slots` ≤ 50 ops, `create_record` ≤ 60 ids, crate item edits ≤ 200 each,
crate filters ≤ 4 096 B (`TestOps_BodyLimitsL2`).

**Inputs:**
- `list_records`: `status` (explode: draft|proposed|published), `q`, `song_id`, `include_hidden`, `limit` 1..100 (50), `cursor`.
- `get_record`: path `id`; `include` (explode: `liner`, `energy`, `tapes`, `changes`, `preview`; the **op's** default is `liner,tapes,changes`; every other op uses none), `changes_limit` 0..50 (10).
- `create_record`: `IdemHeader` + `{title, subtitle?, liner_note?, song_ids?: string[0..60]}`. `update_record`: `{revision, title?, subtitle?, liner_note?, cover_reroll?: bool, hidden?: bool}`.
- `edit_record_slots`: `{revision, ops: SlotOp[1..50]}`; `SlotOp = {op: add|remove|move|reorder|pin|unpin|set_side|pin_all|unpin_all, song_id?, slot_id?, take_id?, position?, side?: OptSide (absent | null | "A".."D"), slot_ids?: string[]}` (C121.3).
- `get_record_changes`: path `id`; `order` (`desc` default | `asc`), `cursor` (the previous page's last `seq`), `kind` (explode), `limit` 1..200 (50).
- `propose_record`: `{revision, rationale: string 1..500}`. `publish_record`: `{revision, published: bool}`.
- `export_record_tape`: `IdemHeader` + `{revision?: int, target_lufs?: number [-30,-9] (rounded to 0.1), include_liner?: bool (default true), accept_warnings?: bool (human principals only, D-L2-33)}`. `get_tape`: path `id`.
- `list_crates`: `kind`, `subject`, `q`, `include_hidden`, `counts` (bool: compute smart counts), `limit` 1..100 (50), `cursor`. `get_crate`: path `id`.
- `create_crate`: `IdemHeader` + `{name, description?, kind: manual|smart, subject?: song|take (default song), filter?: object, items?: [{song_id?|track_id?}] (≤ 200)}`.
  `update_crate`: `{revision, name?, description?, filter?: object, hidden?}`. `edit_crate_items`: `{add?: Member[≤200], remove?: Member[≤200]}`.
- `set_curation_settings`: `{export_muse_names?: bool, subsonic_muse_names?: bool}`.

**Shapes** (abridged; full in `types_l2.go`; principals through `api.RefFor`; **no Muse text outside `RecordView.liner`**):
```
Record        {id, title, subtitle, liner_note, status, revision, cover_rerolls, hidden, created_by (RefFor), created_at, updated_at,
               proposal: {note, by (RefFor), at}|null, published: {by (RefFor), at, first_at}|null}
RecordSummary {id, title, subtitle, status, slots, playable, duration_s, revision, updated_at, hidden, warnings, cover: CoverRef}
CoverRef      = api.CoverLinks {url_512, url_1024, expires_at}         // the M4a type: signed /media/<key>.png links (C18 TTL)
RecordView    {record: Record, slots: SlotView[], totals: {slots, playable, duration_s, lossy, warnings, undecided}, warnings: [{slot_id|null, code}], cover: CoverRef,
               liner?: {markdown, summary} /* get_record ONLY (Muse text, C127.4) */, energy?: Energy, changes?: ChangeView[], tapes?: TapeBrief[],
               preview?: {chapters: [{position, name}], tags: [{key, value}], description, warnings: [{slot_id, code}]}, pending}
SlotView      {id, position, side|null, mode: floating|pinned, state: ok|empty|pin_ineligible,
               song: {id, title, angle|null, state, retired, retired_into|null}, take: TakeBrief|null, pick: {take_id|null, source}|null,
               pin_differs, gain_db|null, why|null /* records.WhyText of the current song_picks row */, warnings: string[]}
TakeBrief     {id, title, status, duration_s, lossy, stars|null (human), recipe_known}
ChangeView    {seq, at, kind, text, slot_id|null, position|null, from: {id, title}|null, to: {id, title}|null, reason|null, evidence|null, actor (RefFor)}
TapeBrief     {id, state, created_at, finished_at|null, contains_lossy, bytes|null, expires_at|null}
Tape          {id, record_id, record_revision, state, reused, progress: {phase, item, items}|null, target_lufs, spread_lu: {before, after}|null,
               items: [{slot_id, take_id, position, gain_db, status, predicted_lufs|null, lossy, resampled, upmixed, downmixed}],
               chapters: [{index, slot_id, start_ms, samples}]|null, warnings: [{slot_id, code}], accepted_warnings, contains_lossy, lossy_sources, total_ms|null, bytes|null, sha256|null,
               download: {url, filename, bytes, expires_at, cue_url?}|null, error: {code, message}|null, created_at, started_at|null, finished_at|null}
Crate         {id, name, description, kind, subject, filter|null, revision, created_by (RefFor), created_at, updated_at, hidden}
CrateSummary  Crate + {count|null}                 CrateDetail {crate: Crate, count, warnings: string[]}
ItemsResult   {added, already, removed, not_member, count}
CurationSettings {export_muse_names, subsonic_muse_names, updated_at|null, updated_by (RefFor)|null}
Energy        (C124.3)
```
**Error codes (new, stable):** 404 `NOT_FOUND` (unknown record/crate/tape/slot ids — message carries only the id); 409 `STALE_REVISION`, `INVALID_RECORD_STATE`, `RECORD_NOT_PUBLISHED`, `SONG_ALREADY_IN_RECORD`,
`SONG_RETIRED`, `CRATE_IS_SMART`, `CRATE_FULL`, `TAPE_NEEDS_REVIEW`, `TAPE_QUOTA`; 403 `HUMAN_ONLY` (hint text per C120.1; also `accept_warnings` from a non-human); 422 `VALIDATION_FAILED` (field errors), `UNKNOWN_SONG`, `UNKNOWN_TRACK`, `UNKNOWN_SLOT`,
`TAKE_NOT_IN_SONG`, `TAKE_NOT_ELIGIBLE`, `TOO_MANY_SLOTS`, `INVALID_POSITION`, `SLOT_ORDER_MISMATCH`, `RECORD_EMPTY`, `SUBJECT_MISMATCH`, `UNKNOWN_FILTER_KEY`, `INVALID_FILTER`, `FILTER_TOO_LARGE`,
`FILTER_CONFLICT`, `TAPE_TOO_LONG`; 503 `UNAVAILABLE {component}` (`records`, `crates`, `tape`, `flac`, `curation`). Every message names ids and fields, never titles or Muse text (C127.4).

### C130. Amendments to earlier contracts (the owning task records each in that plan document in the same commit)

1. **L1 `list_songs` / `search_sounds` / `songs.ListFilter` (C90, C96):** new query parameters `unheard`, `min_duration_s`, `max_duration_s`, `min_stars`, `unrated`, `tag` (explode), `lossy`, `recipe_known`, `in_record`, `crate_id`, `pick_decided`, with the semantics of C125.3; `has_pick` is **unchanged** (D-L2-36); `songs.FilterSQL` implements them (they apply to the subject take). `search_sounds` inherits them (it accepts every `list_songs` filter). `songs.ListFilter` gains `PickDecided, Unheard, MinDurationS, MaxDurationS, MinStars, Unrated, Tags, Lossy, RecipeKnown, InRecord, CrateID`.
2. **M1/L1 `search_tracks` / `library.SearchParams` (C19, C98):** new parameters `unheard`, `in_record`, `sound` (explode, `facet:value`, over `take_sounds`), `crate_id`; every join is gated by `hasTable("records")`/`hasTable("crates")` (the D-M3-35 pattern), so `FixtureV2`…`FixtureL1` still build and read unchanged (`TestLibrary_OldSchemaFixturesStillRead` extended).
3. **L1 `get_song` (`SongView`, C90.6):** gains `records: [{id, title, status, mode}]` (≤ 10, live slots of non-hidden Records, newest first) and `crates: [{id, name}]` (≤ 10, manual Crates holding the Song). `get_muse` gains `catalog: {album_id, neutral_name}|null` (the Muse's auto-album id and its neutral name; **never the opted-in name**; null while the Muse has no listed album). Both are additive; no Muse text enters either.
4. **L1 `/context` (C98):** `trackctx.Bundle` gains `Curation`, serialised after `sounds`: `curation {records: [{id, title, status, position, mode}] (≤ 6; slots whose resolved Take is this Take), crates: [{id, name}] (≤ 6; manual Crates holding this Take or its Song)}`, `null` when both are empty or the services are absent (`trackctx.CurationReader`, nil-safe). `Fit` trims `curation.crates`, then `curation.records` (6 → 3 → 0), **before** L1's `song.siblings` step; names recorded in `_budget.truncated`; goldens re-baselined with a reviewed diff. `next_actions` is unchanged (Muse- and Record-aware actions are L3).
5. **L1 `get_stats` (C98):** gains `curation: {records: {total, draft, proposed, published}, crates: {manual, smart}, tapes: {queued, running, ready, failed}, opt_ins: {export_muse_names, subsonic_muse_names}, pending, review: {undecided, pin_ineligible}}` (`pending` = slots with unconsumed Pick events; `review` counts live slots of **published** Records whose Pick is `newest`/`stars`, or whose pin was discarded, so the operator sees what entered a published Record without a human look; plain `COUNT` SQL in `curation.Service.Stats`, lane A).
6. **L1 `muses.Service` (C89):** gains `Ordinals(ctx) (map[string]MuseMeta, error)`, `Version(ctx) (string, error)` (`COUNT(*)` and `MAX(updated_at)` of `muses`) and `Labels(ctx, ids) (map[string]string, error)` (the `albums.MuseDirectory` implementation; `Labels` is never reached when the flag is off) — in `internal/muses/directory.go`, an A2-owned file in an L1 package (`Ordinals` reads the frozen `muses.ordinal`, D-L2-37); the batch `Refs` already exists in `internal/muses/ref.go`.
7. **L1 C87/C99:** `TestImportGraph_L1`'s banned-source list gains `recordview`/`albums`/`tape`/`liner`/`cover`/`crates`/`records`/`curation` as defined in C118; `TestMuseText_OnlyInAllowlistedOps` gains `get_record` (C127.4).
8. **M1 C18/C16.7 (media):** `png` is already a media extension and `serve.Chain` is already variadic and nil-safe (M4a); F2 appends the tape resolver to the chain in `app/l2.go` and adds `derived/tapes` to the importer's `cacheDirs` (`derived/covers` and `derived/dl` are already there); backups never walk `derived/` (nothing to exclude); `cue` joins `serve` only if the T1 spike requires the sidecar (T2).
9. **M0:** `mediatool.RunOpts.Stdin`; CLI `library verify` runs `records.Verify` after the L1 invariants (and `--files` checks tape files), `library reconcile` runs the Syncer `Settle` after the provenance `Settle`, `--dry-run` prints slots with pending events.
10. **M2a C27/C41/C42, M3 C81/C85/C86:** `TestNoDeleteSQL` allowlist unchanged; routing contract and coverage gain the L2 paths; `servertest.Options.L2`, parity rows, fixtures (C132); op count 109 → 126.
11. **Batch readers for `recordview` (V2-owned additive files; signatures frozen by F2):** `sessions.Service.Refs`, `sounds.Service.FamiliesOf`, `songs.Service.Briefs`, `picks.Service.Current`, `ears.Loudness` (C124.2), and `textfold.Slug` (F2).
12. **L1 C89 / `muses` (D-L2-37):** 0010 adds the frozen `muses.ordinal` (backfilled by rank, assigned by trigger); `MuseDirectory.Ordinals` reads it from `internal/muses/directory.go`; recorded in the L1 plan by X1 with `TestMuses_OrdinalAssigned` and `TestMigration0010_MuseOrdinalBackfill`.

### C131. Web (`web/`; amends M2a C40, M3 C84, L1 C100, M4a C115 and the UI redesign shell)

**Placement decision (D-L2-38).** Records and Crates are **Library sub-views**, not More rows and not new tabs. A Record is a thing you *listen to* in order and a Crate is a pile of Takes or Songs you
browse, judge or remake from — the same job as Takes, Sounds and Playlists, which already share the Library tab's `SubNav` pill row. Make (More) is for generating and the Board; Judge is one-at-a-time
evaluation and borrows the player. Two things stay under More because they are *settings*, not browsing: the read-only `/settings/curation` (Settings group) and the human-only `/human/curation` (Secure group,
behind `SecureGate`). The Record publish page is a secure route reached from a Record, not from a tab.

**C131.1 Shell integration (F3 owns these files; nobody else edits them).**
- `lib/router.ts` `APP_ROUTES` (specific before parametric): `/records` → `records`, `/records/:id` → `record`, `/crates` → `crates`, `/crates/:id` → `crate`, `/settings/curation` → `settings-curation`,
  `/human/records/:id/publish` → `human-record-publish`, `/human/curation` → `human-curation`. All are SPA routes: `/human/*` pages are in-app (only `/human/api` is server-owned), so no L2 page is a full-page load.
- `lib/nav.ts`: `TAB_OF` — `records`, `record`, `crates`, `crate` → `library`; `settings-curation`, `human-record-publish`, `human-curation` → `more`. `NAV_LINKS` — `{Records, /records, library}` and `{Crates, /crates, library}` between
  Sounds and Playlists (so the Library row reads Takes · Sounds · Records · Crates · Playlists; `SubNav` already scrolls horizontally), `{Curation, /settings/curation, more, group Settings}`,
  `{Curation, /human/curation, more, group Secure, secure: true}`. `subActive` gains the two cases; `TITLES` gains `Records`, `Record`, `Crates`, `Crate`, `Curation`, `Publish`; `isTabRoot` gains `records` and `crates`;
  `CONTEXTUAL_ROUTES` gains `record`, `crate`, `human-record-publish`. `navTabs.test.ts` (one tab per route, every `NAV_LINKS` path routable) is the guard; `ux-shell.spec.ts` already walks `NAV_LINKS`.
- `lib/scrollRestore.ts`: `READY_ROUTES` gains `records`, `record`, `crates`, `crate`; each of those routes calls `routeReady()` once its first data has loaded, so Back restores the scroll position (the router owns scroll; a route must never call `scrollTo` itself).
- `App.svelte`: one `{:else if}` branch per route; `record`/`crate`/`human-record-publish` use `{#key $router.params.id}`; the two secure pages are wrapped in `<SecureGate>` like their siblings. F3 ships each page as a **placeholder component in the lane-owned file** (`routes/Records.svelte` etc., a heading and a "not built yet" line), so W1–W3 only fill files they own.
- No new tab, no new icon unless a lane needs one (`GripIcon`, `UpIcon`, `DownIcon`, `PlayIcon`, `QueueAddIcon`, `CloseIcon`, `LockIcon` exist); no colour literal outside `styles/tokens.css`; dark only (no theme switch, no `prefers-color-scheme` branch); every interactive element is ≥ `--hit` (44 px) on touch (`smallTargets` check in the specs); motion respects `prefers-reduced-motion`.

**C131.2 Records (`routes/Records.svelte`, `routes/Record.svelte`; W1).** *List:* `list_records` with status chips, search, cover thumbnails (`RecordCover` over `api.CoverLinks`, never `CoverArt`, which is track-id based), a **New Record** form (`create_record`, idempotency key through `api/idem`'s `idempotentAction`), phone cards ≥ 64 px high. *Studio* (`/records/:id`): header = cover ("Re-roll" ⇒ `update_record{cover_reroll}`), inline-edit title/subtitle/notes (`update_record` + `revision`), status chip, hide toggle; **slot list** (`SlotRow`): drag handle (`GripIcon`, pointer events, desktop) with ▲/▼ buttons (`UpIcon`/`DownIcon`, always present, keyboard-operable, the primary control on touch; either ⇒ one `reorder` op), the Song title (link to `/songs/:id`), the resolved Take card (title, duration, `lossy` badge, stars, play button on the player), a **Floating/Pinned** segmented control (Pin = `pin` with the current Take or one chosen from a menu of the Song's Takes; Unpin), a `pin differs from the Pick` badge, `pin_ineligible` / `empty` / `retired_song` / `undecided_pick` warnings, side select (`A`–`D`), remove; **add Songs** (`SongPicker`: typeahead over `list_songs?q=`, "from Crate…" select, or `?add=<song ids>` from the Board); actions: **Play** (queue, C131.5), **Add to queue**, **Play as tape**, **Freeze** (`pin_all`) / **Unfreeze**, **Propose** (note dialog ⇒ `propose_record`), **Publish…** (an in-app link to `/human/records/:id/publish`, shown only to humans), **Export tape…** (published only). In-page tabs (a segmented control, not routes): **Slots**, **Changelog** (`get_record_changes`, newest first, `text` verbatim, from/to Take links; filtered by slot when one is selected), **Liner notes** (the `liner.markdown` preview rendered as text, with a "what an export would say" toggle explaining the export audience), **Energy** (`EnergyCurve.svelte`: one polyline per slot on a shared time axis, mean-LUFS ticks, BPM labels; table fallback for a11y), **Tapes** (`TapePanel`: start `export_record_tape` (a human sees an "Accept N warnings" confirmation ⇒ `accept_warnings`; any other viewer sees the 409 `TAPE_NEEDS_REVIEW` text), poll `get_tape` every 1 s while `queued|running` (≤ 15 min), phase/progress text, plan table, chapters with offsets, the lossy banner, the download link). **Concurrency:** every edit sends `revision`; a 409 `STALE_REVISION` shows a banner "Changed elsewhere — Reload" and refetches; edits on a published Record by a non-human viewer are not offered (`viewer.canEditPublished`, from `lib/viewer.ts`). When `pending` is true the page refetches `get_record` after 1.5 s, at most three times.
**Publish page** (`/human/records/:id/publish`, `SecureGate`, `routes/HumanPublish.svelte`): shows what leaves the app — the export-audience liner preview, the **neutral album-name policy** ("Subsonic clients see this Record as '<title>'; automatic albums are named `Inspiration NNN` unless you enable names in Curation settings"), the warnings (`pin_ineligible`, and `undecided_pick` with a **Freeze** shortcut = `pin_all`), the **tape preview** (chapter names, export-liner Song titles and tags exactly as the tape would carry them, from `get_record include=preview`, D-L2-27), the slot count, and a confirmation; **Publish** / **Unpublish** call `publish_record` with `revision` and then `router.navigate` back to the Record.

**C131.3 Crates (`routes/Crates.svelte`, `routes/Crate.svelte`; W2).** *List:* `list_crates?counts=true`; **New Crate** (manual or smart, subject Song or Take). *Crate page:* manual ⇒ members through `list_songs?crate_id=` / `search_tracks?crate_id=` (cursor paging; per-row remove — no multi-select, D-L2-41), **Add to Record…** (`AddToRecord`: `create_record` or `edit_record_slots add`), **Add to Crate…**; smart ⇒ the **filter editor** (`FilterBuilder.svelte`): one control per vocabulary key, a **live count** (debounced 300 ms `list_*?limit=1` with the same mapper — the same Go code answers, `total` is the count), **Save** ⇒ `create_crate`/`update_crate` (which also return the count) and a preview list. Hidden Crates only behind a toggle. **`crateFilter.ts` cannot read OpenAPI at runtime** (`schema.d.ts` is types only): it holds an explicit field table typed `satisfies` the generated `operations["list_songs"|"search_tracks"]["parameters"]["query"]` keys, so a new op parameter is a compile error until it is added to the table or to the non-saved set, and a bun test reads `openapi.json` and compares key sets (the TypeScript twin of `TestCrateVocabulary_EqualsListOps`).
**`AddToCrate.svelte`:** a popover following `AddToPlaylist.svelte` (manual Crates of the matching subject, typeahead, "New Crate…" inline) ⇒ `edit_crate_items`; one toast with `added/already`. Entry points (D-L2-41): **Song** page (chips "In Records"/"In Crates", `AddToCrate`, `AddToRecord`), **Track** page (`AddToCrate` subject take, "In Records" chips from `get_track_context.curation`), **Board** (its existing selection and phone `selectMode` action bar gain **Add to Crate…** and **New Record from selection…** ⇒ `create_record{song_ids}` then navigate). The Library list gets no selection UI.

**C131.4 Curation settings (W3).** `/settings/curation` (read-only, `get_curation_settings`; More → Settings) lists both opt-ins with their effect in plain words and links, in-app, to `/human/curation` (`SecureGate`; toggles ⇒ `set_curation_settings` with only the changed key; copy: "Off: Subsonic apps and tape files never contain an inspiration's name. On: …"; `subsonic_muse_names` says it has no effect until automatic albums are enabled, while A2/S2 are unbuilt). Agents see the booleans through `get_curation_settings` and `get_stats`.

**C131.5 Playback (W3 is the only lane that edits `lib/player.svelte.ts` and `lib/playerStore.ts`; D-L2-42).** `lib/recordPlayer.ts`: `playRecord(view)` builds the queue of resolved take ids (skipping empty slots) and a `gains` map `take_id → gain_db` and calls `player.play(first, queue, { title, album: record.title, source: { label: record.title, href: "/records/" + id }, undo: true, gains })`. `PlayOptions` gains `gains?: Record<string, number>`; the player keeps the map in memory only (**not persisted**; a restored queue whose `source.href` matches `/records/:id` re-fetches `get_record` and rebuilds it) and applies `gains[id]` through **one** `GainNode` on the shared `getAudioContext()`: the `MediaElementAudioSourceNode` of the single `<audio>` is created lazily on the first gained play (no map ⇒ no node and the element is not routed through Web Audio; once created it stays in the graph at unity gain for the page's life), `setValueAtTime` at track start, the `audio.volume` path is never used for matching (iOS). The existing next-track prefetch and `ended` handoff are kept (best-effort gapless, D-L2-20; the measured seam goes in the device checklist). **Play as tape** enqueues one `tape:<tape id>` item (`export_record_tape` first when no `ready` tape exists for the current revision; the URL comes from `get_tape.download.url`), shows the chapter list from `get_tape.chapters` under the player (selecting one seeks to `start_ms`), and takes the single `<audio>`; a tape item is not restored after a reload. The screen-locked iOS behaviour of the gained path is checked in DC-L2-2; if it fails, **Play as tape is the supported iOS path** and the SPA offers only that for matched playback there. `singleAudio.test.ts` must stay green (no second `<audio`).

**C131.6 View-model modules (`web/src/lib/`, each with a bun test).** `recordView.ts` (slot ordering and the single `reorder` op, `pin`/`unpin` availability, badge and warning text, `changeText` mirror of C122.3 tested against the shared golden `lib/fixtures/change_text.golden.json` that the Go test writes with `-update`, `canEdit(viewer, record)`), `crateFilter.ts` (field table, filter ⇄ query string ⇄ form model, non-saved set, conflict-free merge preview), `tapeView.ts` (poll policy, progress text, plan table, lossy banner, chapter list), `energy.ts` (downsampling check, polyline path, shared axis), `recordPlayer.ts` (queue + gains, tape chapter seeking), `curationApi.ts` (typed wrappers over the generated client; `idempotentAction` for the three create ops; 409 mapping). No `fetch` outside `web/src/api/` (the ESLint ban stays). New list routes use `routeReady()`; none touches `history` or scroll directly.

**C131.7 Stable test ids.** `records-list`, `record-row`, `record-new`, `record-studio`, `slot-row`, `slot-up`, `slot-down`, `slot-pin`, `slot-warning`, `record-tab-<name>`, `record-changelog-row`, `tape-panel`, `tape-download`, `record-publish`, `crates-list`, `crate-row`, `crate-filter-builder`, `crate-live-count`, `add-to-crate`, `curation-toggle-<key>`; the Library `SubNav` links are found by role and name.

### C132. Tooling, fixtures, CI, performance, docs (amends M2a C41/C42, M3 C85/C86, L1 C101, M4a C116/C117)

**C132.0 The agent loop (binding for every lane; `CLAUDE.md` in the worktree is authoritative).** An agent iterates with **`make verify`** (only what its checkout changed versus `origin/main`: gofmt, vet, lint, `go test -race -short` over the changed packages and their reverse dependencies, web checks if `web/` changed, the openapi drift check, the incremental `make gate`, and the Playwright specs mapped from the changed files by `scripts/e2e-map.txt`), plus **focused package tests** (`go test ./internal/records/ -run TestEditSlots`; a single-package `-race` run is allowed; any other `-race` run and every Playwright run goes through the lock-taking shims **`scripts/go-test`** and **`scripts/pw`**; whole-repo suites only through `make`). A lane runs **only the Playwright specs its change affects** (`MM_E2E_SPECS`/`make verify` mapping), never the whole matrix. **The full `make test`, `make test-long` (only if jobs/acceptance/`TestAcceptance_` packages were touched), `make e2e` and `make perf` are run once by the integration lanes or left to GitHub CI** (6 Go race shards balanced by `scripts/ci-shard-weights.txt`, the perf job, 6 e2e shards, the coverage job with `served-check`, the history gate). Every lane ends with `make gate` green (incremental; CI and the pre-push hook run `gate-full`), `make openapi` producing **no diff** (the API surface is frozen after F2), and its PR description listing the plan-document addenda X1 must apply. A new Go package that takes ≥ 15 s in CI gets a line in `scripts/ci-shard-weights.txt` (I1 owns that file; lanes report timings in their PR).

**C132.1 e2e conventions for L2 specs (INT-W writes them; lanes never add a spec file).** Every `web/e2e/l2-*.spec.ts` imports `test`/`expect` from `./fixtures` (never `@playwright/test`), starts with a `// matrix:` header (`@mobile` for the Records studio, Crates and touch targets — the shell is a phone layout; `@firefox` where the audio element matters, i.e. the tape and gain playback specs; `chrome-only because <why>` for pure data flows) and creates and resets its own data through an API helper `web/e2e/l2Api.ts` (the worker server is shared by the spec file only). `scripts/e2e-map.txt` (I1) maps `web/src/routes/Record*.svelte`, `web/src/lib/components/{SlotRow,TapePanel,…}` and `web/src/lib/{recordView,recordPlayer,tapeView,energy}.ts` to `l2-records`/`l2-tape`, the Crate files to `l2-crates`, the settings files to `l2-curation`; `served-check` requires every L2 op with a UI route to be served to the SPA by some shard.

**C132.2 Fixtures.** (Signatures follow the shipped helpers: `FixtureL1(tb, st, now)`/`ApplyL1`.) `librarytest.FixtureL2(tb, st, now)` / `ApplyL2` on top of `FixtureL1` (which tests `Settle` first) — **SQL-only, written by F1** (no services, no Syncer): the seven Songs S1–S7 of C125.5 laid out in the **fixture table** below; Record **R1** (published; 5 slots in this order: floating A, floating B, **pinned** C to a non-Pick Take, an empty slot on a not-yet-rendered draft Song D, floating E; the letters are aliases for Songs in the table), Record **R2** (an agent-created draft, 2 slots: S1 and S6), Records' changelogs as the ops would have written them; Crates: manual Song crate (3 members), manual Take crate (4), smart Song crate (the C125.5 filter), smart Take crate (`{"status":["draft"],"unheard":true}`); synthetic loudness `analyses` rows (integrated/true-peak/`short_term_1hz`) so the plan and energy curve have numbers without media tools. `librarytest.FixtureL2Audio(tb, st, data)` (**media tier, T1**) adds real audio through the existing synth: three 48 kHz/24-bit stereo FLACs at about −18, −14 and −10 LUFS, one 44.1 kHz/16-bit FLAC (resample path), one mono FLAC (upmix path) and one MP3 (lossy path), with matching loudness analyses measured by the real analyzer. `FixtureL2Mirror(tb, st, now, params)` (INT-B) builds the acceptance mirror on `FixtureL1Mirror` (`Originals 4, Groups 5`, values chosen unlike the real library) plus Records over its Songs; `FixtureL2Scale(tb, st, now, songs, muses, records)` (F1) writes rows only (no audio) for performance tests. `FixtureL2Upgrade(tb, st, now)` (F1) builds the data at schema 9 for `TestMigration0010_UpgradeFromMain`. Existing fixtures keep their row counts; all names are synthetic (`TestFixtures_L2_SyntheticOnly`). Tests that change Picks call `rig.Settle(t)`.

**C132.2a `FixtureL2` table (binding; the C125.5 filter results and the R1 acceptance scenarios both read it).** `Settle` runs first; Muses `M1`, `M2`; "listens" = human/device `start` events; loudness = integrated LUFS / true peak dBTP (synthetic `analyses` rows).

| Song | Muse | Takes (id: duration) | Listens | Pick (take, source) | Sound | Loudness | Slot |
|---|---|---|---|---|---|---|---|
| S1 | M1 | `s1a`: 300 s | 0 | `s1a`, `newest` | reggae | −14.0 / −3.0 | R2 slot 1 |
| S2 | M1 | `a1`: 300 s | 1 (device) on `a1` | `a1`, `newest` | reggae | −15.5 / −2.0 | R1 slot 1 = **A**, floating |
| S3 | M1 | `b1`: 180 s (newer), `b2`: 180 s | 0 | `b1`, `newest` | reggae | `b1` −13.0 / −1.5; `b2` −14.2 / −3.0 | R1 slot 2 = **B**, floating |
| S4 | M2 | `c1`: 300 s, `c2`: 240 s | 0 | `c1`, `stars` (human 4★) | dance | `c1` −14.5 / −3.0; `c2` −11.8 / +0.6 | R1 slot 3 = **C**, **pinned to `c2`** (not the Pick) |
| S5 | M2 | `e1`: 300 s, **lossy** (MP3) | 0 | `e1`, `pinned` (human Song-level pin) | reggae | −14.0 / −3.0 | R1 slot 5 = **E**, floating |
| S6 | none | `s6a`: exactly 240 s | 0 | `s6a`, `stars` | reggae | −14.0 / −3.0 | R2 slot 2 |
| S7 | none | no take (draft Song) | – | none | reggae (declared) | – | R1 slot 4 = **D**, floating, empty |

So the fixture has 7 Songs and 8 Takes; R1's slots A, B (undecided `newest`), C (pinned, differs from the Pick), D (empty) and E (decided) exercise every `ResolveSlot` mode; `auto-albums` (A2, if built): M1's album holds only `s1a` (S2 and S3 are in the published R1), M2's album is empty and unlisted, `unf` holds `s6a` (R2 is a draft). **Seams for tests that change Picks** (L1 code is read first by R-b and INT-B; exact helper names are those of the merged L1 and are listed in each test file's header): decided source `ladder` is produced by calling `picks.Service.RecomputeTx` with an **injected `map[string]ladder.Strength`** (L1 C-picks) together with counted human blind verdict rows from the L1 duel fixture helper, `stars` through the M2a rating service, `pinned` through L1's Song-level pin service, `newest` by adding a Take. The acceptance flip of Song B is: four counted human blind verdicts `b2` over `b1` (so `IntraDuels = 4 ≥ LadderMinDuels = 3`) and strengths `b1 {u −0.8, sd 0.9}`, `b2 {u +0.8, sd 0.9}` (`u₁ − u₂ = 1.6 ≥ 0.5·√(sd₁² + sd₂²) ≈ 0.64`, i.e. z ≥ `LadderChallengeZ = 0.5`), so the Pick moves `b1` → `b2` with source `ladder` and evidence `duels_n 4`.
**C132.2b Rigs.** `servertest.Options` gains `L2 bool` (adds `FixtureL2`; requires `L1`), `ManualSync` (default true: the Syncer and tape runner are built but idle), `RealTape` (media tier: the production `Pipeline`; default is `tapetest.FakePipeline`, which returns a small deterministic FLAC and the planned chapters). `Rig.Settle` runs the provenance `Settle` then `Syncer.Settle`; `Rig.Tapes.RunOne(t)` processes one queued tape. The parity runner gets **15** `ParityCase` rows (`internal/mcpgen/parity_l2_test.go`); `publish_record` and `set_curation_settings` have REST-only tests (`TestPublish_*`, `TestSettings_HumanOnly`, `TestOps_HumanOnly_NoMCP_L2`); the `every_ui_action` script (`internal/acceptance/every_ui_action_test.go`) gets entries for the **15** ops that have a UI route **and** an MCP face (all MCP-faced L2 ops).

**C132.3 CI and coverage (I1).** There are no `go-fast`/`go-media` tiers any more: every package is in one of the 6 Go shards (`scripts/ci-go-test.sh`, weights in `scripts/ci-shard-weights.txt`), the media tier runs with `MM_REQUIRE_MEDIA_TOOLS=1` (tape pipeline, `FixtureL2Audio`, `TestFlacmeta_*` real-file tests and the ffmpeg/flac argv-integration tests need no special list), `TestPerf_*` run in the CI `perf` job (also `make perf`), Playwright runs in 6 e2e shards with the `// matrix:` placement of each spec (`l2-*.spec.ts`), the `coverage` job merges unit + e2e coverage and runs `served-check`. `coverage.thresholds` gains: `internal/records`, `internal/tape`, `internal/albums` ≥ 95 %; `internal/crates`, `internal/liner`, `internal/curation`, `internal/recordview` ≥ 90 %; `internal/media/cover` stays ≥ 90 % (it already is), `internal/media/flacmeta` unchanged floor; a new `internal/archtest/coverage_thresholds_l2_test.go` (the L1 test file is not edited) asserts the file names every L2 package. `.github/workflows/weekly.yml` gains the job **`curation-property`** modelled on `provenance-property`, with `MM_PROPERTY_SEQS: "300"` (the test reads `os.Getenv`, default 60; the per-PR jobs leave it unset) — a scope-valve cut candidate (D-L2-29). New CI-relevant packages ≥ 15 s get a weights line (a CI check fails without one).

**C132.4 Performance targets** (`TestPerf_*`: they run in the CI perf job and `make perf`, not in the race shards) (asserted through the env-scaled budget helper — `MM_PERF_FACTOR`, default 1, ×3 under `-race`; never a hard-coded wall-clock literal): `TestPerf_RecordGet` — 60 slots with `include=liner,energy,changes,tapes`, server p95 over 20 runs **< 100 ms**; `TestPerf_Syncer` — 200 Records × 20 slots (4 000 slots) and 500 Pick events: first `Settle` **< 1 s**, second **< 20 ms**; `TestPerf_AlbumCatalog` — 1 000 Songs, 200 Muses, 30 Records: `List` (all kinds) and `Get` of every album, p95 **< 150 ms** per call; `TestPerf_SmartCrate` — 10 000 Songs, the C125.5 filter, count + first page **< 300 ms**; `TestPerf_CoverRender` — 512 px **< 60 ms**, 1024 px **< 250 ms** CPU; `TestPerf_GainApply` — `ApplyGain` ≥ 100 MSamples/s (one hour of stereo 48 kHz = 346 MSamples in < 3.5 s); `TestPerf_TapeRender` (media tier) — 12 synthetic items × 240 s **< 60 s** wall and Go heap in use < 64 MiB at the peak (streamed, never buffered); a bun test builds the 60-slot Record view model in < 20 ms.
**C132.5 Docs.** `docs/architecture.md` gains "Curation" (the lifecycle table of "Why this model", nouns, publish gate, neutral naming and opt-ins, the tape pipeline, what the catalog is); `docs/runbook.md` gains "Publishing and unpublishing", "Tapes: disk, retention and a failed export", "Curation opt-ins", "After a restore (Record sync, tapes are gone)"; `docs/device-checklist.md` gains DC-L2-1 (open one real tape in three of the operator's own players and record which show chapters, which encoding they read, and the loudness feel at the seams), DC-L2-2 (play a Record in the SPA on a phone and desktop: loudness matched, seam length — logged, not asserted; **on iOS also lock the screen and background the app during a gain-matched Record queue: it must keep playing through at least three track changes on the `MediaElementAudioSourceNode` path, otherwise "Play as tape" is the supported iOS path**, D-L2-20), DC-L2-3 (edit a published Record from a paired phone; confirm publishing needs a web session), DC-L2-4 (deferred to M4a: a Subsonic client shows the published Record and neutral auto-album names). The public plan copy `docs/plans/<date>-l2-library-curation.md` is the text above the private marker (X1, `scripts/l1-public-copy.sh` reused).

---

## Acceptance mapping (concept §7 L2 and design items → named tests)

| Concept / design item | Named test(s) |
|---|---|
| **A Pick change upgrades floating slots and logs it** | `TestAcceptance_L2_PickUpgradesFloatingSlot` (rig on `FixtureL2`: R1 has floating slots on Songs A, B, E; four counted human blind verdicts plus the injected `ladder.Strength` of C132.2a switch B's Pick `b1` → `b2` by ladder; after `Rig.Settle` the resolved Take of B's slot is `b2` **and** the changelog has exactly one new row `slot_upgraded` (from `b1`, to `b2`, reason `ladder`, evidence `duels_n 4`) whose text is `Slot 2 upgraded: <b1 title> → <b2 title> (won 4 blind duels)`; A and E unchanged; the pinned slot C and the empty slot D have no new row) · `TestSyncer_Table` (S01 first Pick on an empty floating slot ⇒ `slot_filled`; S02 `ladder` upgrade; S03 `stars`; S04 `newest`; S05 same Take, source change only ⇒ no row, `synced_seq` advanced; S06 Pick emptied ⇒ `slot_emptied`; S07 two events in one pass ⇒ two rows in order; S08 pinned slot ⇒ no row and `synced_seq` untouched; S09 unpin ⇒ `synced_seq` = the Song's max `seq`, no historic rows; S10 events before the slot was added are never logged; S11 Song retired into another ⇒ `slot_song_followed`; S12 follow target already in the Record ⇒ `slot_removed` reason `duplicate` **and `order_json` rewritten in the same transaction** (`Verify` R1 clean, `revision` unchanged); S13 a hidden Record is synced; S14 re-run ⇒ 0 rows) · `TestSyncer_Idempotent` · `TestSyncer_TwoSyncers` (two `Syncer`s on one DB file settle concurrently, a hook forces one `SQLITE_BUSY` per pass that the retry absorbs: every event produces exactly one row) · `TestEditSlots_CatchesUpFirst` (a pending event is logged **before** the edit's own row; `seq` order proves it) · `TestChangeText_Golden` (24 rows) · `TestProperty_RecordInvariants` (changelog replay equals state) |
| **Pinned slots never change** | `TestAcceptance_L2_PinnedNeverChanges` (slot pinned to take `p`: new better takes, a Song-level pin of another Take, ratings and duels that flip the Pick, discarding `p` — the resolved Take stays `p`, no `slot_upgraded` row is ever written, `p` discarded ⇒ state `pin_ineligible` + warning and still `p`, the catalog omits that slot and a tape export is refused 409 `TAPE_NEEDS_REVIEW` unless a human accepts; unpin ⇒ the slot follows the Pick and logs `slot_unpinned`) · `TestResolveSlot_Table` (R01–R14) · `TestEditSlots_Pin_Rules` (other Song's Take ⇒ 422 `TAKE_NOT_IN_SONG`; discarded ⇒ `TAKE_NOT_ELIGIBLE`; same pin twice ⇒ no row) · `TestEditSlots_PinAll_UnpinAll` |
| **Record lifecycle and slots** | `TestRecords_Create_Rules` · `TestRecords_Update_Rules` (revision lock, cover re-roll changes seed not palette key input, empty patch ⇒ 422) · `TestEditSlots_Table` (24 rows: add default/at position/pinned, duplicate Song ⇒ 409, retired Song ⇒ 409, 61st slot ⇒ 422, remove then re-add ⇒ new slot id, move/reorder permutation errors, op index in `details.op`, all-or-nothing rollback) · `TestRecords_OrderTrigger` · `TestRecords_PrincipalRules` (the C120.1 table: agent edits draft ✓, agent edits published ⇒ 403 `HUMAN_ONLY`, device edits published ✓, device publishes ⇒ 403) · `TestRecords_StatusPath` (legal and illegal transitions incl. `published → proposed` refused by trigger) · `TestRecords_PublishRules` (needs ≥ 1 playable slot; `first_published_at` set once; unpublish clears `published_*` and keeps `first_published_at`) · `TestRecords_PublishCheckOrder` · `TestRecords_PublishFalseFromDraft` · `TestRecords_ProposedEdit` · `TestOptSide_Decode` · `TestWhyText_Golden` (8 rows) · `TestSyncer_RunPollsOnlyWhenPending` |
| **Agents propose, a human publishes (human-only op)** | `TestAcceptance_L2_AgentProposesHumanPublishes` (go-sdk MCP client with a read+write PAT: `list_songs` ⇒ `create_record` ⇒ `edit_record_slots` ⇒ `propose_record` ⇒ `get_record` shows `status proposed` and a proposal note; `publish_record` is absent from `tools/list` and refused 403 `HUMAN_ONLY` over REST for the PAT; a human principal publishes; the agent's next edit ⇒ 403 `HUMAN_ONLY`; the changelog shows `created, slot_added…, proposed, published` in order and `get_audit` lists every step without titles) · `TestL2_PublishGate` · `TestL2_AgentTitleCanary` · `TestPublishPreview_EqualsSnapshot` · `TestPublish_NotInMCP` · `TestOps_HumanOnly_L2` |
| **Records' cover, liner notes, energy curve** | `TestCover_*` (C123) · `TestLiner_Build_Table` (17 rows) · `TestLiner_AppGolden` · `TestLiner_ExportGolden` · `TestLiner_ExportIgnoresPrivateFields` · `TestRecordView_Get` (shape, `gain_db`, `why`, warnings, `pending`) · `TestRecordView_SlotWhy` (golden row: a never-changed slot has a why from its current Pick) · `TestRecordView_LinerAudience` · `TestRecordView_EnergyBudget` · `TestRecordView_PickChangeKeepsCoverKey` |
| **Crates + smart Crates** | `TestAcceptance_L2_SmartCrate` (C125.5: ids `{S1, S6}`, count 2, equal through `list_songs`, `crate_id` and the Crate's own count) · `TestCrateVocabulary_EqualsListOps` · `TestCrates_CreateRules` (manual/smart × song/take, filter errors `UNKNOWN_FILTER_KEY`/`INVALID_FILTER`/`FILTER_TOO_LARGE`, no `crate_id` inside a filter, an empty array inside a stored filter ⇒ 422 `INVALID_FILTER`) · `TestCrates_EditItems` (add/remove/already/not_member, re-add creates a new row, subject mismatch, `CRATE_IS_SMART`, `CRATE_FULL`, retired Song) · `TestCrates_Expand_Merge` (conflict ⇒ `FILTER_CONFLICT`, list intersection) · `TestCrates_RetiredSongFollowed` · `TestSongs_PickDecided` · `TestSongs_UnheardTable` · `TestSongs_SubjectTakePredicates` (a Song with no take never matches a duration/star/tag predicate) · `TestSearchTracks_L2Filters` · `TestCrates_CountsOnRead` |
| **Record albums over Subsonic (additive); catalog membership and token — D-L2-34/35 revised** | `TestAcceptance_L2_SubsonicRecordAlbum` · `TestSubsonic_RecordAlbum_*` · `TestSubsonic_LineageUnchanged` · `TestSubsonic_NoMuseText` (C126.7) · `TestAcceptance_L2_AlbumCatalog` (Go-level, on `FixtureL2`: published R1 listed as `rec-<id>` with its listable slots in order and discs by side; slot C is listed only while its pin is eligible; the agent draft R2 absent; unpublishing R1 changes the Token) · `TestAlbums_MembershipTable` · `TestAlbums_TokenStable` (changes on publish, unpublish, a Pick event, a Song hide, a flag change, a Take title edit, a Take discard, a Song title edit; not on an unrelated rating) · `TestAlbums_TokenNoDirectoryScan` · `TestAlbums_Golden` (`albums_fixture.golden.json`) · `TestAlbums_StatementCount`; **A2/S2 only if built:** `TestAlbums_NeutralNames` · `TestAlbums_MuseLabelsNotCalledWhenOff` · `TestAlbums_OptInNames` · `TestMuses_Directory` · `TestMuses_OrdinalAssigned` (canary Muse: never in an album name or a `/rest` byte with the flag off) |
| **Tape export verified (chapters, loudness, gapless, lossy label)** | `TestAcceptance_L2_TapeExport` (media tier, `FixtureL2Audio`; V1–V8 of C128.6 in one test with named subtests) · `TestPlanGains_Table` (G01–G17) · `TestApplyGain_Table` · `TestAutoTarget_Table` · `TestCuesheet_Block_Golden` · `TestFlacmeta_Rewrite_Golden` · `FuzzRewrite` · `TestPipeline_ArgvGolden` (the exact decode, encode and test argv, literals plus app paths only) · `TestPipeline_GainStage_BoundariesAndGapless` (frames split across chunk boundaries; byte-identical to a one-shot conversion) · `TestPipeline_ClippedAborts` · `TestPipeline_SampleMismatch` · `TestTape_ExportRules` (not published ⇒ 409; stale revision ⇒ 409; empty ⇒ 422; too long ⇒ 422; `LookPath` failure ⇒ 503, unset `MM_FLAC` ⇒ default `flac` works) · `TestTape_ReviewGate` (undecided/ineligible slot ⇒ 409 `TAPE_NEEDS_REVIEW`; agent `accept_warnings` ⇒ 403; human ⇒ 201 with warnings recorded) · `TestTape_Quotas` (4th live tape of a Record ⇒ 409 `TAPE_QUOTA` scope `record`; 8 GiB ready ⇒ scope `total`; a reused key never blocked; `target_lufs` −14.04 and −14.0 ⇒ one key) · `TestTape_FlacVersionOnce` · `TestTape_Reuse` (same snapshot ⇒ same row, `reused`) · `TestTape_KeyTable` (what is and is not in the key) · `TestTape_Runner_States` · `TestTape_Recover` (expired lease ⇒ re-queued, third time ⇒ `TAPE_INTERRUPTED`) · `TestTape_Sweep` (rows to `expired`, files removed, orphan files removed) · `TestTape_LossyLabel_Table` · `TestTape_ChaptersVisibleToFFmpeg` (spike log, no assertion) · `TestPipeline_FFmpegFailureKillsEncoder` · `TestPipeline_EncoderDeathStopsFFmpeg` · `TestPipeline_NoGoroutineLeak` · `TestTape_DeterministicFile` · `TestTape_Preflight_DiskFull` (1.3 × PCM) |
| **Records, Crates and tapes drivable by agents and humans with parity** | parity runner (15 cases: every L2 op with an MCP face) · `TestParityEveryOperation` · `TestAcceptance_M2a_AgentPerformsEveryUIAction` (15 new entries) · `TestOps_RegisteredWithMetaL2` · `TestRegistry_OpCount` (126) · `TestOps_HumanOnly_NoMCP_L2` · served-operation coverage (zero unserved ui ops) · `TestToolsList_L2Ceiling` · `TestOps_BodyLimitsL2` · `TestOps_StubsAnswer501L2` (removed per lane) |
| **Muses never exported, logged, sent to Subsonic clients or committed (default); opt-ins explicit** | `TestImportGraph_L2` · `TestMuseText_OnlyInAllowlistedOps` (L2 cases; `get_record` the only addition) · `TestTape_SnapshotNoMuseByDefault` · `TestRecordView_SnapshotFlag` · `TestSettings_HumanOnly` (agent ⇒ 403; device ⇒ 403; human ✓; unchanged ⇒ no write) · `TestSettings_Visibility` (agents read the booleans) · `TestRecordOps_ErrorsCarryIDsOnly` · `TestMuseText_MutatingOpsLinerless` (five ops + the `idem` row) · canaries of C127.4 · `TestNoSecretLeak_L2` (extends `TestNoSecretLeak`: signed tape/cover links, canaries) · `TestFixtures_L2_SyntheticOnly` · E2E leak scanner (`/__e2e/leaks` = `[]`) · `make gate` |
| **One vocabulary, no stray `album`** | `TestNoAlbumWord_L2` (the word appears only in `internal/albums`, the FLAC tag writers of `tape`/`flacmeta`, tests and docs) |
| **Schema and immutability** | `TestMigration0010_Applies` (user_version 10; every C119 table, index, trigger exists; all L2 tables empty; `PRAGMA foreign_key_check` empty) · `TestMigration0010_UpgradeFromMain` (schema-9 fixture ⇒ unchanged rows) · `TestMigration0010_MuseOrdinalBackfill` · `TestMigration0010_FromSchema5` (0006…0010 in one boot) · `TestTriggers_L2` (table: DELETE on every table ⇒ error; frozen columns; `record_changes` chain/immutable/slot-of-record; `record_slots` pin-in-song, retired Song, follow-only `song_id`, `synced_seq` monotonic; `records` order consistency, status path, `first_published_at` once; `crate_items` manual-only and subject match, live-uniqueness; `tapes` state path, ready-frozen, `contains_lossy` check, one live key) · `TestSQL_WriteOwners_L2` · `TestNoDeleteSQL_AllowlistUnchanged` |
| **Invariants of the existing system still hold** | `TestRatingIsolation_StillHolds` (agent ratings never move a Pick, hence never a Record) · `TestImmutableMastersUntouched` (a tape export leaves every master byte-identical, mode 0444) · `TestL1_*` suites unchanged · `make test` |
| **Reconcile, verify, restore** | `TestVerify_L2_DetectsEachAnomaly` (one planted fault per R1–R9 in a scratch DB, guarding triggers dropped there) · `TestCLI_LibraryVerify_L2` · `TestCLI_LibraryReconcile_RunsSyncer` · `TestRestore_RebuildsRecordChangelog` (restore a snapshot, add Pick events, `Settle` ⇒ `Verify` empty; tapes absent and `expired` handled) · `TestProperty_RecordInvariants` (60 sequences; 300 weekly) |
| **Performance targets** | `TestPerf_*` (C132.4) |
| **Coverage gates** | `coverage` CI job (C132.3) |

---

## Lanes and tasks

Revision 3 cuts the work into **lanes**: each lane is one agent in its own git worktree (`../metamusic-wt/<lane>`, branch `l2-<lane>`), one reviewable PR (target **< ~2 500 changed lines excluding
generated `openapi.json`/`schema.d.ts`/goldens**), and owns a closed set of files. Lanes iterate with `make verify` and focused package tests (C132.0); the integration lanes run the whole suites.

### The order

```
Stage 0 (serial, each lands on main before the next starts)
   F1  migration 0010 + pure functions + SQL fixtures + guard tests
   F2  contract freeze: types, 17 registrations with 501 stubs, services' frozen method sets, wiring, rigs, openapi/schema.d.ts
   F3  web scaffold: routes, nav, scroll-ready list, placeholder pages, e2e-map/matrix placeholders    (needs only F2's schema.d.ts)
Stage 1 (all parallel; backend lanes start when F2 has merged, web lanes when F3 has merged)
   R-a  Records service: create/update/list, slot edits, principal and status rules
   R-b  Records changelog: Syncer, catchUpTx, Verify, CLI hook, property test
   V1   cover mosaic + liner (pure)
   V2   recordview + batch readers + records ops + amendments to get_song / /context      (needs R-a and V1 merged)
   K    Crates + the list-op predicates (list_songs / search_tracks / search_sounds)
   A    album catalog (record kind, token) + curation settings + get_stats.curation + settings ops
   T1   tape media layer: flacmeta writers (extracted from tagged), mediatool.Stdin, gain stage, pipeline, chapters, tags, spike
   T2   tape service layer: export/key/quota, runner, resolver, sweep, tape ops               (needs T1's Pipeline interface from F2; uses FakePipeline)
   I1   CI, coverage, weekly job, e2e-map, scripts, docs skeleton
   W1   Records pages        W2  Crates pages + entry points        W3  playback, curation settings pages   (web lanes; need F3)
Stage 2 (after A)   S1  Subsonic Record albums      [A2 and S2 optional, after A and S1]
Stage 3 (after every feature lane)   INT-B backend wiring + acceptance + leaks + parity     INT-W Playwright specs     (parallel)
Stage 4   X1 integration/public copy/docs/review     X2 production-copy check (operator-assisted)     X3 operator review
```

### Lane table

| Lane | Scope | Owns (closed file set; see "File ownership") | Depends on | Est. size (changed lines, tests included) |
|---|---|---|---|---|
| **F1** | migration `0010_curation.sql` (incl. `muses.ordinal`), `ResolveSlot`/`CoverSeed`/`ChangeText`/`WhyText`/`PlanGains`/`ApplyGain`, vocab tables, `FixtureL2`/`Scale`/`Upgrade`, guard tests | `internal/store/migrations/0010_*`, `internal/store/*_l2_test.go`, `internal/records/{record,params,resolve,changes,text,why}*.go`, `internal/tape/{plan,gain,params}*.go`, `internal/crates/{vocab,params}*.go`, `internal/library/librarytest/fixture_l2*.go`, `internal/archtest/*_l2_test.go` | operator gate P.5 | ~2 400 |
| **F2** | contract freeze and wiring (everything other lanes build against) | `internal/api/{api.go,types_l2.go,ops_*_l2.go (stubs),ops_m2c_test.go,ops_m3_test.go,ops_l2_test.go}`, `internal/*/service.go` constructors + `stub_<lane>.go` files, `internal/app/{app.go,l2.go,l2_<lane>.go}`, `cmd/metamusic/{cmd_serve.go,cmd_library.go,l2_hooks.go}`, `internal/importer/report.go`, `internal/textfold/slug.go`, `internal/servertest/**`, `internal/mcpgen/{parity_l2_test.go,m2a_contract_test.go,testdata/tools_list.golden.json}`, `internal/trackctx/curation.go`, `internal/songs/filter_types_l2.go`, the `SearchParams` fields, `internal/media/cover/{mosaic_types,stub_v1}.go`, `internal/liner/types.go`, `internal/media/flacmeta/types.go`, `openapi.json`, `web/src/api/schema.d.ts` | F1 | ~2 300 (+ ~3 500 generated) |
| **F3** | web shell scaffold | `web/src/{App.svelte,lib/router.ts,lib/nav.ts,lib/scrollRestore.ts}` (+ their tests), placeholder `routes/{Records,Record,Crates,Crate,SettingsCuration,HumanPublish,HumanCuration}.svelte` | F2 | ~500 |
| **R-a** | Records service (C120, C121) | `internal/records/{create,update,list,slots,edit,publish_rules}.go` + tests, deletes `records/stub_a.go` | F2 | ~2 300 |
| **R-b** | changelog, Syncer, Verify, CLI (C122) | `internal/records/{sync,verify}.go` + tests, `cmd/metamusic/l2_cli.go` + tests, deletes `records/stub_b.go` | F2 | ~2 000 |
| **V1** | cover mosaic, liner (C123, C124.1) | `internal/media/cover/{mosaic*,service_mosaic}.go` + tests + `testdata/cover_mosaic.golden.json` (one edit to `service.go`: shared write path), `internal/liner/{liner,render}.go` + goldens | F2 | ~1 700 |
| **V2** | recordview, batch readers, Records ops, `get_song`/`/context` amendments (C124.2–3, C129, C130.3–4) | `internal/recordview/**`, `internal/api/{ops_records_l2.go,ops_context.go}`, `internal/songview/{get.go,library_sections.go}`, `internal/trackctx/{trackctx.go}`, batch readers `internal/{sessions/refs,sounds/families,songs/briefs,picks/current,ears/loudness}.go` | R-a, V1 | ~2 500 |
| **K** | Crates and list-op predicates (C125) | `internal/crates/{service,list,items,expand,count}.go`, `internal/songs/filter.go`, `internal/library/tracks_l2.go`, `internal/api/{ops_crates_l2.go}` + the named parameter edits in `ops_songs_l1.go`, `ops_library.go`, `types_l1.go`, `types_m1.go` | F2 | ~2 300 |
| **A** | album catalog (record kind, token), curation settings and stats (C126.1–6, C127) | `internal/albums/**` (record kind), `internal/curation/{service,settings,stats}.go`, `internal/records/published.go`, `internal/api/ops_settings_l2.go`, `internal/library/stats.go` (curation), `internal/api/ops_stats.go` | F2 | ~2 000 |
| **T1** | tape media layer (C128.3–4) | `internal/media/flacmeta/{write,vorbis,cuesheet,picture}.go` + tests, `internal/media/tagged/write.go` (extraction only), `internal/mediatool/run.go`, `internal/tape/{pipeline,gainstage,chapters,tags,preflight*}.go`, `internal/library/librarytest/fixture_l2_audio*.go` | F2 | ~2 400 |
| **T2** | tape service layer (C128.1, C128.5–7) | `internal/tape/{service,export,runner,resolve,sweep}.go`, `internal/tape/tapetest/**`, `internal/api/ops_tape_l2.go`; `internal/media/serve/handler.go` only for a `cue` extension | F2 (T1 for the real pipeline at integration) | ~2 000 |
| **I1** | CI and docs plumbing (C132.3) | `.github/**`, `scripts/**` (incl. `e2e-map.txt`, `ci-shard-weights.txt`), `coverage.thresholds`, `Makefile`, `internal/archtest/coverage_thresholds_l2_test.go` | F2 | ~400 |
| **W1** | Records pages (C131.2) | `web/src/routes/{Records,Record,HumanPublish}.svelte`, `web/src/lib/{recordView,curationApi,tapeView,energy}.ts`, components `RecordCover, SlotRow, SongPicker, ChangeLog, LinerPreview, EnergyCurve, TapePanel, RecordStatus`, `web/src/lib/fixtures/change_text.golden.json` | F3 | ~2 300 |
| **W2** | Crates pages + entry points (C131.3) | `web/src/routes/{Crates,Crate}.svelte`, `web/src/lib/crateFilter.ts`, components `AddToCrate, AddToRecord, FilterBuilder, CrateMembers`, edits to `routes/{Song,Track,Board}.svelte`, `lib/boardView.ts` | F3 (reads W1's `curationApi.ts` only after it merges; W2 ships its own thin wrappers in `crateApi.ts`) | ~2 200 |
| **W3** | playback and curation settings (C131.4–5) | `web/src/lib/{recordPlayer,player.svelte,playerStore}.ts`, components `ChapterList, CurationToggles`, `routes/{SettingsCuration,HumanCuration}.svelte` | F3 | ~1 800 |
| **S1** | Subsonic Record albums (C126.7) | `internal/subsonic/**` (new files `records.go`, `records_test.go`; named edits to `ids.go`, `lists.go`, `browse.go`, `media.go`, `search.go`, `catalog.go`), `docs/subsonic.md` only via X1 | A (+ V1 for `EnsureMosaic`) | ~2 200 |
| **A2 / S2** (optional) | Muse auto-albums, `unfiled`, neutral names, opt-in effect; served by S2 | `internal/albums/{muse,naming}*.go`, `internal/muses/directory*.go`, `get_muse.catalog` in `ops_muses_l1.go`; S2: more of `internal/subsonic` | A, S1; P.5 box | ~1 800 / ~1 200 |
| **INT-B** | final wiring, acceptance, leaks, parity, property | `internal/app/l2*.go` (bodies), `internal/acceptance/*_l2*_test.go`, `internal/mcpgen/parity_l2_test.go`, `internal/api/ops_l2_leaks_test.go`, `internal/servertest/**` adjustments | all backend lanes | ~2 200 |
| **INT-W** | Playwright specs | `web/e2e/{l2-*.spec.ts,l2Api.ts}` | all web lanes + INT-B | ~1 200 |
| **X1–X3** | integration, public copy, docs, review; production-copy check; operator review | `docs/**`, `docs/plans/*`, release | all | small |

### File ownership and hot-spot rules

A file named under two lanes is a plan bug. **Files owned by F1/F2/F3 stay theirs**: a needed change goes back through this document and a fix commit on `main` that every lane rebases onto (small `fix(l2-freeze): …` PRs; the author is the owner of the file).

| Hot spot | Single owner | Rule for everyone else |
|---|---|---|
| `internal/api/api.go` (`New`, `Deps`), `internal/app/app.go`, `cmd/metamusic/{cmd_serve,cmd_library}.go` | F2 | F2 adds four `registerXxxL2` calls, the `Deps`/`Services` fields and one call into `app/l2.go`, `cmd_serve.go` (start `Syncer`, tape runner, sweeps) and `cmd_library.go` (verify/reconcile hooks into `cmd/metamusic/l2_hooks.go`). Lanes fill bodies **only** in their own `app/l2_<lane>.go`, `cmd/.../l2_cli.go` (R-b) files, which F2 creates as empty functions |
| API registration (`internal/api/ops_*_l2.go`) | the lane named in the table (records ops: V2; crates: K; tape: T2; settings: A) | F2 writes the registrations with final types and `501 NOT_IMPLEMENTED` stub handlers; the owner replaces the handler bodies only — never `Meta`, path, types or `operationId` |
| `openapi.json`, `web/src/api/schema.d.ts` | F2 (frozen afterwards) | `make openapi` must show **no diff** in a lane; if the API surface must change, stop and raise a freeze fix. At rebase, a conflict in these generated files is resolved by `git checkout origin/main -- <file>` then `make openapi`, never by hand |
| Op-count pins (`ops_m3_test.go`, `ops_m2c_test.go`), `toolsCeilingBytes`, `tools_list.golden.json` | F2 | untouched afterwards |
| `internal/store/migrations/**` | F1 | **no other lane adds a migration** (a column a lane thinks it needs is a freeze fix) |
| `internal/*/service.go` of the L2 packages | F2 | stubs live in `stub_<lane>.go` files (`records/stub_a.go`, `records/stub_b.go`, `crates/stub_k.go`, `tape/stub_t1.go`, `tape/stub_t2.go`, `albums/stub_a.go`, `recordview/stub_v2.go`, `liner/stub_v1.go`, `curation/stub_a.go`); the owning lane **deletes its stub file** and adds real files, so two lanes never edit one file |
| `web/src/App.svelte`, `lib/router.ts`, `lib/nav.ts`, `lib/scrollRestore.ts` | F3 | web lanes fill the placeholder components F3 created; no route, nav or `READY_ROUTES` change outside F3 |
| `web/src/lib/player.svelte.ts`, `lib/playerStore.ts` | W3 | W1/W2 call `player.play/enqueue/playNext` only |
| `scripts/e2e-map.txt`, `scripts/ci-shard-weights.txt`, `coverage.thresholds`, `.github/**`, `Makefile` | I1 | lanes report needed mapping/weights in their PR text |
| `docs/architecture.md`, `docs/runbook.md`, `docs/device-checklist.md`, `docs/subsonic.md`, `docs/plans/**` | X1 | lanes list the sections/addenda to write in their PR text; no lane edits docs |
| `web/e2e/**` | INT-W | lanes write bun tests, never a Playwright spec; INT-W is the only writer of `l2-*.spec.ts` |
| `internal/songs/filter.go`, `internal/library/tracks_l2.go`, `ops_songs_l1.go`, `ops_library.go` parameter handling | K | F2 adds only the struct fields and the 501-answering parameters |
| `internal/media/cover/service.go` | V1 (one named edit) | M4a's cover tests are the guard |
| `internal/subsonic/**` | S1 (then S2) | no other lane edits it |

**Rebase rule.** Before opening a PR a lane rebases on `origin/main`, re-runs `make verify` and `make gate`, and checks `git diff origin/main -- openapi.json web/src/api/schema.d.ts` is empty. Each PR description lists: tests added, files outside the lane's closed set (must be none), plan addenda for X1, and measured weights of new slow packages.

### Task L2-F1: migration 0010, pure functions, SQL fixtures, guard tests

**Files:** `internal/store/migrations/0010_curation.sql` (C119, including the `muses.ordinal` block); `internal/store/{schema_l2_test.go,triggers_l2_test.go,upgrade_l2_test.go}` (and the one-line edit of `schema_l1_test.go` if it pins the `muses` column list); `internal/records/{record.go,params.go,resolve.go,changes.go,text.go,why.go,resolve_test.go,text_test.go,why_test.go,seed_test.go,testdata/{change_text,why_text}.golden.json}`; `internal/tape/{plan.go,gain.go,params.go,plan_test.go,gain_test.go}` (`PlanGains`/`ApplyGain` only: `gain_db` needs them); `internal/crates/{vocab.go,params.go,vocab_test.go}` (the key tables of C125.2 as data); `internal/library/librarytest/{fixture_l2.go,fixture_l2_test.go}` (`FixtureL2`, `ApplyL2`, `FixtureL2Scale`, `FixtureL2Upgrade`, SQL-only, C132.2); **new** `internal/archtest/{import_graph_l2_test.go,sql_owners_l2_test.go}` (`TestImportGraph_L2`, `TestSQL_WriteOwners_L2`, `TestNoAlbumWord_L2`; the existing archtest files are not edited).
**Gate (blocking):** the P.5 boxes for D-L2-4, D-L2-15, D-L2-16, D-L2-17, D-L2-19, D-L2-27, D-L2-30 and D-L2-35 are ticked before this task starts.
**Tests first:**
- `TestMigration0010_Applies` (user_version 10 on top of 0009; every C119 table, index and trigger exists; all L2 tables empty; `settings` untouched; `PRAGMA foreign_key_check` empty), `TestMigration0010_UpgradeFromMain` (`store.OpenRaw` + `MigrateTo(…, 9)` + `FixtureL2Upgrade` ⇒ `store.Open` migrates; every earlier row unchanged), `TestMigration0010_FromSchema5` (0006…0010 apply together), `TestMigration0010_MuseOrdinalBackfill` (150 Muses written at schema 9 with equal `created_at` ⇒ ordinals 1…150 in `(created_at, id)` order, unique), `TestMuses_OrdinalAssigned` (a Muse inserted by `muses.Service.Create` and by `EnsureReferenceTx` after 0010 gets `MAX+1`; hiding never changes it), `TestTriggers_MuseOrdinal` (change ⇒ `ordinal is set once`; duplicate ⇒ UNIQUE), `TestSchemaVersionAgnostic`.
- `TestTriggers_L2` table: DELETE on every L2 table ⇒ error; frozen columns (`records.created_at`, `record_slots.record_id`, `crates.kind`, `crate_items.song_id`, `tapes.key`) ⇒ `frozen column`; `records`: status
  `published → proposed` ⇒ `illegal status transition`, `draft → published` ok, `first_published_at` changed twice ⇒ error, `order_json` with a missing/extra/duplicate/removed slot id ⇒ `order must list exactly the live slots`,
  `published` without `published_at` ⇒ CHECK, `draft` with `proposed_at` ⇒ CHECK; `record_slots`: second live slot for the same Song and Record ⇒ UNIQUE but remove-then-add ⇒ ok, pinned Take of another Song ⇒
  `not in the song`, insert on a retired Song ⇒ error, `song_id` change to anything but `retired_into` ⇒ error and to `retired_into` ⇒ ok, `synced_seq` decrease ⇒ error (but ok with a `song_id` follow), update of a removed slot ⇒
  `immutable`; `record_changes`: update/delete ⇒ `append-only`, seq gap ⇒ `broken chain`, slot of another Record ⇒ error, `slot_upgraded` with equal from/to ⇒ CHECK, `slot_filled` with a `from` ⇒ CHECK, a Syncer kind without
  `pick_event_id` ⇒ CHECK, the same `(slot_id, pick_event_id)` twice ⇒ UNIQUE; `crates`: smart without filter ⇒ CHECK, manual with filter ⇒ CHECK, filter > 4 096 B ⇒ CHECK; `crate_items`: item in a smart Crate ⇒ `only manual`,
  track in a song Crate ⇒ `does not match the crate subject`, duplicate live member ⇒ UNIQUE but remove-then-add ⇒ ok, update of a removed item ⇒ `immutable`; `tapes`: `queued → ready` ⇒ `illegal state transition`, `running → queued` ok,
  `ready` row `sha256` update ⇒ `immutable`, `ready` without `sha256` ⇒ CHECK, `failed` without `error_code` ⇒ CHECK, `contains_lossy 1` with `lossy_sources 0` ⇒ CHECK, two live rows with one key ⇒ UNIQUE but `failed` + live ⇒ ok.
- `TestResolveSlot_Table` (R01–R14 of C121.1, each `Slot, pinned, pick → Take, Mode, State, Warnings`), `TestCoverSeed_Golden` (three ids ⇒ fixed 53-bit values; `CoverSeed(id, 0) ≠ CoverSeed(id, 1)`; always `0 ≤ seed ≤ 2⁵³ − 1`),
  `TestChangeText_Golden` (24 rows: one per kind (20) plus `ladder` with 1 duel (singular), `stars` with `tied 3`, `stars` untied, and a row with missing evidence ⇒ the generic form), `TestChangeText_NoTitlesInRows`, `TestWhyText_Golden` (8 rows, C122.3).
- `TestPlanGains_Table` (G01–G17 of C128.2 with the exact integers stated there), `TestAutoTarget_Table` (odd/even medians, clamp edges, silent and unmeasured excluded, none ⇒ −14.0, round half away from zero),
  `TestApplyGain_Table` (the four rows of C128.2 plus identity at 0 cdB and a 1 000-sample ramp equal to a float reference), `TestPerf_GainApply`.
- `TestVocab_Tables` (the C125.2 key sets as data: no duplicates, the non-saved set disjoint), `TestImportGraph_L2` (C118 edges; packages that do not exist yet are skipped), `TestSQL_WriteOwners_L2` (scaffold: the owner table of C119.2), `TestNoAlbumWord_L2`,
  `TestSQL_MuseTables_Allowlist` (L1) still passes with the new packages present.
- `TestFixtureL2_Shape` (SQL counts after `Settle`: 7 Songs S1–S7 with 8 Takes per the C132.2a table; R1 with 4 live slots + 1 empty-Song slot = 5 and `status published`; R2 `draft` with 2; changelog rows as the ops would have written them; 4 Crates (2 manual with 3 and 4 members, 2 smart); loudness analyses for every Take; `provenance_dirty` empty after `Settle`), `TestFixtureL2Upgrade_IsSchema9`, `TestFixtures_L2_SyntheticOnly`.
Done: `make verify` and `make check` green; `openapi.json` unchanged.

### Task L2-F2: contract freeze — types, 17 registrations, stubs, services, wiring, rigs

**Files:** `internal/api/{api.go (Deps, New), types_l2.go, ops_records_l2.go, ops_crates_l2.go, ops_tape_l2.go, ops_settings_l2.go}` (stubs answer 501); `internal/api/{ops_l2_test.go, ops_m2c_test.go and ops_m3_test.go (op count 126)}`; the **parameter additions** of C130.1/2 on `SongFilterIn`/`searchTracksIn` (accepted, answering 501 `NOT_IMPLEMENTED` when used until K) and the new fields of `songs.ListFilter` (`internal/songs/filter_types_l2.go`) / `library.SearchParams`; per package the frozen method sets (C120–C128): `internal/records/service.go` (type, constructor, shared helpers) + `stub_a.go`/`stub_b.go`, `internal/recordview/service.go` + `stub_v2.go`, `internal/crates/service.go` + `stub_k.go`, `internal/albums/service.go` + `stub_a.go`, `internal/tape/service.go` + `stub_t1.go`/`stub_t2.go`, `internal/curation/service.go` + `stub_a.go`, `internal/liner/{types.go,stub_v1.go}`, `internal/media/cover/{mosaic_types.go,stub_v1.go}` (types and the `EnsureMosaic`/`RenderMosaic`/`MosaicKey` signatures; V1 deletes the stub), `internal/media/flacmeta/types.go` (`Comment`); `internal/app/{app.go,l2.go,l2_records.go,l2_crates.go,l2_catalog.go,l2_tape.go}` (`Build` constructs the services; the wiring bodies are the lane files; `serve` starts the Syncer and the tape runner and appends the tape resolver to the `resolvers` chain); `cmd/metamusic/{cmd_serve.go,cmd_library.go,l2_hooks.go}`; `internal/trackctx/curation.go` (interface + nil-safe adapter); `internal/textfold/slug.go`; `internal/importer/report.go` (`derived/tapes` in `cacheDirs`) with a test; `internal/servertest/{rig.go,rig_l2_test.go}` (`Options.L2`, `ManualSync`, `RealTape`, `Rig.Settle` extended, `Rig.Tapes`); `internal/tape/tapetest/**` (`FakePipeline`, `FakeSource`); `internal/mcpgen/{parity_test.go,parity_l2_test.go,m2a_contract_test.go (ceiling),testdata/tools_list.golden.json}`; `openapi.json`; `web/src/api/schema.d.ts`; plan addenda (PR text): M3 C81/C85/C86, M2a C35/C38/C41/C42, M1 C16.7/C18, L1 C90/C96/C98.
**Tests first:**
- `TestRegistry_OpCount` (126, "109 + 17 of L2") in both pins, `TestOps_RegisteredWithMetaL2` (exact Meta of the C129 table: scopes — `set_curation_settings` admin, `publish_record` write —, costs, `mcp` faces — 5 tool, 10 generic (`create_record` generic), 2 `exclude:human-only` —, UI routes, `C`/`H` flags, `human_only` true for exactly the two), `TestL2_NoSpendOps`, `TestOps_BodyLimitsL2`, `TestOps_StubsAnswer501L2` (every stub ⇒ 501 `NOT_IMPLEMENTED`; each new `list_songs`/`search_tracks` parameter ⇒ 501; entries are removed per lane), `TestToolsList_L2Ceiling` (measured compact size ≤ the new committed constant; L2 tools ≤ 9 000 B or the demotion order applied — the test prints the measurement), `TestOps_HumanOnly_NoMCP_L2` (`publish_record` and `set_curation_settings` absent from `tools/list` and from `call_operation`'s catalogue).
- `TestSlug_Table` (ASCII fold, `[a-z0-9._-]`, ≤ 120, parity with the `exports.cleanFilename` cases), `TestOrphans_SkipTapes` (a file under `derived/tapes/` is never reported by `library_orphans`; covers and dl still skipped).
- `TestRig_L2Options` (requires L1; `Rig.Settle` drains provenance then the Syncer; the default `ManualSync` leaves both idle; `FakePipeline` returns a parseable FLAC), `TestParity_L2Cases` scaffold (a case exists for each of the **15** ops with an MCP face; the runner fails on a missing case by name), `TestCrateVocabulary_EqualsListOps` scaffold (derives the key sets from the op input types by reflection and compares them with `crates.Vocab`; the behavioural half lands in K).
Done: `make verify` and `make check` green; `make openapi` committed; every stub reachable over REST and MCP (501); `TestImportGraph_L2` green. **From here the API surface is frozen.**

### Task L2-F3: web scaffold

**Files:** `web/src/lib/{router.ts,nav.ts,scrollRestore.ts}` and `router.test.ts`, `navTabs.test.ts` (extended); `web/src/App.svelte`; placeholder `web/src/routes/{Records,Record,Crates,Crate,SettingsCuration,HumanPublish,HumanCuration}.svelte` (a heading, `data-testid` of the page, `routeReady()` for the four list/detail routes). PR text lists the `e2e-map` lines I1 must add.
**Tests first (bun):** `router.test.ts` — the seven routes match, `/human/records/:id/publish` and `/human/curation` are **not** server paths (`isServerPath` false), specific-before-parametric order holds; `navTabs.test.ts` — every new route name is in `TAB_OF`, the Library sub-view row is Takes · Sounds · Records · Crates · Playlists, the two secure links are in `NAV_LINKS` with `secure: true`, `isTabRoot`/`CONTEXTUAL_ROUTES` entries; a scroll test — `READY_ROUTES` contains `records`, `record`, `crates`, `crate`.
Done: `make verify` green (web checks, `ux-shell` mapping); the app renders each placeholder.

### Task L2-R-a: Records service — model, slot edits, principal rules

**Files:** `internal/records/{create.go,update.go,list.go,slots.go,edit.go,publish_rules.go,*_test.go}` (C120, C121); deletes `internal/records/stub_a.go`; the one `catchUpTx` call inside `EditSlots` goes through F2's seam (`catchUpTx` is declared in `stub_b.go`/`sync.go` and returns nil until R-b).
**Tests first:**
- `TestRecords_Create_Rules` (title/subtitle/note limits and control characters; `song_ids` order ⇒ slots and `slot_added` rows after a `created` row; unknown Song ⇒ 422 `UNKNOWN_SONG`; retired ⇒ 409 `SONG_RETIRED`; 61 ids ⇒ 422; `cover_seed = CoverSeed(id, 0)`; agent and human both allowed),
  `TestRecords_Update_Rules` (revision lock ⇒ 409 with the current revision; text edits write `renamed`/`retexted`; `cover_reroll` increments `cover_rerolls`, recomputes the seed, writes `cover_rerolled`; hide/unhide rows; empty patch ⇒ 422), `TestRecords_List_Filters` (status, `q`, `song_id`, hidden, keyset stability).
- `TestEditSlots_Table` (24 rows): add default position; add at position 1 and n+1; add `position 0` ⇒ `INVALID_POSITION`; add with `take_id` ⇒ pinned slot and `slot_added` `to` = the pin; add of another Song's Take ⇒ `TAKE_NOT_IN_SONG`; add of a discarded Take ⇒ `TAKE_NOT_ELIGIBLE`; duplicate Song ⇒ `SONG_ALREADY_IN_RECORD`;
  retired Song ⇒ `SONG_RETIRED` with `details.retired_into`; hidden Song ok; 61st slot ⇒ `TOO_MANY_SLOTS` and rollback; remove then re-add ⇒ a new slot id; move to the same position ⇒ no row; move 1 → 3 ⇒ one `slot_moved` per displaced slot with `detail.from`; `reorder` with a missing id ⇒ `SLOT_ORDER_MISMATCH` listing it;
  `reorder` identity ⇒ no rows; `set_side` `A` then null; unknown slot ⇒ `UNKNOWN_SLOT` with `details.op`; a 3-op call whose second op fails ⇒ nothing written, `revision` unchanged; 51 ops ⇒ 422; empty ⇒ 422; the op index in every error; `revision` +1 per successful call; `updated_by`.
- `TestEditSlots_Pin_Rules` (pin eligible Take ⇒ `slot_pinned` from the Pick Take to the pin, reason `pin`; same pin again ⇒ no row; other Song's Take ⇒ 422; discarded ⇒ 422 with `details.status`; unpin ⇒ `slot_unpinned` to the Pick Take, `synced_seq` = the Song's max `seq`), `TestEditSlots_PinAll_UnpinAll` (floating slots with a Take pinned, empty slots reported in `warnings`; `unpin_all` reverses), `TestRecords_OrderTrigger` (the service never trips it; a hand-written bad order does).
- `TestRecords_PrincipalRules` (the C120.1 table, each cell: agent draft ✓; agent published ⇒ 403 `HUMAN_ONLY` with the hint; device published edit ✓; device `Publish` ⇒ 403; agent `Publish` ⇒ 403 before any read; human `Propose` ✓), `TestRecords_StatusPath` (draft→proposed→published→draft; `published → proposed` ⇒ trigger error; `Propose` on proposed ⇒ 409 `INVALID_RECORD_STATE`; `Propose` with no slot ⇒ 422 `RECORD_EMPTY`),
  `TestRecords_PublishRules` (needs a playable slot; `first_published_at` once; unpublish clears `published_*`/`proposed_*`, keeps `first_published_at`, row `unpublished`; `proposed → draft` writes `proposal_dismissed`; already-published with the current revision ⇒ 200 unchanged, with a stale one ⇒ 409), `TestRecords_PublishCheckOrder` (principal, 404, stale revision, no-op, empty), `TestRecords_PublishFalseFromDraft` (200 unchanged, no row, no bump), `TestRecords_ProposedEdit` (agent `Update`/`EditSlots` on `proposed` ⇒ `draft`, row `proposal_dismissed` with `reason edited`, `proposed_*` cleared; human edit keeps the proposal; the Syncer never dismisses), `TestOptSide_Decode` (absent/null/`"A"`/`"E"`; `set_side` without `side` ⇒ 422).
Done: `make verify` green; `go test ./internal/records/ -race -shuffle=on` green for the R-a tests.

### Task L2-R-b: Records changelog — Syncer, catch-up, Verify, CLI

**Files:** `internal/records/{sync.go,verify.go,*_test.go}` (C122; deletes `stub_b.go`); `cmd/metamusic/{l2_cli.go,l2_cli_test.go}` (`library verify` runs R1–R9, `reconcile` runs the Syncer, `--files`, `--dry-run`, through F2's hooks); addenda (PR text): M0 C7, L1 D-L1-27.
**Tests first:**
- `TestSyncer_Table` (S01–S14 of the acceptance mapping; Picks are produced through the seams named in C132.2a — `picks.Service.RecomputeTx` with an injected `ladder.Strength` map, the M2a rating service, L1's Song-level pin service, a new Take — so real `pick_events` are written; B3 reads the merged L1 code first and lists the helpers in the test file header), `TestSyncer_Idempotent` (two `Settle`s ⇒ identical `record_changes` hash), `TestSyncer_TwoSyncers` (two `Syncer`s, one DB file, concurrent `Settle`; a test hook makes the first write of each pass return `SQLITE_BUSY` so the bounded retry (C119.1 note 10) is exercised ⇒ still one row per `(slot, event)`), `TestEditSlots_CatchesUpFirst`,
  `TestSyncer_RunPollsOnlyWhenPending` (under `testing/synctest`; the `Syncer` exposes two unexported seams set by the test, `probe func(ctx) (int, error)` wrapping `Pending` and `settle func(ctx) error` wrapping `Settle`, each counting calls: with no pending events N ticks ⇒ N probes and **0** settles; after one Pick event ⇒ exactly 1 settle and the probe goes back to idle), `TestSyncer_BatchBoundary` (250 pending slots ⇒ 2 passes), `TestSyncer_FollowRetired` (L1 retire-into ⇒ follow, pin never lost), `TestSyncer_DuplicateRewritesOrder` (the duplicate removal rewrites `order_json` in its own transaction; `Verify` R1 clean; `revision` and `updated_at` unchanged; a client's stale `reorder` ⇒ `SLOT_ORDER_MISMATCH` with `details.extra`), `TestSyncer_NeverBumpsRevision`.
- `TestVerify_L2_DetectsEachAnomaly` (one planted fault per R1–R9 in a **scratch DB**; faults the new triggers forbid are planted after `dropTrigger(t, name)`; each yields the named code and severity), `TestProperty_RecordInvariants` (seeded `math/rand` sequences of: create Record, add/remove/move/pin/unpin slots, rate, duel verdict, discard/undiscard, new Take, retire a Song, propose/publish/unpublish; `Settle` every few steps and at the end;
  oracle = `records.Verify` and the L1 `provenance.Verify` empty **and** the changelog replay (R5) equals the resolved state; 60 sequences, `MM_PROPERTY_SEQS` overrides; the weekly job sets 300, C132.3), `TestCLI_LibraryVerify_L2` (exit 1 on a planted fault, warnings printed only), `TestCLI_LibraryReconcile_RunsSyncer` (`--dry-run` lists slots with pending events and writes nothing).
Done: `make verify` green; `TestSyncer_*`, `TestVerify_*` green with `-race -shuffle`; with R-a merged, `TestEditSlots_CatchesUpFirst` and `TestProperty_RecordInvariants` pass.

### Task L2-V1: cover mosaic and liner notes (pure)

**Files:** `internal/media/cover/{mosaic.go,mosaic_key.go,service_mosaic.go,mosaic_test.go,service_mosaic_test.go,testdata/cover_mosaic.golden.json}` and the single named edit to `service.go` (shared write path; C123); `internal/liner/{liner.go,render.go,liner_test.go,testdata/*.golden.md}` (C124.1); deletes `liner/stub_v1.go`. Addendum (PR text): M4a C110.1.
**Tests first:**
- the C123 test list (mosaic goldens for 0, 3, 9, 12 parts; `TestCover_NoFloatInRenderer` on `mosaic*.go`; `TestCover_M4aGoldenUnchanged`).
- `TestLiner_Build_Table` (17 rows: app basic; export basic; export ignores cost/session/Muse when supplied; export with `MuseNames` renders the Muse line; lossy notice "(2 of 5 tracks)"; resampled/upmixed notices; empty-slot notice only for app; pinned why prefix; side letters; engines distinct and sorted; Sound families top 3; BPM missing; `h:mm:ss` durations; cost totals `$1.23` and `$0.00` without floats; 8 KB cut at a whole line with a notice; `Description()` ≤ 1 000 characters; **a slot that never changed renders the why of its current Pick, `WhyText`-formatted, with `· undecided` for `newest`/`stars`**), the three goldens, `TestLiner_ExportIgnoresPrivateFields` (canary strings in cost, session and Muse never appear in the export bytes).
Done: `make verify` green (the M4a cover tests untouched and green).

### Task L2-V2: recordview, batch readers, Records ops, read amendments

**Files:** `internal/recordview/{view.go,slots.go,liner_input.go,energy.go,snapshot.go,preview.go,cover_input.go,cover_refs.go,*_test.go}` (C124.2/3; deletes `stub_v2.go`); the batch readers `internal/{sessions/refs,sounds/families,songs/briefs,picks/current,ears/loudness}.go` with tests (`muses.Service.Refs` already exists); `internal/api/{ops_records_l2.go,ops_records_l2_test.go,ops_context.go}`; `internal/songview/{get.go,library_sections.go}` (records, crates); `internal/trackctx/trackctx.go` (the `Curation` section and its `Fit` step, C130.4; the reader interface is F2's `curation.go`); `internal/app/l2_records.go` bodies are F2-owned wiring, V2 only supplies constructor options.
**Tests first:**
- `TestBatchReaders_OneStatement` (each batch reader runs one statement for 1 and for 60 ids).
- `TestRecordView_Get` (slots with `mode`, `state`, `pin_differs`, `gain_db` from the plan with the auto target, `warnings`, `totals`, `pending` false after `Settle` and true while an event is unconsumed), `TestRecordView_LinerAudience` (the `liner` in `get_record` is the `app` audience: Muse line present with a canary Muse; costs present), `TestRecordView_EnergyBudget` (60 slots with `include=energy` < 90 000 B; ≤ 120 points; the gain added; floor −70.0; no analysis ⇒ `lufs: []`),
  `TestRecordView_PickChangeKeepsCoverKey` (a Pick flip changes `slots[].take` but not `cover.key`; adding a slot does), `TestRecordView_Snapshot` (the tape `Snapshot` of a published Record: resolved Takes in order, `Measure.OK`, masters, tags, `description` from the export liner, the cover input; an empty slot skipped), `TestRecordView_SnapshotFlag` (a canary Muse appears only with `export_muse_names` on), `TestRecordView_NoN1` (statement counter ≤ **12** per `Get` with every `include`, and the **same count at 6 and at 60 slots**: record 1 + `Resolved` 3 + songs 1 + Muses 1 + sessions 1 + sounds 1 + loudness 1 + changes 1 + tapes 1 + settings 1), `TestRecordView_Preview` (chapter names, export-liner titles, tags and warnings equal the `Snapshot` ones; nothing written).
- `TestCover_PickChangeKeepsKey` (C123), `TestCover_Signing`-style checks live in V1.
- REST tests for the eight Records ops (shapes, status codes, audit rows, ids-only messages): `create_record` idempotent replay (same key ⇒ stored 201, one Record), `get_record` `include` combinations, `get_record_changes` paging in both orders with `cursor`, `edit_record_slots` conflict (stale `revision`), `propose_record` rules, `list_records` filters, `UNAVAILABLE` when a service is nil, rate classes, `TestMuseText_MutatingOpsLinerless` (the five mutating ops carry no `liner`, no canary byte, also not in the `idem` row).
- `TestPublish_Rules` (REST: a human session ⇒ 200; a paired device ⇒ 403 `HUMAN_ONLY`; a PAT agent ⇒ 403 `HUMAN_ONLY`, no state change, audit row 403), `TestPublish_NotInMCP`.
- `TestContext_L2Curation` (a Take that is the resolved Take of R1's slot and a member of a manual Crate ⇒ both listed; otherwise `null`), `TestContext_L2Trim` (crates then records trimmed before siblings; `_budget.truncated` names), `TestContext_NoCurationReader`, `TestSongView_RecordsCrates` (≤ 10 each, retired follow), goldens re-baselined with a reviewed diff.
Done: Records stubs removed from `TestOps_StubsAnswer501L2`; `make verify` green; goldens re-baselined with a reviewed diff.

### Task L2-K: Crates, smart Crates and the list-op predicates

**Files:** `internal/crates/{service,list,items,expand,count}.go` + tests (C125; deletes `stub_k.go`); `internal/songs/filter.go` (the L2 clauses incl. `pick_decided`) and `internal/library/tracks_l2.go` (the take predicates; `tracks.go` keeps only F2's struct fields) with tests `internal/songs/filter_l2_test.go`, `internal/library/read_l2_test.go`; `internal/api/{ops_crates_l2.go,ops_crates_l2_test.go}` and the real handling of the new `list_songs`/`search_tracks`/`search_sounds` parameters (C130.1/2) in `ops_songs_l1.go`, `ops_library.go`, `types_l1.go`, `types_m1.go` (named, minimal edits); addenda (PR text): L1 C90/C96, M1 C19.
**Tests first:**
- `TestAcceptance_L2_SmartCrate` (C125.5 on `FixtureL2`; also asserts that a Crate-expanded `list_songs` equals the explicit parameters and that `count` equals `total`), `TestCrateVocabulary_EqualsListOps` (behavioural half: for every vocabulary key a fixture query through the op and through a stored filter returns identical ids; the non-saved set is refused inside a filter; `crate_id` inside a filter ⇒ `UNKNOWN_FILTER_KEY`).
- `TestCrates_CreateRules` (manual/smart × song/take: 8 valid rows; smart without filter ⇒ 422; manual with filter ⇒ 422; unknown key; wrong type; 4 097-byte filter ⇒ `FILTER_TOO_LARGE`; name length/control characters; `items` on a smart Crate ⇒ 409; agent allowed; idempotent replay of `create_crate`), `TestCrates_UpdateRules` (revision lock; `filter` only for smart; `kind`/`subject` immutable; the live count returned; hide), `TestCrates_EditItems` (add/already/removed/not_member counts; re-add after remove creates a new row; subject mismatch ⇒ 422 `SUBJECT_MISMATCH`; `CRATE_IS_SMART`; `CRATE_FULL` at 5 000; retired Song ⇒ 409; unknown ids ⇒ 422; ≤ 200 per call), `TestCrates_Expand_Merge` (equal values ok, conflicting ⇒ `FILTER_CONFLICT` naming the field, list fields intersect, a hidden Crate usable, a deleted-id filter matches nothing and reports a warning), `TestCrates_RetiredSongFollowed`, `TestCrates_CountsOnRead` (`list_crates` returns `count: null` for smart Crates unless `counts=true`).
- `TestSongs_PickDecided` (every Pick source × `pick_decided`), `TestSongs_HasPick_Unchanged` (every Pick source × the shipped `has_pick`), `TestSongs_UnheardTable` (device `start` counts, agent `start` does not, a `complete` without `start` does not; Song-level = any Take), `TestSongs_SubjectTakePredicates` (Pick take vs newest eligible; no take ⇒ never matches a duration/star/tag predicate; `min_stars` and `unrated` exclusive ⇒ 422), `TestSongs_InRecord` (draft, proposed, published, hidden Record, removed slot), `TestSearchTracks_L2Filters` (`unheard`, `in_record` = resolved Take, `sound` over `take_sounds`, `crate_id`), `TestLibrary_OldSchemaFixturesStillRead` (extended: pre-L2 fixtures read unchanged; the filters match nothing there), `TestPerf_SmartCrate`.
Done: Crates stubs and the 501 parameters removed from `TestOps_StubsAnswer501L2`; `make verify` green.

### Task L2-A: album catalog (record kind), curation settings and stats

**Files:** `internal/albums/{service,membership,token}.go` + tests + `albumstest/**` + `testdata/albums_fixture.golden.json` (C126.1–6, record kind only; deletes `albums/stub_a.go`); `internal/curation/{service,settings,stats}.go` + tests (C127; deletes `curation/stub_a.go`); `internal/records/{published.go,published_test.go}` (`ResolvedPublished`, the set-based read the catalog uses); `internal/api/{ops_settings_l2.go,ops_settings_l2_test.go}`; `get_stats.curation` in `internal/library/stats.go` + `internal/api/ops_stats.go` (plain `COUNT` SQL in `curation.Service.Stats`, so A needs nothing from R/K/T); addenda (PR text): L1 C98.
**Tests first:**
- `TestAlbums_MembershipTable` (record rows only: published Record ⇒ `rec-` album with playable slots only; Records with ≥ 1 playable slot listed, with none not; draft/proposed/hidden Records absent; discarded pinned Take not listed; Disc by side; duplicates impossible), `TestAlbums_TokenStable` (changes on publish, unpublish, a Pick event, a Song hide, a flag change, a Take title edit, a Take discard and a Song title edit — each through its real op; unchanged after a rating of an unrelated Take), `TestAlbums_TokenNoDirectoryScan` (a nil/fake directory is never asked for `Ordinals`/`Labels`), `TestAlbums_Golden` (`albums_fixture.golden.json`, public-safe), `TestAlbums_StatementCount` (≤ 6 per call; `Token` ≤ 9), `TestPerf_AlbumCatalog`.
- `TestSettings_HumanOnly` (agent ⇒ 403, device ⇒ 403, human ✓; unchanged ⇒ no write; empty patch ⇒ 422; the four keys written together; audit row without values beyond the hash), `TestSettings_Visibility`, `TestSettings_UnknownKeysRejectedByVerify`, `TestGetStats_Curation` (incl. `review.undecided`/`pin_ineligible` counting published Records only).
Done: Settings stubs removed from `TestOps_StubsAnswer501L2`; `make verify` green.

### Task L2-T1: tape media layer — flacmeta writers, Stdin, gain stage, pipeline, chapters, tags (separable, D-L2-29)

**Files:** `internal/media/flacmeta/{write.go,vorbis.go,cuesheet.go,picture.go,write_test.go,fuzz_write_test.go,real_test.go}` (+ `flactest` helpers) — **first commit: extract** `blockHeader`, the VORBIS_COMMENT builder and the PICTURE builder from `internal/media/tagged/write.go` into exported `flacmeta` functions and make `tagged.Write` call them (behaviour-preserving; `tagged`'s tests and goldens are the guard, D-L2-40); then the CUESHEET block and `Rewrite`; `internal/mediatool/{run.go,run_test.go}` (`Stdin`); `internal/tape/{pipeline.go,gainstage.go,chapters.go,tags.go,preflight.go,preflight_unix.go,preflight_other.go,*_test.go}` (C128.3/4; deletes `stub_t1.go`); `internal/library/librarytest/{fixture_l2_audio.go,fixture_l2_audio_test.go}` (`FixtureL2Audio`); addenda (PR text): M0 (`RunOpts.Stdin`), M4a C110.2.
**Step 0 (spike, ≤ 0.5 day):** build a 3-chapter tape by hand with the C128.3 writers; record, for ffmpeg's FLAC demuxer (`-f ffmetadata`), `mpv`, VLC, foobar2000 (if available) and the operator's own phone player, which of the three encodings each reads. The outcome decides nothing about the writers (all three stay) but is a **binding input** (C128.6): it fixes the device-checklist wording and whether T2 builds the `.cue` sidecar of D-L2-29 (built when no player in the spike reads any encoding).
**Tests first:**
- `TestVorbisComment_Escapes`, `TestCuesheet_Block_Golden` (3 tracks at 0, 48 000, 144 000 samples with lead-out 288 000 ⇒ the exact bytes; the chapter milliseconds `0, 1000, 3000`), `TestPictureBlock_Golden`, `TestFlacmeta_Rewrite_Golden` (a `flactest` file: new Vorbis/Cuesheet/Picture blocks, last-block flag correct, STREAMINFO/SEEKTABLE/frames identical, `Read` returns the new content), `FuzzRewrite`, `TestRun_Stdin` (a tool reads stdin; `nil` stdin keeps `/dev/null`; the existing callers' tests unchanged).
- `TestPipeline_ArgvGolden` (exact decode, encode and `--test` argv: literals plus app paths; a title or a Muse canary can never appear in argv), `TestPipeline_GainStage_BoundariesAndGapless` (random chunk splits including 1-byte and odd sizes ⇒ output byte-identical to a one-shot conversion; frame counting exact; 16-bit left-aligned input handled), `TestPipeline_ClippedAborts` (a forced +6 dB on a full-scale sample ⇒ `TAPE_CLIPPED`, nothing claimed), `TestPipeline_SampleMismatch` (a fake encoder reporting a wrong total ⇒ `TAPE_SAMPLE_MISMATCH`), `TestPipeline_SourceChanged` (size differs from `master_bytes`),
  `TestPipeline_Cleanup` (work dir removed on success, failure and cancel; no partial file under `derived/tapes/`), `TestPipeline_Chapters` (offsets from cumulative frame counts ⇒ block, `CHAPTERnnn` and text forms agree: exact, ±1 ms, ≤ 1/75 s), `TestTags_Table` (every tag of C128.4; lossy ⇒ the label forms; the flag off ⇒ no Muse line; sided names prefixed).
- Media tier (`MM_REQUIRE_MEDIA_TOOLS=1` in CI; locally skipped without tools): `TestPipeline_FFmpegFailureKillsEncoder`, `TestPipeline_EncoderDeathStopsFFmpeg`, `TestPipeline_NoGoroutineLeak`, `TestFlacmeta_RealFiles` (`flac --test` accepts every rewritten file), `TestPerf_TapeRender` (12 × 240 s synthetic; wall budget and heap peak), `TestTape_ChaptersVisibleToFFmpeg` (spike log, no assertion), `TestTagged_StillByteIdentical` (the extraction changed no byte of a tagged download).
Done: `make verify` green; the media-tier tests pass locally with tools (or in CI); `tagged` goldens untouched.

### Task L2-T2: tape service layer — export, key, quotas, runner, resolver, sweep, ops

**Files:** `internal/tape/{service.go,export.go,runner.go,resolve.go,sweep.go,*_test.go}` (C128.1/2/5/7; deletes `stub_t2.go`); `internal/tape/tapetest/**` (fakes); `internal/api/{ops_tape_l2.go,ops_tape_l2_test.go}`; `internal/media/serve/handler.go` and `chain` only if the spike requires the `cue` sidecar (`get_tape.download.cue_url`, `TestTape_CueSidecar`).
**Tests first:**
- `TestTape_ExportRules` (not published ⇒ 409 `RECORD_NOT_PUBLISHED`; stale `revision` ⇒ 409; no resolved Take ⇒ 422 `RECORD_EMPTY`; 61 items or > 7 200 s ⇒ 422 `TAPE_TOO_LONG`; target outside [−30, −9] ⇒ 422; `exec.LookPath` failure ⇒ 503, unset `MM_FLAC` ⇒ default `flac`; review gate and quotas as in C128.1), `TestTape_Reuse` (same snapshot twice ⇒ one row, `reused true`; a Pick change that changes a Take ⇒ a new key and row; a title change ⇒ a new key; the Record revision alone ⇒ same key), `TestTape_KeyTable`, `TestTape_Runner_States` (queued → running → ready; failure ⇒ `failed` with a code and no file; a failed then re-exported snapshot ⇒ a new row),
  `TestTape_Recover` (expired lease ⇒ re-queued with `tapes.attempts 1`; a second expiry ⇒ `attempts 2`; third ⇒ `failed TAPE_INTERRUPTED`; progress rewrites in between do not reset it), `TestTape_Sweep` (14-day rule ⇒ `expired`, file removed after commit; orphan files older than a day removed, younger kept), `TestTape_Preflight_DiskFull` (a fake statfs), `TestTape_Resolve` (`ready` sha ⇒ object with `DownloadName`; unknown or `expired` ⇒ `ErrNotFound`; the master resolver and tape resolver never answer the same sha), `TestTape_LinksSigned`, `TestTape_NoMuseByDefault`.
- `TestBackup_ExcludesTapesAndCovers` (a pure assertion: the backup manifest never names `derived/tapes/` or `derived/covers/`). `TestTape_Preflight_DiskFull` in the include above belongs to T1's `preflight.go` and lands with T1. The tests that need the real pipeline are written here against `FakePipeline` where the behaviour allows and re-run by INT-B with `RealTape`: `TestTape_ReviewGate` (undecided/ineligible slot ⇒ 409 `TAPE_NEEDS_REVIEW`; agent `accept_warnings` ⇒ 403; human ⇒ 201 with warnings recorded), `TestTape_Quotas` (4th live tape of a Record ⇒ 409 `TAPE_QUOTA` scope `record`; 8 GiB ready ⇒ scope `total`; a reused key never blocked; `target_lufs` −14.04 and −14.0 ⇒ one key), `TestTape_FlacVersionOnce`, `TestTape_LossyLabel_Table`, `TestTape_DeterministicFile` (no tape id/revision/export-day tag; two exports from different rows are byte-identical under a fixed clock and tool versions), `TestImmutableMastersUntouched` (every master byte-identical, mode 0444, after an export).
- REST tests for `export_record_tape` (201 `queued`, then `RunOne` ⇒ `ready` through `FakePipeline`; idempotent replay; `heavy` rate class; audit row without titles) and `get_tape` (link minted only when `ready` and the file exists; `expired` has no link).
Done: Tape stubs removed from `TestOps_StubsAnswer501L2`; `make verify` green. The end-to-end `TestAcceptance_L2_TapeExport` (V1–V8, media tier, real pipeline) is run by INT-B.

### Task L2-W1: Records pages

**Files:** `web/src/routes/{Records,Record,HumanPublish}.svelte` (replacing F3's placeholders), `web/src/routes/Record.test.ts`; `web/src/lib/{recordView.ts,curationApi.ts,tapeView.ts,energy.ts}` + `.test.ts`; `web/src/lib/fixtures/change_text.golden.json` (written by the Go `-update` test); `web/src/lib/components/{RecordCover,SlotRow,SongPicker,ChangeLog,LinerPreview,EnergyCurve,TapePanel,RecordStatus}.svelte`.
**Tests first (bun):** `recordView.test.ts` — `reorderOp` from a drag result (single `reorder` with the full id list; no-op when unchanged), keyboard ▲/▼ ⇒ the same op, pin/unpin availability (hidden for an empty slot; "pinned, not eligible" label), warnings text, `canEdit` (agent/device/human × draft/published), `changeText` equals every row of the shared golden, a 60-slot model builds in < 20 ms; `tapeView.test.ts` — poll policy (1 s while `queued|running`, stop on terminal, give up at 15 min), progress text per phase, plan table rows, lossy banner text, chapter list from `chapters`; `energy.test.ts` — polyline path and shared axis from a fixture, `lufs: []` slots skipped; `curationApi.test.ts` — wrappers send the idempotency header for the three create ops, map 409 `STALE_REVISION` to a reload action; `Record.test.ts` — the Library `routeReady` fires once after the first load; `HumanPublish` — the preview lists chapter names and warnings from `get_record include=preview`, and the Freeze shortcut sends exactly one `pin_all`.
Done: `make verify` (web lint/check/unit) green; no `fetch` outside `web/src/api/`; no second `<audio`.

### Task L2-W2: Crates pages and entry points

**Files:** `web/src/routes/{Crates,Crate}.svelte`, `web/src/routes/Crate.test.ts`; `web/src/lib/{crateFilter.ts,crateFilter.test.ts,crateApi.ts}`; `web/src/lib/components/{AddToCrate,AddToRecord,FilterBuilder,CrateMembers}.svelte`; named edits to `web/src/routes/{Song,Track,Board}.svelte` (chips, menus, Board action bar) and `web/src/lib/boardView.ts`.
**Tests first (bun):** `crateFilter.test.ts` — the field table `satisfies` the generated parameter keys, a test reads `../../openapi.json` and compares the key sets of `list_songs`/`search_tracks` (minus the non-saved set), `unheard`/`in_record`/`pick_decided` are present; filter ⇄ query-string round trip for repeated keys (`state`, `sound`, `tag`); the concept example builds `{"sound":["genre_family:reggae"],"unheard":true,"min_duration_s":240,"pick_decided":false}`; invalid combinations (`min_stars` + `unrated`) are blocked client-side with the server's message; `boardView.test.ts` extended — "New Record from selection" collects the selected Song ids (cap 60, note beyond), "Add to Crate" uses subject Song; `Track.test.ts`/`Song.test.ts` extended — "In Records" chips and the Crate menu.
Done: `make verify` green.

### Task L2-W3: playback, curation settings pages

**Files:** `web/src/lib/{recordPlayer.ts,recordPlayer.test.ts,player.svelte.ts (gains, tape item),playerStore.ts (no change to the saved shape unless a test demands it),player.test.ts}`; `web/src/lib/components/{ChapterList,CurationToggles}.svelte`; `web/src/routes/{SettingsCuration,HumanCuration}.svelte`.
**Tests first (bun):** `recordPlayer.test.ts` — queue skips empty slots; the gains map is `take_id → gain_db`; source label/href are the Record's; "Play as tape" asks for an export when no ready tape matches the revision and enqueues `tape:<id>` otherwise; chapter seek maps an index to `start_ms`; a restored queue whose source href is `/records/:id` rebuilds the gain map; `player.test.ts` extended — a gain map creates one `GainNode` on the single element (not per track), applies the value at track start, no map ⇒ no node, the `audio.volume` path is never touched (iOS), prefetch unchanged, `isTrackId` still rejects `tape:`; `playerStore.test.ts` — gains are not persisted; `singleAudio.test.ts` unchanged and green; `curationApi.test.ts` — settings wrappers; a component test for `CurationToggles` (copy states both effects; saving calls `set_curation_settings` once with only the changed key).
Done: `make verify` green incl. `m1-player` and `m3-autoadvance` specs (mapped by `player*`).

### Task L2-I1: CI, coverage, weights, e2e map, docs plumbing

**Files:** `.github/workflows/weekly.yml` (`curation-property`); `coverage.thresholds`; `scripts/{e2e-map.txt,ci-shard-weights.txt,ci-heavy-tests.txt}` (lines for the L2 specs and any package ≥ 15 s reported by lanes); `internal/archtest/coverage_thresholds_l2_test.go`; `Makefile`/`scripts/**` only if a selftest needs a case.
**Tests first:** `scripts/ci-tiers-selftest.sh` and `scripts/verify-selftest.sh` still pass with the new mappings; `TestCoverageThresholds_L2` (the file names every L2 package with its minimum); a docs link check for X1's sections once they exist.
Done: CI green on a branch; the coverage job reports the L2 packages.

### Task L2-S1: Subsonic Record albums

**Files:** `internal/subsonic/{records.go,records_test.go}` and named edits to `ids.go`, `lists.go`, `browse.go`, `media.go`, `search.go`, `catalog.go` (C126.7); `internal/app` wiring of `albums.Service` into the Subsonic options is F2's `l2_catalog.go` (S1 supplies the option field in `subsonic.Options` via a named edit of `deps.go`); addendum (PR text): M4a C108/C109, `docs/subsonic.md`.
**Tests first:** the C126.7 list (`TestSubsonic_RecordAlbum_*`, `TestSubsonic_LineageUnchanged`, `TestSubsonic_NoMuseText`, `TestAcceptance_L2_SubsonicRecordAlbum`). The M4a XML/JSON goldens must pass **byte for byte** on a rig with no Records.
Done: `make verify` green; `go test ./internal/subsonic/ -race` green.

### Task L2-A2 / L2-S2 (optional; behind its own P.5 box)

A2: `internal/albums/{muse,naming}*.go`, `internal/muses/{directory.go,directory_test.go}`, `get_muse.catalog` (`ops_muses_l1.go` named edit), tests `TestAlbums_MembershipTable` (muse/unfiled rows: Muse album holds Picks of live non-hidden Songs outside published Records; a Song entering a published Record leaves the Muse album and `unf`; hidden Muse ⇒ no album; empty album unlisted; Muse-less Song ⇒ `unf`; hidden Song excluded), `TestAlbums_NeutralNames` (ordinal-based, zero padding at 999 → 1000), `TestAlbums_MuseLabelsNotCalledWhenOff` (the directory fake panics on `Labels`), `TestAlbums_OptInNames`, `TestMuses_Directory` (`Ordinals` in one statement with hidden flags from the frozen column; `Version` changes on update/hide/create; `Labels` covered by a call-count test), `TestGetMuse_Catalog` (neutral name, never the opted-in one). S2: `mus-<id>`/`unf` in the Subsonic server with the same machinery and tests as S1. If cut: the booleans remain, `subsonic_muse_names` has no effect (documented in C131.4).

### Task L2-INT-B: final wiring, acceptance, property, leaks, parity (runs the whole backend suites)

**Files:** `internal/app/l2*.go` (final bodies: real `MuseDirectory` adapter when A2 exists, `tape.Source`/`Loudness` adapters, tape resolver in the chain, sweeps next to the export sweep, `Syncer.Run` and `tape.Run`/`Recover` in `serve`); `internal/acceptance/{l2_test.go,l2_rig_test.go,every_ui_action_test.go (entries),nosecretleak_test.go (L2 cases),names_test.go (`TestNoAlbumWord_L2` hook)}`; `internal/mcpgen/parity_l2_test.go`; `internal/api/ops_l2_leaks_test.go`; `internal/servertest/**` adjustments; `FixtureL2Mirror`.
**Tests first:** `TestAcceptance_L2_PickUpgradesFloatingSlot`, `TestAcceptance_L2_PinnedNeverChanges`, `TestAcceptance_L2_SmartCrate`, `TestAcceptance_L2_AlbumCatalog` (record kind; A2 rows when built), `TestAcceptance_L2_SubsonicRecordAlbum`, `TestAcceptance_L2_TapeExport` (media tier, real pipeline, V1–V8), `TestAcceptance_L2_AgentProposesHumanPublishes`, `TestAcceptance_L2_Mirror` (`FixtureL2Mirror` at `Originals 4, Groups 5`: Records over the mirror's Songs; `records.Verify` and the L1 `provenance.Verify` empty; the Record view, catalog and a FakePipeline tape all consistent), `TestMuseText_OnlyInAllowlistedOps` (L2 cases), `TestRecordOps_ErrorsCarryIDsOnly`, `TestL2_PublishGate`, `TestL2_AgentTitleCanary`, `TestPublishPreview_EqualsSnapshot`, `TestMuseText_MutatingOpsLinerless`, `TestNoSecretLeak_L2`, `TestAcceptance_M2a_AgentPerformsEveryUIAction` (15 new entries), the parity cases with REST = MCP = stdio after normalisation, `TestProperty_RecordInvariants` with picks, sounds and tapes enabled, `TestRestore_RebuildsRecordChangelog`, `TestSettings_Visibility`, `TestAcceptance_L2_EndToEnd_Agent` (go-sdk MCP client with a read+write PAT: `list_songs` ⇒ `create_crate` + `edit_crate_items` ⇒ `create_record` from the Crate's Songs ⇒ `edit_record_slots` ⇒ `get_record(include=liner,energy)` ⇒ `propose_record` ⇒ `get_record_changes`; the human publishes over REST with a human principal; `export_record_tape` through `FakePipeline` ⇒ `get_tape` ready; `get_audit` lists every step and no title or Muse text), `TestFixtureL2Mirror_Params`.
Done: `make check`, `make test`, `make test-long` (acceptance packages touched) and `make perf` green locally once (or the same in CI); all named tests green.

### Task L2-INT-W: Playwright L2 specs (affected specs only locally; the matrix runs in CI)

**Files:** `web/e2e/{l2-records.spec.ts,l2-crates.spec.ts,l2-tape.spec.ts,l2-curation.spec.ts,l2Api.ts}` (each spec starts with a `// matrix:` header, C132.1; `playwright.config.ts` needs no change — specs are discovered).
**Tests first (chrome + the matrix tags):** `l2-records.spec.ts` (`@mobile`) — create a Record from two Songs, reorder by keyboard, pin a slot and see the badge, rate a Take so the Pick of another slot's Song changes (through the API helper) and watch the changelog line "upgraded" appear within 3 s while the pinned slot stays; propose, then publish through the in-app `/human/records/:id/publish` (the page shows the neutral album policy); an agent edit of the published Record (API helper) is refused and the UI shows it read-only for agents' edits; touch targets ≥ 44 px on `/records`, the studio and the publish page; `l2-crates.spec.ts` (`@mobile`) — create a manual Crate from the Board selection, add from the Song page, build the smart Crate of the concept example (`pick_decided:false`) and see the live count equal the list; remove a member; `l2-tape.spec.ts` (`@firefox`) — export a tape from a published Record (the e2e rig uses `FakePipeline`), poll to ready, the lossy banner and chapters render, Play as tape enqueues the tape item in the single player, the download link is a signed `/media/…flac` URL; `l2-curation.spec.ts` (`@mobile`) — settings page shows both flags off, `/human/curation` toggles one and the page reflects it (the spec resets it); the Library sub-view row shows Records and Crates and Back restores the Records list scroll.
Every spec resets its own data and does not depend on counters other specs change; `served-check` reports zero unserved UI ops; `/__e2e/leaks` is `[]`.
Done: the L2 specs green locally on chrome (the rest in CI).

### Task L2-X1 (Integration, docs, review)

Merge any remaining lane branches; regenerate `openapi.json` and `schema.d.ts` (**must be unchanged after F2**); copy the **public** part of this plan to `docs/plans/<date>-l2-library-curation.md` with `scripts/l1-public-copy.sh` (which cuts at the private-appendix marker comment and fails when the marker is missing); `TestPublicCopy_StopsAtMarker` (archtest) is extended: the L2 copy contains neither `## Appendix P` nor any P.4 pattern; write the docs sections of C132.5 and the `docs/subsonic.md` Record-album section; append decisions D-L2-1…43 to the private design document; update `docs/plans/2026-10-12-roadmap.md` (L2 between L1 and L3) and apply the plan addenda each lane listed (M0 C7, M1 C16.7/C18, M2a C35/C38/C41/C42, M3 C81/C85/C86, L1 C87/C89/C90/C96/C98/C99/C101, M4a C108/C109/C110); record `metrics/` rows; run the final review (spec, quality, security: the publish gate, the neutral-name structure, the tape argv and the Subsonic Record albums are the security focus).
Done: `make check`, `make test`, `make e2e`, `make gate-full` green on the merged tree (or the CI equivalents).

### Task L2-X2 (Production-copy check and deploy; operator-assisted; host details only in private notes)

1. Copy the production database **read-only** snapshot (never the live file) to a scratch directory; run the L2 binary's migrations (every step up to 0010), `metamusic library reconcile`, `library verify` (L1 + L2 invariants clean); time `albums.List` on the copy.
2. On the scratch copy create (through the ops) the three first Records of Appendix P.2, publish them with a human principal, export one tape per Record with the **real** pipeline, and compare the plan (targets, gains, statuses) with P.2; run `flac --test` on each tape and the C128.6 verifications that need no synthetic source; open each published Record from a Subsonic client against the scratch copy.
3. Deploy (the migration is the first boot step); confirm `healthz` reports schema 10, `get_stats.curation` shows zero Records and both opt-ins off, `tools/list` within the ceiling.
4. Operator listening check of one real tape (DC-L2-1 and DC-L2-2); the operator records which players show chapters.
5. Both opt-ins stay **off** unless the operator decides otherwise at X3.
Done: production reports the expected counts; `library verify` clean; rollback = restore the pre-migration backup (runbook).

### Task L2-X3 (Operator review)

Review the studio on the real data (phone and desktop), the first Records and their changelogs after real rating/duelling, the smart Crates the operator wants saved, the neutral album names (if A2/S2 were built), the Record albums in a Subsonic client, and the tape (loudness feel, seams, chapters in the preferred player). Record outcomes and the decision on the two opt-ins and on A2/S2 in the private notes; open L3 inputs (sequencing preferences: tempo/key/energy order, Muse-aware actions).

---


## Definition of done (L2)

- All named tests green; each lane green on `make verify` (+ `make gate`); `make check`, `make test`, `make test-long` (acceptance packages), `make e2e` and `make gate-full` green once on the merged tree (or their CI equivalents: 6 Go race shards, perf job, 6 e2e shards, coverage job with `served-check`); `make routing` green; coverage thresholds (C132.3) met on merged data.
- Parity: every L2 op with an MCP face (15) has a parity case (REST = MCP = stdio after normalisation) and the two human-only ops have REST-only tests; the `tools/list` golden is re-baselined under the new ceiling (C129); `TestAcceptance_M2a_AgentPerformsEveryUIAction` covers the 15 new UI+MCP operations; the registry holds 126 operations.
- Invariants: `library verify` (L1 + L2) clean on every fixture and on the production copy; `TestProperty_RecordInvariants` (60 per CI run, 300 weekly) green with the changelog replay equal to the state; provenance tables and L2 tables follow the no-delete/frozen rules; no L2 table has a second writer (`TestSQL_WriteOwners_L2`); masters untouched by any export.
- Acceptance (concept §7 L2): a Pick change upgrades floating slots and logs it; pinned slots never change; tape export verified (chapters, loudness, gapless, lossy label) — each by its named test; Crates and smart Crates; Record albums visible to a Subsonic client (S1; automatic albums with neutral names only if A2/S2 are built).
- Privacy: the C127 tests green; Muse text reaches no export, log, audit row, notification or Subsonic-bound package by default; the two opt-ins are human-only and visible to agents; no real titles, artists, inspirations, lyrics, hostnames, balances or personal data in any tracked file; the public plan copy contains nothing below the private-appendix marker.
- Money: no L2 op spends; no L2 code path calls a provider.
- Performance targets of C132.4 met on the CI runner and (catalog, tape) on the production copy.
- The tape lanes (T1/T2) and the auto-album lanes (A2/S2) are the separable items (D-L2-29): if the tape lanes are cut, L2 is complete with the tape op answering 503 `UNAVAILABLE {component: tape}`; cover serving, the orphan skip and the Record/catalog code remain; if A2/S2 are cut, `subsonic_muse_names` has no effect and `muses.ordinal` stays unused.
- Release tag `v0.5.0-l2` recorded.

## Risks / open questions

- **Chapter compatibility is the one unproven item.** FLAC has no chapter standard; the three encodings (binary CUESHEET block, `CHAPTERnnn` comments, text `CUESHEET` comment) cover the readers known today, but which of the operator's players shows chapters is only known after the spike and DC-L2-1. If none shows them, the `.cue` sidecar fallback (D-L2-29) is a one-line addition, not a new format.
- **Gapless playback in a browser is best-effort.** Two `<audio>` elements with prefetch can leave tens of milliseconds at a seam; the plan measures it (DC-L2-2) and offers **Play as tape** as the guaranteed path. If the measured seam is unacceptable, a Web Audio scheduler for the next track is the next step (memory-bounded by decoding only the next track) — not designed here. On iOS the `MediaElementAudioSourceNode` path may stop when the screen locks; DC-L2-2 checks it, and **Play as tape is the supported iOS path** if it does.
- **A published Record changes under its listeners.** Floating slots follow Picks by design; every change is logged and `pin_all` freezes. Subsonic clients that cache tracks see a changed album when the token changes (documented for M4a). If real use shows surprise, the cheapest fix is `pin_all` on publish as a default (an option, not built). A Pick that no human judged (`newest`/`stars`) can land in a published Record: it is flagged (`undecided_pick`), counted in `get_stats.curation.review` and on the publish page, and a tape needs a human to accept it (D-L2-30/33), but it is **not held back** in the app or the catalog — holding it back would need a stored take per floating slot, which breaks the derived-never-stored rule.
- **Neutral auto-album names are not browsable.** `Inspiration 007` tells the operator nothing; that is the price of keeping inspirations out of Subsonic. The remedy is by design: curate a Record (a real name), or enable the opt-in. The Muse page shows each Muse's neutral name (`get_muse.catalog`).
- **The Syncer lags by up to one second.** Resolved Takes never do; only the changelog does; `pending` says so. Two Syncers cannot corrupt each other (unique key); a crashed process loses nothing (per-slot cursors live in the database).
- **Tape cost in disk and CPU.** A 35-minute tape is about 600 MB of PCM through the encoder and a few hundred MB of FLAC, but `tape.flac` and `final.flac` coexist during the rewrite, so the peak is about 700 MB and the preflight demands `1.3 × PCM + 64 MiB` (≈ 850 MB); one render at a time, quotas (3 live tapes per Record, 8 GiB `ready`: 409 `TAPE_QUOTA`), 14-day retention, never backed up. The real-library numbers are in Appendix P.2; a much larger Record needs the `MaxDurationS` cap raised by decision.
- **`flac` output differs across encoder versions.** The key includes the encoder version string, so a package upgrade creates new artifacts rather than mismatching old keys; no test pins encoder bytes, only properties (V1–V8) and the Go-written metadata.
- **Loudness depends on stored analyses.** An unmeasured take is not matched (gain 0, warning); the export asks Ears to analyse first. The peak guard uses the stored true-peak (ffmpeg's estimate); the Go-side sample check is the backstop and aborts rather than clips.
- **"Lossless tape" with lossy inputs.** The container is lossless; a decoded MP3 is still an MP3's audio. The label is mandatory and never removable by an option; the Record view shows lossy slots before export.
- **Smart-filter vocabulary grows with the list ops.** The derivation by reflection (`TestCrateVocabulary_EqualsListOps`) makes a new parameter a conscious choice (add it to the non-saved set or accept it into the vocabulary). `has_pick` keeps its shipped meaning; the decided-Pick predicate is the new `pick_decided` (D-L2-36).
- **Publishing from a phone is not possible** (`/human/api`, KindHuman). Editing a published Record from a paired device is. If this proves annoying, the alternative (publish on `/api/` with an `ActsAsHuman` check like D-L1-11) is a one-line registry change plus a decision; it was not chosen because publishing is what lets text leave the app.
- **Record text is operator content.** The publish page shows exactly what leaves; a human who publishes a Record whose agent-authored title names an inspiration has decided that. The structural guards stop *accidents*, not decisions (as in L1). **Catalog text is review-by-display, not a hard gate (D-L2-27):** an agent can still retitle a Take or Song or regroup Muses after a Record was published, and the next tape reads the new strings; the publish preview, the tape key and the plan make that visible, they do not forbid it. If that proves too soft, the fix is a per-Record snapshot of titles taken at publish (a decision, not built).
- **Neutral album ordinals are the frozen `muses.ordinal`** (D-L2-37, assigned by trigger, never reshuffled by a restore or re-backfill); only A2/S2 use them. The draft's dependence on L1 backfill ordering is gone.
- **M4a coupling.** L2 extends M4a's cover package, moves M4a's `tagged` block builders into `flacmeta` and adds Record albums to M4a's Subsonic server; M4a's own goldens are the regression guard for all three (`TestCover_M4aGoldenUnchanged`, `TestTagged_StillByteIdentical`, `TestSubsonic_LineageUnchanged`). If M4a code changes under a lane, rebase and re-run `make verify`.
- **`tools/list` growth** (+5 tools) is bounded by the ceiling and the demotion order (C129).
- **Scope valve.** If time is short, cut in this order (D-L2-29): the weekly 300-sequence CI job, `unfiled` (with A2), the `side` label UI, `cover_reroll`, the Energy curve; the tape lanes (T1/T2) and A2/S2 are separable by design.
- **Merged Subsonic lists.** S1 merges Record albums into every `getAlbumList2` type next to lineage albums; pagination across the union and the aggregates over resolved Takes are the subtle part. `TestSubsonic_LineageUnchanged` pins the no-Record behaviour byte for byte; if the merge proves heavy, S1 falls back to listing Records only in `newest`, `alphabeticalByName`, `random`, `getIndexes` and `search3` (documented in `docs/subsonic.md`).
- **Parallel-lane merge risk.** Eleven lanes touch one repository at once; the hot-spot table in "Lanes and tasks" gives every shared file one owner, stubs live in per-lane files, generated API files are frozen after F2 and regenerated (never hand-merged) on rebase. A change to a frozen file is a small `fix(l2-freeze)` PR that every lane rebases onto.

## Revision log

### Revision 4 (2026-10-30) — as shipped: what each lane changed versus revision 3

Contracts above are the design of record; where the code differs, this log says how. Error codes: `SLOT_ORDER_MISMATCH` and `UNKNOWN_SLOT` answer **422** as shipped (earlier notes said 409/404); both carry the op index in `details.op`.

- **F1 (#51).** `Slot` gains `SongRetired`; warnings come in the fixed order pin_ineligible, empty, retired_song, undecided_pick. A pinned Take missing from the take table resolves to status `absent` with state `pin_ineligible`. The take filter vocabulary has 20 keys (no `origin[]`), the song vocabulary 21. When the loudness peak guard reaches the cut floor the cut clamp wins (`cut_clamped`). Change texts for kinds the plan left open were fixed (see the golden). Fixture Muses are created through the Muse service, not SQL. `TestNoAlbumWord_L2` is scoped to the L2 packages, files named `*l2*.go` and the L2 web files.
- **F2 (#54).** No L2 operation is a dedicated MCP tool (operator decision): all 15 MCP-faced operations are reached through `call_operation`. `get_muse.catalog` is `{id, neutral_name}`. `SlotOp.position` is a pointer on the wire. `Options.ManualSync` became `Options.Sync`. The L2 filter fields sit in the existing filter structs. Nullable object fields use the inline nullable schema.
- **F3 (#56).** The secure nav row is labelled "Opt-ins"; the page title stays "Curation".
- **C1 (#57).** Cover mosaic: each part tile's seed is XORed with one identity-stream draw so the Record seed moves the layout while the palette stays identity-only. Liner gained unexported fields for subtitle, note and date; the app cost total is a footer line that `Description()` never carries.
- **R-a (#58).** `SlotOp.Position` absent means append (`PositionEnd`, -1); 0 or out of range is `INVALID_POSITION`. No-op calls write nothing and do not bump the revision. A playable slot is one in state `ok`. `move`/`reorder` write one `slot_moved` row per displaced slot. `unpin` restarts the Syncer cursor (`sync_cursor.go`). Declining a proposal writes `proposal_dismissed` with reason `declined`.
- **K (#59).** `None` ("matches nothing") added to the list filters for empty intersections. `crate_id` is resolved in the handlers, not in `FilterSQL`. Library joins are gated by `hasTable`. `songs.List` picks its page on ids in one pass (an order of magnitude cheaper at 10 000 Songs). Counts and warnings come from the Crate service.
- **A (#60).** Track numbers in a Record album are contiguous over the listed songs. The token reads the export flag too. `pending` counts floating slots whose Song has a Pick event beyond the cursor; `review` counts live slots of published Records. `get_stats.curation` is attached in the API layer.
- **R-b (#61).** Anomaly codes: `order_mismatch`, `duplicate_song_slot`, `slot_song_missing`, `retired_song`, `pin_not_in_song`, `changes_chain_broken`, `changes_not_created`, `changes_replay`, `cover_seed`, `first_published`, `published_empty`, `crate_item`, `crate_filter_invalid`, `tape_key`, `tape_lease`, `setting_key`, `setting_value`. `retired_song` is a warning for every live slot on a retired Song. The command-line process does not install the Crate-filter validator, so `library verify` there does not raise `crate_filter_invalid` (serve does).
- **T1 (#62).** `RunOpts.Stdin` added to the process runner (pipe plus a detached copier). The encoder gets `--input-size` (an upper bound) and `--seekpoint=10s`; the real STREAMINFO count wins. Mono sources are decoded with `-ac 2`. The pipeline appends the album peak, `CHAPTERnnn` and the text CUESHEET. Failures are `*tape.RenderError`. Spike: ffmpeg reads the binary CUESHEET as unnamed sample-exact chapters and `CHAPTERnnn` as named millisecond chapters.
- **V2 (#63).** `TestRecordView_NoN1` bound is 16 statements. Snapshot tags: TITLE, ALBUM, ARTIST, DATE, GENRE, SUBTITLE, LINER_NOTES.
- **I1 (#65).** Coverage floors are the measured values, not the C132.3 targets; the perf tests run in their own job, not under `-race`.
- **A2 (#66).** The muse directory is an adapter (the Muse package cannot import the catalog). A hidden published Record does not take its Songs out of the auto-albums.
- **W1 (#67), W2 (#68), W3 (#71).** The change-text golden is mirrored for the web test. Published Records are editable by human and device principals, publishing is human-only. `PlayOptions.subtitle` replaces the banned word. A tape that had to be exported autoplays only after a tap.
- **T2 (#69).** The tape key covers the flac version, build version, items, resolved target, include_liner, muse_names, sorted tags, chapter names, description and cover key; not the revision, id, principal or accepted warnings. Stored error messages are path-masked and capped at 500 characters.
- **S1 (#70).** Record albums are merged into the Subsonic lists additively; a list request costs 1 + 2N statements over published Records. Details in `docs/subsonic.md`.
- **S2 (#72).** `albums.ListWithSongs` and `albums.Entry` were added. After a restart the Muse-driven part of `lastModified` is an in-process observation, so a Muse rename that happened while the server was down can leave a stale client cache.
- **#75.** The Pick-upgrade acceptance drives the Pick by a counted human star rating (reason `stars`); the picks ladder in the rig is the real duels service.

Follow-ups left open: align the publish-preview chapter names with the file (" [lossy source]", no side); guard `tape:<id>` current items in consumers that assume a track id; re-point three Crate parity cases and the album-catalog acceptance at the real operations.

