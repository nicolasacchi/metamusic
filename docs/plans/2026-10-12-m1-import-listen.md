# M1 — Import & Listen — Implementation Plan (DRAFT, revision 3)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md` and `docs/plans/2026-10-12-m0-skeleton.md` (contracts C1–C9 incl.
> the C3, C5 and C6 addenda) first; M1 builds on those exact contracts and continues the numbering at C10.
> Where M1 changes an M0 contract (C1 Config, C2 Store, C3/C6 problem writers, C4 `api.Deps`, C6
> `httpserver.Options`, C9 loop target allowlist, the M0 W1 session-loss middleware, the M0 I1 `make check`
> command), the task that makes the change also records it in the M0 document in the same commit (M1-B1,
> M1-B2, M1-B4, M1-W1, M1-I1, M1-I2).
>
> **Revision 3** reconciles every M0 reference with M0's final code (branch `m0`, which `m1` starts
> from): CLI `run` signature and exit-code mapping, session cookie names, `httpserver.Options`, the MCP
> parity/golden harness, the CI workflow split (`ci.yml` + `gate.yml`), `scripts/e2e.sh`, the Playwright
> run-scoped teardown, the metrics wrapper's three allowlists and the M0 web shell.
>
> **Public/private split.** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` (just before Appendix P) is public-safe (no real hostnames, IP addresses,
> titles, artists, lyrics, request-id prefixes, counts from the real archive or personal data;
> `mm.example.com` and synthetic examples only; real id shapes appear only as `<prefix>` placeholders).
> Only that part is copied to `docs/plans/<date>-m1-import-listen.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`); the copy is `git add`ed and checked
> with `scripts/public-gate.sh --staged` (the check the pre-commit hook runs; `make gate` scans only
> files already in the index, so an un-added copy would be skipped) before it is committed. The
> appendix (observed archive facts, M1-X2 expected counts) is never copied.
>
> **Task ids** carry the `M1-` prefix (M1-B1, M1-W1, M1-I1, M1-X1 …) so they never collide with the M0
> task names or with `MM_LANE` metrics labels. Lane labels: `m1-found`, `m1-media`, `m1-import`,
> `m1-web`, `m1-infra`, `m1-int`.

**Goal:** the operator drops the existing YuE2 archive into the inbox; `metamusic import --dry-run`
prints and saves an inventory with a plan digest; `metamusic import --plan <report>` applies exactly
that reviewed plan: every take becomes a frozen, analysed, playable track and every recipe without a
take a visible backlog draft. The library (FTS + filters), track detail (lyrics sections, timed only
from `score.abc`), backlog and a persistent PWA player (canvas waveform, signed Range media, Media
Session, session-loss handling that never interrupts playback, user-gesture device FLAC probe) work on
desktop and phone, and agents see the same data through the registry (REST + MCP).

**Architecture:** `cmd/metamusic import` → `internal/importer` (inventory: scan → classify → parse →
match → plan, reading the inbox only through `internal/importer/inbox.ReadOnly`; apply: per-take
`internal/media/ingest` pipeline + one DB transaction per take via `internal/library`). Ingest =
copy into a work dir → `flac --test` → Go FLAC metadata (`internal/media/flacmeta`) →
`internal/analysis` (ffmpeg `ebur128` loudness; ffmpeg decode pipe → Go peaks + Go spectrum
bandwidth) → one `metaflac` write → invariant check → 0444 → atomic name claim (`Root.Link`) → MP3 320
rendition. External tools run only through `internal/mediatool` (timeouts, output caps, work-dir-only
argv). Reads go through `internal/library` and new Huma ops; `/media/{sha}.{flac|mp3}` is a
non-registry signed Range handler (`internal/media/serve`). Every hand-written problem response goes
through the leaf `internal/problem`. The Svelte shell gains Library / Track / Backlog / Import /
Settings routes and a real player. `internal/synth` generates every test/E2E/dev fixture at runtime.

**Tech stack:** one new Go module dependency, `golang.org/x/text` (NFKD folding for `Slug`; D-M1-3).
FFT, FLAC metadata, peaks and ABC parsing are stdlib Go. Every "UUIDv7" below is the Go 1.27 stdlib
`uuid.NewV7().String()`, as M0's `internal/auth` already uses (no `github.com/google/uuid` import).
Runtime tools: `ffmpeg` (filter `ebur128`,
encoder `libmp3lame`), `flac`, `metaflac` — already in the M0 runtime image; pinned copies in CI (C23).
Web: no new runtime dependencies (own canvas waveform instead of wavesurfer.js: we need draw +
click-to-seek only, and must not touch the shared `<audio>` element). Pin versions at implementation
time (context7 for Vite/Svelte/Playwright).

**Out of scope (YAGNI, with owner milestone):** ratings, status edits, tags, comments, title edits,
JSONL export (it will choose among the `recipe_sources` rows M1 records), playlists, listen events,
device pairing (M2a); duels, clips, spectrogram, clipping / silence / token-loop flags (M2b); jobs,
generation, costs, env fingerprints, lineage tree UI, sampling parameters in recipes (M3); service
worker / offline, cover art, Subsonic (M4a); `listen.flac` (decided by D11 from the loudness-spread
measurement, built later if spread > 2 LU); API-triggered import (`import_library` needs the M3 job
runner — M1 import is an operator CLI; its reports are readable over the API); `purge` (no-delete
triggers are unconditional in M1); importing masters that have no recipe yet (D-M1-2); `latent.npy`
(D-M1-1).

---

## Decisions recorded by this plan

These amend or refine the design of record; M1-X1 copies them into the private design document.

| Id | Decision | Why |
|---|---|---|
| D-M1-1 | YuE2 `latent.npy` is never imported (reported as `ignored:latent`). The other `.npy` artifacts of a result dir (`abc_tokens.npy`, `prefix.npy`, `semantic.npy`) are kept under `takes/<id>/`. Design §3.5's `takes/<id>/*.npy` is amended to name those three. | Design §3.3 already excludes `latent.npy` from job uploads (YAGNI); no planned feature reads it (token-loop analysis uses `semantic.npy`); import and job takes then carry the same artifact set. |
| D-M1-2 | Masters without a matching recipe are **not** imported in M1. They stay in the inbox, are listed as `unmatched` with candidates in every report, and are imported by a later run once their recipe arrives (or a mapping row names it). `tracks.recipe_id` stays `NOT NULL`. | Masters are frozen at ingest with `MM_RECIPE_ID`/`MM_REQUEST_ID`/`MM_SEED` tags written once; a "recipe pending" take would be frozen without them forever. A nullable set-once `recipe_id` would also need a new trigger class. |
| D-M1-3 | `Slug` uses `golang.org/x/text` (`unicode/norm` NFKD + `runes.Remove(runes.In(unicode.Mn))`) plus a fixed supplementary fold table (`ß→ss æ→ae œ→oe ø→o ł→l đ→d þ→th`). | NFKD is not in the standard library; x/text is maintained by the Go team (like the x/crypto already used) and handles arbitrary input correctly; a hand table alone would miss composed forms. |
| D-M1-4 | CI ffmpeg is an LGPL static build re-hosted as a release asset of this repo; CI flac/metaflac come from the Ubuntu 24.04 archive. The design's CI rule (cached static media tools, no apt) is relaxed for `flac` only; M0's CI installs no media tools yet. | Upstream dated build URLs are pruned and GitHub caches expire after 7 days unused, so a cold CI run must not depend on them; the Ubuntu archive is durable and installs `flac` in seconds. LGPL avoids GPL source obligations for bundled components. |
| D-M1-5 | Session loss never reloads the page while audio plays; it shows a "sign in again" banner. Reload happens only when the browser is online, the loss is confirmed (C22.4) and a loop guard allows it. Refines design §5.3 ("full-page reload"). | A network blip on a phone otherwise kills the persistent `<audio>` and can loop. |
| D-M1-6 | The device FLAC probe runs only inside a user gesture; a timeout is *inconclusive* and never downgrades; only `MEDIA_ERR_SRC_NOT_SUPPORTED` or `MEDIA_ERR_DECODE` downgrade to MP3. Refines design §5.2 (which says "on first run"). | iOS does not load media without a user gesture (Apple-documented behaviour; unverified on iOS 26), so a boot-time probe would wrongly mark iPhones as unable to play FLAC. |
| D-M1-7 | Seeds are limited to 0 … 2^53 − 1 although YuE2 accepts < 2^63. | Seeds travel through JSON to JavaScript clients, which cannot represent larger integers exactly. |
| D-M1-8 | `prompt` is validated exactly as YuE2's `request_kwargs` does (must equal the native instruction text, else the line is invalid); `abc_path` is resolved and inlined; `abc_sampling`/`semantic_sampling` are rejected until M3 defines sampling identity. | Silently dropping keys that change the render would make two different recipes look identical. |
| D-M1-9 | Multi-range requests on `/media/` are answered by `http.ServeContent` as `multipart/byteranges`; the `Content-Type: audio/*` invariant covers single-part responses and the parts of a multipart response. | Media elements never send multi-range requests; rejecting a valid request is worse than serving it. |
| D-M1-10 | `Discard` never deletes content-addressed derived files; unreferenced ones are reported as `library_orphans`. | They can be shared between tracks and may have been linked by a concurrent ingest; they are reproducible. |

---

## Input shapes the importer must handle (public-safe; observed facts in Appendix P.1)

- **Recipe JSONL** in `yue2 batch --input` format: one request object per line, keys typically
  `cot,id,lyrics,seed,style` in sorted order, Python-default separators (`", "`, `": "`),
  `ensure_ascii=False`, LF line ends. Lyrics use `[Verse N]`, `[Chorus]`, `[Bridge]`, `[Outro]` headers.
  Backlog ids look like `<prefix>_NNN_vK`: several **independent** variant recipes per source group
  (they share a group key, never a lineage).
- **Subset JSONL** files whose lines are byte-identical copies of lines in another JSONL (a pilot).
- **Collection JSON** `{name, rule, songs:[{n, title, artist, why, variant_ids}]}`; only `name` and
  `variant_ids` are read (C16.9).
- **Reference CSV** with header `variant_id,track_n,source_title,source_artist,variant,variant_type,seed`
  (private reference metadata, C16.9), possibly CRLF and with a UTF-8 BOM.
- **Per-song request JSON** (may add `title`), **YuE2 result dirs** (`audio.flac`, `request.json`
  written by YuE2 itself with explicit `null` defaults, `result.json`, `config.json`, `score.abc`,
  `plan.json`, `plan_manifest.json`, `*.npy`), and `varianti/` request files.
- **Ignorable JSON**: an external reference playlist `{playlist, owner, url, note, tracks:[…]}` and a
  validation report `{pass, files, errors, warnings, …}`.
- **FLAC masters** that may be named by title slug only (no id, no seed), 24-bit / 48 kHz / stereo,
  possibly with zero Vorbis comments, no SEEKTABLE and no PADDING (so the single metaflac write rewrites
  the file — fine, we write a library copy), each possibly with a same-stem MP3 sidecar in another
  directory.
- **In-flight copies**: rsync temp files `.<name>.flac.<6 random chars>`, and — with `--partial` or
  `--inplace` — truncated files under their final names with old mtimes.
- Consequences: the importer must work with zero tracks and hundreds of drafts, re-run cleanly as files
  arrive, match audio ↔ recipe by result dir / tags / ids / slugs with an operator mapping file for
  leftovers, and depend on no real name.

---

## Contracts (all lanes build against these — do not change without updating this file)

### C10. Config, tools, data layout, leaf packages

New env (added to `config.Config`, all optional; M1-B1 amends M0 C1 accordingly):

```go
FFmpeg   string // MM_FFMPEG,   default "ffmpeg"   (resolved via exec.LookPath at use time)
FLAC     string // MM_FLAC,     default "flac"
Metaflac string // MM_METAFLAC, default "metaflac"
Inbox    string // MM_INBOX,    default DataDir + "/inbox"
```
`config.Load` keeps M0's rule that `MM_AUTH` must be `proxy` or `local` (else an error), so every
subcommand that loads config — including `import --dry-run` and `seed-synthetic` — needs `MM_AUTH` set
(the container and the e2e global setup set it; CLI tests pass it in the env map).

Derived key (no new secret file): `MediaKey = HMAC-SHA256(Secrets.Key, "metamusic/media-url/v1")`.

Data layout under `DataDir` (app writes go through one `*os.Root` opened on `DataDir`; the inbox is
reachable only through `inbox.ReadOnly` (C16.0), which wraps a private `*os.Root` on `Inbox` and
exposes no write method):

```
<DataDir>/
  metamusic.db[-wal,-shm]        unchanged from M0
  secrets/                       M0 default MM_SECRETS_DIR (containers: /run/secrets); never read by import
  inbox/                         operator drop zone; the app NEVER writes here (compose mounts it :ro)
  import/                        0700: mapping.csv (operator-edited), mapping.todo.csv, reports/*.json (0600)
  audio/masters/<slug>__<seed>__<yyyymmdd-hhmm>[__N].flac   frozen masters, 0444
  takes/<track_id>/<artifact>    YuE2 result-dir artifacts + score.abc, 0444
  derived/<aa>/<sha>.mp3         MP3 320 rendition, named by ITS OWN sha256, 0444
  derived/<aa>/<sha>.peaks.json  peaks file (format C14), named by its own sha256, 0444
  derived/probe/probe-v1.flac    device-probe file, (re)generated at serve start if missing
  incoming/ingest-<track_id>/    ingest work dir (apply only; removed on success/failure; stale >24h swept at apply start)
  inbox-synthetic/               dev/e2e only (C21)
```
`<aa>` = first two hex chars of the file's sha256. Dirs are 0755 except `import/` (0700) and work dirs
(0700); `DataDir` itself is created 0750 by M0's `boot` (`cmd/metamusic/cmd_serve.go`). Masters,
artifacts and derived files are written once and are 0444 before they get their final
name (C15 step 14).

**Leaf packages** (they import nothing from `internal/` unless stated; they exist to break import
cycles and to keep test code out of the binary):

- `internal/problem` — the one writer of `application/problem+json` outside Huma:
  ```go
  type Body struct {
      Code    string         `json:"code"`
      Message string         `json:"message"`
      Details map[string]any `json:"details"`
  }
  func Write(w http.ResponseWriter, status int, code, msg string, details map[string]any) // nil details ⇒ {}
  ```
  M1-B1 replaces M0's hand-written writers with it: `auth.writeProblem(w, status, code, msg)` (used by
  the C3 middleware and by `LocalAuth.Handler`'s `CROSS_ORIGIN` deny handler), `httpserver.problem`
  (which builds JSON by string concatenation, without escaping), the encoder in `api.notFound` (404 /
  405 `METHOD_NOT_ALLOWED` with `Allow`) and the transport-error rewriter `problemWriter` in
  `internal/mcpgen/problem.go`. Two M0 writers stay: `api.writeProblem(huma.Context, *Problem)` in
  `registry.go` (the op-level auth middleware writes through the Huma context before input is read) and
  `mcpgen.problemResult` (an MCP tool result, not an HTTP body). `api.Problem` keeps its Huma
  integration; `TestProblem_ShapesAgree` asserts that a Huma handler returning
  `api.Err(s, c, m, d)` and `problem.Write(w, s, c, m, d)` produce the same status, the same content type
  and equal decoded bodies (same keys and values). `serve` uses `problem`, so it never imports `api` (`api.Deps` imports `serve`).
- `internal/mediatool` — tool paths and the only way to execute them:
  ```go
  type Tools struct{ FFmpeg, FLAC, Metaflac string } // shared by ingest, analysis and synth
  type RunOpts struct {
      Timeout   time.Duration // required, > 0
      Stdout    io.Writer     // streaming consumer (decode pipe); nil ⇒ captured and returned
      MaxStdout int64         // cap for captured stdout; exceeded ⇒ process killed, ErrOutputLimit
      MaxStderr int           // ring buffer size (default 64 KiB); errors carry its last 2 KiB
  }
  type ToolError struct{ Tool string; ExitCode int; StderrTail string } // never contains argv
  func Run(ctx context.Context, bin string, args []string, o RunOpts) ([]byte, error)
  ```
  `exec.CommandContext` with `ctx` bounded by `Timeout`, `cmd.WaitDelay = 5 * time.Second` (so a child
  that keeps the pipes open cannot hang `Wait`), stdin `/dev/null`, environment `LC_ALL=C` + `PATH`
  only. **Argv rule:** arguments contain only literals and app-created absolute paths under
  `<DataDir>/incoming/`, `<DataDir>/derived/probe/` or `<DataDir>/inbox-synthetic/` (synth), and
  `t.TempDir()` paths in tests — never a string derived from inbox file names, tags, titles or recipe
  text. Every ffmpeg invocation passes `-protocol_whitelist file,pipe` before its first `-i`, prefixes
  every file path (input or output) with `file:`, and uses `-` only for pipes (lavfi sources in `synth`
  are filtergraph literals, not paths). Tags reach `metaflac` only through `tags.txt` (C15 step 7).
- `internal/testtools` (**test-only**; owner M1-B1, no other task edits it): `Tools() mediatool.Tools`
  (lookup: `<module root>/bin/<tool>` first, then `PATH`); `Require(t, names ...string)`: under
  `testing.Short()` it always skips; otherwise it skips when a tool is missing **unless**
  `MM_REQUIRE_MEDIA_TOOLS=1` (set in CI), in which case it fails.
- Test helpers live only in packages named `…test` that are imported only from `_test.go` files:
  `internal/testtools`, `internal/media/flacmeta/flactest` (C13), `internal/library/librarytest` (C17).
  `TestNoTestHelpersInBinary` (M1-B1, `cmd/metamusic`) runs
  `go list -deps -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/metamusic` and fails if any package
  of this module in the list is one of those helpers or imports `testing`. (Stdlib packages are not
  checked: `net/http/httptest`, used by the MCP dispatcher, imports `testing` itself.)
- **Lint:** M0's `.golangci.yml` (golangci-lint v2, run by `make check`) excludes gosec/errcheck only
  for `_test.go` files and keeps other gosec exclusions per path with a one-line justification. M1-B1
  adds the same kind of justified, path-scoped exclusions for gosec G204 in `internal/mediatool` (the
  only `exec` site; argv rule above) and for the non-`_test.go` helper packages (`testtools`,
  `flactest`, `librarytest`); nothing is excluded globally.

### C11. Migration `internal/store/migrations/0002_library.sql` (full DDL) and store additions

After M1, `PRAGMA user_version == 2`. M0's `TestOpenMigratesAndReopens` (`internal/store/store_test.go`,
package `store`) asserts `user_version == 1` twice; both change to the number of embedded migrations
(`loadMigrations()`). M0's runner is `store.Migrate(ctx, db)` (exported, applies every pending file in
its own tx, and already refuses a database newer than the binary with a plain error); it has no partial
mode, so M1-B2 splits out an unexported `migrateTo(ctx, db, n)` that `Migrate` calls with all migrations.
**CHECK constraints only encode invariants that will never change.** Enumerations that will grow —
engines (M6), rendition kinds (spectrogram in M2b, maybe `listen.flac` via D11), analyzers (clipping,
silence, token loops in M2b), analysis tiers, device quality (an Opus tier later), mutation operators,
draft origins, match rules, raw kinds — are validated in Go, so later milestones never rebuild an
immutable table that carries triggers. All timestamps RFC 3339 UTC.

```sql
-- 0002_library.sql — recipes, tracks, drafts, media, analysis, imports, devices, FTS.

CREATE TABLE import_runs (
  id           TEXT PRIMARY KEY,                       -- UUIDv7 (= report_id of the applied report, C16.7)
  started_at   TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,                          -- refreshed every 30 s while running (C16.8)
  finished_at  TEXT,
  state        TEXT NOT NULL CHECK (state IN ('running','completed','failed')),
  principal    TEXT NOT NULL,                          -- 'cli' | 'seed-synthetic'
  inbox_label  TEXT NOT NULL,                          -- 'inbox' | 'synthetic' (never an absolute path)
  plan_digest  TEXT NOT NULL,                          -- C16.7
  summary_json TEXT CHECK (summary_json IS NULL OR json_valid(summary_json)),
  report_json  TEXT CHECK (report_json IS NULL OR json_valid(report_json)),
  error        TEXT
);
CREATE INDEX import_runs_started ON import_runs(started_at);
CREATE TRIGGER import_runs_no_delete BEFORE DELETE ON import_runs
BEGIN SELECT RAISE(ABORT, 'import_runs: append-only'); END;
CREATE TRIGGER import_runs_final BEFORE UPDATE ON import_runs WHEN OLD.state <> 'running'
BEGIN SELECT RAISE(ABORT, 'import_runs: finished runs are immutable'); END;
CREATE TRIGGER import_runs_frozen BEFORE UPDATE OF id, started_at, principal, inbox_label, plan_digest ON import_runs
BEGIN SELECT RAISE(ABORT, 'import_runs: frozen column'); END;

CREATE TABLE recipes (
  id            TEXT PRIMARY KEY,                      -- UUIDv7
  engine        TEXT NOT NULL,                         -- Go-validated; M1: 'yue2'
  request_id    TEXT NOT NULL,                         -- the request "id"
  request_json  TEXT NOT NULL CHECK (json_valid(request_json)),  -- canonical engine-native request (C12)
  style         TEXT NOT NULL,
  lyrics        TEXT NOT NULL,
  seed          INTEGER NOT NULL CHECK (seed BETWEEN 0 AND 9007199254740991),
  cot           TEXT NOT NULL,                         -- Go-validated per engine; yue2: full|melody|off
  sampling_json TEXT CHECK (sampling_json IS NULL OR json_valid(sampling_json)),  -- NULL in M1 (D-M1-8)
  hash          TEXT NOT NULL UNIQUE,                  -- hex sha256 of request_json bytes
  core_hash     TEXT NOT NULL,                         -- hex sha256 of canonical {cot,id,lyrics,seed,style} (C12)
  created_by    TEXT NOT NULL,                         -- 'import' | principal id (M3)
  created_at    TEXT NOT NULL,
  -- every engine's canonical request JSON carries "id" and "seed":
  CHECK (json_extract(request_json, '$.id') IS request_id),
  CHECK (json_extract(request_json, '$.seed') IS seed)
);
CREATE INDEX recipes_request_id ON recipes(request_id);
CREATE INDEX recipes_core_hash  ON recipes(core_hash);
CREATE TRIGGER recipes_immutable BEFORE UPDATE ON recipes
BEGIN SELECT RAISE(ABORT, 'recipes: immutable'); END;
CREATE TRIGGER recipes_no_delete BEFORE DELETE ON recipes
BEGIN SELECT RAISE(ABORT, 'recipes: no physical delete'); END;

CREATE TABLE recipe_sources (                          -- EVERY occurrence of a recipe in the inbox, append-only
  id            INTEGER PRIMARY KEY,
  recipe_id     TEXT NOT NULL REFERENCES recipes(id),
  source_path   TEXT NOT NULL,                         -- inbox-relative path, or 'name:<hex>' (C16.1)
  line_no       INTEGER NOT NULL CHECK (line_no >= 0), -- JSONL line / JSON-array element (1-based); 0 = whole JSON file
  raw           BLOB NOT NULL,                         -- exact bytes: JSONL line without '\n' (BOM stripped), array element, or whole file
  raw_sha256    TEXT NOT NULL,
  raw_kind      TEXT NOT NULL,                         -- Go-validated: 'jsonl_line' | 'json_array_item' | 'json_file'
  import_run_id TEXT NOT NULL REFERENCES import_runs(id),  -- first run that saw this occurrence
  created_at    TEXT NOT NULL,
  UNIQUE (recipe_id, source_path, line_no, raw_sha256)
);
CREATE INDEX recipe_sources_recipe ON recipe_sources(recipe_id);
CREATE TRIGGER recipe_sources_immutable BEFORE UPDATE ON recipe_sources
BEGIN SELECT RAISE(ABORT, 'recipe_sources: immutable'); END;
CREATE TRIGGER recipe_sources_no_delete BEFORE DELETE ON recipe_sources
BEGIN SELECT RAISE(ABORT, 'recipe_sources: no physical delete'); END;

CREATE TABLE recipe_refs (                             -- private reference metadata (index CSV rows, C16.9)
  recipe_id  TEXT NOT NULL REFERENCES recipes(id),
  source     TEXT NOT NULL,                            -- inbox-relative file
  ref_json   TEXT NOT NULL CHECK (json_valid(ref_json)),  -- {header: value} minus variant_id/seed columns
  created_at TEXT NOT NULL,
  PRIMARY KEY (recipe_id, source)
);
CREATE TRIGGER recipe_refs_immutable BEFORE UPDATE ON recipe_refs
BEGIN SELECT RAISE(ABORT, 'recipe_refs: immutable'); END;
CREATE TRIGGER recipe_refs_no_delete BEFORE DELETE ON recipe_refs
BEGIN SELECT RAISE(ABORT, 'recipe_refs: no physical delete'); END;

CREATE TABLE tracks (
  id                 TEXT PRIMARY KEY,                 -- UUIDv7 = MM_TAKE_ID tag
  slug               TEXT NOT NULL,
  title              TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  recipe_id          TEXT NOT NULL REFERENCES recipes(id),   -- D-M1-2
  parent_track_id    TEXT REFERENCES tracks(id),
  root_track_id      TEXT NOT NULL REFERENCES tracks(id),   -- = id for roots
  generation         INTEGER NOT NULL CHECK (generation >= 0),
  status             TEXT NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','to_relisten','final','discarded')),
  status_set_by      TEXT NOT NULL,                    -- 'import' in M1
  origin             TEXT NOT NULL CHECK (origin IN ('import','job')),
  duration_s         REAL NOT NULL CHECK (duration_s > 0),
  sample_rate        INTEGER NOT NULL CHECK (sample_rate > 0),
  bits_per_sample    INTEGER NOT NULL CHECK (bits_per_sample BETWEEN 4 AND 32),
  channels           INTEGER NOT NULL CHECK (channels BETWEEN 1 AND 8),
  total_samples      INTEGER NOT NULL CHECK (total_samples > 0),
  master_path        TEXT NOT NULL UNIQUE,             -- DataDir-relative
  master_sha256      TEXT NOT NULL UNIQUE,             -- sha256 of the frozen file = media address
  master_bytes       INTEGER NOT NULL,
  source_sha256      TEXT NOT NULL,                    -- sha256 of the ingested inbox bytes (provenance)
  frames_sha256      TEXT NOT NULL,                    -- sha256 of the audio-frame region (C13)
  audio_md5          TEXT NOT NULL,                    -- STREAMINFO MD5, hex (may be all zeros; never an identity)
  truncated_abc      INTEGER CHECK (truncated_abc IN (0,1)),       -- NULL = unknown (no result.json)
  truncated_semantic INTEGER CHECK (truncated_semantic IN (0,1)),
  bpm_declared       INTEGER,                          -- parsed from style (C12)
  import_rule        TEXT NOT NULL,                    -- Go-validated: match rule (C16.3) or 'job'
  import_run_id      TEXT REFERENCES import_runs(id),
  recorded_at        TEXT NOT NULL,                    -- best-known render time (inbox mtime)
  created_at         TEXT NOT NULL,
  CHECK ((parent_track_id IS NULL) = (generation = 0)),
  CHECK (parent_track_id IS NULL OR parent_track_id <> id)
);
CREATE UNIQUE INDEX tracks_audio_identity ON tracks(frames_sha256, total_samples);  -- audio identity (C13)
CREATE INDEX tracks_source_sha ON tracks(source_sha256);
CREATE INDEX tracks_root     ON tracks(root_track_id);
CREATE INDEX tracks_parent   ON tracks(parent_track_id);
CREATE INDEX tracks_recipe   ON tracks(recipe_id);
CREATE INDEX tracks_recorded ON tracks(recorded_at);
CREATE INDEX tracks_duration ON tracks(duration_s);
CREATE TRIGGER tracks_no_delete BEFORE DELETE ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: no physical delete'); END;
CREATE TRIGGER tracks_frozen BEFORE UPDATE OF
  id, recipe_id, parent_track_id, root_track_id, generation, origin, duration_s, sample_rate,
  bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256,
  frames_sha256, audio_md5, truncated_abc, truncated_semantic, bpm_declared, import_rule,
  import_run_id, recorded_at, created_at ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;
-- Mutable in later milestones: title, slug, status, status_set_by.

CREATE TABLE track_style_terms (
  track_id TEXT NOT NULL REFERENCES tracks(id),
  term     TEXT NOT NULL,
  PRIMARY KEY (track_id, term)
) WITHOUT ROWID;
CREATE INDEX track_style_terms_term ON track_style_terms(term);
CREATE TRIGGER track_style_terms_immutable BEFORE UPDATE ON track_style_terms
BEGIN SELECT RAISE(ABORT, 'track_style_terms: immutable'); END;
CREATE TRIGGER track_style_terms_no_delete BEFORE DELETE ON track_style_terms
BEGIN SELECT RAISE(ABORT, 'track_style_terms: no physical delete'); END;

CREATE TABLE mutations (                               -- Operator Ledger; M1 writes only inferred_sibling
  id              TEXT PRIMARY KEY,                    -- UUIDv7
  parent_track_id TEXT REFERENCES tracks(id),
  child_track_id  TEXT NOT NULL REFERENCES tracks(id),
  operator        TEXT NOT NULL,                       -- Go-validated; M1: 'inferred_sibling'
  inferred        INTEGER NOT NULL CHECK (inferred IN (0,1)),
  params_json     TEXT CHECK (params_json IS NULL OR json_valid(params_json)),  -- {"rule":"same_request_id"|"seed_family"|"mapping"}
  rationale       TEXT,
  principal_id    TEXT NOT NULL,
  created_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX mutations_child ON mutations(child_track_id);
CREATE TRIGGER mutations_immutable BEFORE UPDATE ON mutations
BEGIN SELECT RAISE(ABORT, 'mutations: immutable'); END;
CREATE TRIGGER mutations_no_delete BEFORE DELETE ON mutations
BEGIN SELECT RAISE(ABORT, 'mutations: no physical delete'); END;

CREATE TABLE renditions (                              -- derived, reproducible files
  track_id    TEXT NOT NULL REFERENCES tracks(id),
  kind        TEXT NOT NULL,                           -- Go-validated; M1: 'mp3_320' | 'peaks'
  sha256      TEXT NOT NULL,                           -- of the derived file (= its media address)
  bytes       INTEGER NOT NULL,
  path        TEXT NOT NULL,                           -- DataDir-relative
  params_json TEXT NOT NULL CHECK (json_valid(params_json)),
  created_at  TEXT NOT NULL,
  PRIMARY KEY (track_id, kind)
);
CREATE INDEX renditions_sha ON renditions(sha256);
CREATE TRIGGER renditions_no_delete BEFORE DELETE ON renditions
BEGIN SELECT RAISE(ABORT, 'renditions: no physical delete'); END;
-- UPDATE allowed (a future rebuild replaces a derived file with a NEW sha-named file).

CREATE TABLE analyses (
  track_id     TEXT NOT NULL REFERENCES tracks(id),
  analyzer     TEXT NOT NULL,                          -- Go-validated; M1: 'loudness' | 'spectrum'
  version      TEXT NOT NULL,                          -- C14: '<definition> ffmpeg <raw token>', e.g. 'mm-loudness/1 ffmpeg n9.0.1'
  tier         TEXT NOT NULL,                          -- Go-validated; M1: '0'
  metrics_json TEXT NOT NULL CHECK (json_valid(metrics_json)),
  created_at   TEXT NOT NULL,
  PRIMARY KEY (track_id, analyzer, version)
);
CREATE TRIGGER analyses_immutable BEFORE UPDATE ON analyses
BEGIN SELECT RAISE(ABORT, 'analyses: immutable'); END;
CREATE TRIGGER analyses_no_delete BEFORE DELETE ON analyses
BEGIN SELECT RAISE(ABORT, 'analyses: no physical delete'); END;

CREATE TABLE artifacts (
  track_id   TEXT NOT NULL REFERENCES tracks(id),
  name       TEXT NOT NULL,                            -- safe top-level name (C16.1): 'request.json','result.json',
                                                       -- 'config.json','plan.json','plan_manifest.json','score.abc','semantic.npy',...
  sha256     TEXT NOT NULL,
  bytes      INTEGER NOT NULL,
  path       TEXT NOT NULL,                            -- takes/<track_id>/<name>
  created_at TEXT NOT NULL,
  PRIMARY KEY (track_id, name)
);
CREATE TRIGGER artifacts_immutable BEFORE UPDATE ON artifacts
BEGIN SELECT RAISE(ABORT, 'artifacts: immutable'); END;
CREATE TRIGGER artifacts_no_delete BEFORE DELETE ON artifacts
BEGIN SELECT RAISE(ABORT, 'artifacts: no physical delete'); END;

CREATE TABLE scores (
  track_id        TEXT PRIMARY KEY REFERENCES tracks(id),
  abc_path        TEXT NOT NULL,                       -- takes/<track_id>/score.abc
  abc_sha256      TEXT NOT NULL,
  bars            INTEGER NOT NULL CHECK (bars >= 0),
  meter           TEXT NOT NULL,                       -- '4/4'
  q               TEXT,                                -- '1/4=92', NULL if absent/unsupported
  bpm             REAL,
  seconds_per_bar REAL,                                -- NULL ⇒ untimed
  aligned         INTEGER NOT NULL CHECK (aligned IN (0,1)),  -- score sections align 1:1 with lyrics sections
  source          TEXT NOT NULL CHECK (source IN ('import','job')),
  created_at      TEXT NOT NULL
);
CREATE TRIGGER scores_immutable BEFORE UPDATE ON scores
BEGIN SELECT RAISE(ABORT, 'scores: immutable'); END;
CREATE TRIGGER scores_no_delete BEFORE DELETE ON scores
BEGIN SELECT RAISE(ABORT, 'scores: no physical delete'); END;

CREATE TABLE sections (
  track_id   TEXT NOT NULL REFERENCES tracks(id),
  source     TEXT NOT NULL CHECK (source IN ('lyrics','plan','measured','manual')),
  idx        INTEGER NOT NULL CHECK (idx >= 0),
  kind       TEXT NOT NULL,                            -- intro|verse|pre-chorus|chorus|bridge|interlude|instrumental|solo|outro|other
  label      TEXT NOT NULL,                            -- 'verse 1'
  raw_label  TEXT NOT NULL,                            -- '[Verse 1]' or '% verse'
  start_s    REAL,
  end_s      REAL,
  confidence REAL CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
  lines_json TEXT CHECK (lines_json IS NULL OR json_valid(lines_json)),
  PRIMARY KEY (track_id, source, idx),
  CHECK ((source = 'lyrics') = (start_s IS NULL)),
  CHECK ((start_s IS NULL) = (end_s IS NULL)),
  CHECK (start_s IS NULL OR (start_s >= 0 AND end_s > start_s))
) WITHOUT ROWID;
CREATE TRIGGER sections_immutable BEFORE UPDATE ON sections
BEGIN SELECT RAISE(ABORT, 'sections: immutable'); END;
CREATE TRIGGER sections_no_delete BEFORE DELETE ON sections
BEGIN SELECT RAISE(ABORT, 'sections: no physical delete'); END;

CREATE TABLE recipe_drafts (
  id                    TEXT PRIMARY KEY,             -- UUIDv7
  origin                TEXT NOT NULL,                -- Go-validated; M1: 'import_backlog' | 'varianti_import'
  source_recipe_id      TEXT REFERENCES recipes(id),  -- the frozen imported recipe (its raw bytes live in recipe_sources)
  parent_track_id       TEXT REFERENCES tracks(id),   -- mother (root of the matching lineage), when known
  group_key             TEXT,                         -- C12 GroupKey(request_id)
  label                 TEXT,                         -- reference variant_type if present, else id suffix ('v2')
  request_id            TEXT NOT NULL,
  request_json          TEXT NOT NULL CHECK (json_valid(request_json)),
  status                TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','fulfilled','archived')),
  fulfilled_by_track_id TEXT REFERENCES tracks(id),
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  CHECK ((status = 'fulfilled') = (fulfilled_by_track_id IS NOT NULL))
);
CREATE UNIQUE INDEX recipe_drafts_import_once ON recipe_drafts(source_recipe_id)
  WHERE source_recipe_id IS NOT NULL;
CREATE INDEX recipe_drafts_status_group ON recipe_drafts(status, group_key, request_id);
CREATE TRIGGER recipe_drafts_no_delete BEFORE DELETE ON recipe_drafts
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: no physical delete'); END;
CREATE TRIGGER recipe_drafts_frozen BEFORE UPDATE OF id, origin, source_recipe_id, created_at ON recipe_drafts
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: frozen column'); END;

CREATE TABLE draft_collections (
  draft_id TEXT NOT NULL REFERENCES recipe_drafts(id),
  name     TEXT NOT NULL,                              -- collection JSON "name" (C16.4)
  source   TEXT NOT NULL,                              -- inbox-relative file
  PRIMARY KEY (draft_id, name)
) WITHOUT ROWID;
CREATE INDEX draft_collections_name ON draft_collections(name);

CREATE TABLE device_profiles (
  device_id      TEXT PRIMARY KEY CHECK (length(device_id) BETWEEN 8 AND 64),
  label          TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
  user_agent     TEXT NOT NULL CHECK (length(user_agent) <= 512),
  quality        TEXT NOT NULL,                        -- Go-validated; M1: 'lossless' | 'high' | 'auto'
  quality_source TEXT NOT NULL CHECK (quality_source IN ('default','user','probe')),
  probe_result   TEXT NOT NULL DEFAULT 'none' CHECK (probe_result IN ('none','ok','failed','inconclusive')),
  flac_can_play  TEXT NOT NULL CHECK (flac_can_play IN ('probably','maybe','')),
  flac_decode_ok INTEGER CHECK (flac_decode_ok IN (0,1)),   -- NULL = not established (inconclusive)
  flac_seek_ok   INTEGER CHECK (flac_seek_ok IN (0,1)),
  probe_version  INTEGER NOT NULL,
  probed_at      TEXT,
  created_by     TEXT NOT NULL,                        -- principal id
  updated_by     TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE TRIGGER device_profiles_no_delete BEFORE DELETE ON device_profiles
BEGIN SELECT RAISE(ABORT, 'device_profiles: no physical delete'); END;
CREATE TRIGGER device_profiles_frozen BEFORE UPDATE OF device_id, created_by, created_at ON device_profiles
BEGIN SELECT RAISE(ABORT, 'device_profiles: frozen column'); END;

-- Full-text search: regular FTS5 tables with an UNINDEXED key column (no rowid coupling, so VACUUM-safe).
CREATE VIRTUAL TABLE tracks_fts USING fts5(
  track_id UNINDEXED, title, request_id, style, lyrics,
  tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER tracks_fts_ai AFTER INSERT ON tracks BEGIN
  INSERT INTO tracks_fts(track_id, title, request_id, style, lyrics)
  SELECT NEW.id, NEW.title, r.request_id, r.style, r.lyrics FROM recipes r WHERE r.id = NEW.recipe_id;
END;
CREATE TRIGGER tracks_fts_au AFTER UPDATE OF title ON tracks BEGIN
  UPDATE tracks_fts SET title = NEW.title WHERE track_id = NEW.id;
END;

CREATE VIRTUAL TABLE drafts_fts USING fts5(
  draft_id UNINDEXED, request_id, style, lyrics,
  tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER drafts_fts_ai AFTER INSERT ON recipe_drafts BEGIN
  INSERT INTO drafts_fts(draft_id, request_id, style, lyrics)
  VALUES (NEW.id, NEW.request_id, json_extract(NEW.request_json, '$.style'),
          json_extract(NEW.request_json, '$.lyrics'));
END;
CREATE TRIGGER drafts_fts_au AFTER UPDATE OF request_id, request_json ON recipe_drafts BEGIN
  UPDATE drafts_fts SET request_id = NEW.request_id,
         style  = json_extract(NEW.request_json, '$.style'),
         lyrics = json_extract(NEW.request_json, '$.lyrics')
   WHERE draft_id = NEW.id;
END;
```
Notes: no DELETE triggers exist for the FTS tables because their parents cannot be deleted. The
`purge_grant` escape hatch (design §4.4) arrives with `purge`; M1 triggers are unconditional. Reference
metadata appears only in `recipe_refs.ref_json` (never in FTS).

**Write SQL (binding; C17 implements exactly these forms):**
- Recipes: `INSERT INTO recipes(…) VALUES (…) ON CONFLICT(hash) DO NOTHING RETURNING id`; no row
  returned ⇒ `SELECT id FROM recipes WHERE hash = ?` (`created = false`).
- Recipe sources: `INSERT INTO recipe_sources(…) VALUES (…) ON CONFLICT(recipe_id, source_path,
  line_no, raw_sha256) DO NOTHING` — written for every occurrence on every run (idempotent).
- Refs: `INSERT … ON CONFLICT(recipe_id, source) DO NOTHING` (a changed CSV row ⇒ warning `ref_changed`).
- Device profiles: `INSERT INTO device_profiles(…) VALUES (…) ON CONFLICT(device_id) DO UPDATE SET
  label=excluded.label, user_agent=excluded.user_agent, quality=excluded.quality,
  quality_source=excluded.quality_source, probe_result=excluded.probe_result,
  flac_can_play=excluded.flac_can_play, flac_decode_ok=excluded.flac_decode_ok,
  flac_seek_ok=excluded.flac_seek_ok, probe_version=excluded.probe_version,
  probed_at=excluded.probed_at, updated_by=excluded.updated_by, updated_at=excluded.updated_at`.
- **Never** `INSERT OR REPLACE` / `REPLACE INTO`: REPLACE deletes and re-inserts, resets `created_at`
  and, with recursive triggers off, deletes without firing the no-delete triggers. `TestNoReplaceSQL`
  greps `internal/**/*.go` and `*.sql` (case-insensitive) and fails on either form.
- Fulfilment: `UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=?, updated_at=?
  WHERE id=? AND status='backlog'`. Mother relink: `UPDATE recipe_drafts SET parent_track_id=?,
  updated_at=? WHERE id=? AND parent_track_id IS NULL`.
- Runs: heartbeat `UPDATE import_runs SET heartbeat_at=? WHERE id=? AND state='running'`; finish
  `UPDATE import_runs SET state=?, finished_at=?, summary_json=?, report_json=?, error=? WHERE id=?
  AND state='running'`.
- Title (tests only in M1): `UPDATE tracks SET title=? WHERE id=?`.
- **A frozen column never appears in an UPDATE's SET list** — a `BEFORE UPDATE OF col` trigger fires
  whenever `col` is named in SET, even when the value is unchanged. Every write path above has a test
  that runs it twice.

**Store additions (M1-B2 amends M0 C2):**
```go
var ErrNoDatabase     = errors.New("store: database does not exist")
var ErrSchemaMismatch = errors.New("store: schema version mismatch") // wrapped with have/want
func OpenReadOnly(ctx context.Context, path string) (*Store, error) // dry-run only
```
- Missing file ⇒ `ErrNoDatabase` and nothing is created (the importer treats it as an empty library).
- `<path>-wal` exists (a live writer owns the sidecars) ⇒ DSN `mode=ro` + `_pragma=query_only(1)` +
  `_pragma=busy_timeout(5000)`.
- Otherwise ⇒ `mode=ro&immutable=1` + `query_only(1)`. Verified with modernc.org/sqlite v1.60.1: plain
  `mode=ro` on a cleanly closed WAL database creates `-wal`/`-shm` and leaves them behind;
  `immutable=1` creates nothing.
- No migration; no `journal_mode` pragma (M0's `dsn` helper always adds `journal_mode(WAL)`, so
  `OpenReadOnly` builds its own DSN). `user_version != len(migrations)` ⇒ `ErrSchemaMismatch`
  (exit 2; the message names both versions and the fix: start this binary's `serve` once — it migrates
  on open, and a freshly deployed container already has — or use the binary that matches the DB).
- `W` and `R` are the same read-only pool, so an accidental write fails ("readonly database") instead of
  panicking.
- The dry-run reads all DB state it needs in one read transaction at start, then re-checks that the
  `-wal` presence, size and mtime of the DB file are unchanged; any change ⇒ fatal
  `DB_CHANGED_DURING_DRY_RUN` (exit 2, re-run).

### C12. Domain: `internal/engine/yue2`

```go
const DefaultSeed = 831001
const MaxSeed = 1<<53 - 1 // D-M1-7
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,179}$`) // not "." or ".."

// Instructions is copied verbatim from yue2-infer 0.1.6 src/yue2/protocol.py INSTRUCTIONS
// (PROTOCOL_VERSION "yue2-native-v1"); golden-tested.
var Instructions = map[string]string{"off": "…", "melody": "…", "full": "…"}

type Request struct {
    ID       string   `json:"id"`
    Style    string   `json:"style"`
    Lyrics   string   `json:"lyrics"`
    Cot      string   `json:"cot"`  // full|melody|off
    Seed     int64    `json:"seed"`
    ABC      *string  `json:"abc,omitempty"`
    CFGScale *float64 `json:"cfg_scale,omitempty"`
}
type ParseOptions struct {
    // ResolveABCPath returns the bytes of an abc_path target. The importer passes a resolver confined
    // to the inbox (C16.2); nil ⇒ any abc_path is the error abc_path_unresolved.
    ResolveABCPath func(rel string) ([]byte, error)
    AllowTitle     bool // per-song JSON files: "title" is app metadata (Parsed.Title), not a dropped key
}
type Parsed struct {
    Request   Request
    Canonical []byte   // CanonicalJSON(Request)
    Hash      string   // hex sha256(Canonical)
    CoreHash  string   // hex sha256(CanonicalJSON of {cot,id,lyrics,seed,style} only)
    Title     string   // AllowTitle only
    ABCPath   string   // the abc_path value when abc was inlined from it
    Dropped   []string // keys present but not part of the canonical request
    Warnings  []string // codes (below)
}
type ParseError struct{ Code, Detail string } // Detail never contains field values (C16.9)
func ParseRequest(raw []byte, o ParseOptions) (Parsed, error)
func CanonicalJSON(r Request) []byte
// Keys sorted lexicographically (abc, cfg_scale, cot, id, lyrics, seed, style), compact separators,
// json.Encoder with SetEscapeHTML(false), no trailing newline. Deterministic.
```
`raw` must be one JSON object. **Canonicalisation and validation rules** (each has a `TestParseRequest`
row; together they make YuE2's own `request.json` hash exactly like the equivalent JSONL line):
1. `tags` is an alias of `style`; both present ⇒ must be equal, else `style_tags_mismatch`.
2. `cot` missing ⇒ `"full"` (warning `cot_defaulted`); `seed` missing ⇒ 831001 (`seed_defaulted`).
3. `abc`: JSON `null` ⇒ absent. Non-string ⇒ `bad_abc`. Empty or whitespace-only ⇒ `empty_abc` (YuE2's
   `SongRequest` rejects it, so no YuE2-written file contains one). With `cot=off` ⇒ `abc_requires_cot`.
4. `abc_path`: together with a non-null `abc` ⇒ `abc_conflict` (YuE2: mutually exclusive). Otherwise
   resolved through `ResolveABCPath` and inlined as `abc` (rule 3 then applies to its content);
   failure ⇒ `abc_path_unresolved` and the line is invalid — dropping it would change the recipe's
   meaning (for example `cot=melody` with no abc).
5. `cfg_scale`: `null` ⇒ absent. Otherwise a finite JSON number in [0, 20], else `bad_cfg_scale`. A value
   equal to YuE2's implied default — 1.0, or 1.01 when `cot=off` (`SongRequest.guidance` in yue2-infer
   0.1.6 `protocol.py`, where the field itself defaults to `None`) — is dropped (warning
   `cfg_scale_default_dropped`). YuE2 writes `request.json` as `dataclasses.asdict(SongRequest)`: field
   order style, lyrics, cot, seed, abc, cfg_scale, id, `"abc": null`, `"cfg_scale": null`, indent 2.
