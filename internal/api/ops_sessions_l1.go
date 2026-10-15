package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/sessions"
)

// registerSessionsL1 registers the Session operations (C96).
func registerSessionsL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_sessions", Method: "GET", Path: "/api/sessions",
		Summary:     "List sessions",
		Description: "Sessions (an import run, a draft collection or a generation batch) newest first, with their draft, take and Song counts.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/board"},
		func(ctx context.Context, in *listSessionsIn) (*sessionsOut, error) {
			if d.Sessions == nil {
				return nil, unavailable("sessions")
			}
			p, err := d.Sessions.List(ctx, sessions.ListFilter{Kinds: in.Kind, Q: in.Q, IncludeHidden: in.IncludeHidden, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			out := &sessionsOut{}
			out.Body.Items = make([]SessionSummary, len(p.Items))
			for i, it := range p.Items {
				out.Body.Items[i] = sessionWire(it)
			}
			out.Body.NextCursor, out.Body.Total = p.NextCursor, p.Total
			return out, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_session", Method: "PATCH", Path: "/api/sessions/{id}",
		Summary:     "Update a session",
		Description: "Renames or hides a session under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/board"},
		func(ctx context.Context, in *updateSessionIn) (*sessionOut, error) {
			if d.Sessions == nil {
				return nil, unavailable("sessions")
			}
			if _, err := d.Sessions.Update(ctx, auth.FromContext(ctx), in.ID, sessions.Patch{Revision: in.Body.Revision, Name: in.Body.Name, Hidden: in.Body.Hidden}); err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			sum, err := d.Sessions.Summary(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &sessionOut{Body: sessionWire(sum)}, nil
		})
}
