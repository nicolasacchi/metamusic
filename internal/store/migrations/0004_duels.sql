-- 0004_duels.sql — blind duels and their verdicts (M2b).

CREATE TABLE duels (
  id               TEXT PRIMARY KEY,                        -- UUIDv7 (Deps.NewID)
  slot_a_track_id  TEXT NOT NULL REFERENCES tracks(id),
  slot_b_track_id  TEXT NOT NULL REFERENCES tracks(id),
  swapped          INTEGER NOT NULL CHECK (swapped IN (0,1)), -- 1 ⇔ slot A holds the *requested* b track
  blind            INTEGER NOT NULL CHECK (blind IN (0,1)),
  from_ms          INTEGER NOT NULL CHECK (from_ms >= 0),
  to_ms            INTEGER NOT NULL,
  clip_def         TEXT NOT NULL,                           -- 'mm-clip/1'
  tool_version     TEXT NOT NULL,                           -- clip renderer ToolVersion() at creation ('ffmpeg <token>')
  match            TEXT NOT NULL,                           -- Go-validated: 'off'|'matched'|'limited'|'window_silent'
  target_lufs      REAL,                                    -- NULL unless match IN ('matched','limited')
  gain_a_cdb       INTEGER NOT NULL CHECK (gain_a_cdb BETWEEN -6000 AND 2400),  -- centi-dB applied to slot A (Go: ≤ 0, D-M2b-2)
  gain_b_cdb       INTEGER NOT NULL CHECK (gain_b_cdb BETWEEN -6000 AND 2400),
  window_lufs_a    REAL,                                    -- measured window loudness (NULL when match='off')
  window_lufs_b    REAL,
  window_tp_a      REAL,                                    -- measured window true peak, dBTP
  window_tp_b      REAL,
  created_by       TEXT NOT NULL,                           -- Principal.Key() (M2a C26)
  created_by_kind  TEXT NOT NULL CHECK (created_by_kind IN ('human','device','agent')),
  created_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL,                           -- created_at + 30 days (D-M2b-14)
  CHECK (slot_a_track_id <> slot_b_track_id),
  CHECK (to_ms > from_ms),
  CHECK (blind = 1 OR swapped = 0)
);
CREATE INDEX duels_created ON duels(created_at, id);
CREATE INDEX duels_slot_a  ON duels(slot_a_track_id);
CREATE INDEX duels_slot_b  ON duels(slot_b_track_id);
CREATE TRIGGER duels_immutable BEFORE UPDATE ON duels
BEGIN SELECT RAISE(ABORT, 'duels: immutable'); END;
CREATE TRIGGER duels_no_delete BEFORE DELETE ON duels
BEGIN SELECT RAISE(ABORT, 'duels: no physical delete'); END;

CREATE TABLE duel_verdicts (
  id              TEXT PRIMARY KEY,                         -- UUIDv7 (Deps.NewID)
  duel_id         TEXT NOT NULL REFERENCES duels(id),
  source          TEXT NOT NULL CHECK (source IN ('human','critic')),             -- D-M2b-16
  principal_kind  TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id    TEXT NOT NULL,                            -- Principal.Key() (M2a C26)
  verdict         TEXT NOT NULL CHECK (verdict IN ('a','b','tie','both_bad')),
  winner_track_id TEXT REFERENCES tracks(id),               -- the slot's track for 'a'/'b', else NULL
  note            TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000),  -- trimmed; empty ⇒ NULL (Go)
  latency_ms      INTEGER CHECK (latency_ms IS NULL OR latency_ms BETWEEN 0 AND 86400000),
  switches        INTEGER CHECK (switches IS NULL OR switches BETWEEN 0 AND 100000),
  saw_reveal      INTEGER NOT NULL CHECK (saw_reveal IN (0,1)),
  created_at      TEXT NOT NULL,
  CHECK ((verdict IN ('a','b')) = (winner_track_id IS NOT NULL)),
  CHECK ((source = 'human') = (principal_kind IN ('human','device')))   -- source isolation (D-M2b-3, as D-M2a-12)
);
CREATE UNIQUE INDEX duel_verdicts_one_human  ON duel_verdicts(duel_id) WHERE source = 'human';
CREATE UNIQUE INDEX duel_verdicts_one_critic ON duel_verdicts(duel_id, principal_id) WHERE source = 'critic';
CREATE INDEX duel_verdicts_source ON duel_verdicts(source, created_at);
CREATE INDEX duel_verdicts_duel   ON duel_verdicts(duel_id);
CREATE TRIGGER duel_verdicts_winner BEFORE INSERT ON duel_verdicts
WHEN NEW.verdict IN ('a','b') AND NEW.winner_track_id IS NOT (
  SELECT CASE NEW.verdict WHEN 'a' THEN slot_a_track_id ELSE slot_b_track_id END
  FROM duels WHERE id = NEW.duel_id)
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: winner does not match slot'); END;
CREATE TRIGGER duel_verdicts_immutable BEFORE UPDATE ON duel_verdicts
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: immutable'); END;
CREATE TRIGGER duel_verdicts_no_delete BEFORE DELETE ON duel_verdicts
BEGIN SELECT RAISE(ABORT, 'duel_verdicts: no physical delete'); END;
