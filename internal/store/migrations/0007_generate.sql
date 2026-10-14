-- 0007_generate.sql — M3: caps, estimates, batches, approvals, jobs, attempts, events, uploads,
-- ledger, billing windows and buckets, worker meter, recipe keys, env fingerprints, RTF stats,
-- runner lease, provider state.

-- Per-token spend limits (NULL ⇒ the global defaults in spend_caps). Set only by set_spend_caps.
ALTER TABLE api_tokens ADD COLUMN daily_budget_micro_usd INTEGER
  CHECK (daily_budget_micro_usd IS NULL OR daily_budget_micro_usd >= 0);
ALTER TABLE api_tokens ADD COLUMN auto_approve_takes INTEGER
  CHECK (auto_approve_takes IS NULL OR auto_approve_takes BETWEEN 0 AND 100);
ALTER TABLE api_tokens ADD COLUMN auto_approve_micro_usd INTEGER
  CHECK (auto_approve_micro_usd IS NULL OR auto_approve_micro_usd >= 0);

-- Generation provenance on tracks and Operator Ledger rows (set once, at insert).
ALTER TABLE tracks    ADD COLUMN job_id TEXT REFERENCES jobs(id);
ALTER TABLE tracks    ADD COLUMN env_id TEXT REFERENCES env_fingerprints(env_id);
CREATE TRIGGER tracks_generation_frozen BEFORE UPDATE OF job_id, env_id ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;
CREATE INDEX tracks_job ON tracks(job_id);
ALTER TABLE mutations ADD COLUMN job_id TEXT REFERENCES jobs(id);
ALTER TABLE mutations ADD COLUMN recipe_patch_json TEXT
  CHECK (recipe_patch_json IS NULL OR json_valid(recipe_patch_json));
-- mutations_immutable (M1) already covers every column.

-- Job recipes carry unique request ids (D-M3-10); imported recipes may legitimately repeat one.
CREATE UNIQUE INDEX recipes_request_id_generated ON recipes(request_id) WHERE created_by <> 'import';

CREATE TABLE recipe_keys (                               -- D-M3-11; one row per recipe, computed in Go
  recipe_id    TEXT PRIMARY KEY REFERENCES recipes(id),
  content_hash TEXT NOT NULL,                            -- sha256 canonical request minus id and seed
  render_hash  TEXT NOT NULL                             -- sha256 canonical request minus id
) WITHOUT ROWID;
CREATE INDEX recipe_keys_content ON recipe_keys(content_hash);
CREATE INDEX recipe_keys_render  ON recipe_keys(render_hash);
CREATE TRIGGER recipe_keys_immutable BEFORE UPDATE ON recipe_keys
BEGIN SELECT RAISE(ABORT, 'recipe_keys: immutable'); END;
CREATE TRIGGER recipe_keys_no_delete BEFORE DELETE ON recipe_keys
BEGIN SELECT RAISE(ABORT, 'recipe_keys: no physical delete'); END;

CREATE TABLE spend_caps (                                -- singleton; D-M3-7 defaults
  id                     INTEGER PRIMARY KEY CHECK (id = 1),
  monthly_micro_usd      INTEGER NOT NULL CHECK (monthly_micro_usd >= 0),
  batch_micro_usd        INTEGER NOT NULL CHECK (batch_micro_usd >= 0),
  human_daily_micro_usd  INTEGER NOT NULL CHECK (human_daily_micro_usd >= 0),
  agent_daily_micro_usd  INTEGER NOT NULL CHECK (agent_daily_micro_usd >= 0),
  auto_approve_takes     INTEGER NOT NULL CHECK (auto_approve_takes BETWEEN 0 AND 100),
  auto_approve_micro_usd INTEGER NOT NULL CHECK (auto_approve_micro_usd >= 0),
  updated_by             TEXT NOT NULL,
  updated_at             TEXT NOT NULL
);
INSERT INTO spend_caps VALUES (1, 10000000, 5000000, 5000000, 1000000, 5, 500000,
                               'system:migration', '1970-01-01T00:00:00.000Z');
CREATE TRIGGER spend_caps_no_delete BEFORE DELETE ON spend_caps
BEGIN SELECT RAISE(ABORT, 'spend_caps: no physical delete'); END;

