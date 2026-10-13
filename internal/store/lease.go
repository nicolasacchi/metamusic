package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Runner lease fencing (M3 D-M3-30, C69). Every runner and reconciler write
// carries the lease predicate; a statement that affects zero rows because of
// it means the writer lost the lease and must stop.

var (
	// ErrLeaseLost: the writer no longer holds the runner lease (or it expired).
	ErrLeaseLost = errors.New("store: runner lease lost")
	// ErrLeaseShort: the lease is held but has too little time left to start a
	// network call (the heartbeat renews first).
	ErrLeaseShort = errors.New("store: runner lease too short for a network call")
)

// LeaseMargin is the lease time that must remain before a transaction that
// will be followed by a network call (C69).
const LeaseMargin = 30 * time.Second

// leaseExists is the C69 predicate body: bind owner, then now.
const leaseExists = `EXISTS (SELECT 1 FROM runner_lease WHERE id = 1 AND owner = ? AND expires_at > ?)`

// LeaseFence carries the lease owner and the transaction's notion of now into
// spend, upload and jobs statements.
type LeaseFence struct {
	Owner string
	Now   time.Time
}

// Where is the predicate without a leading AND, for `INSERT … SELECT … WHERE`.
func (f LeaseFence) Where() string { return leaseExists }

// And is the predicate for appending to an existing WHERE clause.
func (f LeaseFence) And() string { return " AND " + leaseExists }

// Args are the bind values of Where/And, in order: owner, now (FormatTime).
func (f LeaseFence) Args() []any { return []any{f.Owner, FormatTime(f.Now)} }

// Fenced interprets the result of a fenced write: it returns the number of rows
// changed. A caller that expected a change and got none re-reads to tell a lost
// lease (ErrLeaseLost, via Confirm) from another writer winning the job.
func (f LeaseFence) Fenced(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Querier is the read side shared by *sql.DB and *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Confirm checks, through q, that the fence's lease is held with more than
// margin left: ErrLeaseLost when it is not held at all, ErrLeaseShort when it
// is held but about to expire. Use LeaseMargin before a network call.
func (f LeaseFence) Confirm(ctx context.Context, q Querier, margin time.Duration) error {
	var exp string
	err := q.QueryRowContext(ctx, `SELECT expires_at FROM runner_lease WHERE id = 1 AND owner = ? AND expires_at > ?`,
		f.Owner, FormatTime(f.Now)).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	t, err := ParseTime(exp)
	if err != nil {
		return err
	}
	if t.Sub(f.Now) <= margin {
		return ErrLeaseShort
	}
	return nil
}
