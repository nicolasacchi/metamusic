package lineage

import "testing"

// TestModes_DeskRows: render_plan is a yue2-only item mode and an Operator Ledger operator; it takes no patch, renders the plan's
// seed plus n-1 new ones, and may be a root (rootless is the library's list, see TestEnumOperator_DeskOperators there).
func TestModes_DeskRows(t *testing.T) {
	t.Parallel()
	m, ok := Spec(ModeRenderPlan)
	if !ok {
		t.Fatal("render_plan has no row")
	}
	if m.Operator != OpRenderPlan || m.Patch || m.Seeds != SeedsParentNew || m.Parent != "plans" {
		t.Errorf("row = %+v", m)
	}
	if !ModeAllowed(ModeRenderPlan, EngineYuE2) || ModeAllowed(ModeRenderPlan, EngineACEStep) {
		t.Error("render_plan must be yue2 only")
	}
	if IsTemplateMode(ModeRenderPlan) {
		t.Error("render_plan is not a template mode")
	}
	for _, op := range []string{OpRenderPlan, OpABCEdit} {
		if !ValidOperator(op) {
			t.Errorf("%s is not an Operator Ledger operator", op)
		}
	}
	if err := CheckOperator(OpRenderPlan); err != nil {
		t.Errorf("CheckOperator: %v", err)
	}
	for _, row := range Modes {
		if row.Mode == ModeRenderPlan {
			t.Error("render_plan leaked into the M3 table")
		}
	}
	n := 0
	for _, row := range DeskModes {
		if row.Mode == ModeRenderPlan {
			n++
		}
	}
	if n != 1 {
		t.Errorf("DeskModes holds render_plan %d times", n)
	}
}

// TestModes_PlanSeedsRow: plan_seeds is a yue2-only item mode and a job operator that is not an Operator Ledger operator.
func TestModes_PlanSeedsRow(t *testing.T) {
	t.Parallel()
	m, ok := Spec(ModePlanSeeds)
	if !ok {
		t.Fatal("plan_seeds has no row")
	}
	if m.Operator != OpPlanSeeds || !m.Patch || m.Seeds != SeedsNew || m.ABC != Optional || m.CFGScale != Forbidden {
		t.Errorf("row = %+v", m)
	}
	if !ModeAllowed(ModePlanSeeds, EngineYuE2) || ModeAllowed(ModePlanSeeds, EngineACEStep) || IsTemplateMode(ModePlanSeeds) {
		t.Error("plan_seeds must be yue2 only and no template mode")
	}
	if ValidOperator(OpPlanSeeds) || CheckOperator(OpPlanSeeds) == nil {
		t.Error("plan_seeds must not be an Operator Ledger operator")
	}
}
