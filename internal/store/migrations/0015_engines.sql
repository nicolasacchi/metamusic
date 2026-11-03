-- mm:foreign_keys=off
-- 0015_engines.sql: second engine: jobs up to 600 s, RTF measurements with provenance, append-only engine
-- adoption log. Seed rows: the yue2 adoption row only (RTF seeds are a committed JSON file, not a migration).

-- ---- 1. jobs rebuilt (expected_audio_s BETWEEN 1 AND 600; nothing else changes) ---------------
-- BEGIN GENERATED jobs rebuild (scripts/gen-jobs-ddl.go)
-- Generated from the schema after migration 13; do not edit by hand (go run scripts/gen-jobs-ddl.go -check).
CREATE TABLE jobs_new (
  id                  TEXT PRIMARY KEY,                  -- UUIDv7 = take id
  batch_id            TEXT NOT NULL REFERENCES batches(id),
  take_index          INTEGER NOT NULL CHECK (take_index >= 0),
  kind                TEXT NOT NULL,                     -- Go-validated: 'generate' (D-M3-15)
  engine              TEXT NOT NULL,
  backend             TEXT NOT NULL,
  recipe_id           TEXT NOT NULL REFERENCES recipes(id),
  source_track_id     TEXT REFERENCES tracks(id),        -- parent; NULL for compose_new and motherless drafts
  draft_id            TEXT REFERENCES recipe_drafts(id),
  operator            TEXT NOT NULL,                     -- Go-validated (C71.2)
  mutation_json       TEXT NOT NULL CHECK (json_valid(mutation_json)),  -- {operator, params, recipe_patch, rationale}
  title               TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 600),
  expected_s          INTEGER NOT NULL CHECK (expected_s > 0),
  timeout_s           INTEGER NOT NULL CHECK (timeout_s BETWEEN 1 AND 2700),
  expected_micro_usd  INTEGER NOT NULL CHECK (expected_micro_usd >= 0),
  reserved_micro_usd  INTEGER NOT NULL CHECK (reserved_micro_usd > 0),   -- the `take` reserve at create (timeout_s + KillSlackS)
  state               TEXT NOT NULL,
  outcome             TEXT CHECK (outcome IS NULL OR outcome IN ('clean','truncated')),
  error_code          TEXT,
  cancel_requested_at TEXT,
  cancel_requested_by TEXT,
  lease_owner         TEXT,
  lease_expires_at    TEXT,
  next_attempt_at     TEXT NOT NULL,
  queued_at           TEXT,                              -- first entry into 'queued' (anchor of NOT_SUBMITTED, C75.2 row 6)
  first_submitted_at  TEXT,                              -- anchor of the 24 h resubmit window
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  resubmits           INTEGER NOT NULL DEFAULT 0 CHECK (resubmits >= 0),
  queue_expiries      INTEGER NOT NULL DEFAULT 0 CHECK (queue_expiries >= 0),   -- ≤ MaxQueueExpiryResubmits (6)
  state_deadline_at   TEXT,
  started_at          TEXT,                              -- first Running or start announcement of any attempt
  finished_at         TEXT,
  track_id            TEXT REFERENCES tracks(id),
  env_id              TEXT REFERENCES env_fingerprints(env_id),
  audio_s             REAL CHECK (audio_s IS NULL OR audio_s > 0),
  truncated_abc       INTEGER CHECK (truncated_abc IN (0,1)),
  truncated_semantic  INTEGER CHECK (truncated_semantic IN (0,1)),
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL, patch_id TEXT REFERENCES patches(id),
  UNIQUE (batch_id, take_index),
  CHECK ((state = 'completed') = (outcome IS NOT NULL)),
  CHECK (state <> 'completed' OR track_id IS NOT NULL),
  CHECK ((state IN ('completed','failed','cancelled','expired')) = (finished_at IS NOT NULL)),
  CHECK ((cancel_requested_at IS NULL) = (cancel_requested_by IS NULL)),
  CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL)),
  CHECK (state NOT IN ('awaiting_confirmation','queued','completed','failed','cancelled','expired')
         OR lease_owner IS NULL)                                 -- unleased states never hold a job lease (C69)
);
INSERT INTO jobs_new (id, batch_id, take_index, kind, engine, backend, recipe_id, source_track_id, draft_id, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd, state, outcome, error_code, cancel_requested_at, cancel_requested_by, lease_owner, lease_expires_at, next_attempt_at, queued_at, first_submitted_at, attempts, resubmits, queue_expiries, state_deadline_at, started_at, finished_at, track_id, env_id, audio_s, truncated_abc, truncated_semantic, created_at, updated_at, patch_id)
  SELECT id, batch_id, take_index, kind, engine, backend, recipe_id, source_track_id, draft_id, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd, state, outcome, error_code, cancel_requested_at, cancel_requested_by, lease_owner, lease_expires_at, next_attempt_at, queued_at, first_submitted_at, attempts, resubmits, queue_expiries, state_deadline_at, started_at, finished_at, track_id, env_id, audio_s, truncated_abc, truncated_semantic, created_at, updated_at, patch_id FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE INDEX jobs_queue  ON jobs(state, next_attempt_at);
