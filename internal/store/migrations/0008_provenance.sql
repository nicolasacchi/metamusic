-- 0008_provenance.sql — L1: Muses, Songs, Picks, Sessions, Sounds, and the reconcile queue.

-- ---- Muses -------------------------------------------------------------------------------------
CREATE TABLE muses (
  id          TEXT PRIMARY KEY,                          -- UUIDv7
  kind        TEXT NOT NULL CHECK (kind IN ('reference','brief')),
  title       TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  title_fold  TEXT NOT NULL CHECK (length(title_fold) BETWEEN 1 AND 400),     -- textfold.Norm(title) (lower-case, whitespace-collapsed); keyset order
  artist      TEXT CHECK (artist IS NULL OR length(artist) BETWEEN 1 AND 200),
  brief       TEXT CHECK (brief IS NULL OR length(brief) BETWEEN 1 AND 2000),
  key         TEXT NOT NULL UNIQUE CHECK (length(key) BETWEEN 1 AND 820),     -- reference: norm(title) || x'1f' || norm(artist or ''); brief: 'brief:' || id
  source      TEXT NOT NULL CHECK (source IN ('recipe_refs','human','agent')),
  created_by  TEXT NOT NULL,                             -- Principal.Key() or 'system:provenance'
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK ((kind = 'brief') = (brief IS NOT NULL)),
  CHECK (kind = 'reference' OR artist IS NULL),
  CHECK (source <> 'recipe_refs' OR kind = 'reference'),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX muses_order ON muses(title_fold, id);
CREATE TRIGGER muses_no_delete BEFORE DELETE ON muses
BEGIN SELECT RAISE(ABORT, 'muses: no physical delete'); END;
CREATE TRIGGER muses_frozen BEFORE UPDATE OF id, kind, source, created_by, created_at ON muses
BEGIN SELECT RAISE(ABORT, 'muses: frozen column'); END;
CREATE TRIGGER muses_brief_key_frozen BEFORE UPDATE OF key ON muses WHEN OLD.kind = 'brief'
BEGIN SELECT RAISE(ABORT, 'muses: frozen column'); END;

CREATE VIRTUAL TABLE muses_fts USING fts5(
  muse_id UNINDEXED, title, artist, brief, tokenize = 'unicode61 remove_diacritics 2');
CREATE TRIGGER muses_fts_ai AFTER INSERT ON muses BEGIN
  INSERT INTO muses_fts(muse_id, title, artist, brief)
  VALUES (NEW.id, NEW.title, COALESCE(NEW.artist, ''), COALESCE(NEW.brief, ''));
END;
CREATE TRIGGER muses_fts_au AFTER UPDATE OF title, artist, brief ON muses BEGIN
  UPDATE muses_fts SET title = NEW.title, artist = COALESCE(NEW.artist, ''), brief = COALESCE(NEW.brief, '')
   WHERE muse_id = NEW.id;
END;

-- ---- Songs -------------------------------------------------------------------------------------
CREATE TABLE songs (
  id            TEXT PRIMARY KEY,                        -- UUIDv7
  root_track_id TEXT UNIQUE REFERENCES tracks(id),       -- the PRIMARY (first) root; NULL while an unrendered draft; always has a song_roots row (Verify I1)
  draft_id      TEXT REFERENCES recipe_drafts(id),
  muse_id       TEXT REFERENCES muses(id),
  muse_source   TEXT CHECK (muse_source IS NULL OR muse_source IN ('reference','manual')),   -- NULL = never set (the Reconciler may fill); 'manual' also marks an operator-cleared Muse (D-L1-37)
  angle         TEXT CHECK (angle IS NULL OR (length(angle) BETWEEN 1 AND 32
                  AND angle GLOB '[a-z]*' AND angle NOT GLOB '*[^a-z0-9-]*')),
  angle_source  TEXT CHECK (angle_source IS NULL OR angle_source IN ('reference','request_id','manual','cleared')),   -- NULL = never set; 'cleared' = angle NULL by an operator
  title         TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  origin        TEXT NOT NULL CHECK (origin IN ('draft','root')),    -- how the Song was first created
  revision      INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),    -- bumped by update_song / assignments only
  created_by    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_by    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  hidden_at     TEXT,
  hidden_by     TEXT,
  retired_at    TEXT,
  retired_into  TEXT REFERENCES songs(id),
  CHECK ((angle IS NULL) = (angle_source IS NULL OR angle_source = 'cleared')),
  CHECK (muse_id IS NULL OR muse_source IS NOT NULL),
  CHECK (root_track_id IS NOT NULL OR draft_id IS NOT NULL),
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL)),
  CHECK ((retired_at IS NULL) = (retired_into IS NULL)),
  CHECK (retired_at IS NULL OR root_track_id IS NULL),
  CHECK (retired_into IS NULL OR retired_into <> id)
);
CREATE UNIQUE INDEX songs_draft_live ON songs(draft_id) WHERE draft_id IS NOT NULL AND retired_at IS NULL;
CREATE INDEX songs_muse   ON songs(muse_id) WHERE retired_at IS NULL;
CREATE INDEX songs_angle  ON songs(angle)   WHERE retired_at IS NULL;
CREATE INDEX songs_live   ON songs(id)      WHERE retired_at IS NULL AND hidden_at IS NULL;
CREATE TRIGGER songs_no_delete BEFORE DELETE ON songs
BEGIN SELECT RAISE(ABORT, 'songs: no physical delete'); END;
CREATE TRIGGER songs_frozen BEFORE UPDATE OF id, origin, created_by, created_at ON songs
BEGIN SELECT RAISE(ABORT, 'songs: frozen column'); END;
CREATE TRIGGER songs_root_once BEFORE UPDATE OF root_track_id ON songs
WHEN OLD.root_track_id IS NOT NULL AND NEW.root_track_id IS NOT OLD.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: root_track_id is set once'); END;
CREATE TRIGGER songs_draft_once BEFORE UPDATE OF draft_id ON songs
WHEN OLD.draft_id IS NOT NULL AND NEW.draft_id IS NOT OLD.draft_id
BEGIN SELECT RAISE(ABORT, 'songs: draft_id is set once'); END;
CREATE TRIGGER songs_retired_final BEFORE UPDATE ON songs WHEN OLD.retired_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'songs: retired songs are immutable'); END;
CREATE TRIGGER songs_root_is_root BEFORE INSERT ON songs
WHEN NEW.root_track_id IS NOT NULL
 AND (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: not a lineage root'); END;
CREATE TRIGGER songs_root_is_root_upd BEFORE UPDATE OF root_track_id ON songs
WHEN NEW.root_track_id IS NOT NULL
 AND (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'songs: not a lineage root'); END;
CREATE TRIGGER songs_retire_target BEFORE UPDATE OF retired_into ON songs
WHEN NEW.retired_into IS NOT NULL
 AND (SELECT retired_at FROM songs WHERE id = NEW.retired_into) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'songs: retire target is retired'); END;
