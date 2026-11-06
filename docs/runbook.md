# Runbook

Operational procedures. Hostnames below are placeholders (`mm.example.com`).

## Layout

Data and backups live under one volume so snapshots can hardlink the frozen media files:

```
/state/data/     MM_DATA_DIR    database, audio masters, takes, derived files, import mappings
/state/backup/   MM_BACKUP_DIR  one directory per snapshot: <yyyymmddThhmmssZ>[-N]
```

Secrets (`secret_key`, optional `ntfy_token`) are never part of a snapshot.

## Taking a backup

- Scheduled: daily at `MM_BACKUP_AT` (`HH:MM`, process time zone; `off` disables the schedule).
- On demand: `metamusic backup`, the UI, or the `create_backup` operation. Only one backup runs at a
  time across processes (`BACKUP_IN_PROGRESS` otherwise).
- A snapshot appears as a dot-prefixed `.partial` directory while it is written and is renamed
  atomically when complete. Partials older than 24 h are removed. Failures send a high-priority
  notification when `MM_NTFY_URL` is set.
- Retention keeps the newest snapshot of each of the last 14 days and 8 ISO weeks, plus the newest
  overall.

## What a snapshot does and does not protect

- It holds its own verified copy of the database, so database-side changes and corruption after the
  snapshot do not reach it.
- Media files are hardlinked when data and backup share a filesystem, so a snapshot protects them
  against deletion but not against in-place modification of the shared file. Masters are frozen
  (mode 0444) and renditions are immutable, so such a modification would be a bug, not an operation.
  If hardlinking fails (different filesystem) files are copied and verified instead.
- Restore re-hashes every file against the manifest, which detects a corrupted shared file at restore
  time. Snapshots are not periodically re-verified.
- Not included: secrets, the inbox, export artifacts, in-flight upload work. Keep `secret_key`: the same
  key keeps tokens, sessions and devices valid after a restore; a new one invalidates them.
- A snapshot on the same volume does not protect against losing the volume. Copy snapshots off-host
  separately.

## Restore drill

Do this regularly into a scratch directory, never over live data.

1. Pick a snapshot: `ls /state/backup` (ignore dot-prefixed entries).
2. `metamusic restore --from /state/backup/<snapshot> --into /tmp/restore-drill`
   (the target must not exist or be empty; nothing is written if verification fails).
   Exit 0 success, 1 hash mismatch or missing files, 2 usage or schema problem.
3. Read the printed report: file counts (linked vs copied), schema versions, row counts.
4. Start a throwaway server on it: `MM_DATA_DIR=/tmp/restore-drill MM_AUTH=local metamusic dev`,
   check the library and play a track, then stop it and delete the directory.
5. To recover for real: stop the service, move the damaged data directory aside, restore into
   `MM_DATA_DIR`, start the service with the original secrets.

## Notifications

Set `MM_NTFY_URL` (topic URL; `https://` unless loopback). Optional publisher token: secret file
`ntfy_token`. Messages cover backup failure, device pairing and token creation.

## Tier A analysis and the clip cache

**Tier A** is the automatic per-take dossier: silence spans, clipping, token-loop metrics (from
`semantic.npy`, when present), duration against the score plan, and a spectrogram JPEG of at most
100 000 bytes. `metamusic import` and `metamusic seed-synthetic` run it after the take is committed;
a failure is a warning and never changes the import exit code. To (re)run it on demand:

```bash
metamusic analyze            # analyse every take that lacks a current result
metamusic analyze --force    # redo existing results too
```

Analysers are skipped when their current definition already has a row, so a new image build does not
re-analyse the library. If ffmpeg lacks a Tier A filter the command reports `TOOL_MISSING` once; the
image build checks the filters and encoders, so this only happens with a custom `MM_FFMPEG`.

**Clip cache.** Duel and `get_clip` clips are rendered on demand into `derived/clips/` and addressed by
their inputs. The cache is size-capped by `MM_CLIP_CACHE_MB` (default 4096, minimum 256; smaller values
make the server refuse to start). Clips of open duels are pinned; everything else is evicted oldest
first. The cache is not backed up and is ignored by the import orphan report; deleting it is always
safe, the next request renders again. Stale work directories (`incoming/clip-*`, `incoming/ears-*`,
older than one hour) are removed at `serve` and `analyze` start.

