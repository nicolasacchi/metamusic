package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerEarsB registers the Tier B (listening check) read of the M5b Ears track (C338.5). It is generic over MCP: the track's
// share of the tools/list budget is spent on the amended estimate_generation and the dossier's tier value (D-EARS-17).
func registerEarsB(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_track_tier_b", Method: "GET", Path: "/api/tracks/{id}/tier-b",
		Summary: "Get the listening check of a take",
		Description: "What speech recognition and pitch tracking heard on the separated vocals of a take: diction against the lyrics (CER and " +
			"WER overall and per section, worst section), measured section times, coarse melody adherence to the plan score and the ear-check flags. " +
			"A recogniser can mishear sung words: these are suspicions, not verdicts; read blind_spots. " +
			"409 ANALYSIS_UNAVAILABLE with the state (none, pending, missing) when the take has no listening check.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackIDIn) (*tierBOut, error) {
			if d.Ears == nil {
				return nil, unavailable("ears")
			}
			if err := d.visible(ctx, in.ID); err != nil {
				return nil, err
			}
			v, err := d.Ears.TierB(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "EARS-S1")
			}
			return &tierBOut{Body: v}, nil
		})
}
