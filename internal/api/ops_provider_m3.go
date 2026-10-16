package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobs"
)

// providerErr maps provider-admin errors to the problem model.
func providerErr(err error) error {
	var gd *jobs.GenerationDisabledError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &gd):
		return Err(http.StatusConflict, "GENERATION_DISABLED", "generation is disabled, so the provider cannot be armed", map[string]any{"reason": gd.Reason})
	case errors.Is(err, jobs.ErrReconcileRequired):
		return Err(http.StatusConflict, "RECONCILE_REQUIRED", "this database was restored: arm after a billing reconciliation has covered the restore", nil)
	case errors.Is(err, jobs.ErrReasonRequired):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a reason of 1 to 500 characters is required", map[string]any{"field": "reason"})
	case errors.Is(err, errors.ErrUnsupported):
		return Err(http.StatusNotImplemented, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"task": "M3-B6"})
	case errors.Is(err, jobs.ErrNothingToChange):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "set armed and/or reset_trip", map[string]any{"field": "armed"})
	}
	return libErr(err)
}

func providerTrip(t jobs.TripInfo) TripView {
	actions := t.Actions
	if actions == nil {
		actions = []string{}
	}
	return TripView{ID: t.ID, TrippedAt: tsOut(t.TrippedAt), Reason: t.Reason, Actions: actions, ResetAt: tsPtr(t.ResetAt)}
}

// providerStatus renders the jobs read model; it never carries the endpoint id, keys, refs or worker ids.
func providerStatus(viewer auth.Principal, s jobs.ProviderStatus) ProviderStatus {
	st := s.State
	by, byID := keyKind(st.ArmedChangedBy)
	out := ProviderStatus{
		Backend: st.Backend, GenerationEnabled: st.Enabled, Armed: st.Armed,
		ArmedChangedBy: RefFor(viewer, st.ArmedChangedBy, by, byID), ArmedChangedAt: tsOut(st.ArmedChangedAt),
		TripsRecent: []TripView{}, LastReconcileAt: tsPtr(s.LastReconcile),
		Capabilities: ProviderCapabilities{ScaleToZero: s.Capability.ScaleToZero, RevertDrift: s.Capability.RevertDrift, Billing: s.Capability.Billing},
		RTF:          []RTFView{}, ColdStartS: s.ColdStartS, KillSlackS: s.KillSlackS, MaxRuntimeS: s.MaxRuntimeS, TTLS: s.TTLS, PriceMarginPPM: s.PriceMarginPPM,
		QuoteObservedAt: tsPtr(s.QuoteObservedAt), SkewMS: s.SkewMS, RestoredUnreconciled: s.RestoredUnreconciled,
		Balance: Balance{Source: "unknown"},
	}
	if out.Capabilities.Billing == "" {
		out.Capabilities.Billing = "none"
	}
	if st.Reason != "" && !st.Enabled {
		r := st.Reason
		out.DisabledReason = &r
	}
	if s.Trip != nil {
		t := TripOrNull(providerTrip(*s.Trip))
		out.Trip = &t
	}
	for _, t := range s.TripsRecent {
		out.TripsRecent = append(out.TripsRecent, providerTrip(t))
	}
	if w := s.Workers; w != nil {
		v := WorkerCountsOrNull{Running: w.Running, Idle: w.Idle, Initializing: w.Initializing, Throttled: w.Throttled, Unhealthy: w.Unhealthy, ObservedAt: tsOut(w.ObservedAt), InitializingSince: tsPtr(w.InitializingSince)}
		out.Workers = &v
	}
	if e := s.Endpoint; e != nil {
		v := EndpointOrNull{Name: e.Name, WorkersMin: e.WorkersMin, WorkersMax: e.WorkersMax, IdleTimeoutS: e.IdleTimeoutS, GPUPools: nonNil(e.Pools),
			ExcludedTypes: nonNil(e.ExcludedTypes), Flashboot: e.Flashboot, ImageDigestShort: e.ImageDigestShort, Drift: []DriftView{}}
		for _, d := range e.Drift {
			v.Drift = append(v.Drift, DriftView{Field: d.Field, Want: d.Want, Got: d.Got})
		}
		out.Endpoint = &v
	}
	if s.PricePerHour > 0 && s.QuoteObservedAt != nil {
		p := ProviderPriceOrNull{PerHourMicroUSD: int64(s.PricePerHour), PerHourUSD: usd(s.PricePerHour), ObservedAt: tsOut(*s.QuoteObservedAt)}
		out.Price = &p
	}
	for _, r := range s.RTF {
		out.RTF = append(out.RTF, RTFView{GPUClass: r.GPUClass, RTFMilli: r.RTFMilli, Samples: r.Samples})
	}
	if m := s.Meter; m != nil {
		v := MeterOrNull{Lifetimes24h: m.Lifetimes, BilledSeconds24h: m.BilledSeconds, MaxWorkersSeen24h: m.MaxWorkers}
		out.Meter = &v
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// registerProviderM3 registers the provider operations (C81).
func registerProviderM3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_provider_status", Method: "GET", Path: "/api/provider",
		Summary:     "Get the generation provider status",
		Description: "Whether generation is enabled, armed or tripped; the open trip; worker counts; the endpoint without its id; price, real-time factors and limits.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *struct{}) (*providerStatusOut, error) {
			if d.Provider == nil {
				return nil, unavailable("provider")
			}
			p := auth.FromContext(ctx)
			s, err := d.Provider.Status(ctx, p)
			if err != nil {
				return nil, providerErr(err)
			}
			return &providerStatusOut{Body: providerStatus(p, s)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_provider_state", Method: "POST", Path: "/human/api/provider/state",
		Summary:     "Arm, disarm or reset the provider",
		Description: "The operator arms or disarms generation and/or resets an open trip. A reason is required. Arming after a restore waits for a billing reconciliation.",
	}, bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/spend"},
		func(ctx context.Context, in *updateProviderStateIn) (*providerStatusOut, error) {
			if d.Provider == nil {
				return nil, unavailable("provider")
			}
			p := auth.FromContext(ctx)
			reset := in.Body.ResetTrip != nil && *in.Body.ResetTrip
			s, err := d.Provider.Update(ctx, p, jobs.UpdateIn{Armed: in.Body.Armed, ResetTrip: reset, Reason: in.Body.Reason})
			if err != nil {
				return nil, providerErr(err)
			}
			return &providerStatusOut{Body: providerStatus(p, s)}, nil
		})
}
