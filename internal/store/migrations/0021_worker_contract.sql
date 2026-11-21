-- 0021_worker_contract.sql: G1-F3b (C322.3). The worker capability gate: which wire capabilities the operator has opened for one
-- backend's worker image, as an append-only log. A backend with no row has no open capability, so a job with needs is not claimable
-- and not estimable; a job with no needs (every job that exists today) never consults the log. Additive: no existing table changes.
CREATE TABLE worker_contract_log (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  backend      TEXT NOT NULL CHECK (length(backend) > 0),
  image_digest TEXT NOT NULL CHECK (length(image_digest) > 0),   -- 'sha256:<64 hex>' as the backend's quote reports it
  caps_json    TEXT NOT NULL CHECK (json_valid(caps_json) AND json_type(caps_json) = 'array'),
  via          TEXT NOT NULL CHECK (via IN ('cli','auto')),
  attempt_id   TEXT REFERENCES job_attempts(id),                 -- the attempt whose output closed a cap (auto rows only)
  changed_by   TEXT NOT NULL CHECK (length(changed_by) > 0),
  changed_at   TEXT NOT NULL,
  reason       TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
  CHECK ((via = 'auto') = (attempt_id IS NOT NULL))
);
CREATE INDEX worker_contract_log_backend ON worker_contract_log(backend, id DESC);
CREATE TRIGGER worker_contract_log_no_update BEFORE UPDATE ON worker_contract_log
BEGIN SELECT RAISE(ABORT, 'worker_contract_log: append-only'); END;
CREATE TRIGGER worker_contract_log_no_delete BEFORE DELETE ON worker_contract_log
BEGIN SELECT RAISE(ABORT, 'worker_contract_log: no physical delete'); END;
CREATE VIEW worker_contract AS                                    -- the current state: the latest log row per backend
SELECT l.backend, l.image_digest, l.caps_json, l.via, l.attempt_id, l.changed_by, l.changed_at, l.reason
FROM worker_contract_log l
WHERE l.id = (SELECT MAX(id) FROM worker_contract_log WHERE backend = l.backend);
