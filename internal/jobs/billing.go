package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Billing constants (C76.5).
const (
	billingLookback = 72 * time.Hour
	stalledAfter    = 72 * time.Hour
	windowQuiet     = 120 * time.Second
)

// reconcileBilling is step 7 of a pass: with hourly billing it fetches the closed buckets, adjusts windows
// whose buckets changed and closes quiet windows; without readable billing it releases residuals after the
// hold (C76.5).
func (r *Reconciler) reconcileBilling(ctx context.Context) error {
	now := r.o.Clock.Now()
	if r.o.Backend.Capabilities().Billing != "hour" {
		return r.holdRelease(ctx, now)
	}
	fetchFailed := false
	if err := r.fetchBuckets(ctx, now); err != nil {
		switch {
		case errors.Is(err, provider.ErrNotPermitted):
			return r.holdRelease(ctx, now)
		case errors.Is(err, provider.ErrAccess), errors.Is(err, provider.ErrEndpointNotFound):
			return r.Trip(ctx, TripProviderAccess, map[string]any{"op": "billing"})
		default:
			// transient (5xx, timeout, decode): no window may close on buckets that could not be read, or it
			// would settle against a billed amount of zero and release the residuals early. Retried next pass.
			fetchFailed = true
			r.o.Log.Warn("billing fetch", "err", redactErr(err))
		}
	}
	if err := r.lateAdjustments(ctx, now); err != nil {
		return err
	}
	if fetchFailed {
		return r.stalled(ctx, now)
	}
	if r.skewed() {
		r.notifyOnce(ctx, "skew", time.Hour, NoteClockSkew(r.o.PublicURL, int(r.skewAbs().Seconds())))
	} else {
		for {
			closed, err := r.closeWindow(ctx, now)
			if err != nil {
				return err
			}
			if !closed {
				break
			}
		}
	}
	return r.stalled(ctx, now)
}

func (r *Reconciler) skewAbs() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.skew == nil {
		return 0
	}
	if *r.skew < 0 {
		return -*r.skew
	}
	return *r.skew
}

func (r *Reconciler) skewed() bool { return r.skewAbs() > skewLimit }