## Importing audio without a recipe (reference and lossy tracks)

A plain `metamusic import` imports only audio that matches a recipe, exactly as before. Audio that has no
recipe (a FLAC nobody can match, a standalone MP3 from an old session) is imported only when you say so:

```bash
metamusic import --dry-run                          # lists what would be imported; the "Without a recipe" block names every
                                                    # unmatched FLAC and every standalone MP3 and what each would become
metamusic import --dry-run --reference-unmatched --reference-lossy
metamusic import --plan <report> --reference-unmatched --reference-lossy   # same flags as the reviewed dry run
```

- `--reference-unmatched` imports unmatched FLACs as reference tracks; `--reference-lossy` imports standalone
  MP3s as lossy reference tracks. A mapping row whose request id is `reference` imports that one file the same
  way. `skip` rows always win; an ambiguous match is never imported automatically.
- `--plan` refuses a plan that was reviewed with different reference flags (exit 2).
- MP3s of one song (`name_p1.mp3`, `name_p2.mp3`, ...) form one lineage, the lowest part being the root.
- The dry run writes its report and `mapping.todo.csv` into the data directory. To look at an inbox without
  touching the real library, point `MM_DATA_DIR` at a temporary directory for the dry run.

**What the badges mean.** *recipe unknown*: the track is linked to a stub recipe (`engine = unknown`); no
style, lyrics or seed exist, so it is excluded from `songs.jsonl` exports and refused by lint, draft creation
and as a mother (`RECIPE_UNKNOWN`). *lossy reference*: the master is the MP3 itself, byte for byte, read-only,
without tags and without an MP3 rendition; there is never a FLAC link (`get_media_links` returns `flac: null`),
and loudness, bandwidth and clipping are measured on a decode, so they are not comparable with FLAC takes.
Both kinds can be rated, commented, tagged, put in playlists (a playlist ZIP carries the MP3 master behind a
fresh ID3 tag), compared in duels and backed up. The library filters on both (`recipe_known`, `lossy`).

## Generation and RunPod (M3)

Hostnames below are placeholders (`mm.example.com`). Never paste a key into a command line, a ticket or a
chat; keys are read from files.

### Order of a first activation

1. `metamusic runpod probe --schema` (no keys): no drift against the committed management API snapshot.
2. `metamusic runpod provision --image <ref@sha256:...> --dry-run`, review the plan, then provision with the
   endpoint **inactive** (`workers.max = 0`) from a file holding the all-access key
   (`--admin-env FILE`). Only `provision` reads that key.
3. Create the scoped keys in the RunPod console (jobs, read, write) and put them in `runpod.env` in the
   secrets dir (mode 600). `runpod.env` must not hold the all-access key; `serve` refuses to start if it does.
4. `metamusic runpod probe --keys` records which key may call what.
5. `metamusic runpod provision --activate --max 1`, then **delete the admin key file**.
6. Start with `MM_BACKEND=runpod`. The provider starts disarmed: estimates work, creates answer
   `PROVIDER_DISARMED`. Check `metamusic runpod status`.
7. `metamusic runpod arm --endpoint-name <name> --reason TEXT` once the cached quote is fresh.
8. The canary (`make canary` prints the command) is run only by the operator and only with explicit
   approval; it spends real money. Never run it unattended.

Without `runpod.env` the server runs with generation disabled (`GENERATION_DISABLED`, reason `keys_absent`)
and everything else works.

### Manual kill switch

`metamusic runpod provision --deactivate` sets `workers.max = 0` (needs the admin key file again). If that
key is not at hand, set "max workers" to 0 on the endpoint in the RunPod console, then
`metamusic runpod disarm --reason TEXT`.

### Trip and restoring `workers.max`

A trip cancels live attempts, purges the queue and tries to scale the endpoint to zero workers. If the
write key cannot do that, a critical notification tells you to set max workers to 0 in the console.

The admin key file is deleted after activation, so **restoring `workers.max` after a trip's ScaleToZero is
a manual step**. Either:

- provide the admin key file again and run `metamusic runpod provision --activate --max N`, or
- set max workers in the console;

