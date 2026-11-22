package pods

import (
	"context"
	"database/sql"
	"errors"
)

// Querier is the read side of *sql.DB and *sql.Tx (it is jobs.Querier's method set; the app adapts the two, because this package does not
// import jobs).
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Meter is the pod backend's overhead meter (C393.5): the batch's billable pod seconds beyond the executions the server measured.
type Meter struct{}

// OverheadSeconds is Σ lifetime_s of the batch's pods minus Σ ceil(execution_ms / 1000) of the assignments handed to them, floored at 0. It
// is ready only when every pod of the batch is terminated with a lifetime: while one may still be billing the runner does not finish the
// batch. A batch with no pod row is ready with 0 seconds (nothing was ever created).
func (Meter) OverheadSeconds(ctx context.Context, q Querier, batchID string) (int64, bool, error) {
	var total, live, unmeasured int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(lifetime_s), 0), COALESCE(SUM(state <> 'terminated'), 0),
		COALESCE(SUM(state = 'terminated' AND lifetime_s IS NULL), 0) FROM pods WHERE batch_id = ?`, batchID).Scan(&total, &live, &unmeasured)
	if err != nil {
		return 0, false, err
	}
	if live > 0 || unmeasured > 0 {
		return 0, false, nil
	}
	var exec int64
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM((execution_ms + 999) / 1000), 0) FROM pod_assignments
		WHERE batch_id = ? AND pod_id IS NOT NULL AND execution_ms IS NOT NULL`, batchID).Scan(&exec)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	return max(total-exec, 0), true, nil
}
