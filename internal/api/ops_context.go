package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerContext(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_track_context", Method: "GET", Path: "/api/tracks/{id}/context",
		Summary:     "Get a track's context bundle",
		Description: "Everything needed to critique and regenerate one take in a single size-bounded document: recipe, score, lineage, comments, perception and next actions. The _budget key reports the size.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		stub[contextIn, contextOut]("M2a-B8")) // STUB(M2a-B8)
}
