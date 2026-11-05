package provider

import (
	"errors"
	"fmt"
	"slices"
)

// ErrNoBackend means no enabled backend serves the engine (422 BACKEND_UNAVAILABLE at the API, C151.1).
var ErrNoBackend = errors.New("provider: no enabled backend for this engine")

// Set is the ordered set of backends of a process: one per engine (D-E2-1). It is immutable after construction and
// safe for concurrent use. The default backend (yue2) answers the M3 single-backend questions.
type Set struct {
	def   string
	order []string
	by    map[string]Backend
}

// NewSet builds a set. def names the default backend, which must be among bs; names must be unique and non-empty.
func NewSet(def string, bs ...Backend) (*Set, error) {
	s := &Set{def: def, by: make(map[string]Backend, len(bs))}
	for _, b := range bs {
		if b == nil {
			return nil, errors.New("provider: nil backend in set")
		}
		n := b.Name()
		if n == "" {
			return nil, errors.New("provider: backend without a name")
		}
		if _, dup := s.by[n]; dup {
			return nil, fmt.Errorf("provider: duplicate backend %q", n)
		}
		s.by[n] = b
		s.order = append(s.order, n)
	}
	if _, ok := s.by[def]; !ok && len(bs) > 0 {
		return nil, fmt.Errorf("provider: default backend %q is not in the set", def)
	}
	return s, nil
}

// Default is the default backend; nil for an empty set.
func (s *Set) Default() Backend {
	if s == nil {
		return nil
	}
	return s.by[s.def]
}

// Get returns the named backend.
func (s *Set) Get(name string) (Backend, bool) {
	if s == nil {
		return nil, false
	}
	b, ok := s.by[name]
	return b, ok
}

// Names lists the backend names in construction order.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	return slices.Clone(s.order)
}

// EngineOf is the engine the named backend renders; "" when unknown.
func (s *Set) EngineOf(name string) string {
	if b, ok := s.Get(name); ok {
		return b.Engine()
	}
	return ""
}

// Route resolves the backend name for an engine. requested may be "" (the engine's backend) or that backend's own name;
// any other value is ErrNoBackend (no cross routing). yue2 goes to the default backend; any other engine goes to the
// first backend that renders it.
func (s *Set) Route(engine, requested string) (string, error) {
	if s == nil {
		return "", ErrNoBackend
	}
	var name string
	if engine == EngineYuE2 {
		if s.Default() == nil {
			return "", ErrNoBackend
		}
		name = s.def
	} else {
		for _, n := range s.order {
			if s.by[n].Engine() == engine {
				name = n
				break
			}
		}
		if name == "" {
			return "", ErrNoBackend
		}
	}
	if requested != "" && requested != name {
		return "", ErrNoBackend
	}
	return name, nil
}
