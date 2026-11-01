package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
)

// registerNextActionsL3 registers list_next_actions (C139). It is generic: L3 adds no dedicated MCP tool (operator
// decision of 2026-11-01). The item texts name a Muse by an id prefix until D-L3-16 is approved. Lane NA1 owns the handler body.
func registerNextActionsL3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_next_actions", Method: "GET", Path: "/api/next-actions",
		Summary:     "List next actions",
		Description: "What the library needs next, ranked by priority: Muse-aware hints that name a free operation to take. Resolving the condition removes the item. Never spends.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/"},
		func(ctx context.Context, in *listNextActionsIn) (*nextActionsOut, error) {
			if d.NextActions == nil {
				return nil, unavailable("nextactions")
			}
			page, err := d.NextActions.List(ctx, auth.FromContext(ctx), nextactions.Filter{
				SubjectKind: in.SubjectKind, SubjectID: in.SubjectID, MinPriority: in.MinPriority, Limit: in.Limit,
				Cursor: in.Cursor, Audience: nextactions.Audience(in.Audience),
			})
			if err != nil {
				return nil, m2bErr(err, taskNA1)
			}
			return &nextActionsOut{Body: nextActionsPage(page)}, nil
		})
}

// nextActionsPage maps the service page to the wire shape; every list is non-null and every evidence map non-nil.
func nextActionsPage(p nextactions.Page) NextActionsPage {
	out := NextActionsPage{Items: make([]NextAction, len(p.Items)), Groups: make([]NextActionGroup, len(p.Groups)),
		Total: p.Total, NextCursor: p.NextCursor, GeneratedAt: p.GeneratedAt}
	for i, a := range p.Items {
		na := NextAction{Code: a.Code, Priority: a.Priority, Audience: string(a.Audience),
			Subject: NextActionSubject{Kind: a.Subject.Kind, ID: a.Subject.ID}, Text: a.Text, Evidence: a.Evidence}
		if na.Evidence == nil {
			na.Evidence = map[string]any{}
		}
		if a.Hint != nil {
			na.Hint = &NextActionHint{Op: a.Hint.Op, Args: a.Hint.Args}
		}
		out.Items[i] = na
	}
	for i, g := range p.Groups {
		out.Groups[i] = NextActionGroup{Code: g.Code, More: g.More}
	}
	return out
}
