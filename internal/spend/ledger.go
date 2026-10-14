package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Ledger kinds.
const (
	KindReserve  = "reserve"
	KindSettle   = "settle"
	KindResidual = "residual"
	KindRelease  = "release"
	KindBill     = "bill"
)

// Pots (C72.5): the execution pot of a job and the overhead pot of a batch.
const (
	PotNone      = ""
	PotExecution = "execution"
	PotOverhead  = "overhead"
)

// BillingPrincipal owns every bill row.
const BillingPrincipal = "system:billing"

// Amount sign rules of a reason.
const (
	SignPositive    = "positive"    // > 0
	SignNonNegative = "nonnegative" // >= 0
	SignAny         = "any"         // bill adjustments may be negative
)

// ReasonInfo is one row of the ledger reason table (C72.5).
type ReasonInfo struct {
	Kind, Reason, Pot, Sign string
}

// ledgerReasons is the Go-validated table of C72.5.
var ledgerReasons = []ReasonInfo{
	{KindReserve, "take", PotExecution, SignPositive},
	{KindReserve, "attempt_overhead", PotOverhead, SignPositive},
	{KindReserve, "topup_ambiguous", PotExecution, SignPositive},
	{KindReserve, "topup_requeue", PotExecution, SignPositive},
	{KindReserve, "topup_price", PotExecution, SignPositive},
	{KindSettle, "execution", PotExecution, SignNonNegative},
	{KindSettle, "execution_unknown", PotExecution, SignNonNegative},
	{KindSettle, "excess", PotNone, SignPositive},
	{KindSettle, "overhead", PotOverhead, SignNonNegative},
	{KindResidual, "held", PotNone, SignNonNegative},
	{KindRelease, "never_ran", PotExecution, SignNonNegative},
	{KindRelease, "not_accepted", PotOverhead, SignNonNegative},
	{KindRelease, "rejected", PotNone, SignNonNegative},
	{KindRelease, "expired", PotNone, SignNonNegative},
	{KindRelease, "reconciled", PotNone, SignNonNegative},
	{KindRelease, "hold_elapsed", PotNone, SignNonNegative},
	{KindBill, "reconcile", PotNone, SignAny},
	{KindBill, "late_adjust", PotNone, SignAny},
}

// LedgerReasons returns a copy of the reason table.
func LedgerReasons() []ReasonInfo { return append([]ReasonInfo(nil), ledgerReasons...) }

func reasonInfo(kind, reason string) (ReasonInfo, bool) {
	for _, r := range ledgerReasons {
		if r.Kind == kind && r.Reason == reason {
			return r, true
		}
	}
	return ReasonInfo{}, false
}

// Errors of the ledger.
var (
	// ErrLedgerInvariant: a settle or release beyond what was reserved (per pot, job or batch).
	ErrLedgerInvariant = errors.New("spend: ledger invariant violated")
	// ErrBadEntry: an entry that breaks the reason table or its shape rules.
	ErrBadEntry = errors.New("spend: invalid ledger entry")
)

// Entry is one ledger row to append.
type Entry struct {
	Kind                                   string
	MicroUSD                               money.MicroUSD
	BatchID, JobID, AttemptID, WindowStart *string
	PrincipalID, Reason                    string
}

// Observer is a test hook called after every committed ledger transaction (C86.3).
type Observer func(Usage)

// Ledger appends rows and enforces the invariants. Observer calls fire from CommitTx.
type Ledger struct {
	now      func() time.Time
	observer Observer
	usage    func(ctx context.Context, principalKey string) (Usage, error)

	mu      sync.Mutex
	pending map[*sql.Tx]string // transactions that wrote ledger rows -> first principal
}

func newLedger(now func() time.Time, obs Observer, usage func(context.Context, string) (Usage, error)) *Ledger {
	return &Ledger{now: now, observer: obs, usage: usage, pending: map[*sql.Tx]string{}}
}

// Ledger returns the service's ledger.
func (s *Service) Ledger() *Ledger { return s.ledger }

