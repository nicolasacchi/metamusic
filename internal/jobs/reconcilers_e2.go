package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// NewReconcilers builds one Reconciler per backend of set (D-E2-39, C151.2/4): each owns its sweep cadence, initSince,
// unhealthySince, restart counters, crash map, meter observations, open-trip check and billing source, all keyed by its
// backend, so a trip, an unreadable sweep or a stuck worker on one backend never touches another. base carries the shared
// fields (Store, Spend, Runner, Clock ...); its Backend is ignored. gate holds each backend's GateConfig (cold start,
// runtime); a backend without an entry takes the values its own Backend reports. The runner lease and fencing stay global.
//
// The result starts with the default backend's reconciler, followed by the others in set order. Only the first needs to be
// run: Run, Tick and Pass on it drive every other instance concurrently (the app's one Reconciler is the first). A set of
// one backend yields one unscoped reconciler, identical to M3's.
func NewReconcilers(set *provider.Set, base ReconcilerOptions, gate map[string]provider.GateConfig) ([]*Reconciler, error) {
	names := set.Names()
	if len(names) == 0 {
		return nil, errors.New("jobs: reconcilers over an empty backend set")
	}
	def := set.Default().Name()
	order := []string{def}
	for _, n := range names {
		if n != def {
			order = append(order, n)
		}
	}
	out := make([]*Reconciler, 0, len(order))
	for _, n := range order {
		b, _ := set.Get(n)
		o := base
		o.Backend = b
		if cfg, ok := gate[n]; ok {
			o.GateCfg = cfg
		}
		if len(names) > 1 {
			o.Scoped, o.DefaultBackend, o.Peer = true, def, n != def
		}
		if n != def {
			o.ColdStartS, o.MaxRuntime = b.ColdStartS(), b.MaxRuntime()
			if cfg, ok := gate[n]; ok {
				o.ColdStartS, o.MaxRuntime = cfg.ColdStartS, cfg.MaxRuntime
			}
		}
		out = append(out, NewReconciler(o))
	}
	out[0].peers = out[1:]
	return out, nil
}

// Options returns the options the reconciler was built with, so a composition root that learns about further backends after
// the first reconciler exists can derive the others from it (NewReconcilers' base).
func (r *Reconciler) Options() ReconcilerOptions { return r.o }

// MeterRouter is the BatchMeter of a multi-backend process: a finishing batch is metered by the reconciler of its own
// backend, so one backend's workers never add overhead to another's batches (C76.6 per backend).
type MeterRouter struct {
	by  map[string]*Reconciler
	def *Reconciler
}

// NewMeterRouter routes by Reconciler backend name; rs[0] answers batches whose backend has no reconciler.
func NewMeterRouter(rs []*Reconciler) *MeterRouter {
	m := &MeterRouter{by: map[string]*Reconciler{}}
	for _, r := range rs {
		m.by[r.o.Backend.Name()] = r
	}
	if len(rs) > 0 {
		m.def = rs[0]
	}
	return m
}

// OverheadSeconds implements BatchMeter.
func (m *MeterRouter) OverheadSeconds(ctx context.Context, x Querier, batchID string) (int64, bool, error) {
	var backend string
	if err := x.QueryRowContext(ctx, `SELECT backend FROM batches WHERE id = ?`, batchID).Scan(&backend); err != nil {
		return 0, false, fmt.Errorf("jobs: meter route: %w", err)
	}
	r := m.by[backend]
	if r == nil {
		r = m.def
	}
	return r.OverheadSeconds(ctx, x, batchID)
}
