-- 0011_agents_atlas.sql — L3: features, sequences, taxonomy proposals. Writes only queue rows (no feature, proposal or sequence row).

-- ---- Features (derived caches; rebuilt by the Reconciler; deletable, D-L3-3) ---------------------------
CREATE TABLE take_features (
  track_id           TEXT PRIMARY KEY REFERENCES tracks(id),
  bpm_x100           INTEGER CHECK (bpm_x100 IS NULL OR bpm_x100 BETWEEN 2000 AND 30000),            -- POSITION tempo (D-L3-5)
  bpm_basis          TEXT    CHECK (bpm_basis IS NULL OR bpm_basis IN ('measured','declared','plan')),
  bpm_requested_x100 INTEGER CHECK (bpm_requested_x100 IS NULL OR bpm_requested_x100 BETWEEN 2000 AND 30000),   -- declared, else plan
  bpm_measured_x100  INTEGER CHECK (bpm_measured_x100 IS NULL OR bpm_measured_x100 BETWEEN 2000 AND 30000),     -- Ears `tempo`
  bpm_octave         INTEGER NOT NULL DEFAULT 0 CHECK (bpm_octave IN (0,1)),
  energy_x10000      INTEGER CHECK (energy_x10000 IS NULL OR energy_x10000 BETWEEN 0 AND 10000),
  energy_basis       TEXT    CHECK (energy_basis IS NULL OR energy_basis IN ('measured','predicted')),
  key_num            INTEGER CHECK (key_num IS NULL OR key_num BETWEEN 1 AND 12),
  key_letter         TEXT    CHECK (key_letter IS NULL OR key_letter IN ('A','B')),
  key_basis          TEXT    CHECK (key_basis IS NULL OR key_basis IN ('measured','declared','plan')),
  duration_s         REAL    CHECK (duration_s IS NULL OR duration_s > 0),
  duration_expected_s REAL   CHECK (duration_expected_s IS NULL OR duration_expected_s > 0),         -- scores.bars * seconds_per_bar
  detail_json        TEXT    CHECK (detail_json IS NULL OR json_valid(detail_json)),
  digest             TEXT NOT NULL CHECK (length(digest) = 64),
  feature_def        TEXT NOT NULL,                                                                  -- 'mm-features/1'
  computed_at        TEXT NOT NULL,
  CHECK ((bpm_x100 IS NULL) = (bpm_basis IS NULL)),
  CHECK ((energy_x10000 IS NULL) = (energy_basis IS NULL)),
  CHECK ((key_num IS NULL) = (key_letter IS NULL) AND (key_num IS NULL) = (key_basis IS NULL))
) WITHOUT ROWID;

