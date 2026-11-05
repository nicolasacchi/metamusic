package provider

import (
	"context"
	"errors"
	"fmt"
)

// GateSet is one Gate per backend (C151.1): each backend has its own provider_state row, quote, trips and cold start, so
// each Gate carries its own GateConfig (Backend, Engine, ColdStartS, MaxRuntime). The default gate answers the M3
// single-backend questions. Immutable after construction.
type GateSet struct {
	def   string
	order []string
	by    map[string]*Gate
}

// NewGateSet builds the set; def names the default backend's gate. Gate names come from GateConfig.BackendName.
func NewGateSet(def string, gs ...*Gate) (*GateSet, error) {
	s := &GateSet{def: def, by: make(map[string]*Gate, len(gs))}
	for _, g := range gs {
		n := g.Config().BackendName()
		if n == "" {
			return nil, errors.New("provider: gate without a backend name")
		}
		if _, dup := s.by[n]; dup {
			return nil, fmt.Errorf("provider: duplicate gate %q", n)
		}
		s.by[n] = g
		s.order = append(s.order, n)
	}
	if _, ok := s.by[def]; !ok && len(gs) > 0 {
		return nil, fmt.Errorf("provider: default gate %q is not in the set", def)
	}
	return s, nil
}

// Default is the default backend's gate; nil for an empty set.
func (s *GateSet) Default() *Gate {
	if s == nil {
		return nil
	}
	return s.by[s.def]
}

// For returns the gate of the named backend.
func (s *GateSet) For(backend string) (*Gate, bool) {
	if s == nil {
		return nil, false
	}
	g, ok := s.by[backend]
	return g, ok
}

// Names lists the backends in construction order.
func (s *GateSet) Names() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.order...)
}

// StateFor reads one backend's state through its own gate; ErrNoBackend when it has none.
func (s *GateSet) StateFor(ctx context.Context, backend string) (State, error) {
	g, ok := s.For(backend)
	if !ok {
		return State{}, ErrNoBackend
	}
	return g.State(ctx)
}

// StateIn is StateFor through q (use the *sql.Tx inside a write transaction).
func (s *GateSet) StateIn(ctx context.Context, q querier, backend string) (State, error) {
	g, ok := s.For(backend)
	if !ok {
		return State{}, ErrNoBackend
	}
	return g.StateIn(ctx, q)
}
