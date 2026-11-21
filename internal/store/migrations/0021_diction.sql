-- 0021_diction.sql: M5b EARS-D1 (C344.1). The Italian Diction Lab keeps two small tables of human judgement: line marks (what a
-- listener heard, which override the recogniser) and lexicon overrides (a word the Owner declares ignored or active). Both are data
-- the Owner writes and are never physically deleted; a mark is withdrawn by writing 'none', an override reverted by writing
-- 'active'. The lexicon itself is computed on read from the Tier B diction rows and is not stored. Additive, up-only.
CREATE TABLE diction_marks (
  track_id    TEXT NOT NULL REFERENCES tracks(id),
  section_idx INTEGER NOT NULL CHECK (section_idx >= 0),
  line_idx    INTEGER NOT NULL CHECK (line_idx >= 0),
  source      TEXT NOT NULL CHECK (source IN ('human','critic')),
  mark        TEXT NOT NULL CHECK (mark IN ('clear','unclear','none')),
  marked_by   TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (track_id, section_idx, line_idx, source)
) WITHOUT ROWID;
CREATE INDEX diction_marks_updated ON diction_marks(updated_at);
CREATE TRIGGER diction_marks_no_delete BEFORE DELETE ON diction_marks
BEGIN SELECT RAISE(ABORT, 'diction_marks: no physical delete'); END;
CREATE TRIGGER diction_marks_frozen BEFORE UPDATE OF track_id, section_idx, line_idx, source ON diction_marks
BEGIN SELECT RAISE(ABORT, 'diction_marks: frozen column'); END;

CREATE TABLE lexicon_overrides (
  lang       TEXT NOT NULL CHECK (length(lang) BETWEEN 2 AND 3),
  word_fold  TEXT NOT NULL CHECK (length(word_fold) BETWEEN 1 AND 64),
  status     TEXT NOT NULL CHECK (status IN ('active','ignored')),
  note       TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (lang, word_fold)
) WITHOUT ROWID;
CREATE TRIGGER lexicon_overrides_no_delete BEFORE DELETE ON lexicon_overrides
BEGIN SELECT RAISE(ABORT, 'lexicon_overrides: no physical delete'); END;
