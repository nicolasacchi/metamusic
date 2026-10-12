package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerStats(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_stats", Method: "GET", Path: "/api/stats",
		Summary:     "Get library statistics",
		Description: "Counts of tracks, drafts and recipes, loudness spread (listen_flac_recommended when above 2 LU), bandwidth, storage and import runs.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/"},
		func(ctx context.Context, _ *empty) (*statsOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			s, err := d.Library.Stats(ctx)
			if err != nil {
				return nil, libErr(err)
			}
			return &statsOut{Body: s}, nil
		})
}
