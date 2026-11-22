-- 0027_morning_crate.sql: M6N-C3 (C382). The Morning Crate: a blind commute tape built daily from unjudged takes. Additive and
-- up-only: five new tables, no existing table is touched. Rows are never deleted (the 7-day retention removes FILES and marks the
-- side rows), and every column that decides a reveal or a mapping is set once.

-- Settings: one row, seeded with the operator's defaults (07:00, 40 minutes, tape kept 7 days).
CREATE TABLE morning_crate_settings (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  enabled           INTEGER NOT NULL CHECK (enabled IN (0,1)),
  at                TEXT NOT NULL CHECK (at GLOB '[0-2][0-9]:[0-5][0-9]'),   -- local HH:MM (MM_TZ); Go-validated to 00:00..23:59
  minutes           INTEGER NOT NULL CHECK (minutes BETWEEN 5 AND 120),
  side_minutes      INTEGER NOT NULL CHECK (side_minutes BETWEEN 5 AND 15),
  max_takes         INTEGER NOT NULL CHECK (max_takes BETWEEN 1 AND 30),
  anchors           INTEGER NOT NULL CHECK (anchors BETWEEN 0 AND 2),
  include_truncated INTEGER NOT NULL CHECK (include_truncated IN (0,1)),
  keep_days         INTEGER NOT NULL CHECK (keep_days BETWEEN 1 AND 30),
  wait_minutes      INTEGER NOT NULL CHECK (wait_minutes BETWEEN 0 AND 120),
  updated_by        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);
INSERT INTO morning_crate_settings(id, enabled, at, minutes, side_minutes, max_takes, anchors, include_truncated, keep_days, wait_minutes,
  updated_by, updated_at) VALUES (1, 1, '07:00', 40, 15, 14, 1, 0, 7, 45, 'system:migration', '2026-11-21T00:00:00.000Z');
CREATE TRIGGER morning_crate_settings_no_delete BEFORE DELETE ON morning_crate_settings
BEGIN SELECT RAISE(ABORT, 'morning_crate_settings: no physical delete'); END;

-- One crate per build. schedule_key ('morning:YYYY-MM-DD', the local day) is unique, so a scheduled day builds once even across
-- two processes; a manual build has no key.
CREATE TABLE morning_crates (
  id                TEXT PRIMARY KEY,                                           -- UUIDv7
  schedule_key      TEXT UNIQUE CHECK (schedule_key IS NULL OR schedule_key GLOB 'morning:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  trigger           TEXT NOT NULL CHECK (trigger IN ('scheduled','manual')),
  state             TEXT NOT NULL CHECK (state IN ('building','ready','empty','failed')),
  error_code        TEXT CHECK (error_code IS NULL OR length(error_code) BETWEEN 1 AND 64),
  seed              INTEGER NOT NULL,                                           -- blind-order seed, kept so a build is reproducible
  options_json      TEXT NOT NULL CHECK (json_valid(options_json) AND json_type(options_json) = 'object'),
  excluded_json     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(excluded_json) AND json_type(excluded_json) = 'array'),
  adjacency_relaxed INTEGER NOT NULL DEFAULT 0 CHECK (adjacency_relaxed IN (0,1)),
  takes             INTEGER NOT NULL DEFAULT 0 CHECK (takes >= 0),
  anchors           INTEGER NOT NULL DEFAULT 0 CHECK (anchors >= 0),
  duration_ms       INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
  created_by        TEXT NOT NULL,
  created_at        TEXT NOT NULL,
  built_at          TEXT,
  revealed_at       TEXT,
  revealed_by       TEXT,
  playlist_id       TEXT REFERENCES playlists(id),
  CHECK ((state = 'building') = (built_at IS NULL)),
  CHECK ((state = 'failed') = (error_code IS NOT NULL)),
  CHECK ((revealed_at IS NULL) = (revealed_by IS NULL)),
  CHECK (revealed_at IS NULL OR state = 'ready'),                               -- only a crate with sides is revealed
  CHECK (playlist_id IS NULL OR revealed_at IS NOT NULL)                        -- the native playlist exists only after the reveal (D-M6N-OP-4)
);
CREATE INDEX morning_crates_created ON morning_crates(created_at, id);
CREATE TRIGGER morning_crates_no_delete BEFORE DELETE ON morning_crates
BEGIN SELECT RAISE(ABORT, 'morning_crates: no physical delete'); END;
CREATE TRIGGER morning_crates_frozen BEFORE UPDATE OF id, schedule_key, trigger, seed, options_json, created_by, created_at ON morning_crates
BEGIN SELECT RAISE(ABORT, 'morning_crates: frozen column'); END;
CREATE TRIGGER morning_crates_transition BEFORE UPDATE OF state ON morning_crates
WHEN NEW.state IS NOT OLD.state AND OLD.state <> 'building'
BEGIN SELECT RAISE(ABORT, 'morning_crates: a finished crate keeps its state'); END;
-- the build summary is written with the state change out of 'building' and never after
CREATE TRIGGER morning_crates_summary_final BEFORE UPDATE OF excluded_json, adjacency_relaxed, takes, anchors, duration_ms, built_at, error_code
  ON morning_crates WHEN OLD.state <> 'building'
