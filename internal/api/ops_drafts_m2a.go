package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func registerDraftsM2a(a huma.API, d Deps) {
	// Moved from M1's ops_drafts.go (same ids, paths and metadata).
	Register(a, huma.Operation{
		OperationID: "list_drafts", Method: "GET", Path: "/api/drafts",
		Summary:     "List backlog drafts",
		Description: "Recipe drafts (not yet rendered recipes), by default the backlog, ordered by group then request id. Filter by text, collection, group or origin (import_backlog, varianti_import, editor, agent, jsonl_upload). Each row carries its lint counts.",
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
			if d.Drafts != nil {
				if err := d.Drafts.Decorate(ctx, p.Items); err != nil {
					return nil, err
				}
			}
			return &draftPageOut{Body: p}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_draft", Method: "GET", Path: "/api/drafts/{id}",
		Summary:     "Get a draft",
		Description: "One draft with its request, lyric structure, collections, group, recipe sources, revision and lint result. Private reference metadata is returned to the operator only; agents see has_reference.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/backlog/:id"},
		func(ctx context.Context, in *draftIDIn) (*draftOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			dd, err := d.Library.Draft(ctx, in.ID, auth.FromContext(ctx).ActsAsHuman())
			if err != nil {
				return nil, libErr(err)
			}
			if d.Drafts != nil {
				if err := d.Drafts.DecorateDetail(ctx, &dd); err != nil {
					return nil, err
				}
			}
			return &draftOut{Body: dd}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "lint_recipe", Method: "POST", Path: "/api/recipes/lint",
		Summary:     "Lint song requests",
		Description: "Checks up to 200 requests against the protocol rules and the songwriting rules (sections, line counts, repeat markers, language, estimated duration, duplicates). Read-only; nothing is stored.",
	}, bodyLint), Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/backlog/:id"},
		stub[lintRecipeIn, lintOut]("M2a-B5")) // STUB(M2a-B5)

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_draft", Method: "POST", Path: "/api/drafts",
		Summary:     "Create a draft",
		Description: "Stores a new backlog draft from a song request. Drafts are always protocol-valid; songwriting warnings are returned, not blocking.",
	}), bodyDraft), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/backlog/new", Idempotency: "create"},
		stub[createDraftIn, draftOut]("M2a-B5")) // STUB(M2a-B5)

	Register(a, withBody(huma.Operation{
		OperationID: "update_draft", Method: "PATCH", Path: "/api/drafts/{id}",
		Summary:     "Update a draft",
		Description: "Edits a draft's request, label, status (backlog, archived) or parent track under optimistic locking: send the revision you read.",
	}, bodyDraft), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/backlog/:id"},
		stub[updateDraftIn, draftOut]("M2a-B5")) // STUB(M2a-B5)

	Register(a, withBody(created(huma.Operation{
		OperationID: "upload_batch_jsonl", Method: "POST", Path: "/api/drafts/batch",
		Summary:     "Upload a songs.jsonl batch",
		Description: "Creates one draft per line of a songs.jsonl text (at most 1000 lines, 6 MiB in total). All-or-nothing unless allow_partial is set.",
	}), bodyUpload), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/backlog/upload", Idempotency: "create", Rate: "heavy"},
		stub[uploadBatchIn, uploadOut]("M2a-B5")) // STUB(M2a-B5)
}
