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

- Scheduled: daily at `MM_BACKUP_AT` (`HH:MM`, process time zone, not `MM_TZ`; `off` disables the schedule).
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

## Users and roles (U1)

Roles and the visibility rules are in `docs/architecture.md` ("Users and roles"). Operations:

### Bootstrap: `MM_USERS`

`MM_USERS` is a comma list of `name:role` pairs, for example `alice:owner,bob:editor,carol:listener`
(`mm.example.com` in the examples). Rules, enforced at startup and by `metamusic config check`:

- exactly one `owner`; a bare name means owner; roles are `owner`, `editor`, `listener`;
- names are folded to lowercase and must be unique after folding; no commas, colons or whitespace; the
  names `owner`, `critic`, `system`, `user` and `human` are reserved;
- `MM_AUTH=local` is Owner-only (a dev mode): a non-owner user is refused there;
- `MM_TRUSTED_USERS` is still accepted as a deprecated alias of `MM_USERS=<name>:owner`: it must hold
  exactly one name, setting both variables is refused, and `config check` warns when the alias is used;
- a proxy user that is not in the map gets 401.

Always run the check against the exact environment you are about to deploy, before any restart:

```
metamusic config check
```

It validates the whole configuration without opening the database, prints `users: alice:owner,...`
(names and roles only) and exits non-zero with the offending entry on an error. A deploy with a
malformed environment caused a long outage once: never skip it and never deploy unattended.

### Restart to revoke

The map is read once at startup; there is no reload. To remove a user or change a role, edit
`MM_USERS`, run `metamusic config check`, restart attended, and confirm the Owner can still sign in.
A removed user gets 401 on the next request after the restart, and their paired phones and Subsonic
clients stop with them (a device of a demoted user is clamped to the new role). The Owner can also
revoke any device or Subsonic client at once, without a restart, on `/human/devices` and
`/human/subsonic`. A removed user's ratings stay as inert rows: they are never a reason to refuse
to start, the Owner reads them under the raw rater key, and startup and `library verify` print them as orphan-rater
warnings. A nicer "former user" label is deferred (the SPA does not know the configured user list yet).
Renaming a user orphans their old ratings the same way; the new name starts fresh (there is no remap
tool).

### Rollout checklist (first multi-user deploy)

Do this attended, one step at a time; never deploy a migration unattended.

1. Read the current production `MM_TRUSTED_USERS`. It must hold exactly one name (the alias refuses
   several). Note it; it becomes the Owner.
2. Deploy `MM_USERS=<that name>:owner`, identical to today's behaviour, attended, and confirm the
   Owner can sign in.
3. Back up (`MM_BACKUP_DIR` snapshot) and rehearse the migrations on a copy: `VACUUM INTO` a private
   path outside the repo tree, run the new binary against it, then check `PRAGMA quick_check` and
   `PRAGMA foreign_key_check`, rating row counts equal before and after (every earlier rating now has
   rater `owner`, stars and verdict unchanged), picks unchanged and `metamusic library verify`. Delete
   the copy afterwards. The rating migration is not additive (it rebuilds the ratings table); the
   device migration is additive. Downgrading is a restore from the snapshot.
4. In the identity provider create the accounts, a group for the new users and the access-control
   rule (see "Per-role Authelia policy"). A provider user that is not in `MM_USERS` gets 401.
5. Add users to `MM_USERS` one at a time, running `metamusic config check` and restarting attended
   after each.
6. The device migration defaults every existing device and Subsonic client to the Owner. Before any
   non-owner pairs anything, verify that an Owner phone (stream and rate) still works.

### Per-role Authelia policy

The rules example (`deploy/authelia-rules.example.yml`) keeps `^/human/` and `^/oauth/authorize` at
`two_factor` for every user, and it must stay that way: the app does not weaken it per role. Make
the rest of the host available to your users: the domain-wide `one_factor` rule in that file already
covers every Authelia account, so add a group rule only if you want to limit the host to a group.