6. `prompt` (non-null): must equal `Instructions[cot] + "\n[Tags]\n" + style + "\n[Lyrics]\n" + lyrics
   + "\n"` (YuE2 `request_kwargs` behaviour) ⇒ dropped (`prompt_verified`); otherwise `prompt_mismatch`.
7. `abc_sampling` / `semantic_sampling` ⇒ `sampling_unsupported` (they change the render; D-M1-8).
8. `lang`, `eval_index`, `clip_id` ⇒ dropped (`dropped_key:<k>`; YuE2 ignores them). `title` ⇒
   `Parsed.Title` when `AllowTitle`, else dropped. Any other key ⇒ dropped with `dropped_key:<k>` (an
   unknown key cannot change a YuE2 render). Dropped keys never reach `Canonical`, so a per-song JSON
   that adds `title` hashes like its JSONL line.
9. `seed` must be an integer JSON number token (no fraction or exponent) in 0..MaxSeed, else
   `seed_out_of_range`; `id` must match `idRe` and not be `.`/`..`, else `bad_id`; `lyrics`/`style`
   missing ⇒ `missing_lyrics`/`missing_style`; `cot` ∉ {full, melody, off} ⇒ `bad_cot`; any string that
   is not valid UTF-8 ⇒ `bad_utf8`.

Error codes: `not_object, missing_id, bad_id, missing_lyrics, missing_style, style_tags_mismatch,
bad_cot, seed_out_of_range, bad_cfg_scale, bad_abc, empty_abc, abc_requires_cot, abc_conflict,
abc_path_unresolved, prompt_mismatch, sampling_unsupported, bad_utf8`. Warning codes: `seed_defaulted,
cot_defaulted, tags_alias, crlf, bom, dropped_key:<k>, cfg_scale_default_dropped, prompt_verified`.

```go
type Section struct {
    Idx   int      `json:"idx"`
    Kind  string   `json:"kind"`  // intro|verse|pre-chorus|chorus|bridge|interlude|instrumental|solo|outro|other
    Label string   `json:"label"` // "verse 1" (kind + per-kind occurrence number; single-occurrence kinds unnumbered)
    Raw   string   `json:"raw"`   // "[Verse 1]"
    Lines []string `json:"lines"` // non-empty lyric lines, trimmed
}
func LyricsSections(lyrics string) []Section
// Header = a line matching ^\s*\[([^\]]+)\]\s*$. Kind = first word(s) lowercased, digits and any
// annotation after "–", "-", ":", "(" or "+" stripped, mapped through a fixed synonym table
// ("pre chorus"/"prechorus" → pre-chorus, "instrumental break" → instrumental, unknown → other).
// Text before the first header becomes section "other" only if non-empty.

type Score struct {
    Meter         string         // "4/4" ("C" → "4/4", "C|" → "2/2"); default "4/4" when M: absent
    Q             string         // raw "1/4=92"; "" if absent
    BPM           float64
    SecondsPerBar float64        // 0 ⇒ untimed
    Bars          int            // bars of the first voice
    Sections      []ScoreSection // from "% <label>" comment lines
    Warnings      []string       // e.g. voice_sections_differ
}
type ScoreSection struct{ Kind, Label, Raw string; StartBar, Bars int }
func ParseScore(abc []byte) (Score, error)
func AlignSections(lyr []Section, sc Score) (times [][2]float64, ok bool)
// ok iff sc.SecondsPerBar > 0, len(sc.Sections) == len(lyr) and kinds are equal in order; then
// times[i] = {StartBar*spb, (StartBar+Bars)*spb}. Otherwise (nil, false): sections stay untimed.
```
**ParseScore rules.** Header fields `M:`, `L:`, `Q:`, `K:`, `V:`. Supported tempo: `Q:a/b=N`, optionally
preceded by a quoted string. `SecondsPerBar = (mNum/mDen) / (qNum/qDen) * 60 / BPM`. Any of these makes
the score **untimed** (`SecondsPerBar = 0`): an inline or mid-tune `[M:…]`/`[Q:…]`; an unsupported `Q:`
form; **any repeat or volta token in the first voice** (`|:`, `:|`, `::`, `[1`, `[2`, `|1`, `|2`) —
repeats are not expanded, and the claim that YuE2 scores never use them is unverified. Bars are counted
in the FIRST voice only (first `V:` id seen, or the tune body if there is no `V:`). A bar = a barline
token (`|`, `||`, `|]`, `[|`) preceded by at least one note or rest since the previous barline; a
trailing partial bar counts. Text inside `"…"` (chords/annotations) and `!…!` / `+…+` decorations is
skipped; `%%` directive lines are ignored.
A multi-measure rest `Z`/`X` with optional count (`Z4`) counts that many bars (`Z` = 1) and its closing
barline is not counted again; an inline `[V:…]` voice switch, or any section that ends up with zero bars,
makes the score untimed. A `% label` attaches to the next voice block; a trailing partial bar ends with
its voice block.
**Sections** come from comment lines `^%\s*(label)\s*$` whose label maps to a known section kind through
the lyrics synonym table (other comments are ignored). Two voice layouts are supported and tested:
- *interleaved* — some voice id re-appears after another voice (V:1 … V:2 … V:1 … per section): every
  `% label` line starts a section at the first voice's bar count at that point (labels typically sit
  between the previous section's last voice and the next V:1);
- *per-voice* — each voice body is contiguous (all of V:1, then all of V:2): only `% label` lines inside
  the first voice's body (or before it) count; labels inside later voices are ignored, and if their
  sequence differs from the first voice's the score gets warning `voice_sections_differ` (it stays timed
  from the first voice).

```go
func StyleTerms(style string) (terms []string, bpm int)
// Split on , ; | and newlines; trim; lowercase; collapse spaces; drop empties, terms > 40 runes and
// duplicates; a term matching (?i)^(\d{2,3})\s*bpm$ (or "bpm \d+") sets bpm (kept only in 40..250) and
// is not a term. Max 20 terms, input order.
func Slug(s string) string
// D-M1-3: NFKD (golang.org/x/text/unicode/norm), strip Mn marks, apply the fold table (ß→ss æ→ae œ→oe
// ø→o ł→l đ→d þ→th, upper-case forms too), lowercase, runs of non [a-z0-9] → "_", trim "_", max 80 bytes.
func GroupKey(requestID string) string
// Grouping ONLY (draft group_key, backlog grouping, unmatched-audio candidates) — never lineage.
// Strips ONE trailing (?i)[-_](v\d+|s\d+|seed\d+): "x_001_v2"→"x_001", "song-s1101"→"song", "plain"→"plain".
func SeedFamily(requestID string) string
// Lineage (C16.5): strips ONE trailing seed suffix (?i)[-_](s\d+|seed\d+) only:
// "song-s1101"→"song", "song_seed7"→"song", "x_001_v2"→"x_001_v2" (variants are independent recipes).
```