CREATE TRIGGER songs_retire_takeless BEFORE UPDATE OF retired_at ON songs
WHEN NEW.retired_at IS NOT NULL
 AND (OLD.root_track_id IS NOT NULL OR EXISTS (SELECT 1 FROM song_roots WHERE song_id = OLD.id))
BEGIN SELECT RAISE(ABORT, 'songs: a song with takes cannot retire'); END;

CREATE TABLE song_roots (                                -- every lineage root belongs to exactly one Song (D-L1-3)
  root_track_id TEXT PRIMARY KEY REFERENCES tracks(id),
  song_id       TEXT NOT NULL REFERENCES songs(id),
  via           TEXT NOT NULL CHECK (via IN ('own','draft','job_item')),  -- own: created/attached its Song; draft: rendered from the Song's draft; job_item: sibling take of one compose_new item
  attached_by   TEXT NOT NULL,
  attached_at   TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX song_roots_song ON song_roots(song_id);
CREATE TRIGGER song_roots_no_delete BEFORE DELETE ON song_roots
BEGIN SELECT RAISE(ABORT, 'song_roots: no physical delete'); END;
CREATE TRIGGER song_roots_immutable BEFORE UPDATE ON song_roots
BEGIN SELECT RAISE(ABORT, 'song_roots: append-only'); END;
CREATE TRIGGER song_roots_is_root BEFORE INSERT ON song_roots
WHEN (SELECT root_track_id FROM tracks WHERE id = NEW.root_track_id) IS NOT NEW.root_track_id
BEGIN SELECT RAISE(ABORT, 'song_roots: not a lineage root'); END;
CREATE TRIGGER song_roots_live_song BEFORE INSERT ON song_roots
WHEN (SELECT retired_at FROM songs WHERE id = NEW.song_id) IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'song_roots: song is retired'); END;