- Owner and Editor: use two factors, always. An Editor can request spend.
- Listener: one factor is enough for browsing, listening and rating. Pairing a phone and creating a
  Subsonic client live under `/human/`, so the edge then asks for two factors; a Listener who wants a
  device enrols a second factor first.
- Hosted connectors stay Owner-only (`metamusic_owner` policy in `deploy/authelia-oidc.example.yml`).
  That policy matches the literal subject `user:owner`; set it to the Authelia username of the user
  that `MM_USERS` maps to `owner` (for example `user:alice`), or the Owner gets no connector access.

### Devices: ownership and revocation

Each user pairs their own phone or browser and creates their own Subsonic clients on the My devices
page (`/human/devices`, `/human/subsonic`); the device carries the user's role and never more.

- An Editor or Listener is limited to 5 active devices and 5 active Subsonic clients; a sixth answers
  `422 DEVICE_LIMIT` and the user revokes one first. The Owner is unlimited.
- A user lists and revokes only their own. The Owner lists all (with an owner column) and revokes any:
  open the page, find the device by owner and label, revoke; it stops on its next request.
- A lost phone: the user revokes it themselves, or the Owner does. Removing the user from `MM_USERS`
  and restarting cuts all of their devices.
- The offline set stays a single Owner set; a non-owner phone streams but has no offline set.

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
as the hint. The canary is itself a set of generations, so it meets the same gate: adopt the engine right before the canary,
with a reason that names the approved canary, and `reject` it if the canary fails. Generate with it for real only after the
canary passed and you listened to a take. The estimate also needs an RTF for every GPU class the endpoint's pools can serve
(`engine_rtf` row, else the committed seed in `internal/spend/engine_rtf.seed.json`); a class with neither answers
`503 GENERATION_DISABLED` reason `no_rtf`.

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
   `extract`, `base-long`, `lrc` and `cover` (a post-E2 follow-up, see below). `--source-track ID` reuses a kept 30 s track for the edit cases.
3. `init-fail` needs the endpoint provisioned with `provision --engine acestep15 --activate --max 1 --init-fail`, then the canary
   with `--init-fail-confirm`; restore afterwards with a normal `provision --engine acestep15 --deactivate`.
4. A passing run prints a proposed `features.verified.json` and RTF patch; the files are **not** touched. `--measured-out FILE`
   merges the measured numbers (peak VRAM per component, LM memory utilisation, phase seconds, read from the job's
   `timings.extra`) into `engine-canary-measured.json` for you to review and commit. A capability flag is merged into
   `features.verified.json` only for a case that passed; the rest stay off and the UI says "not yet verified".
5. Exit codes: 0 pass, 1 fail, 2 usage, 3 refused (cost or disarmed), 4 aborted.
6. The server refuses a capability that `features.verified.json` does not list (422 `ENGINE_CAPABILITY`), so the `reference`, `repaint`,
   `extend`, `complete`, `extract` and `lrc` cases need the canary-only switch. For the canary window only, set
   `MM_ACESTEP_CANARY_CAPS=reference,repaint` (names from that list, which also has `cover`; `complete`/`extract` also need `MM_ACESTEP_TASKS=turbo,base`)
   in the server's env and redeploy env-only with `DEPLOY_SKIP_BUILD=1` (no image build). `config check` prints
   `acestep canary caps: ...` and the server logs one WARN at startup.
7. Only requests whose id starts `mm-canary-<YYYYMMDD>-` get the capability; everything else, `list_engines` and the SPA still show it
   unverified. Each take that used one logs `canary_cap` and records `canary_caps` in the job's mutation. Right after the last case,
   remove the variable and redeploy with `DEPLOY_SKIP_BUILD=1` again; then commit the proposed `features.verified.json`.