func str(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func validate(e Entry) error {
	ri, ok := reasonInfo(e.Kind, e.Reason)
	if !ok {
		return fmt.Errorf("%w: %s/%s", ErrBadEntry, e.Kind, e.Reason)
	}
	switch ri.Sign {
	case SignPositive:
		if e.MicroUSD <= 0 {
			return fmt.Errorf("%w: %s/%s needs a positive amount", ErrBadEntry, e.Kind, e.Reason)
		}
	case SignNonNegative:
		if e.MicroUSD < 0 {
			return fmt.Errorf("%w: %s/%s needs a non-negative amount", ErrBadEntry, e.Kind, e.Reason)
		}
	}
	if e.Kind == KindBill {
		if e.PrincipalID != BillingPrincipal || e.WindowStart == nil || e.BatchID != nil || e.JobID != nil || e.AttemptID != nil {
			return fmt.Errorf("%w: bill rows carry %s, a window_start and no batch", ErrBadEntry, BillingPrincipal)
		}
		return nil
	}
	if e.BatchID == nil || e.PrincipalID == "" || e.WindowStart != nil {
		return fmt.Errorf("%w: %s/%s needs a batch and a principal", ErrBadEntry, e.Kind, e.Reason)
	}
	if ri.Pot == PotExecution && e.JobID == nil {
		return fmt.Errorf("%w: %s/%s needs a job", ErrBadEntry, e.Kind, e.Reason)
	}
	return nil
}

// Append validates and writes rows in the caller's transaction, then checks per pot, per job and per
// batch that Σ settle (except excess) + Σ release ≤ Σ reserve. A failure writes nothing.
func (l *Ledger) Append(ctx context.Context, tx *sql.Tx, es ...Entry) error {
	return l.append(ctx, tx, nil, es)
}

func (l *Ledger) append(ctx context.Context, tx *sql.Tx, lease *store.LeaseFence, es []Entry) error {
	for _, e := range es {
		if err := validate(e); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT ledger_append`); err != nil {
		return err
	}
	err := l.insertAll(ctx, tx, lease, es)
	if err != nil {
		_, _ = tx.ExecContext(ctx, `ROLLBACK TO ledger_append`)
	}
	if _, rerr := tx.ExecContext(ctx, `RELEASE ledger_append`); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		return err
	}
	if len(es) > 0 {
		l.mu.Lock()
		if _, ok := l.pending[tx]; !ok {
			l.pending[tx] = es[0].PrincipalID
		}
		l.mu.Unlock()
	}
	return nil
}

func (l *Ledger) insertAll(ctx context.Context, tx *sql.Tx, lease *store.LeaseFence, es []Entry) error {
	at := store.FormatTime(l.now())
	for _, e := range es {
		q := `INSERT INTO ledger(at, kind, micro_usd, batch_id, job_id, attempt_id, window_start, principal_id, reason)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?`
		args := []any{at, e.Kind, int64(e.MicroUSD), str(e.BatchID), str(e.JobID), str(e.AttemptID), str(e.WindowStart), e.PrincipalID, e.Reason}
		if lease != nil {
			q += ` WHERE ` + lease.Where() //nolint:gosec // G202: fixed C69 predicate, values are bound
			args = append(args, lease.Args()...)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		n, err := (store.LeaseFence{}).Fenced(res, err)
		if err != nil {
			return fmt.Errorf("spend: append ledger: %w", err)
		}
		if n == 0 {
			return store.ErrLeaseLost
		}
		if err := l.checkInvariants(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) checkInvariants(ctx context.Context, tx *sql.Tx, e Entry) error {
	ri, _ := reasonInfo(e.Kind, e.Reason)
	spends := (e.Kind == KindSettle && e.Reason != "excess") || e.Kind == KindRelease
	if !spends {
		return nil
	}
	var reserved, used int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN kind = 'reserve' THEN micro_usd END), 0),
		COALESCE(SUM(CASE WHEN (kind = 'settle' AND reason <> 'excess') OR kind = 'release' THEN micro_usd END), 0)
		FROM ledger WHERE batch_id = ?`, *e.BatchID).Scan(&reserved, &used); err != nil {
		return err
	}
	if used > reserved {
		return fmt.Errorf("%w: batch spent %d of %d reserved", ErrLedgerInvariant, used, reserved)
	}
	switch ri.Pot {
	case PotExecution:
		rem, err := executionRemaining(ctx, tx, *e.JobID)
		if err != nil {
			return err
		}
		if rem < 0 {
			return fmt.Errorf("%w: execution pot of the job overdrawn by %d", ErrLedgerInvariant, -rem)
		}
	case PotOverhead:
		rem, err := overheadRemaining(ctx, tx, *e.BatchID)
		if err != nil {
			return err
		}
		if rem < 0 {
			return fmt.Errorf("%w: overhead pot of the batch overdrawn by %d", ErrLedgerInvariant, -rem)
		}
	}
	return nil
}

// executionRemaining is reserves minus settles and releases of the job's execution pot (can go
// negative only transiently inside an invariant check).
func executionRemaining(ctx context.Context, q Querier, jobID string) (money.MicroUSD, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE
		WHEN kind = 'reserve' AND reason IN ('take','topup_ambiguous','topup_requeue','topup_price') THEN micro_usd
		WHEN kind = 'settle' AND reason IN ('execution','execution_unknown') THEN -micro_usd
		WHEN kind = 'release' AND reason = 'never_ran' THEN -micro_usd ELSE 0 END), 0)
		FROM ledger WHERE job_id = ?`, jobID).Scan(&v)
	return money.MicroUSD(v), err
}

func overheadRemaining(ctx context.Context, q Querier, batchID string) (money.MicroUSD, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE
		WHEN kind = 'reserve' AND reason = 'attempt_overhead' THEN micro_usd
		WHEN kind = 'settle' AND reason = 'overhead' THEN -micro_usd
		WHEN kind = 'release' AND reason = 'not_accepted' THEN -micro_usd ELSE 0 END), 0)
		FROM ledger WHERE batch_id = ?`, batchID).Scan(&v)
	return money.MicroUSD(v), err
}