CREATE TABLE spend_cap_changes (                         -- append-only history of every cap change
  id          INTEGER PRIMARY KEY,
  at          TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  target      TEXT NOT NULL,                             -- 'global' | 'token:<id>'
  before_json TEXT NOT NULL CHECK (json_valid(before_json)),
  after_json  TEXT NOT NULL CHECK (json_valid(after_json))
);
CREATE TRIGGER spend_cap_changes_immutable BEFORE UPDATE ON spend_cap_changes
BEGIN SELECT RAISE(ABORT, 'spend_cap_changes: append-only'); END;
CREATE TRIGGER spend_cap_changes_no_delete BEFORE DELETE ON spend_cap_changes
BEGIN SELECT RAISE(ABORT, 'spend_cap_changes: append-only'); END;

CREATE TABLE estimates (
  id                  TEXT PRIMARY KEY,                  -- UUIDv7
  principal_id        TEXT NOT NULL,                     -- Principal.Key()
  principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('human','agent')),
  engine              TEXT NOT NULL,
  backend             TEXT NOT NULL,
  items_json          TEXT NOT NULL CHECK (json_valid(items_json)),          -- the expanded plan (C71.4)
  recipe_hashes       TEXT NOT NULL CHECK (json_valid(recipe_hashes) AND json_type(recipe_hashes) = 'array'),
  n_takes             INTEGER NOT NULL CHECK (n_takes BETWEEN 1 AND 100),    -- outer bound; Go enforces MaxTakes = 50 (D-M3-7)
  quote_json          TEXT NOT NULL CHECK (json_valid(quote_json)),          -- C72.1 inputs and per-take costs
  expected_micro_usd  INTEGER NOT NULL CHECK (expected_micro_usd >= 0),
  max_micro_usd       INTEGER NOT NULL CHECK (max_micro_usd >= expected_micro_usd),
  retry_allowance_micro_usd INTEGER NOT NULL CHECK (retry_allowance_micro_usd >= 0),
  ceiling_micro_usd   INTEGER NOT NULL CHECK (ceiling_micro_usd = max_micro_usd + retry_allowance_micro_usd), -- D-M3-29
  price_pph_micro_usd INTEGER NOT NULL CHECK (price_pph_micro_usd > 0),    -- list price × margin (C72.1)
  workers_max         INTEGER NOT NULL CHECK (workers_max >= 1),             -- frozen quote inputs (C77.5, C75.3)
  idle_s              INTEGER NOT NULL CHECK (idle_s >= 1),
  requires_approval   INTEGER NOT NULL CHECK (requires_approval IN (0,1)),
  approval_reasons    TEXT NOT NULL CHECK (json_valid(approval_reasons)),
  token_hash          TEXT NOT NULL UNIQUE,              -- hex HMAC-SHA256(ConfirmKey, token)
  created_at          TEXT NOT NULL,
  expires_at          TEXT NOT NULL,
  consumed_at         TEXT,
  consumed_batch_id   TEXT,
  CHECK ((consumed_at IS NULL) = (consumed_batch_id IS NULL))
);
CREATE INDEX estimates_principal ON estimates(principal_id, created_at);
CREATE TRIGGER estimates_no_delete BEFORE DELETE ON estimates
BEGIN SELECT RAISE(ABORT, 'estimates: no physical delete'); END;
CREATE TRIGGER estimates_frozen BEFORE UPDATE OF id, principal_id, principal_kind, engine, backend,
  items_json, recipe_hashes, n_takes, quote_json, expected_micro_usd, max_micro_usd,
  retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, workers_max, idle_s,
  requires_approval, approval_reasons, token_hash, created_at, expires_at ON estimates
BEGIN SELECT RAISE(ABORT, 'estimates: frozen column'); END;
CREATE TRIGGER estimates_single_use BEFORE UPDATE OF consumed_at, consumed_batch_id ON estimates
WHEN OLD.consumed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'estimates: already consumed'); END;

