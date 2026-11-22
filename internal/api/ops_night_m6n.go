package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nightshift"
)

// registerNightM6N registers the four night-shift operations (M6N C377). Reading and previewing never write and never spend; only the
// Owner in the app changes the settings (human-only, and enabling needs a live mandate); halting only reduces spend, so every
// Owner-side caller with the write scope, the Owner's paired phone included, may.
func registerNightM6N(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_night_shift", Method: "GET", Path: "/api/night-shift",
		Summary: "Get the night shift",
		Description: "The night shift's settings and halt state, the mandate it would spend under (or why it has none), when it next runs, and the last 14 nights " +
			"with their takes, estimate ceiling and settled spend. A stored created night shows as running while its batch works and done after. Reads only. Owner side only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/night-shift", OwnerOnly: true},
		func(ctx context.Context, _ *struct{}) (*nightShiftOut, error) {
			if d.NightShift == nil {
				return nil, unavailable("night_shift")
			}
			p := auth.FromContext(ctx)
			s, err := d.NightShift.Settings(ctx)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			out := NightShift{Settings: nightSettingsWire(p, s), Runs: []NightRun{}}
			m, _, reason, err := d.NightShift.Authority(ctx)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			if m != nil {
				w, err := mandateWire(ctx, d, p, *m)
				if err != nil {
					return nil, m2bErr(err, "M6N-N3")
				}
				mo := MandateOrNull(w)
				out.Authority.Mandate = &mo
			}
			if reason != "" {
				out.Authority.Reason = &reason
			}
			if out.NextRunAt, err = d.NightShift.NextRunAt(ctx); err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			runs, err := d.NightShift.RunViews(ctx, 14)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			for _, r := range runs {
				out.Runs = append(out.Runs, nightRunWire(r))
			}
			return &nightShiftOut{Body: out}, nil
		})

	Register(a, huma.Operation{
		OperationID: "preview_night_shift", Method: "GET", Path: "/api/night-shift/preview",
		Summary: "Preview tonight's plan and price",
		Description: "Plans the coming night with the real planner (one take per lineage, exploit and explore, the mandate's scope) and prices it, trimming takes the " +
			"mandate cannot cover. It claims nothing, creates no estimate or batch and spends nothing; a halted or disabled shift still shows its plan, with would_skip " +
			"naming why a run now would do nothing. Owner side only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/night-shift", Rate: "heavy", OwnerOnly: true},
		func(ctx context.Context, _ *struct{}) (*nightPlanOut, error) {
			if d.NightShift == nil {
				return nil, unavailable("night_shift")
			}
			pv, err := d.NightShift.Preview(ctx)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			return &nightPlanOut{Body: nightPlanWire(pv)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_night_shift", Method: "PUT", Path: "/human/api/night-shift",
		Summary: "Change the night shift",
		Description: "Human only, Owner only. Sets when it runs, how many takes, the explore share, the star floor and the listening checks, and switches it on or off. " +
			"Enabling needs a live mandate (409 MANDATE_REQUIRED) and clears a halt. It holds no money: the mandate alone bounds spend, and the global caps stay the ceiling. " +
			"Absent fields keep their value.",
	}, bodySmall), Meta{Scope: auth.ScopeGenerate, Cost: "free", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/night-shift", OwnerOnly: true},
		func(ctx context.Context, in *updateNightShiftIn) (*nightSettingsOut, error) {
			if d.NightShift == nil {
				return nil, unavailable("night_shift")
			}
			p := auth.FromContext(ctx)
			b := in.Body
			patch := nightshift.SettingsPatch{Enabled: b.Enabled, At: b.At, MaxTakes: b.MaxTakes, ExplorePPM: b.ExplorePPM, MinStars: b.MinStars, ListeningChecks: b.ListeningChecks}
			if err := patch.Validate(); err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			if b.Enabled != nil && *b.Enabled {
				m, _, reason, err := d.NightShift.Authority(ctx)
				if err != nil {
					return nil, m2bErr(err, "M6N-N3")
				}
				if m == nil || reason != "" {
					return nil, Err(409, "MANDATE_REQUIRED", "the night shift needs a live spending mandate before it can be enabled", map[string]any{"reason": reason})
				}
			}
			s, err := d.NightShift.UpdateSettings(ctx, p, patch)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			return &nightSettingsOut{Body: nightSettingsWire(p, s)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "halt_night_shift", Method: "POST", Path: "/api/night-shift/halt",
		Summary: "Halt the night shift",
		Description: "Stops the night shift: records the halt, cancels the batch of a night still running, and skips every later night until the Owner enables the shift again " +
			"in the app. It only reduces spend, so an agent with the write scope and the Owner's paired phone may. Idempotent: a second halt keeps the first.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/night-shift", OwnerOnly: true},
		func(ctx context.Context, in *haltNightShiftIn) (*nightSettingsOut, error) {
			if d.NightShift == nil {
				return nil, unavailable("night_shift")
			}
			p := auth.FromContext(ctx)
			s, err := d.NightShift.Halt(ctx, p, in.Body.Reason)
			if err != nil {
				return nil, m2bErr(err, "M6N-N3")
			}
			return &nightSettingsOut{Body: nightSettingsWire(p, s)}, nil
		})
}

func nightSettingsWire(viewer auth.Principal, s nightshift.Settings) NightSettings {
	w := NightSettings{Enabled: s.Enabled, At: s.At, MaxTakes: s.MaxTakes, ExplorePPM: s.ExplorePPM, MinStars: s.MinStars, ListeningChecks: s.ListeningChecks,
		UpdatedBy: mandateRef(viewer, s.UpdatedBy), UpdatedAt: s.UpdatedAt}
	if s.Halted() {
		h := NightHaltedOrNull{By: mandateRef(viewer, *s.HaltedBy), At: *s.HaltedAt, Reason: *s.HaltReason}
		w.Halted = &h
	}
	return w
}

func nightRunWire(r nightshift.RunView) NightRun {
	w := NightRun{Night: r.Night, State: r.State, Takes: r.Takes, CeilingMicroUSD: int64(r.CeilingMicroUSD), CeilingUSD: r.CeilingMicroUSD.USD(),
		SettledMicroUSD: int64(r.SettledMicroUSD), SettledUSD: r.SettledMicroUSD.USD()}
	w.Reason, w.BatchID, w.MorningCrateID = strPtr(r.Reason), strPtr(r.BatchID), strPtr(r.MorningCrateID)
	return w
}

func nightPlanWire(pv nightshift.PlanView) NightPlan {
	w := NightPlan{Night: pv.Night, Seed: pv.Seed, Items: []NightPlanItem{}, Dropped: []NightDropped{}, Trimmed: []NightDropped{}, WouldSkip: strPtr(pv.WouldSkip)}
	for _, it := range pv.Items {
		w.Items = append(w.Items, NightPlanItem{Kind: it.Kind, RootTrackID: it.RootTrackID, ParentTrackID: strPtr(it.ParentTrackID), Mode: it.Mode,
			Patch: strPtr(it.PatchStyle), Reason: it.Reason})
	}
	for _, x := range pv.Dropped {
		w.Dropped = append(w.Dropped, NightDropped{RootTrackID: x.RootTrackID, Reason: x.Reason})
	}
	for _, x := range pv.Trimmed {
		w.Trimmed = append(w.Trimmed, NightDropped{RootTrackID: x.RootTrackID, Reason: x.Reason})
	}
	if q := pv.Quote; q != nil {
		nq := NightQuoteOrNull{Takes: q.Takes, ExpectedMicroUSD: int64(q.ExpectedMicroUSD), ExpectedUSD: q.ExpectedMicroUSD.USD(), MaxMicroUSD: int64(q.MaxMicroUSD),
			MaxUSD: q.MaxMicroUSD.USD(), CeilingMicroUSD: int64(q.CeilingMicroUSD), CeilingUSD: q.CeilingMicroUSD.USD()}
		w.Quote = &nq
	}
	if m := pv.Mandate; m != nil {
		pm := NightPlanMandateOrNull{ID: m.ID, Covered: m.Covered, Reasons: append([]string{}, m.Reasons...)}
		w.Mandate = &pm
	}
	return w
}

// strPtr is nil for the empty string.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
