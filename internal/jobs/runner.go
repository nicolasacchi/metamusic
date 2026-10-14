package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// RunnerOptions builds the runner (C75.3).
type RunnerOptions struct {
	Store   *store.Store
	Backend provider.Backend
	Engines engine.Registry
	Spend   *spend.Service
	Uploads *upload.Service
	Ingest  Ingester
	Notify  notify.Notifier
	Audit   *audit.Store
	Hub     *Hub // nil => none
	Clock   clock.Clock
	NewID   func() string
	Owner   string // random instance id, never a hostname

	PublicURL              string
	TTL, MaxRuntime        time.Duration
	ColdStartS, KillSlackS int
	Expect                 provider.WorkerExpect
	Log                    *slog.Logger
	IngestWorkers          int // bounded pool for ingest + Ears; default 2
	// Preflight checks disk and the public upload route before a claim (D-M3-33); nil in unit tests.
	Preflight func(ctx context.Context, inFlightMaxBytes int64) error
	// Fault injects a crash at a named point (C86.3); nil in production.
	Fault func(point string) error

	// PriceMarginPPM is added to the cached list price when the claim compares it with the batch's frozen
	// price (MM_PRICE_MARGIN_PPM, D-M3-16).
	PriceMarginPPM int64
	// NeverRan pins the provider-terminal shapes that prove an attempt never ran (C73.2 rules 4 and 5);
	// the zero value pins nothing, so every such case is settled at its time bound.
	NeverRan NeverRanShapes
	// Meter attributes worker-lifetime overhead to a finishing batch (C76.6); nil means no worker is ever
	// seen and the overhead settles at zero.
	Meter BatchMeter
	// Trip opens a provider trip inside the caller's transaction (C76.3). nil writes the trip row and
	// leaves its actions to the reconciler.
	Trip TripFunc
	// MeterKey derives worker_key from a provider worker id (C76.6); nil leaves worker_key empty.
	MeterKey []byte
}

// TripFunc opens a provider trip for reason inside tx under the lease fence.
type TripFunc func(ctx context.Context, tx *sql.Tx, f store.LeaseFence, backend, reason string, details map[string]any) error

// StepReport counts what one runner tick did.
type StepReport struct{ Claimed, Polled, Settled, Finished int }

// Fault points (C75.3): a Fault func returning an error aborts the step as a crash would.
const (
	FaultAfterClaimCommit   = "after_claim_commit"
	FaultAfterSubmitReturn  = "after_submit_return"
	FaultAfterCompletedSeen = "after_completed_seen"
	FaultMidSettle          = "mid_settle"
	FaultMidIngest          = "mid_ingest"
	FaultAfterCancelPost    = "after_cancel_post"
	FaultAfterReserveCommit = "after_reserve_commit"
)

// Runner executes the job state machine.
type Runner struct {
	o RunnerOptions

	mu         sync.Mutex
	held       bool
	leaseUntil time.Time
	wakeCh     chan struct{}
	pool       *pool

	lastPoll     map[string]time.Time // attempt id -> last poll
	holdPoll     map[string]time.Time // attempt id -> no poll before (rate limit)
	lastCancel   map[string]time.Time // attempt id -> last Cancel POST
	cancelWarned map[string]bool      // job id -> the 5-minute ntfy went out
	seenResult   map[string]bool      // upload id -> mm-result.json already handled
	promoted     map[string]bool      // attempt ids promoted during the current step
	idleWarned   map[string]bool      // batch id -> idle drift ntfy sent
	preflightAt  time.Time
	preflightErr error
	preflightMsg map[string]time.Time // check -> last ntfy
	notes        []notify.Message
	reserved     bool         // a top-up reserve was written by the transaction in progress
	settled      atomic.Int64 // attempts finished (and settled) since the runner started
}

