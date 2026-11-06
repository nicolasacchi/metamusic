# M5 — Close the Loop — Implementation Plan (spend-free core built; revision 3 as shipped, see the Revision log at the end; M5b and M6 deferred)

> **Revision 3 changelog (2026-11-03, re-baseline against `main` `5f6eeaf`, read-only worktree `metamusic-wt/review`).**
> The draft (`m5-close-the-loop.draft.md`, revision 2, 3 521 lines, contracts C92–C113, decisions D-M5-1…36) is kept untouched.
> - **How much of the draft is obsolete, plainly.** *Superseded by L1/L2/L3 as shipped: about 3 %* — a handful of seams and names (the
>   `/context` next-action strings of C96.1, the section-ranking rule, the Tier B "tempo ratio / key relation" half of the adherence
>   idea, M4a-era symbol names). **L3 does not cover M5's headline features** (Song PRs, callbacks, voice notes, heat strips,
>   section-locked duels, the Desk, mandates, Tier B, the upgrade gate): the hypothesis that L3's next-action engine, adherence and the
>   Ears key/energy work already deliver much of M5 is **mostly false** (table "Overlap" below). *The large cut is a re-scope, not
>   shipped work:* ≈ 760 lines of contracts (C94, C98, C100, C101, C105, C106, C107, C110) plus the lanes, risks and canary
>   steps that go with them — together roughly **three quarters of the draft** — leave M5 for a separate GPU plan (**M5b**) or for M6,
>   because they need new GPU contracts (`mm.worker/2`, an ears venv, staged planning), a `jobs` table rebuild, or a consumer that does not
>   exist yet (a second engine for the upgrade gate; Night Shift for mandates). **About 20 % of the draft survives, rewritten** as the
>   spend-free core below. The core adds **no new spend path, no GPU contract change and no migration rebuild**.
> - **Renumbering.** Contracts **C250–C265** are used (the draft's C92–C113 are retired; map in "Disposition of the draft"); C266–C299 stay
>   unallocated for M5b and any M5 follow-up, M6 starts at C300. The draft's `0007_close_loop.sql` / "after M4a's `0006_remote.sql`" is wrong
>   on three counts (0007 is M3's `0007_generate.sql`; M4a is 0009; main is at 0012): the migration is named **by purpose**
>   (`…_close_loop.sql`) and takes **the next free migration number at build time (currently 0013)**; the loader rejects numbering gaps.
> - **Scope cut to the genuine gaps** (reality check below): Song PRs (bounded patches, lyrics and style only), Callback Listening,
>   attention (heat strip, hotspots, triage as next-action rules), section-locked duels and active pair selection, walkie-talkie voice
>   notes, and a web-only Receipts grouping. Everything else is deferred with explicit re-entry criteria.
> - **Edit brief dropped as a separate op:** `/context` (C35 + L3) already carries the comments, recipe, ABC, lineage, perception and
>   next actions; it gains per-comment spots, ancestor comments and in-flight patches instead (D-M5-R3-2).
> - **Triage dropped as a separate op:** it becomes four `take`-subject rules in L3's next-action registry (C257).
> - **New spend guard (D-M5-R3-3):** the human's callback answers pace agent spend (`CALLBACKS_BACKLOG`), and a patch renders at most
>   three times. Spend map and cap rules are in "Spend map".
> - **Registry:** nine new operations, all generic or excluded, **no dedicated MCP tool** (L2/L3 operator decision continues; registry
>   133 → 142 ops).
> - **Dev loop:** foundation lanes alone and first (F1 migration+pure, F2 contract freeze with 501 stubs and web scaffold), then
>   PR-sized parallel lanes with per-lane files, INT-B/INT-W, X1 docs, X2 production-copy check, X3 operator review.

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (TDD per task: named tests first, watch them fail,
> implement). Read `CLAUDE.md` (public-repo rules, `make verify`), the roadmap, the M2a, M2b, M3, M4a, L1, L2 and L3 plans under
> `docs/plans/` (L3: `2026-11-02-l3-…`, with its revision log) first. Decisions carried from the draft keep their `D-M5-n` ids; new ones
> are `D-M5-R3-n`. Where M5 amends an earlier contract the owning lane records it in that plan document in the same commit.
>
> **Public/private split.** Everything above the line consisting solely of the HTML comment `<!-- PRIVATE APPENDIX -->` is public-safe
> (no real hostnames, IPs, titles, artists, lyrics, request-id prefixes, endpoint ids, balances or operator-set cap values; `mm.example.com`
> and synthetic examples only). Only that part is copied to `docs/plans/<date>-m5-close-the-loop.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`), and `make gate` runs on the copy first. Operator-set cap values are in P.3.
>
> **Money and secrets (binding).** M3's rules hold unchanged: no agent reads a RunPod key file, prints a key or passes one on a command
> line; no agent arms the provider, provisions the endpoint, runs a canary, approves a batch or confirms a spend. All M5-core tests use the
> fake backend. The only real GPU spend M5 causes is the operator's own first Song PR render in X3, through the existing estimate →
> confirm → approval protocol (see "Spend map").
>
> **Task ids** carry the `M5-` prefix. Lane labels (`MM_LANE`): `m5-found`, `m5-songpr`, `m5-callbacks`, `m5-attention`, `m5-duels`,
> `m5-voice`, `m5-web`, `m5-int`.

**Goal (unchanged in spirit, smaller in means).** The generate → listen → judge → regenerate loop closes on itself, from the phone and
from an agent through the same registry. A timestamped `fix` comment becomes part of a **Song PR** — a stored, bounded patch (a few
lyric lines and/or a style tweak) that names the comments it addresses; the patch is estimated and rendered through M3's existing spend
protocol; when the child take arrives the human gets a **callback** that plays the child at the same musical spot and asks "fixed / not
yet / worse?", the answer moves the comment (`resolved`, back to `open`, `regressed`), and `keep` comments are checked for regressions.
Listening behaviour becomes an **attention heat strip** and hotspots; the human's pending work becomes next-action items; duels can be
**section-locked** and pairs chosen by **active selection**; the phone records **walkie-talkie voice notes** pinned to a timestamp.

**Architecture.**
- Leaf (stdlib only, fully implemented in F1): `internal/anchor` (musical anchor of a time point, computed on read; map between two takes
  of one lineage by section kind + ordinal + fraction, else proportional).
- `internal/songpr` — patch bounds and compilation (pure `Compile`), the `patches` / `patch_comments` tables, propose/get/list.
- `internal/callbacks` — callbacks for Song PR children, human/critic verdicts, comment transitions, regression comments, the take hook.
- `internal/attention` — heat strips and hotspots from `listen_events`, computed on read, no table.
- `internal/voice` — voice notes (validation, content-addressed storage, MP3 playback rendition, media resolver).
- Amended, not new: `generation` (item mode `song_pr`), `jobs` (the `TakeHook` seam in the take transaction), `judgments`
  (`SystemTransition`, `AddCommentTx`, voice and callback origin on comments), `lineage` (operator `multi_edit`), `duels` / `media/clip` /
  `ladder` (section windows, `SuggestPairs`), `nextactions` (four rules, subject kind `take`), `trackctx` (`/context` fields),
  `media/serve` (voice resolver), `app` (wiring), `backup` (hardlink `voice/`).
- **Import seams (binding, `TestImportGraph_M5` in `internal/archtest/import_graph_m5_test.go`):** `generation` never imports `songpr`
  or `callbacks` (injected `generation.PatchSource`); `jobs` never imports `callbacks` (injected `jobs.TakeHook`); no M5 domain package
  imports `api`; `anchor` is stdlib-only; `attention` imports only `listening` types. **Write owners** (`internal/archtest`
  `writeOwners`, the L1/L3 pattern): `patches`, `patch_comments` → `songpr`; `callbacks`, `callback_verdicts` → `callbacks`; `voice_notes` →
  `voice`; comment state and rows stay owned by `judgments` (`callbacks` calls its exported `…Tx` functions inside its own transaction); new
  `duels` columns → `duels`.

**Tech stack:** no new Go dependency, no new web runtime dependency (the draft's abcjs, MCP Apps client and ears stack are deferred).
Voice uses the already-required ffmpeg through `internal/mediatool`. Pin nothing new; use context7 only if a deferred lane returns.

**Out of scope here (with owner):** see "Disposition of the draft" — Composer's Desk, ABC toolkit and bar-level patches, plan jobs, `mm.worker/2`,
Ears Tier B (diction, measured sections, score adherence, Recover recipe), Spending Mandates, the upgrade gate, MCP App widgets,
server-side Receipts. Also out: transcription of voice notes (needs Tier B), `re_perform`, manual section editing, Critic Bench.

---

## Overlap: M5 item → what L1/L2/L3 (and M2/M3/M4a) already shipped

"Where" cites the registry operation or package on `main`.

| # | M5 item (draft) | Verdict | Already shipped (where) | What is genuinely left | Disposition |
|---|---|---|---|---|---|
| 1 | Edit brief (`get_edit_brief`) | **Partly shipped** | `get_track_context` bundle: comments (kind, state, `at_second`, section label), `open_fix` count, recipe request, ABC `score`, lineage, `perception` incl. tempo/key/energy (L1/L3), `listening`, `next_actions` strings incl. L3 Muse-aware ones (`internal/trackctx`) | spots mapped across takes, comments of ancestors, in-flight patches | **Cut as an op**; three `/context` fields (C254) |
| 2 | Bounded patch (Song PR) | **Partly shipped** | `lineage` modes `same_seed_edit`, `lyric_edit`, `style_edit` with a free-form `Patch` (`generation.Modes`, `ModeSpec.CheckPatch`); `estimate_variant` / `create_variant`; Operator Ledger `mutations` | the bound rules, the stored/reviewable patch, the link comment ↔ patch ↔ job ↔ take | **Keep, slim** (C253) |
| 3 | Comment states `addressed` / `regressed` writers | **Missing** | states exist in `judgments` (`commentStates`); `Transition` refuses `→ addressed|regressed` ("belong to M5") | system writers and the callback rule | **Keep** (C255) |
| 4 | Callback Listening | **Missing** | nothing (`grep callback` finds only connectors/oidc) | tables, ops, ntfy, web | **Keep** (C255) |
| 5 | "Same musical spot" mapping | **Missing**, simpler than drafted | section timing rule `library.selectSections` (manual > measured > plan, timed only) and `judgments.sectionFor` (labels at write time) | `anchor` leaf, no ABC bars | **Keep, simplified** (C252) |
| 6 | Next-action engine for human/agent work items | **Shipped (L3)** | `list_next_actions`, `nextactions.Rules`, `Subject` kinds muse/song/record/taxonomy/library, `/context` integration, goldens (C139) | rules for callbacks and regressions only | **Absorbed**: four rules (C257); no new op |
| 7 | Triage queue (`get_triage_queue`) | **Mostly shipped** | Judge tab queue (unrated new takes), `to_relisten` status, L3 `song_duel`, `muse_unjudged` rules | callback-pending, regressed, unaddressed-fix per take | **Cut as an op**; rules (C257) |
| 8 | Adherence ("requested vs got") | **Shipped (L3) for tempo/duration** | `get_adherence`, `song_features` bases, `song_tempo_mismatch` rule, Atlas `requested-got` preset (C141.4) | Tier B melody/chord adherence needs GPU transcription | **Cut**; the draft's Tier B "adherence" is renamed *score adherence* and deferred (M5b) |
| 9 | Ears key / energy / tempo | **Shipped (L1, L3)** | Tier A analyzers `mm-tempo/1`, `mm-key/1`, `mm-energy/1`, `perception.key/energy`, `features` | Tier B (vocals → ASR → diction, measured sections) | Tier B **deferred** (M5b); key-relation from Tier A key vs plan key is spend-free and optional later |
| 10 | Attention heat strip, hotspots | **Missing** | raw `listen_events` (kinds start/segment/seek/complete/skip + diagnostics with `detail`, migration 0006), `listening` aggregate in `/context`; Subsonic events (source `subsonic`) | binned heat, hotspot spans, `/context.listening.hotspots` | **Keep** (C256) |
| 11 | Section-locked duels | **Missing** | `create_duel` with one global `from_s/to_s` window; `clip` windows; blind redaction | per-slot windows aligned to a section | **Keep** (C258) |
| 12 | Active pair selection | **Partly shipped** | `ladder.FitBTD`/`PBeats`, `duels` pair stats, L3 `song_duel` hint ("between the two newest") | information-gain suggestion | **Keep** `suggest_duel_pairs` (C258) |
| 13 | Walkie-talkie voice notes | **Missing** | comments exist; ffmpeg decode in `mediatool`; no voice storage | whole lane | **Keep, optional lane** (C259) |
| 14 | Receipts | **Partly shipped** | `get_audit` (filters principal/op/time, masking), `list_ledger`, `list_batches`, `get_spend` | session grouping and per-session money | **Web-only** view (R1), no op |
| 15 | Composer's Desk (plan → audition → render-from-plan) | **Missing** | `engine.Caps.PlanScore/RenderFromScore` are `true` for yue2 but the worker only speaks `mm.worker/1` render; `yue2.ParseScore` and `scores` exist | whole lane (ABC toolkit, abcjs, plan jobs, `/2`, jobs rebuild) | **Deferred → M5b**; plan-first is *not* cheaper at the predicted numbers (draft D-M5-12) |
| 16 | Spending Mandates + rehearsal | **Missing; the safety need is met** | per-principal limits (`api_tokens` / `connector_grants` daily and auto-approve columns, `spend.TokenLimitsFor`), `set_spend_caps`, approvals (24 h), batch ceiling, trips | windowed human-approved budgets that skip per-batch approval | **Deferred → M6** (Night Shift is the consumer) |
| 17 | Ears Tier B + Recover recipe | **Missing** | Tier A only (`ears.TierA`, tier enum `0,A`) | everything GPU | **Deferred → M5b / M6 Diction Lab** |
| 18 | Upgrade gate | **Missing; no consumer** | `env_fingerprints`, start handshake (M3) | needs a second engine release to gate | **Deferred → M6** (ACE-Step) |
| 19 | MCP App widgets | **Missing** | `mcpgen` `ResourceSpec`/resources (M4a) | spec spike, static `ui://` resources | **Deferred** (conditional, post-M6) |
| 20 | `jobs` rebuild + `-- mm:foreign_keys=off` runner directive | **Not needed** | – | only plan jobs needed it | **Cut**; M5 migration is additive |

---

## Reality check: what the loop already has, and the seven genuine gaps

**Shipped and reusable (verified in the tree):**
- *Listen:* `log_listen_events` (kinds `start|segment|seek|complete|skip` plus diagnostics `autoplay_blocked|stall_end|advance|error` carrying a
  validated `detail`, M2a + migration `0006_listen_detail`); Subsonic scrobbles arrive as device events (source `subsonic`, M4a); `/context.listening`
  is only plays / completion rate / last played.
- *Judge:* `rate_track`, `add_comment` (kinds `note|fix|keep|marker`, states `open|addressed|resolved|regressed`, `at_second`, section label from
  timed sections at write time, sources incl. `subsonic`), `update_comment` (`Transition`: reopen from any state, resolve from any state, nothing else);
  duels (`create_duel`, `next_duel`, `submit_duel`, blind redaction, human vs critic isolation, ladder fit with Davidson ties); Picks and Songs (L1).
- *Regenerate:* `estimate_generation` / `create_generation` and `estimate_variant` / `create_variant` (modes `new_seed`, `same_seed_edit`, `lyric_edit`,
  `style_edit`, `from_draft`, `compose_new`, `from_template`), batch approvals, runner, ingest into a take transaction (`jobs.IngestBridge.commit`),
  the Operator Ledger, `get_lineage`.
- *Agent context:* `get_track_context` and the L3 next-action engine.

**Genuine gaps (the M5 core):** (1) nothing links a comment to a render — no `addressed`; (2) nothing asks the human to re-judge the child *at the
same spot*; (3) edits are unbounded and unreviewable as a unit; (4) listening behaviour is not summarised over time; (5) duels cannot compare the same
section of two takes; (6) the phone cannot leave a spoken note; (7) the human's per-take pending work (callbacks, regressions, unaddressed fixes) is
invisible to the next-action engine. Everything in the core maps to one of these seven.

---

## Spend map: every path that spends money, and the rules it relies on

Only RunPod GPU time costs money (`internal/provider/runpod`; no LLM, storage or notification service is billed by the app). **The M5 core adds
zero new paying operations**: every new op has `cost: free`, and a Song PR renders through the existing paths below.

**Rule set (all enforced in `internal/spend` and `internal/generation`; amounts are integer micro-USD).** Migration defaults of the singleton caps row
are monthly $10, batch $5, operator daily $5, agent daily $1, auto-approve 5 takes / $0.50; the operator's live values (P.3) were set with
`set_spend_caps` and may be overridden per agent token or connector grant (`daily_budget_micro_usd`, `auto_approve_takes`,
`auto_approve_micro_usd`; null = global).
- **Estimate (free).** Plans takes, prices them at the provider's cached quote, issues a single-use `confirmation_token` (TTL), stores the estimate;
  `MaxTakes` 50 and `MaxItems` 20 per estimate. `requires_human_approval` and `approval_reasons` are computed with `NeedsApproval`.
- **Create (the only place a batch starts).** In one transaction: provider gate (enabled, armed, not tripped) → consume the token (`CONFIRMATION_*`) →
  `CheckCaps`: **batch cap vs the batch *ceiling*** (reserve max + retry allowance, not the expected cost), then **monthly** (`MonthSettled +
  Outstanding + max ≤ monthly`, all principals), then **the principal's day** (`PrincipalDaySettled + PrincipalOutstanding + max ≤ daily`: the
  token/grant's own limit or the global agent limit for agents, the operator limit for humans) → revalidate drift → insert batch, jobs, ledger reserves,
  approval row. First failing cap wins: 402 `BUDGET_EXCEEDED {cap}`.
- **Approval (`spend.NeedsApproval`, `InsertApproval`).** Human via the SPA confirm path (`create_generation_human`, `via human_create`): approved at
  once (the click is the approval) but still capped. A human on any other path: always approval. An agent: **auto** (`approvals.state = 'auto'`) iff takes ≤
  its auto-approve takes **and** ceiling ≤ its auto-approve micro-USD; either exceeded ⇒ `pending`, 24 h TTL, expires to `rejected`; the agent may call
  `request_approval` (one ntfy per 10 min); only a human decides (`approve_batch` / `reject_batch`, `/human/api/`, 2FA).
- **Runner top-ups (`ReserveTopUp`).** Per attempt `attempt_overhead`, plus `topup_ambiguous`, `topup_requeue`, `topup_price`: each is checked against
  the **batch ceiling**, the month and the batch owner's day; a refusal writes a `topup … refused` job event and blocks the job (`BUDGET_EXCEEDED`), it never
  overspends. Reserves of a batch can never exceed its ceiling.
- **Settlement and brakes.** Meter and reconciliation settle against the reserve; overshoot or billing above the reserve trips the provider (`meter_exceeds_
  reserved`, `billed_exceeds_reserved`, …) and stops claims; endpoint-level billing adjustments (`bill` rows) are reported, never counted against daily caps.
  `cancel_batch` / `cancel_job` (free) stop unstarted work. Operator-only: `set_spend_caps`, `update_provider_state` (enable/arm/disarm/reset trip),
  `metamusic runpod provision|canary|…` (CLI, never an agent).

**Paths (complete list):**

| # | Path (op or CLI) | Caller | Approval | Caps that apply |
|---|---|---|---|---|
| 1 | `create_generation` (batch from an estimate) | agent (MCP tool) or API client | auto within the principal's auto-approve takes/ceiling, else human | batch ceiling, monthly, principal daily |
| 2 | `create_variant` (one mother track; includes lyric/style edits and the new `song_pr` mode) | agent | as 1 | as 1 |
| 3 | `create_generation_human` (`/human/api/`) | human (SPA, 2FA edge) | none (the confirm is the approval) | batch ceiling, monthly, operator daily |
| 4 | `approve_batch` (spends nothing itself; releases a pending batch) | human only | – | caps were checked at create |
| 5 | Runner top-ups and attempt overhead for the jobs above | system | none | batch ceiling, monthly, owner's day |
| 6 | Reconciler / billing sync (settles, residuals, `bill` rows) | system | none | trips, not caps |
| 7 | `metamusic runpod canary`, `provision`, endpoint scale | operator CLI | operator's written approval (M3 rules) | the CLI's own `--max-usd` |
| – | `propose_patch`, `get_patch`, `list_patches`, callbacks, `get_listen_heat`, `suggest_duel_pairs`, voice notes (**new**) | any | none | **none: free** |
| – | Deferred (not in M5): plan jobs, render-from-plan, Tier B, mandates, trial renders | – | – | each returns with its own plan |

**What M5 adds to the spend rules (D-M5-R3-3):** (a) `song_pr` is a normal item mode, so paths 1–3 apply unchanged; (b) an estimate or create for an
**agent** principal is refused with 409 `CALLBACKS_BACKLOG` while ≥ `MM_CALLBACK_BACKLOG_MAX` (default 6, 0 = off) callbacks in the same lineage root await
a human verdict — the human's judgment paces the spend; humans are exempt; (c) one patch renders at most `PatchMaxRenders` = 3 non-cancelled batches
(422 `PATCH_RENDER_LIMIT`). A Song PR is ≤ 4 takes of one song, so its ceiling stays far below the batch cap (worked numbers in P.3).

---

## Dependencies and sequencing

- **Hard:** M0–M3, L1, L2, L3, M4a as merged on `main` (all deployed 2026-11-02), including degraded startup, token expiry (migration 0012),
  proxy-mode OIDC step-up, reader pool `query_only`, config check and the UI redesign. Branch `m5`, from `main`.
- **Migration:** a new file named for its purpose, `<next free number>_close_loop.sql` (**currently 0013**); every schema-version assertion compares with
  `len(migrations)`; no version literal. Additive only.
- **Contracts:** C250–C265 (this plan); the M6 draft renumbers from C300.
- **Operator inputs:** the P.5 boxes; a real song with timestamped `fix` comments for X3.

---

## Baseline reconciliation (verified on `main` `5f6eeaf`)

Status: OK = exists as the draft assumed; DIFF = exists differently (resolution given); MISSING = does not exist (built here or deferred); PROVIDED-BY-Lx
= shipped by that plan.

| Assumed by the draft | Status | Reality / resolution |
|---|---|---|
| Migration `0007_close_loop.sql`, "after M4a's `0006_remote.sql`" | **DIFF** | on main: 0007 generate, 0008 provenance (L1), 0009 remote (M4a), 0010 curation (L2), 0011 agents_atlas (L3), 0012 token_expiry. Name by purpose; next free number at build time (0013) |
| M4a contracts C76–C91 | **DIFF** | M4a is C102–C117; L1 C92–C101 (L1 range), L2 C118–C132, L3 C133–C146; M5 uses C250–C265 |
| `-- mm:foreign_keys=off` runner directive, `jobs` rebuild | **MISSING / not needed** | only the deferred plan jobs needed it; the core migration is additive (`jobs.patch_id` via `ADD COLUMN`) |
| `spend.LimitsFor` / `spend.LimitsForKey` | **DIFF** | shipped: `(*spend.Service).TokenLimitsFor(ctx, q, principalKey)` (tokens **and** connector grants, null = global), `spend.PrincipalDaily`, `NeedsApproval`, `CheckCaps`, `Usage` |
| `spend.QuoteBatch` | OK | `internal/spend/quote.go`; `QuoteTakes` is MISSING and only needed by deferred lanes |
| `generation` Modes table (C60/C71.2) | OK | `internal/lineage/modes.go` `Modes` (7 rows); `ModeSpec.CheckPatch`; M5 adds `song_pr` |
| `generation.Options` injected interfaces | **DIFF** | none yet; M5 adds `PatchSource` only |
| `lineage` operators | OK | `ValidOperator` map; M5 adds `multi_edit` (no `abc_edit`, `render_from_plan`) |
| `engine.Caps.PlanStaged` | MISSING | `Caps` has `PlanScore`, `RenderFromScore` (true for yue2) but nothing consumes them; deferred |
| `mm.worker/2`, `provider.TakeSpec`/`Output` changes | MISSING | `provider.Contract = "mm.worker/1"` only; deferred |
| `jobhooks.TakeHook` / `TakeInfo` | MISSING | seam: `IngestBridge.commit` (`internal/jobs/ingest.go`, the fenced take transaction after `Library.InsertTake`) and the post-commit `analyse`; M5 adds `jobs.TakeHook` |
| `library.Timing`, `anchor` on timing | **DIFF** | no `library.Timing`; the rule is `library.selectSections` (unexported) and `judgments.sectionFor` (labels at write time). F1 adds exported `library.Timing(ctx, q, trackID)` wrapping the same ranking |
| `judgments.Transition` | OK | `comments.go`: reopen from any, resolve from any; `→ addressed|regressed` refused |
| `judgments.SystemTransition`, `AddCommentTx` | MISSING | only `AddComment`; added by the callbacks lane (C255) |
| `comments` columns `voice_note_id`, `origin_callback_id`; source `callback` | MISSING | `commentSrcs` = web, api, mcp, stdio, subsonic; M5 adds `callback` |
| `api.OptFloat`, `…OrNull` nullable types of M2b | **DIFF** | shipped: `NullableString`, `NullableNumber` (types_m3.go) and per-type `XOrNull` named types (e.g. `PrincipalRefOrNull`); use them |
| `serve.Chain(library, exports, clips, covers, downloads, voice)` | **DIFF** | `serve.Chain(resolvers ...Resolver)` is variadic (`internal/media/serve/chain.go`); wiring in `internal/app/media.go` appends the voice resolver |
| `ResourceSpec` static `ui://` resources | OK / deferred | `mcpgen/resources.go` exists; widgets deferred |
| `FixtureV6` | **DIFF** | fixtures are `librarytest.FixtureL1/L2/L3`, `FixtureM3`, `FixtureM4a`; M5 adds `FixtureM5` on top of `ApplyL3` |
| `playlists.role = 'golden'` | **DIFF / deferred** | `role` exists (M4a, only `offline`); golden set belongs to the upgrade gate (deferred) |
| `nextactions` registry | PROVIDED-BY-L3 | `Rules`, goldens `testdata/rules_v1_{a,b,c}.golden.json`, `Subject` kinds muse/song/record/taxonomy/library; M5 adds kind `take` and rules file `rules_d.go` |
| `trackctx` perception key/energy; `BlindSpotFacts.Key/Energy` | PROVIDED-BY-L3 | `analysis.BlindSpotFacts` has Tier A, Key, Energy; Diction/Adherence facts would be deferred with Tier B |
| Tier A tempo (`mm-tempo/1`) | PROVIDED-BY-L1 | and key/energy by L3 (C136) |
| requested-vs-got / adherence | PROVIDED-BY-L3 | `get_adherence` (C141.4); the draft's Tier B adherence is a different, deferred thing — rename *score adherence* |
| Listen events with diagnostics | OK | `listen_events.detail`, kinds incl. diagnostics (diagnostics are never counted as listening) |
| `duels` columns `from_ms/to_ms`; `create_duel` inputs | OK | `from_s/to_s` global window; M5 adds `section`, `b_from_ms/b_to_ms`, `section_label`, `section_source` |
| `duels_immutable` trigger covers new columns | OK | `BEFORE UPDATE ON duels` (all columns, 0004) |
| `ladder.SuggestPairs` | MISSING | `FitBTD`, `PBeats`, `Laplace` exist (`internal/ladder`) |
| `notify` + builders | OK | `jobs/notify.go` `Note*` pattern; M5 adds a count-only "callbacks ready" |
| Audit read for Receipts | OK | `get_audit`, `list_ledger`, `list_batches`; no op needed |
| `archtest` conventions | OK | `import_graph_*_test.go`, `write_owners_l3_test.go`, `sql_owners_test.go` (per-milestone files) |
| Registry / MCP size | OK | 133 ops; L2/L3 added no dedicated tool; M5 proposes none |
| Web shell | **DIFF** | tabs New · Library · Play queue · Judge · More (`web/src/lib/nav.ts`, `TABS`, `tabFor`); Library sub-views Takes · Sounds · Atlas · Records · Crates · Playlists; Judge sub-nav (Judge, Duels, Ladder, Compare); secure pages are in-app routes; dark only; 44 px hit target; the draft's `/desk`, `/mandates`, `/receipts`… routes are re-placed (C262) |
| `web/e2e` specs, `scripts/e2e-map.txt` | OK | per-milestone `lN-*.spec.ts` / `mNx-*.spec.ts` files; `make verify` maps changed sources to specs |
| Tooling: `make verify/gate/check/test/test-long/perf/e2e`, `coverage.thresholds`, `scripts/ci-shard-weights.txt` | **DIFF** | the draft predates them; see C263 |
| `/context` budget (24 000 UTF-16 units default) | OK | `trackctx.DefaultLimit`, `IncludeLimit`; new fields count against it |
| Config (`MM_*`) | OK | `internal/config` (`JobMaxRuntime`, `ColdStartS`, …); M5 adds one var |

---

## Decisions recorded by this plan

Carried from the draft (renumbered, trimmed to the core): **D-M5-1** a Song PR is a stored, immutable `patches` row created by `propose_patch` (free,
scope `write`); spending stays in M3's estimate/create with item mode `song_pr`. **D-M5-2 (amended)** *bounded* means: (a) lyric edits replace whole
lines inside scoped sections (sections containing an addressed comment's mapped spot, plus ≤ 1 requested neighbour each, ≤ 4 sections in total), ≤ 8 lines,
never adding/removing/renaming a section tag; (b) a style edit changes ≤ 3 style terms (`yue2.StyleTerms`), the declared BPM by ≤ 20, never the
language tag; **(c) ABC bar edits are not part of M5** (they return with the Desk). Violations ⇒ 422 `PATCH_OUT_OF_BOUNDS` with every violation listed.
**D-M5-3** `addresses_comment_ids` (1..10) name non-hidden `fix`/`note` comments in state `open` or `regressed` on the base track or an ancestor on its
path to the root; checked at propose, estimate and create (create ⇒ 409 `ESTIMATE_STALE` reason `comments_changed`). **D-M5-4** the "M5 only" comment
transitions get system writers: take ingest moves addressed comments `open|regressed → addressed` in the take transaction; a **human** `fix` verdict
applies the order-free sibling rule over the human verdicts of every `fix` callback of that comment under the same patch (any `fixed` ⇒ `resolved`; else any
`worse` ⇒ `regressed`; else ⇒ `open`); `resolved` is never moved by a callback; `update_comment` keeps refusing the M5-only pairs. **D-M5-5** callback
verdicts follow the duel isolation rule: `ActsAsHuman` principals write the single human verdict that moves state; agents write `critic` verdicts that never
do; a human never sees critic verdicts before answering. **D-M5-6** callbacks are created in the take transaction of every Song PR child: one `fix`
callback per addressed comment, one `keep` callback per open non-hidden `keep` comment of the base track; `lost` inserts a regression comment on the
child. **D-M5-7 (amended)** the same musical spot is computed on read, never stored: anchor = section kind + ordinal + fraction within the section from the
comment track's best timed source *at read time* (manual > measured > plan), mapped to the target take by kind+ordinal+fraction, else proportional to duration
(confidence 0.3); untimed takes never get a section label (D-M2a-16 holds); bar/beat anchors are deferred with the ABC toolkit. **D-M5-23** voice notes
(≤ 120 s, ≤ 4 MiB, webm/opus first, ogg/opus, MP4 with `mp4a` or `Opus`; base64 JSON; content-addressed, 0444, backed up; only the 64 kbps mono MP3
rendition is served; no second `<audio>` element; `create_voice_note` is `ActsAsHuman` only, `mcp: exclude:human-recording`, a parity exception needing
sign-off). **D-M5-24** section-locked duels: per-slot windows aligned at section start minus a 2 s pre-roll, equal length
`L = clamp(min(len_a, len_b) + 2 s, 10 s, 45 s)`, from each track's best timed sections; untimed ⇒ 422 `SECTION_UNTIMED`; unrevealed views hide per-slot windows.
**D-M5-30** heat strips and hotspots are computed on read from human/device `listen_events` (120 bins, deterministic weights), no table; Subsonic device events
count by kind. **D-M5-31** active pair selection is a pure function over the human ladder fit: score `p(1−p)(σa² + σb²)`, excluding pairs with an open duel
or ≥ 3 human verdicts.

New in revision 3:

| Id | Decision | Why |
|---|---|---|
| D-M5-R3-1 | **M5 = the spend-free core** (Song PR, callbacks, attention, section duels + pair suggestions, voice notes, web-only receipts). Desk, Tier B, mandates, upgrade gate, widgets are deferred (table below). | The draft bundled seven independent systems; four need GPU contracts that are unmeasured (the draft's own D-M5-12 shows plan-first is dearer at predicted numbers), two have no consumer yet. The loop only needs the first five. |
| D-M5-R3-2 | **No `get_edit_brief` op.** `/context` gains, per comment: `relation` (`self`/`ancestor`), `spot`, `lyric_lines` of the mapped section, `in_flight_patch_ids`; plus `judgments.ancestor_open_fix` count. | `/context` already is the agent's brief; a second 24 000-unit document would duplicate it and its golden. |
| D-M5-R3-3 | **Spend pacing:** `CALLBACKS_BACKLOG` (agent principals, per lineage root, default 6) and `PatchMaxRenders` = 3. | The shipped caps bound dollars, not usefulness; a loop that renders faster than the human can judge wastes budget inside the caps. |
| D-M5-R3-4 | **Triage = next-action rules**, subject kind `take`: `callback_waiting` (human, 85), `regressed_comment` (both, 72), `open_fix_unaddressed` (agent, 58), `patch_ready_to_render` (agent, 52). | One registry for "what next" (L3 D-L3-15…19); no parallel queue. Priorities slot between L3's `song_duel` 68 and `muse_unjudged` 65 where intended. |
| D-M5-R3-5 | **Receipts are a web grouping** of `get_audit` + `list_batches` pages (gap 30 min), no op, no table. | The rows and money already exist; agents need no summary of their own audit. |
| D-M5-R3-6 | **Additive migration only**; `jobs` gains `patch_id` (frozen by its own trigger, partial index). | Avoids the `jobs` rebuild and the runner directive. |
| D-M5-R3-7 | Nine new ops, **all generic**, none a dedicated tool; the `propose_patch` verb is reached through `call_operation`. | L2/L3 operator decision (tools/list ≈ 108 KB). Bytes for a dedicated tool are *not* measured here; if the operator wants one, F2 measures it and the box in P.5 is reopened. |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C250. Packages, dependency order, constants, config

Packages as in "Architecture". Constants (one file per package, no magic numbers elsewhere): `songpr`: `MaxLines 8`, `MaxScopedSections 4`,
`MaxStyleTermDelta 3`, `MaxBPMDelta 20`, `MaxAddressed 10`, `MaxOps 20`, `PatchMaxRenders 3`, `MaxSeeds 4`; `attention`: `Bins 120`; `voice`: `MaxSeconds 120`,
`MaxBytes 4 MiB`, `MP3Kbps 64`. Config (`internal/config`, validated, `metamusic config check` lists it): `MM_CALLBACK_BACKLOG_MAX` (int 0..50, default 6, 0 disables
the gate). Nothing else is configurable. Dependency order: `anchor` → `library.Timing` → `songpr`, `attention` → `callbacks` (needs `songpr` tables only through SQL
owned by itself: it reads `patch_comments` through a `songpr` exported reader) → services wired in `internal/app/m5.go`.

### C251. Migration `<next>_close_loop.sql` (additive; full DDL)

After M5, `PRAGMA user_version == len(migrations)`. Enumerations that grow (operators, patch ops, verdicts, map bases, voice containers) are Go-validated.

```sql
-- <next>_close_loop.sql — M5: Song PRs (patches), callbacks, voice notes, section-locked duels.
CREATE TABLE patches (                                   -- D-M5-1; immutable Song PR
  id               TEXT PRIMARY KEY,                     -- UUIDv7
  base_track_id    TEXT NOT NULL REFERENCES tracks(id),
  base_recipe_hash TEXT NOT NULL,
  operator         TEXT NOT NULL,                        -- Go-validated: lyric_edit|style_edit|multi_edit
  ops_json         TEXT NOT NULL CHECK (json_valid(ops_json) AND json_type(ops_json) = 'array'),
  compiled_json    TEXT NOT NULL CHECK (json_valid(compiled_json)),   -- the M3 Patch {style?, lyrics?}
  scope_json       TEXT NOT NULL CHECK (json_valid(scope_json)),      -- scoped sections + bounds report
  diff_json        TEXT NOT NULL CHECK (json_valid(diff_json)),
  title            TEXT CHECK (title IS NULL OR length(title) BETWEEN 1 AND 120),
  rationale        TEXT NOT NULL CHECK (length(rationale) BETWEEN 1 AND 2000),
  principal_kind   TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id     TEXT NOT NULL,
  principal_name   TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX patches_base ON patches(base_track_id, created_at);
CREATE TRIGGER patches_immutable BEFORE UPDATE ON patches BEGIN SELECT RAISE(ABORT, 'patches: immutable'); END;
CREATE TRIGGER patches_no_delete BEFORE DELETE ON patches BEGIN SELECT RAISE(ABORT, 'patches: no physical delete'); END;

CREATE TABLE patch_comments (
  patch_id TEXT NOT NULL REFERENCES patches(id), comment_id TEXT NOT NULL REFERENCES comments(id),
  PRIMARY KEY (patch_id, comment_id)) WITHOUT ROWID;
CREATE INDEX patch_comments_comment ON patch_comments(comment_id);
-- immutable + no_delete triggers as for patches

ALTER TABLE jobs ADD COLUMN patch_id TEXT REFERENCES patches(id);        -- NULL default: allowed by SQLite with FKs on
CREATE INDEX jobs_patch ON jobs(patch_id) WHERE patch_id IS NOT NULL;
CREATE TRIGGER jobs_patch_frozen BEFORE UPDATE OF patch_id ON jobs WHEN OLD.patch_id IS NOT NEW.patch_id
BEGIN SELECT RAISE(ABORT, 'jobs: frozen column'); END;     -- jobs_frozen (0007) lists columns explicitly; this one covers the new column

CREATE TABLE voice_notes (  -- D-M5-23; immutable; exactly the draft's columns
  id TEXT PRIMARY KEY, track_id TEXT NOT NULL REFERENCES tracks(id), sha256 TEXT NOT NULL,
  bytes INTEGER NOT NULL CHECK (bytes > 0 AND bytes <= 4194304), container TEXT NOT NULL, codec TEXT NOT NULL, path TEXT NOT NULL,
  mp3_sha256 TEXT NOT NULL, mp3_path TEXT NOT NULL, mp3_bytes INTEGER NOT NULL CHECK (mp3_bytes > 0),
  duration_s REAL NOT NULL CHECK (duration_s > 0 AND duration_s <= 120.5), at_second REAL CHECK (at_second IS NULL OR at_second >= 0),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device')), principal_id TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX voice_notes_track ON voice_notes(track_id, created_at);
CREATE INDEX voice_notes_sha ON voice_notes(sha256);
-- immutable + no_delete triggers

CREATE TABLE callbacks (   -- D-M5-6; immutable; the spot is computed on read (D-M5-7)
  id TEXT PRIMARY KEY, child_track_id TEXT NOT NULL REFERENCES tracks(id), comment_id TEXT NOT NULL REFERENCES comments(id),
  source_track_id TEXT NOT NULL REFERENCES tracks(id), kind TEXT NOT NULL CHECK (kind IN ('fix','keep')),
  patch_id TEXT NOT NULL REFERENCES patches(id), job_id TEXT NOT NULL REFERENCES jobs(id), created_at TEXT NOT NULL,
  UNIQUE (child_track_id, comment_id));
CREATE INDEX callbacks_comment ON callbacks(comment_id);
CREATE INDEX callbacks_child ON callbacks(child_track_id, created_at);
-- immutable + no_delete triggers

CREATE TABLE callback_verdicts (   -- D-M5-5; append-only
  id TEXT PRIMARY KEY, callback_id TEXT NOT NULL REFERENCES callbacks(id), source TEXT NOT NULL CHECK (source IN ('human','critic')),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')), principal_id TEXT NOT NULL,
  verdict TEXT NOT NULL,                         -- Go-validated per kind: fixed|not_yet|worse ; kept|lost
  note TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000), heard_at_s REAL CHECK (heard_at_s IS NULL OR heard_at_s >= 0),
  map_basis TEXT,                                -- Go-validated: section|proportional
  state_before TEXT, state_after TEXT, regression_comment_id TEXT REFERENCES comments(id), created_at TEXT NOT NULL,
  CHECK ((source = 'human') = (principal_kind IN ('human','device'))),
  CHECK ((state_before IS NULL) = (state_after IS NULL)),
  CHECK (source = 'human' OR (state_before IS NULL AND regression_comment_id IS NULL)));
CREATE UNIQUE INDEX callback_verdicts_one_human ON callback_verdicts(callback_id) WHERE source = 'human';
CREATE UNIQUE INDEX callback_verdicts_one_critic ON callback_verdicts(callback_id, principal_id) WHERE source = 'critic';
-- immutable + no_delete triggers

ALTER TABLE comments ADD COLUMN voice_note_id TEXT REFERENCES voice_notes(id);
ALTER TABLE comments ADD COLUMN origin_callback_id TEXT REFERENCES callbacks(id);
CREATE TRIGGER comments_m5_frozen BEFORE UPDATE OF voice_note_id, origin_callback_id ON comments
BEGIN SELECT RAISE(ABORT, 'comments: frozen column'); END;

ALTER TABLE duels ADD COLUMN section_label TEXT;
ALTER TABLE duels ADD COLUMN section_source TEXT CHECK (section_source IS NULL OR section_source IN ('plan','measured','manual'));
ALTER TABLE duels ADD COLUMN b_from_ms INTEGER CHECK (b_from_ms IS NULL OR b_from_ms >= 0);
ALTER TABLE duels ADD COLUMN b_to_ms INTEGER;
CREATE TRIGGER duels_section_window BEFORE INSERT ON duels
WHEN (NEW.b_from_ms IS NULL) <> (NEW.b_to_ms IS NULL) OR (NEW.b_from_ms IS NULL) <> (NEW.section_label IS NULL)
  OR (NEW.section_label IS NULL) <> (NEW.section_source IS NULL)
  OR (NEW.b_from_ms IS NOT NULL AND NEW.b_to_ms - NEW.b_from_ms <> NEW.to_ms - NEW.from_ms)
BEGIN SELECT RAISE(ABORT, 'duels: section window mismatch'); END;
```
**Rules.** `TestNoDeleteSQL` allowlist unchanged. The take-hook writes use the lease predicate of the ingest transaction. `TestMigration_M5_OnSnapshot`
(skipped without `-snapshot`, run by X2 against a `VACUUM INTO` copy) asserts row counts of every table are unchanged and `PRAGMA foreign_key_check` is clean.
`comments.source` gains the Go value `callback` (no schema change). **Write SQL** (binding): the addressed move is
`UPDATE comments SET state='addressed', state_changed_at=?, state_changed_by='system:job' WHERE id=? AND state IN ('open','regressed')`; the human callback move is
the same shape guarded by the `from` set of C255; both through `judgments` exported `…Tx`.

### C252. Anchors (`internal/anchor`, stdlib only) and `library.Timing`

```go
type Section struct{ Kind string; Ordinal int; Label string; StartS, EndS float64 }   // ordinal: nth of that kind, 1-based
type Timing struct{ Source string /* manual|measured|plan|"" */; Sections []Section; DurationS float64 }
type Point struct{ AtS float64; Basis string /* section|proportional */; Kind string; Ordinal int; Fraction float64; Confidence float64; Label string }
func Compute(t Timing, atS float64) Point                  // timed: section containing atS, fraction in [0,1); else proportional (conf 0.3)
func Map(p Point, to Timing) (atS float64, basis string, conf float64)   // kind+ordinal+fraction when `to` has it, else p.Fraction*to.DurationS as proportional
```
`library.Timing(ctx, q, trackID) (anchor.Timing, error)`: the highest-ranked source present (`manual` > `measured` > `plan`; `lyrics` is untimed and never used)
whose every row is timed; none ⇒ empty `Sections`. `selectSections` keeps its callers. Tests: `TestCompute_Table` (spot in a chorus, at a boundary, last section end,
untimed ⇒ proportional), `TestMap_SameKindOrdinal`, `TestMap_MissingSectionFallsBack`, `TestMap_Roundtrip`, `TestTiming_RankMatchesSelectSections` (shared cases),
property `TestAnchor_MapIsMonotoneWithinSection`. Real-data lesson: fixtures include a take with lyrics-only sections (an import), a plan-timed take and a take with
both plan and manual sections.

### C253. Song Pull Requests (`internal/songpr`)

```go
func Compile(in CompileInput) (Compiled, error)        // pure
func (s *Service) Propose(ctx, p auth.Principal, in ProposeInput) (Patch, error)
func (s *Service) Get(ctx, p auth.Principal, id string) (Patch, error)
func (s *Service) List(ctx, p auth.Principal, f ListFilter) (PatchPage, error)
// generation.PatchSource (injected; generation never imports songpr):
func (s *Service) PatchForEstimate(ctx, q Querier, id string) (generation.PatchRow, error)
func (s *Service) RecheckComments(ctx, q Querier, id string) error     // 409 ESTIMATE_STALE comments_changed
func (s *Service) RenderCount(ctx, q Querier, id string) (int, error)  // non-cancelled batches of the patch (PatchMaxRenders)
```
**C253.1 Propose input** (`propose_patch`, `IdemHeader`): `{base_track_id, addresses_comment_ids: string[] (1..10, unique), rationale: 1..2000, title?: 1..120,
extra_sections?: int[] (≤ 2), ops: Op[] (1..20)}`; `Op` is `{op:"replace_line", section_idx, line_idx, text: 1..200}` or `{op:"set_style", style: 1..1000}` (at most once).
Base must be a track with a known recipe (reference/unknown-engine recipes ⇒ 422 `RECIPE_UNKNOWN`, as variants refuse them) and not `discarded`.
**C253.2 Compile** (collect every failure, never first-fail): (1) addressability (D-M5-3) ⇒ 422 `COMMENT_NOT_ADDRESSABLE {comment_ids, why: hidden|kind|state|lineage}`;
(2) scope: scoped sections = lyrics sections (`yue2.LyricsSections` index) containing the mapped spot of an addressed comment (timed: kind+ordinal aligned through
`yue2.AlignSections`; untimed with a stored label: by label) plus `extra_sections`, ≤ `MaxScopedSections`, every addressed comment covered else 422
`PATCH_OUT_OF_BOUNDS` code `comment_unscoped`; (3) per op: line/section exists, not a section-tag line, no `[…]` tag, no newline, replaced lines inside scope, ≤ `MaxLines`;
style delta ≤ `MaxStyleTermDelta`, BPM delta ≤ `MaxBPMDelta`, language tag unchanged ⇒ 422 `PATCH_INVALID {op_index, field, rule}` / `PATCH_OUT_OF_BOUNDS {violations}`; (4) apply in op order
to the base canonical request, `yue2.ParseRequest`, M2a lint (`drafts.Service.Lint`, errors ⇒ 422 `LINT_FAILED`); (5) unchanged ⇒ 422 `PATCH_NO_CHANGE`; (6) operator =
`lyric_edit` | `style_edit` | `multi_edit`; `compiled` = M3 `Patch{lyrics?, style?}`. `TestCompile_BoundsTable` (≥ 16 rows: 9-line edit, a 4th style term, BPM +21, renamed
tag, untimed comment without `extra_sections`, resolved/hidden/foreign-lineage comment, no-op, a reference base).
**C253.3 Shapes:** `Patch {id, base:{track_id,title,recipe_hash}, operator, title|null, rationale, addresses:[{comment_id,track_id,kind,state,body_excerpt ≤200 runes}], ops, diff,
bounds, compiled, renders:[{batch_id,job_id,state,seed,child_track_id|null}], callbacks:{total,answered,fixed,not_yet,worse,kept,lost}, author: PrincipalRef, created_at}`; `PatchSummary` omits
ops/diff/compiled. `diff` = `{lyrics:[{section_idx,line_idx,before,after}], style:{before,after,added[],removed[]}|null}`.
**C253.4 Generation amendment** (the only `generation` change): `ItemIn` gains `PatchID *string`; `lineage.Modes` gains row `song_pr` (job kind `generate`, base = the patch's base
track recipe, the item's `patch` forbidden, seeds `[base seed]` + n−1 new with n ≤ `MaxSeeds`, operator = the patch's operator, `params.via = "song_pr"`, `params.patch_id`; parent = base
track); expanded takes carry `patch_id` and `addresses_comment_ids` into `items_json`; `jobs.patch_id` is set at insert. `generation.Options` gains the nil-safe `PatchSource` (nil ⇒ 503
`UNAVAILABLE {component:"songpr"}`). Create re-runs `RecheckComments` through the transaction and the `PatchMaxRenders` count (422 `PATCH_RENDER_LIMIT`), and, for agent principals, the callback
backlog gate (C255.5). An addressed comment already addressed by another patch whose renders are not terminal adds the estimate warning `comment_in_flight`. `lineage.ValidOperator` gains `multi_edit`
(export tests extended).
**Tests:** `TestPropose_*` (REST, one per error code), `TestGeneration_SongPRMode_Table`, `TestEstimate_SongPR_StaleComments`, `TestCreate_SongPR_RenderLimit`, `TestModes_Table` extended,
`TestSongPR_NoGenerationImport` (arch).

### C254. `/context` additions (amends M2a C35, L3 C139.4; one owner: lane m5-attention)

`judgments.comments[*]` gains `relation` (`self`|`ancestor`; ancestors' non-hidden open `fix`/`note` comments are included, newest first, within the existing `comments` limit), `spot`
(`{at_s, basis, confidence, label|null}` mapped onto this take by C252; null when the comment has no time), `lyric_lines` (the lines of the mapped lyrics section, only for `fix`/`note` open comments,
capped), `in_flight_patch_ids`. `judgments` gains `ancestor_open_fix` (int). `listening` gains `hotspots` (≤ 3 `{from_s, to_s, score, label|null}`, from C256). All new fields are trimmable in the
existing trim order *before* `comments` (recorded in `_budget.truncated`); with no ancestors, no patches and no listening data the bundle is **byte-identical** to today
(`TestContext_Goldens_PreM5Unchanged`). Goldens re-baselined once, with a reviewed diff.

### C255. Callback Listening (`internal/callbacks`) and comment transitions

```go
func New(o Options) *Service                         // Store, Library(Timing), Judgments, Notify, Now, NewID
func (s *Service) AfterTakeTx(ctx, tx *sql.Tx, t jobs.TakeInfo) error     // jobs.TakeHook; lease predicate on every statement
func (s *Service) AfterTakeCommit(ctx, t jobs.TakeInfo)                   // ntfy "callbacks ready" (count only); warnings only
func (s *Service) List(ctx, p auth.Principal, f Filter) (Page, error)
func (s *Service) Get(ctx, p auth.Principal, id string) (Callback, error)
func (s *Service) Submit(ctx, p auth.Principal, id string, in VerdictInput) (SubmitResult, error)
```
`jobs.TakeHook` (new, in `internal/jobs`, no import of callbacks): `type TakeHook interface{ AfterTakeTx(ctx, tx *sql.Tx, t TakeInfo) error; AfterTakeCommit(ctx, t TakeInfo) }`,
`TakeInfo{TrackID, JobID, BatchID, PatchID *string, Fence store.LeaseFence}`; `IngestBridge.commit` calls the hook after `Library.InsertTake` inside the fenced transaction, and the bridge calls
`AfterTakeCommit` after commit next to `analyse`; a hook error rolls the take transaction back like any other write (so a Song PR child never exists without its callbacks); nil hook ⇒ today's behaviour
(`TestIngest_NoHook_Unchanged`).
**C255.1 Creation** (`AfterTakeTx`, only when `PatchID != nil`): the addressed move (C251 write SQL); one `fix` callback per `patch_comments` row; one `keep` callback per open non-hidden `keep`
comment of the patch's base track; `INSERT … ON CONFLICT(child_track_id, comment_id) DO NOTHING` (idempotent).
**C255.2 View** (spot computed on read on both sides, D-M5-7): `Callback {id, kind, child:{id,title,seed}, patch_id, job_id, comment:{id,track_id,kind,state,body,at_second|null,section|null,voice|null},
relation, spot:{child_at_s, source_at_s|null, basis, confidence, label|null}, links:{child,source}, state:"pending"|"answered", my_verdict, human_verdict, critic_verdicts[], created_at}`;
`CallbackVerdict {id, source, principal_kind, verdict, note, heard_at_s, map_basis, state_before, state_after, regression_comment_id, created_at}`. `critic_verdicts` visible only after the caller's source has
answered (D-M2b-4 rule). `list_callbacks` filters `state` (explode), `kind`, `child_track_id`, `track_id`, `comment_id`, `limit` 1..100 (50), `cursor`; pending first (`created_at` asc), then answered (desc).
**C255.3 Transitions** (`judgments.SystemTransition(writer, from, to)`; `TestSystemTransitions_Table` covers every cell; `update_comment` still refuses the M5-only pairs):
take ingest (`system:job`) `open|regressed → addressed`; human `fix` verdict ⇒ sibling target (D-M5-4) from `open|addressed|regressed`; any callback on a `resolved` comment ⇒ unchanged
(`state_after = state_before`); human `keep` verdict: the keep comment never changes, `lost` creates the regression comment (`kind fix`, `state open`, source `callback`, body `"Lost from parent: " + keep body`, at the mapped
spot, author = the answering human). `TestCallbackSubmit_MultiChildOrder` (order-free).
**C255.4 Submit** (`submit_callback_verdict`, naturally idempotent, no `Idempotency-Key`): verdict valid for the kind (`fixed|not_yet|worse` / `kept|lost`) else 422; source from the principal; one write
transaction (verdict row, human transition + `state_before/after`, regression comment); replay rule as `submit_duel`: same source, identical verdict and normalised note ⇒ 200 `replayed: true`, else 409
`CALLBACK_ALREADY_JUDGED {verdict_id}`; first insert ⇒ 201.
**C255.5 Backlog gate** (D-M5-R3-3): `callbacks.Backlog(ctx, q, rootTrackID) (int, error)` = unanswered (no human verdict) callbacks whose child shares the lineage root; estimate and create of an **agent** item set
that includes `song_pr` **or any item whose parent track has that root** answer 409 `CALLBACKS_BACKLOG {root_track_id, pending, limit}` at `pending ≥ MM_CALLBACK_BACKLOG_MAX`; humans exempt; exposed to
`generation` through the injected `BacklogGate` interface (generation never imports callbacks). `TestBacklogGate_AgentRefusedHumanNot`, `TestBacklogGate_Disabled`.
**C255.6 Notification:** one count-only ntfy "N callbacks ready" per batch of children (never comment text, titles or lyrics), click target the Judge callbacks view.

### C256. Attention: heat strip and hotspots (`internal/attention`)

`ComputeHeat(durationMS int64, ev []listening.Event, bins int) Heat` (pure) and `Service.Heat/Hotspots`. Events of principal kinds `human`/`device` only (diagnostic kinds never count); 120 bins.
`heard[b]` = distinct sessions with a `segment` overlapping ≥ 50 % of bin `b`; `replays[b]` = Σ over sessions of (segments covering `b` − 1) when > 0; `rewinds[b]` = `seek` with `to_ms < from_ms − 1000`
landing in `b`; `skips[b]` = `skip` at `from_ms` in `b` plus forward seek origins jumping > 5 s. **Hotspot span rule:** `h[b] = 3·rewinds + replays`; a span is a maximal run of adjacent bins with `h ≥ 2`; its score
is the **maximum** `h` over the run; ranked by score desc, then earlier start; up to 3, each with the timed section label at its start (or null); `cold` = up to 3 bins by `skips`. Subsonic events (source `subsonic`)
count by kind (they carry no `seek`/`skip`). Output `{track_id, duration_s, bins, bin_s, sessions, plays, completion_rate|null, heard[], replays[], rewinds[], skips[], hotspots[], cold[]}`.
`get_listen_heat` is `read`, free, generic, `ui: /tracks/:id`. Tests: `TestComputeHeat_Example` (120 s: session A 0–60, B 0–30 then seek 30→10 and 10–40, C skip at 5 ⇒ `h[10]=4`, one hotspot `{10,11,4}`),
`TestHotspots_SpanRule`, `TestComputeHeat_SubsonicEvents`, `TestComputeHeat_IgnoresDiagnostics`, `TestHeat_GETNeverWrites`, perf `TestPerf_Heat` (10 000 events, loaded-runner scaling).

### C257. Next-action rules (amends L3 C139; owner m5-callbacks)

`nextactions.Subject` gains kind `take` (validated in `engine.go`, OpenAPI enum `SubjectKind`; `ForTake` today selects rules whose subject is the take's Song, Muse or Record, so C1 extends it with `take`-subject rules for that exact track id). Four rules in `rules_d.go`, golden `testdata/rules_v1_d.golden.json`
(new file, so lanes do not conflict on A/B/C goldens): `callback_waiting` (85, human: a child take with ≥ 1 callback lacking a human verdict; text `"{n} callback(s) wait on "{title}": listen and answer"`; hint
`list_callbacks {state:["pending"], child_track_id}`), `regressed_comment` (72, both: a non-hidden comment in state `regressed`; hint `get_track_context {id}`), `open_fix_unaddressed` (58, agent: open `fix` comments with no
non-terminal patch; text names the count; hint `get_track_context {id}`), `patch_ready_to_render` (52, agent, scope generate: a patch with no non-cancelled batch; hint `estimate_generation {items:[{mode:"song_pr", patch_id}]}`).
All hints validate against the OpenAPI input schemas (`TestNextActions_HintsValidateAgainstOpenAPI`), no text enters ntfy, rule count in `TestNextActions_Rules_Table` increases by four, `/context` integration unchanged
(`ForTake` picks them up under the existing priority ≥ 50 floor and `max` arithmetic; worked case added to `TestContext_NextActions_M5`).

### C258. Section-locked duels and pair suggestions (amends M2b C46–C48, C53)

- `clip.ResolveSectionWindows(a, b SectionSpan, durA, durB float64) (wa, wb Window, err error)`: `L = clamp(min(lenA, lenB) + 2.0, 10, 45)` on the 100 ms grid; `start = max(0, section start − 2.0)`, shifted back
  when `start + L` exceeds the track; equal lengths guaranteed (else 422 `CLIP_WINDOW_INVALID`).
- `create_duel` body gains `section?: {label?: string, kind?: string, ordinal?: integer}` (exactly one of `label` or `kind`+`ordinal`; with `from_s`/`to_s` ⇒ 422 `VALIDATION_FAILED`). Sections come from `library.Timing` of
  each track; none ⇒ 422 `SECTION_UNTIMED {track_id}`; not found ⇒ 422 `SECTION_NOT_FOUND {track_id}`. Loudness matching measures each slot's own window; the row stores slot A's window in `from_ms/to_ms` and slot B's
  in `b_from_ms/b_to_ms` after the slot coin. Views: `Duel.section: {label, source}|null`; `Duel.window` bounds nullable for section-locked duels; `Slot.window` null while unrevealed
  (`TestDuel_BlindRedaction` gains per-slot windows). Imports and originals without timed sections answer `SECTION_UNTIMED`; this is expected until Tier B (deferred) writes `measured` sections.
- `ladder.SuggestPairs(fit Fit, ids []string, verdicts map[[2]int]int, open map[[2]int]bool, k int) []PairScore` (pure, D-M5-31) and `duels.Suggest`; op `suggest_duel_pairs` (read, free, generic, `ui: /duels`):
  scope `lineage` (`root_track_id`; the 40 most recent tracks) or `global` (the 60 most recent tracks with ≥ 1 human rating or duel), optional `section` (both tracks timed), `k` 1..15 (6). Output
  `{scope, root_track_id|null, section|null, model:{nu, verdicts_counted}, pairs:[{a_track_id,b_track_id,a_title,b_title,score,p_a_beats_b,sd_a,sd_b,human_verdicts}]}`.
  `TestSuggestPairs_Golden` (θ = [0, 0.1, 2.0], σ = 0.5, ν = 0.3 ⇒ (0,1) 0.1248, (1,2) 0.0686, (0,2) 0.0648 ± 1e-3, in that order; a pair with an open duel or 3 human verdicts is absent).
  It complements L3's `song_duel` rule (Song-level, "two newest"); it does not replace it.

### C259. Voice notes (`internal/voice`; optional lane m5-voice)

`create_voice_note` (D-M5-23): body `{audio_base64 (≤ 4 MiB decoded), at_second?: number|null, kind?: note|fix|keep (default note), body?: 1..500}` + `IdemHeader`; `MaxBodyBytes` 6 MiB; scope write,
`ActsAsHuman` only (else 403 `HUMAN_ONLY`), `mcp: exclude:human-recording`. Steps as the draft: decode base64 (422 `VOICE_INVALID` reason `encoding|size|container|decode|too_long`); container by magic (`1A 45 DF A3` WebM,
`OggS`, `ftyp` at offset 4 ⇒ MP4) and codec (WebM `A_OPUS`; Ogg `OpusHead`/`vorbis`; MP4 `mp4a` ⇒ aac or `Opus` ⇒ opus); write under `incoming/voice-<uuid>/`; ffmpeg decode to 16 kHz mono PCM (30 s timeout, ≤ 125 s of samples) for
the duration; encode the 64 kbps mono MP3 playback rendition; chmod 0444 and link both into `voice/<aa>/<sha>.<ext>` and `.mp3`; one transaction: `voice_notes` row + the comment through
`judgments.AddCommentTx` (new transactional variant; default body `"[voice note m:ss]"`). `Comment` gains `voice: {id, duration_s, url (signed MP3 link, `TTLFor(p)`), container, codec, bytes}|null`. Only the MP3 is served
(`TestVoice_OriginalsNotServed`); the resolver is appended to `serve.Chain(...)` in `internal/app/media.go`; `internal/backup` hardlinks `voice/`; the SPA plays notes through M2b's shared `AudioContext`, never a second `<audio>`.
`ffmpeg.RequireVoice()` failing ⇒ 503 `UNAVAILABLE {component:"voice"}`. Errors never echo audio bytes. **Fixtures are generated at test time** (no `.webm`/`.ogg`/`.m4a` committed; the privacy gate forbids audio).
The agent-parity test skips this op with its documented reason. Transcription is deferred (needs Tier B's ASR).

### C260. Operations (types in `internal/api/types_m5.go`, per-lane files; JSON snake_case)

New (all `cost: free`; none a dedicated MCP tool; registry 133 → 142):

| Op | Method path | Scope | MCP | UI |
|---|---|---|---|---|
| `propose_patch` | POST `/api/patches` | write | generic | `/tracks/:id` |
| `get_patch` | GET `/api/patches/{id}` | read | generic | `/patches/:id` |
| `list_patches` | GET `/api/patches` | read | generic | `/tracks/:id` |
| `list_callbacks` | GET `/api/callbacks` | read | generic | `/judge/callbacks` |
| `get_callback` | GET `/api/callbacks/{id}` | read | generic | `/judge/callbacks/:id` |
| `submit_callback_verdict` | POST `/api/callbacks/{id}/verdicts` | write | generic | `/judge/callbacks/:id` |
| `get_listen_heat` | GET `/api/tracks/{id}/heat` | read | generic | `/tracks/:id` |
| `suggest_duel_pairs` | GET `/api/duels/suggestions` | read | generic | `/duels` |
| `create_voice_note` | POST `/api/tracks/{id}/voice-notes` | write | `exclude:human-recording` | `/tracks/:id` |

Amended (no new op): `estimate_generation` / `create_generation` / `estimate_variant` item `patch_id` and mode `song_pr`; `create_duel` `section`; `get_track_context` (C254); `list_next_actions` (C257);
`add_comment`/`list_comments` comment `voice` and `origin_callback_id`; `get_duel` section fields. Error codes (UPPER_SNAKE): `PATCH_INVALID`, `PATCH_OUT_OF_BOUNDS`, `PATCH_NO_CHANGE`, `PATCH_RENDER_LIMIT`,
`COMMENT_NOT_ADDRESSABLE`, `RECIPE_UNKNOWN`, `CALLBACK_ALREADY_JUDGED`, `CALLBACKS_BACKLOG`, `SECTION_UNTIMED`, `SECTION_NOT_FOUND`, `CLIP_WINDOW_INVALID`, `VOICE_INVALID`, `HUMAN_ONLY`.
Per-lane `implemented…` maps: `ops_songpr_m5_test.go`, `ops_callbacks_m5_test.go`, `ops_attention_m5_test.go`, `ops_duels_m5_test.go`, `ops_voice_m5_test.go` (no shared table). Generic MCP parity test must keep passing.

### C261. Notifications (amends M2a C37, M3 C71)

One new notification kind, `callbacks_ready` (C255.6), same transport, same count-only discipline as `NoteBatchDone`; nothing else notifies. `TestNotify_M5_NoTextLeaks`.

### C262. Web (`web/`; amends M2a C40, M2b C54, M3 C84, L1–L3 web)

Placement on the shipped shell (tabs New · Library · Play queue · Judge · More; dark only; 44 px targets; secure pages in-app):
- **Track page** (`web/src/routes/Track.svelte`): a **Song PR panel** (open `fix` comments with spots → "Propose a patch" form for the human with the same bounds shown, patch list with renders and callback tallies), a **heat strip** under the waveform with
  hotspot chips (jump with `jumpTo`), a **voice recorder** button in the comment composer (optional lane).
- **Patch page** `/patches/:id` (Library tab; diff, scope, renders, callbacks) — render button goes to `/human/generate` with intent `{mode: song_pr, patch_id, n_seeds}` (M3 confirm flow, unchanged).
- **Judge tab:** new sub-view **Callbacks** (`/judge/callbacks`, `/judge/callbacks/:id`): plays the child at the spot (`jumpTo`), optional "hear the original" at the source spot, three big buttons (fixed / not yet / worse) or kept / lost;
  the Judge landing shows a "callbacks waiting" count from `list_next_actions` (subject kind `take`). **Duels** gains a section picker and a "Smart round" (`suggest_duel_pairs` → `create_duel`).
- **More → Receipts** (`/receipts`, web-only): groups `get_audit` pages by principal with a 30 min gap, money from `list_batches`; no new route data op.
- Network only through `web/src/api/` (ESLint ban holds); voice upload through the generated client (JSON base64); voice playback via `lib/audioContext.ts`; service worker never-handle list unchanged; new routes added in
  `web/src/lib/router.ts` (`APP_ROUTES`) and `nav.ts` (`TAB_OF`, Judge sub-links) by F2 only. Components: `SongPrPanel`, `PatchView`, `HeatStrip`, `CallbackCard`, `VoiceRecorder`, `ReceiptsTable`. Component and unit tests next to them.
- Playwright (INT-W): `m5-songpr.spec.ts`, `m5-callbacks.spec.ts`, `m5-duels.spec.ts`, `m5-voice.spec.ts` (fake MediaRecorder), mapped in `scripts/e2e-map.txt`.

### C263. Tooling, CI, coverage, docs (amends M2a C41, M3 C85/C86, L1–L3 C101/C146)

`make verify` is the iteration target; `make check`/`test`/`e2e` only as final passes (CI runs everything). New coverage floors in `coverage.thresholds` at **measured − 1** by INT-B (provisional: `internal/songpr 92`, `callbacks 92`,
`attention 92`, `anchor 95`, `voice 90`); `scripts/ci-shard-weights.txt` refreshed by `scripts/ci-weights-refresh.sh` after the packages land; perf tests (`TestPerf_Heat`, `TestPerf_SongPRCompile`) calibrated for loaded runners (env-scaled
like L3's) and kept out of the `-race` tiers; privacy gate patterns unchanged (no new hostnames) and fixtures never carry audio or lyrics; docs: `docs/architecture.md`, `docs/runbook.md` (backlog gate, `MM_CALLBACK_BACKLOG_MAX`, voice backup), `docs/device-checklist.md`
(DC-M5-1 record a voice note on the phone, DC-M5-2 answer a callback by ear), the public plan copy with a revision log, and the roadmap edits of "Disposition". Never edit a running script.

### C264. Test harness, fixtures, acceptance

`librarytest.FixtureM5`/`ApplyM5` on top of `ApplyL3`, SQL-only, synthetic names. **Fixtures must include what real data has** (L3 X2 lesson): draft-only Songs (no take), a reference track with an `unknown` recipe, a lossy import, a multi-part
import, a take with lyrics-only sections, a plan-timed take, a take with a `manual` section row, a three-generation lineage with comments on the grandparent, a hidden comment, a Subsonic listen event, and a lossy/plan-less base that
must refuse `propose_patch`. Acceptance: `TestAcceptance_M5_SongPRLoop` (fake backend, real ingest: agent with read+write+generate PAT reads `/context` (ancestor `fix` comment with a spot), `propose_patch`, estimate `song_pr`, create within auto-approve,
the child arrives and its comments become `addressed`; a human PAT answers `fixed` ⇒ `resolved`; a `lost` keep verdict ⇒ regression comment; the critic verdict never moved state), `TestAcceptance_M5_BacklogPacesAgent`, `TestProperty_SongPR_NeverExceedsCaps`
(synctest over C255.5 + existing `CheckCaps`: no sequence of agent estimates/creates plus slow human answers exceeds batch, monthly or the agent's daily limit; `PatchMaxRenders` holds), `TestM5_EveryDatabaseCallMayFail` (fault injection on the hook transaction: no child without callbacks, no callbacks
without child), `TestOps_M5_ParityMCP`, `TestOps_M5_LeaksNothing` (no lyric/comment text in audit, ntfy, job events), `TestImportGraph_M5`, `library verify` clean on `FixtureM5`.

### C265. Reserved

C265 is reserved for the first amendment found during F2's freeze; C266–C299 are unallocated for M5b and M5 follow-ups. M6 starts at C300.

---

## Disposition of the draft (what happened to each contract, and the re-entry criteria)

| Draft | Disposition | New home / destination | Re-entry criterion |
|---|---|---|---|
| C92 config, leaves | kept slim | C250 (`anchor` only; `abc`, `align` deferred) | – |
| C93 migration | kept ~25 % | C251 (patches, callbacks, voice, duel windows); plans/mandates/trials/releases/`jobs` rebuild deferred | with each deferred lane |
| C94 ABC toolkit | **deferred** | M5b (Desk, bar-level patches) | plan-first measured worthwhile |
| C95 anchors | kept, simplified | C252 (no bar anchors) | – |
| C96 Song PR | kept, ABC ops and brief cut | C253, C254 | – |
| C97 callbacks | kept | C255 | – |
| C98 Desk | **deferred** | M5b | canary measures P, O, R with `6P + O < 2R` or the operator picks "breadth per cost" (draft D-M5-12) |
| C99 gen/jobs/provider | kept ~15 % | C253.4 (`song_pr`), C255 hook; plan/`/2`/QuoteTakes/release gate deferred | with Desk / Tier B |
| C100 mandates | **deferred** | M6 Night Shift | Night Shift plan needs a windowed budget the existing per-principal caps cannot express |
| C101 Tier B | **deferred** | M5b / M6 Diction Lab (also Recover recipe addendum, measured sections, score adherence) | ears stack spike (draft M5-P2) passes; models placed by the operator |
| C102 voice | kept | C259 | – |
| C103 attention | kept; triage → next actions | C256, C257 | – |
| C104 section duels, pairs | kept | C258 | – |
| C105 upgrade gate | **deferred** | M6 (ACE-Step) | a second engine release exists |
| C106 receipts | cut to web-only | C262 | – |
| C107 MCP Apps | **deferred** | post-M6 (conditional spike) | spec/host check |
| C108 operations | rewritten | C260 (31 → 9) | – |
| C109 notifications | kept slim | C261 | – |
| C110 Python worker | **deferred** | M5b | with Desk / Tier B |
| C111 web | re-targeted | C262 | – |
| C112 tooling | re-targeted | C263 | – |
| C113 harness | re-targeted | C264 | – |

**Roadmap edits (X1):** M5 line becomes "Song PRs, Callback Listening, attention heat strip and hotspots, section-locked duels and pair suggestions, voice notes"; acceptance becomes "comment → patch → render → callback verdict resolves
the comment (fake engine); an agent loop cannot out-spend the caps or outrun the human's callbacks". New roadmap entry **M5b — GPU lanes (Composer's Desk, ABC toolkit, Ears Tier B incl. Recover recipe)** with its own plan; M6 gains "Spending
Mandates" (with Night Shift) and "upgrade gate" (with ACE-Step). The M6 draft header gets the renumber sentence "starts at C300".

---

## Acceptance mapping (roadmap M5 → named tests)

| Roadmap item | Tests |
|---|---|
| Song PRs (patch addressing comments) | `TestCompile_BoundsTable`, `TestPropose_*`, `TestGeneration_SongPRMode_Table`, `TestContext_*_M5` |
| Callback Listening; verdict resolves the comment | `TestAcceptance_M5_SongPRLoop`, `TestSystemTransitions_Table`, `TestCallbackSubmit_*`, `TestIngest_NoHook_Unchanged`, `TestM5_EveryDatabaseCallMayFail` |
| Attention heat strip | `TestComputeHeat_*`, `TestHotspots_SpanRule`, `TestNextActions_Rules_Table` (rules C257) |
| Section-locked duels, pair selection | `TestResolveSectionWindows_*`, `TestCreateDuel_Section_*`, `TestDuel_BlindRedaction`, `TestSuggestPairs_Golden` |
| Walkie-talkie voice notes | `TestVoice_*` (container matrix generated at test time), `TestVoice_OriginalsNotServed`, `m5-voice.spec.ts` |
| Receipts | `ReceiptsTable` unit tests, `m5-receipts` e2e step (web-only) |
| "An agent … never exceeds its cap" (was mandates) | `TestProperty_SongPR_NeverExceedsCaps`, `TestAcceptance_M5_BacklogPacesAgent` |
| Moved: plan-first cheaper; Tier B; mandates; gate; widgets | M5b / M6 plans |

---

## Lanes and tasks

Foundation lanes first and alone; PR-sized lanes (< ~2.5k changed lines excluding generated files) then run in parallel, each owning files per the hot-spot table; shared test tables are per-lane files.

| Lane | Task | Scope | Depends | Est. size |
|---|---|---|---|---|
| m5-found | **M5-F1** | migration, `anchor`, `library.Timing`, pure `songpr.Compile`, `attention.ComputeHeat`, `ladder.SuggestPairs`, `clip.ResolveSectionWindows`, config var, constants, `FixtureM5`, import-graph and write-owner arch tests | operator box B1, B2 | ~1.9k |
| m5-found | **M5-F2** | contract freeze: `api/types_m5*.go`, nine registrations with 501 stubs naming the lane, `jobs.TakeHook` interface + nil call site, `generation.PatchSource`/`BacklogGate` interfaces, `app/m5.go` wiring skeleton, regenerated `openapi.json` + `schema.d.ts`, web scaffold (routes, nav, api wrappers, empty components), amendments recorded in earlier plan docs, measured tools/list bytes (must be unchanged) | F1 | ~1.4k |
| m5-songpr | **M5-S1** | `songpr` service, propose/get/list handlers, `generation` `song_pr` mode + stale/render-limit checks, `lineage.multi_edit` | F2 | ~2.3k |
| m5-callbacks | **M5-C1** | `callbacks` service, hook wiring in `jobs/ingest.go`, `judgments.SystemTransition`/`AddCommentTx`, ops, ntfy, backlog gate, next-action rules + golden | F2 (hook needs S1's `patch_id`; stub patch rows in tests) | ~2.4k |
| m5-attention | **M5-A1** | `attention` service, `get_listen_heat`, `/context` additions + goldens (single owner of `trackctx`) | F2 | ~1.4k |
| m5-duels | **M5-D1** | section-locked duels, per-slot windows in views, `suggest_duel_pairs` | F2 | ~1.7k |
| m5-voice | **M5-V1** (optional) | `voice` service, `create_voice_note`, media resolver, backup, ffmpeg capability | F2, box B5 | ~1.8k |
| m5-web | **M5-W1** | Track page Song PR panel, patch page, heat strip, comments with spots | F2, S1 stubs | ~2.3k |
| m5-web | **M5-W2** | Judge Callbacks sub-view, duels section picker + Smart round, voice recorder, Receipts | F2 | ~2.2k |
| m5-int | **M5-INT-B** | acceptance, property (caps), fault injection, parity, leaks, coverage floors, shard weights | S1, C1, A1, D1, V1 | ~1.5k |
| m5-int | **M5-INT-W** | Playwright specs, `e2e-map.txt` | W1, W2, INT-B | ~1.0k |
| m5-int | **M5-X1** | docs, public plan copy with revision log, roadmap edits, M6 header | INT-* | small |
| m5-int | **M5-X2** | production-copy check (`VACUUM INTO` snapshot: migration test, `library verify`, a Song PR over real comment shapes: ancestor comments, lyrics-only sections, reference tracks), spend-free | X1 | small |
| m5-int | **M5-X3** | operator review and the **first real Song PR render** (one batch, ≤ 4 takes, existing spend protocol; operator-approved) | X2, operator | small |

**Hot-spot ownership** (one owner per file; others request changes through the owner or the F2 freeze):

| Files | Owner | Note |
|---|---|---|
| `internal/store/migrations/<next>_close_loop.sql`, `internal/archtest/*_m5_test.go`, `internal/config` (one var) | F1 | frozen after F1 |
| `internal/api/types_m5*.go`, `registry.go` registration list, `openapi.json`, `web/src/api/schema.d.ts`, `web/src/lib/{router,nav}.ts`, `web/src/App.svelte` | F2 | later lanes add only in their own per-lane files; schema regenerated at merge |
| `internal/songpr/**`, `internal/generation/{types,plan,expand,estimate,create}.go`, `internal/lineage/modes.go`, `ops_songpr_m5.go` | S1 | `generation` edits are small and listed in C253.4 |
| `internal/callbacks/**`, `internal/jobs/ingest.go` (hook call only), `internal/jobs/hooks.go` (F2 interface), `internal/judgments/comments.go`, `internal/nextactions/{engine.go (kind), rules_d.go}` + `rules_v1_d.golden.json`, `ops_callbacks_m5.go` | C1 | C1 is the single owner of `ingest.go` and `judgments` changes |
| `internal/attention/**`, `internal/trackctx/**` + its goldens, `ops_attention_m5.go` | A1 | single owner of `/context` goldens |
| `internal/duels/**`, `internal/media/clip/window.go`, `internal/ladder/suggest.go`, `ops_duels_m5.go` | D1 | – |
| `internal/voice/**`, `internal/media/serve` wiring in `internal/app/media.go`, `internal/backup`, `ops_voice_m5.go` | V1 | optional lane |
| `web/src/routes/Track.svelte`, `Patch.svelte`, `components/{SongPrPanel,PatchView,HeatStrip}` | W1 | – |
| `web/src/routes/{Judge,Duels,Callbacks,Receipts}.svelte`, `components/{CallbackCard,VoiceRecorder,ReceiptsTable}` | W2 | – |
| `coverage.thresholds`, `scripts/ci-shard-weights.txt`, `scripts/e2e-map.txt`, `docs/**` | INT-B / INT-W / X1 | single owner each |

**Task checklists.** Each task: write the named tests first (see contracts), watch them fail, implement, run `make verify` plus the focused package tests, record any amended contract in the owning plan document in the same commit.
F1 additionally pins `TestTiming_RankMatchesSelectSections`, `TestMigration_M5` (every new table has immutable/no-delete triggers where listed; `ALTER … ADD COLUMN` on populated `jobs`/`comments`/`duels` keeps rows) and the write-owner
tests. F2 additionally asserts `TestToolsList_M5Budget` (no M5 op is a dedicated tool; tools/list byte count equals L3's) and `TestOps_StubsAnswer501M5`. C1 additionally owns `TestIngest_NoHook_Unchanged` and the fault matrix.
X3 step 1 (operator approval before any spend) is mandatory: the agent states the estimate, the operator approves, nothing else spends.

## Definition of done (M5)

All lanes merged through the branch-protected `main` (strict, all required checks); `library verify` clean on a production copy; the acceptance and property tests above green in CI (heavy tiers included); registry 142 ops, no new dedicated
tool, tools/list byte size unchanged; coverage floors set from measured values; public plan copy and docs merged; roadmap and M6 draft edits done; device checks DC-M5-1/2 recorded by the operator; one real Song PR rendered and judged
(X3), results only in private notes.

## Risks / open questions

- **Spots on real data.** Imported originals have lyrics-only sections: callbacks fall back to proportional spots (confidence 0.3) and section-locked duels refuse (`SECTION_UNTIMED`). Mitigation: show the basis in the UI; Tier B (M5b) is the real fix.
- **A hook error rolls back a take.** Intended (no child without callbacks), but it can fail an ingest for a callback bug; mitigated by the fault matrix and by `TakeHook` being nil-safe and narrow.
- **`/context` size.** New per-comment fields consume budget; trimmed first, goldens byte-identical without data.
- **Backlog gate vs. legitimate bursts.** Default 6 per lineage root, 0 disables; operator box B3.
- **Deferral risk.** Cutting the Desk and Tier B postpones the draft's "plan-first" and diction goals; the roadmap edit makes that explicit and the draft stays on disk as the source for M5b.
- **Parity exception** (`create_voice_note`) needs sign-off; the lane is droppable without touching others.

---


## Revision log

### Revision 3 (2026-11-06) — as shipped: what each lane changed versus the plan above

The spend-free core is built: Song PRs, Callback Listening, attention (heat strip and hotspots), section-locked duels with pair suggestions, voice notes and web-only Receipts. The Desk, Tier B, mandates, the upgrade gate and widgets are **M5b / M6 deferrals**. Migration `0013_close_loop` (additive; it took the first free number). Nine operations, all generic or excluded, no dedicated MCP tool; `tools/list` grew from 108,228 to 109,650 bytes from additive fields (no new tool). No M5 operation spends by itself: a Song PR render goes through the M3 estimate and confirmation.

**Operator decisions.** The scope cut and the patch bounds (at most 8 lyric lines, 4 sections, 3 style terms, BPM within 20, no ABC bar edits) are approved; the pacing guards (a callback backlog of 6 per lineage root, 3 renders per patch) are accepted; the other boxes took their defaults.

**Deviations from the contracts.**

- **Rewinds.** Clients emit a backward `seek` which the store (`to_ms >= from_ms`) refused, so rewind telemetry was lost in production. A backward seek is now stored with the positions swapped and `detail = 'rewind'`; `listening.Unnormalise` restores the order for any reader. A backward `segment` is still rejected.
- **Bounds.** The style bound is `max(added, removed) <= 3`; the BPM bound applies only when both sides declare a BPM; a Song PR is refused for a lossy or discarded base, for a non-yue2 engine and for a reference recipe.
- **Comment transitions.** The first child take moves addressed comments to `addressed` (up to three renders per patch); the render count is the distinct non-rejected batches excluding cancelled jobs. `RecheckComments` accepts `addressed`. Callbacks are created in the take transaction and **any error rolls the Song PR take back**; `Build` refuses to wire `song_pr` without the callbacks hook and the backlog gate. A fix callback's source track is the comment's own track. A critic verdict never moves state and is hidden until the caller answers.
- **Same musical spot.** Section windows come from the best timed source; untimed takes answer `SECTION_UNTIMED{track_id}`. A section duel's unrevealed view carries no window bounds and no slot windows (`TestDuel_SectionBlindness_Leak`). `suggest_duel_pairs` defaults to lineage scope and then needs `root_track_id`.
- **Attention.** Heat and hotspots are computed on read from human and device listen events; agent listening is not counted.
- **Voice notes.** `create_voice_note` is a **documented parity exception**: human-only and `mcp: exclude:human-recording`. Upload validation covers encoding, size, container, decode and length; the original upload is stored content-addressed (mode 0444) under `voice/` and only a 64 kbps mono MP3 rendition is served; the backup treats `voice/` as a media root. Without the encoder, `UNAVAILABLE{component: voice}` and stored notes keep playing. The recorder is mounted under the comment composer and offers Save only from a recorded state. `Comment.voice` carries the stored upload size, the MP3 URL and the decoded duration.
- **`/context`.** Gains per-comment `relation`, `spot` and `lyric_lines`, ancestor comments, in-flight patch ids and listening hotspots; trimmed before comments; an empty-take bundle is byte-identical to before. Four `take`-subject next-action rules were added (the L3 next-action golden was re-baselined: `open_fix_unaddressed`).
- **Web.** Song PR panel, patch page `/patches/:id`, heat strip (zero height; overlays the waveform edge), Judge "callbacks waiting" list and card, Smart round, Receipts grouped by principal with a 30-minute gap. Bugs found by the end-to-end lane and fixed: the patch page crashed on lyrics-only patches (a null style diff) and a heading overflowed at 360 px. A follow-up (fix PR) lets the Song PR panel offer `regressed` fix comments as well as `open` ones.
- **Harness.** The test rigs never set the callback backlog (the gate was off in every rig; the default is now 6). A flaky canary in an L3 leak test (a random canary decoded as a valid cursor about 6 % of the time) was fixed.
- **Floors.** Measured, below the plan's targets: `songpr` 87, `callbacks` 82, `attention` 90, `voice` 81, `anchor` 91; to be raised from merged CI.

**Deferred (M5b / M6).** The Composer's Desk and ABC bar edits, Ears Tier B (separation, diction, measured sections), the Recover recipe, Spending Mandates, the upgrade gate (its seed is E2's engine adoption), Night Shift, widgets and an edit-brief operation. Section-locked duels and spot mapping use only plan and manual sections until Tier B exists.

**Hand-offs recorded from E2 (2026-11-06).** The engine trial and adoption log of E2 (`engine_adoption_log` and its view; written by `metamusic engines adopt|reject`) are the seed of the M6 upgrade gate. M5 added no migrator directive or `jobs` rebuild of its own (migration `0013` is additive), so E2's `0015_engines` rebuild of `jobs` (which copies M5's `patch_id` column, index and trigger) is the only one. A Song PR is refused for non-yue2 engines. ACE-Step is no longer an M5 or M6 item.
