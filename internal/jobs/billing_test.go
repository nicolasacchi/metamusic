package jobs

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 14, h, m, 0, 0, time.UTC) }

// billBatch seeds a one-job batch created at `created`, with one finished attempt whose execution was
// settled for `settle`, and finishes the batch at `finished` (zero = it stays active). The clock ends at `now`.
func (r *rig) billBatch(created, finished time.Time, settle money.MicroUSD, now time.Time) seeded {
	r.t.Helper()
	r.clk.Set(created)
	s := r.seedBatch(batchSpec{})
	job := s.Jobs[0]
	r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, execution_ms, terminal_basis, terminal_at, settled, created_at)
		VALUES(?, ?, 1, 'fake', 'done', ?, ?, 690000, 60000, 'provider', ?, 1, ?)`, r.newID(), job, store.FormatTime(created), store.FormatTime(created.Add(3*time.Hour)),
		store.FormatTime(created.Add(time.Minute)), store.FormatTime(created))
	if !finished.IsZero() {
		r.clk.Set(finished)
	}
	if settle > 0 {
		bid, jid := s.Batch, job
		if err := r.sp.RunTx(r.t.Context(), func(tx *sql.Tx) error {
			return r.sp.Ledger().Append(r.t.Context(), tx, spend.Entry{Kind: spend.KindSettle, MicroUSD: settle, BatchID: &bid, JobID: &jid, PrincipalID: "human:h1", Reason: "execution"})
		}); err != nil {
			r.t.Fatal(err)
		}
	}
	if !finished.IsZero() {
		r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(finished), s.Batch)
	}
	r.clk.Set(now)
	return s
}

// hours scripts the billing buckets the backend returns (hour start -> total, compute).
func (r *rig) hours(h map[time.Time][2]money.MicroUSD) {
	r.mu.Lock()
	r.billingFn = func(from, to time.Time) ([]provider.BillBucket, error) {
		var out []provider.BillBucket
		for start, v := range h {
			if !start.Before(from) && start.Before(to) {
				out = append(out, provider.BillBucket{Start: start, End: start.Add(time.Hour), Total: v[0], Compute: v[1], Disk: v[0] - v[1]})
			}
		}
		return out, nil
	}
	r.mu.Unlock()
}

func (r *rig) windows() int { return r.count(`SELECT COUNT(*) FROM billing_windows`) }

func TestBilling_QuietWindowNeverOvershoots(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.billBatch(at(12, 50), at(13, 20), 20000, at(13, 30))
	r.hours(map[time.Time][2]money.MicroUSD{at(12, 0): {5000, 5000}, at(13, 0): {25000, 25000}})
	rec := r.recon()
	for now := at(13, 30); now.Before(at(23, 30)); now = now.Add(30 * time.Minute) {
		r.clk.Set(now)
		r.mustPass(rec)
		// the property at every commit: a closed window always lies after the batch's last activity
		if n := r.count(`SELECT COUNT(*) FROM billing_windows w JOIN batches b ON b.id = ? WHERE w.window_end <= b.finished_at`, s.Batch); n != 0 {
			t.Fatalf("at %s a window closed inside the batch", now.Format("15:04"))
		}
		var billed, reserved, released int64
		billed = r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE kind = 'bill'`)
		reserved = r.ledger(s.Batch, "reserve", "")
		released = r.ledger(s.Batch, "release", "")
		if r.ledger(s.Batch, "settle", "")+billed > reserved || released > reserved {
			t.Fatalf("at %s: settled %d + billed %d, released %d, reserved %d", now.Format("15:04"), r.ledger(s.Batch, "settle", ""), billed, released, reserved)
		}
	}
	if r.windows() != 1 {
		t.Fatalf("windows %d", r.windows())
	}
	if got := r.str(`SELECT window_start || '|' || window_end || '|' || billed_micro_usd || '|' || provisional_micro_usd || '|' || reserved_micro_usd || '|' || batches || '|' || verdict FROM billing_windows`); got !=
		"2026-10-14T12:00:00.000Z|2026-10-14T14:00:00.000Z|30000|20000|38525|1|ok" {
		t.Errorf("window %q", got)
	}
	if got := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE kind = 'bill' AND reason = 'reconcile' AND principal_id = 'system:billing' AND batch_id IS NULL`); got != 10000 {
		t.Errorf("bill adjustment %d, want billed - provisional = 10000", got)
	}
	if r.outstanding(s.Batch) != 0 || r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, s.Batch) == "" {
		t.Error("the residual must be released with the window")
	}
	if r.auditOps("reconciler:billing_window") != 1 {
		t.Error("billing window not audited")
	}
}

func TestBilling_WindowWaitsForStraddlingBatch(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.billBatch(at(12, 50), at(13, 20), 20000, at(19, 30))
	r.hours(map[time.Time][2]money.MicroUSD{at(12, 0): {5000, 5000}, at(13, 0): {25000, 25000}})
	rec := r.recon()
	// 19:30 - 6 h = 13:30: the latest closed bucket end is 13:00, inside the batch (it ran until 13:20)
	r.mustPass(rec)
	if r.windows() != 0 || r.outstanding(s.Batch) == 0 {
		t.Fatal("a window closed through a batch that straddles the bucket boundary")
	}
	r.clk.Set(at(20, 30)) // the next boundary, 14:00, is after the batch's quiet time
	r.mustPass(rec)
	if r.windows() != 1 {
		t.Fatalf("windows %d, want 1", r.windows())
	}
	if got := r.str(`SELECT billed_micro_usd FROM billing_windows`); got != "30000" {
		t.Errorf("the window must cover both hours: billed %s", got)
	}
}

func TestBilling_RejectedBatchDoesNotBlock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// a batch that never reached the provider and is still unfinished does not hold the window back
	r.clk.Set(at(12, 10))
	idle := r.seedBatch(batchSpec{})
	rej := r.seedBatch(batchSpec{state: "rejected"})
	_ = rej
	finished := r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {25000, 25000}})
	rec := r.recon()
	r.mustPass(rec)
	if r.windows() != 1 {
		t.Fatal("the window must close")
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, finished.Batch) == "" {
		t.Error("the finished batch is reconciled")
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, idle.Batch) != "" {
		t.Error("an unfinished batch must stay unreconciled")
	}
}

func TestBilling_WindowNeverCrossesMonth(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	created := time.Date(2026, 10, 31, 22, 30, 0, 0, time.UTC)
	s := r.billBatch(created, created.Add(20*time.Minute), 20000, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	r.hours(map[time.Time][2]money.MicroUSD{
		time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC): {10000, 10000},
		time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC): {5000, 5000},
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC):   {7000, 7000},
	})
	rec := r.recon()
	r.mustPass(rec)
	if got := r.str(`SELECT window_start || '|' || window_end || '|' || billed_micro_usd FROM billing_windows ORDER BY window_start LIMIT 1`); got != "2026-10-31T22:00:00.000Z|2026-11-01T00:00:00.000Z|15000" {
		t.Errorf("first window %q", got)
	}
	// the bill row belongs to the window that contains the bucket's start, so it counts in October
	if got := r.str(`SELECT window_start FROM ledger WHERE kind = 'bill' ORDER BY id LIMIT 1`); got != "2026-10-31T22:00:00.000Z" {
		t.Errorf("bill window %q", got)
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, s.Batch) == "" {
		t.Error("batch not reconciled")
	}
}

func TestBilling_NeverReadsInProgressBucket(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(14, 30))
	cur := at(14, 0) // the hour in progress
	r.mu.Lock()
	r.billingFn = func(from, to time.Time) ([]provider.BillBucket, error) {
		return []provider.BillBucket{
			{Start: at(13, 0), End: at(14, 0), Total: 100, Compute: 100},
			{Start: cur, End: cur.Add(time.Hour), Total: 999999, Compute: 999999}, // in progress: must never be stored
			{Start: at(12, 30), End: at(13, 30), Total: 5, Compute: 5},            // not hour-aligned
		}, nil
	}
	r.mu.Unlock()
	rec := r.recon()
	r.mustPass(rec)
	r.mu.Lock()
	calls := append([][2]time.Time(nil), r.billCalls...)
	r.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("billing calls %d", len(calls))
	}
	if !calls[0][0].Equal(at(12, 0)) || !calls[0][1].Equal(at(14, 0)) {
		t.Errorf("range %s - %s: aligned bounds, ending at the start of the current hour", calls[0][0], calls[0][1])
	}
	if got := r.count(`SELECT COUNT(*) FROM billing_buckets WHERE total_micro_usd = 999999 OR bucket_start = '2026-10-14T12:30:00.000Z'`); got != 0 {
		t.Error("an in-progress or misaligned bucket was stored")
	}
	if got := r.count(`SELECT COUNT(*) FROM billing_buckets`); got != 1 {
		t.Errorf("buckets stored %d", got)
	}
	// at most one fetch per 15 minutes
	r.advance(5 * time.Minute)
	r.mustPass(rec)
	r.mu.Lock()
	n := len(r.billCalls)
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("fetched again after 5 minutes (%d calls)", n)
	}
	r.advance(11 * time.Minute)
	r.mustPass(rec)
	r.mu.Lock()
	n = len(r.billCalls)
	r.mu.Unlock()
	if n != 2 {
		t.Errorf("no fetch after 16 minutes (%d calls)", n)
	}
}

func TestBilling_LateIncreaseAdjustsAndTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.billBatch(at(12, 50), at(13, 20), 20000, at(20, 30))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {30000, 30000}})
	rec := r.recon()
	r.mustPass(rec)
	if r.windows() != 1 || r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Fatal("setup: one clean window")
	}
	// RunPod revises the bucket upwards after the window closed
	r.advance(20 * time.Minute)
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {60000, 60000}})
	r.mustPass(rec)
	if got := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE kind = 'bill' AND reason = 'late_adjust'`); got != 30000 {
		t.Errorf("late adjustment %d, want 30000", got)
	}
	if got := r.str(`SELECT window_start FROM ledger WHERE reason = 'late_adjust'`); got != "2026-10-14T12:00:00.000Z" {
		t.Errorf("late adjust window %q", got)
	}
	if got := r.openTripReason(); got != TripBilledExceeds {
		t.Errorf("trip %q: compute 60000 exceeds the reserved 38525", got)
	}
	if r.auditOps("reconciler:late_adjust") != 1 {
		t.Error("late adjustment not audited")
	}
	// idempotent: nothing more to adjust
	r.advance(20 * time.Minute)
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE reason = 'late_adjust'`); n != 1 {
		t.Errorf("late_adjust rows %d", n)
	}
	_ = s
}

func TestBilling_PausesOnSkew(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {25000, 25000}})
	rec := r.recon()
	skew := 45 * time.Second
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Skew: &skew, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
	r.mustPass(rec)
	if r.windows() != 0 {
		t.Fatal("a window closed under a 45 s clock skew")
	}
	m, ok := r.findNote("Clock skew 45 s")
	if !ok || m.Priority != 4 {
		t.Errorf("notes %v", r.noteTitles())
	}
	skew = 5 * time.Second
	r.advance(time.Minute)
	r.mustPass(rec)
	if r.windows() != 1 {
		t.Error("the window must close once the skew is back inside 30 s")
	}
}

func TestBilling_StalledNtfy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	fin := r.billBatch(at(12, 50), at(13, 20), 20000, at(13, 30))
	// a batch that keeps running blocks every window
	r.clk.Set(at(13, 5))
	blocker := r.seedBatch(batchSpec{})
	r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
		VALUES(?, ?, 1, 'fake', 'unknown', ?, ?, 690000, ?)`, r.newID(), blocker.Jobs[0], store.FormatTime(at(13, 5)), store.FormatTime(at(13, 5).Add(1000*time.Hour)), store.FormatTime(at(13, 5)))
	r.hours(map[time.Time][2]money.MicroUSD{})
	rec := r.recon()
	r.clk.Set(at(13, 30).Add(71 * time.Hour))
	r.mustPass(rec)
	if _, ok := r.findNote("Billing reconciliation stalled"); ok {
		t.Fatal("alerted before 72 hours")
	}
	r.clk.Set(at(13, 30).Add(73 * time.Hour))
	r.mustPass(rec)
	m, ok := r.findNote("Billing reconciliation stalled")
	if !ok || m.Priority != 4 || !strings.Contains(m.Body, fin.Batch) {
		t.Fatalf("stalled ntfy %+v (%v)", m, r.noteTitles())
	}
	r.clk.Set(at(13, 30).Add(80 * time.Hour))
	r.mustPass(rec)
	n := 0
	for _, title := range r.noteTitles() {
		if title == "Billing reconciliation stalled" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("alerts %d within 72 h", n)
	}
}

