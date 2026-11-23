package lineage

// The Composer's Desk modes (M5b C360.1). render_plan renders the score text of a stored plan through the ordinary estimate, approval
// and create path; its takes are ordinary yue2 takes whose recipe carries the plan's score as `abc` and whose mutation records
// params.plan_id. plan_seeds (DESK-M2) is the GPU planning job: an item mode and a job operator that creates no take, so it is never an
// Operator Ledger operator.
const (
	ModeRenderPlan = "render_plan"
	OpRenderPlan   = "render_plan"
	ModePlanSeeds  = "plan_seeds"
	OpPlanSeeds    = "plan_seeds" // job operator only: a plan job makes no take, so it is not in the operators map
	// OpABCEdit (the Song PR bar-edit operator, C362, DESK-R1) is declared in modes.go; init below registers it with the Desk operators.
)

// DeskModes are the rows the Desk adds, kept apart from the M3 table like M5Modes and EarsModes; Spec reads them. render_plan names
// its bases by plan ids (desk.plan_ids), never by a parent; the plan's own base take, when it has one, becomes the take's parent.
var DeskModes = []ModeSpec{
	{Mode: ModeRenderPlan, Operator: OpRenderPlan, Seeds: SeedsParentNew, Parent: "plans", Engines: []string{EngineYuE2}},
	// plan_seeds: the base is a parent take, one draft or a composed recipe (Parent "optional": exactly one of the three, checked by
	// the planner); the patch may set style, lyrics and cot, and abc only as null. Seeds are new ones above the base's.
	{Mode: ModePlanSeeds, Operator: OpPlanSeeds, Patch: true, Style: Optional, Lyrics: Optional, Cot: Optional, ABC: Optional,
		Seeds: SeedsNew, Parent: "optional", Engines: []string{EngineYuE2}},
}

func init() {
	operators[OpRenderPlan] = true
	operators[OpABCEdit] = true
}
