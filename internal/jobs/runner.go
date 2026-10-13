package jobs

import (
	"context"
	"log/slog"
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
}

// StepReport counts what one runner tick did.
type StepReport struct{ Claimed, Polled, Settled, Finished int }

// Runner executes the job state machine.
type Runner struct{ o RunnerOptions }

// NewRunner builds the runner.
func NewRunner(o RunnerOptions) *Runner {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.IngestWorkers <= 0 {
		o.IngestWorkers = 2
	}
	return &Runner{o: o}
}

// Run acquires and keeps the runner lease and ticks until ctx ends. STUB(M3-B5).
func (r *Runner) Run(ctx context.Context) error { return ErrNotImplemented }

// Step performs one tick (tests drive it directly); it never blocks on ingest. STUB(M3-B5).
func (r *Runner) Step(ctx context.Context) (StepReport, error) {
	return StepReport{}, ErrNotImplemented
}

// Drain waits until the ingest pool is idle. STUB(M3-B5).
func (r *Runner) Drain(ctx context.Context) error { return ErrNotImplemented }

// Wake nudges the runner without blocking; other packages see it only as an injected func().
func (r *Runner) Wake() {}

// Holding reports whether the lease is held with at least 30 s left. STUB(M3-B5).
func (r *Runner) Holding() bool { return false }