func TestBilling_BilledExceedsReservedTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	// compute 60000 > reserved 38525
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {70000, 60000}})
	r.mustPass(r.recon())
	if got := r.str(`SELECT verdict FROM billing_windows`); got != "billed_exceeds_reserved" {
		t.Errorf("verdict %q", got)
	}
	if r.openTripReason() != TripBilledExceeds {
		t.Errorf("trip %q", r.openTripReason())
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, s.Batch) == "" {
		t.Error("the window is still reconciled: a trip is an alarm, not a blocker")
	}
	// disk is recorded and never trips: compute within the reserve, a large disk amount
	r2 := newRig(t)
	r2.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	r2.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {900000, 25000}})
	r2.mustPass(r2.recon())
	if got := r2.str(`SELECT verdict || '|' || billed_micro_usd || '|' || billed_compute_micro_usd FROM billing_windows`); got != "ok|900000|25000" {
		t.Errorf("window %q", got)
	}
	if r2.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Error("disk spend tripped the provider")
	}
	if got := r2.str(`SELECT disk_micro_usd FROM billing_buckets`); got != "875000" {
		t.Errorf("disk %s", got)
	}
}

func TestBilling_SpendWithoutJobsTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(13, 30), at(13, 40), 20000, at(22, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {5000, 5000}, at(15, 0): {20000, 20000}})
	r.mustPass(r.recon())
	if got := r.str(`SELECT verdict FROM billing_windows`); got != "spend_without_jobs" {
		t.Errorf("verdict %q", got)
	}
	if r.openTripReason() != TripSpendWithoutJobs {
		t.Errorf("trip %q", r.openTripReason())
	}
}

