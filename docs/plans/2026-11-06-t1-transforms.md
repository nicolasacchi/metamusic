# T1 — Transforms — Implementation Plan (T1a and T1h built; revision 4 as shipped, see the Revision log at the end; T1b deferred)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, then the shipped plan copies in `docs/plans/` (M0–M3, L1, M4a, L2, L3; vocabulary
> **Muse · Song · Take · Pick · Session · Sound · Record · Crate**, do not rename a noun) and — **by name and purpose, never by
> contract number** — the E2 plan that is being re-baselined in parallel (list in "Dependencies on E2" below). T1 builds on
> the contracts as merged on `main` at `5f6eeaf` (2026-11-02, all of M0–M3, L1, M4a, L2, L3 and the reliability work deployed).

> **Revision 4 (2026-11-03) — changelog.** Operator decision of 2026-11-03 (P.5-G2): the hi-fi/3D lanes are built **in this round**. Everything not listed here is unchanged from revision 3.
> 1. **C218 rewritten** from an optional sketch (O1–O3) into the overview of **T1h**; **C219–C225 are new**: C219 `master`, C220 `widen` (correlation bounds), C221 `binaural`, C222 HRTF dataset/licence finding and table build, C223 estimate/cost/API/warnings, C224 goldens/acceptance/listening gate X3h, C225 what F1 freezes and the registration seam. C226–C249 stay free.
> 2. **Three new kinds, nine in all**: ordinary Takes through the same seven operations (no new op, table, config variable or tool bytes). Deterministic DSP only, no ML; integer/fixed-point rules for parameters, delays and the Q15 HRIR table (C218.3).
> 3. **Lanes H1 (master + widen) and H2 (binaural; H2b gated by the HRTF/licence box P.5-G4)** added to the lane table; **F1 gains a freeze list** (C225: nine-kind CHECK, nine operators, API types once, `RegisterKind` and `ops.Register` seams, `FinishIn.CeilingDBTP`, gate patterns). B5 +0.3k, W3 +0.2k, X3h listening gate.
> 4. **HRTF licence finding (C222), read 2026-11-03:** SADIE II is Apache-2.0 and HUTUBS is CC BY 4.0 (both fit a public MIT repo with a notice); MIT KEMAR has only a "free, cite the authors" permission (no redistribution grant) and its SOFA copies show no licence; others unverified. Default: SADIE II dummy head; fallback: a parametric ITD/ILD + head-shadow model, built in H2 either way.
> 5. **Edits outside C218–C225 (listed in C225.8):** C201 DDL CHECK, C202.2 `Kind` comment, C211.1 kind count, D-T1-25, lane/hot-spot tables, acceptance mapping, definition of done, risks, P.5. Revision 3 changelog item 10 and baseline row B35 are historical and left as written.

> **Revision 3 (2026-11-03).** Written against `origin/main` `5f6eeaf` (a read-only checkout; no network, no server,
> no database). The previous revision (2) is kept beside this file for diffing. Every row of the
> **Baseline reconciliation** below was checked in that worktree with `go doc`, `rg` and a read of the code; nothing was run.
>
> ## Revision 3 changelog (every material change)
>
> 1. **Numbering.** Contracts `CT.1…CT.17` → **C200…C216** (`CTn` → `C(199+n)`, mechanical); two new contracts **C217** (derived files,
>    work dirs and the media-serve chain, verified against the L2 tape) and **C218** (optional spatial/mastering lanes, *not scope until
>    approved*). Range C200–C249 is reserved for T1; **C219–C249 stay free**. Decisions keep `D-T1-n`; **D-T1-20…26 are new**.
>    The migration is written `NNNN_transforms.sql` = **the next free migration number at build time** (currently `0013`; `0012_token_expiry`
>    is the last on main; E2 and M5 also add migrations, the loader rejects gaps, so whoever merges first takes `0013`). No test or text
>    hard-codes 0013; schema assertions compare with `len(migrations)`.
> 2. **The "symbols that did not exist" stub list is gone.** L1/L3 shipped them (table rows B1–B9): `analysis.OnsetEnvelope`,
>    `analysis.PCM`/`NewPCM`, `ffmpeg.Mono`, `ComputeKey`/`ComputeEnergy`, `KeyDef`/`EnergyDef`/`TempoDef`, and `ParseStyleKey`/`ParseABCKey`
>    (**in `internal/features`, not `analysis`, with the signature `(tonicPC int, mode string, ok bool)`**). Dropped: the T1-B2 `keyparse.go`
>    stub, the `AnalysisReader` interface and the "rung skipped" language. **New finding that survives:** `analysis.OnsetEnvelope` calls
>    `math.Log1p`, which D-T1-3(a) bans in `dsp`, and its fixed hop (128 samples at any rate) is not the draft's rate-scaled hop (D-T1-20).
> 3. **Alignment reads L3's `take_features` rows** (basis `measured|declared|plan`, tempo `bpm_octave`, Camelot key) through
>    `features.Service.Take`, instead of re-deriving the ladder from `analyses`/`scores`/`ParseABCKey` itself (D-T1-9 and C204.2 rewritten);
>    explicit parameters still win, and the resolved numbers are still frozen in the spec.
> 4. **Runner lease: the `internal/runlease` extraction is dropped (D-T1-17 replaced).** The L2 tape runner already established a second
>    pattern: a **row-level lease** (`owner`, `lease_expires_at`, `Recover`) that does not touch `runner_lease` or `jobs.Runner`. T1 copies it
>    (columns on `transforms`). This removes the largest hot-spot (E2 rebuilds `jobs.Runner` per backend) and one lane's worth of work.
> 5. **`tools/list`: all seven operations are generic** (the L2/L3 operator rule, ceiling `toolsCeilingBytes = 108300`, measured 108 228, **72 bytes
>    of headroom**). The draft's four dedicated tools would have cost an estimated 14–17 KB (measured analogues on main: `estimate_generation` 5 386,
>    `create_generation` 4 322, `wait_for_job` 3 785, `cancel_job` 3 374 = 16 867 B). Dedicated tools are an **operator decision (P.5-G3)**, default generic.
> 6. **Engine sweep (new, from the reconciliation).** Nine readers treat `recipes.engine <> 'unknown'` as "a real YuE2 recipe"
>    (`exports/source.go:116`, `library/stats.go:296,315`, `library/write.go:815`, `generation/plan.go:174`, …); a `dsp` recipe would be exported
>    as a YuE2 request and skew seed/style statistics. C211.1c lists them and pins each with a test (`TestEngineSweep_Dsp`).
> 7. **Web re-targeted** to the shipped shell (C214): the Take page `/tracks/:id` (Library tab) gets the Transform panel, `/transforms` lives under **More**
>    next to Jobs (not a Library sub-view), no new tab, dark only, 44 px targets, secure pages untouched (nothing here is human-only).
> 8. **Tooling re-targeted** (C215): `make verify` inner loop, `scripts/e2e-map.txt`, `ci-shard-weights.txt`/`ci-heavy-tests.txt` refresh, coverage floors =
>    **measured − 1** (not aspirational 90), `.public-gate-patterns.example` exists, README config table row per `MM_*` variable
>    (`TestDocs_ConfigTableListsEveryEnv`), stubs answer **501 `NOT_IMPLEMENTED`** with `details.task` (not 503), per-lane test registration files.
> 9. **Lanes re-cut** to PR-sized pieces (< ~2.5k changed lines excluding generated): old T1-B3 split into B3 (runner/bridge) and B6 (library/readers);
>    foundation lane F1 first and alone; hot-spot ownership table added (Lanes and tasks).
> 10. **Gaps recorded, not invented:** no spatial/binaural/3D or mastering transform exists in the draft (the operator's "hi-fi / 3D / binaural"
>     wish for sound families); C218 sketches three **optional lanes** O1–O3 and P.5-G2/O boxes ask the operator. Nothing in them is T1a.
> 11. **Lessons baked in:** fixtures include draft-only Songs, references, lossy imports and multi-part imports (L3 X2 finding); coverage floors measured − 1; perf
>     tests calibrated for loaded runners (the tape perf test is the template); CI shard weights refreshed in I1; no running script is ever edited.
> 12. **Appendix P** kept (the old private appendix, renumbered P.1–P.5) with dated stale notes; **P.5 operator boxes** rewritten, gating boxes marked.

## Baseline reconciliation (checked against `origin/main` `5f6eeaf`, 2026-11-03)

Status: **OK** exists as assumed · **DIFF** exists, differs (resolution in the last column) · **MISSING** does not exist (T1 creates it) · **PROVIDED-L1/L2/L3** shipped by that plan.

| # | Assumed symbol / fact (draft) | Status | Finding on `main` | Resolution in this plan |
|---|---|---|---|---|
| B1 | `analysis.OnsetEnvelope` (**MISSING** at 226ae0f; local copy planned) | PROVIDED-L3 / DIFF | `func OnsetEnvelope(x []float32, sr int) []float64` in `internal/analysis/onset.go`; window 1024, **hop fixed at 128 samples at any rate**, band 40–5000 Hz, half-wave log-spectral flux minus moving mean; returns nil below 1024 samples. Uses `math.Log1p` and `analysis.FFT` | Local copy and `dsp/align/onset.go` parity test **dropped**. Use the export; D-T1-20 settles `Log1p` (X0 spike) and pins it with `TestOnset_PinnedForT1` (a golden, so a later analysis change fails loudly and forces a `dsp_version` bump). Tolerances of the beat tests re-derived: a frame is 8 ms at 16 kHz, 2.7 ms at 48 kHz |
| B2 | `analysis.ParseABCKey` / `ParseStyleKey` (**MISSING**; stub in `transform/keyparse.go`) | PROVIDED-L3 / DIFF | They live in **`internal/features/keyparse.go`**, `func ParseStyleKey(style string) (tonicPC int, mode string, ok bool)` and `ParseABCKey(abc string) (…)`, mode `"major"`/`"minor"`; plus `features.CamelotOf(tonicPC, mode)`, `KeyName` | Stub dropped. T1 does not call them at all: the key arrives from `take_features` (B5). `features.ParseABCKey` is a fallback only when a Take has a `score.abc` but no feature row (draft-only subjects do not matter: T1 inputs are Takes) |
| B3 | `mm-tempo/1` and `mm-key/1` analyses read through a nil-safe `AnalysisReader` | PROVIDED-L1/L3 | `analysis.TempoDef`, `KeyDef = "mm-key/1"`, `EnergyDef = "mm-energy/1"`, `KeyEnabled = true`; stored in `analyses` (analyzer `tempo`/`key`/`energy`); `features.SelectAnalysis` picks the row; `ears` `AnalyzeTrack` writes them | `AnalysisReader` dropped; not read directly (B5) |
| B4 | `analysis.PCM`, `ffmpeg.Mono`, `ComputeKey`, `ComputeEnergy` | PROVIDED-L3 | `analysis.PCM{Rate, Samples, AnalyzedS}`/`NewPCM`; `(*FFmpeg).Mono(ctx, workPath, maxS)` decodes mono **11 025 Hz** float32 ≤ 240 s; `ComputeKey(PCM)`, `ComputeEnergy(PCM, lufs)` | Not reused by the render: the child decodes native-rate stereo (D-T1-19). `ffmpeg.Mono` stays Ears' decode. After a transform Take is ingested, `ears.AnalyzeTrack` computes tempo/key/energy of the **result** as for any Take (C210.6 step 8) |
| B5 | `scores.bpm`, `bpm_declared`, `scores.aligned`, `scores.seconds_per_bar`, `sections` | OK + PROVIDED-L3 | `scores(bpm, seconds_per_bar, aligned, bars)` (0002); `sections(source ∈ lyrics/plan/measured/manual, start_s…)` — **`source='lyrics'` ⇔ `start_s IS NULL`**; `tracks.bpm_declared`. **New:** `take_features` (0011: `bpm_x100`+`bpm_basis ∈ measured/declared/plan`, `bpm_requested_x100`, `bpm_measured_x100`, `bpm_octave`, `key_num/key_letter/key_basis`, `duration_expected_s`, `digest`) read by `features.Service.Take(ctx, id)`/`Takes` | C204.2 ladder rewritten on `take_features` (D-T1-9). A Take without a row (feature sweep not run yet, or a freshly ingested transform Take) falls back to the same three direct reads as the draft |
| B6 | `tracks.origin CHECK IN ('import','job')`; transform Takes use `origin='job'` + `transform_id` (D-T1-4) | OK | Unchanged in 0002; no later migration widened it. Added since: `tracks.status_set_at` (0009), `lossy` (0005), `job_id`, `env_id` (0007) | D-T1-4 stands. `tracks_transform_shape` trigger columns re-checked (`bits_per_sample`, `channels`, `lossy`, `import_rule`, `job_id` all exist) |
| B7 | `origin` readers (draft: `jobs/ingest.go:424`, `stats.go:222`, `tracks.go:281/845/1218`, `write.go:343…467`) | DIFF (moved) | Now: `library/write.go:377` (insert; `isJob()` = `Origin == "job"`, `scoreSource`), `library/stats.go:279` (`COUNT(*) FROM tracks WHERE origin = 'job'` = `TakesMonth`), `library/tracks.go:299` (`origin IN` filter), `tracks.go:1336` (`GenerationOf` joins `jobs` on `t.job_id` and `t.origin='job'`), `jobs/ingest.go` (the only writer of `Origin: "job"`) | C211.1/4b keep the intent; the line numbers above replace the draft's. Re-grep at B6 start and append hits here |
| B8 | `mutations_child` UNIQUE; one parent per ledger row | OK | `CREATE UNIQUE INDEX mutations_child` (0002:151); 0007 added `mutations.job_id`, `recipe_patch_json`; `mutations_immutable` covers all columns | D-T1-5 stands (`transform_inputs` + `also_from`) |
| B9 | `enumOperator`, `rootlessOperators`, `NewMutation{Operator…}` | OK | `library/write.go:458/461`: 7 operators, rootless `from_draft`,`compose_new`; `NewMutation` has `Params`, `JobID`, `RecipePatchJSON` | C211.1 amendments unchanged (six operators; `score_render` rootless) |
| B10 | `library.InsertRecipe` YuE2-specific, `ErrRecipeExists`; new `InsertTransformRecipe` | OK | `write.go:127/177`; `recipes.hash` UNIQUE | Unchanged (C201.4). **E2 generalises `InsertRecipe`/`core_hash` per engine** (dependency E2-3) |
| B11 | `library.RequireRecipeKnown`/`RequireLosslessMaster`; `RecipeKnownEngine(e) = e != "unknown"` | OK | `library/reference.go:53,69,94`; `api/types_m1.go:189` maps `RECIPE_UNKNOWN`/`LOSSY_MASTER`; planner call site is `generation/plan.go` | `RequireRenderableMother` goes beside them; `RecipeKnownEngine('dsp')` stays true |
| B12 | **Engine sweep** — readers that assume a non-`unknown` recipe is YuE2 | **MISSING from draft** | `exports/source.go:116` (`SelectRecipes … r.engine <> 'unknown'` — would put dsp recipes into `songs.jsonl`/yue2 batch/ZIP manifests), `library/stats.go:296,315` (style-term and seed statistics over `engine <> 'unknown'`), `library/write.go:815` (`RelinkVariantiMothers`), `library/snapshot.go:75`, `generation/plan.go:174` (mother load parses `request_json` as YuE2), `lineage/tree.go:62`; `lineage/backfill.go:14` is already `engine = 'yue2'` (OK) | New C211.1c: each switched to `engine = 'yue2'` (exports, backfill, mother load) or documented neutral (tree), with `TestEngineSweep_Dsp` (a library holding a dsp Take: exports byte-identical, stats unchanged, planner refuses with `TRANSFORM_TAKE_NOT_MOTHER`) |
| B13 | Style readers and FTS | OK (re-verify) | `track_style_terms`, `tracks_fts_*` triggers on title (0002:298); `scanSummary`/`TrackDetail` recompute terms from `recipes.style` | C211.1b unchanged; `recipes.style = ''` accepted (verify at F1 step 1) |
| B14 | `lyrics_of` search filter | MISSING | `rg lyrics_of` finds nothing | New in B6 (C211.4); extends `search_tracks` and `internal/api/l2_filters.go`-style converters |
| B15 | `ingest.Request`/`Pipeline.Ingest`, `Result.Created`, `Discard` | DIFF (moved) | Package is **`internal/media/ingest`** (not `internal/ingest`): `Request{TrackID, Title, Slug, RecipeID, RequestID, Seed, Source fs.File, SourceSHA256, RecordedAt, Artifacts []ArtifactSrc, Reference}`; `Pipeline.Ingest(ctx, Request) (Result, error)`; `Discard(Result)` removes only `Result.Created` and **never `derived/`**; there is no `ingest.DiscardOrphan` | Paths fixed throughout. C210.3's "orphan `takes/<id>`" cleanup uses `Discard`-style removal written in `transform` (small helper, tested) rather than a helper that does not exist |
| B16 | `mediatool.Tools{FFmpeg, FLAC, Metaflac}`; `mediatool.Run` | OK / DIFF | `RunOpts{Timeout, Stdout, MaxStdout, MaxStderr, Stdin}`; **no process-group option** | As draft: opt-in group mode added (T1b adds `Fluidsynth`); the render child uses it |
| B17 | `jobs` runner lease inside `jobs.Runner` (private) | OK | `jobs/lease.go`, `store.LeaseFence`, table `runner_lease` | **Not touched** (D-T1-17 replaced): the transform runner uses a tape-style row lease (B18) |
| B18 | L2 tape runner pattern | PROVIDED-L2 | `internal/tape/runner.go`: `claim` sets `owner`, `lease_expires_at` (`LeaseTTL = 60 s`), `renew` every `LeaseTTL/6`, `Recover` re-queues on expiry (third expiry fails), `Run` loop = claim → process → `Recover` → wait on `wake` or 5 s | Copied by name into C210.2/3. `tapes` DDL (0010) is the template for the extra `transforms` columns |
| B19 | Derived-file chain | PROVIDED-L2 | See **C217** (verified): tape output `derived/tapes/<aa>/<sha>.flac`, `Resolve(sha, ext "flac")`, `serve.Chain(resolvers…)` in `app.go:326` (library first, then exports, clips, M4a covers/downloads, L2 resolvers), cache dirs listed in `importer/report.go:287`, `backup/snapshot.go:70` | C217 states which parts T1 reuses (the library resolver and the ingest pipeline; **no new resolver**) |
| B20 | `engine.Engine`, `engine/registry`, `engine/yue2` | OK | `internal/engine/{engine.go,registry,yue2}` | D-T1-12 stands: `dsp` is not an `engine.Engine`. **E2 rebuilds this surface (Caps v2)** — dependency E2-1 |
| B21 | Registry/`Meta`/stubs | DIFF | `api.Meta{Scope,Cost,MCP,UI,HumanOnly,Idempotency,Rate,SecretFields,MCPImage,AliasOf}`; stubs are `501 NOT_IMPLEMENTED` with `details.task` constants (`api/stub.go`: `taskT2`, …); per-plan `implementedL2/L3` test tables | Stub status 501, `taskT1-*` constants, per-lane registration files (Lanes) |
| B22 | `tools/list` baseline/ceiling | DIFF | `toolsCeilingBytes = 108300` (`mcpgen/m2a_contract_test.go:65`), golden 108 228 B / 39 tools (`testdata/tools_list.golden.json` is 256 737 B pretty); `toolsListBaselineBytes = 252891` (`parity_test.go:646`); registry op count in `TestRegistry_OpCount` (`api/ops_m2c_test.go:123`) — re-count at F1 | Draft's "ceiling 87 300" is obsolete. T1: all generic, golden's tool list unchanged, ceiling unchanged (C212.1) |
| B23 | Sound compute for a `dsp` take; Pick "newest" rule | OK | `sounds/compute.go:130-135` writes an `engine` facet row from `recipes.engine` (value `dsp` would be a new facet value; E2 adds `acestep15`) and reads `r.style`; `picks/derive.go:91` step 4 `SourceNewest` | C211.6 amendments stand (nearest non-`dsp` ancestor; never `newest`). Test names unchanged. E2 touches the same facet (dependency E2-7) |
| B24 | L3 features of a transform Take | **MISSING from draft** | `features/load.go:190-209`: for a recipe with engine ≠ unknown it reads `style`/`request_json` (empty for dsp), so a `dsp` Take would derive tempo/key/energy with **no basis except `measured`** and a facet-less energy prior | C211.6 adds: features of a `dsp` take use measured rows only, and take **no** declared/plan/style input (`TestFeatures_DspMeasuredOnly`); `features.Compute` is triggered after `ears.AnalyzeTrack` as for any new Take |
| B25 | L2 tape accepting transform Takes | OK | `tape` takes any lossless 48 kHz-resamplable Take (decodes `-ac 2 -ar 48000`, gain stage, `TAPE_CLIPPED` guard) | A transform Take in a Record exports as any Take; `TestAcceptance_T1_JoinsLibrary` gains a tape row |
| B26 | `ears.AnalyzeTrack(ctx, id, AnalyzeOptions)` | OK | `ears/analyze.go:155` | As draft |
| B27 | `canonjson`, `idem`, `redact`, `audit`, `store.LeaseFence` | OK | all present | As draft |
| B28 | `internal/dsp`, `internal/score`, `internal/transform`, `internal/runlease` | MISSING | none exists | `dsp`, `score` (T1b), `transform` created; **`runlease` is not created** |
| B29 | Web shell | DIFF | Tabs **New · Library · Play queue · Judge · More** (`lib/nav.ts`); Library sub-views Takes·Sounds·Atlas·Records·Crates·Playlists; Take page is route `track` `/tracks/:id` (Library tab); Jobs under More; router table `lib/router.ts`; per-feature `lib/*Api.ts`/`*View.ts` + `lib/components/`; e2e helpers `e2e/l*Api.ts`; secure pages in-app | C214 |
| B30 | CLI | OK | `cmd/metamusic/main.go` switch dispatch (`library`, `analyze`, `runpod`, `config`, …); `l2_cli.go`/`l3_cli.go` glue files | `transform` and hidden `dsp-run` added as cases; files `t1_cli.go`, `cmd_dsprun.go` |
| B31 | Config docs gate | **MISSING from draft** | `config/docs_test.go` requires a README configuration-table row for every `getenv("MM_*")` | F1 adds the nine rows |
| B32 | Fixtures | DIFF | `librarytest/fixture_l3.go` is the latest (`FixtureM3` is two generations old) | `FixtureT1` extends `FixtureL3` and adds the L3-X2 real-data cases (C216.2) |
| B33 | `GOAMD64`, toolchain pin | MISSING | `rg GOAMD64 Makefile Dockerfile .github` finds nothing | Added by I1 (default `v1`, made explicit) |
| B34 | `Dockerfile`, `.github/workflows/{ci,e2e-extra,gate,nightly,routing,weekly,…}.yml`, `scripts/{e2e-map.txt,ci-shard-weights.txt,ci-heavy-tests.txt,l1-public-copy.sh}` | OK | exist; the public-copy script cuts at the exact line `<!-- PRIVATE APPENDIX -->` and refuses a copy that still holds `## Appendix P` | I1 owns them; the marker line and `## Appendix P` heading below are kept verbatim |
| B35 | Spatial / binaural / mastering transforms | **MISSING (gap)** | Not in the draft's kinds, out-of-scope list or risks; "hifi" in the sound-family memory is a **generation level** (YuE2 production), not a DSP kind | C218 + P.5-G2: optional lanes O1–O3, operator decision, **not T1a** |

## Dependencies on E2 (by name and purpose; E2 is re-baselined in parallel, do not cite its numbers)

