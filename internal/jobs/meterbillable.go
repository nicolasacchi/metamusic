package jobs

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// What worker_meter.uptime_s means (X3 canary #3, 2026-10-15).
//
// The RunPod listing's uptimeSeconds counts from the creation of the worker record. It includes time RunPod
// does not bill: the image pull, the cached-model download ("initializing model files", 46 min on a fresh
// host) and flashboot standby (about an hour), and RunPod keeps more than one standby record. Metering it
// tripped meter_exceeds_reserved five seconds after a batch was created.
//
// uptime_s is therefore BILLABLE SECONDS OBSERVED: a worker is billable while RUNNING, plus at most the
// endpoint idle timeout after its last RUNNING observation (RunPod bills the idle window before scale-down).
// INITIALIZING, THROTTLED, UNHEALTHY and IDLE beyond the idle timeout contribute nothing. The seconds are
// accumulated in memory from successive listings (each delta is the smaller of the uptime delta and the
// wall-clock delta) and written as a running total with max() semantics, so a retried transaction cannot
// double count and the monotonic trigger holds. A row exists only once the worker has been observed RUNNING,
// and started_at/first_seen_at/last_seen_at then span billable observation. The schema is unchanged.

// settingMeterBillableSince is the settings key holding the instant from which worker_meter rows are
// trusted: rows first seen earlier carry the listing's raw uptime (the pre-fix meaning) and are ignored by
// the meter, the trip test and the provider status.
const settingMeterBillableSince = "meter.billable_since"

// meterObs is the last listing observation of one worker and the billable seconds accumulated for it.
type meterObs struct {
	status    string // upper case
	uptimeS   int64
	at        time.Time
	billable  int64 // accumulated billable seconds (the value stored in worker_meter.uptime_s)
	idleLeft  int64 // idle seconds still billable after the last RUNNING observation
	started   time.Time
	hasRow    bool
	firstSeen bool // no previous observation in this process
}

type meterPlan struct {
	key, status, image string
	obs                *meterObs
	write              bool
}

