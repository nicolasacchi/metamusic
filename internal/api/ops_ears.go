package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerEars(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_track_analysis", Method: "GET", Path: "/api/tracks/{id}/analysis",
		Summary: "Get the Ears dossier of a track",
		Description: "Measured facts about a take: loudness, bandwidth, silence spans, clipping, token-loop metrics from the model's semantic tokens, " +
			"structure and duration against the score, plus computed flags with evidence. tier is A when every applicable analyzer ran. " +
			"Read blind_spots first: it lists what these numbers cannot tell you.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackIDIn) (*dossierOut, error) {
			if d.Ears == nil {
				return nil, unavailable("ears")
			}
			do, err := d.Ears.Dossier(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "M2b-E3")
			}
			return &dossierOut{Body: do}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_track_spectrogram", Method: "GET", Path: "/api/tracks/{id}/spectrogram",
		Summary: "Get the spectrogram of a track",
		Description: "A log-frequency spectrogram JPEG (at most 100 000 bytes) with section boundaries and lyric lines drawn in. " +
			"Returns the image as MCP ImageContent; never audio. Over REST the image is base64 in image.data_base64, or pass inline=false for a signed link only. " +
			"A spectrogram shows energy over time and frequency, not pitch accuracy or words; see blind_spots. " +
			"Missing until Tier A analysis has run (ANALYSIS_UNAVAILABLE).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id", MCPImage: true},
		func(ctx context.Context, in *spectrogramIn) (*spectrogramOut, error) {
			if d.Ears == nil {
				return nil, unavailable("ears")
			}
			v, err := d.Ears.Spectrogram(ctx, auth.FromContext(ctx), in.ID, in.Inline)
			if err != nil {
				return nil, m2bErr(err, "M2b-E3")
			}
			return &spectrogramOut{Body: v}, nil
		})

	Register(a, huma.Operation{
		OperationID: "compare_tracks", Method: "GET", Path: "/api/compare",
		Summary: "Compare two takes",
		Description: "Metric, flag, recipe and verdict deltas between two takes (b minus a): loudness, clipping, silence, token loops, " +
			"which request fields changed, head-to-head duel counts and both tracks' ladder strengths. " +
			"relation says how they are related (siblings, parent_child, child_parent, same_lineage, unrelated). See blind_spots.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/compare"},
		func(ctx context.Context, in *compareIn) (*comparisonOut, error) {
			if d.Ears == nil {
				return nil, unavailable("ears")
			}
			c, err := d.Ears.Compare(ctx, in.A, in.B)
			if err != nil {
				return nil, m2bErr(err, "M2b-E3")
			}
			return &comparisonOut{Body: c}, nil
		})
}
