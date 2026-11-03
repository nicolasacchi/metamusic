// Package transform is the domain service of T1: CPU sound transforms whose result is an ordinary Take (engine dsp,
// D-T1-1). It never spends money and never calls a backend: it must not import api, jobs, spend, provider, generation or
// upload (TestImportGraph_T1). F1 freezes the contracts (spec, limits, codes, the kind registry); B2 and B3 build the
// service behind Service.
package transform

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options carries the service's dependencies. B2 and B3 add the fields their lanes need (Lineage, Ears, Pipeline, Tools, Data,
// Fault, ...); F1 fixes only what the API layer needs to be wired.
type Options struct {
	Store   *store.Store
	Library *library.Service
	Config  config.Config
	Now     func() time.Time
	NewID   func() string
	// Wake nudges the runner after a commit; nil-safe, injected, never an import of the runner's package (D-T1-17).
	Wake func()
}

// Service implements the seven transform operations (C212). F1 builds the skeleton so the registry can wire it and its
// operations answer 501 NOT_IMPLEMENTED naming the lane that fills them.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Enabled reports whether MM_TRANSFORM_ENABLED is on.
func (s *Service) Enabled() bool { return s != nil && s.o.Config.TransformEnabled }
