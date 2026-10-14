package jobs

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// BatchMeter attributes worker-lifetime overhead to a finishing batch (C76.6). The reconciler owns the
// implementation; the runner only asks.
type BatchMeter interface {
	// OverheadSeconds returns the billed worker seconds attributed to the batch beyond its attempts'
	// execution. ready is false while an attributed worker lifetime may still be running, in which case
	// the runner does not finish the batch yet.
	OverheadSeconds(ctx context.Context, x Querier, batchID string) (seconds int64, ready bool, err error)
}

// Querier is the read side of *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// queueNote holds a notification until the transaction that caused it is over.
func (r *Runner) queueNote(m notify.Message) {
	r.mu.Lock()
	r.notes = append(r.notes, m)
	r.mu.Unlock()
}

func (r *Runner) flushNotes(ctx context.Context) {
	r.mu.Lock()
	notes := r.notes
	r.notes = nil
	r.mu.Unlock()
	for _, m := range notes {
		if err := r.o.Notify.Notify(ctx, m); err != nil {
			r.o.Log.Warn("notify", "title", m.Title)
		}
	}
}

// phaseFailQueued fails queued jobs that can never run (row 6): NOT_SUBMITTED after 24 h unclaimed,
// NO_CAPACITY after 24 h of resubmitting or the 7th queue expiry, BUDGET_EXCEEDED when an ambiguous
// attempt closed without its top-up.
func (r *Runner) phaseFailQueued(ctx context.Context, _ *StepReport) error {
	js, err := queryJobs(ctx, r.o.Store.R, `state = 'queued' ORDER BY created_at, take_index`)
	if err != nil {
		return err
	}
	for _, c := range js {
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil || j.State != StateQueued {
				return err
			}
			atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
			if err != nil {
				return err
			}
			if len(liveAttempts(atts)) > 0 {
				return nil
			}
			b, err := loadBatch(ctx, tx, j.BatchID)
			if err != nil {
				return err
			}
			switch {
			case j.CancelRequestedAt != nil:
				return nil // phaseCancel owns it
			case j.ErrorCode != "":
				// a submit refusal remembered while a ghost could still run (row 10 once nothing can spend)
				return r.failQueuedJob(ctx, tx, f, &j, j.ErrorCode, Event{Name: EvFailed, Code: j.ErrorCode})
			case j.FirstSubmit == nil && j.QueuedAt != nil && !f.Now.Before(j.QueuedAt.Add(noCapacityAfter)):
				return r.failQueuedJob(ctx, tx, f, &j, CodeNotSubmitted, Event{Name: EvNotSubmitted, Code: CodeNotSubmitted, Detail: map[string]any{"queued_at": store.FormatTime(*j.QueuedAt)}})
			case j.FirstSubmit != nil && !f.Now.Before(j.FirstSubmit.Add(noCapacityAfter)), j.QueueExpiries > MaxQueueExpiryResubmits:
				return r.failNoCapacity(ctx, tx, f, &j, b)
			}
			var ghost int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts g WHERE g.job_id = ?
				AND (g.state = 'unknown' OR (g.state = 'done' AND g.terminal_basis = 'time_bound'))
				AND NOT EXISTS (SELECT 1 FROM ledger l WHERE l.attempt_id = g.id AND l.reason = 'topup_ambiguous')`, j.ID).Scan(&ghost); err != nil {
				return err
			}
			if ghost > 0 {
				return r.failQueuedJob(ctx, tx, f, &j, CodeBudgetExceeded, Event{Name: EvFailed, Code: CodeBudgetExceeded})
			}
			return nil
		})
		if err := skipStale(err); err != nil {
			return err
		}
	}
	return nil
}

// failNoCapacity fails j with NO_CAPACITY and every never-submitted job of its batch with it (row 6b).
func (r *Runner) failNoCapacity(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec) error {
	if err := r.failQueuedJob(ctx, tx, f, j, CodeNoCapacity, Event{Name: EvFailed, Code: CodeNoCapacity}); err != nil {
		return err
	}
	rest, err := queryJobs(ctx, tx, `batch_id = ? AND state = 'queued' AND first_submitted_at IS NULL AND id <> ? ORDER BY take_index`, j.BatchID, j.ID)
	if err != nil {
		return err
	}
	var released money.MicroUSD
	for _, o := range rest {
		o := o
		before, err := r.o.Spend.Ledger().ExecutionRemaining(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if err := r.failQueuedJob(ctx, tx, f, &o, CodeNoCapacity, Event{Name: EvBatchNoCapacity, Code: CodeNoCapacity, Detail: map[string]any{"failed_jobs": 1}}); err != nil {
			return err
		}
		released += before
	}
	if len(rest) == 0 {
		return nil
	}
	return r.auditTx(ctx, tx, f, "runner:fail_remainder", map[string]any{"batch": b.ID, "jobs": len(rest)}, released)
}

// phaseFinish finishes batches whose jobs are all terminal and whose attempts are all done or rejected and
// settled: overhead settles from the meter, the residual is recorded, the batch is `finished` and one ntfy
// summary goes out (C75.3).
func (r *Runner) phaseFinish(ctx context.Context, rep *StepReport) error {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT b.id FROM batches b WHERE b.state = 'active'
		AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.batch_id = b.id AND j.state NOT IN ('completed','failed','cancelled','expired'))
		AND NOT EXISTS (SELECT 1 FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = b.id
		      AND (a.state IN ('pending','submitted','unknown') OR (a.state = 'done' AND a.settled = 0)))
		ORDER BY b.created_at`)
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
	rows.Close()
	for _, id := range ids {
		ok, err := r.finishBatch(ctx, id)
		if err != nil {
			return err
		}
		if ok {
			rep.Finished++
		}
	}
	return nil
}