// NewRunner builds the runner.
func NewRunner(o RunnerOptions) *Runner {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.IngestWorkers <= 0 {
		o.IngestWorkers = 2
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Notify == nil {
		o.Notify = notify.Nop{}
	}
	if o.NewID == nil {
		o.NewID = newUUID
	}
	return &Runner{
		o: o, wakeCh: make(chan struct{}, 1), pool: newPool(o.IngestWorkers),
		lastPoll: map[string]time.Time{}, holdPoll: map[string]time.Time{}, lastCancel: map[string]time.Time{}, cancelWarned: map[string]bool{},
		seenResult: map[string]bool{}, promoted: map[string]bool{}, idleWarned: map[string]bool{}, preflightMsg: map[string]time.Time{},
	}
}

// UseMeter wires the worker-lifetime meter after the reconciler is built (the reconciler needs the runner,
// the runner asks the meter); call it before Run.
func (r *Runner) UseMeter(m BatchMeter) { r.o.Meter = m }

// Wake nudges the runner without blocking; other packages see it only as an injected func().
func (r *Runner) Wake() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

// Drain waits until the ingest pool is idle (tests; shutdown).
func (r *Runner) Drain(ctx context.Context) error { return r.pool.drain(ctx) }

func (r *Runner) fault(point string) error {
	if r.o.Fault == nil {
		return nil
	}
	return r.o.Fault(point)
}

func (r *Runner) changed() {
	if r.o.Hub != nil {
		r.o.Hub.Notify()
	}
}

// tx runs fn in one write transaction through spend.RunTx (so the ledger observer fires) and nudges
// waiters after a commit.
func (r *Runner) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	r.mu.Lock()
	r.reserved = false
	r.mu.Unlock()
	if err := r.o.Spend.RunTx(ctx, fn); err != nil {
		return err
	}
	r.changed()
	r.mu.Lock()
	reserved := r.reserved
	r.reserved = false
	r.mu.Unlock()
	if reserved {
		return r.fault(FaultAfterReserveCommit) // a top-up reserve just committed
	}
	return nil
}

// Step performs one tick (tests drive it directly); it never blocks on ingest.
func (r *Runner) Step(ctx context.Context) (rep StepReport, err error) {
	if err := r.ensureLease(ctx); err != nil {
		return rep, err
	}
	r.mu.Lock()
	r.promoted = map[string]bool{}
	r.mu.Unlock()
	defer r.flushNotes(ctx)
	settled0 := r.settled.Load()
	defer func() { rep.Settled = int(r.settled.Load() - settled0) }()
	phases := []func(context.Context, *StepReport) error{
		r.adopt, r.phaseApprovals, r.phasePromote, r.phaseResults, r.phaseCancel, r.phaseFailQueued,
		r.phaseClaim, r.phasePoll, r.phaseTransfer, r.phaseDeadlines, r.phaseCloseWindows, r.phaseFailQueued,
		r.phaseFinish,
	}
	for _, p := range phases {
		if err := p(ctx, &rep); err != nil {
			return rep, r.stepErr(err)
		}
	}
	return rep, nil
}

// stepErr normalises a phase error: a lost lease also drops the cached lease state.
func (r *Runner) stepErr(err error) error {
	if errors.Is(err, store.ErrLeaseLost) {
		r.setHeld(false, time.Time{})
	}
	return err
}

// Run acquires and keeps the runner lease and ticks until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = r.Drain(dctx)
		r.releaseLease(dctx)
	}()
	for ctx.Err() == nil {
		ok, err := r.acquire(ctx)
		if err != nil {
			r.o.Log.Warn("runner lease", "err", err.Error())
		}
		if !ok {
			if !r.sleep(ctx, HeartbeatEvery) {
				return nil
			}
			continue
		}
		r.hold(ctx)
	}
	return nil
}

// sleep waits d on the injected clock; false when ctx ended first.
func (r *Runner) sleep(ctx context.Context, d time.Duration) bool {
	t := r.o.Clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}

// hold ticks while the lease is held; it returns when the lease is lost or ctx ends. The heartbeat runs
// in its own goroutine so nothing inside Step can lose the lease.
func (r *Runner) hold(ctx context.Context) {
	hctx, stop := context.WithCancel(ctx)
	lost := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.heartbeatLoop(hctx, lost)
	}()
	defer func() {
		stop()
		wg.Wait()
	}()
	for {
		if _, err := r.stepOnce(ctx); err != nil {
			if errors.Is(err, store.ErrLeaseLost) || errors.Is(err, ErrNotHolder) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			r.o.Log.Warn("runner step", "err", err.Error())
		}
		t := r.o.Clock.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-lost:
			t.Stop()
			return
		case <-r.wakeCh:
			t.Stop()
		case <-t.C():
		}
	}
}

