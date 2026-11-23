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
paths, from a trusted proxy peer, with a matching edge secret header, for a configured set of
allow-listed users, each mapped to a role (see "Users and roles"). Agents use scoped personal
access tokens (stored hashed). Human-only operations live under `/human/api/` and are refused for
any non-human principal.

## Users and roles (U1)

One library, several people. A user is a name in the `MM_USERS` config map
(`MM_USERS=alice:owner,bob:editor,carol:listener`); there is no users table. Login stays SSO through
the proxy: a request is accepted only on the trusted-peer, edge-secret and single `Remote-User`
conditions above, the name is folded to lowercase and looked up in the map, and a name that is not in
the map is anonymous (401). Exactly one user is the Owner. The role is derived from the map when the
process starts and is never stored in a session, device or token, so a demotion or removal takes
effect on the next restart. `MM_TRUSTED_USERS` is a deprecated alias that stands for one Owner.
`metamusic config check` validates the map without starting the server.

| Role | What it can do |
|---|---|
| Owner | Everything: spend, approvals, caps, publishing, tokens, backups, administration. |
| Editor | Creates and edits drafts, playlists, Crates and Records (unpublished), rates, and requests generations; every Editor batch waits for the Owner's approval. Cannot approve, change caps, publish or administer. |
| Listener | Listens and rates. Read-only view of the library. |

Two scopes, `listen` and `rate`, back this: a Listener holds only those two, so no operation outside
browsing, playback and rating is reachable. One pure function (`api.Permitted`) gates REST, the MCP
tool list and the SPA's `allowed_ops`; operations that spend, approve, publish or administer carry
`owner_only` in their `x-mm` metadata and are refused for an Editor or Listener. Field-level
redaction lives in one package (`internal/view`) called by the handlers.

What is hidden from whom:

- **Money.** Costs, spend, ledger, caps and provider status are Owner-side. An Editor sees its own
  estimate and batch cost, nothing of anyone else's. A Listener sees
  no money field at all (the fields are absent, not zero).
- **Muse text and structure, private notes and comments.** Hidden from a Listener; an Editor can read
  Muses. Voice notes are Owner-only.
- **Discarded takes.** A Listener gets 404 for a discarded take and never sees it in search, lineage,
  Songs, Crates, Records, the Atlas, counts or Subsonic.
- **Clean renditions.** A Listener never receives a frozen master or its hash. Playback, download and
  Subsonic resolve to two renditions built on demand with every `MM_*` tag removed: `mp3_320_clean`
  and `flac_clean`. Every user opens Settings to pick this device's playback quality (lossless FLAC or
  MP3); the Owner-only sections of that page (system info, backups, audit, device probe and profiles)
  show only to the Owner.

Ratings are per user: a rating row is keyed by track, source and rater (`owner`, the username, or
`critic` for agents). Every curation signal (picks, Song state, Atlas, next actions, ladder, stats)
reads the Owner's rows only, so another user rating changes nothing the Owner curates. Each user
sees their own stars. The Owner sees every user's rating and the average, an Editor sees its own and
the average, a Listener sees only its own and no average. Removing a user leaves their ratings as
inert rows (the Owner reads them under the raw key).

