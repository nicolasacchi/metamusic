# G1 — Shared foundations for M5b + M6 + T1b — Implementation Plan (draft 1, 2026-11-20; nothing built)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (TDD per task: named tests first, watch them
> fail, implement). Read `CLAUDE.md` (public-repo rules, `make check`), `docs/architecture.md`, `docs/runbook.md`, the roadmap and the
> M3, E2, M5, T1 and U1 plans under `docs/plans/` first. Vocabulary is unchanged (Muse, Song, Take, Pick, Session, Sound, Record, Crate);
> do not rename a noun. Contracts are **C320–C329** (U1 used C301–C315). The four track plans that build on this one use **C330–C349**
> (Ears: M5b ears + Diction Lab), **C350–C364** (Composer's Desk + ABC toolkit + T1b score renderer), **C365–C389** (Night Shift +
> Spending Mandates + Morning Crate + Critic Bench) and **C390–C399** (pod bulk backend + R2/S3 upload fallback). Decisions are
> `D-G1-n`; the operator's binding decisions of 2026-11-20 are `D-G1-OP-n`. Task ids carry the `G1-` prefix; lane labels (`MM_LANE`) are
> `g1-seams`, `g1-jobs`, `g1-wire`, `g1-gate`, `g1-worker`, `g1-int`.
>
> **Public/private split.** Everything here is public-safe: the host is `mm.example.com`, users are `alice` (owner), `bob` (editor),
> `carol` (listener), fixtures use synthetic canaries. Operator-set money values (caps, the night ceiling), the deployment time zone,
> host paths, provider account details and real prices live in the private appendix (`docs/private/plans/g1-foundations.md`, never
> committed). `make gate` must stay green.
>
> **Money and secrets (binding, unchanged from M3).** No agent arms a backend, opens a worker capability, provisions an endpoint, runs a
> canary, approves a batch or a mandate, or edits caps while building or testing G1. Every test uses the fake backend. The only GPU
> spend G1 causes is the operator's own wire canary in task G1-X3, through the existing canary command with an explicit `--max-usd`.

**Goal.** One set of seams that the four parallel tracks build on, so that no track has to change the worker protocol, the jobs table,
the scheduler, the capability gate or the U1 rules on its own: a versioned worker protocol (`mm.worker/2`, a strict superset of today's
`mm.worker/1`) with job kinds, options, inputs and new artifact kinds; a per-backend, per-image **capability gate** that keeps every new
GPU feature unestimable until the operator has rolled out an image that speaks it; **one** rebuild of the `jobs` table for the whole
program; a timezone-aware daily scheduler with editable times; registration seams that make merges conflict-free; and the merge
discipline for about thirty lanes built by Sonnet agents one lane at a time.

**Architecture (one paragraph).** A job row gains `kind` (`generate | analyze | plan`), `needs_json` (the worker capabilities it
requires, empty for every job that exists today) and `spec_json` (its frozen worker options: Tier B, input pins, plan seeds,
attachments). At submit the runner picks the wire contract: `mm.worker/1` for a job with no needs (byte-identical to today), else
`mm.worker/2`. A job is claimable only when its needs are a subset of the capabilities the operator has opened for that backend **and
for the exact image digest the backend's fresh quote reports**; estimates refuse closed needs with 503 before anything is reserved; a
worker that answers `CONTRACT_VERSION` or `KIND_UNAVAILABLE` closes the capability automatically. The Python worker gains a stage
registry (`render`, `ears`, `analyze`, `plan`) whose stages the tracks fill; the image is built by the existing CI workflow and rolled
out by the operator in "worker waves", each followed by a canary. Scheduled work (Morning Crate at 07:00, Night Shift nightly) runs
through `internal/schedule.Daily` in the configured zone (`MM_TZ`), registered as an `app` loop.

## Draft judgement and what was kept

The design sources are the private M6 v2 draft (G0, G1, tracks AN, DL, MC, NS, CB, PA, R2), the M5 draft (Desk, ABC toolkit, Tier B,
mandates, plan jobs, `mm.worker/2`), the M5 as-built plan (deferral table, Recover recipe) and the T1 plan (T1b). They were written before
E2, T1, M5-core and U1 shipped; every name below was re-read on `origin/main` `aec888b`.

| Draft item | Status on main | Taken here |
|---|---|---|
| M5 `mm.worker/2` with `release` in the start body | never built; E2 extended `/1` instead (an `engine` discriminator plus `inputs`, the contract string stays `/1`) | **`/2` as a strict superset of `/1`**, no `release` (the upgrade gate is out, D-G1-OP-1); ACE-Step stays on its `/1` variant |
| M5 `worker_v2_refused_digest` (refuse once per digest) | not built | kept as the **auto-close** half of a positive gate bound to a digest (C322) |
| M5/M6 jobs rebuild with `recipe_id` NULL for plan jobs | `jobs` rebuilt by E2 (0015), `recipe_id NOT NULL`, completion needs `track_id` | **one** rebuild here; `recipe_id` stays NOT NULL (no scanner breaks), only the completion CHECK is relaxed for `plan` (C323) |
| M6 G0 backend set, per-backend runner, reconciler, billing, cold start | **built by E2** (`provider.Set`, `jobs.NewReconcilers`, `0016_billing_backend.sql`, `ColdStartS`) | nothing to build; what pods still need is listed in C325 and owned by the C390s track |
| M6 G1 job inputs (`inputs.src_audio`) | built for ACE-Step only, derived from the engine request; never minted for yue2 | generalised to pins in `spec_json` (C323.5) |
| M2a `Scheduler.AtFunc` | no such type; the only scheduler is `backup.Scheduler` (static time, `time.Local`, `time.Now`) | new leaf `internal/schedule` (C324) |
| M6 crate tables named `crates`, op `get_crate` | L2 owns `crates`, `get_crate`, `list_crates`, `create_crate`, `update_crate`, `edit_crate_items` | Morning Crate uses the `morning_` prefix (D-G1-8) |
| Draft track AS (ACE-Step 1.5) | E2 built it; the operator rejected adoption on 2026-11-20 | **dropped**; nothing may depend on it (D-G1-OP-2, archtest in C320) |

## Dependencies and sequencing

Builds on `main` at `aec888b` (migrations through `0019_device_owner.sql`; the loader rejects gaps, so a plan names migrations by
purpose and the number is assigned at merge). G1 adds two migrations: `<next>_job_kinds.sql` (a `jobs` rebuild, lane `g1-jobs`) and
`<next>_worker_contract.sql` (additive, lane `g1-gate`). Order is fixed by risk: the jobs rebuild lands early, alone, and is deployed
attended after a production-copy rehearsal, before any track lane adds rows that depend on it. Track lanes that need none of the wire
(Critic Bench, Morning Crate builder, mandates core, T1b toolchain) may start as soon as `g1-seams` is merged.

```
g1-seams ──► g1-jobs ──► g1-wire ──► g1-gate ──► g1-worker ──► G1-X1 docs ──► G1-X2 attended deploy ──► G1-X3 operator wave W1
    │            │                                                   (jobs rebuild rehearsed)        (image, provision, canary, open v2)
    └─► track lanes without GPU work (C350 T1b toolchain, C365s mandates/crate/bench) may start here
                 └─► track lanes that add job kinds or needs start after g1-gate; their worker lanes after g1-worker
```

## Decisions recorded by this plan

**Operator decisions of 2026-11-20 (binding; copied verbatim in intent):**

- **D-G1-OP-1 "Do all".** Build Ears Tier B (including measured sections, score adherence and the Recover recipe), the Italian Diction
  Lab, Composer's Desk with the ABC toolkit, the T1b score renderer, Night Shift with Spending Mandates, Morning Crate, Critic Bench, the
  pod bulk backend and the R2/S3 upload fallback. MCP widgets and the upgrade gate are out of this program unless one of these needs them
  (none does, D-G1-1).
- **D-G1-OP-2 ACE-Step rejected.** The engine adoption log says `not_adopted`, the endpoint is inactive. Nothing may depend on ACE-Step;
  the M6 draft's track AS is dropped. The E2 engine layer (registry, per-backend runner, billing, adoption log) stays and is the base for
  new backends.
- **D-G1-OP-3 Night shift money.** A per-night ceiling (value in the private appendix) is compared with the **estimate ceiling**, not
  the expected spend. The monthly cap and every other cap stay as they are; nothing in this program may raise a cap; the spending mandate
  is the operator's own two-factor act in the app.
- **D-G1-OP-4 Morning Crate.** Built daily at 07:00 local time (zone `MM_TZ`, value in the private appendix), 40 minutes, **blind**
  (titles hidden until the operator has rated; the native Subsonic playlist appears only at reveal).
- **D-G1-OP-5 Listening checks (Tier B).** On **new** takes only: every new render opts in by default and its cost is in the estimate;
  the existing library is **not** analysed automatically. An on-demand analyze of chosen takes exists where a feature needs it (Recover of
  imported references; the Diction Lab validation set), always through an ordinary estimate and approval.
- **D-G1-OP-6 Pods and R2.** Build both. The draft's entry criteria (D-M6-35) are waived; the R2 privacy trade-off (unreleased audio in a
  private third-party bucket, deleted by a 7-day lifecycle) and the pod key on the server during bulk windows only (D-M6-23) are
  accepted. The plan recommends one provider (D-G1-18); the operator creates the bucket and keys later.
- **D-G1-OP-7 Builders.** Sonnet agents, lane by lane: TDD, one worktree per lane, one PR per lane, CI is the heavy gate. Lanes are sized
  for that: one coherent PR each, explicit file ownership, named tests.

**Plan decisions:**

- **D-G1-1** No MCP widget and no upgrade gate are needed by any track: the capability gate (C322) replaces the release check for this
  program's purpose (never send a job to an image that cannot run it). The `release` field of the M5 draft is not built.
- **D-G1-2** `mm.worker/2` is a **strict superset** of `mm.worker/1`: every `/1` field keeps its name and meaning; `/2` adds optional
  fields only. `/1` is frozen; a job with no needs is sent as `/1`, byte-identical to today (`TestTakeSpec_Yue2InputByteIdentical`
  passes unmodified). The ACE-Step variant of `/1` is untouched and never becomes `/2`.
- **D-G1-3** The wire contract is chosen per attempt at submit: `/2` iff the job's `needs` is non-empty, or the backend's open
  capabilities include `fallback` and an upload fallback is configured (R2, C390s); else `/1`. `needs` is frozen at create.
- **D-G1-4** The capability gate is per backend and bound to one image digest; its state is an append-only log (the `engine_adoption_log`
  pattern); only the operator CLI opens capabilities; the server closes them automatically on evidence; estimates and creates refuse a
  closed need with 503 `GENERATION_DISABLED` reason `worker_outdated` before anything is reserved.
- **D-G1-5** **One** `jobs` rebuild for the whole program, in lane `g1-jobs`. Any later change to `jobs` in this program is additive
  (`ALTER TABLE … ADD COLUMN` plus a per-column frozen trigger, the `0013` pattern); a second rebuild is a plan amendment.
- **D-G1-6** `recipe_id` stays NOT NULL. An `analyze` job points at the analysed take's recipe and its `source_track_id` is that take;
  a `plan` job points at its base recipe (reused when it exists, inserted otherwise; the Desk track owns the rule and its `library verify`
  check).
