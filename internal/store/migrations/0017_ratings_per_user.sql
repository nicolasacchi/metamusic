-- 0017_ratings_per_user.sql: U1-R1 (C306). One current rating row per (track, source, rater) instead of per (track, source),
-- so several people can rate the same track. SQLite cannot alter a primary key, so the table is rebuilt: the same columns plus
-- rater, the same CHECKs plus the pairing rule, then every trigger and index of the old table is recreated.
--
-- rater is 'owner' for the Owner (and the Owner's devices and clients), the username for any other human and 'critic' for the
-- agent row. Every existing human rating, device-origin ones included, becomes 'owner'; the agent row becomes 'critic'. The
-- migration reads no configuration (the Owner key is the constant 'owner'). Up-only: once another user has rated, the old
-- shape cannot hold the data; take a backup first. No foreign key points at ratings, so the rebuild needs no
-- foreign_keys=off. The copy runs before the triggers exist, so it enqueues nothing for the provenance reconciler.

CREATE TABLE ratings_new (
  track_id       TEXT NOT NULL REFERENCES tracks(id),
  source         TEXT NOT NULL CHECK (source IN ('human','agent')),
  rater          TEXT NOT NULL CHECK (length(rater) > 0),         -- 'owner' | username | 'critic'
  stars          INTEGER CHECK (stars IS NULL OR stars BETWEEN 1 AND 5),      -- NULL = cleared
  verdict        TEXT CHECK (verdict IS NULL OR length(verdict) BETWEEN 1 AND 280),
  principal_kind TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent')),
  principal_id   TEXT NOT NULL,                        -- Principal.Key()
  principal_name TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  PRIMARY KEY (track_id, source, rater),
  CHECK ((source = 'human') = (principal_kind IN ('human','device'))),   -- rating isolation, at the schema level
  CHECK ((source = 'agent') = (rater = 'critic'))                        -- a human is never the critic, the agent never a user
) WITHOUT ROWID;
INSERT INTO ratings_new (track_id, source, rater, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at)
  SELECT track_id, source, CASE WHEN source = 'human' THEN 'owner' ELSE 'critic' END,
         stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at FROM ratings;
DROP TABLE ratings;
ALTER TABLE ratings_new RENAME TO ratings;

CREATE INDEX ratings_source_stars ON ratings(source, stars);
CREATE INDEX ratings_track_rater ON ratings(track_id, rater);
CREATE TRIGGER ratings_no_delete BEFORE DELETE ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: no physical delete'); END;
CREATE TRIGGER ratings_frozen BEFORE UPDATE OF track_id, source, rater, created_at ON ratings
BEGIN SELECT RAISE(ABORT, 'ratings: frozen column'); END;

-- Provenance: only the Owner's rating steers curation (D-U1-9), so only it marks the track dirty; a rating storm of other
-- users never churns the queue. pd_verdicts_ai (duels, Owner-only) is unchanged.
CREATE TRIGGER pd_ratings_ai AFTER INSERT ON ratings WHEN NEW.source = 'human' AND NEW.rater = 'owner' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_ratings_au AFTER UPDATE OF stars ON ratings WHEN NEW.source = 'human' AND NEW.rater = 'owner' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

-- Views: the one place the Owner's curation signal and the critic are named. Static (the Owner key is a constant).
CREATE VIEW owner_ratings AS
  SELECT track_id, source, rater, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at
  FROM ratings WHERE source = 'human' AND rater = 'owner';
CREATE VIEW critic_ratings AS
  SELECT track_id, source, rater, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at
  FROM ratings WHERE source = 'agent';
