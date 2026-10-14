// Package fake is the in-process provider.Backend used by dev, E2E, the servertest rigs, acceptance and
// the property suite, plus the simulated worker (M3 C78).
//
// The simulation is lazy and deterministic: every method first advances a virtual timeline (queue, cold
// start, execution, idle, billing) to the injected clock's current time, then acts at that time. There are
// no goroutines or timers, so a manual clock drives it exactly and the outcome never depends on how often
// it is polled. Handlers called by the simulated worker (the start and upload endpoints) must not call the
// backend synchronously: the backend lock is held while they run.
package fake

import (
	"context"
	_ "embed"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

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

// Options builds a Backend.
type Options struct {
	Clock         clock.Clock
	Speed         Speed
	Worker        *SimWorker     // nil: jobs complete with no output and upload nothing
	PricePPH      money.MicroUSD // list price; default 690000
	WorkersMax    int            // default 2
	IdleS         int            // default from canary-measured.json (20)
	ColdStartS    int            // default from canary-measured.json (3)
	KillLatencyMS int64          // executionTime overshoot at a timeout kill; default from canary-measured.json
	Permissions   Permissions
	Adversarial   Adversarial
	// BatchOf maps a job id to its batch for AccruedCostByBatch (TakeSpec carries no batch). Nil means each
	// job is its own batch.
	BatchOf func(jobID string) string
}

// Canary is the content of canary-measured.json: the values the real canary measured, which the friendly
// fake uses as defaults. The committed file holds PLACEHOLDERS until M3-X4 (Measured is false).
type Canary struct {
	Measured      bool  `json:"measured"`
	ColdStartS    int   `json:"cold_start_s"`
	IdleS         int   `json:"idle_s"`
	KillLatencyMS int64 `json:"kill_latency_ms"`
	WorkersPerJob int   `json:"workers_per_job"`
	IdleBilled    bool  `json:"idle_billed"`
}

//go:embed canary-measured.json
var canaryJSON []byte

var (
	canaryOnce sync.Once
	canary     Canary
)

// CanaryDefaults returns the committed canary values.
func CanaryDefaults() Canary {
	canaryOnce.Do(func() {
		if err := json.Unmarshal(canaryJSON, &canary); err != nil {
			panic("fake: canary-measured.json: " + err.Error()) // a committed file; caught by tests
		}
	})
	return canary
}

// Counters count backend calls (every method, atomically).
type Counters struct {
	Quote, Submit, Poll, Cancel, Sweep, PurgeQueue, ScaleToZero, RevertDrift, Billing int64
}

// Total is the sum of every call.
func (c Counters) Total() int64 {
	return c.Quote + c.Submit + c.Poll + c.Cancel + c.Sweep + c.PurgeQueue + c.ScaleToZero + c.RevertDrift + c.Billing
}

const (
	// imageDigest is the simulated worker image (sha256: plus 64 zeros).
	imageDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	// endpointTimeoutMS is the simulated endpoint execution timeout (the default MM_JOB_MAX_RUNTIME).
	endpointTimeoutMS = 15 * 60 * 1000
)

// Backend is the fake provider.
type Backend struct {
	o Options
	n struct{ quote, submit, poll, cancel, sweep, purge, scale, revert, billing atomic.Int64 }

	mu       sync.Mutex
	now      time.Time // the simulation's current time (never moves back)
	seq      int
	jobs     []*job
	byID     map[string]*job
	attempts map[string]int
	workers  []*worker
	wseq     int
	// the endpoint as it currently is, and as the operator wants it
	maxW, idleS             int
	desiredMax, desiredIdle int
	drifted                 bool
}

var _ provider.Backend = (*Backend)(nil)

// New builds the backend with the C78.1 defaults filled in.
func New(o Options) *Backend {
	c := CanaryDefaults()
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
		o.IdleS = c.IdleS
	}
	if o.ColdStartS == 0 {
		o.ColdStartS = c.ColdStartS
	}
	if o.KillLatencyMS == 0 {
		o.KillLatencyMS = c.KillLatencyMS
	}
	return &Backend{o: o, now: o.Clock.Now(), byID: map[string]*job{}, attempts: map[string]int{},
		maxW: o.WorkersMax, desiredMax: o.WorkersMax, idleS: o.IdleS, desiredIdle: o.IdleS}
}

// Options returns the options with every default filled in.
func (b *Backend) Options() Options { return b.o }

// Counters returns the call counts so far.
func (b *Backend) Counters() Counters {
	return Counters{Quote: b.n.quote.Load(), Submit: b.n.submit.Load(), Poll: b.n.poll.Load(), Cancel: b.n.cancel.Load(), Sweep: b.n.sweep.Load(),
		PurgeQueue: b.n.purge.Load(), ScaleToZero: b.n.scale.Load(), RevertDrift: b.n.revert.Load(), Billing: b.n.billing.Load()}
}