func floorHour(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

// fetchBuckets reads the closed hourly buckets (never the in-progress one: the end is the start of the
// current hour, the bounds are hour-aligned and `lastN` is never used) and appends an observation for each
// bucket whose amounts changed. At most every 15 minutes.
func (r *Reconciler) fetchBuckets(ctx context.Context, now time.Time) error {
	r.mu.Lock()
	last := r.lastFetch
	r.mu.Unlock()
	if !last.IsZero() && now.Sub(last) < billingFetchGap && !now.Before(last) {
		return nil
	}
	var lastEnd, oldest sql.NullString
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT (SELECT MAX(window_end) FROM billing_windows),
		(SELECT MIN(created_at) FROM batches WHERE reconciled_at IS NULL AND state IN ('active','finished'))`).Scan(&lastEnd, &oldest); err != nil {
		return err
	}
	var from time.Time
	if lastEnd.Valid {
		from = mustT(lastEnd.String).Add(-billingLookback)
	}
	if oldest.Valid {
		if o := floorHour(mustT(oldest.String)); from.IsZero() || o.Before(from) {
			from = o
		}
	}
	end := floorHour(now)
	if from.IsZero() || !from.Before(end) {
		return nil
	}
	from = floorHour(from)
	buckets, err := r.o.Backend.Billing(ctx, from, end)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.lastFetch = now
	r.mu.Unlock()
	ts := store.FormatTime(now)
	return r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.o.Runner.fence()
		for _, b := range buckets {
			if !b.Start.Equal(floorHour(b.Start)) || !b.End.Equal(b.Start.Add(time.Hour)) || b.End.After(end) || b.Start.Before(from) {
				continue // misaligned or in-progress: never read
			}
			var total, compute, disk int64
			err := tx.QueryRowContext(ctx, `SELECT total_micro_usd, compute_micro_usd, disk_micro_usd FROM billing_buckets WHERE bucket_start = ? ORDER BY fetched_at DESC LIMIT 1`,
				store.FormatTime(b.Start)).Scan(&total, &compute, &disk)
			if err == nil && total == int64(b.Total) && compute == int64(b.Compute) && disk == int64(b.Disk) {
				continue
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			//nolint:gosec // G202: the fixed C69 predicate, values are bound
			if _, err := tx.ExecContext(ctx, `INSERT INTO billing_buckets(bucket_start, fetched_at, total_micro_usd, compute_micro_usd, disk_micro_usd)
				SELECT ?, ?, ?, ?, ? WHERE `+f.Where()+` ON CONFLICT DO NOTHING`,
				append([]any{store.FormatTime(b.Start), ts, int64(b.Total), int64(b.Compute), int64(b.Disk)}, f.Args()...)...); err != nil {
				return err
			}
		}
		return nil
	})
}

type bucketSum struct {
	total, compute, disk money.MicroUSD
	n                    int
}

// latestBuckets sums the latest observation of every bucket starting in [from, to).
func (r *Reconciler) latestBuckets(ctx context.Context, x Querier, from, to time.Time) (bucketSum, []bucketRow, error) {
	rows, err := x.QueryContext(ctx, `SELECT b.bucket_start, b.total_micro_usd, b.compute_micro_usd, b.disk_micro_usd FROM billing_buckets b
		WHERE b.bucket_start >= ? AND b.bucket_start < ?
		  AND b.fetched_at = (SELECT MAX(x.fetched_at) FROM billing_buckets x WHERE x.bucket_start = b.bucket_start) ORDER BY b.bucket_start`,
		store.FormatTime(from), store.FormatTime(to))
	if err != nil {
		return bucketSum{}, nil, err
	}
	defer rows.Close()
	var s bucketSum
	var list []bucketRow
	for rows.Next() {
		var br bucketRow
		var st string
		var t, c, d int64
		if err := rows.Scan(&st, &t, &c, &d); err != nil {
			return s, nil, err
		}
		br.start, br.total, br.compute, br.disk = mustT(st), money.MicroUSD(t), money.MicroUSD(c), money.MicroUSD(d)
		s.total, s.compute, s.disk, s.n = s.total+br.total, s.compute+br.compute, s.disk+br.disk, s.n+1
		list = append(list, br)
	}
	return s, list, rows.Err()
}

type bucketRow struct {
	start                time.Time
	total, compute, disk money.MicroUSD
}

// lateAdjustments corrects windows reconciled within the last 72 h whose buckets changed afterwards (C76.5
// step 3): a `late_adjust` bill row for the difference, and a trip when compute now exceeds the reserve.
func (r *Reconciler) lateAdjustments(ctx context.Context, now time.Time) error {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT window_start, window_end, provisional_micro_usd, reserved_micro_usd FROM billing_windows WHERE window_end > ? ORDER BY window_start`,
		store.FormatTime(now.Add(-billingLookback)))
	if err != nil {
		return err
	}
	type win struct {
		start, end            string
		provisional, reserved int64
	}
	var ws []win
	for rows.Next() {
		var w win
		if err := rows.Scan(&w.start, &w.end, &w.provisional, &w.reserved); err != nil {
			rows.Close()
			return err
		}
		ws = append(ws, w)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, w := range ws {
		sum, _, err := r.latestBuckets(ctx, r.o.Store.R, mustT(w.start), mustT(w.end))
		if err != nil {
			return err
		}
		var bills int64
		if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE kind = 'bill' AND window_start = ?`, w.start).Scan(&bills); err != nil {
			return err
		}
		delta := int64(sum.total) - (w.provisional + bills)
		if delta != 0 {
			ws := w.start
			err := r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
				f := r.o.Runner.fence()
				if err := r.o.Runner.ledgerAppend(ctx, tx, f, spend.Entry{Kind: spend.KindBill, MicroUSD: money.MicroUSD(delta), WindowStart: &ws,
					PrincipalID: spend.BillingPrincipal, Reason: "late_adjust"}); err != nil {
					return err
				}
				return r.auditTx(ctx, tx, f, "reconciler:late_adjust", map[string]any{"window_start": ws, "delta_micro_usd": delta}, money.MicroUSD(abs64(delta)))
			})
			if err != nil {
				return err
			}
		}
		if int64(sum.compute) > w.reserved {
			key := "late:" + w.start
			r.mu.Lock()
			done := r.actioned[key]
			r.actioned[key] = true
			r.mu.Unlock()
			if !done {
				if err := r.Trip(ctx, TripBilledExceeds, map[string]any{"window_start": w.start, "cause": "late_increase"}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type billBatch struct {
	id        string
	created   time.Time
	finished  *time.Time
	idleS     int
	submitted int
	state     string
}

func (r *Reconciler) unreconciled(ctx context.Context) ([]billBatch, error) {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT b.id, b.created_at, b.finished_at, b.idle_s, b.state,
		(SELECT COUNT(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = b.id AND a.state <> 'rejected')
		FROM batches b WHERE b.reconciled_at IS NULL AND b.state IN ('active','finished') ORDER BY b.created_at, b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []billBatch
	for rows.Next() {
		var b billBatch
		var cr string
		var fin sql.NullString
		if err := rows.Scan(&b.id, &cr, &fin, &b.idleS, &b.state, &b.submitted); err != nil {
			return nil, err
		}
		b.created, b.finished = mustT(cr), parseT(fin)
		out = append(out, b)
	}
	return out, rows.Err()
}

// windowEnd picks the latest hour boundary B at which the window [start, B) is quiet (C76.5 step 4).
func (r *Reconciler) windowEnd(ctx context.Context, now, start time.Time, batches []billBatch) (time.Time, bool, error) {
	maxB := floorHour(now.Add(-r.o.BillingLag))
	loc := r.o.Location
	s := start.In(loc)
	nm := time.Date(s.Year(), s.Month()+1, 1, 0, 0, 0, 0, loc).UTC()
	monthCap := floorHour(nm)
	if monthCap.Before(nm) {
		monthCap = monthCap.Add(time.Hour)
	}
	for b := maxB; b.After(start); b = b.Add(-time.Hour) {
		if nm.Before(b) {
			// the window never crosses a calendar month; a bucket belongs to the month of its start
			if b = monthCap; !b.After(start) {
				break
			}
		}
		if b.Equal(monthCap) {
			// D-M3-37: the month boundary closes the window even when a batch straddles it; closeWindow carries
			// that batch (its provisional cost and release) into the following window.
			return b, true, nil
		}
		ok := true
		for _, bt := range batches {
			if !bt.created.Before(b) || bt.submitted == 0 {
				continue
			}
			if bt.finished == nil || bt.finished.Add(time.Duration(bt.idleS)*time.Second+windowQuiet).After(b) {
				ok = false
				break
			}
		}
		if ok {
			var n int
			if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE created_at >= ? AND created_at < ?`,
				store.FormatTime(b.Add(-windowQuiet)), store.FormatTime(b)).Scan(&n); err != nil {
				return time.Time{}, false, err
			}
			ok = n == 0
		}
		if ok {
			return b, true, nil
		}
	}
	return time.Time{}, false, nil
}

