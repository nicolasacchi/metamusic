package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerAtlasL3 registers get_song_atlas (C141.3). It embeds SongFilterIn, the filter shared with list_songs and
// search_sounds, converts it with songFilter and expands crate_id through Crates (D-L3-44), exactly as list_songs does. The
// default page depends on the principal, never on the transport (400 for a person or device, 100 otherwise), so REST and MCP
// agree.
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
			f, err := songFilter(in.SongFilterIn)
			if err != nil {
				return nil, m2bErr(err, taskAT2)
			}
			if f, err = expandSongCrate(ctx, d, f); err != nil {
				return nil, err
			}
			limit := in.Limit
			if limit == 0 {
				limit = atlas.AgentDefaultPage
				if auth.FromContext(ctx).ActsAsHuman() {
					limit = atlas.DefaultPage
				}
			}
			a, err := d.Atlas.Get(ctx, atlas.Query{X: in.X, Y: in.Y, ColorBy: in.ColorBy, Filter: f, Dim: in.Dim,
				NearSongID: in.NearSongID, K: in.K, Limit: limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, taskAT2)
			}
			return &atlasOut{Body: a}, nil
		})
}
