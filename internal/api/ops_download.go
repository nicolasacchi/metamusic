package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerDownloadM4a registers get_download_link (C110.2, C113). STUB(M4a-M2): replaced by the media lane.
func registerDownloadM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_download_link", Method: "GET", Path: "/api/tracks/{id}/download",
		Summary:     "Get a download link with current tags",
		Description: "A signed, expiring link to a FLAC copy of the track carrying its current title, album, track number and tags (audio frames unchanged). A lossy track answers a link to its MP3 master unchanged. Humans get 12 h links, agents 15 min.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id", Rate: "heavy"},
		func(context.Context, *trackIDIn) (*downloadLinkOut, error) {
			if d.Downloads == nil {
				return nil, unavailable("downloads")
			}
			return nil, notImplemented("M4a-M2") // STUB(M4a-M2)
		})
}