// closeWindow closes one quiet window if there is one and reports whether it did (C76.5 steps 4 to 7).
func (r *Reconciler) closeWindow(ctx context.Context, now time.Time) (bool, error) {
	batches, err := r.unreconciled(ctx)
	if err != nil || len(batches) == 0 {
		return false, err
	}
	var lastEnd sql.NullString
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT MAX(window_end) FROM billing_windows`).Scan(&lastEnd); err != nil {
		return false, err
	}
	start := floorHour(batches[0].created)
	if lastEnd.Valid {
		start = mustT(lastEnd.String)
	}
	end, ok, err := r.windowEnd(ctx, now, start, batches)
	if err != nil || !ok {
		return false, err
	}
	var in, carried []billBatch
	for _, b := range batches {
		if !b.created.Before(end) {
			continue
		}
		switch {
		case b.finished != nil && (b.submitted == 0 || !b.finished.Add(time.Duration(b.idleS)*time.Second+windowQuiet).After(end)):
			in = append(in, b)
		case b.submitted > 0:
			// D-M3-37: active across the month end that bounds this window. It is neither billed against nor
			// released here; its reserve still bounds the window's compute and the following window settles it.
			carried = append(carried, b)
		}
	}
	if len(in) == 0 && len(carried) == 0 {
		return false, nil
	}
	sum, buckets, err := r.latestBuckets(ctx, r.o.Store.R, start, end)
	if err != nil {
		return false, err
	}
	var provisional, reserved int64
	for _, b := range in {
		var p, rs int64
		if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN kind = 'settle' THEN micro_usd END), 0), COALESCE(SUM(CASE WHEN kind = 'reserve' THEN micro_usd END), 0) FROM ledger WHERE batch_id = ?`, b.id).Scan(&p, &rs); err != nil {
			return false, err
		}
		provisional, reserved = provisional+p, reserved+rs
	}
	for _, b := range carried {
		var rs int64
		if err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, b.id).Scan(&rs); err != nil {
			return false, err
		}
		reserved += rs
	}
	verdict := "ok"
	switch {
	case int64(sum.compute) > reserved:
		verdict = "billed_exceeds_reserved"
	default:
		all, err := r.activities(ctx)
		if err != nil {
			return false, err
		}
		for _, bk := range buckets {
			if bk.compute <= 0 {
				continue
			}
			if !overlapsActivity(bk.start, all) {
				verdict = "spend_without_jobs"
				break
			}
		}
	}
	hours := max(1, int(end.Sub(start)/time.Hour))
	ws := store.FormatTime(start)
	billed := int64(sum.total)
	err = r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.o.Runner.fence()
		//nolint:gosec // G202: the fixed C69 predicate, values are bound
		if _, err := tx.ExecContext(ctx, `INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd,
			provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at) SELECT ?, ?, 'hour', ?, ?, ?, ?, ?, ?, ?, ? WHERE `+f.Where(),
			append([]any{ws, store.FormatTime(end), hours, billed, int64(sum.compute), provisional, reserved, len(in), verdict, store.FormatTime(now)}, f.Args()...)...); err != nil {
			return fmt.Errorf("jobs: billing window: %w", err)
		}
		if err := r.o.Runner.ledgerAppend(ctx, tx, f, spend.Entry{Kind: spend.KindBill, MicroUSD: money.MicroUSD(billed - provisional), WindowStart: &ws,
			PrincipalID: spend.BillingPrincipal, Reason: "reconcile"}); err != nil {
			return err
		}
		for _, b := range in {
			if _, err := r.o.Spend.ReconcileBatch(ctx, tx, b.id, "reconciled", f); err != nil {
				return err
			}
		}
		return r.auditTx(ctx, tx, f, "reconciler:billing_window", map[string]any{"window_start": ws, "batches": len(in), "verdict": verdict}, money.MicroUSD(billed))
	})
	if err != nil {
		return false, err
	}
	r.o.Runner.changed()
	switch verdict {
	case "billed_exceeds_reserved":
		err = r.Trip(ctx, TripBilledExceeds, map[string]any{"window_start": ws})
	case "spend_without_jobs":
		err = r.Trip(ctx, TripSpendWithoutJobs, map[string]any{"window_start": ws})
	}
	return true, err
}

