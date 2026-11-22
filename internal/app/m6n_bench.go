package app

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/criticbench"
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/nightshift"
)

// The M6N Critic Bench wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build). It reads analyses,
// ratings and duel verdicts and writes nothing. The Tier B critics read the stored listening-check scalars through the Ears
// service and the label critic reads the Owner's Diction Lab marks; both are added only when that service is wired, so a build
// without the Ears track has neither critic (absent, not failing). This hook runs after the Ears hook (file order), which sets
// Deps.Diction.
func init() { RegisterBuildHook(wireCriticBench) }

func wireCriticBench(env *BuildEnv) error {
	svc := criticbench.New(criticbench.Options{Store: env.Store, Now: env.Now})
	for _, src := range benchSources(env) {
		svc.Use(src)
	}
	env.Services.Deps.CriticBench = svc
	return nil
}

// benchSources are the critic sources the wired services allow.
func benchSources(env *BuildEnv) []criticbench.Source {
	var out []criticbench.Source
	if env.Services.Ears != nil {
		out = append(out, criticbench.NewTierB(earsScalars{env.Services.Ears}))
	}
	if env.Services.Deps.Diction != nil {
		out = append(out, criticbench.NewLabels(env.Services.Deps.Diction))
	}
	return out
}

var _ diction.LabelSource = (*diction.Service)(nil)

// earsScalars adapts the Ears service's stored Tier B scalars to the bench's reader (the bench never imports Ears).
type earsScalars struct{ svc *ears.Service }

func (e earsScalars) TierBScalars(ctx context.Context, ids []string) (map[string]criticbench.TierBScalar, error) {
	m, err := e.svc.TierBScalars(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("app: tier B scalars: %w", err)
	}
	out := make(map[string]criticbench.TierBScalar, len(m))
	for id, s := range m {
		out[id] = criticbench.TierBScalar{CER: s.CER, WER: s.WER, MelodyChroma: s.MelodyChroma}
	}
	return out, nil
}

// BenchTrust adapts the bench to the night planner's tie-break input (nightshift may not import the bench). The night runner
// (M6N-N2) passes it to nightshift.TieBreakFor; trust never stops a night, so the runner logs an error and plans without it.
type BenchTrust struct{ Bench *criticbench.Service }

var _ nightshift.TrustSource = BenchTrust{}

// TrustedMetrics implements nightshift.TrustSource.
func (b BenchTrust) TrustedMetrics(ctx context.Context, tracks []string) ([]nightshift.TrustedCritic, error) {
	ms, err := b.Bench.TrustedMetrics(ctx, tracks)
	if err != nil {
		return nil, err
	}
	out := make([]nightshift.TrustedCritic, len(ms))
	for i, m := range ms {
		out[i] = nightshift.TrustedCritic{ID: m.ID, Direction: m.Direction, Values: m.Values}
	}
	return out, nil
}