CREATE TABLE batches (
  id                  TEXT PRIMARY KEY,                  -- UUIDv7
  estimate_id         TEXT NOT NULL UNIQUE REFERENCES estimates(id),
  principal_id        TEXT NOT NULL,
  principal_kind      TEXT NOT NULL CHECK (principal_kind IN ('human','agent')),
  principal_name      TEXT NOT NULL,
  via                 TEXT NOT NULL,                     -- Go-validated: api | human_create
  engine              TEXT NOT NULL,
  backend             TEXT NOT NULL,
  n_takes             INTEGER NOT NULL CHECK (n_takes BETWEEN 1 AND 100),    -- outer bound; Go enforces 50
  rationale           TEXT CHECK (rationale IS NULL OR length(rationale) BETWEEN 1 AND 1000),
  expected_micro_usd  INTEGER NOT NULL CHECK (expected_micro_usd >= 0),
  max_micro_usd       INTEGER NOT NULL CHECK (max_micro_usd >= expected_micro_usd),
  retry_allowance_micro_usd INTEGER NOT NULL CHECK (retry_allowance_micro_usd >= 0),
  ceiling_micro_usd   INTEGER NOT NULL CHECK (ceiling_micro_usd = max_micro_usd + retry_allowance_micro_usd), -- D-M3-29
  price_pph_micro_usd INTEGER NOT NULL CHECK (price_pph_micro_usd > 0),
  attempt_overhead_s  INTEGER NOT NULL CHECK (attempt_overhead_s >= 0),     -- ColdStartS + IdleS at estimate time
  kill_slack_s        INTEGER NOT NULL CHECK (kill_slack_s >= 0),
  workers_max         INTEGER NOT NULL CHECK (workers_max >= 1),             -- frozen; in-flight ≤ min(live, this) (D-M3-14)
  idle_s              INTEGER NOT NULL CHECK (idle_s >= 1),                  -- frozen; claims reserve max(live, this) (C75.3)
  state               TEXT NOT NULL CHECK (state IN ('awaiting_confirmation','active','finished','rejected','expired')),
  created_at          TEXT NOT NULL,
  finished_at         TEXT,
  reconciled_at       TEXT,                              -- residual released (C76.5)
  CHECK ((state IN ('finished','rejected','expired')) = (finished_at IS NOT NULL)),
  CHECK (reconciled_at IS NULL OR finished_at IS NOT NULL)
);
CREATE INDEX batches_state   ON batches(state, created_at);
CREATE INDEX batches_open    ON batches(created_at) WHERE reconciled_at IS NULL;
CREATE INDEX batches_principal ON batches(principal_id, created_at);
CREATE TRIGGER batches_no_delete BEFORE DELETE ON batches
BEGIN SELECT RAISE(ABORT, 'batches: no physical delete'); END;
CREATE TRIGGER batches_frozen BEFORE UPDATE OF id, estimate_id, principal_id, principal_kind,
  principal_name, via, engine, backend, n_takes, rationale, expected_micro_usd, max_micro_usd,
  retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, attempt_overhead_s, kill_slack_s,
  workers_max, idle_s, created_at ON batches
BEGIN SELECT RAISE(ABORT, 'batches: frozen column'); END;
CREATE TRIGGER batches_final BEFORE UPDATE OF state, finished_at ON batches
WHEN OLD.state IN ('finished','rejected','expired')
BEGIN SELECT RAISE(ABORT, 'batches: finished batches are immutable'); END;
CREATE TRIGGER batches_reconciled_once BEFORE UPDATE OF reconciled_at ON batches
WHEN OLD.reconciled_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'batches: already reconciled'); END;

CREATE TABLE approvals (
  batch_id          TEXT PRIMARY KEY REFERENCES batches(id),
  state             TEXT NOT NULL CHECK (state IN ('pending','approved','rejected','expired','auto')),
  reasons           TEXT NOT NULL CHECK (json_valid(reasons)),   -- why approval was (not) needed (C72.6)
  via               TEXT,                                -- Go-validated: auto | human_create | web | expiry
  decided_by        TEXT,                                -- Principal.Key(); 'system:reconciler' for expiry
  decided_at        TEXT,
  reason            TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
  requested_count   INTEGER NOT NULL DEFAULT 0 CHECK (requested_count >= 0),
  last_requested_at TEXT,
  created_at        TEXT NOT NULL,
  expires_at        TEXT NOT NULL,
  CHECK ((state = 'pending') = (decided_at IS NULL)),
  CHECK ((decided_at IS NULL) = (via IS NULL))
);
CREATE INDEX approvals_pending ON approvals(expires_at) WHERE state = 'pending';
CREATE TRIGGER approvals_no_delete BEFORE DELETE ON approvals
BEGIN SELECT RAISE(ABORT, 'approvals: no physical delete'); END;
CREATE TRIGGER approvals_frozen BEFORE UPDATE OF batch_id, reasons, created_at, expires_at ON approvals
BEGIN SELECT RAISE(ABORT, 'approvals: frozen column'); END;
CREATE TRIGGER approvals_decided BEFORE UPDATE OF state, via, decided_by, decided_at, reason ON approvals
WHEN OLD.state <> 'pending'
BEGIN SELECT RAISE(ABORT, 'approvals: decided approvals are immutable'); END;