CREATE VIEW take_songs AS                                -- Take mapping (D-L1-3): derived from song_roots, never stored per take
  SELECT t.id AS track_id, r.song_id AS song_id
    FROM tracks t JOIN song_roots r ON r.root_track_id = t.root_track_id;

-- ---- Picks -------------------------------------------------------------------------------------
CREATE TABLE pick_events (                               -- append-only; the Record changelog source (L2)
  id           TEXT PRIMARY KEY,                         -- UUIDv7 (identity only; the feed cursor is the table's rowid, C91.8)
  song_id      TEXT NOT NULL REFERENCES songs(id),
  seq          INTEGER NOT NULL CHECK (seq >= 1),
  at           TEXT NOT NULL,
  from_take_id TEXT REFERENCES tracks(id),
  to_take_id   TEXT REFERENCES tracks(id),
  from_source  TEXT NOT NULL CHECK (from_source IN ('none','pinned','ladder','stars','newest')),
  to_source    TEXT NOT NULL CHECK (to_source   IN ('none','pinned','ladder','stars','newest')),
  reason       TEXT NOT NULL CHECK (reason IN ('initial','pin','unpin','pin_ineligible','pin_restored','ladder','stars','newest','emptied')),
  actor        TEXT NOT NULL,                            -- Principal.Key() for pin/unpin, else 'system:provenance'
  actor_kind   TEXT NOT NULL CHECK (actor_kind IN ('human','agent','system')),   -- with actor_name: the inputs api.RefFor(viewer, key, kind, name) needs
  actor_name   TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 120),         -- 'provenance' for system
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),     -- mm.pick/1 (C91.3)
  UNIQUE (song_id, seq),
  CHECK ((from_source = 'none') = (from_take_id IS NULL)),
  CHECK ((to_source   = 'none') = (to_take_id   IS NULL)),
  CHECK (from_take_id IS NOT to_take_id OR from_source <> to_source)
);
CREATE INDEX pick_events_song ON pick_events(song_id, seq);
CREATE TRIGGER pick_events_immutable BEFORE UPDATE ON pick_events
BEGIN SELECT RAISE(ABORT, 'pick_events: append-only'); END;
CREATE TRIGGER pick_events_no_delete BEFORE DELETE ON pick_events
BEGIN SELECT RAISE(ABORT, 'pick_events: append-only'); END;
CREATE TRIGGER pick_events_chain BEFORE INSERT ON pick_events
WHEN NEW.seq <> COALESCE((SELECT MAX(seq) FROM pick_events WHERE song_id = NEW.song_id), 0) + 1
  OR COALESCE((SELECT 1 FROM pick_events p WHERE p.song_id = NEW.song_id AND p.seq = NEW.seq - 1
                  AND p.to_source = NEW.from_source AND p.to_take_id IS NEW.from_take_id),
              NEW.seq = 1 AND NEW.from_source = 'none') = 0
BEGIN SELECT RAISE(ABORT, 'pick_events: broken chain'); END;
CREATE TRIGGER pick_events_take_in_song BEFORE INSERT ON pick_events
WHEN NEW.to_take_id IS NOT NULL
 AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.to_take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'pick_events: take is not in the song'); END;

