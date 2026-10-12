-- 0003_judge.sql — M2a: draft revisions, ratings, tags, comments, playlists, listen events,
-- exports, audit log, idempotency, devices, pairing codes, backup runs.

-- Drafts become editable (C32). New columns only; M1's recipe_drafts_frozen keeps
-- id, origin, source_recipe_id and created_at frozen.
ALTER TABLE recipe_drafts ADD COLUMN revision   INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1);
ALTER TABLE recipe_drafts ADD COLUMN created_by TEXT NOT NULL DEFAULT 'import';
ALTER TABLE recipe_drafts ADD COLUMN updated_by TEXT NOT NULL DEFAULT 'import';
CREATE TRIGGER recipe_drafts_created_by_frozen BEFORE UPDATE OF created_by ON recipe_drafts
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: frozen column'); END;
CREATE TRIGGER recipe_drafts_fulfilled_frozen BEFORE UPDATE OF request_id, request_json ON recipe_drafts
WHEN OLD.status = 'fulfilled'
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: fulfilled drafts are immutable'); END;

CREATE TABLE ratings (                                 -- one current row per (track, source); D-M2a-12
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  source         TEXT NOT NULL CHECK (source IN ('human','agent')),
  stars          INTEGER CHECK (stars IS NULL OR stars BETWEEN 1 AND 5),      -- NULL = cleared
  verdict        TEXT CHECK (verdict IS NULL OR length(verdict) BETWEEN 1 AND 280),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id   TEXT NOT NULL,                        -- Principal.Key()
  principal_name TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (track_id, source),
  CHECK ((source = 'human') = (principal_kind IN ('human','device')))   -- rating isolation, at the schema level
) WITHOUT ROWID;
CREATE INDEX ratings_source_stars ON ratings(source, stars);
CREATE TRIGGER ratings_no_delete BEFORE DELETE ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: no physical delete'); END;
CREATE TRIGGER ratings_frozen BEFORE UPDATE OF track_id, source, created_at ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: frozen column'); END;

CREATE TABLE tags (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40),  -- normalised (C31)
  created_at TEXT NOT NULL
);
CREATE TRIGGER tags_immutable BEFORE UPDATE ON tags
BEGIN SELECT RAISE(ABORT, 'tags: immutable'); END;
CREATE TRIGGER tags_no_delete BEFORE DELETE ON tags
BEGIN SELECT RAISE(ABORT, 'tags: no physical delete'); END;

CREATE TABLE track_tags (
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  tag_id         INTEGER NOT NULL REFERENCES tags(id),
  source         TEXT NOT NULL CHECK (source IN ('human','agent','inherited')),
  inherited_from TEXT REFERENCES tracks(id),           -- the parent track (source = 'inherited' only)
  principal_id   TEXT NOT NULL,                        -- who added or re-added it
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  removed_at     TEXT,                                 -- D-M2a-4: soft removal
  removed_by     TEXT,
  PRIMARY KEY (track_id, tag_id, source),
  CHECK ((source = 'inherited') = (inherited_from IS NOT NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
) WITHOUT ROWID;
CREATE INDEX track_tags_active ON track_tags(tag_id, track_id) WHERE removed_at IS NULL;
CREATE TRIGGER track_tags_no_delete BEFORE DELETE ON track_tags
BEGIN SELECT RAISE(ABORT, 'track_tags: no physical delete'); END;
CREATE TRIGGER track_tags_frozen BEFORE UPDATE OF track_id, tag_id, source, inherited_from, created_at ON track_tags
BEGIN SELECT RAISE(ABORT, 'track_tags: frozen column'); END;

CREATE TABLE comments (
  id                 TEXT PRIMARY KEY,                 -- UUIDv7
  track_id           TEXT NOT NULL REFERENCES tracks(id),
  body               TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
  at_second          REAL CHECK (at_second IS NULL OR at_second >= 0),
  section_label      TEXT,
  section_source     TEXT CHECK (section_source IS NULL OR section_source IN ('plan','measured','manual')),  -- never 'lyrics' (D-M2a-16)
  section_confidence REAL CHECK (section_confidence IS NULL OR section_confidence BETWEEN 0 AND 1),
  kind               TEXT NOT NULL,                    -- Go-validated: note|fix|keep|marker
  state              TEXT NOT NULL,                    -- Go-validated state machine (C31): open|addressed|resolved|regressed
  source             TEXT NOT NULL,                    -- Go-validated: web|api|mcp|stdio (subsonic, headset later)
  principal_kind     TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id       TEXT NOT NULL,
  principal_name     TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  edited_at          TEXT,
  state_changed_at   TEXT,
  state_changed_by   TEXT,
  hidden_at          TEXT,
  hidden_by          TEXT,
  CHECK ((section_label IS NULL) = (section_source IS NULL)),
  CHECK (section_label IS NULL OR at_second IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL)),
  CHECK ((state_changed_at IS NULL) = (state_changed_by IS NULL))
);
CREATE INDEX comments_track ON comments(track_id, created_at);
CREATE INDEX comments_open  ON comments(track_id) WHERE state = 'open' AND hidden_at IS NULL;
CREATE TRIGGER comments_no_delete BEFORE DELETE ON comments
BEGIN SELECT RAISE(ABORT, 'comments: no physical delete'); END;
CREATE TRIGGER comments_frozen BEFORE UPDATE OF
  id, track_id, at_second, section_label, section_source, section_confidence, source,
  principal_kind, principal_id, principal_name, created_at ON comments
BEGIN SELECT RAISE(ABORT, 'comments: frozen column'); END;
-- Mutable: body, kind, state, updated_at, edited_at, state_changed_*, hidden_*.

CREATE TABLE playlists (
  id          TEXT PRIMARY KEY,                        -- UUIDv7
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
  items_json  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(items_json) AND json_type(items_json) = 'array'),
  items_count INTEGER NOT NULL DEFAULT 0 CHECK (items_count = json_array_length(items_json)),
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  created_by  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  hidden_at   TEXT
);
CREATE TRIGGER playlists_no_delete BEFORE DELETE ON playlists
BEGIN SELECT RAISE(ABORT, 'playlists: no physical delete'); END;
CREATE TRIGGER playlists_frozen BEFORE UPDATE OF id, created_by, created_at ON playlists
BEGIN SELECT RAISE(ABORT, 'playlists: frozen column'); END;

