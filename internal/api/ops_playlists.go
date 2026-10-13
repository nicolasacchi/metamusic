package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerPlaylists(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_playlists", Method: "GET", Path: "/api/playlists",
		Summary:     "List playlists",
		Description: "Playlists with their track counts and total duration; hidden ones only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/playlists"},
		stub[includeHiddenIn, playlistsOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, huma.Operation{
		OperationID: "get_playlist", Method: "GET", Path: "/api/playlists/{id}",
		Summary:     "Get a playlist",
		Description: "One playlist with its tracks in order.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/playlists/:id"},
		stub[playlistIDIn, playlistOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_playlist", Method: "POST", Path: "/api/playlists",
		Summary:     "Create a playlist",
		Description: "Creates a playlist, optionally with an ordered list of track ids (at most 1000).",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/playlists", Idempotency: "create"},
		stub[createPlaylistIn, playlistOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, withBody(huma.Operation{
		OperationID: "update_playlist", Method: "PATCH", Path: "/api/playlists/{id}",
		Summary:     "Update a playlist",
		Description: "Renames, hides, replaces (track_ids) or extends (add_track_ids) a playlist under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/playlists/:id"},
		stub[updatePlaylistIn, playlistOut]("M2a-B6")) // STUB(M2a-B6)
}
