package desk_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// TestPlansForEstimate: the rows a render_plan estimate reads. A take plan carries its score, request without abc, base take and
// timing; an edit plan is marked edited and keeps its own score; the order of the ids is kept.
func TestPlansForEstimate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	take := e.plan()
	child, _, err := e.svc.Edit(ctx, alice, take.ID, desk.EditIn{Ops: []abc.Op{{Op: abc.OpSetTempo, BPM: 60}}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.PlansForEstimate(ctx, e.st.R, []string{child.ID, take.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != child.ID || rows[1].ID != take.ID {
		t.Fatalf("rows = %+v", rows)
	}
	tr, er := rows[1], rows[0]
	if tr.Source != "take" || tr.Engine != "yue2" || string(tr.Score) != aligned || tr.ScoreSHA256 != lt256(aligned) || !tr.Timed {
		t.Errorf("take plan = %+v", tr)
	}
	if tr.BaseTrackID == nil || take.Base.TrackID == nil || *tr.BaseTrackID != *take.Base.TrackID {
		t.Errorf("base = %v", tr.BaseTrackID)
	}
	if tr.ExpectedAudioS != take.ExpectedAudioS || tr.Cot != take.Cot || tr.Seed != take.Seed {
		t.Errorf("take plan timing/cot/seed = %d %s %d, plan says %d %s %d", tr.ExpectedAudioS, tr.Cot, tr.Seed, take.ExpectedAudioS, take.Cot, take.Seed)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(tr.Request, &req); err != nil {
		t.Fatal(err)
	}
	if _, has := req["abc"]; has {
		t.Error("the stored request of a plan carries no abc")
	}
	if er.Source != "edit" || string(er.Score) == aligned || !strings.Contains(string(er.Score), "60") {
		t.Errorf("edit plan = %+v", er)
	}
	// a plan whose score has no tempo is untimed
	e.exec(`DROP TRIGGER desk_plans_immutable`)
	e.exec(`UPDATE desk_plans SET seconds_per_bar = NULL, q = NULL WHERE id = ?`, take.ID)
	rows, err = e.svc.PlansForEstimate(ctx, e.st.R, []string{take.ID})
	if err != nil || rows[0].Timed {
		t.Errorf("untimed: %+v %v", rows, err)
	}
}

// TestPlansForEstimate_Refusals: an unknown id is 404; a score file that is missing or no longer the recorded one is 409
// PLAN_NOT_READY naming the plan (a corrupted score is never rendered); an unwired service is 503.
func TestPlansForEstimate_Refusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	pl := e.plan()
	wantProblem(t, func() error { _, err := e.svc.PlansForEstimate(ctx, e.st.R, []string{pl.ID, "nope"}); return err }(), 404, "NOT_FOUND")

	child, _, err := e.svc.Edit(ctx, alice, pl.ID, desk.EditIn{Ops: []abc.Op{{Op: abc.OpSetTempo, BPM: 60}}})
	if err != nil {
		t.Fatal(err)
	}
	path := e.scalar(`SELECT score_path FROM desk_plans WHERE id = ?`, child.ID)
	if err := e.root.Remove(path); err != nil {
		t.Fatal(err)
	}
	pe := wantProblem(t, func() error { _, err := e.svc.PlansForEstimate(ctx, e.st.R, []string{pl.ID, child.ID}); return err }(), 409, "PLAN_NOT_READY")
	if ids, _ := pe.Details["plan_ids"].([]string); len(ids) != 1 || ids[0] != child.ID {
		t.Errorf("details = %v", pe.Details)
	}
	e.writeScore(path, "X:1\nK:C\nCDEF|\n") // a file that is not the recorded score
	wantProblem(t, func() error { _, err := e.svc.PlansForEstimate(ctx, e.st.R, []string{child.ID}); return err }(), 409, "PLAN_NOT_READY")

	wantProblem(t, func() error {
		_, err := desk.New(desk.Options{}).PlansForEstimate(ctx, e.st.R, []string{pl.ID})
		return err
	}(), 503, "UNAVAILABLE")
}