func (r *Runner) finishBatch(ctx context.Context, batchID string) (bool, error) {
	var overhead int64
	if r.o.Meter != nil {
		secs, ready, err := r.o.Meter.OverheadSeconds(ctx, r.o.Store.R, batchID)
		if err != nil {
			return false, fmt.Errorf("jobs: meter: %w", err)
		}
		if !ready {
			return false, nil
		}
		overhead = secs
	}
	var summary notify.Message
	finished := false
	err := r.tx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		b, err := loadBatch(ctx, tx, batchID)
		if err != nil || b.State != "active" {
			return err
		}
		res, err := r.o.Spend.FinishBatch(ctx, tx, spend.FinishInput{BatchID: batchID, OverheadMicro: money.ForSeconds(overhead, b.Price), Lease: f})
		if err != nil {
			return err
		}
		settled, err := settledOf(ctx, tx, batchID)
		if err != nil {
			return err
		}
		if err := r.auditTx(ctx, tx, f, "runner:batch_finish", map[string]any{"batch": batchID, "overhead_micro_usd": int64(res.Overhead)}, settled); err != nil {
			return err
		}
		summary, err = r.batchSummary(ctx, tx, b, settled)
		finished = err == nil
		return err
	})
	if err != nil {
		return false, err
	}
	if finished {
		r.queueNote(summary)
	}
	return finished, nil
}

// batchSummary is the "Batch done" message of C82: "6 completed (1 truncated), 1 failed - $0.15 provisional".
func (r *Runner) batchSummary(ctx context.Context, x q, b batchRec, settled money.MicroUSD) (notify.Message, error) {
	var completed, truncated, failed, cancelled int
	err := x.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(state = 'completed'), 0), COALESCE(SUM(state = 'completed' AND outcome = 'truncated'), 0),
		COALESCE(SUM(state = 'failed'), 0), COALESCE(SUM(state IN ('cancelled','expired')), 0) FROM jobs WHERE batch_id = ?`, b.ID).
		Scan(&completed, &truncated, &failed, &cancelled)
	if err != nil {
		return notify.Message{}, err
	}
	body := fmt.Sprintf("%d completed", completed)
	if truncated > 0 {
		body += fmt.Sprintf(" (%d truncated)", truncated)
	}
	if failed > 0 {
		body += fmt.Sprintf(", %d failed", failed)
	}
	if cancelled > 0 {
		body += fmt.Sprintf(", %d cancelled", cancelled)
	}
	body += " - " + settled.Display() + " provisional"
	return notify.Message{Title: "Batch done", Body: body, Priority: 3, Click: r.o.PublicURL + "/batches/" + b.ID}, nil
}