- **D-G1-7** New outputs on an existing take reuse E2's **attach** shape (C166 there): the job completes with `track_id` = that take, no
  `tracks` row is created, artifacts are immutable and a repeated name gets a `-<n>` suffix, never an overwrite.
- **D-G1-8** Naming: the Morning Crate uses the `morning_` prefix for its package, tables and operations (L2 owns `crate`/`crates`);
  the program's single ABC parser is `internal/score/abc` (the T1 plan's C208 name), owned by the C350s track and imported by Ears for
  score adherence; `internal/ears/tierb` is the Tier B package; mandates live in `internal/mandates`.
- **D-G1-9** Scheduled work uses `internal/schedule.Daily` in the zone `MM_TZ` (IANA name; empty means the process zone). Each run
  carries a **local-day key**; at-most-once per key is the consumer's unique constraint, never the scheduler's memory.
- **D-G1-10** Background loops register through `app.Services.Loops`; `cmd/metamusic/cmd_serve.go` is edited once (lane `g1-seams`)
  and never by a track.
- **D-G1-11** No new first-class MCP tool in this program (the L2, L3, M5, E2 and U1 precedent): every new operation is `mcp: generic`
  or `exclude:<reason>`. Schema growth of the 39 existing tools is re-baselined per lane in `toolsCeilingBytes` with a comment; the
  program's total growth budget is **4 000 compact bytes**; a lane that would exceed what is left stops and asks the operator.
- **D-G1-12** Per-file registration for the two package-level maps every track touches: shape rules (`internal/api/shape_u1.go`
  `shapeRules`) and schema names (`internal/api/api.go` `wireNames`). Tracks add `shape_<track>.go` and `wire_<track>.go` files.
- **D-G1-13** One lane merges at a time. Generated files (`openapi.json`, `web/src/api/schema.d.ts`,
  `internal/api/testdata/role_matrix.golden`, `internal/mcpgen/testdata/*.golden.json`, the generated jobs DDL block) are regenerated
  after every rebase by `make regen` and never edited by hand.
- **D-G1-14** Worker changes ship in **worker waves**: the worker lanes merged since the last wave become one image revision; the
  operator commits its digest, provisions it attended, runs the `/1` regression case and the new capabilities' canary cases, then opens
  those capabilities. Rollout order is always server first (tolerant of the new output), image second, capability third.
- **D-G1-15** No model or soundfont whose licence forbids redistribution enters the public worker image or the repository; such a model
  is either replaced or placed by the operator outside the image (the Ears and T1b tracks decide which).
- **D-G1-16** U1 defaults for new operations (C326): new curation signals (crate verdicts, diction marks, critic trust) are Owner-only in
  v1 (D-U1-10 precedent); every new GPU feature is a **new item mode or option of the existing `estimate_generation` /
  `create_generation` pair**, not a new spend operation, so U1's Editor rule (always awaiting Owner approval), the caps and parity apply
  unchanged; every operation that changes a spending authority is `HumanOnly` + `OwnerOnly`.
- **D-G1-17** No package added by this program imports `internal/engine/acestep`, names `provider.BackendRunPodACEStep` or
  `BackendFakeACEStep`, or routes to engine `acestep15` (archtest, C320.3).
- **D-G1-18** Upload fallback provider: **Cloudflare R2** is recommended over Backblaze B2's S3 API (reasons in C325.4); the C390s track
  builds a generic S3 SigV4 client so either works.
- **D-G1-19** Tier B time is added **on top** of the render's timeout, never carved out of it (M5 draft D-M5-19 kept): the worker's
  generation deadline for a render with `ears` equals the `/1` deadline of the same render (C321.6).

## Baseline reconciliation (read in worktree `metamusic-wt/plan-m6`, branch `plan-m6` at `origin/main` `aec888b`)

| Fact | Where | Consequence |
|---|---|---|
| One contract string, `mm.worker/1`, in Go and Python | `internal/provider/parse_input.go:13`; `worker/src/mmworker/contract.py:11` | `/2` constants are new (C321) |
| The worker refuses any other `contract` with `CONTRACT_VERSION`, **before** the start handshake (no announce) | `contract.py:149-150`; `serverless.py` `make_handler` | an old image answers a `/2` input with an error output after a short billed run; the gate's auto-close (C322.5) relies on it |
| `ParseWorkerInput` and `DecodeOutput` are strict (`DisallowUnknownFields`) | `parse_input.go:65-68`, `:224-227` | a server that predates `/2` rejects a `/2` output: **server first, image second** (D-G1-14) |
| Start body is decoded leniently (`json.Unmarshal` into `{runpod_job_id}`), ≤ 1 KiB | `internal/upload/start.go:79-86`, `:20` | the worker may add start fields later without breaking old servers; G1 adds none |
| `BuildWorkerInput` serialises one struct for both engines; ACE adds `engine`, `inputs` | `parse_input.go:211-220`, `:20-38` | `/2` gets its own builder; `/1` path untouched |
| `TakeSpec` has `Inputs` (ACE) and `MinAudioS` (yue2); no kind, options or attachments | `internal/provider/provider.go:140-160` | C321.2 adds fields; zero values keep `/1` |
| Runner: yue2 jobs return before inputs are minted; inputs derive from the ACE request (`src`, `ref`) | `internal/jobs/submit.go:465-494`; `internal/jobs/inputs.go:29-52`; `InputMinter` in `internal/jobs/seams_e2.go:27-29` | analyze inputs must come from pins in `spec_json` (C323.5) |
| Upload allowlist is per engine, by fixed names; triple equality Go / Python / schema | `internal/provider/artifacts.go` (`Artifacts`, `ArtifactsFor`); `contract.py:21-33`; `TestArtifacts_SchemaEnum` | `/2` names (C321.5) join the same tables and tests |
| `jobs.kind` is written as the literal `'generate'`; Go-validated only | `internal/generation/create.go:397-399`; `0015_engines.sql:12` | C323 adds a SQL CHECK and writes `kind` from the plan |
| `jobs` completion needs a track: `CHECK (state <> 'completed' OR track_id IS NOT NULL)`; `recipe_id NOT NULL`; no options column | `0015_engines.sql:15`, `:51` | plan jobs need the CHECK relaxed: one rebuild (D-G1-5) |
| The rebuild generator edits exactly one CHECK (360 → 600 s) | `scripts/gen-jobs-ddl.go:31-32`, `:132-135` | generalised to named profiles (C323.2) |
| The `-- mm:foreign_keys=off` migration directive exists | `internal/store/migrate.go:83`; first line of `0015_engines.sql` | the rebuild uses it |
| Readers scan `recipe_id` into a string and join `recipes` with an inner join | `internal/jobs/rows.go:82-94`; `internal/jobs/service.go:322`; `internal/generation/batchview.go`; `internal/songpr/read.go` | keeping `recipe_id NOT NULL` avoids touching them (D-G1-6) |
| Attach terminal shape exists (stem jobs complete on the source take, no `tracks` row) | `internal/jobs/ingest_attach.go` (`attachStem`) | reused for `analyze` (D-G1-7) |
| `artifacts` PK `(track_id, name)`, immutable; `analyses` PK `(track_id, analyzer, version)`, immutable; `sections.source` already allows `measured`, and `library.sectionRank` ranks it | `0002_library.sql:172-199`, `:219-238`; `internal/library/tracks.go:943` | Tier B needs no schema for analyses or sections; tier `B` is Go-validated |
| One `TakeHook` (callbacks), wired through `app.lateTakeHook` | `internal/jobs/hooks.go`; `internal/jobs/ingest.go:77-78`; `internal/app/m5.go:57-90` | C323.7 adds `jobs.MultiTakeHook` so Ears can add its own |
| The only scheduler: `backup.Scheduler{At, Loc, Run}`, static time, `time.Now` | `internal/backup/retain.go:151-197`; wired at `cmd/metamusic/cmd_serve.go:293-301` | new `internal/schedule` (C324); `NextRun` moves there, backup delegates |
| No configured time zone; the app image has `tzdata`; the binary is `CGO_ENABLED=0` | `Dockerfile:41`; `internal/config/config.go` | `MM_TZ` plus an embedded `time/tzdata` (C324.2) |
| Background goroutines are listed one by one in `serve` | `cmd/metamusic/cmd_serve.go:270-301` | `app.Services.Loops` (C324.4); `app.Services` is `internal/app/app.go:115` |
| Backend set routes one backend per engine; no cross routing | `internal/provider/set.go:79-106` | a second yue2 backend (pods) needs `Route` amended (C325) |
| Extra backends are constructed only for ACE-Step (`cfg.ACEStep`) | `internal/app/e2_set.go:23-58`, `:111` (`jobs.NewReconcilers`) | the pod track generalises the hook (C325) |
| Per-backend billing, meter and bill rows | `0016_billing_backend.sql` | pods reuse it; nothing for G1 |
| `provider_state` is one row per backend (armed, quote); quote carries `ImageDigest` | `0007_generate.sql:485-497`; `provider.EndpointConfig.ImageDigest`; `internal/provider/runpod/quote.go:50` | the gate binds to that digest (C322) |
| Worker image: built by `.github/workflows/worker-image.yml` on main pushes touching `worker/**`, published to GHCR; the digest is committed by hand in `deploy/worker-image.digest`; provisioning is `metamusic runpod provision --image <ref@sha256:…>` with the admin key | workflow lines 1-20; `deploy/worker-image.digest`; `docs/runbook.md:116-135`; `internal/provider/runpod/profile.go:20-22` | rollout is an operator step plus a canary (C322.7) |
| yue2 canary cases are a slice; per-case default ceiling $0.50 | `internal/provider/runpod/canary.go:34-52` | G1 adds one case, `v2-wire` (C322.8) |
| `MODE=agent` exits 2 ("arrives in M6") | `worker/src/mmworker/__main__.py:18-19` | the pod track implements it on the stage registry |
| Registry: 150 operations, every one classified in the role matrix; 39 first-class MCP tools; ceiling 117 406 bytes | `internal/api/testdata/role_matrix.golden`; `internal/mcpgen/m2a_contract_test.go:91`; `TestToolsList_M5Budget` (`parity_m5_test.go:66`) | C326 rules |
| `api.Permitted` is the one gate; `validate()` forces `OwnerOnly` on human-only, approve, admin and sensitive reads; shaping is a Huma transformer over `shapeRules`; Listener-reachable ops get money and private keys removed by default | `internal/api/permit.go:16`, `:51-64`; `internal/api/registry.go:56-160`; `internal/api/shape_u1.go:20-90`; `internal/view/shape.go` | new ops inherit the rules; tracks add rules per file (D-G1-12) |
| U1 sweeps exist and must keep passing for every new op | `internal/acceptance/intb_money_test.go` (`TestIntBMoneyFieldScan`), `intb_sweep_test.go`, `intb_listener_test.go`; `internal/archtest/role_predicates_u1_test.go`, `visible_u1_test.go` | C326.6 |
| L2 already owns crate names | role matrix rows `create_crate`, `edit_crate_items`, `get_crate`, `list_crates`, `update_crate`; `internal/crates` | D-G1-8 |
| Generation modes are per-milestone slices | `internal/lineage/modes.go:72` (`Modes`), `:83` (`M5Modes`), `:204` (`E2Modes`) | tracks append their own slice |
| Per-take estimate items (`items_json`) are `generation.planTake` | `internal/generation/types.go:31-61` | C323.4 adds `kind`, `needs`, `spec` with `omitempty` |
| `score_render` exists as a transform kind but fails "not part of this build" | `internal/transform/spec.go:16`; `internal/transform/bridge.go:140-141` | T1b fills it (C350s); no G1 work |