BEGIN SELECT RAISE(ABORT, 'morning_crates: a finished crate keeps its summary'); END;
-- reveal once, playlist once
CREATE TRIGGER morning_crates_reveal_once BEFORE UPDATE OF revealed_at, revealed_by ON morning_crates
WHEN OLD.revealed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'morning_crates: already revealed'); END;
CREATE TRIGGER morning_crates_playlist_once BEFORE UPDATE OF playlist_id ON morning_crates
WHEN OLD.playlist_id IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'morning_crates: playlist already set'); END;

-- Chapters, written once when the build finishes (so a failed build leaves no item and its takes stay eligible).
-- Positions are frames at 48 kHz relative to the side (C378.6).
CREATE TABLE morning_crate_items (
  crate_id         TEXT NOT NULL REFERENCES morning_crates(id),
  n                INTEGER NOT NULL CHECK (n >= 1),                              -- global chapter number = the slate number
  side_index       INTEGER NOT NULL CHECK (side_index >= 0),
  track_id         TEXT NOT NULL REFERENCES tracks(id),
  anchor           INTEGER NOT NULL CHECK (anchor IN (0,1)),
  source           TEXT NOT NULL CHECK (source IN ('manual','night_shift','job')),
  start_frame      INTEGER NOT NULL CHECK (start_frame >= 0),
  audio_start_frame INTEGER NOT NULL,
  end_frame        INTEGER NOT NULL,
  gain_cdb         INTEGER NOT NULL,
  limited          INTEGER NOT NULL CHECK (limited IN (0,1)),
  lufs_after       REAL,                                                         -- measured in the built file; NULL when not measured
  PRIMARY KEY (crate_id, n),
  UNIQUE (crate_id, track_id),
  CHECK (start_frame <= audio_start_frame AND audio_start_frame < end_frame)
) WITHOUT ROWID;
CREATE INDEX morning_crate_items_track ON morning_crate_items(track_id);
CREATE TRIGGER morning_crate_items_immutable BEFORE UPDATE ON morning_crate_items
BEGIN SELECT RAISE(ABORT, 'morning_crate_items: immutable'); END;
CREATE TRIGGER morning_crate_items_no_delete BEFORE DELETE ON morning_crate_items
BEGIN SELECT RAISE(ABORT, 'morning_crate_items: no physical delete'); END;