CREATE TABLE song_picks (                                -- current pick; one row per Song that ever had an eligible take
  song_id        TEXT PRIMARY KEY REFERENCES songs(id),
  take_id        TEXT REFERENCES tracks(id),
  source         TEXT NOT NULL CHECK (source IN ('none','pinned','ladder','stars','newest')),
  pinned_take_id TEXT REFERENCES tracks(id),             -- kept while the pinned take is ineligible (D-L1-38); cleared only by an explicit unpin
  pinned_by      TEXT,
  pinned_at      TEXT,
  evidence_json  TEXT NOT NULL CHECK (json_valid(evidence_json)),
  last_event_id  TEXT NOT NULL REFERENCES pick_events(id),
  changed_at     TEXT NOT NULL,                          -- time of last_event_id
  computed_at    TEXT NOT NULL,                          -- last derivation (evidence refresh)
  CHECK ((take_id IS NULL) = (source = 'none')),
  CHECK (source <> 'pinned' OR take_id = pinned_take_id),
  CHECK ((pinned_take_id IS NULL) = (pinned_by IS NULL)),
  CHECK ((pinned_take_id IS NULL) = (pinned_at IS NULL))
) WITHOUT ROWID;
CREATE INDEX song_picks_take ON song_picks(take_id);
CREATE TRIGGER song_picks_no_delete BEFORE DELETE ON song_picks
BEGIN SELECT RAISE(ABORT, 'song_picks: no physical delete'); END;
CREATE TRIGGER song_picks_frozen BEFORE UPDATE OF song_id ON song_picks
BEGIN SELECT RAISE(ABORT, 'song_picks: frozen column'); END;
CREATE TRIGGER song_picks_first_event BEFORE INSERT ON song_picks
WHEN COALESCE((SELECT 1 FROM pick_events e WHERE e.id = NEW.last_event_id AND e.song_id = NEW.song_id
                  AND e.seq = 1 AND e.from_source = 'none' AND e.to_source = NEW.source
                  AND e.to_take_id IS NEW.take_id), 0) = 0
BEGIN SELECT RAISE(ABORT, 'song_picks: a pick needs its first event'); END;
CREATE TRIGGER song_picks_event_required BEFORE UPDATE OF take_id, source, last_event_id ON song_picks
WHEN (NEW.take_id IS NOT OLD.take_id OR NEW.source <> OLD.source OR NEW.last_event_id IS NOT OLD.last_event_id)
 AND COALESCE((SELECT 1 FROM pick_events e WHERE e.id = NEW.last_event_id AND e.song_id = NEW.song_id
                  AND e.from_source = OLD.source AND e.from_take_id IS OLD.take_id
                  AND e.to_source = NEW.source AND e.to_take_id IS NEW.take_id
                  AND e.seq = (SELECT MAX(seq) FROM pick_events x WHERE x.song_id = NEW.song_id)), 0) = 0
BEGIN SELECT RAISE(ABORT, 'song_picks: change needs a matching new event'); END;
CREATE TRIGGER song_picks_take_in_song BEFORE INSERT ON song_picks
WHEN (NEW.take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
            IS NOT NEW.song_id)
  OR (NEW.pinned_take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
            IS NOT NEW.song_id)
BEGIN SELECT RAISE(ABORT, 'song_picks: take is not in the song'); END;
CREATE TRIGGER song_picks_take_in_song_upd BEFORE UPDATE OF take_id, pinned_take_id ON song_picks
WHEN (NEW.take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
            IS NOT NEW.song_id)
  OR (NEW.pinned_take_id IS NOT NULL
        AND (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.pinned_take_id))
            IS NOT NEW.song_id)
BEGIN SELECT RAISE(ABORT, 'song_picks: take is not in the song'); END;

