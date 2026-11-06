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
  `reference` (a 2 to 30 s clip as timbre and style reference, rendered to an exact 30 s FLAC and cached), `extend`, `complete`
  and `stems`. Every change is a new Take through a job; a stem is the exception, an artifact of its source Take that creates no
  Take, Song or Pick. ACE-Step pins every output-affecting parameter in `engine_params.json`, tied to the pinned upstream commit.
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

## GPU work

Rendering runs on a RunPod Serverless endpoint per engine (YuE2 and, when enabled, ACE-Step); the server submits jobs and ingests results. Transforms run on the application host.

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
