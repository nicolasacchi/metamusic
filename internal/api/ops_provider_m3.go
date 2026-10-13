package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerProviderM3 registers the provider operations (C81). Both handlers are STUB(M3-B6).
func registerProviderM3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_provider_status", Method: "GET", Path: "/api/provider",
		Summary:     "Get the generation provider status",
		Description: "Whether generation is enabled, armed or tripped; the open trip; worker counts; the endpoint without its id; price, real-time factors and limits.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *struct{}) (*providerStatusOut, error) {
			if d.Provider == nil {
				return nil, unavailable("provider")
			}
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_provider_state", Method: "POST", Path: "/human/api/provider/state",
		Summary:     "Arm, disarm or reset the provider",
		Description: "The operator arms or disarms generation and/or resets an open trip. A reason is required. Arming after a restore waits for a billing reconciliation.",
	}, bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/spend"},
		func(ctx context.Context, in *updateProviderStateIn) (*providerStatusOut, error) {
			if d.Provider == nil {
				return nil, unavailable("provider")
			}
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
		})
}