CREATE TABLE pick_proposals (
  id             TEXT PRIMARY KEY,                       -- UUIDv7
  song_id        TEXT NOT NULL REFERENCES songs(id),
  take_id        TEXT NOT NULL REFERENCES tracks(id),
  rationale      TEXT NOT NULL CHECK (length(rationale) BETWEEN 1 AND 500),
  principal_id   TEXT NOT NULL,                          -- Principal.Key() (agents only)
  principal_name TEXT NOT NULL,
  state          TEXT NOT NULL CHECK (state IN ('open','accepted','dismissed','superseded','expired')),   -- expired: the take became ineligible or the Pick (C91.5)
  created_at     TEXT NOT NULL,
  decided_at     TEXT,
  decided_by     TEXT,                                   -- Principal.Key() of the human; the proposing key when superseded; 'system:provenance' when expired
  CHECK ((state = 'open') = (decided_at IS NULL)),
  CHECK ((decided_at IS NULL) = (decided_by IS NULL))
);
CREATE UNIQUE INDEX pick_proposals_one_open ON pick_proposals(song_id, principal_id) WHERE state = 'open';
CREATE INDEX pick_proposals_song ON pick_proposals(song_id, created_at);
CREATE TRIGGER pick_proposals_no_delete BEFORE DELETE ON pick_proposals
BEGIN SELECT RAISE(ABORT, 'pick_proposals: no physical delete'); END;
CREATE TRIGGER pick_proposals_frozen BEFORE UPDATE OF id, song_id, take_id, rationale, principal_id,
  principal_name, created_at ON pick_proposals
BEGIN SELECT RAISE(ABORT, 'pick_proposals: frozen column'); END;
CREATE TRIGGER pick_proposals_decided BEFORE UPDATE ON pick_proposals WHEN OLD.state <> 'open'
BEGIN SELECT RAISE(ABORT, 'pick_proposals: decided proposals are immutable'); END;
CREATE TRIGGER pick_proposals_take_in_song BEFORE INSERT ON pick_proposals
WHEN (SELECT song_id FROM song_roots WHERE root_track_id = (SELECT root_track_id FROM tracks WHERE id = NEW.take_id))
     IS NOT NEW.song_id
BEGIN SELECT RAISE(ABORT, 'pick_proposals: take is not in the song'); END;

-- ---- Sessions ----------------------------------------------------------------------------------
CREATE TABLE sessions (
  id          TEXT PRIMARY KEY,                          -- UUIDv7
  kind        TEXT NOT NULL CHECK (kind IN ('import','upload','batch','night')),   -- frozen: a collection's kind is decided once, at first sync (D-L1-13)
  source_key  TEXT UNIQUE CHECK (source_key IS NULL OR length(source_key) BETWEEN 1 AND 300),
  name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  started_at  TEXT NOT NULL,
  ended_at    TEXT,
  created_by  TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  hidden_at   TEXT,
  hidden_by   TEXT,
  CHECK (ended_at IS NULL OR ended_at >= started_at),    -- both written as FormatTime(ParseInstant(source)) so the string compare is valid (D-L1-34)
  CHECK ((hidden_at IS NULL) = (hidden_by IS NULL))
);
CREATE INDEX sessions_started ON sessions(started_at, id);
CREATE TRIGGER sessions_no_delete BEFORE DELETE ON sessions
BEGIN SELECT RAISE(ABORT, 'sessions: no physical delete'); END;
CREATE TRIGGER sessions_frozen BEFORE UPDATE OF id, kind, source_key, created_by, created_at ON sessions
BEGIN SELECT RAISE(ABORT, 'sessions: frozen column'); END;

CREATE TABLE session_members (
  session_id  TEXT NOT NULL REFERENCES sessions(id),
  member_kind TEXT NOT NULL CHECK (member_kind IN ('draft','track')),
  member_id   TEXT NOT NULL,
  added_at    TEXT NOT NULL,
  PRIMARY KEY (session_id, member_kind, member_id)
) WITHOUT ROWID;
CREATE INDEX session_members_member ON session_members(member_kind, member_id);
CREATE TRIGGER session_members_immutable BEFORE UPDATE ON session_members
BEGIN SELECT RAISE(ABORT, 'session_members: append-only'); END;
CREATE TRIGGER session_members_no_delete BEFORE DELETE ON session_members
BEGIN SELECT RAISE(ABORT, 'session_members: append-only'); END;
CREATE TRIGGER session_members_exists BEFORE INSERT ON session_members
WHEN (NEW.member_kind = 'draft' AND NOT EXISTS (SELECT 1 FROM recipe_drafts WHERE id = NEW.member_id))
  OR (NEW.member_kind = 'track' AND NOT EXISTS (SELECT 1 FROM tracks WHERE id = NEW.member_id))
BEGIN SELECT RAISE(ABORT, 'session_members: unknown member'); END;

