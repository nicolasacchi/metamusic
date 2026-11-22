-- 0026_night_shift.sql: M6N-N2 (C377). The night shift's behaviour (one settings row, OFF until the Owner enables it in the app) and
-- the record of every night (one row per local day). Additive and up-only: two new tables and their triggers, nothing existing is
-- touched. The money lives in the mandate (0025), never here; a night is never deleted.

CREATE TABLE night_shift_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  at TEXT NOT NULL CHECK (length(at) = 5 AND substr(at, 3, 1) = ':'),
  max_takes INTEGER NOT NULL CHECK (max_takes BETWEEN 1 AND 12),
  explore_ppm INTEGER NOT NULL CHECK (explore_ppm BETWEEN 0 AND 500000),
  min_stars INTEGER NOT NULL CHECK (min_stars BETWEEN 1 AND 5),
  listening_checks TEXT NOT NULL CHECK (listening_checks IN ('on','off')),
  halted_at TEXT, halted_by TEXT,
  halt_reason TEXT CHECK (halt_reason IS NULL OR length(halt_reason) BETWEEN 1 AND 500),
  updated_by TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK ((halted_at IS NULL) = (halted_by IS NULL)),
  CHECK ((halted_at IS NULL) = (halt_reason IS NULL))
);
-- the only row: off, 01:30, up to 8 takes (the operator's approved 6 is set in the app), 30% explore, 4 stars, listening checks on
INSERT INTO night_shift_settings (id, enabled, at, max_takes, explore_ppm, min_stars, listening_checks, halted_at, halted_by, halt_reason, updated_by, updated_at)
VALUES (1, 0, '01:30', 8, 300000, 4, 'on', NULL, NULL, NULL, 'system:migration', '1970-01-01T00:00:00.000Z');

CREATE TABLE night_shift_runs (
  id TEXT PRIMARY KEY,
  night TEXT NOT NULL UNIQUE,                               -- the local YYYY-MM-DD of the slot (the claim)
  state TEXT NOT NULL CHECK (state IN ('planned','created','skipped','halted','failed')),
  reason TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 200),
  plan_json TEXT CHECK (plan_json IS NULL OR json_valid(plan_json)),
  estimate_id TEXT REFERENCES estimates(id),
  batch_id TEXT UNIQUE REFERENCES batches(id),
  mandate_id TEXT REFERENCES mandates(id),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  CHECK ((batch_id IS NULL) = (mandate_id IS NULL)),
  CHECK (batch_id IS NULL OR estimate_id IS NOT NULL),
  CHECK (state <> 'created' OR batch_id IS NOT NULL)
);
CREATE INDEX night_shift_runs_batch ON night_shift_runs(batch_id) WHERE batch_id IS NOT NULL;

CREATE TRIGGER night_shift_settings_no_delete BEFORE DELETE ON night_shift_settings
BEGIN SELECT RAISE(ABORT, 'night_shift_settings: no physical delete'); END;
CREATE TRIGGER night_shift_runs_no_delete BEFORE DELETE ON night_shift_runs
BEGIN SELECT RAISE(ABORT, 'night_shift_runs: no physical delete'); END;
CREATE TRIGGER night_shift_runs_frozen BEFORE UPDATE OF id, night, created_at ON night_shift_runs
BEGIN SELECT RAISE(ABORT, 'night_shift_runs: frozen column'); END;
-- the batch and its mandate are set once (when the batch was created) and never change
CREATE TRIGGER night_shift_runs_batch_once BEFORE UPDATE OF batch_id, mandate_id ON night_shift_runs
WHEN (OLD.batch_id IS NOT NULL AND NEW.batch_id IS NOT OLD.batch_id) OR (OLD.mandate_id IS NOT NULL AND NEW.mandate_id IS NOT OLD.mandate_id)
BEGIN SELECT RAISE(ABORT, 'night_shift_runs: batch and mandate are set once'); END;
-- planned -> created | skipped | halted | failed; created -> halted; the rest is final
CREATE TRIGGER night_shift_runs_transition BEFORE UPDATE OF state ON night_shift_runs
WHEN NEW.state IS NOT OLD.state AND NOT (OLD.state = 'planned' OR (OLD.state = 'created' AND NEW.state = 'halted'))
BEGIN SELECT RAISE(ABORT, 'night_shift_runs: illegal transition'); END;
