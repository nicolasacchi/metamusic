package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerExports(a huma.API, d Deps) {
	Register(a, created(huma.Operation{
		OperationID: "export_playlist_zip", Method: "POST", Path: "/api/playlists/{id}/exports",
		Summary:     "Export a playlist as a ZIP",
		Description: "Builds a ZIP of the playlist's MP3 files with ID3 tags (at most 200 tracks, 2 GiB) and returns a signed download link.",
	}), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/playlists/:id", Idempotency: "create", Rate: "heavy"},
		stub[exportPlaylistIn, exportOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, huma.Operation{
		OperationID: "list_export_sources", Method: "GET", Path: "/api/exports/sources",
		Summary:     "List exportable source files",
		Description: "The imported songs.jsonl paths that export_jsonl can reproduce byte for byte, with line and recipe counts.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/export"},
		stub[empty, exportSourcesOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, withBody(created(huma.Operation{
		OperationID: "export_jsonl", Method: "POST", Path: "/api/exports/jsonl",
		Summary:     "Export songs.jsonl",
		Description: "Exports an imported source file, a track selection or drafts as a songs.jsonl that yue2 batch accepts, with the validator result and a signed download link. Small files are also returned inline.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/export", Idempotency: "create", Rate: "heavy"},
		stub[exportJSONLIn, exportOut]("M2a-B6")) // STUB(M2a-B6)

	Register(a, huma.Operation{
		OperationID: "get_export", Method: "GET", Path: "/api/exports/{id}",
		Summary:     "Get an export",
		Description: "An earlier export with a fresh download link; 410 once the file was swept (create a new export).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/export"},
		stub[exportIDIn, exportOut]("M2a-B6")) // STUB(M2a-B6)
}
