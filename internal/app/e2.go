package app

import (
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// E2 wiring (C151, C160). The contract freeze (E2-F2) owns this file and the call from Build; lanes fill the bodies of the
// per-lane files: e2_set.go (J1: the backend set, per-backend gates, runner and reconcilers), e2_ops.go (OP: the engine
// operations' collaborators, the trackctx advisor) and e2_hooks.go (B-INT: the final wiring and the acceptance seams).

// e2Inputs is what the E2 wiring draws on.
type e2Inputs struct {
	cfg   config.Config
	st    *store.Store
	m3    *m3Services
	clips *clip.Service
	now   func() time.Time
	newID func() string
}

// e2Services is what buildE2 adds to Services.
type e2Services struct {
	// Engines is the registry generation and the runner use (yue2 only until E2-INT flips registry.Options.ACEStep).
	Engines engine.Registry
	// Backends and Gates are the per-engine backend set and its gates: one entry (the M3 backend) until E2-PF/PR add the
	// ACE-Step backends; empty for MM_BACKEND=none.
	Backends *provider.Set
	Gates    *provider.GateSet
	// Adoption is the engine adoption record (D-E2-15).
	Adoption *engineadopt.Service
}

// buildE2 constructs the E2 services. It calls no backend method and starts nothing.
func buildE2(in e2Inputs) (*e2Services, error) {
	e := &e2Services{Engines: in.m3.Engines, Adoption: engineadopt.New(engineadopt.Options{Store: in.st, Now: in.now})}
	var err error
	if e.Backends, e.Gates, err = buildE2Set(in); err != nil {
		return nil, err
	}
	if in.m3.Generation != nil {
		// a typed nil *clip.Service must never reach the interface
		var rc generation.ReferenceClipper
		if in.clips != nil {
			rc = in.clips
		}
		in.m3.Generation.UseEngines(e.Backends, e.Gates, e.Adoption, rc)
	}
	buildE2Ops(in, e)
	return e, nil
}
