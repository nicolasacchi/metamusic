package jobs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Settlement bases of finishAttempt.
const (
	basisMeasured  = "measured"   // the provider's executionTime
	basisTimeBound = "time_bound" // timeout_s + KillSlackS
	basisNeverRan  = "never_ran"  // the attempt provably never ran: nothing to settle
)

func moneyForTimeout(timeoutS, killSlackS int, price money.MicroUSD) money.MicroUSD {
	return money.ForSeconds(int64(timeoutS+killSlackS), price)
}

// workerKey is hex HMAC-SHA256(MeterKey, worker id): the raw id is never stored (C76.6).
func workerKey(key []byte, id string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))
}

// finishAttempt turns a live attempt `done` and settles it exactly once in the caller's transaction
// (C76.2): `execution` from the provider's executionTime, `execution_unknown` at the time bound, or
// nothing for an attempt that provably never ran. terminalBasis is "provider" or "time_bound". The uploads
// of the attempt are closed unless it delivered (keepUploads, or a result_received/completed_remote event).
func (r *Runner) finishAttempt(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec, a *attRec, basis string, exec *int64, terminalBasis string, keepUploads bool) error {
	ts := store.FormatTime(f.Now)
	switch basis {
	case basisNeverRan:
		zero := int64(0)
		exec = &zero
		if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'done', terminal_basis = ?, terminal_at = ?, execution_ms = 0, settled = 1", terminalBasis, ts); err != nil {
			return err
		}
	case basisMeasured:
		if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'done', terminal_basis = ?, terminal_at = ?, execution_ms = ?", terminalBasis, ts, *exec); err != nil {
			return err
		}
	default:
		if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'done', terminal_basis = ?, terminal_at = ?", terminalBasis, ts); err != nil {
			return err
		}
	}
	a.State, a.TerminalBasis, a.ExecutionMS = "done", terminalBasis, exec
	r.settled.Add(1)
	d := map[string]any{"basis": basis}
	if basis != basisNeverRan {
		in := spend.AttemptSettle{BatchID: b.ID, JobID: j.ID, AttemptID: a.ID, PrincipalID: b.PrincipalID, TimeoutS: j.TimeoutS,
			KillSlackS: b.KillSlackS, PricePPH: a.Price, Lease: f}
		if basis == basisMeasured {
			in.Basis, in.ExecutionMS = spend.BasisMeasured, exec
		} else {
			in.Basis = spend.BasisTimeBound
		}
		res, err := r.o.Spend.SettleAttempt(ctx, tx, in)
		if err != nil {
			return err
		}
		d["micro_usd"], d["settle_reason"] = int64(res.Settled), res.Reason
		if res.Excess > 0 {
			d["excess_micro_usd"] = int64(res.Excess)
			if err := r.trip(ctx, tx, f, b.Backend, "provisional_exceeds_reserved", map[string]any{"cause": "excess"}); err != nil {
				return err
			}
		}
		if exec != nil {
			d["execution_ms"] = *exec
		}
		if err := r.fault(FaultMidSettle); err != nil {
			return err
		}
	}
	if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvSettled, Detail: d}); err != nil {
		return err
	}
	var delivering int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE attempt_id = ? AND event IN ('result_received','completed_remote')`, a.ID).Scan(&delivering); err != nil {
		return err
	}
	if delivering == 0 && !keepUploads {
		return r.o.Uploads.CloseAttempt(ctx, tx, a.ID)
	}
	return nil
}

// conclude ends a cancelling or failing job once every attempt is terminal (rows 22 and 24).
func (r *Runner) conclude(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec) error {
	atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
	if err != nil {
		return err
	}
	basis := "provider"
	for _, a := range atts {
		if a.live() {
			return nil
		}
		if a.TerminalBasis == "time_bound" {
			basis = "time_bound"
		}
	}
	switch j.State {
	case StateCancelling:
		if err := r.moveJob(ctx, tx, f, j, StateCancelled, Event{Name: EvCancelConfirmed, Code: j.ErrorCode, Detail: map[string]any{"basis": basis}}); err != nil {
			return err
		}
	case StateFailing:
		if err := r.moveJob(ctx, tx, f, j, StateFailed, Event{Name: EvFailed, Code: j.ErrorCode}); err != nil {
			return err
		}
	default:
		return nil
	}
	return r.releaseNeverRan(ctx, tx, f, j, b)
}

// phaseCloseWindows closes attempts whose upload window has ended (C76.2): such an attempt cannot spend
// any more, so it becomes `done` with the time-bound basis and settles `execution_unknown`.
func (r *Runner) phaseCloseWindows(ctx context.Context, rep *StepReport) error {
	now := r.o.Clock.Now()
	atts, err := queryAtts(ctx, r.o.Store.R, `state IN ('pending','submitted','unknown') AND window_ends_at <= ? ORDER BY window_ends_at`, store.FormatTime(now))
	if err != nil {
		return err
	}
	for _, c := range atts {
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			as, err := queryAtts(ctx, tx, `id = ?`, c.ID)
			if err != nil || len(as) == 0 || !as[0].live() {
				return err
			}
			a := as[0]
			j, err := loadJob(ctx, tx, a.JobID)
			if err != nil {
				return err
			}
			b, err := loadBatch(ctx, tx, j.BatchID)
			if err != nil {
				return err
			}
			if err := r.finishAttempt(ctx, tx, f, &j, b, &a, basisTimeBound, nil, "time_bound", false); err != nil {
				return err
			}
			var settled int64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE attempt_id = ? AND kind = 'settle'`, a.ID).Scan(&settled); err != nil {
				return err
			}
			if err := r.auditTx(ctx, tx, f, "reconciler:settle_time_bound", map[string]any{"job": j.ID, "attempt": a.ID}, money.MicroUSD(settled)); err != nil {
				return err
			}
			switch j.State {
			case StateCancelling, StateFailing:
				return r.conclude(ctx, tx, f, &j, b)
			case StateStarting, StateGenerating:
				others, err := liveOthers(ctx, tx, j.ID, a.ID)
				if err != nil {
					return err
				}
				if len(others) > 0 {
					return r.moveJob(ctx, tx, f, &j, StateFailing, Event{AttemptID: a.ID, Name: EvLost, Code: CodeWorkerLost}, set("error_code", CodeWorkerLost))
				}
				return r.moveJob(ctx, tx, f, &j, StateFailed, Event{AttemptID: a.ID, Name: EvLost, Code: CodeWorkerLost}, set("error_code", CodeWorkerLost))
			}
			return nil
		})
		if err := skipStale(err); err != nil {
			return err
		}
	}
	return nil
}