CREATE TABLE song_features (                                   -- from the Song's SUBJECT exactly as sounds.SubjectOf defines it (C135.5, D-L3-39)
  song_id            TEXT PRIMARY KEY REFERENCES songs(id),
  subject_kind       TEXT NOT NULL CHECK (subject_kind IN ('take','draft')),
  subject_id         TEXT NOT NULL,
  subject_note       TEXT CHECK (subject_note IS NULL OR subject_note IN ('all_takes_discarded')),        -- set when a Song that HAS takes is derived from its draft because every take is discarded (C135.5)
  bpm_x100           INTEGER CHECK (bpm_x100 IS NULL OR bpm_x100 BETWEEN 2000 AND 30000),
  bpm_basis          TEXT    CHECK (bpm_basis IS NULL OR bpm_basis IN ('measured','declared','plan')),
  bpm_requested_x100 INTEGER CHECK (bpm_requested_x100 IS NULL OR bpm_requested_x100 BETWEEN 2000 AND 30000),
  bpm_measured_x100  INTEGER CHECK (bpm_measured_x100 IS NULL OR bpm_measured_x100 BETWEEN 2000 AND 30000),
  bpm_octave         INTEGER NOT NULL DEFAULT 0 CHECK (bpm_octave IN (0,1)),
  energy_x10000      INTEGER CHECK (energy_x10000 IS NULL OR energy_x10000 BETWEEN 0 AND 10000),
  energy_basis       TEXT    CHECK (energy_basis IS NULL OR energy_basis IN ('measured','predicted')),
  key_num            INTEGER CHECK (key_num IS NULL OR key_num BETWEEN 1 AND 12),
  key_letter         TEXT    CHECK (key_letter IS NULL OR key_letter IN ('A','B')),
  key_basis          TEXT    CHECK (key_basis IS NULL OR key_basis IN ('measured','declared','plan')),
  duration_s         REAL    CHECK (duration_s IS NULL OR duration_s > 0),
  duration_expected_s REAL   CHECK (duration_expected_s IS NULL OR duration_expected_s > 0),
  family             TEXT CHECK (family   IS NULL OR length(family)   BETWEEN 1 AND 40),             -- PRIMARY Sound values (smallest song_sounds.ord, D-L3-9)
  vocal              TEXT CHECK (vocal    IS NULL OR length(vocal)    BETWEEN 1 AND 40),
  era                TEXT CHECK (era      IS NULL OR length(era)      BETWEEN 1 AND 40),
  mood               TEXT CHECK (mood     IS NULL OR length(mood)     BETWEEN 1 AND 40),
  language           TEXT CHECK (language IS NULL OR length(language) BETWEEN 1 AND 40),
  digest             TEXT NOT NULL CHECK (length(digest) = 64),
  feature_def        TEXT NOT NULL,
  computed_at        TEXT NOT NULL,
  CHECK ((bpm_x100 IS NULL) = (bpm_basis IS NULL)),
  CHECK ((energy_x10000 IS NULL) = (energy_basis IS NULL)),
  CHECK ((key_num IS NULL) = (key_letter IS NULL) AND (key_num IS NULL) = (key_basis IS NULL))
) WITHOUT ROWID;
CREATE INDEX song_features_xy     ON song_features(bpm_x100, energy_x10000);
CREATE INDEX song_features_family ON song_features(family, vocal);
CREATE INDEX song_features_key    ON song_features(key_num, key_letter);

-- ---- Sequences (append-only; the explanation of an applied ordering, D-L3-10/13) --------------------
CREATE TABLE record_sequences (
  id                TEXT PRIMARY KEY,                          -- UUIDv7
  record_id         TEXT NOT NULL REFERENCES records(id),
  record_revision   INTEGER NOT NULL CHECK (record_revision >= 1),       -- the revision the edit was made against
  strategy          TEXT NOT NULL CHECK (length(strategy) BETWEEN 1 AND 24),     -- validated in Go (sequencer.Strategies, D-L3-40)
  first_song_id     TEXT REFERENCES songs(id),
  last_song_id      TEXT REFERENCES songs(id),
  order_before_json TEXT NOT NULL CHECK (json_valid(order_before_json) AND json_type(order_before_json) = 'array'),   -- slot ids
  order_after_json  TEXT NOT NULL CHECK (json_valid(order_after_json)  AND json_type(order_after_json)  = 'array'),
  exact             INTEGER NOT NULL CHECK (exact IN (0,1)),
  cost_input        INTEGER NOT NULL CHECK (cost_input  >= 0),                                                       -- J of the input order as given (even when it violates the endpoints), units of 1e-10
  cost_output       INTEGER NOT NULL CHECK (cost_output >= 0),
  coverage_json     TEXT NOT NULL CHECK (json_valid(coverage_json)),
  digest            TEXT NOT NULL CHECK (length(digest) = 64),
  explanation_json  TEXT NOT NULL CHECK (json_valid(explanation_json)),                                              -- mm.sequence/1: ids and numbers only
  actor             TEXT NOT NULL,                                                                                   -- Principal.Key()
  actor_kind        TEXT NOT NULL CHECK (actor_kind IN ('human','agent')),
  actor_name        TEXT NOT NULL CHECK (length(actor_name) BETWEEN 1 AND 120),
  at                TEXT NOT NULL,
  CHECK (json_array_length(order_before_json) = json_array_length(order_after_json))                               -- NO cost CHECK: with endpoints the output may cost more than the input order (C137.4)
);
CREATE INDEX record_sequences_record ON record_sequences(record_id, at);
CREATE TRIGGER record_sequences_immutable BEFORE UPDATE ON record_sequences
BEGIN SELECT RAISE(ABORT, 'record_sequences: append-only'); END;
CREATE TRIGGER record_sequences_no_delete BEFORE DELETE ON record_sequences
BEGIN SELECT RAISE(ABORT, 'record_sequences: append-only'); END;

