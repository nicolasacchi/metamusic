package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/curation"
)

// settingsOut renders the settings for viewer: the operator's key is hidden from agents by RefFor, and a store that has never
// been written has no author and no time.
func settingsOut(viewer auth.Principal, s curation.Settings) CurationSettings {
	out := CurationSettings{ExportMuseNames: s.ExportMuseNames, SubsonicMuseNames: s.SubsonicMuseNames}
	if s.UpdatedAt != "" {
		at := s.UpdatedAt
		out.UpdatedAt = &at
	}
	if s.UpdatedBy != "" {
		kind, name, _ := strings.Cut(s.UpdatedBy, ":")
		ref := RefFor(viewer, s.UpdatedBy, kind, name)
		out.UpdatedBy = &ref
	}
	return out
}

// registerSettingsL2 registers the two curation-settings operations of C129 (lane A): the read for everyone with read scope,
// and the human-only write under /human/api, which the registry refuses for any other principal before the handler runs.
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
			set, err := d.Curation.Get(ctx)
			if err != nil {
				return nil, m2bErr(err, taskA)
			}
			return &curationSettingsOut{Body: settingsOut(auth.FromContext(ctx), set)}, nil
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
			viewer := auth.FromContext(ctx)
			set, err := d.Curation.Set(ctx, viewer, curation.SettingsPatch{ExportMuseNames: in.Body.ExportMuseNames, SubsonicMuseNames: in.Body.SubsonicMuseNames})
			if err != nil {
				return nil, m2bErr(err, taskA)
			}
			return &curationSettingsOut{Body: settingsOut(viewer, set)}, nil
		})
}