type activity struct{ from, to time.Time }

// activities lists the activity window [created_at, finished_at + idle_s + 120 s] of
// every batch that reached the provider (an unfinished batch is active until now).
func (r *Reconciler) activities(ctx context.Context) ([]activity, error) {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT b.created_at, b.finished_at, b.idle_s FROM batches b
		WHERE EXISTS (SELECT 1 FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = b.id AND a.state <> 'rejected')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activity
	for rows.Next() {
		var cr string
		var fin sql.NullString
		var idle int
		if err := rows.Scan(&cr, &fin, &idle); err != nil {
			return nil, err
		}
		a := activity{from: mustT(cr)}
		if f := parseT(fin); f != nil {
			a.to = f.Add(time.Duration(idle)*time.Second + windowQuiet)
		} else {
			a.to = r.o.Clock.Now().Add(time.Hour)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func overlapsActivity(bucketStart time.Time, as []activity) bool {
	end := bucketStart.Add(time.Hour)
	for _, a := range as {
		if a.from.Before(end) && a.to.After(bucketStart) {
			return true
		}
	}
	return false
}

// stalled pushes one ntfy per 72 h when finished batches wait for a quiet window that never comes.
func (r *Reconciler) stalled(ctx context.Context, now time.Time) error {
	var oldest sql.NullString
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT MIN(finished_at) FROM batches WHERE state = 'finished' AND reconciled_at IS NULL`).Scan(&oldest); err != nil {
		return err
	}
	if !oldest.Valid {
		return nil
	}
	base := mustT(oldest.String)
	var closed sql.NullString
	if err := r.o.Store.R.QueryRowContext(ctx, `SELECT MAX(fetched_at) FROM billing_windows`).Scan(&closed); err != nil {
		return err
	}
	if closed.Valid {
		if c := mustT(closed.String); c.After(base) {
			base = c
		}
	}
	if now.Sub(base) < stalledAfter {
		return nil
	}
	bs, err := r.unreconciled(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(bs))
	for _, b := range bs {
		if len(ids) < 10 {
			ids = append(ids, b.id)
		}
	}
	r.notifyOnce(ctx, "billing_stalled", stalledAfter, NoteBillingStalled(r.o.PublicURL, ids))
	return nil
}

// holdRelease releases the outstanding of batches finished more than ResidualHold ago when billing cannot
// be read (C76.5 "none"): one transaction per batch, reason `hold_elapsed`, and a weekly reminder.
func (r *Reconciler) holdRelease(ctx context.Context, now time.Time) error {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT id FROM batches WHERE state = 'finished' AND reconciled_at IS NULL AND finished_at <= ? ORDER BY finished_at`,
		store.FormatTime(now.Add(-r.o.ResidualHold)))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		err := r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
			f := r.o.Runner.fence()
			out, err := r.o.Spend.ReconcileBatch(ctx, tx, id, "hold_elapsed", f)
			if err != nil {
				return err
			}
			return r.auditTx(ctx, tx, f, "reconciler:hold_release", map[string]any{"batch": id}, out)
		})
		if errors.Is(err, spend.ErrInvalidTransition) {
			continue
		}
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.holdFreed++
		r.mu.Unlock()
		r.o.Runner.changed()
	}
	r.mu.Lock()
	freed := r.holdFreed
	r.mu.Unlock()
	if freed > 0 && r.notifyOnce(ctx, "billing_unreadable", weeklyReminder, NoteBillingUnreadable(r.o.PublicURL, freed)) {
		r.mu.Lock()
		r.holdFreed = 0
		r.mu.Unlock()
	}
	return nil
}