then `metamusic runpod reset-trip --reason TEXT` and arm again. Nothing re-enables spending by itself, and
an agent cannot arm or reset.

### Deploying

Order: **disarm, drain, deploy, arm.**

1. `metamusic runpod disarm --reason TEXT`.
2. Drain: `scripts/deploy-preflight.sh` refuses (non-zero) while any attempt is live; wait until
   `metamusic runpod status` shows none. `--force` skips the check and is for emergencies only.
3. Deploy (the container's `stop_grace_period` is 90 s).
4. Arm again (step 7 above).

### Restore rule

A restored database is always disarmed (`reason = restored`) and its unfinished attempts become
`unknown`. Arming is refused (`RECONCILE_REQUIRED`) until one billing reconciliation covered the time
since the snapshot. With billing unreadable, `metamusic runpod arm --after-restore --reason TEXT` is the
explicit, audited acknowledgment.

### Billing check

`metamusic runpod status` shows the billing summary and the meter summary. Compare the billed amount with
the reserved amount per batch; billed above reserved is a bug to report, not to ignore. Residual amounts
are released after the hold period when billing is not readable.

### Upload directory

Received worker uploads of jobs that ended without a take are removed 24 h after the job's end.
`incoming/` is never part of a backup.

## Remote agents and mobile (M4a)

### Service-worker recovery

The app registers `/sw.js` only in a secure context (https or loopback). It never handles `/api/`, `/human/`,
`/auth/`, `/mcp` or `/rest/`, and it serves a cached shell only when the network fails. If a browser seems stuck on
an old shell:

1. Reload twice (a new worker activates on the second load).
2. In the browser's site settings, unregister the worker and clear site data for the app origin. This removes the
   offline set from that device only; sync it again afterwards.
3. Check that `/sw.js` answers with `Cache-Control: no-cache` and without any identity header.

### Connector revocation

Revoke a hosted connector on `/human/connectors` (or with `set_connector_grant` and an `admin` token). The next tool
call is refused with 403 `CONNECTOR_REVOKED`; confirm it in the request log. Restoring the grant makes the same
sign-in work again. To cut a client off entirely, remove its id from `MM_OIDC_CLIENTS` and restart. For a
compromised provider, unset `MM_OIDC_ISSUER`: connectors are disabled and `/mcp` accepts only personal tokens.

### Subsonic client rotation

Create the replacement on `/human/subsonic`, enter the new key or password in the app, then revoke the old
client. Revocation takes effect on the next request. A lost phone: revoke its client and its paired device
(`/human/devices`).

### Offline set and download cache

The offline set is one protected playlist (at most 100 tracks) that agents can edit. Tagged downloads are cached
under `derived/dl/`, swept after 7 days and never backed up; covers are cached the same way and are rebuilt on
demand.

## Reconcile and verify (L1)

The library model (Muses, Songs, Picks, Sessions, Sounds) is derived and reconciled in the background.
`healthz` reports `library_model.pending`; zero means the queue is drained.

- `metamusic library reconcile --dry-run` reports the queue and what a pass would create; without the
  flag it settles. `--wait` polls until the queue is empty.
- `metamusic library verify` checks the invariants and exits 1 on any anomaly. Run it after a migration
  and after every restore.
- The server runs its startup derivation steps (taxonomy seed, first Settle) before it opens the listener. Only one
  process holds the settle lease; a CLI run while a server is up only enqueues, and the server drains. If a step
  fails, the server does not exit: it serves degraded (next section).
- Rollback of an L1 deploy is restoring the pre-migration backup.

## Degraded mode (startup derivation failed)

A startup derivation step (`sound_taxonomy_seed`, `library_settle`, and any later L3 step) that fails does not end
`serve`. A restart loop would give you no HTTP, no UI and no health signal. The server opens the listener anyway,
logs the redacted error, and retries the failed step in the background with backoff (5 s, doubling, capped at 5 min).
A corrupt or unmigratable database, bad config and missing secrets stay fatal: `serve` exits before it listens.

- `GET /healthz` always answers 200 (liveness). Healthy: `{"status":"ok","ready":true}`. Degraded:
  `{"status":"degraded","ready":false,"startup_issues":[{"step":"library_settle","code":"SETTLE_FAILED","attempts":3,"since":"..."}]}`.
  `ready` is the readiness field: false while any step fails. The body carries stable codes only, never error text.
  `get_system_info` (Settings) lists the same failures in `startup_issues` as `step=CODE` strings (empty when ready).
- The Docker `HEALTHCHECK` (`metamusic healthcheck`) checks only for HTTP 200, so a degraded container stays
  **healthy**. This is deliberate: unhealthy would let Docker restart it and Traefik drop the route, which is the
  outage degraded mode avoids, and a restart does not repair a persistent derivation error. If you want an
  orchestrator to act on it, probe `/healthz` for `"ready":true` yourself.
- What to check: the container log line `startup step failed; serving degraded and retrying` (step, code, redacted
  error); then `metamusic library verify` and `metamusic library reconcile --dry-run` on the same data directory.
  Until the step succeeds the derived model (Songs, Muses, Sessions, Sounds) may be stale; generation and the
  masters are unaffected.
- Recovery: fix the cause (restore a backup, or deploy the fix) and the next retry clears the issue by itself;
  `healthz` returns to `ok`/`ready:true` without a restart. Restarting is only needed to apply a new binary.

## Taxonomy edits (L1)

The taxonomy is versioned and append-only. The code seed is applied at startup as one version per seed
id; extensions are added with `map_sound_term` (at most 50 mappings per call) and recompute the affected
facets synchronously. A mapping is never edited in place: add a `remove` and an `add`. A better seed
ships as a new seed revision, never as a silent edit. Review the top unmapped terms periodically and map
the worthwhile ones.

## After a restore (L1)

A restored database keeps the derived rows of its snapshot. After restoring, start the server (the first
`Settle` completes what the snapshot lacked), then run `metamusic library verify`; it must report
nothing. If derived rows were dropped or damaged, `metamusic library reconcile` rebuilds them from the
source tables.

## Features, Atlas and next actions (L3)

### Recompute and verify features

`take_features` and `song_features` are derived. A normal `Settle` (server start, or `metamusic library reconcile`)
writes whatever is missing or stale; the taxonomy recompute refreshes the affected Songs itself. `reconcile` prints a
`features N` count.

```bash
metamusic library reconcile --dry-run   # what a pass would write, features included
metamusic library reconcile             # rebuilds missing or stale feature rows
metamusic library verify                # exit 1 on any anomaly (L1 + L2 + L3)
```

The L3 anomaly codes are `song_features_*`, `take_feature*`, `bpm_octave_invalid`, `sequence_*` and `proposal_*`. A
`song_features_missing` after a restore means the settle has not run yet; run it, then verify again. A restored database keeps its
snapshot's rows and `reconcile` completes them (see After a restore, L1).

### Key and energy: `metamusic analyze`

Key and energy are Tier A analyzers like tempo. `metamusic analyze` runs every analyzer that lacks a result for its
current definition (`mm-key/1`, `mm-energy/1`); `--force` redoes all, `--track ID` limits it, `--jobs N` parallelises
tracks, `--dry-run` writes nothing. One decode serves the three analyzers, so a track costs about two seconds of CPU.
After it finishes run `metamusic library reconcile` (the new measurements replace predicted energy and declared keys) and
`library verify`.

Check the keys by ear on a few recordings you know before relying on them: the estimator is validated on synthetic
signals, returns null when unsure, and can be wrong on modal, modulating or percussion-heavy music. Several parts of one
song may legitimately get different keys.

### The key fallback switch

`analysis.KeyEnabled` (`internal/analysis/key.go`) is a compile-time constant. Setting it to `false` and redeploying makes
the analyzer store its null result: key stays declared, plan or null, and sequencing and the Atlas treat key as unknown
(neutral cost). Existing measured keys are replaced on the next `analyze --force` and `reconcile`. Energy and tempo are
unaffected. There is no runtime flag by design.

### Next-action rules and the scope-valve cut order

Next actions come from a rule table (`internal/nextactions/rules*.go`); there is no per-rule runtime switch. If the list is
noise, cut by editing the table and shipping a build. The core seven stay: `record_proposed_waiting`,
`muse_record_candidate`, `muse_unrendered`, `taxonomy_proposals_open`, `song_duel`, `muse_unjudged`,
`muse_partial_angles`. Demotable, lowest priority first (the order to cut them): `features_missing`, `song_no_muse`, `taxonomy_unmapped`,
`record_unsequenced`, `song_tempo_mismatch`, `song_proposals_waiting`, `record_empty_slot`, `record_undecided_slots`.
The scope-valve order for the rest of L3, if something must be cut: neighbours on the Atlas and the Song page; colouring
by Record; the duration and key axes; sequencing a Crate; by-facet adherence and its tab; the key analyzer; the demotable
rules above; the weekly property job. Record sequencing, next actions, taxonomy proposals and the Atlas itself are never
cut. A cut that changes a surface needs the matching decision and golden.

### Degraded mode and L3

The startup `Settle` now includes the feature step, so a failure there is a `library_settle` startup issue like any
other (see the previous section): the server serves degraded, `/healthz` says `ready:false` and the retry loop heals it.
While degraded, the Atlas, adherence and sequencing read whatever feature rows exist (rows may be missing or stale, and
the totals strip counts what is missing); `suggest_sequence` answers `SEQUENCE_NO_FEATURES` when no Song has features.
The taxonomy recompute runs the feature step inside each batch, so a taxonomy decision still refreshes features while
the settle retries. Nothing here spends money or touches masters.

### Taxonomy proposals

Agents propose with `propose_taxonomy`; you decide on the Sounds page (Proposals tab) or with
`decide_taxonomy_proposal` (human or paired device only). Review the impact preview (Songs gaining and losing values,
terms no longer unmapped), accept all or a subset, or reject. A mapping that became effective meanwhile is skipped; if every
mapping is stale the proposal expires. Open proposals are capped (10 per principal, 50 overall).

### Reading the Atlas totals

The totals strip counts points by basis (measured, declared, plan, predicted), the points *not plotted* (the Song has
no value on an axis) and, as a lower bound, the points that sit on a predicted value. Hollow markers are predicted and can
be hidden. Until Songs are rendered and analysed most points are predicted: a handful of dense stacks is the expected
picture, not a bug. The requested-versus-got view is empty until generated takes have been measured.

### After a restore (features rebuild)

Start the server (the first `Settle` rebuilds missing feature rows), then `metamusic library verify`. If rows were damaged,
`metamusic library reconcile` rebuilds them; run `metamusic analyze` first only if the analyses table itself was lost.

## Records, Crates and tapes (L2)

### Publishing and unpublishing

Agents edit drafts and may propose; publishing is a human action on the web (a paired device cannot publish).
The publish page lists the text that will leave the app. Unpublishing returns the Record to draft and clears its
publication fields; its first publication time is kept. A published Record still follows Pick changes and logs them;
`pin_all` freezes it.

### Tapes: disk, retention and a failed export

- Tapes live under `derived/tapes/`, are **never backed up**, and are kept 14 days after they become ready. A sweep
  expires old rows, removes their files and removes orphan files older than a day.
- Quotas: 3 live tapes per Record, 8 GiB of ready tapes (409 `TAPE_QUOTA`). A render needs about 1.3 times its PCM size
  plus 64 MiB of free disk; otherwise it fails with `TAPE_DISK_FULL`.
- A failed export keeps its row with a code (`TAPE_ENCODE_FAILED`, `TAPE_DISK_FULL`, `TAPE_INTERRUPTED`) and a path-masked
  message; fix the cause and export again. A render interrupted by a restart is
  re-queued while its lease is recent and fails with `TAPE_INTERRUPTED` after three tries.
- Without `flac` on the host, export answers 503 `UNAVAILABLE {component: flac}`; set `MM_FLAC` to its path if it is not
  on `PATH`.
- `metamusic library verify --files` also checks that every ready tape's file exists with its recorded size and prints
  ids and relative paths only. Without `--files` the file system is not looked at.

### Curation opt-ins

`export_muse_names` (Muse names in tape tags and liner export) and `subsonic_muse_names` (Muse names in Subsonic
auto-album names) are off by default and can be changed only by a human on the Opt-ins page. Changing either moves the
Subsonic token and, for the first, the tape key, so new exports differ. Leave both off unless you accept that Muse text
leaves the app in those two places.

### After a restore (Record sync, tapes are gone)

A restore brings back Records, slots and the changelog of the snapshot but no tapes. Start the server (the Syncer then
logs the Pick changes the snapshot lacked), run `metamusic library reconcile --wait` and `metamusic library verify`
(L1 and L2 invariants); it must report nothing. Tape rows whose file is gone are shown as expired; export again.

### Rollback

Rollback of the L2 deploy (migration `0010`, schema 10) is restoring the pre-migration backup. Do not run an older
binary against a migrated database.

## Second engine: ACE-Step (E2)

Everything in "Generation and RunPod (M3)" applies per engine. The ACE-Step engine has its own serverless endpoint
(`mm-acestep`), its own worker image, its own backend and its own `provider_state` row, so arming, disarming, trips and
reconciliation are per engine and never touch the YuE2 endpoint. It is off until `MM_ACESTEP=1` (needs `MM_BACKEND=fake` or
`runpod`); with `fake` the engine runs against an in-process simulation and spends nothing.

### Provisioning the second endpoint (inactive) and the cached model

RunPod offers exactly **one cached model per endpoint** and its management API snapshot has no cached-model field, so the model
is attached by hand in the console. The base DiT that `complete` and `stems` need is baked into the image instead.

1. `metamusic runpod provision --engine acestep15 --image <ref@sha256:...> --dry-run`, review the plan. The image digest is
   committed in `deploy/worker-acestep-image.digest`; tags are refused. `--tasks turbo` (default from `MM_ACESTEP_TASKS`) or
   `--tasks turbo,base`; `--pools` defaults to `MM_ACESTEP_POOLS` (24 GB classes; 480 s is the documented maximum duration there).
2. Run it without `--dry-run` from the all-access key file (`--admin-env FILE`, as for YuE2). **An ACE-Step endpoint is always
   created inactive** (`workers.max = 0`); `--vae-source` is refused for it.
3. **Console step (printed by the command, one per cached model):** open the endpoint `mm-acestep`, Model settings, and set the
   cached model to the printed repository and revision. There is no `runpodctl` line on purpose. `provision --engine acestep15
   --verify-models` is a read-only report of the endpoint and of those steps; whether the model is really visible to a worker is
   proven by the canary's first case.
4. Put the endpoint id into `runpod.env` as `RUNPOD_ACESTEP_ENDPOINT_ID` (the scoped keys are the same file). Without it the
   engine reports `keys_absent` and everything else works. `metamusic runpod probe --keys` classifies the keys for both endpoints.
5. Activation (`provision --engine acestep15 --activate --max 1`) follows the same rules as the first activation, then delete the
   admin key file. `provision --activate` does not itself check the global concurrency bound; read `metamusic runpod status`
   afterwards (see below).

The first start of a fresh host pulls a large image and downloads the cached model; both are unbilled but long (tens of minutes
the first time). The first canary case records the wall time. Do not read a long first start as a hang.

### Engine adoption

An engine other than YuE2 receives money only after the operator adopts it. There is no REST or MCP path.

```
metamusic engines list
metamusic engines adopt acestep15 --reason "..."
metamusic engines reject acestep15 --reason "..."
```

Each call appends one row to the append-only adoption log and one audit row (`cli:engines-adopt` or `cli:engines-reject`); history
is never rewritten, and the latest row decides. A create for a non-adopted engine answers `ENGINE_NOT_ADOPTED` with this command
as the hint. Adopt only after the canary passed and you listened to a take.

### The ACE-Step canary

The canary spends real money, is typed by the operator, and runs only with written approval. `--max-usd` is required and bounds
the sum of the stage ceilings; the command refuses when the sum exceeds it.

1. `metamusic runpod canary --engine acestep15 --all --dry-run [--part a|b]` prints the worst case per case and spends nothing
   (`--price-usd-hour` overrides the illustrative price). Part `a` is the E2a spine, part `b` the E2b modes. Read the figures and
   choose `--max-usd` from them.
2. Run one case at a time, with `MM_TOKEN` (a PAT with read, write and generate):
   `metamusic runpod canary --engine acestep15 --max-usd N --case t2m` and then `t2m-long` (use `--max-s 480`; 600 only on a
   32/48 GB pool being verified), `reference`, `repaint`, `terminate`, `timeout`, `kill-switch` (needs `--kill-switch-confirm` and
   `provision --engine acestep15 --deactivate` while the job runs), `init-fail` and `first-pull`. E2b adds `extend`, `complete`,
   `extract`, `base-long` and `lrc`. `--source-track ID` reuses a kept 30 s track for the edit cases.
3. `init-fail` needs the endpoint provisioned with `provision --engine acestep15 --activate --max 1 --init-fail`, then the canary
   with `--init-fail-confirm`; restore afterwards with a normal `provision --engine acestep15 --deactivate`.
4. A passing run prints a proposed `features.verified.json` and RTF patch; the files are **not** touched. `--measured-out FILE`
   merges the measured numbers (peak VRAM per component, LM memory utilisation, phase seconds, read from the job's
   `timings.extra`) into `engine-canary-measured.json` for you to review and commit. A capability flag is merged into
   `features.verified.json` only for a case that passed; the rest stay off and the UI says "not yet verified".
5. Exit codes: 0 pass, 1 fail, 2 usage, 3 refused (cost or disarmed), 4 aborted.

### Arming, disarming and deploying per backend

`metamusic runpod status|arm|disarm|reset-trip` take `--engine yue2|acestep15` (default yue2). `arm` needs the endpoint name typed:
`--endpoint-name mm-acestep`. `get_provider_status` (and the Spend page) lists every backend and the global bounds `G` (burn rate
if every endpoint ran flat out) and `X` (worst simultaneous exposure). **Arming is refused with `CONCURRENCY_EXCEEDS_CAP`** when
`X` exceeds the per-batch ceiling or the month headroom; disarming is never refused. After raising `workers.max`, re-check status.

Deploy order is the M3 order, **per backend that is armed: disarm, drain, deploy, arm.**

1. `metamusic runpod disarm --engine acestep15 --reason TEXT` (and the same for the YuE2 backend if it is armed).
2. Drain: `scripts/deploy-preflight.sh` refuses while any attempt is live; wait until `status` shows none for each engine.
3. Deploy.
4. Arm each backend again.

A trip on one backend cancels only that backend's attempts, purges its queue and scales only its endpoint to zero; reset it with
`reset-trip --engine ...`. If a worker never starts, the operator sees restarts or an `UNHEALTHY` worker in `status`, the job
stays queued, and the reconciler trips that backend only on evidence older than five minutes (the job ends by its TTL); the manual
step is the same scale-to-zero as in the M3 kill switch, for the `mm-acestep` endpoint.

### Billing, the meter fix and drift

Since migration `0016` the billing windows, buckets, worker meter and ledger carry a backend column, so each backend is trued up
against its own billing. The worker-lifetime meter is wired in production (earlier builds settled every batch's worker overhead at
zero); after the first deploy of this version, compare billed and reserved amounts per batch as in "Billing check". The weekly
drift job watches the REST snapshot for each endpoint.

### Stems, lyrics timing and reference clips

A stem is stored as a `stem-<name>.flac` artifact of its source take; a repeated stem is numbered from 2. It creates no take, so
it is not in the library lists. An invalid `lyrics.lrc` is dropped with an `lrc_dropped` job event and the take still lands.
Reference clips are cut and cached under `derived/clips/` (evictable; not backed up).

## Transforms (T1)

Transforms are ML-free DSP renders on the application host and **never spend money**. The server must run with
`MM_TRANSFORM_ENABLED` on; the render child is the same binary (`metamusic dsp-run`, started by the server, not for humans).

### Quotas and limits

`MM_TRANSFORM_AGENT_DAILY_CPU_S` (shared by all agent tokens) and `MM_TRANSFORM_HUMAN_DAILY_CPU_S` are daily CPU-second pools;
one render runs at a time; `MM_TRANSFORM_QUEUE_MAX` bounds the queue (a full queue is 429 `TRANSFORM_QUEUE_FULL` with
`Retry-After`); `MM_TRANSFORM_MAX_CPU_S`, `MM_TRANSFORM_MAX_OUT_S` and `MM_TRANSFORM_MAX_MEM_MB` bound one job (the budget kills
a runaway child; a memory guard kills a child that exceeds its estimate). A refused render says when its pool resets.

### Verifying a transform: `transform verify`

`metamusic transform verify TAKE_ID` (or `--all [--limit N]`) re-renders the transform from its stored spec in a scratch directory
and compares the PCM hash. It ingests nothing and writes no queue row. Output per take is `match`, `MISMATCH` or `inconclusive`;
**exit 1 on any mismatch**, exit 0 for inconclusive. `inconclusive` means the answer cannot be trusted either way: the Go
toolchain or the DSP version changed since the render, so a different hash is not evidence of corruption. A MISMATCH on an
unchanged toolchain is a bug to report.

### The determinism pin

Release binaries and CI are built with `GOAMD64=v1`; the Dockerfile asserts it from the binary's build info. A binary built for a
newer level fuses multiply-adds and changes the DSP results, so never override it. Any DSP algorithm change must bump
`dsp_version`; renders made under an older version verify as inconclusive.

### A transform is stuck: `transform sweep`

First cancel it (`cancel_transform` or the Cancel button). A stopped server leaves a running row until its lease expires, then
startup recovery requeues it (a second interruption ends as `INTERRUPTED`). For offline recovery, stop the server and run
`metamusic transform sweep`; it refuses while a server holds an unexpired lease on a running transform. It removes unreferenced
scratch directories and orphan masters.

### Third-party data (binaural)

The binaural kind embeds a derived head-related impulse response table from SADIE II (Apache-2.0); the credit and NOTICE are in
`THIRD_PARTY_NOTICES.md` and `internal/dsp/hrtf/NOTICE`. Regenerating the table is an offline developer task (`tools/hrtfgen`,
which needs the dataset, never committed); do not regenerate it casually, because changing it changes every binaural render.
A binaural take is for headphones; do not play it through spatial or crossfeed processing.

## Close the loop (M5)

### Pacing guard

`MM_CALLBACK_BACKLOG_MAX` (default 6, 0..50; 0 disables) stops an agent from creating Song PR renders while a lineage root has that
many unanswered callbacks; answer the callbacks on the Judge page (or turn the guard off deliberately). A patch renders at most
three times. These bound re-rendering speed; the dollar caps are unchanged. A Song PR render is an ordinary M3 estimate and
confirmation.

### Voice notes: storage and backup

A voice note is recorded in the browser and saved only on Save (at most 120 s and 4 MiB). The original upload is stored
content-addressed and read-only under `<data>/voice/`; a 64 kbps mono MP3 rendition is the only thing served. The backup treats
`voice/` as a media root: the upload is backed up as an artifact and the MP3 as a rendition, hardlinked or copied like other media.
If the MP3 encoder is missing, saving answers 503 `UNAVAILABLE` for `voice` and stored notes keep playing. There is no physical delete of a voice note through the API, so the directory only grows; watch it with the backup size.

### After the first deploy

Rewind (backward seek) listening events are stored only from this version on; earlier ones were dropped, so the heat strip of old
listening has no rewind marks.

## Migrations 0013 to 0016

Applied in order at startup (the loader refuses gaps; numbers follow merge order, not plan order):

| Migration | Milestone | What it does |
|---|---|---|
| `0013_close_loop` | M5 | additive: `patches`, `patch_comments`, `callbacks`, `callback_verdicts`, `voice_notes`, comment voice and origin columns, section-duel columns, `jobs.patch_id` (frozen by its own trigger) |
| `0014_transforms` | T1 | additive: `transforms` (state machine, no deletes), `transform_inputs`, `transform_id` on tracks and mutations, engine `dsp` shape triggers |
| `0015_engines` | E2 | rebuilds `jobs` (foreign keys off for the rebuild, generated DDL), adds `engine_rtf` and the append-only `engine_adoption_log` with its view |
| `0016_billing_backend` | E2 | adds the backend column (default `runpod-serverless`) to billing windows, buckets, the worker meter and the ledger |

Before deploying this set to a production database, run the migrations on a **copy** (`VACUUM INTO` a private path, never in the
repo tree) and check: `PRAGMA foreign_key_check` is empty, row counts of the rebuilt and altered tables are equal before and
after, `metamusic library verify` is clean, and the YuE2 path still generates. Delete the copy afterwards. Downgrading is a
restore from a snapshot taken before the deploy; a restored database is always disarmed (see "Restore rule").
