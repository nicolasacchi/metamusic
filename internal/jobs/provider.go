package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ProviderOptions builds the provider-state service behind get_provider_status and
// update_provider_state (C81).
type ProviderOptions struct {
	Store *store.Store
	Gate  *provider.Gate
	Clock clock.Clock // nil => clock.Real

	// Limits shown by get_provider_status (the configuration of the process).
	KillSlackS     int
	TTL            time.Duration
	PriceMarginPPM int64
	// Sweep returns what the reconciler's last sweep saw (Reconciler.Snapshot); nil shows no worker data.
	Sweep func() SweepSnapshot
	// Capabilities returns what the configured keys allow (Backend.Capabilities, never a network call).
	Capabilities func() provider.Capabilities
}

// ProviderAdmin reads and changes provider_state (arming, trip reset). Status never carries the
// endpoint id, keys, refs or worker ids.
type ProviderAdmin struct {
	o  ProviderOptions
	mu sync.RWMutex
}

// NewProviderAdmin builds the service.
func NewProviderAdmin(o ProviderOptions) *ProviderAdmin {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &ProviderAdmin{o: o}
}

// UseSweep wires the reconciler's snapshot after both are built (the app creates the admin first).
func (a *ProviderAdmin) UseSweep(f func() SweepSnapshot) {
	a.mu.Lock()
	a.o.Sweep = f
	a.mu.Unlock()
}

// UseCapabilities wires the backend's capabilities after both are built.
func (a *ProviderAdmin) UseCapabilities(f func() provider.Capabilities) {
	a.mu.Lock()
	a.o.Capabilities = f
	a.mu.Unlock()
}

// TripInfo is one provider trip.
type TripInfo struct {
	ID, Reason string
	TrippedAt  time.Time
	ResetAt    *time.Time
	Actions    []string
}

// WorkerCounts is the last worker sweep.
type WorkerCounts struct {
	Running, Idle, Initializing, Throttled, Unhealthy int
	ObservedAt                                        time.Time
	InitializingSince                                 *time.Time // nil unless a worker has been INITIALIZING (none running)
}

// EndpointInfo is the endpoint without its id.
type EndpointInfo struct {
	Name                   string
	WorkersMin, WorkersMax int
	IdleTimeoutS           *int
	Pools, ExcludedTypes   []string
	Flashboot              string
	ImageDigestShort       string
	Drift                  []provider.Drift
}

// RTFRow is the real-time factor of one GPU class.
type RTFRow struct {
	GPUClass          string
	RTFMilli, Samples int
}

// ProviderStatus is the read model of C81 ProviderStatus.
type ProviderStatus struct {
	State           provider.State
	LastReconcile   *time.Time
	PricePerHour    money.MicroUSD
	QuoteObservedAt *time.Time

	Trip        *TripInfo
	TripsRecent []TripInfo
	Workers     *WorkerCounts
	Endpoint    *EndpointInfo
	Capability  provider.Capabilities
	RTF         []RTFRow

	ColdStartS, KillSlackS, MaxRuntimeS, TTLS int
	PriceMarginPPM                            int64
	Meter                                     *MeterStats
	SkewMS                                    *int64
	RestoredUnreconciled                      bool
}

// UpdateIn is a provider-state change: arm/disarm and/or reset the open trip.
type UpdateIn struct {
	Armed     *bool
	ResetTrip bool
	Reason    string
	// AfterRestore is the explicit acknowledgment of `runpod arm --after-restore`: arming a restored
	// database without a covering billing reconciliation (needed when billing cannot be read).
	AfterRestore bool
}

// Errors of Update.
var (
	ErrReasonRequired  = errors.New("jobs: a reason of 1 to 500 characters is required")
	ErrNothingToChange = errors.New("jobs: nothing to change: set armed or reset_trip")
)

// GenerationDisabledError says the backend cannot generate (C68), so it cannot be armed.
type GenerationDisabledError struct{ Reason string }

func (e *GenerationDisabledError) Error() string { return "jobs: generation is disabled: " + e.Reason }