CREATE TABLE listen_events (                           -- D-M2a-8; immutable telemetry
  id             TEXT PRIMARY KEY,                     -- client UUID (v7 recommended)
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  session_id     TEXT NOT NULL CHECK (length(session_id) BETWEEN 8 AND 64),
  kind           TEXT NOT NULL,                        -- Go-validated: start|segment|seek|complete|skip
  from_ms        INTEGER CHECK (from_ms IS NULL OR from_ms >= 0),
  to_ms          INTEGER CHECK (to_ms IS NULL OR to_ms >= 0),
  format         TEXT,                                 -- Go-validated: flac|mp3|NULL
  device_id      TEXT,                                 -- C22.3 device-profile id (not a credential)
  source         TEXT NOT NULL,                        -- Go-validated: web|api|mcp|stdio
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id   TEXT NOT NULL,
  client_at      TEXT NOT NULL,
  received_at    TEXT NOT NULL,
  CHECK (to_ms IS NULL OR from_ms IS NULL OR to_ms >= from_ms)
);
CREATE INDEX listen_events_track ON listen_events(track_id, client_at);
CREATE TRIGGER listen_events_immutable BEFORE UPDATE ON listen_events
BEGIN SELECT RAISE(ABORT, 'listen_events: immutable'); END;
CREATE TRIGGER listen_events_no_delete BEFORE DELETE ON listen_events
BEGIN SELECT RAISE(ABORT, 'listen_events: no physical delete'); END;

CREATE TABLE exports (                                 -- D-M2a-7
  id           TEXT PRIMARY KEY,                       -- UUIDv7
  kind         TEXT NOT NULL,                          -- Go-validated: songs_jsonl|songs_bundle|playlist_zip
  params_json  TEXT NOT NULL CHECK (json_valid(params_json)),
  sha256       TEXT NOT NULL,
  bytes        INTEGER NOT NULL CHECK (bytes > 0),
  path         TEXT NOT NULL,                          -- derived/exports/<aa>/<sha>.<ext> (DataDir-relative)
  filename     TEXT NOT NULL CHECK (length(filename) BETWEEN 1 AND 120),
  summary_json TEXT NOT NULL CHECK (json_valid(summary_json)),
  principal_id TEXT NOT NULL,
  created_at   TEXT NOT NULL
);
CREATE INDEX exports_sha ON exports(sha256);
CREATE TRIGGER exports_immutable BEFORE UPDATE ON exports
BEGIN SELECT RAISE(ABORT, 'exports: immutable'); END;
CREATE TRIGGER exports_no_delete BEFORE DELETE ON exports
BEGIN SELECT RAISE(ABORT, 'exports: no physical delete'); END;

CREATE TABLE audit_log (                               -- design §4.4; append-only
  id               TEXT PRIMARY KEY,                   -- UUIDv7 (time-ordered; keyset cursor)
  at               TEXT NOT NULL,
  principal_kind   TEXT NOT NULL,                      -- human|device|agent|system|anonymous
  principal_id     TEXT NOT NULL,
  principal_name   TEXT NOT NULL,
  transport        TEXT NOT NULL,                      -- Go-validated: rest|spa|mcp|stdio|cli|pair
  client           TEXT NOT NULL DEFAULT '',           -- reqinfo.SanitizeClient
  op               TEXT NOT NULL,                      -- operationId, 'cli:<cmd>', 'auth:pair' or '-' (no op matched)
  method           TEXT NOT NULL,
  args_sha256      TEXT NOT NULL,                      -- hash of the canonical request; never the args
  status           INTEGER NOT NULL,
  error_code       TEXT,
  cost_micro_usd   INTEGER NOT NULL DEFAULT 0 CHECK (cost_micro_usd >= 0),
  idempotency      TEXT NOT NULL,                      -- none|stored|replayed|conflict|in_progress|missing
  created_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(created_ids_json)),
  duration_ms      INTEGER NOT NULL CHECK (duration_ms >= 0)
);
CREATE INDEX audit_log_at        ON audit_log(at);
CREATE INDEX audit_log_principal ON audit_log(principal_id, id);
CREATE INDEX audit_log_op        ON audit_log(op, id);
CREATE TRIGGER audit_log_immutable BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log: append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log: append-only'); END;

