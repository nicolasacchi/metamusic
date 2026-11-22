package desk_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

func replaceBar1(text string) desk.EditIn {
	return desk.EditIn{Ops: []abc.Op{{Op: abc.OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: text}}, Rationale: "a brighter second bar"}
}

// TestEdit_CreatesChild: an edit makes a child plan with its own files and leaves the parent alone (D-DESK-1).
func TestEdit_CreatesChild(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	ctx := context.Background()
	child, created, err := e.svc.Edit(ctx, bob, parent.ID, replaceBar1("CDEFGABc"))
	if err != nil || !created {
		t.Fatalf("created %v err %v", created, err)
	}
	want := strings.Replace(aligned, "CDEF|CDEF|\n% chorus", "CDEF|CDEFGABc|\n% chorus", 1)
	if child.ABC == nil || *child.ABC != want {
		t.Fatalf("child abc:\n%q\nwant\n%q", deref(child.ABC), want)
	}
	if child.Source != "edit" || child.ParentPlanID == nil || *child.ParentPlanID != parent.ID || child.Cot != parent.Cot || child.Seed != parent.Seed {
		t.Errorf("child = %+v", child.PlanSummary)
	}
	if child.Base.TrackID == nil || *child.Base.TrackID != *parent.Base.TrackID || child.Title != parent.Title || child.CreatedBy.ID != "human:bob" {
		t.Errorf("base %+v title %q by %+v", child.Base, child.Title, child.CreatedBy)
	}
	if child.Edit == nil || len(child.Edit.Ops) != 1 || child.Edit.Rationale == nil || *child.Edit.Rationale != "a brighter second bar" {
		t.Errorf("edit = %+v", child.Edit)
	}
	if child.Score.SHA256 != lt256(want) || child.Score.Bytes != len(want) || !child.Score.Aligned || child.Score.ABCTokens != nil {
		t.Errorf("score = %+v", child.Score)
	}
	// the parent is untouched and lists the child
	after, err := e.svc.Get(ctx, alice, parent.ID, desk.DefaultInclude)
	if err != nil || *after.ABC != aligned || len(after.Children) != 1 || after.Children[0].PlanID != child.ID {
		t.Fatalf("parent after: %v children %+v", err, after.Children)
	}
	// files: score.abc 0444 in a 0755 directory, a desk_plan_files row with its hash
	fi, err := e.root.Stat("plans/" + child.ID + "/score.abc")
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("score file: %v %v", fi, err)
	}
	if di, err := e.root.Stat("plans/" + child.ID); err != nil || di.Mode().Perm() != 0o755 {
		t.Errorf("plan dir: %v %v", di, err)
	}
	if got := e.scalar(`SELECT sha256 || ':' || bytes || ':' || path FROM desk_plan_files WHERE plan_id = ? AND name = 'score.abc'`, child.ID); got != lt256(want)+":"+itoa(len(want))+":plans/"+child.ID+"/score.abc" {
		t.Errorf("plan file row %q", got)
	}
	if left, _ := os.ReadDir(e.dir + "/plans"); len(left) != 1 {
		t.Errorf("plans/ holds %d entries", len(left))
	}
	// an edit of an edit
	grand, created, err := e.svc.Edit(ctx, alice, child.ID, replaceBar1("GABcCDEF"))
	if err != nil || !created || grand.ParentPlanID == nil || *grand.ParentPlanID != child.ID {
		t.Fatalf("grandchild: %v %v", created, err)
	}
}

// TestEdit_SetTempoRetimes: set_tempo changes Q, the seconds per bar and the expected audio length.
func TestEdit_SetTempoRetimes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	child, _, err := e.svc.Edit(context.Background(), alice, parent.ID, desk.EditIn{Ops: []abc.Op{{Op: abc.OpSetTempo, BPM: 60}}})
	if err != nil {
		t.Fatal(err)
	}
	if child.Q == nil || *child.Q != "1/4=60" || *child.SecondsPerBar != 4 || child.ExpectedAudioS != 24 || child.Edit.Rationale != nil {
		t.Errorf("child %+v", child.PlanSummary)
	}
	if child.Sections[1].StartS == nil || *child.Sections[1].StartS != 8 {
		t.Errorf("sections %+v", child.Sections)
	}
}

