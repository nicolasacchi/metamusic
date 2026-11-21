package app

import (
	"github.com/nicolasacchi/metamusic/internal/mandates"
)

// The M6N spending mandates wire themselves here (G1 C328: tracks add internal/app/<track>.go and never edit Build). The service is
// the authority a mandate gives; its operations are served from Deps. A revoke cancels the mandate's unfinished jobs through the jobs
// service in its own transaction and then wakes the runner. Without a spend service (MM_BACKEND=none builds one, but a hand-built
// Services in a test may not) nothing is wired and the operations answer 503 UNAVAILABLE {component: mandates}.
func init() { RegisterBuildHook(wireMandates) }

func wireMandates(env *BuildEnv) error {
	sv := env.Services
	if sv.Spend == nil {
		return nil
	}
	o := mandates.Options{Store: env.Store, Spend: sv.Spend, Adoption: sv.Adoption, Now: env.Now, NewID: env.NewID}
	if sv.Jobs != nil {
		o.Canceller = sv.Jobs.CancelIntentsTx
	}
	if sv.Runner != nil {
		o.Wake = sv.Runner.Wake
	}
	svc := mandates.New(o)
	sv.Deps.Mandates = svc
	return nil
}