### C13. FLAC metadata: `internal/media/flacmeta` (pure Go) + `flactest`

```go
type BlockType uint8 // 0 STREAMINFO,1 PADDING,2 APPLICATION,3 SEEKTABLE,4 VORBIS_COMMENT,5 CUESHEET,6 PICTURE
type StreamInfo struct {
    MinBlock, MaxBlock uint16; MinFrame, MaxFrame uint32
    SampleRate uint32; Channels, BitsPerSample uint8; TotalSamples uint64; MD5 [16]byte
}
type Block struct{ Type BlockType; Offset int64; Length uint32 }
type SeekPoint struct{ Sample, Offset uint64; FrameSamples uint16 }
type Info struct {
    StreamInfo   StreamInfo
    Blocks       []Block
    FramesOffset int64       // first byte after the last metadata block
    Vendor       string
    Comments     []string    // "KEY=value" in file order (from the first VORBIS_COMMENT)
    SeekPoints   []SeekPoint // placeholders (Sample==0xFFFFFFFFFFFFFFFF) excluded
}
func Read(r io.ReaderAt, size int64) (Info, error) // ErrNotFLAC, ErrCorrupt; never panics (fuzzed)
func (i Info) Count(t BlockType) int
func (i Info) Duration() float64                    // TotalSamples / SampleRate
func FramesSHA256(r io.ReaderAt, size, off int64) (string, error)
func AudioID(framesSHA256 string, totalSamples uint64) string // "<frames sha>:<total samples>"
```
**Audio identity** = `AudioID(FramesSHA256, TotalSamples)` (DB: unique `(frames_sha256, total_samples)`).
The STREAMINFO MD5 is never an identity (encoders may leave it all zeros).
**Bounded allocations** (`Read` must not allocate more than the bytes it is entitled to read):
- at most 1024 metadata blocks, else `ErrCorrupt`; STREAMINFO must come first with length 34;
- every block's declared length must fit in the file (`offset + 4 + length ≤ size`), else `ErrCorrupt`;
- only STREAMINFO, SEEKTABLE (length a multiple of 18) and the first VORBIS_COMMENT are loaded; other
  blocks (PICTURE, APPLICATION, PADDING, …) are skipped by offset and never read into memory;
- VORBIS_COMMENT larger than 1 MiB ⇒ `ErrCorrupt`; vendor length ≤ block length − 8; comment count ≤
  (bytes left in the block) / 4 before any slice is allocated; each comment length ≤ bytes left in the
  block.

`internal/media/flacmeta/flactest` (**test-only**, owner M1-B1): builds header-valid FLAC files for
tests in every package (a `_test.go` builder could not be imported across packages):
```go
type Spec struct {
    SampleRate uint32; Channels, BitsPerSample uint8; TotalSamples uint64; MD5 [16]byte
    Vendor string; Comments []string; SeekPoints, PaddingBytes, PictureBytes int
    Frames []byte // opaque bytes after the metadata (header-valid file, not decodable audio)
}
func Build(s Spec) []byte
func WriteFile(tb testing.TB, path string, s Spec) // mode 0644
```

### C14. Analyzer: `internal/analysis` + `internal/analysis/ffmpeg`

```go
type Loudness struct {
    IntegratedLUFS   float64   `json:"integrated_lufs"`
    LRA              float64   `json:"lra_lu"`
    LRALowLUFS       float64   `json:"lra_low_lufs"`
    LRAHighLUFS      float64   `json:"lra_high_lufs"`
    TruePeakDBTP     float64   `json:"true_peak_dbtp"`
    SamplePeakDBFS   float64   `json:"sample_peak_dbfs"`
    ShortTermMaxLUFS float64   `json:"short_term_max_lufs"`
    ShortTerm1Hz     []float64 `json:"short_term_1hz"` // one value per whole second, floor -70.0, 0.1 LU
}
type Spectrum struct {
    BandwidthHz *int    `json:"bandwidth_hz"` // nil for silence
    RefDB       float64 `json:"ref_db"`
    ThresholdDB float64 `json:"threshold_db"`
    Frames      int     `json:"frames"`
}
type Waveform struct{ Version, Channels, SampleRate, SamplesPerPixel, Bits, Length int; Data []int8 } // json snake_case
type Peaks struct{ Overview, Detail Waveform; DurationS float64 }
const (LoudnessDef = "mm-loudness/1"; SpectrumDef = "mm-spectrum/1"; PeaksFormat = "mm.peaks/1")
type Analyzer interface {
    ToolVersion() string // "ffmpeg <raw token>"
    Loudness(ctx context.Context, workPath string) (Loudness, error)
    Scan(ctx context.Context, workPath string, sampleRate, channels int, totalSamples uint64) (Peaks, Spectrum, error)
}
func ffmpeg.New(ctx context.Context, tools mediatool.Tools) (*FFmpeg, error)
// Runs "-hide_banner -version" (token = the first field after "ffmpeg version ", stored VERBATIM:
// "n9.0.1", "7.1.2-0ubuntu1", git builds print "N-118123-g…"), "-filters" (requires " ebur128 ") and
// "-encoders" (requires "libmp3lame"); otherwise ErrToolMissing naming the capability. Nothing needs
// soxr (C21 uses the built-in swr resampler).
```
**Version strings (one scheme everywhere):** `analyses.version = <definition> + " " + ToolVersion()`,
e.g. `mm-loudness/1 ffmpeg n9.0.1` and `mm-spectrum/1 ffmpeg n9.0.1`; the MP3 rendition's params carry
`"ffmpeg": "<raw token>"`; peaks carry `"format": "mm.peaks/1"`.

**Loudness invocation** (exact; parsed from the metadata stream on stdout, never from stderr; timeout
300 s, stdout cap 32 MiB):
```
ffmpeg -nostdin -hide_banner -nostats -v error -protocol_whitelist file,pipe -i file:<work>/master.flac \
  -map 0:a:0 -af ebur128=peak=true+sample:framelog=quiet:metadata=1,ametadata=mode=print:file=- -f null -
```
Parsed: lines `frame:N pts:P pts_time:T` then `lavfi.r128.<key>=<float>`. From the **last** frame: `I`,
`LRA`, `LRA.low`, `LRA.high`, `true_peak`, `sample_peak` (linear, cumulative max → dB via 20·log10;
0 → −120). ffmpeg 9.0.1 prints the peaks as linear values with only 3 decimals, so dB precision is poor
on quiet signals. **Short-term curve:** for each whole second k = 1 … floor(duration), take `S` from the
frame with the largest `pts_time ≤ k + 1e-6` (ffmpeg 9.0.1 emits 100 ms frames; other builds may frame
differently, so exact `pts_time` matches are never required); clamp ≥ −70.0, round to 0.1.
`ShortTermMaxLUFS` = max `S` over all frames. Non-zero exit ⇒ `*mediatool.ToolError` (stderr tail ≤ 2
KiB, no argv).

**Decode invocation** (one pass feeds peaks and spectrum in Go; timeout 300 s; streamed, not captured):
```
ffmpeg -nostdin -hide_banner -v error -protocol_whitelist file,pipe -i file:<work>/master.flac -map 0:a:0 -f f32le -acodec pcm_f32le -
```
stdout is read in chunks of `channels × 4096` float32 frames. `Scan` fails if the decoded frame count
differs from STREAMINFO `TotalSamples` by more than one block, or if more than `TotalSamples + 1 block`
frames arrive (the process is then killed).

**Bandwidth (Go, from the decode stream).** `aspectralstats` was rejected: its `rolloff` is fixed at
85 % of spectral magnitude (measured: full-band white noise → 20.4 kHz = 0.85·Nyquist), so it measures
balance, not bandwidth. Definition (`mm-spectrum/1`): mono mix m = mean of channels; frames of
N = 8192 samples, hop 8192, Hann window; frames with RMS(m) < −60 dBFS skipped; power spectrum
averaged over frames (radix-2 FFT, `internal/analysis/fft.go`); bands of 100 Hz (bins grouped by
centre frequency); band level = 10·log10(mean power); `ref` = max band level over bands ≥ 50 Hz;
`threshold = ref − 70 dB`; `bandwidth_hz` = upper edge of the highest band ≥ threshold (multiple of
100). No voiced frames ⇒ nil.

**Peaks format** (`mm.peaks/1`, file `derived/<aa>/<sha>.peaks.json`, compact JSON + `\n`):
```json
{"format":"mm.peaks/1","duration_s":6.0,
 "overview":{"version":2,"channels":1,"sample_rate":48000,"samples_per_pixel":141,"bits":8,"length":2043,"data":[-64,64,…]},
 "detail":  {"version":2,"channels":1,"sample_rate":48000,"samples_per_pixel":2400,"bits":8,"length":120,"data":[…]}}
```
Each level is an audiowaveform JSON v2 object (BBC format; we only reuse the format, no GPL code).
`detail.samples_per_pixel = round(sample_rate/20)`; `overview.samples_per_pixel = max(1, ceil(total/2048))`;
`length = ceil(total/spp)`; `data` = interleaved min,max per pixel over **all channels**;
`q(v) = clamp(round(v*127), -127, 127)` (`[]int8` encodes as numbers).

### C15. Ingest pipeline: `internal/media/ingest`

```go
type ArtifactSrc struct {
    Name   string  // safe top-level name (C16.1)
    File   fs.File // opened by the importer via inbox.OpenRegular; never a path
    SHA256 string  // from the inventory
}
type Request struct {
    TrackID, Title, Slug, RecipeID, RequestID string
    Seed         int64
    Source       fs.File   // opened by the importer via inbox.OpenRegular (regular file, identity-checked)
    SourceSHA256 string    // from the inventory; re-verified while copying
    RecordedAt   time.Time
    Artifacts    []ArtifactSrc // result-dir files and/or score.abc (Name "score.abc")
}
type Rendition struct{ Kind, RelPath, SHA256 string; Bytes int64; Params map[string]any }
type Result struct {
    MasterRelPath, MasterSHA256, FramesSHA256, AudioMD5 string
    MasterBytes int64
    Info        flacmeta.Info // post-freeze
    MP3, Peaks  Rendition
    Loudness    analysis.Loudness
    Spectrum    analysis.Spectrum
    ToolVersion string        // Analyzer.ToolVersion()
    ReplayGainDB, ReplayGainPeak float64
    Artifacts   []Rendition   // Kind "artifact"
    Created     []string      // DataDir-relative paths THIS call claimed (step 14); Discard's only input
}
type Pipeline struct{ /* data *os.Root, dataDir string, tools mediatool.Tools, an analysis.Analyzer, now */ }
func New(data *os.Root, dataDir string, tools mediatool.Tools, an analysis.Analyzer, now func() time.Time) *Pipeline
func (p *Pipeline) Ingest(ctx context.Context, req Request) (Result, error) // writes files only; never sees an inbox path
func (p *Pipeline) Discard(r Result) error // after a failed DB insert (below)
// Error codes (type *Error{Code, Path, Detail}; Path is DataDir-relative): SOURCE_CHANGED, INVALID_AUDIO,
// FREEZE_INVARIANT, TOOL_FAILED, IO.
```
The importer owns and closes `Source` and artifact files. Every tool call goes through
`mediatool.Run` with the timeouts below; argv contains only absolute paths inside the work dir (C10).

Steps (exact order):
1. `work = incoming/ingest-<TrackID>/` (0700; must not exist — TrackIDs are fresh UUIDv7).
2. Stream-copy `Source` → `work/master.flac`, hashing on the fly; sha ≠ `SourceSHA256` ⇒
   `SOURCE_CHANGED`; fsync.
3. `flac --test --silent <work>/master.flac` (timeout 120 s; exit ≠ 0 ⇒ `INVALID_AUDIO`; verifies frames
   + STREAMINFO MD5 — this is the guard against truncated `--partial` transfers).
4. `flacmeta.Read` → StreamInfo, `FramesOffset`; `pre := FramesSHA256`; MD5 hex.
5. `Analyzer.Loudness` and `Analyzer.Scan` (peaks + spectrum).
6. ReplayGain: `gain = clamp(-18 - I, -24, +24)` formatted `"%+.2f dB"`; `peak = 10^(TP/20)` formatted
   `"%.6f"`; `REPLAYGAIN_REFERENCE_LOUDNESS = "-18.00 LUFS"`.
7. Write `work/tags.txt` (UTF-8, one `KEY=value` per line): `TITLE`, `MM_TAKE_ID`, `MM_RECIPE_ID`,
   `MM_REQUEST_ID`, `MM_SEED`, `REPLAYGAIN_TRACK_GAIN`, `REPLAYGAIN_TRACK_PEAK`,
   `REPLAYGAIN_REFERENCE_LOUDNESS` (this order). Values are sanitised: invalid UTF-8 → U+FFFD; C0
   controls (incl. NUL, TAB, CR, LF), DEL and C1 controls → a space; trimmed; `TITLE` ≤ 200 runes. Tag
   values never appear in argv.
8. **The single metadata write** (timeout 30 s):
   `metaflac --no-utf8-convert --remove-all-tags --import-tags-from=<work>/tags.txt --add-seekpoint=2s <work>/master.flac`
   (one invocation; vendor string preserved; verified: frames bytes and STREAMINFO MD5 unchanged).
9. Re-read: MD5 equal; `FramesSHA256` equal to `pre`; `Count(VORBIS_COMMENT) == 1`; comments equal
   tags.txt exactly (order and bytes); SEEKTABLE present; else `FREEZE_INVARIANT`.
10. `master_sha256` = sha256 of the frozen file.
11. MP3 (timeout 300 s): `gain_db = min(0, -1.0 - TP)`;
    `ffmpeg -nostdin -hide_banner -v error -protocol_whitelist file,pipe -i file:<work>/master.flac -map 0:a:0 -map_metadata -1 -fflags +bitexact -flags:a +bitexact [-af volume=<gain_db>dB] -c:a libmp3lame -b:a 320k -ar 48000 -write_xing 1 -f mp3 file:<work>/hq.mp3`
    (`-af` omitted when gain is 0; output is byte-deterministic for a given ffmpeg build).
12. Peaks file → `work/peaks.json`; sha both derived files.
13. Copy `Artifacts` into `work/artifacts/` (re-hash; mismatch ⇒ `SOURCE_CHANGED`).
14. **Claim final names atomically.** Every file is `Chmod(0444)` in `work` first, then hard-linked into
    place with `Root.Link(work/…, final)`, which fails with `EEXIST` instead of replacing. (`Root.Rename`
    replaces an existing target on POSIX, so with `Jobs = 2` a "check then rename" could clobber a frozen
    master.)
    - master → `audio/masters/<slug>__<seed>__<yyyymmdd-hhmm UTC of RecordedAt>.flac`; `EEXIST` ⇒ try
      `__2`, `__3`, … up to `__99` (then `IO`);
    - mp3/peaks → `derived/<aa>/<sha>.<ext>`; `EEXIST` ⇒ identical content already exists
      (content-addressed) ⇒ reuse it; it is **not** added to `Created`;
    - artifacts → `takes/<TrackID>/<name>` (`EEXIST` ⇒ `IO`).
    Each successful link appends its path to `Result.Created`.
15. `RemoveAll(work)`. Any error after step 14 began ⇒ `Discard` of what was claimed so far, then return
    the error.

**Discard(r)** removes exactly the master and `takes/<TrackID>/…` paths listed in `r.Created` (and the
then-empty `takes/<TrackID>/`). It never removes `derived/` files, not even ones this call created:
content-addressed files may meanwhile be linked by another ingest's rendition row (D-M1-10).
Unreferenced derived files show up as `library_orphans` (C16.7). Discard never touches a path that is
not in `Created`.

Rendition params: mp3 `{"encoder":"libmp3lame","bitrate":"320k","sample_rate":48000,"gain_db":-0.6,"ffmpeg":"n9.0.1"}`;
peaks `{"format":"mm.peaks/1"}`.

### C16. Importer: `internal/importer`

**C16.0 Inbox access** (`internal/importer/inbox`):
```go
type FileID struct{ Dev, Ino uint64; Size int64; ModTime time.Time } // from Lstat during the scan
type ReadOnly struct{ /* private *os.Root on the inbox dir */ }
func Open(dir string) (*ReadOnly, error)
func (r *ReadOnly) FS() fs.FS // = root.FS(): ReadDir / Stat / Lstat (fs.ReadLinkFS); no write method is reachable
func (r *ReadOnly) OpenRegular(rel string, want FileID) (fs.File, error)
// Opens with O_RDONLY|O_NONBLOCK|O_CLOEXEC (+O_NOFOLLOW where supported), so a FIFO swapped in after the
// scan never blocks; then fstat must show a regular file whose (dev, ino, size, mtime) equals want,
// else ErrChanged (⇒ SOURCE_CHANGED for ingest, `changed` for parsing). The returned file is read-only.
```
The importer receives the inbox only as `*inbox.ReadOnly`; ingest receives already-opened files (C15).
There is no code path from the importer or ingest to a writable handle on the inbox.

```go
type Options struct {
    Inbox       *inbox.ReadOnly
    InboxLabel  string        // goes into reports; never an absolute path
    MappingPath string        // optional CSV (C16.6); default <DataDir>/import/mapping.csv if it exists
    MinAge      time.Duration // default 2m; younger files are "unsettled" (secondary guard, C16.1)
    Jobs        int           // concurrent ingests, default 2
    Principal   string        // 'cli' | 'seed-synthetic'
    Now         func() time.Time
}
type Ingester interface {
    Ingest(ctx context.Context, r ingest.Request) (ingest.Result, error)
    Discard(r ingest.Result) error
}
type ApplyOptions struct {
    ExpectDigest string // plan_digest of the reviewed dry-run report ("" only with Force)
    Force        bool   // CLI --yes: apply the current plan even if it differs (the diff is printed)
}
func New(st *store.Store, lib *library.Service, data *os.Root, ing Ingester, o Options) (*Importer, error)
func (im *Importer) Inventory(ctx context.Context) (*Inventory, error) // inbox reads + DB reads only
func (im *Importer) Apply(ctx context.Context, inv *Inventory, ao ApplyOptions) (*RunResult, error)
func WriteMappingTemplate(w io.Writer, inv *Inventory) error
```
For a dry-run `st` comes from `store.OpenReadOnly` (or is nil after `ErrNoDatabase` ⇒ empty library),
`data` is used only to write under `import/`, and `ing` is nil.

**C16.1 Scan & classify.** `fs.WalkDir(inbox.FS(), ".")` in lexical order. Before classification, per
entry (each case has a test):
- **symlinks** — every symlink, whether it points inside or outside the root ⇒ `ignored:symlink`, never
  followed (the type comes from the directory entry; `fs.Lstat` when the type is unknown);
- **non-regular files** (FIFO, socket, device) ⇒ `ignored:not_regular`, never opened (opening a FIFO
  blocks forever);
- **unreadable** directories or files (`EACCES`, …) ⇒ error `unreadable` for that path; the walk
  continues (an inbox written by another uid with mode 0640 is a normal deployment mistake);
- **non-UTF-8 names** ⇒ warning `non_utf8_name`; the name is never printed or stored: reports, logs and
  the mapping file refer to the file as `sha256:<content hex>` when it was hashed, else as
  `name:<hex sha256 of the raw path bytes>`;
- extensions match **case-insensitively** (`.FLAC`, `.Jsonl`); a leading UTF-8 **BOM** in JSON, JSONL, CSV
  and ABC content is stripped (warning `bom`).

**Read caps** (exceeded ⇒ error `too_large` for the file, which is not parsed): JSONL 64 MiB per file and
1 MiB per line (`line_too_long` for that line only); JSON 4 MiB; CSV 16 MiB; ABC 1 MiB. FLAC, MP3 and
artifacts are streamed while hashing, never loaded. **Only candidate files are hashed** (recipe sources,
collection and reference files, ABC, FLAC — plus frames sha and STREAMINFO — and result-dir artifacts);
ignored files are listed with their size only.

**Result dirs** (pre-pass): a directory containing exactly one FLAC and a `request.json` that parses as a
recipe. Only its **top-level** regular files with safe names (`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`) can be
artifacts; unsafe names ⇒ `ignored:unsafe_name`; subdirectories ⇒ warning `result_subdir` (their files
are classified by the general rules, never as artifacts).

Classification (first match wins; `abc_source` is decided after recipe parsing, C16.2):

| Kind | Rule | Use |
|---|---|---|
| `ignored:temp` | any path component starting with `.` (incl. rsync `.<name>.<6 chars>`), suffix `.part` `.partial` `.tmp` `.crdownload` `~`, `__MACOSX/`, `Thumbs.db`, `.DS_Store` | ignored |
| `ignored:archive` | `.zip` `.tar` `.tgz` `.tar.gz` `.tar.xz` `.txz` `.tar.bz2` `.tbz2` `.tar.zst` `.7z` `.rar` | ignored; never opened or extracted (no zip-slip exposure) |
| `ignored:unsettled` | mtime newer than `now − MinAge`, or size changed between stat and hash | skipped this run. Secondary guard only: rsync preserves old mtimes and `--partial` leaves a truncated file under its final name — C16.2 and `flac --test` are the real guards |
| *inside a result dir* | FLAC → `audio_flac`; `request.json` → `request_json` **and** artifact; `result.json` → `yue2_result` **and** artifact; `latent.npy` → `ignored:latent` (D-M1-1); `*.abc` → `score_abc` **and** artifact; other safe top-level files → `artifact` | copied to `takes/<id>/` |
| `abc_source` | `*.abc` that some recipe's `abc_path` resolved to | inlined into that recipe; not a score |
| `recipes_jsonl` | `*.jsonl` | recipe source (one recipe per non-blank line) |
| `recipes_json_array` | `*.json` whose top level is an array of objects | recipe source: each element one recipe (`raw_kind` `json_array_item`, `line_no` = 1-based index); an array with any non-request element ⇒ the whole file `ignored:json` + warning `json_array_not_requests` |
| `request_json` | `*.json` object with `lyrics` and `style`/`tags` | recipe source (`json_file`; `AllowTitle`) |
| `collection_json` | `*.json` object with a `songs` array whose items have `variant_ids` | draft collection |
| `reference_index` | `*.csv` whose header contains `variant_id` | `recipe_refs` |
| `ignored:reference_playlist` | `*.json` object with `playlist` and `tracks` keys | ignored (never parsed beyond the key check) |
| `ignored:json` | any other `*.json` | ignored |
| `score_abc` | `*.abc` | score |
| `audio_flac` | `*.flac` starting with `fLaC` (else error `not_flac`) | take |
| `ignored:mp3_sidecar` | `*.mp3` whose stem equals the stem of FLAC files that all share one audio identity, in **any** directory | ignored (hq.mp3 is regenerated); listed under that audio's `sidecars` |
| `ignored:orphan_mp3` | any other `*.mp3` | ignored + warning `orphan_mp3` (with candidate FLACs when the stem is ambiguous) — never silently dropped |
| `ignored:unknown` | everything else (incl. `*.npy` outside result dirs) | ignored |

**Audio identity and duplicates.** Each FLAC's identity is `AudioID(frames_sha256, total_samples)`
(C13). FLACs with the same identity — byte-identical copies, re-tagged copies (e.g. tags added by a
player) and `flac/<slug>.flac` copies of a result-dir `audio.flac` alike — form one `duplicate_audio`
group and are processed once. A non-zero STREAMINFO MD5 shared by two different identities ⇒ warning
`possible_reencode` (both processed).

**C16.2 Recipes.**
- JSONL lines are split on `\n`; a trailing `\r` stays in the raw bytes and adds warning `crlf`; blank
  lines are skipped. **A file whose last line has no trailing `\n`** ⇒ warning `unterminated_last_line`
  and that line is skipped this run (it may be a truncated rsync `--partial`/`--inplace` transfer that
  kept an old mtime). The report says so; the line imports on a later run once the file ends with a
  newline.
- `abc_path` resolver (passed to `ParseRequest`): the value must be valid UTF-8, `path.Clean(v) == v`,
  relative, with no `..` element and no backslash; it resolves to `path.Join(path.Dir(<recipe file>), v)`
  and is opened through `inbox.OpenRegular` (regular file, ABC cap, valid UTF-8). Any failure ⇒
  `abc_path_unresolved` (line invalid). The target is classified `abc_source`.
- Each source → `yue2.ParseRequest`. Invalid → `errors[]` with path/line/code (never field values),
  never fatal. Identity = canonical `Hash`. **Every occurrence** (path, line, raw bytes) is recorded:
  the report lists all sources of each recipe, and apply writes one `recipe_sources` row per occurrence
  (C11) — there is no "first source wins", so M2a can re-emit any source byte-identically.
- Same `request_id` with different hashes → warning `id_conflict` (both imported).
- Recipes already in the DB (hash) → `already_imported`; their new occurrences still get
  `recipe_sources` rows.

**C16.3 Audio ↔ recipe matching** (precedence; the first rule that yields exactly one recipe wins):

| # | Rule | Condition |
|---|---|---|
| 1 | `mapping` | mapping row for this audio path or `sha256:<hex>` (C16.6); `request_id` (+ `seed` when ambiguous) must resolve uniquely; `-` ⇒ skip audio (`placed: skipped`) |
| 2 | `result_dir` | audio sits in a result dir ⇒ that dir's `request.json` recipe |
| 3 | `tag` | Vorbis comment `MM_REQUEST_ID`, else `MM_RECIPE_ID` (re-import of our own export), else a `COMMENT`/`DESCRIPTION` value equal to a known request id |
| 4 | `filename_id` | file stem == a request id (case-sensitive) |
| 5 | `sidecar_json` | `<stem>.json` request file in the same dir or in any `requests/` dir |
| 6 | `slug` | `Slug(stem)` == `Slug(request_id)` |
| 7 | `slug_seed` | stem ends with a seed hint (`[-_]s?<digits>`), and `Slug(stem minus hint)` == `Slug(request_id)` and the seed matches |
| 8 | `title_slug` | `Slug(stem)` — or `Slug(TITLE tag)` when the file has one — equals `Slug(title)` of exactly one per-song JSON recipe |
| — | `group_slug` | `Slug(stem)` == `Slug(GroupKey(request_id))` — **produces candidates only, never a match** |

- More than one candidate at the deciding rule ⇒ `ambiguous` (candidates listed, not imported).
- No rule ⇒ `unmatched` with up to 5 candidates (`group_slug` hits, same `GroupKey` slug prefix,
  Levenshtein ≤ 3 on slugs). Both feed `mapping.todo.csv`.
- **Duplicate groups:** the rules are evaluated for every member path, and the group takes the
  strongest (lowest-numbered) rule that yields exactly one recipe on any member — a result-dir
  `audio.flac` beats a `flac/<slug>.flac` copy regardless of lexical order. Members that yield different
  recipes at that strongest rule ⇒ `ambiguous`. The representative (the file whose bytes are ingested)
  is the member that produced the deciding match (ties: lexical path).
- Audio whose identity already exists in `tracks` ⇒ `already_imported` (this covers a re-tagged copy of
  an imported take).

