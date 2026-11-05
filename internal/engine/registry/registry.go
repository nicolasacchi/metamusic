// Package registry builds the engine registry. It is the only package that imports both the engine
// interface and its implementations (M3 C70).
package registry

import (
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Options selects the optional engines.
type Options struct {
	// ACEStep registers the ACE-Step engine (MM_ACESTEP=1, default off).
	ACEStep bool
}

// Default is {"yue2": yue2.Engine{Observed: ...}} plus the engines o enables. The observed artifact sets
// come from the committed provider/artifacts.observed.json (recorded from real renders in M3-X3; a
// placeholder until then).
func Default(o Options) engine.Registry {
	reg := engine.Registry{yue2.EngineName: yue2.Engine{Observed: provider.ObservedArtifacts()}}
	if o.ACEStep {
		reg[acestep.Name] = acestep.Engine{Verified: acestep.EmbeddedFeatures()}
	}
	return reg
}

// Registry names engine.Registry for packages that must not import the engine interface next to an implementation
// (the import-graph rule of C70): the API, the app and the HTTP server hold the registry through this alias.
type Registry = engine.Registry
