package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerCratesL2 registers the five Crate operations of C129. STUB(L2-K): lane K replaces the handler bodies only.
func registerCratesL2(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_crates", Method: "GET", Path: "/api/crates",
		Summary:     "List Crates",
		Description: "Crates (working sets of Songs or Takes, manual or saved filters) by kind, subject or name. Smart Crate counts are computed only with counts=true.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/crates"},
		func(ctx context.Context, in *listCratesIn) (*cratesOut, error) {
			if d.Crates == nil {
				return nil, unavailable("crates")
			}
			return nil, notImplemented(taskK) // STUB(L2-K)
		})

	Register(a, huma.Operation{
		OperationID: "get_crate", Method: "GET", Path: "/api/crates/{id}",
		Summary:     "Get a Crate",
		Description: "One Crate with its live count; list its members through list_songs or search_tracks with crate_id.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/crates/:id"},
		func(ctx context.Context, in *getCrateIn) (*crateDetailOut, error) {
			if d.Crates == nil {
				return nil, unavailable("crates")
			}
			return nil, notImplemented(taskK) // STUB(L2-K)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_crate", Method: "POST", Path: "/api/crates",
		Summary:     "Create a Crate",
		Description: "Creates a manual Crate (with up to 200 Songs or Takes) or a smart Crate (a saved list_songs or search_tracks filter). Returns the live count. Needs an Idempotency-Key.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/crates", Idempotency: "create"},
		func(ctx context.Context, in *createCrateIn) (*crateDetailOut, error) {
			if d.Crates == nil {
				return nil, unavailable("crates")
			}
			return nil, notImplemented(taskK) // STUB(L2-K)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_crate", Method: "PATCH", Path: "/api/crates/{id}",
		Summary:     "Update a Crate",
		Description: "Renames, hides, or changes the saved filter of a smart Crate under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/crates/:id"},
		func(ctx context.Context, in *updateCrateIn) (*crateDetailOut, error) {
			if d.Crates == nil {
				return nil, unavailable("crates")
			}
			return nil, notImplemented(taskK) // STUB(L2-K)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "edit_crate_items", Method: "POST", Path: "/api/crates/{id}/items",
		Summary:     "Edit a Crate's items",
		Description: "Adds and removes up to 200 members each of a manual Crate. Adding a present member counts as already; removing an absent one as not_member. Not revision-locked.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/crates/:id"},
		func(ctx context.Context, in *editCrateItemsIn) (*itemsResultOut, error) {
			if d.Crates == nil {
				return nil, unavailable("crates")
			}
			return nil, notImplemented(taskK) // STUB(L2-K)
		})
}