**Cover (restyle).** The `cover` case verifies the audio-to-audio restyle of a whole take (docs/plans/2026-11-18-ace-cover.md). It
needs a worker image that contains the cover handler: merge, let CI build the image, commit its digest in
`deploy/worker-acestep-image.digest` and redeploy before running it. Run it with `MM_ACESTEP_CANARY_CAPS=cover` (turbo class: no
`MM_ACESTEP_TASKS=turbo,base` needed) and `metamusic runpod canary --engine acestep15 --max-usd N --case cover`. It renders the
case's own 30 s source, then restyles it at strength 0.5 and 1.0 (two renders after the source; the dry run prices them). The
case passes when both jobs complete with verified artifacts and the exact source length (0.1 s); the report records whether the
strength changed the audio (`cover_strength_moves_output`, informational). Then listen: the structure and melody of the source
must survive at strength 1.0, and a lower strength must move toward the new caption; a cross-engine source (a YuE2 take) is the
usual input and needs a listening review too. Only after that, flip the flag in a **separate PR**: add `"cover"` to `tasks` in
`internal/engine/acestep/features.verified.json` with the canary commit (a test pins that the file does not list it until then),
and write the turbo `engine_rtf` row only if you want it (cover never teaches the RTF). Restyling someone else's recording is the
operator's call; the server only checks that the parent take has a master.

**Status (2026-11-17):** E2-X4 ran against production and its result is committed: `t2m`, `t2m-long`, `reference`, `repaint`,
`terminate`, `kill-switch`, `init-fail`, `first-pull`, `extend`, `complete`, `extract`, `lrc` and `base-long` passed; `timeout`
was not run (with a lowered `MM_ACESTEP_MAX_RUNTIME` the estimate refuses any item whose unclamped timeout exceeds it, D-E2-17b,
so the server never submits a job that can time out; a real test needs a deliberately slow worker mode, i.e. an image change).
Every run landed on one GPU class (`PRO 6000 MIG 24GB`), so the pool `max_s` of 480 is verified on that class only.
`reference_reproducible` and `seed_reproducible` were recorded as false. The base-class `engine_rtf` row is still to be written
by the operator (`canary:<id>`); until it exists the estimate uses the base seed.

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

### Score renders: fluidsynth and the soundfont (T1b)

The `score_render` transform kind synthesises an ABC score on the application host with fluidsynth (a separate process; LGPL, not
linked) and a General MIDI soundfont. The app image carries both: the Alpine `fluidsynth` package and `FluidR3_GM.sf2` (MIT, about
148 MB, fetched at build time by exact URL and sha256 from the Debian snapshot archive, never committed) at
`/opt/mm/soundfonts/FluidR3_GM.sf2` with its licence beside it. **The image sets no `MM_SOUNDFONTS`**: enable the kind by adding
`MM_SOUNDFONTS=gm=/opt/mm/soundfonts/FluidR3_GM.sf2` to the deployment environment (up to 8 `name=path` entries, separated by `;`).
`metamusic config check` prints `soundfonts: gm <sha256[:12]> <bytes>` and `fluidsynth: <version>`; without either, the kind is
simply unavailable (a configuration state, not an error).

Development: `scripts/tools.sh soundfont` (opt-in, never in CI) installs `./bin/soundfonts/FluidR3_GM.sf2` (0444) with its licence,
sha256-verified (it needs `ar` and `xz`), and `make dev` then sets `MM_SOUNDFONTS` for you; install fluidsynth with your package
manager (`MM_FLUIDSYNTH` overrides the binary). Changing the soundfont or the fluidsynth version changes the rendered PCM, so
`transform verify` reports older score renders as `inconclusive`, not as corruption. The pins live in `scripts/tools.sh` and the
`Dockerfile` and must stay equal (`TestDockerfile_SoundfontPinned`); bumping the soundfont means new pins, a new licence check and a
new `THIRD_PARTY_NOTICES.md` entry.

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

## Worker waves, the capability gate and the time zone (G1)

### Time zone: `MM_TZ`

`MM_TZ` is an IANA zone name (for example `Europe/Rome`) in which the daily schedules of later tracks (Morning Crate, Night Shift) read their
wall-clock times. Empty means the process zone. An unknown name, and the literal `Local`, stop startup (the error does not echo the value).
The binary embeds the zone database, so a minimal container image needs no `tzdata`. `MM_BACKUP_AT` keeps the process zone. Run
`metamusic config check` before a deploy: it prints `time zone: <name>` (`Local` for the process zone). The scheduler (`internal/schedule`) can catch up a missed slot of the same
day only when a track sets `CatchUp` and supplies `Done`; no track does yet, and each track enforces one run per local day itself.