// Status reads the provider status for viewer.
func (a *ProviderAdmin) Status(ctx context.Context, viewer auth.Principal) (ProviderStatus, error) {
	_ = viewer // principals are rendered by the API layer
	if a.o.Store == nil || a.o.Gate == nil {
		return ProviderStatus{}, errors.ErrUnsupported
	}
	st, err := a.o.Gate.State(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	a.mu.RLock()
	sweep, caps := a.o.Sweep, a.o.Capabilities
	a.mu.RUnlock()
	out := ProviderStatus{State: st, QuoteObservedAt: st.QuoteAt, ColdStartS: a.o.Gate.Config().ColdStartS, KillSlackS: a.o.KillSlackS,
		MaxRuntimeS: int(a.o.Gate.Config().MaxRuntime / time.Second), TTLS: int(a.o.TTL / time.Second), PriceMarginPPM: a.o.PriceMarginPPM,
		Capability: provider.Capabilities{Billing: "none"}}
	if caps != nil {
		out.Capability = caps()
	}
	if st.Quote != nil {
		out.PricePerHour = st.Quote.ListPricePPH
		out.Endpoint = endpointInfo(st.Quote.Endpoint, nil)
		q := *st.Quote // the model never carries request URLs, even redacted ones
		q.Endpoint.RequestURLs = nil
		out.State.Quote = &q
	}
	if sweep != nil {
		snap := sweep()
		if !snap.LastReconcileAt.IsZero() {
			t := snap.LastReconcileAt
			out.LastReconcile = &t
		}
		if !snap.ObservedAt.IsZero() {
			wc := &WorkerCounts{Running: snap.WorkerCounts["running"], Idle: snap.WorkerCounts["idle"], Initializing: snap.WorkerCounts["initializing"],
				Throttled: snap.WorkerCounts["throttled"], Unhealthy: snap.WorkerCounts["unhealthy"], ObservedAt: snap.ObservedAt}
			if !snap.InitializingSince.IsZero() {
				t := snap.InitializingSince
				wc.InitializingSince = &t
			}
			out.Workers = wc
		}
		if snap.Endpoint != nil {
			out.Endpoint = endpointInfo(*snap.Endpoint, snap.Drift)
		} else if out.Endpoint != nil {
			out.Endpoint.Drift = snap.Drift
		}
		if snap.Skew != nil {
			ms := snap.Skew.Milliseconds()
			out.SkewMS = &ms
		}
	}
	now := a.o.Clock.Now()
	if ms, err := meterStats(ctx, a.o.Store.R, now); err == nil && ms.Lifetimes > 0 {
		out.Meter = &ms
	}
	if err := a.loadTrips(ctx, st.Backend, now, &out); err != nil {
		return ProviderStatus{}, err
	}
	if err := a.loadRTF(ctx, &out); err != nil {
		return ProviderStatus{}, err
	}
	if st.Backend != "" {
		g, err := ArmGate(ctx, a.o.Store.R, st.Backend)
		if err != nil {
			return ProviderStatus{}, err
		}
		out.RestoredUnreconciled = g.Restored && !g.Covered
	}
	return out, nil
}

func endpointInfo(ep provider.EndpointConfig, drift []provider.Drift) *EndpointInfo {
	short := strings.TrimPrefix(ep.ImageDigest, "sha256:")
	if len(short) > 12 {
		short = short[:12]
	}
	return &EndpointInfo{Name: ep.Name, WorkersMin: ep.WorkersMin, WorkersMax: ep.WorkersMax, IdleTimeoutS: ep.IdleTimeoutS,
		Pools: append([]string(nil), ep.Pools...), ExcludedTypes: append([]string(nil), ep.ExcludedTypes...), Flashboot: ep.Flashboot,
		ImageDigestShort: short, Drift: append([]provider.Drift(nil), drift...)}
}

func (a *ProviderAdmin) loadRTF(ctx context.Context, out *ProviderStatus) error {
	rows, err := a.o.Store.R.QueryContext(ctx, `SELECT gpu_class, rtf_milli, samples FROM rtf_stats ORDER BY gpu_class`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r RTFRow
		if err := rows.Scan(&r.GPUClass, &r.RTFMilli, &r.Samples); err != nil {
			return err
		}
		out.RTF = append(out.RTF, r)
	}
	return rows.Err()
}

// loadTrips reads the five most recent trips; their actions come from the audit rows the reconciler wrote
// while acting (actions_json is frozen once a trip exists).
func (a *ProviderAdmin) loadTrips(ctx context.Context, backend string, now time.Time, out *ProviderStatus) error {
	if backend == "" {
		return nil
	}
	rows, err := a.o.Store.R.QueryContext(ctx, `SELECT id, reason, tripped_at, reset_at FROM provider_trips WHERE backend = ? ORDER BY tripped_at DESC, id DESC LIMIT 5`, backend)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t TripInfo
		var at string
		var reset sql.NullString
		if err := rows.Scan(&t.ID, &t.Reason, &at, &reset); err != nil {
			rows.Close()
			return err
		}
		t.TrippedAt, t.ResetAt = mustT(at), parseT(reset)
		out.TripsRecent = append(out.TripsRecent, t)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(out.TripsRecent) == 0 {
		return nil
	}
	oldest := out.TripsRecent[len(out.TripsRecent)-1].TrippedAt
	arows, err := a.o.Store.R.QueryContext(ctx, `SELECT at, op FROM audit_log WHERE op IN ('reconciler:purge_queue','reconciler:scale_to_zero','reconciler:revert_drift','reconciler:trip') AND at >= ? ORDER BY at`,
		oldest.Truncate(time.Second).Format(time.RFC3339))
	if err != nil {
		return err
	}
	defer arows.Close()
	type ev struct {
		at time.Time
		op string
	}
	var evs []ev
	for arows.Next() {
		var at, op string
		if err := arows.Scan(&at, &op); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339, at)
		if err == nil {
			evs = append(evs, ev{t, op})
		}
	}
	for i := range out.TripsRecent {
		t := &out.TripsRecent[i]
		end := now.Add(time.Minute)
		if t.ResetAt != nil {
			end = t.ResetAt.Add(time.Minute)
		}
		for _, e := range evs {
			if !e.at.Before(t.TrippedAt.Truncate(time.Second)) && e.at.Before(end) {
				if name, ok := TripActionOps[e.op]; ok && name != "trip" {
					t.Actions = appendUnique(t.Actions, name)
				}
			}
		}
	}
	for i := range out.TripsRecent {
		if out.TripsRecent[i].ResetAt == nil {
			t := out.TripsRecent[i]
			out.Trip = &t
			break
		}
	}
	return arows.Err()
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// Update arms/disarms and/or resets the open trip, writing the audit row in the same transaction.
func (a *ProviderAdmin) Update(ctx context.Context, p auth.Principal, in UpdateIn) (ProviderStatus, error) {
	if a.o.Store == nil || a.o.Gate == nil {
		return ProviderStatus{}, errors.ErrUnsupported
	}
	reason := strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > 500 {
		return ProviderStatus{}, ErrReasonRequired
	}
	if in.Armed == nil && !in.ResetTrip {
		return ProviderStatus{}, ErrNothingToChange
	}
	st, err := a.o.Gate.State(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	if in.Armed != nil && *in.Armed {
		if st.Backend == "" {
			return ProviderStatus{}, &GenerationDisabledError{Reason: provider.ReasonBackendNone}
		}
		if !st.Enabled {
			return ProviderStatus{}, &GenerationDisabledError{Reason: st.Reason}
		}
	}
	if in.Armed == nil && st.Backend == "" {
		return ProviderStatus{}, &GenerationDisabledError{Reason: provider.ReasonBackendNone}
	}
	start := a.o.Clock.Now()
	err = a.tx(ctx, func(tx *sql.Tx) error {
		now := store.FormatTime(start)
		if in.Armed != nil {
			if *in.Armed {
				if err := CheckArm(ctx, tx, st.Backend, in.AfterRestore); err != nil {
					return err
				}
			}
			armed := 0
			if *in.Armed {
				armed = 1
			}
			q := `INSERT INTO provider_state(backend, armed, changed_by, changed_at, reason) VALUES (?,?,?,?,?)
				ON CONFLICT(backend) DO UPDATE SET armed = excluded.armed, changed_by = excluded.changed_by, changed_at = excluded.changed_at, reason = excluded.reason`
			if _, err := tx.ExecContext(ctx, q, st.Backend, armed, p.Key(), now, reason); err != nil {
				return fmt.Errorf("jobs: update provider state: %w", err)
			}
			if *in.Armed && in.AfterRestore {
				if _, err := tx.ExecContext(ctx, `UPDATE provider_state SET restored_at = NULL WHERE backend = ?`, st.Backend); err != nil {
					return err
				}
			}
		}
		if in.ResetTrip {
			if _, err := tx.ExecContext(ctx, `UPDATE provider_trips SET reset_at = ?, reset_by = ?, reset_reason = ? WHERE backend = ? AND reset_at IS NULL`,
				now, p.Key(), reason, st.Backend); err != nil {
				return fmt.Errorf("jobs: reset trip: %w", err)
			}
		}
		return completeAudit(ctx, tx, a.o.Clock, start, nil)
	})
	if err != nil {
		return ProviderStatus{}, err
	}
	return a.Status(ctx, p)
}

func (a *ProviderAdmin) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := a.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		idem.MarksFrom(ctx).Reset()
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
