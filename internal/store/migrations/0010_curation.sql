-- 0010_curation.sql — L2: Records, slots, changelog, Crates, Tapes, and the frozen Muse ordinal. Creates no L2 row (no seed, no queue).

-- ---- Muse ordinal (D-L2-37): the stable rank behind the neutral Subsonic names ---------------------
ALTER TABLE muses ADD COLUMN ordinal INTEGER CHECK (ordinal IS NULL OR ordinal >= 1);
UPDATE muses SET ordinal = (SELECT COUNT(*) + 1 FROM muses m2
                             WHERE m2.created_at < muses.created_at OR (m2.created_at = muses.created_at AND m2.id < muses.id));
CREATE UNIQUE INDEX muses_ordinal ON muses(ordinal) WHERE ordinal IS NOT NULL;
CREATE TRIGGER muses_ordinal_assign AFTER INSERT ON muses WHEN NEW.ordinal IS NULL
BEGIN UPDATE muses SET ordinal = (SELECT COALESCE(MAX(ordinal), 0) + 1 FROM muses) WHERE id = NEW.id; END;
CREATE TRIGGER muses_ordinal_once BEFORE UPDATE OF ordinal ON muses
WHEN OLD.ordinal IS NOT NULL AND NEW.ordinal IS NOT OLD.ordinal
BEGIN SELECT RAISE(ABORT, 'muses: ordinal is set once'); END;

