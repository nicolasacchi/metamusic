package api

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// knownEngines are the engines list_engines always shows, registered in this process or not: the picker needs to say
// "not enabled" rather than hide the engine.
var knownEngines = []string{engineYuE2, acestep.Name}

// engineYuE2 is the engine that is always adopted and always listed first.
const engineYuE2 = "yue2"

var engineLabels = map[string]string{engineYuE2: "YuE2", acestep.Name: "ACE-Step 1.5"}

// Reasons an engine cannot be selected, in the order they are checked (EngineView.unavailable_reason).
const (
	unavailNotEnabled      = "not_enabled"
	unavailNotAdopted      = "not_adopted"
	unavailBackendAbsent   = "backend_absent"
	unavailBackendDisabled = "backend_disabled"
	unavailDisarmed        = "disarmed"
	unavailTripped         = "tripped"
)

// featuresView shows the verified-features file; the capabilities the engine supports but no canary proved are listed as
// unverified (text2music, repaint, extend, complete, extract, reference_audio and lrc).
func featuresView(f acestep.Features, pools []string) *EngineFeatures {
	v := &EngineFeatures{VerifiedAt: f.VerifiedAt, Tasks: nonNil(slices.Sorted(slices.Values(f.Tasks))), Unverified: []string{},
		ReferenceReproducible: f.ReferenceReproducible, SeedReproducible: f.SeedReproducible, Pools: []EnginePool{}}
	if f.ModelsMode != "" {
		m := f.ModelsMode
		v.ModelsMode = &m
	}
	for _, t := range []string{acestep.TaskText2Music, acestep.TaskRepaint, acestep.TaskExtend, acestep.TaskComplete, acestep.TaskExtract} {
		if !slices.Contains(f.Tasks, t) {
			v.Unverified = append(v.Unverified, t)
		}
	}
	if !f.Reference {
		v.Unverified = append(v.Unverified, "reference_audio")
	}
	if !f.LRC {
		v.Unverified = append(v.Unverified, "lrc")
	}
	sort.Strings(v.Unverified)
	if len(pools) == 0 {
		pools = acestep.DefaultPools()
	}
	for _, p := range pools {
		ep := EnginePool{Pool: p, MaxS: f.MaxSFor([]string{p})}
		if pf, ok := f.Pools[p]; ok && pf.CanaryCommit != "" {
			ep.Verified = true
		}
		v.Pools = append(v.Pools, ep)
	}
	return v
}

// backendOf finds the backend that renders eng: the first of the set's backends whose engine it is.
func (d Deps) backendOf(eng string) (string, bool) {
	if d.Backends == nil {
		return "", false
	}
	for _, n := range d.Backends.Names() {
		if d.Backends.EngineOf(n) == eng {
			return n, true
		}
	}
	return "", false
}

