package generation

import (
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// planPhase says whether a take's quote carries the planning phase (spend.TakeIn.Full). A render_plan take is a cot=full render
// whose score is given, so YuE2 should skip the phase: until the plan-render canary has measured that, DeskRenderSkipsPlan is false
// and the take is charged the phase like any cot=full render (D-DESK-4, conservative). Every other take keeps its own answer.
func planPhase(t *planTake, full bool) bool {
	if t.Mode == lineage.ModeRenderPlan && DeskRenderSkipsPlan {
		return false
	}
	return full
}

// ReasonDeskRender is the approval reason of a batch that renders a Desk plan (the operator's decision of 2026-11-21: a render from
// the Desk awaits the Owner). It is an API value (approval_reasons).
const ReasonDeskRender = "desk_render"

// deskRenderNeed makes a batch with a render_plan take wait for the Owner whatever the principal's own limits say: an agent within
// its auto-approve limits would otherwise be auto-approved, and an Editor already waits (editor_batch). The Owner's own click on the
// human path is the approval, so that path is left alone. A mandate that covers the batch replaces the whole need afterwards
// (mandateNeed), which is the only way a Desk render runs unattended.
func deskRenderNeed(p auth.Principal, via string, n spend.ApprovalNeed, hasRenderPlan bool) spend.ApprovalNeed {
	if !hasRenderPlan || n.Needed || (via == spend.ViaHumanCreate && !p.IsAgent() && p.IsOwnerSide()) {
		return n
	}
	n.Needed = true
	n.Reasons = append(slices.Clone(n.Reasons), ReasonDeskRender)
	return n
}

func anyPlanRender(takes []*planTake) bool {
	return slices.ContainsFunc(takes, func(t *planTake) bool { return t.Mode == lineage.ModeRenderPlan })
}

func anyStoredRender(takes []planTake) bool {
	return slices.ContainsFunc(takes, func(t planTake) bool { return t.Mode == lineage.ModeRenderPlan })
}
