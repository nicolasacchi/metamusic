package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
)

type createPlanAuditionIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Plan id"`
	Body struct {
		Soundfont string   `json:"soundfont,omitempty" doc:"A configured soundfont name; default the first"`
		Chords    string   `json:"chords,omitempty" enum:"none,block" doc:"How chord symbols sound; default block"`
		TailS     *float64 `json:"tail_s,omitempty" minimum:"0" maximum:"10" doc:"Seconds of tail after the last note; default 3"`
		Gain      *float64 `json:"gain,omitempty" minimum:"0.1" maximum:"1.5" doc:"Synth gain; default 0.6"`
	} `required:"false"`
}

type getPlanAuditionIn struct {
	ID  string `path:"id" doc:"Plan id"`
	AID string `path:"aid" doc:"Audition id"`
}

// auditionOut answers 201 for a freshly rendered audition and 200 for a cached one or a read.
type auditionOut struct {
	Status int
	Body   desk.Audition
}

// registerDeskAudition registers the two audition operations (M5b C359.1). An audition is a CPU render of a plan's score (no GPU,
// no spend, no approval): the Owner and Editors may make one; it is a preview, never a take.
func registerDeskAudition(a huma.API, d Deps) {
	Register(a, created(huma.Operation{
		OperationID: "create_plan_audition", Method: "POST", Path: "/api/plans/{id}/auditions",
		Summary: "Audition a plan",
		Description: "Renders the plan's score on the server through a General MIDI soundfont (CPU only, free, up to 90 s) and returns an MP3 preview with a signed url. " +
			"The same plan and settings return the stored audition (200 instead of 201) at no cost. Options: soundfont, chords (none or block, default block), tail_s, gain. " +
			"An audition is a preview, never a take; keep one as a take with create_transform kind score_render. 409 AUDITION_BUSY (retry after retry_after_s), " +
			"429 AUDITION_LIMIT, 422 AUDITION_TOO_LONG or SCORE_INVALID, 503 UNAVAILABLE naming transform, soundfont or fluidsynth. Needs an Idempotency-Key.",
	}), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/desk/plans/:id", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *createPlanAuditionIn) (*auditionOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			au, made, err := d.Desk.Audition(ctx, auth.FromContext(ctx), in.ID, desk.AuditionIn{Soundfont: in.Body.Soundfont, Chords: in.Body.Chords,
				TailS: in.Body.TailS, Gain: in.Body.Gain})
			if err != nil {
				return nil, m2bErr(err, "DESK-S2")
			}
			return &auditionOut{Status: createdStatus(made), Body: au}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_plan_audition", Method: "GET", Path: "/api/plans/{id}/auditions/{aid}",
		Summary:     "Get a plan's audition",
		Description: "One stored audition of a plan: its soundfont, synth version, chords, seconds, CPU time and a signed playback url for you.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/desk/plans/:id"},
		func(ctx context.Context, in *getPlanAuditionIn) (*auditionOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			au, err := d.Desk.GetAudition(ctx, auth.FromContext(ctx), in.ID, in.AID)
			if err != nil {
				return nil, m2bErr(err, "DESK-S2")
			}
			return &auditionOut{Status: 200, Body: au}, nil
		})
}