func TestBilling_UnreadableHoldElapsed(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"none", "not_permitted"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t)
			b1 := r.billBatch(at(10, 0), at(10, 30), 20000, at(10, 40))
			if mode == "none" {
				r.caps.Billing = "none"
			} else {
				r.mu.Lock()
				r.billingFn = func(time.Time, time.Time) ([]provider.BillBucket, error) { return nil, provider.ErrNotPermitted }
				r.mu.Unlock()
			}
			rec := r.recon()
			r.clk.Set(at(10, 30).Add(47 * time.Hour))
			r.mustPass(rec)
			if r.outstanding(b1.Batch) == 0 {
				t.Fatal("released before the 48 h hold")
			}
			r.clk.Set(at(10, 30).Add(48*time.Hour + time.Minute))
			r.mustPass(rec)
			if r.outstanding(b1.Batch) != 0 || r.ledger(b1.Batch, "release", "hold_elapsed") == 0 {
				t.Errorf("outstanding %d", r.outstanding(b1.Batch))
			}
			if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, b1.Batch) == "" {
				t.Error("reconciled_at not set")
			}
			if r.windows() != 0 {
				t.Error("no window without readable billing")
			}
			m, ok := r.findNote("Billing not reconciled")
			if !ok || m.Priority != 2 || !strings.Contains(m.Body, "1 batches") {
				t.Fatalf("weekly ntfy %+v (%v)", m, r.noteTitles())
			}
			if r.auditOps("reconciler:hold_release") != 1 {
				t.Error("hold release not audited")
			}
			// a second batch released within the week does not repeat the reminder
			b2 := r.billBatch(at(10, 0).Add(49*time.Hour), at(10, 30).Add(49*time.Hour), 1000, at(10, 30).Add(49*time.Hour+time.Minute))
			r.clk.Set(at(10, 30).Add(98 * time.Hour))
			r.mustPass(rec)
			if r.outstanding(b2.Batch) != 0 {
				t.Error("second batch not released")
			}
			n := 0
			for _, title := range r.noteTitles() {
				if title == "Billing not reconciled" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("weekly reminders %d", n)
			}
			r.clk.Set(at(10, 30).Add(48*time.Hour + 8*24*time.Hour))
			r.billBatch(at(10, 0).Add(10*24*time.Hour), at(10, 30).Add(10*24*time.Hour), 1000, at(10, 40).Add(10*24*time.Hour))
			r.clk.Set(at(10, 40).Add(13 * 24 * time.Hour))
			r.mustPass(rec)
			n = 0
			for _, title := range r.noteTitles() {
				if title == "Billing not reconciled" {
					n++
				}
			}
			if n != 2 {
				t.Errorf("after a week the reminder returns: %d", n)
			}
		})
	}
}

