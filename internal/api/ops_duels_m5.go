package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
)

// registerDuelsM5 registers suggest_duel_pairs (C258, C260). create_duel's section field is handled by registerDuels.
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
			q := duels.SuggestQuery{Scope: in.Scope, RootTrackID: in.RootTrackID, K: in.K}
			if in.SectionLabel != "" || in.SectionKind != "" || in.SectionOrdinal != 0 {
				sel := duels.SectionSelector{}
				if in.SectionLabel != "" {
					sel.Label = &in.SectionLabel
				}
				if in.SectionKind != "" {
					sel.Kind = &in.SectionKind
				}
				if in.SectionOrdinal != 0 {
					sel.Ordinal = &in.SectionOrdinal
				}
				q.Section = &sel
			}
			res, err := d.Duels.Suggest(ctx, q)
			if err != nil {
				return nil, m2bErr(err, "M5-D1")
			}
			out := PairSuggestions{Scope: res.Scope, RootTrackID: res.RootTrackID, Section: res.Section,
				Model: PairSuggestionModel{Nu: res.Model.Nu, VerdictsCounted: res.Model.VerdictsCounted}, Pairs: make([]PairSuggestion, 0, len(res.Pairs))}
			for _, p := range res.Pairs {
				out.Pairs = append(out.Pairs, PairSuggestion{ATrackID: p.ATrackID, BTrackID: p.BTrackID, ATitle: p.ATitle, BTitle: p.BTitle,
					Score: p.Score, PAWinsB: p.PAWinsB, SDA: p.SDA, SDB: p.SDB, HumanVerdict: p.HumanVerdict})
			}
			return &pairSuggestionsOut{Body: out}, nil
		})
}
