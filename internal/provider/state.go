package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Why generation is disabled (C68). The strings are API values (details.reason, disabled_reason).
const (
	ReasonBackendNone       = "backend_none"
	ReasonKeysAbsent        = "keys_absent"
	ReasonEndpointInactive  = "endpoint_inactive"
	ReasonEndpointConfig    = "endpoint_config"
	ReasonQuoteStale        = "quote_stale"
	ReasonCatalogUnreadable = "catalog_unreadable"
	ReasonSweepUnreadable   = "sweep_unreadable"
)

// QuoteMaxAge is how long a cached quote stays usable (C68, C77.5).
const QuoteMaxAge = time.Hour

// MaxWorkers is the largest workers.max the endpoint may have (C77.5).
const MaxWorkers = 2

// jobsHost is the host every requestUrls entry of the endpoint must have (C77.5).
const jobsHost = "api.runpod.ai"

// Backend modes of MM_BACKEND (internal/config repeats them; provider must not import config).
const (
	ModeNone   = "none"
	ModeFake   = "fake"
	ModeRunPod = "runpod"
)

// GateConfig is the process-local input of the generation-enabled computation.
type GateConfig struct {
	Mode       string        // ModeNone | ModeFake | ModeRunPod
	KeysLoaded bool          // runpod.env loaded in this process
	ColdStartS int           // MM_COLD_START_S: the provisioned queueDelay is ColdStartS + 60
	MaxRuntime time.Duration // MM_JOB_MAX_RUNTIME: the endpoint timeout
}

// BackendName is the provider_state key of the configured mode ("" for none).
func (c GateConfig) BackendName() string {
	switch c.Mode {
	case ModeFake:
		return BackendFake
	case ModeRunPod:
		return BackendRunPod
	}
	return ""
}

// QuoteCheck applies the fail-closed checks of C77.5 to a cached quote and returns the disabled
// reason, or "" when the quote may be used. It never touches the network.
func QuoteCheck(q Quote, now time.Time, c GateConfig) string {
	if q.ObservedAt.IsZero() || now.Sub(q.ObservedAt) > QuoteMaxAge {
		return ReasonQuoteStale
	}
	if q.SweepUnreadable {
		return ReasonSweepUnreadable
	}
	e := q.Endpoint
	if e.WorkersMax == 0 {
		return ReasonEndpointInactive
	}
	if e.Type != "QUEUE" || e.ScalerType != "QUEUE_DELAY" || e.ScalerValue != strconv.Itoa(c.ColdStartS+60) ||
		e.IdleTimeoutS == nil || e.WorkersMax > MaxWorkers || e.WorkersMax < 0 || e.GPUCount != 1 ||
		e.TimeoutMS != c.MaxRuntime.Milliseconds() {
		return ReasonEndpointConfig
	}
	for _, raw := range e.RequestURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != jobsHost {
			return ReasonEndpointConfig
		}
	}
	if q.ListPricePPH <= 0 {
		return ReasonCatalogUnreadable
	}
	return ""
}

// State is what the generation gate knows without any network call.
type State struct {
	Mode    string // the configured mode
	Backend string // provider_state key; "" for none
	// Enabled: generation can work at all (C68), ignoring arming and trips.
	Enabled bool
	Reason  string // disabled reason when !Enabled
	Armed   bool   // provider_state.armed
	Tripped bool   // an open trip exists
	// TripID and TripReason describe the open trip.
	TripID, TripReason string
	// ArmedChangedBy and ArmedChangedAt come from the provider_state row.
	ArmedChangedBy string
	ArmedChangedAt time.Time
	Quote          *Quote // the cached quote, when valid JSON
	QuoteAt        *time.Time
	RestoredAt     *time.Time
}

// querier is the read side the gate needs (*sql.DB, *sql.Tx and the store pools satisfy it).
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Gate computes State from local facts only: configuration, key presence, the provider_state row
// (including the quote the reconciler cached) and the open-trip row. No method of any Backend is
// ever called (TestApp_GenerationEnabledNoNetwork).
type Gate struct {
	q   querier
	cfg GateConfig
	now func() time.Time
}

// NewGate builds the gate; now may be nil.
func NewGate(q querier, cfg GateConfig, now func() time.Time) *Gate {
	if now == nil {
		now = time.Now
	}
	return &Gate{q: q, cfg: cfg, now: now}
}

// Config returns the gate's configuration.
func (g *Gate) Config() GateConfig { return g.cfg }

// State reads the current state. A tx may be passed through StateIn to read inside a transaction.
func (g *Gate) State(ctx context.Context) (State, error) { return g.StateIn(ctx, g.q) }

// StateIn reads the state through q (use the *sql.Tx inside a write transaction).
func (g *Gate) StateIn(ctx context.Context, q querier) (State, error) {
	st := State{Mode: g.cfg.Mode, Backend: g.cfg.BackendName()}
	switch g.cfg.Mode {
	case ModeFake:
		st.Enabled = true
	case ModeRunPod:
		if !g.cfg.KeysLoaded {
			st.Reason = ReasonKeysAbsent
		}
	default:
		st.Reason = ReasonBackendNone
		return st, nil
	}
	var armed int
	var by, at string
	var quoteJSON, quoteAt, restoredAt sql.NullString
	err := q.QueryRowContext(ctx, `SELECT armed, changed_by, changed_at, quote_json, quote_at, restored_at FROM provider_state WHERE backend = ?`, st.Backend).
		Scan(&armed, &by, &at, &quoteJSON, &quoteAt, &restoredAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return State{}, fmt.Errorf("provider: read state: %w", err)
	default:
		st.Armed, st.ArmedChangedBy = armed == 1, by
		st.ArmedChangedAt = parseStoreTime(at)
		if restoredAt.Valid {
			t := parseStoreTime(restoredAt.String)
			st.RestoredAt = &t
		}
		if quoteJSON.Valid {
			var qt Quote
			if json.Unmarshal([]byte(quoteJSON.String), &qt) == nil {
				st.Quote = &qt
				t := parseStoreTime(quoteAt.String)
				st.QuoteAt = &t
				if !t.IsZero() {
					st.Quote.ObservedAt = t
				}
			}
		}
	}
	var tripID, tripReason string
	switch err := q.QueryRowContext(ctx, `SELECT id, reason FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, st.Backend).Scan(&tripID, &tripReason); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return State{}, fmt.Errorf("provider: read trip: %w", err)
	default:
		st.Tripped, st.TripID, st.TripReason = true, tripID, tripReason
	}
	if g.cfg.Mode == ModeRunPod && st.Reason == "" {
		if st.Quote == nil {
			st.Reason = ReasonQuoteStale
		} else if r := QuoteCheck(*st.Quote, g.now(), g.cfg); r != "" {
			st.Reason = r
		} else {
			st.Enabled = true
		}
	}
	return st, nil
}

// parseStoreTime reads the fixed-width M3 layout; a malformed value is the zero time (fails closed:
// a zero quote time is stale).
func parseStoreTime(s string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