Roadmap order is M3 → L1 → L2 → L3 → E2 → **T1**. T1 has **no hard code dependency on E2**; these are the join points. Each is re-checked
by T1-X1 against whatever E2 merged (or, if T1 merges first, recorded in E2's plan by T1-X1).

| # | E2 contract (name / purpose) | How T1 touches it | Rule |
|---|---|---|---|
| E2-1 | Engine capability/registry rework (Caps v2, per-engine artifacts, `list_engines`) | `dsp` is **not** an `engine.Engine` (D-T1-12); `library.EngineDSP` is a constant beside E2's engine constants | T1 adds nothing to `engine/registry`; if E2 introduces a registry-wide "engine set" check, `dsp` is registered as render-less |
| E2-2 | Migration `NNNN_engines` (jobs rebuild; new `-- mm:foreign_keys=off` directive in the migrator; E2c adds a second migration) | T1's migration needs no rebuild and no directive | Numbers are assigned by merge order; T1 states "next free at build time" and must not sit between E2's two |
| E2-3 | `recipes.engine` gains `acestep15`; `core_hash`/`cot` validated per engine; `InsertRecipe` generalised | `recipes_dsp_shape` trigger and `InsertTransformRecipe` must coexist with the per-engine validation; `recipes.style = ''` for dsp | T1-F1 rebases on E2's `InsertRecipe` if merged first |
| E2-4 | Generation **modes that take a mother** (reroll, lyric/style edit, port, repaint, extend, reference) and the mother guards in `generation/plan.go` | `RequireRenderableMother` (D-T1-12) is called for every mode that reads the mother's **recipe**. Modes that only read the mother's **audio** (reference conditioning, repaint/extend of audio) may accept a transform Take — **open question for the operator, default: refuse all** until E2 says which modes read audio only | One test row per E2 mode (`TestEstimate_RefusesTransformMother` table) |
| E2-5 | Stems are **artifacts, not Takes** | T1 transforms accept Takes only. The draft's "stem Take" (ensemble demo, M5 Tier B parent) does not exist as a Take; ensemble keeps using an a-cappella-like intro Take | Out of scope unchanged; recorded so nobody assumes a stem parent |
| E2-6 | `jobs` runner and reconciler **per backend** | T1 no longer touches `jobs.Runner` (row lease, B17/B18) | None |
| E2-7 | Sound facet `engine` gains `acestep15` | `dsp` is another facet value; take compute for a dsp take walks to the nearest non-dsp ancestor | Taxonomy/prior tables (`features/prior.go`) get no `dsp` entry (prior is style-based) |
| E2-8 | Ingest per engine (artifact allowlist, `jobs/ingest.go`) | T1 uses `media/ingest.Pipeline.Ingest` directly with its own artifacts (`request.json`, `transform.json`) | None; no change to the jobs ingest |
| E2-9 | Suites and tape transitions (E2c) | Morph does not replace them (stay the fallback); a morph Take is an ordinary Take usable as a Suite/Record slot | None |
| E2-10 | Registry/tool counts and `toolsCeilingBytes` | T1 adds seven generic operations and no tool bytes; op-count pins (`TestRegistry_OpCount`) move by +7 in merge order | X1 recounts |
| E2-11 | Output sample rate / duration limits of ACE-Step (up to ~480–600 s) | `MaxInputS = 600`, native-rate decode and the Go resampler for a second input (D-T1-19) accept 44.1/48 kHz mixes | None |

> **Revision 2 scope split (binding).** **T1a** = the five DSP operators (morph, shepard, glacier, polytempo, ensemble), the service,
> runner, quota, ops, CLI, web panel and `/transforms` page. **T1b** = `score_render` (ABC parser, SMF writer, fluidsynth adapter,
> soundfont set, `/transforms/score` page, the image's fluidsynth package). T1b is the **final stage**, is gated by its own switch
> (`MM_SOUNDFONTS` non-empty **and** fluidsynth present; otherwise `kind: score_render` answers 503 `UNAVAILABLE {component: "soundfont"}`
> and the capabilities omit it), lands in its own PR (`t1b`) and **cannot block the T1a merge**. Everything below marked **(T1b)** is
> outside the T1a definition of done. The migration is complete for both (kind list, operator names) so T1b needs no second migration. **Optional spatial/mastering lanes (C218) are neither T1a nor T1b and are not scheduled.**
> **Public/private split (continued).** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` is public-safe (no real hostnames, IP addresses, song titles, artists,
> lyrics, request-id prefixes, archive counts, balances or personal data; `mm.example.com` and synthetic
> examples only). Only that part is copied to `docs/plans/<date>-t1-transforms.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`), and `make gate` runs on the copy before
> it is committed. The appendix is never copied.
>
> **Money and secrets (binding for every agent executing this plan).** T1 spends nothing: no backend
> call, no RunPod call (real or fake), no key file is read, no confirmation token exists. Every operation
> has `cost: free` and reports `$0.000000` explicitly; the cost of a transform is **CPU time on the
> application host**, bounded by C209/C210. No agent reads `.env` files or anything under `secrets/`.
> The only operator input is a soundfont file (T1-X0, T1-X3); no agent downloads one without the licence
> check recorded in the private notes.

**Goal:** a Take can be turned into new Takes by deterministic CPU sound operators, with the same three
faces as everything else. A human (a "Transform" panel on the Take page, a queue page) and an agent
(`estimate_transform` → `create_transform` → `wait_for_transform`) can build: a **morph** (a beat- and
key-aligned transition between two Takes of the same lyrics in different styles), a **shepard** (an
endlessly rising or falling construction from a Take's loop), a **glacier** (10–50× extreme time-stretch
into an ambient piece), a **polytempo** (two Takes at different tempos converging on a shared downbeat)
and an **ensemble** (the same material stacked across octaves and formant-shifted inside a synthetic giant
space made of generated impulse responses) — plus, in the optional **T1b** lane, a **score render** that turns an ABC score into
a score-faithful audio Take with an orchestral soundfont, the ground truth the classical demo is judged
against. Every result is an ordinary Take: engine `dsp`, recipe = the transform spec plus the parent
masters' sha256, an Operator Ledger edge (several input edges for two-input transforms), a frozen
24-bit FLAC master, Ears Tier A, Songs/Picks/Records/Crates membership. The same spec on the same parents
yields the same PCM (`metamusic transform verify`). CPU is protected like money is in M3: an estimate with
CPU seconds and `$0`, a per-job work budget, a fixed concurrency of one render, a queue limit, a daily CPU quota
per principal **kind** (a shared pool, D-T1-10), true cancellation (the render runs in a killable child process), a memory guard, and
a loudness-safety stage (target loudness from the parent, true-peak limiter, post-ingest verification).

**Architecture:** new packages, each behind a small interface so lanes build in parallel:
- `internal/dsp` (+ `dsp/pcm`, `dsp/pv`, `dsp/ir`, `dsp/safety`, `dsp/ops`, `dsp/align`) — pure Go, no cgo,
  no goroutine in an operator (determinism, D-T1-3): float64 planar buffers, 24-bit PCM writer, FFT (own implementation with
  **precomputed twiddles**; `analysis.FFT` is not wrapped), **deterministic `dsp.Exp`/`Log`/`Pow`** (`math.Exp`/`Log`/`Pow` are banned in
  the tree), a PCG wrapper with our own float conversion, windowed-sinc resampler (used only for a second input at another
  rate, C210.6), the STFT **phase vocoder** (variable time-scale, peak-shift
  pitch, cepstral formant warp), generated impulse responses and partitioned convolution, BS.1770 loudness,
  true-peak limiter, the five operators and the **work meter** (C203–C207). Imports only the standard
  library, `analysis` (the exported `analysis.OnsetEnvelope` only, D-T1-20) and `score`-free leaf types.
- **(T1b)** `internal/score` (+ `score/abc`, `score/smf`, `score/synth`) — the neutral note-event model, a pure-Go ABC
  parser (two dialects), a Standard MIDI File **writer**, and the synth adapter (fluidsynth CLI behind a
  `Synth` interface, D-T1-8). Imports the standard library only (`synth` also `mediatool`).
- `internal/transform` — the domain service: spec building and canonicalisation, alignment resolution from
  library data, estimate, create (one transaction), the runner (DB-resident queue, lease, quotas), the
  child-process supervisor (progress, deadline, RSS guard, cancel), the ingest bridge (encode → ingest →
  `library.InsertTake` → `ears.AnalyzeTrack`), reads, `Wait`, `Verify`.
- `cmd/metamusic dsp-run` — the render child (C210.4); `metamusic transform verify <take-id>` (C213).
- `internal/library`, `internal/lineage`, `internal/api`, `internal/store` — amendments only (C201, C211,
  C212): the migration, `InsertTransformRecipe`, the take write, the mother guard, lineage `also_from`,
  seven registry operations, types, errors.
- **Import seams (binding):** `dsp` and `score` import nothing of the application (`TestImportGraph_T1`,
  `go list -deps -json`, extends M3's `TestImportGraph_M3`); `transform` never imports `api`, `jobs`,
  `spend`, `provider`, `generation` (no money, no backend); `jobs` never imports `transform`; the runner
  is woken through an injected, nil-safe `Wake func()` and owns its row lease like the tape runner (D-T1-17); `library` never imports `transform`.
- Every T1 operation passes M2a's gate. `create_transform` and `cancel_transform` commit their idempotency
  row and audit row inside the operation's transaction (D-M3-21 pattern) with `CostMicroUSD = 0`.
- The SPA gains a Transform panel on Track, `/transforms` (queue, history, today's CPU use, Cancel) and
  (T1b) `/transforms/score` (score render form), and shows multi-parent lineage.

**Tech stack:** no new Go module dependency (stdlib `math` (minus the banned functions, D-T1-3), `math/rand/v2` **only `NewPCG` and `Uint64`**, `encoding/binary`,
`crypto/sha256`, `os/exec` through `mediatool`). **Toolchain pin:** release and test builds use the Go version of `go.mod` and
`GOAMD64=v1` (the default; `v3` lets the compiler emit FMA), set explicitly in the Makefile, CI and the Dockerfile. Runtime tools: **ffmpeg** (decode/encode, already in the
image), **flac/metaflac** (already), **fluidsynth ≥ 2.3** (**T1b only**; new; LGPL-2.1-or-later per its source headers, a separate process: no linking,
no licence contamination of the MIT repository; Alpine package `fluidsynth`) for `score_render` only. A
**soundfont** (SF2/SF3) is supplied by the operator at run time and never committed (D-T1-8). Web: no new
runtime dependency. Pin every version at implementation time (context7 for Svelte/Vite/Playwright; the
fluidsynth CLI surface is pinned by T1-X0's recorded `--help` output and `TestFluidsynth_Argv`).

**Out of scope (YAGNI, with owner):** vocal/stem isolation (E2 stems and M5 Tier B produce stem Takes that
any transform then accepts as a parent — T1 does not isolate anything; **T1 has no dependency on stems**: the ACE-Step research notes say
`extract`/`lego`/`complete` exist only on the base DiT models, not on the fast turbo/sft ones, so a stem Take may never exist); transforms of lossy or reference
Takes (D-T1-11); user-uploaded impulse responses, soundfonts or arbitrary audio (import it first); a pure-Go
SF2 sample synth (rejected for T1, D-T1-8; the `Synth` interface leaves the door open); beat tracking for
rubato/variable-tempo music (constant tempo is assumed and listed as a blind spot); automation curves and
per-section parameters; real-time or streaming audition and a "preview" render (a 60 s excerpt is a cheap
render of its own); several transforms in one estimate (a loop of calls does it; agents have the loop);
automatic Pick of a transform Take (D-T1-13); GPU DSP; humanised or expressive score playback; MusicXML,
ABC multi-tune files, lyrics alignment to the score render; per-operator ML models; spending mandates or
approvals (there is no money; M5); **replacing E2's Suite/tape-assembler joins** (they stay the fallback; T1 morph does not replace them in this milestone); a MIDI upload or `.mid` picker, a MIDI reader in production code, custom impulse responses, free-form ensemble voices, `coincide` polytempo, linear/hold morph variants, glacier low-pass/linked stereo, score reverb and a voice-name instrument heuristic (all cut in revision 2: every parameter multiplies the validation table, the capabilities, the forms and the tests; add one only on a demonstrated need) and **spatial/binaural/3D and mastering transforms** (the draft never had them; recorded as optional lanes in C218, operator decision P.5-G2).

---

## Dependencies and sequencing

- **Hard:** M0, M1, M2a, M2b, M2c, M3, L1, M4a, L2 and L3 as merged on `main` (`5f6eeaf`). Branches `t1-<lane>`; worktrees `metamusic-wt/t1-*`. Roadmap order is M3 → L1 → L2 → L3 → E2 → **T1** → M4a/M5/M6; T1 needs **no E2 code** — the join points are the E2 table above, each re-checked by T1-X1.
- **Soft (graceful degradation, D-T1-9):** L3's `take_features` rows and the Ears `tempo`/`key` analyses feed alignment *defaults*; with none present every transform still works when the caller passes explicit tempo/key values (otherwise 422 `ALIGNMENT_UNRESOLVED` names the missing value). **All former stub symbols are real** (reconciliation rows B1–B5); T1 never edits L1/L3 tables, it only adds readers and the amendments listed in C211.6.
- **T1b gate:** the T1b lane (S1–S3, W2, the score parts of B2–B4, I1b) starts after T1a's PR is open, merges separately, and a missing soundfont/fluidsynth disables only `score_render`.
- **Order and lanes:** see "Lanes and tasks" (lane table, hot-spot ownership). Foundation first and alone (F1), then parallel PR-sized lanes, INT-B/INT-W, X1 docs, X2 production-copy check, X3 operator review.

## Decisions recorded by this plan

T1-X1 copies them into the private design document.

| Id | Decision | Why |
|---|---|---|
| D-T1-1 | **A transform is a Take.** Engine `dsp`; the recipe's `request_json` is the canonical transform spec (C202), `recipes.hash` is M1's hash (sha256 of the `request_json` bytes **including** `id`; `spec_hash` is a different, id-less hash, C202.1), `recipes.style` is `''` (the human label lives in `transforms.title`, so no style reader can mistake it for style text, C211.2), `lyrics` and `cot` are `''`, `seed` is the spec seed (0 when unused), `created_by` is the principal. The Take has an Operator Ledger row (operator = the kind), `parent_track_id` = the first input, `root_track_id` = that parent's root, `generation` = parent + 1; `score_render` is a motherless root (generation 0). Output: stereo, 24-bit FLAC, the sample rate of the first input (48 000 for a score render), frozen by M1's pipeline like every master. | One noun, no second library; Songs, Picks, Records, Crates, duels, Ears and the player all work unchanged (concept §6). |
| D-T1-2 | **Same spec ⇒ same take (reuse rules).** A create whose `spec_hash` matches a transform in state `queued`, `running` or `done` returns that transform (`reused: true`, HTTP 200 for `done`, 202 otherwise); a `failed` or `cancelled` one is replaced by a new transform row on the same recipe. A partial UNIQUE index enforces it. The recipe id contains the hash (`dsp-<kind>-<hash20>`, first 20 hex of `spec_hash`), so the unique request-id index of M3 holds. **Discarded results:** if the matching `done` transform's Take has status `discarded`, create does **not** answer `reused` forever: because the render is deterministic, a re-render would be identical audio, so create **restores** the existing Take to `draft` in the create transaction (the library's own un-discard function, verified at align; `status_set_by` = the principal, audit row `transform_restored`) and returns it (`reused: true`, `restored: true`, 200). The user can therefore always get a discarded spec back; nothing is re-rendered and `tracks_audio_identity` is never violated. **Cross-principal reuse:** agent B asking for agent A's live spec receives A's transform (view shows `principal` = A, no new quota charge to B); B **cannot cancel it** (403 `FORBIDDEN`, `details.owner`), A and any human can; if A cancels it, B's `wait_for_transform` ends with `cancelled`/`CANCELLED_BY_USER` and B may create it again. Tests: `TestCreate_DiscardedDoneRestored`, `TestCreate_CrossPrincipalReuse`, `TestCancel_CrossPrincipalReuse`. | Determinism makes identical audio a duplicate: M1's `tracks_audio_identity` index would refuse a second copy anyway, and a cache hit costs nothing. |
| D-T1-3 | **Determinism contract (revision 2).** DSP is single-goroutine per job, `float64` throughout, fixed algorithms and constants versioned by `dsp_version = "mm-dsp/1"` (part of the spec: any algorithm change bumps it and therefore the hash). **No host-dependent arithmetic:** (a) *(the list is narrowed by D-T1-20 if the X0 spike shows `Log`/`Log1p` are pure Go on the targets)* `math.Exp`, `math.Exp2`, `math.Expm1`, `math.Log`, `math.Log2`, `math.Log10`, `math.Log1p`, `math.Pow`, `math.Sinh/Cosh/Tanh`, `math.Cbrt` and `math.FMA` are **banned in `internal/dsp/...`** (go/ast scan, `TestDSP_BannedMath`, next to `TestOps_NoGoroutinesNoClock`): Go's amd64 `Exp` is assembly that picks an FMA path at run time, so two amd64 CPUs can differ by an ulp; the tree uses its own pure-Go **`dsp.Exp`, `dsp.Log`, `dsp.Pow`** (range reduction plus fixed polynomial, every product written `float64(a*b)` so the compiler cannot fuse, accuracy ≤ 2 ulp against `math`, bit-pattern goldens `TestMath_Golden`); `math.Sqrt`, `Abs`, `Floor`, `Ceil`, `Sin`, `Cos`, `Atan2` stay allowed (exact or pure Go on the pinned targets; confirmed by reading the Go 1.27 source in T1-X0); (b) randomness comes only from `math/rand/v2`'s `NewPCG` and **`Uint64()`**, with our own conversion (`float64(u>>11) * 0x1p-53`; the Go `rand/v2` method algorithms such as `Float64` are not a stability promise and are not used: `TestDSP_RandOnlyPCG`); (c) FFT twiddles are **precomputed per size** from `math.Sin`/`math.Cos` of the exact index (no recurrence, whose error grows with size); (d) `GOAMD64=v1` (the default) is pinned in Makefile, CI and Dockerfile (`v3` emits FMA), and the golden test **fails** (not skips) on amd64 when the build's `GOAMD64` differs; (e) no map iteration order, no time, no concurrency in an operator. The oracle is `pcm_sha256` = sha256 of the final 24-bit little-endian interleaved PCM *before* encoding. Goldens are pinned for `linux/amd64`; other architectures (arm64 fuses multiply-adds) run the same-machine tests only. `transform.json` records the toolchain `{go_version, goarch, goamd64, dsp_version}`; **`Verify` answers `Inconclusive` when any of them differs from the running binary**, exactly as it does for fluidsynth. `TestDeterminism_FMAOff` re-executes the golden under `GODEBUG=cpu.fma=off` (name checked on Go 1.27 in T1-X0) and requires the same hashes. `score_render` (T1b) adds the external synth: reproducible given soundfont sha256 + fluidsynth `major.minor` (both recorded; `Inconclusive` when the version differs). | "Deterministic from the spec" must be testable, not hoped for, and must survive a CI-runner or host change. |
| D-T1-4 | **No `tracks` rebuild.** Transform Takes are written with `origin='job'`, `job_id IS NULL`, `import_rule='transform'` and the new frozen column `tracks.transform_id` (= the take id). SQL triggers tie the pieces together (C201). The API's `origin` is derived: `transform` when `transform_id IS NOT NULL`, `job` when `job_id IS NOT NULL`, else `import`. | Widening a CHECK in SQLite means rebuilding the busiest table under foreign keys; the column costs one `ALTER`. |
| D-T1-5 | **Several inputs, one ledger row.** `transform_inputs(transform_id, ord, role, track_id, master_sha256)`, ord 0 = primary parent (mirrors `mutations.parent_track_id`). The lineage tree and `/context` list the other inputs as `also_from` with their role; the transform row records the input master hashes at create time and the recipe repeats them. A Take that is an `also_from` input of another never gains a child edge in the tree (the tree stays a tree; extra edges are annotations). | Keeps M1's invariants (`mutations_child`, one parent) and still tells the truth about provenance. |
| D-T1-6 | **Estimate is pure and free.** `estimate_transform` reads the database only (no audio decode, no file read), writes nothing, takes no token. It returns CPU milliseconds (expected, max, budget), output length and size, memory, the frozen alignment resolution, warnings, quota and queue position, and **`cost.micro_usd = 0`** on every call. `create_transform` re-resolves the same inputs and refuses to disagree with nothing: there is no estimate id (YAGNI); the create body is the spec. | Nothing to confirm when nothing is spent; a token would be ceremony. The estimate still earns its keep: it tells an agent how long and whether it will be refused. |
| D-T1-7 | **The render runs in a child process.** `metamusic dsp-run --work <abs dir>` (the same binary), started by the supervisor with `Pdeathsig`, low priority (`setpriority +10`), `GOMEMLIMIT` set, a wall deadline and an RSS poll; it reads decoded inputs from the work dir, writes 24-bit PCM and a report. Cancel = SIGKILL (≤ 2 s); an operator bug, OOM or panic costs one job, never the server. The in-process work meter (C203.6) enforces the deterministic CPU budget; the wall deadline is the backstop. | The DSP is CPU- and memory-heavy and written by us; isolation is cheaper than being wrong about it, and gives real cancellation. |
| D-T1-8 | **(T1b) Score render = pure-Go score toolchain + fluidsynth + operator soundfont.** ABC → events (`abc.Parse`; MIDI upload is cut) → a normalised SMF (`smf.Write`, padded with an End-of-Track at `last_tick + tail_s`, ≤ 200 000 events) → `fluidsynth -q -ni -F` → 24-bit WAV → safety → FLAC. The API accepts only a **soundfont name** from the server's configured set (`MM_SOUNDFONTS`), never a path; the recipe records `{name, sha256}`. No soundfont is ever committed, downloaded by CI or bundled; CI tests use a generated single-instrument SF2 (`dsptest.WriteSF2`). A pure-Go SF2 synth was rejected for T1 (≈ 1.5 kLoC with envelope/filter/loop/modulator parity and no quality edge over fluidsynth); `score/synth.Synth` is an interface so one can replace it. Candidate soundfonts and their licences are checked in T1-X0 (private notes), the plan assumes only "a licence compatible with private use of rendered audio; the file itself is never redistributed"; **any soundfont that is ever distributed inside the container image must ship its licence text** (the image contains none today, C215.1). | Parser and SMF writer are small, testable and ours; the sample playback problem is solved and LGPL-isolated in a separate process. |
| D-T1-9 | **Alignment is resolved, then frozen; phase and local tempo are measured locally.** At create time the service resolves per input a tempo and a key and writes the *numbers and their sources* into the spec. **Tempo ladder (revision 3):** explicit `bpm_*` → plan (`scores.bpm` when `scores.aligned = 1`, direct read; `plan`) → **`take_features.bpm_requested_x100`** (L3: the declared tempo, else the style term, else the plan; `declared`) → **`take_features.bpm_measured_x100`** (Ears `mm-tempo/1`; `measured`; the analyzer already answers "no tempo" below its own confidence gate `tempoMinConf = 2.0`, so the draft's extra 0.30 threshold is dropped). A measured value whose `take_features.bpm_octave` flag is set (octave-ambiguous against the requested tempo) is folded toward the partner (`OctaveFold`, C204.5); plan/declared/explicit values are trusted and never folded. The position tempo `bpm_x100` is **not** used (it can be the measured value even when a declared one exists; local refinement makes the distinction moot). **Key ladder:** explicit `semitones_b` skips it; else `take_features.key_num/key_letter` with its `key_basis` (L3 prefers measured, then style, then plan; the basis is recorded as `key_source`); a Take with no row, or no key in it, falls back to `features.ParseABCKey(score.abc)` then none ⇒ shift 0 + warning `key_unknown_assumed_none`. The Camelot → (pitch class, minor) inverse is a 24-row table in `transform/resolve.go` pinned against `features.CamelotOf` (`TestCamelot_Inverse`). **A feature row can be absent or stale** (it is a derived cache; a Take ingested seconds ago has none): the three direct reads (`scores`, `tracks.bpm_declared`, the `analyses` rows through `features.SelectAnalysis`) are the fallback and are what the spec records, so create never waits for the feature sweep. Beat phase and *local* tempo are the only audio-derived values; they are estimated **in a window around the transition** (tA / tB, about 8 bars), not once globally, because declared BPM is an integer and measured tempo is rounded (a 0.3–0.5 % error is 0.5 s or more after 2–3 minutes); they are a deterministic function of the master (identified by sha256 in the spec) and `dsp_version`, computed inside the render (C204.3). The **downbeat** is anchored, in order, by an explicit `downbeat_*_s`, then a plan/score bar grid (`scores.seconds_per_bar`, C204.2), and otherwise is **mandatory** (422 `ALIGNMENT_UNRESOLVED {quantity: "downbeat"}`) unless the caller opts in with `downbeat_*_s: "auto"`, which runs the energy heuristic and reports `downbeat_confidence` and the `downbeat_ambiguous` warning (surfaced in the estimate, the transform view, `transform.json` and the UI). No tempo or key anywhere ⇒ 422 `ALIGNMENT_UNRESOLVED` with the input and the parameter to pass. | The recipe stays a pure function of its text even though analyses and feature rows can be recomputed; L3 already did the basis/octave reconciliation, T1 reuses it instead of a second rule. |
| D-T1-10 | **Limits (defaults, config C200).** ≤ 2 inputs per transform; each input ≤ 600 s; output ≤ **900 s**; memory estimate ≤ **1 024 MiB**; per-job CPU budget `min(600 s, max(30 s, 3 × expected))`; **exactly 1 render at a time** (constant `ConcurrentRenders = 1`; the `MM_TRANSFORM_CONCURRENCY` knob is cut); **one** queue limit: `MM_TRANSFORM_QUEUE_MAX` (default 6) live (queued+running) transforms overall ⇒ 429 `TRANSFORM_QUEUE_FULL` (the per-principal/overall pair is cut). **Daily CPU quota** = rolling 24 h per principal **kind**, one shared pool: all agent tokens together draw from the agent cap (**1 800 s**), humans from their own cap (**14 400 s**), so one rogue agent can exhaust the agent pool and starve other agents but **never humans**; this is deliberate and stated here, in the capabilities text and in `TestQuota_AgentPoolSharedHumanUnaffected`. Accounting: a live row reserves `max_cpu_ms`; **every terminal row records `cpu_ms`, 0 for a job that never ran** (cancelled while queued, failed before start), so create-then-cancel loops release their reservation immediately (`TestQuota_CancelledQueuedReleases`). 429 `TRANSFORM_QUOTA_EXCEEDED` with `details.resets_at`. Rates: `create_transform` is `heavy`. | The host also serves media and the SPA; one core for transforms leaves the rest, and the quota stops a loop-happy agent without a human in it. |
| D-T1-11 | **Inputs are lossless, known, ordinary Takes.** `RequireLosslessMaster` (`LOSSY_MASTER`) and `RequireRecipeKnown` (`RECIPE_UNKNOWN`) apply to every input; transform Takes qualify (chains). A reference Take cannot be a parent because `tracks_reference_lineage` ties child and parent to the same kind; widening that is a later, explicit amendment. | Reuses M2c's guards and refusals verbatim. |
| D-T1-12 | **A transform Take is not a generation mother.** `lineage`/`generation` call the new `library.RequireRenderableMother` next to `RequireRecipeKnown`/`RequireLosslessMaster`: 422 `TRANSFORM_TAKE_NOT_MOTHER` with `details.track_id` and `details.nearest_renderable_ancestor_id` (null when the chain ends at a `score_render`). Draft parents are guarded by two triggers like M2c's reference trigger. | A `dsp` recipe is not a YuE2 request; "regenerate" must point at the nearest Take that is. |
| D-T1-13 | **Library behaviour of transform Takes.** (1) Status `draft`, `status_set_by 'system:transform'`, tags inherited from the primary parent (M2a C27), `bpm_declared` NULL and no style terms (C211.1). (2) Sound facets: the take compute walks to the nearest non-`dsp` ancestor for style-derived facets, keeps `engine` = `dsp` and `tempo_band` from its own measured tempo (amends L1 C93; test in T1-B6). (3) **Pick:** a transform Take is eligible only through the sources `pinned`, `ladder` and `stars`, never through "newest non-discarded" (amends L1 C91), so a freshly rendered glacier never silently becomes a Song's Pick. (4) Songs: a transform with a mother joins the primary parent's Song via `root_track_id`; a `score_render` is a root and gets its own Song (angle `reference`, set by the L1 reconciler's recipe-engine rule). (5) Records/Crates/playlists/duels/exports/Subsonic treat it as any Take; **Stats and generation readers do not count it as a generation** (`origin='job'` readers are switched to `job_id`, C211.4b); `lyrics_of` never matches it (C211.4); `songs.jsonl` and `yue2 batch` exports skip `dsp` recipes. | Concept §6: no second kind of citizen, but no silent promotion either. |
| D-T1-14 | **Loudness safety is a fixed policy, not a knob; the gain is derived, never failed on.** After the operator: (1) fail **only** on non-finite samples (`DSP_NONFINITE`) or a result quieter than −70 dBFS peak (`DSP_SILENT_OUTPUT`); (2) remove DC when |mean| > 1e-4; (3) 10 ms fades at both ends (glacier brings its own); (4) the **requested target** = the primary parent's measured LUFS (Ears `loudness`), else −16.0, clamped to [−30, −12] (optional `target_lufs` in the same range); (5) **second pass in the safety stage:** gain to the target, run the lookahead true-peak limiter (ceiling **−1.0 dBTP**, 5 ms lookahead, 80 ms release, 4× oversampled detection) on a copy and measure the shortfall `target − LUFS(limited)`; while the shortfall exceeds **6 LU** lower the target by the excess (bisection, ≤ 8 iterations, deterministic) — the result is `target_lufs_effective`, recorded with the requested target and the reduction in the report; a finished render is never thrown away for being loud; (6) after ingest the pipeline's own measurement is a **bug alarm**, not a policy: `true_peak ≤ −0.5 dBTP` and `|integrated − target_lufs_effective| ≤ 1.5 LU` unless the limiter engaged for > 10 % of the time (then `≥ target_lufs_effective − 6 LU`); a violation ⇒ `LOUDNESS_UNSAFE` and the ingest is discarded (honest renders cannot trigger it; it exists to catch a defect in step 5). The report records gain, limiter reduction, engagement, requested and effective targets. | Extreme operators (stacked octaves, 50× stretch, resonant space) routinely exceed their input's level and a parent's LUFS can ask for an unreachable target; a phone user must never be surprised, duels need comparable levels, and CPU already spent must not be discarded. |
| D-T1-15 | **(T1b) Score dialects.** `standard` follows the ABC 2.1 accidental rule (an accidental applies to the same pitch in the same octave until the bar line); `yue2` carries it across octaves (research notes: YuE2's native rule). `abc.Parse` defaults to the dialect of the source: `yue2` when the Take's `score.abc` came from a YuE2 plan (`from_track_id`), else `standard`; an explicit parameter wins. | The classical demo is judged against the *intended* notes; rendering a YuE2 plan with the wrong rule would "correct" what the model actually wrote. |
| D-T1-16 | **Naming guard.** "Reference" already means a recipe-less imported Take (M2c). The score renderer is `score_render` (operator, op id `*_transform` with `kind: score_render`), its Takes are *score renders*, and the demo prose says "score-faithful render"; no identifier in T1 uses `reference` for it. | Two meanings of one word in one schema is how guards get wired to the wrong thing. |
| D-T1-17 | **Runner coordination = a row-level lease, as the L2 tape runner (revision 3; replaces the `internal/runlease` extraction).** `transforms` carries `owner` and `lease_expires_at` (C201.1) exactly like `tapes` (0010): `claim` sets them in one transaction, a running row's lease is renewed every `LeaseTTL/6` (`LeaseTTL = 60 s`), `Recover` (startup and whenever the runner is idle) re-queues a `running` row whose lease expired (`attempts + 1`; the second expiry fails the row `INTERRUPTED`), and every write of a running row repeats `owner = ?` in its predicate. `jobs.Runner`, `runner_lease` and `store.LeaseFence` are **not touched**; with generation disabled, or the backend unconfigured, the transform runner works unchanged (a transforms-only deployment is supported by construction). Single instance is assumed (single-user, one process); two processes on one database are protected by the row lease alone. Tests: `TestRunner_RunsWithGenerationDisabled`, `TestRunner_LeaseRenewed`, `TestRunner_RecoverExpiredLease` (first expiry re-queued, second `INTERRUPTED`), `TestRunner_OwnerFence` (a stale owner's finish writes nothing). | The tape runner proved the pattern on this codebase; sharing the singleton lease would have edited `jobs.Runner` while E2 rebuilds it. |
| D-T1-18 | **Documented parity exceptions: `transform verify` and `transform sweep` are CLI-only.** CLAUDE.md rule 1 admits only documented exceptions; these two are recorded in `docs/architecture.md` ("Transforms — parity exceptions") with the reason: `verify` is a host-operator maintenance re-render with **no ingest and no queue row** (it writes only to a scratch dir and cannot be expressed as a queued kind without inventing a transform that produces no Take), it is not reachable by any API principal, so the daily quota that protects the host from API principals does not apply to it; `sweep` is offline recovery that refuses to run while a server holds a live transform lease. Agents obtain reproducibility evidence from `get_transform` (`pcm_sha256`, toolchain record). Test: `TestArchitectureDoc_T1Exceptions` (the doc names both commands and the reason). | Adding a queued `verify_transform` kind was weighed and rejected as unrequested scope; the exception is explicit instead of silent. |
| D-T1-19 | **Decode policy.** Inputs are decoded by ffmpeg at their **native rate and channel count** (no `-ar`, no `-ac`; FLAC decode and 24-bit → f32 are exact); mono is duplicated to stereo in Go; the output rate is the first input's rate and a second input at another rate is resampled in Go by `dsp.Resample` (so no ffmpeg resampler or up-mix level decision is ever part of the PCM). The ffmpeg version is recorded in `transform.json` for information only. | An `apk upgrade` of ffmpeg must not be able to flip `Match` with no `dsp_version` bump. |
| D-T1-20 | **Onset front end and the determinism pin.** The beat-phase front end is the exported `analysis.OnsetEnvelope(x []float32, sr int)` (window 1024, hop 128 samples at **any** rate, 40–5000 Hz), fed a mono float32 mix of the working-rate input; the draft's rate-scaled hop and local copy are dropped. It calls `math.Log1p`, which D-T1-3(a) bans in `dsp`: **X0 step 6 reads the Go 1.27 sources** (`Log`, `Log1p`, `Log2`, `Log10`, `Sqrt`, `Sin`, `Cos`, `Atan2`) and decides — if they are pure Go on amd64 (no assembly, no run-time FMA selection) the banned list is **narrowed** to the functions that are not (`Exp`, `Exp2`, `Expm1`, `Pow`, `Sinh`, `Cosh`, `Tanh`, `Cbrt`, `FMA`) and the export is called as shipped; otherwise T1 keeps a local `dsp/align/onset.go` that replaces `Log1p` by `dsp.Log` and a parity test against the export within 1e-9. Either way **`TestOnset_PinnedForT1`** commits a golden hash of the envelope of a fixed synthetic signal: a later change to `analysis` (a retune, an `mm-tempo/2`) fails loudly and the response is a `dsp_version` bump, never silent drift. `analysis` is standard-library-only, so the render child may import it (`TestDspRun_Imports` allow-list gains it). | The export exists but was written for an analyzer, not for a bit-exact render. |
| D-T1-21 | **All seven operations are generic MCP operations by default** (the L2/L3 operator rule; `tools/list` ceiling `toolsCeilingBytes = 108300`, measured 108 228, 72 bytes of headroom). `estimate_transform`, `create_transform`, `wait_for_transform`, `cancel_transform`, `get_transform`, `list_transforms`, `get_transform_capabilities` are all `MCP: "generic"`; an agent reaches them through `call_operation` and discovers them through the existing catalogue. Promoting the four lifecycle operations to dedicated tools is an **operator decision (P.5-G3)** with measured analogue bytes on main: `estimate_generation` 5 386, `create_generation` 4 322, `wait_for_job` 3 785, `cancel_job` 3 374, i.e. about **14–17 KB** and a ceiling re-baseline; the first lane to need it measures the real schemas and demotes `estimate_transform` first (the D-L1-20 / D-L3-23 pattern). | The tool list is the scarcest shared resource; four more tools would exceed the rule that every milestone's new tools fit 16 000 B. |
| D-T1-22 | **Engine sweep.** A transform recipe has engine `dsp`; every reader that uses `engine <> 'unknown'` to mean "a YuE2 recipe" is audited and switched (C211.1c). Exports and the mother loader use `engine = 'yue2'`; statistics skip `dsp`; `RelinkVariantiMothers` and the snapshot reader are checked for harm and either skipped or documented neutral. One table test, `TestEngineSweep_Dsp`, runs a library holding a transform Take through every export, stats and planner path. | The draft found only `origin='job'` readers and the exports; reading the code finds nine. |
| D-T1-23 | **Results are Takes built through the existing media ingest; they are derived artifacts of frozen parents (rule 7).** The output is a *new* file frozen at ingest (`audio/masters/…`, `takes/<id>/`, mode 0444, tags written once, renditions `mp3_320` + `peaks` content-addressed under `derived/`) and is served by the **existing library resolver** — no new resolver, no `derived/transforms/` tree (C217). No parent master or its tags is ever rewritten (`TestAcceptance_T1_MastersFrozen`). The alternative, a tape-style derived-only file (`derived/<kind>/<aa>/<sha>.flac`, own table, retention, own `serve.Resolver`, not a Take, so no Songs/Picks/Records/duels/stars), is **rejected as the default** because judging by ear (duels, stars, Picks) is the point of a transform and tapes cannot be judged; it stays available as an operator choice (P.5-G1) with its changed lane list recorded in C217. | The operator's use case is "listen and judge a transformed version"; only Takes carry judgments. |
| D-T1-24 | **Features of a transform Take are measured-only.** `features/load.go` reads `style`/`request_json` for any known engine; for engine `dsp` both are empty, so the take compute must pass **no declared, style, plan or facet input** (it would otherwise read an empty style as "no key" and derive a prior from no facets). `TestFeatures_DspMeasuredOnly`; `features.Compute` runs after `ears.AnalyzeTrack` like for any new Take (the feature sweep covers the rest). | Avoids a silently wrong "predicted energy" on the Atlas for a take that has measurements. |
| D-T1-25 | **(Superseded for T1h by revision 4, C218–C225: `master`, `widen`, `binaural` are built in lanes H1/H2.)** **Spatial / binaural / mastering transforms are not in T1a/T1b.** The draft covers none (the ensemble's synthetic space is a reverb, `safety` is loudness policy, not mastering). They are recorded as optional lanes O1–O3 (C218) and as operator boxes; nothing is built, sized into the lane table, or reserved in the migration kind list until approved (a new kind later is one `CHECK` rewrite on the small `transforms` table). | "Don't invent scope." |
| D-T1-26 | **Process.** Stubs answer `501 NOT_IMPLEMENTED` with `details.task = "T1-<lane>"` (constants in the style of `api/stub.go`); each lane registers the operations it implements in its **own** test file (`ops_t1_<lane>_test.go`, appending to a package-level slice in `init`), never editing a shared `implementedT1` table; `make verify` plus focused tests while iterating; full suites in CI. | The L2/L3 lanes conflicted on shared tables. |

## Contracts (all lanes build against these — do not change without updating this file)

### C200. Config, limits, data layout, leaf packages

1. `internal/config/config_t1.go` (same `getenv`/`durationVar`/`intVar` helpers as `config_m3.go`; every
   value validated at startup, errors name the variable never a value). Fields and defaults:

| Variable | Field | Default | Range |
|---|---|---|---|
| `MM_TRANSFORM_ENABLED` | `TransformEnabled` | `true` | bool; `false` ⇒ ops answer 503 `UNAVAILABLE {component:"transform"}` |
| `MM_TRANSFORM_QUEUE_MAX` | `TransformQueueMax` | 6 (overall live transforms; no per-principal limit, D-T1-10) | 1–32 |
| `MM_TRANSFORM_MAX_CPU_S` | `TransformMaxCPUS` | 600 | 30–3600 |
| `MM_TRANSFORM_MAX_OUT_S` | `TransformMaxOutS` | 900 | 60–3600 |
| `MM_TRANSFORM_MAX_MEM_MB` | `TransformMaxMemMB` | 1024 | 256–8192 |
| `MM_TRANSFORM_AGENT_DAILY_CPU_S` | `TransformAgentDailyCPUS` | 1800 | 0–86400 (0 ⇒ agents cannot transform) |
| `MM_TRANSFORM_HUMAN_DAILY_CPU_S` | `TransformHumanDailyCPUS` | 14400 | 0–86400 |
| `MM_SOUNDFONTS` | `Soundfonts` | empty (**T1b gate:** empty ⇒ `score_render` disabled) | `name=/abs/file.sf2[;name=/abs/file.sf3…]`, names `^[a-z][a-z0-9_-]{0,31}$`, ≤ 8 |
| `MM_FLUIDSYNTH` | `FluidsynthPath` | `PATH` lookup (T1b) | absolute path override |

2. **Data layout (under `DataDir`):** work dirs `incoming/transform-<id>/` (mode 0700; contains
   `job.json`, `in-<n>.f32`, `out.pcm24`, `report.json`, `out.flac`, `score.mid`; removed on every terminal
   path and swept at startup), the ingested take under `takes/<id>/` with artifacts `request.json` (the spec),
   `transform.json` (the report, C210.5) and, for score renders, `score.abc` and/or `score.mid`. Soundfonts
   stay where the operator put them (read-only, never copied).
3. **Leaf packages:** `internal/dsp/*` and `internal/score/*` (above). `internal/dsp/dsptest` (test-only
   helpers, C216). `TestImportGraph_T1` enforces the seams of the Architecture section.
4. **Constants** (`internal/transform/limits.go`; tests reference the names): `MaxInputs = 2`,
   `MaxInputS = 600`, `MaxScoreBytes = 262144` (T1b), `MaxScoreEvents = 200000` (T1b), `MaxTitleRunes = 200`,
   `ConcurrentRenders = 1`, `QuotaWindow = 24h`, `ProgressThrottle = 1s`, `CancelGrace = 2s`,
   `RSSPoll = 500ms`, `WallFactor = 3` (deadline = `WallFactor × budget`, min 60 s), `MaxAttempts = 2`,
   `WorkSweepAge = 1h`, `LeaseTTL = 60s`, `DSPVersion = "mm-dsp/1"`, `TargetLUFSDefault = -16.0`, `CeilingDBTP = -1.0`, `MaxLimitingLU = 6.0`.

### C201. Migration `internal/store/migrations/NNNN_transforms.sql` (full DDL) and write SQL

1. **Schema.** `transform_inputs.ord` 0 is the primary parent (mirrors `mutations.parent_track_id`) for every kind **except `score_render`**, whose only input (`role = 'score_source'`, present when `from_track_id` is used) is a provenance annotation and never the ledger parent (the score render stays rootless). (Schema version `N` = `len(migrations)`; a database at any earlier version migrates in one
   boot; no `foreign_keys=off` directive is needed because no table is rebuilt.)

```sql
-- NNNN_transforms.sql — T1: CPU sound operators as Takes (engine 'dsp').

CREATE TABLE transforms (
  id                  TEXT PRIMARY KEY,                  -- UUIDv7 = the take id
  kind                TEXT NOT NULL CHECK (kind IN ('morph','shepard','glacier','polytempo','ensemble','score_render','master','widen','binaural')),
  recipe_id           TEXT NOT NULL REFERENCES recipes(id),
  spec_hash           TEXT NOT NULL CHECK (length(spec_hash) = 64),   -- sha256 of the canonical spec WITHOUT id (C202.1); NOT recipes.hash
  title               TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  rationale           TEXT CHECK (rationale IS NULL OR length(rationale) BETWEEN 1 AND 1000),
  principal_id        TEXT NOT NULL,                     -- Principal.Key()
  principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('human','agent')),
  principal_name      TEXT NOT NULL,
  state               TEXT NOT NULL CHECK (state IN ('queued','running','done','failed','cancelled')),
  phase               TEXT CHECK (phase IS NULL OR phase IN ('preparing','analyzing','rendering','finishing','encoding','ingesting')),
  progress_permille   INTEGER NOT NULL DEFAULT 0 CHECK (progress_permille BETWEEN 0 AND 1000),
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 2),
  expected_cpu_ms     INTEGER NOT NULL CHECK (expected_cpu_ms >= 0),
  max_cpu_ms          INTEGER NOT NULL CHECK (max_cpu_ms >= expected_cpu_ms),   -- the budget; reserved against the daily quota while live (released at any terminal state)
  out_seconds_est     REAL    NOT NULL CHECK (out_seconds_est > 0),
  mem_mb_est          INTEGER NOT NULL CHECK (mem_mb_est > 0),
  cpu_ms              INTEGER CHECK (cpu_ms IS NULL OR cpu_ms >= 0),            -- measured (child user+sys time + tool time); 0 for a job that never ran
  pcm_sha256          TEXT CHECK (pcm_sha256 IS NULL OR length(pcm_sha256) = 64),
  error_code          TEXT,                              -- Go-validated stable UPPER_SNAKE (C212.4)
  error_text          TEXT CHECK (error_text IS NULL OR length(error_text) <= 500),   -- redacted, no paths
  cancel_requested_at TEXT,
  owner               TEXT,                              -- runner id while running (row lease, D-T1-17; the tapes pattern)
  lease_expires_at    TEXT,
  created_at          TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,
  CHECK ((state IN ('done','failed','cancelled')) = (finished_at IS NOT NULL)),
  CHECK ((state IN ('failed','cancelled')) = (error_code IS NOT NULL)),
  CHECK ((state = 'done') = (pcm_sha256 IS NOT NULL)),
  CHECK ((state IN ('done','failed','cancelled')) = (cpu_ms IS NOT NULL)),   -- every terminal row records cpu_ms (0 if it never ran): quota reservations are always released
  CHECK ((state = 'running') = (owner IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX transforms_state     ON transforms(state, created_at);
CREATE INDEX transforms_principal ON transforms(principal_id, created_at);
CREATE INDEX transforms_finished  ON transforms(finished_at) WHERE finished_at IS NOT NULL;
CREATE UNIQUE INDEX transforms_live_spec ON transforms(spec_hash) WHERE state IN ('queued','running','done');  -- D-T1-2
CREATE TRIGGER transforms_no_delete BEFORE DELETE ON transforms
BEGIN SELECT RAISE(ABORT, 'transforms: no physical delete'); END;
CREATE TRIGGER transforms_frozen BEFORE UPDATE OF id, kind, recipe_id, spec_hash, title, rationale,
  principal_id, principal_kind, principal_name, expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, created_at ON transforms
BEGIN SELECT RAISE(ABORT, 'transforms: frozen column'); END;
CREATE TRIGGER transforms_final BEFORE UPDATE ON transforms
WHEN OLD.state IN ('done','failed','cancelled')
BEGIN SELECT RAISE(ABORT, 'transforms: terminal rows are immutable'); END;
CREATE TRIGGER transforms_state_machine BEFORE UPDATE OF state ON transforms
WHEN NOT (OLD.state = NEW.state
  OR (OLD.state = 'queued'  AND NEW.state IN ('running','cancelled','failed'))
  OR (OLD.state = 'running' AND NEW.state IN ('queued','done','failed','cancelled')))
BEGIN SELECT RAISE(ABORT, 'transforms: illegal state transition'); END;

CREATE TABLE transform_inputs (
  transform_id  TEXT NOT NULL REFERENCES transforms(id),
  ord           INTEGER NOT NULL CHECK (ord BETWEEN 0 AND 1),
  role          TEXT NOT NULL CHECK (role IN ('source','a','b','score_source')),   -- score_source (T1b): annotation only, never the ledger parent
  track_id      TEXT NOT NULL REFERENCES tracks(id),
  master_sha256 TEXT NOT NULL CHECK (length(master_sha256) = 64),
  PRIMARY KEY (transform_id, ord)
) WITHOUT ROWID;
CREATE INDEX transform_inputs_track ON transform_inputs(track_id);
CREATE TRIGGER transform_inputs_immutable BEFORE UPDATE ON transform_inputs
BEGIN SELECT RAISE(ABORT, 'transform_inputs: immutable'); END;
CREATE TRIGGER transform_inputs_no_delete BEFORE DELETE ON transform_inputs
BEGIN SELECT RAISE(ABORT, 'transform_inputs: no physical delete'); END;
CREATE TRIGGER transform_inputs_master BEFORE INSERT ON transform_inputs     -- the recorded hash is the track's master
WHEN NEW.master_sha256 <> COALESCE((SELECT master_sha256 FROM tracks WHERE id = NEW.track_id), '')
BEGIN SELECT RAISE(ABORT, 'transform_inputs: master hash mismatch'); END;
CREATE TRIGGER transform_inputs_lossless BEFORE INSERT ON transform_inputs
WHEN NEW.role <> 'score_source' AND COALESCE((SELECT lossy OR import_rule = 'reference' FROM tracks WHERE id = NEW.track_id), 1)
BEGIN SELECT RAISE(ABORT, 'transform_inputs: lossy or reference input'); END;

-- Provenance on tracks and ledger rows (set once, at insert).
ALTER TABLE tracks    ADD COLUMN transform_id TEXT REFERENCES transforms(id);
ALTER TABLE mutations ADD COLUMN transform_id TEXT REFERENCES transforms(id);
CREATE UNIQUE INDEX tracks_transform ON tracks(transform_id) WHERE transform_id IS NOT NULL;
CREATE TRIGGER tracks_transform_frozen BEFORE UPDATE OF transform_id ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;
-- A track has a transform id iff its recipe is a dsp recipe; the shape is fixed (D-T1-4).
CREATE TRIGGER tracks_transform_shape BEFORE INSERT ON tracks
WHEN (NEW.transform_id IS NOT NULL) <> COALESCE((SELECT engine = 'dsp' FROM recipes WHERE id = NEW.recipe_id), 0)
  OR (NEW.transform_id IS NOT NULL AND (NEW.id <> NEW.transform_id OR NEW.origin <> 'job' OR NEW.job_id IS NOT NULL
      OR NEW.lossy = 1 OR NEW.import_rule <> 'transform' OR NEW.bits_per_sample <> 24 OR NEW.channels <> 2))
BEGIN SELECT RAISE(ABORT, 'tracks: transform shape'); END;
-- The recipe of a transform: dsp engine, generated (never 'import'), versioned request, no style or lyrics text (T1-F1 verifies recipes.style accepts '').
CREATE TRIGGER recipes_dsp_shape BEFORE INSERT ON recipes
WHEN NEW.engine = 'dsp' AND NOT (NEW.created_by <> 'import' AND NEW.sampling_json IS NULL AND NEW.lyrics = '' AND NEW.cot = '' AND NEW.style = ''
   AND COALESCE(json_extract(NEW.request_json, '$.v') = 1, 0)
   AND COALESCE(json_type(NEW.request_json, '$.kind') = 'text', 0)
   AND COALESCE(json_extract(NEW.request_json, '$.dsp_version') LIKE 'mm-dsp/%', 0))
BEGIN SELECT RAISE(ABORT, 'recipes: dsp shape'); END;
-- A draft's mother can never be a transform take (D-T1-12), like M2c's reference trigger.
CREATE TRIGGER recipe_drafts_dsp_parent_ins BEFORE INSERT ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE transform_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: not renderable'); END;
CREATE TRIGGER recipe_drafts_dsp_parent_upd BEFORE UPDATE OF parent_track_id ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE transform_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: not renderable'); END;
```

2. **Notes (binding).** (1) `transform_inputs_lossless` reads `lossy OR import_rule = 'reference'` as an
   integer (SQLite booleans); a missing track yields 1 and aborts; it does not apply to a `score_source` row (the source Take supplies text, not audio). (2) `mutations_immutable` already covers
   the new `mutations.transform_id`. (3) No trigger on `tracks.origin`: D-T1-4. (4) Backup/restore need no
   change (whole-database snapshot; the `incoming/` work dirs are excluded as M3's are); a restored database's
   `running` rows keep their (expired) lease and are re-queued by `Recover`, C210.3. (5) `PRAGMA
   foreign_key_check` is empty after the migration on the M3 fixture; the three fixtures of earlier schemas
   upgrade (`TestUpgrade_T1`).
3. **Write SQL** (all in the caller's transaction; times `store.FormatTime`): `InsertTransform` (state
   `queued`, attempts 0), `InsertTransformInputs`, `ClaimNextTransform` (`UPDATE … SET state='running',
   attempts=attempts+1, started_at=?, phase='preparing', owner=?, lease_expires_at=? WHERE id=(SELECT id FROM transforms WHERE
   state='queued' ORDER BY created_at, id LIMIT 1) AND (SELECT COUNT(*) FROM transforms WHERE
   state='running') < ? RETURNING …`; the row lease replaces the runner-lease predicate, D-T1-17), `RequeueTransform`, `RenewTransformLease`, `RecoverTransforms`, `FinishTransform` (done /
   failed / cancelled with the column set each state requires), `SetProgress` (throttled by the caller),
   `DailyCPUUsedMS(principalKind, since)` = `SUM(COALESCE(cpu_ms, max_cpu_ms))` over rows of that **kind** (not principal: one shared pool, D-T1-10) with
   `state IN ('queued','running')` or `finished_at >= since`; because every terminal row has `cpu_ms` (CHECK), only live rows reserve `max_cpu_ms`. `FinishTransform` always sets `cpu_ms` (0 when `started_at IS NULL`), and the cancel-queued path of `cancel_transform` does too (`TestQuota_CancelledQueuedReleases`).
4. **`library.InsertTransformRecipe(ctx, tx, SpecRecipe{Kind, Canonical []byte, Hash, RequestID,
   Seed int64}, createdBy) (id string, created bool, err error)`** — `INSERT … ON CONFLICT(hash) DO NOTHING
   RETURNING id` (`Hash` = M1's `recipes.hash` = sha256 of `Canonical`, which includes `id`), else `SELECT id`; `style = ''`; **never** `ErrRecipeExists` (idempotent by design, D-T1-2);
   `core_hash = hash`; `engine='dsp'`; no `recipe_keys` row (a transform has no content/render split;
   `lineage.Keys` is YuE2-shaped) — `TestBackfillRecipeKeys_SkipsDsp`.

### C202. The transform spec (the recipe) and its parameters

1. **Canonical spec** = `canonjson` of this object, `recipes.request_json` (key order and number
   formatting are `canonjson`'s, pinned by `TestSpec_Canonical_Golden`):

```json
{"id":"dsp-<kind>-<spec_hash20>","seed":0,"v":1,"kind":"glacier","dsp_version":"mm-dsp/1","sample_rate":48000,
 "inputs":[{"role":"source","track_id":"<uuid>","master_sha256":"<64 hex>"}],
 "params":{ "…resolved per-kind parameters, every default written out…": 0 },
 "alignment":{ "…morph/polytempo/shepard only: per input {bpm, bpm_source, octave_fold, key, key_source}…": 0 },
 "target_lufs":-16.0,
 "synth":{"name":"orchestra","soundfont_sha256":"<64 hex>","engine":"fluidsynth"},
 "score":{"sha256":"<64 hex of the exact score.abc bytes>","bytes":1234,"source_track_id":"<uuid or null>","dialect":"standard"}}
```
   **Two hashes, defined once (revision 2).** `spec_hash` = sha256 of the canonical object **without** `id`; `id` = `"dsp-" + kind + "-" + first 20 hex of spec_hash`;
   `transforms.spec_hash` = `spec_hash` (reuse, D-T1-2, and the estimate use it). `recipes.hash` = sha256 of the exact `request_json` bytes, which **include** `id`
   (M1's rule), `core_hash = recipes.hash`; the two differ and neither is derived from the other at read time. `TestSpec_IDFromHash` pins a golden for **both** hashes
   and the id per kind. For **(T1b) `score_render`** the spec embeds `score.sha256` (the exact `score.abc` bytes rendered) and, with `from_track_id`, the
   source Take id; that Take is also written as a `transform_inputs` row of role `score_source` (annotation: lineage `also_from`, never a parent), so the source is visible in
   lineage and the render is reproducible from the recipe plus the stored `score.abc` artifact. Further: ` `seed` = the PCG seed
   (glacier, ensemble, shepard use it; others 0); `synth` and `score` only for `score_render`; every omitted optional
   parameter is **written with its default** so that two requests that differ only by spelling a default
   out hash equally (`TestSpec_DefaultsWrittenOut`). `target_lufs` is the resolved target (D-T1-14).
2. **Go types** (`internal/transform/spec.go`; the API types of C212 mirror them one to one; `*float64`
   means optional on input, always present in the spec):

```go
type Kind string // "morph" | "shepard" | "glacier" | "polytempo" | "ensemble" | "score_render" | "master" | "widen" | "binaural" (rev 4, C218–C225)
type Spec struct {
	ID string; Seed int64; V int; Kind Kind; DSPVersion string; SampleRate int
	Inputs []InputRef; Params json.RawMessage; Alignment *Alignment; TargetLUFS float64; Synth *SynthRef; Score *ScoreRef // Synth/Score: T1b
}
type InputRef struct{ Role, TrackID, MasterSHA256 string }
type Alignment struct{ Inputs []InputAlign } // one per input, in input order
type InputAlign struct {
	BPM float64; BPMSource string // "explicit" | "plan" | "declared" | "measured"
	OctaveFold int // -1, 0, +1: the BPM was halved/doubled to minimise the stretch to the partner (morph, polytempo)
	KeyPC int; KeyMinor bool; KeySource string // "" when unknown or not needed; pitch class 0..11 (C=0)
	DownbeatS *float64; DownbeatAuto bool // explicit override, or the opt-in "auto" heuristic (D-T1-9); one of the two is required unless a plan bar grid exists
	PlanBarS *float64     // scores.seconds_per_bar when a plan grid anchors the downbeat
	BeatsPerBar int
}
```
3. **Per-kind parameters** (JSON snake_case; defaults in the third column are written into the spec;
   ranges are validated in Go with 422 `INVALID_TRANSFORM {field, rule}`; rules named in the last column):

| Kind | Parameter | Default | Range / rule |
|---|---|---|---|
| morph | `track_a_id`, `track_b_id` | required | two different Takes (`same_input`) |
| | `bars` | 8 | 1–32 transition length in bars of the output grid |
| | `a_at` / `b_at` | A: last `bars + 2` bars before its end; B: its first bar | `{bar: int ≥ 0}` or `{section: "chorus#1"}` (`kind#ordinal`; resolves **only** from sections that carry times (`measured`/`manual`); `plan` sections of lyrics have no `start_s`, so `section_unavailable` is the common outcome and the first row of its test) |
| | `bpm_a`, `bpm_b` | resolved (D-T1-9) | 40–240 |
| | `beats_per_bar` | 4 | 2–7 (both inputs) |
| | `downbeat_a_s`, `downbeat_b_s` | **required** unless the input has a plan bar grid | seconds ≥ 0, < input length, or the string `"auto"` (opt in to the heuristic; `downbeat_ambiguous` is reported when unsure) |
| | `key_policy` | `to_a` | `to_a` (B shifted into A's key, relative major/minor treated as the same key) · `none` |
| | `semitones_b` | resolved | −12…+12, 0.01 steps (overrides the resolved shift; the glide to 0 over the transition is fixed, **`key_tail`, `curve` and `b_tail_bars` are cut**: equal-power fade, `ramp`, B plays to its end) |
| shepard | `source_id` | required | |
| | `direction` | `up` | `up` · `down` |
| | `loop_bars` / `loop_s` | `loop_bars` 4 when a grid resolves, else `loop_s` required | 1–8 bars / 1–20 s (`loop_source_required`) |
| | `start_s` | the first chorus start from a timed (`measured`/`manual`) section, else 0 with warning `start_defaulted` | ≥ 0 |
| | `layers` | 5 | 3–7 |
| | `period_s` | 24 | 8–120 seconds per octave of glide |
| | `duration_s` | 120 | `period_s`–600 (≤ `MM_TRANSFORM_MAX_OUT_S`) |
| | `bell_sigma_octaves` | `layers / 5` | 0.4–2.0 |
| glacier | `source_id` | required | |
| | `factor` | 20 | 10–50 (two decimals) |
| | `start_s`, `length_s` | 0, min(30, duration − start) | `length_s × factor ≤ MM_TRANSFORM_MAX_OUT_S` (`too_long`) |
| | `window_s` | 0.5 | 0.1–2 (rounded up to a power of two in samples; stereo is always decorrelated, **`stereo` and `lowpass_hz` are cut**) |
| | `fade_in_s`, `fade_out_s` | 4, 4 | 0–30 |
| polytempo | `track_a_id`, `track_b_id` | required | different |
| | (mode) | `converge` only | B's tempo ramps from `bpm_b` to `bpm_a` so both downbeats coincide at the meeting and then run in unison (**`mode` and the `coincide` variant are cut**) |
| | `bpm_a`, `bpm_b`, `beats_per_bar`, `downbeat_a_s`, `downbeat_b_s` | as morph | |
| | `converge_bar` | the bar of A at 40 % of A's length | ≥ 4 (bars of A before the meeting) |
| | `unison_bars` | 8 | 0–64 (0 ends at the meeting) |
| | `pan` | `split` | `split` (A left-weighted, B right-weighted, ±35 %) · `centre` |
| ensemble | `source_id` | required | |
| | voices | fixed preset `choir_octaves`: semitones [−12, −7, 0, +5, +12], formant ratios [0.85, 0.92, 1.0, 1.08, 1.18], delays 0/12/23/31/42 ms, gains −3/−4/0/−5/−6 dB, pans −0.6/−0.3/0/0.3/0.6 | **not a parameter** (free voices 2–8 are cut) |
| | `space` | `cathedral` | preset `cathedral` (rt60 9 s) · `cistern` (18 s, dark) · `hangar` (5 s, bright) · `canyon` (12 s, sparse); **`custom` is cut** |
| | `wet_db`, `dry_db` | −6, −12 (`dry_db: null` ⇒ −∞) | −40…0 |
| | `tail_s` | 20 | 0–40 (cut where the tail falls below −70 dB, never longer) |
| score_render **(T1b)** | `score` | required | exactly one of `abc` (text) or `from_track_id` (that Take's `score.abc`) — ≤ 256 KiB (`score_too_large`); **`midi_base64` is cut** |
| | `dialect` | by source (D-T1-15) | `standard` · `yue2` |
| | `soundfont` | the first configured name | a configured name (`soundfont_unknown`) |
| | programs | `%%MIDI program n` in the score, else program 0 | **not a parameter** (the voice-name heuristic map is cut) |
| | `bpm` | the score's `Q:` (default 120) | 20–300 |
| | `tail_s` | 3 | 0–10 (rendered by padding the normalised SMF with an End-of-Track at `last_tick + tail_s`, C208.5; **never** by padding PCM) |
| | reverb | always off (`-R 0`) | **not a parameter** (`hall` is cut) |
| | `gain` | 0.6 | 0.1–1.5 (the synth master gain; the safety stage sets the final level) |
4. **Titles.** Default `title` = `<primary parent title> — <label>` cut to 200 runes at a rune boundary
   (`—` U+2014): morph `→ <B title>` + ` morph`, `glacier ×20`, `shepard ↑`, `polytempo 90/120`, `ensemble
   cathedral`; `score_render`: the ABC `T:` line, else `Score render`. An explicit `title` wins.

### C203. DSP core (`internal/dsp`, `dsp/pcm`, `dsp/pv`)

1. **Buffer** `pcm.Buffer{Rate int; Ch [2][]float64}` (mono inputs are duplicated **in Go**, D-T1-19; planar). `pcm.ReadF32LE(r,
   frames)` and `pcm.WritePCM24(w, b, seed)` (round-to-nearest, clamp ±(2²³−1), interleaved little-endian,
   no dither — the noise floor is already −144 dBFS) return the running **`pcm_sha256`**. **(T1b)** `pcm.ReadWAV24` accepts `WAVE_FORMAT_PCM` and `WAVE_FORMAT_EXTENSIBLE` (libsndfile may write either).
2. **FFT.** `dsp.FFT`/`IFFT` are our own iterative radix-2 (power of two) with a **per-size twiddle table precomputed from `math.Sin`/`math.Cos` of each index** (no recurrence: its error grows with size, and the impulse-response build runs a whole-noise FFT at 2²¹); real-input helper `RFFT(x,
   n)`/`IRFFT`; `NextPow2`. `TestFFT_RoundTrip` (max error < 1e-12 at n = 4096 **and relative error < 1e-11 at n = 2²¹**), `TestFFT_Parseval`, `TestFFT_MatchesAnalysisFFT` (agrees with `analysis.FFT` within 1e-9 at 8192, so the old implementation stays a cross-check, not a dependency).
2a. **Deterministic math** (`dsp/mathx.go`; D-T1-3): `Exp`, `Log`, `Pow` (and `Pow` with integer exponent by repeated multiplication) in pure Go with fixed polynomials and `float64(a*b)` written products. `TestExp_Accuracy`/`TestLog_Accuracy`/`TestPow_Accuracy` (≤ 2 ulp against `math` on 10 000 fixed inputs, plus edge cases 0, ±large, denormals), `TestMath_Golden` (bit patterns of 64 inputs committed; identical on any host), `TestDSP_BannedMath` and `TestDSP_RandOnlyPCG` (go/ast scans of `dsp/...`: no `math.Exp|Exp2|Expm1|Log|Log2|Log10|Log1p|Pow|Sinh|Cosh|Tanh|Cbrt|FMA`; `math/rand` imported only by `dsp/rng.go`, which calls only `NewPCG` and `Uint64`). `dsp.Rand.Float64()` = `float64(u>>11) * 0x1p-53`, `Norm` by our Box–Muller on `dsp.Log`/`Sqrt`/`Cos`.
3. **Resampler** `dsp.Resample(x []float64, ratio float64, zeros int)` — windowed sinc (Kaiser β = 8.6, 32
   zero crossings, cutoff `min(1, 1/ratio) × 0.97`), exact output length `round(len × ratio)`, table-driven
   (1024 sub-sample steps, linear interpolation between them; the Kaiser window uses a series Bessel I0 on `+` and `*` only). **Consumer (revision 2):** a second
   input whose rate differs from the first input's (C210.6). `ResampleVar` is cut (no consumer). `TestResample_Sine` (440 Hz at 16 k → 22.05 k, peak at 440 ± 1 Hz, alias
   products < −80 dB), `TestResample_Length`, `TestResample_MixedRateInput` (a 44.1 kHz second input of a morph lands on the first input's 48 kHz grid; the pipeline never calls an ffmpeg resampler).
4. **Phase vocoder** (`dsp/pv`): STFT with Hann analysis/synthesis windows, `N = 4096` (tests: 1024 at
   16 kHz), synthesis hop `Hs = N/4`, window-sum normalisation, **identity phase locking** (Laroche–Dolson:
   spectral peaks found as local maxima over ±2 bins, regions of influence split at the amplitude minima,
   non-peak bins inherit their peak's phase rotation).
   - `Stretch(x []float64, alpha func(outFrame int) float64, p Params) []float64` — analysis hop
     `Ha = Hs/α` varies per synthesis frame (α > 1 slows down); the analysis position is the running sum so
     the mapping is monotone and **exact**: `StretchLen(n, alpha)` is the closed-form output length and
     `TestStretch_Length` asserts equality, not approximation.
   - `Shift(x []float64, rho func(outFrame int) float64, p Params) []float64` — pitch change by peak
     remapping: each analysis frame's peaks move to bin `round(k × ρ)` with the phase advanced by the shifted
     frequency; time position unchanged (output length = input length); `ρ` varies per frame.
   - `FormantWarp(frame, phi)` (inside `Shift`, enabled by `Params.FormantRatio`): the magnitude envelope is
     the cepstrally smoothed log spectrum (lifter at `quefrency = rate/1000` samples), the fine structure is
     divided by it and multiplied by the envelope resampled at `k/φ` (linear in log-frequency), so φ moves
     the formants independently of ρ. `Params.FormantRatio = 0` means "follow ρ" (no correction).
   - `Params{N, Hs int; Lock bool; FormantRatio func(outFrame int) float64; Meter *Meter}`.
5. **Defined behaviours, tested** (16 kHz test rate, 2 s signals): `TestStretch_Sine` (440 Hz, α = 1.5 ⇒
   length exactly `round(1.5·n)` ± 0 samples by `StretchLen`, spectral peak 440 ± 2 Hz, amplitude within
   1 dB of the source's steady state), `TestStretch_Identity` (α = 1 ⇒ `max|y − x| / rms(x) < 0.02` after the
   first window), `TestStretch_Clicks` (click train at 2 Hz stretched by 1.25 ⇒ click spacing 0.625 s ±
   5 ms), `TestShift_Sine` (ρ = 2^(3/12) ⇒ peak 523.25 ± 2 Hz from 440 Hz), `TestShift_RampSine` (440 Hz,
   `ρ(t) = 2^(t/P)`, P = 1.5 s ⇒ the peak frequency at 25/50/75 % of the run is `440·2^(t/P)` within 20
   cents), `TestShift_Formant` (synthetic vowel f0 = 200 Hz, formants 700/1200 Hz, ρ = 0.5, φ = 1 ⇒ the
   spectral-envelope maximum stays at 700 Hz ± 8 %; with φ = 0 it moves to 350 Hz ± 8 %).
6. **Work meter** `dsp.Meter{Max int64; …}`: `Add(units int64) error` returns `ErrBudget` once the total
   exceeds `Max` (checked at frame/block granularity, ≤ 1 ms of work between checks) and polls the process
   context every 4096 units; every operator and primitive takes a `*Meter` (nil = unmetered in tests).
   One unit is one float64 multiply-add equivalent; the table of units per primitive is `dsp.Units`
   (FFT of n = `5·n·log2 n`; PV frame = `3·FFT(N) + 40·N`; resample sample = `2·zeros`; convolution block =
   C206).

### C204. Alignment (`internal/dsp/align`, resolution in `internal/transform/resolve.go`)

1. **Grid** `align.Grid{BPM float64; FirstDownbeatS float64; BeatsPerBar int}`; `BarS = BeatsPerBar × 60 / BPM`.
2. **Resolution ladder** (service, D-T1-9), per input and per quantity, first hit wins; the spec records the
   value and its source:
   - tempo (D-T1-9, revision 3): explicit `bpm_*` → `scores.bpm` when `scores.aligned = 1` (`plan`) → `take_features.bpm_requested_x100` (`declared`) → `take_features.bpm_measured_x100` (`measured`; the analyzer's own gate `tempoMinConf = 2.0` already applies, no extra threshold). For a
     **measured** value whose `take_features.bpm_octave` is set, the octave reading nearest to the partner's tempo wins (`OctaveFold` −1/0/+1, `align.Fold(partner, measured)`); explicit/plan/declared values are trusted and never folded. A Take without a feature row reads `tracks.bpm_declared` and the `analyses` tempo row directly (`features.SelectAnalysis`). Nothing found ⇒ 422 `ALIGNMENT_UNRESOLVED {input, quantity: "bpm", pass: "bpm_a"}`.
   - **downbeat anchor** (morph, polytempo; shepard when a grid is wanted): explicit `downbeat_*_s` → a **plan bar grid** (`scores.aligned = 1` with
     `scores.seconds_per_bar`; the first bar of the audio is bar 1 and the downbeat is the first beat-phase beat of that bar, recorded
     `downbeat_source: "plan"`, warning `downbeat_from_plan_assumed`) → **none**: 422 `ALIGNMENT_UNRESOLVED {quantity: "downbeat", pass: "downbeat_a_s"}`, unless the caller passed
     `"auto"` (heuristic, C204.3, `downbeat_source: "auto"`, estimate warning `downbeat_auto`). Nothing is ever silently guessed.
   - **sections:** `{section: "chorus#1"}` and shepard's default `start_s` resolve only from `sections` rows that carry times (`source IN ('measured','manual')`); on main a
     `plan` section has `start_s IS NULL` (ABC sections have no times), so it resolves only when it carries a bar index **and** the input has a plan bar grid (time =
     `bar × seconds_per_bar`); otherwise `INVALID_TRANSFORM {rule: "section_unavailable"}` (shepard's default silently falls back to 0 with warning `start_defaulted`).
     `section_unavailable` is the common case and is the first row of `TestResolve_Sections`.
   - key (morph with `key_policy: to_a` and no `semitones_b` only): explicit `semitones_b` skips the ladder; else `take_features.key_num/key_letter` (basis `measured`, `declared` or `plan` as L3 chose it, recorded as `key_source`; converted to pitch class and mode by the 24-row inverse of `features.CamelotOf`, `TestCamelot_Inverse`); no row ⇒ `features.ParseABCKey(score.abc)` (`plan`) → none. Unknown for either input ⇒ **not an error**:
     the shift is 0 and the estimate warns `key_unknown_assumed_none`.
3. **Local beat phase and local tempo** (audio-derived, inside the render, deterministic; revision 2). The global-phase estimate of revision 1 is **replaced**: a
   declared BPM is an integer and a measured one is rounded, so a 0.3–0.5 % error accumulates 0.5 s or more over 2–3 minutes and bars land several beats off at tA or tB.
   `align.LocalGrid(x, rate, bpm0, bpb, centreS, windowBars=8) (Grid, LocalInfo)`:
   - front end = the onset-strength envelope `analysis.OnsetEnvelope(mono float32, rate)` **as shipped** (window 1024, hop 128 samples at the working rate: 375 frames/s at 48 kHz, 125 at 16 kHz; D-T1-20; pinned by `TestOnset_PinnedForT1`), restricted to a window of `windowBars` bars centred on `centreS` (tA for A, tB for B; the window is clamped inside the input);
   - **local tempo** `T` maximises the comb sum over a grid `bpm0 × (1 ± 0.03)` in 0.05 % steps, **local phase** `φ` over a quarter-frame grid inside the window; the
     returned `Grid.BPM` is the refined tempo, `Grid.FirstDownbeatS` the downbeat nearest `centreS` on the local grid; if the refined tempo differs from the spec's
     BPM by more than 2 % the refinement is rejected (`tempo_refine_rejected`, spec BPM used) — the morph/polytempo ramp then uses the local tempo as its `T0`/`T1`
     (recorded in the report as `local_bpm`; the spec's `bpm` stays the declared/measured value);
   - `Confidence = S(φ*,T*) / mean S` (< 1.5 ⇒ `beat_phase_weak`, the render proceeds);
   - **downbeat** (only when no explicit `downbeat_*_s` and no plan anchor, i.e. `"auto"`): the beat position `b ∈ [0, bpb)` whose beats carry the largest < 150 Hz onset
     energy; `DownbeatConfidence = (E_best − E_second) / E_best`; **< 0.25 ⇒ warning `downbeat_ambiguous`**. This heuristic cannot work for four-on-the-floor
     (kick on every beat ⇒ ambiguous by construction) and is **wrong by construction for a reggae one-drop** (kick on beat 3 is read as the bar's downbeat): both
     are listed as blind spots, the one-drop is detected only as "kick lands on a single beat position" (`downbeat_one_drop_suspected` when the winning position is
     exactly `bpb/2` and its confidence ≥ 0.6), and the warning is surfaced in the estimate (`downbeat_auto`), `TransformView.warnings`, `transform.json` and the UI.
   - `downbeat_*_s` overrides every step above for the downbeat; phase and tempo are still refined locally around it.
   **Blind spots** (written to the report and the capabilities text): rubato, free-time intros, half/double-time feel, swing, anything without a steady pulse within the window,
   tempo changes inside the window, and the downbeat cases above.
   **Tests:** `TestBeatPhase_Clicks` (T1-D3), `TestBeatPhase_FourOnFloor` (accent on every beat ⇒ `downbeat_ambiguous`, never a silent pick), `TestBeatPhase_OneDrop` (kick on beat 3 only ⇒
   `downbeat_one_drop_suspected`), `TestBeatPhase_Drift_LocalRefine` (a click track 0.4 % away from the declared integer BPM, 150 s long: the grid near a transition at 140 s is within 15 ms
   of the true beats, where a global-phase estimate would be ≥ 0.5 s off), `TestBeatPhase_TempoChangeInWindow` (a step change inside the window ⇒ low confidence, no panic).
4. **Key shift** `align.KeyShift(aPC int, aMinor bool, bPC int, bMinor bool) float64`: `eq(pc, minor) = (pc +
   3·minor) mod 12` (a minor key and its relative major are the same pitch set), `d = (eq(a) − eq(b)) mod 12`,
   folded into `[−6, +5]`. Table `TestKeyShift_Table`: (A min, C maj) → 0; (C, D) → −2; (C, F♯) → −6;
   (F♯, C) → −6 (6 folds to −6); (A min, B min) → −2; (E♭ maj, C min) → 0; (C, B♭) → +2.
5. **Octave fold table** `TestFold_Table` (`Fold(partner, measured)`): (92, 128) → 0; (140, 70) → +1 (⇒ 140);
   (90, 200) → −1 (⇒ 100); (120, 60) → +1; (100, 100) → 0; ties go to 0.
6. **Ramp closed forms** (`align.Ramp{T0, T1, Beats float64}`; local tempo `T(β) = T0·r^(β/B)`, `r = T1/T0`,
   `B = Beats`): `Time(β) = 60·B·(1 − r^(−β/B)) / (T0·ln r)` (`r = 1` ⇒ `60β/T0`), `BeatAt(τ)` its inverse,
   `Alpha(τ) = r^(−BeatAt(τ)/B)` (the stretch of the track that starts at `T0`), `Total() = Time(B)`.
   `TestRamp_Values`: `T0 = 92, T1 = 128, Beats = 32` ⇒ `Total() = 17.77 ± 0.01 s`, `BeatAt(Total()) = 32 ±
   1e-9`, `Alpha(0) = 1`, `Alpha(Total()) = 92/128 = 0.71875 ± 1e-9`; `TestRamp_Roundtrip` (`BeatAt(Time(β)) =
   β` for 100 β, error < 1e-9); `r = 1` degenerates without a division by zero.

### C205. Operators (`internal/dsp/ops`)

Common signature, every kind (`TestOps_Signature` calls all six through it):

```go
type Job struct {
	Kind   string; Rate int; Seed int64
	Params json.RawMessage // the resolved, default-complete params of the spec
	Inputs []Input         // decoded stereo float64 at Rate; Grid is nil until BeatPhase ran
	TargetLUFS, RefLUFS float64; Meter *dsp.Meter
	Progress func(permille int, phase string) // throttled by the caller
}
type Input struct{ Role string; Buf pcm.Buffer; BPM float64; BeatsPerBar int; KeyShiftS float64; DownbeatS *float64 }
type Report struct{ /* C210.5 */ }
func Run(ctx context.Context, j Job) (pcm.Buffer, Report, error)
```

1. **morph** (timeline in output time; `bpb` beats per bar; `T0 = bpm_a`, `T1 = bpm_b`, `B = bars·bpb`,
   `ramp = Ramp{T0, T1, B}`; `tA` = A's transition start = `downbeatA + a_bar·BarA` (a `section` is converted
   to the bar nearest its start second); `tB` = B's entry = `downbeatB + b_bar·BarB`):
   1. *Head:* `A[0, tA)` verbatim (sample count `round(tA·R)`).
   2. *Transition* of exactly `ramp.Total()` seconds: A is read from `tA` with `pv.Stretch(α_A(τ) = ramp.Alpha(τ))`
      (A starts at its tempo and slows or speeds to `T1`); B is read from `tB − N` (lead-in) with `α_B(τ) =
      T1 / T(BeatAt(τ)) = r^(1 − BeatAt(τ)/B)` (B starts at `T0` and relaxes to its own tempo, `α_B(Total) = 1`);
      B's pitch is moved by `s = semitones_b` through `pv.Shift` with `ρ(τ) = dsp.Exp(ln2 · s·(1 − u)/12)` (the glide to 0 is fixed;
      `u = BeatAt(τ)/B`); gains `gA = cos(πu/2)`, `gB = sin(πu/2)` (equal power only). `T0`, `T1` are the **local** tempos of C204.3.
   3. *Tail:* B continues from `tB + B·60/T1` at natural tempo and natural pitch to its end.
   4. Joins (head→transition, transition→tail) use 10 ms equal-power crossfades against the verbatim signals; the
      transition's lead-in/lead-out context is `N` samples.
   `OutSeconds = tA + ramp.Total() + tail` exactly (`TestMorph_Length`); a transition that would run past A's end,
   or a tail past B's, is `INVALID_TRANSFORM {rule: "a_too_short" | "b_too_short"}` from the estimate. The 'estimate knows A and B's length' comes from the track rows (no audio read).
2. **polytempo** (converge only): A plays unstretched from output 0; `Tc = downbeatA + converge_bar·BarA`;
   B's tempo ramps over `[o, Tc]` log-linearly in *time* from `T_B` to `T_A` (`L = (T_A − T_B)/ln(T_A/T_B)`, `L = T`
   when equal); B's first downbeat sits `dB = downbeatB·T_B/60` beats into B; the entry offset is
   `o = Tc − 60·(dB + m·bpb)/L` with `m` the **largest integer ≥ 0 that keeps `o ≥ 0`** (B enters as early as it
   can; `m` does not exist ⇒ `INVALID_TRANSFORM {rule: "converge_too_early"}`); `0 ≤ o < 60·bpb/L`. After `Tc` both
   run at `T_A` (B stretched by the constant `T_B/T_A`) for `unison_bars` bars of A; output ends at
   `Tc + unison_bars·BarA`. `T_A`, `T_B`, `downbeatA/B` are the **local** values of C204.3 near `Tc`. Mix: each side −3 dB, constant-power pan (`split`: A −0.35, B +0.35).
3. **glacier** (Paulstretch family): `N = NextPow2(round(window_s·R))`, sine window `w[n] = sin(π(n+½)/N)` at
   analysis and synthesis (`w²` sums to 1 at hop `N/2`), output hop `Hs = N/2`, frames `F = ⌈outLen/Hs⌉ + 1`,
   `outLen = round(length_s·R·factor)` (**exact**: `GlacierLen`), the analysis window start advances evenly from
   `start − N/2` to `start + length − N/2` (excerpt zero-padded at both ends), so the whole excerpt is scanned
   once whatever the factor; per frame and channel: window → real FFT → keep magnitudes, replace phases by
   `U[0, 2π)` from `PCG(seed, 2·seed' + ch)` via `dsp.Rand.Float64` → IFFT → window → overlap-add; fades `fade_in_s/fade_out_s` (raised
   cosine); streaming: memory is O(N), output is written as it is produced. `TestGlacier_Len` (2.0 s at 16 kHz,
   factor 10 ⇒ 320 000 samples), `TestGlacier_Sine` (440 Hz ⇒ peak 440 ± 3 Hz, RMS within 1 dB of the source's),
   `TestGlacier_SeedChangesAudio`, `TestGlacier_Decorrelated` (|ρ(L,R)| < 0.2 on noise input).
4. **shepard**: the loop is `x[start − 30 ms, start + L)` with its head equal-power-faded into its tail (a seamless
   cycle of length `L`); layers `j = 0…layers−1`; at output time `τ` the layer's pitch offset in octaves is
   `o_j(τ) = ((τ/P + j) mod layers) − layers/2` (`up`; `down`: `layers/2 − ((τ/P + j) mod layers)`); weight
   `w(o) = dsp.Exp(−½(o/σ)²) · edge(o)` with `edge` a raised cosine that reaches **0 at |o| = layers/2** (a layer that wraps from
   top to bottom is silent when it does); all layers read the loop at natural speed (time unchanged) through one
   `pv.ShiftBank` (one analysis FFT per frame, one synthesis per layer); output length `round(duration_s·R)` exactly.
   `TestShepard_PitchTrack` (220 Hz sine loop of 1 s, `layers 3`, `period_s 2`, `σ = 0.6`: at τ = 0 two equal peaks
   at 155.6 and 311.1 Hz (±3 Hz, within 1.5 dB), at τ = 1 s the dominant peak at 220 Hz ± 3, at τ = 2 s the same two
   peaks as at 0), `TestShepard_EnvelopeFlat` (RMS in 0.5 s windows within ±1.5 dB over the whole output),
   `TestShepard_Down_Mirrors` (down at τ equals up's peak set at −τ). **Capabilities text (required sentence):** "a shepard of five layers of a full mix is expected to sound muddy; use a pad-heavy, drum-free loop" (`TestCapabilities_ShepardMuddyNote`).
5. **ensemble**: `voices` run through one `pv.ShiftBank` (ρ_i = 2^(semitones_i/12), formant φ_i); each voice is delayed
   `round(delay_ms·R/1000)` samples, gained, constant-power panned, summed into a stereo stack `S`; wet = `S_L * h_L`,
   `S_R * h_R` (C206, true decorrelated tails); output = `dry·S + wet·wetGain`,
   length `len(x) + IR.EffectiveLen` (cut at −70 dB, ≤ `tail_s`, ≤ the output cap). **Limitation, stated in the UI and in `get_transform_capabilities`:** a Take is a full mix; the stack is
   a stack of the *whole* band (the `band_hz` split is cut). A vocal stem Take would make it musical, but **stems are not a T1 dependency** (ACE-Step `extract`/`lego`/`complete`
   exist only on the base DiT models, not the turbo/sft ones; E2/M5 may never produce one) — the demo may simply use an a-cappella-like intro Take.
6. Progress: every operator reports `permille` from frames done over frames total; `phase ∈ analyzing` (beat phase),
   `rendering`, `finishing` (safety).
7. All operators call `Meter.Add` per frame/block, return `ErrBudget` or the context error promptly, never
   spawn goroutines, never read the clock; `TestOps_NoGoroutinesNoClock` (go/ast scan of `dsp/...` for `go ` and
   `time.Now`), joined by `TestDSP_BannedMath` and `TestDSP_RandOnlyPCG` (C203.2a). Operators use `dsp.Exp/Log/Pow` only.

### C206. Generated impulse responses and convolution (`internal/dsp/ir`, `dsp/conv.go`)

1. `ir.Build(p ir.Params, rate int, seed int64, m *dsp.Meter) (L, R []float64, info Info)`; `Params{RT60 float64;
   PredelayMS, Damping, SizeM, Width float64}`; presets `cathedral{9 s, 40 ms, 0.45, 60, 0.9}`, `cistern{18, 15, 0.8,
   30, 0.6}`, `hangar{5, 25, 0.15, 120, 0.7}`, `canyon{12, 120, 0.3, 250, 1.0}` (`Preset(name)`).
2. **Late tail:** two independent `PCG` noise streams (one per channel), split into 7 octave bands by FFT masking of the
   whole noise (band edges 125, 250, 500, 1k, 2k, 4k Hz; zero-phase), each band multiplied by `dsp.Exp(−6.9078·t/RT60_b)`
   with `RT60_b = clamp(RT60·dsp.Pow(f_b/500, −0.4·damping), 0.1·RT60, 1.5·RT60)` (`f_b` the band's geometric centre), summed;
   rising 30 ms raised-cosine onset after the pre-delay.
3. **Early reflections:** image sources of a shoebox of dimensions `(size, 0.7·size, 0.4·size)` metres, source at
   (0.3, 0.4, 0.3) and the two receivers at (0.7 ± 0.05·Width, 0.6, 0.3) in room fractions, order ≤ 2 (up to 25
   images), speed of sound 343 m/s, amplitude `dsp.Pow(0.8, order) / r`, fractional delay by a 16-tap windowed sinc, faded out by a
   raised cosine at 80 ms where the late tail has taken over.
4. Both channels are normalised to unit energy (`Σh² = 1`); `Info{EffectiveLen int; RT60Measured float64}` where the
   effective length is the first sample after which the backward-integrated energy stays below −70 dB.
5. **Convolution:** `dsp.ConvolveUP(x, h []float64, block int, m *dsp.Meter) []float64` — uniform-partitioned overlap-save,
   block 4096, FFT 8192, frequency-domain delay line; output length `len(x) + len(h) − 1`; units per block `partitions ×
   8192·4 + 2·FFT(8192)`.
6. **Tests:** `TestConvolveUP_MatchesDirect` (random 1 000-sample `x`, 300-tap `h`, block 64: `max|diff| < 1e-9`; also
   a 5 000-sample `h` spanning many partitions), `TestIR_RT60` (cathedral: Schroeder backward integration, T20
   extrapolated to 60 dB within ± 10 % of 9 s; hangar 5 s ± 10 %), `TestIR_Deterministic` (same seed ⇒ identical bytes, and the cathedral IR at 16 kHz hashes to a committed golden on any host (`TestIR_Golden`);
   other seed ⇒ differing but with RT60 within 5 %), `TestIR_Damping` (damping 0.8: the 4 kHz band's decay time is
   ≤ 55 % of the 250 Hz band's), `TestIR_UnitEnergy` (1 ± 1e-9), `TestIR_EffectiveLen` (cathedral at 48 kHz ≤
   `9·1.3·48 000` samples), `TestIR_StereoDecorrelated` (|ρ| of the tails < 0.1 at width 1).

### C207. Loudness safety (`internal/dsp/safety`)

1. `safety.LUFS(b pcm.Buffer) (integrated float64, ok bool)` — ITU-R BS.1770-4: K-weighting (shelf + high-pass biquads
   computed from the rate by the standard bilinear-transform formulas), channel gains 1.0, 400 ms blocks with 100 ms
   step, absolute gate −70 LUFS, relative gate −10 LU. `ok = false` when no block survives the gate.
2. `safety.TruePeakDBTP(b)` — 4× oversampling with a 48-tap Kaiser-windowed-sinc polyphase filter.
3. `safety.Limit(b *pcm.Buffer, ceilingDBTP float64, m *dsp.Meter) LimiterInfo` — lookahead 5 ms, attack by a smoothed
   minimum (raised-cosine ramp over the lookahead), release 80 ms exponential, gain computed from the 4×-oversampled peak
   of both channels linked (no image shift), delay-compensated; `LimiterInfo{MaxReductionDB, EngagedFraction float64}`.
4. `safety.Finish(b *pcm.Buffer, in FinishIn) (FinishReport, error)` — the D-T1-14 pipeline in order: finite check →
   silence check (peak < −70 dBFS ⇒ `ErrSilent`) → DC → 10 ms fades (skipped when `in.OwnFades`) → **second pass:** gain to
   `in.TargetLUFS` (`in.RefLUFS` is only echoed in the report), `Limit` on a copy, measure the shortfall; while it exceeds `MaxLimitingLU` (6) lower the target by the excess
   (bisection, ≤ 8 iterations) → `FinishReport.TargetLUFSEffective` → final `Limit` on the real buffer → final `TruePeakDBTP ≤ −1.0 + 0.05` assertion
   (re-limit once if violated, error if still violated). Only `ErrNonFinite` and `ErrSilent` fail a render (D-T1-14). A buffer for which `LUFS` is not `ok` ⇒ `ErrSilent`.
5. **Tests** (oracle revised after measuring with ffmpeg `ebur128` 9.0.1: a stereo signal with the **same** sine on both channels reads its per-channel peak dBFS as LUFS; only a **one-channel** signal is 3.01 LU lower; EBU Tech 3341 style):
   `TestLUFS_Sine` (stereo 997 Hz at −20 dBFS peak on **both** channels ⇒ **−20.0 ± 0.05 LUFS**; at −3.01 dBFS on both ⇒ **−3.01 ± 0.05**; the same sine at −20 dBFS on **one** channel only, the other silent ⇒ **−23.0 ± 0.05** (−23.01)),
   `TestLUFS_GatesSilence` (10 s of the both-channels −20 dBFS sine followed by 10 s of zeros ⇒ **−20.0 ± 0.1** — the absolute gate removes the silent blocks; the boundary blocks are partially filled — re-derived, not −23.01),
   `TestLUFS_MatchesFFmpeg` (go-media: noise burst + sine mix, ours vs `ffmpeg ebur128` integrated within 0.1 LU; T1-D1 first records the ffmpeg values of the three sine cases above in `testdata/lufs-oracles.txt` and the unit test asserts those),
   `TestLimit_Ceiling` (white noise at +12 dBFS peak ⇒ `TruePeakDBTP ≤ −0.95`; a sample-peak-safe but inter-sample-hot
   pattern (alternating ±0.9 at fs/4, phase-shifted by 45°) measured ≤ −0.95 after, > −0.9 before),
   `TestLimit_Transparent` (a −20 dBFS sine passes unchanged: `max|y − x| < 1e-6`, `EngagedFraction = 0`),
   `TestFinish_Targets` (a −40 LUFS pink-ish noise buffer with `TargetLUFS −16` ⇒ output −16.0 ± 0.3 LUFS, `target_lufs_effective = −16`,
   limiter inactive), `TestFinish_LowersTarget` (a crest-factor-30 dB click/noise buffer with `TargetLUFS −16` ⇒ **no error**, `target_lufs_effective < −16`, shortfall `≤ 6 LU`, report says limited and records requested and effective targets),
   `TestFinish_NonFinite` (a NaN sample ⇒ `ErrNonFinite`), `TestFinish_Silent` (all zeros and a −90 dBFS sine ⇒ `ErrSilent`),
   `TestFinish_DCRemoved` (a +0.01 DC offset sine ⇒ |mean| < 1e-6), `TestFinish_Deterministic`.

### C208. (T1b) Score toolchain (`internal/score`, `score/abc`, `score/smf`, `score/synth`) — final stage, gated by `MM_SOUNDFONTS`, cannot block the T1a merge

1. **Event model** (`score`): `Score{PPQ int (= 480); Tempos []Tempo{Tick int64; USPerQ uint32}; Meters []Meter{Tick;
   Num, Den}; Voices []Voice; Bars int; Warnings []Warning}`, `Voice{ID, Name string; Program, Channel int; Notes
   []Note{Pitch, Vel int; Tick, Dur int64}}`; `Score.SecondsAt(tick)` via the tempo map; `Score.Normalise()` sorts notes by
   `(Tick, Pitch)` and caps (`MaxScoreEvents`, 16 voices, 20 min).
2. **`abc.Parse(src []byte, o abc.Options{Dialect, DefaultBPM float64}) (*score.Score, error)`** returns `*abc.SyntaxError{Line,
   Col int; Rule, Msg string}` (→ 422 `SCORE_INVALID {line, col, rule}`). **Supported:** header fields `X T M L Q K V`
   and `I:`/`%%` lines (only `%%MIDI program n` and `%%MIDI channel n` are interpreted; the rest are ignored); `L:`
   default by the ABC rule (1/8 when M ≥ 3/4, else 1/16); `Q:` as `1/4=92`, `92` (quarter), `"text" 1/4=92`; `K:` with
   mode words (`maj min mix dor phr lyd loc ion aeo`) and explicit accidentals (`K:D exp ^f`); notes with `^ ^^ _ __ =`,
   octave marks `' ,`, lengths `n`, `/`, `n/m`, `/n`; rests `z`, multi-bar `Z[n]`; broken rhythm `>`/`<` (`>`, `>>`,
   `<<`); ties `-`; slurs `( )` ignored; chords `[ceg]2` (length outside multiplies); tuplets `(p`, `(p:q:r`; bar lines `|
   || |] [| |: :| :|: |[1 |[2 [1 [2` with **repeats and first/second endings** expanded in the event stream; inline fields
   `[K:] [M:] [L:] [Q:] [V:]` and their line forms (tempo and meter changes become tempo/meter map entries); `V:` voices
   (`V:1 name="Violin" clef=treble`), one channel per voice (skipping 9); chord symbols `"Am"`, decorations `!…!`/`.`,
   grace groups `{…}` and `w:`/`W:` lyric lines are ignored (each adds a warning once per kind). **Unsupported →
   SyntaxError rules:** `unsupported_macro` (`U:`, `m:`), `unsupported_continuation` (`+:`), `unsupported_overlay` (`&`),
   `unsupported_multi_tune` (a second `X:`), `bad_header`, `bad_note`, `bad_length`, `unterminated_chord`,
   `unterminated_tuplet`, `too_many_voices`, `too_many_events`, `empty_score`. Velocity is constant 80 (no humanisation).
3. **Dialects (D-T1-15):** `standard` — an accidental applies to the same pitch **and octave** until the bar line (ties
   across the bar keep it); `yue2` — it applies to the same pitch class in every octave until the bar line. Key signature
   accidentals apply in both. Table `TestABC_Accidentals`: `K:C` `^F f |` ⇒ standard `[66, 77]`, yue2 `[66, 78]`; next bar
   resets both to `[65, 77]`; `K:D` `F f` ⇒ `[66, 78]` in both; `K:C` `_B B, |` ⇒ standard `[70, 59]`, yue2 `[70, 58]`.
4. **`smf`:** `Write(*score.Score, tailS float64) []byte` (format 1, PPQ 480; track 0 = tempo/meter/name; one track per voice: program change at
   tick 0, notes with **note-off before note-on at equal ticks**, **`End of Track` at `last_tick + round(tailS · ticks/s)` on every track** — this is how the tail is rendered, see item 5; deterministic).
   **There is no production reader** (MIDI upload is cut); `smf_test.go` carries a ~60-line test-side decoder (format 0/1, running status, tempo/meter/program, note-on velocity 0 = off)
   that exists only to verify the writer. `TestSMF_RoundTrip` (a 3-voice score with a tempo change ⇒ identical `Score` after Write→test-decode),
   `TestSMF_Golden` (the bytes of a fixed score, generated at test time and compared byte for byte
   against a committed hex dump `.hex`, not a binary file — the privacy gate blocks `*.mid`), `TestSMF_TailEndOfTrack` (the End-of-Track tick equals `last_tick + tail`), `FuzzParseABC` (no panic, ≤ 2 s,
   allocations bounded by the 256 KiB input; **CI runs it for 20 s**, a longer corpus run weekly).
5. **`synth`:** `type Synth interface{ Render(ctx context.Context, r Request) (Info, error) }`, `Request{SMF, Soundfont, Out string (absolute,
   app-created); Rate int; Gain float64; Reverb bool; Timeout time.Duration}`, `Info{Name, Version string}`. `Fluidsynth` runs through the
   `mediatool` rule (literal flags, absolute app paths, bounded stderr): argv =
   `fluidsynth -q -ni -g <gain> -r <rate> -R <0|1> -C 0 -T wav -O s24 -F <out.wav> <soundfont> <smf>` (`-F -T -O -R -C -g -r -n -i` all exist upstream; **`-q` is required**, otherwise the banner goes to stdout; `-R` is `0` in production, `1` only in the tail test below; re-verified against the pinned
   `fluidsynth --help` in T1-X0; `TestFluidsynth_Argv` is the golden, asserts `-R 0` and `-q`, and the recorded help text lives in `testdata/fluidsynth-help.txt`).
   **Tail (revision 2):** upstream's fast-render loop is `while (player status == PLAYING) process_block` and the renderer is deleted right after — it does **not** wait for release or reverb tails, so a `tail_s` that is only a parameter would silently be 0.
   The tail is therefore produced by the **SMF**: `smf.Write(score, tail_s)` ends every track with an End-of-Track at `last_tick + tail_s` (a sustained no-op), never by padding PCM. `TestSynth_TailRendered` (go-media): with a test SF2 whose
   release is 1 s, RMS in the window `[last_note_end, last_note_end + tail_s)` is above a floor (−60 dBFS) and the output length is `score seconds + tail_s ± 10 ms`; a second case runs `-R 1` and requires a longer, non-silent tail.
   `TestSynth_DryByDefault`: with `-R 0` and an instant-release SF2 the energy after the last note + 50 ms is < −60 dB (no reverb surprise).
   **Process and environment:** the adapter gets its own `mediatool` entry — `mediatool.Tools` gains `Fluidsynth` and `mediatool.Run` gains an opt-in process-group mode (`Setpgid`, group SIGKILL on timeout/cancel), used by fluidsynth and `dsp-run`, existing tools unchanged
   (`TestMediatool_GroupKill`). fluidsynth runs with an **explicit minimal environment** (`PATH`, `LC_ALL=C`, `HOME` = the work dir, nothing inherited) so no user config (`~/.fluidsynth`, `/etc/fluidsynth.conf`) is read; T1-X0 records
   which config files the pinned version reads and `TestSynth_NoUserConfig` (a decoy `HOME`-style file with a hostile setting does not change the render) pins it.
   timeout `max(10 s, 0.5 × expected output seconds)`; `Version` from `fluidsynth --version` (first line, ≤ 80 bytes). **Soundfont set**
   `synth.Set` parsed from `MM_SOUNDFONTS`: absolute regular files only, names validated, streaming sha256 at startup (≤ 2 GiB; a missing or
   unreadable file disables only that name and is logged without the path), `Names()`, `Lookup(name) (path, sha256)`.
6. **Render pipeline** (`transform` bridge for `score_render`): parse → normalise → SMF bytes (with the tail) → write `score.mid` → synth → 24-bit WAV (read
   back through `pcm.ReadWAV24`, which accepts `WAVE_FORMAT_EXTENSIBLE`) → `safety.Finish` (`OwnFades` false) → the common encode/ingest path. **No `scores` row (revision 2):** `InsertTake` receives no `ScoreABC` for a transform, so `scores.source` (`import`|`job`) and the YuE2-shaped bars/meter/Q/aligned/plan-sections handling are never entered for a standard-dialect score; the score text is the `score.abc` artifact and its sha256 is in the spec. (Consequence: a score render has no `scores.aligned` plan grid; the `from_track_id` source keeps its own.) The **score-faithfulness report**
   (`transform.json`) lists per voice the note count and the first/last onset in seconds from the tempo map, so the audio can be compared with
   the score without trusting the synth.
7. **Tests (go-fast unless noted):** `TestABC_Table` (≥ 25 rows, expected MIDI numbers and tick positions: `X:1 M:4/4 L:1/4 Q:1/4=120 K:C
   CDEF|G4|` ⇒ pitches 60, 62, 64, 65 at ticks 0/480/960/1440 and G (67) at 1920 lasting 1 920 ticks (`G4` = 4 quarters = 1 920 ticks),
   total 2.0 s per bar at 120 BPM, 2 bars ⇒ `Bars = 2`; `K:D` `F|` ⇒ 66; `C>D` ⇒ durations 1.5 : 0.5 of the unit; `(3CDE` ⇒ three
   notes of 2/3 unit each (320 ticks at L:1/4); `C-C` ⇒ one note of 2 units; `[CEG]2` ⇒ three notes at the same tick, 2 units; `|:A:|`
   ⇒ A twice; `|:A|1B:|2C|` ⇒ A B A C; `Z2` ⇒ a rest of 2 bars; `[Q:1/4=60]` mid-tune ⇒ a second tempo entry at the right tick; `V:1`/`V:2`
   ⇒ two voices on channels 0 and 1; `%%MIDI program 40` ⇒ program 40), `TestABC_Errors` (each unsupported rule, line/col asserted),
   `TestABC_YuE2Dialect`, `TestParseABC_MatchesParseScoreBars` (on the M1 fixture archive's `score.abc`: `abc.Parse` bars == `yue2.ParseScore`
   bars when the latter times it), `TestSMF_*`, `TestFluidsynth_Argv`, `TestSet_*` (names, absolute paths, hashing, unreadable file).
   **go-media:** `TestSynth_TestSoundfont_Pitch` — the generated SF2 (`dsptest.WriteSF2`: one preset, one looped 440 Hz-tuned sine, root key 69,
   instant envelope) renders a 4-note score (A4 C5 E5 A5 at 120 BPM, quarter notes): each note's detected pitch (FFT peak with parabolic
   interpolation over its middle half) is within 15 cents of 440 / 523.25 / 659.26 / 880 Hz, and each onset (envelope crossing −30 dB re peak)
   is within 25 ms of 0 / 0.5 / 1.0 / 1.5 s; `TestSynth_Deterministic` (two renders ⇒ identical PCM on the same machine and version), `TestSynth_TailRendered`, `TestSynth_DryByDefault`, `TestSynth_NoUserConfig`;
   `TestSynth_Timeout` (a 1 ms timeout kills the process and returns a `TOOL_TIMEOUT`-class error, no zombie).

### C209. Cost model and estimates (`internal/transform/cost.go`)

1. **Units per kind** (one unit = a float64 multiply-add; `FFT(n) = 5·n·log2 n`; `N` the PV window, `F` frames; ch = 2):
   morph: `3·FFT(N)+40·N` per frame per channel for each of the three processed signals of the transition (the verbatim head and tail are copies and cost nothing)
   (A stretch, B stretch, B shift); polytempo the same for B only plus the mix; glacier
   `F·ch·(2·FFT(N)+20·N)`; shepard `F·ch·((1+layers)·FFT(N)+layers·40·N)`; ensemble `F·ch·((1+V)·FFT(N)+V·40·N + 25·N)` plus the IR
   (`7·FFT(2^k)` for `2^k ≥ IRlen`, plus noise `20·IRlen`) plus convolution (C206.5) per channel; score_render has no child units (the
   synth is a tool: `tool_ms`) and `safety` costs `LUFS+Limit ≈ 30·len`.
2. `expected_cpu_ms = ⌈1.3 · units · NSPerUnit/1e6 + decode_ms + encode_ms + ingest_ms + tool_ms⌉` with the **starting values**
   `NSPerUnit = 1.2`, `decode_ms = 3·Σ input_seconds`, `encode_ms = 10·out_seconds`, `ingest_ms = 30·out_seconds` (M1's analysis +
   MP3 rendition), `tool_ms = 40·out_seconds` (score_render only). T1-X0 recalibrates them on the build host and records the table in
   the private notes; `TestCost_Table` pins the formula on three fixed inputs with `NSPerUnit` injected (so the test does not depend on hardware),
   `TestCost_Monotonic` (more bars/layers/factor ⇒ never less), and the **informational** `BenchmarkTransforms` (weekly job) reports measured/estimated
   per kind into `metrics/loop.jsonl` (target ratio 0.5–1.0; > 1.0 means the estimate under-promised and the constants move up).
3. `max_cpu_ms = min(MaxCPUS·1000, 2·expected + 5000)` = the **budget**; the meter's `Max` is `units_budget = ⌊(max_cpu_ms − tool/encode/ingest/decode
   shares) · 1e6 / NSPerUnit⌋`. `expected_cpu_ms > MaxCPUS·1000` ⇒ 422 `TRANSFORM_TOO_LARGE {limit: "cpu_seconds"}`.
4. **Memory** `mem_mb_est = ⌈(Σ in_frames + out_frames·(1+k))·16 B / 2²⁰ + 64⌉` (planar float64 stereo = 16 B per frame) with `k` = morph 3,
   polytempo 3, glacier 0, shepard 1, ensemble 3, score_render 1; `> MaxMemMB` ⇒ 422 `TRANSFORM_TOO_LARGE {limit: "memory_mb"}`; output
   seconds above `MaxOutS` ⇒ `{limit: "output_seconds"}`; inputs above `MaxInputS` ⇒ `{limit: "input_seconds"}`; free disk below `3 × out_bytes_est`
   on `DataDir` (and the work dir) ⇒ `{limit: "disk"}`. `out_bytes_est = 0.65 · 6 · out_frames`.
5. **Estimate result** (C212.3) is deterministic for a database state (`TestEstimate_Deterministic`), writes nothing (`TestEstimate_WritesNothing`:
   row counts of every table unchanged), reads no audio, and always says `cost.micro_usd = 0`.

### C210. Service, runner, render child, ingest bridge (`internal/transform`, `cmd/metamusic/cmd_dsprun.go`)

1. **Service** (`transform.New(Options)`): `Capabilities`, `Estimate`, `Create`, `Get`, `List`, `Cancel`, `Wait`, `Run(ctx)`, `Verify`. Options carry
   `Store`, `Library`, `Lineage`, `Ears`, `Pipeline`, `Tools mediatool.Tools`, `Synth synth.Synth` (T1b), `Soundfonts synth.Set` (T1b), `Data *os.Root`, `DataDir`,
   `Config`, `Clock`, `NewID`, `Log`, `Wake func()`, `Owner string` (runner id, D-T1-17), `Self string` (the binary path, `os.Executable`), `Fault func(point string) error` (tests; points
   `after_decode`, `after_child`, `after_encode`, `after_ingest`). **Create order of checks** (`TestCreate_OrderOfChecks`): (1) disabled/tools missing
   (503; `score_render` without soundfonts/fluidsynth ⇒ 503 `UNAVAILABLE {component: "soundfont"|"fluidsynth"}`) → (2) body shape and ranges (422 `INVALID_TRANSFORM`) → (3) inputs exist (404) and qualify (`LOSSY_MASTER`, `RECIPE_UNKNOWN`) →
   (4) alignment (422 `ALIGNMENT_UNRESOLVED`) and score parse (422 `SCORE_INVALID`) → (5) limits (422 `TRANSFORM_TOO_LARGE`) → (6) reuse by `spec_hash` (live or done: return it, any principal; a done transform whose Take is `discarded` is restored, D-T1-2)
   → (7) queue (429 `TRANSFORM_QUEUE_FULL`, `Retry-After`; one overall limit) → (8) daily quota of the principal's **kind** (429 `TRANSFORM_QUOTA_EXCEEDED`) → (9) one transaction: recipe, transform,
   inputs, idempotency completion, audit row. Then `Wake()`.
2. **Runner** (`Run`; the L2 tape pattern, D-T1-17): one loop on `Wake` or a 5 s tick (clock-injected, synctest-friendly): `Recover`, then `claim` — `ClaimNextTransform` keeps `running < ConcurrentRenders (1)` (FIFO by `created_at, id`), sets `owner`, `lease_expires_at` and `started_at`; one goroutine per job executes steps 1–9 of item 6 below (a `pool` of that size: a job is in it at most once). The lease is renewed every `LeaseTTL/6` while the child runs and **every write of the running row repeats `owner = ?`**; a renew that affects no row means the row is no longer ours: the child is killed and nothing is written. There is no shared runner lease: with generation disabled, or the backend unconfigured, transforms run unchanged.
   **Cancel** is an intent: `cancel_requested_at` is set by the op (queued rows are cancelled by the op itself in its transaction — `cancelled`, `CANCELLED_BY_USER`, `cpu_ms = 0`, nothing ran, the quota reservation is released), the runner kills a running child within `CancelGrace` (SIGKILL on the process group), removes the work dir and
   finishes the row `cancelled`. **Shutdown:** the context ends ⇒ children are killed and the row is left `running`; its lease expires and `Recover` re-queues it (attempt counted), as the tape runner does; the server's 30 s drain covers the kill.
3. **Recover and startup sweep** (before the first claim, and whenever the runner is idle): a `running` row whose `lease_expires_at` has passed ⇒ `queued` (`attempts + 1`, owner cleared) when `attempts < MaxAttempts` else `failed INTERRUPTED`; work dirs
   `incoming/transform-*` without a `running` row, or older than `WorkSweepAge`, are removed; a `takes/<id>` directory whose id has no `tracks` row (a crash between ingest and commit) is removed by a small helper in `transform` that mirrors `media/ingest.Discard` (there is **no** `ingest.DiscardOrphan` on main) before the next attempt; a restored database needs nothing special.
4. **Render child.** The supervisor writes `<work>/job.json`:
   `{"v":1,"kind":"glacier","rate":48000,"seed":7,"params":{…},"inputs":[{"role":"source","file":"in-0.f32","frames":N,"bpm":92,"beats_per_bar":4,"key_shift_s":0,"downbeat_s":null}],"units_max":123456789,"target_lufs":-16.0,"ref_lufs":-15.2,"out":"out.pcm24","report":"report.json"}`
   (file names are fixed literals; the child opens only names inside its work dir through `os.Root`) and starts `metamusic dsp-run --work <abs dir>`: environment
   **explicit and minimal** (`GOMEMLIMIT=<mem_mb_est·1.25>MiB`, `GOMAXPROCS=1`, nothing inherited — no secrets, no config), `Setpgid`, `Pdeathsig=SIGKILL`,
   `setpriority(+10)` after start, stdin closed. It prints progress lines `{"p":412,"phase":"rendering"}` on stdout (≤ 64 KiB total, ≥ 1 s apart enforced by the
   parent's throttle), exits `0` ok / `10` invalid job / `11` budget / `12` non-finite or silent / `13` internal; stderr's last 4 KiB is kept (paths and ids redacted by
   `internal/redact`) for `error_text`. The supervisor enforces: **wall deadline** `max(60 s, WallFactor × max_cpu_ms)` (`TRANSFORM_TIMEOUT`), **RSS guard**
   (`/proc/<pid>/status` VmRSS every `RSSPoll`, kill above `1.5 × mem_mb_est + 128 MiB`: `TRANSFORM_MEMORY_EXCEEDED`; a no-op where `/proc` is absent),
   **cancel**. `cpu_ms` = child user+system time (`ProcessState`) + the wall time of the tool runs (ffmpeg decode, flac/ffmpeg encode, fluidsynth). The child imports only
   `internal/dsp/...` and the standard library (`TestDspRun_Imports`, go/parser over the file and `go list -deps` of its entry package).
5. **`transform.json`** (artifact, redacted, stable keys): `{"v":1,"dsp_version":…,"toolchain":{"go_version":…,"goarch":…,"goamd64":…},"kind":…,"spec_hash":…,"pcm_sha256":…,"frames":N,"rate":R,"cpu_ms":…,"units":…,
   "inputs":[{"role":…,"track_id":…,"master_sha256":…,"resampled_from":null|R0,"beat_phase":{"local_bpm":…,"first_downbeat_s":…,"confidence":…,"downbeat_source":"explicit|plan|auto","downbeat_confidence":…|null,"downbeat_ambiguous":bool}|null}],"safety":{"gain_db":…,"limiter":{"max_reduction_db":…,
   "engaged_fraction":…},"target_lufs_requested":…,"target_lufs_effective":…,"lufs_in":…,"lufs_out":…,"true_peak_dbtp":…},"warnings":[{"code":…,"field":…}],"synth":{"name":…,"version":…,"soundfont_sha256":…}|null,
   "voices":[…]|null,"tool_versions":{"ffmpeg":"…","flac":"…"},"blind_spots":["tempo_drift","rubato",…]}`.
6. **Ingest bridge** (steps 1–9 of one attempt): (1) verify inputs still exist and their `master_sha256` equal the spec's (`INPUT_CHANGED` otherwise; masters
   are frozen, so this is a corruption alarm) and remove an orphan `takes/<id>`; (2) *decode* (D-T1-19): ffmpeg → `in-<n>.f32` at the file's **native rate and channel count** (`-f f32le` only: no `-ar`, no `-ac`; the rate and channel count come from the `tracks` row); mono is duplicated in Go; an input whose rate differs from the first input's is resampled in Go by `dsp.Resample` (recorded as `resampled_from`);
   partial reads with `-ss/-t` are **not** used: whole inputs, capped by `MaxInputS`; (3) child; (4) *encode*:
   `ffmpeg -f s24le -ar R -ac 2 -i out.pcm24 -c:a flac -compression_level 8 -sample_fmt s32 -bits_per_raw_sample 24 out.flac` (argv literal + absolute app paths;
   **verified at T1-X0 on the pinned ffmpeg: `metaflac --show-bps` = 24**, `TestEncode_24Bit`); (5) `Pipeline.Ingest(Request{TrackID: id, Title, Slug,
   RecipeID, RequestID: spec.id, Seed, Source: out.flac, RecordedAt: now, Artifacts: request.json, transform.json[, score.abc, score.mid]})`; (6) **safety
   verification** from the pipeline's measurement, a **bug alarm** against `target_lufs_effective` (D-T1-14.6): failure ⇒ `Discard(res.Created)` and `LOUDNESS_UNSAFE`; (7) one transaction (owner predicate, D-T1-17):
   `InsertTake{Origin: "transform", TransformID, ImportRule: "transform", Mutation{Operator: kind, Params {transform_id, spec_hash, kind}, Rationale}}` (no `ScoreABC`, C208.6), the
   `transform_inputs` rows already exist, `FinishTransform done`; (8) after commit `ears.AnalyzeTrack(ctx, id, AnalyzeOptions{})` (warning on failure) and the
   library's derived-data hooks run as for any new take; (9) remove the work dir. **Error mapping:** ingest `*ingest.Error` ⇒ `INGEST_FAILED:<code>`; child exit 10/11/12/13 ⇒
   `INVALID_TRANSFORM`/`TRANSFORM_BUDGET_EXCEEDED`/`DSP_NONFINITE` or `DSP_SILENT_OUTPUT`/`TRANSFORM_FAILED`; wall deadline ⇒ `TRANSFORM_TIMEOUT`; RSS ⇒
   `TRANSFORM_MEMORY_EXCEEDED`; fluidsynth failure ⇒ `SYNTH_FAILED`; missing soundfont at run time ⇒ `SOUNDFONT_UNKNOWN`; restart ⇒ `INTERRUPTED`; cancel ⇒
   `CANCELLED_BY_USER`. **Identical audio from two different specs:** M1's `tracks_audio_identity` refuses the second Take; the ingest error maps to row code
   **`INGEST_FAILED:DUPLICATE_AUDIO`** (no retry; `error.message` names the existing Take id, never a path), the work dir and `takes/<id>` are cleaned and the row is `failed` with `cpu_ms` recorded
   (`TestBridge_DuplicateAudio`).
7. **Wait/notify:** every row change broadcasts on an in-process hub; `Wait(ctx, p, id, sinceState, timeout)` returns on any difference or terminal state, else after
   `timeout` (0–55 s, default 25) with `timed_out: true` — the semantics of `wait_for_job`.
8. **Authorisation:** every principal with read scope sees every transform (the library has one owner); an agent cancels only its own transforms (403 `FORBIDDEN`
   otherwise, like M3's approval ops), a human cancels any. A principal that received another principal's transform through reuse (D-T1-2) is not its owner and cannot cancel it (403 `FORBIDDEN`, `details.owner`).
9. **`Verify(ctx, takeID)`** (CLI-only, documented parity exception D-T1-18) re-renders from the stored spec and the parents' masters into a scratch dir (no ingest, no queue row) and compares `pcm_sha256`: `VerifyReport{Match bool;
   Inconclusive bool; Reason string; Expected, Got string}`. **`Inconclusive` (never `Match=false`) when the recorded toolchain differs from the running one:** `go_version`, `goarch`, `goamd64`, `dsp_version`; for
   `score_render` also the recorded `synth.version` major.minor or the soundfont sha256. (ffmpeg is not in the rule: decode is native-format and exact, D-T1-19.) `TestVerify_InconclusiveOnToolchain` (each field altered in a copied `transform.json`).


### C211. Library, lineage, Songs, export amendments

1. **`library` write path (M1 C17):** `NewTake` gains `TransformID string`; `Origin == "transform"` is stored as `origin='job'` with `job_id NULL` and `import_rule
   'transform'` (`enumRule` gains `"transform"`); `validateTransformTake` requires `TransformID == ID`, `Reference == false`, `Lossy == false`, 24 bits, 2 channels, a
   `Mutation` whose operator is one of the nine kinds, and — for every kind except `score_render` — a `ParentTrackID`; `enumOperator` gains the nine operators and `rootlessOperators` gains `score_render`;
   the mutation row stores `transform_id`; the tag-inheritance step is unchanged. **Every `Origin == "job"` branch is audited and given an explicit transform arm** (the stored value is `'job'`, the semantics are not):
   `isJob()` stays "has a `JobID`" (so `Origin: "transform"` is **not** a job: `validateJobTake`'s job-id/env requirements do not apply and `validateTransformTake` runs instead), `scoreSource`
   (feeds `scores.source ∈ {import, job}`) returns "no scores row" for a transform and never yields `transform`, `insertMutation`'s job branch is replaced by a three-way switch on take kind, `fulfil` is skipped.
   **`InsertTake` derives no style terms and sets `bpm_declared = NULL`** for transform Takes (it must not call `yue2.StyleTerms` on anything: the label is not style text).
   Tests: `TestInsertTake_Transform` (rows: tracks with `transform_id`, `origin='job'`, `import_rule='transform'`, `job_id NULL`, `bpm_declared NULL`; mutation with `transform_id`; no
   `track_style_terms`; tags inherited), `TestInsertTake_TransformRefusals` (reference/lossy/mismatched id/16-bit/mono/missing parent for morph ⇒ error; `score_render`
   rootless ok), **`TestInsertTake_TransformScoreSource`** (a transform never writes a `scores` row and never reaches `scores.source`; a YuE2 job take still does), `TestInsertTake_JobUnchanged` (golden rows of an M3 job take
   before/after the change), `TestTriggers_Transform` (`tracks_transform_shape`, `recipes_dsp_shape`, draft-parent triggers, `transform_inputs_master`/`_lossless`, state machine, finality,
   live-spec uniqueness, `cpu_ms` required on every terminal state), `TestUpgrade_T1`.
1b. **Style readers skip engine `dsp`.** `scanSummary`, `TrackDetail` (both recompute style terms live from `recipes.style` today; F1 re-locates them), the FTS triggers, `drafts.go`'s style-term step and the
   `search_tracks` style filter skip recipes of engine `dsp` (belt and braces with `recipes.style = ''`): a transform's title text such as `dsp polytempo 90/120` can never become style chips, parse as a BPM or be FTS-indexed.
   Tests: **`TestSummary_DspNoStyleTerms`** (summary and detail of a transform Take: empty `style_terms`, `bpm_declared` null, even if the title is `polytempo 90/120`), `TestSearch_StyleFilterSkipsDsp`, `TestFTS_DspNotIndexed` (a transform's
   title is searchable as a title but no style/lyrics FTS content exists for its recipe).

1c. **Engine sweep (D-T1-22; new in revision 3).** Every reader that treats `recipes.engine <> 'unknown'` as "a real YuE2 recipe" is audited at F1 and switched: `exports/source.go:116` `SelectRecipes` (feeds `songs.jsonl`, yue2 batch and ZIP manifests) and the ZIP recipe reader ⇒ `engine = 'yue2'`; `generation/plan.go:174` mother load (parses `request_json` as YuE2) ⇒ refuses `dsp` through `RequireRenderableMother` before it parses; `library/stats.go:296,315` (style-term and seed statistics) ⇒ skip `dsp`; `library/write.go:815` `RelinkVariantiMothers` and `library/snapshot.go:75` ⇒ verified harmless (a `dsp` `request_id` equals the spec id and cannot match a group key) and pinned by a test; `lineage/tree.go:62` ⇒ neutral (reads the engine for display); `lineage/backfill.go:14` is already `engine = 'yue2'`. Test: **`TestEngineSweep_Dsp`** (a library holding a transform Take: `songs.jsonl`, yue2 batch and ZIP manifest byte-identical to the library without it, `get_stats` seed/term statistics unchanged, `estimate_generation` of every mother-taking mode refused with `TRANSFORM_TAKE_NOT_MOTHER`). Any further hit found by `rg "engine (<>|=) 'unknown'"` at build time is appended to baseline row B12.

2. **`library.RequireRenderableMother(ctx, trackID)`** → `*TransformMotherError{TrackID, NearestAncestorID *string}` (`Is(ErrTransformMother)`); the M3 planner/`generation` calls it in
   the same places as `RequireRecipeKnown`; `libErr` maps it to 422 `TRANSFORM_TAKE_NOT_MOTHER` with `details {track_id, nearest_renderable_ancestor_id}`. `TestEstimate_RefusesTransformMother`
   (every mode that takes a mother, `from_template` with one transform among five ⇒ whole estimate refused, nothing written), `TestNearestRenderableAncestor` (chain morph → glacier → generated child's
   parent returns the generated take; a `score_render` chain returns null).
3. **Lineage (M3 C71.8):** tree nodes gain `transform: {kind, transform_id} | null` and `also_from: [{track_id, role}]` (empty array otherwise; never null); the mutation edge `operator` is
   the kind; extra inputs are annotations only (D-T1-5); `TestLineage_AlsoFrom` (morph of A and B: tree under A; B appears as `also_from` of the morph node, not as its parent; B's own node has no new child),
   `TestLineage_TransformNodes`, `TestLineage_ScoreRenderRoot`.
4. **`search_tracks` / `get_track` (M1 C19):** `origin` accepts `transform` (derived, D-T1-4; `origin=job` selects `job_id IS NOT NULL` only); new filter `lyrics_of=<track id>` — the morph partner finder.
   **Definition:** normalise both lyrics (CRLF and CR → LF, trim leading/trailing whitespace of the whole text and trailing whitespace of each line); the filter returns Takes whose recipe lyrics equal the target's normalised lyrics
   (excluding itself; reference and lossy excluded; ≤ 200). **Empty or whitespace-only lyrics ⇒ empty result** (every instrumental would otherwise match every other), **recipes of engine `dsp` are excluded** from the
   candidates and a `dsp` target answers empty (their lyrics are `''`). `TrackSummary`/`TrackDetail` gain `transform {kind, transform_id,
   spec_hash} | null` and `engine: "dsp"` flows through the existing recipe block. `/context` (M2a C35) gains `transform` (kind, params summary, inputs with roles, alignment sources, safety summary). `TestSearch_OriginTransform`,
   `TestSearch_LyricsOf` (rows: three takes with equal lyrics + one different ⇒ the two others, ordered by `created_at` then id; CRLF vs LF equal; trailing-space difference equal; **instrumental target ⇒ []**; **transform Take as target ⇒ []**;
   a transform Take never appears among candidates), `TestContext_Transform`.
4b. **Stats and the other `origin='job'` consumers (revision 2: a task, not a risk).** `library/stats.go:279` counts `tracks WHERE origin = 'job'` as `TakesMonth`; it becomes `job_id IS NOT NULL`, so transform Takes do not inflate generation stats;
   `TrackDetail.Origin == "job"` gates the `GenerationOf` call and is now computed from the derived origin (the reader at `tracks.go:1336` joins `jobs` on `job_id` and already returns nothing for a transform; made explicit).
   T1-B6 re-greps every `origin` reader (jobs, reconciler, `get_stats`, views) at align and appends hits to the baseline table. Tests (in T1-B6's tests-first list): **`TestStats_T1`** (`get_stats` generation counts, `TakesMonth` and cost sums unchanged by transform Takes),
   `TestTrackDetail_TransformNoGenerationOf` (a transform Take's detail has `origin: "transform"`, `GenerationOf` null and no error).

5. **Export (M2c C62):** the `songs.jsonl` and `yue2 batch` selections add `recipes.engine = 'yue2'`; `TestExport_SkipsDsp` (byte-identical output for the imported rows with and without transform takes in the database). The ZIP/playlist exports
   and the Subsonic mapping (M4a) treat a transform Take as any Take.
6. **L1/L2/L3 amendments** (recorded in their plan documents by T1-B6 once they are merged; each has a test in the owning package): Sound compute resolves style-derived facets of a `dsp` take through the nearest non-`dsp`
   ancestor (a `score_render` root has none: `engine` and measured `tempo_band` only) — `TestSounds_DspInheritsFromAncestor`; Pick derivation excludes transform Takes from the `newest` source —
   `TestPick_TransformNeverNewest` (a Song with two generated takes and a newer glacier: the Pick is unchanged; after `pin` the glacier is the Pick; after human 5★ and a duel win `ladder`/`stars` can select it); features of a `dsp` Take are measured-only (D-T1-24, `TestFeatures_DspMeasuredOnly`). `analysis.OnsetEnvelope` is **already exported** and T1 does not change it (D-T1-20, `TestOnset_PinnedForT1`).
7. **Acceptance of the guards:** `TestGuards_TransformsNeverSpend` — `go list -deps` of `internal/transform` has no `spend`, `provider`, `jobs`, `generation`, `upload`; no table of M3 gains a row during a transform (row counts of
   `ledger`, `batches`, `jobs`, `estimates`, `provider_state` unchanged across an end-to-end transform).

### C212. Operations (types in `internal/api/types_t1.go`; JSON snake_case; no `tools/list` change, D-T1-21)

1. **Registry rows** (all `Idempotency` rules per `api.Meta`; every handler checks `d.Transform != nil` ⇒ 503 `UNAVAILABLE {component: "transform"}`):

| Op id | Method + path | Scope | Cost | MCP | UI | Other |
|---|---|---|---|---|---|---|
| `estimate_transform` | POST `/api/transforms/estimate` | write | free | generic | `/tracks/:id` | `Meta` note states why `write`: a body-bearing POST that mutates nothing (rule "GET never mutates" unaffected) but reveals quota/queue state and previews CPU-shaped work, so read-only tokens do not get it |
| `create_transform` | POST `/api/transforms` | write | free | generic | `/tracks/:id` | `Idempotency: create`, `Rate: heavy` |
| `get_transform` | GET `/api/transforms/{id}` | read | free | generic | `/transforms` | |
| `list_transforms` | GET `/api/transforms` (`state`, `kind`, `track_id`, `limit` ≤ 100, `cursor`) | read | free | generic | `/transforms` | |
| `wait_for_transform` | GET `/api/transforms/{id}/wait` (`since_state`, `timeout_s` 0–55) | read | free | generic | `/tracks/:id` | |
| `cancel_transform` | POST `/api/transforms/{id}/cancel` | write | free | generic | `/transforms` | |
| `get_transform_capabilities` | GET `/api/transforms/capabilities` | read | free | generic | `/tracks/:id` | |

   **All seven are generic** (D-T1-21; operator box P.5-G3): the `tools/list` golden and `toolsCeilingBytes = 108300` are unchanged, `TestToolsList_Ceiling` stays green with its 72 bytes of headroom. An agent reaches them through `call_operation`. Should the operator promote the four lifecycle operations to dedicated tools, the first lane to need it measures the real schemas (analogues on main: 5 386 + 4 322 + 3 785 + 3 374 B) and demotes `estimate_transform` first. No op is human-only.
2. **Request body** (create and estimate; exactly the member named by `kind` is present, else 422 `INVALID_TRANSFORM {rule: "kind_params_mismatch"}`):

```json
{"kind":"glacier","title":null,"rationale":"ambient intro candidate","seed":null,"target_lufs":null,
 "glacier":{"source_id":"…","factor":20,"start_s":0,"length_s":30,"window_s":0.5,"fade_in_s":4,"fade_out_s":4}}
```
   Members: `morph`, `shepard`, `glacier`, `polytempo`, `ensemble`, `score_render` (T1b) with the fields of C202.3 (all optional except the required ones; `null` = default; ensemble `space`
   as a preset name; morph/polytempo `downbeat_a_s`/`downbeat_b_s` as a number or `"auto"`; score_render `score: {abc | from_track_id}`); `rationale` ≤ 1000 chars (→ the ledger row); `seed` defaults to 0 for kinds that use one (glacier and
   ensemble: a deterministic default derived from the first input's id, so two takes of one glacier differ only when asked: `DefaultSeed(trackID) = first 8 bytes of sha256 mod 2⁵³`).
3. **`TransformEstimate`** (output of `estimate_transform`): `{kind, spec_hash, title, cost {micro_usd: 0, usd: "0.000000"}, cpu {expected_ms, max_ms, budget_ms, model: "cpu/1"},
   wall_expected_s, output {seconds, sample_rate, bits: 24, channels: 2, bytes_est}, memory_mb, inputs [{role, track_id, title, duration_s, bpm {value, source, octave_fold, confidence?} | null,
   key {camelot?, pc, minor, source} | null, beats_per_bar}], resolved {…the params with every default…}, warnings [{code, field, message}], quota {used_ms, reserved_ms, cap_ms, resets_at},
   queue {running, queued, position_if_created}, existing {transform_id, take_id?, state} | null, tools {ffmpeg, flac, fluidsynth: bool, soundfont: string | null}}`; warning codes
   `tempo_measured_low_confidence`, `tempo_octave_folded`, `key_unknown_assumed_none`, `large_tempo_change` (|ln(T1/T0)| > 0.25 for morph/polytempo), `heavy_pitch_shift` (|s| > 6 or any
   ensemble voice beyond ±12), `ensemble_full_mix`, `beat_phase_pending` (always present for morph/polytempo/shepard: the local grid is measured in the render), `downbeat_auto` (the caller opted in to the heuristic), `section_unavailable`, `start_defaulted`, `soundfont_default_used` (T1b); the **render-time** warnings `downbeat_ambiguous`, `downbeat_one_drop_suspected`, `beat_phase_weak`, `tempo_refine_rejected`, `downbeat_from_plan_assumed` appear in `TransformView.warnings` and `transform.json` and are shown by the UI.
4. **`TransformView`** (create/get/list/wait): `{id, take_id (= id), kind, state, phase, progress_permille, title, spec_hash, reused, principal: PrincipalRef, rationale, inputs [{role, track_id, title}],
   cost {micro_usd: 0, usd: "0.000000"}, cpu {expected_ms, max_ms, used_ms | null}, output_seconds_est, restored (bool, D-T1-2), warnings [{code, field, message}] (render-time, empty until `done`), error {code, message} | null, cancel_requested_at, created_at, started_at, finished_at}`.
   `create_transform` returns **202** for `queued`/`running` (also for a reused live one) and **200** for a reused `done` transform (the output struct carries `Status int`).
   `wait_for_transform` returns `{transform, changed, timed_out}`. `list_transforms` returns `{items, next_cursor}` ordered by `created_at DESC, id DESC`.
5. **`TransformCapabilities`:** `{enabled, kinds [{kind, summary, inputs, params [{name, type, default, min, max, enum, required}], blind_spots [string], example}] (`score_render` listed only when T1b is enabled; the shepard entry carries the sentence "a shepard of five layers of a full mix is expected to sound muddy; use a pad-heavy, drum-free loop"; the ensemble entry states the full-mix limitation; the quota block states that the cap is a shared pool per principal kind), limits {…C200.4/D-T1-10},
   quota {principal_kind, cap_ms, used_ms, reserved_ms, resets_at}, queue {running, queued, max}, tools {ffmpeg, flac, fluidsynth {available, version}}, soundfonts [{name, sha256_12, bytes}],
   dsp_version}` — generated from the same Go tables that validate (`TestCapabilities_MatchesValidation`: every listed range is enforced and every enforced range is listed).
6. **Errors** (problem+json `{code, message, details}`, UPPER_SNAKE): 422 `INVALID_TRANSFORM {field, rule}` (rules in C202 and C205), `ALIGNMENT_UNRESOLVED {input, quantity: "bpm"|"downbeat", pass}`, `SCORE_INVALID {line, col,
   rule}`, `SOUNDFONT_UNKNOWN {name, available}`, `TRANSFORM_TOO_LARGE {limit, value, max}`, `RECIPE_UNKNOWN`, `LOSSY_MASTER` (existing, `details.track_id`), `TRANSFORM_TAKE_NOT_MOTHER` (C211.2); 404 `NOT_FOUND`;
   403 `FORBIDDEN`; 429 `TRANSFORM_QUEUE_FULL {running, queued, max}` (+ `Retry-After`), `TRANSFORM_QUOTA_EXCEEDED {used_ms, reserved_ms, cap_ms, resets_at}`; 503 `UNAVAILABLE {component: "transform" | "fluidsynth" |
   "soundfont"}`. Row-level (in `TransformView.error.code`): `INVALID_TRANSFORM`, `TRANSFORM_BUDGET_EXCEEDED`, `TRANSFORM_TIMEOUT`, `TRANSFORM_MEMORY_EXCEEDED`, `DSP_NONFINITE`, `DSP_SILENT_OUTPUT`, `LOUDNESS_UNSAFE`,
   `SYNTH_FAILED`, `SOUNDFONT_UNKNOWN`, `INPUT_CHANGED`, `INGEST_FAILED:<code>` (including `INGEST_FAILED:DUPLICATE_AUDIO`), `INTERRUPTED`, `CANCELLED_BY_USER`, `TRANSFORM_FAILED`. Messages never contain paths, argv or score text.
7. **Audit (M2a C28):** `create_transform` writes `{kind, input track ids, spec_hash, reused}` (never the score text, only `score_sha256`) with `CostMicroUSD = 0`; `cancel_transform` writes the id; the runner's terminal
   transitions write system audit rows `transform_done|failed|cancelled` with `cpu_ms`. `TestNoSecretLeak` extends to transform text fields and `transform.json`.
8. **Parity (M2a C42):** a `ParityCase` for each of the seven ops (`create_transform` reads nothing from `prev`; `wait_for_transform` and `cancel_transform` read `id` from the create result) must return equal normalised
   JSON over REST, MCP and stdio; all seven are reached through `call_operation` (the generic face).

### C213. CLI (`cmd/metamusic`; amends M0 C7)

`metamusic transform verify <take-id> | --all [--limit n]` (C210.9; exit 1 on any `Match=false`, `Inconclusive` is reported and exits 0), `metamusic transform sweep` (C210.3 offline, refuses
while a server holds a live transform lease: a `running` row with an unexpired `lease_expires_at`), and the hidden `metamusic dsp-run --work <dir>` (the render child; absent from `--help`; refuses to run with a relative or non-0700 directory). `TestCLI_TransformVerify`
(match, tampered `out` hash ⇒ exit 1), `TestCLI_Sweep`, `TestDspRun_Refusals`. No new REST surface. **Documented parity exception (D-T1-18, rule 1):** `verify` and `sweep` are host-operator maintenance (a re-render with no ingest and no queue row; offline recovery under the lease) and are recorded as exceptions, with this reason, in `docs/architecture.md`; `TestArchitectureDoc_T1Exceptions` fails if the doc loses them.

### C214. Web (`web/`; amends M2a C40, M3 C84, L2 C131, L3 C145 and the UI redesign shell; Svelte 5 runes only, network only through `web/src/api/`)

The shipped shell is **five tabs, listen-first: New · Library · Play queue · Judge · More** (`lib/nav.ts`: every route belongs to exactly one tab); Library sub-views are Takes · Sounds · Atlas · Records · Crates · Playlists; secure pages are in-app routes; dark only; 44 px targets; the SPA's only network door is the generated client.

1. **Routes (added by F1, `lib/router.ts` + `lib/nav.ts`):** `/transforms` (name `transforms`, tab **more**, beside Jobs — a queue/history page, not a Library sub-view; `Transforms.svelte`) and **(T1b)** `/transforms/score` (name `transforms-score`, tab more; `ScoreRender.svelte`: ABC textarea or `.abc` file picker read client-side to text ≤ 256 KiB, soundfont select from capabilities, tempo override, tail, estimate preview, Render). No new tab. Queue page: state chips, progress bars, Cancel on live rows, today's CPU used / reserved / cap meter (shared-pool wording), link to the Take. The `MoreRow` list gains a "Transforms" entry (More page owner: W1 edits `More.svelte` only for that row).
2. **Take page "Transform" panel** (`lib/components/TransformPanel.svelte`, route `track` `/tracks/:id`, Library tab; one sub-form per kind): a segmented control (Morph · Shepard · Glacier · Polytempo · Ensemble; "Render score" in T1b when the Take has `score.abc`, using `from_track_id`); forms are generated from the capabilities' parameter tables (labels, ranges, defaults) with kind-specific pickers — morph/polytempo partner = a Take picker pre-filled from `search_tracks {lyrics_of}` ("same lyrics, other styles"), ensemble space as four preset buttons (voices are the fixed preset); morph/polytempo downbeat fields are **required unless the input has a plan bar grid**, each with an "auto (guess)" toggle that sends `"auto"` and then shows the `downbeat_auto` note. A **debounced (400 ms) estimate** shows `$0.00 · ≈ 14 s of CPU · 3 min 20 s of audio · 180 MB`, the resolved tempo/key per input with its source chip (`explicit`/`plan`/`declared`/`measured`) and every warning as an inline note; render-time warnings (`downbeat_ambiguous`, `beat_phase_weak`, …) are shown on the transform's card and on the new Take's provenance panel. **Render** calls `create_transform` with a fresh idempotency key, then follows `wait_for_transform` (long-poll) with progress bar and phase label, a Cancel button, and on `done` a link to the new Take plus "A/B with source" (M2b `create_duel`, non-blind). Quota/queue refusals render their `details` (when it resets, how many are queued). The panel is a **bottom-sheet on the phone width** (the shell's pattern for forms), full-width controls ≥ 44 px, and keeps the mini player usable (music keeps playing while a transform renders). The panel never calls `fetch`; everything goes through `web/src/api/`.
3. **Lineage page** (`Lineage.svelte`, `LineageTree.svelte`) shows `also_from` as a dotted secondary edge with the role label and an engine chip `dsp`/kind; the Track provenance panel shows kind, resolved parameters (collapsed), spec hash prefix; "verify" is **not** a UI action (CLI only). The Library **Takes** list gains an `origin: transform` chip and the existing origin filter accepts `transform`.
4. **Served-op coverage (M0 C5):** `estimate_transform`, `create_transform`, `wait_for_transform`, `get_transform_capabilities` (Track panel), `get_transform`, `list_transforms`, `cancel_transform` (`/transforms`) are each called by a Playwright spec (`TestServedOps` fails otherwise). Bun unit tests: `transformForm.test.ts` (each kind's form ⇒ request body golden; defaults omitted as `null`), `transformView.test.ts` (the estimate line for a fixture response; refusal rendering for the 4xx/429 codes; Cancel visible only for live states; sorting; the meter), `Transforms.test.ts`, (T1b) `ScoreRender.test.ts` (file ⇒ text body; 256 KiB guard).
5. **Playwright** (`web/e2e/t1-*.spec.ts`, chrome + firefox, proxy auth mode, helper `e2e/t1Api.ts` in the style of `l3Api.ts`, on `FixtureT1` with the real child on 16 kHz-scaled fixtures and `MM_E2E_FAST_DSP=1`, which only shortens the fixture audio, never the code path): `t1-glacier.spec.ts` (open Take → Transform → Glacier → estimate shows `$0.00` and CPU seconds → Render → done → new Take opens, origin chip "transform", duration = source × factor ± 1 s, lineage shows the edge), `t1-morph.spec.ts` (partner picker offers exactly the same-lyrics Take; estimate shows tempo sources; Render → done; lineage shows `also_from`), `t1-cancel.spec.ts` (start a slow render, Cancel ⇒ `cancelled`, no Take appears, `/transforms` shows it), `t1-quota.spec.ts` (token with a tiny cap ⇒ the 429 explanation with a reset time), (T1b) `t1-score.spec.ts`. Counters are never shared across specs (`/__e2e/leaks` empty); new specs are added to `scripts/e2e-map.txt` so `make verify` selects them.
### C215. Tooling, CI, Docker, gate, coverage, docs (amends M1 C23/C24, M2a C41, M3 C85)

1. **Dockerfile (T1b only):** `apk add --no-cache … fluidsynth`; the build-time check runs `fluidsynth --version` and asserts the `-F`, `-T`, `-O` and `-R` options appear in `fluidsynth --help` (same style as the ffmpeg
   filter check). The image contains **no** soundfont (and therefore ships no soundfont licence text); the compose example mounts one read-only and sets `MM_SOUNDFONTS` (documented in `docs/runbook.md`, no real paths). **Image licensing (stated in the runbook):** fluidsynth is LGPL-2.1-or-later per its source headers and runs as a separate process, so the MIT repository is untouched; Alpine's `fluidsynth` package pulls in further LGPL/GPL dependencies on top of an ffmpeg apk that is already GPL-flavoured, so the image is an aggregate: T1-I1 records `apk info -R fluidsynth` and the licence list of the closure in the private notes and the runbook says "any soundfont distributed with the container image must ship its licence text". `GOAMD64=v1` is set explicitly in the build stage.
2. **CI:** `go-media` installs `fluidsynth` (apt; T1b) and sets `MM_REQUIRE_MEDIA_TOOLS=1` (so the media tier fails, not skips, without it); `go-fast` runs `internal/dsp/...` and `internal/score/...` with `-short` **and without `-race`** (≤ 1 s per test at
   16 kHz; `TestFast_Budget` reads `go test -json` timings in the weekly job and fails above 3 s); a weekly `transform-bench` job (`BenchmarkTransforms`, informational) and `TestDeterminism_Golden_AMD64`
   (skips off amd64, **fails** when `GOAMD64 ≠ v1`, asserts the committed `pcm_sha256` of one fixed spec per kind from synthetic inputs; `-update` rewrites after a deliberate `dsp_version` bump) plus `TestDeterminism_FMAOff` (same hashes under `GODEBUG=cpu.fma=off`).
   **Shard weights (revision 3):** `scripts/ci-shard-weights.txt` and `scripts/ci-heavy-tests.txt` are refreshed by I1 (`scripts/ci-weights-refresh.sh`), because the new packages and the media-tier tests change the shards; the 20 required checks stay the merge gate.
   **CI budget (revision 2):** property suite **12 timelines per PR** (300 weekly); `FuzzParseABC` **20 s** (T1b; no second fuzz target); chaos suite on PRs limited to `TestChaos_KillChildAtProgress` (3 progress points), `TestChaos_ServerRestartMidRender` and `TestChaos_ChildHangs`, the full set weekly; Playwright with the real child runs the glacier, cancel and quota specs on PRs, morph weekly and on `main`.
   Expected added wall time, to be re-measured at T1-X0 and recorded: `go-fast` +1.5 min (against its 10-minute timeout, hard cap +2 min: beyond it the slowest tests move to the weekly job), `go-media` +3 min, `e2e` +2 min.
3. **`make` targets:** `make check` unchanged in budget and runs `internal/dsp/...` and `internal/score/...` **without `-race`**; `make test` runs the media tier and keeps `-race` everywhere; new `make dsp` = `go test -short ./internal/dsp/... ./internal/score/...` (no `-race`) through `scripts/loop`.
4. **Privacy gate:** `.public-gate-patterns.example` and `internal/gate` add `*.sf2`, `*.sf3`, `*.mid`, `*.midi`, `*.f32`, `*.pcm24`; `TestGate_T1Patterns` plants each and expects a block; **`TestGate_PublicPlanCopyHasNoPrivateLayout`** runs the gate on the sed-produced public copy of this plan and fails if it names the private plan directory or an L-plan draft path (the exact strings live in the test; revision 2 removed them from the public portion); `TestGate_NoAudioFixturesCommitted`
   (the test SF2, MIDI and audio are generated at test time; golden SMF bytes are kept as a hex dump).
5. **Coverage thresholds:** floors are set by I1 at the **measured value minus 1** after each lane lands (`internal/dsp/...`, `internal/transform`, T1b `internal/score/...`), never above what was measured (the L2/L3 rule); the draft's 90 % figures are aims; `internal/... 85` stays the floor. `internal/transform` joins the merge gate only once the B3 fake-child tests exist (a dry-run report before that).
6. **Docs:** `docs/architecture.md` gets a "Transforms" section (public-safe: the nine-line model, the no-money statement, the determinism contract, the safety policy); `docs/runbook.md`: soundfont setup and licence reminder,
   the CPU budget/quota knobs (the quota is a shared pool per principal kind), `transform verify`, "a transform is stuck" (cancel, sweep), the toolchain pin (`GOAMD64=v1`, Go version recorded; what `Inconclusive` means), the image-licensing paragraph above; the roadmap gets a T1 row (T1-X1). `docs/architecture.md` also lists the **documented parity exceptions** `transform verify` and `transform sweep` with their reason (D-T1-18).

### C216. Test harness, fixtures, property and chaos suites (amends M3 C86)

1. **`internal/dsp/dsptest`:** `Clicks(rate, bpm, firstS, durS, bpb int, accent bool) pcm.Buffer` (a 3 ms low-passed burst per beat, 2× amplitude plus a 90 Hz burst on every `bpb`-th beat when `accent`),
   `Sine`, `Sweep`, `Vowel(rate, f0, f1, f2, durS)` (impulse train through two two-pole resonators), `PinkNoise(seed)`, `Peak(frame, rate)` (FFT peak with parabolic interpolation), `PeakTrack`, `RMSdB`, `OnsetTimes`
   (spectral-flux picker, 5 ms resolution), `Schroeder` (backward integration), `WriteSF2(path, opts{ReleaseS})` (T1b; the generated test soundfont: INFO/sdta/pdta chunks, one preset/instrument/sample, looped sine, root key 69, fine tune 0,
   attack/decay near zero), `FLAC(ctx, tools, buf)` (writes a real 24-bit FLAC through ffmpeg for media-tier tests). All generators are deterministic.
2. **`FixtureT1`** (`librarytest`, extends `FixtureL3`): two lossless 24-bit Takes of **equal lyrics and different styles** (plus one instrumental Take with empty lyrics and one with whitespace-only lyrics, for `lyrics_of`) (`style` A "reggae, 92 bpm" with `bpm_declared 92`; B "eurodance, 128 bpm" with a stored `mm-tempo/1`
   analysis of 128, confidence 0.8, `octave_ambiguous false`), 20 s clicks audio each (accented downbeats, 4/4), a third Take with a `score.abc` and an aligned `scores` row (with `seconds_per_bar`, for the plan-grid downbeat anchor), a reference Take and a lossy Take (refusal
   tests), a Song/Record/Crate for the library-membership tests (L1/L2/L3 exist: `FixtureT1.WithLibrary`), **and the real-data shapes that L3-X2 found missing from fixtures: a draft-only Song, a reference Take, a lossy import, a multi-part import, a Take with no `take_features` row, a Take whose feature tempo basis is `declared` and one with `bpm_octave` set**. Real FLACs are generated by `dsptest.FLAC` into `t.TempDir()`; **nothing audio is committed**.
3. **`servertest.Options`** gains `Transform *transform.Options` overrides: `Child ChildRunner` (production = the exec supervisor; tests use `FakeChild{Duration, ExitCode, Progress []int, WriteOut func}` driven by
   `testing/synctest`, or the real child for media/e2e), `Synth` (`FakeSynth` writes a known WAV), `Clock`, `Fault`. The fake child makes runner/queue/quota/cancel tests instant and deterministic; the real child
   is covered by the media-tier acceptance tests.
4. **Property suite** (the five DSP kinds; `score_render` joins in T1b): `TestProperty_AnyTransformIsSafe` (**12 timelines per PR run**, 300 weekly; random in-range parameters per kind on synthetic 16 kHz inputs of 3–8 s, run through the pure `ops.Run` + `safety.Finish`):
   finite samples; length equals the closed-form length of the kind (`MorphLen`, `GlacierLen`, …); `TruePeakDBTP ≤ −0.95`; integrated loudness within ±1.5 LU of `target_lufs_effective` or reported `limited`;
   `pcm_sha256` equal across two runs; no goroutine started (`runtime.NumGoroutine` unchanged); peak RSS growth below the memory estimate (`runtime.MemStats`, a loose 2× bound). `TestProperty_SpecCanonical`
   (random defaults spelled out or omitted ⇒ equal hash; any parameter change ⇒ different hash; parent sha change ⇒ different hash).
5. **Chaos suite (go-media, real child):** `TestChaos_KillChildAtProgress` (kill at 10 progress points ⇒ `queued` then completed on the retry, or `failed INTERRUPTED` after two kills; never a Take; the work dir and any
   `takes/<id>` orphan are gone), `TestChaos_ServerRestartMidRender` (runner stopped mid-render, sweep, restart ⇒ exactly one Take), `TestChaos_DiskFullAtEncode` and `_AtIngest` (via `Fault`/a size-limited tmpfs where
   available ⇒ `failed`, no orphan files), `TestChaos_ParentRowTampered` (a `transform_inputs.master_sha256` that no longer matches ⇒ `INPUT_CHANGED`, nothing written), `TestChaos_ChildHangs` (a child that ignores everything ⇒
   wall deadline kills it, `TRANSFORM_TIMEOUT`), `TestChaos_ChildMemoryBomb` (a test hook allocating beyond the estimate ⇒ `TRANSFORM_MEMORY_EXCEEDED`, the server process unaffected).

### C217. Derived files, work dirs and the media-serve chain (verified against the L2 tape on `5f6eeaf`; new in revision 3)

1. **What the L2 tape does (the reference).** `internal/tape/runner.go` claims a queued `tapes` row (row lease, B18), renders in a work area under `incoming/` (`workParent = "incoming"`), then `claimFile` links the finished FLAC to `derived/tapes/<aa>/<sha256>.flac` mode 0444 (content-addressed name; an existing name is reused) and finishes the row. `tape.Service.Resolve(ctx, sha, ext)` answers only ext `flac`, only a 64-hex sha that names a `ready` tape row, and returns `serve.Object{RelPath, ContentType "audio/flac", ETag sha, DownloadName}`; anything else is `serve.ErrNotFound`, so `serve.Chain` falls through. `app.go` builds the chain once: `[lib, exp, clips, m4a covers/downloads, l2.Resolvers…]` (`app.go:246-279`) and hands it to `mediaserve.Handler(signer, data, Chain(resolvers…), probe)` (`app.go:326`); media URLs are signed (`serve/sign.go`). Cache directories under `derived/` (`probe`, `clips`, `exports`, `covers`, `dl`, `tapes`) are skipped by the importer's orphan scan (`importer/report.go:287`); the backup snapshot takes `audio/masters/`, `takes/` and `derived/` except `derived/exports/` (`backup/snapshot.go:70-78`). Tape retention is `RetentionDays = 14` and a sweep removes unreferenced files older than a day (`tape/sweep.go`).
2. **What T1 reuses (decision D-T1-23).** The **library resolver** (`library.Service.Resolve`, `library/tracks.go:1227`, first in the chain) already serves a Take's master and its renditions by sha, so a transform Take is served, signed, Range-served, cached offline, listed in Subsonic and downloaded exactly like a generated Take — **no new resolver and no change to `app.go`'s chain**. Work dirs are `incoming/transform-<id>/` (0700, removed on every terminal path and by the startup sweep; the importer already ignores `incoming/`). Intermediate PCM (`in-<n>.f32`, `out.pcm24`) never leaves the work dir and is never served. The only persistent files are the ingest products: master (`audio/masters/`), `takes/<id>/request.json` + `transform.json` (+ T1b `score.abc`/`score.mid`), `derived/<aa>/<sha>.mp3` and `.peaks.json` — content-addressed, covered by the backup snapshot, never expiring.
3. **Rule 7 and the parents.** The render reads parent masters through an ffmpeg decode only (the work dir holds decoded copies), never opens a master for writing, and verifies `master_sha256` equals the spec's before the run (`INPUT_CHANGED`). The new master is frozen by the same pipeline as every other (`flac -t`, tags once, 0444). `TestAcceptance_T1_MastersFrozen` (C216) pins parent sha/mode/mtime before and after a run and after `transform verify`.
4. **If the operator chooses the derived-only variant (P.5-G1).** Changes versus this plan: no `tracks` row, no `transform_id` column on `tracks`/`mutations`, no `RequireRenderableMother`, no Song/Pick/lineage/search amendments (lane B6 disappears except the engine-sweep tests, which become unnecessary), no `lyrics_of`; add instead `sha256/path/bytes/filename/expires_at` columns on `transforms` like `tapes`, a `derived/transforms/<aa>/<sha>.flac` claim (mode 0444, hard link), a `transform.Service.Resolve` appended to the resolver list, `derived/transforms` added to `importer/report.go:287`, a retention sweep and a quota on ready bytes, and a signed download op; the UI plays it through the existing media URL but **cannot rate, duel, pick or collect it** (the option means "a render to keep, not to judge"). The DSP lanes (D1–D5), spec, estimate, runner, supervisor, quota, CLI and web panel are identical in both variants.
5. **Tests.** `TestServe_TransformTakeMaster` (a signed URL of a transform Take's master and mp3 rendition returns 200/206 with the right ETag through the real handler chain), `TestImporterIgnoresTransformWork` (`incoming/transform-*` is not reported as an orphan), `TestBackup_IncludesTransformTakes` (a restored backup keeps the Take playable), `TestNoNewResolver_T1` (`app.go` builds the same resolver list as before T1, asserted by count and type names).
### C218. Hi-fi / 3D lanes — overview, operator meaning and shared rules (**in scope this round by operator decision 2026-11-03; revision 4; replaces the optional sketch O1–O3 of revision 3**)

Contracts C218–C225 are this lane family ("T1h"): three more kinds of the same pipeline, `master`, `widen` and `binaural`, built as lanes **H1** (master + widen) and **H2** (binaural). Nothing in C200–C217 changes except the places listed in C225 (the kind list, the operator list, two signatures and a registration seam), all frozen by F1 so no later lane edits a frozen file.

1. **What they mean for the single operator.** In the sound-families idea a "hi-fi" level is a *generation* (a better-produced YuE2 sibling of a loved original, M3/E2 spend, not T1). T1h is the other half: a **free, CPU-only polish layer for any Take** — a loved original, an imported file or a generated sibling. Worked example: an Indian bhajan Take (a harmonium drone, tabla and a voice in a stereo mix) becomes a family of listening copies without any model or spend: `master` (cleaner low end, a gentle tonal tilt, light levelling, level-matched to the parent and true-peak safe) → `widen` (a broader stage whose mono fold-down is the original's mono; the tabla's bass stays centred) → `binaural` (a headphone copy in which the stereo image sits in front of the head in a small virtual room). Recommended chain order is `master → widen → binaural`; `binaural` is **terminal** (a listening copy for headphones). Every result is an ordinary Take (P.5-G1, D-T1-1/23): it joins the Song, can be starred, put in a duel against its parent and collected into Records and Crates, and because D-T1-14 level-matches every transform to its parent's loudness the duel compares tone, not volume.
   **Honest limits (stated in `get_transform_capabilities` and the UI):** all three process the **full mix** (stems are not Takes, E2-5); `master` cannot add detail or rescue a poor mix; `widen` adds width, not new content; `binaural` is *virtual loudspeakers* — the two stereo channels are placed in front of the head (plus an optional small room) and the phantom centre emerges from them; it is **not** per-instrument 3D placement, it is **headphones only**, and its mono fold-down and loudspeaker playback are worse than the parent's.
2. **Kinds and Take shape.** `Kind` gains `master`, `widen`, `binaural` (nine kinds in all; T1a's five are unchanged). One input, role `source`, `transform_inputs.ord = 0` = the ledger parent; engine `dsp`, `dsp_version` stays `mm-dsp/1` (new kinds change no existing hash; any later algorithm change to one of them bumps it for all, D-T1-3); mutation operator = kind; output stereo 24-bit FLAC at the **input's rate** (no resample of the audio), frozen by the existing pipeline; the same seven operations, the same create → estimate → run → ingest path, the same quota, queue, cancel and reuse rules (D-T1-2, 10) — **no new operation, table, config variable, resolver or tool bytes** (`TestToolsList_Ceiling` unchanged). Output length: `master` and `widen` equal the input length exactly; `binaural` equals the input length plus the room tail (C221.4), exactly `len + tail_samples`.
3. **Determinism and the integer rule (extends D-T1-3).** All maths is `float64` with the tree's own `dsp.Exp/Log/Pow` (the banned list and `TestDSP_BannedMath` cover the new files); `Sin`/`Cos`/`Sqrt` only; `tan(x) = Sin/Cos`. **Everything that selects a value is an integer or a fixed step, so two requests that mean the same thing hash the same:** dB parameters step 0.1 (`master`) or 0.05 (`widen`), Hz and degrees are integers, `width`/`decorrelate`/`comp_amount` step 0.05, all validated and written into the spec; delays are integer samples (`round(ms · rate / 1000)`) or **1/64-sample fixed point** with a 16-tap windowed-sinc table; the HRIR table is **int16 Q15** (exact scaling by 2⁻¹⁵), committed as a binary file with a pinned sha256; filter coefficients are derived at run time from those integers by pure-Go closed forms; accumulation order is fixed; single goroutine; no clock; no map iteration. Seeds: none for `master`/`widen`; `binaural` uses the PCG only for the room tail (default seed `DefaultSeed(trackID)`). The oracle stays `pcm_sha256`; goldens per kind and preset are pinned for `linux/amd64`, `GOAMD64=v1` and re-checked under `GODEBUG=cpu.fma=off` (C224).
4. **Safety (extends D-T1-14, C207).** Every H kind ends in `safety.Finish`: the target = the parent's measured LUFS (else −16; optional `target_lufs` for `master`), second-pass gain, lookahead true-peak limiter, derived-not-failed, fails only on non-finite or silent output. **New in C225:** `FinishIn.CeilingDBTP` (default −1.0; `master` may request −3.0…−1.0; **no kind can raise it above −1.0**, `TestFinish_CeilingClamped`). The post-ingest bug alarm is unchanged (`true_peak ≤ −0.5 dBTP`, within 1.5 LU of the effective target). Per-kind guards (below) are **derived values reported, never failures**: `master` limits compression, `widen` lowers its own strength to keep the correlation bounds, `binaural` reports ear balance. No H kind can clip: peaks are limited at the end, never hard-clamped except by the PCM24 rounding clamp.
5. **Chain rules.** `binaural` of a Take whose primary-parent chain (≤ 16 hops, one recursive query on `tracks.transform_id → transforms.kind`) contains a `binaural` is refused: 422 `INVALID_TRANSFORM {field: "source_id", rule: "already_binaural"}` — **the HRTF is never applied twice**. `master` or `widen` of a `binaural` Take is allowed and warns `after_binaural` (the image is already folded; widening it breaks it). Everything else chains freely.
6. **Not built, on purpose:** per-instrument placement, head tracking, distance and Doppler, personalised HRTFs, ambisonics, mid/side tone shaping inside `master`, multiband compression, any ML or generated audio (the `master` of a "hi-fi" level is deterministic DSP only).

### C219. `master` — gentle tonal polish, levelling and true-peak finish (lane H1)

1. **Parameters** (`master` member of the create body; every default written into the spec; 422 `INVALID_TRANSFORM {field, rule}` outside the range):

| Parameter | Default | Range / rule |
|---|---|---|
| `source_id` | required | |
| `preset` | `neutral` | `neutral` · `warm` · `bright` · `punch` (table below) |
| `tilt_db` | the preset's | −3.0…+3.0, step 0.1 (overrides the preset) |
| `comp_amount` | the preset's | 0…1, step 0.05; `0` removes the compressor stage entirely |
| `low_cut_hz` | the preset's | `0` (off) or 20…40 |
| `target_lufs` | `null` ⇒ the D-T1-14 rule (parent's LUFS, else −16) | −30…−12 (the D-T1-14 clamp; **no louder than −12 LUFS**: there is no loudness war here) |
| `ceiling_dbtp` | −1.0 | −3.0…−1.0, step 0.1 |

   Preset table (fixed, in `ops/master.go`, pinned by `TestMaster_PresetTable`): `neutral` tilt 0.0, comp 0.25, low cut 25 · `warm` tilt −1.5, comp 0.30, low cut 25 · `bright` tilt +1.5, comp 0.25, low cut 25 · `punch` tilt 0.0, comp 0.50, low cut 30.
2. **Chain** (stereo-linked, `float64`, one pass; `dsp/filt` biquads from the RBJ closed forms, coefficients from `Sin/Cos/Sqrt` and `dsp.Pow` only): (a) `low_cut_hz` → 2nd-order Butterworth high-pass (off ⇒ skipped, but DC is still removed by Finish); (b) tilt = a low shelf at 250 Hz of `−tilt/2` dB plus a high shelf at 2.5 kHz of `+tilt/2` dB, shelf slope 0.7 (`tilt = 0` ⇒ both skipped); (c) compressor, feed-forward, detector = `max(|L|,|R|)` through a one-pole peak follower (attack 30 ms, release 300 ms), level in dB by `dsp.Log`, threshold `T = lufs_in + 2.0 dB` (`lufs_in` measured in the child by `safety.LUFS`, so it never depends on an Ears row), soft knee 6 dB, ratio `R = 1 + 1.5 · comp_amount` (1.0…2.5), **maximum reduction 6 dB**, no make-up gain (Finish supplies it); (d) `safety.Finish` with the target and ceiling. The input is never trimmed or re-timed: output frames = input frames.
3. **Reported values (not failures):** `gain_db`, `comp.mean_reduction_db`, `comp.max_reduction_db`, `lufs_in`, `lufs_out`, `true_peak_in/out`, requested and effective target, limiter reduction. **Warnings:** `master_heavy_compression` (mean reduction > 3 dB), `master_quiet_boost` (net gain > +12 dB: the noise floor rises), `master_target_lowered` (effective < requested, from the D-T1-14 bisection), `master_input_clipped` (input true peak > 0 dBTP: there is no declipper), `after_binaural`.
4. **Tests (H1; each ≤ 1 s at 16 kHz):** `TestFilt_BiquadResponse` (shelf and high-pass magnitude at five frequencies within 0.1 dB of the analytic response, measured by FFT of the impulse response), `TestFilt_Deterministic` (bit-exact table on the committed coefficients of the four presets), `TestMaster_Transparent` (`comp 0`, `tilt 0`, `low_cut 0`: after gain the output equals the input within 1e-9 before the limiter), `TestMaster_TiltResponse` (±3 dB tilt: −1.5/+1.5 dB at 100 Hz/8 kHz ± 0.3 dB), `TestMaster_CompressorCurve` (a stepped-level tone: static reduction at +6 and +12 dB over threshold equals the formula ± 0.3 dB; never above 6 dB), `TestMaster_AttackRelease` (time constants ± 15 %), `TestMaster_TargetAndPeak` (pink noise at −30 LUFS, target −16 ⇒ −16 ± 0.3 LUFS, true peak ≤ −0.95; ceiling −3.0 ⇒ ≤ −2.95), `TestMaster_NeverLouderThanClamp` (`target_lufs −12`, loud crest-limited input ⇒ no error, effective target reported), `TestMaster_PresetTable`, `TestMaster_Length` (frames equal), `TestMaster_Deterministic` (two runs equal; golden `pcm_sha256` per preset from a synthetic input in `testdata/hifi_master.sha256`).

### C220. `widen` — stereo widening that stays mono-compatible (lane H1)

1. **Parameters:**

| Parameter | Default | Range / rule |
|---|---|---|
| `source_id` | required | |
| `width` | 1.3 | 1.0…2.0, step 0.05; gain on the side signal (`1.0` = unchanged) |
| `decorrelate` | 0.10 | 0…0.5, step 0.05; amount of a flat-magnitude all-pass copy of the **mid** added to the side (pseudo-stereo for near-mono sources) |
| `bass_mono_hz` | 120 | `0` (off) or 60…250: the side signal is high-passed here (a 4th-order Linkwitz–Riley), so the bass stays centred (tabla, drone, kick; vinyl- and club-safe) |
| `target_lufs` | the D-T1-14 rule | not a parameter (the parent's loudness) |

2. **Algorithm:** `M = (L+R)/2`, `S = (L−R)/2`; `S₁ = width·S + decorrelate·AP(M)` with `AP` = four cascaded Schroeder all-pass sections (gain 0.5, delays 3.1, 4.7, 7.3, 11.1 ms rounded to integer samples; magnitude exactly flat); `S₂ = HP_LR4(S₁)` at `bass_mono_hz`; `L' = M + S₂`, `R' = M − S₂`; then `safety.Finish`. **Mono compatibility holds by construction:** `(L'+R')/2 = M` exactly (the side cancels), so the mono fold-down of the result is the parent's mono fold-down (up to the one linked Finish gain). There are no inter-channel delays (no Haas, no comb filtering). When every parameter is neutral (`width 1.0`, `decorrelate 0`, `bass_mono_hz 0`) the operator returns the input unchanged.
3. **Correlation bounds (fixed policy, the D-T1-14 pattern: derived, never failed).** Inter-channel correlation ρ is the Pearson coefficient of raw `L`, `R`, measured (a) over the whole programme and (b) per 400 ms block at a 100 ms hop, using only blocks above −60 dBFS RMS; `ρ_p10` is the 10th percentile of the block values (nearest rank). The render computes a strength `s ∈ [0,1]` scaling both `(width − 1)` and `decorrelate` and finds the largest `s` (bisection, ≤ 8 iterations, deterministic) with: **global ρ_out ≥ min(0.30, ρ_in)**, **`ρ_p10_out ≥ min(0.05, ρ_p10_in)`** (no sustained out-of-phase passages), and **bass-band (< `bass_mono_hz`) ρ_out ≥ 0.98** whenever `bass_mono_hz > 0` (by construction). The result `strength_effective`, `ρ_in`, `ρ_out`, `ρ_p10_out` and `monofold_delta_lu` (loudness change of the mono fold-down against the parent's, after Finish) are recorded. A parent already wider than the bound gets `s = 0` (a pass-through, never *narrowed*): warning `widen_already_wide` (ρ_in < 0.30) or `widen_out_of_phase_input` (ρ_in < 0); `L = R` input with `decorrelate = 0`: `widen_mono_source`; `widen_strength_reduced` when `s < 1`; `widen_monofold_quieter` when `monofold_delta_lu < −1.5`; `after_binaural`.
4. **Tests (H1):** `TestWiden_MonoFoldIdentity` (random stereo: `max|(L'+R')/2 − M| < 1e-12` at op level; after Finish the fold-down differs from the parent's by a constant gain only, ± 0.05 dB), `TestWiden_NeutralIsIdentity`, `TestWiden_BassMono` (80 Hz hard-left sine: side content ≥ 40 dB down, ρ_bass ≥ 0.98; a 2 kHz hard-left sine keeps its side), `TestWiden_AllpassFlat` (|AP| = 1 ± 1e-9 at 64 frequencies), `TestWiden_CorrelationBounds` (table: ρ_in 0.9, 0.6 ⇒ ρ_out ≥ 0.30 with `strength_effective < 1` for width 2.0; ρ_in 0.20 ⇒ pass-through + `widen_already_wide`; ρ_in −0.5 ⇒ pass-through + `widen_out_of_phase_input`; every row also asserts `ρ_p10_out`), `TestWiden_MonoSource` (L = R: `decorrelate 0` ⇒ unchanged + warning; `decorrelate 0.3` ⇒ ρ_out ≥ 0.30, fold-down = input), `TestWiden_Bisection_Deterministic`, `TestWiden_Length`, `TestWiden_Deterministic` (golden per parameter set, `testdata/hifi_widen.sha256`).

### C221. `binaural` — HRTF headphone placement (lane H2; HRTF data gated by P.5-G4, C222)

1. **Parameters:**

| Parameter | Default | Range / rule |
|---|---|---|
| `source_id` | required | not a `binaural` Take or descendant (`already_binaural`, C218.5) |
| `model` | `hrtf` | `hrtf` (measured set, C222) · `parametric` (C221.3; also the automatic fallback if P.5-G4 yields no dataset, then the default flips) |
| `preset` | `studio` | the virtual loudspeaker pair: `studio` ±30° · `wide` ±45° · `near` ±22° |
| `left_az_deg`, `right_az_deg` | the preset's | −90…−10 and +10…+90, integers, step 1 (azimuth 0 = front, positive to the right); snapped to the table (below) and the **snapped values are what the spec records** |
| `elevation_deg` | 0 | 0…30, integer; `hrtf` snaps to the nearest measured elevation of {0, 15, 30}; `parametric` ignores it (warning `elevation_ignored`) |
| `room` | `studio` | `none` · `studio` (rt60 ≈ 0.3 s) · `hall` (rt60 ≈ 1.4 s): generated reverberation (C206 machinery, two new presets in `ir/presets_hifi.go`), never a dataset |
| `room_mix_db` | −24 | −40…−12, step 1 (ignored for `none`) |
| `tail_s` | 1 | 0…10 (cut where the room tail falls below −70 dB; `0` or `room: none` ⇒ output length = input length) |

   "Source positions" means exactly these two virtual loudspeakers (the left and right channels of the mix) plus the room; a full mix has no other sources.
2. **HRTF model (`model: hrtf`).** For each speaker, ipsilateral and contralateral impulse responses (256 taps at 48 kHz, the committed table, C222) are taken at the snapped direction; the table stores one ear and the other is **mirrored** (`h_left(az) = h_right(−az)`), which guarantees L/R symmetry. At an output rate ≠ 48 000 Hz the responses are resampled once by `dsp.Resample` (recorded as `hrir_resampled_from`). Per ear: `yL = L ∗ h_L←L + R ∗ h_L←R`, `yR = L ∗ h_R←L + R ∗ h_R←R`, by `dsp.ConvolveUP` (C206.5; four convolutions). The table was **diffuse-field equalised and common-delay-aligned at build time** (the same delay removed from every response, so interaural time differences are preserved and left and right stay in sync). The output then goes through the room (C221.4) and `safety.Finish`.
3. **Parametric model (`model: parametric`; no dataset, no licence).** Woodworth interaural time difference `τ = (a/c)(θ + sin θ)` with `a = 0.0875 m`, `c = 343 m/s`, `θ` = azimuth folded to ±90°, quantised to 1/64 sample and applied by a 16-tap fractional delay to the far ear; **head shadow** per ear by the Brown–Duda one-pole/one-zero model `H(s) = (1 + αs/2ω₀)/(1 + s/2ω₀)`, `ω₀ = c/a`, `α(θ_ear) = 1.05 + 0.95·cos(θ_ear·π/150°)` discretised by the bilinear transform with closed-form coefficients; no pinna cues, no elevation, no distance. It gives **lateralisation, not externalisation** (stated in the capabilities text; warning `binaural_parametric_basic` always).
4. **Room and length.** `room ≠ none`: `ir.Build` (unit-energy stereo pair, seed from the spec) convolved with `(yL, yR)` by `ConvolveUP`, mixed at `room_mix_db` (`out = y + 10^(db/20) · wet`); the room tail is cut where it falls below −70 dB and at `tail_s`. Output length = `len(x) + tail_samples` exactly (`BinauralLen`); the HRIR convolution tail beyond that is dropped under the final 10 ms fade of Finish. `room: none` ⇒ `tail_samples = 0`.
5. **Reported and warned:** `model`, `az_used`, `el_used`, `hrtf {dataset, subject, table_sha256_12, licence, credit}` (null for `parametric`), `room`, `ear_balance_db` (`10·log10(E_L/E_R)` of the output vs the parent's, after Finish). **Always present** (estimate, view, `transform.json`, UI): `binaural_headphones_only`, `binaural_mono_incompatible`. Conditional: `binaural_ear_imbalance` (|Δ ear balance| > 3 dB while the parent was within 1 dB), `after_binaural` (not applicable: refused), `elevation_ignored`.
6. **Safety.** Level restored to the parent's loudness by Finish (an HRTF colours and attenuates the mix); the ceiling is −1.0 dBTP; contralateral summing cannot clip because the limiter is linked and runs last. The Take is labelled `listening: "headphones"` in the capabilities and carries the UI chip "headphones"; the shipped players apply no spatial processing, and the plan records that no player may stack one on a binaural Take (device-checklist row, C224).
7. **Tests (H2; each ≤ 1 s at 16 kHz, table tests at 48 kHz are the media tier):** `TestBinaural_ITD` (a click on the right speaker only at +90°: the left-ear arrival lags the right-ear arrival by 0.55–0.75 ms, i.e. 26–36 samples at 48 kHz; `parametric` matches Woodworth within 1 sample), `TestBinaural_ILD` (4 kHz noise at +90° ⇒ contralateral at least 8 dB below ipsilateral; `parametric` within the Brown–Duda curve ± 1.5 dB), `TestBinaural_Symmetry` (swapping L/R input swaps the output channels exactly, `az_l = −az_r`), `TestBinaural_CentreImage` (L = R input ⇒ ear signals' correlation ≥ 0.7 at ±30°), `TestBinaural_Snap` (az 31 ⇒ the table's nearest, recorded; elevation snap), `TestBinaural_Length` (`len + tail_samples` exactly, `room: none` ⇒ equal), `TestBinaural_AlreadyBinauralRefused` (direct parent and a two-hop ancestor), `TestBinaural_RoomOff_Dry`, `TestBinaural_Resampled44k1` (table resampled; ITD unchanged ± 1 sample), `TestBinaural_EarBalanceReported`, `TestBinaural_Deterministic` (golden `pcm_sha256` for `hrtf/studio/none`, `hrtf/wide/studio`, `parametric/studio/hall`, `testdata/hifi_binaural.sha256`).

### C222. HRTF dataset, licence, table build (**P.5-G4 gates H2b**; verified 2026-11-03 against the sources named, by fetching the pages; this is a reading of public terms, not legal advice — the operator reads the licence text at G4)

1. **Requirements.** The repository is **public and MIT**; the committed artefact must be redistributable with a notice; no non-commercial (NC) and no share-alike (SA) terms; the operator's rendered audio (derived Takes) must be free to keep and share. The raw dataset is **never committed** (gate patterns `*.sofa`, `*.hrir.zip`, `*.hrirs` added in F1); only a small derived table is.
2. **Findings.**

| Dataset | Licence / terms found | Verified how | Fit |
|---|---|---|---|
| **SADIE II** (Univ. of York) | **Apache-2.0** ("Licensed under the Apache License, Version 2.0"); "the original dataset must be referenced whenever used in original or modified form" | project database page, 2026-11-03 | **Fits** a public MIT repo with a NOTICE; derived audio unrestricted; offers dummy-head subjects; WAV and SOFA delivery to be confirmed in X0 (WAV avoids an HDF5 reader) |
| **HUTUBS** (TU Berlin) | **CC BY 4.0** | DepositOnce record and documentation (cite Brinkmann et al., JAES 2019) | **Fits** with attribution and "changes indicated"; distributed as SOFA only (HDF5): convert offline with an external tool; 96 human subjects plus a dummy head |
| **MIT KEMAR** (Gardner & Martin, 1994) | No named licence. "Copyright 1994 by the MIT Media Laboratory … provided free with no restrictions on use, provided the authors are cited" | MIT Media Lab resource page, 2026-11-03 | Derived audio: fine. **Redistributing the data or a derived table in the repo is not expressly granted** (a copyright notice plus a permission to *use*): usable only with the operator's acceptance of that residual risk, citing the authors; plain WAV/raw files, no HDF5 |
| MIT KEMAR as SOFA (`sofacoustics.org` copies) | **Licence not found**: the directory lists two files (`mit_kemar_{normal,large}_pinna.sofa`), no README (the README URL returned 404) | fetched | **Unverified — do not use**; use the original MIT archive if KEMAR is chosen |
| ARI, CIPIC, LISTEN (IRCAM), TH Köln (KU100) | Not read in this revision; believed to carry NC (ARI), research-use (CIPIC, LISTEN) or share-alike (TH Köln) terms | not verified | **Not used** unless X0 reads the exact text and it passes C222.1 |

3. **Recommendation (default for G4):** **SADIE II, one dummy-head subject** (Apache-2.0; X0 names the subject and confirms the WAV files and any NOTICE text). Alternative: HUTUBS dummy head (CC BY 4.0). KEMAR sound preferred ⇒ MIT's original archive with the residual-risk acceptance recorded. **If none is accepted:** `binaural` ships **parametric only** (C221.3: Woodworth ITD + Brown–Duda head shadow, no table, no licence; H2b disappears, about −1.0k) and `model` defaults to `parametric`.
4. **Table build (offline, once; reproducible).** `tools/hrtfgen` (`//go:build ignore`, run by hand, not in CI) reads the dataset's WAV responses at the needed directions (azimuth −180…180 step 5°, elevations {0, 15, 30}, snapped from the nearest available), removes the common onset delay, diffuse-field equalises (inverse of the energy-averaged magnitude over all directions, smoothed 1/3 octave, limited ±12 dB), truncates to 256 taps at 48 kHz with a raised-cosine tail, quantises to int16 Q15 and writes `internal/dsp/hrtf/table.q15` (≤ 128 KiB, `go:embed`) plus a header recording the source file hashes, subject, dataset, licence id and credit string. `internal/dsp/hrtf/NOTICE` holds the licence notice and attribution; the README third-party section gets a row; the credit string is written into every binaural recipe (`hrtf` block) and `transform.json`. Tests: `TestHRTF_TableHash` (sha256 pinned), `TestHRTF_TableShape` (size, ranges, no NaN-equivalents, symmetric directions present), `TestHRTF_DiffuseFieldFlat` (energy-averaged magnitude within ±1 dB, 200 Hz–10 kHz), `TestHRTF_NoticePresent` (NOTICE exists and names the dataset and licence id; gate runs green), `TestGate_NoRawHRIRCommitted`.

### C223. Estimate, cost, capabilities, API and errors for the H kinds

1. **Units and memory (C209 extended; constants recalibrated at X0 step 5):** `master` `frames·ch·60 + safety` (biquads 5·4, compressor 30, detector); `widen` `frames·70 + safety` (M/S, four all-pass sections, LR4, correlation passes); `binaural` four `ConvolveUP` (C206.5, 256 taps) + two for the room (IR length) + `10·frames` + (HRIR resample `2·zeros·taps·4` when rate ≠ 48 000) + safety. Memory factor `k`: `master` 1, `widen` 1, `binaural` 3. Output seconds: `master`/`widen` = input; `binaural` = input + `tail_s` at most. `TestCost_Monotonic` covers the new kinds.
2. **API.** Create/estimate body members `master`, `widen`, `binaural` with the fields above (all optional except `source_id`; `null` = default; rule `kind_params_mismatch` unchanged). Capabilities: kind entries `master`, `widen`, `binaural` with parameter tables generated from the same Go tables as validation (`TestCapabilities_MatchesValidation`), their `blind_spots`, and two new optional fields per kind, `listening` (`"headphones"` for `binaural`, absent otherwise) and `dataset` (the credit string, `binaural` hrtf only). Required capabilities sentences (pinned by `TestCapabilities_HifiNotes`): "master processes the full mix; it cannot add detail"; "widen keeps the mono fold-down; a source that is already wide is left unchanged"; "binaural is for headphones only; it places the stereo pair in front of the head, not individual instruments; a binaural Take is never processed twice".
3. **Warning codes (estimate and view):** `master_heavy_compression`, `master_quiet_boost`, `master_target_lowered`, `master_input_clipped`, `widen_already_wide`, `widen_out_of_phase_input`, `widen_mono_source`, `widen_strength_reduced`, `widen_monofold_quieter`, `binaural_headphones_only`, `binaural_mono_incompatible`, `binaural_parametric_basic`, `binaural_ear_imbalance`, `elevation_ignored`, `after_binaural`. Estimate-time (from rows only, no audio read): `binaural_headphones_only`, `binaural_mono_incompatible`, `binaural_parametric_basic`, `elevation_ignored`, `after_binaural`; the rest are render-time (view and `transform.json`). **No new error codes**: `INVALID_TRANSFORM` rules `already_binaural`, `preset_unknown`, `az_order` (left ≥ right), `range` and the existing ones.
4. **`transform.json`** gains a `hifi` object: `master {preset, tilt_db, comp_amount, low_cut_hz, comp {mean_reduction_db, max_reduction_db}}`, `widen {strength_effective, rho_in, rho_out, rho_p10_out, monofold_delta_lu}`, `binaural {model, az_used, el_used, hrtf, room, ear_balance_db, hrir_resampled_from}` (only the member of the kind), stable keys, redacted like the rest; `TestNoSecretLeak` extended.
5. **Audit, quota, cancel, reuse, estimate-is-free** — unchanged and covered by the generic tests running over the registered kinds (`TestEstimate_CPUAndZeroCost`, `TestEstimate_WritesNothing`, `TestRegistry_T1Meta` (still seven ops)).

### C224. Goldens, acceptance, listening gates and docs for T1h

1. **Goldens (synthetic inputs, committed hashes, never audio):** `testdata/hifi_{master,widen,binaural}.sha256` (one `pcm_sha256` per preset or parameter set listed in C219–C221), `table.q15` hash (C222.4), the preset coefficient table (C219.4), the all-pass delay table. Rows are added to `TestDeterminism_Golden_AMD64` and `TestDeterminism_FMAOff` **through the kind registry** (`ops.Kinds()`), so the same two tests cover them with no edit to their files; `-update` after a deliberate `dsp_version` bump only.
2. **Property suite:** `TestProperty_AnyTransformIsSafe` ranges over registered kinds (H kinds in; 12 timelines per PR as before): finite samples, closed-form length (`MasterLen = len`, `WidenLen = len`, `BinauralLen`), `TruePeakDBTP ≤ −0.95`, loudness within ±1.5 LU of the effective target, equal hashes across two runs, plus `widen` ρ_out ≥ min(0.30, ρ_in) for every random input.
3. **Acceptance (B5 adds file `internal/acceptance/t1_hifi_test.go`, real child, `FixtureT1`):** `TestAcceptance_T1_HiFi_TakeShape` (each H kind: engine `dsp`, one input, mutation operator = kind, 24-bit stereo, mode 0444, parent unchanged), `TestAcceptance_T1_HiFi_LoudnessMatched` (output within 1.5 LU of the parent's LUFS, true peak ≤ −0.5 dBTP), `TestAcceptance_T1_HiFi_Chain` (master → widen → binaural creates three Takes, the third refuses a second binaural, `after_binaural` on a following master), `TestAcceptance_T1_HiFi_SameSpecReuses`, `TestAcceptance_T1_HiFi_JoinsLibrary` (Song membership, `origin=transform`, duel against the parent works).
4. **Listening gate X3h (operator, blocks no code; results in the private notes only):** (a) `master`: blind duels, level-matched by construction, of each of the four presets against the parent on three loved originals and one generated sibling — pick the default preset and the presets worth keeping; (b) `widen`: headphones and a phone speaker in mono — bass stays centred, nothing hollow; choose the default width; (c) `binaural`: headphones — image in front, centre phantom stable, no harsh colour; compare `hrtf` against `parametric` and choose the room preset. Outcomes change **constants only** (preset table, defaults): before any release this is free; after release a `dsp_version` bump.
5. **Docs (X1):** `docs/architecture.md` gets three public-safe lines (the three kinds, "headphones only", "no ML, deterministic"); `docs/runbook.md`: the dataset credit and NOTICE location, "regenerating the HRTF table" (`tools/hrtfgen`, offline); `docs/device-checklist.md`: a binaural Take is not played through any spatial or crossfeed processing.

### C225. What F1 freezes for T1h, and the registration seam (so H1/H2 edit no frozen or shared file)

This plan has one foundation lane (**F1**; the web scaffold is F1's step 8, there is no F2). Everything below goes into F1 (stubs and constants only, ~+0.15k lines on its 2.4k budget; watch the cap and move the web-scaffold step to a follow-up commit in the same PR if F1 exceeds 2.5k):

1. **Migration:** the `transforms.kind` CHECK lists **nine** kinds (`…,'ensemble','score_render','master','widen','binaural'`), written once in the same `CREATE TABLE` (C201 edited in this revision); no later rewrite. `TestSchema_T1`/`TestTriggers_Transform` cover the three new values. `transform_inputs.role` is unchanged (`source`).
2. **Library:** `enumOperator` gains `master`, `widen`, `binaural` (B9 row: 7 + 9); `validateTransformTake` accepts all nine operators and requires a `ParentTrackID` for the three new ones (`rootlessOperators` unchanged); `TestEnumOperator_T1` lists nine.
3. **API types (`types_t1.go`) and `make openapi`:** `Kind` constants, the three create/estimate body members with every field of C219–C221, capability fields `listening` and `dataset`, the warning-code constants of C223.3 and the rule-name constants — all frozen once so the generated `openapi.json`/`schema.d.ts` are regenerated **once**; the seven ops and their stubs are unchanged (no new `501` task).
4. **`transform` registration seam:** `internal/transform/kinds.go` (F1) defines `KindSpec{Kind; Validate func; Units func; Title func; Capabilities func; MemFactor float64; Warnings func}` and `RegisterKind(KindSpec)` (init-time, duplicate or unsorted registration panics in tests); B2's `validate.go`, `cost.go`, `title.go`, `capabilities.go` look kinds up through it, the T1a five register from B2's own files, and **H1/H2 register from their own files** (`kind_master.go`, `kind_widen.go`, `kind_binaural.go`). `TestKinds_RegistryComplete` asserts that the CHECK list, `Kind` constants, registry, `ops` registry and capabilities list are the same set.
5. **`dsp` seams:** `internal/dsp/ops/registry.go` (F1 stub, D5 consumes): `Register(kind string, fn RunFunc, units UnitsFunc)`, `Lookup`, `Kinds()`; D5's `run.go` dispatches **only** through it (no `switch`), `TestRun_Dispatch` iterates `Kinds()`; per-kind files register in `init()`. `safety.FinishIn` gains `CeilingDBTP float64` (0 ⇒ −1.0; validated −3.0…−1.0, never above −1.0) — a signature change D1 implements and `TestFinish_CeilingClamped` pins. `internal/dsp/dsptest` stereo generators (`Correlated(rho)`, `HardPanned`, `PinkStereo`) are H1's own file `dsptest/stereo.go`.
6. **Web contract (W1, no scaffold change):** the panel's segmented control and forms are generated from `capabilities.kinds` (not a hard-coded five); `listening` renders a chip, `dataset` renders the credit line, enum parameters render as buttons. `TestKindsFromCapabilities` (Bun) feeds a fixture with nine kinds.
7. **Gate and docs:** `*.sofa`, `*.hrir.zip`, `*.hrirs` added to the gate patterns (`TestGate_T1Patterns` extended; `table.q15` is allowed, `NOTICE` present); **no new `MM_*` variable** (README config table unchanged, `TestDocs_ConfigTableListsEveryEnv` stays green); `FixtureT1` gains one near-mono, one wide and one out-of-phase stereo Take (generated by `dsptest`, nothing committed).
8. **Textual edits made by this revision elsewhere:** C201 DDL CHECK (nine kinds), C202.2 `Kind` comment, C211.1 ("the nine kinds"), D-T1-25 (superseded for T1h), lane table, hot-spot table, acceptance mapping, definition of done, risks and P.5.

## Acceptance mapping (request → named tests)

| Request / design item | Named test(s) |
|---|---|
| a transform produces a new Take: engine `dsp`, recipe = spec + parent shas, Ledger edge, frozen 24-bit FLAC | `TestAcceptance_T1_TakeShape` (media tier, each of the five T1a kinds, and `score_render` in T1b, on `FixtureT1`: `engine = 'dsp'`; `recipes.request_json` canonical with `inputs[].master_sha256` equal to the parents' masters and `dsp_version`; `tracks.bits_per_sample = 24`, `channels = 2`, master mode 0444, `MM_TAKE_ID` tag present; one `mutations` row whose operator = kind, `inferred = 0`, `transform_id` set; `parent_track_id` = input 0, `root_track_id` = its root, `generation = parent + 1` (`score_render`: parent NULL, generation 0); `transform_inputs` rows in `ord` order; Ears Tier A rows exist) |
| masters stay frozen | `TestAcceptance_T1_MastersFrozen` (sha256 + mode + mtime of every parent master and of the output master unchanged by re-reading after the run and after `transform verify`; attempts to `UPDATE tracks SET master_sha256` still abort) |
| deterministic from the spec | `TestAcceptance_T1_Deterministic` (same spec twice in two scratch libraries ⇒ equal `pcm_sha256`; glacier with another seed ⇒ different) · `TestAcceptance_T1_SameSpecReusesTake` (second create ⇒ `reused: true`, HTTP 200, same take id, no new transform row, no new job-like work) · `TestAcceptance_T1_VerifyCLI` · `TestDeterminism_Golden_AMD64` · `TestDeterminism_FMAOff` · `TestMath_Golden` · `TestDSP_BannedMath` · `TestVerify_InconclusiveOnToolchain` · `TestResample_MixedRateInput` |
| **morph:** beat- and key-aligned transition of two Takes of the same lyrics in different styles | `TestMorph_TempoRamp` (clicks 92 → 128 BPM, 8 bars: transition length 17.77 ± 0.05 s; onset spacing in each bar within 2 % of `T(β)`) · `TestMorph_DownbeatsLand` (B's downbeats after the transition on the output bar grid ± 15 ms) · `TestMorph_EqualPower` (uncorrelated equal-RMS noises: RMS through the transition within ± 0.5 dB) · `TestMorph_KeyShift` (A 440 Hz sine, B 523.25 Hz, `semitones_b −3` ⇒ B's partial at 440 ± 3 cents at the end of the ramp's start region, gliding to 523.25 ± 3 cents by the end of the ramp) · `TestMorph_Length` · `TestBeatPhase_FourOnFloor` · `TestBeatPhase_OneDrop` · `TestBeatPhase_Drift_LocalRefine` · `TestResolve_DownbeatRequired` (no plan grid and no `downbeat_*_s` ⇒ 422 `ALIGNMENT_UNRESOLVED {quantity: "downbeat"}`; `"auto"` accepted with the `downbeat_auto` warning) · `TestMorph_Resolution` (declared 92 / measured 128 ⇒ `bpm_source` `declared`/`measured`, recipe holds the numbers) · `TestAcceptance_T1_MorphSameLyrics` (`FixtureT1`: `search_tracks lyrics_of` offers B; estimate → create → done; also_from edge) |
| **shepard:** endlessly rising construction | `TestShepard_PitchTrack` · `TestShepard_EnvelopeFlat` · `TestShepard_Down_Mirrors` · `TestShift_RampSine` · `TestAcceptance_T1_ShepardLoop` (output length exactly `duration_s`; audible loop seam absent: spectral flux at the loop seam ≤ the median flux) |
| **glacier:** 10–50× time-stretch into long ambient pieces | `TestGlacier_Len` · `TestGlacier_Sine` · `TestGlacier_Decorrelated` · `TestGlacier_SeedChangesAudio` · `TestGlacier_RejectsOutOfRange` (factor 9.99 and 50.01 ⇒ 422 `INVALID_TRANSFORM`, `length_s × factor` above the cap ⇒ `too_long`) · `TestAcceptance_T1_Glacier50x` (60 s source excerpt of 18 s ⇒ 900 s output within budget on the fake child; real child on the 16 kHz fixture) |
| **polytempo:** two Takes at different tempos converging on a shared downbeat | `TestPolytempo_Converge` (clicks 90 and 120 BPM, `converge_bar 12`: downbeats coincide at `Tc` within 10 ms and in the unison section |ΔtA − ΔtB| ≤ 10 ms; B's offset `o` satisfies `0 ≤ o < 60·bpb/L`) · `TestPolytempo_TooEarly` (`converge_too_early`) |
| **ensemble:** octave-stacked / formant-shifted vocal in a synthetic giant space (generated IRs) | `TestShift_Formant` · `TestEnsemble_Stack` (a 200 Hz harmonic source, preset voices ⇒ spectral peaks at the stack's pitches ± 3 Hz, relative levels within 1.5 dB of the gains) · `TestIR_RT60` · `TestIR_Deterministic` · `TestConvolveUP_MatchesDirect` · `TestEnsemble_TailLength` (output = input + effective IR length ≤ `tail_s`) |
| **(T1b) score render:** ABC → score-faithful audio with an orchestral soundfont | `TestABC_Table` · `TestABC_Accidentals` · `TestABC_YuE2Dialect` · `TestSMF_RoundTrip` · `TestSMF_TailEndOfTrack` · `TestSynth_TestSoundfont_Pitch` · `TestSynth_TailRendered` · `TestSynth_DryByDefault` · `TestAcceptance_T1_ScoreRenderFaithful` (an 8-note ABC melody at `Q:1/4=120` with the test soundfont, through the full service: each note's detected pitch within 15 cents and its onset within 25 ms of the score; total duration = 4 s of score + `tail_s` ± 10 ms; artifacts `score.abc`/`score.mid` stored with the spec's hashes; **no `scores` row**; engine `dsp`, root, generation 0) · `TestAcceptance_T1_ScoreFromTake` (`from_track_id` of a Take with `score.abc` renders it; YuE2 dialect inferred; the source appears as `also_from` role `score_source` and its `score.abc` sha256 is in the spec) · `TestScoreRender_SoundfontName` (unknown name ⇒ 422 `SOUNDFONT_UNKNOWN` listing the configured names; a path is never accepted) · `TestInsertTake_TransformScoreSource` · `TestScoreRender_DisabledWithoutSoundfonts` (empty `MM_SOUNDFONTS` ⇒ 503 `UNAVAILABLE {component: "soundfont"}`, capabilities omit the kind) |
| every transform is a registry op; estimate shows CPU seconds and $0; MCP-callable | `TestEstimate_CPUAndZeroCost` (every kind: `cost.micro_usd = 0`, `cpu.expected_ms > 0`, `cpu.max_ms ≥ expected`) · `TestEstimate_WritesNothing` · `TestEstimate_Deterministic` · `TestRegistry_T1Meta` (the seven rows of C212.1 verbatim) · `TestParity_T1` (REST = MCP = stdio for all seven, all through the generic face) · `TestAcceptance_T1_AgentFlow` (go-sdk MCP client with a write-scope PAT: `get_transform_capabilities` (generic) → `search_tracks lyrics_of` → `estimate_transform` → `create_transform` → `wait_for_transform` until `done` → `get_audit` shows create and terminal rows in order with `cost 0`; a read-only PAT is refused 403 on create) · `TestToolsList_Ceiling` (constant unchanged: no tool bytes added) |
| results join Songs/Records like any Take | `TestAcceptance_T1_JoinsLibrary` (a glacier of a Take in Song S: `take_songs` maps it to S; the Song page lists it; `search_tracks origin=transform` returns it and `origin=job` does not; `TestPick_TransformNeverNewest`; pinning it as Pick works; it can be pinned into a Record slot; `songs.jsonl` export is byte-identical to the export before; `/context` shows the `transform` block; a `score_render` gets its own Song) |
| CPU budget | `TestMeter_BudgetExceeded` (an operator given a budget of N units against a job needing 10 N stops within 1.1 N ⇒ `TRANSFORM_BUDGET_EXCEEDED`, no PCM written) · `TestCost_Table` · `TestCost_Monotonic` · `TestCreate_TooLarge` (output/memory/input/cpu/disk each ⇒ 422 with `limit`) · `TestChaos_ChildHangs` |
| concurrency limit and queue | `TestRunner_Concurrency1` (synctest, three queued jobs: never more than one `running`, FIFO order by `created_at`) · `TestRunner_RunsWithGenerationDisabled` · `TestRunner_LeaseRenewed` · `TestCreate_QueueFull429` (the 7th live transform overall ⇒ 429 with `Retry-After`; no per-principal limit) · `TestRunner_RecoverExpiredLease` · `TestRunner_OwnerFence` · `TestRunner_WakeOnCreate` |
| daily CPU quota | `TestQuota_DailyCPU` (synctest, agent cap 1 800 s: two jobs with `max_cpu_ms` 1 000 s ⇒ the second refused 429 `TRANSFORM_QUOTA_EXCEEDED` with `used_ms`, `reserved_ms`, `resets_at`; after 24 h it is accepted; a human principal has its own cap; unused reservation is replaced by `cpu_ms` at completion) · `TestQuota_ZeroCapDisables` · `TestQuota_CancelledQueuedReleases` (create then cancel five times ⇒ the cap is untouched; a failed-before-start row records `cpu_ms = 0`) · `TestQuota_AgentPoolSharedHumanUnaffected` (agent A exhausts the agent cap ⇒ agent B refused, a human still accepted) |
| cancellation | `TestCancel_Queued` (op cancels in its transaction; nothing ran) · `TestCancel_Running_FakeChild` · `TestAcceptance_T1_CancelRunning` (real child: killed ≤ 2 s after the op, row `cancelled` `CANCELLED_BY_USER`, no Take, work dir gone, no orphan process via `/proc` scan) · `TestCancel_AgentOnlyOwn` · `TestShutdown_RequeuesRunning` · `TestRestart_RunningRequeuedOnce` |
| output loudness safety | `TestLUFS_*` · `TestLimit_*` · `TestFinish_*` · `TestProperty_AnyTransformIsSafe` · `TestAcceptance_T1_LoudnessSafe` (all five T1a kinds on `FixtureT1`: the pipeline's own measurement: `true_peak ≤ −0.5 dBTP`, `|integrated − target_lufs_effective| ≤ 1.5 LU` (or `limited` recorded), `clipping.count = 0`, no `silence` flag; the requested target is the primary parent's measured LUFS and an extreme-crest fixture proves the target is **lowered, not failed**: `TestFinish_LowersTarget`) · `TestAcceptance_T1_LoudnessUnsafeRefused` (the bug alarm: a test hook forces +6 dB after `Finish` ⇒ `LOUDNESS_UNSAFE`, ingest discarded, no Take) |
| transforms never spend money or call a backend | `TestGuards_TransformsNeverSpend` · `TestAcceptance_T1_NoSpendRows` · `TestNoSecretLeak` (extended) |
| mother guard | `TestEstimate_RefusesTransformMother` · `TestNearestRenderableAncestor` · `TestTriggers_Transform` (draft parent) |
| hostile inputs | (T1b) `FuzzParseABC` (20 s in CI) · `TestCreate_ScoreTooLarge` (257 KiB ⇒ 422) · `TestCreate_PathNeverAccepted` (soundfont/score fields with `/`, `..`, NUL ⇒ 422, nothing executed) · `TestDspRun_Imports` · `TestFluidsynth_Argv` |
| served-op coverage and UI | `TestServedOps` (the seven ops) · `TransformPanel.test.ts` · `t1-glacier.spec.ts` · `t1-morph.spec.ts` · `t1-cancel.spec.ts` · `t1-score.spec.ts` · `t1-quota.spec.ts` |
| revision-2 review resolutions (library readers, stats, reuse, determinism) | `TestSummary_DspNoStyleTerms` · `TestSearch_StyleFilterSkipsDsp` · `TestFTS_DspNotIndexed` · `TestSearch_LyricsOf` (instrumental ⇒ [], dsp ⇒ [], CRLF/whitespace equal) · `TestStats_T1` · `TestTrackDetail_TransformNoGenerationOf` · `TestInsertTake_TransformScoreSource` · `TestInsertTake_JobUnchanged` · `TestCreate_DiscardedDoneRestored` · `TestCreate_CrossPrincipalReuse` · `TestCancel_CrossPrincipalReuse` · `TestSpec_IDFromHash` (goldens for `spec_hash`, `recipes.hash`, id) · `TestBridge_DuplicateAudio` · `TestArchitectureDoc_T1Exceptions` · `TestGate_PublicPlanCopyHasNoPrivateLayout` · `TestCapabilities_ShepardMuddyNote` · `TestLUFS_Sine` (oracles re-derived) |
| revision-3 re-baseline (engine sweep, features, derived chain, tape) | `TestEngineSweep_Dsp` · `TestFeatures_DspMeasuredOnly` · `TestOnset_PinnedForT1` · `TestCamelot_Inverse` (24 rows against `features.CamelotOf`) · `TestServe_TransformTakeMaster` · `TestImporterIgnoresTransformWork` · `TestBackup_IncludesTransformTakes` · `TestNoNewResolver_T1` · `TestRunner_RecoverExpiredLease` · `TestAcceptance_T1_JoinsLibrary` (tape row: a Record holding a transform Take exports a tape; no `TAPE_*` refusal) |
| revision-4 hi-fi/3D lanes (master, widen, binaural) | `TestMaster_*` · `TestFilt_*` · `TestWiden_MonoFoldIdentity` · `TestWiden_CorrelationBounds` · `TestBinaural_ITD` · `TestBinaural_Symmetry` · `TestBinaural_AlreadyBinauralRefused` · `TestHRTF_TableHash` · `TestCapabilities_HifiNotes` · `TestKinds_RegistryComplete` · `TestFinish_CeilingClamped` · `TestAcceptance_T1_HiFi_*` (C219–C224) |


## Lanes and tasks

**Order (stages).** Stage 0 spikes (T1-X0, spend-free, operator-assisted) → **Stage 1: T1-F1 foundation, alone** (migration + pure code, contract freeze with 501 stubs and regenerated `openapi.json`/`schema.d.ts`, web scaffold, fixture, gate patterns) → **Stage 2, parallel PR-sized lanes** (each under ~2.5k changed lines excluding generated files, each owning files per the table): D1 first inside the DSP group (D2, D3, D4 stack on D1's frozen `pcm`/`dsp.Meter`/FFT signatures, which F1 freezes as stubs), then D5; B2, B3, B6, B4, W1, I1 in parallel with them → Stage 3 (B5 acceptance/property/chaos, W3 e2e) → **INT-B** (Go integration: rebase, parity, `tools/list` unchanged check, op-count pin, amendments recorded) and **INT-W** (web integration: served-op coverage, e2e map) → **T1-X1** (align, docs, public copy) → **T1-X2** (production-copy check, spend-free) → **T1-X3** operator review (listening). T1b (S1–S3, W2, I1b, X3b) starts after the T1a PR is open and never blocks it. **T1h (revision 4): H1 starts when D1 has merged, H2a when D4 has merged, H2b when P.5-G4 is ticked; they run in parallel with D2–D5/B2–B6 (none uses the phase vocoder, so X0 step 4 does not gate them) and merge before B5.**

**Machine rules.** Whole-repo race suites only through `make` (serialised by flock, `MM_HEAVY_SLOTS`, 20 GiB cap via `systemd-run`); agents iterate with `make verify` and focused `scripts/go-test` runs (single-package `go test -race ./internal/transform/` is fine); no agent starts a second whole-repo suite; `internal/dsp/...` and `internal/score/...` are single-goroutine pure packages and run **without `-race`** in `make check`; nobody edits a running script.

### Lane table (T1a unless marked)

| Lane | Scope | Owns (create or edit) | Depends on | Est. size (changed lines, excl. generated) |
|---|---|---|---|---|
| X0 | spikes: Go math table and the `Log1p` decision (D-T1-20), ffmpeg 24-bit encode check, LUFS oracle values, phase-vocoder listening gate, cost calibration (T1b: fluidsynth, soundfont licences) | private notes; `testdata/lufs-oracles.txt`; later the `transform/cost.go` constants | — | < 0.3k |
| **F1** | **foundation, alone:** migration `NNNN_transforms.sql`; `config_t1.go` + README rows; library write path (`NewTake`, `validateTransformTake`, operators, `enumRule`), `InsertTransformRecipe`, `RequireRenderableMother`, summary/detail style skip, stats `job_id`; **engine-sweep** edits (C211.1c); `api` types and the seven registrations answering 501; `transform` skeleton; `dsp` freeze (`pcm.Buffer`, `dsp.Meter`, FFT/`Rand` signatures as stubs); `fixture_t1.go`; gate patterns; `TestImportGraph_T1`; web scaffold (router rows, `nav.ts` mapping, empty `Transforms.svelte`, generated client); `make openapi` | `internal/store/migrations/NNNN_transforms.sql`, `internal/store/*_t1_test.go`, `internal/config/config_t1*.go`, `README.md` (config table), `internal/library/{write.go,tracks.go,stats.go,snapshot.go,recipes_t1.go,mother_t1.go,types_t1.go}`, `internal/exports/source.go`, `internal/generation/plan.go` (guard call only), `internal/api/{types_t1.go,ops_transform_t1.go,stub.go,errors_t1.go}`, `internal/transform/{spec,limits,transform}.go`, `internal/dsp/{pcm,meter,fft,rng}` stubs, `internal/library/librarytest/fixture_t1.go`, `internal/gate` patterns, `.public-gate-patterns.example`, `internal/acceptance/t1_importgraph_test.go`, `web/src/lib/{router.ts,nav.ts}`, `web/src/routes/Transforms.svelte` (empty), `openapi.json`, `web/src/api/schema.d.ts`, `cmd/metamusic/main.go` (two `case` lines); **T1h freeze (C225):** nine-kind CHECK, nine operators, API types for `master`/`widen`/`binaural`, `internal/transform/kinds.go`, `internal/dsp/ops/registry.go`, `FinishIn.CeilingDBTP` stub, gate patterns | X0 steps 1–3; P.5-G1, G2, G3 | ~2.4k (+~0.15k for the T1h freeze) |
| D1 | `dsp` core: real FFT (own twiddles), `dsp.Exp/Log/Pow`, PCG, resampler, `ConvolveUP`, PCM24, `safety` (LUFS, true peak, limiter, `Finish`), `dsptest` | `internal/dsp/{meter,fft,mathx,rng,resample,conv}.go`, `internal/dsp/pcm/*`, `internal/dsp/safety/*`, `internal/dsp/dsptest/{signals,measure}.go` | F1 | ~2.4k |
| D2 | phase vocoder (`pv`: stretch, shift bank, formant warp) | `internal/dsp/pv/*` | D1 | ~1.6k |
| D3 | alignment: ramp, key shift, fold, beat phase on `analysis.OnsetEnvelope` (D-T1-20) | `internal/dsp/align/*` | D1 | ~1.0k |
| D4 | impulse responses, glacier, shepard, ensemble | `internal/dsp/ir/*`, `internal/dsp/ops/{glacier,shepard,ensemble,loop}.go` | D1, D2 | ~2.0k |
| D5 | morph, polytempo, dispatcher, report, units, render-child entry | `internal/dsp/ops/{morph,polytempo,run,report,units}.go`, `cmd/metamusic/cmd_dsprun*.go` | D2, D3, D4 | ~2.2k |
| B2 | spec building, resolution on `take_features`, validate, cost, estimate, create, reads, wait, capabilities | `internal/transform/{canonical,resolve,validate,cost,estimate,create,read,wait,hub,title,capabilities}.go` | F1 (D3 for `align` types) | ~2.4k |
| B3 | runner (row lease), supervisor, ingest bridge, encode, sweep/recover, quota, audit, verify | `internal/transform/{runner,supervisor,child,bridge,encode,sweep,quota,audit,verify}.go`, `internal/mediatool/run.go` (group mode only), `internal/app/t1.go` | F1; D5 for the real child (fake child before) | ~2.4k |
| B6 | library/lineage/search/export/features/sounds/picks amendments: `lyrics_of`, `origin=transform`, lineage `also_from`, `/context` block, engine-sweep tests, L1/L2/L3 amendments (C211.6), features measured-only (D-T1-24) | `internal/library/{search_t1,track_t1}.go`, `internal/lineage/tree.go`, `internal/api/ops_context.go`, `internal/features/load.go`, `internal/sounds/compute.go`, `internal/picks/derive.go`, `internal/export/select.go` | F1 | ~1.8k |
| B4 | operation handlers, parity cases, CLI (`verify`, `sweep`) | `internal/api/ops_transform_t1.go` (handlers), `internal/mcpgen/parity_t1_test.go`, `cmd/metamusic/t1_cli.go` | B2, B3 | ~1.5k |
| W1 | Transform panel on the Take page, `/transforms`, lineage `also_from` | `web/src/lib/{transformApi,transformForm,transformView}.ts` (+tests), `web/src/lib/components/Transform*.svelte`, `web/src/routes/{Transforms,Track,Lineage}.svelte` | F1 (generated client); B4 for live data | ~2.3k |
| I1 | Dockerfile/Makefile/CI: `GOAMD64=v1`, `make dsp`, dsp/score outside `-race`, weekly jobs, shard weights, e2e map, coverage floors | `Dockerfile`, `Makefile`, `.github/workflows/*`, `scripts/{ci-shard-weights.txt,ci-heavy-tests.txt,e2e-map.txt}`, `coverage.thresholds` | F1 | ~0.6k |
| B5 | acceptance, property, chaos, leak tests | `internal/acceptance/t1_*_test.go`, `internal/transform/{property,chaos}_test.go` | B3, B4, D5 | ~2.0k (tests only) |
| W3 | Playwright specs and fixtures | `web/e2e/{t1Api.ts,t1-*.spec.ts}`, `web/e2e/fixtures/t1.ts`, `internal/httpserver/e2e.go` hook | W1, B4 | ~1.0k |
| INT-B / INT-W | integration: rebase, op-count pin, parity, constants, served-op coverage | shared pins only | all lanes | < 0.5k each |
| X1 | align, docs, public copy | `docs/architecture.md`, `docs/runbook.md`, `docs/plans/<date>-t1-transforms.md`, roadmap row | INT | < 0.6k |
| X2 / X3 | production-copy check (read-only copy, spend-free) / operator listening review | — | X1 | — |
| T1b: S1 / S2 / S3 / W2 / I1b / X3b | ABC parser · SMF writer · synth adapter + soundfont set · `/transforms/score` · fluidsynth in the image · integration | `internal/score/*`, `internal/dsp/dsptest/sf2.go`, `web/src/routes/ScoreRender.svelte`, Dockerfile apt line | T1a PR open; P.5 soundfont box | ~1.6k / 0.6k / 1.4k / 0.6k / 0.2k |
| **H1** | **T1h:** `master` + `widen`: `dsp/filt` (biquads, shelves, LR4), compressor, correlation guard, all-pass, the two operators, kind registrations (validate, units, title, capabilities), stereo test generators, goldens (C219, C220) | `internal/dsp/filt/*`, `internal/dsp/ops/{master,widen,stereo}.go`, `internal/dsp/dsptest/stereo.go`, `internal/transform/{kind_master,kind_widen}.go`, `testdata/hifi_{master,widen}.*` | F1 (seams, `CeilingDBTP`), D1 | ~2.3k (if over 2.5k at the 70 % checkpoint split H1a master / H1b widen: disjoint files) |
| **H2** | **T1h:** `binaural`: **H2a** parametric model, fractional delay, room presets, operator, kind registration (C221); **H2b** `dsp/hrtf` table loader, snap, resample, `tools/hrtfgen`, `table.q15`, `NOTICE`, README third-party row (C222) | `internal/dsp/hrtf/*` (+ `table.q15`, `NOTICE`), `internal/dsp/ir/presets_hifi.go`, `internal/dsp/ops/binaural.go`, `internal/transform/kind_binaural.go`, `tools/hrtfgen/main.go`, `testdata/hifi_binaural.*` | F1, D1, D4 (`ir.Build`, `ConvolveUP`); **H2b: P.5-G4** | ~2.4k excl. the generated table (H2a ~1.4k, H2b ~1.0k; with no dataset H2b disappears) |
| B5 (+T1h) / W3 (+T1h) | `internal/acceptance/t1_hifi_test.go` (+0.3k) / `web/e2e/t1-hifi.spec.ts` (+0.2k, panel generated from capabilities) | own new files only | B3, B4, D5, H1, H2 / W1 | as stated |
| X3h | operator listening gate for the three kinds (C224.4) | — | H1, H2, X1 | — |

### Hot-spot ownership (a file has one owner per stage; everyone else asks the owner or waits for INT)

| Hot spot | Owner | Rule for others |
|---|---|---|
| `openapi.json`, `web/src/api/schema.d.ts` | F1 (freeze), B4 (final regenerate) | never hand-edit; a lane that changes a type asks B4 |
| `internal/api` registrations, `api/stub.go` task constants | F1 | a lane implements its operations in its **own** `ops_t1_<lane>_test.go` registration file (D-T1-26) |
| `internal/mcpgen/m2a_contract_test.go` (`toolsCeilingBytes`), `TestRegistry_OpCount` pin | INT-B | all-generic default leaves the ceiling unchanged; the op-count pin moves by +7 |
| `internal/library/write.go`, `tracks.go`, `stats.go` | F1 | B6 adds new files (`search_t1.go`, `track_t1.go`) and edits `tracks.go` only for the origin-filter line it owns after F1 merges |
| `cmd/metamusic/main.go` | F1 (two `case` lines) | D5 and B4 own their files only |
| `web/src/lib/{router.ts,nav.ts}`, `App.svelte` | F1 | W1 never edits them |
| `Dockerfile`, `Makefile`, `.github/workflows/*`, `scripts/*.txt`, `coverage.thresholds` | I1 | others request changes in their PR description |
| README configuration table | F1 | — |
| `docs/architecture.md`, `docs/runbook.md`, roadmap | X1 | lanes put text in their PR description |
| `internal/transform/*.go` | B2 (spec side), B3 (run side) | disjoint file lists above; `spec.go`/`limits.go`/`kinds.go` frozen by F1; H1/H2 add only `kind_<name>.go` files and register through `RegisterKind` |
| `internal/dsp/ops/run.go`, `units.go` | D5 | dispatch only through `ops.Register`/`Lookup` (F1); H1/H2 register from their own files and never edit these |

Every task: tests first, `make verify` green before hand-off, no `STUB(T1-xx)` of its own left, public-safe text only. Coverage floors are set by I1 at **measured − 1** after each lane lands (the draft's 90 % figures stay aims, not gates).
### Task T1-X0: spikes (Stage 0; operator-assisted where marked; nothing here spends money)

**Files:** private notes only, plus `internal/score/synth/testdata/fluidsynth-help.txt` (recorded `--help`, public-safe) and the constants in `internal/transform/cost.go` after calibration.
**Steps:** (1) **(T1b)** install fluidsynth ≥ 2.3 on the build host, record `--version` and `--help`; render a generated MIDI with the generated test SF2 to a 24-bit WAV by the C208.5 argv; render twice ⇒ identical bytes; measure real-time factor; (2) **(T1b, operator)** shortlists orchestral soundfonts, reads each licence
text and records the decision (the plan assumes only that the file is never redistributed and rendered audio may be kept privately); the chosen file's name and sha256 go into the private notes; (3) encode a 24-bit PCM file with the C210.6(4) ffmpeg argv, confirm `metaflac --show-bps` = 24 and `flac -t` passes through the M1 ingest; (4)
after T1-D2 lands: render three prototypes (stretch ×1.25 and ×0.8, shift ±3 semitones with and without formant correction) of a real Take; **operator listens** and records "acceptable / artefacts" — if the phase vocoder's quality is not acceptable, the fallback is a WSOLA time-stretch behind the same `pv.Stretch` signature (decided here, before D4/D5 start); (5) after T1-D5: calibrate `NSPerUnit`,
`decode_ms`, `encode_ms`, `ingest_ms`, `tool_ms` on the build host (three kinds, three lengths) and update C209 constants; (6) **determinism spike (T1a):** read the Go 1.27 `math` sources and confirm which of `Log`/`Log1p`/`Log2`/`Log10`/`Sin`/`Cos`/`Atan2`/`Floor`/`Sqrt` are pure Go or exact on amd64 and arm64 (**decides D-T1-20: whether `analysis.OnsetEnvelope`, which calls `Log1p`, can be used as shipped**), confirm the `GODEBUG=cpu.fma=off` name, build `dsp.Exp/Log/Pow` and run `TestMath_Golden` on two different amd64 CPUs (one with FMA); record the table; (7) **ffmpeg oracle:** record `ffmpeg ebur128` integrated values for the three sine cases of `TestLUFS_Sine` into `testdata/lufs-oracles.txt`; (8) **(T1b) fluidsynth behaviour:** render a note with a 1 s release SF2 and `tail_s` padding in the SMF (`TestSynth_TailRendered`), confirm `-q` silences stdout, record which config files the pinned version reads under the explicit minimal environment, confirm libsndfile's WAV header type (PCM vs EXTENSIBLE).
**Done:** notes written; `TestFluidsynth_Argv` golden agrees with the recorded help; cost constants updated by a commit that cites the measured table in the private appendix only.

### Task T1-F1 (Stage 1, alone): contract freeze — migration, config, library write path, engine sweep, types, 501 stubs, web scaffold, harness

**Files:** see the F1 row of the lane table. **Steps:** (1) verify `recipes.style` accepts `''` and `library.EngineDSP` fits next to the existing engine constants (else the migration documents the replacing constant); (2) migration + schema tests; (3) config + README rows; (4) library write path and guards; (5) the engine sweep (C211.1c); (6) registrations answering 501 `NOT_IMPLEMENTED` with `details.task`; (7) `make openapi`, commit both generated files; (8) web scaffold; (9) fixture, gate patterns, import-graph test.
**Tests first:** `TestSchema_T1` (tables, columns, indexes, triggers exist; `PRAGMA foreign_key_check` empty on the L3 fixture), `TestTriggers_Transform` (every trigger of C201 refuses its bad row and accepts its good one: shape, dsp recipe shape, draft parents, inputs master/lossless, state machine incl. `done → running` refused, terminal immutability, no delete, live-spec uniqueness with a `failed` row not blocking a new one, the running-row owner/lease CHECK), `TestUpgrade_T1` (a database at each earlier schema — the 0012 fixture and the earlier ones — migrates in one boot; counts unchanged), `TestConfig_T1` (defaults from C200; each out-of-range value rejected naming the variable; `MM_SOUNDFONTS` parsing table), `TestDocs_ConfigTableListsEveryEnv` (existing, must stay green), `TestInsertTransformRecipe`, `TestInsertTake_Transform*`, `TestRequireRenderableMother`, `TestRegistry_T1Meta`, `TestOps_StubsAnswer501T1`, `TestImportGraph_T1`, `TestGate_T1Patterns`, `TestEnumOperator_T1`, `TestSummary_DspNoStyleTerms`, `TestSearch_StyleFilterSkipsDsp`, `TestFTS_DspNotIndexed`, `TestInsertTake_TransformScoreSource`, `TestInsertTake_JobUnchanged`, **`TestEngineSweep_Dsp`** (C211.1c).
**Done:** `make check` green; stubs registered so the served-operation and parity tests run (stub cases listed); `internal/library`, `internal/store` coverage at or above the committed floors; migration number is whatever is next free at build time.

### Task T1-D1 (Stage 2, dsp, first of the DSP group): PCM, FFT, resampler, meter, loudness, limiter, safety, test signals

**Files:** `internal/dsp/{meter.go,fft.go,mathx.go,rng.go,resample.go,conv.go}`, `internal/dsp/pcm/{buffer.go,pcm24.go,wav.go (T1b)}`, `internal/dsp/safety/{lufs.go,truepeak.go,limit.go,finish.go}`, `internal/dsp/dsptest/{signals.go,measure.go}` + tests; **no `analysis` edit** (the onset envelope is the existing export, D-T1-20).
**Tests first:** `TestFFT_RoundTrip` (incl. 2²¹), `TestFFT_Parseval`, `TestFFT_MatchesAnalysisFFT`, `TestExp_Accuracy`, `TestLog_Accuracy`, `TestPow_Accuracy`, `TestMath_Golden`, `TestDSP_BannedMath`, `TestDSP_RandOnlyPCG`, `TestResample_Sine`, `TestResample_Length`, `TestResample_MixedRateInput`, `TestPCM24_RoundTrip` (a ramp of 2²⁴ values survives write→read exactly; clamp at ±(2²³−1); `pcm_sha256` of a fixed 8-sample vector equals the committed
literal), (T1b) `TestWAV24_Read` (a hand-built header with `LIST` chunk and odd-sized chunk padding, in both `WAVE_FORMAT_PCM` and `WAVE_FORMAT_EXTENSIBLE` forms), `TestMeter_Budget`/`_Context`, `TestConvolveUP_MatchesDirect`, all `TestLUFS_*` (with the re-derived oracles), `TestLimit_*`, `TestFinish_*` of C207 (incl. `TestFinish_LowersTarget`), `TestDsptest_Peak` (a 1 000.0 Hz sine ⇒ 1 000 ± 0.05 Hz), `TestDsptest_Clicks` (spacing equals 60/bpm ± 1 sample).
**Done:** `internal/dsp` ≥ 90 %, each test ≤ 1 s at 16 kHz; `TestOps_NoGoroutinesNoClock` covers the package.

### Task T1-D2 (Stage 2, dsp): phase vocoder — stretch, shift bank, formant warp

**Files:** `internal/dsp/pv/{stft.go,peaks.go,lock.go,stretch.go,shift.go,formant.go}` + tests.
**Tests first:** `TestStretch_Length`, `TestStretch_Sine`, `TestStretch_Identity`, `TestStretch_Clicks`, `TestStretch_VariableAlpha` (α ramping 1 → 2 over the signal: a 2 Hz click train's spacing at 25/50/75 % follows `α(τ)·0.5 s` within 5 ms), `TestShift_Sine`,
`TestShift_RampSine`, `TestShift_Formant`, `TestShiftBank_MatchesShift` (a bank of one voice equals `Shift` bit for bit; a bank of three equals the sum of three single shifts within 1e-9), `TestShift_NyquistGuard` (ρ = 8 on a 4 kHz tone at 16 kHz ⇒ silence above Nyquist, no NaN, no wrap), `TestPeaks_Table` (peak and region-of-influence detection on a
two-sine spectrum), `TestPV_Deterministic` (two runs ⇒ identical samples), `TestPV_Budget` (the meter stops it).
**Done:** coverage ≥ 90 %; the quality checkpoint of T1-X0(4) is scheduled with the operator.

### Task T1-D3 (Stage 2, dsp): alignment — ramp, key shift, fold, beat phase

**Files:** `internal/dsp/align/{ramp.go,key.go,fold.go,beat.go}` (+ `onset.go` only if X0 step 6 says `Log1p` is not usable, D-T1-20) and tests.
**Tests first:** `TestRamp_Values`, `TestRamp_Roundtrip`, `TestKeyShift_Table`, `TestFold_Table`, **`TestOnset_PinnedForT1`** (golden hash of `analysis.OnsetEnvelope` on a fixed synthetic signal), `TestBeatPhase_Clicks` (120 BPM, 4/4, first beat 0.31 s, accent on beat 2 of the first bar ⇒ `FirstDownbeatS` = 0.31 + 0.5 ± 0.015; no accent ⇒ beat phase 0.31 ± 0.015 and `confidence ≥ 3`; **tolerances re-derived for the fixed hop of 128 samples**: 8 ms per frame at 16 kHz, 2.7 ms at 48 kHz, interpolated to a quarter frame), `TestBeatPhase_Noise` (white noise ⇒ confidence < 1.5), `TestBeatPhase_FourOnFloor` (`downbeat_ambiguous`), `TestBeatPhase_OneDrop`, `TestBeatPhase_Drift_LocalRefine` (0.4 % off the declared BPM over 150 s ⇒ beats within 15 ms at a transition at 140 s), `TestBeatPhase_TempoChangeInWindow`, `TestGridBar`.
**Done:** coverage at the measured floor; every test ≤ 1 s at 16 kHz.

### Task T1-S1 (Stage 4, **T1b**, score): note-event model and ABC parser

**Files:** `internal/score/{score.go,tempo.go}`, `internal/score/abc/{lexer.go,header.go,body.go,repeats.go,dialect.go,errors.go}` + tests and `testdata/*.abc` (original synthetic tunes only, public-safe).
**Tests first:** `TestABC_Table`, `TestABC_Errors`, `TestABC_Accidentals`, `TestABC_YuE2Dialect`, `TestABC_Repeats`, `TestABC_TempoMap`, `TestABC_Voices`, `TestParseABC_MatchesParseScoreBars`, `TestScore_SecondsAt` (tempo map 120 → 60 at tick 1 920 ⇒ 2.0 s and 5.0 s at ticks 1 920 / 3 840), `FuzzParseABC` (CI runs it for 20 s; longer weekly).
**Done:** coverage ≥ 90 %; the fuzz target found nothing in 5 minutes locally.

### Task T1-S2 (Stage 4, **T1b**, score): Standard MIDI File writer (the reader is test-side only)

**Files:** `internal/score/smf/{write.go,vlq.go}` + tests with a test-side decoder, `testdata/score.golden.hex`.
**Tests first:** `TestSMF_RoundTrip`, `TestSMF_Golden`, `TestSMF_NoteOffBeforeOn`, `TestSMF_TailEndOfTrack`, `TestSMF_Limits` (the writer refuses more than `MaxScoreEvents` events or 20 min).
**Done:** coverage ≥ 90 %.

### Task T1-S3 (Stage 4, **T1b**, score): synth adapter, soundfont set, test soundfont writer

**Files:** `internal/score/synth/{synth.go,fluidsynth.go,set.go}`, `internal/dsp/dsptest/sf2.go`, tests, `testdata/fluidsynth-help.txt`.
**Tests first:** `TestFluidsynth_Argv` (golden; absolute paths only; no `;`/`|`/shell in any element; gain/rate formatted exactly), `TestSet_*`, `TestSF2_Writer` (the generated file parses back through a minimal RIFF reader in the test: one preset, one instrument, one sample of the expected length and loop points),
`TestMediatool_GroupKill`, media tier `TestSynth_TestSoundfont_Pitch`, `TestSynth_Deterministic`, `TestSynth_TailRendered`, `TestSynth_DryByDefault`, `TestSynth_NoUserConfig`, `TestSynth_Timeout`, `TestSynth_MissingBinary` (a `TOOL_UNAVAILABLE`-class error, not a panic).
**Done:** media tier green with `MM_REQUIRE_MEDIA_TOOLS=1`; the helper `dsptest.WriteSF2` is used by every score test.

### Task T1-D4 (Stage 2, dsp): impulse responses, glacier, shepard, ensemble

**Files:** `internal/dsp/ir/{build.go,presets.go,erl.go}`, `internal/dsp/ops/{glacier.go,shepard.go,ensemble.go,loop.go}` + tests.
**Tests first:** all of C206.6, `TestGlacier_*`, `TestShepard_*`, `TestEnsemble_Stack`, `TestEnsemble_TailLength`, `TestEnsemble_PanLaw`, `TestIR_Golden` (constant power: `L² + R² = 1 ± 1e-9` for every pan), `TestLoop_Seamless` (the 30 ms seam crossfade keeps the loop's boundary discontinuity < −40 dB re RMS), `TestOps_GlacierStreamingMemory` (a 900 s output uses < 64 MiB of heap: `runtime.MemStats`).
**Done:** coverage ≥ 90 %.

### Task T1-D5 (Stage 2, dsp): morph, polytempo, dispatcher, render child entry, cost units

**Files:** `internal/dsp/ops/{morph.go,polytempo.go,run.go,report.go,units.go}`, `cmd/metamusic/{cmd_dsprun.go,cmd_dsprun_test.go}` + tests.
**Tests first:** all `TestMorph_*` and `TestPolytempo_*` of the acceptance table, `TestOps_Signature`, `TestRun_Dispatch` (every kind, unknown kind ⇒ error), `TestUnits_Table` (units of one fixed job per kind equal committed literals — the meter and the cost model share them), `TestDspRun_Protocol` (a job dir for glacier ⇒ exit 0, `out.pcm24`
length = 6 × frames bytes, `report.json` keys, progress lines monotone), `TestDspRun_Exit11_Budget`, `TestDspRun_Exit10_BadJob` (unknown kind, path traversal in a file name, `frames` larger than the file), `TestDspRun_Imports`, `TestDspRun_RelativeDirRefused`.
**Done:** coverage ≥ 90 %; T1-X0(4) and (5) can run.

### Task T1-B2 (Stage 2, core): spec building, resolution, estimate, create, reads

**Files:** `internal/transform/{spec.go,canonical.go,resolve.go,validate.go,cost.go,estimate.go,create.go,read.go,wait.go,hub.go,title.go,capabilities.go}` + tests.
**Tests first:** `TestSpec_Canonical_Golden` (bytes and hash of one spec per kind committed as `testdata/spec_<kind>.json` + `.sha256`), `TestSpec_DefaultsWrittenOut`, `TestSpec_IDFromHash` (goldens for **both** hashes and the id), `TestValidate_Table` (≥ 40 rows after the parameter cuts: each range of C202.3 at min, max and just outside; `kind_params_mismatch`; `same_input`; unknown fields rejected with the field name),
`TestResolve_Ladder` (tempo: explicit > plan > declared > measured with the source recorded; confidence 0.29 rejected, 0.30 accepted; octave fold only for `measured` + ambiguous; nothing ⇒ `ALIGNMENT_UNRESOLVED` naming `bpm_a`; key unknown ⇒ shift 0 + warning), `TestResolve_Sections` (first row: a lyrics `plan` section with `start_s IS NULL` ⇒ `section_unavailable`, the common case; `chorus#1` resolves from a `measured` section; a plan section with a bar index resolves only with a plan bar grid), `TestResolve_DownbeatRequired`,
`TestEstimate_*` (C209.5 and the acceptance rows), `TestCost_*`, `TestCreate_OrderOfChecks`, `TestCreate_HappyPath` (rows: recipe, transform `queued`, inputs, idempotency `done` and audit in the same transaction, `Wake` called once), `TestCreate_SameSpecReuses` (live and done), `TestCreate_DiscardedDoneRestored`, `TestCreate_CrossPrincipalReuse`, `TestCreate_AfterFailureNewRow`,
`TestCreate_InputQualifies` (reference ⇒ `RECIPE_UNKNOWN`, lossy ⇒ `LOSSY_MASTER`, transform input ok), `TestCreate_PathNeverAccepted`, `TestCreate_ScoreTooLarge`, `TestCreate_QueueFull429`, `TestQuota_DailyCPU` (with a fake clock; includes the "cancelled queued job releases its reservation" case), `TestQuota_AgentPoolSharedHumanUnaffected`, `TestWait_*` (changed, timeout, terminal, since_state default), `TestCapabilities_MatchesValidation`, `TestDefaultSeed`, `TestTitle_Table`
(rune-boundary cut at 200, `—` separator, explicit title wins).
**Done:** `internal/transform` ≥ 90 %.

### Task T1-B3 (Stage 2, core): runner (row lease), supervisor, ingest bridge, sweep/recover, quota, verify

**Files:** `internal/transform/{runner.go,supervisor.go,child.go,bridge.go,encode.go,sweep.go,quota.go,audit.go,verify.go}` + tests; `internal/mediatool/run.go` (opt-in process-group mode only; existing tools unchanged); `internal/app/t1.go` (wiring: service, runner started by `serve`, `Wake`, tools check). **Not edited:** `internal/jobs`, `runner_lease`, `store/lease.go` (D-T1-17).
**Tests first (fast tier with `FakeChild`/`FakeSynth`, `testing/synctest`):** `TestRunner_Concurrency1`, `TestRunner_RunsWithGenerationDisabled`, `TestRunner_LeaseRenewed`, `TestRunner_RecoverExpiredLease`, `TestRunner_OwnerFence`, `TestRunner_WakeOnCreate`, `TestRunner_FIFO`, `TestRunner_InterruptedRequeuedOnce`, `TestSweep_Startup` (running rows, stale work dirs, orphan `takes/<id>` removed by a `transform` helper that mirrors `ingest.Discard`), `TestQuota_*` (incl. `TestQuota_CancelledQueuedReleases`), `TestCancel_Queued`, `TestCancel_CrossPrincipalReuse`, `TestVerify_InconclusiveOnToolchain`, `TestBridge_DuplicateAudio`, `TestCancel_Running_FakeChild`, `TestCancel_AgentOnlyOwn`, `TestShutdown_RequeuesRunning`, `TestBridge_ErrorMapping`, `TestBridge_LoudnessUnsafe`, `TestBridge_InputChanged`, `TestSupervisor_Deadline`/`_RSSGuard` (fake `/proc` reader), `TestSupervisor_EnvMinimal`, `TestSupervisor_Progress`, `TestMediatool_GroupKill`; media tier (real child, real ingest): `TestAcceptance_T1_TakeShape` for `glacier` first, `TestEncode_24Bit`.
**Done:** `internal/transform` at the measured floor; `make verify` green.

### Task T1-B6 (Stage 2, library readers): search, lineage, context, engine sweep, L1/L2/L3 amendments

**Files:** see the B6 row. **Steps:** (1) re-grep every `origin` reader and every `engine <> 'unknown'` reader and append hits to the baseline table (B7, B12); (2) `lyrics_of` and `origin=transform` in `search_tracks`; (3) lineage `also_from` and transform nodes; (4) `/context` `transform` block; (5) C211.6 amendments (Sound compute through the nearest non-`dsp` ancestor; Pick never "newest"; features measured-only, D-T1-24) recorded in the L1/L3 plan copies; (6) export selection.
**Tests first:** `TestStats_T1`, `TestTrackDetail_TransformNoGenerationOf`, `TestSearch_OriginTransform`, `TestSearch_LyricsOf`, `TestContext_Transform`, `TestLineage_AlsoFrom`, `TestLineage_TransformNodes`, `TestLineage_ScoreRenderRoot`, `TestExport_SkipsDsp`, `TestSounds_DspInheritsFromAncestor`, `TestPick_TransformNeverNewest`, `TestFeatures_DspMeasuredOnly`, `TestGuards_TransformsNeverSpend`, `TestEngineSweep_Dsp` (extended), `TestAcceptance_T1_JoinsLibrary` (library part, tape row included).
**Done:** coverage at the measured floors of `library`, `lineage`, `picks`, `sounds`, `features`; amendments recorded in the plan copies.

### Task T1-B4 (Stage 2, core): operations, parity, CLI

**Files:** `internal/api/{ops_transform_t1.go (handlers), errors_t1.go}`; `internal/mcpgen/parity_t1_test.go`; `cmd/metamusic/{t1_cli.go,t1_cli_test.go}`; `openapi.json` and `web/src/api/schema.d.ts` (`make openapi`, commit both).
**Tests first:** REST test per op (happy path, 401/403 by scope, 404, every error code of C212.6 with its `details`), `TestParity_T1` (seven ops, REST = MCP = stdio, all seven reached through `call_operation`), `TestToolsList_Ceiling` unchanged (the constant 108300 must still hold: **no tool bytes added**; the test prints the measurement), `TestEstimate_CPUAndZeroCost`, `TestEstimate_WritesNothing`, `TestCreate_Status202And200`, `TestNullable_Schemas`, `TestCLI_TransformVerify`, `TestCLI_Sweep`, `TestAudit_T1`, `TestArchitectureDoc_T1Exceptions`.
**Done:** no `STUB(T1-B4)` left; `internal/api` coverage at the measured floor.

### Task T1-W1 (Stage 2, web): Transform panel on the Take page, `/transforms` under More, lineage `also_from`

**Files:** `web/src/lib/{transformApi,transformApi.test,transformForm,transformForm.test,transformView,transformView.test}.ts`, `web/src/lib/components/{TransformPanel,TransformForm,TransformEstimate,TransformCard}.svelte`, `web/src/routes/{Transforms.svelte,Transforms.test.ts,Track.svelte,Lineage.svelte}`. **Not edited:** `router.ts`, `nav.ts`, `App.svelte` (F1), `web/src/api/*` (generated).
**Tests first:** the Bun unit tests of C214.4; the existing ESLint network ban stays green; `lint`/`svelte-check` clean; the estimate debounce uses a fake timer. Svelte 5 runes only; dark only; every control ≥ 44 px.
**Done:** `make check` web part green; a phone-width pass recorded in `docs/device-checklist.md` (new rows: the panel is usable one-handed; progress and Cancel are reachable).

### Task T1-W2 (Stage 4, **T1b**, web): `/transforms/score`

**Files:** `web/src/routes/{ScoreRender.svelte,ScoreRender.test.ts}`, `web/src/App.svelte` (route, nav, shown only when capabilities list `score_render`).
**Tests first:** `ScoreRender.test.ts` (file ⇒ text body; 256 KiB guard); `lint`/`svelte-check` clean.
**Done:** `make check` web part green.

### Task T1-I1 (Stage 2, infra): Dockerfile, CI, make, gate, coverage, shard weights

**Files:** `Dockerfile` (`GOAMD64=v1` in the build stage; T1b: fluidsynth), `Makefile` (`dsp` target through `scripts/loop`, `GOAMD64=v1`, dsp/score outside `-race` in `check`), `.github/workflows/{ci,weekly}.yml` (go-fast dsp/score without `-race`; weekly `transform-bench` and `TestDeterminism_*`; T1b: `go-media` apt `fluidsynth`), `scripts/{ci-go-test.sh,ci-shard-weights.txt,ci-heavy-tests.txt,e2e-map.txt}`, `coverage.thresholds` (measured − 1), `.public-gate-patterns.example` (with F1), `deploy/compose.example.yml` (T1b soundfont volume placeholder).
**Tests first:** `TestGate_T1Patterns`, `TestGate_PublicPlanCopyHasNoPrivateLayout`, the `scripts/` selftests that cover shard weights and the e2e map (`ci-selftest.sh`, `e2e-cache-selftest.sh`), `TestFast_Budget` (weekly), coverage-gate dry run on the merged profile.
**Done:** CI green on the branch (all 20 required checks); added minutes per job measured and written next to the C215 estimates; shard weights refreshed.

### Task T1-B5 (Stage 3, core): acceptance, property, chaos, leaks

**Files:** `internal/acceptance/{t1_test.go,t1_agent_test.go,t1_rig_test.go}`, `internal/transform/{property_test.go,chaos_test.go}`, `internal/acceptance/nosecretleak_test.go` (extended), `internal/acceptance/names_test.go` (the `TestAcceptance_T1_*` names listed in the mapping exist).
**Tests first:** every `TestAcceptance_T1_*`, `TestProperty_*`, `TestChaos_*` of the mapping and C216.
**Done:** `make test` green with the media tools and fluidsynth; the property suite at 12 timelines per PR (300 weekly) and the chaos suite (PR subset, full weekly) green three times in a row under `-shuffle`; `TestNoSecretLeak` covers `transform.json`, `error_text`, audit rows and the work dir contents.

### Task T1-W3 (Stage 3, web): Playwright

**Files:** `web/e2e/t1-{glacier,morph,cancel,quota}.spec.ts` (+ `t1-score.spec.ts`, T1b), `web/e2e/fixtures/t1.ts` (fixture library with real child), `internal/httpserver/e2e.go` (a `/__e2e` hook to switch the fixture DSP duration scale; counters are never shared across specs, M2b/M2c lesson).
**Tests first:** the specs of C214.5 (red before the panel exists is not required, they gate the lane).
**Done:** `make e2e` green on chrome and firefox, `/__e2e/leaks` empty, zero unserved ui ops.

### Task T1-X1 (Integration and align)

Rebase on the then-current `main`; fill `NNNN`/`N` with the next free migration number; **re-check every name in the baseline reconciliation table and the E2 dependency table** (`go doc`, `rg`; this revision found a dozen differences by doing exactly that); update the roadmap (T1 row, order, `lyrics_of`, "no money"), copy the public part of this plan to `docs/plans/<date>-t1-transforms.md` with `scripts/l1-public-copy.sh` (cuts at the marker line; the copy must hold neither the marker nor `## Appendix P`) and run `make gate`; record decisions D-T1-1…26 in the private design document; run `make check`, `make test`, `make e2e`, `make gate`, `make routing` in CI; open the PR; GitHub CI green (read `gh pr checks` myself; a skipped required job is not green); merge via the merge queue; tag `v0.x-t1`.

### Task T1-X2 (Production-copy check — spend-free, read-only copy, host details only in the private notes)

Copy the production database read-only to a scratch directory (never the live file) and run the T1 binary's migrations; run `metamusic library reconcile` and `library verify`; **fixtures must have included** draft-only Songs, references, lossy imports and multi-part imports (the L3 X2 lesson) — check here that real Takes with those shapes neither break `estimate_transform` (422 with the right code, never a 500) nor the engine sweep. Run one glacier of a real Take on the scratch copy (estimate ≈ expected CPU; done within the budget); one morph of the two demo Takes with explicit `downbeat_*_s` (or `"auto"` and the warning read); `transform verify --all` matches (or reports `Inconclusive` with the reason); compare the calibration table with the build host's.

### Task T1-X3 (Operator review — listening)

The operator listens to the prototypes (phase-vocoder quality gate, X0 step 4) and to the demo results through the normal UI, rates them (stars/duels), and ticks the P.5 boxes that depend on the sound; results only in the private notes. (T1b: the score render demo, separate PR.)

## Definition of done (T1)

**T1a (merge gate):**
- All named tests above for the five DSP kinds green; `make check` < 30 s warm (dsp/score without `-race`); `make test` green with ffmpeg and flac; `make e2e` green (chrome + firefox) with zero unserved ui ops and `/__e2e/leaks` empty; `make gate`, `make routing` green; coverage floors met on merged data (`internal/transform ≥ 90` enforced once the T1-B3 fake-child tests exist).
- Parity: the seven ops have parity cases (REST = MCP = stdio after normalisation); the `tools/list` golden and the `toolsCeilingBytes` constant are unchanged (all seven operations are generic) and `TestToolsList_Ceiling` is green; the two CLI-only commands are recorded as documented exceptions (D-T1-18).
- Determinism: the golden `pcm_sha256` of one fixed spec per kind is committed and green on amd64 with `GOAMD64=v1`, equal under `GODEBUG=cpu.fma=off`; the toolchain is recorded in `transform.json`; `transform verify --all` matches on the demo library (or says `Inconclusive` and why); `dsp_version` bump procedure documented in the runbook.
- Safety and budgets: every transform on the fixtures meets D-T1-14 in the pipeline's own measurement (a loud render lowers its target, never fails); the budget, concurrency, queue, quota (cancel releases reservations), cancel, restart and chaos tests are green three times in a row; no transform ever writes a spend row.
- Engine sweep and derived chain: `TestEngineSweep_Dsp` and the C217 tests are green; no new resolver exists; parent masters are byte- and mode-identical after every run.
- Library: transform Takes appear in Songs, Records and Crates like any Take, are never auto-picked by recency, never become generation mothers, never appear in the YuE2 exports, never count in generation stats, never show style chips or match `lyrics_of`.
- No real titles, artists, lyrics, hostnames, soundfont file names or hashes, balances or personal data, and no private plan path, in any tracked file; no soundfont, MIDI, audio or PCM file committed (gate patterns); the public plan copy contains nothing below the private-appendix marker.

**T1h (revision 4; merges with or after T1a, own PRs H1, H2):** all named tests of C219–C224 green; goldens committed and green on amd64 with `GOAMD64=v1` and under `GODEBUG=cpu.fma=off`; every H kind meets D-T1-14 in the pipeline's own measurement; `widen` never leaves its correlation bounds and its mono fold-down is the parent's; `binaural` is refused on a binaural ancestry and labelled headphones-only; the HRTF table, `NOTICE` and credit are in place per the P.5-G4 decision (or the build is parametric-only and says so); no raw dataset, SOFA or audio file in any tracked file; the seven operations, `toolsCeilingBytes` and the config table are unchanged; the X3h listening results are in the private notes.

**T1b (own PR, optional):** the score tests of the mapping green with `MM_REQUIRE_MEDIA_TOOLS=1` and fluidsynth, the image carries fluidsynth and no soundfont, `score_render` is disabled (503, absent from capabilities) when `MM_SOUNDFONTS` is empty, and the T1a definition of done still holds.


## Risks / open questions

- **Phase-vocoder quality.** Identity phase locking is good for sustained harmonic material and smears transients; stretch factors beyond ±25 % and pitch shifts beyond ±6 semitones will sound processed. T1-X0(4) is the quality gate with an explicit fallback (WSOLA behind the same `Stretch` signature) decided before D4/D5; the estimate warns on large tempo changes and heavy shifts; the glacier and shepard are *meant* to sound processed.
- **Beat phase and downbeat accuracy.** A steady-pulse assumption on generated music: YuE2 output may drift or breathe. Phase and tempo are now refined locally around each transition (C204.3); the downbeat heuristic cannot work for four-on-the-floor and mis-reads a reggae one-drop, so the downbeat is **mandatory** unless a plan bar grid or the explicit `"auto"` opt-in exists, and `downbeat_ambiguous` is surfaced everywhere. If real Takes fail the spot check, morph degrades to "bar-aligned from explicit downbeats" without changing any contract.
- **Tempo and key defaults now come from shipped, still-unproven analyzers.** L3's `take_features` and Ears' tempo/key exist on main, but the tempo analyzer assumes a steady tempo and the key analyzer is a 24-key model; T1 uses them as the last rungs of the ladder and always accepts explicit values (D-T1-9). A reviewer should expect `ALIGNMENT_UNRESOLVED` often until the demo Takes get declared tempo/key or a plan bar grid.
- **Ensemble of a full mix.** Octave-stacking a whole mix stacks the drums and bass too (the `band_hz` split is cut); real vocal stems are not a T1 dependency (ACE-Step stem modes exist only on the base DiT models) so the demo uses an a-cappella-like intro Take. The UI says so.
- **(T1b) fluidsynth determinism and availability.** Reproducibility is conditional on version and soundfont hash (recorded; `verify` answers `inconclusive` otherwise). The CLI surface changes between 2.x releases (T1-X0 pins it and `TestFluidsynth_Argv` guards it). SF3 needs Ogg/Vorbis support in the distro build; a build without it fails the Dockerfile check, not production.
- **(T1b) Soundfont licence and size.** The plan stores no soundfont; whether a given orchestral font is allowed for the operator's intended use of the *rendered audio* is the operator's check (T1-X0(2)). Large SF3 files (hundreds of MB) load in seconds per render; a render holds the file open read-only.
- **Single-core CPU budget.** 1 concurrent render and a 600 s CPU ceiling are conservative guesses until T1-X0(5) measures the host; too tight ⇒ `TRANSFORM_BUDGET_EXCEEDED` on honest jobs (visible, adjustable by config), too loose ⇒ media streaming latency. `setpriority(+10)` and one core are the mitigation, not a guarantee on a busy host.
- **Memory model.** Planar float64 is simple and costly (16 B per stereo frame); a 600 s input at 48 kHz is 460 MB per buffer. The estimate refuses before the render; the RSS guard kills after. Streaming operators (glacier) are the only memory-light ones; if long morphs are wanted the decode-region optimisation (read only the regions needed) is a contained later change.
- **`origin='job'` for transform Takes.** No longer a mere risk: the known readers are listed in baseline rows B7/B12 and switched in C211.1/1c/4b with `TestStats_T1` and `TestEngineSweep_Dsp`; any reader found at align is added there. A `tracks` rebuild to a true `origin` value stays possible later through the migrator's `-- mm:foreign_keys=off` rebuild path that E2 introduces (if E2 merged it).
- **One pitch-class key model.** Modal and chromatic music map poorly to 24 keys; `none` and `semitones_b` exist for it.
- **Cross-host determinism.** Closed by D-T1-3 (own `Exp/Log/Pow`, banned `math` functions, `GOAMD64=v1`, toolchain recorded, `Inconclusive` on mismatch, FMA-off test). The residual risk is a Go release that changes `Sin`/`Cos`/`Atan2`/`Sqrt` behaviour: the golden fails loudly and the response is a `dsp_version` bump, not a silent drift.
- **Scope.** `score_render` was three lanes plus web and image work on T1's critical path; it is T1b. Parameters cut in revision 2 are listed in Out of scope and may return one at a time against a demonstrated need.
- **Parallel streams.** E2 is being re-baselined in parallel and E2, M5 and M6 add migrations too; T1 states "next free migration at build time" and the E2 join points are listed by name (E2-1…E2-11); T1-X1 is the single place that absorbs a move, and nothing in C204–C210 depends on a table of another plan. The one structural coupling that remained (the shared runner lease) was removed (D-T1-17).
- **Onset front end determinism.** `analysis.OnsetEnvelope` was written for an analyzer and calls `math.Log1p`; if the X0 spike cannot show it is host-independent, T1 carries a local copy. Either way a golden pins it; a later `analysis` retune forces a `dsp_version` bump (D-T1-20).
- **Engine sweep completeness.** Nine readers were found by reading; a tenth added by another lane before T1 merges is caught by `TestEngineSweep_Dsp` only if it is reachable from the test's paths; B6 re-greps at start and X1 again at align.
- **Take vs derived-only output.** Making results Takes puts them in every Take-shaped reader (Songs, Picks, stats, exports, Subsonic, tape); the engine sweep and the C211 amendments are the cost. The tape-style alternative (C217.4) is cheaper for the code and useless for judging; the operator chooses at P.5-G1.
- **Spatial/binaural expectation gap (revision 4).** T1h delivers hi-fi/3D *listening copies of a full mix* (virtual loudspeakers, light mastering, mono-safe widening); it does not deliver per-instrument placement, a better-produced song (a generation level) or loudspeaker-safe 3D. Say so at the first demo (C218.1). **HRTF licence risk:** only reading the exact licence text at P.5-G4 settles it; MIT KEMAR's permission is a "use freely, cite" notice rather than a licence, the SOFA mirrors showed none, and a public repo must not carry a dataset it may not redistribute (C222). A sound-quality risk of the same weight: a generic dummy-head HRTF externalises poorly for some listeners — X3h decides whether the room preset and the parametric alternative are enough; `master` on a full mix may be a sidegrade at best.


## Revision log

### Revision 4 (2026-11-06) — as shipped: what each lane changed versus the plan above

T1a (the five DSP kinds: `morph`, `shepard`, `glacier`, `polytempo`, `ensemble`) and T1h (`master`, `widen`, `binaural`) are built; **T1b (`score_render`, the soundfont toolchain) is deferred** and `score_render` answers 503 `UNAVAILABLE{soundfont}` and is hidden from capabilities. Migration `0014_transforms`. Seven generic operations, no dedicated MCP tool (`tools/list` delta 0). **No transform spends money**; CPU time on the application host is bounded by the quota pools.

**Operator decisions.** Transform results are ordinary Takes (engine `dsp`); scope includes the hi-fi and 3D kinds; operations are generic; the binaural HRTF table is derived from SADIE II (Apache-2.0, one dummy-head subject) with a NOTICE and a credit, and the public gate has a narrow exemption for that one file (exact path, `MMHRTF01` magic, the existing size cap).

**Determinism contract.** The release binary and CI are built with `GOAMD64=v1` (pinned in the Makefile, the Dockerfile with a build-info assertion, CI, and a release-binary check in the lint job). It is load-bearing: v3 fuses multiply-adds and changes `Log1p`, `Sin`, `Cos` and `Atan2`. `dsp` code uses its own `Exp`, `Log` and `Pow`; `verify` answers inconclusive on a toolchain change; any algorithm change bumps `dsp_version` (a failing onset pin means bump, never `-update`). The weekly CI job `t1-determinism` re-renders under `GODEBUG=cpu.fma=off`; `TestMath_Golden` on a non-FMA amd64 CPU is owed to CI. D-T1-20 was resolved by using `analysis.OnsetEnvelope` as shipped.

**Deviations from the contracts.**

- **Render child and streaming finish.** A render runs in a child process (`metamusic dsp-run --work <abs>`, stdlib plus `internal/dsp` only; exit codes 0/10/11/12/13; a relative path is exit 2). `safety.FinishStream` mirrors `Finish` arithmetic for streamed output (`TestFinishStream_MatchesFinish` fails on drift) and `glacier` streams to scratch: a 600 s glacier went from 3.4 GB to 124 MiB resident with the same PCM hash. `master` and `widen` still finish in memory. An external signal requeues the job once (`ErrChildSignaled`); a second interruption ends as `INTERRUPTED`.
- **Cost model.** The safety stage costs about 700 units per frame, not 30; the CPU budget is `min(Max*1000, 2*expected+5000)` ms, not three times the estimate; shepard needs a memory factor of 3 plus a 384 MiB bank bound; the disk check is 40 (glacier) or 24 bytes per frame plus the output; glacier memory is the input plus eight windows. A tempo ratio outside 1/4 to 4 is refused at estimate and create (422 `INVALID_TRANSFORM` with `bpm_b` and `tempo_ratio`). Cost constants stay provisional until the production-copy calibration.
- **DSP detail.** The resampler cutoff is `min(1, ratio) * 0.97`; the limiter attack is two cascaded boxcars; the tempo measurement has no 0.30 confidence threshold (the warning `tempo_measured_low_confidence` is raised instead); alignment confidence is `best / (mean + 2 sigma)`; `Fold` returns the shift and `Folded` applies it; a grid on fewer than 1024 samples is unrefined with confidence 0. The cepstral lifter cannot separate formants closer than about 500 Hz, so formant correction is imperfect on dense mixes and attenuates shifted near-pure tones.
- **Hi-fi (T1h).** `master` and `widen` run the safety stage themselves and report it; a hard-panned input passes through `widen_already_wide`. A `master` preset enum value outside the set is refused with rule `range`. **Binaural:** the parametric model is Woodworth plus Brown–Duda; the measured ITD at 90 degrees is about 0.80 ms, so the plan's 0.55–0.75 ms range is corrected to **0.70–0.90 ms**; the Brown–Duda low-frequency group delay lowers the parametric low-frequency ITD by up to about 0.22 ms; the parametric model stores `elevation_deg` as sent, ignores it in the render and warns `elevation_ignored`. The HRTF table is 3 elevations by 73 azimuths by 256 taps (int16, right ear; the left is the mirror, 113,128 bytes), built offline by `tools/hrtfgen`; the diffuse-field flatness is 0.76 dB. Rooms `studio` and `hall` are generated impulse responses.
- **Library and API.** `also_from` labels an out-of-tree partner by an 8-character id prefix. The downbeat field schema is `oneOf` number, `"auto"` or null (generated surface fixed in INT-B). `create_transform` answers 202, or 200 for a reused done render; a full queue is 429 `TRANSFORM_QUEUE_FULL` with `Retry-After`. Audit rows carry the operation and principal but not the transform kind, inputs or spec hash (the audit entry has no free-form field).
- **CLI.** `metamusic transform verify <id>|--all` (exit 1 on a mismatch, inconclusive exits 0) and `transform sweep` (refuses while an unexpired running lease exists) are host-operator maintenance and **documented parity exceptions** (`docs/architecture.md`, pinned by `TestArchitectureDoc_T1Exceptions`).
- **Bugs found by the test lane and fixed.** Shepard faded 10 ms at both ends, leaving a notch at the loop seam; a resolved `target_lufs: null` was read as 0 (a -14 LUFS parent came out too hot); a crash between the master freeze and the claim file left an orphan master (now tagged and removed); stored error text kept an `incoming/` path layout. `DSPVersion` stays `mm-dsp/1`: bump it before the production check if any shepard was rendered on a deployed copy.
- **CI.** Floors: `dsp` 93, `transform` 88 (measured). Go shards rebalanced; weekly jobs for chaos, property and bench. `dsp/...` and `score` run without `-race` in the shards.

**Not built / open.** T1b (`score_render`, soundfonts, ABC and MIDI synthesis); a transform quota e2e spec; `master` and `widen` on `FinishStream`; the audit detail above; the optional X0 phase-vocoder listening gate and the hi-fi listening gate are operator tasks.

**Hand-offs recorded from E2 (2026-11-06).** T1 needed no E2 code. Engine `dsp` Takes can be the source of E2's audio-input modes only where the engine allows: a transform Take is a lossless mother, but E2's reference, repaint and extend modes read the source master through a minted input link and are checked per engine. The Suite and tape-join work of E2c (not built) remains the fallback for gapless joins; bar-aligned and key-aligned morphs live in T1 and produce Takes. Migration `0014_transforms` landed before E2's `0015_engines`, whose `jobs` rebuild did not touch transforms.