CREATE TABLE job_transitions (                           -- D-M3-3; equals jobs.Transitions (Go)
  from_state TEXT NOT NULL,
  to_state   TEXT NOT NULL,
  PRIMARY KEY (from_state, to_state)
) WITHOUT ROWID;
INSERT INTO job_transitions(from_state, to_state) VALUES
  ('awaiting_confirmation','queued'), ('awaiting_confirmation','cancelled'), ('awaiting_confirmation','expired'),
  ('queued','submitting'), ('queued','starting'), ('queued','cancelled'), ('queued','cancelling'),
  ('queued','failed'), ('queued','transferring'),
  ('submitting','starting'), ('submitting','queued'), ('submitting','failed'), ('submitting','cancelling'),
  ('submitting','transferring'),
  ('starting','generating'), ('starting','queued'), ('starting','transferring'), ('starting','cancelling'),
  ('starting','failing'), ('starting','failed'),
  ('generating','transferring'), ('generating','cancelling'), ('generating','failing'), ('generating','failed'),
  ('transferring','ingesting'), ('transferring','failed'), ('transferring','failing'), ('transferring','cancelling'),
  ('ingesting','completed'), ('ingesting','failed'),
  ('cancelling','cancelled'), ('cancelling','transferring'),
  ('failing','failed'), ('failing','transferring');
CREATE TRIGGER job_transitions_immutable BEFORE UPDATE ON job_transitions
BEGIN SELECT RAISE(ABORT, 'job_transitions: immutable'); END;
CREATE TRIGGER job_transitions_no_delete BEFORE DELETE ON job_transitions
BEGIN SELECT RAISE(ABORT, 'job_transitions: immutable'); END;
CREATE TRIGGER job_transitions_no_insert BEFORE INSERT ON job_transitions   -- created after the 34 rows above
BEGIN SELECT RAISE(ABORT, 'job_transitions: immutable'); END;

CREATE TABLE jobs (
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
  expected_audio_s    INTEGER NOT NULL CHECK (expected_audio_s BETWEEN 1 AND 360),
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
  updated_at          TEXT NOT NULL,
  UNIQUE (batch_id, take_index),
  CHECK ((state = 'completed') = (outcome IS NOT NULL)),
  CHECK (state <> 'completed' OR track_id IS NOT NULL),
  CHECK ((state IN ('completed','failed','cancelled','expired')) = (finished_at IS NOT NULL)),
  CHECK ((cancel_requested_at IS NULL) = (cancel_requested_by IS NULL)),
  CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL)),
  CHECK (state NOT IN ('awaiting_confirmation','queued','completed','failed','cancelled','expired')
         OR lease_owner IS NULL)                                 -- unleased states never hold a job lease (C69)
);
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

