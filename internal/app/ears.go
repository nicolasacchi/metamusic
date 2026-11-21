package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
)

// The M5b Ears track wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build): the Tier B take
// hook and the CPU-only sweep that recomputes listening checks from the stored documents.
func init() { RegisterBuildHook(wireEars) }

func wireEars(env *BuildEnv) error {
	svc := env.Services.Ears
	if svc == nil {
		return nil
	}
	env.AddTakeHook(earsjobs.TierBHook{Ears: svc})
	env.AddLoop(Loop{Name: "ears-tierb-sweep", Run: func(ctx context.Context) { svc.RunTierBSweep(ctx, ears.TierBSweepInterval) }})
	return nil
}
