-- 0025_mandates.sql: M6N-M1 (C370). Spending mandates: a standing approval the Owner creates in the app for one holder (the night
-- shift). Additive and up-only: one new table, two nullable columns on estimates and batches (existing rows keep NULL), and the
-- triggers that keep them honest. A mandate is never deleted: revoking it is a state.

CREATE TABLE mandates (
  id               TEXT PRIMARY KEY,                       -- UUIDv7
  holder_key       TEXT NOT NULL,                          -- Go-validated: 'agent:nightshift' (C366.1); free text so a later holder needs no migration
  title            TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
  budget_micro_usd INTEGER NOT NULL CHECK (budget_micro_usd BETWEEN 1 AND 100000000),
  valid_from       TEXT NOT NULL,
  valid_until      TEXT NOT NULL,
  scope_json       TEXT NOT NULL CHECK (json_valid(scope_json) AND json_type(scope_json) = 'object'),
  state            TEXT NOT NULL CHECK (state IN ('approved','revoked')),
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  revoked_by       TEXT,
  revoked_at       TEXT,
  revoke_reason    TEXT CHECK (revoke_reason IS NULL OR length(revoke_reason) BETWEEN 1 AND 500),
  CHECK (valid_until > valid_from),
  CHECK ((state = 'revoked') = (revoked_at IS NOT NULL)),
  CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
  CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);
CREATE INDEX mandates_holder ON mandates(holder_key, state, valid_until);

CREATE TRIGGER mandates_no_delete BEFORE DELETE ON mandates
BEGIN SELECT RAISE(ABORT, 'mandates: no physical delete'); END;
CREATE TRIGGER mandates_frozen BEFORE UPDATE OF id, holder_key, title, budget_micro_usd, valid_from, valid_until, scope_json,
  created_by, created_at ON mandates
BEGIN SELECT RAISE(ABORT, 'mandates: frozen column'); END;
CREATE TRIGGER mandates_transition BEFORE UPDATE OF state ON mandates
WHEN NOT (OLD.state = 'approved' AND NEW.state = 'revoked') AND NEW.state IS NOT OLD.state
BEGIN SELECT RAISE(ABORT, 'mandates: illegal transition'); END;
CREATE TRIGGER mandates_final BEFORE UPDATE OF state, revoked_by, revoked_at, revoke_reason ON mandates
WHEN OLD.state = 'revoked'
BEGIN SELECT RAISE(ABORT, 'mandates: a revoked mandate is immutable'); END;

-- the mandate an estimate was quoted under and the batch it paid for: set at insert, never changed (the 0013 per-column pattern)
ALTER TABLE estimates ADD COLUMN mandate_id TEXT REFERENCES mandates(id);
ALTER TABLE batches   ADD COLUMN mandate_id TEXT REFERENCES mandates(id);
CREATE INDEX batches_mandate ON batches(mandate_id) WHERE mandate_id IS NOT NULL;
CREATE TRIGGER estimates_mandate_frozen BEFORE UPDATE OF mandate_id ON estimates WHEN NEW.mandate_id IS NOT OLD.mandate_id
BEGIN SELECT RAISE(ABORT, 'estimates: frozen column'); END;
CREATE TRIGGER batches_mandate_frozen BEFORE UPDATE OF mandate_id ON batches WHEN NEW.mandate_id IS NOT OLD.mandate_id
BEGIN SELECT RAISE(ABORT, 'batches: frozen column'); END;
