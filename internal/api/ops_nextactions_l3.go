package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerNextActionsL3 registers list_next_actions (C139). It is generic: L3 adds no dedicated MCP tool (operator
// decision of 2026-11-01). Lane NA1 replaces the handler body only.
func registerNextActionsL3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_next_actions", Method: "GET", Path: "/api/next-actions",
		Summary:     "List next actions",
		Description: "What the library needs next, ranked by priority: Muse-aware hints that name a free operation to take. Resolving the condition removes the item. Never spends.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/"},
		func(ctx context.Context, in *listNextActionsIn) (*nextActionsOut, error) {
			if d.NextActions == nil {
				return nil, unavailable("nextactions")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskNA1)
		})
}
