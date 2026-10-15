package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerPromptsM4a registers get_prompt (C112.2, C113). STUB(M4a-A1): replaced by the agent lane.
func registerPromptsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_prompt", Method: "GET", Path: "/api/prompts/{name}",
		Summary:     "Render a prompt",
		Description: "Renders one of the built-in prompts (critique_track, write_variant, session_review) for a track or a listening session: the messages an agent or the web UI hands to a model. critique_track and write_variant need track_id.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(context.Context, *getPromptIn) (*promptOut, error) {
			if d.Prompts == nil {
				return nil, unavailable("prompts")
			}
			return nil, notImplemented("M4a-A1") // STUB(M4a-A1)
		})
}
