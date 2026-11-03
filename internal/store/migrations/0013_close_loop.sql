-- 0013_close_loop.sql: M5 close the loop. Song PRs (patches), callbacks and their verdicts, voice notes, section-locked duels.
-- Additive only: new tables, ADD COLUMN on jobs/comments/duels (existing rows keep NULL), no rebuild.
-- Enumerations that grow (operators, patch ops, verdicts, map bases, voice containers) are Go-validated.
CREATE TABLE patches (                                   -- D-M5-1; immutable Song PR
  id               TEXT PRIMARY KEY,                     -- UUIDv7
  base_track_id    TEXT NOT NULL REFERENCES tracks(id),
  base_recipe_hash TEXT NOT NULL,
  operator         TEXT NOT NULL,                        -- Go-validated: lyric_edit|style_edit|multi_edit
  ops_json         TEXT NOT NULL CHECK (json_valid(ops_json) AND json_type(ops_json) = 'array'),
  compiled_json    TEXT NOT NULL CHECK (json_valid(compiled_json)),
  scope_json       TEXT NOT NULL CHECK (json_valid(scope_json)),
  diff_json        TEXT NOT NULL CHECK (json_valid(diff_json)),
  title            TEXT CHECK (title IS NULL OR length(title) BETWEEN 1 AND 120),
  rationale        TEXT NOT NULL CHECK (length(rationale) BETWEEN 1 AND 2000),
  principal_kind   TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id     TEXT NOT NULL,
  principal_name   TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX patches_base ON patches(base_track_id, created_at);
CREATE TRIGGER patches_immutable BEFORE UPDATE ON patches BEGIN SELECT RAISE(ABORT, 'patches: immutable'); END;
CREATE TRIGGER patches_no_delete BEFORE DELETE ON patches BEGIN SELECT RAISE(ABORT, 'patches: no physical delete'); END;

CREATE TABLE patch_comments (
  patch_id   TEXT NOT NULL REFERENCES patches(id),
  comment_id TEXT NOT NULL REFERENCES comments(id),
  PRIMARY KEY (patch_id, comment_id)
) WITHOUT ROWID;
CREATE INDEX patch_comments_comment ON patch_comments(comment_id);
CREATE TRIGGER patch_comments_immutable BEFORE UPDATE ON patch_comments BEGIN SELECT RAISE(ABORT, 'patch_comments: immutable'); END;
CREATE TRIGGER patch_comments_no_delete BEFORE DELETE ON patch_comments BEGIN SELECT RAISE(ABORT, 'patch_comments: no physical delete'); END;

ALTER TABLE jobs ADD COLUMN patch_id TEXT REFERENCES patches(id);
CREATE INDEX jobs_patch ON jobs(patch_id) WHERE patch_id IS NOT NULL;
-- jobs_frozen (0007) lists columns explicitly; this trigger covers the new column.
CREATE TRIGGER jobs_patch_frozen BEFORE UPDATE OF patch_id ON jobs WHEN OLD.patch_id IS NOT NEW.patch_id
BEGIN SELECT RAISE(ABORT, 'jobs: frozen column'); END;

CREATE TABLE voice_notes (                               -- D-M5-23; immutable
  id             TEXT PRIMARY KEY,
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  sha256         TEXT NOT NULL,
  bytes          INTEGER NOT NULL CHECK (bytes > 0 AND bytes <= 4194304),
  container      TEXT NOT NULL,
  codec          TEXT NOT NULL,
  path           TEXT NOT NULL,
  mp3_sha256     TEXT NOT NULL,
  mp3_path       TEXT NOT NULL,
  mp3_bytes      INTEGER NOT NULL CHECK (mp3_bytes > 0),
  duration_s     REAL NOT NULL CHECK (duration_s > 0 AND duration_s <= 120.5),
  at_second      REAL CHECK (at_second IS NULL OR at_second >= 0),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device')),
  principal_id   TEXT NOT NULL,
  created_at     TEXT NOT NULL
);
CREATE INDEX voice_notes_track ON voice_notes(track_id, created_at);
CREATE INDEX voice_notes_sha ON voice_notes(sha256);
CREATE TRIGGER voice_notes_immutable BEFORE UPDATE ON voice_notes BEGIN SELECT RAISE(ABORT, 'voice_notes: immutable'); END;
CREATE TRIGGER voice_notes_no_delete BEFORE DELETE ON voice_notes BEGIN SELECT RAISE(ABORT, 'voice_notes: no physical delete'); END;

CREATE TABLE callbacks (                                 -- D-M5-6; immutable; the spot is computed on read (D-M5-7)
  id              TEXT PRIMARY KEY,
  child_track_id  TEXT NOT NULL REFERENCES tracks(id),
  comment_id      TEXT NOT NULL REFERENCES comments(id),
  source_track_id TEXT NOT NULL REFERENCES tracks(id),
  kind            TEXT NOT NULL CHECK (kind IN ('fix','keep')),
  patch_id        TEXT NOT NULL REFERENCES patches(id),
  job_id          TEXT NOT NULL REFERENCES jobs(id),
  created_at      TEXT NOT NULL,
  UNIQUE (child_track_id, comment_id)
);
CREATE INDEX callbacks_comment ON callbacks(comment_id);
CREATE INDEX callbacks_child ON callbacks(child_track_id, created_at);
CREATE TRIGGER callbacks_immutable BEFORE UPDATE ON callbacks BEGIN SELECT RAISE(ABORT, 'callbacks: immutable'); END;
CREATE TRIGGER callbacks_no_delete BEFORE DELETE ON callbacks BEGIN SELECT RAISE(ABORT, 'callbacks: no physical delete'); END;

CREATE TABLE callback_verdicts (                         -- D-M5-5; append-only
  id                    TEXT PRIMARY KEY,
  callback_id           TEXT NOT NULL REFERENCES callbacks(id),
  source                TEXT NOT NULL CHECK (source IN ('human','critic')),
  principal_kind        TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id          TEXT NOT NULL,
  verdict               TEXT NOT NULL,                   -- Go-validated per kind: fixed|not_yet|worse ; kept|lost
  note                  TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000),
  heard_at_s            REAL CHECK (heard_at_s IS NULL OR heard_at_s >= 0),
  map_basis             TEXT,                            -- Go-validated: section|proportional
  state_before          TEXT,
  state_after           TEXT,
  regression_comment_id TEXT REFERENCES comments(id),
  created_at            TEXT NOT NULL,
  CHECK ((source = 'human') = (principal_kind IN ('human','device'))),
  CHECK ((state_before IS NULL) = (state_after IS NULL)),
  CHECK (source = 'human' OR (state_before IS NULL AND regression_comment_id IS NULL))
);
CREATE UNIQUE INDEX callback_verdicts_one_human ON callback_verdicts(callback_id) WHERE source = 'human';
CREATE UNIQUE INDEX callback_verdicts_one_critic ON callback_verdicts(callback_id, principal_id) WHERE source = 'critic';
CREATE TRIGGER callback_verdicts_immutable BEFORE UPDATE ON callback_verdicts BEGIN SELECT RAISE(ABORT, 'callback_verdicts: immutable'); END;
CREATE TRIGGER callback_verdicts_no_delete BEFORE DELETE ON callback_verdicts BEGIN SELECT RAISE(ABORT, 'callback_verdicts: no physical delete'); END;