// stepOnce is Step without re-acquiring: the heartbeat goroutine keeps the lease while Run ticks.
func (r *Runner) stepOnce(ctx context.Context) (StepReport, error) { return r.Step(ctx) }

func (r *Runner) heartbeatLoop(ctx context.Context, lost chan<- struct{}) {
	for r.sleep(ctx, HeartbeatEvery) {
		if err := r.heartbeat(ctx); err != nil {
			if errors.Is(err, store.ErrLeaseLost) {
				close(lost)
				return
			}
			r.o.Log.Warn("runner heartbeat", "err", err.Error())
		}
	}
}

type setCol struct {
	col string
	val any
}

func set(col string, val any) setCol { return setCol{col, val} }

// moveJob applies one row of the transition table to j under the lease predicate and appends its event.
// 0 rows means another writer won (errStale) or the lease is gone (ErrLeaseLost).
func (r *Runner) moveJob(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, to string, ev Event, sets ...setCol) error {
	if err := CanTransition(j.State, to); err != nil {
		return err
	}
	ts := store.FormatTime(f.Now)
	cols := []string{"state = ?", "updated_at = ?"}
	args := []any{to, ts}
	switch {
	case IsTerminal(to):
		cols = append(cols, "finished_at = ?", "lease_owner = NULL", "lease_expires_at = NULL")
		args = append(args, ts)
	case to == StateQueued:
		cols = append(cols, "lease_owner = NULL", "lease_expires_at = NULL")
	case IsLeased(to) && !IsLeased(j.State):
		cols = append(cols, "lease_owner = ?", "lease_expires_at = ?")
		args = append(args, r.o.Owner, store.FormatTime(r.leaseExpiry()))
	}
	for _, s := range sets {
		cols = append(cols, s.col+" = ?")
		args = append(args, s.val)
	}
	where := " WHERE id = ? AND state = ?"
	args = append(args, j.ID, j.State)
	if IsLeased(j.State) {
		where += " AND lease_owner = ?"
		args = append(args, r.o.Owner)
	} else {
		where += " AND lease_owner IS NULL"
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET `+strings.Join(cols, ", ")+where+f.And(), append(args, f.Args()...)...) //nolint:gosec // G202: column names are constants of this package
	n, err := f.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("jobs: move %s %s -> %s: %w", j.ID, j.State, to, err)
	}
	if n == 0 {
		if cerr := f.Confirm(ctx, tx, 0); cerr != nil {
			return cerr
		}
		return errStale
	}
	if ev.Name != "" {
		ev.JobID, ev.From, ev.To = j.ID, j.State, to
		if err := insertEvent(ctx, tx, f, ev); err != nil {
			return err
		}
	}
	j.State = to
	if IsTerminal(to) {
		j.LeaseOwner = ""
	}
	return nil
}

// setJob updates columns of a job that do not change its state (counters, deadlines).
func (r *Runner) setJob(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, sets ...setCol) error {
	cols := []string{"updated_at = ?"}
	args := []any{store.FormatTime(f.Now)}
	for _, s := range sets {
		cols = append(cols, s.col+" = ?")
		args = append(args, s.val)
	}
	where := " WHERE id = ? AND state = ?"
	args = append(args, j.ID, j.State)
	if IsLeased(j.State) {
		where += " AND lease_owner = ?"
		args = append(args, r.o.Owner)
	} else {
		where += " AND lease_owner IS NULL"
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET `+strings.Join(cols, ", ")+where+f.And(), append(args, f.Args()...)...) //nolint:gosec // G202: column names are constants of this package
	n, err := f.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("jobs: update %s: %w", j.ID, err)
	}
	if n == 0 {
		if cerr := f.Confirm(ctx, tx, 0); cerr != nil {
			return cerr
		}
		return errStale
	}
	return nil
}

// skipStale lets a phase continue with the next job when this one changed under it.
func skipStale(err error) error {
	if errors.Is(err, errStale) {
		return nil
	}
	return err
}