-- ---- Records -----------------------------------------------------------------------------------
CREATE TABLE records (
  id                 TEXT PRIMARY KEY,                       -- UUIDv7
  title              TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
  subtitle           TEXT NOT NULL DEFAULT '' CHECK (length(subtitle) <= 200),
  liner_note         TEXT NOT NULL DEFAULT '' CHECK (length(liner_note) <= 4000),   -- the operator's own text; generated notes are never stored
  cover_seed         INTEGER NOT NULL CHECK (cover_seed BETWEEN 0 AND 9007199254740991),   -- records.CoverSeed(id, cover_rerolls) (Verify R6)
  cover_rerolls      INTEGER NOT NULL DEFAULT 0 CHECK (cover_rerolls >= 0),
  status             TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','proposed','published')),
  order_json         TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(order_json) AND json_type(order_json) = 'array'),   -- live slot ids in order (D-L2-5)
  proposal_note      TEXT CHECK (proposal_note IS NULL OR length(proposal_note) BETWEEN 1 AND 500),
  proposed_by        TEXT,                                   -- Principal.Key()
  proposed_at        TEXT,
  published_by       TEXT,                                   -- Principal.Key() of a KindHuman
  published_at       TEXT,
  first_published_at TEXT,                                   -- set once, never cleared (catalog ordering, D-L2-15)
  revision           INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),   -- bumped by every human/agent edit; never by the Syncer (incl. its duplicate-removal order rewrite, C122.2.1)
  created_by         TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  updated_by         TEXT NOT NULL,
  updated_at         TEXT NOT NULL,
  hidden_at          TEXT,
  hidden_by          TEXT,
  CHECK ((proposed_at IS NULL) = (proposed_by IS NULL)),
  CHECK ((published_at IS NULL) = (published_by IS NULL)),
  CHECK ((published_at IS NOT NULL) = (status = 'published')),
  CHECK (status <> 'draft' OR proposed_at IS NULL),
  CHECK (proposal_note IS NULL OR proposed_at IS NOT NULL),
  CHECK (published_at IS NULL OR first_published_at IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX records_updated ON records(updated_at, id);
CREATE INDEX records_published ON records(first_published_at, id) WHERE status = 'published' AND hidden_at IS NULL;
CREATE TRIGGER records_no_delete BEFORE DELETE ON records
BEGIN SELECT RAISE(ABORT, 'records: no physical delete'); END;
CREATE TRIGGER records_frozen BEFORE UPDATE OF id, created_by, created_at ON records
BEGIN SELECT RAISE(ABORT, 'records: frozen column'); END;
CREATE TRIGGER records_first_published_once BEFORE UPDATE OF first_published_at ON records
WHEN OLD.first_published_at IS NOT NULL AND NEW.first_published_at IS NOT OLD.first_published_at
BEGIN SELECT RAISE(ABORT, 'records: first_published_at is set once'); END;
CREATE TRIGGER records_status_path BEFORE UPDATE OF status ON records
WHEN NOT (NEW.status = OLD.status
       OR (OLD.status = 'draft'    AND NEW.status IN ('proposed','published'))
       OR (OLD.status = 'proposed' AND NEW.status IN ('published','draft'))
       OR (OLD.status = 'published' AND NEW.status = 'draft'))
BEGIN SELECT RAISE(ABORT, 'records: illegal status transition'); END;

CREATE TABLE record_slots (
  id             TEXT PRIMARY KEY,                           -- UUIDv7; stable across reorders (the changelog's subject)
  record_id      TEXT NOT NULL REFERENCES records(id),
  song_id        TEXT NOT NULL REFERENCES songs(id),         -- follows retired_into only (trigger)
  side           TEXT CHECK (side IS NULL OR side IN ('A','B','C','D')),
  pinned_take_id TEXT REFERENCES tracks(id),
  pinned_by      TEXT,
  pinned_at      TEXT,
  synced_seq     INTEGER NOT NULL DEFAULT 0 CHECK (synced_seq >= 0),    -- highest pick_events.seq of song_id accounted for (D-L2-7)
  added_by       TEXT NOT NULL,
  added_at       TEXT NOT NULL,
  removed_by     TEXT,
  removed_at     TEXT,
  CHECK ((pinned_take_id IS NULL) = (pinned_by IS NULL)),
  CHECK ((pinned_take_id IS NULL) = (pinned_at IS NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
);
CREATE UNIQUE INDEX record_slots_live_song ON record_slots(record_id, song_id) WHERE removed_at IS NULL;
CREATE INDEX record_slots_song   ON record_slots(song_id) WHERE removed_at IS NULL;
CREATE INDEX record_slots_record ON record_slots(record_id);
CREATE TRIGGER record_slots_no_delete BEFORE DELETE ON record_slots
BEGIN SELECT RAISE(ABORT, 'record_slots: no physical delete'); END;
CREATE TRIGGER record_slots_frozen BEFORE UPDATE OF id, record_id, added_by, added_at ON record_slots
BEGIN SELECT RAISE(ABORT, 'record_slots: frozen column'); END;
CREATE TRIGGER record_slots_removed_final BEFORE UPDATE ON record_slots WHEN OLD.removed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'record_slots: removed slots are immutable'); END;
CREATE TRIGGER record_slots_song_follow BEFORE UPDATE OF song_id ON record_slots
WHEN NEW.song_id IS NOT OLD.song_id
 AND (SELECT retired_into FROM songs WHERE id = OLD.song_id) IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: song_id follows retired_into only'); END;
CREATE TRIGGER record_slots_synced_monotonic BEFORE UPDATE OF synced_seq ON record_slots
WHEN NEW.synced_seq < OLD.synced_seq AND NEW.song_id IS OLD.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: synced_seq never decreases'); END;
CREATE TRIGGER record_slots_live_song_ins BEFORE INSERT ON record_slots
WHEN (SELECT retired_at FROM songs WHERE id = NEW.song_id) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'record_slots: song is retired'); END;
CREATE TRIGGER record_slots_pin_in_song BEFORE INSERT ON record_slots
WHEN NEW.pinned_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: pinned take is not in the song'); END;
CREATE TRIGGER record_slots_pin_in_song_upd BEFORE UPDATE OF pinned_take_id, song_id ON record_slots
WHEN NEW.pinned_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'record_slots: pinned take is not in the song'); END;

CREATE TRIGGER records_order_consistent BEFORE UPDATE OF order_json ON records     -- defined after record_slots (it reads it)
WHEN json_array_length(NEW.order_json) <> (SELECT COUNT(*) FROM record_slots s WHERE s.record_id = NEW.id AND s.removed_at IS NULL)
  OR (SELECT COUNT(DISTINCT j.value) FROM json_each(NEW.order_json) j) <> json_array_length(NEW.order_json)
  OR EXISTS (SELECT 1 FROM json_each(NEW.order_json) j
              WHERE NOT EXISTS (SELECT 1 FROM record_slots s
                                 WHERE s.id = j.value AND s.record_id = NEW.id AND s.removed_at IS NULL))
BEGIN SELECT RAISE(ABORT, 'records: order must list exactly the live slots'); END;

CREATE TABLE record_changes (                                  -- append-only changelog (concept §2)
  id            TEXT PRIMARY KEY,                              -- UUIDv7
  record_id     TEXT NOT NULL REFERENCES records(id),
  seq           INTEGER NOT NULL CHECK (seq >= 1),             -- per-record chain 1..n
  at            TEXT NOT NULL,
  kind          TEXT NOT NULL CHECK (kind IN (
                  'created','renamed','retexted','cover_rerolled','hidden','unhidden',
                  'slot_added','slot_removed','slot_moved','slot_side','slot_pinned','slot_unpinned',
                  'slot_upgraded','slot_filled','slot_emptied','slot_song_followed',
                  'proposed','proposal_dismissed','published','unpublished')),
  slot_id       TEXT REFERENCES record_slots(id),
  position      INTEGER CHECK (position IS NULL OR position >= 1),          -- the slot's position when the row was written
  from_take_id  TEXT REFERENCES tracks(id),
  to_take_id    TEXT REFERENCES tracks(id),
  reason        TEXT CHECK (reason IS NULL OR reason IN
                  ('initial','pin','unpin','pin_ineligible','pin_restored','ladder','stars','newest','emptied','retired','duplicate')),
  pick_event_id TEXT REFERENCES pick_events(id),               -- the L1 event that caused a Syncer row
  evidence_json TEXT CHECK (evidence_json IS NULL OR json_valid(evidence_json)),   -- the event's mm.pick/1, copied
  detail_json   TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),       -- ids/numbers only, never titles or Muse text
  actor         TEXT NOT NULL,                                 -- Principal.Key(), or 'system:records-sync'
  actor_kind    TEXT NOT NULL CHECK (actor_kind IN ('human','agent','system')),
  actor_name    TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 120),
  UNIQUE (record_id, seq),
  CHECK ((kind LIKE 'slot\_%' ESCAPE '\') = (slot_id IS NOT NULL)),
  CHECK (kind NOT IN ('slot_upgraded','slot_filled','slot_emptied') OR pick_event_id IS NOT NULL),
  CHECK (kind <> 'slot_upgraded' OR (from_take_id IS NOT NULL AND to_take_id IS NOT NULL AND from_take_id <> to_take_id)),
  CHECK (kind <> 'slot_filled'   OR (from_take_id IS NULL AND to_take_id IS NOT NULL)),
  CHECK (kind <> 'slot_emptied'  OR (from_take_id IS NOT NULL AND to_take_id IS NULL))
);
CREATE UNIQUE INDEX record_changes_event ON record_changes(slot_id, pick_event_id) WHERE pick_event_id IS NOT NULL;   -- Syncer idempotence
CREATE INDEX record_changes_record ON record_changes(record_id, seq);
CREATE INDEX record_changes_slot   ON record_changes(slot_id, seq) WHERE slot_id IS NOT NULL;
CREATE TRIGGER record_changes_immutable BEFORE UPDATE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record_changes: append-only'); END;
CREATE TRIGGER record_changes_no_delete BEFORE DELETE ON record_changes
BEGIN SELECT RAISE(ABORT, 'record_changes: append-only'); END;
CREATE TRIGGER record_changes_chain BEFORE INSERT ON record_changes
WHEN NEW.seq <> COALESCE((SELECT MAX(seq) FROM record_changes WHERE record_id = NEW.record_id), 0) + 1
BEGIN SELECT RAISE(ABORT, 'record_changes: broken chain'); END;
CREATE TRIGGER record_changes_slot_of_record BEFORE INSERT ON record_changes
WHEN NEW.slot_id IS NOT NULL
 AND (SELECT record_id FROM record_slots WHERE id = NEW.slot_id) IS NOT NEW.record_id
BEGIN SELECT RAISE(ABORT, 'record_changes: slot is not in the record'); END;

-- ---- Crates ------------------------------------------------------------------------------------
CREATE TABLE crates (
  id          TEXT PRIMARY KEY,                              -- UUIDv7
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  kind        TEXT NOT NULL CHECK (kind IN ('manual','smart')),
  subject     TEXT NOT NULL DEFAULT 'song' CHECK (subject IN ('song','take')),
  filter_json TEXT CHECK (filter_json IS NULL OR (json_valid(filter_json) AND json_type(filter_json) = 'object' AND length(filter_json) <= 4096)),
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),     -- bumped by name/description/filter/hidden edits, not by item edits
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK ((kind = 'smart') = (filter_json IS NOT NULL)),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX crates_updated ON crates(updated_at, id);
CREATE TRIGGER crates_no_delete BEFORE DELETE ON crates
BEGIN SELECT RAISE(ABORT, 'crates: no physical delete'); END;
CREATE TRIGGER crates_frozen BEFORE UPDATE OF id, kind, subject, created_by, created_at ON crates
BEGIN SELECT RAISE(ABORT, 'crates: frozen column'); END;

CREATE TABLE crate_items (                                     -- manual crates only; soft removal; re-adding creates a new row
  id         TEXT PRIMARY KEY,                                 -- UUIDv7
  crate_id   TEXT NOT NULL REFERENCES crates(id),
  song_id    TEXT REFERENCES songs(id),
  track_id   TEXT REFERENCES tracks(id),
  added_by   TEXT NOT NULL,
  added_at   TEXT NOT NULL,
  removed_by TEXT,
  removed_at TEXT,
  CHECK ((song_id IS NULL) <> (track_id IS NULL)),
  CHECK ((removed_at IS NULL) = (removed_by IS NULL))
);
CREATE UNIQUE INDEX crate_items_live_song  ON crate_items(crate_id, song_id)  WHERE song_id  IS NOT NULL AND removed_at IS NULL;
CREATE UNIQUE INDEX crate_items_live_track ON crate_items(crate_id, track_id) WHERE track_id IS NOT NULL AND removed_at IS NULL;
CREATE INDEX crate_items_song  ON crate_items(song_id)  WHERE song_id  IS NOT NULL AND removed_at IS NULL;
CREATE INDEX crate_items_track ON crate_items(track_id) WHERE track_id IS NOT NULL AND removed_at IS NULL;
CREATE TRIGGER crate_items_no_delete BEFORE DELETE ON crate_items
BEGIN SELECT RAISE(ABORT, 'crate_items: no physical delete'); END;
CREATE TRIGGER crate_items_frozen BEFORE UPDATE OF id, crate_id, song_id, track_id, added_by, added_at ON crate_items
BEGIN SELECT RAISE(ABORT, 'crate_items: frozen column'); END;
CREATE TRIGGER crate_items_removed_final BEFORE UPDATE ON crate_items WHEN OLD.removed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'crate_items: removed items are immutable'); END;
CREATE TRIGGER crate_items_manual BEFORE INSERT ON crate_items
WHEN (SELECT kind FROM crates WHERE id = NEW.crate_id) <> 'manual'
BEGIN SELECT RAISE(ABORT, 'crate_items: only manual crates hold items'); END;
CREATE TRIGGER crate_items_subject BEFORE INSERT ON crate_items
WHEN (NEW.song_id  IS NOT NULL AND (SELECT subject FROM crates WHERE id = NEW.crate_id) <> 'song')
  OR (NEW.track_id IS NOT NULL AND (SELECT subject FROM crates WHERE id = NEW.crate_id) <> 'take')
