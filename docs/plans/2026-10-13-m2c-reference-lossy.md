# M2c — Reference & Lossy Imports — Implementation Plan (revision 2, implemented)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, `docs/plans/2026-10-12-roadmap.md`, the M1 plan (contracts C10–C24,
> decisions D-M1-*), the M2a plan (contracts C25–C42, D-M2a-*) and the M2b plan (contracts C43–C56,
> D-M2b-*) first. M2c is a **small** milestone that builds on those exact contracts and continues the
> numbering: M2c contracts are **C57–C66**, decisions **D-M2c-1…14**. M2c branches from `main` **after
> M2b has merged** (branch `m2c`). Where M2c changes an earlier contract, the task that makes the change
> records it in that plan document in the same commit (named in each task's **Files**). Amended contracts:
>   (Revision 2: contract numbers re-checked against the merged plans; the previous text attributed the
>   media route to M1 C17 and the schema-version assertion to M2a C27.)
> - **M1:** **D-M1-2** (still the default: a plain `import` never imports recipe-less audio; M2c adds an
>   explicit opt-in), C11 (schema: one column, new triggers), C12 (recipe `engine` gains `unknown`),
>   C14 (`Analyzer.CountFrames`), C15 (ingest: reference tags, `IngestLossy`), C16 (importer: classification
>   of standalone MP3s, mapping target `reference`, reference lineage grouping, report fields, CLI flags),
>   C17 (library service: `Media`, `Service.Resolve`), C18 (media handler: the lossy master is served as
>   `.mp3`), C19 (`get_media_links`, `search_tracks`, `get_track` shapes), C22 (player format choice, device
>   probe).
> - **M2a:** C32 (drafts: `RECIPE_UNKNOWN`), C33 (`songs.jsonl` export excludes unknown recipes; playlist
>   ZIP carries lossy masters), C34 (backup: the `export/songs.jsonl` writer), C35 (`/context`), C40 (web
>   library filters and badges), C41 (`tools/list` golden and `toolsCeilingBytes`), C42 (fixtures).
> - **M2b:** C44 (the one hard-coded schema-version assertion becomes 5), C45 (`library.TrackRef`),
>   C46/C51 (clip and Ears work-dir links keep the master's extension; Ears `lossy_source` blind spot),
>   C53 (`duels.TrackBrief` and `ears.Side` gain `lossy`/`recipe_known`; a duel's `TrackBrief` is null until
>   reveal, so the flags cannot leak slot identity), C54 (web duel reveal and compare views).
>
> **Public/private split.** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` is public-safe (no real hostnames, titles, artists, lyrics, request-id
> prefixes, counts from the real archive or personal data; `mm.example.com` and synthetic examples only).
> Only that part is copied to `docs/plans/<date>-m2c-reference-lossy.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`); the copy is `git add`ed and checked with
> `scripts/public-gate.sh --staged` before it is committed. The appendix (real titles, observed archive
> facts, expected dry-run counts) is never copied.
>
> **Task ids** carry the `M2c-` prefix. Lane labels (`MM_LANE`): `m2c-found`, `m2c-media`, `m2c-import`,
> `m2c-data`, `m2c-web`, `m2c-int`.
>
> **Revision 1** incorporates the operator's final decisions: (1) recipe-less **reference tracks** for
> original masters that have lost their recipe; (2) **lossy reference takes**: standalone MP3 files that
> have **no recipe and never will** import as MP3-master tracks that are *also* recipe-less (a lossy track
> is always a reference track), grouped per song as inferred siblings; (3) everything the app generates
> later stays FLAC-only; (4) a clean seam for a future M5 "Recover recipe" feature, which is documented
> here and **not built**. Matching MP3s to recipes was dropped as YAGNI (D-M2c-4).
>
> **Revision 2** reconciles the plan with the merged M0 + M1 + M2a + M2b code (branch `m2c` starts at `main`
> at the M2b merge). Every symbol, file, table, column, migration number, op id, Makefile target, CI job,
> test helper and contract number the plan names was checked; the corrections are indexed in "Baseline
> reconciliation" below and applied in place, each marked "(rev 2)". The unmerged M3 draft also starts its
> contracts at C57; M2c merges first, so M3 renumbers from C67 (noted in the reconciliation table).
> **Binding lessons from M2b:** never mutate process globals (`slog.SetDefault` etc.) in `run()` or
> handlers; e2e specs must not depend on global counters that other specs change; startup waits must
> tolerate instrumented (`-cover`) runs; new tests are safe under `t.Parallel()` and `-shuffle` (no shared
> buffers, loggers or globals); if the CI `go` job exceeds 6 minutes M2c-X1 splits it (C67).

**Goal:** the operator can bring audio into the library that has no recipe, deliberately and visibly.
`metamusic import --dry-run` lists every unmatched FLAC and every standalone MP3 with what it would become;
`--reference-unmatched` and/or `--reference-lossy` (or mapping rows with the target `reference`) import them
as ordinary tracks (playable with Range, peaks, loudness and bandwidth, rateable, commentable, duel-able,
in playlists and backups) linked to a stub recipe with `engine = 'unknown'`. The UI, the API, MCP and
`/context` show **"recipe unknown"** and, for MP3 masters, **"lossy reference"**; the library can filter
on both. Stub recipes are excluded from `songs.jsonl` export and refused by lint, draft creation and as
mothers, in the database and in Go (`RecipeKnown`, ready for M3). A plain `import` still never imports
recipe-less audio.

**Architecture:** no new service and no new operation. One migration (`0005_reference.sql`: a `tracks.lossy`
column and nine triggers). `internal/library` gains the predicates and the reference-take write path.
`internal/media/mp3info` (new leaf) parses an MPEG audio header; `internal/media/ingest` gains the
reference tag set and `IngestLossy` (copy → verify → decode-based analysis → freeze 0444 → claim; no
`metaflac`, no MP3 rendition); `internal/analysis` gains `CountFrames`. `internal/importer` gains the two
opt-ins, the `reference` mapping target, standalone-MP3 candidates and per-song sibling grouping. Existing
operations change only their shapes (nullable `flac` link, two booleans, two filters). The Svelte app gains
badges, two filters and a player fallback.

**Tech stack:** no new Go dependency, no new runtime tool, no new web dependency. Runtime tools: `ffmpeg`
(decode; already required), no `ffprobe`. MP3 header parsing is stdlib Go.

**Out of scope (YAGNI, with owner milestone):** **"Recover recipe"** (Whisper lyrics + LLM style proposal →
draft; M5, seam in C65); matching standalone MP3s to recipes (D-M2c-4; nothing in the schema prevents it
later, the trigger would be relaxed in that milestone); ID3 title/artist reading from MP3 masters (titles
come from the mapping row or the file name); an MP3 → FLAC "upgrade"; any lossy output of the app other
than the existing MP3 renditions; OpenSubsonic handling of lossy masters (M4a must serve `master` MP3 as
the raw stream for them); re-linking a reference track to a recipe later (recipes and `tracks.recipe_id`
are immutable by design — a recovered recipe becomes a new draft/track, C65); a facet count for references;
lossy tracks in `export_playlist_zip` beyond including their master (no re-encode).

---

## Dependencies and sequencing

- **Hard:** M0, M1, M2a **and M2b merged** (migration `0004_duels.sql`, schema version 4, the clip/duels/ears
  services, `main` at `ec8275f`). Revision 2 was written against that tree; **M2c-B1 starts with a short
  baseline re-check** (below) and records the result in the commit message.
- **Baseline re-check (M2c-B1, first step):**
  1. `internal/store/migrations/` ends at `0004_duels.sql`; the next file is `0005_reference.sql` and
     `user_version` becomes 5. The only hard-coded schema-version assertion is
     `internal/httpserver/server_test.go` (`m["schema_version"] != float64(4)`); update it to 5. The backup
     manifest tests assert `>= 3` / the live version and need no change.
  2. `analysis.Analyzer` has exactly `ToolVersion`, `Loudness`, `Scan`. Its only production implementation is
     `*ffmpeg.FFmpeg`; no test fake implements it (the ingest tests embed the interface in `overrideTP`).
     M2b's capabilities are **separate** interfaces (`clip.WindowAnalyzer`, `ears.TierA`) satisfied by
     `*ffmpeg.FFmpeg`; `cliptest`/`earstest` fakes implement those and do not change.
  3. `library.TrackRef`/`trackRefSelect` (`internal/library/refs.go`) carry `MasterPath`; `clip.Service`
     links the master as `master.flac` in **three** places (`workMaster`, `Measure`, the render call) and
     `internal/ears/analyze.go` links it in `compute` (two lines). `Service.Resolve` is in
     `internal/library/tracks.go` (cases `flac`, `mp3`, `jpg`); `serve.Chain` tries resolvers in order and
     passes on `ErrNotFound` only. `export.Source` is implemented by `exports.NewDBSource` only.
  4. Duels: `duels.TrackBrief` is reachable only through `Slot.Track`, which is `null` until the duel is
     revealed (D-M2b-4); M2c adds fields to `TrackBrief` and `ears.Side` and **nothing** to unrevealed
     payloads.
- **Hard on the data side:** none. The real archive is only needed for M2c-X1 (operator-assisted).

---

## Baseline reconciliation (revision 2: merged M0 + M1 + M2a + M2b code)

Checked against `main` (`ec8275f`). Where the plan and the code disagreed, the plan was fixed (not the code).

**Verified as the plan assumed.** Migrations `0001`-`0004` (gap-free; `0005_reference.sql` is next); the DDL of
C58 applied cleanly on top of 0001-0004 in a scratch SQLite 3.53 and every trigger fired as specified
(stub shape, lossy shape, reference rule, reference lineage, `mp3_320` rendition for a lossy track, draft
insert/update with a reference mother); `tracks_audio_identity UNIQUE(frames_sha256, total_samples)` holds
for lossy rows because `frames_sha256 = master_sha256`; `recipes` CHECKs (`$.id`, `$.seed`) are satisfied by
the stub JSON; `tracks_frozen`/`recipes_immutable`/`recipes_no_delete` exist; `tracks.recipe_id NOT NULL`,
`origin IN ('import','job')`, `bits_per_sample BETWEEN 4 AND 32`, `channels BETWEEN 1 AND 8`,
`total_samples > 0`; `renditions` PK `(track_id, kind)` with kinds `mp3_320`/`peaks` (+ M2b `spectrogram`);
`mutations` (one row per child, `params_json {"rule":…}`, operator `inferred_sibling` only);
`recipe_drafts.parent_track_id`; `library.enumRule`, `NewTake`, `NewMutation`, `InsertTake` (tag
inheritance runs for every child with a parent, so reference-group children inherit the root's human tags),
`Snapshot`/`LibrarySnapshot`; `ingest.Request/Result/Pipeline/Discard` (Discard removes any `audio/masters/*`
file in `Created`, so `.mp3` masters need no change), `masterName`, `claimMaster`, `tagLines`, peaks named
`derived/<aa>/<sha>.peaks.json`, `TestIngest_ToolTimeouts`, `TestNoStubMarker`; importer `Options`,
`Ingester` (`Ingest`, `Discard`; `fakeIngester` is its only test implementation, in `helpers_test.go`),
`builder.classifyMP3`, `matchGroups`, `evalMember`, `placeTakes`, `placeScores`, `planLineage`,
`predictFulfilment`, `assemble`, `existingNodes`, `commitTake`, `ingestOne`, `newTake`; mapping parser
(`MapRow`, `bad_mapping_row`); `exports` ZIP (`skipID3`, `renditions()`, `MissingMP3`); drafts
(`UploadIssue`, `Error{Status,Code,Msg,Details}`, `checkParent`, `knownHashes`); `trackctx` (`order` map,
`UseM2b`, `UseScoreABC`, `_links.stream`); `analysis.BlindSpots` (six fixed entries); Makefile targets
`build web openapi test race lint check e2e cover seed routing gate hooks tools fixture-db dev docker`;
scripts `loop`, `e2e.sh`, `public-gate.sh --staged`; CI jobs `go lint web e2e coverage`; Playwright
projects `chrome chrome-pixel7 chrome-iphone15 firefox` plus the `listen-*` phase-2 projects;
`store.OpenRaw` + `store.MigrateTo` (`export_test.go`); `TestNoReplaceSQL`, `TestNoDeleteSQL`,
`TestNoTestHelpersInBinary`; 58 registered operations (`make openapi` paths) and the ops named in C63 with
`mcp: tool` for `search_tracks`, `get_track`, `get_track_context`, `get_media_links`, `create_draft`,
`lint_recipe`, `export_jsonl`.

**Corrections applied.**

| Area | Plan revision 1 said | Merged code has | Fix (where) |
|---|---|---|---|
| Contract numbers | media route is M1 C17; schema-version assertion is M2a C27 | M1 C17 = library service, **C18** = media handler; the assertion is M2b **C44**; tooling is M2a C41 | header |
| Numbering collision | none | the unmerged M3 draft also starts at C57 | M2c keeps C57-C66 (+C67 below); M3 renumbers from C68. No collision with M0-M2b |
| `Analyzer` interface | gains a method; "list every implementation (production + test fakes)"; M2b adds capability methods to it | three methods; one production impl; M2b capabilities are separate interfaces; no fake implements `Analyzer` | Dependencies, C60, B1, B2 |
| Clip work-dir link | `workMaster` only | three `master.flac` uses in `clip/service.go` (`workMaster`, `Measure`, `render`) | C60, B2 |
| Snapshot key | `AudioIDs` keyed by `flacmeta.AudioID(frames, total)` | exactly that format (`"<frames sha>:<total>"`), built with `fmt.Sprintf` in `Snapshot`; `SnapTrack.SourceSHA` (not `SourceSHA256`); `SnapRecipe` has no engine | C59 |
| Mother linking | "write.go mothers/relink query" | `library.RelinkVariantiMothers` (write.go), `applyState.relinkMothers` (apply.go) and `planMothers` (place.go) | C59, C61, B1, B3 |
| Importer classification | `classify.go` / `scan.go` hold the MP3 decision; `humanize` titles | `builder.classifyMP3` is in `inventory.go`; `classify.go` holds the kinds; titles come from `builder.takeTitle`; only FLACs become `audioFile`s (`readAudio`) | C61, B3 |
| Export empty case | "empty result plus the note" | `export.Build` returns `ErrEmpty` with no `Result`; notes are `Result.Warnings` / `JSONLInfo.Warnings` | C62 |
| Backup | not mentioned | `backup.writeExport` calls `export.Build` over every track; a stub in the selection would break it | C62, B4 |
| Parity cases | `servertest` `parityCases` | `parityCases` is in `internal/mcpgen/parity_test.go` | C66, B1 |
| `tools/list` budget | "two optional parameters" only | `TestToolsList_Ceiling`: 25 tools, 55 308 bytes against `toolsCeilingBytes = 55500` (192 bytes headroom); changed first-class output schemas and two new parameters exceed it | C63, B1 |
| Nullable `flac` | `FlacLink` nullable | Huma refuses `nullable:"true"` on an object `$ref`; the repo convention is a `huma.SchemaProvider` shadow type (`library.nullableObject`) | C59, C63 |
| `synth.MP3` | `MP3(ctx, tools, dst, tone, seconds, o)`; 440 Hz tone, 44.1 kHz | `Tone` already holds `Seconds`; synth renders 48 kHz stereo partials (220/331 Hz) or band-limited noise; sample rate is a package constant | C60, B2 |
| Seed profiles | `seed-synthetic --references` plus `ProfileReference` | `--profile` accepts `dev\|e2e`; `archivetest.SyntheticArchive` accepts `acceptance\|e2e` only; e2e global setup seeds once and the seeder refuses a library with other import runs | C66, B3, W1 |
| Shared e2e library | "specs that count library rows are re-baselined" | `m1-library`, `m1-pwa`, `m1-import` hard-code 9 tracks / 9 audio files; `ownTracks` filters `Syn song` titles | W1 |
| Playwright | "runs on desktop Chrome and the Pixel 7 project only" | an `ignoreM2c(keep)` helper in the config's style, plus the skip accounting comment | W1 |
| Stub markers | stubs return `ErrUnsupported` | the repo convention is `STUB(<task>)` markers checked by `TestNoStubMarker`/the final grep | B1 |
| Coverage threshold | "95 % media range handling" | `coverage.thresholds` has `internal/media/serve 95`, `internal/... 85` | X1 |
| CI | "if the go job exceeds 6 min, split" | `go` job (timeout 10 min) uploads `covdata-unit`; `coverage` needs `[go, e2e]` | C67, X1 |
| Risks | "M2b is still moving" | M2b is merged | Risks |

## Decisions recorded by this plan

| Id | Decision | Why |
|---|---|---|
| D-M2c-1 | A **reference track** is an ordinary `tracks` row whose `recipe_id` points at a **stub recipe**: `recipes.engine = 'unknown'`, `request_id = ref_<slug>_<hex8>` (`slug` = `Slug(title)`, `hex8` = first 8 hex digits of `sha256(audio id)`), `request_json = {"id":"<request_id>","seed":0,"title":"<humanised title>"}`, empty `style`/`lyrics`/`cot`, `seed = 0`, no sampling, `core_hash = hash`. `tracks.recipe_id` stays `NOT NULL`. Reference tracks carry `import_rule = 'reference'`, are roots (generation 0) or members of a reference group (D-M2c-8), never get scores, sections, artifacts or fulfilment. | Keeps every query, FTS trigger, foreign key and export that joins `tracks → recipes` working; recipes stay immutable (D-M1-2's reasoning against a nullable set-once column still holds). The `seed = 0` and `$.seed` in the JSON exist only because M1's table CHECKs (`seed BETWEEN …`, `json_extract(request_json,'$.seed') IS seed`) require them; **`engine = 'unknown'` is the only discriminator**, never `seed = 0` (0 is a valid real seed). Including the audio-id hash makes two files with the same title distinct recipes and the id stable across re-imports. |
| D-M2c-2 | **Import is opt-in and explicit.** A plain `import` behaves exactly as before (D-M1-2). Recipe-less audio is imported only when (a) `--reference-unmatched` is given (FLAC identities whose status would be `unmatched`; **never** `ambiguous`), (b) `--reference-lossy` is given (standalone MP3s, D-M2c-3), or (c) a mapping row names the target `reference`. A mapping `-` (skip) wins over every opt-in; a mapping `reference` wins over rule matching (warning `reference_overrides_match` when a recipe would have matched). The dry-run always lists every candidate, with its would-be status, so the opt-in is informed. | The operator's "never silently" requirement. Two flags rather than one because the two sets differ in value and risk: unmatched FLACs are the operator's own masters; standalone MP3s in an inbox may be previews, exports or throw-away renders that nobody wants in a lossless library. Per-file control is the mapping row. The flags are part of the reviewed plan: they are recorded in the report and compared by `--plan` (C57). |
| D-M2c-3 | A **lossy track** is `tracks.lossy = 1`: its master **is** the MP3 file, copied byte-for-byte from the inbox, frozen like a FLAC master (sha256-addressed under `audio/masters/…mp3`, mode 0444, never rewritten, no tagging, no `metaflac`, no FLAC invariants), with **no** `mp3_320` rendition (the master is served for both qualities), peaks and loudness/bandwidth computed by ffmpeg decode. Non-physical column values are fixed by trigger: `bits_per_sample = 16` (nominal decode depth, **not** a property of the file), `audio_md5` all zeros, `frames_sha256 = source_sha256 = master_sha256`, `origin = 'import'`. `total_samples`/`duration_s` come from a counting decode. The audio identity for dry-run/re-import is `mp3:<sha256 of the file>`. | Loosening `bits_per_sample`/`total_samples`/`frames_sha256` CHECKs would need a table rebuild of the most-referenced table; documented nominal values plus triggers keep the migration to one `ALTER TABLE` and make the lossy shape a schema invariant. Hashing the whole file is the only identity that survives without an MP3 frame parser; the file is never modified, so file hash = master hash. |
| D-M2c-4 | **A lossy track is always a reference track** (trigger `tracks_reference_rule`). Standalone MP3s are **not** matched against recipes (no ranked rules, no `_p<N>` ordinal rule, no proposals). A mapping row for an MP3 may only be `reference` or `-`; any other `request_id` is the inventory error `lossy_needs_reference`. The general capability "MP3 matched to a recipe imports as a lossy take with that recipe" is **YAGNI**. | Operator confirmed the MP3 pilot has no recipe file and never will; the capability would need rule plumbing, ambiguity handling and a test matrix for no current user. The schema is not closed to it: a later milestone relaxes one trigger. |
| D-M2c-5 | **Stub recipes are excluded from `songs.jsonl` exports — always, with no flag** — and the export answers a note `recipe_unknown_excluded` naming the affected track ids. The playlist ZIP *does* include reference tracks (it ships audio, not recipes). | An export line is a request to *render*; a stub has no lyrics or style, so any line would be invalid for `yue2 batch` or, worse, render a bogus song. A flag would only add a way to produce unusable files. |
| D-M2c-6 | **Refusal is layered.** (1) Go: `drafts` refuses to create/update a draft or upload line whose request `id` equals an existing stub's `request_id`, and a draft whose `parent_track_id` is a reference track — error `RECIPE_UNKNOWN` (422); `lint_recipe` reports the issue code `recipe_unknown` (severity error); (2) DB: triggers `recipe_drafts_ref_parent_*` and `tracks_reference_lineage` make the same states impossible even for a buggy writer; (3) M3: `library.Service.RecipeKnown` / `RequireRecipeKnown` (returns `RECIPE_UNKNOWN`) and `LosslessMaster` / `RequireLosslessMaster` (returns `LOSSY_MASTER`) are added and unit-tested now; M3's estimate/create paths must call them for every mother. | The operator asked for stable codes and for M3 to have the predicate. Defence in depth is cheap here (three small triggers) and the invariants are exactly the kind M0's rules say belong in the schema. |
| D-M2c-7 | **Serving.** A lossy master is served at `/media/<master sha>.mp3` (`Content-Type: audio/mpeg`, Range, immutable cache); the same sha under `.flac` is **404** (`Resolve` filters `lossy = 0`). `get_media_links` returns `flac: null`, `lossy: true`, and an `mp3` link whose `sha256` is the master's and whose `bitrate_kbps` is `round(master_bytes·8 / duration_s / 1000)`. | The M1 media route addresses by sha + extension; the extension is the truth about the container. A null `flac` is the honest contract for "there is no lossless file"; clients must not fall back by guessing. |
| D-M2c-8 | **Reference grouping.** Standalone MP3s named `<song>_p<N>.mp3` are siblings of one song: the lowest `N` (ties: earliest `recorded_at`, then sha) is the group root; each other member is a generation-1 child with an **inferred** mutation (`operator = 'inferred_sibling'`, `inferred = 1`, rule `reference_group`). A re-run that adds `_p4` later attaches to the existing group root (existing tracks are recognised by `lossy = 1`, `import_rule = 'reference'` and a slug matching `<song>_p<N>`). A mapping `parent` (path/sha form) overrides; `-` opts out. FLAC references are not grouped automatically (mapping `parent` still works). Reference tracks never take part in M1's seed-family inference. The title of a grouped file is the default humanised stem (`Song name p1`), so the group reads naturally in the library. | The operator wants the three takes of a song together for listening and duels (duels, ladder and lineage views are keyed by root). Using the file-name convention the operator already has costs one regex; anything fuzzier would be a guess. Reusing `inferred_sibling` keeps the Operator Ledger vocabulary unchanged. |
| D-M2c-9 | **Device and player.** A track without a lossless file always plays its MP3 link. The device quality setting and the FLAC probe are untouched: playing a lossy track never starts a probe, never records `probe_result = failed`, and never changes `quality`. In `auto`/`lossless` modes the player shows a small `MP3` chip for such tracks. Listen events record `format: "mp3"` as usual. | A failed probe means "this device cannot decode FLAC"; a lossy track says nothing about that, and an MP3 error must not downgrade the device. |
| D-M2c-10 | **Analysis is comparable but labelled.** Loudness (`mm-loudness/1`), spectrum (`mm-spectrum/1`) and peaks run on the decoded MP3 with the unchanged definitions and versions; the track's `lossy` flag is the label. Ears Tier A runs unchanged and adds the blind spot `lossy_source`; M2b duels and `compare_tracks` accept lossy tracks (a bandwidth cue between an MP3 and a FLAC take is a known, accepted blind-test leak, surfaced in `/context` and after reveal, never before). | Fork-free analysis keeps one code path and one set of golden parsers; the blind spot tells readers what to distrust. |
| D-M2c-11 | **Everything generated by the app later stays FLAC-only.** The trigger `tracks_lossy_shape` requires `origin = 'import'` for `lossy = 1`; M3's job path inserts `origin = 'job'` and therefore cannot create a lossy track. The existing `mp3_320` rendition stays the only lossy *derivative*. | Enforced where it cannot be forgotten. |
| D-M2c-12 | **Reference FLAC tags:** the freeze writes the usual eight tags with `MM_REFERENCE=1` **replacing** `MM_SEED`; `MM_RECIPE_ID`/`MM_REQUEST_ID` carry the stub's ids. Lossy masters get no tags (they are never rewritten). Match rule 3 (`tag`) can never resolve a stub id, so a re-import of an exported reference master stays a reference. | `MM_SEED=0` would claim a real seed. |
| D-M2c-13 | **No new operations and no new `Meta`.** REST/MCP/SPA parity is kept by changing the shapes of `search_tracks`, `get_track`, `get_media_links` and `get_track_context`, and by the existing draft/export/lint ops reporting the new refusals. | CLAUDE.md rule 1: a capability exists iff it is a registered op; filtering and display are capabilities of existing ops. |
| D-M2c-14 | **Recover seam (documented, unbuilt):** M5's "Recover recipe" is operator `recover` (MCP/REST op `recover_recipe`) on a track with `RecipeKnown = false` — lossless *or* lossy; it decodes the master (an MP3 decodes like a FLAC), attaches Whisper output as analyses/sections of the **reference track**, and creates a **draft** (`origin = 'recovered'`) — never mutates the reference. Details and what M2c provides: C65. | The operator asked that the seam be clean for lossy references too. |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C57. Configuration, CLI and flags

No new environment variable. `internal/importer.Options` gains:

```go
ReferenceUnmatched bool // D-M2c-2(a): unmatched (never ambiguous) FLAC identities become reference tracks
ReferenceLossy     bool // D-M2c-2(b): standalone MP3 candidates become lossy reference tracks
```

CLI (`cmd/metamusic/cmd_import.go`): `--reference-unmatched`, `--reference-lossy`, each valid in every mode
(`--dry-run`, `--plan`, `--yes`; the existing rule that `--dry-run` cannot be combined with `--plan`/`--yes`
is unchanged). `Report` gains `Flags {ReferenceUnmatched, ReferenceLossy bool}`
(`json:"flags"`); because the flags change the plan, **`--plan <report>` with flags different from the
report's** is a usage error (`import: --plan was reviewed with different reference flags`, exit 2) before
any work; the digest check (`PLAN_CHANGED`) remains the second line of defence. `seed-synthetic` gains
`--references` (C66; valid with `--profile dev|e2e`, it selects `ProfileReference` and imports with both flags).
The flags are parsed into `importer.Options`; nothing is stored in a package variable. The usage text in `cmd/metamusic/main.go` lists the flags. A `--yes` run with a flag
applies the current plan unreviewed, as before.

### C58. Migration `internal/store/migrations/0005_reference.sql` (full DDL)

```sql
-- 0005_reference.sql — M2c: reference tracks (recipe-less) and lossy masters.

-- A lossy track's master is an MP3 file. One column; the shape is enforced by triggers (D-M2c-3).
ALTER TABLE tracks ADD COLUMN lossy INTEGER NOT NULL DEFAULT 0 CHECK (lossy IN (0,1));
CREATE INDEX tracks_lossy ON tracks(lossy) WHERE lossy = 1;
CREATE TRIGGER tracks_lossy_frozen BEFORE UPDATE OF lossy ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;

-- A stub recipe (engine 'unknown') has exactly this shape (D-M2c-1): {id, seed:0, title} and nothing else.
CREATE TRIGGER recipes_unknown_shape BEFORE INSERT ON recipes
WHEN NEW.engine = 'unknown' AND NOT (
       NEW.style = '' AND NEW.lyrics = '' AND NEW.cot = '' AND NEW.seed = 0
   AND NEW.sampling_json IS NULL AND NEW.created_by = 'import' AND NEW.core_hash = NEW.hash
   AND json_type(NEW.request_json, '$.title') = 'text'
   AND (SELECT COUNT(*) FROM json_each(NEW.request_json)) = 3)
BEGIN SELECT RAISE(ABORT, 'recipes: unknown-engine shape'); END;

-- Lossy shape: the master is an .mp3 iff lossy; a lossy row is an untouched import (D-M2c-3, D-M2c-11).
CREATE TRIGGER tracks_lossy_shape BEFORE INSERT ON tracks
WHEN (NEW.lossy = 1) <> (lower(NEW.master_path) LIKE '%.mp3')
  OR (NEW.lossy = 1 AND (NEW.origin <> 'import' OR NEW.bits_per_sample <> 16 OR NEW.channels > 2
      OR NEW.source_sha256 <> NEW.master_sha256 OR NEW.frames_sha256 <> NEW.master_sha256
      OR NEW.audio_md5 <> '00000000000000000000000000000000'))
BEGIN SELECT RAISE(ABORT, 'tracks: lossy shape'); END;

-- import_rule 'reference' <=> the recipe is a stub; a lossy track is always a reference (D-M2c-4).
CREATE TRIGGER tracks_reference_rule BEFORE INSERT ON tracks
WHEN (NEW.import_rule = 'reference')
       <> COALESCE((SELECT engine = 'unknown' FROM recipes WHERE id = NEW.recipe_id), 0)
  OR (NEW.lossy = 1 AND NEW.import_rule <> 'reference')
BEGIN SELECT RAISE(ABORT, 'tracks: reference rule'); END;

-- A reference track never has a known-recipe parent or child: no mothers from references (D-M2c-6).
CREATE TRIGGER tracks_reference_lineage BEFORE INSERT ON tracks
WHEN NEW.parent_track_id IS NOT NULL
 AND (NEW.import_rule = 'reference')
       <> COALESCE((SELECT import_rule = 'reference' FROM tracks WHERE id = NEW.parent_track_id), 0)
BEGIN SELECT RAISE(ABORT, 'tracks: reference lineage'); END;

-- Lossy masters are served for both qualities; no MP3 rendition is ever stored for them.
CREATE TRIGGER renditions_lossy_no_mp3 BEFORE INSERT ON renditions
WHEN NEW.kind = 'mp3_320' AND COALESCE((SELECT lossy FROM tracks WHERE id = NEW.track_id), 0) = 1
BEGIN SELECT RAISE(ABORT, 'renditions: a lossy track has no mp3 rendition'); END;
CREATE TRIGGER renditions_lossy_no_mp3_upd BEFORE UPDATE OF kind, track_id ON renditions
WHEN NEW.kind = 'mp3_320' AND COALESCE((SELECT lossy FROM tracks WHERE id = NEW.track_id), 0) = 1
BEGIN SELECT RAISE(ABORT, 'renditions: a lossy track has no mp3 rendition'); END;

-- A draft can never have a reference track as its mother.
CREATE TRIGGER recipe_drafts_ref_parent_ins BEFORE INSERT ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE import_rule = 'reference')
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: recipe unknown'); END;
CREATE TRIGGER recipe_drafts_ref_parent_upd BEFORE UPDATE OF parent_track_id ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE import_rule = 'reference')
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: recipe unknown'); END;
```

Rules: no table is rebuilt; existing rows get `lossy = 0` (the upgrade test proves M1/M2a/M2b rows are
unchanged and every existing track still satisfies the new insert triggers' converse: none has an `.mp3`
master). `recipes_immutable`/`recipes_no_delete` already make a stub unchangeable. `PRAGMA user_version`
becomes 5. Enumerations stay Go-validated: `enumRule` (library) gains `reference`; `recipes.engine` accepts
`unknown`. The backup/restore code needs no change (it copies every `tracks.master_path`).

### C59. Library (`internal/library`)

```go
const EngineUnknown = "unknown" // recipes.engine of a stub recipe
const RuleReference = "reference" // tracks.import_rule of a reference track

var (
    ErrRecipeUnknown = errors.New("library: RECIPE_UNKNOWN")
    ErrLossyMaster   = errors.New("library: LOSSY_MASTER")
)
type RecipeUnknownError struct{ TrackID string } // Is(ErrRecipeUnknown); Error() "library: RECIPE_UNKNOWN"
type LossyMasterError  struct{ TrackID string } // Is(ErrLossyMaster)

func RecipeKnownEngine(engine string) bool                                // engine != EngineUnknown
func (s *Service) RecipeKnown(ctx context.Context, trackID string) (bool, error)        // ErrNotFound for an unknown id
func (s *Service) RequireRecipeKnown(ctx context.Context, trackID string) error         // *RecipeUnknownError
func (s *Service) LosslessMaster(ctx context.Context, trackID string) (bool, error)     // !tracks.lossy
func (s *Service) RequireLosslessMaster(ctx context.Context, trackID string) error      // *LossyMasterError

type NewReferenceRecipe struct{ Title, Slug, AudioKey string } // AudioKey = the importer's audio id
func ReferenceRequestID(slug, audioKey string) string          // "ref_" + slug + "_" + hex8(sha256(audioKey))
func (s *Service) InsertReferenceRecipe(ctx context.Context, tx *sql.Tx, r NewReferenceRecipe) (id string, created bool, err error)
```

`InsertReferenceRecipe` writes the C58 shape: `request_json` is the canonical (sorted keys, compact,
via `internal/canonjson`) text of `{"id","seed":0,"title"}` with the title cut to 200 runes; `hash =
sha256(request_json)`, `core_hash = hash`, `engine = 'unknown'`, `created_by = 'import'`; `ON CONFLICT(hash)`
returns the existing id (`created = false`).

`NewTake` gains `Lossy bool` and `Reference bool`. `InsertTake` rules: `Reference` requires
`ImportRule == RuleReference`, no `ScoreABC`, no `Artifacts`, skips draft fulfilment and the core-hash
fallback, and writes no `track_style_terms`; `Lossy` requires `Reference`, `BitsPerSample == 16`,
`AudioMD5` zeros, `SourceSHA256 == FramesSHA256 == MasterSHA256`, and only a `peaks` rendition (an
`mp3_320` rendition is `ValidationError{Field:"rendition"}`); the `lossy` column is written. A reference
take's `Mutation`, when present, is `inferred_sibling` with rule `reference_group` or `mapping`.
`insertScoreAndSections` writes **no** rows for an empty lyrics text (test). `library.LibrarySnapshot`
(C11): `SnapRecipe` gains `Engine`, `SnapTrack` gains `Lossy` and `Reference bool` (its source-hash field is
`SourceSHA`), and `AudioIDs` keeps the existing `"<frames sha>:<total samples>"` key (what
`flacmeta.AudioID` returns) for FLAC masters and adds `"mp3:" + master_sha256` for lossy ones. The importer
never treats a snapshot recipe with `Engine == "unknown"` as a match candidate (`siblings.go` `requestIDOf`
and `existingNodes` read `snap.Recipes`).

Read-model changes (JSON snake_case, all additive):
- `TrackSummary` and `TrackDetail`: `recipe_known bool`, `lossy bool` (`recipe_known = engine <> 'unknown'`).
  `TrackDetail.Recipe.Engine` (`RecipeInfo`) is `"unknown"` and `style`/`lyrics`/`cot` are `""` for stubs; `Audio.MP3` is
  `null` for lossy tracks; `Audio.BitsPerSample` documents "nominal for lossy" in its schema doc.
- `SearchParams`: `RecipeKnown *bool`, `Lossy *bool`, compiled to `(r.engine <> 'unknown') = ?` and
  `t.lossy = ?`. Neither touches FTS.
- `TrackRef`: `Lossy`, `RecipeKnown bool` (clip, duels, Ears and trackctx read them; M2b's
  `trackRefSelect` gains `t.lossy, (r.engine <> 'unknown')`).
- `MediaSet` (`tracks.go`; today `Flac FlacMedia`, `Media()` returns `ErrMediaUnavailable` when there is no
  `mp3_320` rendition, which must not happen for lossy tracks): `Flac *FlacMedia` (nil when lossy), `MP3 MP3Media` (the master for lossy: `SHA256 =
  master_sha256`, `Bytes = master_bytes`, `BitrateKbps` computed per D-M2c-7), `Lossy bool`.
- `Service.Resolve`: `flac` → `… WHERE master_sha256 = ? AND lossy = 0`; `mp3` → `SELECT path, created_at
  FROM renditions WHERE kind = 'mp3_320' AND sha256 = ? UNION ALL SELECT master_path, created_at FROM
  tracks WHERE lossy = 1 AND master_sha256 = ? LIMIT 1` (content type `audio/mpeg`).
- Mother inference that groups by `request_id` — `library.RelinkVariantiMothers` (`write.go`), the importer's
  `applyState.relinkMothers` (`apply.go`) and `planMothers` (`place.go`) — adds `AND r.engine <> 'unknown'` (and
  skips reference nodes), so a stub can never become a draft's mother (the C58 trigger would abort the apply).

### C60. Media: `mp3info`, analyzer, ingest, work-dir links, synth

**`internal/media/mp3info`** (new leaf, stdlib only):

```go
var ErrNotMP3 = errors.New("mp3info: not an MPEG audio layer III stream")
type Info struct{ MPEGVersion string /* "1","2","2.5" */; SampleRate, Channels, BitrateKbps int }
// Parse skips one ID3v2 tag (syncsafe size, footer flag), then requires two consecutive valid layer III
// frame headers within the next 64 KiB (resync allowed over junk); it reads at most 128 KiB.
func Parse(r io.ReaderAt, size int64) (Info, error)
```
Channels is 1 or 2. Layer I/II, free-format and reserved fields are `ErrNotMP3`. A file that starts with
`ID3` followed by a zero-size tag and no frame (the M1 synthetic "fake mp3") is `ErrNotMP3`.

**`internal/analysis`**: the `Analyzer` interface gains
`CountFrames(ctx context.Context, workPath string, sampleRate, channels int) (uint64, error)` — decodes
`-map 0:a:0 -f f32le -acodec pcm_f32le -` through a counting writer and returns `bytes / (4·channels)`;
the ffmpeg implementation uses the same `input()` (absolute path, `file:` prefix, protocol whitelist) and
timeout as `Scan`. `Loudness` and `Scan` are unchanged and work on MP3 input. `*ffmpeg.FFmpeg` is the only implementation;
no test fake implements `analysis.Analyzer`, so nothing else gains the method (B1 adds it to `FFmpeg` as a
`STUB(M2c-B2)` returning `errors.ErrUnsupported`).

**`internal/media/ingest`**:

```go
type Request struct { /* …unchanged… */ Reference bool } // reference FLAC: MM_REFERENCE=1 replaces MM_SEED
type LossyInfo struct{ SampleRate, Channels int; TotalSamples uint64; DurationS float64 }
type Result struct { /* …unchanged… */ Lossy *LossyInfo } // non-nil for IngestLossy results
func (p *Pipeline) IngestLossy(ctx context.Context, req Request) (Result, error)
```
`tagLines` with `Reference`: `TITLE, MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_REFERENCE=1,
REPLAYGAIN_*` (still eight, step 9's tag-equality check unchanged). `IngestLossy` steps:
1. work dir `incoming/ingest-<TrackID>` (as C15 step 1); `req.Artifacts` must be empty (`IO`).
2. copy to `master.mp3` hashing; mismatch with `SourceSHA256` → `SOURCE_CHANGED`.
3. `mp3info.Parse` → `INVALID_AUDIO` ("not an MPEG audio layer III stream") on `ErrNotMP3`.
4. `Analyzer.CountFrames` → `total`; `total == 0` → `INVALID_AUDIO`; `Loudness`; `Scan(sr, ch, total)`
   (`INVALID_AUDIO` on a decode error, `TOOL_FAILED` on tool errors, as C15).
5. peaks file; **no** tag write, **no** MP3 encode.
6. chmod 0444 `master.mp3` and `peaks.json`; claim `audio/masters/<slug>__0__<yyyymmdd-hhmm>[__N].mp3`
   (`masterName` gains an extension parameter; `Discard` already removes any file in `Created` under
   `audio/masters/`), peaks under `derived/<aa>/<sha>.peaks.json`.
7. re-open the claimed master and re-hash: `sha256 == SourceSHA256` else `FREEZE_INVARIANT`.
`Result`: `MasterSHA256 = FramesSHA256 = SourceSHA256`, `AudioMD5` 32 zeros, `MP3` zero-valued, `Info`
zero-valued, `Lossy` filled (`DurationS = total / sr`). A failure removes everything this call claimed.

**Work-dir links keep the master's extension:** `clip.Service.workMaster` (which also returns the master's
file name for its two callers, `Measure` and the render call, replacing the literal `abs+"/master.flac"`) and
`ears.Service.compute` link `master` + `path.Ext(MasterPath)` instead of the literal `master.flac` (ffmpeg probes content, the
extension keeps tool hints honest); clip *outputs* stay FLAC. `analysis.BlindSpotFacts` gains `Lossy bool`;
`BlindSpots` appends `"analysed from a lossy MP3 decode: bandwidth, clipping and true peak are not
comparable with FLAC takes"` (after the fixed entries and before the conditional ones; one source for Ears
payloads and `/context`).