**C16.4 Placement.**
- **Tracks:** one per matched audio identity. Title precedence: mapping `title` → per-song JSON `title`
  → `TITLE` tag → humanised stem (`_`/`-` → space, first letter upper; stem `audio` ⇒ humanised request
  id). `recorded_at` = inbox mtime of the representative (rsync preserves it).
- **Drafts:** at apply, **every recipe of the inventory that has no committed track** gets a draft (if it
  has none yet) — including recipes whose audio is planned in this same run. Drafts are written before
  collections and before any take. `origin` = `varianti_import` when one of the recipe's source paths
  has a `varianti` path component, else `import_backlog`; `group_key = GroupKey(request_id)`; `label` =
  reference `variant_type` if present, else the stripped suffix (`v2`), else NULL. A planned take that
  then fails ingest therefore leaves its recipe with a backlog draft and intact collection memberships.
- **Fulfilment** (inside the take's transaction): the backlog draft of the take's own recipe becomes
  `fulfilled` (`fulfilled_by_track_id` = this take; later takes of the same recipe do not change it).
  Fallback: backlog drafts whose recipe has the same `core_hash` (request_id, seed, style, lyrics, cot)
  as the take's recipe are fulfilled too, with warning `fulfilled_by_core_match` naming the differing
  canonical keys (e.g. a result-dir `request.json` with an explicit non-default `cfg_scale`).
- **Equivalence:** importing everything in one run yields the same library as importing it
  incrementally as files arrive, except lineage roots kept stable by `older_than_root` (C16.5) —
  `TestAcceptance_M1_AllAtOnceEqualsIncremental`.
- **Varianti mothers:** `parent_track_id` = the unique `root_track_id` among tracks whose recipe
  `GroupKey` equals the draft's `GroupKey` (a mother with siblings still resolves, because siblings share
  their root). No such track ⇒ NULL + warning `mother_not_found`; several roots ⇒ NULL + warning
  `mother_ambiguous`; a `draft:` mapping row (C16.6) overrides. Every apply ends with
  `RelinkVariantiMothers`, which fills NULL links once the mother arrives.
- **Scores:** a result-dir `score.abc` → that take. A standalone `<name>.abc` attaches only when `<name>`
  equals the audio stem of exactly one take, or `<name>` = `<request_id>` + a seed hint (`-s<seed>`,
  `_s<seed>`, `-seed<seed>`, `_seed<seed>`) matching exactly one take's (request_id, seed) — **never by
  request id alone**, because with `cot=full` every seed renders its own score. Otherwise warning
  `score_unattached`. Parsed with `yue2.ParseScore`, aligned via `AlignSections`; `plan` section rows
  (confidence 0.7) only when aligned; `lyrics` section rows always.
- `yue2_result` ⇒ `truncated_abc/semantic` from a `truncated` **object** `{abc, semantic}` of bools; any
  other shape ⇒ NULL + warning `truncated_not_dict`.
- **Reference index** (RFC 4180, CRLF or LF, BOM stripped): a row attaches as `recipe_refs` to every
  recipe with that request id **whose seed equals the row's `seed`** (empty cell ⇒ attach); a mismatch
  ⇒ warning `ref_seed_mismatch` (row not attached, counted under `metadata[].unmatched`). Only
  `variant_id`, `seed`, `variant` and `variant_type` are interpreted; all other columns are opaque private
  values (C16.9).
- **Collections:** a `collection_json` ⇒ collection named by its JSON `name` (fallback: file stem),
  members = its `variant_ids` that resolve to drafts. A recipes JSONL whose every line duplicates a line of
  another recipe source (a subset such as a pilot) forms a collection named after its file stem **only
  when no collection JSON covers exactly the same set of ids** — so a pilot JSONL next to its pilot
  JSON yields one collection, not two.

**C16.5 Siblings.** Tracks sharing a `request_id` (rule `same_request_id`), else tracks whose
`SeedFamily(request_id)` is equal (rule `seed_family`: `x`, `x-s1101` and `x_seed7` are one family),
form a sibling set; the earliest by (`recorded_at`, then `frames_sha256`) is the root; the others get
`parent_track_id = root`, `generation = 1`, and a `mutations` row `operator='inferred_sibling',
inferred=1, params_json={"rule":…}`. **Variant ids (`<prefix>_NNN_v1..vK`) never create lineage** — they
are independent recipes that only share a `group_key`. On re-runs new takes attach to the existing root
(existing lineage is never rewritten, even if the newcomer is older; warning `older_than_root`). Mapping
column `parent` overrides (`-` ⇒ root; a request id ⇒ the unique root of that request's takes; an audio
path or `sha256:` key ⇒ that take; rule `mapping`). The dry-run lists every proposed edge.

**C16.6 Mapping file** (`<DataDir>/import/mapping.csv`, UTF-8, RFC 4180, `#` comment lines):
```
audio,request_id,seed,title,parent
music/flac/some_take.flac,syn-song-01,1001,,
sha256:3f…9a,-,,,
draft:syn-song-01-v2,,,,syn-song-01
```
- `audio` is one of: a **clean inbox-relative path** (valid UTF-8, `path.Clean(p) == p`, not absolute, no
  `..` element, no backslash, and `fs.Lstat` through `inbox.FS()` finds a regular file); `sha256:<64 hex>`
  (the content sha of any member of a duplicate group); or `draft:<request_id>` — a **draft row** for
  varianti mothers (`request_id`/`seed`/`title` must be empty; `parent` = a request id ⇒ the unique root
  of that request's takes, an audio path / `sha256:` key ⇒ that take, `-` ⇒ no mother and no
  `mother_not_found` warning). Anything else ⇒ row error `bad_mapping_row` (reported; row ignored).
- `mapping.todo.csv` has the same columns plus a trailing `candidates` column (ignored on read) and is
  pre-filled for every unmatched/ambiguous audio and every varianti draft with `mother_not_found` or
  `mother_ambiguous`.

**C16.7 Inventory / report** (`format: "mm.inventory/1"`):
- `report_id` = UUIDv7 (for an applied run also `import_runs.id`).
- `plan_digest` = hex sha256 of the canonical JSON (sorted keys and arrays) of everything apply would
  write: recipes (hash, placement), recipe sources (hash, path, line, raw_sha256), refs (hash, source,
  sha256 of ref_json), drafts (recipe hash, origin, group_key, label, mother), collections (name,
  member hashes), takes (audio id, representative path and sha256, rule, recipe hash, title, parent
  edge, score, artifact names and shas). Timestamps and ids are excluded, so an unchanged inbox gives an
  unchanged digest.
- File `<DataDir>/import/reports/<yyyymmddThhmmssZ>-<report_id>[-dry-run].json`, created with `O_EXCL`,
  mode 0600, never overwritten (two runs in the same second cannot collide). Applied runs also store it
  as `import_runs.report_json`.
```json
{"format":"mm.inventory/1","report_id":"…","generated_at":"…","inbox":"inbox","dry_run":true,"plan_digest":"…",
 "summary":{"files":0,"bytes":0,"by_kind":{},"recipes_valid":0,"recipes_unique":0,"recipe_sources":0,
   "recipe_lines_invalid":0,"recipes_already_imported":0,"audio_files":0,"audio_identities":0,
   "audio_matched":0,"audio_ambiguous":0,"audio_unmatched":0,"audio_skipped":0,"audio_already_imported":0,
   "audio_invalid":0,"duplicate_groups":0,"tracks_new":0,"drafts_new":0,"drafts_fulfilled":0,
   "siblings_inferred":0,"scores_attached":0,"refs_attached":0,"collections":0,"ignored":0,"unsettled":0,
   "warnings":0,"errors":0,"library_orphans":0},
 "recipe_sources":[{"path":"…","kind":"recipes_jsonl","lines":0,"valid":0,"invalid":[{"line":3,"code":"not_object"}],"warnings":[]}],
 "recipes":[{"request_id":"…","hash":"…","sources":["path:line"],"placement":"track|draft_backlog|draft_varianti|already_imported","group_key":"…","collections":[],"has_reference":true}],
 "audio":[{"path":"…","sha256":"…","audio_id":"…","bytes":0,"duration_s":0,"sample_rate":48000,"bits":24,"channels":2,
   "status":"new|already_imported|duplicate|invalid|unsettled|skipped|unmatched|ambiguous",
   "match":{"rule":"slug","request_id":"…","recipe_hash":"…"},"candidates":[],"title":"…",
   "parent":{"rule":"same_request_id","of":"<path or track id>"},"score":"…","artifacts":[],"sidecars":[]}],
 "duplicates":[{"audio_id":"…","paths":[],"representative":"…","rule":"result_dir"}],
 "drafts":[{"request_id":"…","origin":"import_backlog","group_key":"…","mother":null}],
 "metadata":[{"path":"…","kind":"reference_index|collection","rows":0,"matched":0,"unmatched":0}],
 "ignored":[{"path":"…","reason":"temp|archive|unsettled|mp3_sidecar|orphan_mp3|reference_playlist|json|latent|unknown|symlink|not_regular|unsafe_name"}],
 "warnings":[{"path":"…","line":0,"code":"…"}],
 "errors":[{"path":"…","line":0,"code":"…","message":"…"}],
 "library_orphans":["audio/masters/…","derived/…"]}
```
`library_orphans` = files under `audio/masters/` and `derived/` (except `derived/probe/`) not referenced
by `tracks`/`renditions` (reported, never deleted). Reports contain no absolute paths, never print
non-UTF-8 names (C16.1) and never contain reference values (C16.9). CLI stdout prints the summary as
aligned text plus the unmatched/ambiguous lists and the next command; every printed path has
non-printable characters escaped (C0/C1 controls and bidi controls U+202A–U+202E, U+2066–U+2069 shown
as `\x..`/`\u....`).

**C16.8 Apply.**
- `BeginRun` refuses with `IMPORT_IN_PROGRESS` while a `running` run has `heartbeat_at` newer than
  `now − 2 min`; a `running` run with an older heartbeat is marked `failed` (`error = "stale: no
  heartbeat"`). The running importer refreshes `heartbeat_at` every 30 s; there is no age-based rule.
- **Plan binding:** apply recomputes the inventory and compares its `plan_digest` with
  `ApplyOptions.ExpectDigest`; on mismatch without `Force` ⇒ `PLAN_CHANGED` before anything is written
  (the CLI prints the added/removed plan entries). Files that arrived after the reviewed dry-run are
  therefore never imported unreviewed (M1 has no purge).
- Order: run row (`running`) → recipes + `recipe_sources` (one tx per source file) → refs (one tx) →
  drafts for every recipe without a committed track (one tx) → collections (one tx; `draft_collections`
  references drafts) → takes (`Jobs` concurrent ingests; each take's rows — track, style terms,
  renditions, analyses, artifacts, score, sections, mutation, draft fulfilment — in **one** tx; tx
  failure ⇒ `Discard`) → `RelinkVariantiMothers` (one tx) → finish.
- Per-take failures are recorded and do not stop the run. `completed` even with per-file errors;
  `failed` only on fatal errors.
- Idempotent: a second run over the same inbox inserts nothing except its own `import_runs` row.
- Stale `incoming/ingest-*` dirs older than 24 h are swept at apply start (never by a dry-run).

**C16.9 Reference-metadata privacy (binding).**
- Private reference values: every reference-CSV column other than `variant_id`, `seed`, `variant`,
  `variant_type` (e.g. `source_title`, `source_artist`, `track_n`); every collection-JSON field other
  than `name` and `variant_ids` (e.g. `title`, `artist`, `why`, `rule`); the whole reference playlist
  (`owner`, `url`, titles, artists).
- Stored only in `recipe_refs.ref_json` (collection-JSON extras and playlists are not stored at all).
  Never used for matching, titles, slugs, FTS, logs, reports, mapping files, CLI output or error
  messages (errors cite path, line and column name, never a value).
- `get_draft.reference` is returned to **human principals only**; agents get `reference: null` with
  `has_reference` (MCP output ends up in third-party transcripts).
- OpenAPI `example`/`doc` strings use synthetic values only.
- Importer logs carry only codes, counts and inbox-relative paths.
- Enforced by `TestPrivacy_ReferenceMetadataConfined` (M1-B9).

### C17. Library service: `internal/library` + `librarytest`

```go
type Service struct{ /* st *store.Store; data *os.Root; now func() time.Time */ }
func New(st *store.Store, data *os.Root, now func() time.Time) *Service
var ErrNotFound = errors.New("library: not found") // reads; Resolve returns serve.ErrNotFound instead

// Writes (importer only in M1); tx from st.W.BeginTx; SQL forms exactly as in C11.
func (s *Service) InsertRecipe(ctx context.Context, tx *sql.Tx, p yue2.Parsed) (id string, created bool, err error)
func (s *Service) InsertRecipeSource(ctx context.Context, tx *sql.Tx, recipeID string, src SourceRef, runID string) (created bool, err error)
func (s *Service) InsertRefs(ctx context.Context, tx *sql.Tx, recipeID, source string, ref map[string]string) error
func (s *Service) InsertDraft(ctx context.Context, tx *sql.Tx, d NewDraft) (id string, created bool, err error)
func (s *Service) AddToCollection(ctx context.Context, tx *sql.Tx, draftID, name, source string) error
func (s *Service) InsertTake(ctx context.Context, tx *sql.Tx, t NewTake) error // track+terms+renditions+analyses+artifacts+score+sections+mutation+fulfilment (own recipe + core_hash fallback)
func (s *Service) BeginRun(ctx context.Context, principal, inboxLabel, planDigest string) (runID string, err error) // C16.8 guard
func (s *Service) Heartbeat(ctx context.Context, runID string) error
func (s *Service) FinishRun(ctx context.Context, runID, state string, summary, report []byte, runErr error) error
func (s *Service) RelinkVariantiMothers(ctx context.Context, tx *sql.Tx) (int, error) // C16.4
func (s *Service) Snapshot(ctx context.Context) (LibrarySnapshot, error) // one read tx: hashes, audio ids, drafts, roots (importer/dry-run)

// Reads (ops)
func (s *Service) SearchTracks(ctx context.Context, p SearchParams) (TrackPage, error)
// ImportRuns and DeviceProfiles are paged like every list (limit, opaque cursor, 90 000-byte cap):
func (s *Service) ImportRuns(ctx context.Context, limit int, cursor string) (ImportRunList, error)
func (s *Service) DeviceProfiles(ctx context.Context, limit int, cursor string) (DeviceProfileList, error)
// Import edge: library also imports huma/v2 (schema.go: inline nullable sub-object schemas for
// Truncation, ScoreInfo, MP3Info, LoudnessOut, SpectrumOut, DraftSource); no other web framework code.
func (s *Service) Facets(ctx context.Context) (Facets, error)
func (s *Service) Track(ctx context.Context, id string) (TrackDetail, error)
func (s *Service) Peaks(ctx context.Context, id, level string) (PeaksLevel, error)
func (s *Service) Media(ctx context.Context, trackID string) (MediaSet, error)        // shas, bytes, types
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error)  // implements serve.Resolver
func (s *Service) ListDrafts(ctx context.Context, p DraftParams) (DraftPage, error)
func (s *Service) Draft(ctx context.Context, id string, withReference bool) (DraftDetail, error)
func (s *Service) Stats(ctx context.Context) (Stats, error)
func (s *Service) ImportRuns(ctx context.Context, limit int) ([]ImportRunSummary, error)
func (s *Service) ImportRun(ctx context.Context, id, section, cursor string, limit int) (ImportRunPage, error)
func (s *Service) PutDeviceProfile(ctx context.Context, p auth.Principal, in DeviceProfileIn) (DeviceProfile, error)
func (s *Service) DeviceProfiles(ctx context.Context) ([]DeviceProfile, error)
```
`NewTake` is the library's own struct (track fields, terms, renditions, analyses as typed values, artifacts,
score, sections, mutation); the importer maps `ingest.Result` into it — `library` never imports
`ingest`. Import graph: `library` → `serve` (for `Object`), `store`, `auth` (`Principal`), `yue2`,
`analysis` (types); `serve` → `auth`, `problem`; `serve` imports neither `library` nor `api`.
(No title-edit API in M1; acceptance tests change a title with `UPDATE tracks SET title=? WHERE id=?`
through `st.W`, which the triggers allow.)
FTS query building (`fts.go`): split `q` into Unicode letter/digit runs, max 8 tokens, each emitted
as `"tok"*`, joined by spaces (implicit AND); empty ⇒ no FTS clause. User input never reaches FTS5
syntax. Ranking `bm25(tracks_fts, 0, 10, 2, 2, 1)` (track_id, title, request_id, style, lyrics).
Cursor = opaque base64url of `{"o":<offset>}` (keyset can replace it later without an API change).
**Page cap:** list reads stop adding items once the serialized page would exceed 90 000 bytes and set
`next_cursor` there, so every response stays under the design's ~100k-character MCP result cap.

`internal/library/librarytest` (**test-only**, owner M1-B4):
```go
type Fixtures struct{ Tracks, Drafts, Runs, Devices, Recipes []string } // ids, deterministic order
func Fixture(tb testing.TB, st *store.Store, now time.Time) Fixtures
// Inserts directly (no ffmpeg): 5 tracks (one with an aligned score, one untimed, two siblings, one
// truncated), 6 drafts in 2 groups, collection "pilot-x" (3 drafts), 1 completed import run with a small
// report, 1 device profile. Used by ops, parity and library tests. Ids are FIXED constants (UUIDv7-shaped,
// exported by librarytest), identical on every call: M0's parity harness keys its fixtures by operation
// id in a static `parityArgs` map and runs mutating ops on two fresh rigs, so ids cannot be random.
func WriteFixtureDB(tb testing.TB, path string) // `make fixture-db` (go test -run TestWriteFixtureDB, MM_FIXTURE_DB=<path>)
// lets the web lane run against a fixture library before seed-synthetic exists (M1-B9): `make fixture-db`
// writes ./data/fixture/metamusic.db (data/ is gitignored) and M0's dev command serves it with
// `go run ./cmd/metamusic dev --data-dir ./data/fixture` (metadata only; media requests fail: there are no files).
// NOTE: this needs serve() to construct library.Service and serve.Signer; see the B4 Done line.
```

### C18. Media URLs and handler: `internal/media/serve`

```go
type Signer struct{ /* key []byte; now func() time.Time */ }
func NewSigner(secretKey []byte, now func() time.Time) *Signer // key = HMAC-SHA256(secretKey, "metamusic/media-url/v1")
type Link struct{ URL string; Expires time.Time }
func (s *Signer) Sign(sha, ext string, ttl, bucket time.Duration) Link
func (s *Signer) Verify(sha, ext, exp, sig string) error // ErrLinkInvalid | ErrLinkExpired
func TTLFor(p auth.Principal) (ttl, bucket time.Duration) // human: 12h, 1h; anything else: 15m, 1m
// Owned here (not in library) to avoid the library⇄serve cycle:
type Object struct{ RelPath, ContentType, ETag string; ModTime time.Time }
type Resolver interface{ Resolve(ctx context.Context, sha, ext string) (Object, error) }
var ErrNotFound = errors.New("serve: no such object") // Resolver result for unknown objects ⇒ 404
func Handler(s *Signer, data *os.Root, res Resolver, probe *Object) http.Handler
```
- URL: `/media/<sha>.<ext>?exp=<unix>&sig=<b64url>`; `sha` = 64 lowercase hex; `ext ∈ {flac, mp3}`.
- `exp = ceil((now + ttl) / bucket) * bucket` (stable URLs within a bucket, so the browser cache works).
- `sig = base64url_nopad(HMAC-SHA256(MediaKey, "v1\n" + sha + "\n" + ext + "\n" + exp))` (43 chars),
  compared with `hmac.Equal`.
- Checks in order: method GET/HEAD (else 405 problem `METHOD_NOT_ALLOWED` + `Allow: GET, HEAD`, the code
  M0's `api.notFound` uses for 405); path regex (else 404 problem
  `NOT_FOUND`); `exp`/`sig` present and well-formed, MAC valid (else 403 `MEDIA_LINK_INVALID`);
  `exp < now` ⇒ 403 `MEDIA_LINK_EXPIRED`; `exp > now + 14h` ⇒ 403 `MEDIA_LINK_INVALID`; resolve
  (`ErrNotFound` ⇒ 404 `NOT_FOUND`); open the file through `data` — a resolved object whose file is
  missing or unreadable ⇒ 500 `INTERNAL`, logged with sha and ext (never the query). Extra query params
  are ignored. The principal is irrelevant (bearer errors are still rejected earlier by the C3
  middleware, row 1). Problem bodies via `internal/problem`.
- Response: `Content-Type` set explicitly (`audio/flac`, `audio/mpeg`), `Accept-Ranges: bytes`,
  `ETag: "<file sha256>"`, `Cache-Control: private, max-age=31536000, immutable`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `Cross-Origin-Resource-Policy: same-origin`, then `http.ServeContent(w, r, "", obj.ModTime, f)`
  (Range, If-Range, If-None-Match, HEAD). Multi-range requests are answered as
  `multipart/byteranges` whose parts carry the audio content type (D-M1-9). Never compressed. Logs
  carry the path only, never the query.
- Dot-segment paths (`/media/x/../y`) never reach the handler: M0's `auth.Classify` puts any path with a
  `..` segment in ClassUI (anonymous ⇒ proxy mode 401 plain text "Not authenticated", local mode 303 to
  `/auth/login`), and with a principal (e.g. a bearer, C3 row 1) ServeMux answers 301 to the cleaned
  path; tests assert "never 200, never file bytes".
- Resolution: `flac` ⇒ `tracks.master_sha256`; `mp3` ⇒ `renditions(kind='mp3_320').sha256`; the probe
  object (registered at startup) by its sha.

### C19. Operations (all `Cost: "free"`; registered via `api.Register`; JSON snake_case)

| OperationID | Method + path | Scope | MCP | UI | Output (abridged; full types in `internal/api/types_m1.go`) |
|---|---|---|---|---|---|
| `search_tracks` | GET `/api/tracks` | read | tool | `/` | `{items: TrackSummary[], total, next_cursor}` |
| `get_library_facets` | GET `/api/library/facets` | read | generic | `/` | `{style_terms:[{term,count}] (top 50), statuses:[{status,count}], duration_s:{min,max}, recorded_at:{min,max}}` |
| `get_track` | GET `/api/tracks/{id}` | read | tool | `/tracks/:id` | `TrackDetail` |
| `get_track_peaks` | GET `/api/tracks/{id}/peaks` | read | generic | `/tracks/:id` | `{track_id, level, duration_s, peaks: Waveform}` |
| `get_media_links` | GET `/api/tracks/{id}/media` | read | tool | `/tracks/:id` | `{track_id, expires_at, ttl_s, flac:{url,content_type,bytes,sha256,sample_rate,bits_per_sample}, mp3:{url,content_type,bytes,sha256,bitrate_kbps}}` |
| `list_drafts` | GET `/api/drafts` | read | tool | `/backlog` | `{items: DraftSummary[], total, groups_total, next_cursor}` |
| `get_draft` | GET `/api/drafts/{id}` | read | tool | `/backlog/:id` | `DraftDetail` (`reference` human-only, C16.9) |
| `get_stats` | GET `/api/stats` | read | tool | `/` | `Stats` |
| `list_import_runs` | GET `/api/imports` | read | generic | `/import` | `{items: ImportRunSummary[], next_cursor}` (query `limit`, `cursor`; page-capped, C17) |
| `get_import_run` | GET `/api/imports/{id}` | read | generic | `/import` | `{run: ImportRunSummary, section, items[], total, next_cursor}` |
| `get_device_probe` | GET `/api/device-probe` | read | generic | `/` | `{version:1, url, content_type:"audio/flac", sample_rate:48000, bits_per_sample:24, channels:2, duration_s:3.0, seek_to_s:1.5, expires_at}` |
| `put_device_profile` | PUT `/api/device-profiles/{device_id}` | write | generic | `/settings` | `DeviceProfile` (upsert; idempotent) |
| `list_device_profiles` | GET `/api/device-profiles` | read | generic | `/settings` | `{items: DeviceProfile[], next_cursor}` (query `limit`, `cursor`; page-capped, C17) |

(`/api/device-profiles` instead of `/api/devices`: M2a device pairing owns the "devices" name.)

Inputs:
- `search_tracks` query: `q` (≤200), `status` (repeatable enum), `style` (repeatable term, AND, ≤5),
  `min_duration_s`, `max_duration_s`, `recorded_from`, `recorded_to` (RFC 3339 date or date-time),
  `has_score` (bool), `truncated` (bool), `root_track_id`, `sort` ∈ `recorded_desc` (default),
  `recorded_asc`, `duration_desc`, `duration_asc`, `title_asc`, `relevance` (default when `q`),
  `limit` 1..100 (default 50), `cursor`.
- **Repeatable query params use explode:** Huma v2.39.1 turns `explode` off for query parameters by
  default (comma-separated) and always writes the flag into the OpenAPI document, while openapi-fetch
  sends `?style=a&style=b`. Every array query field is tagged `query:"style,explode"` /
  `query:"status,explode"`, so the OpenAPI document says `explode: true`. The MCP dispatcher already
  follows each parameter's flag (M0 C5 addendum; `internal/mcpgen/dispatch.go`, pinned by M0's
  `TestArrayQueryParamsReachHandlerIntact`): repeated keys when true, one comma-joined value when false
  (a non-exploded element containing a comma is refused with `VALIDATION_FAILED`); an absent flag means
  the OpenAPI default, true. M1 changes nothing in `mcpgen` dispatch.
- `get_track_peaks`: `level` ∈ `overview` (default) | `detail`.
- `list_drafts`: `q`, `status` (default `backlog`), `collection`, `group_key`, `origin`, `limit` 1..100,
  `cursor`; ordered by `group_key, request_id`.
- `get_import_run`: `section` ∈ `errors` (default) | `warnings` | `unmatched` | `ambiguous` | `audio` |
  `recipes` | `drafts` | `duplicates` | `ignored`, `limit` 1..200 (default 50), `cursor`. Never returns
  the full inventory (hundreds of recipes plus all audio would exceed the ~100k-character MCP cap).
- `put_device_profile` body: `{label, user_agent, quality, quality_source, probe_result, flac_can_play,
  flac_decode_ok?, flac_seek_ok?, probe_version, probed_at?}`; `device_id` must match
  `^[A-Za-z0-9_-]{8,64}$` (422 `VALIDATION_FAILED`); `quality` ∈ lossless|high|auto validated in Go.
- Every list response obeys the 90 000-byte page cap (C17).

