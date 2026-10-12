package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func registerDrafts(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_drafts", Method: "GET", Path: "/api/drafts",
		Summary:     "List backlog drafts",
		Description: "Recipe drafts (not yet rendered recipes), by default the backlog, ordered by group then request id. Filter by text, collection, group or origin.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/backlog"},
		func(ctx context.Context, in *listDraftsIn) (*draftPageOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			p, err := d.Library.ListDrafts(ctx, library.DraftParams{Q: in.Q, Status: in.Status, Collection: in.Collection,
				GroupKey: in.GroupKey, Origin: in.Origin, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, libErr(err)
			}
			return &draftPageOut{Body: p}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_draft", Method: "GET", Path: "/api/drafts/{id}",
		Summary:     "Get a draft",
		Description: "One draft with its request, lyric structure, collections, group and recipe sources. Private reference metadata is returned to human users only; agents see has_reference.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/backlog/:id"},
		func(ctx context.Context, in *draftIDIn) (*draftOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			human := auth.FromContext(ctx).Kind == auth.KindHuman
			dd, err := d.Library.Draft(ctx, in.ID, human)
			if err != nil {
				return nil, libErr(err)
			}
			return &draftOut{Body: dd}, nil
		})
}
