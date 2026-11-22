package generation

import (
	"context"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// The Composer's Desk seam (M5b C360.4, amendment A5). generation never imports desk: the app injects the plan reader through
// Options.PlanSource, late-bound like PatchSource because the Desk is built by a build hook after this service. A nil PlanSource
// refuses a render_plan item with 503 UNAVAILABLE {component: "desk"}; an estimate without a Desk item never reads it.

// PlanRow is a stored Desk plan as a render needs it. Request is the plan's canonical yue2 request (no abc; this plan's seed and
// cot), Score the stored score text.
type PlanRow struct {
	ID, Engine, Cot, Source, Title string
	Seed                           int64
	Request                        json.RawMessage
	Score                          []byte
	ScoreSHA256                    string
	BaseTrackID, BaseDraftID       *string
	Truncated                      bool // the planning pass hit its token limit (a score that may end early)
	Timed                          bool // the score carries a Q: tempo, so its length is known
	ExpectedAudioS                 int  // the score-timed length, 1..600
}

// PlanSource reads Desk plans for estimate (desk.Service implements it). Plans are immutable, so create needs no plan re-check.
type PlanSource interface {
	// PlansForEstimate returns the plans in the order of ids. An unknown id is 404 NOT_FOUND; a plan whose score file is missing or
	// no longer matches its recorded hash is 409 PLAN_NOT_READY {plan_ids}.
	PlansForEstimate(ctx context.Context, q lineage.Querier, ids []string) ([]PlanRow, error)
}

// DeskItem is the Desk part of an item (C360.2): the plans a render_plan item renders. NSeeds, Seeds and Cot belong to the plan_seeds
// mode (DESK-M2); a render_plan item that sets them is INVALID_ITEM rule field_forbidden.
type DeskItem struct {
	PlanIDs []string
	NSeeds  int
	Seeds   []int64
	Cot     string
}

// DeskRenderSkipsPlan says whether YuE2 skips its planning phase when it renders from a score it is given (D-DESK-4). It is false
// until the plan-render canary has measured the phase: a render_plan take is quoted like a cot=full render (the planning term
// included), which over-quotes rather than under-quotes. The flip is a one-line PR the operator approves.
const DeskRenderSkipsPlan = false
