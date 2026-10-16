package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// registerBoardL1 registers the Muse Board (C96, C97). The Board never
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
			sounds, err := songs.ParseSoundFilters(in.Sound)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			b, err := d.Board.Get(ctx, board.Query{Detail: in.Detail, MuseID: in.MuseID, SessionID: in.SessionID, Angle: in.Angle, States: in.State, Sounds: sounds,
				Q: in.Q, IncludeHidden: in.IncludeHidden, Limit: in.Limit, Cursor: in.Cursor, UnassignedAfter: in.UnassignedAfter})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &boardOut{Body: boardWire(b)}, nil
		})
}
