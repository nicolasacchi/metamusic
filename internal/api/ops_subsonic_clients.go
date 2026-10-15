package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSubsonicClientsM4a registers the Subsonic-client management operations (C107, C113, D-M4a-24): scope
// admin, not human-only, MCP generic. STUB(M4a-S1): the handlers are replaced by the Subsonic lane.
func registerSubsonicClientsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_subsonic_clients", Method: "GET", Path: "/api/subsonic-clients",
		Summary:     "List Subsonic clients",
		Description: "The native-app credentials of the OpenSubsonic bridge with their last use, plus the server URL to enter in the app. Never key material. Needs scope admin.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic"},
		func(context.Context, *empty) (*subsonicListOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			return nil, notImplemented("M4a-S1") // STUB(M4a-S1)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_subsonic_client", Method: "POST", Path: "/api/subsonic-clients",
		Summary:     "Create a Subsonic client",
		Description: "Creates a credential for one native app: an API key (mms_...) and/or an app password for token authentication. Both are shown once; a replay of the same Idempotency-Key returns them as null. Needs scope admin.",
	}), bodyM4a), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic", Idempotency: "create",
		SecretFields: []string{"api_key", "password"}},
		func(context.Context, *createSubsonicClientIn) (*subsonicCreatedOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			return nil, notImplemented("M4a-S1") // STUB(M4a-S1)
		})

	Register(a, huma.Operation{
		OperationID: "revoke_subsonic_client", Method: "POST", Path: "/api/subsonic-clients/{id}/revoke",
		Summary:     "Revoke a Subsonic client",
		Description: "Revokes a native-app credential by id; repeating the call returns the same result. Needs scope admin.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/subsonic"},
		func(context.Context, *subsonicIDIn) (*subsonicOut, error) {
			if d.Subsonic == nil {
				return nil, unavailable("subsonic")
			}
			return nil, notImplemented("M4a-S1") // STUB(M4a-S1)
		})
}
