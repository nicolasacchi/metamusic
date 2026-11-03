-- 0014_transforms.sql — T1: CPU sound operators as Takes (engine 'dsp').
-- A transform is a queued CPU job whose result is an ordinary Take (D-T1-1). No table is rebuilt.

CREATE TABLE transforms (
  id                  TEXT PRIMARY KEY,                  -- UUIDv7 = the take id
  kind                TEXT NOT NULL CHECK (kind IN ('morph','shepard','glacier','polytempo','ensemble','score_render','master','widen','binaural')),
  recipe_id           TEXT NOT NULL REFERENCES recipes(id),
  spec_hash           TEXT NOT NULL CHECK (length(spec_hash) = 64),   -- sha256 of the canonical spec WITHOUT id; NOT recipes.hash
  title               TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  rationale           TEXT CHECK (rationale IS NULL OR length(rationale) BETWEEN 1 AND 1000),
  principal_id        TEXT NOT NULL,                     -- Principal.Key()
  principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('human','agent')),
  principal_name      TEXT NOT NULL,
  state               TEXT NOT NULL CHECK (state IN ('queued','running','done','failed','cancelled')),
  phase               TEXT CHECK (phase IS NULL OR phase IN ('preparing','analyzing','rendering','finishing','encoding','ingesting')),
  progress_permille   INTEGER NOT NULL DEFAULT 0 CHECK (progress_permille BETWEEN 0 AND 1000),
  attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 2),
  expected_cpu_ms     INTEGER NOT NULL CHECK (expected_cpu_ms >= 0),
  max_cpu_ms          INTEGER NOT NULL CHECK (max_cpu_ms >= expected_cpu_ms),   -- the budget; reserved against the daily quota while live
  out_seconds_est     REAL    NOT NULL CHECK (out_seconds_est > 0),
  mem_mb_est          INTEGER NOT NULL CHECK (mem_mb_est > 0),
  cpu_ms              INTEGER CHECK (cpu_ms IS NULL OR cpu_ms >= 0),            -- measured; 0 for a job that never ran
  pcm_sha256          TEXT CHECK (pcm_sha256 IS NULL OR length(pcm_sha256) = 64),
  error_code          TEXT,                              -- Go-validated stable UPPER_SNAKE
  error_text          TEXT CHECK (error_text IS NULL OR length(error_text) <= 500),   -- redacted, no paths
  cancel_requested_at TEXT,
  owner               TEXT,                              -- runner id while running (row lease, the tapes pattern)
  lease_expires_at    TEXT,
  created_at          TEXT NOT NULL,
  started_at          TEXT,
  finished_at         TEXT,
  CHECK ((state IN ('done','failed','cancelled')) = (finished_at IS NOT NULL)),
  CHECK ((state IN ('failed','cancelled')) = (error_code IS NOT NULL)),
  CHECK ((state = 'done') = (pcm_sha256 IS NOT NULL)),
  CHECK ((state IN ('done','failed','cancelled')) = (cpu_ms IS NOT NULL)),   -- every terminal row records cpu_ms: quota reservations are always released
  CHECK ((state = 'running') = (owner IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX transforms_state     ON transforms(state, created_at);
CREATE INDEX transforms_principal ON transforms(principal_id, created_at);
CREATE INDEX transforms_finished  ON transforms(finished_at) WHERE finished_at IS NOT NULL;
CREATE UNIQUE INDEX transforms_live_spec ON transforms(spec_hash) WHERE state IN ('queued','running','done');
CREATE TRIGGER transforms_no_delete BEFORE DELETE ON transforms
BEGIN SELECT RAISE(ABORT, 'transforms: no physical delete'); END;
CREATE TRIGGER transforms_frozen BEFORE UPDATE OF id, kind, recipe_id, spec_hash, title, rationale,
  principal_id, principal_kind, principal_name, expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, created_at ON transforms
BEGIN SELECT RAISE(ABORT, 'transforms: frozen column'); END;
CREATE TRIGGER transforms_final BEFORE UPDATE ON transforms
WHEN OLD.state IN ('done','failed','cancelled')
BEGIN SELECT RAISE(ABORT, 'transforms: terminal rows are immutable'); END;
CREATE TRIGGER transforms_state_machine BEFORE UPDATE OF state ON transforms
WHEN NOT (OLD.state = NEW.state
  OR (OLD.state = 'queued'  AND NEW.state IN ('running','cancelled','failed'))
  OR (OLD.state = 'running' AND NEW.state IN ('queued','done','failed','cancelled')))
BEGIN SELECT RAISE(ABORT, 'transforms: illegal state transition'); END;

CREATE TABLE transform_inputs (
  transform_id  TEXT NOT NULL REFERENCES transforms(id),
  ord           INTEGER NOT NULL CHECK (ord BETWEEN 0 AND 1),
  role          TEXT NOT NULL CHECK (role IN ('source','a','b','score_source')),   -- score_source: annotation only, never the ledger parent
  track_id      TEXT NOT NULL REFERENCES tracks(id),
  master_sha256 TEXT NOT NULL CHECK (length(master_sha256) = 64),
  PRIMARY KEY (transform_id, ord)
) WITHOUT ROWID;
CREATE INDEX transform_inputs_track ON transform_inputs(track_id);
CREATE TRIGGER transform_inputs_immutable BEFORE UPDATE ON transform_inputs
BEGIN SELECT RAISE(ABORT, 'transform_inputs: immutable'); END;
CREATE TRIGGER transform_inputs_no_delete BEFORE DELETE ON transform_inputs
BEGIN SELECT RAISE(ABORT, 'transform_inputs: no physical delete'); END;
CREATE TRIGGER transform_inputs_master BEFORE INSERT ON transform_inputs     -- the recorded hash is the track's master
WHEN NEW.master_sha256 <> COALESCE((SELECT master_sha256 FROM tracks WHERE id = NEW.track_id), '')
BEGIN SELECT RAISE(ABORT, 'transform_inputs: master hash mismatch'); END;
CREATE TRIGGER transform_inputs_lossless BEFORE INSERT ON transform_inputs
WHEN NEW.role <> 'score_source' AND COALESCE((SELECT lossy OR import_rule = 'reference' FROM tracks WHERE id = NEW.track_id), 1)
BEGIN SELECT RAISE(ABORT, 'transform_inputs: lossy or reference input'); END;

-- Provenance on tracks and ledger rows (set once, at insert).
ALTER TABLE tracks    ADD COLUMN transform_id TEXT REFERENCES transforms(id);
ALTER TABLE mutations ADD COLUMN transform_id TEXT REFERENCES transforms(id);
CREATE UNIQUE INDEX tracks_transform ON tracks(transform_id) WHERE transform_id IS NOT NULL;
CREATE TRIGGER tracks_transform_frozen BEFORE UPDATE OF transform_id ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;
-- A track has a transform id iff its recipe is a dsp recipe; the shape is fixed (D-T1-4).
CREATE TRIGGER tracks_transform_shape BEFORE INSERT ON tracks
WHEN (NEW.transform_id IS NOT NULL) <> COALESCE((SELECT engine = 'dsp' FROM recipes WHERE id = NEW.recipe_id), 0)
  OR (NEW.transform_id IS NOT NULL AND (NEW.id <> NEW.transform_id OR NEW.origin <> 'job' OR NEW.job_id IS NOT NULL
      OR NEW.lossy = 1 OR NEW.import_rule <> 'transform' OR NEW.bits_per_sample <> 24 OR NEW.channels <> 2))
BEGIN SELECT RAISE(ABORT, 'tracks: transform shape'); END;
-- The recipe of a transform: dsp engine, generated (never 'import'), versioned request, no style or lyrics text.
CREATE TRIGGER recipes_dsp_shape BEFORE INSERT ON recipes
WHEN NEW.engine = 'dsp' AND NOT (NEW.created_by <> 'import' AND NEW.sampling_json IS NULL AND NEW.lyrics = '' AND NEW.cot = '' AND NEW.style = ''
   AND COALESCE(json_extract(NEW.request_json, '$.v') = 1, 0)
   AND COALESCE(json_type(NEW.request_json, '$.kind') = 'text', 0)
   AND COALESCE(json_extract(NEW.request_json, '$.dsp_version') LIKE 'mm-dsp/%', 0))
BEGIN SELECT RAISE(ABORT, 'recipes: dsp shape'); END;
-- A draft's mother can never be a transform take (D-T1-12), like the reference trigger of 0005.
CREATE TRIGGER recipe_drafts_dsp_parent_ins BEFORE INSERT ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE transform_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: not renderable'); END;
CREATE TRIGGER recipe_drafts_dsp_parent_upd BEFORE UPDATE OF parent_track_id ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE transform_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: not renderable'); END;
