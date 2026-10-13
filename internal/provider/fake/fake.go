// Package fake is the in-process provider.Backend used by dev, E2E, the servertest rigs, acceptance and
// the property suite, plus the simulated worker (M3 C78). M3-B2 freezes Options, the counters and the
// Backend method set; M3-B7 replaces the STUB bodies (workers, billing, fault markers, SimWorker).
package fake

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ErrNotImplemented marks a STUB(M3-B7) body.
var ErrNotImplemented = errors.New("fake: not implemented (STUB(M3-B7))")

// Speed selects how long simulated queue and run phases take.
type Speed struct {
	// Slow, when positive, is the run duration; zero means fast (queue 0.2 s, run 1 s).
	Slow time.Duration
}

// Permissions switch off what the configured keys would allow.
type Permissions struct {
	PatchForbidden, PatchFails, BillingForbidden, AccessRevoked bool
}

// Adversarial switches (C86.3 b); the zero value is the friendly fake used by dev and E2E.
type Adversarial struct {
	NoWarmReuse, ExtraWorkersPerJob, CancelAckButKeepRunning, Requeue, PurgeResultsAfter30m bool
	AcceptThenFailRun                                                                       float64 // fraction of /run that fail after the job was accepted
	RevisedBucketsAfter                                                                     time.Duration
	PriceRiseAt                                                                             *time.Time
	IdleDriftTo, MaxDriftTo                                                                 int
}

// SimWorker simulates the worker handler (C78.3). STUB(M3-B7): a placeholder type so Options is frozen.
type SimWorker struct{}

// Options builds a Backend.
type Options struct {
	Clock         clock.Clock
	Speed         Speed
	Worker        *SimWorker
	PricePPH      money.MicroUSD // list price; default 690000
	WorkersMax    int            // default 2
	IdleS         int            // default 20
	ColdStartS    int            // default 3 in fast mode
	KillLatencyMS int64
	Permissions   Permissions
	Adversarial   Adversarial
}

// Counters count backend calls (every method, atomically).
type Counters struct {
	Quote, Submit, Poll, Cancel, Sweep, PurgeQueue, ScaleToZero, RevertDrift, Billing int64
}

// Total is the sum of every call.
func (c Counters) Total() int64 {
	return c.Quote + c.Submit + c.Poll + c.Cancel + c.Sweep + c.PurgeQueue + c.ScaleToZero + c.RevertDrift + c.Billing
}

// Backend is the fake provider.
type Backend struct {
	o Options
	n struct{ quote, submit, poll, cancel, sweep, purge, scale, revert, billing atomic.Int64 }
}

var _ provider.Backend = (*Backend)(nil)

// New builds the backend with the C78.1 defaults filled in.
func New(o Options) *Backend {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.PricePPH == 0 {
		o.PricePPH = 690000
	}
	if o.WorkersMax == 0 {
		o.WorkersMax = 2
	}
	if o.IdleS == 0 {
		o.IdleS = 20
	}
	if o.ColdStartS == 0 {
		o.ColdStartS = 3
	}
	return &Backend{o: o}
}

// Counters returns the call counts so far.
func (b *Backend) Counters() Counters {
	return Counters{Quote: b.n.quote.Load(), Submit: b.n.submit.Load(), Poll: b.n.poll.Load(), Cancel: b.n.cancel.Load(), Sweep: b.n.sweep.Load(),
		PurgeQueue: b.n.purge.Load(), ScaleToZero: b.n.scale.Load(), RevertDrift: b.n.revert.Load(), Billing: b.n.billing.Load()}
}

// AccruedCost is the ground-truth cost of every simulated worker lifetime up to t. STUB(M3-B7).
func (b *Backend) AccruedCost(t time.Time) money.MicroUSD { return 0 }

// AccruedCostByBatch attributes accrued cost to batches (C86.3). STUB(M3-B7).
func (b *Backend) AccruedCostByBatch(t time.Time) map[string]money.MicroUSD { return nil }

// Name implements provider.Backend.
func (b *Backend) Name() string { return provider.BackendFake }

// Quote implements provider.Backend. STUB(M3-B7).
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	b.n.quote.Add(1)
	return provider.Quote{}, ErrNotImplemented
}

// Submit implements provider.Backend. STUB(M3-B7).
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	b.n.submit.Add(1)
	return provider.Ref{}, ErrNotImplemented
}

// Poll implements provider.Backend. STUB(M3-B7).
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	b.n.poll.Add(1)
	return provider.RemoteStatus{}, ErrNotImplemented
}

// Cancel implements provider.Backend. STUB(M3-B7).
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error {
	b.n.cancel.Add(1)
	return ErrNotImplemented
}

// Sweep implements provider.Backend. STUB(M3-B7).
func (b *Backend) Sweep(ctx context.Context) (provider.SweepReport, error) {
	b.n.sweep.Add(1)
	return provider.SweepReport{}, ErrNotImplemented
}

// PurgeQueue implements provider.Backend. STUB(M3-B7).
func (b *Backend) PurgeQueue(ctx context.Context) error {
	b.n.purge.Add(1)
	return ErrNotImplemented
}

// ScaleToZero implements provider.Backend. STUB(M3-B7).
func (b *Backend) ScaleToZero(ctx context.Context) error {
	b.n.scale.Add(1)
	return ErrNotImplemented
}

// RevertDrift implements provider.Backend. STUB(M3-B7).
func (b *Backend) RevertDrift(ctx context.Context, d []provider.Drift) error {
	b.n.revert.Add(1)
	return ErrNotImplemented
}

// Billing implements provider.Backend. STUB(M3-B7).
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	b.n.billing.Add(1)
	return nil, ErrNotImplemented
}

// Capabilities implements provider.Backend: the fake can do everything.
func (b *Backend) Capabilities() provider.Capabilities {
	return provider.Capabilities{ScaleToZero: !b.o.Permissions.PatchForbidden, RevertDrift: !b.o.Permissions.PatchForbidden,
		Billing: map[bool]string{true: "none", false: "hour"}[b.o.Permissions.BillingForbidden]}
}
