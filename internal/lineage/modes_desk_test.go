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
