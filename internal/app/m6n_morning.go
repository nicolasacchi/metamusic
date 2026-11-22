package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/morning"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// The Morning Crate wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build): the service, the daily
// build and the daily sweep as app.Services.Loops (G1 C324, MM_TZ semantics: an empty zone is the process zone), and the resolver
// that serves a built side by the sha256 of its file. The service never spends, so it needs neither the spend nor the jobs services.
// The night batch the build waits for is wired by the reveal lane (C4); until then a build never waits.
func init() { RegisterBuildHook(wireMorning) }

func wireMorning(env *BuildEnv) error {
	sv := env.Services
	b := env.Options.MorningBuilder
	if b == nil && sv.Media != nil && sv.Media.Lazy != nil && env.Data != nil {
		// the lazy analyzer answers "tool missing" until the ffmpeg probe has succeeded, so a build before that fails cleanly
		b = morning.NewBuilder(env.Data, env.Cfg.DataDir, mediatool.Tools{FFmpeg: env.Cfg.FFmpeg, FLAC: env.Cfg.FLAC, Metaflac: env.Cfg.Metaflac}, sv.Media.Lazy)
	}
	svc := morning.New(morning.ServiceOptions{Store: env.Store, Data: env.Data, Builder: b, Signer: sv.Deps.Signer, Clock: nowClock{env.Now}, Loc: env.Cfg.Zone(), NewID: env.NewID})
	sv.Deps.Morning = svc
	if err := svc.Recover(env.Ctx); err != nil { // a build row left by a process that died is failed, not left blocking
		slog.Warn("morning: recover at startup", "err", redact.Error(err))
	}
	env.AddResolver(svc)
	env.AddLoop(Loop{Name: "morning-crate", Run: svc.Daily().Loop})
	env.AddLoop(Loop{Name: "morning-sweep", Run: svc.SweepDaily().Loop})
	env.AddLoop(Loop{Name: "morning-close", Run: func(ctx context.Context) { <-ctx.Done(); svc.Close() }})
	return nil
}

// nowClock reads the time from the build's clock (a rig advances it) and sleeps on the wall clock.
type nowClock struct{ now func() time.Time }

func (c nowClock) Now() time.Time { return c.now() }

func (nowClock) NewTimer(d time.Duration) clock.Timer { return clock.Real.NewTimer(d) }