Every citation above was read for this plan. Lanes re-read the lines they edit and record corrections in the Revision log; task G1-F1
starts with that check.

## Contracts (all lanes build against these — do not change without updating this file)

### C320. Program frame: names, numbering, ownership of new packages

1. **Package names** (new; each owned by the track named): `internal/schedule` (G1), `internal/jobspec` (G1), `internal/ears/tierb`
   (Ears), `internal/diction` (Ears), `internal/score`, `internal/score/abc`, `internal/score/smf`, `internal/score/synth` (Desk+T1b),
   `internal/desk` (Desk), `internal/mandates`, `internal/nightshift`, `internal/morning`, `internal/criticbench` (C365s track),
   `internal/provider/runpod/pods` or a `pods` file set inside `internal/provider/runpod`, `internal/upload/fallback` (C390s track). A
   track may rename its own packages in its plan; it may not take a name listed for another track.
2. **Operation naming.** New ops use the track prefix where a generic word would collide: `*_morning_crate*` (never `*_crate*`),
   `*_mandate*`, `*_night_shift*`, `*_critic_bench*`, `*_diction*`, `*_plan*` (Desk), `*_pod*`. No new op reuses an id in
   `role_matrix.golden`.
3. **ACE-Step independence** (D-G1-17): `internal/archtest/import_graph_g1_test.go` (`TestImportGraph_G1`) lists every package above and
   fails if one imports `internal/engine/acestep` or `internal/provider/fake` outside tests, or if a non-test file of theirs contains
   `BackendRunPodACEStep`, `BackendFakeACEStep` or `"acestep15"`. `internal/schedule` and `internal/jobspec` may import only the standard
   library and `internal/clock`.
4. **Migrations** are named `<NNNN>_<purpose>.sql`; the number is the next free one **at merge** (the merging lane renames its file on
   rebase); at most one migration per lane; every schema test uses `len(migrations)`, never a literal.
5. **Lane labels** are `<track>-<lane>`; the four track plans choose their own prefixes (proposed: `ears-`, `desk-`, `loop-`, `bulk-`).

### C321. The worker protocol `mm.worker/2`

1. **Constants.** Go: `provider.ContractV1 = "mm.worker/1"` (the existing `provider.Contract` becomes an alias kept for callers),
   `provider.ContractV2 = "mm.worker/2"`. Python: `contract.CONTRACT_V1`, `contract.CONTRACT_V2`, `contract.CONTRACTS = (V1, V2)`.
   Schemas: `worker/src/mmworker/schema/mm-worker-2.input.schema.json` and `mm-worker-2.output.schema.json` (new, `additionalProperties:
   false` at every level); the three `/1` schema files are unchanged.
2. **Go types** (`internal/provider/provider.go`, additive; the zero value of every new field means "absent"):
   ```go
   // TakeSpec gains:
   Kind   string                // "" or "render" | "analyze" | "plan"; "" = render
   Ears   *EarsSpec             // Tier B on a render, or the analyze stage
   Plan   *PlanSpec             // kind plan
   Attach map[string]AttachFile // small inline files written into the worker's work dir before the stage runs
   Wire   string                // the contract chosen at submit (D-G1-3); "" = ContractV1
   type EarsSpec struct{ Lang string /* "" = auto */; BudgetS int; Stages []string /* subset of separate, asr, transcribe */; KeepVocals bool }
   type PlanSpec struct{ Seeds []int64 /* 1..16 */; SecondsPerSeed int }
   type AttachFile struct{ SHA256 string; Data []byte } // decoded size of all attachments ≤ 1 MiB
   // Output gains (nil/empty for /1 outputs; a /1 output carrying any of them is a decode error):
   Kind  string     `json:"kind,omitempty"`
   Ears  *EarsOut   `json:"ears,omitempty"`  // {status: ok|error|skipped, code}
   Plans []PlanOut  `json:"plans,omitempty"` // [{index, seed, status, code, truncated, bundle}]
   Caps  []string   `json:"caps,omitempty"`  // what this worker image supports (diagnostic, recorded in the job event)
   func WireFor(t TakeSpec, needs []string, fallbackOpen bool) string // D-G1-3
   func BuildWorkerInputV2(t TakeSpec) ([]byte, error)                // BuildWorkerInput keeps building /1 for Wire == "" or V1
   func ParseWorkerInput(raw []byte) (WorkerInput, error)             // accepts both; dispatches on "contract"
   ```
3. **Input document** (`/2`). Every `/1` key keeps its meaning; new keys and rules:
   ```json
   {"contract":"mm.worker/2","kind":"render","engine":"yue2","take_id":"0190…","attempt_id":"0190…",
    "request":{"cot":"melody","id":"syn-a-s1102","lyrics":"[verse]\nla la","seed":1102,"style":"pop, 100 BPM"},
    "ears":{"lang":"auto","budget_s":110,"stages":["separate","asr","transcribe"],"keep_vocals":false},
    "attach":{"score.abc":{"sha256":"…","data_base64":"…"}},
    "timeout_s":296,"upload_budget_s":60,"deadline_unix":1760000000,
    "start":{"url":"https://mm.example.com/worker/v1/attempts/0190…/start?exp=…&sig=…"},
    "uploads":{"audio.flac":{"url":"…","max_bytes":167772160},"analysis.json":{"url":"…","max_bytes":4194304},"mm-result.json":{"url":"…","max_bytes":65536}},
    "expect":{"weights":{"m-a-p/YuE2-3B":"<rev>","m-a-p/YuE2-Vae":"<rev>"}}}
   ```
   - `kind` (optional, default `render`). `render`: `request` required; `ears`, `attach`, `min_audio_s` optional; `inputs` refused for
     yue2 (ACE keeps `/1`). `analyze`: `request` absent, `inputs.src_audio` and `ears` required, `attach` optional. `plan`: `request` and
     `plan {seeds, s_per_seed}` required; `ears` refused.
   - `inputs` has the `/1` ACE shape `{src_audio: {url, sha256, bytes}}`; the worker verifies size and sha256 before any use
     (`INPUT_FAILED`, `INPUT_MISMATCH`, existing codes).
   - `uploads[name]` may carry `fallback: {url}` and the document a top-level `fallback_after_s` (60..600). **Reserved** here; the worker
     ignores both until it advertises capability `fallback` (C390s track implements the switch).
   - Size: the whole `/run` body stays ≤ `provider.MaxRunBody` (2 MiB); attachments ≤ 1 MiB decoded.
4. **Output document** (`/2`): the `/1` object plus `kind`, `ears`, `plans`, `caps`. `audio_seconds` is the rendered length for
   `render`, the input's length for `analyze`, `null` for `plan`; `truncated` is the yue2 dict for `render` and `null` otherwise (per-plan
   truncation lives in `plans[]`). For `analyze`, `ears.status != "ok"` makes the output `status: "error"` with code `ANALYZE_FAILED`;
   for `render`, a Tier B failure never fails the take (`ears.status = "error"`, the take completes).
5. **Artifacts** (one table in `internal/provider/artifacts.go`, mirrored in `contract.py` and both schema enums; per kind):

   | Kind | Required | Optional-but-verified | Max bytes |
   |---|---|---|---|
   | render (`/1` or `/2` without ears) | the `/1` set | the `/1` set | `/1` |
   | render with `ears` | the `/1` set | `/1` set + `analysis.json` (required once `ears.status = ok`) + `stem-vocals.flac` (only with `keep_vocals`) | 4 MiB; 200 MiB |
   | analyze | `analysis.json`, `mm-result.json` | `stem-vocals.flac` (only with `keep_vocals`) | 4 MiB; 200 MiB |
   | plan (n seeds) | `mm-result.json` | `plan-00.tar` … `plan-15.tar`, each required once `plans[i].status = ok` | 160 MiB each (the Desk may ask less per job) |

   `analysis.json` is the raw Tier B document, contract name **`mm.ears-b/1`** (reserved here; the Ears track owns its schema:
   word timings, transcription, vocal statistics). Server-derived kinds are **not** worker artifacts: word timings are read from
   `analysis.json`; measured sections are `sections` rows (`source = 'measured'`); diction and score adherence are `analyses` rows
   (tier `B`); plan scores are Desk rows built from the bundles. A track that needs another worker artifact amends this table.
6. **Deadlines** (amends M3 C83.2 for `/2` only; D-G1-19): `upload_deadline` exactly as `/1`; `ears_deadline = upload_deadline −
   upload_budget_s`; `generation_deadline = ears_deadline − ears.budget_s` when `ears` is present, else `ears_deadline`. The server adds
   `ears.budget_s` on top of the render's `/1` timeout (the Ears track's quote rule), so the render window equals `/1`'s. If generation
   ends after `ears_deadline − ears.budget_s/3`, the stage is skipped (`ears.status = skipped`, code `DEADLINE`). For `analyze`, the fetch
   counts against `ears_deadline − ears.budget_s`.
7. **Order of a `/2` run** (amends C83.2/C153.3): 1a start handshake (410 ⇒ stop); **1c** the kind and every requested stage must be
   available in this image, else `KIND_UNAVAILABLE` before any GPU work; 1b fetch `inputs`; 2 weights; 3 request validation (render,
   plan); 3b write `attach` files (sha256 checked; `ATTACH_MISMATCH`); 4 stage run; 5 save; 6 truncation (yue2 render); 7 collect
   against the kind's allowlist; 8 uploads, `mm-result.json` last.
8. **Error codes** gain `KIND_UNAVAILABLE`, `ATTACH_MISMATCH`, `ANALYZE_FAILED`, `PLANS_FAILED` (every plan seed failed); job error codes
   gain `WORKER_OUTDATED` (C322.5).
9. **Transports.** The pod agent (C390s) hands out the **same** input documents through its poll and runs the same `process()`; R2 only
   adds the reserved `fallback` fields. No track defines another job document.
10. Tests (lane `g1-wire` unless marked): `TestTakeSpec_Yue2InputByteIdentical` (existing, unmodified), `TestWorkerInputV2_Golden`
    (render+ears, analyze, plan, render+attach; `testdata/worker-input-v2-*.json`), `TestParseWorkerInput_V2_Table` (≥ 25 rows: unknown key
    at each level, analyze without inputs, analyze with request, plan without seeds, 17 seeds, ears on plan, inputs on a yue2 render,
    attach over 1 MiB, `fallback` without `fallback_after_s`, `/2` with `engine: acestep15`), `TestWorkerSchemasV2_MatchParser`
    (test-only `jsonschema-go`: schema and parser accept and reject the same documents), `TestDecodeOutput_V1RejectsV2Keys`,
    `TestDecodeOutput_V2Kinds`, `TestArtifacts_V2SchemaEnum` (both directions, per kind), `TestWireFor_Table`; Python (lane
    `g1-worker`): `test_parse_v2_table`, `test_v1_documents_unchanged`, `test_v2_render_plain_same_artifacts_as_v1`,
    `test_v2_kind_unavailable_before_gpu`, `test_v2_analyze_fetches_before_stage`, `test_v2_attach_written_and_verified`,
    `test_v2_deadlines_ears_budget` (input `timeout_s` 296, `upload_budget_s` 60, `ears.budget_s` 110 ⇒ deadlines +116 / +226 / +286 s),
    `test_artifacts_v2_equal_go_table` (reads the Go table exported to `worker/contract/testdata/artifacts-v2.json` by
    `go test ./internal/provider -run TestWriteArtifactsV2 -update`), `test_acestep_stays_v1`.

