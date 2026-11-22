package app

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earsjobs"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/subsonic"
)

// The M5b Ears track wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build): the Tier B take
// hook and the CPU-only sweep that recomputes listening checks from the stored documents.
func init() {
	RegisterBuildHook(wireEars)
	// The simulated worker's Tier B stage of a render (C329.2): the fake backend then reports the ears capability.
	fake.RegisterSimKind(fake.SimKindEars, fake.EarsRenderHook)
	// and its analyze kind (C340.6): the fake backend then reports the analyze capability too.
	fake.RegisterSimKind(fake.SimKindAnalyze, fake.AnalyzeHook)
}

func wireEars(env *BuildEnv) error {
	svc := env.Services.Ears
	if svc == nil {
		return nil
	}
	if env.Cfg.E2E {
		// The e2e rig's reference takes are listened to by the fake recogniser, which hears this invented lyric, so the web Recover
		// flow (analyze, then Create draft from transcript) runs for real in CI.
		fake.SetAnalyzeDefaultLyrics("[verse]\nla luna dorme piano sul mare\nil vento canta una storia\n[chorus]\nil mare canta ancora\nla luna torna a casa")
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
	// An analyze job attaches its document to the existing take it listened to (C340.5).
	if err := env.AddKindIngester(jobs.KindAnalyze, earsjobs.NewAnalyzeIngester(svc)); err != nil {
		return err
	}
	// Quote and plan the listening check (C339): renders with lyric lines get one by default, priced on top of the render's timeout.
	if g := env.Services.Generation; g != nil {
		g.UseEarsQuote(earsOptions(env.Cfg))
	}
	env.AddLoop(Loop{Name: "ears-tierb-sweep", Run: func(ctx context.Context) { svc.RunTierBSweep(ctx, ears.TierBSweepInterval) }})
	return nil
}

// yue2LyricLines counts the sung lines of a lyrics text (section headers excluded), as the engine reads them.
func yue2LyricLines(lyrics string) int {
	n := 0
	for _, s := range yue2.LyricsSections(lyrics) {
		n += len(s.Lines)
	}
	return n
}

// earsOptions maps the config to the quote seam. A Config built by hand rather than by config.Load (tests, tools) has the three
// values at zero, which no valid configuration has (MM_EARS_RTF_MILLI is at least 10): the defaults apply then.
func earsOptions(c config.Config) generation.EarsOptions {
	o := generation.EarsOptions{LoadS: c.EarsLoadS, RTFMilli: c.EarsRTFMilli, TranscribeRTFMilli: c.EarsTranscribeRTFMilli, AnalyzeFetchS: c.AnalyzeFetchS,
		Language: yue2.DetectLanguage, LyricLines: yue2LyricLines}
	if c.EarsRTFMilli == 0 {
		o.LoadS, o.RTFMilli, o.TranscribeRTFMilli, o.AnalyzeFetchS = 40, 150, 40, 20
	}
	return o
}