### The capability gate: `metamusic runpod contract`

A job that needs `mm.worker/2` features (kind, Tier B, plan, attachments) can only be estimated, created or claimed when its needs are a
subset of the capabilities **the operator has opened** for that backend **and** for the exact image digest the backend's fresh quote reports.
A new image digest therefore starts closed. Otherwise a job is refused with 503 `GENERATION_DISABLED` (reason `worker_outdated`, the missing
capabilities in `details`) before anything is reserved. Plain jobs (no needs) never consult the gate and keep running on `mm.worker/1`.

- `metamusic runpod contract status [--backend B] [--json]` shows the open capabilities and the log row behind them
  (`get_provider_status` shows the same per backend). `--backend` defaults to `runpod-serverless`.
- `metamusic runpod contract open --backend B --caps v2,... --reason TEXT` records the capabilities as open for the quote's digest. It
  refuses a stale quote, an unknown capability, a list without `v2` and a missing reason, and adds to what is already open for the same digest.
- `metamusic runpod contract close --backend B [--caps ...] --reason TEXT [--strand-queued]` closes capabilities (all by default). It lists
  the queued jobs that need them and refuses (exit 3) unless `--strand-queued` is given; those jobs then fail `WORKER_OUTDATED` after one
  hour and release their reservations.
- **Opening a capability is an operator action**, taken only after an attended provision of the image and a passing canary. No agent, no
  REST or MCP call and no SPA page can open one. Every change is a log row (`worker_contract_log`, append-only) and an audit row.
- The server **closes** capabilities by itself on evidence: a worker answer `CONTRACT_VERSION` or `BAD_INPUT` on a `/2` job closes all of
  them (the job ends `WORKER_CONTRACT`), `KIND_UNAVAILABLE` removes the missing ones (the job ends `WORKER_OUTDATED`), each with a critical
  notification naming the backend and the capabilities. After an auto-close, find the cause (usually a wrong image) before reopening.

### Worker wave checklist

A worker wave is one new worker image revision, built from the worker lanes merged since the last wave. Order is always **server first,
image second, capability third**: a server that decodes strictly must understand the new outputs before any image can emit them.

1. **Server deploy, attended.** A deploy that carries a migration (a table rebuild such as `0020_job_kinds`, or an additive one such as
   `0022_worker_contract`) is never unattended. Take a backup first, then: `metamusic runpod disarm --reason TEXT`; drain (wait until
   `scripts/deploy-preflight.sh` and `metamusic runpod status` show no live attempt); deploy; check the startup log and
   `metamusic library verify`; then arm again with `metamusic runpod arm --endpoint-name <name> --reason TEXT`.
2. **Image.** CI (`worker-image.yml`) builds and smokes the image from `main` (the smoke asserts that the `io.metamusic.caps` label equals
   the `MODE=selftest` output) and publishes it. The operator commits the digest to `deploy/worker-image.digest` in a one-line PR.
3. **Provision, attended.** Disarm and drain, then `metamusic runpod provision --image <ref@sha256:...> --dry-run`, review, provision from an
   admin key file (deleted afterwards, see "Order of a first activation"), arm. A new digest means the gate is closed: nothing `/2` is
   estimable yet. A long first start on a larger image is not a hang; check `status` and the worker meter before assuming a failure.
4. **Regression canary.** `make canary` prints the command; case `main` (a plain `/1` render) under its per-case ceiling.
5. **Open and canary the wave's capabilities.** Run `metamusic runpod contract open --caps v2` (and the wave's other capabilities) right
   before the wave's canary cases, with a reason naming the approved canary, then each case (`v2-wire` for `v2`; tracks add `tier-b`,
   `analyze`, `plan`, ...). On any failure run `contract close` for the capabilities that case covers. Only after all pass do agents and the
   SPA see the features as estimable.
