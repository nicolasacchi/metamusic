package lineage

// The Composer's Desk modes (M5b C360.1). render_plan renders the score text of a stored plan through the ordinary estimate, approval
// and create path; its takes are ordinary yue2 takes whose recipe carries the plan's score as `abc` and whose mutation records
// params.plan_id. The plan_seeds row (the GPU planning job) is DESK-M2's.
const (
	ModeRenderPlan = "render_plan"
	OpRenderPlan   = "render_plan"
	// OpABCEdit is the operator of a Song PR that edits bars of the score (C362, DESK-R1); it is listed here so the one registration
	// of Desk operators lives in one file.
	OpABCEdit = "abc_edit"
)

// DeskModes are the rows the Desk adds, kept apart from the M3 table like M5Modes and EarsModes; Spec reads them. render_plan names
// its bases by plan ids (desk.plan_ids), never by a parent; the plan's own base take, when it has one, becomes the take's parent.
var DeskModes = []ModeSpec{
	{Mode: ModeRenderPlan, Operator: OpRenderPlan, Seeds: SeedsParentNew, Parent: "plans", Engines: []string{EngineYuE2}},
}

func init() {
	operators[OpRenderPlan] = true
	operators[OpABCEdit] = true
}