ALTER TABLE comments ADD COLUMN voice_note_id TEXT REFERENCES voice_notes(id);
ALTER TABLE comments ADD COLUMN origin_callback_id TEXT REFERENCES callbacks(id);
CREATE TRIGGER comments_m5_frozen BEFORE UPDATE OF voice_note_id, origin_callback_id ON comments
BEGIN SELECT RAISE(ABORT, 'comments: frozen column'); END;

ALTER TABLE duels ADD COLUMN section_label TEXT;
ALTER TABLE duels ADD COLUMN section_source TEXT CHECK (section_source IS NULL OR section_source IN ('plan','measured','manual'));
ALTER TABLE duels ADD COLUMN b_from_ms INTEGER CHECK (b_from_ms IS NULL OR b_from_ms >= 0);
ALTER TABLE duels ADD COLUMN b_to_ms INTEGER;
CREATE TRIGGER duels_section_window BEFORE INSERT ON duels
WHEN (NEW.b_from_ms IS NULL) <> (NEW.b_to_ms IS NULL) OR (NEW.b_from_ms IS NULL) <> (NEW.section_label IS NULL)
  OR (NEW.section_label IS NULL) <> (NEW.section_source IS NULL)
  OR (NEW.b_from_ms IS NOT NULL AND NEW.b_to_ms - NEW.b_from_ms <> NEW.to_ms - NEW.from_ms)
BEGIN SELECT RAISE(ABORT, 'duels: section window mismatch'); END;
