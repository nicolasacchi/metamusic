package fake

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// E2 (C151.1, C153.4): the Backend methods the Set routes by, and the ACE-Step instance. `fake` serves yue2 exactly
// as M3; `fake-acestep` is a second instance with its own name, engine, endpoint and worker set.

// NewACEStep builds the fake-acestep backend: the same simulation as New under the name provider.BackendFakeACEStep,
// serving engine acestep15. Give it a Worker built with Weights set to the ACE-Step weights to also answer
// WEIGHTS_MISMATCH.
func NewACEStep(o Options) *Backend {
	o.Name, o.Engine = provider.BackendFakeACEStep, provider.EngineACEStep
	return New(o)
}

// ColdStartS is the fake model's cold start allowance (the same value the endpoint config advertises).
func (b *Backend) ColdStartS() int { return b.o.ColdStartS }

// Engine is the engine this backend renders.
func (b *Backend) Engine() string {
	if b.o.Engine != "" {
		return b.o.Engine
	}
	return provider.EngineYuE2
}

// MaxRuntime is the endpoint execution timeout of the simulation.
func (b *Backend) MaxRuntime() time.Duration { return endpointTimeoutMS * time.Millisecond }

func (b *Backend) endpointName() string {
	if b.Engine() == provider.EngineACEStep {
		return "mm-fake-acestep"
	}
	return "mm-fake"
}

// slowLoad is the cold start of a worker whose job carries fake:slow_load: the explicit seconds, or by default the
// allowance plus two minutes (beyond the scaler window of allowance + 60 s).
func (b *Backend) slowLoad(m markers) time.Duration {
	if m.slowLoadFor > 0 {
		return m.slowLoadFor
	}
	return time.Duration(b.o.ColdStartS+120) * time.Second
}

func (b *Backend) hasUnhealthy() bool {
	for _, w := range b.workers {
		if w.unhealthy && w.alive() {
			return true
		}
	}
	return false
}

// healthLocked is the /health view of the simulation. It is reported only while an unhealthy worker exists, so a
// backend that never sees fake:init_fail sweeps exactly as before (Health nil).
func (b *Backend) healthLocked(now time.Time) *provider.Health {
	if !b.hasUnhealthy() {
		return nil
	}
	h := provider.Health{}
	for _, w := range b.workers {
		if !w.alive() {
			continue
		}
		switch w.status(now) {
		case "UNHEALTHY":
			h.Unhealthy++
		case "INITIALIZING":
			h.Initializing++
		case "RUNNING":
			h.Running++
		default:
			h.Idle++
		}
	}
	for _, j := range b.jobs {
		switch j.phase {
		case pQueued:
			h.InQueue++
		case pRunning:
			h.InProgress++
		}
	}
	return &h
}