-- Sides: one derived FLAC each. A file is shared by every crate with its sha; the sweep marks removed_at, never deletes the row.
CREATE TABLE morning_crate_sides (
  crate_id   TEXT NOT NULL REFERENCES morning_crates(id),
  side_index INTEGER NOT NULL CHECK (side_index >= 0),
  sha256     TEXT NOT NULL CHECK (length(sha256) = 64),
  path       TEXT NOT NULL CHECK (path LIKE 'derived/morning/%'),
  bytes      INTEGER NOT NULL CHECK (bytes > 0),
  samples    INTEGER NOT NULL CHECK (samples > 0),
  removed_at TEXT,
  PRIMARY KEY (crate_id, side_index)
) WITHOUT ROWID;
CREATE INDEX morning_crate_sides_sha ON morning_crate_sides(sha256);
CREATE TRIGGER morning_crate_sides_frozen BEFORE UPDATE OF crate_id, side_index, sha256, path, bytes, samples ON morning_crate_sides
BEGIN SELECT RAISE(ABORT, 'morning_crate_sides: frozen column'); END;
CREATE TRIGGER morning_crate_sides_removed_once BEFORE UPDATE OF removed_at ON morning_crate_sides
WHEN OLD.removed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'morning_crate_sides: removal is final'); END;
CREATE TRIGGER morning_crate_sides_no_delete BEFORE DELETE ON morning_crate_sides
BEGIN SELECT RAISE(ABORT, 'morning_crate_sides: no physical delete'); END;

-- Raw headset and rating events (written by the reveal lane). Before the reveal an event maps to nothing; the mapping columns are
-- set once, in the reveal transaction (or the event's own insert transaction after it).
CREATE TABLE morning_crate_events (
  id              TEXT PRIMARY KEY,                                              -- client UUIDv7 (idempotent intake)
  crate_id        TEXT NOT NULL REFERENCES morning_crates(id),
  side_index      INTEGER NOT NULL CHECK (side_index >= 0),
  chapter         INTEGER NOT NULL CHECK (chapter >= 1),
  kind            TEXT NOT NULL CHECK (kind IN ('marker','strong_marker','skip','complete','play','rate')),
  stars           INTEGER CHECK (stars IS NULL OR stars BETWEEN 1 AND 5),         -- rate only; NULL clears
  side_ms         INTEGER NOT NULL CHECK (side_ms >= 0),
  latency_ms      INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms BETWEEN 0 AND 5000),
  client_at       TEXT NOT NULL,
  received_at     TEXT NOT NULL,
  received_by     TEXT NOT NULL,
  comment_id      TEXT,                                                          -- marker / strong_marker target
  rating_track_id TEXT,                                                          -- rate target
  listen_event_id TEXT,                                                          -- skip / complete / play target
  mapped_at       TEXT,
  superseded      INTEGER NOT NULL DEFAULT 0 CHECK (superseded IN (0,1)),        -- a rate event the Owner's newer rating overrode
  CHECK (kind = 'rate' OR stars IS NULL),
  CHECK (mapped_at IS NOT NULL OR (comment_id IS NULL AND rating_track_id IS NULL AND listen_event_id IS NULL AND superseded = 0)),
  CHECK (superseded = 0 OR (comment_id IS NULL AND rating_track_id IS NULL AND listen_event_id IS NULL)),
  CHECK (comment_id IS NULL OR kind IN ('marker','strong_marker')),
  CHECK (rating_track_id IS NULL OR kind = 'rate'),
  CHECK (listen_event_id IS NULL OR kind IN ('skip','complete','play')),
  CHECK ((comment_id IS NOT NULL) + (rating_track_id IS NOT NULL) + (listen_event_id IS NOT NULL) <= 1)
);
CREATE INDEX morning_crate_events_crate ON morning_crate_events(crate_id, received_at, id);
CREATE INDEX morning_crate_events_unmapped ON morning_crate_events(crate_id) WHERE mapped_at IS NULL;
CREATE TRIGGER morning_crate_events_frozen BEFORE UPDATE OF id, crate_id, side_index, chapter, kind, stars, side_ms, latency_ms, client_at,
  received_at, received_by ON morning_crate_events
BEGIN SELECT RAISE(ABORT, 'morning_crate_events: frozen column'); END;
CREATE TRIGGER morning_crate_events_map_once BEFORE UPDATE OF comment_id, rating_track_id, listen_event_id, mapped_at, superseded
  ON morning_crate_events WHEN OLD.mapped_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'morning_crate_events: already mapped'); END;
CREATE TRIGGER morning_crate_events_no_delete BEFORE DELETE ON morning_crate_events
BEGIN SELECT RAISE(ABORT, 'morning_crate_events: no physical delete'); END;
