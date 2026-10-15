package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerMusesL1 registers the Muse operations (C96). The handlers are stubs until L1-B5 (STUB(L1-B5)).
func registerMusesL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_muses", Method: "GET", Path: "/api/muses",
		Summary:     "List Muses",
		Description: "Muses (inspirations: a reference title and artist, or a brief) with their Song counts by state; q is a phrase-prefix search over title, artist and brief. Hidden Muses only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/board"},
		func(ctx context.Context, in *listMusesIn) (*musesOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, huma.Operation{
		OperationID: "get_muse", Method: "GET", Path: "/api/muses/{id}",
		Summary:     "Get a Muse",
		Description: "One Muse with its coverage (Songs by state), its angles and its Songs.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/muses/:id"},
		func(ctx context.Context, in *getMuseIn) (*museDetailOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_muse", Method: "POST", Path: "/api/muses",
		Summary:     "Create a Muse",
		Description: "Creates a reference Muse (title, optional artist) or a brief Muse (short title and free text). A reference whose normalised title and artist already exist answers 409 MUSE_EXISTS with the existing id.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/board", Idempotency: "create"},
		func(ctx context.Context, in *createMuseIn) (*museOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_muse", Method: "PATCH", Path: "/api/muses/{id}",
		Summary:     "Update a Muse",
		Description: "Changes a Muse's title, artist or brief, or hides it, under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/muses/:id"},
		func(ctx context.Context, in *updateMuseIn) (*museOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "assign_muse_songs", Method: "POST", Path: "/api/muses/{id}/songs",
		Summary:     "Assign a Muse to Songs",
		Description: "Assigns the Muse to 1..200 Songs, all or nothing; an assignment is never overwritten by the index backfill.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/board"},
		func(ctx context.Context, in *assignMuseSongsIn) (*assignResultOut, error) {
			if d.Muses == nil {
				return nil, unavailable("muses")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})
}
