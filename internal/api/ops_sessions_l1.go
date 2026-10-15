package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSessionsL1 registers the Session operations (C96). The handlers are stubs until L1-B5 (STUB(L1-B5)).
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
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
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
			return nil, m2bErr(errors.ErrUnsupported, "L1-B5") // STUB(L1-B5)
		})
}
