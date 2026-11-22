package app

import (
	"log/slog"
	"path/filepath"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// The M5b Composer's Desk wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build): the store and
// service behind the plan operations, and the startup sweep of what a crash left under plans/.
func init() { RegisterBuildHook(wireDesk) }

func wireDesk(env *BuildEnv) error {
	if env.Store == nil || env.Data == nil {
		return nil // a Build without a data root (some tests) serves no Desk: its operations answer 503 UNAVAILABLE
	}
	o := desk.Options{Store: env.Store, Data: env.Data, Signer: env.Services.Deps.Signer, Now: env.Now, NewID: env.NewID, Log: slog.Default(),
		DataDir: env.Cfg.DataDir, FFmpeg: env.Cfg.FFmpeg, Zone: env.Cfg.Zone()}
	if abs, err := filepath.Abs(env.Cfg.DataDir); err == nil {
		o.DataDir = abs
	}
	if env.Services.Transform != nil { // never a typed-nil *transform.Service inside the interface
		o.Transform = env.Services.Transform
	}
	svc := desk.New(o)
	// the sweep removes only orphans older than its grace (10 minutes): a CLI that builds the services while the server runs may have a row about to be inserted (C357.2)
	if n, err := svc.Sweep(env.Ctx); err != nil {
		slog.Warn("desk sweep failed", "err", redact.Error(err))
	} else if n > 0 {
		slog.Info("desk sweep removed orphans", "paths", n)
	}
	env.Services.Deps.Desk = svc
	env.AddResolver(svc) // audition MP3s, by their own hash (C358.4)
	return nil
}
