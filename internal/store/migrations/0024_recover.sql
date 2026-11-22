-- 0024_recover.sql: M5b EARS-R1 (C343.1). Recover turns the listening check of an imported reference (a take whose recipe is unknown)
-- into a NEW draft. The link from the draft to the reference lives here, not on recipe_drafts and not on the reference: the
-- reference, its recipe stub, its sections and its analyses never change. A row is written in the same transaction as the draft it
-- describes, is immutable and is never physically deleted; the render of the draft later records the `recover` mutation that has
-- the reference as its parent. Additive, up-only.
CREATE TABLE draft_recoveries (
  draft_id        TEXT PRIMARY KEY REFERENCES recipe_drafts(id),
  track_id        TEXT NOT NULL REFERENCES tracks(id),          -- the reference (import_rule 'reference')
  analysis_sha256 TEXT NOT NULL CHECK (length(analysis_sha256) = 64),
  language        TEXT CHECK (language IS NULL OR length(language) BETWEEN 2 AND 3),
  words           INTEGER NOT NULL CHECK (words >= 1),
  confidence      REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
  style_source    TEXT NOT NULL CHECK (style_source IN ('caller','placeholder')),
  rationale       TEXT CHECK (rationale IS NULL OR length(rationale) BETWEEN 1 AND 1000),
  created_by      TEXT NOT NULL,
  created_at      TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX draft_recoveries_track ON draft_recoveries(track_id);
CREATE TRIGGER draft_recoveries_reference BEFORE INSERT ON draft_recoveries
WHEN COALESCE((SELECT import_rule FROM tracks WHERE id = NEW.track_id), '') <> 'reference'
BEGIN SELECT RAISE(ABORT, 'draft_recoveries: not a reference track'); END;
CREATE TRIGGER draft_recoveries_immutable BEFORE UPDATE ON draft_recoveries
BEGIN SELECT RAISE(ABORT, 'draft_recoveries: immutable'); END;
CREATE TRIGGER draft_recoveries_no_delete BEFORE DELETE ON draft_recoveries
BEGIN SELECT RAISE(ABORT, 'draft_recoveries: no physical delete'); END;
