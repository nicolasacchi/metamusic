package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
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
	// the sweep removes only orphans older than its grace (10 minutes): a CLI that builds the services while the server runs may have a row about to be inserted (C357.2)
	if n, err := svc.Sweep(env.Ctx); err != nil {
		slog.Warn("desk sweep failed", "err", redact.Error(err))
	} else if n > 0 {
		slog.Info("desk sweep removed orphans", "paths", n)
	}
	env.Services.Deps.Desk = svc
	if env.late != nil {
		env.late.plans.target = svc // render_plan estimates read plans through this seam (C360.4)
	}
	return nil
}

// latePlanSource is generation's PlanSource before the Desk exists: a seam with no target behaves as if the Desk were not wired.
type latePlanSource struct{ target generation.PlanSource }

func (l *latePlanSource) PlansForEstimate(ctx context.Context, q lineage.Querier, ids []string) ([]generation.PlanRow, error) {
	if l.target == nil {
		return nil, errors.ErrUnsupported
	}
	return l.target.PlansForEstimate(ctx, q, ids)
}
