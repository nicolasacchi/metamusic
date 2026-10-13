// Package registry builds the engine registry. It is the only package that imports both the engine
// interface and its implementations (M3 C70).
package registry

import (
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Default is {"yue2": yue2.Engine{Observed: ...}}. The observed artifact sets come from the committed
// provider/artifacts.observed.json (recorded from real renders in M3-X3; a placeholder until then).
func Default() engine.Registry {
	return engine.Registry{yue2.EngineName: yue2.Engine{Observed: provider.ObservedArtifacts()}}
}
