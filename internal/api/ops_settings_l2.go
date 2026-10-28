package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSettingsL2 registers the two curation-settings operations of C129. STUB(L2-A): lane A replaces the handler
// bodies only.
func registerSettingsL2(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_curation_settings", Method: "GET", Path: "/api/curation/settings",
		Summary:     "Get the curation settings",
		Description: "The two privacy opt-ins: whether tape files carry a Muse's name (export_muse_names) and whether automatic Subsonic groupings are named by their Muse (subsonic_muse_names). Both default to off.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings/curation"},
		func(ctx context.Context, _ *empty) (*curationSettingsOut, error) {
			if d.Curation == nil {
				return nil, unavailable("curation")
			}
			return nil, notImplemented(taskA) // STUB(L2-A)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_curation_settings", Method: "PUT", Path: "/human/api/curation/settings",
		Summary:     "Change the curation settings",
		Description: "Human only. Turns the Muse-name opt-ins on or off; send only the keys to change. An agent reads them with get_curation_settings.",
	}, bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/curation", HumanOnly: true},
		func(ctx context.Context, in *setCurationSettingsIn) (*curationSettingsOut, error) {
			if d.Curation == nil {
				return nil, unavailable("curation")
			}
			return nil, notImplemented(taskA) // STUB(L2-A)
		})
}
