package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerAtlasL3 registers get_song_atlas (C141.3). It embeds SongFilterIn, the filter shared with list_songs and
// search_sounds; lane AT2 converts it with songFilter and expands crate_id through Crates in the handler (D-L3-44), and
// replaces the handler body only.
func registerAtlasL3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_song_atlas", Method: "GET", Path: "/api/atlas",
		Summary:     "Get the Song Atlas",
		Description: "A 2-D map of the library: any two axes (tempo, energy, family, vocal, era, mood, language, key, duration, bpm_requested, bpm_measured), coloured by Muse or Sound, with totals and the basis of every value. Accepts the list_songs filters.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/atlas"},
		func(ctx context.Context, in *atlasIn) (*atlasOut, error) {
			if d.Atlas == nil {
				return nil, unavailable("atlas")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskAT2)
		})
}
