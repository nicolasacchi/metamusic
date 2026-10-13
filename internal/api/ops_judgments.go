package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerJudgments(a huma.API, d Deps) {
	Register(a, withBody(huma.Operation{
		OperationID: "rate_track", Method: "PUT", Path: "/api/tracks/{id}/rating",
		Summary:     "Rate a track",
		Description: "Sets or clears your 1-5 star rating and short verdict. Humans rate as the operator; agent ratings are kept apart as the critic.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		stub[rateTrackIn, ratingOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, withBody(huma.Operation{
		OperationID: "update_track", Method: "PATCH", Path: "/api/tracks/{id}",
		Summary:     "Update a track",
		Description: "Changes the title and/or the status (draft, to_relisten, final, discarded). The audio is never touched.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		stub[updateTrackIn, trackDetailOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, withBody(huma.Operation{
		OperationID: "set_tags", Method: "PATCH", Path: "/api/tracks/{id}/tags",
		Summary:     "Add or remove tags",
		Description: "Adds and removes free-form tags. Tags normalise to lower case; a human removal wins over agent tags.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		stub[setTagsIn, tagsOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, huma.Operation{
		OperationID: "list_tags", Method: "GET", Path: "/api/tags",
		Summary:     "List tags",
		Description: "Every active tag with its track count and counts per source.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/"},
		stub[empty, tagCountsOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, withBody(created(huma.Operation{
		OperationID: "add_comment", Method: "POST", Path: "/api/tracks/{id}/comments",
		Summary:     "Comment on a track",
		Description: "Adds a comment, optionally at a second of the track (kind note, fix, keep or marker). The section label is computed from timed sections only.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id", Idempotency: "create"},
		stub[addCommentIn, commentOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, withBody(huma.Operation{
		OperationID: "update_comment", Method: "PATCH", Path: "/api/comments/{id}",
		Summary:     "Update a comment",
		Description: "Edits the body or kind, hides it, or changes its state (open, resolved; reopen). Authors and the operator may edit; agents only their own.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		stub[updateCommentIn, commentOut]("M2a-B4b")) // STUB(M2a-B4b)

	Register(a, huma.Operation{
		OperationID: "list_comments", Method: "GET", Path: "/api/tracks/{id}/comments",
		Summary:     "List comments",
		Description: "A track's comments, newest first or by time, filtered by state and kind. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		stub[listCommentsIn, commentPageOut]("M2a-B4b")) // STUB(M2a-B4b)
}