var _ = time.Second

func TestBilling_LateDecreaseAdjustsNegative(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(20, 30))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {30000, 30000}})
	rec := r.recon()
	r.mustPass(rec)
	r.advance(20 * time.Minute)
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {24000, 24000}})
	r.mustPass(rec)
	if got := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE reason = 'late_adjust'`); got != -6000 {
		t.Errorf("late adjustment %d, want -6000", got)
	}
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Error("a downward revision tripped")
	}
	if r.auditOps("reconciler:late_adjust") != 1 {
		t.Error("not audited")
	}
}

func TestBilling_MonthBoundaryOfAnotherZone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	zone := time.FixedZone("half", 5*3600+1800) // local midnight is 18:30 UTC: not an hour boundary
	created := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r.billBatch(created, created.Add(20*time.Minute), 20000, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	r.hours(map[time.Time][2]money.MicroUSD{time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC): {5000, 5000}})
	r.mustPass(r.recon(func(o *ReconcilerOptions) { o.Location = zone }))
	if got := r.str(`SELECT window_end FROM billing_windows ORDER BY window_start LIMIT 1`); got != "2026-10-31T19:00:00.000Z" {
		t.Errorf("window_end %q: the first bucket end at or after local midnight", got)
	}
}

func TestBilling_UnfinishedBatchCountsAsActivity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(14, 30))
	r.clk.Set(at(14, 0))
	live := r.seedBatch(batchSpec{})
	r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
		VALUES(?, ?, 1, 'fake', 'unknown', ?, ?, 690000, ?)`, r.newID(), live.Jobs[0], store.FormatTime(at(14, 0)), store.FormatTime(at(14, 0).Add(1000*time.Hour)), store.FormatTime(at(14, 0)))
	r.clk.Set(at(21, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {25000, 25000}, at(14, 0): {500, 500}})
	r.mustPass(r.recon())
	if r.windows() != 1 {
		t.Fatalf("windows %d: the unfinished batch started after the window end", r.windows())
	}
	if got := r.str(`SELECT verdict FROM billing_windows`); got != "ok" {
		t.Errorf("verdict %q", got)
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, live.Batch) != "" {
		t.Error("the running batch must not be reconciled")
	}
}

