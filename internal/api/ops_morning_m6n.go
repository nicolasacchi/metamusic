package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/morning"
)

// registerMorningM6N registers the seven Morning Crate operations (M6N C365.3, C383). They are Owner-only (D-M6N-8): the crate is the
// Owner's curation tape. The two that write the Owner's own judgments (submit events, reveal) are person-only: an agent is refused
// 403 HUMAN_ONLY in the handler and the operations are not offered over MCP. Before the reveal no response names a take.
func registerMorningM6N(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_morning_crate_settings", Method: "GET", Path: "/api/morning-crates/settings",
		Summary:     "Get the Morning Crate settings",
		Description: "When the crate is built each day (local time), how long it is, how many takes and anchors it holds, how long the side files are kept. Behaviour only: it holds no money.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/morning", OwnerOnly: true},
		func(ctx context.Context, _ *empty) (*morningSettingsOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			set, err := d.Morning.Settings(ctx)
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningSettingsOut{Body: MorningSettingsOut(auth.FromContext(ctx), set)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_morning_crate_settings", Method: "PUT", Path: "/api/morning-crates/settings",
		Summary:     "Change the Morning Crate settings",
		Description: "Changes the fields that are sent: enabled, the local build time (HH:MM), length, side length, takes, anchors, truncated takes, retention and the wait for tonight's batch. A rule breach is 422 MORNING_SETTINGS_INVALID {field, rule}.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/morning", OwnerOnly: true},
		func(ctx context.Context, in *updateMorningSettingsIn) (*morningSettingsOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			p := auth.FromContext(ctx)
			set, err := d.Morning.UpdateSettings(ctx, p, in.Body.Patch())
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningSettingsOut{Body: MorningSettingsOut(p, set)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_morning_crates", Method: "GET", Path: "/api/morning-crates",
		Summary:     "List Morning Crates",
		Description: "Crates newest first as summaries (state, counts, length, whether revealed). A summary names no take. Filter by state.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/morning", OwnerOnly: true},
		func(ctx context.Context, in *listMorningCratesIn) (*morningCratePageOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			pg, err := d.Morning.List(ctx, auth.FromContext(ctx), morning.ListFilter{State: in.State, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningCratePageOut{Body: MorningCratePageOut(pg)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_morning_crate", Method: "GET", Path: "/api/morning-crates/{id}",
		Summary: "Get a Morning Crate",
		Description: "One crate with its sides (signed links) and chapters. Blind: until the crate is revealed every chapter is a number with the blind stars, markers and verdict " +
			"so far, and no take, title, lineage, gain or playlist is given to anyone. After the reveal each chapter names its take and the playlist is listed. Never writes.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/morning/:id", OwnerOnly: true},
		func(ctx context.Context, in *getMorningCrateIn) (*morningCrateOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			c, err := d.Morning.Get(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningCrateOut{Body: MorningCrateOut(c)}, nil
		})

	build := withBody(huma.Operation{
		OperationID: "build_morning_crate", Method: "POST", Path: "/api/morning-crates",
		Summary: "Build a Morning Crate now",
		Description: "Starts a crate out of the unjudged takes (plus the ones named in include_track_ids) and answers 202 with the building row; poll get_morning_crate. " +
			"One build at a time (409 MORNING_CRATE_BUILD_IN_PROGRESS). Free: it only reads takes that exist. Needs an Idempotency-Key.",
	}, bodySmall)
	build.DefaultStatus = 202
	Register(a, build, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/morning", OwnerOnly: true, Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *buildMorningCrateIn) (*morningCrateOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			c, err := d.Morning.Build(ctx, auth.FromContext(ctx), in.Body.In())
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningCrateOut{Body: MorningCrateOut(c)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "submit_morning_crate_events", Method: "POST", Path: "/api/morning-crates/{id}/events",
		Summary: "Submit headset and rating events of a Morning Crate",
		Description: "Up to 200 raw events (marker, strong_marker, skip, complete, play, rate with blind stars) addressed to the crate's own chapters. Idempotent by event id. " +
			"Before the reveal they write nothing to the library; the batch that gives every non-anchor chapter blind stars reveals the crate in the same call, and after " +
			"the reveal each event becomes the Owner's comment, rating or listen event at once. Only the operator and the operator's devices: an agent is refused 403 HUMAN_ONLY.",
	}, bodyMorningEvents), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:person-only", UI: "/morning/:id", OwnerOnly: true},
		func(ctx context.Context, in *submitMorningEventsIn) (*morningEventsOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			p := auth.FromContext(ctx)
			if !(p.IsPerson() && p.IsOwnerSide()) {
				return nil, Err(403, "HUMAN_ONLY", "only the operator rates a morning crate", nil)
			}
			evs := make([]morning.EventIn, 0, len(in.Body.Events))
			for _, e := range in.Body.Events {
				evs = append(evs, morning.EventIn{ID: e.ID, Side: e.Side, Chapter: e.Chapter, Kind: e.Kind, Stars: e.Stars, SideMS: e.SideMS, LatencyMS: e.LatencyMS, ClientAt: e.ClientAt})
			}
			res, err := d.Morning.SubmitEvents(ctx, p, in.ID, evs)
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			out := MorningEventsResult{Accepted: res.Accepted, Duplicates: res.Duplicates, Revealed: res.Revealed, Rejected: make([]MorningEventRejection, 0, len(res.Rejected))}
			for _, r := range res.Rejected {
				out.Rejected = append(out.Rejected, MorningEventRejection(r))
			}
			return &morningEventsOut{Body: out}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "reveal_morning_crate", Method: "POST", Path: "/api/morning-crates/{id}/reveal",
		Summary: "Reveal a Morning Crate",
		Description: "Names the crate's takes: in one transaction it maps the raw events to the Owner's comments, ratings and listen events and creates the shared playlist " +
			"\"Morning crate YYYY-MM-DD\", which every listener then sees. Revealing a revealed crate changes nothing. Only the operator and the operator's devices: an agent is refused 403 HUMAN_ONLY.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:person-only", UI: "/morning/:id", OwnerOnly: true},
		func(ctx context.Context, in *revealMorningCrateIn) (*morningCrateOut, error) {
			if d.Morning == nil {
				return nil, unavailable("morning")
			}
			p := auth.FromContext(ctx)
			if !(p.IsPerson() && p.IsOwnerSide()) {
				return nil, Err(403, "HUMAN_ONLY", "only the operator reveals a morning crate", nil)
			}
			c, err := d.Morning.Reveal(ctx, p, in.ID)
			if err != nil {
				return nil, m2bErr(err, "M6N-C4")
			}
			return &morningCrateOut{Body: MorningCrateOut(c)}, nil
		})
}