BEGIN SELECT RAISE(ABORT, 'crate_items: item does not match the crate subject'); END;

-- ---- Tapes -------------------------------------------------------------------------------------
CREATE TABLE tapes (
  id              TEXT PRIMARY KEY,                           -- UUIDv7
  record_id       TEXT NOT NULL REFERENCES records(id),
  record_revision INTEGER NOT NULL CHECK (record_revision >= 1),
  key             TEXT NOT NULL CHECK (length(key) = 64),     -- input address (C128.5)
  state           TEXT NOT NULL CHECK (state IN ('queued','running','ready','failed','expired')),
  params_json     TEXT NOT NULL CHECK (json_valid(params_json)),     -- {target_lufs|null, include_liner, muse_names}
  plan_json       TEXT NOT NULL CHECK (json_valid(plan_json)),       -- items, gains, flags; chapters added when ready (C128.4)
  contains_lossy  INTEGER NOT NULL CHECK (contains_lossy IN (0,1)),
  lossy_sources   INTEGER NOT NULL CHECK (lossy_sources >= 0),
  total_samples   INTEGER CHECK (total_samples IS NULL OR total_samples > 0),
  bytes           INTEGER CHECK (bytes IS NULL OR bytes > 0),
  sha256          TEXT,
  path            TEXT,                                       -- derived/tapes/<aa>/<sha>.flac (DataDir-relative)
  filename        TEXT CHECK (filename IS NULL OR length(filename) BETWEEN 1 AND 120),
  progress_json   TEXT CHECK (progress_json IS NULL OR json_valid(progress_json)),
  attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),     -- recovery re-queues (C128.5); NOT in progress_json, which the runner overwrites about once a second
  error_code      TEXT,
  error_message   TEXT CHECK (error_message IS NULL OR length(error_message) <= 500),
  owner           TEXT,                                       -- runner id while running
  lease_expires_at TEXT,
  principal_id    TEXT NOT NULL,                              -- Principal.Key()
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  finished_at     TEXT,
  CHECK ((state IN ('ready','expired')) = (sha256 IS NOT NULL)),
  CHECK ((sha256 IS NULL) = (bytes IS NULL) AND (sha256 IS NULL) = (path IS NULL)
     AND (sha256 IS NULL) = (filename IS NULL) AND (sha256 IS NULL) = (total_samples IS NULL)),
  CHECK ((state = 'failed') = (error_code IS NOT NULL)),
  CHECK ((state = 'running') = (lease_expires_at IS NOT NULL AND owner IS NOT NULL)),
  CHECK (finished_at IS NULL OR state IN ('ready','failed','expired')),
  CHECK (contains_lossy = (lossy_sources > 0))
);
CREATE UNIQUE INDEX tapes_live_key ON tapes(key) WHERE state IN ('queued','running','ready');
CREATE INDEX tapes_record ON tapes(record_id, created_at);
CREATE INDEX tapes_state  ON tapes(state, created_at);
CREATE TRIGGER tapes_no_delete BEFORE DELETE ON tapes
BEGIN SELECT RAISE(ABORT, 'tapes: no physical delete'); END;
CREATE TRIGGER tapes_frozen BEFORE UPDATE OF id, record_id, record_revision, key, params_json, principal_id, created_at ON tapes
BEGIN SELECT RAISE(ABORT, 'tapes: frozen column'); END;
CREATE TRIGGER tapes_state_path BEFORE UPDATE OF state ON tapes
WHEN NOT (NEW.state = OLD.state
       OR (OLD.state = 'queued'  AND NEW.state IN ('running','failed'))
       OR (OLD.state = 'running' AND NEW.state IN ('ready','failed','queued'))
       OR (OLD.state = 'ready'   AND NEW.state = 'expired'))
BEGIN SELECT RAISE(ABORT, 'tapes: illegal state transition'); END;
CREATE TRIGGER tapes_ready_frozen BEFORE UPDATE OF sha256, bytes, path, filename, total_samples, plan_json, contains_lossy, lossy_sources ON tapes
WHEN OLD.state IN ('ready','expired')
BEGIN SELECT RAISE(ABORT, 'tapes: a finished tape is immutable'); END;