// TestEdit_SameResultReturnsExisting: the same bytes from the same parent are one plan, whatever the ops that made them.
func TestEdit_SameResultReturnsExisting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	ctx := context.Background()
	a, created, err := e.svc.Edit(ctx, alice, parent.ID, replaceBar1("CDEFGABc"))
	if err != nil || !created {
		t.Fatal(err)
	}
	two := desk.EditIn{Ops: []abc.Op{
		{Op: abc.OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "AAAAAAAA"},
		{Op: abc.OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "CDEFGABc"},
	}}
	b, created, err := e.svc.Edit(ctx, bob, parent.ID, two)
	if err != nil || created || b.ID != a.ID {
		t.Fatalf("second edit: created %v id %s want %s err %v", created, b.ID, a.ID, err)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE source = 'edit'`); n != "1" {
		t.Errorf("%s edit plans", n)
	}
	if left, _ := os.ReadDir(e.dir + "/plans"); len(left) != 1 {
		t.Errorf("plans/ holds %d entries, want 1", len(left))
	}
}

// TestEdit_NoChange: an edit that leaves the bytes alone is PLAN_NO_CHANGE and stores nothing.
func TestEdit_NoChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	_, _, err := e.svc.Edit(context.Background(), alice, parent.ID, replaceBar1("CDEF"))
	wantProblem(t, err, 422, desk.CodeNoChange)
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE source = 'edit'`); n != "0" {
		t.Errorf("%s edit plans", n)
	}
}

// TestEdit_Violations: structural violations and malformed ops are PLAN_EDIT_INVALID with the reason in details.
func TestEdit_Violations(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	parent := e.plan()
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		in     desk.EditIn
		key    string
		expect string
	}{
		{"short bar", replaceBar1("CD"), "violations", "bar_length"},
		{"unknown voice", desk.EditIn{Ops: []abc.Op{{Op: abc.OpReplaceBars, Voice: "9", BarFrom: 1, BarTo: 1, ABC: "CDEFGABc"}}}, "rule", "bad_voice"},
		{"no ops", desk.EditIn{}, "rule", "no_ops"},
		{"unknown op", desk.EditIn{Ops: []abc.Op{{Op: "explode"}}}, "rule", "bad_op"},
		{"bar out of range", desk.EditIn{Ops: []abc.Op{{Op: abc.OpReplaceBars, Voice: "1", BarFrom: 9, BarTo: 9, ABC: "CDEFGABc"}}}, "rule", "bad_range"},
		{"too many bars", desk.EditIn{Ops: []abc.Op{{Op: abc.OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "CDEFGABc|CDEFGABc"}}}, "rule", "bad_abc"},
	} {
		_, _, err := e.svc.Edit(ctx, alice, parent.ID, c.in)
		pe := wantProblem(t, err, 422, desk.CodeEditInvalid)
		if c.key == "violations" {
			v, ok := pe.Details["violations"].([]abc.Violation)
			if !ok || len(v) == 0 || v[0].Code != c.expect {
				t.Errorf("%s: details %+v", c.name, pe.Details)
			}
		} else if pe.Details[c.key] != c.expect {
			t.Errorf("%s: details %+v, want %s %s", c.name, pe.Details, c.key, c.expect)
		}
	}
	_, _, err := e.svc.Edit(ctx, alice, parent.ID, desk.EditIn{Ops: replaceBar1("CDEFGABc").Ops, Rationale: strings.Repeat("r", desk.MaxRationale+1)})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, _, err = e.svc.Edit(ctx, alice, "no-such-plan", replaceBar1("CDEFGABc"))
	wantProblem(t, err, 404, "NOT_FOUND")
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans WHERE source = 'edit'`); n != "0" {
		t.Errorf("%s edit plans after refusals", n)
	}
	if _, err := os.Stat(e.dir + "/plans"); err == nil {
		if left, _ := os.ReadDir(e.dir + "/plans"); len(left) != 0 {
			t.Errorf("refused edits left %d entries in plans/", len(left))
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
