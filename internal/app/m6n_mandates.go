package app

import (
	"github.com/nicolasacchi/metamusic/internal/mandates"
)

// The M6N spending mandates wire themselves here (G1 C328: tracks add internal/app/<track>.go and never edit Build). The service is
// the authority a mandate gives; its operations are served from Deps. A revoke cancels the mandate's unfinished jobs through the jobs
// service in its own transaction and then wakes the runner. Without a spend or jobs service (MM_BACKEND=none builds one, but a hand-built
// Services in a test may not) nothing is wired and the operations answer 503 UNAVAILABLE {component: mandates}.
func init() { RegisterBuildHook(wireMandates) }

func wireMandates(env *BuildEnv) error {
	sv := env.Services
	if sv.Spend == nil || sv.Jobs == nil { // no jobs service: a revoke could not stop running work, so serve 503 instead of failing open
		return nil
	}
	o := mandates.Options{Store: env.Store, Spend: sv.Spend, Adoption: sv.Adoption, Now: env.Now, NewID: env.NewID, Canceller: sv.Jobs.CancelIntentsTx}
	if sv.Runner != nil {
		o.Wake = sv.Runner.Wake
	}
	svc := mandates.New(o)
	sv.Deps.Mandates = svc
	return nil
}