### C322. Worker capability gate and image rollout

1. **Capability vocabulary** (frozen; Go `provider.Cap*`, Python `caps.py`): `v2` (speaks `/2` render), `ears` (Tier B stage on a
   render), `analyze` (kind analyze), `plan` (kind plan), `attach` (render with attachments, e.g. render-from-plan), `fallback` (upload
   fallback, C390s), `agent` (`MODE=agent` pod loop, C390s). Every non-empty `needs` includes `v2`.
2. **Image declaration.** The yue2 image carries `LABEL io.metamusic.contracts="mm.worker/1 mm.worker/2"` and
   `LABEL io.metamusic.caps="<space-separated caps>"`; `MODE=selftest` prints the same caps and exercises a `/2` render with the fake
   pipeline; CI (`worker-image.yml` smoke) asserts label = selftest output. A stage registers its cap only when its code **and** its
   models are present in the image.
3. **Migration `<next>_worker_contract.sql`** (additive; lane `g1-gate`):
   ```sql
   CREATE TABLE worker_contract_log (
     id           INTEGER PRIMARY KEY AUTOINCREMENT,
     backend      TEXT NOT NULL,
     image_digest TEXT NOT NULL,                     -- 'sha256:<64 hex>' as the backend's quote reports it
     caps_json    TEXT NOT NULL CHECK (json_valid(caps_json) AND json_type(caps_json) = 'array'),
     via          TEXT NOT NULL CHECK (via IN ('cli','auto')),
     attempt_id   TEXT REFERENCES job_attempts(id),  -- the attempt whose output closed a cap (auto rows only)
     changed_by   TEXT NOT NULL,
     changed_at   TEXT NOT NULL,
     reason       TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
     CHECK ((via = 'auto') = (attempt_id IS NOT NULL))
   );
   CREATE INDEX worker_contract_log_backend ON worker_contract_log(backend, id DESC);
   -- append-only and no-delete triggers; view worker_contract = the latest row per backend
   ```
4. **Open capabilities** of a backend at an instant: the latest row's `caps_json` if its `image_digest` equals the digest in the
   backend's **fresh** cached quote, else `[]`. Fake backends: every cap the fake registers, always (no rows needed).
   `provider.WorkerCaps(ctx, q, backend, quote) ([]string, error)`.
