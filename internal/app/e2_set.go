package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// e2Extra is one additional backend of the set: the ACE-Step backend of an engine other than the default one.
type e2Extra struct {
	Backend provider.Backend
	// KeysLoaded: the credentials of the backend are present (always true for the fake).
	KeysLoaded bool
	// Expect pins the weights the backend's worker must report (provider.WorkerExpect, per engine).
	Expect provider.WorkerExpect
}

// e2ExtraBackends constructs the ACE-Step backends (`fake-acestep` under MM_BACKEND=fake, `runpod-serverless-acestep` under
// runpod when RUNPOD_ACESTEP_ENDPOINT_ID is present) once MM_ACESTEP is on. The constructors live in the provider packages
// (lanes E2-PF and E2-PR), so the final wiring (E2-INT) installs them from an init in e2_hooks.go; nil, or a nil/empty result,
// keeps the single-backend M3 set. It is only called when cfg.ACEStep is set.
var e2ExtraBackends func(in e2Inputs) ([]e2Extra, error)

// buildE2Set builds the backend set and its gates (C151.1). Lane E2-J1 owns this file: without extra backends a process
// behaves as before (the M3 backend alone). With them it adds each backend's provider_state row (disarmed except fake*,
// D-M3-33: a new backend starts disarmed), its gate (its own cold start, runtime and engine), re-bases the runner over the
// set and replaces the one reconciler by the first of one reconciler per backend.
func buildE2Set(in e2Inputs) (*provider.Set, *provider.GateSet, error) {
	m := in.m3
	if m.Backend == nil || m.Gate == nil || m.Gate.Config().BackendName() == "" {
		set, err := provider.NewSet("")
		if err != nil {
			return nil, nil, err
		}
		gates, err := provider.NewGateSet("")
		return set, gates, err
	}
	var extras []e2Extra
	if in.cfg.ACEStep && e2ExtraBackends != nil {
		var err error
		if extras, err = e2ExtraBackends(in); err != nil {
			return nil, nil, fmt.Errorf("app: acestep backends: %w", err)
		}
	}
	bs, gs := []provider.Backend{m.Backend}, []*provider.Gate{m.Gate}
	for _, x := range extras {
		if x.Backend == nil {
			continue
		}
		g := provider.NewGate(in.st.R, provider.GateConfig{Mode: in.cfg.Backend, Backend: x.Backend.Name(), Engine: x.Backend.Engine(),
			KeysLoaded: x.KeysLoaded, ColdStartS: x.Backend.ColdStartS(), MaxRuntime: x.Backend.MaxRuntime()}, in.now)
		if err := ensureE2State(in, x.Backend.Name()); err != nil {
			return nil, nil, err
		}
		bs, gs = append(bs, x.Backend), append(gs, g)
	}
	set, err := provider.NewSet(m.Backend.Name(), bs...)
	if err != nil {
		return nil, nil, err
	}
	gates, err := provider.NewGateSet(m.Gate.Config().BackendName(), gs...)
	if err != nil {
		return nil, nil, err
	}
	if len(bs) > 1 {
		if err := rebaseOnSet(in, set, gates, extras); err != nil {
			return nil, nil, err
		}
	}
	return set, gates, nil
}

// ensureE2State writes the provider_state row of an additional backend once: the fakes are armed by system:default, every
// other backend is disarmed (D-M3-33). An existing row is never rewritten.
func ensureE2State(in e2Inputs, name string) error {
	armed := 0
	if strings.HasPrefix(name, "fake") {
		armed = 1
	}
	if _, err := in.st.W.ExecContext(context.Background(), `INSERT OR IGNORE INTO provider_state(backend, armed, changed_by, changed_at) VALUES (?,?,?,?)`,
		name, armed, "system:default", store.FormatTime(in.now())); err != nil {
		return fmt.Errorf("app: provider state: %w", err)
	}
	return nil
}

// rebaseOnSet gives the runner and the provider admin their view of the whole set and replaces the single reconciler by one
// per backend (the first, the default backend's, drives the others; callers keep running only that one).
func rebaseOnSet(in e2Inputs, set *provider.Set, gates *provider.GateSet, extras []e2Extra) error {
	m := in.m3
	if m.Runner == nil || m.Reconciler == nil {
		return nil // no runner (MM_RUNNER off): the set serves estimates and status only
	}
	expects := map[string]provider.WorkerExpect{provider.EngineYuE2: {Weights: yue2.Weights}}
	for _, x := range extras {
		if x.Backend != nil {
			expects[x.Backend.Engine()] = x.Expect
		}
	}
	m.Runner.UseSet(set, gates, expects)
	cfgs := map[string]provider.GateConfig{}
	for _, n := range gates.Names() {
		g, _ := gates.For(n)
		cfgs[n] = g.Config()
	}
	rs, err := jobs.NewReconcilers(set, m.Reconciler.Options(), cfgs)
	if err != nil {
		return err
	}
	m.Reconciler = rs[0]
	m.Runner.UseMeter(jobs.NewMeterRouter(rs)) // each backend's overhead lands on its own batches
	sweeps := map[string]func() jobs.SweepSnapshot{}
	caps := map[string]func() provider.Capabilities{}
	for _, r := range rs {
		n := r.Options().Backend.Name()
		sweeps[n], caps[n] = r.Snapshot, r.Options().Backend.Capabilities
	}
	m.Provider.UseSweep(rs[0].Snapshot)
	m.Provider.UseBackends(gates, sweeps, caps)
	return nil
}
