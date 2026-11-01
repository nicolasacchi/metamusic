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