6. **Canaries spend real money and need the operator's written approval** (case list and `--max-usd` per case). They are never run
   unattended, by an agent, or in CI.

Rollback: `contract close` stops new `/2` work at once; to return to the previous image, provision the previous digest (the gate closes by
itself on the digest change). Plain jobs are unaffected throughout.

### Wave W2 (M5b Ears): canary cases `tier-b` and `analyze`

Wave W2 ships the listening check: the image carries capabilities `v2 ears analyze`. Order (C341.2, the checklist above): server release with
every server lane of the track deployed, CI image, digest PR, attended provision, regression canary `main`, then the two Ears cases.

**These canaries spend real money and need the operator's written approval**: the case list and `--max-usd` per case, in writing, before the
first command. They are never run by an agent on its own, unattended or in CI. Print the worst case first, it spends nothing and needs no
token: `metamusic runpod canary --case tier-b --dry-run` (the same text for `analyze`). The figures are illustrative; the real estimate is
compared with `--max-usd` before any spend.

1. `metamusic runpod canary --url URL --case main --max-usd N` (plain `/1` regression).
2. Open the capabilities **only right before the approved canary**: `metamusic runpod contract open --caps v2,ears,analyze --reason "canary W2: <approval ref>"` (add `--backend B` unless it is the default `runpod-serverless`).
3. `metamusic runpod canary --url URL --case tier-b --max-usd N`: a render with synthetic sung lyrics, a score and `options.tier_b: true`.
   Passes when the take completes, the stored document has status `ok` and three `ok` stages, `get_track_tier_b` answers, measured sections exist
   (or the flag is `alignment_failed`), and the report's `measured` carries `ears_seconds`, `ears_timings`, `peak_vram_mib` and the suggested
   `ears_load_s` and `ears_rtf_milli`.
4. `metamusic runpod canary --url URL --case analyze --max-usd N [--source-track ID]`: an analyze item for a take. Without `--source-track` it
   first renders one `main` take (its checks are listed as `source.*`), and `--max-usd` then bounds the **sum** of both estimate ceilings, so
   approve the sum; the report's `measured` names the source batch (`source_batch_id`) and `ceiling_micro_usd` is the sum. The source take is discarded on every exit unless `--keep`. With `--source-track` the operator's take is analyzed and never discarded.
5. **On any failure** (exit other than 0, a trip, a closed capability in `get_provider_status`): `metamusic runpod contract close --caps ears,analyze --reason TEXT`
   (add `--backend B` unless it is the default `runpod-serverless`, and `--strand-queued` when Ears jobs are queued: close refuses with exit 3 otherwise; add `v2` only when the plain `/2` wire is in doubt), find the cause, and do not reopen until a case passes again.
6. After both pass, the measured seconds calibrate the quote: set `MM_EARS_LOAD_S` and `MM_EARS_RTF_MILLI` (attended env change, then
   `metamusic config check`), see "Listening checks: the quote".

Each case refuses before any spend (exit 3) when the provider status does not list the capabilities it needs (`v2` and `ears`; `analyze` also
needs `analyze`), exactly like `v2-wire`.

### Rebuild deploy (`0020_job_kinds`)

The `jobs` table was rebuilt once for the whole program (new columns `kind`, `needs_json`, `spec_json`). As for `0015`, run it first on a
**copy**: `VACUUM INTO` a private path outside the repo, start the new binary against it, check `PRAGMA foreign_key_check` is empty, row counts
of `jobs`, `job_attempts` and `job_events` are equal before and after, `metamusic library verify` is clean; delete the copy. Existing jobs get
kind `generate`, needs `[]`, spec `{}`. Then deploy attended as in step 1 above. Downgrading is a restore from the pre-deploy snapshot.

## Bulk pods: configuration and the pod key (BULK)

The pod backend is off until the operator turns it on (`MM_POD_BACKEND=1`). These rows are the whole configuration; every value is checked at
startup and by `metamusic config check`, which prints `pod backend: on|off`, `pod image digest: <12 hex>`, `pod key: present|absent` and
`upload fallback: configured|not configured`, never a value.

