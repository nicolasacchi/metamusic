package app

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/nightshift"
)

// The M6N night shift wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build). The runner is built only
// when the spend path exists (generation, mandates, the duel ladder and the jobs service that cancels). Its Daily loop is always
// registered, but the night shift is OFF in the database until the Owner enables it in the app, so on a fresh deploy the loop wakes
// once a minute, reads one row and does nothing. It spends only through the ordinary estimate and create path under a live mandate.
func init() { RegisterBuildHook(wireNightShift) }

// nightResumeEvery is how often a night that nothing works on any more is looked for (C375.6).
const nightResumeEvery = time.Minute

func wireNightShift(env *BuildEnv) error {
	sv := env.Services
	if sv.Generation == nil || sv.Deps.Mandates == nil || sv.Duels == nil || sv.Jobs == nil || sv.Audit == nil {
		return nil
	}
	var backlog generation.BacklogGate
	if env.late != nil {
		backlog = &env.late.backlog
	}
	r := nightshift.New(nightshift.Options{
		Store: env.Store, Audit: sv.Audit, Generation: sv.Generation, Mandates: sv.Deps.Mandates,
		Reader: &nightshift.Reader{Strengths: sv.Duels, Backlog: backlog},
		Cancel: func(ctx context.Context, p auth.Principal, batchID, reason string) error {
			_, err := sv.Jobs.CancelBatch(ctx, p, batchID, reason)
			return err
		},
		Now: env.Now, NewID: env.NewID, Loc: env.Cfg.Zone(),
	})
	sv.NightShift = r
	sv.Deps.NightShift = r
	daily := r.Daily(env.Cfg.Zone())
	env.AddLoop(Loop{Name: "night-shift", Run: daily.Loop})
	env.AddLoop(Loop{Name: "night-shift-resume", Run: func(ctx context.Context) { r.Watch(ctx, nightResumeEvery) }})
	return nil
}
