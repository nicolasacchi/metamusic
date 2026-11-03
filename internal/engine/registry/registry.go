// Package registry builds the engine registry. It is the only package that imports both the engine
// interface and its implementations (M3 C70).
package registry

import (
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Options selects the optional engines.
type Options struct {
	// ACEStep registers the ACE-Step engine. The engine itself lands in a later lane (E2 F1c); until it does
	// the flag is accepted and registers nothing, so callers can already thread MM_ACESTEP through.
	ACEStep bool
}

// Default is {"yue2": yue2.Engine{Observed: ...}} plus the engines o enables. The observed artifact sets
// come from the committed provider/artifacts.observed.json (recorded from real renders in M3-X3; a
// placeholder until then).
func Default(o Options) engine.Registry {
	_ = o // no optional engine exists yet
	return engine.Registry{yue2.EngineName: yue2.Engine{Observed: provider.ObservedArtifacts()}}
}