// ExecutionRemaining is what is left of a job's execution pot.
func (l *Ledger) ExecutionRemaining(ctx context.Context, q Querier, jobID string) (money.MicroUSD, error) {
	return executionRemaining(ctx, q, jobID)
}

// OverheadRemaining is what is left of a batch's overhead pot.
func (l *Ledger) OverheadRemaining(ctx context.Context, q Querier, batchID string) (money.MicroUSD, error) {
	return overheadRemaining(ctx, q, batchID)
}

// Outstanding is a batch's reserved-but-unsettled amount, floored at zero.
func (l *Ledger) Outstanding(ctx context.Context, q Querier, batchID string) (money.MicroUSD, error) {
	return BatchOutstanding(ctx, q, batchID)
}

// RunTx runs fn in one write transaction on the writer pool. The observer (if any) is called after a
// commit that wrote ledger rows, never after a rollback.
func (s *Service) RunTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		s.ledger.discard(tx)
		_ = tx.Rollback()
		return err
	}
	return s.CommitTx(ctx, tx)
}

// CommitTx commits tx and then fires the observer when the transaction wrote ledger rows.
func (s *Service) CommitTx(ctx context.Context, tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		s.ledger.discard(tx)
		return err
	}
	s.ledger.fire(ctx, tx)
	return nil
}

// RollbackTx rolls tx back and forgets its pending observer call.
func (s *Service) RollbackTx(tx *sql.Tx) error {
	s.ledger.discard(tx)
	return tx.Rollback()
}

func (l *Ledger) discard(tx *sql.Tx) {
	l.mu.Lock()
	delete(l.pending, tx)
	l.mu.Unlock()
}

func (l *Ledger) fire(ctx context.Context, tx *sql.Tx) {
	l.mu.Lock()
	who, ok := l.pending[tx]
	delete(l.pending, tx)
	l.mu.Unlock()
	if !ok || l.observer == nil || l.usage == nil {
		return
	}
	if u, err := l.usage(ctx, who); err == nil {
		l.observer(u)
	}
}
