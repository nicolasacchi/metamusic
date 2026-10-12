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