**`internal/synth`**: `synth.MP3(ctx context.Context, t mediatool.Tools, dst string, tone Tone, o MP3Options) error`
(`Tone` already carries `Seconds`; `MP3Options{SampleRate int /* default 44100 */, Channels int /* default 2 */,
BitrateK int /* default 128 */, NoID3 bool}`; libmp3lame, `-write_xing 1`; the tone is rendered and
loudness-calibrated exactly as `synth.FLAC` does at the package's fixed 48 kHz and then resampled/encoded;
published atomically with the existing `publish` helper). Tests never use committed audio.

### C61. Importer (`internal/importer`)

**Classification (C16.1 amended).** `builder.classifyMP3` (`inventory.go`; the kinds `kPendingMP3`,
`kIgnMP3Sidecar`, `kIgnOrphanMP3` live in `classify.go`) keeps its decision for same-stem MP3s
(`ignored:mp3_sidecar`). Today only FLACs become `audioFile`s (`readAudio`); a new `readMP3` hashes a valid
standalone MP3, runs `mp3info.Parse` and produces an `audioFile` with `format = "mp3"` and id `mp3:<sha256>`. A *standalone* MP3 (no valid FLAC with that stem, or ambiguous) is first tested with
`mp3info.Parse`:

| Input | Result |
|---|---|
| same stem as exactly one valid FLAC identity | `ignored:mp3_sidecar` (unchanged) |
| stem matches FLACs of different audio | `ignored:orphan_mp3` + warning (unchanged) |
| standalone, `mp3info` fails (incl. M1's synthetic fake) | `ignored:orphan_mp3` + warning `orphan_mp3` (unchanged) |
| standalone, valid, mapping `-` | `ignored:orphan_mp3`, group status `skipped` |
| standalone, valid, neither flag nor mapping `reference` | stays `ignored:orphan_mp3` with warning `orphan_mp3` (message: "standalone MP3: import with --reference-lossy or a mapping row"), **and** gets an audio entry (`format: "mp3"`, status `unmatched`) and a mapping-template row |
| standalone, valid, `--reference-lossy` or mapping `reference` | kind `audio_mp3`, group status `reference` |
| standalone, valid, mapping `request_id` other than `reference`/`-` | inventory **error** `lossy_needs_reference` (D-M2c-4); the file is not imported |

Byte-identical copies of an MP3 form one group (audio id `mp3:<sha256>`, first path = representative), as for
FLACs. A group already in the library (`snap.AudioIDs["mp3:"+sha]`) is `already_imported`.

**Mapping (C16.6 amended).** In the `request_id` column the literal `reference` (case-sensitive) is the
target "import as reference track"; the `seed` cell must then be empty (else `bad_mapping_row`), `title`
and `parent` (path or `sha256:` form only; a request-id parent is warning `reference_parent_unresolved`)
are honoured. `-` still skips. A recipe whose request id is literally `reference` cannot be targeted by a
mapping row (documented limitation). The template (`mapping.todo.csv`) gains a row for every unmatched FLAC
and every valid standalone MP3 that is not yet resolved, `candidates` empty for MP3s.

**FLAC matching (C16.3 amended).** `evalMember`: a mapping row `reference` returns
`memberResult{ruleNo: 1, reference: true}` (warning `reference_overrides_match` if rules 2–8 would have
produced a unique recipe). After `matchGroups`, with `Options.ReferenceUnmatched`, every group still
`unmatched` becomes `reference` (`ambiguous` never does; operators resolve it by mapping). `audioGroup.rule()`
returns `reference` for reference groups; `g.recipe` is nil for them — **every use of `take.recipe` in
`placeTakes`, `placeScores`, `planLineage`, `predictFulfilment`, `assemble`, `apply.go` (`ingestOne`,
`newTake`, `commitTake`) and `siblings.go` is guarded by `t.reference`** (a `*refPlan{slug, title, requestID}` on `plannedTake`).
Reference takes get no score, no artifacts, no fulfilment; `recordedAt` is the file mtime as before.

**Titles.** `builder.takeTitle`'s precedence without the recipe cases: mapping `title` > FLAC `TITLE` tag >
`humanize(stem)` (a `refTitle` variant; same control-character and 200-rune cleaning); slug =
`Slug(title)`, fallback `Slug(stem)`, then `track`.

**Lineage (D-M2c-8).** `planLineage` excludes reference takes and reference/stub nodes (`existingNodes`
skips tracks with `Reference`, and `planMothers` ignores them) from seed-family inference and adds `planReferenceGroups`: for planned
lossy-reference takes without a mapping `parent`, key `song = m[1]` of `^(.+)_p([0-9]+)$` applied to
`Slug(title)` (stems without the suffix form no group); existing reference tracks with `lossy = 1` join
the key through their stored `slug`. Root = lowest `N` (ties: `recordedAt`, then sha); an existing root wins
over planned members. Children get `parent = root`, rule `reference_group` (`NewMutation{Operator:
"inferred_sibling", Inferred: true, Rule: "reference_group"}`). Mapping `parent` overrides (rule
`mapping`, `Inferred: false`) and `-` opts out.

**Plan entries and report (C16.7 amended).**
- Plan entry for a reference take: `{"k":"take","reference":true,"format":"flac|mp3","audio_id","path","sha256","rule":"reference","hash":<stub hash>,"title","parent","parent_rule"}`; the stub hash is
  computed in the dry-run with `library.ReferenceRequestID` and the canonical JSON, so the digest covers
  exactly what apply will write. `{"k":"recipe",…,"placement":"track"}` entries are not emitted for stubs.
- `AudioReport` gains `Format string` (`"flac"|"mp3"`); new statuses `reference` (will import as a
  reference track). `Summary` gains `audio_reference`, `tracks_reference`, `tracks_lossy`,
  `reference_groups`; `TracksNew` counts reference tracks too. `Report.Flags` per C57. The text output
  prints a "Without a recipe" block: every unmatched FLAC and standalone MP3 with its would-be status and
  the exact flag that would import it.

**Apply (C16.8 amended).** `Ingester` gains `IngestLossy`; `ingestOne` chooses by `t.g.format`
(FLAC reference: `Ingest` with `Request.Reference = true`, `RecipeID` = the stub id, `Seed` 0). The stub
recipe is inserted **in the take's transaction** by `InsertReferenceRecipe` before `InsertTake`
(inside the `s.tx` closure of `commitTake`; ingest runs before it, so a failed ingest leaves no stub; today
`s.recipeIDs[hash]` holds recipes inserted earlier, which does not apply to stubs). `newTake` sets `Reference`, `Lossy`, `ImportRule = RuleReference`; lossy takes get
`SampleRate/Channels/TotalSamples/DurationS` from `Result.Lossy`, `BitsPerSample = 16`, only the peaks
rendition. Ordering by generation, parent-failure fallback ("take becomes a root") and `DB_INSERT`
handling are unchanged. Re-running an import after success reports `already_imported` for every group.
No `Ears.Backfill` change (M2b's post-commit backfill runs for reference tracks like any other).

### C62. Refusals, exports, playlist ZIP

- **`internal/drafts`** (`drafts.Service`): a helper `rejectUnknown(ctx, requestIDs []string, parent *string)`
  runs one query `SELECT request_id FROM recipes WHERE engine = 'unknown' AND request_id IN (…)` and the
  `RecipeKnown` check for `parent`. `Create`/`Update` (when the request or parent changes) return
  `&Error{Status: 422, Code: "RECIPE_UNKNOWN", Msg, Details{"field": "id"|"parent_track_id"}}`;
  `Upload` records per-line `UploadIssue{Code: "recipe_unknown", Field: "id"}`; `Lint` adds the issue
  `{Code: "recipe_unknown", Field: "id", Severity: "error", Message: "this id belongs to a reference track whose recipe is unknown"}` to
  the item (no effect on other items). `knownHashes` needs no change (a stub's `{id,seed,title}` JSON can never equal a valid request), and
  `checkParent` is the hook for the parent refusal. `Lint` returns `yue2.LintReport`; the service adds the issue
  to the matching `LintResult.Errors` and sets `OK = false`.
- **Error mapping** (`internal/api/types_m1.go` `libErr`): `ErrRecipeUnknown` → 422 `RECIPE_UNKNOWN`
  `{"track_id"}`; `ErrLossyMaster` → 422 `LOSSY_MASTER` `{"track_id"}`. No operation calls them yet (M3 will).
- **`internal/export` / `internal/exports`**: `dbSource.SelectRecipes` adds `AND r.engine <> 'unknown'`;
  the `export.Source` interface gains `UnknownRecipeTracks(ctx, f TrackFilter) ([]string, error)` (implemented
  by `exports.NewDBSource` only); for `Selection.Kind == "tracks"` `Build` appends
  `Note{Code: "recipe_unknown_excluded", Message: "tracks without a known recipe cannot be exported as songs.jsonl", Details{"track_ids": [≤50 ids], "count": n}}`
  to `Result.Warnings` (surfaced as `JSONLInfo.Warnings`) when non-empty. A selection that is only reference
  tracks has no rows, so `Build` returns `ErrEmpty` as today; to keep the note it returns
  `&export.EmptyError{Notes []Note}` (`Is(ErrEmpty)`), and `exports.problemOf` maps it to the existing 422
  `EXPORT_EMPTY` with `Details{"notes": […]}`.
- **Backup** (`internal/backup/snapshot.go` `writeExport`): it builds `export/songs.jsonl` of every track with
  `export.Build`; reference tracks are excluded by the same filter, `ErrEmpty` (including `EmptyError`) is
  already treated as "no export", and the note is ignored there. No code change expected beyond the test.
- **Playlist ZIP** (`exports/zip.go`): `renditions()` becomes `SELECT track_id, path, bytes FROM renditions
  WHERE kind = 'mp3_320' AND track_id IN (…) UNION ALL SELECT id, master_path, master_bytes FROM tracks
  WHERE lossy = 1 AND id IN (…)`; the existing `skipID3` strips any ID3v2 of a lossy master before the
  generated tag is prepended. For reference tracks the TXXX frames are `MM_TAKE_ID`, `MM_REFERENCE=1`,
  `MM_STATUS` (no `MM_REQUEST_ID`, no `MM_SEED`); the entry builder reads `recipe_known` from the playlist's
  `library.TrackSummary`.

### C63. Operations, MCP and `/context`

No operation is added or removed; `api.Meta` of every operation is unchanged; REST ↔ MCP parity and the
generic MCP parity test must keep passing. Changed shapes (all additive except `flac`):

| Op id (path) | Meta (unchanged) | Change |
|---|---|---|
| `search_tracks` (GET /api/tracks) | read, free, `mcp: tool`, ui `/` | query `recipe_known` (bool), `lossy` (bool); items gain `recipe_known`, `lossy` |
| `get_track` (GET /api/tracks/{id}) | read, free, `mcp: tool`, ui `/tracks/:id` | `recipe_known`, `lossy`; `audio.mp3` null for lossy |
| `get_media_links` (GET /api/tracks/{id}/media) | read, free, `mcp: tool`, ui `/tracks/:id` | `flac` becomes `FlacLink` **nullable** (null when lossy); new `lossy bool`; `mp3` for lossy points at the master with computed `bitrate_kbps`; description updated |
| `get_track_context` (GET /api/tracks/{id}/context) | read, free, `mcp: tool`, ui `/tracks/:id` | see below |
| `create_draft`, `update_draft`, `upload_batch_jsonl`, `lint_recipe` | unchanged | new refusal `RECIPE_UNKNOWN` / issue `recipe_unknown` (C62) |
| `next_duel`, `get_duel`, `list_duels`, `submit_duel`, `compare_tracks`, `get_ladder` | unchanged | `duels.TrackBrief` (revealed slots only) and `ears.Side` gain `recipe_known`, `lossy`; `duels.LadderItem` is unchanged |
| `export_jsonl` | unchanged | note `recipe_unknown_excluded` (C62) |

The `FlacLink` field of `get_media_links` is made nullable with a `huma.SchemaProvider` shadow type
(`library.nullableObject` convention; `nullable:"true"` on an object `$ref` is refused by Huma v2.39.1).
**`tools/list` budget (D-M2b-22):** `TestToolsList_Ceiling` measures 25 tools / 55 308 compact bytes against
`toolsCeilingBytes = 55500`; two new `search_tracks` parameters and the larger output schemas of the four
changed first-class tools will exceed it. M2c-B1 measures, sets the constant to the measured value rounded up
(at most +1 500 bytes, i.e. ≤ 57 000) with a one-line justification next to it, and re-baselines
`toolsListBaselineBytes` and the golden (`internal/mcpgen/testdata/tools_list.golden.json`); if the growth is
larger it demotes `export_jsonl`, then `lint_recipe`, to `mcp: generic` (never `get_track_context`, which
agents read every session). `TestToolsListBudgetFires` stays green.

`/context` (`internal/trackctx`, bundle `mm.context/1`): `track` gains `recipe_known`, `lossy` (after
`duration_s` in the key order); `recipe` becomes `{"engine":"unknown","known":false,"hash":…,"request":null,"note":"recipe unknown: this is a reference track; no style, lyrics or seed exist"}`
for stubs (key order `engine, known, hash, request, note`; `known` is `true` for every other recipe, so every M2a/M2b golden changes by one key; the `order` map entry
for `recipe` becomes `{"engine","known","hash","request","note"}` and `track` gains `recipe_known`, `lossy`);
`provenance.import_rule` is `reference`; `score` is absent; `_links.stream` has `mp3` only when lossy
(no `flac` key; `trackctx` must not dereference the now-nullable `MediaSet.Flac`); `perception.blind_spots` carries `lossy_source` text (C60) for lossy tracks via Ears.
Section keys unknown to the budgeter (`known`, `note`) are not trimmed. The golden bundle fixtures gain
one reference and one lossy bundle; the worst-case budget fixture is unchanged. The `tools/list` golden
(C41) is regenerated (two optional parameters on `search_tracks`; nothing else). `openapi.json` and
`web/src/api/schema.d.ts` are regenerated (`make openapi`) and committed.

### C64. Web (`web/`)

- `lib/libraryQuery.ts`: filter fields `recipeKnown: boolean | null`, `lossy: boolean | null` (URL params
  `recipe_known`, `lossy`; parsed like `has_score`; included in `toQuery`).
- `lib/components/LibraryFilters.svelte`: two selects, **"Recipe: any / known / unknown"** and **"Audio:
  any / lossless / lossy"**; the "Clear" button resets them; the "active filters" indicator counts them.
- `lib/components/TrackTable.svelte`: a chip next to the title — `Recipe unknown` (neutral outline) for
  `recipe_known === false`, plus `Lossy MP3` (amber outline) for `lossy`; both with `title` text
  explaining ("no recipe exists for this track; it cannot be re-rendered or used as a mother" / "master is
  an MP3; there is no lossless file"). Chips are text, not colour-only.
- `routes/Track.svelte`: header chips as above; when `recipe_known === false` the Recipe and Lyrics panels
  are replaced by one **"Recipe unknown"** panel (the explanation, the stub title, and — for lossy — the
  bitrate and "no lossless master"); the Ears panel shows the `lossy_source` blind spot as it does any
  other; a hint line "Recover recipe — planned" is **not** shown (nothing is built).
- `lib/player.svelte.ts` `apply()`: `const format = links.flac ? this.formatFor(id) : "mp3"`; a track
  whose links have `flac === null` never enters `forceMp3`/`probePending` and never calls
  `recordFailedProbe`. `PlayerBar.svelte` shows a small `MP3` chip when the playing track's format is
  forced by `flac === null`.
- Draft editor: the parent-track picker excludes tracks with `recipe_known === false` (the server refuses
  them anyway); a `RECIPE_UNKNOWN` problem shows its message on the field.
- **Blind duels (M2b) show neither chip before the verdict**; `DuelReveal.svelte` and `Compare.svelte`
  show them. `lib/fixtures/**` gains reference and lossy track rows and a `flac: null` media-links
  fixture. Existing specs that count library rows are re-baselined (W1 lists them by grep).

### C65. Recover seam (documented only — M5 builds it)

M2c provides, and M5 relies on:
1. **Eligibility** is exactly `RecipeKnown(trackID) == false`; lossless and lossy references qualify.
2. **Operator and op names:** mutation operator `recover` (to be added to the Go enumeration of
   `mutations.operator`, today `inferred_sibling`), op id `recover_recipe` (`POST /api/tracks/{id}/recover`,
   `cost: estimate` because Whisper/LLM run remotely, MCP `tool`, UI `/tracks/:id`).
3. **Inputs it reads:** the master via `master<ext>` work-dir links (C60 — an MP3 master decodes like a
   FLAC), the stub's `request_json.title`, the track's `analyses`.
4. **Data it attaches** (all existing tables, Go-validated enumerations extended in M5): `analyses` rows
   (`analyzer = 'whisper_lyrics'`, `'style_proposal'`, tier `B`) on the **reference track**; `sections` rows
   with `source = 'measured'`; and a **new draft** with `origin = 'recovered'` whose recovered provenance is
   a new column `recipe_drafts.recovered_from_track_id` (M5 migration). It must not use `parent_track_id`:
   the M2c trigger refuses reference mothers for drafts by design.
5. **Lineage when rendered:** the rendered child is a **new root** (`generation 0`, known recipe) whose
   `mutations` row carries `parent_track_id = <reference track>`, `operator = 'recover'` (the
   `mutations.parent_track_id` column is independent of `tracks.parent_track_id`, so
   `tracks_reference_lineage` does not apply to it). M5 confirms this when it designs the op.
6. The reference track itself never changes (`tracks`/`recipes` frozen).

### C66. Test fixtures and harness

- `synth.MP3` (C60). New archive profile `synth.ProfileReference` (`Archive` switch case; = `e2e` archive plus: two unmatched FLACs
  `syn_ref_a.flac`, `syn_ref_b.flac` at `music/ref/`; six MP3s `music/lossy/syn_trio_p1.mp3`,
  `syn_trio_p2.mp3`, `syn_trio_p3.mp3`, `syn_lone.mp3`, `syn_pair_p2.mp3` + `syn_pair_p5.mp3` (so ordinal
  gaps work); one fake MP3 `music/lossy/syn_fake.mp3` (`ID3` junk); one sidecar `music/ref/syn_ref_a.mp3`
  rendered from the same tone). `seed-synthetic --references` (with `--profile dev|e2e`) generates the profile and applies both flags.
`archivetest.SyntheticArchive` accepts the new profile name `reference` (it imports with both flags set), so
acceptance tests can use a template; `ProfileReference` fills `Manifest.Expect` for the extra files and
`ProfileE2E`/`ProfileDev` are unchanged.
- `librarytest.Fixture` gains two rows through the real write path: a reference FLAC track and a lossy
  reference track (with `peaks` rendition only), used by API, trackctx, export and playlist tests.
- `servertest` needs no new options; parity cases for the changed ops are added to `parityCases`
  (`internal/mcpgen/parity_test.go`, in a new `parity_m2c_test.go` that appends to it like
  `parity_tokens_test.go`): `search_tracks` with `recipe_known=false`, `get_media_links` on the lossy track.
  `TestRegistry_NoNewOps_M2c` pins the operation count at **58** (the number of `operationId`s in
  `openapi.json` at the M2b merge).
- Unit tests of the importer use `fakeIngester` (extended with `IngestLossy`) and `env.put` for MP3 bytes
  (a valid header + junk, built by a test helper `mp3Bytes(n)` that emits two layer III frame headers);
  real-ffmpeg tests live in `internal/media/ingest` and the acceptance tests (X1).

### C67. CI and time budget (M2c-X1)

M2c adds real-ffmpeg tests (lossy ingest, acceptance) to the CI `go` job (`timeout-minutes: 10`, no `-short`,
uploads `covdata-unit`, consumed by `coverage` with `needs: [go, e2e]`). X1 measures the job on the merged
branch. **If it exceeds 6 minutes it is split** into two parallel jobs: `go-fast` (`-short -race`, the
unit tier) and `go-media` (`-run 'TestAcceptance|…'` plus every package that calls `testtools.Require`),
each uploading its own coverage artifact (`covdata-unit-fast`, `covdata-unit-media`); `coverage` downloads
both into `covdata/unit` (distinct file names; `go tool covdata` merges directories), `needs` lists both,
and `openapi drift` stays in `go-fast`. Branch protection names are listed in the commit message. If the job
is under 6 minutes nothing changes. Any new startup wait in tests tolerates `-cover` instrumented binaries
(generous deadline, readiness poll, never a fixed sleep).

---

## Acceptance mapping (decision → named tests)

| Decision | Tests (all named below in the tasks; * = TestAcceptance) |
|---|---|
| D-M2c-1 | `TestMigration0005_StubShape`, `TestInsertReferenceRecipe`, `TestInsertTake_Reference`, `TestReferenceRequestID`, `TestAcceptance_M2c_ReferenceFlac*` |
| D-M2c-2 | `TestReference_PlainImportIgnores`, `TestReference_FlagUnmatched`, `TestReference_AmbiguousNeverAuto`, `TestReference_MappingTarget`, `TestReference_SkipWins`, `TestCLI_ReferenceFlagsMismatch`, `TestDryRun_ListsRecipeLess` |
| D-M2c-3 | `TestIngestLossy_Freeze`, `TestIngestLossy_NoTagsNoRendition`, `TestMigration0005_LossyShape`, `TestMp3info_*`, `TestAcceptance_M2c_LossyPilot*` |
| D-M2c-4 | `TestLossy_MappingNeedsReference`, `TestMigration0005_ReferenceRule` |
| D-M2c-5 | `TestExport_ExcludesUnknownRecipe`, `TestExport_UnknownNote`, `TestZip_IncludesLossyAndReference` |
| D-M2c-6 | `TestDrafts_RejectUnknown*`, `TestLint_RecipeUnknown`, `TestMigration0005_DraftParentTrigger`, `TestRecipeKnown_Predicate`, `TestLosslessMaster_Predicate`, `TestLibErr_RecipeUnknown` |
| D-M2c-7 | `TestMedia_LossyLinks`, `TestResolve_LossyMaster`, `TestHandler_LossyRange`, `TestOpGetMediaLinks_Lossy` |
| D-M2c-8 | `TestReferenceGroups_*` |
| D-M2c-9 | `player.test.ts: lossy track never probes`, `device.test.ts: lossy playback leaves quality`, Playwright `m2c-reference.spec.ts` |
| D-M2c-10 | `TestAnalysis_LossyComparable`, `TestBlindSpots_LossySource`, `TestClip_LossyMaster`, `TestEars_LossyMaster` |
| D-M2c-11 | `TestMigration0005_LossyShape` (origin `job` rejected) |
| D-M2c-12 | `TestIngest_ReferenceTags`, `TestReference_StubIdNeverTagMatches` |
| D-M2c-13 | existing parity tests + `TestParity_M2c_Shapes`, `TestRegistry_NoNewOps_M2c` (op count unchanged), `tools/list` golden |
| D-M2c-14 | `TestRecoverSeam_Eligibility` (documented predicate), no feature test |

---

## Lanes and tasks

Stage 1 (sequential, branch `m2c` from `main` **with M2b merged**, `MM_LANE=m2c-found`): **M2c-B1**.
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from M2c-B1): **Media** (`m2c-media`:
**M2c-B2**), **Import** (`m2c-import`: **M2c-B3**), **Data** (`m2c-data`: **M2c-B4**), **Web**
(`m2c-web`: **M2c-W1**). The four lanes are independent: B3 tests the importer with the extended
`fakeIngester`; B4 tests against `librarytest` rows; W1 against generated types and fixtures.
Stage 3 (`m2c-int`): **M2c-X1** (integration, real-ffmpeg acceptance, Playwright, deploy dry-run).

**File ownership** (as M2a/M2b): in Stage 2 no lane edits `internal/store/**`, `internal/library/**`,
`internal/api/**`, `openapi.json`, `web/src/api/schema.d.ts`, `web/src/lib/fixtures/**` (W1 may add
fixtures), `internal/mcpgen/**`, `internal/httpserver/**`, `internal/app/**`; a needed contract change goes
back through this document and a Stage-1-owner fix commit every lane rebases onto. Shared tests are
extended by new functions in new files (`*_m2c_test.go`), never by editing the same function.

### Task M2c-B1: baseline check, schema, library, `mp3info`, interfaces, API shapes, fixtures
**Files:** `internal/store/migrations/0005_reference.sql`; `internal/store/{schema_m2c_test.go,
triggers_m2c_test.go,upgrade_m2c_test.go}` (M2a/M2b naming); the schema-version assertion
`internal/httpserver/server_test.go` (4 → 5); `internal/library/{reference.go,tracks.go,write.go,
snapshot.go,refs.go,service.go}` + `reference_test.go`, `m2c_test.go` (`write.go` also holds
`RelinkVariantiMothers`); `internal/media/mp3info/{mp3info.go,mp3info_test.go,fuzz_test.go}`;
interface freeze: `internal/analysis/analysis.go` (`CountFrames`) with a `STUB(M2c-B2)` returning
`errors.ErrUnsupported` in `internal/analysis/ffmpeg/ffmpeg.go` (the only implementation; no fakes);
`internal/media/ingest/{types.go,ingest.go,claim.go}` (`Request.Reference`, `Result.Lossy`, `IngestLossy`
as a `STUB(M2c-B2)` returning `errors.ErrUnsupported`, `masterName` extension parameter);
`internal/importer/importer.go` (`Ingester.IngestLossy`) and the `fakeIngester` method in a new
`helpers_m2c_test.go`; `internal/duels/{types.go,view.go}` and `internal/ears/types.go`
(`recipe_known`/`lossy` on `TrackBrief` and `Side`, filled from `TrackRef`);
`internal/api/{types_m1.go (libErr, MediaLinks + nullable FlacLink shadow type, track shapes),ops_library.go,
ops_media.go}`, `internal/trackctx/trackctx.go` (the `/context` shape of C63; the full golden update);
`internal/mcpgen/{m2a_contract_test.go (toolsCeilingBytes),parity_test.go (toolsListBaselineBytes),
parity_m2c_test.go,testdata/tools_list.golden.json}`; `internal/library/librarytest/fixture.go` (two rows);
`openapi.json`, `web/src/api/schema.d.ts`; records in the M1/M2a/M2b plan documents named in the header;
`docs/architecture.md` public paragraph.
**Tests first (expected values):**
- `TestMigration0005_Applies` (`user_version == 5 == len(migrations)`; `tracks.lossy` exists, `NOT NULL
  DEFAULT 0`; the nine triggers and `tracks_lossy` index exist). `TestMigration0005_UpgradeFromM2b` (package
  `store_test`: a real v4 DB via `store.OpenRaw` + `store.MigrateTo(ctx, raw, 4)` + `librarytest.Fixture`
  migrates; every track `lossy = 0`; M1/M2a/M2b rows and a token still verify).
- `TestMigration0005_StubShape` (table): stub insert with the C58 shape succeeds; each of — `style='x'`,
  `lyrics='x'`, `cot='full'`, `seed=5` (with `$.seed` 5), `sampling_json='{}'`, `created_by='cli'`,
  `core_hash<>hash`, JSON without `title`, JSON with an extra key — fails with `recipes: unknown-engine
  shape`; an `UPDATE recipes SET engine='yue2'` fails `recipes: immutable`.
- `TestMigration0005_LossyShape`: base lossy insert (master `….mp3`, `bits 16`, md5 zeros, three equal
  shas, `origin 'import'`, stub recipe, rule `reference`) succeeds; failing, each with `tracks: lossy shape`:
  `lossy=1` with a `.flac` path; `lossy=0` with an `.mp3` path; `origin='job'`; `bits_per_sample=24`;
  `channels=3`; `source_sha256<>master_sha256`; `frames_sha256<>master_sha256`; `audio_md5` non-zero.
  `UPDATE tracks SET lossy=0` → `tracks: frozen column`.
- `TestMigration0005_ReferenceRule`: `import_rule='reference'` with a `yue2` recipe, a non-reference rule
  with a stub, and `lossy=1` with rule `mapping` each fail `tracks: reference rule`.
  `TestMigration0005_ReferenceLineage`: child reference of a known-recipe parent and known child of a
  reference parent fail `tracks: reference lineage`; reference child of reference parent succeeds.
  `TestMigration0005_RenditionAndDraftTriggers`: `mp3_320` rendition for a lossy track (insert and update)
  → `renditions: a lossy track has no mp3 rendition`; a `peaks` rendition succeeds; draft insert/update with
  `parent_track_id` of a reference track → `recipe_drafts: recipe unknown`; a known parent succeeds.
- `TestReferenceRequestID`: `ReferenceRequestID("syn_ref_a", "abc")` equals `"ref_syn_ref_a_"+hex8(sha256("abc"))`
  (`ba7816bf`); stable across calls; different audio keys differ.
- `TestInsertReferenceRecipe`: stored `request_json` is exactly `{"id":"ref_…","seed":0,"title":"Syn ref a"}`
  (sorted, compact), `engine = 'unknown'`, `core_hash = hash`; second call returns `created=false` and the
  same id; a 300-rune title is stored cut to 200.
- `TestInsertTake_Reference`: a reference take (FLAC-shaped) inserts; `tracks_fts` has a row whose `style`
  and `lyrics` are empty and whose `title` matches a search; zero `sections`, `scores`, `artifacts`, no
  draft fulfilled even when a draft has the same `request_id`; `Reference` with `ScoreABC` or artifacts →
  `ValidationError`; `Lossy` with an `mp3_320` rendition → `ValidationError{Field:"rendition"}`; lossy
  insert stores `lossy = 1`.
- `TestRecipeKnown_Predicate` (yue2 track `true`; reference FLAC `false`; lossy reference `false`; unknown
  id `ErrNotFound`), `TestRequireRecipeKnown` (`errors.Is(err, ErrRecipeUnknown)`, `TrackID` set),
  `TestLosslessMaster_Predicate` (yue2 `true`; lossy `false`), `TestRequireLosslessMaster`.
- `TestSearch_RecipeKnownAndLossyFilters` (fixture: 1 known + 1 reference FLAC + 1 lossy reference:
  `recipe_known=false` → 2 ids, `lossy=true` → 1, `lossy=false&recipe_known=true` → the known one, `q` search
  by the reference title finds the reference), `TestTrackDetail_Reference` (`recipe.engine "unknown"`,
  `recipe_known false`, `audio.mp3` null for the lossy one), `TestTrackRefs_Flags`.
- `TestMedia_LossyLinks` (`MediaSet.Flac == nil`, `MP3.SHA256 == master_sha256`, `BitrateKbps ==
  round(bytes·8/duration/1000)`), `TestResolve_LossyMaster` (`mp3` + master sha → master path, `audio/mpeg`;
  `flac` + master sha → `serve.ErrNotFound`; `flac` of a normal track unchanged), `TestHandler_LossyRange`
  (Range matrix on the lossy master: `bytes=0-9` → 206 `Content-Range: bytes 0-9/<size>`, suffix range,
  unsatisfiable → 416, `Content-Type: audio/mpeg`; a `flac` link with that sha → 404).
- `TestOpGetMediaLinks_Lossy` (body `flac: null`, `lossy: true`, `mp3.url` ends `.mp3`),
  `TestOpSearchTracks_Filters`, `TestLibErr_RecipeUnknown` (422 `RECIPE_UNKNOWN` `{track_id}`) and
  `…_LossyMaster` (422 `LOSSY_MASTER`), `TestRegistry_NoNewOps_M2c` (operation count equals M2b's: 58),
  `TestContext_Reference` and `TestContext_Lossy` (key order per C63; `recipe.request` null; no `score`;
  `_links.stream` has no `flac`), M2a/M2b `/context` goldens and budgets updated.
- `TestMp3info_*` (table, all synthetic bytes built in the test): MPEG-1 L3 44.1 kHz stereo 128 kbps →
  `{"1", 44100, 2, 128}`; MPEG-2 L3 22.05 kHz mono; MPEG-2.5 L3 8 kHz; an ID3v2 tag (size 2048, footer
  flag set → 10 extra) before the frames is skipped; one junk byte run of 100 bytes before the first frame
  is resynced; layer II, layer I, `0xFFFFFF`, bitrate index 15, sample-rate index 3, a single header without a
  second, an empty file and `ID3` + zero-size tag + `"synthetic"` → `ErrNotMP3`; reads at most 128 KiB
  (counting `ReaderAt`). `FuzzMp3info` (no panic, bounded reads).
`TestToolsList_Ceiling` (re-measured; constant and baseline updated per C63; the measured size is in the
commit message), `TestToolsList_SlimOutputSchemas`, `TestToolsListBudgetFires` green;
`TestToolResultsConformToOutputSchema` still validates every MCP result (the nullable `flac`).
**Done:** `make check` green; `make openapi` clean; `TestNoReplaceSQL`/`TestNoDeleteSQL` green; the
baseline-check result is in the commit message; the only `STUB(M2c-` markers are the two B2 stubs.

### Task M2c-B2 (Media): decode analysis, `IngestLossy`, reference tags, work-dir links, Ears blind spot, synth
**Files:** `internal/analysis/ffmpeg/ffmpeg.go` (`CountFrames`; replaces the B1 stub), `internal/analysis/blindspots.go`
(+ `BlindSpotFacts.Lossy`), `internal/media/ingest/{ingest.go,lossy.go,tags.go,claim.go}` + tests (the
`p.to` timeout set gains the decode-count timeout; `TestNoStubMarker` keeps guarding `STUB(M1-`),
`internal/media/clip/service.go` (all three `master.flac` uses), `internal/ears/{analyze.go,service.go,compare.go}`
(extension-preserving link; `Lossy` in the two `BlindSpotFacts` builders: `service.go` for one track, `compare.go`
when either side is lossy),
`internal/synth/{mp3.go,mp3_test.go}`, the `TestArgv_OnlyWorkPaths_*` family gets one new function
`TestArgv_OnlyWorkPaths_CountFrames` in a new file.
**Tests first (expected values; synthetic fixture = `synth.MP3` of the default `Tone` (220/331 Hz partials) 2.000 s,
stereo, 44.1 kHz, 128 kbps, with ID3 and a second copy with `NoID3`; control = the same `Tone` rendered by
`synth.FLAC`):**
- `TestCountFrames`: 2.000 s fixture → `88200` frames (measured with ffmpeg n9.0.1: 705 600 bytes of f32le
  stereo); mono fixture of 1.000 s at 22.05 kHz → `22050`; garbage file → error, no panic;
  `TestArgv_OnlyWorkPaths_CountFrames` (argv contains only the absolute work path with `file:`).
- `TestIngestLossy_Freeze`: result `MasterSHA256 == SourceSHA256 == sha256(inbox bytes)`; the claimed file is
  byte-identical to the source, mode `0444`, path `audio/masters/<slug>__0__<date>.mp3`; source file
  untouched (mtime and bytes); `Lossy == &{44100, 2, 88200, 2.0}`; `AudioMD5` 32 zeros;
  `FramesSHA256 == MasterSHA256`. `TestIngestLossy_NoTagsNoRendition`: no `hq.mp3`/`MP3` rendition, `Peaks`
  present with overview length `44` (`(88200+2047)/2048`), `ReplayGainDB` finite, no `metaflac` call (a spy
  `Tools.Metaflac = "/nonexistent"` still succeeds).
- `TestIngestLossy_Rejections`: a FLAC file → `INVALID_AUDIO`; an AAC stream in a `.mp3` name → `INVALID_AUDIO`;
  a truncated MP3 (first 4000 bytes of the fixture) → either `INVALID_AUDIO` or success with a smaller
  `TotalSamples` (documented: the decode count is the truth), never a panic; changed source →
  `SOURCE_CHANGED`; artifacts supplied → `IO`; failure leaves no file under `audio/masters/`, `derived/`
  or `takes/` (`Created` empty); ToolTimeouts like `TestIngest_ToolTimeouts`.
- `TestAnalysis_LossyComparable`: loudness of the MP3 fixture within ±0.5 LU of the FLAC control (same
  `Tone`); `bandwidth_hz` non-nil; `integrated_lufs` of the sum matches `Loudness` called directly.
- `TestIngest_ReferenceTags`: a reference FLAC freeze writes exactly
  `TITLE, MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_REFERENCE=1, REPLAYGAIN_TRACK_GAIN,
  REPLAYGAIN_TRACK_PEAK, REPLAYGAIN_REFERENCE_LOUDNESS` (no `MM_SEED`); all M1 `TestFreeze_*` stay green
  for the non-reference path.
- `TestBlindSpots_LossySource` (the new entry sits after the six fixed entries and before the conditional
  ones; absent when `Lossy` is false; existing blind-spot goldens unchanged), `TestClip_LossyMaster` (a clip
  rendered from the MP3 fixture is a FLAC, 24-bit, correct duration ±1 sample; the work dir held
  `master.mp3`), `TestEars_LossyMaster` (Tier A runs; the payload's blind spots include `lossy_source`).
- `TestSynthMP3` (valid per `mp3info.Parse`; `NoID3` starts with the sync byte `0xFF`; deterministic
  structure, not bytes).
**Done:** `make check` green; ingest coverage stays ≥ 85 % (≥ 95 % for the freeze helpers); no `STUB(M2c-B2)`
marker remains.

### Task M2c-B3 (Import): classification, mapping, opt-ins, reference takes, grouping, report, CLI, apply
**Files:** `internal/importer/{classify.go (kinds),inventory.go (`classifyMP3`, `readMP3`),match.go,mapping.go,
place.go (`takeTitle`, `planMothers`),siblings.go,report.go,apply.go,importer.go}` + new `reference.go`, tests
`reference_test.go`, `lossy_test.go`, `groups_test.go`, `cmd/metamusic/cli_m2c_test.go`, and the helpers
`mp3Bytes` and `fakeIngester.IngestLossy` in a new `helpers_m2c_test.go` (the existing `helpers_test.go` is
not edited); `cmd/metamusic/{cmd_import.go,cmd_seed.go,main.go}`; `internal/synth/archive.go`
(`ProfileReference`); `internal/archivetest/archivetest.go` (profile `reference`, both flags).
**Tests first (expected values; fixtures built in `t.TempDir()` with `env.put`/`env.flac`):**
- `TestReference_PlainImportIgnores`: inbox with 2 unmatched FLACs and 2 standalone MP3 (valid headers),
  default options → `TracksNew == 0`, 0 rows added to `tracks`/`recipes`, FLACs `unmatched` with candidates as
  before, MP3s stay `ignored:orphan_mp3`, mapping template lists all four, exit state unchanged (**D-M1-2
  regression**: the whole M1 suite is unchanged and green).
- `TestDryRun_ListsRecipeLess`: report has audio entries `format: flac|mp3`, statuses `unmatched`; text output
  contains the block "Without a recipe" and names `--reference-unmatched` for the FLACs and `--reference-lossy`
  for the MP3s; `Summary.AudioUnmatched == 2` (FLAC only), digest identical to a run without the block data
  (the listing does not change the plan).
- `TestReference_FlagUnmatched`: `ReferenceUnmatched` → both FLACs status `reference`, rule `reference`,
  `Summary.TracksNew == 2`, `TracksReference == 2`; a third FLAC that matches a recipe by rule is still a
  normal take; an `ambiguous` FLAC stays `ambiguous` (**`TestReference_AmbiguousNeverAuto`**); MP3s unaffected
  by this flag.
- `TestReference_MappingTarget`: mapping `music/x.flac,reference,,My Title,` → reference, title `My Title`,
  works without the flag; with a seed in the row → `bad_mapping_row`; a mapping row `reference` on a FLAC that
  rule 4 would match → reference + warning `reference_overrides_match`; `TestReference_SkipWins` (`-` with the
  flag → `skipped`).
- `TestLossy_FlagAndGroups` (**`TestReferenceGroups_*`**): files `trio_p1.mp3`, `trio_p2.mp3`, `trio_p3.mp3`,
  `lone.mp3`, `pair_p2.mp3`, `pair_p5.mp3` with `ReferenceLossy` → 6 `reference` takes, `reference_groups == 2`
  (`trio`, `pair`); in the plan `trio_p1` is the root (generation 0), `trio_p2`/`trio_p3` parents = `trio_p1`
  rule `reference_group` inferred; `pair_p2` root, `pair_p5` its child; `lone` a root; `SiblingsInferred == 3`.
  `TestReferenceGroups_ExistingRoot`: after apply, adding `trio_p4.mp3` and re-running attaches `p4` to the
  stored `trio_p1` track (`parent` existing, generation 1, no change to existing rows); adding `trio_p0.mp3`
  makes `p0` a child of the *existing* root with warning `older_than_root` (existing lineage never rewritten).
  `TestReferenceGroups_MappingParent` (a mapping `parent` path overrides, rule `mapping`, not inferred; `-`
  opts out), `TestReferenceGroups_NoSeedFamily` (stub ids never form seed families with real recipes).
- `TestLossy_ClassifyMP3`: table of the C61 classification rows (sidecar of one FLAC, mismatched stems, fake
  `ID3` junk, valid standalone with/without flag, mapping `-`); `TestLossy_MappingNeedsReference`
  (`request_id=some-id` for an MP3 → error `lossy_needs_reference`, nothing imported);
  `TestLossy_DuplicateCopies` (two byte-identical MP3 → one group, `Duplicates` lists both).
- `TestReference_StubIdNeverTagMatches`: a FLAC whose `MM_REQUEST_ID` equals a stub's request id is
  unmatched (not resolved to the stub).
- `TestApply_ReferenceTakes` (fake ingester): FLAC reference → ingest request has `Reference true`, `Seed 0`,
  `RecipeID` = the stub's id; MP3 → `IngestLossy` called with no artifacts; the stub is inserted in the take's
  transaction (a failing ingest leaves **no** recipe row: `count("recipes")` unchanged); `tracks.lossy`,
  `import_rule`, `bits_per_sample 16` as C61; re-running `Apply` → `already_imported`, 0 new rows.
- `TestCLI_ReferenceFlags`: both flags parse on `--dry-run/--plan/--yes`; `--plan` with different flags than
  the report → exit 2 and the message of C57; the report JSON has `flags`; usage text lists them.
- `TestPlanDigest_ReferenceCoverage`: changing one reference file's bytes, its title (mapping) or a flag changes
  the digest; `PLAN_CHANGED` on apply. `TestPrivacy_M2c` (the privacy test family of M1: no absolute path or
  title leaks into report text; escapes unchanged).
- Regression: `TestOrphans_*`, `TestScan_*`, M1/M2a/M2b acceptance tests unchanged and green.
No test or handler mutates a process global (the CLI flags go through `importer.Options`).
**Done:** `make check` green; importer coverage ≥ 85 %.

### Task M2c-B4 (Data): refusals, exports, ZIP, `/context` wiring
**Files:** `internal/drafts/{drafts.go (`checkParent`, `Create`, `Update`, `Upload`, `Lint`),reject_m2c.go}` + tests; `internal/export/{source.go,build.go}` (`EmptyError`),
`internal/exports/{source.go,zip.go,service.go}` (`problemOf`) + tests; `internal/api/ops_drafts_m2a.go` (message only, no
signature change); `internal/trackctx` (the `lossy_source` perception wiring through Ears payloads, if not
done in B1); `internal/backup` test only (`writeExport` is expected to need no change).
**Tests first:**
- `TestDrafts_RejectUnknownID` (`create_draft` with a request whose `id` equals a stored stub's request id →
  422 `RECIPE_UNKNOWN`, `details.field == "id"`, no row created), `TestDrafts_RejectUnknownParent`
  (`parent_track_id` = reference track on create and on update → 422 `RECIPE_UNKNOWN`, `details.field ==
  "parent_track_id"`; a known track parent still works), `TestDrafts_UploadRejectsUnknown` (a 3-line upload
  where line 2 uses a stub id → issue `{line 2, code "recipe_unknown", field "id"}`, all-or-nothing creates 0,
  with `allow_partial` creates 2), `TestLint_RecipeUnknown` (item 2 of 3 gets error issue `recipe_unknown`;
  items 1 and 3 clean; `batch` duplicate logic unaffected), `TestKnownHashes_IgnoreStubs`.
- `TestExport_EmptyErrorKeepsNote` (`errors.Is(err, export.ErrEmpty)`, `problemOf` answers 422 `EXPORT_EMPTY` with `details.notes`).
- `TestExport_ExcludesUnknownRecipe` (selection of 3 tracks, one reference: output has exactly the other two
  lines, byte-identical to the M2a rule for imported rows; the reference's id absent),
  `TestExport_UnknownNote` (note `recipe_unknown_excluded`, `details.count == 1`, `details.track_ids` = that
  id; no note when none), `TestExport_OnlyReferenceSelection` (422 `EXPORT_EMPTY` with the note in `details`, no error from the stored
  stub request).
- `TestZip_IncludesLossyAndReference` (playlist of [known, reference FLAC, lossy] → 3 entries, `MissingMP3`
  empty; the lossy entry's bytes after the generated ID3v2 equal the master's bytes after its own ID3v2;
  reference entries carry `MM_REFERENCE=1` and no `MM_SEED`/`MM_REQUEST_ID` TXXX).
- `TestBackup_ReferenceAndLossy` (backup + restore into an empty dir: both reference tracks verify, master
  sha256 and mode 0444 intact, `lossy` preserved).
- `TestTrackctx_PerceptionLossy` (blind spots include the lossy text once, in position).
**Done:** `make check` green.

### Task M2c-W1 (Web): filters, chips, track page, player fallback, fixtures
**Files:** `web/src/lib/{libraryQuery.ts,player.svelte.ts}`, `web/src/lib/components/{LibraryFilters,
TrackTable,PlayerBar,DraftEditor,DuelReveal}.svelte`, `web/src/routes/{Track,Compare}.svelte`,
`web/src/lib/fixtures/m2c/{index.ts,index.test.ts,*.json}` (M2b's fixture layout), unit tests next to each,
`web/e2e/m2c-reference.spec.ts`, `web/playwright.config.ts` (an `ignoreM2c(keep)` helper in the style of
`ignoreM2a`/`ignoreM2b`: the spec runs on `chrome` and `chrome-pixel7` only, so it joins `firefoxIgnore` and the
iPhone ignore list; the skip-accounting comment is updated), `web/e2e/global-setup.ts` (`seed-synthetic
--profile e2e --references`), and the re-baselined specs: `m1-library.spec.ts` (`TITLES`, the literal 9s),
`m1-pwa.spec.ts` (`toHaveCount(9)`), `m1-import.spec.ts` (`audio_files: 9`, 9 audio lines) and whatever `rg
'toHaveCount\(\d+\)|\d+ tracks'` finds in the M2a/M2b specs. Re-baselining derives the numbers from one
exported constant (`SEED_TRACKS`, `SEED_AUDIO_FILES` in `web/e2e/env.ts`, filled from the profile manifest
facts) or filters by `recipe_known=true`, never a new bare literal; `ownTracks` is untouched because the
reference titles do not start with `Syn song`.
**Tests first:**
- `libraryQuery.test.ts: recipe_known and lossy round-trip` (`?recipe_known=false&lossy=true` parses to
  `{recipeKnown:false, lossy:true}` and serialises back; absent → `null`; invalid value → `null`);
  `LibraryFilters` renders the two selects and "Clear" resets them (component test).
- `TrackTable` chip tests: `recipe_known false` → text "Recipe unknown"; `lossy true` → "Lossy MP3"; known +
  lossless → no chip; chips have `title` text.
- `player.test.ts: lossy track plays mp3 and never probes`: links with `flac: null` → `el.src` is the mp3
  URL, `state.format === "mp3"`, an `error` event code 4 does **not** add to `forceMp3`/`probePending`;
  `device.test.ts: lossy playback leaves quality`: `recordFailedProbe` not called, `quality` stays
  `lossless`; normal track behaviour tests unchanged.
- `Track` page test: reference fixture shows the "Recipe unknown" panel and no Lyrics/Recipe panels; lossy
  fixture shows "Lossy MP3" and the bitrate; known fixture unchanged.
- Duel tests: a blind duel fixture whose tracks are lossy shows neither chip before the verdict
  (`DuelCard`), both after reveal (`DuelReveal`).
- Playwright `m2c-reference.spec.ts` (seeded `--references` server): library filter "Recipe: unknown" lists
  exactly the 2 reference FLACs + the lossy ones (counts from the profile manifest); open a lossy track:
  badges visible, play, `currentTime` advances, seek via the waveform works; open a reference FLAC: badge,
  rate it 4 stars, add a comment; the draft editor's parent picker does not offer a reference track; the
  `export` page shows the note for a selection containing one; the lossy trio appears as siblings under one
  root in the lineage block. No console errors; served-operation coverage unchanged (no new ops).
**Done:** `make check` green (bun lint/check/unit); `make e2e` (X1).

### Task M2c-X1 (Integration, Stage 3)
**Files:** `internal/acceptance/m2c_test.go`, `scripts/e2e.sh` (no change expected), `.github/workflows/ci.yml` and
`scripts/coverage-gate.sh` only if the C67 split applies, docs
(`docs/runbook.md` — importing recipe-less audio, flags, what the badges mean), `docs/architecture.md`
(public paragraph), `docs/device-checklist.md` (play a lossy track on a phone), `docs/plans/<date>-m2c-
reference-lossy.md` (public copy via the sed split, gate-checked), private design-document update of the
decisions.
**Tests first (real ffmpeg/flac/metaflac, synthetic archive `ProfileReference`):**
- `TestAcceptance_M2c_ReferenceFlac`: `import --dry-run --reference-unmatched` then `--plan` →
  2 reference FLAC tracks. Each: frozen master 0444 under `audio/masters/…flac`, tags
  (`MM_REFERENCE=1`, no `MM_SEED`), `mp3_320` rendition + peaks present, loudness/spectrum analyses present,
  `recipe_known false`, engine `unknown`, FTS finds the title; Range matrix on both `.flac` and `.mp3`
  links passes; rating, comment, tag, playlist add, duel creation between the two (blind, loudness-matched
  clips) and a verdict all succeed; `songs.jsonl` export of both returns no lines and the note; backup →
  restore verifies them.
- `TestAcceptance_M2c_LossyPilot`: `--reference-lossy` → `syn_trio_p1..p3`, `syn_lone`, `syn_pair_p2/p5`
  imported as 6 lossy reference tracks (not the fake MP3, not the sidecar): each master byte-identical to the
  inbox file, 0444, **no** `mp3_320` rendition, peaks present, analyses present, `get_media_links` →
  `flac: null`, `lossy true`; trio forms one lineage (root p1, 2 children, `inferred_sibling`/`reference_group`);
  `search_tracks?lossy=true` → 6 ids; `/context` shows `lossy`, `recipe.known false`, the blind-spot text; a
  duel between two lossy siblings and a ladder read work; a lossy track and a FLAC reference in one playlist
  ZIP export; no FLAC link is ever minted for a lossy track (`get_media_links` and `/context`); creating a
  draft with a lossy track as parent → 422 `RECIPE_UNKNOWN`.
- `TestAcceptance_M2c_PlainImportUnchanged`: the M1/M2a/M2b acceptance archives imported **without** flags
  produce the same track and recipe counts as before M2c (golden numbers from those tests).
- `TestAcceptance_M2c_RegistryDerived`: every changed op reachable over REST, MCP and stdio with equal JSON;
  `TestNoSecretLeak` extended over the new fields; `TestGETNeverWritesDB_M2c` (all changed GETs).
- `make check`, `make test`, `make e2e` (4 browser projects + the `listen-*` phase), `make gate`, coverage gates
  (`coverage.thresholds`: ≥ 85 % `internal/...`, ≥ 95 % `internal/media/serve`), `grep -rn 'STUB(M2c-' internal
  cmd` empty. CI `go` job duration measured; the C67 split applied if it exceeds 6 minutes.
**X1 deploy step (operator-assisted; host details only in the private notes):** run the dry-run against the
real inbox read-only **with a temporary `MM_DATA_DIR`** (the dry-run writes its report and `mapping.todo.csv`
into the data dir; it reports counts only, no titles in the repository), review the "Without a recipe"
block, then apply with the flags the operator chooses. A non-dry-run import is **never** run against the
production data directory by an agent; counts are recorded privately.

---

## Definition of done (M2c)

- All named tests green; `make check` < 30 s warm; `make test` green with media tools; `make e2e` green on
  all four projects; `make gate` green; coverage gates met.
- A plain `import` of any archive imports exactly what it did before M2c (D-M1-2 regression test).
- Reference FLACs and lossy MP3s import only with an explicit flag or mapping row, are playable with Range,
  have peaks, loudness and bandwidth, are rateable, commentable, duel-able, in playlists and backups, and are
  badged "recipe unknown" / "lossy reference" in UI, API, MCP and `/context`; the library filters on both.
- Stub recipes are never exported as `songs.jsonl`, are refused by lint, draft creation and as mothers (Go
  and database), and `RecipeKnown` / `LosslessMaster` exist and are tested for M3.
- A lossy master is byte-identical to its inbox file, mode 0444, has no tags and no MP3 rendition, and no
  `.flac` link is ever minted for it; generated tracks (`origin = 'job'`) cannot be lossy.
- No new operation (count stays 58); `openapi.json`, `schema.d.ts`, the `tools/list` golden and the
  re-measured `toolsCeilingBytes` committed.
- No real titles, hostnames, lyrics, audio or personal data in any tracked file; the public plan copy
  contains nothing below the private-appendix marker.

## Risks / open questions

- **Shared e2e library.** `ProfileReference` adds 8 tracks to the library every Playwright spec shares; the
  re-baselining in W1 (constants, not literals) is the mitigation, and the M2c spec only asserts on rows it
  can identify by title and `recipe_known`.
- **`tools/list` headroom.** 192 bytes under the ceiling today; B1 measures and re-baselines (C63).
- **MP3 duration/sample count** is the decoder's count (gapless trimming via the Xing/LAME header when
  present, otherwise padded by the decoder). Two decoders can differ by ≤ 1 frame; the track stores ours and
  everything downstream (peaks, clips, comment timestamps) uses it consistently. Real files from other
  encoders may be CBR without Xing; `TestIngestLossy_Rejections` covers the truncated case, the real-archive
  dry-run in X1 reports any file whose frame count looks odd.
- **Blind-duel leak:** a lossy take next to a FLAC take differs in bandwidth, which a keen ear (or an agent)
  can hear or measure. Accepted (D-M2c-10); the ladder is unaffected by the flag; M6's Critic Bench should
  treat lossy-vs-lossless pairs as sighted.
- **`seed = 0` placeholder.** Any new code that reads `recipes.seed` must treat `engine = 'unknown'` first
  (library read models already join `r.seed`; for stubs they show 0). A future "seed family" or "same seed"
  feature must filter on `recipe_known`; B1 adds a note to the library package doc.
- **Mapping literal `reference`** collides with a real request id of that exact name (documented, accepted).
- **Idempotent identity of lossy audio** is the file hash; re-encoding the same song produces a different
  file and therefore a new track. That is the intended behaviour for masters that are never rewritten.
- **iOS** playback of MP3 masters through the persistent `<audio>` element is the same path as the existing
  MP3 rendition; the device checklist gets one line.
- **Operator workflow cost of two flags**: documented in the runbook; the dry-run text says exactly which
  flag imports which list.