| Variable | Default | Meaning |
|---|---|---|
| `MM_POD_BACKEND` | `0` | build the backend (needs `MM_POD_IMAGE`, `MM_POD_GPU_TYPES`) |
| `MM_POD_IMAGE` | none | `<ref>@sha256:<64 hex>`, the yue2 digest the serverless endpoint runs |
| `MM_POD_GPU_TYPES` | none | ordered catalog GPU type ids, 1..4 (the cheapest 24 GB type; an extra type makes the quote dearer) |
| `MM_POD_MIN_TAKES` / `MM_POD_MAX_TAKES` | 30 (10..100) / 100 (30..100) | batch size bounds |
| `MM_POD_BOOT_S` | 1200 (300..3600) | boot allowance; `MM_JOB_TTL` must be at least this plus 10 minutes |
| `MM_POD_IDLE_S` | 120 (30..600) | idle seconds before the pod is deleted |
| `MM_POD_SLACK_S` | 300 (60..1800) | reserved slack |
| `MM_POD_MAX_RUNTIME` | `6h` (`1h..12h`) | absolute deadline from the first create |
| `MM_POD_DEADMAN` | `10m` (`2m..30m`) | failed-poll time after which the pod ends itself |
| `MM_POD_ORPHAN_AFTER` | `180s` (`60s..900s`) | silence after which the server deletes the pod |
| `MM_POD_WINDOW_MAX` | `8h` (`1h..24h`) | bulk window length (also 30 minutes idle, fixed) |
| `MM_POD_DISK_GB` | 40 (20..200) | container disk |
| `MM_FALLBACK_AFTER_S` | 120 (60..600) | worker switch-over to the upload fallback |

**The pod key** is the file `runpod-pod.env` in the secrets directory with exactly one variable, `RUNPOD_POD_KEY`, a RunPod key restricted to pods
(read and write), catalog read and billing read. Mode 0600 or 0400 (any group or other bit is refused), read at every call and never cached, so
deleting the file closes the backend at once. Any other variable, and an all-access key, is refused. Keep it on the server during a bulk window only.
`RUNPOD_POD_KEY` and the six `R2_` variables (`R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_REGION`, `R2_PREFIX`) in the process environment stop startup: secrets are files, not environment.

**Windows and deploys (BULK-P5).** A bulk window starts when the operator arms `runpod-pod` (the pod key file in place, a probe row `ok`) and ends
by itself: the server disarms the backend (`changed_by system:bulk`, reason `window_ended`) `MM_POD_WINDOW_MAX` after arming and stops any live pod
then, or disarms with `window_idle` after 30 minutes with no pod batch active or awaiting confirmation and no live pod. Pods are created only inside
the window, one at a time. After the window delete the key file. `scripts/deploy-preflight.sh` refuses while `metamusic runpod status --json`
shows `live_pods` above zero: disarm, wait until it reads 0, deploy, arm (`--force` overrides, as for live attempts).

## Listening checks: the quote (M5b Ears)

A new yue2 render with at least one lyric line gets a listening check (Tier B: vocal separation, speech recognition and, for a take
with a plan score, a pitch contour) by default. The check runs on the GPU worker after the render, so the estimate prices it on top:
the take's timeout grows by the check's budget and the reservation covers that timeout, so the ceiling of a typical take grows by
about a fifth. Caps are unchanged; fewer takes fit a batch.

| Variable | Default | Meaning |
|---|---|---|
| `MM_EARS_LOAD_S` | 40 (0..600) | model load seconds |
| `MM_EARS_RTF_MILLI` | 150 (10..2000) | separation plus recognition seconds per 1000 s of audio |
| `MM_EARS_TRANSCRIBE_RTF_MILLI` | 40 (0..2000) | the pitch contour, only when the take has a plan score |
| `MM_ANALYZE_FETCH_S` | 20 (5..300) | fetch seconds of an on-demand analyze job |

