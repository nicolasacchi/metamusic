package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Settlement bases.
const (
	BasisMeasured  = "measured"   // the provider's executionTime
	BasisTimeBound = "time_bound" // timeout_s + KillSlackS
)

// AttemptSettle is the input of the one settlement helper (C72.5).
type AttemptSettle struct {
	BatchID, JobID, AttemptID, PrincipalID string
	Basis                                  string
	ExecutionMS                            *int64 // measured basis only
	TimeoutS, KillSlackS                   int
	PricePPH                               money.MicroUSD
	Lease                                  store.LeaseFence
}

// ErrAlreadySettled means the attempt was settled before.
var ErrAlreadySettled = errors.New("spend: attempt already settled")

// SettleResult is what SettleAttempt wrote.
type SettleResult struct {
	Provisional, Settled, Excess money.MicroUSD
	Reason                       string
}

// SettleAttempt writes the provisional settlement of an attempt in the caller's transaction: settle =
// min(provisional, remaining execution pot of the job), plus an excess row for the rest, and marks the
// attempt settled with the lease predicate. It is the only writer of execution, execution_unknown and
// excess rows.
func (s *Service) SettleAttempt(ctx context.Context, tx *sql.Tx, a AttemptSettle) (SettleResult, error) {
	var r SettleResult
	switch a.Basis {
	case BasisMeasured:
		if a.ExecutionMS == nil || *a.ExecutionMS < 0 {
			return r, fmt.Errorf("%w: measured settlement needs execution_ms", ErrBadEntry)
		}
		r.Provisional = money.ForMillis(*a.ExecutionMS, a.PricePPH)
		r.Reason = "execution"
	case BasisTimeBound:
		r.Provisional = money.ForSeconds(int64(a.TimeoutS+a.KillSlackS), a.PricePPH)
		r.Reason = "execution_unknown"
	default:
		return r, fmt.Errorf("%w: settlement basis %q", ErrBadEntry, a.Basis)
	}
	//nolint:gosec // G202: fixed C69 predicate, values are bound
	res, err := tx.ExecContext(ctx, `UPDATE job_attempts SET settled = 1 WHERE id = ? AND job_id = ? AND settled = 0`+a.Lease.And(),
		append([]any{a.AttemptID, a.JobID}, a.Lease.Args()...)...)
	n, err := a.Lease.Fenced(res, err)
	if err != nil {
		return r, fmt.Errorf("spend: settle attempt: %w", err)
	}
	if n == 0 {
		if cerr := a.Lease.Confirm(ctx, tx, 0); cerr != nil {
			return r, cerr
		}
		var settled int
		if err := tx.QueryRowContext(ctx, `SELECT settled FROM job_attempts WHERE id = ? AND job_id = ?`, a.AttemptID, a.JobID).Scan(&settled); errors.Is(err, sql.ErrNoRows) {
			return r, ErrNotFound
		} else if err != nil {
			return r, err
		}
		return r, ErrAlreadySettled
	}
	rem, err := executionRemaining(ctx, tx, a.JobID)
	if err != nil {
		return r, err
	}
	r.Settled = min(r.Provisional, max(rem, 0))
	r.Excess = r.Provisional - r.Settled
	es := []Entry{{Kind: KindSettle, MicroUSD: r.Settled, BatchID: &a.BatchID, JobID: &a.JobID, AttemptID: &a.AttemptID, PrincipalID: a.PrincipalID, Reason: r.Reason}}
	if r.Excess > 0 {
		es = append(es, Entry{Kind: KindSettle, MicroUSD: r.Excess, BatchID: &a.BatchID, JobID: &a.JobID, AttemptID: &a.AttemptID, PrincipalID: a.PrincipalID, Reason: "excess"})
	}
	return r, s.ledger.append(ctx, tx, &a.Lease, es)
}