Shapes:
```
TrackSummary {id, title, status, duration_s, recorded_at, created_at, request_id, seed, style_terms[],
  bpm_declared, has_score, timed, truncated (bool|null = abc||semantic), integrated_lufs, bandwidth_hz,
  root_track_id, parent_track_id, children_count}
TrackDetail {id, title, slug, status, status_set_by, origin, recorded_at, created_at, duration_s,
  audio:{sample_rate, bits_per_sample, channels, total_samples, master_sha256, master_bytes, audio_md5,
         mp3:{sha256, bytes, gain_db}},
  recipe:{id, engine, request_id, style, lyrics, cot, seed, hash, sources_count},
  style_terms[], bpm_declared,
  lineage:{root_track_id, parent_track_id, generation, operator, inferred, rule,
           siblings:[{id, title, seed, recorded_at}]},
  truncated:{abc, semantic}|null,
  score:{bars, meter, q, bpm, seconds_per_bar, aligned}|null,
  sections:[{idx, kind, label, raw, lines[], start_s|null, end_s|null, source, confidence|null}], timed,
  analysis:{loudness: Loudness+{replaygain_track_gain_db, version}, spectrum: Spectrum+{version}},
  import:{rule, run_id}}
DraftSummary {id, request_id, group_key, label, origin, status, seed, cot, style_excerpt (≤120 runes),
  style_terms[], section_kinds[], collections[], parent_track_id, fulfilled_by_track_id}
DraftDetail {id, origin, status, request_id, group_key, label, request:{id, style, lyrics, cot, seed},
  sections[] (lyrics structure), collections[], has_reference, reference: map[string]string|null (human only),
  source:{recipe_id, recipe_hash, sources:[{path, line, raw_kind}]}, parent_track_id, fulfilled_by_track_id,
  group:[{id, request_id, label, status}], created_at, updated_at}
Stats {tracks:{total, by_status{}, by_origin{}, duration_s_total, with_score, timed, truncated},
  backlog:{drafts, fulfilled, groups, collections:[{name, drafts}]},
  recipes:{total, sources},
  loudness:{n, integrated_lufs_min, integrated_lufs_max, spread_lu, listen_flac_recommended (spread_lu > 2.0)},
  bandwidth:{n, min_hz, median_hz, max_hz},
  storage:{masters_bytes, derived_bytes},
  imports:{runs, last_run_at, last_state}}
ImportRunSummary {id, started_at, heartbeat_at, finished_at, state, inbox_label, plan_digest, summary, errors, warnings}
DeviceProfile {device_id, label, user_agent, quality, quality_source, probe_result, flac_can_play,
  flac_decode_ok, flac_seek_ok, probe_version, probed_at, effective_format ("flac"|"mp3"), updated_at}
```
`effective_format`: `high` ⇒ mp3; `lossless` ⇒ flac; `auto` ⇒ flac (the client may downgrade on
cellular/saveData). Errors: 404 `NOT_FOUND`; 409 `MEDIA_UNAVAILABLE` (missing rendition row); 503
`UNAVAILABLE` with `details.component` ∈ `library` | `signer` | `probe` when that dependency is nil.
All three are returned as `api.Err(status, code, msg, details)`: M0's `codeFor` maps any status ≥ 500
raised through `huma.NewError` to `INTERNAL` and hides its message, so `UNAVAILABLE` must be explicit.

`api.Deps` (M0: `Store`, `Tokens`, `Cfg`, `Version`, `Commit`, `Started`) gains
`Library *library.Service`, `Signer *serve.Signer`, `Probe *serve.Object` and `Now func() time.Time`
(nil ⇒ `time.Now`; `Deps.Now` itself only drives `get_system_info` uptime. `updated_at` comes from
`library.Service`'s clock and link expiry from `serve.Signer`'s clock; both are authoritative and wiring
(C20 "same now") must pass them the same func as `Options.Now`, so tests fix one clock). **Registration never dereferences `Library`, `Signer` or `Probe`**:
`metamusic openapi` calls `api.New` with no database; handlers check for nil at call time and answer 503.

### C20. HTTP server and CLI additions

- `httpserver.Options` (M0: `Cfg`, `Sec`, `Store`, `Tokens`, `Sessions`, `MCP`, `MCPFactory`, `Started`,
  `SPA`, `Version`, `Commit`) gains (M1-B4 amends M0 C6): `Library *library.Service`,
  `Signer *serve.Signer`, `Probe *serve.Object`, `Media http.Handler` (nil ⇒ `/media/` answers 404
  problem `NOT_FOUND`, like M0's nil-`MCP` case), `Now func() time.Time`. `New` passes `Library`,
  `Signer`, `Probe` and `Now` into the `api.Deps` it builds (next to the existing `Started`; `MCPFactory`
  keeps receiving the same `huma.API` and inner mux) and mounts `Media` at `/media/` on the outer mux
  before the SPA catch-all `/` (ClassMachine, C3).
- `/manifest.webmanifest` served with `Content-Type: application/manifest+json`; `/icons/*` as files;
  both reachable anonymously (ClassPublic, unchanged). Both come from the embedded SPA through M0's
  `spaHandler` (`internal/httpserver/spa.go`, `http.ServeFileFS`), whose content type for
  `.webmanifest` depends on the host MIME table, so `spa.go` sets it explicitly.
- `serve` (and `dev`) open the DataDir root, build `library.Service`, `serve.Signer` (same `now`) and the
  media handler, and ensure the probe file (`synth.ProbeFLAC`, 3 s, 24-bit/48 kHz stereo, 440 Hz at
  −20 dBFS) if ffmpeg is available (else `Probe` is nil ⇒ `get_device_probe` 503). In M0 both commands
  share `boot()` and `serve()` in `cmd/metamusic/cmd_serve.go` (`cmdDev` lives there too, with its
  `--data-dir` flag); the wiring goes into `serve()`.
- `openapi` needs no config, database or DataDir — already true in M0 (`cmdOpenAPI` calls `api.New`
  with only `Version`/`Commit`; `TestOpenAPIGolden` runs with an empty env);
  `TestCLI_OpenAPIWithoutDatabase` pins that the M1 `Deps` additions keep it so.
- CLI harness (M0): `run(ctx, args, getenv, stdin, stdout, stderr) int` in `cmd/metamusic/main.go`
  (tests call it through the `do(t, env, stdin, args...)` helper in `main_test.go`); flag sets come from
  `newFlags(name, s)`; a returned `usageError` exits 2 and any other error exits 1. M1-B9 adds an
  `exitError{code int; err error}` that `run` maps to its code, so `import` can exit 1/2/3/4 as below
  (a fatal import error is 2, unlike M0's generic 1). `import` and the standalone `seed-synthetic`
  command call `config.Load` directly, not M0's `boot()`, which loads secrets, creates `DataDir` and
  opens (creates and migrates) the database: the dry-run must create nothing, and apply/seed open the
  store themselves with `store.Open`. (`dev --seed-synthetic` keeps using `boot()`.)
- New subcommands (stdlib `flag`, testable through `run`):
  - `import [--dry-run] [--plan REPORT] [--yes] [--inbox DIR] [--mapping FILE] [--report FILE]
    [--mapping-template FILE] [--min-age 2m] [--jobs 2] [--json]`
    - `--dry-run`: inventory only; DB through `store.OpenReadOnly` (missing DB ⇒ empty library); writes
      only under `<DataDir>/import/` (report, `mapping.todo.csv` when needed; creates `import/` and
      `import/reports/` if missing). It never creates the DB or its `-wal`/`-shm`, `incoming/`, `audio/`,
      `derived/` or `takes/`, never sweeps work dirs, never loads secrets.
    - apply (no `--dry-run`): requires `--plan <dry-run report>` (digest must match, C16.8) or `--yes`;
      neither ⇒ usage error telling the operator to run `--dry-run` first. `--plan` + `--yes` applies the
      current plan after printing the diff.
    - `--report FILE` writes an extra copy of the report to FILE (created `O_EXCL`); `--mapping-template
      FILE` writes the `mapping.todo.csv` content to FILE instead; `--json` prints the summary as JSON;
      `--inbox` overrides `MM_INBOX`; `--mapping` overrides the default mapping path.
    - Exit codes: **0** success, including warnings (unmatched, ambiguous, ignored, orphan MP3s, …);
      **1** per-file errors (invalid recipe lines, failed ingests, `unreadable`, `too_large`,
      `not_flac`, bad mapping rows); **2** usage or fatal; **3** `PLAN_CHANGED`; **4**
      `IMPORT_IN_PROGRESS`. Dry-run and apply use the same codes.
  - `seed-synthetic [--profile dev|e2e] [--seed 1] [--force]`: synthetic rows are permanent, so it
    refuses (exit 2) when any `import_runs` row with a principal other than `seed-synthetic` exists
    (always, even with `--force`), and unless the target is a dev or e2e data dir — `MM_E2E=1`, or
    invoked via `dev`, or `--force`. Then it generates `<DataDir>/inbox-synthetic` (only if absent) via
    `internal/synth` and imports it with `MinAge=0`, `Principal="seed-synthetic"`,
    `InboxLabel="synthetic"`, `Force=true`.
  - `dev --seed-synthetic` (new flag next to M0's `--data-dir`, default `./data/dev`): seeds when the
    library has zero tracks, then serves (warns and continues without a library if ffmpeg/flac/metaflac
    are missing). M0's dev bootstrap (auto-created secrets, one-time `dev` password) is unchanged.

### C21. Synthetic generator: `internal/synth`

Everything synthetic is generated at test/dev time by ffmpeg + Go; nothing from the real archive; no
audio is committed. Vocabulary: invented syllable words plus fixed edge strings (`è à ì`, `<`, `>`,
`&`, and the lyric line `<b>&` used by the `{@html}` check), no real lyrics. `synth` is
production code (`seed-synthetic`), so it uses `mediatool.Tools`, never `testtools`.

```go
type Tone struct{ LUFS float64; Seconds float64; BandLimitHz int /* 0 = full band */; BPM int; Variant int }
func FLAC(ctx context.Context, t mediatool.Tools, dst string, tone Tone, style FLACStyle) error
// FLACStyle: StyleFFmpeg (ffmpeg default: encoder tag + PADDING) | StyleYuE2 (no tags, no PADDING,
// no SEEKTABLE — mirrors the shape YuE2 writes). Audio: 2 sine partials (Variant shifts them, giving
// different frames) + click track at BPM, stereo, 48 kHz, 24-bit. Loudness is calibrated in two passes
// (render, measure with ebur128, apply the correction gain, re-render) to |I − LUFS| ≤ 0.05.
// Band limit: anoisesrc at 2·BandLimitHz + aresample=48000:resampler=swr (built-in; no soxr).
func Retag(ctx context.Context, t mediatool.Tools, src, dst string, comments []string) error // same frames, new tags
func EBU3341(ctx context.Context, t mediatool.Tools, dst string, caseN int) (expectedLUFS float64, err error)
// Case 1: stereo 1 kHz sine -23 dBFS 20 s → -23.0; case 2: -33 dBFS → -33.0;
// case 3: -36/-23/-36 dBFS for 10/60/10 s → -23.0; case 4: -72/-36/-23/-36/-72 for 10/10/60/10/10 s → -23.0.
// Generator: aevalsrc with per-channel amplitude 10^(dBFS/20) (no -ac upmix: it attenuates 3 dB).
func ProbeFLAC(ctx context.Context, t mediatool.Tools, dst string) error
func Archive(ctx context.Context, t mediatool.Tools, dir string, profile Profile, seed int64) (Manifest, error)
// Profiles "acceptance" (Go tests), "e2e" and "dev". The Manifest lists every generated file and the
// expected inventory/import counts; tests assert against it and against the numbers below.
```

**Profile `acceptance`** (exact; tests assert these numbers):
- `music/songs.jsonl` (sorted keys, `", "`/`": "`, `ensure_ascii=False`, LF, trailing newline): 10 valid
  lines + 1 invalid (`{not json`): `syn-song-01..07` (seeds 1001..1007), `syn-song-01` seed 2001 (same
  id, other seed), `syn-song-02-s2002` (seed 2002), and `syn-song-08` (seed 1008, `cot: melody`,
  `"abc_path": "abc/syn-song-08.abc"`); one line with `è`, `<`, `>`, `&`.
- `music/abc/syn-song-08.abc` (resolved via `abc_path` ⇒ `abc_source`, inlined; the recipe's
  `recipe_sources.raw` still contains `abc_path`).
- `music/requests/syn-song-06.json` (per-song JSON, same request as the JSONL line plus
  `"title": "Synthetic Six"` ⇒ same hash, second source).
- `music/flac/`: `syn_song_01.flac` (ambiguous between the two `syn-song-01` recipes → resolved by the
  mapping, seed 1001), `syn_song_01_s2001.flac` (`slug_seed`), `syn_song_02.flac` (`slug`),
  `syn_song_02_s2002.flac` (`slug`), `syn_song_03.flac`, `syn_song_04.flac` (`slug`), `take_final.flac`
  tagged `MM_REQUEST_ID=syn-song-07` (`tag`), `synthetic_six.flac` (`title_slug`), `syn_song_05.flac`
  (byte copy of the result-dir audio), `syn_song_04.mp3` (sidecar),
  `.syn_song_06.flac.Xy12Ab` (rsync temp: a complete, distinct 6 s take of syn-song-06 with
  `Variant: 1`; run 4 of the incremental test renames it to `syn_song_06.flac`), `link_to_02.flac`
  (symlink to `syn_song_02.flac`), `pipe.flac` (FIFO).
- `music/audio/syn_song_04.flac` (byte duplicate) and `music/player-export/Syn Song 04.flac`
  (`synth.Retag` copy of `syn_song_04.flac` with `TITLE=Syn Song 04`: different bytes, same audio id).
- `music/mp3/`: `syn_song_02.mp3`, `syn_song_03.mp3`, `take_final.mp3` (sidecars across directories),
  `orphan_take.mp3` (no FLAC ⇒ `orphan_mp3`).
- `music/results/syn-song-05/{audio.flac, request.json, result.json, config.json, plan.json, score.abc,
  semantic.npy, latent.npy}`: `request.json` written exactly like YuE2 (`asdict` field order, indent 2,
  `"abc": null`, `"cfg_scale": null`) ⇒ same hash as the JSONL line; `truncated
  {"abc":false,"semantic":true}`; score aligned → timed.
- `music/scores/syn_song_03.abc` (attached by audio stem; aligned → timed; 3 sections × 2 bars, M:4/4,
  Q:1/4=120 → 2 s/bar, 12 s).
- `music/backlog-variants.jsonl` 12 lines `synb_001_v1 … synb_004_v3`; `music/pilot.jsonl` = the 3
  `synb_001_*` lines byte-identical; `music/pilot.json` collection `pilot-synthetic` (variant_ids =
  the same 3 ids, plus synthetic `title`/`artist`/`why`); `music/index.csv` (CRLF, UTF-8 BOM) 12 rows
  with synthetic reference strings, where the `synb_003_v2` row has a seed that differs from its JSONL
  line; `music/playlist.json` (reference playlist); `music/review.json` (validation report);
  `music/old-batch.zip`.
- `varianti/syn-song-01-v2.json` (draft; mother = the root of the syn-song-01 lineage, which has two
  sibling takes).
- `unmatched/mystery.flac` (no recipe; mapping `-`).
- Mapping file (provided with the profile): `music/flac/syn_song_01.flac,syn-song-01,1001,,` and
  `unmatched/mystery.flac,-,,,`.
- Loudness: syn-song-01 (1001) −10 LUFS, syn-song-04 −20 LUFS, others −14; syn-song-02 band-limited
  8 kHz; durations 6 s except syn-song-03 and syn-song-05 (12 s).
- **Expected after one `import` with the mapping:** recipes 23 (10 songs + 12 backlog + 1 varianti);
  recipe_sources 28; tracks 9; drafts 23 (14 `backlog` = 13 `import_backlog` + 1 `varianti_import`;
  9 `fulfilled`); collections 1 (3 drafts — `pilot.jsonl` does not add a second); refs 11 (1
  `ref_seed_mismatch`); sibling edges 2 (`same_request_id` for syn-song-01, `seed_family` for
  syn-song-02/-s2002); scores 2 (both timed); audio files 13, audio identities 10, duplicate groups 2
  (syn-song-04 × 3 incl. the re-tagged copy; syn-song-05 × 2, won by `result_dir`); ignored 12 (4
  `mp3_sidecar`, 1 `orphan_mp3`, 1 `temp`, 1 `latent`, 1 `reference_playlist`, 1 `json`, 1 `symlink`, 1
  `not_regular`, 1 `archive`); recipe line errors 1; skipped audio 1; loudness spread 10.0 LU; exit
  code 1 (the invalid line).

**Profile `e2e`/`dev`:** 6 recipes with one take each (one 12 s timed, one untimed whose lyrics contain
`<b>&`, two siblings sharing a request id, durations 6–12 s, LUFS −12…−16), 12 backlog recipes
in 4 groups with a 3-draft collection, 1 varianti recipe ⇒ 19 recipes, 6 tracks, 19 drafts (13
backlog, 6 fulfilled), 1 sibling edge; no unmatched/invalid inputs, no symlink/FIFO/archive.

### C22. Web (`web/`)

Routes (M0 router): `/` Library · `/tracks/:id` Track · `/backlog` Backlog · `/backlog/:id` Draft ·
`/import` Import runs · `/settings` Settings (extended) · 404.

```
web/src/
  App.svelte                         shell (M0; holds THE <audio> and the static route table): nav · <main> · player bar
  routes/{Library,Track,Backlog,Draft,Import,Settings}.svelte   (Library replaces M0's routes/Home.svelte)
  lib/components/PlayerBar.svelte    player CONTROLS only (no <audio>): mini waveform, play/pause, prev/next, time, title link
  lib/components/SessionBanner.svelte  "Session expired — sign in again" / offline notice (C22.4)
  lib/components/Waveform.svelte     canvas; props {peaks, duration, current, onseek, height}
  lib/components/TrackTable.svelte   columns: title, style terms, BPM, duration, LUFS, score/timed badge, children
  lib/components/LibraryFilters.svelte  q, style terms (facets), duration range, date range, has_score, sort
  lib/components/LyricsPanel.svelte  sections; highlights current section ONLY when section.start_s != null
  lib/components/MetricsCard.svelte  loudness, true peak, bandwidth, RG gain, truncation badge
  lib/components/Pager.svelte
  lib/player.svelte.ts               store (C22.1)
  lib/mediaSession.ts                Media Session glue (C22.2)
  lib/device.svelte.ts               device id, quality, probe (C22.3)
  lib/peaks.ts                       pure: peaksToColumns(data, pairs, width) → {min,max}[]
  lib/sections.ts                    pure: sectionAt(sections, t) → idx | -1 (−1 when untimed)
  lib/time.ts                        fmt(seconds) "m:ss"; parseT("72"|"1:12") → seconds
  lib/libraryQuery.ts                filters ⇄ URL query string (back/forward restores filters)
  api/session.ts                     session-loss detection + confirmation probe (C22.4; inside web/src/api/)
```
**One owner for THE `<audio>` element** (confirmed in M0's final code): `web/src/App.svelte`, inside
`<footer class="player" aria-label="Player">`, as `<audio bind:this={audio} data-audio-id={audioId}
controls preload="none">`, handed to the store by `player.attach(audio)` in an `$effect`
(`web/src/lib/player.svelte.ts`, class `Player` with `el`, `title`, `src`, `attach`). M1 never moves,
re-creates or conditionally renders it; M1-W1 may drop the native `controls` attribute once
`PlayerBar.svelte` (new; controls only, receives the store) is mounted in the same footer.
`singleAudio.test.ts` asserts exactly one `<audio` in `web/src/**`, in `App.svelte`; M0's Playwright
check (`web/e2e/smoke.spec.ts` › navigation keeps the same audio element, a `data-e2e-mark` set on the
element survives navigation) keeps passing.
**M0 shell facts M1 builds on:** the router (`web/src/lib/router.ts`) has a static route table — App.svelte's
`routes: Route[]` (`{pattern, name}`, `:param` segments) plus an `{#if $router.route?.name === …}` chain
— so every new route is an App.svelte edit; it intercepts same-origin link clicks except
`SERVER_PREFIXES` (`/api`, `/human/api`, `/auth`, `/mcp`, `/healthz`, `/__e2e`, `/.well-known`), and
M1-W1 adds `/media`, `/icons` and `/manifest.webmanifest` so a media or icon link is never swallowed by
the SPA. M0's `/` route is `routes/Home.svelte` (route name `home`, heading "Library", test ids
`identity`, `via`, `version` filled from `whoami` + `ping`); M0's `/settings` shows `get_system_info`
under test id `system-info`. `ping`/`whoami` (`ui: "/"`) and `get_system_info` (`ui: "/settings"`) must
stay served by the SPA, and `smoke.spec.ts` asserts those test ids, so Library and Settings keep the
calls and the ids. Random ids come from M0's `randomId()` (`web/src/lib/id.ts`, 16 hex chars from
`getRandomValues`; `crypto.randomUUID` exists only in secure contexts, and plain-HTTP LAN origins must
work). Storage keys use M0's `mm:` prefix (M0: `sessionStorage['mm:last-reload']`).
**No raw HTML:** ESLint `svelte/no-at-html-tags` is an error — already so in M0 through
`svelte.configs["flat/recommended"]` in `web/eslint.config.js`; M1-W1 also sets it explicitly so a
config change cannot drop it. Lyrics, titles and styles render as text (e2e: a lyric line `<b>&` shows
literally).

**C22.1 Player store** (`$state` class): `attach(el)`, `play(trackId, queue?: string[])`, `toggle()`,
`seek(s)`, `next()`, `prev()`, `state: {trackId, queue, index, playing, current, duration, format,
error}`. Links come from `get_media_links` and are cached per track until `expires_at − 10 min`; the
next track's links are prefetched when playback starts. `ended` ⇒ set the next `src` and call `play()`
**synchronously** (no `await` before `play()`; falls back to fetching links when not prefetched). On
media `error` with `MEDIA_ERR_NETWORK` (e.g. 403 expired link): re-mint once and restore `currentTime`.
On `MEDIA_ERR_SRC_NOT_SUPPORTED` or `MEDIA_ERR_DECODE` for a FLAC: switch this play to the MP3 at the same
position and record a failed probe (C22.3). Format = device effective format (`lossless`→flac,
`high`→mp3, `auto`→mp3 iff `navigator.connection?.saveData || type==='cellular'`).
`navigator.audioSession.type = 'playback'` where available. No Web Audio in the playback path.
**Deep link** `/tracks/:id?t=72` loads that track paused and applies the seek in the `loadedmetadata`
handler (setting `currentTime` earlier is ignored on iOS); if the track is already current, it seeks
immediately.

**C22.2 Media Session:** metadata `{title, artist: "metamusic", album: <root title>, artwork:
[/icons/icon-512.png 512x512]}`; handlers `play`, `pause`, `previoustrack`, `nexttrack`, `seekto`;
`seekforward`/`seekbackward` set to `null`; `setPositionState` on `loadedmetadata`, `seeked`, and
`timeupdate` (throttled 1/s) — called only when `Number.isFinite(duration) && duration > 0`, with
`position` clamped to [0, duration] and `playbackRate > 0` (live/unknown durations would throw);
handlers re-registered on `playing`, track change, `visibilitychange` (visible) and `pageshow`.

**C22.3 Device probe (D-M1-6).** `device_id` = `localStorage['mm:device-id']` (M0's `randomId()`:
16 hex chars, which satisfies the C19 `device_id` pattern), label from UA. At boot the client only
calls `get_device_probe` (to have the URL ready); it never
creates a media element. The probe runs **only inside a user gesture**: (a) the first Play tap while
`localStorage` has no probe result for `probe_version` 1 and `quality_source != 'user'`, and (b) the
Settings "Test this device" button. In the gesture handler, synchronously: `probe = new Audio()` (never
the player element), `probe.src = url`, `probe.load()`; the main player's `play()` is called in the same
handler, so the probe never delays playback. Steps: `canPlayType('audio/flac')`; wait `loadedmetadata`
(10 s); `|duration − 3.0| ≤ 0.1`; `currentTime = 1.5`; wait `seeked` then `canplay` (10 s).
Outcomes: **ok** (all steps pass); **failed** only on `MEDIA_ERR_SRC_NOT_SUPPORTED` or
`MEDIA_ERR_DECODE`; **inconclusive** on timeout, `MEDIA_ERR_NETWORK`, `MEDIA_ERR_ABORTED`, a duration
mismatch, or the page being hidden mid-probe. Every outcome is stored with `put_device_profile`
(`flac_decode_ok`/`flac_seek_ok` NULL when not established). Only `failed` with `quality_source !=
'user'` downgrades: quality `high`, `quality_source 'probe'`. Inconclusive changes nothing and the probe
is offered again on the next gesture (at most 3 attempts per `probe_version`, counted in
`localStorage`). Settings shows the probe result, the quality selector (Lossless / High / Auto only
when `navigator.connection` exists), data estimates (FLAC ≈ 11 MB/min, MP3 ≈ 2.4 MB/min) and
`list_device_profiles`.

**C22.4 Session loss (D-M1-5; M1-W1 records this in the M0 document).** It replaces M0's behaviour in
`web/src/api/client.ts`: the openapi-fetch middleware reloads on any 401 response and on a `TypeError`
for `/api`/`/human/api`, throttled to one reload per 10 s through `sessionStorage['mm:last-reload']`; a
throttled reload sets the exported `connectionLost` store, which App.svelte renders as a "Connection
lost. Reload the page to retry." alert. M1 removes `connectionLost`, the 10 s throttle and that alert
(SessionBanner replaces them) and rewrites the matching cases of `web/src/api/client.test.ts`; the
middleware keeps setting `X-MM-Client: spa` and `X-Requested-With: XMLHttpRequest` (served-operation
recording and the C24 XHR check depend on them). Detection lives in `web/src/api/` (the only network
layer):
- a 401 on `/api` or `/human/api` ⇒ **confirmed** auth loss;
- a `TypeError` ⇒ retry the request once after 1 s (idempotent methods only: GET, HEAD, PUT); a 401 on
  the retry ⇒ confirmed; another `TypeError` ⇒ if `navigator.onLine` is false ⇒ transient (offline
  notice, nothing else); else a confirmation probe `GET /api/ping` with `redirect: 'manual'`,
  `cache: 'no-store'`: status 401 or `response.type === 'opaqueredirect'` (the edge redirected to the
  login page) ⇒ confirmed; anything else ⇒ transient ("connection problem" notice).

On confirmed loss: **if audio is playing, never reload** — show the persistent `SessionBanner` ("Session
expired — Sign in again"; its button reloads); media keeps playing on its signed URL. If audio is not
playing ⇒ reload, unless the **loop guard** trips: `sessionStorage['mm:auth-reloads']` keeps reload
timestamps, and a reload within the last 30 s (or 3 within 10 min) shows the banner instead. Pausing
never triggers a reload by itself.

**C22.5 PWA:** `index.html` has `<link rel="manifest" href="/manifest.webmanifest">` (no
`crossorigin`; the alternative `use-credentials` is a device-checklist item), `apple-touch-icon`
`/icons/apple-touch-icon.png` (180×180), `theme-color`. Manifest: `name`, `short_name`,
`start_url:"/"`, `scope:"/"`, `display:"standalone"`, icons 192, 512 and 512 maskable. No service
worker in M1. M0 already ships the manifest link (no `crossorigin`), `theme-color`, an
`apple-touch-icon` pointing at `/icons/icon-192.png`, and a manifest with `name`, `short_name`,
`start_url`, `display`, colours and icons `icon-192.png`, `icon-512.png`, `icon.svg`; M1 adds `scope`,
a dedicated 180×180 `apple-touch-icon.png` and a maskable 512 PNG (`icons/icon-512-maskable.png`). The
privacy gate rejects binary blobs except real PNGs under `web/public/icons/`, so icons go only there.

Playwright config additions: Chrome `--autoplay-policy=no-user-gesture-required` on M0's three Chrome
projects (`chrome`, `chrome-pixel7`, `chrome-iphone15` — the last is an iPhone 15 viewport on Chrome);
Firefox prefs `media.autoplay.default=0`, `media.autoplay.blocking_policy=0` on project `firefox`. M0's
`web/e2e/global-setup.ts` creates a temp DataDir + secrets, spawns `$MM_E2E_BIN serve` in proxy mode and
returns a run-scoped teardown that checks served-operation coverage; M1 adds a step before the spawn
that runs `$MM_E2E_BIN seed-synthetic --profile e2e` synchronously with the same env (`MM_AUTH=proxy`,
`MM_DATA_DIR`, `MM_SECRETS_DIR`, `MM_E2E=1`, inherited `GOCOVERDIR` and `PATH`) and fails setup on a
non-zero exit. The probe specs still trigger the probe with a real click, so the gesture rule is exercised.

### C23. Tooling, CI, coverage

- **ffmpeg** (D-M1-4): `scripts/tools.sh` installs a pinned, sha256-verified **LGPL** static linux64
  build that includes `libmp3lame` and `ebur128`. Upstream dated build URLs are not durable (dated
  autobuild tags are pruned upstream — verify at implementation), so M1-I1 downloads the chosen archive
  once, verifies it, and re-hosts it unchanged as an asset of a GitHub release of this repo
  (`tools-ffmpeg-<version>`) together with the matching FFmpeg source tarball and the build's licence and
  configure files (LGPL redistribution duties). Never a GPL build (it would add GPL source obligations
  for every bundled component). tools.sh downloads from that release URL with the sha256 pinned in the
  script. M0's `scripts/tools.sh` (bash ≥ 4) installs only golangci-lint and gitleaks: its `fetch`
  helper extracts `.tar.gz` archives and looks up `SHA[<name>-<os>-<arch>]` for linux/darwin ×
  amd64/arm64, and `have` skips a tool whose `--version` already matches. M1-I1 extends `fetch` to the
  ffmpeg archive format and pins ffmpeg for `linux-amd64` only (the CI runner); on other hosts tools.sh
  prints a notice and `testtools` falls back to an ffmpeg on `PATH`.
- **flac/metaflac:** CI installs the Ubuntu 24.04 archive package
  (`sudo apt-get install -y --no-install-recommends flac`; durable, seconds) and asserts
  `metaflac --version` ≥ 1.4. tools.sh gains (M0 has no media tools) a build of the pinned xiph release
  tarball (xiph keeps old releases), used only as a local fallback when no system package exists
  (`./configure --disable-shared --enable-static --disable-ogg --disable-cpplibs --disable-doxygen-docs && make`).
- `scripts/tools-selftest.sh`: every pinned URL answers 2xx to a HEAD request; a tampered sha256 makes
  `tools.sh` exit non-zero and leaves no binary; a second run is a no-op.
- CI: `actions/cache` for `./bin` keyed `tools-${{ runner.os }}-${{ hashFiles('scripts/tools.sh') }}` in
  the `go` and `e2e` jobs; `PATH=$PWD/bin:$PATH`; `MM_REQUIRE_MEDIA_TOOLS=1`. GitHub evicts caches
  unused for 7 days, so the **cold path is the budget**: tools step ≤ 60 s cold (measured on a forced
  cold run and recorded in `metrics/REPORT.md`), ≤ 5 s warm. That exact key is already used by M0's
  `lint` job (`ci.yml`) and by the `gate` job, which lives in its own workflow `.github/workflows/gate.yml`
  (every push to any branch or tag and every PR; `--history --require-patterns` with the
  `PUBLIC_GATE_PATTERNS` secret). Whichever job saves the key first fixes `./bin` for all of them, so
  tools.sh installs the same set in every job (the xiph build fallback never runs in CI, where `flac`
  comes from apt in the jobs that need it). New jobs and workflows reuse the SHA-pinned actions already
  in `ci.yml` (checkout v7.0.1, setup-go v7.0.0, setup-bun v2.2.0, cache v6.1.0, upload-artifact v7.0.2,
  download-artifact v8.0.2), with `permissions: {}` at the top and `contents: read` per job like M0.
- **Test tiers:** heavy tests (everything that calls `testtools.Require`, all `TestAcceptance_*`, fuzz
  corpora longer than 1 s) skip under `testing.Short()`. `make check` runs
  `go test -race -short -shuffle=on ./...`: `-shuffle` is not a cacheable flag (verified with
  `go help test`), so every `make check` re-runs every test and only `-short` keeps it under 30 s warm.
  In M0's Makefile `check` wraps an internal `_check` target (gofmt, vet, golangci-lint,
  `CGO_ENABLED=1 go test -race -shuffle=on ./...`, then web lint/check/unit when `web/node_modules`
  exists); the `-short` change goes into `_check`. `make test` (M0: `env CGO_ENABLED=1 go test -race
  -shuffle=on ./...`, same command as `make race`) and the CI `go` job run without `-short` and with
  `MM_REQUIRE_MEDIA_TOOLS=1`. (M1-I1 updates the M0 I1 `check` definition and the `CLAUDE.md` command
  list accordingly.)
- Coverage: `go` job runs `go test -race -shuffle=on -cover ./... -args -test.gocoverdir=$PWD/covdata/unit`;
  `e2e` job builds `bin/metamusic-e2e` with `-cover -covermode=atomic -coverpkg=./...` (M0 builds it
  with `-cover -covermode=atomic` only; atomic matches the `-race` unit run so the two sets merge) and
  runs with `GOCOVERDIR=$PWD/covdata/e2e` (M0: `covdata`; the same change goes into `scripts/e2e.sh`,
  which backs `make e2e`, clears `covdata/*` and prints `go tool covdata percent`; it also exports
  `PATH=$root/bin:$PATH` so the seeded e2e server finds the pinned tools); new `coverage` job (needs
  both) runs
  `scripts/coverage-gate.sh covdata/unit covdata/e2e`, which uses `go tool covdata textfmt` +
  `go tool cover -func` (total over `internal/...`) and `go tool covdata percent` (per package), and
  enforces `coverage.thresholds`:
  ```
  internal/...            85
  internal/auth           95
  internal/media/serve    95
  ```
  M0 baseline at the head M1 starts from (unit tests only, 2026-10-12): `internal/...` 86.3 % without
  `-coverpkg` (88.7 % with `-coverpkg=./internal/...`), `internal/auth` 90.8 % either way (weakest:
  `checkPassword`, `SetPassword`, `VerifySession` at 75–76 %), `internal/api` 68.9 % own-package /
  82.0 % cross-package, `internal/store` 66.2 %. The e2e run (proxy mode) adds little to `internal/auth`,
  so the 95 % floor is not met by M0's code: M1-B1, which already edits `internal/auth`, adds the missing
  auth error-path tests (target ≥ 95 % from unit tests alone).
- The metrics wrapper's target allowlist exists in three places that must change together:
  `scripts/loop` (the `case` list), `metrics/loop.schema.json` (`target` enum) and `scripts/metrics-row.jq`
  (used by `scripts/public-gate.sh` to validate committed `metrics/*.jsonl` rows and by
  `scripts/metrics-report.sh`); `scripts/loop-selftest.sh` (run by `make gate`) must keep passing. They
  gain targets `cover`, `routing`, `seed`, `fixture-db` (M0 C9 updated in the same commit). Makefile
  (add each to `.PHONY`): `make cover`, `make routing`, `make seed` (=
  `MM_AUTH=local MM_DATA_DIR=./data/dev go run ./cmd/metamusic seed-synthetic --profile dev --force`:
  `config.Load` needs `MM_AUTH`, and the C20 guard needs `--force` outside `dev`/`MM_E2E`),
  `make fixture-db`, and M0's unwrapped `make dev` (`go run ./cmd/metamusic dev --data-dir ./data/dev`)
  gains `--seed-synthetic`. `hooks`, `tools`, `dev`, `metrics` and `clean` stay unwrapped (M0 C9).

### C24. Edge routing contract (design §7, sanitised for the public repo)

`deploy/labels.example.yml` uses the host `mm.example.com`; every router also gets entrypoint
`websecure`, TLS and service `metamusic`.

| Router | Rule (inside `Host(…) && (…)`) | Priority | Middlewares, in this order |
|---|---|---|---|
| `mm-public` | `Path(/manifest.webmanifest) \|\| PathPrefix(/icons/)` | 120 | `mm-strip-identity`, `sec-headers` |
| `mm-api-bearer` | `PathPrefix(/api/) && HeaderRegexp(Authorization, ^[Bb]earer .+)` | 110 | `mm-strip-identity`, `sec-ratelimit`, `sec-headers` |
| `mm-agent` | `Path(/mcp) \|\| PathPrefix(/mcp/) \|\| PathPrefix(/.well-known/) \|\| Path(/oauth/token) \|\| Path(/oauth/register) \|\| Path(/oauth/revoke) \|\| Path(/api/openapi.json)` | 100 | `mm-strip-identity`, `sec-ratelimit`, `sec-headers` |
| `mm-media` | `PathPrefix(/rest/) \|\| PathPrefix(/media/) \|\| PathPrefix(/worker/)` | 100 | `mm-strip-identity`, `sec-headers`; access log off (`observability.accesslogs=false`) |
| `mm-ui` | everything else | 10 | `authelia` (forward-auth), `mm-edge-mark`, `sec-headers`, `mm-compress` |

- `mm-strip-identity` empties `Remote-User`, `Remote-Groups`, `Remote-Name`, `Remote-Email` and
  `X-MM-Edge`. It is the **first** middleware on every router except `mm-ui`, and never appears on
  `mm-ui`: there it would run after forward-auth and delete the `Remote-User` that Authelia just set. On
  `mm-ui`, forward-auth replaces `Remote-*` and `mm-edge-mark` overwrites `X-MM-Edge`.
- `mm-compress` never applies to `mm-media`. `/healthz` is not routed publicly (container healthcheck
  only). `mm-device` (paired devices) arrives with M2a.
- App-side classes (M0 `auth.Classify`) differ from the routers in two places, by design: `/assets/*` is
  ClassPublic in the app but goes through `mm-ui` at the edge; `/api/openapi.json` is ClassUI in the app.
  Bearer requests for it match `mm-api-bearer` (110) first, so every request that reaches `mm-agent`
  through `Path(/api/openapi.json)` is anonymous and gets the app's 401 problem (the rule only keeps the
  SSO redirect away from agents). Anonymous access to the spec would be a C3 `Classify` change recorded
  in the M0 document; M1 does not make it.
- Authelia rules (`deploy/authelia-rules.example.yml`): `^/human/` and `^/oauth/authorize([/?].*)?$`
  two_factor; everything else on the host one_factor.
- **Authelia 4.39 XHR check (design §5.3):** an `/api/` request carrying `X-Requested-With:
  XMLHttpRequest` with an expired or missing session must get **401**, not a redirect. The stub
  forward-auth implements that contract and the routing test asserts it; M1-X2 verifies it against the
  real Authelia and records the result (if it fails, C22.4's opaque-redirect confirmation still detects
  the loss).

---

## Acceptance mapping (roadmap M1 → named tests)

| Roadmap item | Named test(s) |
|---|---|
| [S] all songs imported with lyrics/style/seed/duration/audio; every take placed (siblings inferred, scores attached) | `TestAcceptance_M1_ImportAllSongs` (profile `acceptance`, exact C21 counts) · real archive: reviewed dry-run in M1-X2; the real "all songs" criterion is tracked in private notes until the archive is complete (DoD) |
| inventory-first dry-run; the reviewed plan is what gets applied | `TestAcceptance_M1_ImportDryRunInventory` · `TestApply_PlanDigestBinding` · `TestCLI_ImportExitCodes` |
| re-runnable as files arrive; idempotent; order-independent | `TestAcceptance_M1_ImportIdempotentAndIncremental` · `TestAcceptance_M1_AllAtOnceEqualsIncremental` |
| inbox never modified; dry-run writes nothing outside `import/` | `TestAcceptance_M1_InboxNeverModified` · `TestInbox_ReadOnlyHasNoWriteMethods` · `TestCLI_DryRunWritesOnlyImportDir` · `TestOpenReadOnly_*` |
| hostile/odd inbox content handled | `TestScan_*` (symlinks, FIFO, unreadable, archives, caps, BOM/case, JSON arrays, non-UTF-8) · `TestMapping_PathConfinement` · `TestRecipes_ABCPathConfinement` · `TestInbox_OpenRegularRejectsSwaps` |
| recipes without audio = visible backlog | `TestAcceptance_M1_BacklogDrafts` · `TestApply_FailedTakeKeepsDraftAndCollection` · `m1-backlog.spec.ts › backlog lists drafts grouped by group` |
| reference metadata stays private | `TestPrivacy_ReferenceMetadataConfined` · `TestGetDraft_ReferenceHumanOnly` |
| [S] play + seek on desktop (Chrome + Firefox CI) | `m1-player.spec.ts › plays and seeks a FLAC master` (chrome, firefox) · `› waveform click seeks` · device checklist DC-M1-2 (phone) |
| frozen-master invariants | `TestAcceptance_M1_FrozenMasterInvariants` (+ C15 unit tests incl. `TestIngest_ConcurrentSameNameNoClobber`, `TestIngest_DiscardOnlyOwnFiles`) |
| Range matrix | `TestAcceptance_M1_MediaRangeMatrix` · `TestHandler_DotSegmentsNeverServe` · `TestHandler_MissingFile500` |
| routing/auth trust matrix | `TestAcceptance_M1_MediaTrustMatrix` (app level) · `TestRoutingContract` (traefik + stub forward-auth incl. the XHR 401 check, M1-I3) |
| loudness spread reported (settles D11) | `TestAcceptance_M1_LoudnessSpreadReported` · operator records the real spread once real masters are imported (M1-X2) |
| MP3 320 rendition | `TestIngest_MP3Rendition` |
| peaks in Go | `TestPeaks_SineAmplitude`, `TestPeaks_Lengths` |
| loudness / bandwidth via ffmpeg Analyzer | `TestLoudness_EBU3341_Cases`, `TestLoudness_TruePeakLoudSine`, `TestParseLoudness_OtherFraming`, `TestBandwidth_*` |
| external tools hardened | `TestRun_*` (mediatool) · `TestArgv_OnlyWorkPaths` · `TestIngest_HostileTagsSanitised` · `TestRead_HostileLengths` |
| library FTS + filters | `TestSearchTracks_FTS`, `TestSearchTracks_Filters`, `TestParity_MultiValueQuery` · `m1-library.spec.ts › search and filters` (two style values) |
| track detail; lyrics sections timed only from score.abc | `TestTrackDetail_SectionsTimedOnlyFromScore`, `TestParseScore` (repeats ⇒ untimed, both voice layouts) · `m1-track.spec.ts › highlights sections only for timed tracks` |
| persistent player + canvas waveform | `m1-player.spec.ts › player persists across navigation while playing` · `singleAudio.test.ts` |
| signed media with Range | `TestSigner_*`, `TestAcceptance_M1_MediaRangeMatrix` · `m1-player.spec.ts › media requests are signed and ranged` |
| PWA: public manifest/icons | `TestHTTP_ManifestAndIconsPublic` · `m1-pwa.spec.ts › manifest and icons load without auth` · DC-M1-1 |
| Media Session | `m1-pwa.spec.ts › media session handlers and metadata` · `m1-player.spec.ts › auto-advance` · `mediaSession.test.ts` (position-state guard) |
| session-loss handling | `session.test.ts` · `m1-pwa.spec.ts › 401 while paused reloads` · `› 401 while playing shows banner and keeps playing` · `› transient TypeError does not reload` · `› confirmed loss via opaque redirect reloads` · `› reload loop guard` · DC-M1-5 |
| device FLAC probe stored server-side | `TestDeviceProfile_*` · `device.svelte.test.ts` · `m1-pwa.spec.ts › device probe stored` · `› undecodable probe falls back to MP3` · `› hanging probe is inconclusive` · DC-M1-6 |
| synthetic seed library for E2E | `TestSeedSynthetic_E2EProfile` · `TestSeedSynthetic_Guard` |
| get_stats real counts | `TestGetStats_RealCounts` |
| parity (REST ↔ MCP) for every new op | M0's `TestParityEveryOperation` (`internal/mcpgen/parity_test.go`) extended with the fixture library and a fixed clock (M1-B4) · `TestParity_MultiValueQuery` · `TestToolsListGolden` (+ `toolsListBaselineBytes` budget) · `TestOps_NilDepsUnavailable` |
| MCP-sized responses | `TestSearchTracks_PageCap` · `TestImportRun_Sections` |
| served-operation coverage | M0's run-scoped teardown in `web/e2e/global-setup.ts` (`/__e2e/served` vs `uiOperations()` from `openapi.json`): all 16 ui ops served (the 13 M1 ops plus M0's `ping`, `whoami`, `get_system_info`) |
| coverage gates | `coverage` CI job (`scripts/coverage-gate.sh`) |
| [N] device checklist: PWA installs with icons, standalone, plays 24-bit FLAC on iPhone + Android | `docs/device-checklist.md` DC-M1-1..6 |

