package app

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// t1Inputs is what the T1 wiring draws on.
type t1Inputs struct {
	cfg   config.Config
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string
}

// buildT1 constructs the transform service (C210). It returns nil when MM_TRANSFORM_ENABLED is false, which makes the seven
// operations answer 503 UNAVAILABLE {component: transform}. Nothing is started here: serve starts the runner (lane B3, which
// owns this file from here on).
func buildT1(in t1Inputs) *transform.Service {
	if !in.cfg.TransformEnabled {
		return nil
	}
	return transform.New(transform.Options{Store: in.st, Library: in.lib, Config: in.cfg, Now: in.now, NewID: in.newID})
}