CREATE TABLE job_attempts (                              -- D-M3-2/D-M3-4
  id                  TEXT PRIMARY KEY,                  -- UUIDv7
  job_id              TEXT NOT NULL REFERENCES jobs(id),
  n                   INTEGER NOT NULL CHECK (n >= 1),
  backend             TEXT NOT NULL,                     -- = jobs.backend (D-M3-14 counts per backend)
  state               TEXT NOT NULL CHECK (state IN ('pending','submitted','unknown','rejected','done')),
  ref                 TEXT,                              -- provider job id; never returned by the API
  submitted_at        TEXT NOT NULL,                     -- when the row was written (before /run)
  window_ends_at      TEXT NOT NULL,                     -- = upload exp (C74.1): the attempt cannot spend after it
  price_pph_micro_usd INTEGER NOT NULL CHECK (price_pph_micro_usd > 0),
  image_digest        TEXT,                              -- from the worker that ran it (D-M3-26)
  worker_key          TEXT,                              -- hex HMAC of the status's workerId (C76.6); never the raw id
  remote_status       TEXT,                              -- last observed (C73.2)
  in_progress_seen    INTEGER NOT NULL DEFAULT 0 CHECK (in_progress_seen IN (0,1)),  -- observation only; never classifies (C73.2)
  requeue_seen        INTEGER NOT NULL DEFAULT 0 CHECK (requeue_seen IN (0,1)),      -- Running, then Queued again
  last_queued_at      TEXT,                              -- last poll that answered Queued (C73.2 404 rule)
  announced_at        TEXT,                              -- start handshake received (D-M3-28)
  announced_ref       TEXT,                              -- provider id from the start body; promoted to ref by the runner
  execution_ms        INTEGER CHECK (execution_ms IS NULL OR execution_ms >= 0),
  terminal_basis      TEXT CHECK (terminal_basis IS NULL OR terminal_basis IN ('provider','time_bound','not_accepted')),
  terminal_at         TEXT,
  settled             INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0,1)),
  created_at          TEXT NOT NULL,
  UNIQUE (job_id, n),
  CHECK ((state = 'submitted') <= (ref IS NOT NULL)),
  CHECK ((state IN ('pending','rejected')) <= (ref IS NULL)),     -- 'unknown' may carry a ref (status lost, C73.2)
  CHECK ((state IN ('done','rejected')) = (terminal_basis IS NOT NULL)),
  CHECK ((terminal_basis IS NULL) = (terminal_at IS NULL))
);
CREATE INDEX job_attempts_live ON job_attempts(backend, state) WHERE state IN ('pending','submitted','unknown');
CREATE TRIGGER job_attempts_no_delete BEFORE DELETE ON job_attempts
BEGIN SELECT RAISE(ABORT, 'job_attempts: no physical delete'); END;
CREATE TRIGGER job_attempts_frozen BEFORE UPDATE OF id, job_id, n, backend, submitted_at, window_ends_at,
  price_pph_micro_usd, created_at ON job_attempts
BEGIN SELECT RAISE(ABORT, 'job_attempts: frozen column'); END;
CREATE TRIGGER job_attempts_ref_once BEFORE UPDATE OF ref ON job_attempts
WHEN OLD.ref IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'job_attempts: ref is set once'); END;
CREATE TRIGGER job_attempts_transition BEFORE UPDATE OF state ON job_attempts   -- legal attempt moves
WHEN NEW.state <> OLD.state AND NOT (
     (OLD.state = 'pending'   AND NEW.state IN ('submitted','unknown','rejected'))
  OR (OLD.state = 'unknown'   AND NEW.state = 'submitted' AND OLD.ref IS NULL)  -- late ref via start (D-M3-28)
  OR (OLD.state = 'unknown'   AND NEW.state = 'done')
  OR (OLD.state = 'submitted' AND NEW.state IN ('unknown','done')))             -- 'unknown': status lost (404)
BEGIN SELECT RAISE(ABORT, 'job_attempts: illegal transition'); END;
CREATE TRIGGER job_attempts_announced_once BEFORE UPDATE OF announced_ref ON job_attempts
WHEN OLD.announced_ref IS NOT NULL AND NEW.announced_ref IS NOT OLD.announced_ref
BEGIN SELECT RAISE(ABORT, 'job_attempts: announced_ref is set once'); END;
CREATE TRIGGER job_attempts_final BEFORE UPDATE OF state, remote_status, in_progress_seen, requeue_seen,
  last_queued_at, announced_at, announced_ref, execution_ms, terminal_basis, terminal_at, image_digest,
  worker_key ON job_attempts
WHEN OLD.state IN ('done','rejected')
BEGIN SELECT RAISE(ABORT, 'job_attempts: terminal attempts are immutable'); END;
CREATE TRIGGER job_attempts_settled_once BEFORE UPDATE OF settled ON job_attempts
WHEN OLD.settled = 1
BEGIN SELECT RAISE(ABORT, 'job_attempts: already settled'); END;

