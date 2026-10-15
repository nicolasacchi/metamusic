package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerConnectorsM4a registers the connector-grant management operations (C106, C113, D-M4a-24): scope admin,
// not human-only, MCP generic. Every call is audited by the gate. STUB(M4a-C2): the handlers are replaced by the
// connectors lane (masking, ntfy push, spend_cap_changes row).
func registerConnectorsM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_connector_grants", Method: "GET", Path: "/api/connectors",
		Summary:     "List hosted-connector grants",
		Description: "The OAuth connector status (issuer, resource, allowed clients, key cache) and every connector grant with its scopes, limits and last use. Needs scope admin. Usernames and subject hints are shown to humans only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/connectors"},
		func(context.Context, *empty) (*connectorListOut, error) {
			if d.Connectors == nil {
				return nil, unavailable("connectors")
			}
			return nil, notImplemented("M4a-C2") // STUB(M4a-C2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_connector_grant", Method: "PUT", Path: "/api/connectors/{id}",
		Summary:     "Change a connector grant",
		Description: "Sets a connector grant's scopes (read is required; generate is off by default), its spend limits (the whole limits object; null means the global default) and/or revokes or restores it. Needs scope admin. The global monthly cap stays human-only, so a connector budget never exceeds it.",
	}, bodyM4a), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "generic", UI: "/human/connectors"},
		func(context.Context, *setConnectorGrantIn) (*connectorOut, error) {
			if d.Connectors == nil {
				return nil, unavailable("connectors")
			}
			return nil, notImplemented("M4a-C2") // STUB(M4a-C2)
		})
}
