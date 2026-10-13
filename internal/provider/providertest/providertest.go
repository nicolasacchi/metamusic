// Package providertest is a scripted provider.Backend with call counters, for the runner, spend and
// generation tests. It is test-only: TestNoTestHelpersInBinary keeps it out of the production binary.
package providertest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Script overrides the default behaviour of individual methods; a nil func keeps the default.
type Script struct {
	Quote        func(ctx context.Context) (provider.Quote, error)
	Submit       func(ctx context.Context, t provider.TakeSpec) (provider.Ref, error)
	Poll         func(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error)
	Cancel       func(ctx context.Context, r provider.Ref) error
	Sweep        func(ctx context.Context) (provider.SweepReport, error)
	PurgeQueue   func(ctx context.Context) error
	ScaleToZero  func(ctx context.Context) error
	RevertDrift  func(ctx context.Context, d []provider.Drift) error
	Billing      func(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error)
	Capabilities *provider.Capabilities
}

// Calls counts backend calls per method.
type Calls struct {
	Quote, Submit, Poll, Cancel, Sweep, PurgeQueue, ScaleToZero, RevertDrift, Billing int
}

// Total is the sum of every call.
func (c Calls) Total() int {
	return c.Quote + c.Submit + c.Poll + c.Cancel + c.Sweep + c.PurgeQueue + c.ScaleToZero + c.RevertDrift + c.Billing
}

// Backend is the scripted backend. It is safe for concurrent use.
type Backend struct {
	name  string
	s     Script
	panic bool

	mu    sync.Mutex
	calls Calls
	specs []provider.TakeSpec
	seq   int
}

var _ provider.Backend = (*Backend)(nil)

// New builds a scripted backend named name ("fake" or "runpod-serverless").
func New(name string, s Script) *Backend { return &Backend{name: name, s: s} }

// NewPanicking builds a backend whose every method panics (after counting the call): a test that
// builds services over it proves the exercised path never calls the provider.
func NewPanicking() *Backend { return NewPanickingNamed(provider.BackendFake) }

// NewPanickingNamed is NewPanicking under another backend name (provider.BackendRunPod).
func NewPanickingNamed(name string) *Backend { return &Backend{name: name, panic: true} }

// GoodQuote is a quote that passes every C77.5 check for the default configuration (cold start 180 s,
// job timeout 15 min).
func GoodQuote(backend string, at time.Time) provider.Quote {
	idle := 20
	return provider.Quote{Backend: backend, ListPricePPH: 690000, Pools: []string{"AMPERE_24"}, GPUClasses: []string{"A5000"}, ObservedAt: at,
		Endpoint: provider.EndpointConfig{Name: "mm-yue2", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: "240", WorkersMax: 2, IdleTimeoutS: &idle,
			TimeoutMS: 900000, GPUCount: 1, Pools: []string{"AMPERE_24"}, Flashboot: "FLASHBOOT",
			RequestURLs: map[string]string{"run": "https://api.runpod.ai/v2/endpoint/run"}}}
}

func (b *Backend) enter(f func(*Calls)) {
	b.mu.Lock()
	f(&b.calls)
	b.mu.Unlock()
}

func (b *Backend) boom(m string) {
	if b.panic {
		panic("providertest: " + m + " called on a panicking backend")
	}
}

// Calls returns the call counts so far.
func (b *Backend) Calls() Calls {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// Specs returns a copy of every submitted TakeSpec, in order.
func (b *Backend) Specs() []provider.TakeSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]provider.TakeSpec(nil), b.specs...)
}

// Name implements provider.Backend.
func (b *Backend) Name() string { return b.name }

// Quote implements provider.Backend.
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	b.enter(func(c *Calls) { c.Quote++ })
	b.boom("Quote")
	if b.s.Quote != nil {
		return b.s.Quote(ctx)
	}
	return GoodQuote(b.name, time.Now()), nil
}

// Submit implements provider.Backend; the spec is recorded before the script runs.
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	b.mu.Lock()
	b.calls.Submit++
	b.specs = append(b.specs, t)
	b.seq++
	n := b.seq
	b.mu.Unlock()
	b.boom("Submit")
	if b.s.Submit != nil {
		return b.s.Submit(ctx, t)
	}
	return provider.Ref{Backend: b.name, ID: fmt.Sprintf("ref-%d", n)}, nil
}

// Poll implements provider.Backend; the default answer is Queued.
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	b.enter(func(c *Calls) { c.Poll++ })
	b.boom("Poll")
	if b.s.Poll != nil {
		return b.s.Poll(ctx, r)
	}
	return provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"}, nil
}

// Cancel implements provider.Backend.
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error {
	b.enter(func(c *Calls) { c.Cancel++ })
	b.boom("Cancel")
	if b.s.Cancel != nil {
		return b.s.Cancel(ctx, r)
	}
	return nil
}

// Sweep implements provider.Backend.
func (b *Backend) Sweep(ctx context.Context) (provider.SweepReport, error) {
	b.enter(func(c *Calls) { c.Sweep++ })
	b.boom("Sweep")
	if b.s.Sweep != nil {
		return b.s.Sweep(ctx)
	}
	return provider.SweepReport{ObservedAt: time.Now()}, nil
}

// PurgeQueue implements provider.Backend.
func (b *Backend) PurgeQueue(ctx context.Context) error {
	b.enter(func(c *Calls) { c.PurgeQueue++ })
	b.boom("PurgeQueue")
	if b.s.PurgeQueue != nil {
		return b.s.PurgeQueue(ctx)
	}
	return nil
}

// ScaleToZero implements provider.Backend.
func (b *Backend) ScaleToZero(ctx context.Context) error {
	b.enter(func(c *Calls) { c.ScaleToZero++ })
	b.boom("ScaleToZero")
	if b.s.ScaleToZero != nil {
		return b.s.ScaleToZero(ctx)
	}
	return nil
}

// RevertDrift implements provider.Backend.
func (b *Backend) RevertDrift(ctx context.Context, d []provider.Drift) error {
	b.enter(func(c *Calls) { c.RevertDrift++ })
	b.boom("RevertDrift")
	if b.s.RevertDrift != nil {
		return b.s.RevertDrift(ctx, d)
	}
	return nil
}

// Billing implements provider.Backend.
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	b.enter(func(c *Calls) { c.Billing++ })
	b.boom("Billing")
	if b.s.Billing != nil {
		return b.s.Billing(ctx, from, to)
	}
	return nil, nil
}

// Capabilities implements provider.Backend; it is not counted (it reads configuration only).
func (b *Backend) Capabilities() provider.Capabilities {
	if b.s.Capabilities != nil {
		return *b.s.Capabilities
	}
	return provider.Capabilities{ScaleToZero: true, RevertDrift: true, Billing: "hour"}
}