CREATE TABLE job_events (                                -- append-only; never URLs, refs or keys (C75.7)
  id         INTEGER PRIMARY KEY,
  job_id     TEXT NOT NULL REFERENCES jobs(id),
  attempt_id TEXT REFERENCES job_attempts(id),
  at         TEXT NOT NULL,
  from_state TEXT,
  to_state   TEXT,
  event      TEXT NOT NULL,                              -- Go-validated (C75.7)
  code       TEXT,
  detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json))  -- redacted, ≤ 2 KiB
);
CREATE INDEX job_events_job ON job_events(job_id, id);
CREATE TRIGGER job_events_immutable BEFORE UPDATE ON job_events
BEGIN SELECT RAISE(ABORT, 'job_events: append-only'); END;
CREATE TRIGGER job_events_no_delete BEFORE DELETE ON job_events
BEGIN SELECT RAISE(ABORT, 'job_events: append-only'); END;

CREATE TABLE uploads (                                   -- design §3.4; URLs are never persisted
  id          TEXT PRIMARY KEY,                          -- upload_id (UUIDv7)
  job_id      TEXT NOT NULL REFERENCES jobs(id),
  attempt_id  TEXT NOT NULL REFERENCES job_attempts(id),
  artifact    TEXT NOT NULL,                             -- Go-validated allowlist (C73.4)
  max_bytes   INTEGER NOT NULL CHECK (max_bytes > 0),
  exp         INTEGER NOT NULL,                          -- unix seconds
  state       TEXT NOT NULL CHECK (state IN ('open','received','consumed','closed')),
  sha256      TEXT,
  bytes       INTEGER CHECK (bytes IS NULL OR bytes >= 0),
  received_at TEXT,
  created_at  TEXT NOT NULL,
  UNIQUE (attempt_id, artifact),
  CHECK ((state IN ('received','consumed')) = (sha256 IS NOT NULL)),
  CHECK ((sha256 IS NULL) = (bytes IS NULL)),
  CHECK ((sha256 IS NULL) = (received_at IS NULL))
);
CREATE INDEX uploads_job ON uploads(job_id, attempt_id);
CREATE TRIGGER uploads_no_delete BEFORE DELETE ON uploads
BEGIN SELECT RAISE(ABORT, 'uploads: no physical delete'); END;
CREATE TRIGGER uploads_frozen BEFORE UPDATE OF id, job_id, attempt_id, artifact, max_bytes, exp, created_at ON uploads
BEGIN SELECT RAISE(ABORT, 'uploads: frozen column'); END;
CREATE TRIGGER uploads_received_once BEFORE UPDATE OF sha256, bytes, received_at ON uploads
WHEN OLD.sha256 IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'uploads: received uploads are immutable'); END;

CREATE TABLE ledger (                                    -- append-only; integer micro-USD (C72.5)
  id           INTEGER PRIMARY KEY,
  at           TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('reserve','settle','residual','release','bill')),
  micro_usd    INTEGER NOT NULL,
  batch_id     TEXT REFERENCES batches(id),
  job_id       TEXT REFERENCES jobs(id),
  attempt_id   TEXT REFERENCES job_attempts(id),
  window_start TEXT,                                     -- 'bill' rows: start of the reconciled window
  principal_id TEXT NOT NULL,                            -- batch owner's Key(); 'system:billing' for 'bill'
  reason       TEXT NOT NULL,                            -- Go-validated (C72.5)
  CHECK (kind = 'bill' OR micro_usd >= 0),
  CHECK (kind <> 'reserve' OR micro_usd > 0),
  CHECK ((kind = 'bill') = (window_start IS NOT NULL)),
  CHECK ((kind = 'bill') = (batch_id IS NULL))
);
CREATE INDEX ledger_batch     ON ledger(batch_id, kind);
CREATE INDEX ledger_job       ON ledger(job_id);
CREATE INDEX ledger_at        ON ledger(at);
CREATE INDEX ledger_principal ON ledger(principal_id, at);
CREATE TRIGGER ledger_immutable BEFORE UPDATE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger: append-only'); END;
CREATE TRIGGER ledger_no_delete BEFORE DELETE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger: append-only'); END;

