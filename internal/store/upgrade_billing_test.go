package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// billingMigration is the position of 0016_billing_backend.sql.
const billingMigration = 16

// TestUpgrade_BillingBackend_FromEveryPriorSchema populates a library at each schema since the generation tables exist (7 ... 15),
// brings it to schema 15, lets store.Open apply 0016, and checks that every row of every table is unchanged except for the new
// backend column ('runpod-serverless' on the rows of billing_windows, billing_buckets, worker_meter and ledger), the
// foreign keys are clean, the rebuilt tables keep their immutability triggers and two backends may now hold the same window.
func TestUpgrade_BillingBackend_FromEveryPriorSchema(t *testing.T) {
	t.Parallel()
	m3 := func(tb testing.TB, s *store.Store) { librarytest.FixtureM3(tb, s, librarytest.FixedNow) }
	l2 := func(tb testing.TB, s *store.Store) { librarytest.FixtureL2Upgrade(tb, s, librarytest.FixedNow) }
	l3 := func(tb testing.TB, s *store.Store) { librarytest.FixtureL3Upgrade(tb, s, librarytest.FixedNow) }
	m5 := func(tb testing.TB, s *store.Store) { librarytest.FixtureM5(tb, s, librarytest.FixedNow) }
	for _, c := range []struct {
		version int
		fixture func(testing.TB, *store.Store)
	}{{7, m3}, {9, l2}, {10, l3}, {11, l3}, {12, l3}, {13, m5}, {14, m5}, {15, m5}} {
		t.Run(fmt.Sprintf("from_%d", c.version), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			prev, p := openAt(t, c.version)
			c.fixture(t, prev)
			if err := store.MigrateTo(ctx, prev.W, billingMigration-1); err != nil {
				t.Fatal(err)
			}
			// seed the billing tables with a second window and a lifetime and a bill row, so the copy is not trivial
			for _, q := range []string{
				`INSERT OR IGNORE INTO billing_buckets(bucket_start, fetched_at, total_micro_usd, compute_micro_usd, disk_micro_usd) VALUES('2026-10-14T13:00:00.000Z','2026-10-14T20:00:00.000Z',7,6,1)`,
				`INSERT OR IGNORE INTO billing_windows(window_start,window_end,granularity,buckets,billed_micro_usd,billed_compute_micro_usd,provisional_micro_usd,reserved_micro_usd,batches,verdict,fetched_at)
					VALUES('2026-10-14T13:00:00.000Z','2026-10-14T14:00:00.000Z','hour',1,7,6,5,9,1,'ok','2026-10-14T20:00:00.000Z')`,
				`INSERT OR IGNORE INTO worker_meter(worker_key,started_at,first_seen_at,last_seen_at,uptime_s,status_last) VALUES('wk-up','2026-10-14T12:00:00.000Z','2026-10-14T12:00:00.000Z','2026-10-14T12:10:00.000Z',60,'RUNNING')`,
			} {
				if _, err := prev.W.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			before := dbSnapshot(t, prev.R)
			if len(before["ledger"]) == 0 || len(before["billing_windows"]) == 0 || len(before["billing_buckets"]) == 0 || len(before["worker_meter"]) == 0 {
				t.Fatalf("thin fixture: ledger %d, windows %d, buckets %d, meter %d", len(before["ledger"]), len(before["billing_windows"]), len(before["billing_buckets"]), len(before["worker_meter"]))
			}
			prev.W.Close()
			s, err := store.Open(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < billingMigration {
				t.Fatalf("version=%d, migrations=%d", v, store.MigrationCount())
			}
			after := dbSnapshot(t, s.R)
			changed := map[string]bool{"ledger": true, "billing_windows": true, "billing_buckets": true, "worker_meter": true}
			for tb, rows := range before {
				got := after[tb]
				if changed[tb] {
					got = make([]string, len(after[tb]))
					for i, r := range after[tb] {
						if !strings.HasSuffix(r, "backend=runpod-serverless|") {
							t.Errorf("%s row without the default backend: %s", tb, r)
						}
						got[i] = strings.TrimSuffix(r, "backend=runpod-serverless|")
					}
				}
				if strings.Join(rows, "\n") != strings.Join(got, "\n") {
					t.Errorf("table %s changed by the upgrade: %d rows before, %d after", tb, len(rows), len(after[tb]))
				}
			}
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
				t.Errorf("foreign_key_check rows = %s", n)
			}
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%_new%' AND name LIKE 'billing%'`); n != "0" {
				t.Errorf("%s billing objects still mention _new", n)
			}
			// the rebuilt tables are immutable / append-only as before
			if _, err := s.W.ExecContext(ctx, `UPDATE billing_windows SET verdict = 'ok'`); err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Errorf("billing_windows is not immutable: %v", err)
			}
			if _, err := s.W.ExecContext(ctx, `DELETE FROM billing_buckets`); err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Errorf("billing_buckets is not append-only: %v", err)
			}
			// two backends can hold the same window and the same bucket observation
			if _, err := s.W.ExecContext(ctx, `INSERT INTO billing_windows(window_start,window_end,granularity,buckets,billed_micro_usd,billed_compute_micro_usd,provisional_micro_usd,reserved_micro_usd,batches,verdict,fetched_at,backend)
				SELECT window_start,window_end,granularity,buckets,billed_micro_usd,billed_compute_micro_usd,provisional_micro_usd,reserved_micro_usd,batches,verdict,fetched_at,'runpod-serverless-acestep' FROM billing_windows LIMIT 1`); err != nil {
				t.Errorf("a second backend cannot hold the same window: %v", err)
			}
			if _, err := s.W.ExecContext(ctx, `INSERT INTO billing_buckets(bucket_start,fetched_at,total_micro_usd,compute_micro_usd,disk_micro_usd,backend)
				SELECT bucket_start,fetched_at,total_micro_usd,compute_micro_usd,disk_micro_usd,'runpod-serverless-acestep' FROM billing_buckets LIMIT 1`); err != nil {
				t.Errorf("a second backend cannot hold the same bucket: %v", err)
			}
			if _, err := s.W.ExecContext(ctx, `INSERT INTO billing_windows(window_start,window_end,granularity,buckets,billed_micro_usd,billed_compute_micro_usd,provisional_micro_usd,reserved_micro_usd,batches,verdict,fetched_at)
				SELECT window_start,window_end,granularity,buckets,billed_micro_usd,billed_compute_micro_usd,provisional_micro_usd,reserved_micro_usd,batches,verdict,fetched_at FROM billing_windows WHERE backend='runpod-serverless' LIMIT 1`); err == nil {
				t.Error("the same backend can hold a window twice")
			}
		})
	}
}
