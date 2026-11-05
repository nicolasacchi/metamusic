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

// NewReconcilers builds one Reconciler per backend of set (D-E2-39): each owns its sweep cadence, initSince, unhealthySince,
// restart counters, trips and billing source, all keyed by its backend. base carries the shared fields; gate holds each
// backend's GateConfig. The runner lease and fencing stay global.
//
// STUB(E2-J1)
func NewReconcilers(set *provider.Set, base ReconcilerOptions, gate map[string]provider.GateConfig) ([]*Reconciler, error) {
	return nil, ErrE2NotImplemented
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

// UseBackends wires the per-backend sweep snapshots and capabilities next to the Gates of ProviderOptions.
//
// STUB(E2-J1)
func (a *ProviderAdmin) UseBackends(gates *provider.GateSet, sweeps map[string]func() SweepSnapshot, caps map[string]func() provider.Capabilities) {
	panic("STUB(E2-J1): ProviderAdmin.UseBackends")
}

// StatusAll reads every backend's status and the global bound.
//
// STUB(E2-OP)
func (a *ProviderAdmin) StatusAll(ctx context.Context, viewer auth.Principal) (ProviderStatuses, error) {
	return ProviderStatuses{}, ErrE2NotImplemented
}

// StatusFor reads one backend's status; an unknown backend is provider.ErrNoBackend.
//
// STUB(E2-OP)
func (a *ProviderAdmin) StatusFor(ctx context.Context, viewer auth.Principal, backend string) (ProviderStatus, error) {
	return ProviderStatus{}, ErrE2NotImplemented
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