CREATE TABLE billing_windows (                           -- one row per reconciled quiet window (C76.5)
  window_start           TEXT PRIMARY KEY,
  window_end             TEXT NOT NULL,                  -- never in a later calendar month than window_start (C76.5)
  granularity            TEXT NOT NULL CHECK (granularity = 'hour'),   -- always queried with bucketSize=hour
  buckets                INTEGER NOT NULL CHECK (buckets >= 1),
  billed_micro_usd       INTEGER NOT NULL CHECK (billed_micro_usd >= 0),        -- Σ totalAmount (real spend, incl. disk)
  billed_compute_micro_usd INTEGER NOT NULL CHECK (billed_compute_micro_usd >= 0), -- Σ gpuAmount + cpuAmount (trips use this)
  provisional_micro_usd  INTEGER NOT NULL CHECK (provisional_micro_usd >= 0),
  reserved_micro_usd     INTEGER NOT NULL CHECK (reserved_micro_usd >= 0),
  batches                INTEGER NOT NULL CHECK (batches >= 0),
  verdict                TEXT NOT NULL CHECK (verdict IN ('ok','billed_exceeds_reserved','spend_without_jobs')),
  fetched_at             TEXT NOT NULL,
  CHECK (window_end > window_start)
) WITHOUT ROWID;
CREATE TRIGGER billing_windows_immutable BEFORE UPDATE ON billing_windows
BEGIN SELECT RAISE(ABORT, 'billing_windows: immutable'); END;
CREATE TRIGGER billing_windows_no_delete BEFORE DELETE ON billing_windows
BEGIN SELECT RAISE(ABORT, 'billing_windows: immutable'); END;

CREATE TABLE billing_buckets (                           -- every fetched observation of an hourly bucket (C76.5)
  bucket_start      TEXT NOT NULL,
  fetched_at        TEXT NOT NULL,
  total_micro_usd   INTEGER NOT NULL CHECK (total_micro_usd >= 0),     -- totalAmount, ParseUSD of the literal (round up)
  compute_micro_usd INTEGER NOT NULL CHECK (compute_micro_usd >= 0),   -- gpuAmount + cpuAmount
  disk_micro_usd    INTEGER NOT NULL CHECK (disk_micro_usd >= 0),      -- recorded; never tripped on
  PRIMARY KEY (bucket_start, fetched_at)                 -- window membership follows billing_windows ranges
) WITHOUT ROWID;
CREATE TRIGGER billing_buckets_immutable BEFORE UPDATE ON billing_buckets
BEGIN SELECT RAISE(ABORT, 'billing_buckets: append-only'); END;
CREATE TRIGGER billing_buckets_no_delete BEFORE DELETE ON billing_buckets
BEGIN SELECT RAISE(ABORT, 'billing_buckets: append-only'); END;

CREATE TABLE worker_meter (                              -- C76.6; one row per observed worker lifetime
  worker_key    TEXT NOT NULL,                           -- hex HMAC-SHA256(MeterKey, worker id); never the raw id
  started_at    TEXT NOT NULL,                           -- the worker record's startedAt
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  uptime_s      INTEGER NOT NULL CHECK (uptime_s >= 0),  -- max uptimeSeconds observed (NULL uptime ⇒ not inserted)
  status_last   TEXT NOT NULL,
  image         TEXT,                                    -- the record's image (D-M3-26)
  PRIMARY KEY (worker_key, started_at)
) WITHOUT ROWID;
CREATE TRIGGER worker_meter_no_delete BEFORE DELETE ON worker_meter
BEGIN SELECT RAISE(ABORT, 'worker_meter: no physical delete'); END;
CREATE TRIGGER worker_meter_monotonic BEFORE UPDATE OF uptime_s ON worker_meter
WHEN NEW.uptime_s < OLD.uptime_s
BEGIN SELECT RAISE(ABORT, 'worker_meter: uptime never decreases'); END;

CREATE TABLE env_fingerprints (                          -- D-M3-26
  env_id        TEXT PRIMARY KEY,
  engine        TEXT NOT NULL,
  pkg_version   TEXT NOT NULL,
  code_commit   TEXT NOT NULL,
  weights_json  TEXT NOT NULL CHECK (json_valid(weights_json)),
  decoder       TEXT NOT NULL,
  backend       TEXT NOT NULL,
  gpu_class     TEXT NOT NULL,
  image_digest  TEXT NOT NULL,
  detail_json   TEXT NOT NULL CHECK (json_valid(detail_json)),   -- first seen: gpu_name, cuda, torch, worker_build, python
  first_seen_at TEXT NOT NULL
);
CREATE TRIGGER env_fingerprints_immutable BEFORE UPDATE ON env_fingerprints
BEGIN SELECT RAISE(ABORT, 'env_fingerprints: immutable'); END;
CREATE TRIGGER env_fingerprints_no_delete BEFORE DELETE ON env_fingerprints
BEGIN SELECT RAISE(ABORT, 'env_fingerprints: no physical delete'); END;

