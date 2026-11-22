package nightshift

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/schedule"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Run states as stored (C377.1). A run is claimed `planned` and ends in exactly one of the others; `created` may later become `halted`.
const (
	StatePlanned = "planned"
	StateCreated = "created"
	StateSkipped = "skipped"
	StateHalted  = "halted"
	StateFailed  = "failed"
)

// Run is one row of night_shift_runs.
type Run struct {
	ID, Night, State               string
	Reason, PlanJSON               string
	EstimateID, BatchID, MandateID string
	CreatedAt, UpdatedAt           time.Time
}

const runCols = `id, night, state, COALESCE(reason,''), COALESCE(plan_json,''), COALESCE(estimate_id,''), COALESCE(batch_id,''), COALESCE(mandate_id,''), created_at, updated_at`

const runColsR = `r.id, r.night, r.state, COALESCE(r.reason,''), COALESCE(r.plan_json,''), COALESCE(r.estimate_id,''), COALESCE(r.batch_id,''), COALESCE(r.mandate_id,''), r.created_at, r.updated_at`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var c, u string
	if err := sc.Scan(&r.ID, &r.Night, &r.State, &r.Reason, &r.PlanJSON, &r.EstimateID, &r.BatchID, &r.MandateID, &c, &u); err != nil {
		return Run{}, err
	}
	var err error
	if r.CreatedAt, err = store.ParseTime(c); err != nil {
		return Run{}, err
	}
	r.UpdatedAt, err = store.ParseTime(u)
	return r, err
}

func (r *Runner) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// RunOf reads the run of one night; ok is false when there is none.
func (r *Runner) RunOf(ctx context.Context, night string) (Run, bool, error) {
	run, err := scanRun(r.o.Store.R.QueryRowContext(ctx, `SELECT `+runCols+` FROM night_shift_runs WHERE night = ?`, night))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	return run, err == nil, err
}

// Runs lists the newest runs first (at most limit, default 14).
func (r *Runner) Runs(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 14
	}
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT `+runCols+` FROM night_shift_runs ORDER BY night DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// claim inserts the night's planned row; false means the night is already claimed (another process, or an earlier run).
func (r *Runner) claim(ctx context.Context, night string) (id string, won bool, err error) {
	id = r.o.NewID()
	now := store.FormatTime(r.o.Now())
	res, err := r.o.Store.W.ExecContext(ctx, `INSERT INTO night_shift_runs(id, night, state, created_at, updated_at) VALUES(?,?, 'planned', ?, ?)
		ON CONFLICT(night) DO NOTHING`, id, night, now, now)
	if err != nil {
		return "", false, err
	}
	n, err := res.RowsAffected()
	return id, n == 1, err
}

// finish moves a planned run to a final state with its reason.
func (r *Runner) finish(ctx context.Context, id, state, reason string) error {
	_, err := r.o.Store.W.ExecContext(ctx, `UPDATE night_shift_runs SET state = ?, reason = ?, updated_at = ? WHERE id = ? AND state = 'planned'`,
		state, nullIf(reason), store.FormatTime(r.o.Now()), id)
	return err
}

func (r *Runner) setPlan(ctx context.Context, id, planJSON string) error {
	_, err := r.o.Store.W.ExecContext(ctx, `UPDATE night_shift_runs SET plan_json = ?, updated_at = ? WHERE id = ? AND state = 'planned'`,
		planJSON, store.FormatTime(r.o.Now()), id)
	return err
}

func (r *Runner) setEstimate(ctx context.Context, id, estimateID string) error {
	_, err := r.o.Store.W.ExecContext(ctx, `UPDATE night_shift_runs SET estimate_id = ?, updated_at = ? WHERE id = ? AND state = 'planned'`,
		estimateID, store.FormatTime(r.o.Now()), id)
	return err
}

// adopt records the batch the night created (or that a crashed run created before it could record it).
func (r *Runner) adopt(ctx context.Context, id, batchID, mandateID string) error {
	_, err := r.o.Store.W.ExecContext(ctx, `UPDATE night_shift_runs SET state = 'created', reason = NULL, batch_id = ?, mandate_id = ?, updated_at = ?
		WHERE id = ? AND state = 'planned'`, batchID, mandateID, store.FormatTime(r.o.Now()), id)
	return err
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Done reports whether the night of k already has a run row: the scheduler's catch-up asks it (D-M6N-13).
func (r *Runner) Done(ctx context.Context, k schedule.Key) (bool, error) {
	_, ok, err := r.RunOf(ctx, k.Day)
	return ok, err
}

// unfinished lists the runs whose batch has not reached a final state, oldest first.
func (r *Runner) unfinished(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, before string) ([]Run, error) {
	query := `SELECT ` + runColsR + ` FROM night_shift_runs r JOIN batches b ON b.id = r.batch_id
		WHERE r.state IN ('created','halted') AND b.state NOT IN ('finished','rejected','expired')`
	var args []any
	if before != "" {
		query += ` AND r.night < ?`
		args = append(args, before)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY r.night`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
