package app

import (
	"log/slog"

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
	svc := desk.New(desk.Options{Store: env.Store, Data: env.Data, Signer: env.Services.Deps.Signer, Now: env.Now, NewID: env.NewID, Log: slog.Default()})
	// the sweep runs before any request: no write is in flight, so every directory without a row is an orphan (C357.2)
	if n, err := svc.Sweep(env.Ctx); err != nil {
		slog.Warn("desk sweep failed", "err", redact.Error(err))
	} else if n > 0 {
		slog.Info("desk sweep removed orphans", "paths", n)
	}
	env.Services.Deps.Desk = svc
	return nil
}