CREATE TABLE rtf_stats (                                 -- real-time factor × 1000 per GPU class (C72.1)
  gpu_class  TEXT PRIMARY KEY,
  rtf_milli  INTEGER NOT NULL CHECK (rtf_milli BETWEEN 50 AND 5000),
  samples    INTEGER NOT NULL CHECK (samples >= 0),
  updated_at TEXT NOT NULL
) WITHOUT ROWID;
INSERT INTO rtf_stats VALUES ('A5000', 440, 0, '1970-01-01T00:00:00.000Z'),
                             ('RTX 3090', 440, 0, '1970-01-01T00:00:00.000Z'),
                             ('RTX 4090', 330, 0, '1970-01-01T00:00:00.000Z');
CREATE TRIGGER rtf_stats_no_delete BEFORE DELETE ON rtf_stats
BEGIN SELECT RAISE(ABORT, 'rtf_stats: no physical delete'); END;

CREATE TABLE runner_lease (                              -- design §3.2 singleton
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  owner        TEXT NOT NULL,                            -- random instance id (never a hostname)
  acquired_at  TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,
  expires_at   TEXT NOT NULL
);
CREATE TRIGGER runner_lease_no_delete BEFORE DELETE ON runner_lease
BEGIN SELECT RAISE(ABORT, 'runner_lease: no physical delete'); END;

CREATE TABLE provider_state (                            -- one row per backend name (D-M3-8)
  backend    TEXT PRIMARY KEY,
  armed      INTEGER NOT NULL CHECK (armed IN (0,1)),
  changed_by TEXT NOT NULL,                              -- 'system:default' on first start
  changed_at TEXT NOT NULL,
  reason     TEXT CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
  quote_json TEXT CHECK (quote_json IS NULL OR json_valid(quote_json)),   -- cached provider.Quote (C77.5); never secrets
  quote_at   TEXT,                                       -- when quote_json was observed
  restored_at TEXT,                                      -- set by restore (C76.7): re-arm needs a window_end ≥ this
  CHECK ((quote_json IS NULL) = (quote_at IS NULL))
) WITHOUT ROWID;
CREATE TRIGGER provider_state_no_delete BEFORE DELETE ON provider_state
BEGIN SELECT RAISE(ABORT, 'provider_state: no physical delete'); END;

CREATE TABLE provider_trips (                            -- append-only history; open trip = reset_at IS NULL
  id           TEXT PRIMARY KEY,                         -- UUIDv7
  backend      TEXT NOT NULL,
  tripped_at   TEXT NOT NULL,
  reason       TEXT NOT NULL,                            -- Go-validated (C76.3)
  details_json TEXT NOT NULL CHECK (json_valid(details_json)),   -- redacted
  actions_json TEXT NOT NULL CHECK (json_valid(actions_json)),   -- what the reconciler did
  reset_at     TEXT,
  reset_by     TEXT,
  reset_reason TEXT CHECK (reset_reason IS NULL OR length(reset_reason) BETWEEN 1 AND 500),
  CHECK ((reset_at IS NULL) = (reset_by IS NULL)),
  CHECK ((reset_at IS NULL) = (reset_reason IS NULL))
);
CREATE UNIQUE INDEX provider_trips_one_open ON provider_trips(backend) WHERE reset_at IS NULL;
CREATE TRIGGER provider_trips_no_delete BEFORE DELETE ON provider_trips
BEGIN SELECT RAISE(ABORT, 'provider_trips: no physical delete'); END;
CREATE TRIGGER provider_trips_frozen BEFORE UPDATE OF id, backend, tripped_at, reason, details_json, actions_json ON provider_trips
BEGIN SELECT RAISE(ABORT, 'provider_trips: frozen column'); END;
CREATE TRIGGER provider_trips_reset_once BEFORE UPDATE OF reset_at, reset_by, reset_reason ON provider_trips
WHEN OLD.reset_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'provider_trips: already reset'); END;
