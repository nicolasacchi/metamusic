package app

import "github.com/nicolasacchi/metamusic/internal/criticbench"

// The M6N Critic Bench wires itself here (G1 C328: tracks add internal/app/<track>.go and never edit Build). It reads analyses,
// ratings and duel verdicts and writes nothing; later lanes attach their critic sources to Services.Deps.CriticBench.
func init() { RegisterBuildHook(wireCriticBench) }

func wireCriticBench(env *BuildEnv) error {
	env.Services.Deps.CriticBench = criticbench.New(criticbench.Options{Store: env.Store, Now: env.Now})
	return nil
}
