package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerContext(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_track_context", Method: "GET", Path: "/api/tracks/{id}/context",
		Summary:     "Get a track's context bundle",
		Description: "Everything needed to critique and regenerate one take in a single size-bounded document: recipe, score, lineage, comments, perception and next actions. The _budget key reports the size.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *contextIn) (*contextOut, error) {
			if d.Context == nil {
				return nil, unavailable("context")
			}
			b, err := d.Context.Build(ctx, auth.FromContext(ctx), in.ID, in.Include)
			if err != nil {
				return nil, judgErr(err)
			}
			return &contextOut{Body: b}, nil
		})
}
