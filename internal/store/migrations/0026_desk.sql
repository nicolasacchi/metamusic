-- 0026_desk.sql: M5b DESK-S1 (C357.1). The Composer's Desk keeps immutable candidate scores (plans), the files that belong to a plan,
-- one verdict per rater and the content-addressed audition previews. A plan is never a Take. Additive and up-only; nothing here is
-- ever physically deleted, and a plan, its files and an audition never change after the insert (a verdict does).
CREATE TABLE desk_plans (
  id              TEXT PRIMARY KEY,                                   -- UUIDv7
  source          TEXT NOT NULL CHECK (source IN ('job','take','edit')),
  job_id          TEXT REFERENCES jobs(id),
  seed_index      INTEGER CHECK (seed_index IS NULL OR seed_index BETWEEN 0 AND 15),
  track_id        TEXT REFERENCES tracks(id),                         -- source take
  parent_plan_id  TEXT REFERENCES desk_plans(id),                     -- source edit
  base_track_id   TEXT REFERENCES tracks(id),
  base_draft_id   TEXT REFERENCES recipe_drafts(id),
  engine          TEXT NOT NULL CHECK (engine = 'yue2'),
  request_json    TEXT NOT NULL CHECK (json_valid(request_json)),     -- canonical yue2 request, no abc, this plan's seed and cot
  seed            INTEGER NOT NULL CHECK (seed >= 0),
  cot             TEXT NOT NULL CHECK (cot IN ('full','melody')),
  score_path      TEXT NOT NULL,
  score_sha256    TEXT NOT NULL CHECK (length(score_sha256) = 64),
  score_bytes     INTEGER NOT NULL CHECK (score_bytes BETWEEN 1 AND 65536),
  dialect         TEXT NOT NULL CHECK (dialect IN ('yue2','standard')),
  bars            INTEGER NOT NULL CHECK (bars >= 0),
  meter           TEXT NOT NULL,
  q               TEXT,
  seconds_per_bar REAL,
  aligned         INTEGER NOT NULL CHECK (aligned IN (0,1)),
  sections_json   TEXT NOT NULL CHECK (json_valid(sections_json)),
  expected_audio_s INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 600),
  abc_tokens      INTEGER,
  truncated       INTEGER CHECK (truncated IS NULL OR truncated IN (0,1)),
  edit_json       TEXT CHECK (edit_json IS NULL OR json_valid(edit_json)),   -- {ops, rationale}
  title           TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  principal_id    TEXT NOT NULL,
  principal_kind  TEXT NOT NULL CHECK (principal_kind IN ('human','device','agent','system')),
  principal_name  TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  CHECK ((source = 'job') = (job_id IS NOT NULL AND seed_index IS NOT NULL)),
  CHECK ((source = 'take') = (track_id IS NOT NULL)),
  CHECK ((source = 'edit') = (parent_plan_id IS NOT NULL AND edit_json IS NOT NULL))
);
CREATE UNIQUE INDEX desk_plans_job_seed ON desk_plans(job_id, seed_index) WHERE job_id IS NOT NULL;
CREATE UNIQUE INDEX desk_plans_take     ON desk_plans(track_id) WHERE source = 'take';
CREATE UNIQUE INDEX desk_plans_edit     ON desk_plans(parent_plan_id, score_sha256) WHERE source = 'edit';
CREATE INDEX desk_plans_base ON desk_plans(base_track_id, created_at);
CREATE TRIGGER desk_plans_immutable BEFORE UPDATE ON desk_plans
BEGIN SELECT RAISE(ABORT, 'desk_plans: immutable'); END;
CREATE TRIGGER desk_plans_no_delete BEFORE DELETE ON desk_plans
BEGIN SELECT RAISE(ABORT, 'desk_plans: no physical delete'); END;

CREATE TABLE desk_plan_files (
  plan_id    TEXT NOT NULL REFERENCES desk_plans(id),
  name       TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  bytes      INTEGER NOT NULL CHECK (bytes BETWEEN 1 AND 2097152),
  path       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (plan_id, name)
) WITHOUT ROWID;                                                      -- job and edit plans only (take plans use scores)
CREATE TRIGGER desk_plan_files_immutable BEFORE UPDATE ON desk_plan_files
BEGIN SELECT RAISE(ABORT, 'desk_plan_files: immutable'); END;
CREATE TRIGGER desk_plan_files_no_delete BEFORE DELETE ON desk_plan_files
BEGIN SELECT RAISE(ABORT, 'desk_plan_files: no physical delete'); END;

CREATE TABLE desk_plan_verdicts (
  plan_id    TEXT NOT NULL REFERENCES desk_plans(id),
  rater      TEXT NOT NULL CHECK (rater IN ('owner','critic')),
  verdict    TEXT CHECK (verdict IS NULL OR verdict IN ('liked','rejected')),
  note       TEXT CHECK (note IS NULL OR length(note) BETWEEN 1 AND 2000),
  decided_by TEXT NOT NULL,
  decided_at TEXT NOT NULL,
  PRIMARY KEY (plan_id, rater)
) WITHOUT ROWID;                                                      -- updatable; no_delete trigger only
CREATE TRIGGER desk_plan_verdicts_no_delete BEFORE DELETE ON desk_plan_verdicts
BEGIN SELECT RAISE(ABORT, 'desk_plan_verdicts: no physical delete'); END;

CREATE TABLE desk_auditions (
  id               TEXT PRIMARY KEY,
  plan_id          TEXT NOT NULL REFERENCES desk_plans(id),
  spec_hash        TEXT NOT NULL UNIQUE CHECK (length(spec_hash) = 64),
  soundfont        TEXT NOT NULL,
  soundfont_sha256 TEXT NOT NULL,
  synth_version    TEXT NOT NULL,
  chords           TEXT NOT NULL CHECK (chords IN ('none','block')),
  mp3_path         TEXT NOT NULL,
  mp3_sha256       TEXT NOT NULL UNIQUE,
  mp3_bytes        INTEGER NOT NULL,
  pcm_sha256       TEXT NOT NULL,
  seconds          REAL NOT NULL CHECK (seconds > 0),
  cpu_ms           INTEGER NOT NULL CHECK (cpu_ms >= 0),
  report_json      TEXT NOT NULL CHECK (json_valid(report_json)),
  principal_id     TEXT NOT NULL,
  created_at       TEXT NOT NULL
);
CREATE INDEX desk_auditions_plan      ON desk_auditions(plan_id, created_at);
CREATE INDEX desk_auditions_principal ON desk_auditions(principal_id, created_at);
CREATE TRIGGER desk_auditions_immutable BEFORE UPDATE ON desk_auditions
BEGIN SELECT RAISE(ABORT, 'desk_auditions: immutable'); END;
CREATE TRIGGER desk_auditions_no_delete BEFORE DELETE ON desk_auditions
BEGIN SELECT RAISE(ABORT, 'desk_auditions: no physical delete'); END;

-- the renders of a plan (get_plan include=renders, list_plans rendered): the jobs of operator render_plan that name it
CREATE INDEX jobs_desk_plan ON jobs(json_extract(mutation_json, '$.params.plan_id')) WHERE operator = 'render_plan';