// meterIdleS is the idle timeout that bounds the billable idle tail. The live endpoint configuration is what
// RunPod applies, so it is authoritative; batches.idle_s is only its snapshot from batch creation and serves
// when the sweep did not read the endpoint. MaxIdleTimeoutS is the ceiling of the last resort.
func (r *Reconciler) meterIdleS(ctx context.Context, ep *provider.EndpointConfig) int {
	if ep != nil && ep.IdleTimeoutS != nil && *ep.IdleTimeoutS > 0 {
		return min(*ep.IdleTimeoutS, MaxIdleTimeoutS)
	}
	var idle int
	err := r.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(MAX(idle_s), 0) FROM batches WHERE state = 'active' OR finished_at >= ?`,
		store.FormatTime(r.o.Clock.Now().Add(-time.Hour))).Scan(&idle)
	if err != nil || idle <= 0 {
		return MaxIdleTimeoutS
	}
	return min(idle, MaxIdleTimeoutS)
}

// planMeter computes, without side effects, the new observation of every listed worker and whether its
// worker_meter row is written.
func (r *Reconciler) planMeter(ctx context.Context, ws []provider.Worker, now time.Time, idleS int) ([]meterPlan, error) {
	since := r.billableSinceValue()
	var plan []meterPlan
	seen := map[string]bool{}
	for _, w := range ws {
		key := r.workerKeyOf(w.ID)
		if key == "" || w.UptimeS == nil || w.StartedAt == nil || seen[key] {
			continue
		}
		seen[key] = true
		st := strings.ToUpper(w.Status)
		up := int64(max(*w.UptimeS, 0))
		r.mu.Lock()
		var o meterObs
		prev := r.meterObs[key]
		if prev != nil {
			o = *prev
		}
		r.mu.Unlock()
		o.firstSeen = prev == nil
		if o.firstSeen {
			var started string
			var total int64
			err := r.o.Store.R.QueryRowContext(ctx, `SELECT started_at, uptime_s FROM worker_meter WHERE worker_key = ? AND first_seen_at >= ? ORDER BY started_at DESC LIMIT 1`,
				key, since).Scan(&started, &total)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				o.started, o.billable, o.hasRow = mustT(started), total, true
			}
		}
		var d int64
		switch {
		case o.firstSeen:
			// After a restart (or the first listing of a worker) only one sweep interval of RUNNING time is
			// credited: under-counting by a sweep is acceptable, over-counting unbilled time is the bug.
			if st == "RUNNING" {
				d = min(up, int64(meterSweepGap/time.Second))
			}
		default:
			d = min(max(up-o.uptimeS, 0), max(int64(now.Sub(o.at)/time.Second), 0))
			switch st {
			case "RUNNING":
				if o.status != "RUNNING" {
					d = min(d, int64(meterSweepGap/time.Second)) // the interval was mostly not billable
				}
			case "IDLE":
				d = min(d, o.idleLeft)
			default:
				d = 0
			}
		}
		switch st {
		case "RUNNING":
			o.idleLeft = int64(idleS)
		case "IDLE":
			if !o.firstSeen {
				o.idleLeft -= d
			} else {
				o.idleLeft = 0 // unknown since when it is idle: nothing more is credited
			}
		default:
			o.idleLeft = 0
		}
		o.billable += d
		o.status, o.uptimeS, o.at = st, up, now
		p := meterPlan{key: key, status: w.Status, image: w.Image, obs: &o}
		if st == "RUNNING" && !o.hasRow {
			started := *w.StartedAt
			var one int
			err := r.o.Store.R.QueryRowContext(ctx, `SELECT 1 FROM worker_meter WHERE worker_key = ? AND started_at = ?`, key, store.FormatTime(started)).Scan(&one)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				started = now // that lifetime is a legacy row (raw listing uptime): this one is a new row
			}
			o.started, o.hasRow = started, true
		}
		p.write = o.hasRow && (st == "RUNNING" || d > 0)
		plan = append(plan, p)
	}
	return plan, nil
}

// commitMeter makes the planned observations the in-memory state; workers absent from the listing are
// forgotten (their next appearance is a first observation).
func (r *Reconciler) commitMeter(plan []meterPlan) {
	next := make(map[string]*meterObs, len(plan))
	for _, p := range plan {
		next[p.key] = p.obs
	}
	r.mu.Lock()
	r.meterObs = next
	r.mu.Unlock()
}

// billableSinceValue is the cached meter.billable_since ("" when not set yet: nothing is excluded).
func (r *Reconciler) billableSinceValue() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.billableSince
}

// meterBillableSince reads the instant from which worker_meter rows are trusted; "" when unset.
func meterBillableSince(ctx context.Context, x Querier) (string, error) {
	var v string
	err := x.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingMeterBillableSince).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// ensureBillableSince, on the first sweep with the billable meter, records now as the instant before which
// worker_meter rows hold raw listing uptime, and audits it. It never deletes or rewrites rows.
func (r *Reconciler) ensureBillableSince(ctx context.Context, now time.Time) error {
	if r.billableSinceValue() != "" {
		return nil
	}
	v, err := meterBillableSince(ctx, r.o.Store.R)
	if err != nil {
		return err
	}
	if v == "" {
		ts := store.FormatTime(now)
		err = r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
			f := r.o.Runner.fence()
			res, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) SELECT ?, ? WHERE `+f.Where()+` ON CONFLICT(key) DO NOTHING`, //nolint:gosec // G202: fixed C69 predicate
				append([]any{settingMeterBillableSince, ts}, f.Args()...)...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				return r.auditTx(ctx, tx, f, "reconciler:meter_billable_since", map[string]any{"billable_since": ts}, 0)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if v, err = meterBillableSince(ctx, r.o.Store.R); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.billableSince = v
	r.mu.Unlock()
	return nil
}
