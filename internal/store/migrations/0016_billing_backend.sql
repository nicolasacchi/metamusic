-- 0016_billing_backend.sql: per-backend billing (E2-J1b, C151.4). Every backend reconciles its own batches against its own
-- endpoint's hourly buckets, so the billing windows, the observed buckets, the bill rows of the ledger and the worker
-- lifetimes each name the backend they belong to. The default 'runpod-serverless' is the yue2 backend of M3: every existing
-- row keeps meaning exactly what it did. No foreign key points at any of these tables.

-- ---- 1. worker_meter, ledger: one column each (keys and ids are already unique, so the primary keys stay) -------------
ALTER TABLE worker_meter ADD COLUMN backend TEXT NOT NULL DEFAULT 'runpod-serverless';
ALTER TABLE ledger ADD COLUMN backend TEXT NOT NULL DEFAULT 'runpod-serverless';   -- meaningful for kind = 'bill'

-- ---- 2. billing_windows: one window per (backend, window_start) -----------------------------------------------------
CREATE TABLE billing_windows_new (
  window_start           TEXT NOT NULL,
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
  backend                TEXT NOT NULL DEFAULT 'runpod-serverless',
  CHECK (window_end > window_start),
  PRIMARY KEY (backend, window_start)
) WITHOUT ROWID;
INSERT INTO billing_windows_new (window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd,
  provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at)
  SELECT window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd,
  provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at FROM billing_windows;
DROP TABLE billing_windows;
ALTER TABLE billing_windows_new RENAME TO billing_windows;
CREATE TRIGGER billing_windows_immutable BEFORE UPDATE ON billing_windows
BEGIN SELECT RAISE(ABORT, 'billing_windows: immutable'); END;
CREATE TRIGGER billing_windows_no_delete BEFORE DELETE ON billing_windows
BEGIN SELECT RAISE(ABORT, 'billing_windows: immutable'); END;

-- ---- 3. billing_buckets: one observation per (backend, bucket_start, fetched_at) -----------------------------------
CREATE TABLE billing_buckets_new (
  bucket_start      TEXT NOT NULL,
  fetched_at        TEXT NOT NULL,
  total_micro_usd   INTEGER NOT NULL CHECK (total_micro_usd >= 0),     -- totalAmount, ParseUSD of the literal (round up)
  compute_micro_usd INTEGER NOT NULL CHECK (compute_micro_usd >= 0),   -- gpuAmount + cpuAmount
  disk_micro_usd    INTEGER NOT NULL CHECK (disk_micro_usd >= 0),      -- recorded; never tripped on
  backend           TEXT NOT NULL DEFAULT 'runpod-serverless',
  PRIMARY KEY (backend, bucket_start, fetched_at)        -- window membership follows billing_windows ranges
) WITHOUT ROWID;
INSERT INTO billing_buckets_new (bucket_start, fetched_at, total_micro_usd, compute_micro_usd, disk_micro_usd)
  SELECT bucket_start, fetched_at, total_micro_usd, compute_micro_usd, disk_micro_usd FROM billing_buckets;
DROP TABLE billing_buckets;
ALTER TABLE billing_buckets_new RENAME TO billing_buckets;
CREATE TRIGGER billing_buckets_immutable BEFORE UPDATE ON billing_buckets
BEGIN SELECT RAISE(ABORT, 'billing_buckets: append-only'); END;
CREATE TRIGGER billing_buckets_no_delete BEFORE DELETE ON billing_buckets
BEGIN SELECT RAISE(ABORT, 'billing_buckets: append-only'); END;