---

## Lanes and tasks

Stage 1 (sequential, branch `m1` created at M0's final head — branch `m0`, not yet merged into `main` —
main worktree, `MM_LANE=m1-found`):
**M1-B1 → M1-B2 → M1-B3 → M1-B4**. Stage 1 lands every shared contract (leaf packages, schema, domain,
library, ops, compile-ready stubs and test builders), so the Stage 2 lanes compile independently.
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from M1-B4):
**Media** (`m1-media`: M1-B5 → M1-B6 → M1-B7), **Import** (`m1-import`: M1-B8), **Web** (`m1-web`:
M1-W1 → M1-W2 → M1-W3), **Infra** (`m1-infra`: M1-I1 → M1-I2 → M1-I3 → M1-I4).
Stage 3 (`m1-int`): **M1-B9** (wiring, synth archive, CLI, acceptance) → **M1-W4** (Playwright) →
**M1-X1** integration → final review → **M1-X2** deploy + real-archive dry-run (operator-assisted).
File ownership: each file is edited by one task per stage; `internal/testtools/testtools.go` belongs to
M1-B1 only; `Makefile` changes in Stage 2 belong to the Infra lane (M1-B4 adds only `fixture-db`).
Exception: M0's static route table makes `web/src/App.svelte` an edit point for every new route, so it
passes W1 → W2 → W3 inside the (sequential) Web lane; no other lane touches it. Stage 2 worktrees read
the untracked `.public-gate-patterns` from the main checkout (M0's gate looks in `<git-common-dir>/..`).

### Task M1-B1: leaf packages, config, FLAC metadata, compile-ready stubs
**Files:** `internal/problem/{problem.go,problem_test.go}` and its adoption in `internal/auth/middleware.go`,
`internal/httpserver/server.go`, `internal/api/api.go`, `internal/mcpgen/problem.go` (+
`internal/api/problem_shape_test.go`); `internal/auth/{local_test.go,middleware_test.go}` (coverage
floor, C23); `.golangci.yml` (C10 lint exclusions);
`internal/mediatool/{run.go,run_test.go}`; `internal/testtools/{testtools.go,testtools_test.go}`;
`internal/config/{config.go,config_test.go}` (C10 env); `internal/media/flacmeta/{flacmeta.go,flacmeta_test.go,fuzz_test.go}`;
`internal/media/flacmeta/flactest/flactest.go`; stubs `internal/analysis/analysis.go` (C14 types,
interface and constants; no implementation) and `internal/media/ingest/{types.go,errors.go,stub.go}` (C15
types and error codes; `New` + `Ingest`/`Discard` returning `errors.ErrUnsupported`, marked
`// STUB(M1-B7)`); `cmd/metamusic/nohelpers_test.go`; `docs/plans/2026-10-12-m0-skeleton.md` (C1 extended
by M1 C10; C3/C6 problem writer now `internal/problem`).
Tests first:
- `TestProblem_Write` (shape, content type, nil details ⇒ `{}`, a message with quotes/newlines stays valid
  JSON — M0's string-concatenating writer would break), `TestProblem_ShapesAgree`; all M0 auth,
  httpserver and mcpgen tests (incl. `TestMCPTransportErrorsAreProblemJSON`) stay green unchanged.
- Auth coverage to the C23 floor: unit tests for the uncovered error paths of `checkPassword`,
  `SetPassword` and `VerifySession` (malformed PHC strings, store errors, expired and sliding sessions)
  until `go test -cover ./internal/auth` reports ≥ 95 % (M0: 90.8 %). Tests only; no auth behaviour
  changes.
- `TestRun_Timeout` (sleeping script killed within `Timeout` + `WaitDelay`), `TestRun_WaitDelayOrphanPipe`
  (a grandchild holding stdout open cannot hang `Run`), `TestRun_OutputCap` (10 MiB stdout with
  `MaxStdout` 1 MiB ⇒ `ErrOutputLimit`), `TestRun_ErrorHasNoArgv` (sentinel argument absent from the
  error), `TestRun_EnvMinimal`.
- `TestConfig_MediaTools` (defaults, overrides, `Inbox` default `DataDir/inbox`).
- `TestTesttools_Require` (short ⇒ skip; missing + `MM_REQUIRE_MEDIA_TOOLS=1` ⇒ fail, checked in a
  subprocess `go test -run`).
- `TestRead_StreamInfo` (bytes from `flactest`: STREAMINFO 48000/2/24/288000 + MD5; Duration 6.0),
  `TestRead_BlocksAndFramesOffset` (STREAMINFO+SEEKTABLE+VORBIS_COMMENT+PADDING → offsets/lengths;
  vendor; comment order), `TestRead_SeekPointsSkipPlaceholders`, `TestRead_Errors` (`ErrNotFLAC` on
  `OggS`; truncated header → `ErrCorrupt`), `TestRead_HostileLengths` (comment count 0xFFFFFFFF in a
  64-byte block, vendor length 2^31, block length past EOF, 2000 tiny blocks ⇒ `ErrCorrupt` with
  `runtime.MemStats.TotalAlloc` growth < 1 MiB), `TestRead_PictureNotLoaded` (16 MiB − 1 PICTURE ⇒ < 1 MiB
  allocated), `TestFramesSHA256`, `TestAudioID`, `FuzzRead` (no panic; seed corpus from `flactest`;
  inputs ≤ 64 KiB). Seeds are added with `f.Add` at runtime; any corpus file committed under
  `testdata/fuzz/FuzzRead/` uses Go's text encoding — M0's privacy gate blocks committed blobs that
  contain NUL bytes or start with audio/archive magic such as `fLaC`.
- `TestNoTestHelpersInBinary` (C10).
Done: `make check` green; `grep -rn 'STUB(M1-' internal` lists only the ingest stub; fuzz 30 s locally
clean.

### Task M1-B2: schema, read-only store, triggers, upgrade
**Files:** `internal/store/migrations/0002_library.sql`, `internal/store/migrate.go` (`migrateTo`
split, C11), `internal/store/{readonly.go,readonly_test.go,store_test.go (version),triggers_test.go,
upgrade_test.go,export_test.go,schema_test.go,noreplace_test.go}`,
`docs/plans/2026-10-12-m0-skeleton.md` (C2 addendum: `OpenReadOnly`, `ErrNoDatabase`,
`ErrSchemaMismatch`).
Tests first:
- `TestMigration0002_Applies` (user_version 2; all tables, indexes, FTS tables and trigger names present
  via `sqlite_master`).
- `TestMigration0002_UpgradeFromM0` (a v1 database built by applying only `0001` through the test hook
  `MigrateTo` — `export_test.go` (package `store`) sets `var MigrateTo = migrateTo` — with a token created
  via `auth.TokenStore.Create` and a local password set via `auth.LocalAuth.SetPassword` (≥ 12
  characters; M0 returns `ErrWeakPassword` for shorter ones); `store.Open`
  migrates to 2; the token still verifies; `settings`/`human_account` rows unchanged).
  `upgrade_test.go` is in package `store_test`: M0's store tests are in package `store`, which cannot
  import `internal/auth` (auth imports store — an import cycle).
- `TestTriggers_Immutable` table: `UPDATE recipes SET style='x'` → `recipes: immutable`; DELETE on
  recipes, recipe_sources, tracks, mutations, renditions, analyses, artifacts, scores, sections,
  recipe_drafts, recipe_refs, track_style_terms, device_profiles, import_runs → `no physical delete`/
  `append-only`; `UPDATE tracks SET master_sha256=…` → `tracks: frozen column`; `UPDATE tracks SET
  title='New'` → ok; `UPDATE tracks SET title=?, recorded_at=recorded_at` → frozen (a frozen column in
  SET fires even when unchanged); `UPDATE recipe_drafts SET origin='x'` → frozen; `UPDATE
  device_profiles SET created_at=…` → frozen; finished `import_runs` update → error; running run
  `heartbeat_at` update → ok.
- `TestSchema_GrowingEnumsNotChecked` (`engine='other'`, `renditions.kind='spectrogram'`,
  `analyses.analyzer='clipping'`, `analyses.tier='D'`, `device_profiles.quality='opus'` insert fine at
  the SQL level — Go validates them).
- `TestRecipes_Checks` (`request_json.$.id` ≠ `request_id` → constraint error; seed 2^53 → CHECK error),
  `TestTracks_AudioIdentityUnique` (same `(frames_sha256, total_samples)` → constraint error; same frames
  sha with a different total → ok).
- `TestFTS_SyncedByTriggers` (insert track → MATCH `"città"` finds it via `citta`; `UPDATE tracks SET
  title` → old title no longer matches, new does; draft `request_json` update → `drafts_fts` reflects
  the new lyrics).
- `TestOpenReadOnly_MissingDB` (`ErrNoDatabase`; directory listing unchanged),
  `TestOpenReadOnly_ClosedWALCreatesNothing` (DB bytes and directory listing identical after queries
  and close), `TestOpenReadOnly_LiveWriter` (writer open ⇒ committed rows visible; no files beyond the
  writer's sidecars), `TestOpenReadOnly_SchemaMismatch` (v1 DB ⇒ `ErrSchemaMismatch` naming both
  versions), `TestOpenReadOnly_WritesFail`.
- `TestNoReplaceSQL` (C11).
Done: `make check` green.

### Task M1-B3: domain `internal/engine/yue2`
**Files:** `internal/engine/yue2/{request.go,canonical.go,instructions.go,sections.go,score.go,style.go,
slug.go,keys.go}` + tests, `internal/engine/yue2/testdata/*` (golden hash, captured `INSTRUCTIONS`),
`go.mod`/`go.sum` (`golang.org/x/text`, D-M1-3; also pinned in `internal/deps/tools.go` if it is not
yet imported elsewhere).
Tests first:
- `TestParseRequest` table (≥ 30 rows): sorted/unsorted keys same hash; `tags` alias; style+tags unequal
  → `style_tags_mismatch`; seed missing → 831001 + `seed_defaulted`; seed `1.5`/`"7"`/`-1`/`1e3`/`2^53`
  → `seed_out_of_range`; bad id `../x` → `bad_id`; cot `none` → `bad_cot`; cfg 21 → `bad_cfg_scale`;
  **YuE2-written `request.json`** (asdict order, indent 2, `"abc": null`, `"cfg_scale": null`) → same
  hash as the JSONL line; cfg 1.0 with cot full → dropped; 1.01 with cot off → dropped; 1.0 with cot off
  → kept; abc `""` and `"  \n"` → `empty_abc`; abc + abc_path → `abc_conflict`; abc_path with a fake
  resolver → inlined (hash equals the same request with inline abc; `ABCPath` set); abc_path with a nil
  resolver → `abc_path_unresolved`; matching `prompt` → dropped + `prompt_verified`; other `prompt` →
  `prompt_mismatch`; `abc_sampling` → `sampling_unsupported`; `title` with `AllowTitle` → `Title`, else
  `dropped_key:title`; `lang` → `dropped_key:lang`; requests differing only in a non-default
  `cfg_scale` → different `Hash`, equal `CoreHash`; fixed example
  `{"cot":"full","id":"syn-a","lyrics":"[Verse]\nla","seed":7,"style":"pop"}` → canonical bytes exactly
  that string and a golden hash stored in testdata.
- `TestInstructions_Golden` (the three strings byte-equal to the captured `protocol.py` values).
- `TestLyricsSections` (`[Verse 1]`,`[Chorus]`,`[Verse 2]`,`[Chorus]`,`[Outro – Piano + fade]` →
  labels `verse 1, chorus 1, verse 2, chorus 2, outro`; kinds; lines trimmed; preamble handling).
- `TestParseScore` table: (M:4/4, Q:1/4=120, 8 bars) → spb 2.0, total 16 s; (M:6/8, Q:3/8=80, 4 bars)
  → spb 1.5; (M:3/4, Q:1/4=90, 3 bars) → spb 2.0; `M:C` → 4/4; `"A|B"` chord text not a barline; `|]`
  final; trailing partial bar counts; `[Q:1/4=100]` inline → untimed; `Q:120` → untimed; no Q →
  untimed; **`|:`, `:|`, `::`, `[1`, `[2` → untimed**; **interleaved layout** (V:1/V:2 alternating per
  section, labels between blocks) → sections at voice-1 bar counts; **per-voice layout** → voice-2 labels
  ignored; per-voice with a different voice-2 label sequence → `voice_sections_differ`, still timed;
  `%%MIDI` lines and `% generated by …` comments ignored.
- `TestAlignSections` (3 aligned sections → times 0–4, 4–8, 8–12; count mismatch → ok=false; kind
  mismatch → ok=false).
- `TestStyleTerms` (`"Italian, Pop Rock ,  female vocal, 120 BPM"` → `[italian, pop rock, female vocal]`,
  120; `"bpm 300"` dropped; dedupe; 25 terms → 20).
- `TestSlug` (`"Città d'Oro — Live!"` → `citta_d_oro_live`; `Straße` → `strasse`; `Ærø` → `aero`;
  `Łódź` → `lodz`; full-width `ＡＢＣ` → `abc`; emoji stripped; 200-byte input → ≤ 80 bytes),
  `TestGroupKeyAndSeedFamily` (`x_001_v2` → GroupKey `x_001`, SeedFamily `x_001_v2`; `song-s1101` → both
  `song`; `song_seed7` → both `song`; `plain` → `plain`).
Done: `make check` green; one new module dependency, justified in the commit message.

### Task M1-B4: library, signer, ops, server options, MCP explode, parity
**Files:** `internal/library/{service.go,write.go,search.go,fts.go,tracks.go,drafts.go,stats.go,
imports.go,devices.go,snapshot.go}` + tests, `internal/library/librarytest/{fixture.go,fixture_test.go}`,
`internal/media/serve/{types.go,sign.go,sign_test.go}`, `internal/api/{api.go (Deps),ops_library.go,
ops_drafts.go,ops_media.go,ops_stats.go,ops_imports.go,ops_devices.go,types_m1.go}` + tests,
`internal/httpserver/{server.go,server_test.go}` (Options), `internal/mcpgen/parity_test.go` (M0's
harness: `newRig`, `parityArgs`, `toolsListBaselineBytes`), `internal/mcpgen/testdata/tools_list.golden.json`,
`cmd/metamusic/cmd_openapi_test.go`, `openapi.json`, `web/src/api/schema.d.ts`, `Makefile` +
`scripts/loop` + `metrics/loop.schema.json` + `scripts/metrics-row.jq` (`fixture-db` only, C23),
`docs/plans/2026-10-12-m0-skeleton.md` (C4 `Deps`, C6 `Options` and C9 allowlist addenda). No change to
`internal/mcpgen/dispatch.go`: M0 already honours `explode` (C19).
Tests first:
- `TestSigner_SignVerify` (fixed key/clock: URL equals golden; tamper sha/ext/exp/sig → `ErrLinkInvalid`;
  `now > exp` → `ErrLinkExpired`; exp > now+14h → invalid), `TestSigner_BucketedExpiry` (human: now
  10:20:00Z → exp 23:00:00Z; agent: 10:20:30Z → 10:36:00Z), `TestTTLFor`.
- `TestSearchTracks_FTS` (fixture: accent-folded match; prefix `lun` matches `luna`; q with `"`, `*`,
  `(`, `NEAR`, `-x`, `AND` never errors), `TestSearchTracks_Filters` (each filter alone and combined,
  expected IDs), `TestSearchTracks_SortAndCursor` (limit 2 pages through 5 fixture tracks without
  dup/miss), `TestSearchTracks_PageCap` (100 tracks with 20 maximal style terms → every page
  < 90 000 bytes; the cursor continues without dup/miss).
- `TestTrackDetail_SectionsTimedOnlyFromScore` (fixture track with aligned score → all sections have
  times, source `plan`, confidence 0.7, `timed:true`; track without score → all `start_s:null`, source
  `lyrics`, `timed:false`), `TestTrackDetail_Golden` (JSON golden, `-update`).
- `TestListDrafts_*` (status default backlog; collection filter; group ordering; fulfilled excluded),
  `TestGetDraft_ReferenceHumanOnly` (human → map; agent over REST bearer and over MCP → `reference: null`,
  `has_reference: true`; reference never inside `request`).
- `TestGetStats_RealCounts` (fixture: tracks 5, by_status {draft:5}, backlog 6, groups 2, collections
  [{pilot-x,3}], spread_lu = max−min of fixture LUFS, `listen_flac_recommended` true when > 2.0).
- `TestGetMediaLinks_TTLByPrincipal` (human vs agent → expected `exp`; URLs verify with the signer; 404
  for unknown track).
- `TestDeviceProfile_PutUpsert` (PUT twice → one row, `updated_at`/`updated_by` change, `created_at`/
  `created_by` do not; bad device_id → 422; quality outside the Go enum → 422).
- `TestImportRun_Sections` (default `errors`; each section paged with cursor; a 500-recipe report → every
  page < 90 000 bytes; unknown section → 422).
- `TestLibrary_WritesTwice` (every C11 write form executed twice: idempotent, no frozen-column error),
  `TestInsertTake_FulfilsOwnAndCoreMatch`, `TestRelinkVariantiMothers_UniqueRoot` (mother with siblings →
  their root; two roots → NULL + `mother_ambiguous`).
- `TestOps_RegisteredWithMeta` (all 13 ops with the C19 x-mm values and paths; `style`/`status` carry
  `explode: true` in the OpenAPI document), `TestOps_NilDepsUnavailable` (`api.New` with zero `Deps`
  registers every op without panicking; each M1 op answers 503 `UNAVAILABLE` naming the component),
  `TestCLI_OpenAPIWithoutDatabase` (`run(ctx, []string{"openapi"}, getenv, …)` through M0's `do` helper
  with an empty env, in an empty temp cwd (`t.Chdir`) → equals the committed `openapi.json`; no file or
  directory created), `TestHTTPServer_OptionsWired` (Library/Signer/Probe/Now reach `api.Deps`; `Media`
  nil → `/media/x` 404 problem).
- Explode: no new dispatcher test — M0's `TestArrayQueryParamsReachHandlerIntact` already pins repeated
  keys for `explode: true` and comma-joining for `false`; `TestOps_RegisteredWithMeta` pins the flag on
  the M1 ops and `TestParity_MultiValueQuery` pins it end to end.
- M0 parity test (`TestParityEveryOperation`, `internal/mcpgen/parity_test.go`) extended. M0's harness:
  `newRig(t)` builds the full chain with `httpserver.New` + `MCPFactory`; a static `parityArgs` map holds
  a `parityFixture{Args, Method, Path, Body}` per operation id; `needsFixture` already fails any tool or
  generic op that has parameters, a body or a non-GET method but no entry; GET ops are compared up to
  three times (uptime may tick); mutating ops run REST on one fresh rig and MCP on another. M1-B4 makes
  `newRig` seed `librarytest.Fixture` (fixed ids, C17) and pass `Library`, `Signer` and a fixed clock
  through `Options.Now` (so `get_media_links` buckets and `updated_at` agree between the two faces and
  the two rigs), and adds a `parityArgs` entry for every M1 op with inputs — a fixture id for every path
  parameter (`{id}` per op: track, draft or run; `{device_id}`) — so `put_device_profile` (generic) runs
  through REST and `call_operation` with equal JSON; `TestParity_MultiValueQuery`:
  `search_tracks?style=a&style=b` over REST equals MCP `{"style":["a","b"]}`.
- `tools/list` golden regenerated (`go test ./internal/mcpgen -run TestToolsListGolden -update`); the
  budget constant `toolsListBaselineBytes` (M0: 4412; the test fails above 1.5× it, independent of
  `-update`) is re-baselined in review with a one-line justification next to it.
Done: `make check` green; `make openapi` committed; `schema.d.ts` regenerated; parity + golden green;
`make fixture-db` writes a fixture DB. It becomes servable (`dev --data-dir ./data/fixture`) only once
serve() builds `library.Service` + `serve.Signer`; until then every M1 op answers 503 UNAVAILABLE. That wiring
(no probe, no seeding) is owned by M1-B5 (`cmd/metamusic/cmd_serve.go`) so W1-W3 can start; B9 adds probe and seeding.

### Task M1-B5 (Media): media handler (Range) and mounting
**Files:** `cmd/metamusic/cmd_serve.go` (serve()/dev build the DataDir root, `library.Service` and
`serve.Signer` with one shared `now`, pass them through `httpserver.Options`; no probe, no seeding),
`internal/media/serve/{handler.go,handler_test.go}`, `internal/httpserver/server.go` (mount
`/media/`), `internal/httpserver/spa.go` (manifest content type, C20) + tests.
Tests first:
- `TestAcceptance_M1_MediaRangeMatrix` against a 10 000-byte deterministic object (resolver stub):
  no Range → 200, `Content-Length: 10000`; `bytes=0-1` → 206 `bytes 0-1/10000`, 2 bytes;
  `bytes=100-` → 206 `bytes 100-9999/10000`; `bytes=-500` → 206 `bytes 9500-9999/10000`;
  `bytes=9999-20000` → 206 `bytes 9999-9999/10000`; `bytes=10000-` → 416 `bytes */10000`;
  `bytes=abc` → 416; `bytes=0-1,5-6` → 206 `multipart/byteranges` whose two parts carry
  `Content-Type: audio/flac` and correct `Content-Range` (D-M1-9); `If-Range: "<etag>"` + Range → 206,
  `If-Range: "other"` → 200 full; `If-None-Match: "<etag>"` → 304; HEAD → 200 + Content-Length, empty
  body; HEAD + Range → 206; POST → 405 problem `METHOD_NOT_ALLOWED` + `Allow: GET, HEAD`;
  `Accept-Encoding: gzip` → no `Content-Encoding`; every **single-part** 2xx has `Content-Type: audio/flac` (`audio/mpeg` for `.mp3`),
  and every 2xx has `Accept-Ranges: bytes`, `ETag`, `Cache-Control: private, max-age=31536000,
  immutable`, `nosniff`.
- `TestHandler_Paths` (uppercase hex, 63 hex chars, `.wav`, extra segment → 404 problem).
- `TestHandler_DotSegmentsNeverServe` (full httpserver chain, proxy and local modes, anonymous and with
  a valid signature: `/media/x/../y`, `/media/<sha>.flac/../<sha>.flac` and `%2e%2e` variants → status
  never 200/206 and the body never contains file bytes; 301, 303, 401 and 404 are all acceptable).
- `TestHandler_Signatures` (missing exp/sig, tampered, expired, too-far exp → 403 with the C18 codes;
  valid sig + unknown sha → 404; probe object served).
- `TestHandler_MissingFile500` (resolved object whose file was removed → 500 `INTERNAL`; one log line
  with sha and ext; no `exp=`/`sig=` in logs).
- `TestAcceptance_M1_MediaTrustMatrix` (full httpserver chain, proxy and local modes, local mode with
  both an `http://` and an `https://` `PublicURL`): anonymous signed GET → 200; with spoofed
  `Remote-User`/`X-MM-Edge` and a session cookie under both M0 names (`mm_session`, and
  `__Host-mm_session`, which M0 issues when `PublicURL` is https — `auth.CookieName(secure)`) → still
  200 and no principal used; a valid local session (minted with `LocalAuth.Login` directly — M0
  serialises failed `POST /auth/login` attempts with a growing `FailDelay` and caps logins in flight,
  so tests never brute-force the form) + valid sig → 200;
  `Authorization: Bearer bogus` → 401 (C3 row 1); valid PAT + valid sig → 200; unsigned → 403;
  `/manifest.webmanifest` and `/icons/icon-192.png` anonymous → 200 (`TestHTTP_ManifestAndIconsPublic`,
  manifest `application/manifest+json`); `/api/tracks` anonymous → 401; `/api/stats` with a read-less
  PAT → 403 `FORBIDDEN_SCOPE`.
Done: `internal/media/serve` ≥ 95 % statements (unit alone).

### Task M1-B6 (Media): Analyzer (ffmpeg adapter, peaks, spectrum, FFT) + synth tones
**Files:** `internal/analysis/{peaks.go,spectrum.go,fft.go}` + tests (types from M1-B1),
`internal/analysis/ffmpeg/{ffmpeg.go,parse.go}` + tests, `internal/analysis/ffmpeg/testdata/*.txt`
(captured metadata-stream text — synthetic signals only), `internal/synth/{tone.go,ebu.go,probe.go,
retag.go}` + tests.
Tests first:
- `TestFFT_KnownBins` (N=8192, 1 kHz sine at 48 kHz → peak bin 171 (=round(1000·8192/48000)), energy
  ratio > 0.99 within ±2 bins); `TestFFT_Parseval`.
- `TestPeaks_SineAmplitude` (amplitude 0.5 → every detail max ∈ {63,64}, min ∈ {−64,−63});
  `TestPeaks_SilenceZero`; `TestPeaks_Lengths` (10 s @48k: detail spp 2400 → length 200; overview spp
  235 → length 2043; 1 s @ 44.1k: detail spp 2205 → length 20); `TestPeaks_MultiChannelMinMax`
  (L=+0.8, R=−0.4 constant → pairs (−51, 102)); `TestPeaks_JSONGolden`.
- `TestParseLoudness_Golden` (ffmpeg 9.0.1 capture → I, LRA, low/high, TP/SP dB, 1 Hz curve, −120.691 →
  −70.0), `TestParseLoudness_OtherFraming` (the same values re-framed at 50 ms and at irregular
  `pts_time`s → identical 1 Hz curve), `TestParseLoudness_TruncatedOutput` (no frames → error).
- `TestFFmpeg_VersionTokenVerbatim` (`n9.0.1`, `7.1.2-0ubuntu1`, `N-118123-gabcdef` kept as-is).
- With tools (`testtools.Require`): `TestLoudness_EBU3341_Cases` (cases 1–4 → I within ±0.1 of −23.0 /
  −33.0 / −23.0 / −23.0; true peak not asserted on these quiet signals), `TestLoudness_TruePeakLoudSine`
  (−3 dBFS 1 kHz sine → TP −3.0 ± 0.2 dBTP), `TestBandwidth_Upsampled16k` (8000 ± 200),
  `TestBandwidth_Upsampled32k` (16000 ± 300), `TestBandwidth_FullBandNoise` (≥ 23000),
  `TestBandwidth_Silence` (nil); `TestScan_FrameCountMatchesStreamInfo`; `TestScan_ExcessFramesKilled`;
  `TestFFmpeg_NewChecksCapabilities` (fake `ffmpeg` script without `ebur128` → `ErrToolMissing` naming
  it); `TestFFmpeg_ErrorCarriesStderrTail` (nonexistent input → error with ≤ 2 KiB stderr, no argv);
  `TestArgv_OnlyWorkPaths` (fake ffmpeg records argv: `-protocol_whitelist file,pipe` before `-i`;
  inputs/outputs `file:` + absolute paths or `-`).
- `TestSynthTone_LUFSWithinTolerance` (requested −14 → measured −14 ± 0.05), `TestSynth_UsesSwr` (argv
  contains `resampler=swr`, never `soxr`), `TestSynthRetag_SameAudioID`, `TestProbeFLAC` (24-bit, 48k,
  stereo, 3.0 s via flacmeta).
Done: package tests < 10 s warm (without `-short`); no Go dependency added.

### Task M1-B7 (Media): ingest pipeline (freeze, MP3, peaks, artifacts, atomic claim)
**Files:** `internal/media/ingest/{ingest.go,tags.go,claim.go,errors.go}` + tests (replacing the M1-B1
stub).
Tests first (all with tools; synthetic FLACs in both `StyleFFmpeg` and `StyleYuE2`):
- `TestFreeze_PreservesStreamInfoMD5`, `TestFreeze_PreservesFrameBytes` (pre/post `FramesSHA256` equal),
  `TestFreeze_ExactlyOneVorbisComment`, `TestFreeze_TagsWrittenOnce` (comments exactly the 8 C15 keys in
  order; title `Città è <&>` byte-exact), `TestFreeze_SeekTable` (present; first point 0; consecutive
  point gap ≤ 2 s + max block size), `TestFreeze_Mode0444AndShaAddress` (mode 0444 before the final name
  exists; sha256 of file == `MasterSHA256`; path `audio/masters/<slug>__<seed>__<yyyymmdd-hhmm>.flac`;
  existing name → `__2`).
- `TestIngest_SourceUntouched` (source passed as an opened read-only file; inbox dir 0555 + file 0444;
  sha/mode/mtime/size unchanged).
- `TestIngest_SourceChangedAbort` (sha mismatch → `SOURCE_CHANGED`; nothing left in library or
  incoming), `TestIngest_InvalidAudioRejected` (first 60 % of a FLAC → `INVALID_AUDIO`; nothing left).
- `TestIngest_MP3Rendition` (48 kHz, 320 kb/s CBR via a frame-header parse in the test; TP of a −0.2 dBTP
  source → `gain_db` −0.8; quiet source → no `-af`; two runs → identical sha).
- `TestIngest_PeaksAndAnalysesInResult` (versions `mm-loudness/1 ffmpeg <token>`),
  `TestIngest_ArtifactsCopied0444`, `TestIngest_ReplayGainFormatting` (I −12.34 → `-5.66 dB`, I −20 →
  `+2.00 dB`, silent → clamped `+24.00 dB`).
- `TestIngest_ConcurrentSameNameNoClobber` (two ingests with the same slug, seed and minute, released
  together at step 14 → `….flac` and `…__2.flac`, both 0444, each sha matching its own `Result`).
- `TestIngest_DiscardOnlyOwnFiles` (a pre-existing derived MP3 with the same sha and a pre-existing master
  under the base name: `Discard` removes only this ingest's master and `takes/<id>/` files; the shared
  derived file and the other master are untouched; `Created` lists exactly what was linked).
- `TestIngest_HostileTagsSanitised` (title with NUL, CR, LF, TAB, invalid UTF-8, a leading `--flag` and
  `=` → one `tags.txt` line per key; no title or request-id bytes in any argv).
- `TestIngest_ToolTimeouts` (fake `flac` that sleeps → `TOOL_FAILED` within timeout + WaitDelay; work dir
  removed).
Done: ingest of a 12 s synthetic track < 2 s on the CI runner; no `STUB(M1-` marker remains.

### Task M1-B8 (Import): importer (inventory, matching, placement, apply)
**Files:** `internal/importer/inbox/{inbox.go,inbox_test.go}`, `internal/importer/{importer.go,scan.go,
classify.go,recipes.go,match.go,siblings.go,place.go,inventory.go,digest.go,mapping.go,apply.go,
report.go,escape.go}` + tests, `internal/importer/layout_test.go` (test-local builder of the C21
`acceptance` layout using `flactest` header-only FLACs; M1-B9 later checks `synth.Archive` against the
same C21 numbers). Uses a fake `Ingester` returning deterministic `Result`s; real ingest arrives in M1-B9.
Tests first:
- `TestInbox_ReadOnlyHasNoWriteMethods` (method set of `*ReadOnly` is exactly `FS`, `OpenRegular`; the
  `FS()` value implements no write/remove/create interface), `TestInbox_OpenRegularRejectsSwaps` (file
  replaced by a FIFO after the scan → `ErrChanged` without blocking (2 s deadline); replaced by a
  symlink → `ErrChanged`).
- `TestClassify_Table` (≥ 40 paths incl. `.x.flac.Ab12Cd`, `a.flac.part`, `__MACOSX/x.flac`, `x.flac`
  without magic → `not_flac`, `latent.npy`, `X.FLAC`, `y.Jsonl`, six archive forms, a result dir with
  `sub/x.flac` and an unsafe artifact name, `abc_source`, a JSON array of requests and a mixed array,
  playlist-shaped JSON, index CSV header, sidecar MP3 in another directory, orphan MP3, stem matching two
  different audio identities).
- `TestScan_SymlinksIgnored` (inside and escaping the root; never followed), `TestScan_FIFONeverOpened`
  (2 s deadline), `TestScan_UnreadableReported` (mode 000 file and directory; skipped when running as
  root), `TestScan_ArchivesNotOpened` (a zip whose entries would write `../x`: nothing extracted, file
  never opened — spy FS), `TestScan_ResultDirTopLevelOnly`, `TestScan_ReadCaps` (65 MiB sparse JSONL →
  `too_large`; one 2 MiB line → `line_too_long`, other lines imported), `TestScan_CaseInsensitiveAndBOM`,
  `TestScan_JSONArrayOfRequests`, `TestScan_NonUTF8Names` (name `\xff.flac` → `sha256:`/`name:` keys
  only; no raw bytes in report, mapping or stdout), `TestScan_IgnoredNotHashed` (spy FS: ignored files
  never opened), `TestScan_Unsettled` (mtime now−30 s with MinAge 2 m → unsettled).
- `TestRecipes_RawBytesKept` (raw == line bytes exactly, incl. non-ASCII and a trailing `\r`; BOM
  excluded), `TestRecipes_InvalidLineReported` (line 10 → `not_object`, others imported),
  `TestRecipes_SourcesAllRecorded` (pilot subset: 1 recipe each, both occurrences recorded with their own
  path/line/raw; a re-run adds no rows; a new occurrence in run 2 adds exactly one), `TestRecipes_IDConflict`,
  `TestRecipes_UnterminatedLastLine` (last line without `\n` → skipped + warning; next run with the
  newline appended imports it), `TestRecipes_ABCPathConfinement` (`../x.abc`, `/etc/x`, `a\b.abc`,
  symlinked target, FIFO target, 2 MiB target → `abc_path_unresolved`; valid relative path → inlined),
  `TestRecipes_ResultDirRequestJSONSameHash`.
- `TestMatch_Rules` (one row per rule 1–8, `group_slug` producing candidates only, ambiguous,
  unmatched-with-candidates, precedence: a file matching both `tag` and `slug` reports `tag`),
  `TestMatch_DuplicateGroupStrongestRule` (result-dir copy sorting after a slug copy → `result_dir`
  wins; representative = the result-dir file), `TestMatch_RetaggedCopyAlreadyImported`,
  `TestMatch_PossibleReencodeWarning`.
- `TestSiblings_Infer` (same request id → earliest root; seed family `x`/`x-s1101`/`x_seed7`; re-run
  attaches to the existing root; older newcomer → `older_than_root`; mapping parent override; `-` →
  root), `TestSiblings_VariantsNoLineage` (takes of `<p>_001_v1..v3` → three roots, zero mutations).
- `TestPlace_DraftsForAllTracklessRecipes`, `TestPlace_BacklogAndVarianti` (group_key/label),
  `TestPlace_VariantiMotherWithSiblings`, `TestPlace_DraftMappingRow`, `TestPlace_ScoreMatchingRules`
  (stem match; `<id>-s<seed>.abc`; request-id-only name → `score_unattached`; two seeds of one id each
  get their own score), `TestPlace_ScoresAndTruncation` (result dir truncated dict → 0/1;
  `"truncated": true` → NULL + warning), `TestPlace_RefSeedMismatch`, `TestPlace_OneCollectionForPilot`.
- `TestAcceptance_M1_ImportDryRunInventory` (test-local acceptance layout; summary equals the C21
  expectations except loudness- and duration-derived values; two variants: (a) no DB → no DB file and no
  `-wal`/`-shm` afterwards; (b) an existing DB created with `store.Open` + `librarytest.Fixture` and
  closed → DB bytes unchanged. In both, the DataDir tree outside `import/` is identical before and after
  (paths, sizes, mtimes), and the report plus `mapping.todo.csv` are the only new files).
- `TestApply_Idempotent` (second run: every table's row count unchanged except `import_runs` +1),
  `TestApply_PartialFailureContinues` (fake ingester fails one take → others committed; run
  `completed`, `errors == 1`), `TestApply_FailedTakeKeepsDraftAndCollection` (the failing take belongs
  to a pilot recipe → its draft stays `backlog` and in the collection), `TestApply_InsertOrder` (drafts
  exist before `draft_collections` rows; FK holds), `TestApply_TxFailureDiscards` (inject DB error →
  `Discard` called; no track row), `TestApply_ConcurrentRunRefused` (fresh heartbeat →
  `IMPORT_IN_PROGRESS`), `TestApply_StaleHeartbeatMarkedFailed` (heartbeat 3 min old → `failed`, new run
  proceeds), `TestApply_HeartbeatRefreshed` (`testing/synctest`: refreshed every 30 s during a slow fake
  ingest), `TestApply_PlanDigestBinding` (file added after the dry-run → `PLAN_CHANGED`, nothing written;
  `Force` → applied; unchanged inbox → identical digest across dry-runs).
- `TestMappingTemplate_Written` (golden CSV incl. a draft row), `TestMapping_Parse` (comments, quoting,
  `sha256:` keys, `draft:` rows, bad rows reported), `TestMapping_PathConfinement` (absolute, `..`,
  `./x`, `a//b`, backslash, non-UTF-8, symlink, missing file → `bad_mapping_row`; clean path accepted).
- `TestReport_NoAbsolutePaths` (no `Options.Inbox` prefix, no DataDir path), `TestReport_FileNamesUnique`
  (two reports in the same second → two files), `TestReport_DigestStable`, `TestEscape_ControlChars`.
Done: package coverage ≥ 90 % with the fake ingester.

### Task M1-B9 (Stage 3, Backend): wiring, synthetic archive, CLI, acceptance
**Files:** `internal/synth/archive.go` + tests, `cmd/metamusic/{cmd_import.go,cmd_seed.go}` + tests,
`cmd/metamusic/cmd_serve.go` (M0's `boot`/`serve`/`cmdDev`: library, signer, media handler and probe
wiring; `dev --seed-synthetic`), `cmd/metamusic/main.go` (dispatch of `import`/`seed-synthetic`, usage
text, the C20 `exitError` mapping) + `main_test.go`, `internal/importer/{acceptance_test.go,privacy_test.go}`,
`internal/httpserver` probe wiring.
Tests first:
- `TestSynthArchive_MatchesC21` (the generated `acceptance` archive has exactly the C21 file list; its
  manifest counts equal C21; the FIFO and symlink exist only in this profile).
- `TestAcceptance_M1_ImportAllSongs` (profile `acceptance` + its mapping file; real ingest): exact
  counts from C21; every track has non-empty lyrics and style, seed equal to its recipe, `duration_s`
  within 0.01 s of the synthesized length, a 0444 master, an mp3 rendition, a peaks rendition, loudness
  and spectrum analyses; syn-song-02 bandwidth 8000 ± 200; sibling edges and both timed scores as
  listed; the syn-song-04 group of three is ingested once; the syn-song-05 take came from the result dir
  and its recipe has two sources; syn-song-08's recipe `abc` equals the ABC file and its source raw
  still contains `abc_path`; the varianti draft's mother is the root of the syn-song-01 lineage;
  `synb_002_v1` carries `reference` (human) and is in no collection; `synb_003_v2` has no reference.
- `TestAcceptance_M1_ImportIdempotentAndIncremental`: run 1 with only the recipe-bearing files
  (songs.jsonl, `requests/`, `abc/`, backlog, pilot, index, varianti, playlist, review) → 23 recipes,
  0 tracks, 23 backlog drafts, warning `mother_not_found`; copy the audio and the result dir in (mtimes
  in the past) → run 2: 9 tracks, the 9 corresponding drafts `fulfilled`, one new `recipe_sources` row
  (the result-dir `request.json`), the varianti draft gains its mother (`RelinkVariantiMothers`); run 3:
  every table's row count unchanged except `import_runs` (+1), report `already_imported` everywhere;
  the temp file renamed to `music/flac/syn_song_06.flac` with a newer mtime → run 4 imports it (rule
  `slug`) as a second take of syn-song-06 under the `synthetic_six` root (+1 track, +1
  `same_request_id` mutation, drafts unchanged).
- `TestAcceptance_M1_AllAtOnceEqualsIncremental` (DataDir A: one run over the full archive; DataDir B:
  runs 1 + 2 above; normalised dumps — recipes by hash, sources by (hash, path, line, raw sha), drafts by
  request id with status/origin/group/label/collections/mother's audio id, tracks by audio id with recipe
  hash, root audio id, generation and rule — are equal).
- `TestAcceptance_M1_InboxNeverModified` (snapshot of path/size/mode/mtime/sha for every inbox entry
  before/after dry-run and apply; inbox chmod read-only throughout).
- `TestAcceptance_M1_FrozenMasterInvariants` (post-import: MD5 + frames sha vs source, one
  VORBIS_COMMENT, mode 0444, sha == `/media` address; then `UPDATE tracks SET title=? WHERE id=?` → file
  sha, mode and mtime unchanged; `get_track` shows the new title).
- `TestAcceptance_M1_LoudnessSpreadReported` (`get_stats.loudness.spread_lu` = 10.0 ± 0.2 — each
  synthetic track is calibrated to ±0.05; `listen_flac_recommended` true; CLI import summary prints the
  spread line).
- `TestAcceptance_M1_BacklogDrafts` (`list_drafts` total 14, groups_total 6, collection filter 3).
- `TestPrivacy_ReferenceMetadataConfined`: a synthetic inbox whose `index.csv` puts sentinel S1 in
  `source_title`, `source_artist` and `track_n`, whose `pilot.json` puts S2 in `title`, `artist`, `why`
  and `rule`, and whose `playlist.json` puts S3 in `owner`, `url`, titles and artists. Run the dry-run
  CLI (stdout, stderr, report, `mapping.todo.csv`), the apply CLI (slog output captured), every GET op as
  a human and as an agent (REST bearer and MCP), and `metamusic openapi`. S1 appears only in human
  `get_draft` responses (and in `recipe_refs.ref_json` in a DB dump); S2 and S3 appear nowhere.
- `TestCLI_ImportExitCodes` (warnings only → 0; invalid line → 1; failed ingest → 1; bad flag → 2;
  apply without `--plan`/`--yes` → 2 with the hint; `PLAN_CHANGED` → 3; `IMPORT_IN_PROGRESS` → 4),
  `TestCLI_ImportJSONOutput`, `TestCLI_DryRunWritesOnlyImportDir`, `TestCLI_OutputEscapesControlChars`,
  `TestSeedSynthetic_E2EProfile` (counts; second invocation is a no-op), `TestSeedSynthetic_Guard`
  (a non-synthetic run exists → refused even with `--force`; plain DataDir without `MM_E2E`/`--force` →
  refused; `MM_E2E=1` → ok), `TestDev_SeedsOnlyWhenEmpty` (extends M0's `TestDevBootstrap` setup). All
  CLI tests go through M0's `do(t, env, stdin, args...)` helper with `MM_AUTH` in the env map; M0's
  `TestServeRefusesWithoutAuth` (exit 1 when `MM_AUTH` is missing) keeps passing for `serve`.
Done: `make check` < 30 s warm (recorded in metrics); `metamusic dev --seed-synthetic` shows a library.

### Task M1-W1 (Web): player store, PlayerBar, Waveform, Media Session, session loss
**Files:** `web/src/lib/{player.svelte.ts,mediaSession.ts,peaks.ts,time.ts,sections.ts}` + `*.test.ts`,
`web/svelte-bun.ts` + `web/bunfig.toml` (bun test preload compiling `*.svelte.ts` runes modules; W3's device test relies on it),
`web/src/lib/components/{PlayerBar.svelte,Waveform.svelte,SessionBanner.svelte}`,
`web/src/api/{session.ts,session.test.ts,client.ts,client.test.ts}` (remove M0's `connectionLost` /
`mm:last-reload` throttle, C22.4), `web/src/App.svelte` (mount PlayerBar and SessionBanner in place of
M0's "Connection lost" alert; THE `<audio>` stays in the footer where M0 put it, C22),
`web/src/lib/{router.ts,router.test.ts}` (`SERVER_PREFIXES` += `/media`, `/icons`,
`/manifest.webmanifest`), `web/eslint.config.js` (`svelte/no-at-html-tags`: error, set explicitly —
already on via the recommended config), `web/src/singleAudio.test.ts`,
`docs/plans/2026-10-12-m0-skeleton.md` (W1 session-loss behaviour superseded by M1 C22.4).
Tests first (`bun test`): `peaksToColumns` (2048 pairs → 300 columns: min of mins / max of maxes per
column; width > pairs → repeat; empty → []); `fmt`/`parseT` (`72`→72, `1:12`→72, `bad`→null);
`sectionAt` (timed: t=4.0 → idx 1 with boundaries [0,4),[4,8); untimed → −1); player store with a fake
media element + fake client: `play` sets `src` from links and calls `play()`; `ended` calls `play()`
synchronously on the next src (spy records no microtask gap); `MEDIA_ERR_NETWORK` re-mints once;
`MEDIA_ERR_SRC_NOT_SUPPORTED` on a FLAC → MP3 at the same position + failed probe recorded;
quality → format mapping; a deep-link seek is applied in `loadedmetadata`, not before;
`mediaSession` registers the 5 handlers, nulls seekforward/backward, re-registers on `playing`, never
calls `setPositionState` with NaN/Infinity/0 duration and clamps the position; `session.ts`: 401 →
confirmed; TypeError then 200 → nothing; TypeError twice while offline → transient; TypeError twice
online + probe `opaqueredirect` → confirmed; probe 200 → transient; confirmed while playing → banner,
no reload; confirmed while paused → reload; second confirmed loss within 30 s → banner (loop guard);
POST is never retried; M0's `client.test.ts` cases "reloads on 401" / "reloads on network TypeError" /
"throttles automatic reloads to one per window" / "flags connection lost when a reload is throttled"
are rewritten to the C22.4 rules ("adds SPA headers" stays); `isServerPath('/media/x')` is true;
`singleAudio.test.ts`.
Done: `bun run lint` (fetch ban + `no-at-html-tags`) + `bun run check` (svelte-check and
`tsc -p tsconfig.test.json`) clean; M0's `smoke.spec.ts` audio-identity check still passes.

### Task M1-W2 (Web): Library + Track detail
**Files:** `web/src/routes/{Library.svelte,Track.svelte}` (Library replaces M0's
`web/src/routes/Home.svelte`, which is deleted), `web/src/App.svelte` (route table + `{#if}` chain: `/`
→ Library, `/tracks/:id` → Track; the nav link text stays "Library"),
`web/src/lib/components/{TrackTable.svelte,LibraryFilters.svelte,LyricsPanel.svelte,MetricsCard.svelte,Pager.svelte}`,
`web/src/lib/libraryQuery.ts` + test.
Calls: `search_tracks`, `get_library_facets`, `get_stats` (header counts), `get_track`,
`get_track_peaks` (`overview` for both waveforms; `detail` only when the canvas is wider than 2048
device pixels), `get_media_links`; plus M0's `whoami` + `ping` (moved from Home into a compact Library
header strip that keeps the test ids `identity`, `via`, `version`, C22).
Tests first: `libraryQuery` round-trip (filters ⇄ query string, repeatable `style` as
`style=a&style=b`, defaults omitted); the generated client serialises an array query param as repeated
keys (matches `explode: true`).
Done: track page deep link `?t=` works (seek on `loadedmetadata`); untimed tracks show structure with no
highlight and a note "section times need a score"; truncated takes show a red badge; lyrics render as
text.

### Task M1-W3 (Web): Backlog, Draft, Import, Settings + device probe
**Files:** `web/src/routes/{Backlog.svelte,Draft.svelte,Import.svelte,Settings.svelte}` (Settings
extends M0's page and keeps its `get_system_info` block, test id `system-info`), `web/src/App.svelte`
(route table + nav: `/backlog`, `/backlog/:id`, `/import`), `web/src/lib/device.svelte.ts` + test,
`web/index.html` (`apple-touch-icon` → `/icons/apple-touch-icon.png`),
`web/public/{manifest.webmanifest,icons/apple-touch-icon.png,icons/icon-512-maskable.png}` (C22.5).
Calls: `list_drafts`, `get_draft`, `list_import_runs`, `get_import_run` (paged sections),
`get_device_probe`, `put_device_profile`, `list_device_profiles`; M0's `get_system_info` stays.
Tests first: `device.svelte.ts` with a fake Audio + fake client: boot calls only `get_device_probe` and
creates no media element; the probe starts synchronously inside the gesture callback; success → PUT with
decode/seek true, quality unchanged; `MEDIA_ERR_SRC_NOT_SUPPORTED` or `MEDIA_ERR_DECODE` → `failed`,
quality `high`, `quality_source 'probe'`; timeout or `MEDIA_ERR_NETWORK` → `inconclusive`, PUT with
NULL decode/seek, quality unchanged; a user-chosen quality is never overridden; at most 3 inconclusive
attempts per version; skipped when a result for version 1 exists.
Done: Import page renders the latest run summary and its paged sections (unmatched, ambiguous, errors);
no absolute paths exist to render; Draft page shows the reference block only when present.

### Task M1-W4 (Stage 3, Web): Playwright M1 specs
**Files:** `web/e2e/{m1-library.spec.ts,m1-track.spec.ts,m1-player.spec.ts,m1-backlog.spec.ts,m1-pwa.spec.ts,m1-import.spec.ts}`,
`web/e2e/global-setup.ts` (seed step, C22), `web/playwright.config.ts` (autoplay flags).
Specs run on M0's Playwright projects `chrome` (desktop), `chrome-pixel7`, `chrome-iphone15` (Chrome
with the iPhone 15 viewport) and `firefox` (desktop; player/FLAC/seek). Every request carries M0's
`extraHTTPHeaders` from `web/e2e/env.ts` (`Remote-User: e2e`, `X-MM-Edge: <per-run secret>`); the
"without auth" checks use `browser.newContext()` with no extra headers. M0's spec `smoke.spec.ts` stays.
- library: seeded titles listed; `q` accent-insensitive search; **two style facets selected → the
  request URL carries `style=a&style=b` and the results are their AND**; duration filter; URL restores
  filters on back/forward.
- track: `?t=3` → after play `currentTime ≥ 3`; timed track highlights `verse 1` at t≈1 s and `chorus 1`
  after seeking to its start; untimed track shows no highlight; the lyric line `<b>&` is shown as literal
  text (no `<b>` element inside the lyrics panel).
- player: **plays and seeks a FLAC master** (`currentSrc` matches `/media/[0-9a-f]{64}\.flac\?exp=`,
  `currentTime` advances ≥ 0.5 s within 3 s); **waveform click seeks** (click at 50 % → `currentTime`
  within ±0.5 s of duration/2); **media requests are signed and ranged** (a `/media/` response with
  status 206 observed); **persists across navigation while playing** (navigate Library → Backlog →
  Track; same element id, not paused, time keeps increasing); **auto-advance** (seek to duration−0.5 →
  next queue track becomes current and plays).
- backlog: groups shown with 3 drafts each; collection filter; draft detail shows lyrics structure and
  reference metadata (human session).
- pwa: manifest and icons load in a fresh context **without** auth headers (200, correct types); media
  session handlers + metadata (init-script spy on `navigator.mediaSession`); **session loss** (via
  `page.route`): 401 on `/api` while paused → full reload; 401 while playing → banner shown, no reload,
  `currentTime` keeps increasing; one `route.abort()` then success → no reload; two aborts + `/api/ping`
  fulfilled as a 302 → reload (paused); two confirmed losses within 30 s → the second shows the banner;
  **device probe** (triggered by clicking Play): stored as ok (`list_device_profiles` shows it in
  Settings); probe URL fulfilled `200 audio/flac` with random bytes → `failed`, quality `high`, the next
  play uses `.mp3`; probe URL that never responds → `inconclusive`, quality unchanged.
- import: Import page shows the seed run with counts and paged sections.
Served-operation coverage (the run-scoped teardown returned by M0's `global-setup.ts`; there is no
separate `global-teardown.ts`) must list zero unserved ops; filtered local runs set
`MM_E2E_COVERAGE=0` to skip that check.
Done: `make e2e` (`scripts/e2e.sh`) green locally on chrome + firefox; CI e2e ≤ 4 min.

### Task M1-I1 (Infra): pinned media tools, CI caching, test tiers
**Files:** `scripts/tools.sh`, `scripts/tools-selftest.sh`, `.github/workflows/ci.yml` (tools cache in
`go`/`e2e` under the same key as M0's `lint` job and `gate.yml`, `PATH`, apt `flac`,
`MM_REQUIRE_MEDIA_TOOLS=1`, `go` job without `-short`), `Makefile` (`_check` with `-short`, `test` without), `CLAUDE.md` (command
list: `make check` uses `-short`; `make tools` also installs media tools),
`docs/plans/2026-10-12-m0-skeleton.md` (I1 `check` definition), GitHub release `tools-ffmpeg-<version>`
(binary archive, FFmpeg source tarball, licence and configure files). Does not touch
`internal/testtools`.
Tests first: `scripts/tools-selftest.sh` (HEAD of every pinned URL is 2xx; a tampered sha256 makes
`tools.sh` exit non-zero and leaves no binary; a second run is a no-op); a forced cold-cache CI run (key
bumped on a branch) records the tools step ≤ 60 s; the next run hits the cache.
Done: `./bin/ffmpeg -filters | grep ebur128`, `./bin/ffmpeg -encoders | grep libmp3lame` and
`metaflac --version` work after `make tools` (or with system packages); `make check` < 30 s warm.

### Task M1-I2 (Infra): coverage gate
**Files:** `scripts/coverage-gate.sh`, `coverage.thresholds`, `Makefile` (`make cover`),
`.github/workflows/ci.yml` (`go` job `-cover` + `covdata/unit`; `e2e` job `-coverpkg=./...` +
`covdata/e2e` instead of M0's `covdata`; new `coverage` job; artifacts `covdata-unit`, `covdata-e2e`),
`scripts/e2e.sh` (same build flags and `covdata/e2e`; `PATH=$root/bin:$PATH`), `scripts/loop` +
`metrics/loop.schema.json` + `scripts/metrics-row.jq` allowlists (`cover`, `routing`, `seed`; C23),
`docs/plans/2026-10-12-m0-skeleton.md` (C9 allowlist).
Tests first: `scripts/coverage-gate-selftest.sh` (a synthetic textfmt profile below threshold → exit 1
naming the package and percentages; above → 0); thresholds parser rejects unknown patterns.
Done: CI `coverage` job enforces the C23 thresholds on merged unit + e2e data.

### Task M1-I3 (Infra): routing contract + deploy examples (C24)
**Files:** `deploy/labels.example.yml` (C24 routers and middleware order for `mm.example.com`),
`deploy/authelia-rules.example.yml`, `deploy/traefik-contract/{compose.yml,stub-forwardauth/main.go,
echo/main.go,contract_test.go}` (build tag `routing`; pinned `traefik:v3.7` image digest; the test
compose adds a test-only per-router `X-Test-Router` response header), `Makefile` (`make routing`),
`.github/workflows/routing.yml`.
Tests first (`TestRoutingContract`, table over every router):
- routing: `/media/x`, `/rest/x`, `/worker/x` → mm-media; `/mcp`, `/mcp/x`, `/.well-known/oauth-protected-resource/mcp`,
  `/oauth/token`, `/api/openapi.json` → mm-agent; bearer `/api/tracks` → mm-api-bearer;
  `/manifest.webmanifest`, `/icons/a.png` → mm-public; none of these reach the stub forward-auth;
  `/restore`, `/mediax`, `/api/tracks` without bearer, `/human/api/x` with a bearer → mm-ui (stub
  called);
- identity: spoofed `Remote-User`, `Remote-Groups`, `Remote-Name`, `Remote-Email`, `X-MM-Edge` (case and
  duplicate variants) arrive empty at the echo backend on every non-ui router; on mm-ui `Remote-User`
  equals the stub's user and `X-MM-Edge` arrives exactly once with the configured value;
- middleware order (parsed from `deploy/labels.example.yml`): `mm-strip-identity` first on every
  non-ui router and absent from mm-ui; mm-ui order authelia, mm-edge-mark, sec-headers, mm-compress;
  no compress on mm-media;
- with the stub at one factor `POST /human/api/x` is rejected and at two factors allowed;
- XHR check: mm-ui `/api/tracks` with `X-Requested-With: XMLHttpRequest` and no session → 401; the same
  request as a navigation → 302;
- `/media/` responses are not compressed.
Done: `make routing` green locally (docker); CI runs it from a separate workflow
`.github/workflows/routing.yml` with `on.push.paths` / `on.pull_request.paths` = `deploy/**` and the
workflow file itself (GitHub has no per-job path filter; M0 likewise keeps the gate in its own
`gate.yml` because it needs different triggers); same pinned actions and `permissions` layout as M0.

### Task M1-I4 (Infra): image, compose, docs
**Files:** `Dockerfile` (RUN check: `ffmpeg -hide_banner -filters | grep -q ' ebur128 '`,
`ffmpeg -hide_banner -encoders | grep -q libmp3lame`, `metaflac --version`; M0's runtime stage is
`alpine:3.24` with `apk add ffmpeg flac` and already runs as `USER 1000:1000` with `MM_DATA_DIR=/data`,
`MM_SECRETS_DIR=/run/secrets`), `deploy/compose.example.yml` (M0: named volume `metamusic-data:/data`,
`read_only: true`, `tmpfs: /tmp`, `cap_drop: [ALL]`, file secret `secret_key`; M1 adds an explicit
`user: "1000:1000"` and a nested read-only bind `./data/inbox:/data/inbox:ro` — the host side is
relative to `deploy/` and sits under a gitignored `data/` dir; the container side is the `MM_INBOX`
default under the container's DataDir `/data`, not a host-style path), `docs/architecture.md`
(M1 section, public-safe), `docs/device-checklist.md` (M0's untagged template gains an M1 section with
the ids below), `README.md` (status line; Configuration table gains `MM_INBOX`, `MM_FFMPEG`, `MM_FLAC`,
`MM_METAFLAC`; import quick start with synthetic data: dry-run, then `--plan`).
Device checklist M1:
- DC-M1-1 install the PWA (iPhone + Android): icon, name, standalone window.
- DC-M1-2 play a 24-bit/48 kHz FLAC master; seek by waveform; seek by lock-screen scrubber.
- DC-M1-3 lock-screen controls: play/pause/next/prev work; no ±10 s buttons.
- DC-M1-4 auto-advance with the screen locked (record result; known WebKit bugs).
- DC-M1-5 forward-auth session expiry while playing: audio continues; the "sign in again" banner
  appears; no automatic reload while playing; tapping the banner reloads into login. Airplane mode
  toggled while playing: no reload, only the offline notice.
- DC-M1-6 device probe: the first Play tap runs it; Settings shows `ok` on both phones (an
  `inconclusive` result is recorded as such and never downgrades); `crossorigin="use-credentials"`
  manifest variant tried.
Done: `docker build .` green; container `/healthz` ok; `metamusic import --dry-run` runs inside it
against a read-only mounted synthetic inbox as uid 1000.

### Task M1-X1 (Integration)
Merge lane branches into `m1`; `make tools hooks check test e2e gate cover routing` green;
`grep -rn 'STUB(M1-' internal` empty; regenerate `openapi.json` + `schema.d.ts` (`make openapi`); update
`metrics/REPORT.md` with `make metrics` (M0's `scripts/metrics-report.sh`; it aggregates warm, successful
runs per target) plus the CI wall times incl. the cold tools path and the coverage numbers; review (spec
+ quality + security: signed URLs, no query logging, inbox read-only, triggers, argv rule, C16.9
privacy, `TestNoTestHelpersInBinary`); copy D-M1-1 … D-M1-10 into the private design document (§3.5,
§5.2, §5.3 amendments).

### Task M1-X2 (Deploy + real archive, operator-assisted; host details and results only in private notes)
1. **Prerequisites (operator):** `secret_key` and `edge_secret` exist in the secrets dir (created with
   shell tools, never echoed). Authelia rules per C24 are added and checked with `authelia config
   validate`; the Authelia restart (it logs everyone out) happens in a window scheduled in advance and
   recorded in the private notes.
2. **Deploy only via `deploy.sh` from a clean detached worktree of `origin/main`** (fetch → worktree →
   `docker build` → backup → export the edge secret via shell `cat` → `docker compose up -d --no-deps
   --no-build metamusic` → wait healthy → smoke). Never run a bare `docker compose up -d` or `docker
   compose config` (use `--quiet`): the shared compose file holds other services and interpolated
   secrets.
3. **First deploy only:** `docker compose create --no-deps metamusic` creates the `mm-edge` network;
   `docker network connect --ip <pinned edge IP> mm-edge traefik` attaches Traefik live without
   recreating it; `MM_TRUSTED_PROXY` is exactly that /32 (address in private notes).
4. **Smoke tests:** `/` redirects to the SSO login; `/manifest.webmanifest` → 200 without a cookie;
   `/.well-known/…` reaches the app (app response, not the SSO redirect — M0 serves no `.well-known`
   document yet, so the app answers with its SPA shell); `/mcp` with a spoofed
   `Remote-User` → 401 with `WWW-Authenticate: Bearer resource_metadata=…`; a bogus bearer on
   `/api/tracks` → the app's JSON 401, not the SSO's; a bearer on `/human/api/…` is refused; an unsigned
   `/media/<sha>.flac` → 403 problem; a link minted with `get_media_links` → 206 for a Range request;
   **Authelia 4.39 XHR check:** `/api/stats` with `X-Requested-With: XMLHttpRequest` and an expired
   session → 401, not a redirect (record the result; on failure C22.4's opaque-redirect path covers it).
5. **Inbox:** the host inbox is bind-mounted read-only at `<DataDir>/inbox`; the container (uid 1000)
   needs read access to every file — fix modes/ACLs on the host, never through the app; the dry-run lists
   anything `unreadable`.
6. **Import:** `docker exec metamusic metamusic import --dry-run` → review the report against Appendix
   P.2 → fill `mapping.csv` → `docker exec metamusic metamusic import --plan <in-container report path>`;
   as files arrive, repeat dry-run → review → `--plan`.
7. **Gate patterns:** add the real request-id prefix (and any other private token seen in the report) to
   the untracked `.public-gate-patterns` (read by the pre-commit `--staged` and pre-push
   `--history --require-patterns` hooks) **and** to the `PUBLIC_GATE_PATTERNS` Actions secret that
   M0's `.github/workflows/gate.yml` passes with `--require-patterns`.
8. Record the real loudness spread (`get_stats`) once real masters are imported → decide D11
   (`listen.flac`).
9. Run the device checklist; tag `v0.1.0-m1` per the Definition of done.

## Definition of done (M1)
- All named tests above green; `make check` < 30 s warm; `make test` green with media tools; `make e2e`
  green (chrome + firefox) with zero unserved ui ops; `make gate` green; `make routing` green; coverage
  gates met on merged data.
- `metamusic import --dry-run` and `metamusic import --plan` behave per C16 on the synthetic archive;
  the inbox is provably untouched; the dry-run writes nothing outside `import/`; a second run inserts
  nothing but its run row; one-shot and incremental imports agree.
- Every master is frozen (one VORBIS_COMMENT, unchanged frames/MD5, 0444, sha-addressed).
- **Real archive:** M1 is done when the synthetic acceptance passes **and** a real dry-run has been
  reviewed by the operator (M1-X2 step 6; differences from Appendix P.2 explained in the private notes).
  The real "[S] all songs imported" criterion cannot pass while masters still lack their recipes
  (D-M1-2); it stays tracked in the private notes until the archive is complete and does not block
  `v0.1.0-m1`.
- No real titles, artists, lyrics, hostnames, id prefixes or personal data in any tracked file (gate +
  review); the public plan copy contains nothing below the private-appendix marker.

## Risks / open questions
- **Firefox headless audio sink in CI:** if `currentTime` does not advance without an audio device,
  add a `media.cubeb.backend` null-sink pref or a PulseAudio null sink in the e2e job (validate in M1-W4).
- **ffmpeg version drift** (CI LGPL static build vs Alpine runtime): tests use tolerances (±0.1 LU, ±200
  Hz) and frame-agnostic parsing; analyses store the raw ffmpeg version token; MP3 bytes are deterministic
  only per build (no cross-build sha asserts).
- **Unknown shapes of the not-yet-copied files** (`songs.jsonl`, per-song JSON, result dirs, `varianti/`):
  classification is content-based, every unknown lands in `ignored:*` with a reason, and the plan digest
  makes the operator review surprises before any write; rules can be added without schema changes.
- **ABC dialect:** bar counting is validated only on synthetic scores; the first real `score.abc` goes
  through `TestParseScore` as a sanitised structural fixture (bar/section skeleton only, no notes or
  lyrics copied) before timed highlighting is trusted. Repeats make a score untimed until then.
- **Authelia XHR behaviour** unverified until M1-X2; C22.4 does not depend on it.
- **iOS probe behaviour** (gesture requirement documented by Apple, unverified on iOS 26): the probe is
  gesture-driven and treats timeouts as inconclusive, so a wrong guess costs a retry, not a downgrade.
- **Large inboxes:** inventory hashes every candidate file each run (fine at tens of GB/minute local
  disk); revisit with a (path, size, mtime, inode) cache only if the dry-run exceeds ~30 s.
- **Unterminated last lines:** a complete JSONL saved without a final newline is skipped (by design)
  until the newline is added; the warning names the file.

