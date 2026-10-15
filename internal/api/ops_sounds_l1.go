package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSoundsL1 registers the Sound operations (C96). The handlers are stubs until L1-B6 (STUB(L1-B6)).
func registerSoundsL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "search_sounds", Method: "GET", Path: "/api/sounds/search",
		Summary:     "Search Sounds",
		Description: "Songs filtered by Sound facets (genre family, era, tempo band, vocal, mood, language, engine) with facet counts that keep a selected value's siblings visible; limit 0 returns the counts only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/sounds"},
		func(ctx context.Context, in *searchSoundsIn) (*soundSearchOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B6") // STUB(L1-B6)
		})

	Register(a, huma.Operation{
		OperationID: "get_sound_taxonomy", Method: "GET", Path: "/api/sounds/taxonomy",
		Summary:     "Get the Sound taxonomy",
		Description: "The effective mapping of style terms to facet values with the version history, or with unmapped=true the most frequent style terms no mapping matches.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/sounds"},
		func(ctx context.Context, in *taxonomyIn) (*taxonomyOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B6") // STUB(L1-B6)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "map_sound_term", Method: "POST", Path: "/api/sound-terms",
		Summary:     "Map Sound terms",
		Description: "Adds or removes up to 50 style-term mappings as one new taxonomy version and recomputes the affected Songs before answering.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/sounds", Rate: "heavy"},
		func(ctx context.Context, in *mapSoundTermIn) (*mapResultOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B6") // STUB(L1-B6)
		})
}