-- ---- Sounds ------------------------------------------------------------------------------------
CREATE TABLE sound_taxonomy_versions (
  version    INTEGER PRIMARY KEY CHECK (version >= 1),
  kind       TEXT NOT NULL CHECK (kind IN ('seed','extension')),
  seed_id    TEXT UNIQUE,                                -- 'seed/1' … for code seeds; NULL for extensions
  created_by TEXT NOT NULL,                              -- 'system:seed' or Principal.Key()
  created_at TEXT NOT NULL,
  note       TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
  CHECK ((kind = 'seed') = (seed_id IS NOT NULL))
);
CREATE TRIGGER sound_versions_immutable BEFORE UPDATE ON sound_taxonomy_versions
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: append-only'); END;
CREATE TRIGGER sound_versions_no_delete BEFORE DELETE ON sound_taxonomy_versions
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: append-only'); END;
CREATE TRIGGER sound_versions_monotonic BEFORE INSERT ON sound_taxonomy_versions
WHEN NEW.version <> COALESCE((SELECT MAX(version) FROM sound_taxonomy_versions), 0) + 1
BEGIN SELECT RAISE(ABORT, 'sound_taxonomy_versions: versions are consecutive'); END;

CREATE TABLE sound_terms (
  version INTEGER NOT NULL REFERENCES sound_taxonomy_versions(version),
  term    TEXT NOT NULL CHECK (length(term) BETWEEN 1 AND 60),      -- textfold.Norm (lower-case, whitespace-collapsed) with - and / replaced by a space
  facet   TEXT NOT NULL CHECK (facet IN ('genre_family','era','vocal','mood','language')),
  value   TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  op      TEXT NOT NULL CHECK (op IN ('add','remove')),
  PRIMARY KEY (version, term, facet, value)
) WITHOUT ROWID;
CREATE INDEX sound_terms_term ON sound_terms(term);
CREATE TRIGGER sound_terms_immutable BEFORE UPDATE ON sound_terms
BEGIN SELECT RAISE(ABORT, 'sound_terms: append-only'); END;
CREATE TRIGGER sound_terms_no_delete BEFORE DELETE ON sound_terms
BEGIN SELECT RAISE(ABORT, 'sound_terms: append-only'); END;
CREATE TRIGGER sound_terms_newest_only BEFORE INSERT ON sound_terms
WHEN NEW.version <> (SELECT MAX(version) FROM sound_taxonomy_versions)
BEGIN SELECT RAISE(ABORT, 'sound_terms: only the newest version accepts terms'); END;

-- Derived caches (rebuilt by the Reconciler; deletable, D-L1-24).
CREATE TABLE take_sounds (
  track_id    TEXT NOT NULL REFERENCES tracks(id),
  facet       TEXT NOT NULL CHECK (facet IN ('genre_family','era','tempo_band','vocal','mood','language','engine')),
  value       TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  origin      TEXT NOT NULL CHECK (origin IN ('style','lyrics','declared','plan','measured','recipe')),
  detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),
  PRIMARY KEY (track_id, facet, value, origin)
) WITHOUT ROWID;
CREATE INDEX take_sounds_facet ON take_sounds(facet, value);

CREATE TABLE song_sounds (
  song_id     TEXT NOT NULL REFERENCES songs(id),
  facet       TEXT NOT NULL CHECK (facet IN ('genre_family','era','tempo_band','vocal','mood','language','engine')),
  value       TEXT NOT NULL CHECK (length(value) BETWEEN 1 AND 40),
  origin      TEXT NOT NULL CHECK (origin IN ('style','lyrics','declared','plan','measured','recipe')),
  detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)),
  PRIMARY KEY (song_id, facet, value)
) WITHOUT ROWID;
CREATE INDEX song_sounds_facet ON song_sounds(facet, value, song_id);

CREATE TABLE sound_state (                               -- freshness of every derived Sound subject
  subject_kind     TEXT NOT NULL CHECK (subject_kind IN ('take','song')),
  subject_id       TEXT NOT NULL,
  taxonomy_version INTEGER NOT NULL,
  input_digest     TEXT NOT NULL,                        -- sha256 over the inputs (C93.4)
  computed_at      TEXT NOT NULL,
  PRIMARY KEY (subject_kind, subject_id)
) WITHOUT ROWID;

