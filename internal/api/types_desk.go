package api

import (
	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// Types of the Composer's Desk operations (M5b C359). Domain shapes live in internal/desk and internal/score/abc; this file holds
// the request inputs and the envelopes. No Desk type carries money (C359.1), so no shape rules are needed.

// bodyDesk caps the bodies of the Desk operations that carry score text (C359.1).
const bodyDesk = 300 << 10

type listPlansIn struct {
	JobID       string         `query:"job_id" doc:"Only the plans of this plan job"`
	BatchID     string         `query:"batch_id" doc:"Only the plans of this batch"`
	BaseTrackID string         `query:"base_track_id" doc:"Only the plans made for this take (its own plan and every edit of it)"`
	Source      string         `query:"source" enum:"job,take,edit" doc:"job: a plan job's seed; take: a take's own score; edit: bar edits of another plan"`
	Verdict     string         `query:"verdict" enum:"liked,rejected,none" doc:"By your own verdict: the Owner's for a person, the critic's for an agent"`
	Rendered    optParam[bool] `query:"rendered" doc:"true: only plans with a take rendered from them; false: only the others"`
	Limit       int            `query:"limit" minimum:"0" maximum:"100" doc:"Default 50"`
	Cursor      string         `query:"cursor"`
}
type planPageOut struct{ Body desk.Page }

type getPlanIn struct {
	ID      string `path:"id" doc:"Plan id"`
	Include string `query:"include" doc:"Comma list of abc, bars, display, auditions, renders. Default: abc,display,auditions,renders"`
}
type planOut struct{ Body desk.Plan }

// planCreatedOut answers 201 for a new plan and 200 for an existing one.
type planCreatedOut struct {
	Status int
	Body   desk.Plan
}

type checkABCIn struct {
	Body struct {
		ABC     string `json:"abc" minLength:"1" doc:"The score text to check (at most 64 KiB)"`
		BaseABC string `json:"base_abc,omitempty" doc:"A score to compare with: the report then lists the structural violations of an edit"`
		Dialect string `json:"dialect,omitempty" enum:"yue2,standard" doc:"Default yue2"`
	}
}
type checkABCOut struct{ Body abc.Report }

type createPlanFromTakeIn struct {
	IdemHeader
	ID string `path:"id" doc:"Take id"`
}

type editPlanIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Plan id"`
	Body struct {
		Ops       []abc.Op `json:"ops" minItems:"1" maxItems:"20" doc:"replace_bars, transpose_bars or set_tempo, applied in order; bars are 0-based and inclusive"`
		Rationale string   `json:"rationale,omitempty" maxLength:"1000" doc:"Why, in a sentence"`
	}
}

type setPlanVerdictIn struct {
	ID   string `path:"id" doc:"Plan id"`
	Body struct {
		Verdict *string `json:"verdict" required:"true" nullable:"true" doc:"liked, rejected, or null to clear your verdict"`
		Note    *string `json:"note,omitempty" minLength:"1" maxLength:"2000"`
	}
}
