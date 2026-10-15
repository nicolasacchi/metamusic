package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerOfflineM4a registers get_offline_set (C111, C113). STUB(M4a-A1): the handler is replaced by the agent lane.
func registerOfflineM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_offline_set", Method: "GET", Path: "/api/offline-set",
		Summary:     "Get the offline set",
		Description: "The protected offline-set playlist with per-item media facts (FLAC and MP3 sizes and hashes), so a device can plan downloads without further calls. Edit it with update_playlist, sending the revision from this response.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/offline"},
		func(context.Context, *empty) (*offlineSetOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			return nil, notImplemented("M4a-A1") // STUB(M4a-A1)
		})
}