// Name implements provider.Backend.
func (b *Backend) Name() string { return provider.BackendFake }

func (b *Backend) revoked() bool { return b.o.Permissions.AccessRevoked }

// enter takes the lock and advances the simulation to the clock's now.
func (b *Backend) enter(ctx context.Context) time.Time {
	b.mu.Lock()
	now := b.o.Clock.Now()
	b.advance(ctx, now)
	return b.now
}

// Quote implements provider.Backend.
func (b *Backend) Quote(ctx context.Context) (provider.Quote, error) {
	b.n.quote.Add(1)
	if b.revoked() {
		return provider.Quote{}, provider.ErrAccess
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	return provider.Quote{
		Backend: provider.BackendFake, ListPricePPH: b.priceAt(now),
		Pools: []string{"AMPERE_24"}, GPUClasses: []string{"NVIDIA RTX A5000"},
		Endpoint: b.endpointConfig(), ObservedAt: now,
	}, nil
}

func (b *Backend) endpointConfig() provider.EndpointConfig {
	idle := b.idleS
	return provider.EndpointConfig{
		Name: "mm-fake", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: strconv.Itoa(b.o.ColdStartS + 60),
		WorkersMin: 0, WorkersMax: b.maxW, IdleTimeoutS: &idle, TimeoutMS: endpointTimeoutMS, GPUCount: 1, MinCUDA: "12.8",
		Pools: []string{"AMPERE_24"}, Flashboot: "true", ImageDigest: imageDigest,
	}
}

// Capabilities implements provider.Backend: the fake can do everything its permissions allow.
func (b *Backend) Capabilities() provider.Capabilities {
	return provider.Capabilities{ScaleToZero: !b.o.Permissions.PatchForbidden, RevertDrift: !b.o.Permissions.PatchForbidden,
		Billing: map[bool]string{true: "none", false: "hour"}[b.o.Permissions.BillingForbidden]}
}

// Sweep implements provider.Backend.
func (b *Backend) Sweep(ctx context.Context) (provider.SweepReport, error) {
	b.n.sweep.Add(1)
	if b.revoked() {
		return provider.SweepReport{}, provider.ErrAccess
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	var ws []provider.Worker
	for _, w := range b.workers {
		if !w.alive() {
			continue
		}
		pw := provider.Worker{ID: w.id, Status: w.status(now), Image: imageDigest}
		st := w.start
		pw.StartedAt = &st
		if !now.Before(w.ready) {
			u := int(now.Sub(w.start) / time.Second)
			pw.UptimeS = &u
		}
		ws = append(ws, pw)
	}
	ep := b.endpointConfig()
	var drift []provider.Drift
	for _, d := range b.driftReport() {
		drift = append(drift, provider.Drift{Field: d.field, Want: d.want, Got: d.got})
	}
	skew := time.Duration(0)
	return provider.SweepReport{Workers: ws, Endpoint: &ep, Drift: drift, ObservedAt: now, Skew: &skew}, nil
}

// ScaleToZero implements provider.Backend: workers.max = 0; idle workers stop at once, busy ones when
// their job ends.
func (b *Backend) ScaleToZero(ctx context.Context) error {
	b.n.scale.Add(1)
	if b.revoked() {
		return provider.ErrAccess
	}
	if err := b.patchErr(); err != nil {
		return err
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	b.maxW, b.desiredMax = 0, 0
	for _, w := range b.workers {
		if w.alive() && (w.stuck || (w.job == nil && !w.phantom)) {
			w.stop = now
		}
	}
	return nil
}

// RevertDrift implements provider.Backend: restores the Want values of the reported fields.
func (b *Backend) RevertDrift(ctx context.Context, d []provider.Drift) error {
	b.n.revert.Add(1)
	if b.revoked() {
		return provider.ErrAccess
	}
	if err := b.patchErr(); err != nil {
		return err
	}
	b.enter(ctx)
	defer b.mu.Unlock()
	for _, x := range d {
		n, err := strconv.Atoi(x.Want)
		if err != nil {
			continue
		}
		switch x.Field {
		case "workers.max":
			b.maxW = n
		case "workers.idleTimeout":
			b.idleS = n
		}
	}
	return nil
}

func (b *Backend) patchErr() error {
	switch {
	case b.o.Permissions.PatchForbidden:
		return provider.ErrNotPermitted
	case b.o.Permissions.PatchFails:
		return errPatchFailed
	}
	return nil
}
