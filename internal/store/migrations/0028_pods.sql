-- 0028_pods.sql: BULK-P3 (C394). The tables of the bulk pod backend: pod states and their legal moves, pods, the assignment of a
-- take to a pod, an append-only event log and the append-only safety log (probe and canary verdicts). Additive and up-only:
-- nothing in an existing table changes, and nothing reads these tables unless the pod backend is on. No physical deletes.
-- Beyond C394: pods.overhead_reserved_at (set once) lets the lifecycle lane reserve the pod overhead exactly once per pod
-- (it sets the column and writes the ledger reserve in one transaction; a second set aborts).

CREATE TABLE pod_transitions (from_state TEXT NOT NULL, to_state TEXT NOT NULL, PRIMARY KEY (from_state, to_state)) WITHOUT ROWID;
INSERT INTO pod_transitions VALUES
  ('intent','creating'),('intent','terminated'),
  ('creating','booting'),('creating','terminated'),('creating','orphaned'),
  ('booting','ready'),('booting','terminating'),('booting','orphaned'),
  ('ready','busy'),('ready','booting'),('ready','terminating'),('ready','orphaned'),
  ('busy','ready'),('busy','booting'),('busy','terminating'),('busy','orphaned'),
  ('orphaned','terminating'),('orphaned','terminated'),
  ('terminating','terminated');
CREATE TRIGGER pod_transitions_no_insert BEFORE INSERT ON pod_transitions
BEGIN SELECT RAISE(ABORT, 'pod_transitions: fixed by the migration'); END;
CREATE TRIGGER pod_transitions_no_update BEFORE UPDATE ON pod_transitions
BEGIN SELECT RAISE(ABORT, 'pod_transitions: fixed by the migration'); END;
CREATE TRIGGER pod_transitions_no_delete BEFORE DELETE ON pod_transitions
BEGIN SELECT RAISE(ABORT, 'pod_transitions: fixed by the migration'); END;

CREATE TABLE pods (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE CHECK (name GLOB 'mm-*'),
  kind TEXT NOT NULL CHECK (kind IN ('batch','canary')),
  batch_id TEXT NOT NULL REFERENCES batches(id),
  n INTEGER NOT NULL CHECK (n BETWEEN 1 AND 2), gpu_try INTEGER NOT NULL CHECK (gpu_try BETWEEN 1 AND 4), gpu_type TEXT NOT NULL,
  state TEXT NOT NULL, provider_ref TEXT UNIQUE,                    -- the RunPod pod id; never returned, logged or in events
  cloud TEXT NOT NULL CHECK (cloud = 'SECURE'), image_digest TEXT NOT NULL,
  price_pph_micro_usd INTEGER NOT NULL CHECK (price_pph_micro_usd > 0), reported_pph_micro_usd INTEGER,
  overhead_reserved_micro_usd INTEGER NOT NULL CHECK (overhead_reserved_micro_usd >= 0),
  overhead_reserved_at TEXT,                                        -- set once, in the transaction of the ledger reserve
  deadline_at TEXT NOT NULL, poll_exp INTEGER NOT NULL, restarts INTEGER NOT NULL DEFAULT 0 CHECK (restarts BETWEEN 0 AND 2),
  create_sent_at TEXT, running_seen_at TEXT, booted_at TEXT, first_poll_at TEXT, last_poll_at TEXT, last_reported TEXT,
  terminate_sent_at TEXT, gone_seen_at TEXT, terminated_at TEXT, lifetime_s INTEGER CHECK (lifetime_s IS NULL OR lifetime_s >= 0),
  settled INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0,1)), error_code TEXT,
  boot_json TEXT CHECK (boot_json IS NULL OR json_valid(boot_json)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (batch_id, n, gpu_try),
  CHECK ((state = 'terminated') = (terminated_at IS NOT NULL)), CHECK (settled = 0 OR state = 'terminated'),
  CHECK ((first_poll_at IS NULL) = (last_poll_at IS NULL)), CHECK (kind = 'batch' OR n = 1)
);
CREATE INDEX pods_live ON pods(state) WHERE state <> 'terminated';
CREATE INDEX pods_batch ON pods(batch_id);

CREATE TRIGGER pods_initial_state BEFORE INSERT ON pods WHEN NEW.state IS NOT 'intent'
BEGIN SELECT RAISE(ABORT, 'pods: a pod starts as intent'); END;
CREATE TRIGGER pods_no_delete BEFORE DELETE ON pods
BEGIN SELECT RAISE(ABORT, 'pods: no physical delete'); END;
CREATE TRIGGER pods_frozen BEFORE UPDATE OF id, name, kind, batch_id, n, gpu_try, gpu_type, cloud, image_digest,
  price_pph_micro_usd, overhead_reserved_micro_usd, deadline_at, poll_exp, created_at ON pods