// tripSQL is the trip insert up to its lease predicate.
const tripSQL = `INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json)
	SELECT ?, ?, ?, ?, ?, '[]' WHERE `

// trip opens a provider trip inside tx (C76.3). Without an injected TripFunc it writes the trip row
// (one open trip per backend) and leaves the actions (cancel, purge, scale to zero, ntfy) to the
// reconciler, which acts on trips whose actions_json is empty.
func (r *Runner) trip(ctx context.Context, tx *sql.Tx, f store.LeaseFence, backend, reason string, details map[string]any) error {
	if r.o.Trip != nil {
		return r.o.Trip(ctx, tx, f, backend, reason, details)
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	args := []any{r.o.NewID(), backend, store.FormatTime(f.Now), reason, string(raw)}
	q := tripSQL + f.Where() + ` ON CONFLICT DO NOTHING` //nolint:gosec // G202: fixed C69 predicate
	res, err := tx.ExecContext(ctx, q, append(args, f.Args()...)...)
	if _, err := f.Fenced(res, err); err != nil {
		return fmt.Errorf("jobs: trip: %w", err)
	}
	return nil
}

// tripProvider opens a trip in its own transaction.
func (r *Runner) tripProvider(ctx context.Context, reason string, details map[string]any) error {
	return r.tx(ctx, func(tx *sql.Tx) error {
		return r.trip(ctx, tx, r.fence(), r.o.Backend.Name(), reason, details)
	})
}

// settledSince is a small helper for tests and the batch summary: Σ settle rows of a batch.
func settledOf(ctx context.Context, x q, batchID string) (money.MicroUSD, error) {
	var v int64
	err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'settle'`, batchID).Scan(&v)
	return money.MicroUSD(v), err
}

var _ = time.Second