The defaults are placeholders; the wave W2 canary report calibrates them (an attended env change, checked with `metamusic config check`).
`ears_s = load + ceil(rtf x audio / 1000)` (plus the pitch term), the budget is `ceil(3 x ears_s / 2)`, the timeout is the plain timeout
plus the budget; the render's own window on the worker is unchanged.

The option `options.tier_b` of `estimate_generation` is tri-state. Absent (auto): on when the take has lyric lines, its check fits
`MM_JOB_MAX_RUNTIME` and the routed backend has the `v2` and `ears` capabilities open; otherwise off, with a warning on the take
(`tier_b_unavailable`, `tier_b_no_lyrics`, `tier_b_exceeds_runtime`, `tier_b_engine`). `true` requires the check: a closed capability is
503 `GENERATION_DISABLED` (`worker_outdated`), a take that does not fit is 422 `INVALID_ITEM` rule `tier_b_exceeds_runtime`, another
engine is 422 `VALIDATION_FAILED`. `false` skips it. Canary renders (`mm-canary-` request ids) stay plain unless `true`.

Until the operator has opened the capabilities (`metamusic runpod contract open --caps v2,ears` after a passing canary) every
estimate on the real backend runs auto-off with `tier_b_unavailable`, quotes and creates exactly as before, and an explicit `true`
is refused. `runpod contract close --caps ears` stops the default without a deploy.

### Analyzing chosen takes

`estimate_generation` item `{mode: "analyze", track_ids: [...]}` (1 to 50 takes, an estimate of analyze items alone) listens to takes that
already exist: one job of kind `analyze` per take, which attaches the document to that take (artifact `analysis.json`, the Tier B rows,
measured sections) and creates no track and no mutation. It is an ordinary spend: the estimate prices the input fetch
(`MM_ANALYZE_FETCH_S`) plus the check in integer micro-USD, the Owner approves it like a render (an Editor's batch always awaits the
Owner), and it needs the `analyze`, `ears` and `v2` capabilities, so with the capabilities closed it is 503 `GENERATION_DISABLED`
(`worker_outdated`) before anything is reserved. Nothing creates analyze work on its own: the library is never backfilled. A take is
analysed once (`already_analyzed`), at most 600 s long (`audio_too_long`), and only one analyze job per take runs at a time
(409 `ANALYSIS_IN_PROGRESS`). A reference or a lossy master is allowed (warnings `lossy_master`, `no_lyrics`).

### Production status: listening checks are off

**Listening checks are off in production** until worker wave W2 (EARS-X3) opens the `v2`, `ears` and `analyze` capabilities, which needs an
attended provision of the new image and a canary that carries the operator's written spend approval ("Wave W2" above). Until then:
every estimate of a lyric render carries the warning `tier_b_unavailable` and is quoted and created as before, an explicit
`options.tier_b: true` and every analyze item are refused 503 `GENERATION_DISABLED` (`worker_outdated`), and no Tier B document exists.
**Analyze is an ordinary approved spend** (priced in the estimate, approved by the Owner like a render, capped like any other job).
**Recover never spends**: it only reads a stored document and writes a draft, so it works on any document already attached to a track, but a
reference track gets a document only through an analyze job, i.e. after W2. Nothing in the server starts GPU work for the check on its own.

### Troubleshooting a listening check

Start with `get_track_tier_b` (or the panel on the Track page) and `get_provider_status` (`worker.caps` per backend).