WHEN NEW.id IS NOT OLD.id OR NEW.name IS NOT OLD.name OR NEW.kind IS NOT OLD.kind OR NEW.batch_id IS NOT OLD.batch_id
  OR NEW.n IS NOT OLD.n OR NEW.gpu_try IS NOT OLD.gpu_try OR NEW.gpu_type IS NOT OLD.gpu_type OR NEW.cloud IS NOT OLD.cloud
  OR NEW.image_digest IS NOT OLD.image_digest OR NEW.price_pph_micro_usd IS NOT OLD.price_pph_micro_usd
  OR NEW.overhead_reserved_micro_usd IS NOT OLD.overhead_reserved_micro_usd OR NEW.deadline_at IS NOT OLD.deadline_at
  OR NEW.poll_exp IS NOT OLD.poll_exp OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'pods: frozen column'); END;
CREATE TRIGGER pods_provider_ref_once BEFORE UPDATE OF provider_ref ON pods WHEN OLD.provider_ref IS NOT NULL AND NEW.provider_ref IS NOT OLD.provider_ref
BEGIN SELECT RAISE(ABORT, 'pods: provider_ref is set once'); END;
CREATE TRIGGER pods_first_poll_once BEFORE UPDATE OF first_poll_at ON pods WHEN OLD.first_poll_at IS NOT NULL AND NEW.first_poll_at IS NOT OLD.first_poll_at
BEGIN SELECT RAISE(ABORT, 'pods: first_poll_at is set once'); END;
CREATE TRIGGER pods_running_seen_once BEFORE UPDATE OF running_seen_at ON pods WHEN OLD.running_seen_at IS NOT NULL AND NEW.running_seen_at IS NOT OLD.running_seen_at
BEGIN SELECT RAISE(ABORT, 'pods: running_seen_at is set once'); END;
CREATE TRIGGER pods_gone_seen_once BEFORE UPDATE OF gone_seen_at ON pods WHEN OLD.gone_seen_at IS NOT NULL AND NEW.gone_seen_at IS NOT OLD.gone_seen_at
BEGIN SELECT RAISE(ABORT, 'pods: gone_seen_at is set once'); END;
CREATE TRIGGER pods_overhead_reserved_once BEFORE UPDATE OF overhead_reserved_at ON pods WHEN OLD.overhead_reserved_at IS NOT NULL AND NEW.overhead_reserved_at IS NOT OLD.overhead_reserved_at
BEGIN SELECT RAISE(ABORT, 'pods: overhead_reserved_at is set once'); END;
CREATE TRIGGER pods_settled_once BEFORE UPDATE OF settled ON pods WHEN OLD.settled = 1 AND NEW.settled IS NOT 1
BEGIN SELECT RAISE(ABORT, 'pods: settled is set once'); END;
CREATE TRIGGER pods_transition_exists BEFORE UPDATE OF state ON pods
WHEN NEW.state IS NOT OLD.state AND NOT EXISTS (SELECT 1 FROM pod_transitions WHERE from_state = OLD.state AND to_state = NEW.state)
BEGIN SELECT RAISE(ABORT, 'pods: illegal state transition'); END;

CREATE TABLE pod_assignments (
  attempt_id TEXT PRIMARY KEY REFERENCES job_attempts(id), batch_id TEXT NOT NULL REFERENCES batches(id),
  pod_id TEXT REFERENCES pods(id),                                 -- set at hand-out
  queued_at TEXT NOT NULL, handed_at TEXT, done_at TEXT,
  status TEXT CHECK (status IS NULL OR status IN ('ok','error','lost','not_handed','cancelled')),
  execution_ms INTEGER CHECK (execution_ms IS NULL OR execution_ms >= 0), agent_execution_ms INTEGER CHECK (agent_execution_ms IS NULL OR agent_execution_ms >= 0), error_code TEXT,
  CHECK ((done_at IS NULL) = (status IS NULL)), CHECK ((handed_at IS NULL) = (pod_id IS NULL)),
  CHECK (status IS NULL OR status NOT IN ('ok','error','lost') OR pod_id IS NOT NULL),
  CHECK (status IS NOT 'not_handed' OR pod_id IS NULL)
) WITHOUT ROWID;
CREATE INDEX pod_assignments_pod ON pod_assignments(pod_id) WHERE pod_id IS NOT NULL;
CREATE INDEX pod_assignments_batch ON pod_assignments(batch_id);

