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