Devices (paired phones and browsers) and Subsonic clients belong to the user who created them: the
row carries an owner key (`owner` for the Owner; every pre-existing row is the Owner's). A device acts
as its user's role and never more, its ratings and plays attribute to that user, and each Editor or
Listener lists and revokes only their own (the Owner lists and revokes all). Each of them is limited
to 5 active devices and 5 active Subsonic clients (`422 DEVICE_LIMIT`; the Owner is unlimited).

Agents are unchanged: personal access tokens, connector grants and the MCP endpoint remain
Owner-side delegates, and only the Owner mints or grants them. Editors and Listeners have no MCP path.

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

## Generation (M3)

- **Operations**: estimating and creating generations, batches, approvals, jobs, spend caps and the
  provider state are registry operations like everything else (REST, MCP and the web client derive from
  them). A create takes a short-lived confirmation token minted by the estimate that quoted it; the cost
  shown is the cost reserved.
- **Money**: integer micro-USD everywhere. Spend is checked against per-period caps before any submit;
  an agent batch over its limits waits for a human approval (`/human/confirm/<batch>`, second factor).
  Spend caps and arming the provider are human-only operations.
- **Backends**: `none` (default: generation disabled), `fake` (in-process simulation, no network, used by
  `dev` and the tests) and `runpod`. The RunPod backend starts disarmed; arming, disarming and resetting a
  trip are deliberate human acts (`/human/api/provider/state` or the CLI). A tripped provider cancels live
  attempts, purges the queue and scales the endpoint to zero workers; only a human resets it.
- **Runner**: one process holds the lease and submits, polls and settles attempts; reads (`mcp-stdio`)
  never run it. Whether generation is enabled is computed from local rows and key presence, never from
  the network.
- **Worker**: a container image (built by CI, pinned by digest) runs the YuE2 engine behind a small HTTP
  contract. Results come back through HMAC-signed upload URLs on `/worker/v1/...`, which the edge routes
  like media: no forward-auth, no access log, no compression, identity headers stripped.
- **Lineage**: every generated track records its parent, its mutation (new seed, edited lyrics, ...) and
  its job; recipes are immutable, so every change is a new track. Stub recipes and lossy masters are
  refused as mothers.
- **Keys**: RunPod keys live in a separate file in the secrets dir and are scoped (jobs, read, write); the
  all-access admin key is used only by `runpod provision` from an operator-provided file.

## Library model (L1)

- **Nouns**: a Muse is an inspiration (a reference or a brief, with angles); a Song is one piece of work
  (a draft or a lineage of takes); a Take is a track; a Pick is the Song's chosen take; a Session groups
  Songs by import run, collection or batch; Sounds are faceted terms (genre family, era, vocal, mood,
  language, tempo band, engine) derived from parameters through a versioned taxonomy.
- **Derivation**: every L1 row is derived from the earlier tables by one writer, the provenance
  reconciler. Source writes mark keys in a dirty queue through triggers; the reconciler drains the
  queue in the background and once at startup before the listener opens. A fresh or restored database
  is rebuilt by the same pass, and `library verify` checks the invariants.
- **Picks** follow ratings, verdicts and duels unless a human pins a take; every change is an
  append-only event, and agents can only propose.
- **Visibility rule**: Muse text (titles, artists, notes) appears only in the allow-listed Muse
  operations; everywhere else Muses are referred to by id.
- **Reconcile queue**: eventually consistent and crash safe; one settler holds a lease, a stale epoch
  writes nothing, and a transient anomaly is re-marked a bounded number of times.

## Curation (L2)

- **Nouns.** A **Record** is a curated, ordered selection of Songs (its slots), with a title, optional notes and a
  generated cover. A **Crate** is a saved set of Songs or Takes: manual (explicit members) or smart (a stored filter).
  A **tape** is a rendered, gapless FLAC of a published Record. The word "album" belongs to the Subsonic catalog only.
- **Lifecycle.** `draft` (agents and humans edit) to `proposed` (an agent asks for publication, with a rationale) to
  `published` (a human decision). `published` can return to `draft` by unpublishing; `hidden` is a separate flag.
  Agents propose, a human publishes: `publish_record` is human-only (`/human/api/`), absent from `tools/list` and
  refused with 403 `HUMAN_ONLY` for every other principal. A paired device may edit a published Record but never publish it.
- **Slots follow Picks.** A floating slot shows its Song's current Pick; a pinned slot keeps one Take until unpinned.
  The Record view is derived and never stores a resolved Take. A background Syncer turns Pick events into an
  append-only changelog (`slot_upgraded`, `slot_filled`, ...); `pin_all` freezes a Record. Undecided Picks
  (`newest`/`stars`) are flagged and counted, not held back.
- **Publish gate.** Publishing shows exactly what leaves the app (Record-owned text and membership). Take and Song titles
  are read live and displayed for review; the gate is review-by-display, not a hard lock on later catalog edits.
- **Neutral naming and opt-ins.** Muse text never reaches an export, log, audit row, notification or Subsonic response by
  default. Two human-only settings, both off by default, allow Muse names in tape/liner exports (`export_muse_names`) and
  in Subsonic auto-album names (`subsonic_muse_names`). Agents can read the booleans, not change them. Without the opt-in an
  auto-album is named by a frozen neutral ordinal.
- **Catalog.** `internal/albums` derives the Subsonic album list (published, non-hidden Records; Muse and unfiled
  auto-albums) and a change token from the same tables. It owns no rows. See `docs/subsonic.md`.
- **Tape pipeline.** `ffmpeg` decodes each resolved Take to 48 kHz / 24-bit / stereo PCM, a Go stage applies the planned
  gain (loudness matched, true-peak guarded; clipping aborts), `flac` encodes the stream, and Go writes the metadata
  (tags, cover, binary CUESHEET, `CHAPTERnnn` and a text `CUESHEET`) in one rewrite. Lossy sources are labelled and never
  relabelled lossless. A tape needs a published Record and a matching revision; undecided or ineligible slots need a human
  to accept them. One render at a time; at most 3 live tapes per Record and 8 GiB of ready tapes (409 `TAPE_QUOTA`). The
  key covers every input, so an unchanged Record reuses its tape. Masters are never touched.
- **Crates.** A smart Crate stores a filter whose vocabulary is derived from the list operations' parameters; it expands
  to the same SQL as the list operations, so `list_songs` with `crate_id` equals the Crate's own count.
- **No L2 operation spends money or calls a provider.**

## Features, sequencing and the Atlas (L3)

- **Features.** `take_features` and `song_features` are derived, rebuildable rows (migration `0011`): one per Take and one
  per Song (the Song's *subject*: its Pick take, else its newest non-discarded take, else its draft; no subject, no row).
  They hold tempo, key, energy and duration. Every value carries a **basis**: `measured` (an Ears analyzer ran on the
  audio), `declared` (the style text or recipe says so), `plan` (the ABC score says so) or `predicted` (energy only, from a
  fixed mood, genre and tempo prior). A measured value always replaces a predicted one. Values are fixed-point integers,
  never floats: BPM in hundredths (centi-BPM), energy in ten-thousandths (0 to 10 000), key as a Camelot number and
  letter, duration in seconds. Requested and measured BPM are kept side by side with an `octave` flag
  (`sounds.CompareTempo`: measured within 6 % of double or half the request). A Take or Song row is rewritten only when its
  digest or its `feature_def` changes. The Reconciler (server and CLI alike) owns the rows; the taxonomy recompute refreshes
  them in the same batch transaction, so a decided proposal is visible at once.
- **Key and energy (Ears).** Two Tier A analyzers, `mm-key/1` and `mm-energy/1`, share one 240-second mono decode with
  the tempo analyzer. Key is null when the estimator is unsure (a correlation floor and a margin to the runner-up);
  energy is a composite of onset density, brightness and mastered loudness, used *relatively*. Both appear on perception
  and the dossier as `key` and `energy`; they are `omitempty` (absent, never null) so existing `/context` goldens and the
  schema do not move. `analysis.KeyEnabled` is the scope-valve constant for key.
- **The sequencer.** `internal/sequencer` is a pure, deterministic planner (stdlib only). It orders Songs to minimise a
  weighted cost: tempo steps, key distance on the Camelot wheel (neighbours cheap, a clash dear), energy steps, and a
  shape term toward a target curve. Strategies: `arc` (rise to a peak about 70 % in, soft landing; the default),
  `ramp_up` and `steady`. Unknown tempo, key or energy costs a neutral amount and is named in the explanation. Up to 12
  Songs are solved exactly; above that a beam search plus local search, never worse than the input order (without fixed
  endpoints). `internal/sequence` loads Song features and wraps the planner: `suggest_sequence` is a read-only preview of a
  Record, a list of Songs or a Crate; the `sequence` operation of `edit_record_slots` and the `sequence` argument of
  `propose_record` apply it as ordinary logged slot moves (`slot_moved` with `by: sequencer`) plus one stored run with its
  explanation. A run is `stale` once the live order or a slot's Take differs. Pinned slots take part and keep their pin.
- **Next actions.** `list_next_actions` (also folded into `/context`) evaluates a table of rules, each one batch
  statement, and returns ranked items with a text, evidence numbers and an optional hint naming a free operation. Seven
  core rules (proposed Records waiting, a Muse that could make a Record, an unrendered Muse, open taxonomy proposals,
  Songs lacking a duel, unjudged Muses, partial angles) and eight demotable ones. The audience is part of each rule
  (human, agent or both). It is read-only and no hint names an operation that spends. The text names Muses by a short id only;
  `list_next_actions` is the one new entry on the Muse-text allowlist.
- **Taxonomy proposals.** An agent calls `propose_taxonomy` (mappings plus a rationale; at most 10 open per principal,
  50 overall); the stored proposal carries an impact preview (which Songs would gain or lose which facet values, what
  stops being unmapped). `decide_taxonomy_proposal` is a human act on `/api/` with a runtime check (a human or a paired
  device; every other principal gets 403 `HUMAN_ONLY` before any read). Accepting applies the chosen mappings as one new
  taxonomy version noted with the proposal id, skips mappings that became effective meanwhile, and recomputes affected Songs
  and their features. `map_sound_term` remains available to agents.
- **The Atlas.** `get_song_atlas` returns the library as points laid out on the server: the axes are tempo (log),
  energy, duration, family, vocal, era, mood, language, key, and requested versus measured BPM. A deterministic integer
  layout spreads stacked points; paging is by an opaque token over the whole layout. Colour overlays include Muse (ids
  only), state, Pick, family, vocal and Record. The honesty layer is part of the answer: totals by basis, the count of
  points not plotted (no value) and the count of points on a *predicted* axis, which the page draws hollow and can hide.
  The Atlas never invents a position. The filter is the one `list_songs` uses. The page (`/atlas`, under Library) has a
  canvas with pan, pinch, a Select toggle for rectangle selection, and a table fallback.
- **Adherence.** `get_adherence` compares requested with measured tempo (and expected with actual duration) per Song and per
  facet value, with the octave flag and a 5 % band; the error is the minimum over the equal, double and half comparisons.
- **Invariants.** Features, sequencer runs and proposals have one writer each; `library verify` checks them
  (missing or orphaned rows, octave validity, a run that is not a permutation, a cost that regressed, malformed
  proposals). No L3 operation spends money or calls a provider, and none is a dedicated MCP tool: all are reached through
  `call_operation`.

## Engines (E2)

- **Engine layer.** `internal/engine` defines what an engine is: capabilities (`Caps`: tasks, modes, limits, stems), an
  optional *recipe codec* (parse a stored request, apply an operator or patch, hash it) and an ingest policy (does the engine
  report truncation, does it teach the RTF table). `internal/engine/registry` holds the engines the build knows (yue2 always,
  ACE-Step when `MM_ACESTEP=1`). Recipes, lineage, generation planning and ingest dispatch through the registry by engine name;
  nothing outside `internal/engine/**` and the two ingest bridges branches on an engine name. The yue2 codec is byte-identical
  to the pre-E2 code, and a yue2 job on the wire is unchanged.
- **Capabilities follow proof.** What an engine can do is the intersection of its code (`Caps`), the operator's `MM_ACESTEP_TASKS`
  (turbo, or turbo and base) and a committed `features.verified.json` that only a passing canary case may turn on; the rest shows
  as "not yet verified" in `list_engines` and on the `/engines` page.
- **Modes.** Beyond the yue2 modes: `port` (an existing song re-rendered by another engine), `repaint` (a time range),
  `reference` (a 2 to 30 s clip as timbre and style reference, rendered to an exact 30 s FLAC and cached), `extend`, `complete`,
  `stems` and `cover` (a whole-take restyle from a new caption, with a strength). Every change is a new Take through a job; a stem is the exception, an artifact of its source Take that creates no
  Take, Song or Pick. A `complete` item may name an extracted stem of its parent (`edit.source_stem`) as its source, which with a
  `patch.style` is a remix: new arrangement over the stem's vocals, a new Take whose parent is the source Take (the stem must exist
  already; nothing is chained). ACE-Step pins every output-affecting parameter in `engine_params.json`, tied to the pinned upstream commit.
- **One backend per endpoint.** Each engine has its own serverless endpoint and worker image and therefore its own backend
  (`runpod-serverless`, `runpod-serverless-acestep`) with its own provider state (disarmed by default), quote, trips, reconciler
  view, billing source, meter and cold-start allowance. One runner holds one view per backend, sharing the lease and the ingest
  pool; a trip, crash loop or init failure on one backend never stops the other. With a single backend the M3 path is unchanged.
  Spend caps stay global; the status shows the burn bound `G` and the worst simultaneous exposure `X`, and arming is refused when
  `X` exceeds the per-batch ceiling or the month headroom.
- **Adoption.** An engine other than yue2 receives money only after the operator adopts it with the CLI
  (`metamusic engines adopt|reject --reason`); decisions are an append-only log with one audit row each, and there is no REST or
  MCP path. A create for a non-adopted engine fails with `ENGINE_NOT_ADOPTED` (the gate fails closed when it is not wired).
- **Worker inputs.** Input audio (a source master or a reference clip) reaches the worker as signed, short-lived download links
  minted per attempt and never stored; the worker checks length and sha256 before use (`INPUT_FAILED`, `INPUT_MISMATCH`).
- **Money.** Quotes use per-engine, per-task-class RTF (seeded from a committed file, then measured, with provenance). Billing
  windows, buckets, the worker meter and the ledger carry a backend column, so each backend reconciles against its own billing.

## Transforms (T1)

- **What they are.** A transform turns existing Takes into a new Take with deterministic, ML-free DSP: `morph` (two songs,
  tempo and key aligned), `shepard`, `glacier`, `polytempo`, `ensemble` (a room of copies) and the hi-fi kinds `master`,
  `widen` and `binaural` (headphones only). The result is an ordinary Take of engine `dsp`: a frozen master that joins Songs,
  Picks, Records, duels and stars; the parents are never modified. A transform never spends money.
- **Pipeline.** `estimate_transform` resolves the spec (alignment, tempo ratios, expected length) and prices it in CPU units
  against a quota (one pool for agents, one for the human, a queue limit, a per-job output and memory ceiling). A render is a
  queue row; the runner starts a **render child** (`metamusic dsp-run --work <dir>`, stdlib plus `internal/dsp` only) with a
  CPU budget and a memory guard. The child decodes the parents, runs the operator, applies the loudness and true-peak safety
  stage (`internal/dsp/safety`, streaming for long outputs) and writes 24-bit PCM; the bridge re-hashes it, encodes FLAC and
  ingests it through the normal path.
- **Determinism contract.** The same spec re-renders to the same PCM hash. The release binary and CI are built with
  `GOAMD64=v1` (v3 fuses multiply-adds and changes transcendental results); `internal/dsp` uses its own `Exp`, `Log` and `Pow`
  and bans host-dependent math; any algorithm change bumps `dsp_version`; `transform verify` re-renders and compares, answering
  "inconclusive" when the toolchain changed.
- **Streaming finish.** `safety.FinishStream` reproduces the in-memory finish arithmetic for outputs rendered to scratch
  (glacier), so long renders stay in bounded memory; a parity test fails on drift.
- **Third-party data.** The binaural kind can use a derived HRTF table from SADIE II (Apache-2.0), committed at
  `internal/dsp/hrtf/table.q15` with its NOTICE and credit; the privacy gate has one narrow exemption for exactly that file.

## Closing the loop (M5)

- **Flow.** A timestamped `fix` comment on a Take is *addressed* by a **Song PR**: `propose_patch` stores an immutable, bounded
  patch (lyric lines inside the scoped sections, a few style terms, a small BPM change; never structure or ABC bars). Rendering
  is the ordinary M3 estimate and confirmation with item mode `song_pr`; a patch renders at most three times. When the child
  Take lands, in the same transaction, the addressed comments move to `addressed` and **callbacks** are created: one "did the
  fix work" per comment and one "was this kept" per keep comment. The human answers fixed, not yet or worse (and kept or lost) at
  the same musical spot (computed on read from section kind, ordinal and fraction); the human verdict moves the comment to
  `resolved` or `regressed`, and a lost keep adds a regression comment. Agent (critic) answers are stored but never move state
  and are hidden from the human until they have answered.
- **Pacing.** Dollar caps bound money, not usefulness, so an agent's Song PR creates are refused while a lineage root has six
  unanswered callbacks. A callback error rolls the Song PR take back; the build refuses to wire Song PRs without the callbacks
  hook and the backlog gate.
- **Attention.** Heat (120 bins) and hotspots are computed on read from human and device listen events; a backward seek is stored
  with swapped positions and `detail = 'rewind'`. Agent listening never counts.
- **Section duels.** A duel may be locked to a section; both slots play equal-length windows from each track's best timed
  sections, and the unrevealed view carries no window or slot data. Untimed tracks are refused (`SECTION_UNTIMED`).
  `suggest_duel_pairs` ranks pairs by ladder uncertainty.
- **`/context`** carries per-comment relation and spot, ancestor comments, in-flight patches and listening hotspots.
- **Voice notes.** A spoken comment (at most 120 s, 4 MiB) is recorded in the browser and saved only on an explicit Save. The
  upload is stored content-addressed and read-only under `voice/`; only a 64 kbps mono MP3 rendition is served. Voice storage
  is a media root of the backup.

## Shared foundations for the M5b, M6 and T1b program (G1)

Plan: [plans/2026-11-20-g1-foundations.md](plans/2026-11-20-g1-foundations.md) (contracts C320 to C329, revisions 3 to 7 record what each
lane built). G1 adds seams only; no track feature is built here, and nothing in G1 opens a GPU capability.

- **Worker protocol `mm.worker/2`.** A strict superset of `mm.worker/1`: every `/1` field keeps its name and meaning, `/2` adds optional
  fields (`kind`, `ears`, `plan`, `attach`, and the reserved `fallback` upload targets). `/1` is frozen; a job with no needs is sent as `/1`,
  byte-identical to before. The contract is chosen per attempt at submit by `provider.WireFor` (`internal/provider/wire_v2.go`): `/2` only for
  a yue2 job whose needs are non-empty (or, later, when the upload fallback is open); ACE-Step always stays on its `/1` variant. Input and output documents are parsed strictly in Go
  (`ParseWorkerInput`, `DecodeOutput`) and in Python (`worker/src/mmworker/contract.py`), and agree with the schemas
  `mm-worker-2.input.schema.json` and `mm-worker-2.output.schema.json`; a `/1` output that carries a `/2` key is a decode error.
  `Runner.applyWire` (`internal/jobs/wire_v2.go`) fills a `/2` document from the job's frozen spec and builds and re-parses it before sending,
  so a document the worker would refuse is rejected at submit (`SUBMIT_REJECTED`) and never reaches the worker.
- **Artifacts per kind.** One table (`internal/provider/artifacts_v2.go`, mirrored in `contract.py`, both schema enums and
  `worker/contract/testdata/artifacts-v2.json`) lists the upload targets of a document: `analysis.json` for Tier B, `stem-vocals.flac` for
  `keep_vocals`, `plan-NN.tar` (at most 2 MiB) per planned seed. Verification is per kind: an analyze or plan job needs neither audio
  seconds nor a truncation object. The deadlines of a `/2` run put the Tier B budget on top of the render's `/1` window.
- **Job kinds.** `jobs` was rebuilt once (migration `0020_job_kinds`, generated DDL) and gained `kind` (`generate | analyze | plan`),
  `needs_json` (the worker capabilities the job requires; empty for every job that existed before) and `spec_json` (frozen options: input pins,
  Tier B settings, plan seeds, attachment references), all three frozen by trigger (`internal/jobspec` is the strict parser and the canonical
  needs). `recipe_id` stays NOT NULL; only a `plan` job may complete without a take. Input audio is minted from pins on the master by
  sha256 (`jobs.PinMinter`), so a master that changed since the estimate fails `INPUT_MISMATCH` before submit. Ingest dispatches by kind
  through `jobs.KindIngester` (`internal/jobs/seams_g1.go`): a completed analyze or plan job with no registered ingester fails
  `INGEST_FAILED` with `KIND_UNAVAILABLE`. Take hooks chain through `jobs.MultiTakeHook`. Round-trip-factor learning only counts
  plain renders (`jobs.LearnsRTF`).
- **Capability gate.** The worker image declares what it can run (`LABEL io.metamusic.caps`, printed by `MODE=selftest`; vocabulary `v2`,
  `ears`, `analyze`, `plan`, `attach`, `fallback`, `agent` in `internal/provider/workercaps.go` and `caps.py`). A backend's open capabilities are
  the latest row of the append-only `worker_contract_log` (migration `0022_worker_contract`) if its image digest equals the digest in the
  backend's fresh quote, else none; a simulated backend with no row has every capability open. Enforcement is three-fold and fails closed:
  an estimate or create that needs a closed capability is refused with 503 `GENERATION_DISABLED` (reason `worker_outdated`, the missing
  capabilities in `details`) before anything is quoted or reserved; the claim query skips a job whose needs are not a subset of the open set;
  and a worker answer of `CONTRACT_VERSION`, `BAD_INPUT` (on a `/2` job) or `KIND_UNAVAILABLE` closes the capability automatically
  (`via = 'auto'`, only when the attempt ran the gate's digest). A queued job whose needs stay closed fails `WORKER_OUTDATED` after one
  hour and releases its reservation. Only the operator CLI opens capabilities (`metamusic runpod contract`); there is no REST, MCP or SPA
  path. `get_provider_status` reports `worker: {image_digest, caps, changed_at, via}` per backend.
- **Scheduler and time zone.** `internal/schedule.Daily` runs a function once per local day at an editable time, in the zone `MM_TZ`
  (IANA name; empty means the process zone; the binary embeds `time/tzdata`). Each run carries a local-day key; at-most-once per key is the
  consumer's unique constraint, never the scheduler's memory. DST: a nonexistent local time runs at the first valid instant after it, a
  repeated one once. The nightly backup keeps the process zone. Background loops register through `app.Services.Loops` (`app.StartLoops`
  starts them in the prune group; a panicking loop is logged and restarted after a minute).
- **Registration seams.** Tracks do not edit shared hot spots: build hooks (`app.RegisterBuildHook`), media resolvers, system audit ops
  (`audit.AddSystemOps`), shape rules, wire names and item modes (`internal/api/seams_g1.go`) are per-file, init-time and append-only.
  Generated files (`openapi.json`, `schema.d.ts`, goldens, the jobs DDL block) are regenerated with `make regen`, never by hand.
- **Spend rules unchanged.** Every new GPU feature is a new item mode or option of the existing estimate and create pair, so caps, the
  Editor approval rule and parity apply as before. ACE-Step was rejected on 2026-11-20; no new package may depend on it (archtest).

## Ears Tier B, Recover and the Diction Lab (M5b)

Plan: [plans/2026-11-20-m5b-ears.md](plans/2026-11-20-m5b-ears.md) (contracts C330 to C349; each lane's "as built" note records what the
code does). All code lanes are merged and deployed. The GPU side is **not open in production**: the capabilities `v2`, `ears` and `analyze`
stay closed until worker wave W2 (an attended provision of the new image and a canary under the operator's written spend approval), so
today every estimate runs with the listening check off (`tier_b_unavailable`) and an analyze item is refused 503 `GENERATION_DISABLED`.
Everything below the worker boundary (parsing, storage, flags, Recover, the Diction Lab) is spend-free and works on any stored document.

- **Where the listening check runs.** After a yue2 render, or as the whole of an `analyze` job, the worker runs a subprocess (`mmears`,
  `worker/ears`, driven by `worker/src/mmworker/stages/ears.py`) in its own site directory with three pinned MIT models (vocal separation,
  speech recognition, pitch contour; `worker/ears/models.lock.json`). It emits only raw output as `analysis.json` (contract `mm.ears-b/1`, schema
  `worker/src/mmworker/schema/mm-ears-b-1.schema.json`): words, segments, a pitch contour, timings and resources. It never judges. The stage fails
  closed: a crash, a deadline or a missing document becomes a schema-valid `status: error` document written by the parent.
- **Everything else is Go, deterministic and recomputable.** `internal/ears/tierb` is pure: strict parse (`Parse`, `ErrContract`), text
  normalisation and alignment (`Norm`, `Fold`, `Align`), per-line and per-section CER and WER (`Diction`), measured sections
  (`MeasuredSections`), score adherence (`SungNotes`, `Adherence`) and `RecoverLyrics`. `internal/ears` stores the results as `analyses` rows
  (`tier_b_raw`, `tier_b_diction`, `tier_b_adherence`) from the artifact `analysis.json`; a new definition version is recomputed from the
  stored artifact by a CPU-only sweep (`TierBSweep`, loop `ears-tierb-sweep`, every 30 minutes), which never creates GPU work and never
  backfills a take that has no document. Measured sections are written once per track (`library.AddMeasuredSections`) and become the best timing
  source; `library.TimingChain` lets the plan fill the gaps of a measured source for Song PRs, duels, attention and callbacks.
- **Ingest.** `earsjobs.TierBHook` analyses a take whose job needed `ears` once it is committed; `earsjobs.AnalyzeIngester` attaches the
  document of an `analyze` job to the existing take (`AttachAnalysis`, artifact `analysis.json` or the next free `analysis-N.json`) without a
  new track and without a mutation. A stored error document is terminal: a take is never silently re-billed.
- **Read surfaces.** The Dossier tier is `B` when Tier A is complete and the check is `ok`; flags `diction_poor`, `alignment_failed`,
  `vocals_not_detected`, `listening_check_failed`, `melody_off_plan`, `sung_transposed` come from the stored rows (so library filters see them);
  perception carries `diction` and `adherence`; `/context` carries the diction summary. `get_track_tier_b` answers the stored document and
  rows, and 409 `ANALYSIS_UNAVAILABLE` (state `none`, `pending` or `missing`) otherwise. Editors see the check but never money.
- **Quote and gate.** The check is priced into the take's timeout and reservation (`spend.QuoteTakes`, `TakeIn.EarsS`); `options.tier_b` of
  `estimate_generation` is tri-state (auto, `true`, `false`), and the analyze item mode (`track_ids`, 1 to 50) is an ordinary approved spend.
  Both need the worker capabilities (the gate of "Shared foundations"); the canary cases `tier-b` and `analyze` are operator CLI and spend real
  money only under written approval (runbook, "Wave W2").
- **Recover.** `recover_recipe` (generic MCP, REST, SPA on a track whose recipe is unknown) turns the stored transcript of a **reference** track
  into a draft: `tierb.RecoverLyrics` cleans, splits and groups the heard lines into sections, `ears.RecoverInput` assembles lyrics and a
  placeholder style from the Tier A facts, and `drafts.CreateRecovered` writes the draft (origin `recovered`) and its `draft_recoveries` row in
  one transaction (lint failure leaves nothing). **Recover never spends**: it creates no job, estimate or ledger row. The draft renders later
  through the ordinary estimate as operator `recover`: a new root (generation 0, no parent track) whose mutation names the reference as
  `parent_track_id` for provenance only. Without a stored `ok` check it answers 409 `ANALYSIS_UNAVAILABLE` (the hint says to estimate an
  analyze item); an empty transcript is 422 `TRANSCRIPT_EMPTY`; a track with a known recipe is 422 `RECIPE_KNOWN`.
- **Diction Lab.** `internal/diction` (no jobs, spend, provider or API imports) builds per-line views of a take from the stored rows and the
  lyric (`get_track_diction`, with a basis: `asr` timed by the recogniser, or `proportional` from the plan), a per-language lexicon of words
  the recogniser mangled (`get_diction_lexicon`, `update_lexicon_entry`) and human or critic marks (`set_diction_marks`; `diction_marks` is
  append-only, a mark is withdrawn by writing `none`). `set_diction_marks` and `update_lexicon_entry` are `OwnerOnly` ops: the Owner, the Owner's devices and role-less principals (agents) with write scope may call them, an Editor or Listener gets 403 `OWNER_ONLY`. A person's marks are `human` and feed the lexicon and calibration; an agent's are `critic` and affect neither. Discarded takes are left out of the lexicon, which is shared by every viewer. The lexicon feeds the draft lint (a
  warning, never an error, naming words heard wrongly in earlier takes) and the estimate's planner warnings, and `diction.HumanLabelledLines` is
  the interface the Critic Bench reads. The SPA has the karaoke view on the Track page and `/diction`. With the Subsonic option on, a take
  with word timings answers `getLyricsBySongId` as synced lyrics (at least 75 % of lines timed); every other take answers as in M4a.
- **Migrations.** `0021_diction` and `0024_recover`, both additive (new tables with triggers; no table rebuild).
- **Ownership.** `draft_recoveries`, `diction_marks` and `lexicon_overrides` have a single writing package (`TestWriteOwners_Ears`);
  `internal/ears/earsjobs` may not import `internal/provider` (`TestImportGraph_Ears`, `TestImportGraph_Diction`).

## Night shift, mandates, Morning Crate and Critic Bench (M6N)

Plan: [plans/2026-11-20-m6-night.md](plans/2026-11-20-m6-night.md) (contracts C365 to C389; each lane's "as built" note records what the
code does). Every code lane is merged and deployed (migrations `0025_mandates`, `0027_morning_crate` and `0029_night_shift`, all additive).
**The night is off until the Owner creates a mandate and enables it**: the seeded `night_shift_settings` row is `enabled = 0`, so deploying
changes nothing. The Morning Crate is on from the start (07:00, local time `MM_TZ`) and never spends.

- **A mandate is the Owner's own spend approval, in advance** (`internal/mandates`). `create_mandate` is human-only (two-factor secure page,
  `/human/mandates/new`): holder `night_shift` (principal key `agent:nightshift`), a window, a budget in integer micro-USD, and a scope (modes,
  engine, minimum stars, optional root tracks, takes per lineage and per batch, a per-batch ceiling, batches per day, Tier B). One live mandate
  per holder. `revoke_mandate` stays on `/api/` so the Owner's paired phone can stop spend; it cancels the mandate's unfinished batches in
  the same transaction. Nothing in this track can raise a cap: `internal/spend` owns the arithmetic (`MandateUsage`, `MandateFits`), counts
  settled plus reserved against the budget, and `generation` (`seams_m6n.go`, `estimate_m6n.go`, `create_m6n.go`) re-checks coverage inside the
  create transaction, with the ordinary monthly, daily and batch caps still applying under a mandate. A batch covered by a mandate has approval
  `via = mandate`; a create without a mandate is byte-identical to before (`TestCreate_NoMandateByteIdentical`).
- **The night shift** (`internal/nightshift`) is a scheduled agent, not a new spender. At its local time (default 01:30; the Owner's approved
  setting is $1.00 and up to 6 takes, typed into the app, never a code default) it plans one take per rated lineage (`plan.go`, pure: explore
  and exploit split, mandate scope filter, a trusted Critic Bench critic only as the last tie-break), prices the plan with the ordinary
  estimate (`Estimate` once per night), trims it until the **estimate ceiling** fits the mandate's per-batch ceiling and remaining budget, and
  creates one batch through the ordinary `Create` as principal `agent:nightshift` (read and generate scopes, no token). With no live mandate,
  or when nothing fits, the night is `skipped` with a reason and writes no estimate, batch, job, approval or ledger row; there is no
  notification. A night is claimed once per local day (`night_shift_runs.night` unique), is crash-safe (a `planned` run is adopted or redone
  within 2 hours) and leaves exactly one audit row (`nightshift:create` or `nightshift:skip`). Tier B on night takes follows the
  `listening_checks` setting and the mandate, and the night never sends `tier_b: true`; with the capability closed it renders plain.
- **Halt and settings.** `halt_night_shift` (any Owner-side principal with write scope, including the paired phone) records the halt, then
  cancels the night's unfinished batch; `update_night_shift` (human-only) enables, which needs a live mandate and clears a halt, and carries no
  money field. `preview_night_shift` shows tonight's plan and would-skip reason and writes nothing.
- **The Morning Crate** (`internal/morning`, never imports generation, spend or jobs) builds the daily tape at 07:00 (catch-up 2 hours): the
  takes made by jobs since the previous crate that the Owner has not rated, night-batch takes first, up to 40 minutes in sides of at most 15
  minutes, loudness matched to -16 LUFS with a -1 dBTP ceiling (`gain.go`), one calibration anchor mixed in, written as lossless FLAC sides under
  `derived/morning/` (`build.go`, `wav.go`) with a slate tone naming each chapter's number. **Blind**: until the reveal no chapter carries a
  track id, title, lineage, anchor flag or gain. The headset grammar (`previoustrack`, `nexttrack` and the buffered marker) and blind 1 to 5
  star ratings arrive as raw events (`submit_morning_crate_events`, person-only); the batch that gives every non-anchor chapter stars reveals
  the crate in the same transaction, and the reveal (`reveal_morning_crate`) maps the events to the Owner's comments, ratings and listen
  events and creates the shared playlist "Morning crate YYYY-MM-DD", which the Subsonic clients then list. Side files are kept 7 days
  (`keep_days`); the daily sweep (04:30) removes older files and marks their rows removed, never deletes a row.
- **The Critic Bench** (`internal/criticbench`, read-only SQL over `analyses`, the ratings views and `duel_verdicts`) measures how well each
  critic (Tier A metrics, Tier B scalars, each agent's stars and duel verdicts, the Diction Lab's line classifier) agrees with the Owner: Wilson
  95 % intervals over the Owner's within-lineage rating pairs and blind duels; a critic is `trusted` only with at least 30 effective pairs and a
  lower bound above 0.55. Trust never discards or approves anything; it breaks a tie in the night's plan and picks the label critic.
- **Operations and roles.** Sixteen operations. Every one is Owner-side: an Editor or Listener, and their devices, get 403 `OWNER_ONLY`
  from all sixteen. For agents it differs per operation. `create_mandate` and `update_night_shift` are human-only (403 `HUMAN_ONLY` for every
  agent and every device). `submit_morning_crate_events` and `reveal_morning_crate` pass the role check for a write-scope agent but the
  handler refuses it with 403 `HUMAN_ONLY` (person-only, see the parity exceptions). The reads, `halt_night_shift`, `revoke_mandate` and
  `build_morning_crate` are open to an agent with the needed scope (a read token gets 403 `FORBIDDEN_SCOPE` on the writes). The matrix is pinned by
  `role_matrix.golden`, and the two person-only refusals by `TestAcceptance_M6N_NightFeedsMorningCrate`.
  `TestImportGraph_M6N` pins the package rules (the night never imports jobs, the crate never imports generation, spend or jobs), and no
  package may depend on ACE-Step.
- **Tests that pin the loop.** `TestAcceptance_M6N_SimulatedNight` (one night under a mandate, then a skipped night),
  `TestAcceptance_M6N_NightFeedsMorningCrate` (a night's takes are the first, blind chapters of the next crate; blind stars reveal it; the
  playlist reaches a Subsonic client; the night's ceiling stayed within $1.00 and the mandate, and no cap moved). The money
  properties are `TestProperty_MandateNeverOverCommits` and `TestProperty_RevokeRacingCreate` (`internal/jobs/property_m6n_test.go`); the
  plan's randomised `TestProperty_MandateNeverExceeded` under M3's fault menu was not built (see its revision log). Operating notes are in the runbook, "The night shift, mandates and
  the Morning Crate (M6N)".

## Documented parity exceptions

CLAUDE.md rule 1 admits only documented exceptions to "a capability is a registry operation". They are:

- **Binary and streaming routes** (media, stems, `.lrc`, worker upload and download links): registered with
  `mcp: exclude:binary` and reached by signed links, never as tools.
- **`create_voice_note`**: a registry operation (REST, audited, idempotent) with `mcp: exclude:human-recording`. It needs a
  human or paired-device principal and is refused with 403 `HUMAN_ONLY` for every other principal before anything is read; the
  reason for the exclusion is that the recording is made by a person in a browser and an agent has no microphone.
- **Transforms: `transform verify` and `transform sweep`** are CLI-only host-operator maintenance. `verify` is a re-render with
  no ingest and no queue row, writing only to a scratch directory; `sweep` is offline recovery under the runner lease. Neither
  can be expressed as a queued transform kind, and neither is reachable by an agent token.
- **`metamusic engines adopt|reject`**: an operator decision deliberately kept off REST and MCP, so that no agent can admit an
  engine to spend. `engines list` mirrors `list_engines`.
- **Provisioning and canary commands** (`runpod provision|probe|canary|arm|disarm`) are operator CLI; the provider state is
  also reachable by the human-only `update_provider_state` operation.
- **M6N: `create_mandate` and `update_night_shift`** are registry operations with `mcp: exclude:human-only` under `/human/api/`: only the
  Owner as a person, with two factors, creates a mandate or changes the night's settings, so no agent can grant itself spend or switch the night on.
  **`submit_morning_crate_events` and `reveal_morning_crate`** are registry operations with `mcp: exclude:person-only` (the voice-note
  precedent): they need an Owner-side person and answer 403 `HUMAN_ONLY` to every other principal, because blind ratings are the Owner's own
  judgment and an agent that rated or revealed a crate would break the blindness the Critic Bench measures.

## GPU work

Rendering runs on a RunPod Serverless endpoint per engine (YuE2 and, when enabled, ACE-Step); the server submits jobs and ingests results. A yue2 job speaks `mm.worker/1`, or `mm.worker/2` when it needs a capability the operator has opened for the running image (see "Shared foundations"). Transforms run on the application host.

## Build and delivery

`make check` (format, vet, lint, race tests, web checks), `make e2e` (Playwright on chrome and
firefox against a coverage-instrumented binary) and `make gate` (privacy gate) mirror the CI jobs
`go-fast` and `go-media` (the module split in two parallel jobs), `lint`, `web`, `e2e` and `gate`. The E2E teardown fails the job if a secret-shaped value
appeared in any served response; no HAR files are recorded or uploaded. The release image is a multi-stage Docker build running as
a non-root user.

See [plans/2026-10-12-roadmap.md](plans/2026-10-12-roadmap.md) for milestones.

## Remote agents and mobile (M4a)

- **Hosted connectors.** `/mcp` also accepts OAuth access tokens from an operator-configured OpenID Connect provider
  (`MM_OIDC_ISSUER`). The app is only the resource server: it verifies RS256 JWTs (issuer, audience, expiry, scope
  `metamusic`, trusted user) against the provider's keys and maps the client to a **connector grant** (agent
  principal, scopes from the grant, optional budget limits). Management operations need scope `admin`, are audited and
  pushed; the global spend cap stays human-only. See `docs/connectors.md`.
- **Subsonic.** `/rest/` serves a Subsonic subset for native players with dedicated credentials (`mms_` API key or an
  app password; device principal, never generate). Streams, downloads and covers go through the same media services as
  the web player. See `docs/subsonic.md`.
- **Tagged downloads and covers.** Downloads are the frozen master's audio frames with a freshly written tag block,
  cached outside the masters; covers are derived from the seed or lineage.
- **PWA offline.** A service worker (the network layer of the SPA) caches the app shell and an explicit offline set
  of tracks; it never touches API, auth, MCP or Subsonic paths.
- **MCP resources and prompts** are derived from the same registry as tools.
