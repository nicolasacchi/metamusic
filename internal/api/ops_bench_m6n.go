package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/criticbench"
)

// registerBenchM6N registers get_critic_bench (M6N C387). Owner-only: the disagreement lists name takes and the reference is the
// Owner's own ratings (D-M6N-8). It never writes.
func registerBenchM6N(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_critic_bench", Method: "GET", Path: "/api/critic-bench",
		Summary: "Measure how well each critic agrees with the Owner",
		Description: "Every critic (Tier A measurements such as true peak, the stars and duel verdicts of each agent, later the listening-check scalars " +
			"and the diction classifier) against the Owner's own judgement: pairs of takes in the same lineage with different Owner stars, and the " +
			"Owner's blind duel verdicts. Per critic and reference: pairs, effective sample size, agreement rate and its Wilson 95 % interval, and a " +
			"state: trusted (n_eff at least 30 and lower bound above 0.55), contrary (n_eff at least 30 and upper bound below 0.45), insufficient " +
			"(n_eff under 30) or unknown. A direction is a hypothesis, not a finding. Nothing is discarded or acted on automatically. " +
			"Pass critic for one critic and its disagreements. Reads only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/critics", OwnerOnly: true},
		func(ctx context.Context, in *getCriticBenchIn) (*criticBenchOut, error) {
			if d.CriticBench == nil {
				return nil, unavailable("criticbench")
			}
			b, err := d.CriticBench.Bench(ctx, criticbench.Query{Critic: in.Critic, IncludeUnknown: in.IncludeUnknown})
			if err != nil {
				return nil, m2bErr(err, "M6N-B1")
			}
			return &criticBenchOut{Body: *b}, nil
		})
}