-- ---- Taxonomy proposals (D-L3-20) --------------------------------------------------------------------
CREATE TABLE taxonomy_proposals (
  id             TEXT PRIMARY KEY,                             -- UUIDv7
  state          TEXT NOT NULL CHECK (state IN ('open','accepted','rejected','expired')),
  base_version   INTEGER NOT NULL CHECK (base_version >= 1),   -- the taxonomy version the proposal was validated against
  rationale      TEXT NOT NULL CHECK (length(rationale) BETWEEN 1 AND 500),
  mappings_json  TEXT NOT NULL CHECK (json_valid(mappings_json) AND json_type(mappings_json) = 'array'
                                      AND json_array_length(mappings_json) BETWEEN 1 AND 50),   -- [{term, facet, value, op}] folded, validated, in call order
  mappings_digest TEXT NOT NULL CHECK (length(mappings_digest) = 64),
  impact_json    TEXT NOT NULL CHECK (json_valid(impact_json)),   -- snapshot at proposal time (C140.3)
  principal_id   TEXT NOT NULL,                                -- Principal.Key()
  principal_kind TEXT NOT NULL,
  principal_name TEXT NOT NULL CHECK (length(principal_name) BETWEEN 1 AND 120),
  created_at     TEXT NOT NULL,
  decided_at     TEXT,
  decided_by     TEXT,                                         -- Principal.Key() of the human; 'system:sounds' when expired
  decided_note   TEXT CHECK (decided_note IS NULL OR length(decided_note) BETWEEN 1 AND 300),
  accepted_json  TEXT CHECK (accepted_json IS NULL OR (json_valid(accepted_json) AND json_type(accepted_json) = 'array')),   -- accepted mapping indices
  skipped_json   TEXT CHECK (skipped_json IS NULL OR (json_valid(skipped_json) AND json_type(skipped_json) = 'array')),     -- [{index, reason}]
  applied_version INTEGER CHECK (applied_version IS NULL OR applied_version >= 1),
  CHECK ((state = 'open') = (decided_at IS NULL)),
  CHECK ((decided_at IS NULL) = (decided_by IS NULL)),
  CHECK ((state = 'accepted') = (applied_version IS NOT NULL)),
  CHECK ((state = 'accepted') = (accepted_json IS NOT NULL))
);
CREATE UNIQUE INDEX taxonomy_proposals_open_dup ON taxonomy_proposals(principal_id, mappings_digest) WHERE state = 'open';
CREATE INDEX taxonomy_proposals_state ON taxonomy_proposals(state, created_at);
CREATE TRIGGER taxonomy_proposals_no_delete BEFORE DELETE ON taxonomy_proposals
BEGIN SELECT RAISE(ABORT, 'taxonomy_proposals: no physical delete'); END;
CREATE TRIGGER taxonomy_proposals_frozen BEFORE UPDATE OF id, base_version, rationale, mappings_json, mappings_digest, impact_json,
  principal_id, principal_kind, principal_name, created_at ON taxonomy_proposals
BEGIN SELECT RAISE(ABORT, 'taxonomy_proposals: frozen column'); END;
CREATE TRIGGER taxonomy_proposals_decided BEFORE UPDATE ON taxonomy_proposals WHEN OLD.state <> 'open'
BEGIN SELECT RAISE(ABORT, 'taxonomy_proposals: decided proposals are immutable'); END;

-- ---- L1 amendments (D-L3-9) --------------------------------------------------------------------------
ALTER TABLE song_sounds ADD COLUMN ord INTEGER NOT NULL DEFAULT 0 CHECK (ord >= 0);   -- derived cache: the position of the row in the emitted rows, written by the Sounds compute (songDigest compute_def mm-sound-compute/2, D-L3-9)

-- A new Ears measurement re-derives the features of its take (L1's pd_analyses_tempo already covers `tempo`).
CREATE TRIGGER pd_analyses_feat AFTER INSERT ON analyses WHEN NEW.analyzer IN ('key','energy') BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('track', NEW.track_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;

-- Backfill = the steady-state path (D-L1-4): every take and every draft is re-marked once, so the Reconciler recomputes the
-- Sound subjects (new compute definition) and derives the features.
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'track', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM tracks WHERE 1
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
INSERT INTO provenance_dirty(kind, id, at)
  SELECT 'draft', id, strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM recipe_drafts WHERE 1
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