CREATE TRIGGER pod_assignments_batch_matches BEFORE INSERT ON pod_assignments
WHEN NEW.batch_id IS NOT (SELECT j.batch_id FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE a.id = NEW.attempt_id)
BEGIN SELECT RAISE(ABORT, 'pod_assignments: batch_id must be the attempt''s batch'); END;
CREATE TRIGGER pod_assignments_new_row_queued BEFORE INSERT ON pod_assignments
WHEN NEW.pod_id IS NOT NULL OR NEW.handed_at IS NOT NULL OR NEW.done_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'pod_assignments: a new row is queued, not handed or done'); END;
CREATE TRIGGER pod_assignments_no_delete BEFORE DELETE ON pod_assignments
BEGIN SELECT RAISE(ABORT, 'pod_assignments: no physical delete'); END;
CREATE TRIGGER pod_assignments_frozen BEFORE UPDATE OF attempt_id, batch_id, queued_at ON pod_assignments
WHEN NEW.attempt_id IS NOT OLD.attempt_id OR NEW.batch_id IS NOT OLD.batch_id OR NEW.queued_at IS NOT OLD.queued_at
BEGIN SELECT RAISE(ABORT, 'pod_assignments: frozen column'); END;
CREATE TRIGGER pod_assignments_pod_once BEFORE UPDATE OF pod_id ON pod_assignments WHEN OLD.pod_id IS NOT NULL AND NEW.pod_id IS NOT OLD.pod_id
BEGIN SELECT RAISE(ABORT, 'pod_assignments: pod_id is set once'); END;
CREATE TRIGGER pod_assignments_handed_once BEFORE UPDATE OF handed_at ON pod_assignments WHEN OLD.handed_at IS NOT NULL AND NEW.handed_at IS NOT OLD.handed_at
BEGIN SELECT RAISE(ABORT, 'pod_assignments: handed_at is set once'); END;
CREATE TRIGGER pod_assignments_pod_same_batch BEFORE UPDATE OF pod_id ON pod_assignments
WHEN NEW.pod_id IS NOT NULL AND NEW.pod_id IS NOT OLD.pod_id AND (SELECT batch_id FROM pods WHERE id = NEW.pod_id) IS NOT NEW.batch_id
BEGIN SELECT RAISE(ABORT, 'pod_assignments: the pod belongs to another batch'); END;
CREATE TRIGGER pod_assignments_done_once BEFORE UPDATE OF done_at, status, execution_ms, agent_execution_ms, error_code ON pod_assignments
WHEN OLD.done_at IS NOT NULL AND (NEW.done_at IS NOT OLD.done_at OR NEW.status IS NOT OLD.status OR NEW.execution_ms IS NOT OLD.execution_ms
  OR NEW.agent_execution_ms IS NOT OLD.agent_execution_ms OR NEW.error_code IS NOT OLD.error_code)
BEGIN SELECT RAISE(ABORT, 'pod_assignments: done is set once'); END;

CREATE TABLE pod_events (id INTEGER PRIMARY KEY, pod_id TEXT NOT NULL REFERENCES pods(id), at TEXT NOT NULL,
  from_state TEXT, to_state TEXT, event TEXT NOT NULL, code TEXT, detail_json TEXT CHECK (detail_json IS NULL OR json_valid(detail_json)));
CREATE INDEX pod_events_pod ON pod_events(pod_id, id);
CREATE TRIGGER pod_events_no_update BEFORE UPDATE ON pod_events
BEGIN SELECT RAISE(ABORT, 'pod_events: append-only'); END;
CREATE TRIGGER pod_events_no_delete BEFORE DELETE ON pod_events
BEGIN SELECT RAISE(ABORT, 'pod_events: no physical delete'); END;

CREATE TABLE pod_safety_log (id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('probe','canary')),
  image_digest TEXT, verdict TEXT NOT NULL CHECK (verdict IN ('ok','too_broad','failed','verified','inconclusive')),
  detail_json TEXT NOT NULL CHECK (json_valid(detail_json)), created_by TEXT NOT NULL, created_at TEXT NOT NULL,
  CHECK ((kind = 'canary') = (image_digest IS NOT NULL)),
  CHECK ((kind = 'probe' AND verdict IN ('ok','too_broad','failed')) OR (kind = 'canary' AND verdict IN ('verified','failed','inconclusive'))));
CREATE TRIGGER pod_safety_log_no_update BEFORE UPDATE ON pod_safety_log
BEGIN SELECT RAISE(ABORT, 'pod_safety_log: append-only'); END;
CREATE TRIGGER pod_safety_log_no_delete BEFORE DELETE ON pod_safety_log
BEGIN SELECT RAISE(ABORT, 'pod_safety_log: no physical delete'); END;
