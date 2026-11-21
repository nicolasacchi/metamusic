package app

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/subsonic"
)

// The M5b Ears track wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build): the Tier B take
// hook and the CPU-only sweep that recomputes listening checks from the stored documents.
func init() { RegisterBuildHook(wireEars) }

func wireEars(env *BuildEnv) error {
	svc := env.Services.Ears
	if svc == nil {
		return nil
	}
	// The Diction Lab reads the Tier B rows and writes only its marks and lexicon overrides; its operations are served from Deps.
	lab := diction.New(diction.Options{Store: env.Store, Library: env.Services.Deps.Library, Now: env.Now})
	env.Services.Deps.Diction = lab
	// The lab feeds /context (C345.5), the draft lint (C345.4) and the estimate planner (C345.6).
	svc.UseDiction(lab)
	// Synced Subsonic lyrics (C348): optional, and only for takes with Tier B word timings.
	if env.Subsonic != nil && !subsonic.UseDiction(env.Subsonic, lab) {
		return errors.New("ears: the Subsonic handler cannot take the Diction Lab")
	}
	if d := env.Services.Deps.Drafts; d != nil {
		d.UseDiction(lab)
	}
	if g := env.Services.Generation; g != nil {
		g.UseEars(generation.EarsSeams{DictionRisk: func(ctx context.Context, lyrics string) ([]generation.Issue, error) {
			risks, err := lab.Risks(ctx, lyrics)
			if err != nil {
				return nil, err
			}
			var out []generation.Issue
			for _, is := range yue2.DictionRiskIssues(risks) {
				out = append(out, generation.Issue{Code: is.Code, Field: is.Field, Severity: is.Severity, Message: is.Message})
			}
			return out, nil
		}})
	}
	env.AddTakeHook(earsjobs.TierBHook{Ears: svc})
	env.AddLoop(Loop{Name: "ears-tierb-sweep", Run: func(ctx context.Context) { svc.RunTierBSweep(ctx, ears.TierBSweepInterval) }})
	return nil
}
