package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSongsL1 registers the Song operations (C96). The handlers are stubs until L1-B5 (STUB(L1-B5)).
func registerSongsL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_songs", Method: "GET", Path: "/api/songs",
		Summary:     "List Songs",
		Description: "Songs (one idea each, however many takes were rendered) filtered by Muse, angle, state, session, Sound facets, draft or take; draft_id and track_id return the single Song of that draft or take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:agent-face (the Sound browser lists Songs through search_sounds)"},
		func(ctx context.Context, in *listSongsIn) (*songsOut, error) {
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, huma.Operation{
		OperationID: "get_song", Method: "GET", Path: "/api/songs/{id}",
		Summary:     "Get a Song",
		Description: "One Song with its Muse, takes, current Pick and proposals, recent Pick events, Sound facets and sessions. With by=draft or by=track the id is a draft or take id. A retired Song answers 200 with retired_into.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/songs/:id"},
		func(ctx context.Context, in *getSongIn) (*songViewOut, error) {
			if d.SongView == nil {
				return nil, unavailable("songview")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_song", Method: "PATCH", Path: "/api/songs/{id}",
		Summary:     "Update a Song",
		Description: "Renames a Song, sets or clears its angle and Muse, or hides it, under optimistic locking: send the revision you read. A cleared angle is never refilled by the backfill.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/songs/:id"},
		func(ctx context.Context, in *updateSongIn) (*songCoreOut, error) {
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})
}
