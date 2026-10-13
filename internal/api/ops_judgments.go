package api

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// judgErr maps judgments and library errors to the problem model.
func judgErr(err error) error {
	var je *judgments.Error
	if errors.As(err, &je) {
		details := map[string]any{}
		if je.Field != "" {
			details["field"] = je.Field
		}
		return Err(je.Status, je.Code, je.Msg, details)
	}
	return libErr(err)
}

// maskRating renders both rating rows' principal references for viewer (D-M2a-19).
func maskRating(v auth.Principal, tr *library.TrackRating) {
	for _, row := range []*library.RatingRow{tr.Human, tr.Critic} {
		if row != nil {
			row.By = RefFor(v, row.By.ID, row.By.Kind, row.By.Name)
		}
	}
}

// maskTrack renders the principal-valued fields of a track detail for viewer.
func maskTrack(v auth.Principal, t *library.TrackDetail) {
	t.StatusSetBy = KeyFor(v, t.StatusSetBy)
	maskRating(v, &t.Rating)
}

// renderComment fills the absolute URL and renders the author for viewer.
func renderComment(v auth.Principal, publicURL string, c *judgments.Comment) {
	c.Author = RefFor(v, c.Author.ID, c.Author.Kind, c.Author.Name)
	if c.Link != "" {
		c.URL = strings.TrimRight(publicURL, "/") + c.Link
	}
}

// commentSource maps the transport of the request to a comment origin.
func commentSource(ctx context.Context) string {
	tr, _ := reqinfo.TransportFrom(ctx)
	switch tr {
	case reqinfo.SPA:
		return "web"
	case reqinfo.MCP:
		return "mcp"
	case reqinfo.Stdio:
		return "stdio"
	}
	return "api"
}

func registerJudgments(a huma.API, d Deps) {
	Register(a, withBody(huma.Operation{
		OperationID: "rate_track", Method: "PUT", Path: "/api/tracks/{id}/rating",
		Summary:     "Rate a track",
		Description: "Sets or clears your 1-5 star rating and short verdict. Humans rate as the operator; agent ratings are kept apart as the critic.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *rateTrackIn) (*ratingOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			p := auth.FromContext(ctx)
			tr, err := d.Judgments.Rate(ctx, p, in.ID, in.Body.Stars, in.Body.Verdict)
			if err != nil {
				return nil, judgErr(err)
			}
			maskRating(p, &tr)
			return &ratingOut{Body: tr}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_track", Method: "PATCH", Path: "/api/tracks/{id}",
		Summary:     "Update a track",
		Description: "Changes the title and/or the status (draft, to_relisten, final, discarded). The audio is never touched.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *updateTrackIn) (*trackDetailOut, error) {
			if d.Judgments == nil || d.Library == nil {
				return nil, unavailable("judgments")
			}
			p := auth.FromContext(ctx)
			if err := d.Judgments.UpdateTrack(ctx, p, in.ID, in.Body.Title, in.Body.Status); err != nil {
				return nil, judgErr(err)
			}
			t, err := d.Library.Track(ctx, in.ID)
			if err != nil {
				return nil, libErr(err)
			}
			maskTrack(p, &t)
			return &trackDetailOut{Body: t}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_tags", Method: "PATCH", Path: "/api/tracks/{id}/tags",
		Summary:     "Add or remove tags",
		Description: "Adds and removes free-form tags. Tags normalise to lower case; a human removal wins over agent tags.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *setTagsIn) (*tagsOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			tt, err := d.Judgments.SetTags(ctx, auth.FromContext(ctx), in.ID, in.Body.Add, in.Body.Remove)
			if err != nil {
				return nil, judgErr(err)
			}
			return &tagsOut{Body: tt}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_tags", Method: "GET", Path: "/api/tags",
		Summary:     "List tags",
		Description: "Every active tag with its track count and counts per source.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/"},
		func(ctx context.Context, _ *empty) (*tagCountsOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			items, err := d.Judgments.TagCounts(ctx)
			if err != nil {
				return nil, judgErr(err)
			}
			out := &tagCountsOut{}
			out.Body.Items = items
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "add_comment", Method: "POST", Path: "/api/tracks/{id}/comments",
		Summary:     "Comment on a track",
		Description: "Adds a comment, optionally at a second of the track (kind note, fix, keep or marker). The section label is computed from timed sections only.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *addCommentIn) (*commentOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			p := auth.FromContext(ctx)
			c, err := d.Judgments.AddComment(ctx, p, commentSource(ctx), in.ID, judgments.NewComment{Body: in.Body.Body, AtSecond: in.Body.AtSecond, Kind: in.Body.Kind})
			if err != nil {
				return nil, judgErr(err)
			}
			renderComment(p, d.Cfg.PublicURL, &c)
			return &commentOut{Body: c}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_comment", Method: "PATCH", Path: "/api/comments/{id}",
		Summary:     "Update a comment",
		Description: "Edits the body or kind, hides it, or changes its state (open, resolved; reopen). Authors and the operator may edit; agents only their own.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *updateCommentIn) (*commentOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			p := auth.FromContext(ctx)
			c, err := d.Judgments.UpdateComment(ctx, p, in.ID, judgments.CommentPatch{Body: in.Body.Body, Kind: in.Body.Kind, State: in.Body.State, Hidden: in.Body.Hidden})
			if err != nil {
				return nil, judgErr(err)
			}
			renderComment(p, d.Cfg.PublicURL, &c)
			return &commentOut{Body: c}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_comments", Method: "GET", Path: "/api/tracks/{id}/comments",
		Summary:     "List comments",
		Description: "A track's comments, newest first or by time, filtered by state and kind. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *listCommentsIn) (*commentPageOut, error) {
			if d.Judgments == nil {
				return nil, unavailable("judgments")
			}
			p := auth.FromContext(ctx)
			page, err := d.Judgments.Comments(ctx, in.ID, judgments.CommentFilter{State: in.State, Kind: in.Kind, IncludeHidden: in.IncludeHidden,
				Order: in.Order, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, judgErr(err)
			}
			for i := range page.Items {
				renderComment(p, d.Cfg.PublicURL, &page.Items[i])
			}
			return &commentPageOut{Body: page}, nil
		})
}