// engineViews builds list_engines. It reads local state only: no backend method that touches the network is called.
func (d Deps) engineViews(ctx context.Context) (EngineList, error) {
	names := slices.Clone(knownEngines)
	for n := range d.Engines {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		if (names[i] == engineYuE2) != (names[j] == engineYuE2) {
			return names[i] == engineYuE2
		}
		return names[i] < names[j]
	})
	out := EngineList{Engines: []EngineView{}, DefaultEngine: engineYuE2}
	for _, n := range names {
		v := EngineView{Name: n, Label: n, TaskClasses: []string{}, Warnings: []string{}}
		if l, ok := engineLabels[n]; ok {
			v.Label = l
		}
		eng, registered := d.Engines[n]
		v.Enabled = registered
		v.Caps = EngineCaps{StemNames: []string{}, Tasks: []string{}}
		if registered {
			c := eng.Caps() // the type is engine.Caps; api may not name it next to yue2 (import-graph rule of C70)
			v.Caps = EngineCaps{Text2Song: c.Text2Song, PlanScore: c.PlanScore, RenderFromScore: c.RenderFromScore, RepaintInterval: c.RepaintInterval,
				Extend: c.Extend, Complete: c.Complete, Stems: c.Stems, ReferenceAudio: c.ReferenceAudio, LRC: c.LRC, Tempo: c.Tempo, Key: c.Key, LoRA: c.LoRA,
				MinSeconds: c.MinSeconds, MaxSeconds: c.MaxSeconds, SampleRate: c.SampleRate, RepaintMinS: c.RepaintMinS, RepaintMaxS: c.RepaintMaxS,
				ExtendMinS: c.ExtendMinS, ExtendMaxS: c.ExtendMaxS, ReferenceMaxS: c.ReferenceMaxS,
				StemNames: nonNil(slices.Clone(c.StemNames)), Tasks: nonNil(slices.Clone(c.Tasks))}
		}
		st, err := d.Adoption.State(ctx, d.Store.R, n)
		if err != nil {
			return EngineList{}, err
		}
		if n == engineYuE2 && st.State == engineadopt.StateNone {
			st.State = engineadopt.StateAdopted // the seed row is the contract; a database without it still serves yue2
		}
		v.Adoption = EngineAdoption{State: st.State}
		if st.State != engineadopt.StateNone {
			via, by, at := st.Via, st.ChangedBy, tsOut(st.ChangedAt)
			v.Adoption.Via, v.Adoption.ChangedBy, v.Adoption.ChangedAt, v.Adoption.Reason, v.Adoption.TrialID = &via, &by, &at, st.Reason, st.TrialID
		}
		switch ae := eng.(type) {
		case acestep.Engine:
			v.TaskClasses = taskClasses(d.Cfg.ACEStepTasks)
			v.Features = featuresView(ae.Verified, ae.Pools)
			if ae.Verified.VerifiedAt == "1970-01-01" {
				v.Warnings = append(v.Warnings, "no canary has run: only text2music is offered, the other capabilities are not yet verified")
			}
		}
		reason := ""
		if st.State != engineadopt.StateAdopted {
			hint := engineadopt.AdoptHint(n) // text only: adopting is the operator's CLI, never an operation
			v.AdoptHint = &hint
		}
		switch {
		case !registered:
			reason = unavailNotEnabled
		case st.State != engineadopt.StateAdopted:
			reason = unavailNotAdopted
		}
		if bn, ok := d.backendOf(n); ok {
			b, _ := d.Backends.Get(bn)
			bv := EngineBackend{Name: bn, ColdStartS: b.ColdStartS(), MaxRuntimeS: int(b.MaxRuntime().Seconds())}
			gs, err := d.Gates.StateFor(ctx, bn)
			switch {
			case err != nil && !errors.Is(err, provider.ErrNoBackend):
				return EngineList{}, err
			case err == nil:
				bv.Enabled, bv.Armed, bv.Tripped = gs.Enabled, gs.Armed, gs.Tripped
				if gs.Reason != "" && !gs.Enabled {
					r := gs.Reason
					bv.DisabledReason = &r
				}
				if gs.Tripped && gs.TripReason != "" {
					r := gs.TripReason
					bv.TripReason = &r
				}
			}
			v.Backend = &bv
			if reason == "" {
				switch {
				case !bv.Enabled:
					reason = unavailBackendDisabled
				case !bv.Armed:
					reason = unavailDisarmed
				case bv.Tripped:
					reason = unavailTripped
				}
			}
			if bv.Tripped {
				v.Warnings = append(v.Warnings, "the backend is tripped")
			}
		} else if reason == "" {
			reason = unavailBackendAbsent
		}
		if reason != "" {
			v.UnavailableReason = &reason
		}
		v.Selectable = reason == ""
		out.Engines = append(out.Engines, v)
	}
	return out, nil
}

// taskClasses are the model families the endpoint is provisioned for: turbo always, base when MM_ACESTEP_TASKS says so.
func taskClasses(cfg []string) []string {
	if len(cfg) == 0 {
		return []string{acestep.ClassTurbo}
	}
	return slices.Clone(cfg)
}

// registerEnginesE2 registers list_engines (C160). It is a generic MCP operation (D-E2-35, operator decision of 2026-11-03: no
// dedicated tool, tools/list grows only through the amended schemas). E2-F2 froze the Meta, the path and the types. The
// adoption hint is text: no operation, tool or agent path can adopt an engine (D-E2-15); only the operator CLI does. The trial and
// Suite operations of E2c are registered by that plan, not here.
func registerEnginesE2(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_engines", Method: "GET", Path: "/api/engines",
		Summary: "List the render engines",
		Description: "Each engine with its adoption state, backend (armed, tripped, cold start), effective capabilities (modes, limits, stem names), " +
			"verified features and whether a generation of it can be estimated right now, and why not.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/engines"},
		func(ctx context.Context, _ *empty) (*enginesOut, error) {
			if d.Engines == nil || d.Adoption == nil || d.Store == nil {
				return nil, unavailable("engines")
			}
			l, err := d.engineViews(ctx)
			if err != nil {
				return nil, libErr(err)
			}
			return &enginesOut{Body: l}, nil
		})
}
