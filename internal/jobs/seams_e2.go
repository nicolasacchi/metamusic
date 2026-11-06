package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The E2 seams of the jobs package (C151.2-5, C153.1, D-E2-30, D-E2-39). The contract freeze (E2-F2) fixed these shapes; the
// bodies are the lane named in each STUB marker. A stub that is called by a path an M3 configuration reaches returns the M3
// behaviour; the others return ErrE2NotImplemented.

// ErrE2NotImplemented marks an E2 seam whose body has not landed.
var ErrE2NotImplemented = errors.New("jobs: E2 seam not implemented yet")

// InputMinter mints the fetch URLs of a take's inputs at submit (C153.3, D-E2-9). It reads src and ref (track id and
// digest) from the canonical request, signs media links that outlive window_ends_at and returns InputRef values that are
// never persisted, logged, returned or evented. Lane E2-J2 implements it over media/serve.Signer; a request without src
// and ref yields a nil map (so a yue2 job is byte-identical).
type InputMinter interface {
	MintInputs(ctx context.Context, request json.RawMessage, expires time.Time) (map[string]provider.InputRef, error)
}

// ProviderStatuses is get_provider_status over every backend: the default backend's M3 object, one object per backend and
// the global worker bound (C151.3, C151.5).
type ProviderStatuses struct {
	Default  ProviderStatus
	Backends []ProviderStatus
	Bound    GlobalBoundView
}

// GlobalBoundView is G (burn rate if every endpoint runs flat out) and X (worst simultaneous exposure), micro-USD.
type GlobalBoundView struct {
	GMicroPerHour, XMicro money.MicroUSD
	Backends              []string // the backends with live workers.max > 0
}

// BoundInputs are what the global worker bound is compared with (C151.5): the spend service reads the caps and the month's
// usage, and the process configuration supplies the kill slack and price margin that statusFor does not carry per backend.
type BoundInputs struct {
	Spend *spend.Service
	// KillSlackS and PriceMarginPPM are the process configuration; when set they replace the values the admin's own options
	// carry (app.buildM3 leaves those zero), so the bound never understates X.
	KillSlackS     int
	PriceMarginPPM int64
}

// UseBound wires the cap comparison of Update. A single-backend process never asks (M3 behaviour); app.buildE2Ops wires it
// whenever the process has the E2 backend set (TestBuildE2Ops_WiresBound).
func (a *ProviderAdmin) UseBound(b BoundInputs) {
	a.mu.Lock()
	a.bound = b
	a.mu.Unlock()
}

// engineOf names the engine a status renders ("yue2" unless the gate says otherwise).
func engineOf(g *provider.Gate) string {
	if g != nil && g.Config().Engine != "" {
		return g.Config().Engine
	}
	return "yue2"
}

// StatusAll reads every backend's status and the global bound.
func (a *ProviderAdmin) StatusAll(ctx context.Context, viewer auth.Principal) (ProviderStatuses, error) {
	_ = viewer // principals are rendered by the API layer
	def, err := a.statusFor(ctx, "")
	if err != nil {
		return ProviderStatuses{}, err
	}
	def.Engine = engineOf(a.o.Gate)
	out := ProviderStatuses{Default: def}
	a.mu.RLock()
	gates := a.gates
	a.mu.RUnlock()
	names := gates.Names()
	if len(names) == 0 {
		out.Backends = []ProviderStatus{def}
	}
	for _, n := range names {
		st, err := a.statusFor(ctx, n)
		if err != nil {
			return ProviderStatuses{}, err
		}
		g, _ := gates.For(n)
		st.Engine = engineOf(g)
		out.Backends = append(out.Backends, st)
	}
	a.mu.RLock()
	bi := a.bound
	a.mu.RUnlock()
	out.Bound = boundOf(out.Backends, "", bi)
	return out, nil
}

// StatusFor reads one backend's status; an unknown backend is provider.ErrNoBackend.
func (a *ProviderAdmin) StatusFor(ctx context.Context, viewer auth.Principal, backend string) (ProviderStatus, error) {
	_ = viewer
	g, _, _, err := a.resolve(backend)
	if err != nil {
		return ProviderStatus{}, err
	}
	st, err := a.statusFor(ctx, backend)
	if err != nil {
		return ProviderStatus{}, err
	}
	st.Engine = engineOf(g)
	return st, nil
}

// boundOf is spend.ComputeGlobalBound over the statuses. A backend that is not armed contributes nothing (its endpoint is
// scaled to zero by the arming rules); arm names a backend to count as armed, for the check before arming it.
func boundOf(sts []ProviderStatus, arm string, bi BoundInputs) GlobalBoundView {
	in := make([]spend.BackendBound, 0, len(sts))
	for _, s := range sts {
		if bi.KillSlackS > 0 {
			s.KillSlackS = bi.KillSlackS
		}
		if bi.PriceMarginPPM > 0 {
			s.PriceMarginPPM = bi.PriceMarginPPM
		}
		workers := 0
		if s.Endpoint != nil && (s.State.Armed || (arm != "" && s.State.Backend == arm)) {
			workers = s.Endpoint.WorkersMax
		}
		in = append(in, spend.BackendBound{Backend: s.State.Backend, WorkersMax: workers, PricePPH: money.WithMarginPPM(s.PricePerHour, s.PriceMarginPPM),
			MaxRuntimeS: s.MaxRuntimeS, ColdStartS: s.ColdStartS, KillSlackS: s.KillSlackS})
	}
	b := spend.ComputeGlobalBound(in)
	return GlobalBoundView{GMicroPerHour: b.GMicroPerHour, XMicro: b.XMicro, Backends: b.Backends}
}

// checkBound refuses arming backend when, with it armed, X (the worst simultaneous exposure of all endpoints) exceeds the
// per-batch ceiling or the month's remaining headroom (C151.5, D-E2-30). A single-backend process is exactly M3 and skips it.
func (a *ProviderAdmin) checkBound(ctx context.Context, backend string) error {
	a.mu.RLock()
	gates, bi := a.gates, a.bound
	sp := bi.Spend
	a.mu.RUnlock()
	if len(gates.Names()) < 2 || sp == nil { // app wiring (e2_ops.go) always supplies spend; a bare admin has no caps to compare
		return nil
	}
	var sts []ProviderStatus
	for _, n := range gates.Names() {
		st, err := a.statusFor(ctx, n)
		if err != nil {
			return err
		}
		sts = append(sts, st)
	}
	b := boundOf(sts, backend, bi)
	caps, err := sp.LoadCaps(ctx, a.o.Store.R)
	if err != nil {
		return err
	}
	u, err := sp.Usage(ctx, a.o.Store.R, "", a.o.Clock.Now())
	if err != nil {
		return err
	}
	headroom := max(caps.MonthlyMicro-u.MonthSettled-u.Outstanding, 0)
	if b.XMicro > caps.BatchMicro || b.XMicro > headroom {
		return &ConcurrencyExceedsCapError{Backends: b.Backends, GMicroPerHour: b.GMicroPerHour, XMicro: b.XMicro}
	}
	return nil
}

// ConcurrencyExceedsCapError says arming or activating would let the two endpoints together exceed the per-batch ceiling or
// the month headroom (C151.5): 422 CONCURRENCY_EXCEEDS_CAP {backends, g_micro_per_hour, x_micro}.
type ConcurrencyExceedsCapError struct {
	Backends      []string
	GMicroPerHour money.MicroUSD
	XMicro        money.MicroUSD
}

func (e *ConcurrencyExceedsCapError) Error() string {
	return fmt.Sprintf("jobs: the global worker bound exceeds the cap (backends %v)", e.Backends)
}