5. **Enforcement.**
   - *Estimate and create*: `generation.(*Service).requireCaps(ctx, backend, needs)` ⇒ 503 `GENERATION_DISABLED`, `details: {backend,
     reason: "worker_outdated", missing: [caps]}`; no estimate row, no token, no reservation; create re-checks inside its transaction
     (token not consumed on refusal). `TestEstimate_RefusesClosedNeeds`, `TestCreate_RechecksNeeds`.
   - *Claim*: the claim SQL gains `AND NOT EXISTS (SELECT 1 FROM json_each(j.needs_json) n WHERE n.value NOT IN (SELECT value FROM
     json_each(?)))` with the backend's open caps. A job that loses its caps stays `queued` and ends `NOT_SUBMITTED` after 24 h (M3 row 6),
     releasing its reservation. `TestClaim_NeedsSubsetOfOpenCaps`, `TestClaim_DigestChangeClosesCaps`.
   - *Auto-close* (in the attempt's settle transaction): output error `CONTRACT_VERSION` or `BAD_INPUT` on a `/2` input ⇒ a row with
     `caps_json = []`; `KIND_UNAVAILABLE` ⇒ a row without the missing caps; both `via = 'auto'`, `changed_by = 'system:runner'`, only when
     the attempt's `image_digest` equals the gate's digest (an attempt on a stale worker changes nothing). The job ends `failed`
     `WORKER_CONTRACT` (contract or input refusal) or `WORKER_OUTDATED` (kind unavailable), settled normally; a critical notification
     names the backend and the closed caps. `TestSettle_ContractVersionAutoCloses`, `TestSettle_KindUnavailableRemovesCap`,
     `TestSettle_StaleDigestChangesNothing`.
6. **Operator CLI** (no API operation; like `runpod arm`): `metamusic runpod contract status [--backend B]`, `metamusic runpod contract
   open --backend B --caps v2,ears --reason TEXT` (reads the backend's fresh quote digest; refuses a stale quote, an unknown cap, a cap the
   image label does not list when the label is known, and a missing reason), `metamusic runpod contract close --backend B [--caps …]
   --reason TEXT`. Every change is a log row and an audit row. `get_provider_status` (already `OwnerOnly`) gains per backend `worker:
   {image_digest, caps, changed_at, via}`. `TestCLI_ContractOpenClose`, `TestProviderStatus_WorkerCaps`.
7. **Rollout of an image revision (a "worker wave", D-G1-14)** — operator steps, documented in the runbook by G1-X1:
   1. The server release that understands the wave's outputs is deployed first (attended; disarm, drain, deploy, arm).
   2. CI publishes the image from `main`; the operator commits its digest in `deploy/worker-image.digest` (a one-line PR).
   3. Disarm, drain, `metamusic runpod provision --image <ref@sha256:…> --dry-run`, then provision (admin key file, deleted afterwards),
      arm. The gate closes by itself (new digest, no row).
   4. Regression: `make canary` case `main` (a `/1` take) under the existing per-case ceiling.
   5. `contract open` for the wave's caps **right before** the wave's canary cases, with a reason naming the approved canary; each case
      must pass; on any failure `contract close` the caps it covers. Only then do agents and the SPA see the features as estimable.
8. **Canary case** `v2-wire` (lane `g1-gate`, `internal/provider/runpod/canary.go` gains it in `CanaryCases`): a plain render sent as
   `/2` (needs `[v2]`), checking the `/2` output, the caps list and artifact parity with `/1`. Tracks add their cases (`tier-b`,
   `analyze`, `plan`, `fallback`, `pod-…`) in their own files. `TestCanary_V2WireCase`.

### C323. Jobs: kinds, needs and spec (migration `<next>_job_kinds.sql`)

1. **Rebuild** (`-- mm:foreign_keys=off`; generated block, D-G1-13). Changes against the table after the last migration, nothing else:
   - `kind TEXT NOT NULL CHECK (kind IN ('generate','analyze','plan'))`;
   - `needs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(needs_json) AND json_type(needs_json) = 'array')`;
   - `spec_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(spec_json) AND json_type(spec_json) = 'object')`;
   - `CHECK (state <> 'completed' OR track_id IS NOT NULL)` becomes `CHECK (state <> 'completed' OR track_id IS NOT NULL OR kind =
     'plan')`; new `CHECK (kind <> 'plan' OR track_id IS NULL)` and `CHECK (kind <> 'analyze' OR source_track_id IS NOT NULL)`;
   - `jobs_frozen` lists `kind, needs_json, spec_json` too. Every other column, index and trigger is re-created verbatim; existing rows
     get `generate`, `[]`, `{}`.
2. **Generator.** `scripts/gen-jobs-ddl.go` gains `-profile engines|job_kinds` (a table of edits per profile); `-check` passes for both
   `0015_engines.sql` (profile `engines`, unchanged output) and the new file. `TestGenJobsDDL_CheckBothProfiles` (in `make check` through
   `make regen -check`).
3. **`internal/jobspec`** (leaf): `type Spec struct{ Inputs *Inputs; Ears *Ears; Plan *Plan; Attach []AttachRef }` with `Inputs{SrcAudio
   *Pin}`, `Pin{TrackID, MasterSHA256 string; Bytes int64}`, `Ears{Lang string; BudgetS int; Stages []string; KeepVocals bool}`,
   `Plan{Seeds []int64; SecondsPerSeed int}`, `AttachRef{Name, SHA256, Source string}` (`Source` names where the server reads the bytes at
   submit, e.g. `plan:<id>/score.abc`; the Desk defines its sources); `Parse([]byte) (Spec, error)` strict; `Needs(kind string, s Spec)
   []string` sorted and canonical (`v2` plus `ears`, `analyze`, `plan`, `attach` as the spec implies). `TestJobspec_StrictParse`,
   `TestJobspec_NeedsCanonical`.
4. **Create path.** `generation.planTake` gains `Kind string \`json:"kind,omitempty"\``, `Needs []string \`json:"needs,omitempty"\``,
   `Spec json.RawMessage \`json:"spec,omitempty"\``; `insertBatch` writes them (empty ⇒ `generate`, `[]`, `{}`); an `analyze` take sets
   `ReuseRecipeID` to the analysed take's recipe. Every existing estimate is byte-identical. `TestInsertBatch_DefaultsByteIdentical`.
5. **Inputs from pins.** `jobs.InputMinter` gains `MintPins(ctx, pins jobspec.Inputs, expires time.Time) (map[string]provider.InputRef,
   error)` on the existing signed-media minter (`jobs.NewInputMinter`, `internal/jobs/inputs.go`): the master is re-read by track id and
   its sha256 must equal the pin (else the attempt fails `INPUT_MISMATCH` before submit, `ErrInputChanged`). `takeSpec` mints pins for any
   engine; the ACE request-derived path is untouched. URLs are never persisted, logged or returned
   (`TestTakeSpec_InputsNeverPersisted` extended to a yue2 analyze job). `TestInputs_FromSpecPins`.
6. **Ingest by kind** (`internal/jobs/seams_g1.go`):
   ```go
   const (KindGenerate = "generate"; KindAnalyze = "analyze"; KindPlan = "plan")
   type KindIngest struct{ Job JobRow; Attempt AttemptRow; Files []upload.Received; Output provider.Output; EnvID string; Fence store.LeaseFence }
   type KindIngester interface {
       IngestKind(ctx context.Context, tx *sql.Tx, in KindIngest) (trackID *string, outcome string, err error) // inside the take tx
       AfterKindCommit(ctx context.Context, in KindIngest)                                                   // post-commit, warnings only
   }
   // IngestOptions gains Kinds map[string]KindIngester; "generate" is never in the map (today's path).
   ```
   A completed `analyze` or `plan` job whose kind has no ingester fails `INGEST_FAILED` with `ingest_code = KIND_UNAVAILABLE`.
   `TestIngest_DispatchByKind`, `TestJobsReaders_PlanJobVisible` (`list_jobs`, `get_job`, `get_batch` show a completed plan job with
   `track_id` null).
7. **Take hooks.** `jobs.MultiTakeHook(hs ...TakeHook) TakeHook` (in order; the first error rolls the take back); `TakeInfo` gains `Kind`
   and `Needs`; `app.lateTakeHook` holds a list, and tracks add theirs from their own `internal/app` file. `TestMultiTakeHook_OrderAndRollback`.
8. **RTF learning** (D-M5-36 kept): only `kind = 'generate'` with empty `needs` updates `rtf_stats` or `engine_rtf`.
   `TestRTF_OnlyPlainRenders`.
9. **Upgrade test.** `TestMigration_JobKinds`: a database at the previous version with fixture jobs in every state (a song PR job, a stem
   attach job, a cancelled and an expired job) migrates with identical column values, the three defaults, every index and trigger
   present, and the refusals of `TestJobs_KindCheck_Table` (unknown kind; generate completed without track; plan with a track; analyze
   without source; needs not an array; spec not an object; frozen columns).

### C324. Scheduling, time zone and background loops

1. **`internal/schedule`** (leaf):
   ```go
   type Key struct{ Day string /* local YYYY-MM-DD */; At string /* HH:MM */; Planned time.Time }
   type Daily struct {
       Name     string
       At       func(ctx context.Context) (at string, enabled bool, err error) // re-read on every wake; an error keeps the previous value
       Loc      *time.Location
       Clock    clock.Clock
       CatchUp  time.Duration                              // run a missed slot of today if it is at most this late (0 = never)
       Done     func(ctx context.Context, k Key) (bool, error) // consulted before a catch-up run; nil = never catch up
       Run      func(ctx context.Context, k Key)
       MaxSleep time.Duration                              // default 1 min: an edited time takes effect within a minute
   }
   func (d *Daily) Loop(ctx context.Context)
   func NextRun(now time.Time, at string, loc *time.Location) time.Time // moved from internal/backup; backup.NextRun delegates
   func DayKey(t time.Time, loc *time.Location) string
   ```
   DST: a nonexistent local time runs at the first valid instant after it; a repeated one runs once (the first occurrence, as
   `backup.NextRun` does today). Tests (synctest): `TestDaily_RunsAtLocalTime`, `TestDaily_DSTSpringAndAutumnOnce`,
   `TestDaily_AtChangeMovesNextRun` (06:00 changed to 05:00 at 04:00 ⇒ runs at 05:00), `TestDaily_AtErrorKeepsPrevious`,
   `TestDaily_DisabledWaits`, `TestDaily_CatchUpWithinGrace`, `TestDaily_NoCatchUpAfterGraceOrWhenDone`, `TestDaily_KeyIsLocalDay`;
   the existing `internal/backup` tests pass unmodified.
2. **`MM_TZ`** (`internal/config/config_g1.go`): IANA name, default empty (= the process zone); invalid ⇒ startup error naming the
   variable; `Config.Loc *time.Location`; `metamusic config check` prints `time zone: <name>`; the binary imports `time/tzdata` so the
   zone resolves even without system zoneinfo. `TestConfig_TZ`, `TestConfigCheck_PrintsTZ`, `TestTZData_Embedded`. The runbook's config
   table lists it (`TestDocs_ConfigTableListsEveryEnv`).
3. **Consumers.** Morning Crate: `Daily{At: 07:00 from its settings, CatchUp: its choice, Done: a crate exists for the key}`; Night
   Shift: `Daily{At: its settings time}`; each run row is unique on the local-day key (the consumer's migration).
4. **Loops.** `app.Services` gains `Loops []Loop` (`type Loop struct{ Name string; Run func(ctx context.Context) }`); tracks append from
   their `internal/app/<track>.go`; `serve` starts each in the prune group and waits for all on shutdown; a panicking loop is logged and
   restarted after a minute (never kills `serve`). `TestServe_StartsAndDrainsLoops`, `TestServe_LoopPanicRestarts`.

### C325. Backend set (draft G0) reconciled, and what the pod backend still needs

1. **Built by E2 and reused unchanged:** `provider.Set` (ordered, immutable, `Default`, `Get`, `Names`, `Route`, `EngineOf`;
   `set.go`), `provider.GateSet` and one gate per backend (cold start, runtime, engine; `gateset.go`), `Backend.ColdStartS/Engine/
   MaxRuntime`, the runner over the set (`Runner.UseSet`), one reconciler per backend (`jobs.NewReconcilers`, adoption, sweep, trips,
   drift, meter, billing), `jobs.NewMeterRouter`, per-backend billing windows, buckets, bill rows and worker meter
   (`0016_billing_backend.sql`), per-backend `provider_state` and trips, the global worker bound (`spend/global_bound.go`, C151.5),
   per-backend `get_provider_status` and `runpod arm|disarm|reset-trip --backend`.
2. **What the pod backend still needs (owned by the C390s track, not G1):** `Set.Route` accepting a second yue2 backend when
   `requested` names it (default routing unchanged; `TestBackendSet_Routing` gains rows); generalising `app.e2ExtraBackends` (today
   ACE-only behind `cfg.ACEStep`) into a list of extra-backend constructors with their own enablement (`MM_POD_BACKEND` plus the key
   file); a `Backend` implementation whose `Submit` queues for the pod's pull, `Poll` reads app-side state, `Sweep` lists `mm-*` pods,
   `ScaleToZero` terminates them and `Billing` reads pod billing; the pull endpoint under `/worker/v1/pods/`, dead-man and orphan rules;
   the `pod_overhead` reserve (a ledger `reserve` row with a new Go-validated reason, no migration: `ledger.reason` is Go-validated); the
   per-backend batch bound (batches allow 1..100 takes in SQL, Go enforces 50 today, `generation.MaxTakes`); `MODE=agent` on the stage
   registry with capability `agent`. The capability gate (C322) applies to the pod backend's own image digest.
3. **No ACE-Step dependency:** the pod backend is engine `yue2`; it reuses the yue2 image; nothing routes to `acestep15`.
4. **Upload fallback provider (D-G1-18).** Recommended: **Cloudflare R2**, one private bucket in the EU jurisdiction, a bucket-scoped
   API token with object read and write only, an object lifecycle rule "delete 7 days after upload" (R2 supports expiration rules from the
   dashboard and the S3 `PutBucketLifecycleConfiguration` call; deletion happens within about a day of expiry). Reasons: presigned
   S3 SigV4 `PUT` URLs with region `auto` and expiry up to 7 days (an attempt window is at most 14 h); no egress fees when the app fetches
   the objects back; the expected volume (fallback only, a handful of takes a year) sits inside the free tier; an EU jurisdiction is
   available. Limits the C390s track designs for: no POST policies (we only use `PUT`), no size enforcement in a presigned `PUT` (the app
   verifies `bytes ≤ max_bytes` and the sha256 while fetching, bounded reader), the worker sends its own `User-Agent` (Cloudflare 1010
   trap). Backblaze B2's S3 API is the alternative (prefix-scoped keys, cheaper storage, lifecycle by hide-then-delete); the client is
   generic, so switching is configuration.

### C326. Rules every new operation follows (U1, MCP, parity)

1. **Registration.** Every capability is a Huma operation via `api.Register` with a complete `api.Meta` (scope, cost, mcp, ui,
   human_only, owner_only). No private SPA endpoint; binary routes are the documented exception (`mcp: exclude:binary`).
2. **Role classification.** Every new op gets a row in `internal/api/testdata/role_matrix.golden`, generated (`go test ./internal/api -run
   TestRoleMatrixComplete -update`, through `make regen`) and reviewed in the PR diff; `TestRoleMatrixComplete`,
   `TestRoleMatrixDelegatesUnchanged`, `TestRoleMatrixFailClosedHolds` and `TestRoleMatrixListenerBounds` must pass. Default
   classification for this program (a track plan may loosen a row only with a written reason in its C305-style table):

   | Family | Scope | Owner | Editor | Listener | Agents |
   |---|---|---|---|---|---|
   | New GPU work (Tier B option, analyze mode, plan modes, render-from-plan, pod bulk) | through `estimate_generation`/`create_generation` (D-G1-16) | Y | Y, always awaiting Owner approval (U1 `editor_batch`) | N | by scope; bulk pods human only |
   | Spending authority (approve/reject/revoke mandate, enable night shift) | `generate` or `admin`, `HumanOnly`, `OwnerOnly`, `cost: approve` where it decides money | Y (two-factor page) | N | N | N (`exclude:human-only`) |
   | Agent proposals (propose mandate, queue a night item) | `generate` | Y | N | N | Y |
   | Curation signals (Morning Crate events and reveal, diction marks, critic bench) | `write`/`read` | Y | N unless the track says | N | as the track says |
   | Reads of new derived data (diction, adherence, plans, crate after reveal) | `read`, or `listen` only if a Listener may see it | Y | Y (money removed) | only if `listen` | Y |

3. **Money.** Integer micro-USD only. Any op an Editor or Listener reaches that returns money carries shape rules (`MoneyKeys` or explicit
   `Cost` paths) in the track's `shape_<track>.go`; the money fields are optional in the schema; `TestIntBMoneyFieldScan` passes with no new
   allow-list entry unless the track justifies it.
4. **Listener visibility.** Only scope `listen`/`rate` ops reach a Listener and they get the common rules (`listenerCommon`) for free;
   any new read that returns tracks filters discarded takes through `view.Viewer.NotDiscarded` / `library.Visible`
   (`internal/archtest/visible_u1_test.go` must keep passing); discarded is `404`, never `403`.
5. **MCP** (D-G1-11): `mcp: generic` by default; `exclude:human-only` for spending authority; no new `tool`. A lane that grows the schema
   of an existing first-class tool (for example `estimate_generation` gaining the analyze mode, `get_track` gaining Tier B fields)
   re-baselines `toolsCeilingBytes` with a comment line `<lane>: <old> -> <new> (measured …, +…): <what>` and keeps the program total under
   the 4 000-byte budget; `TestToolsList_Ceiling` and the 39-tool assertion in `TestToolsList_M5Budget` hold.
6. **Parity.** Each track adds `internal/mcpgen/parity_<track>_test.go`: every non-excluded new op answers the same body over REST and
   `call_operation` (the `parity_m5_test.go` pattern), and every excluded op is absent from `call_operation`'s catalogue. The U1 sweeps
   (`internal/acceptance/intb_*_test.go`) run over the new ops automatically and must stay green.
7. **Human-only exceptions** need the operator's sign-off, listed in each track plan's operator inputs (the draft's `set_night_shift` and
   mandate decisions are examples).
8. **Errors** are problem+json with stable UPPER_SNAKE codes; GET never mutates; no physical delete (status flags); secrets never in
   bodies, logs, events, audit or OpenAPI.

### C327. Spend invariants every track keeps

1. Every GPU job is a job in a batch created by `generation` from an estimate, with M3's reservation, approval and settlement; no
   other code path submits to a backend. `settled + reserved ≤ cap` (the M3 property, E2's two-backend variant) stays green.
2. No code in this program writes `spend_caps`, a token's daily budget or a connector grant's limits; a mandate never raises a cap
   (D-G1-OP-3). `TestNoCapWriterOutsideSpend` (archtest, lane `g1-seams`): only `internal/spend` writes `spend_caps`.
3. A track that adds a quote term extends `spend.TakeIn` with a field whose zero value is byte-identical (the E2 pattern;
   `TestQuoteBatch_Example`, `TestQuoteTakes_Yue2Golden` and `TestQuoteTakes_YueUnchanged` pass unmodified).
4. Comparisons against a money limit use the estimate's **ceiling** (`ceiling_micro_usd`) unless a plan states otherwise (D-G1-OP-3).

### C328. Shared hot spots, ownership and merge discipline

**Hot spots** (one writer at a time; "append" means the lane adds its own block and keeps every other line):

| Files | Rule |
|---|---|
| `internal/api/api.go` (`Deps` fields, `register*` calls), `internal/api/registry.go`, `permit.go` | append one `register<Track>` line and the track's `Deps` fields; `registry.go`/`permit.go` change only by amending C326 |
| `internal/api/shape_u1.go`, `wireNames` in `api.go` | frozen after `g1-seams`; tracks add `shape_<track>.go` / `wire_<track>.go` (D-G1-12) |
| `openapi.json`, `web/src/api/schema.d.ts`, `internal/api/testdata/role_matrix.golden`, `internal/mcpgen/testdata/*.golden.json` | generated: `make regen` after every rebase; never hand-edited; diffs reviewed |
| `internal/mcpgen/m2a_contract_test.go` (`toolsCeilingBytes`) | one re-baseline line per lane, recomputed after rebase |
| `internal/store/migrations/` | renumber on rebase; one migration per lane |
| `internal/lineage/modes.go` | append a per-track `…Modes` slice |
| `internal/generation/{service.go,types.go,create.go,estimate.go}` | G1 edits once (`g1-jobs`, `g1-gate`); tracks put mode logic in `expand_<track>.go`, `estimate_<track>.go`; a shared-file edit is a small serial PR in merge order |
| `internal/spend/quote.go` | additive `TakeIn` terms (C327.3): Ears first, then pods |
| `internal/jobs/{submit.go,claim.go,ingest.go,rows.go}` | G1 only; tracks use `KindIngester`, `TakeHook`, `jobspec` |
| `internal/provider/{provider.go,parse_input.go,artifacts.go}`, `worker/src/mmworker/{contract.py,handler.py,schema/*}` | G1 owns; tracks extend additively (new optional properties, new artifact rows) with the triple-equality tests; one worker lane merges at a time |
| `worker/Dockerfile`, `.github/workflows/worker-image.yml`, `deploy/worker-image.digest` | worker lanes in sequence; the digest file only in a wave PR after CI publishes |
| `internal/provider/fake/simworker.go` | G1 adds a kind-hook table; tracks add `simworker_<track>.go` |
| `internal/provider/runpod/canary.go` (`CanaryCases`) | append cases; one lane at a time |
| `internal/app/m3.go`, `internal/app/m5.go` | G1 edits once (fake kind hooks, take-hook list); tracks add `internal/app/<track>.go` |
| `cmd/metamusic/cmd_serve.go` | G1 only (loops); new CLI commands in `cmd_<name>.go` |
| `internal/config/config.go` | frozen; tracks add `config_<track>.go`; runbook config table updated in the same PR |
| `docs/runbook.md`, `docs/architecture.md` | each track's docs lane appends its sections; `architecture_doc_test.go` passes |
| `web/src/lib/{nav.ts,router.ts}`, `web/src/routes/More.svelte`, `web/src/App.svelte` | one web lane at a time; each adds its routes, `ROUTE_OPS` rows and nav entries in one block; `navGate.test.ts` passes |
| `scripts/e2e-map.txt`, `web/e2e/*.spec.ts`, `coverage.thresholds`, `internal/archtest/*`, `internal/acceptance/*` | per-track files and appended lines |

**Merge discipline (binding):**
1. One lane, one worktree (`metamusic-wt/<lane>`), one branch, one PR; `MM_LANE` set; TDD; the lane's named tests first.
2. Before asking for merge: rebase on `main`, rename the migration if needed, `make regen`, `make check`, the focused package tests,
   `make gate`. A PR with conflicts gets no CI run, so a green CI only counts on a conflict-free, freshly rebased branch.
3. The integrator (Opus) merges **one PR at a time**; after each merge every open PR rebases and regenerates before its CI counts again.
4. Order: `g1-seams` → `g1-jobs` → `g1-wire` → `g1-gate` → `g1-worker`; then track lanes by dependency. A track lane may merge between
   foundation lanes only if it depends on nothing unmerged and touches no G1-owned file.
5. Worker lanes merge one at a time and ship in waves (C322.7); the server half of a worker feature merges before its worker half.
6. Deploys are attended; `metamusic config check` first; the `jobs` rebuild is rehearsed on a production copy (G1-X2) before it is
   deployed; nothing is deployed with a live attempt (`scripts/deploy-preflight.sh`).

### C329. Harness, tooling and test seams for the program

1. **`make regen`** (through `scripts/loop`): `make openapi`, the role-matrix update, the mcpgen golden updates, `go run
   scripts/gen-jobs-ddl.go -check` for every profile, `go test ./internal/provider -run TestWriteArtifactsV2 -update`; idempotent
   (`scripts/regen-selftest.sh`: a second run changes nothing).
2. **Fake `/2`.** `fake` speaks `/1` and `/2`; `SimKind` hooks per kind/stage (`fake.SimKind{Kind, Stage string; Run func(…) error}`)
   are passed at construction (`internal/app` collects them from per-track files); an unregistered kind answers `KIND_UNAVAILABLE`.
   `TestSimWorker_V2Kinds`, `TestFake_AllCapsOpen`.
3. **Rig.** `servertest.Rig` gains `OpenCaps(backend string, caps ...string)` (writes a gate row for the fake's digest) and
   `WithLoc(*time.Location)`; synctest drives `schedule.Daily`.
4. Heavy suites run in CI only; agents run focused package tests and `make check` (operator rule).

## Rollout and risks

1. **Jobs rebuild** (highest): a non-additive migration on the table every spend path uses. Mitigation: generated DDL with `-check`,
   `TestMigration_JobKinds` over every state, a production-copy rehearsal (G1-X2: `VACUUM INTO`, migrate, `library verify`, job and
   ledger counts equal), an attended deploy with no live attempt, backup first.
2. **Wrong image for a `/2` job** (paid failure): closed gate by default, digest binding, estimate refusal, claim filter, auto-close;
   the worst case is one short attempt on a stale worker during a provision (accepted).
3. **Strict output decoding** makes an image that emits `/2` fields fatal to an older server: server first, always (D-G1-14); the wave
   checklist starts with the server deploy.
4. **Hot-spot churn** across about thirty lanes: per-file registration, `make regen`, one merge at a time, generated files never
   hand-merged.
5. **Image growth** from the ears stack (cold starts, unbilled pulls but longer first starts) and licences (D-G1-15): the Ears track
   measures and decides; FlashBoot and the runbook's "a long first start is not a hang" note apply.
6. **Scheduler drift and restarts**: a deploy at 07:00 could skip a crate; `CatchUp` + `Done` cover it; DST tests.
7. **MCP growth**: the 4 000-byte budget and the stop-and-ask rule.
8. **ACE-Step code left in the tree**: dormant, its tests keep running; the archtest keeps new code off it.

## Lanes and tasks

Each lane is one PR for one Sonnet builder (target ≤ 2.2k changed lines excluding generated files). Checklist per task: write the named
tests, watch them fail, implement, `make check` plus focused tests, record any amended contract here in the same commit.

| Lane | Task | Scope | Depends | Est. size |
|---|---|---|---|---|
| g1-seams | **G1-F1** | re-verify this plan's citations (Revision log); `internal/schedule` + tests; `backup.NextRun` delegates; `MM_TZ` + `time/tzdata` + `config check`; `app.Services.Loops` and the `serve` change; `addShapeRules` / `addWireNames` per-file registration (existing maps move behind them, byte-identical OpenAPI); `make regen` + selftest; `TestImportGraph_G1`; `TestNoCapWriterOutsideSpend` | none | ~1.3k |
| g1-jobs | **G1-F2** | generator profiles; `<next>_job_kinds.sql`; `internal/jobspec`; `JobRow`/`scanJob` fields; `planTake` fields and `insertBatch`; `KindIngester` dispatch; `MultiTakeHook` and `TakeInfo.Kind/Needs`; `app.lateTakeHook` list; RTF rule; tests of C323 | F1 | ~2.0k |
| g1-wire | **G1-F3a** | `/2` constants, Go types, builder, parser, output decode, `WireFor`; `/2` schemas; artifact table per kind; `TestWriteArtifactsV2` export; `MintPins` and `takeSpec` from spec; fake `/2` and `SimKind` hooks (`internal/app/m3.go` once); tests of C321.10 (Go) and C323.5 | F2 | ~2.0k |
| g1-gate | **G1-F3b** | `<next>_worker_contract.sql`; `provider.WorkerCaps`; claim filter; `requireCaps` in estimate and create; auto-close in settle; `WORKER_OUTDATED`; CLI `runpod contract`; `get_provider_status.worker`; canary case `v2-wire`; `Rig.OpenCaps`; tests of C322 | F3a | ~1.7k |
| g1-worker | **G1-F4** | Python `/2`: `contract.py` dispatch, `caps.py`, stage registry (`mmworker/stages/`), kinds framework in `handler.py` (render as today; analyze and plan raise `KIND_UNAVAILABLE` until a stage registers), attachments, deadlines, `/2` output, selftest, Dockerfile labels, CI smoke of label vs selftest; ACE tests untouched; Python tests of C321.10 | F3a (schemas) | ~1.4k |
| g1-int | **G1-X1** | docs: architecture (worker protocol, gate, kinds, scheduler), runbook (worker wave checklist, `runpod contract`, `MM_TZ`, rebuild deploy), roadmap edit (M5b/M6/T1b program, G1 entry), revision log | F1–F4 | small |
| g1-int | **G1-X2** | production-copy rehearsal of the jobs rebuild (spend-free; `VACUUM INTO` copy, migrate, `library verify`, counts equal; copy deleted); then the attended server deploy (disarm, drain, deploy, arm) | X1 | small |
| g1-int | **G1-X3** | operator: worker wave W1 (CI-published image with caps `v2`), digest PR, attended provision, canary `main` (regression) and `v2-wire` (spend approval), `contract open --caps v2` | X2, operator | small |

## Acceptance mapping

| Deliverable | Named tests |
|---|---|
| `/1` unchanged for every existing job | `TestTakeSpec_Yue2InputByteIdentical`, `TestInsertBatch_DefaultsByteIdentical`, `test_v1_documents_unchanged`, M3 quote goldens unmodified |
| `/2` documents agreed by Go, Python and the schemas | `TestWorkerSchemasV2_MatchParser`, `TestParseWorkerInput_V2_Table`, `TestArtifacts_V2SchemaEnum`, `test_parse_v2_table`, `test_artifacts_v2_equal_go_table` |
| No spend on an image that cannot run the job | `TestEstimate_RefusesClosedNeeds`, `TestCreate_RechecksNeeds`, `TestClaim_NeedsSubsetOfOpenCaps`, `TestClaim_DigestChangeClosesCaps`, `TestSettle_ContractVersionAutoCloses`, `TestSettle_KindUnavailableRemovesCap` |
| One jobs rebuild without loss | `TestMigration_JobKinds`, `TestJobs_KindCheck_Table`, `TestGenJobsDDL_CheckBothProfiles`, G1-X2 counts |
| Kinds ingest through seams | `TestIngest_DispatchByKind`, `TestJobsReaders_PlanJobVisible`, `TestMultiTakeHook_OrderAndRollback`, `TestRTF_OnlyPlainRenders` |
| Daily schedules in the configured zone | `TestDaily_*`, `TestConfig_TZ`, `TestTZData_Embedded`, `TestServe_StartsAndDrainsLoops` |
| Conflict-free merges | `scripts/regen-selftest.sh`, per-file registration tests, `TestImportGraph_G1` |
| Image rollout | `TestCanary_V2WireCase`, worker-image smoke (label = selftest), G1-X3 |

## Definition of done (G1)

F1–F4 merged through branch-protected `main` one at a time; `make check` and CI heavy tiers green; `TestTakeSpec_Yue2InputByteIdentical`
and the M3 quote goldens unmodified; the rebuild rehearsed on a production copy and deployed attended; the wave W1 image published,
its digest committed, the endpoint provisioned, canary cases `main` and `v2-wire` passed and capability `v2` open on the yue2 backend;
docs, runbook and roadmap updated; `make gate` clean. The four track plans cite C320–C329 by number and add no second jobs rebuild.

## Operator inputs

1. **Jobs rebuild deploy** (G1-X2): an attended deploy slot after the production-copy rehearsal (spend-free).
2. **Worker wave W1** (G1-X3): merge-time approval of the digest PR; an admin-key session for `runpod provision --image`; written approval
   of the canary cases `main` and `v2-wire` with `--max-usd` (per-case ceiling in the private appendix); then `runpod contract open --caps
   v2`.
3. **`MM_TZ`** value in the production env (private appendix), checked with `metamusic config check` before the deploy.
4. **MCP policy**: confirm "no new first-class tool, 4 000-byte growth budget" (D-G1-11).
5. **Upload fallback provider**: confirm R2 (D-G1-18) or choose B2 before the C390s track's bucket step.

## Open questions for the operator

1. Should a later wave also open `ears` automatically when its canary passes, or always by hand? (Assumed: by hand, C322.7.)
2. Catch-up for the Morning Crate after a restart past 07:00: build late (within two hours) or skip the day? (The C365s track asks.)
3. MCP growth budget of 4 000 bytes: enough, or should the Ears fields on `get_track` stay out of the tool schema (generic
   `describe_operation` only)?

## Revision log

Draft 1 (2026-11-20): design only, read against `origin/main` `aec888b`. Every citation in the baseline table was read for this draft;
lane G1-F1 re-verifies them before the contracts freeze and records corrections here.

Revision 2 (2026-11-20, orchestrator): the track plans' "Amendments requested to foundations" sections are settled here. Every accepted
amendment is part of the contract it names. Where a G1 lane has not merged yet, that lane owns the change. Where it has merged, the first
track lane that needs it makes the change as a declared serial edit.

- **Registration seams (accepted, lane G1-F1):**
  - an `api.Deps` pass-through from `app.Build` into `httpserver.New`;
  - an append-only per-track build hook list in `internal/app`;
  - per-track registration of media resolvers;
  - `audit.AddSystemOps`, validated once at startup;
  - per-file registration of `ItemIn.Mode` values.

  These requests come from the night plan (A1–A3) and the desk plan (A4). With these seams, the tracks stop editing `httpserver/server.go`,
  the `app.Build` literals, `audit/system.go` and `types_m3.go:116`.
- **Ingest kinds and generation sources (accepted, lane G1-F2):** registered per track through `internal/app` (desk A5). G1-F2 built the ingest kinds and take hooks only; generation sources are deferred to the Desk lane (Revision 4, C323.7).
- **RTF learning (accepted, lane G1-F2):** a `generate` job teaches `rtf_stats` when its needs are a subset of
  `{v2, ears, agent, fallback}`. The `/2` `timings.e2e_seconds` covers the render only; Tier B time is reported in `ears.seconds`. This
  covers ears A1 and bulk A3.
- **Other G1-F2 changes (accepted):**
  - `MintPins` signs the master with its own extension, so a lossy reference can be an analyze input (ears A4);
  - the job views (`get_job`, `list_jobs`, the `get_batch` job rows) expose `kind` (ears A5);
  - `needs` may carry a capability that the routed backend requires, added at estimate and frozen at create, through
    `jobspec.WithCaps` (bulk A2).
- **Protocol (accepted, lane G1-F3a):**
  - the `/2` output's `ears` block carries `seconds` (ears A2);
  - `plan-NN.tar` bundles are at most 2 MiB (desk A1);
  - the plan-kind worker rules are those of desk A2;
  - `attach` stays reserved with no v1 consumer (desk A3);
  - the Tier B post-stage is step 6b of the render order (ears A6).
- **Capability gate (accepted, lane G1-F3b):**
  - `runpod contract close` reports the queued jobs that need the closed capabilities and requires `--strand-queued` when there are any;
  - the claim loop fails a job whose needs stayed closed for more than one hour with `WORKER_OUTDATED` and releases its reservation
    (ears A3);
  - the `ParseCanaryCase` error text is generated from `CanaryCases` (desk A8);
  - `selftest` and `caps.py` accept the transport capabilities `agent` and `fallback`, which have no stage (bulk A8).
- **Import graph (accepted, lane G1-F1):** `TestImportGraph_G1` checks DIRECT imports only. Today `jobs`, `generation`, `httpserver` and
  `provider/runpod` reach `engine/acestep` transitively, so a transitive check could not list any package that imports them. New packages
  that want the stronger guarantee add their own transitive test, as bulk does. `internal/objstore` joins the C390s package list (bulk A1).
- **Bulk seams (bulk A4, A5):** the upload-fallback minter (`RunnerOptions.Fallback`) and the rescue hook (`RunnerOptions.Rescue`) stay
  in the bulk track, as serial edits by lane `bulk-fallback` after `g1-gate` has merged.
- **Text-only corrections:**
  - pods are "Owner acting as human only" (bulk A7);
  - agent proposals are empty in v1 (night A4);
  - the T1b soundfont is the pinned MIT FluidR3_GM, fetched by checksum (desk A7), so D-G1-15 holds;
  - a plan recipe without a take is normal (desk A6).
- **Open questions answered by the orchestrator with the recommended defaults:**
  1. capabilities are opened by hand after each canary;
  2. the Morning Crate catches up within 2 h after a late restart (D-M6N-13);
  3. the 4 000-byte MCP growth budget stands and is split ears 1 200, desk ≤ 1 000 and bulk ≈ 300, with the night track taking the rest.

Revision 3 (2026-11-20, lane G1-F1 `g1-seams`, built on `origin/main` `3143050`): the baseline table was re-read against the code. Code is
unchanged since `aec888b` (only plan documents merged), and every citation this lane touched still holds at the cited line: `retain.go:151-197`
(`NextRun`, `Scheduler`), `cmd_serve.go:270-301` (the goroutine list) and `:293-301` (the backup scheduler), `app.go:115` (`Services`),
`types_m3.go:116` (`ItemIn.Mode`), `shape_u1.go:20-90`, `Dockerfile:41` (`tzdata`), 150 operations in `role_matrix.golden`, ceiling 117 406.
Amended contracts, as built:

- **C324.1 `Daily`** is built as specified, with `Clock` nil meaning the wall clock and `Loc` nil the process zone. A slot is run when it
  was crossed since the previous wake; the in-process guard is the pair (local day, `HH:MM`) and only stops a repeated wake (the autumn
  overlap), never a second process. `backup.NextRun` delegates to `schedule.NextRun`; `backup.Scheduler` is unchanged and keeps the process
  zone (`MM_BACKUP_AT` is documented as such), so setting `MM_TZ` does not move the nightly backup.
- **C324.2 `MM_TZ`**: `Config.Loc` is **nil when the variable is empty**, and `Config.Zone()` returns `time.Local` for nil (the existing
  `Config` equality tests stay unmodified). `MM_TZ=Local` is refused (spell the process zone by leaving it empty); errors never echo the
  value. The config table that `TestDocs_ConfigTableListsEveryEnv` checks is the **README**, not the runbook; the runbook row is the G1-X1
  docs lane's. `config check` prints `time zone: <name>` (`Local` for the process zone).
- **C324.4 loops**: `app.Loop`, `Services.Loops`, `app.StartLoops(ctx, loops, wg)`; serve calls it once, in the prune group. The two named
  tests are `TestServe_StartsAndDrainsLoops` and `TestServe_LoopPanicRestarts` in `internal/app` (the helper lives there, so serve itself
  needs no test double). The panic text goes through `redact.String` (pinned by the log assertion in `TestServe_LoopPanicRestarts`). No test boots
  `serve`; `TestServeStartsTrackLoops` in `internal/archtest` pins that `cmd_serve.go` calls `app.StartLoops` with `svc.Loops`.
- **Registration seams (Revision 2), as built**, all init-time and append-only:
  - `api.Deps` pass-through: `httpserver.Options.Deps *api.Deps`; `api.Deps.Fill` copies every field the `Options` fields left zero, so a
    track that adds a `Deps` field adds nothing to `server.go`. `app.Build` sets `Server.Deps = &Services.Deps` (also what `Gated()` uses).
  - Build hooks: `app.RegisterBuildHook(func(*BuildEnv) error)` (init-time, registration order) and `Options.Hooks` (tests). A hook runs after
    `Services.Deps` is filled and before `Server` is built; it may set `Deps` fields, `AddLoop`, `AddResolver`. A hook error aborts `Build`.
  - Media resolvers: `BuildEnv.AddResolver` and `app.RegisterResolvers(func(*BuildEnv) []mediaserve.Resolver)`; they are chained after the
    built-in resolvers.
  - `audit.AddSystemOps(ops...)` registers `actor:name` rows (actor `runner`, `reconciler`, `cli` or a lowercase track actor). `audit.SystemOps`
    stays the 24-row M3 ledger pinned by its own test; `audit.AllSystemOps()` is the union, `IsSystemOp` knows both. A malformed or duplicate
    registration never panics at init; `app.Build` returns it through `audit.ValidateSystemOps()` (once per start).
  - `addShapeRules`, `addWireNames` and `addItemModes` (`internal/api/seams_g1.go`) panic on a duplicate (a duplicate wire component name too).
    The old maps moved behind them: `u1ShapeRules` (`shape_u1.go`) and `baseWireNames` (`wire_base.go`). `ItemIn.Mode` keeps its `enum` struct
    tag for the 15 modes of today (`TestSchema_E2Enums` reads the tag and stays unmodified); `addItemModes` appends further modes through
    `ItemIn.TransformSchema` and refuses one already in the tag. `openapi.json` is byte-identical.
    Tracks add `shape_<track>.go`, `wire_<track>.go` and `item_modes_<track>.go`.
- **C320.3 import graph**: `TestImportGraph_G1` checks direct imports only (Revision 2). The sentence about what `schedule` and `jobspec`
  may import was garbled; the rule enforced is: standard library plus `internal/clock` for `schedule`, plus `internal/canonjson` for
  `jobspec`, no third-party package. The package list adds `internal/objstore`. The token check (`BackendRunPodACEStep`,
  `BackendFakeACEStep`, `"acestep15"`) also fires on comments.
- **C327.2** `TestNoCapWriterOutsideSpend` scans `internal/` and `cmd/` for SQL writes to `spend_caps`; `servertest` (a helper subtree) and
  the migrations are exempt. Today only `internal/spend/caps.go` writes it.
- **C329.1 `make regen`**: `scripts/regen.sh` (target `regen` added to the `scripts/loop` allowlist, `metrics-row.jq` and the metrics schema)
  runs the `make openapi` recipe, `TestRoleMatrixComplete -update`, the three mcpgen golden tests with `-update`, and the existing
  `gen-jobs-ddl.go -check`. `scripts/regen-selftest.sh` (`make regen-selftest`) proves a second run is a no-op and a hand-edited golden is
  repaired; it runs real generators, so it is not in `gate.sh`'s light selftest list. Lanes append their step above the `REGEN-STEPS`
  marker (g1-jobs: DDL profiles; g1-wire: `TestWriteArtifactsV2 -update`).

Revision 4 (2026-11-20, lane G1-F2 `g1-jobs`, built on `origin/main` `87af833`): C323 as built, with the amendments the code forced.

- **C323.1 migration** is `0020_job_kinds.sql`, generated (`-profile job_kinds`). `needs_json` and `spec_json` are appended after
  `patch_id`, so the old columns keep their order and `INSERT ... SELECT` names the old columns only; the new ones take their
  defaults. `kind` loses its `Go-validated` comment and gains the CHECK. `TestMigration_JobKinds` (upgrade from 0019, thirteen
  states, byte-identical old columns), `TestJobs_KindCheck_Table`, `TestJobs_KindCheck_CompletionShapes`, `TestJobs_KindFrozen`.
  Older upgrade tests compare jobs rows through `dbSnapshot`, which now strips the two default columns (the 0019 precedent).
- **C323.2 generator**: `-profile engines|job_kinds` (a table in the script; each edit must match exactly once), `-check-all`
  checks every profile at its default file (what `make regen` runs). `-profile` is required; the old bare `-before 15` form is gone
  (`TestMigration_JobsRebuild_GeneratorCurrent` uses `-profile engines`). A table renamed by an earlier rebuild is stored with a
  quoted name; the generator accepts both. A lane that renumbers changes `before` and `file` in the profile table.
- **C323.3 `internal/jobspec`**: as specified, plus `jobspec.Canonical()`, `ParseNeeds`, the `Cap*` and `Kind*` constants, and
  validation inside `Parse` (lowercase 64-hex digests, 1 to 16 distinct seeds, ears stages within `separate|asr|transcribe`, at most
  16 attachments with distinct names). `Needs` returns an empty (non-nil) slice for a plain render, so a plain job stays on `/1`; every
  non-empty set includes `v2` (C322.1). `WithCaps(needs, caps...)` (bulk A2) is built with that signature.
- **C323.4 create path**: `planTake` gets `Kind`, `Needs`, `Spec` (omitempty; `TestInsertBatch_DefaultsByteIdentical` pins that a
  plain estimate has none of the keys). `insertBatch` writes them through `jobKindColumns`, which refuses an unknown kind, needs that
  are not a sorted set and a spec that does not parse strictly, and stores the spec canonically. Setting `ReuseRecipeID` for an
  analyze take is the Ears track's expand code; nothing in G1 creates an analyze or plan take yet.
- **C323.5 pins**: the method is on a **separate interface**, `jobs.PinMinter` (`MintPins`), implemented by the same
  `NewInputMinter` value, because widening `InputMinter` would have forced edits to three existing jobs test fakes. `takeSpec` is
  not changed here (lane G1-F3a owns "`takeSpec` from spec" and asks the runner's `Inputs` for a `PinMinter`). A lossy master is signed
  `<sha>.mp3` (`TestInputs_FromSpecPins`, `_LossyMaster`).
- **C323.6 ingest by kind**: `KindIngester`, `KindIngest`, `BridgeOptions.Kinds` as specified, plus an optional runner-facing
  `jobs.KindJobIngester` (`IngestKindJob`), implemented by `IngestBridge`; the runner calls it for every job whose kind is not
  generate and falls back to `INGEST_FAILED` / `KIND_UNAVAILABLE` for an Ingester that lacks it, so the plain `Ingester` interface and
  its test stubs are unchanged. `IngestTake` itself refuses a non-generate job with the same code. The bridge verifies only the
  kind-neutral part of the output (status ok, take id, every reported artifact matches its upload, `mm-result.json` present); the
  kind's own artifact rules belong to the ingester (and the artifact table per kind to G1-F3a). One fenced transaction writes the
  environment fingerprint, the ingester's rows, a `kind_ingested` job event (detail `kind`, `outcome`, `track_id`) and the upload
  consumption. A job whose kind transaction committed but whose row did not follow is completed from that event
  (`TestIngest_KindCommittedCompletesFromEvent`). The completed job has `outcome` from the ingester ("" means clean), `track_id` only
  when the ingester returned one, and no truncation columns.
- **C323.7 hooks**: `jobs.MultiTakeHook`, `TakeInfo.Kind` and `TakeInfo.Needs` as specified. `app.lateTakeHook` keeps its `target`
  (the M5 callbacks service, always first) and gains `extra`; tracks add through `BuildEnv.AddTakeHook`. Ingest kinds are registered
  through `BuildEnv.AddKindIngester(kind, ki)` (analyze and plan only, once each; an error aborts Build). The kinds map is created in
  `m5Late` and shared with the bridge by reference, so a build hook can fill it after `buildM3`. Generation sources have no
  consumer in G1 and were not built: the Desk adds its own late field (`PlanSource`) next to this seam.
- **C323.8 RTF**: `jobs.LearnsRTF(kind, needs)` is the rule of Revision 2 (generate with needs within `{v2, ears, agent, fallback}`),
  applied to both `rtf_stats` (yue2) and `engine_rtf` learning (`TestRTF_OnlyPlainRenders`, `TestRTF_GenerateNeedsDecideLearning`).
- **Job views** expose `kind` (`jobs.Job.Kind`, `generation.JobView.Kind`, API `JobSummary.kind`, enum `generate|analyze|plan`):
  `list_jobs`, `get_job`, `get_batch` and the create/estimate batch views. `openapi.json`, `schema.d.ts` and `tools_list.golden.json`
  were regenerated.

Revision 5 (2026-11-21, lane G1-F3a `g1-wire`, built on `origin/main` `d8bc577`): C321 as built, and C323.5 / Revision 4 per-kind
verification, with the amendments the code forced.

- **C321.1 / C321.2 constants and types**: `provider.ContractV1`, `ContractV2` and `Contract = ContractV1` (an alias constant). `TakeSpec` gains
  `Kind`, `Ears`, `Plan`, `Attach`, `Wire` and one more field, `FallbackAfterS` (reserved, C321.3); `UploadTarget` gains the reserved
  `Fallback *FallbackTarget`. `Output` gains `Kind`, `Ears`, `Plans`, `Caps`; `EarsOut` is `{status, code, seconds}` (Revision 2, ears A2) and
  `PlanOut` is `{index, seed, status, code, truncated, bundle}` where `bundle` names the seed's `plan-NN.tar` (the plan did not say what it held).
  `WorkerInput` gains the `/2` keys (all `omitempty`; `request` too, since `analyze` has none). `BuildWorkerInput` dispatches on `Wire`.
- **ParseWorkerInput** dispatches on `contract`: `/2` goes to `parseV2` (`internal/provider/wire_v2.go`), everything else to the unchanged `/1`
  rules, which now also refuse the `/2` keys (`kind`, `ears`, `plan`, `attach`, `fallback_after_s`). One existing test row changed: in
  `TestParseWorkerInput_MatchesSchema` the row "contract v2" became "contract v3", because a `/1`-shaped document with contract `/2` is now a valid
  `/2` render. No `/1` schema, golden or document changed.
- **/2 rules the plan left open**: the engine must be `yue2`; `inputs` is part of `analyze` only (a `plan` refuses it, as `render` does) and holds
  exactly `src_audio`; `min_audio_s` is part of `render` only; `attach` is refused on `plan`; `ears` needs `budget_s` (1..2700) and 1..3 distinct
  `stages`, `lang` is `auto` or a 2-3 letter code; `plan.s_per_seed` is 1..360; attachment names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, at most 16,
  1 MiB decoded together; `fallback_after_s` is present exactly when an upload carries a `fallback` (60..600); no `null` anywhere outside `request`.
  `ParseWorkerInput` and `BuildWorkerInputV2` refuse a document over `MaxRunBody`; the schema cannot state that, nor the attachment total
  (`parserOnly` rows in the tests).
- **C321.5 artifact table** (`internal/provider/artifacts_v2.go`; `KindShape{Kind, Ears, KeepVocals, Seeds}`): `ArtifactsV2(shape)` lists the upload
  targets of a document and `Required` marks the mandatory ones. A target exists exactly when its condition holds, and the document must carry it:
  `analysis.json` for `ears`, `stem-vocals.flac` for `keep_vocals`, `plan-00..plan-(n-1).tar` for n seeds. `ExtraRequiredV2(shape, output)` lists what
  a finished output must report beyond the engine's own set (`analysis.json` once `ears.status = ok`, the bundle of each ok plan). **Amendment:
  `plan-NN.tar` is at most 2 MiB** (Revision 2, desk A1), not the 160 MiB of C321.5. `UploadLimitV2(name)` serves the upload service, which opens
  rows before it knows the document (`upload.OpenAttempt` accepts the `/2` names for yue2 only); `ArtifactMaxBytesFor` (the `/1` allowlist) is
  unchanged. The table is exported to `worker/contract/testdata/artifacts-v2.json` by `TestWriteArtifactsV2` (`make regen` runs it with `-update`).
- **Output decoding**: `DecodeOutput` refuses a `/1` output that carries `kind`, `ears`, `plans` or `caps`, and checks a `/2` output's kind, blocks, caps
  vocabulary and null shapes (render: `audio_seconds` and `truncated` set on ok; analyze: ok ears block, `truncated` null; plan: no ears,
  `audio_seconds` and `truncated` null, one ok seed on an ok output). `CheckOutputV2(shape, out)` adds what depends on the document (ears asked
  iff reported, one plan entry per seed in order).
- **Schemas**: `mm-worker-2.input.schema.json` and `mm-worker-2.output.schema.json` (generated once from a script, then hand-maintained);
  `TestWorkerSchemasV2_MatchParser` and `TestWorkerSchemasV2_OutputMatchesDecoder` run about a hundred rows through schema and Go and require the
  same verdict.
- **C323.5 takeSpec from spec**: `Runner.applyWire` (`internal/jobs/wire_v2.go`) fills kind, ears, plan, attachments and the signed input of the
  spec's pins (`RunnerOptions.Inputs` must implement `PinMinter`; `RunnerOptions.Attach` is a new `AttachSource` that reads and sha-checks each
  `attach` ref at submit). It then builds and parses the `/2` document, so **a document the worker would refuse is rejected at submit
  (`SUBMIT_REJECTED`) and never sent**: a worker `BAD_INPUT` would close the capability gate (C322.5) for a fault of one job. A plain job and a job
  with only `needs: [v2]` and no spec are handled as before (`WireFor`: `/1` for an empty needs, a non-yue2 engine always `/1`).
- **Per-kind verification** (Revision 4's "artifact table per kind"): the transfer verifier, the ingest bridge (`verify`) and `IngestKindJob` take
  the contract from the job (`contractOf`), hold the output to its kind (`jobWire.outputProblem`), allow only the kind's artifacts and require
  `engineRequired + ExtraRequiredV2`. An analyze or plan job no longer needs `audio_seconds` or a truncation object. Attempt uploads are opened from
  `ArtifactsV2` (an analyze or plan job gets only its own targets; a render keeps the engine's set and adds the stage names).
- **Fake worker** (`internal/provider/fake/simworker_g1.go`): speaks `/2` for yue2; `RegisterSimKind(name, hook)` (init-time, names `ears`, `analyze`,
  `plan`) and `SimWorkerOptions.Kinds` are the hook table; a kind or stage with no hook answers `KIND_UNAVAILABLE` before any fetch or render, an
  attachment checks its sha256 (`ATTACH_MISMATCH`), an `analyze` fetches and digest-checks its input. The process-wide table means
  `internal/app/m3.go` needed no edit and tracks add hooks from `simworker_<track>.go` as C328 says.
- **Tests added to existing fixtures** (jobs rig): `deliver` builds the `/2` output of a `/2` spec, seeded `plan` and `analyze` jobs get the smallest
  valid `spec_json` by default, the rig's default `Inputs` is a fake pin minter, and `TestIngestKindJob_Refusals` reports an allowlisted bundle that
  was not received (an unlisted name is now a `WORKER_CONTRACT` violation first).
