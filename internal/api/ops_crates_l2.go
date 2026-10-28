package api

import (
	"context"
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
)

// crateWire renders a Crate for viewer (principals are masked for agents like every other reference).
func crateWire(v auth.Principal, c crates.Crate) Crate {
	out := Crate{ID: c.ID, Name: c.Name, Description: c.Description, Kind: string(c.Kind), Subject: string(c.Subject), Revision: c.Revision,
		CreatedBy: RefFor(v, c.CreatedBy, kindOfKey(c.CreatedBy), nameOfKey(c.CreatedBy)), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Hidden: c.HiddenAt != nil}
	if len(c.Filter) > 0 {
		_ = json.Unmarshal(c.Filter, &out.Filter)
	}
	return out
}

func nameOfKey(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[i+1:]
		}
	}
	return key
}

func crateDetail(ctx context.Context, d Deps, c crates.Crate, n int) (*crateDetailOut, error) {
	warn, err := d.Crates.Warnings(ctx, c)
	if err != nil {
		return nil, m2bErr(err, taskK)
	}
	return &crateDetailOut{Body: CrateDetail{Crate: crateWire(auth.FromContext(ctx), c), Count: n, Warnings: warn}}, nil
}

func members(in []CrateMember) []crates.Member {
	out := make([]crates.Member, len(in))
	for i, m := range in {
		out[i] = crates.Member{SongID: m.SongID, TrackID: m.TrackID}
	}
	return out
}

func rawFilter(f map[string]any) (json.RawMessage, error) {
	if f == nil {
		return nil, nil
	}
	b, err := json.Marshal(f)
	return b, err
}

// registerCratesL2 registers the five Crate operations of C129.
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
			p, err := d.Crates.List(ctx, crates.ListFilter{Kind: crates.Kind(in.Kind), Subject: crates.Subject(in.Subject), Q: in.Q,
				IncludeHidden: in.IncludeHidden, Counts: in.Counts, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			v := auth.FromContext(ctx)
			out := &cratesOut{}
			out.Body.Items = make([]CrateSummary, len(p.Items))
			for i, it := range p.Items {
				out.Body.Items[i] = CrateSummary{Crate: crateWire(v, it.Crate), Count: it.Count}
			}
			out.Body.NextCursor, out.Body.Total = p.NextCursor, p.Total
			return out, nil
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
			c, n, err := d.Crates.Get(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			return crateDetail(ctx, d, c, n)
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
			filter, err := rawFilter(in.Body.Filter)
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			c, n, err := d.Crates.Create(ctx, auth.FromContext(ctx), crates.NewCrate{Name: in.Body.Name, Description: in.Body.Description,
				Kind: crates.Kind(in.Body.Kind), Subject: crates.Subject(in.Body.Subject), Filter: filter, Items: members(in.Body.Items)})
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			return crateDetail(ctx, d, c, n)
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
			filter, err := rawFilter(in.Body.Filter)
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			c, n, err := d.Crates.Update(ctx, auth.FromContext(ctx), in.ID, crates.Patch{Revision: in.Body.Revision, Name: in.Body.Name,
				Description: in.Body.Description, Filter: filter, Hidden: in.Body.Hidden})
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			return crateDetail(ctx, d, c, n)
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
			r, err := d.Crates.EditItems(ctx, auth.FromContext(ctx), in.ID, crates.ItemsEdit{Add: members(in.Body.Add), Remove: members(in.Body.Remove)})
			if err != nil {
				return nil, m2bErr(err, taskK)
			}
			return &itemsResultOut{Body: ItemsResult{Added: r.Added, Already: r.Already, Removed: r.Removed, NotMember: r.NotMember, Count: r.Count}}, nil
		})
}
