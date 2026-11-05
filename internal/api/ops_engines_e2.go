package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerEnginesE2 registers list_engines (C160). It is a generic MCP operation (D-E2-35, operator decision of 2026-11-03: no
// dedicated tool, tools/list grows only through the amended schemas). E2-F2 froze the Meta, the path and the types; lane
// E2-OP replaces the handler body (it answers 501 NOT_IMPLEMENTED naming taskE2OP until then). The trial and Suite operations
// of E2c are registered by that plan, not here.
func registerEnginesE2(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_engines", Method: "GET", Path: "/api/engines",
		Summary: "List the render engines",
		Description: "Each engine with its adoption state, backend (armed, tripped, cold start), effective capabilities (modes, limits, stem names), " +
			"verified features and whether a generation of it can be estimated right now, and why not.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/engines"},
		func(ctx context.Context, _ *empty) (*enginesOut, error) {
			if d.Engines == nil {
				return nil, unavailable("engines")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskE2OP)
		})
}
