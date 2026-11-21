-- 0023_upload_fallback.sql: BULK-R2 (C399.3). The upload fallback: a private S3-compatible bucket the worker can put artifacts into when
-- the app is unreachable. Additive and up-only: one nullable column on uploads (existing rows keep NULL; the handler sets it at
-- receipt from now on), two new tables, and the triggers that keep them honest. Nothing in an existing path reads these tables unless r2.env is present.

ALTER TABLE uploads ADD COLUMN via TEXT CHECK (via IS NULL OR via IN ('primary','fallback'));

-- via says which route delivered the file: set once, with the receipt, never on an upload that is not received
CREATE TRIGGER uploads_via_on_insert BEFORE INSERT ON uploads WHEN NEW.via IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'uploads: via is set at receipt'); END;
CREATE TRIGGER uploads_via_set_once BEFORE UPDATE OF via ON uploads WHEN OLD.via IS NOT NULL AND NEW.via IS NOT OLD.via
BEGIN SELECT RAISE(ABORT, 'uploads: via is set once'); END;
CREATE TRIGGER uploads_via_when_received BEFORE UPDATE OF via ON uploads WHEN NEW.via IS NOT NULL AND NEW.state NOT IN ('received','consumed')
BEGIN SELECT RAISE(ABORT, 'uploads: via needs a received upload'); END;

-- one row per upload that was given a fallback object key (the key is the upload id under the configured prefix; no URL is stored)
CREATE TABLE fallback_objects (
  upload_id  TEXT PRIMARY KEY REFERENCES uploads(id),
  attempt_id TEXT NOT NULL REFERENCES job_attempts(id),
  minted_at  TEXT NOT NULL,
  fetched_at TEXT,
  deleted_at TEXT
);
CREATE INDEX fallback_objects_attempt ON fallback_objects(attempt_id);
CREATE INDEX fallback_objects_pending ON fallback_objects(deleted_at) WHERE deleted_at IS NULL;
CREATE TRIGGER fallback_objects_attempt_matches BEFORE INSERT ON fallback_objects
WHEN NEW.attempt_id IS NOT (SELECT attempt_id FROM uploads WHERE id = NEW.upload_id)
BEGIN SELECT RAISE(ABORT, 'fallback_objects: attempt_id must be the upload''s attempt'); END;
CREATE TRIGGER fallback_objects_new_row_open BEFORE INSERT ON fallback_objects
WHEN NEW.fetched_at IS NOT NULL OR NEW.deleted_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'fallback_objects: a new row is neither fetched nor deleted'); END;
CREATE TRIGGER fallback_objects_frozen BEFORE UPDATE OF upload_id, attempt_id, minted_at ON fallback_objects
BEGIN SELECT RAISE(ABORT, 'fallback_objects: frozen column'); END;
CREATE TRIGGER fallback_objects_fetched_once BEFORE UPDATE OF fetched_at ON fallback_objects WHEN OLD.fetched_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'fallback_objects: fetched_at is set once'); END;
CREATE TRIGGER fallback_objects_deleted_once BEFORE UPDATE OF deleted_at ON fallback_objects WHEN OLD.deleted_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'fallback_objects: deleted_at is set once'); END;
CREATE TRIGGER fallback_objects_no_delete BEFORE DELETE ON fallback_objects
BEGIN SELECT RAISE(ABORT, 'fallback_objects: no physical delete'); END;

-- an attempt-scoped, time-bounded outage drill: while until is in the future the primary upload route answers 503 for that attempt
CREATE TABLE upload_drills (
  attempt_id TEXT PRIMARY KEY REFERENCES job_attempts(id),
  until      TEXT NOT NULL,
  reason     TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
  created_by TEXT NOT NULL CHECK (length(created_by) > 0),
  created_at TEXT NOT NULL
);
CREATE TRIGGER upload_drills_no_update BEFORE UPDATE ON upload_drills
BEGIN SELECT RAISE(ABORT, 'upload_drills: append-only'); END;
CREATE TRIGGER upload_drills_no_delete BEFORE DELETE ON upload_drills
BEGIN SELECT RAISE(ABORT, 'upload_drills: no physical delete'); END;
