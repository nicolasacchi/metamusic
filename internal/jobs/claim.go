package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// gateInfo is the claim gate read from local rows only.
type gateInfo struct {
	ok         bool
	reason     string
	quote      *provider.Quote
	workersMax int
	idleS      int
	price      money.MicroUSD // list price x margin; 0 without a usable quote
}

// gateConfig is the quote-check configuration of this runner's backend: the backend's own gate when the runner sits over a Set
// (each backend has its own cold start and runtime, C151.1), otherwise M3's single-backend value.
func (r *Runner) gateConfig() provider.GateConfig {
	if g, ok := r.o.Gates.For(r.o.Backend.Name()); ok {
		return g.Config()
	}
	mode := provider.ModeFake
	if r.o.Backend.Name() == provider.BackendRunPod {
		mode = provider.ModeRunPod
	}
	return provider.GateConfig{Mode: mode, KeysLoaded: true, ColdStartS: r.o.ColdStartS, MaxRuntime: r.o.MaxRuntime}
}

// readGate reads armed, tripped and the cached quote of the view's backend through x.
func (r *Runner) readGate(ctx context.Context, x q) (gateInfo, error) {
	g := gateInfo{workersMax: provider.MaxWorkers}
	backend := r.o.Backend.Name()
	var armed int
	var quoteJSON, quoteAt sql.NullString
	err := x.QueryRowContext(ctx, `SELECT armed, quote_json, quote_at FROM provider_state WHERE backend = ?`, backend).Scan(&armed, &quoteJSON, &quoteAt)
	if errors.Is(err, sql.ErrNoRows) {
		g.reason = "not_armed"
		return g, nil
	}
	if err != nil {
		return g, fmt.Errorf("jobs: read provider state: %w", err)
	}
	var trips int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reset_at IS NULL`, backend).Scan(&trips); err != nil {
		return g, fmt.Errorf("jobs: read trips: %w", err)
	}
	cfg := r.gateConfig()
	foreign := false
	if quoteJSON.Valid {
		qt, qerr := decodeQuote(quoteJSON.String, quoteAt.String)
		if qerr == nil {
			g.quote = &qt
			// a quote observed for another backend never enables or prices this one (C151: each backend has its own quote)
			foreign = r.scoped && qt.Backend != "" && qt.Backend != backend
		}
	}
	now := r.o.Clock.Now()
	switch {
	case armed != 1:
		g.reason = "not_armed"
	case trips > 0:
		g.reason = "tripped"
	case cfg.Mode == provider.ModeRunPod:
		if g.quote == nil {
			g.reason = provider.ReasonQuoteStale
		} else if foreign {
			g.reason = provider.ReasonEndpointConfig
		} else if why := provider.QuoteCheck(*g.quote, now, cfg); why != "" {
			g.reason = why
		}
	default:
		// the fake backend needs no quote; a stale or foreign one is ignored rather than refused
		if g.quote != nil && (foreign || now.Sub(g.quote.ObservedAt) > provider.QuoteMaxAge) {
			g.quote = nil
		}
	}
	if g.reason != "" {
		return g, nil
	}
	g.ok = true
	if g.quote != nil {
		if g.quote.Endpoint.WorkersMax > 0 {
			g.workersMax = g.quote.Endpoint.WorkersMax
		}
		if g.quote.Endpoint.IdleTimeoutS != nil {
			g.idleS = *g.quote.Endpoint.IdleTimeoutS
		}
		if g.quote.ListPricePPH > 0 {
			g.price = money.WithMarginPPM(g.quote.ListPricePPH, r.o.PriceMarginPPM)
		}
	}
	return g, nil
}

// claimViews is one runner view per backend of the Set, in construction order (C151.2). Without a Set, or with a
// single-backend one, the runner is its own only view and nothing changes from M3. A view shares the lease, the ingest pool
// and every clock of the runner (rstate) and differs in the backend, its cold start, its runtime and the weights it pins.
func (r *Runner) claimViews() []*Runner {
	names := r.o.Backends.Names()
	if len(names) <= 1 {
		return []*Runner{r}
	}
	views := make([]*Runner, 0, len(names))
	for _, n := range names {
		b, _ := r.o.Backends.Get(n)
		v := &Runner{rstate: r.rstate, o: r.o, scoped: true}
		v.o.Backend = b
		if n != r.o.Backends.Default().Name() {
			v.o.ColdStartS, v.o.MaxRuntime = b.ColdStartS(), b.MaxRuntime()
			if w, ok := r.o.Expects[b.Engine()]; ok {
				v.o.Expect = w
			}
		}
		views = append(views, v)
	}
	return views
}

// backendFor is the backend an attempt was submitted to. Without a Set that is the runner's backend; with one, an attempt
// whose backend left the set cannot be reached and ok is false (it settles by its time bound, never against another endpoint).
func (r *Runner) backendFor(name string) (provider.Backend, bool) {
	if r.o.Backends == nil {
		return r.o.Backend, true
	}
	return r.o.Backends.Get(name)
}

// phaseClaim claims and submits queued jobs, backend by backend: a backend that is disarmed, tripped, unquoted or
// unhealthy claims nothing and does not stop the others (C151.2; the gate of each view reads only its own rows).
func (r *Runner) phaseClaim(ctx context.Context, rep *StepReport) error {
	views := r.claimViews()
	if len(views) == 1 {
		return views[0].claimLoop(ctx, rep)
	}
	for _, v := range views {
		// a provider failure of one backend is handled inside its submit (classified, tripped, retried); an error that
		// reaches here is the store, the lease or an injected crash, which stops the step for every backend alike.
		if err := v.claimLoop(ctx, rep); err != nil {
			return err
		}
	}
	return nil
}

// claimLoop claims while capacity allows.
func (r *Runner) claimLoop(ctx context.Context, rep *StepReport) error {
	for range maxClaimsPerStep {
		ok, err := r.claimOne(ctx)
		if err != nil {
			if errors.Is(err, store.ErrLeaseShort) {
				return nil // the heartbeat renews first; no submit this tick
			}
			return err
		}
		if !ok {
			return nil
		}
		rep.Claimed++
	}
	return nil
}