CREATE INDEX jobs_lease  ON jobs(lease_owner, lease_expires_at);
CREATE INDEX jobs_batch  ON jobs(batch_id, take_index);
CREATE INDEX jobs_track  ON jobs(track_id);
CREATE INDEX jobs_source ON jobs(source_track_id);
CREATE INDEX jobs_draft  ON jobs(draft_id);
CREATE TRIGGER jobs_no_delete BEFORE DELETE ON jobs
BEGIN SELECT RAISE(ABORT, 'jobs: no physical delete'); END;
CREATE TRIGGER jobs_initial_state BEFORE INSERT ON jobs
WHEN NEW.state NOT IN ('awaiting_confirmation','queued')
BEGIN SELECT RAISE(ABORT, 'jobs: illegal initial state'); END;
CREATE TRIGGER jobs_frozen BEFORE UPDATE OF id, batch_id, take_index, kind, engine, backend, recipe_id,
  source_track_id, draft_id, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s,
  expected_micro_usd, reserved_micro_usd, created_at ON jobs
BEGIN SELECT RAISE(ABORT, 'jobs: frozen column'); END;
CREATE TRIGGER jobs_transition BEFORE UPDATE OF state ON jobs
WHEN NEW.state <> OLD.state AND NOT EXISTS
  (SELECT 1 FROM job_transitions t WHERE t.from_state = OLD.state AND t.to_state = NEW.state)
BEGIN SELECT RAISE(ABORT, 'jobs: illegal transition'); END;
CREATE TRIGGER jobs_final BEFORE UPDATE ON jobs
WHEN OLD.state IN ('completed','failed','cancelled','expired')
BEGIN SELECT RAISE(ABORT, 'jobs: terminal jobs are immutable'); END;
CREATE TRIGGER pd_jobs_au AFTER UPDATE OF track_id ON jobs WHEN NEW.track_id IS NOT NULL BEGIN
  INSERT INTO provenance_dirty(kind, id, at) VALUES ('batch', NEW.batch_id, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
  ON CONFLICT(kind, id) DO UPDATE SET at = excluded.at, tries = 0;
END;
CREATE INDEX jobs_patch ON jobs(patch_id) WHERE patch_id IS NOT NULL;
CREATE TRIGGER jobs_patch_frozen BEFORE UPDATE OF patch_id ON jobs WHEN OLD.patch_id IS NOT NEW.patch_id
BEGIN SELECT RAISE(ABORT, 'jobs: frozen column'); END;
-- END GENERATED jobs rebuild

-- ---- 2. RTF measurements per engine, task class and GPU class ----
CREATE TABLE engine_rtf (                                 -- measurements only; seeds live in internal/spend/engine_rtf.seed.json
  engine     TEXT NOT NULL,
  task_class TEXT NOT NULL CHECK (task_class IN ('turbo','base')),
  gpu_class  TEXT NOT NULL,
  rtf_milli  INTEGER NOT NULL CHECK (rtf_milli BETWEEN 5 AND 5000),
  fixed_s    INTEGER NOT NULL CHECK (fixed_s BETWEEN 0 AND 600),
  samples    INTEGER NOT NULL CHECK (samples >= 1),
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL CHECK (updated_by GLOB 'job:?*' OR updated_by GLOB 'canary:?*' OR updated_by GLOB 'human:?*'),
  canary_id  TEXT,                                        -- the canary run that wrote the row; NULL otherwise
  CHECK ((updated_by GLOB 'canary:*') = (canary_id IS NOT NULL)),
  PRIMARY KEY (engine, task_class, gpu_class)
) WITHOUT ROWID;
CREATE TRIGGER engine_rtf_no_delete BEFORE DELETE ON engine_rtf
BEGIN SELECT RAISE(ABORT, 'engine_rtf: no physical delete'); END;

-- ---- 3. adoption: an append-only decision log, the latest row per engine is the state ----
CREATE TABLE engine_adoption_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  engine     TEXT NOT NULL,
  state      TEXT NOT NULL CHECK (state IN ('adopted','not_adopted')),
  via        TEXT NOT NULL CHECK (via IN ('seed','cli','trial')),   -- 'trial' rows exist once engine trials land
  trial_id   TEXT,                                        -- the deciding trial (written by the trials feature; no FK); NULL for seed and cli rows
  env_ids    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(env_ids) AND json_type(env_ids) = 'array'),
  changed_by TEXT NOT NULL,
  changed_at TEXT NOT NULL,
  reason     TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
  CHECK (via <> 'cli' OR reason IS NOT NULL),
  CHECK ((via = 'trial') = (trial_id IS NOT NULL))
);
CREATE INDEX engine_adoption_log_engine ON engine_adoption_log(engine, id DESC);
INSERT INTO engine_adoption_log (engine, state, via, trial_id, env_ids, changed_by, changed_at, reason)
VALUES ('yue2','adopted','seed',NULL,'[]','system:default','1970-01-01T00:00:00.000Z',NULL);
CREATE TRIGGER engine_adoption_log_no_update BEFORE UPDATE ON engine_adoption_log
BEGIN SELECT RAISE(ABORT, 'engine_adoption_log: append-only'); END;
CREATE TRIGGER engine_adoption_log_no_delete BEFORE DELETE ON engine_adoption_log
BEGIN SELECT RAISE(ABORT, 'engine_adoption_log: no physical delete'); END;
CREATE VIEW engine_adoption AS                              -- the current state: the latest log row per engine
SELECT l.engine, l.state, l.via, l.trial_id, l.env_ids, l.changed_by, l.changed_at, l.reason
FROM engine_adoption_log l
WHERE l.id = (SELECT MAX(id) FROM engine_adoption_log WHERE engine = l.engine);