CREATE TABLE idempotency (                             -- cache table: pruned after expiry (no triggers)
  principal_key  TEXT NOT NULL,
  key            TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
  op             TEXT NOT NULL,
  request_sha256 TEXT NOT NULL,
  derived        INTEGER NOT NULL CHECK (derived IN (0,1)),
  state          TEXT NOT NULL CHECK (state IN ('pending','done')),
  status         INTEGER,
  content_type   TEXT,
  response       BLOB,                                 -- secret fields nulled (C29)
  created_at     TEXT NOT NULL,
  lease_until    TEXT NOT NULL,                        -- pending rows: reclaimable after this (D-M2a-21)
  expires_at     TEXT NOT NULL,
  PRIMARY KEY (principal_key, key),
  CHECK ((state = 'done') = (status IS NOT NULL AND response IS NOT NULL))
) WITHOUT ROWID;
CREATE INDEX idempotency_expires ON idempotency(expires_at);

CREATE TABLE devices (                                 -- paired device sessions (C26)
  id           TEXT PRIMARY KEY,                       -- UUIDv7
  name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  prefix       TEXT NOT NULL,
  hmac_hash    TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL CHECK (scopes = 'read,write'),   -- never generate or admin
  user_agent   TEXT NOT NULL CHECK (length(user_agent) <= 512),
  paired_by    TEXT NOT NULL,                          -- Principal.Key() of the human who minted the code
  created_at   TEXT NOT NULL,
  last_seen_at TEXT,
  expires_at   TEXT NOT NULL,
  revoked_at   TEXT
);
CREATE TRIGGER devices_no_delete BEFORE DELETE ON devices
BEGIN SELECT RAISE(ABORT, 'devices: no physical delete'); END;
CREATE TRIGGER devices_frozen BEFORE UPDATE OF id, prefix, hmac_hash, scopes, paired_by, created_at ON devices
BEGIN SELECT RAISE(ABORT, 'devices: frozen column'); END;

CREATE TABLE pairing_codes (
  id          TEXT PRIMARY KEY,
  hmac_hash   TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  redeemed_at TEXT,
  device_id   TEXT REFERENCES devices(id),
  revoked_at  TEXT,
  CHECK ((redeemed_at IS NULL) = (device_id IS NULL))
);
CREATE TRIGGER pairing_codes_no_delete BEFORE DELETE ON pairing_codes
BEGIN SELECT RAISE(ABORT, 'pairing_codes: no physical delete'); END;
CREATE TRIGGER pairing_codes_frozen BEFORE UPDATE OF id, hmac_hash, name, created_by, created_at, expires_at ON pairing_codes
BEGIN SELECT RAISE(ABORT, 'pairing_codes: frozen column'); END;
CREATE TRIGGER pairing_codes_single_use BEFORE UPDATE OF redeemed_at, device_id ON pairing_codes
WHEN OLD.redeemed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'pairing_codes: already redeemed'); END;

CREATE TABLE backup_runs (
  id           TEXT PRIMARY KEY,                       -- UUIDv7 (= manifest id)
  cause        TEXT NOT NULL,                          -- Go-validated: schedule|api|cli
  principal_id TEXT NOT NULL,
  state        TEXT NOT NULL CHECK (state IN ('running','completed','failed')),
  snapshot     TEXT,                                   -- snapshot directory name (never an absolute path)
  started_at   TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,
  finished_at  TEXT,
  stats_json   TEXT CHECK (stats_json IS NULL OR json_valid(stats_json)),
  error        TEXT                                    -- redacted (internal/redact)
);
CREATE INDEX backup_runs_started ON backup_runs(started_at);
CREATE UNIQUE INDEX backup_runs_one_running ON backup_runs(state) WHERE state = 'running';  -- cross-process guard (C34)
CREATE TRIGGER backup_runs_no_delete BEFORE DELETE ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs: no physical delete'); END;
CREATE TRIGGER backup_runs_final BEFORE UPDATE ON backup_runs WHEN OLD.state <> 'running'
BEGIN SELECT RAISE(ABORT, 'backup_runs: finished runs are immutable'); END;
CREATE TRIGGER backup_runs_frozen BEFORE UPDATE OF id, cause, principal_id, started_at ON backup_runs
BEGIN SELECT RAISE(ABORT, 'backup_runs: frozen column'); END;