CREATE TABLE song_unmapped_terms (                       -- style parts no taxonomy phrase matched (C93.3)
  song_id TEXT NOT NULL REFERENCES songs(id),
  term    TEXT NOT NULL CHECK (length(term) BETWEEN 1 AND 40),
  PRIMARY KEY (song_id, term)
) WITHOUT ROWID;
CREATE INDEX song_unmapped_term ON song_unmapped_terms(term);

-- ---- Reconcile queue (D-L1-4) ------------------------------------------------------------------
CREATE TABLE provenance_dirty (
  kind TEXT NOT NULL CHECK (kind IN ('track','draft','run','collection','batch')),
  id   TEXT NOT NULL,                                    -- track/draft/run/batch id, or the collection name
  at   TEXT NOT NULL,
  tries INTEGER NOT NULL DEFAULT 0 CHECK (tries >= 0),   -- Reconciler re-marks of a transient anomaly (≤ MaxQueueTries, C95.1); reset by every trigger mark
  PRIMARY KEY (kind, id)
) WITHOUT ROWID;

CREATE TABLE provenance_lease (                          -- the cross-process settle fence (D-L1-35); one row, never deleted
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  owner      TEXT NOT NULL,                              -- '' = free
  epoch      INTEGER NOT NULL CHECK (epoch >= 1),        -- bumped on every acquisition; phase transactions check it
  expires_at TEXT NOT NULL
);
CREATE TRIGGER provenance_lease_no_delete BEFORE DELETE ON provenance_lease
BEGIN SELECT RAISE(ABORT, 'provenance_lease: no physical delete'); END;
INSERT INTO provenance_lease(id, owner, epoch, expires_at) VALUES (1, '', 1, '1970-01-01T00:00:00.000Z');

-- Triggers on source tables only enqueue (never write derived rows).
CREATE TRIGGER pd_tracks_ai AFTER INSERT ON tracks BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_tracks_status AFTER UPDATE OF status ON tracks BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_ratings_ai AFTER INSERT ON ratings WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_ratings_au AFTER UPDATE OF stars ON ratings WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_verdicts_ai AFTER INSERT ON duel_verdicts WHEN NEW.source = 'human' BEGIN
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', d.slot_a_track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM duels d WHERE d.id = NEW.duel_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', d.slot_b_track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM duels d WHERE d.id = NEW.duel_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_analyses_tempo AFTER INSERT ON analyses WHEN NEW.analyzer = 'tempo' BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_drafts_ai AFTER INSERT ON recipe_drafts BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_drafts_au AFTER UPDATE OF parent_track_id, request_json, status, fulfilled_by_track_id, label ON recipe_drafts BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_collections_ai AFTER INSERT ON draft_collections BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('collection', NEW.name, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('draft', NEW.draft_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_runs_ai AFTER INSERT ON import_runs BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('run', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_runs_au AFTER UPDATE OF state, finished_at ON import_runs BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('run', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_batches_ai AFTER INSERT ON batches BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_batches_au AFTER UPDATE OF state, finished_at ON batches BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE TRIGGER pd_jobs_au AFTER UPDATE OF track_id ON jobs WHEN NEW.track_id IS NOT NULL BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.batch_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

CREATE TRIGGER pd_refs_ai AFTER INSERT ON recipe_refs BEGIN    -- a late index row reaches existing Songs (D-L1-37)
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'draft', d.id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM recipe_drafts d WHERE d.source_recipe_id = NEW.recipe_id
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
  INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', t.id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM tracks t WHERE t.recipe_id = NEW.recipe_id AND t.generation = 0
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

-- Backfill = the steady-state path: seed the queue from every existing source row (D-L1-4). The `at` values are incidental
-- (one strftime per row); the Reconciler orders by kind rank first, then `at` (C95.1).
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'run', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM import_runs;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'collection', name, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (SELECT DISTINCT name FROM draft_collections);
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'batch', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM batches;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'draft', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM recipe_drafts;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM tracks;