| Symptom | Meaning and action |
|---|---|
| Estimate warning `tier_b_unavailable` | The routed backend has not got `v2` and `ears` open for the current image digest. Expected until W2; after W2 check `metamusic runpod contract status`. A new image digest starts closed. |
| 503 `GENERATION_DISABLED` `worker_outdated` | Same cause, for an explicit `true` or an analyze item; the missing capabilities are in `details`. Only the operator CLI opens them. |
| Warning `tier_b_exceeds_runtime` | The check would pass `MM_JOB_MAX_RUNTIME`; the render goes without it (auto) or is refused 422 (`true`). Raise the cap or shorten the take. |
| `tier_b_no_lyrics`, `tier_b_engine` | No lyric lines, or an ACE-Step batch: no check by design. |
| `get_track_tier_b` 409 `ANALYSIS_UNAVAILABLE`, state `pending` | A document is stored or an analyze job is live and the rows are not computed yet; the sweep runs every 30 minutes and an ingest computes at once. |
| State `missing` | A completed render needed a check but no document arrived. Check the job events; the stage fails closed with a `status: error` document, so a missing one means the worker image lacks the stage. |
| State `error` or `skipped` | The document says the recogniser did not run (`code` names why: `OOM`, `DEADLINE`, `MODELS_MISSING`, `SUBPROCESS_FAILED`, `INPUT_DECODE`). The flag `listening_check_failed` shows it. A stored error document is terminal, an analyze item answers `already_analyzed`; make a new take to listen again. |
| `analysis_duplicate` job event | An analyze job returned a second document for a take that already had one; it was stored as the next `analysis-N.json` and not computed again. |
| Flag `alignment_failed` | The heard words did not match the lyric (under 40 % aligned): no measured sections, lines of basis `asr` have no times. Usually a wrong language or an instrumental take. |
| A worker answer `KIND_UNAVAILABLE`, `BAD_INPUT` or `CONTRACT_VERSION` | The gate closed itself with a critical notification. Find the cause (usually a wrong image) before `contract open`. |
| Stuck queued analyze or check jobs after `contract close` | They fail `WORKER_OUTDATED` after one hour and release their reservations. |

The listening-check rows are recomputable: after a definition change the sweep recomputes them from the stored artifact, and a document that
no longer matches its stored sha256 is `ErrBadTierBDocument` (nothing is written). `metamusic library verify` must stay clean.

### Recover: a draft from a reference's transcript

Use it when a reference track (imported without a recipe) has a stored, `ok` listening check and you want its words as a starting draft.

1. Estimate and approve an analyze item for the reference (`estimate_generation` with `{mode: "analyze", track_ids: [id]}`; see "Analyzing
   chosen takes"). That is the only spend; skip it when the track already has a document.
2. Call `recover_recipe` for the track (Track page: "Create draft from transcript"; REST and MCP: `recover_recipe`, with an `Idempotency-Key`),
   optionally with a style line. It answers 201 with the draft, the cleaned transcript summary, the Tier A evidence it used and where the
   style came from. No job, estimate or ledger row is created.
3. Open the draft (Backlog, origin `recovered`), correct the lyrics (the recogniser's text is a transcription, often wrong) and the
   placeholder style (for example `italian vocals, 104 BPM, A minor`), and run the lint.
4. Optionally render the draft through the ordinary estimate. The take is a new root with operator `recover`; a mother override is refused
   (422 `INVALID_ITEM`, rule `recovered_draft_parent`). The reference is never modified.

Refusals: 409 `ANALYSIS_UNAVAILABLE` (no `ok` check yet; the hint says what to do), 422 `TRANSCRIPT_EMPTY` (no sung line was heard), 422
`RECIPE_KNOWN` (the track has a recipe; Recover is for references only), 422 `LINT_FAILED` (nothing is created).

### Diction Lab

The Diction Lab reads the stored checks; it never spends. The karaoke view on a Track page (a take with a stored `ok` check, or a plan score)
lets the Owner mark each line `clear` or `unclear` (a second press withdraws the mark); `/diction` lists the words the recogniser mangled
per language with their rates, and the Owner (or an agent with write scope) can ignore a word or add a note; an Editor or Listener cannot. An agent's marks are stored as `critic` and do not count toward the calibration set. Discarded takes are not in the lexicon. Marked lines are the calibration set of the Critic Bench
(target: 60 lines across 6 takes before the thresholds are tuned; a numbers-only change to `DefaultThresholds`). A risky word of the lexicon
appears as a warning in the draft lint and the estimate, never as an error.

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

Later migrations: `0020_job_kinds` (G1, the one `jobs` rebuild), `0021_diction` (the Diction Lab tables) and `0022_worker_contract` (G1, additive:
the append-only `worker_contract_log` and its view). See "Worker waves, the capability gate and the time zone (G1)".
