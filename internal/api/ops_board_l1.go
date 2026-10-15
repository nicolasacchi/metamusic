package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerBoardL1 registers the Muse Board (C96, C97). The handler is a stub until L1-B5 (STUB(L1-B5)). The Board never
// calls a spend operation.
func registerBoardL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_muse_board", Method: "GET", Path: "/api/board",
		Summary:     "Get the Muse Board",
		Description: "The grid of Muses (rows) by angles (columns) with Song counts or chips, filtered by session, angle, state or Sound facets, row-paged under a 90 000-byte page cap; the Unassigned row has its own cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/board"},
		func(ctx context.Context, in *boardIn) (*boardOut, error) {
			if d.Board == nil {
				return nil, unavailable("board")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})
}
