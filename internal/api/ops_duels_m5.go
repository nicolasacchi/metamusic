package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerDuelsM5 registers suggest_duel_pairs (C258, C260). Frozen by M5-F2; lane M5-D1 fills the handler (and adds the id to
// implementedDuelsM5 in ops_duels_m5_test.go). create_duel's section field is gated in ops_duels.go until D1 lands (see
// sectionDuelPending).
func registerDuelsM5(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "suggest_duel_pairs", Method: "GET", Path: "/api/duels/suggestions",
		Summary:     "Suggest duel pairs",
		Description: "The pairs of takes whose next human verdict would teach the ladder most, by expected information, for one lineage or the whole library, optionally for the same section of both takes.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/duels"},
		func(ctx context.Context, in *suggestDuelPairsIn) (*pairSuggestionsOut, error) {
			if d.Duels == nil {
				return nil, unavailable("duels")
			}
			return nil, notImplemented(taskD1)
		})
}