func TestBilling_NegativeSkewPausesToo(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {25000, 25000}})
	skew := -40 * time.Second
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Skew: &skew, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
	rec := r.recon()
	r.mustPass(rec)
	if r.windows() != 0 {
		t.Error("closed under a negative skew of 40 s")
	}
	if _, ok := r.findNote("Clock skew 40 s"); !ok {
		t.Errorf("notes %v", r.noteTitles())
	}
}

func TestBilling_StalledMeasuresFromTheLastWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(13, 30))
	// a running batch blocks every window; an older window closed 50 hours after the batch finished
	r.exec(`INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd, provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at)
		VALUES('2026-10-10T00:00:00.000Z', '2026-10-10T06:00:00.000Z', 'hour', 6, 0, 0, 0, 0, 0, 'ok', ?)`, store.FormatTime(at(13, 30).Add(50*time.Hour)))
	r.clk.Set(at(13, 30).Add(100 * time.Hour))
	r.hours(map[time.Time][2]money.MicroUSD{})
	rec := r.recon()
	r.hold(r.run)
	if err := rec.stalled(t.Context(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.findNote("Billing reconciliation stalled"); ok {
		t.Fatal("alerted 50 hours after the last closed window")
	}
	r.clk.Set(at(13, 30).Add(123 * time.Hour))
	if err := rec.stalled(t.Context(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.findNote("Billing reconciliation stalled"); !ok {
		t.Errorf("73 hours after the last window: %v", r.noteTitles())
	}
}
