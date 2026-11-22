package library_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// TestInsertMutation_RenderPlanOperators: render_plan is an Operator Ledger operator that may be a root (a plan made for a draft or an
// imported score has no base take) and may name a parent (the plan's base take); abc_edit is an operator too but always needs a parent.
func TestInsertMutation_RenderPlanOperators(t *testing.T) {
	t.Parallel()
	if library.OperatorRenderPlan != lineage.OpRenderPlan || library.OperatorABCEdit != lineage.OpABCEdit {
		t.Fatalf("library %q/%q, lineage %q/%q", library.OperatorRenderPlan, library.OperatorABCEdit, lineage.OpRenderPlan, lineage.OpABCEdit)
	}
	for _, c := range []struct {
		name, op, parent string
		wantErr          string
	}{
		{"render_plan root", "render_plan", "", ""},
		{"render_plan with parent", "render_plan", lt.TrackLuna, ""},
		{"abc_edit with parent", "abc_edit", lt.TrackLuna, ""},
		{"abc_edit rootless", "abc_edit", "", "needs a parent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnvWith(t, lt.FixtureM2c)
			ctx := context.Background()
			seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "d"})
			tx, err := e.st.W.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, "desk-song-001", 5), "agent:tok1")
			if err != nil {
				t.Fatal(err)
			}
			nt := jobTake("01900000-0000-7000-8000-00000000d001", rid, c.parent, seed.JobID, seed.EnvID)
			if c.parent == "" {
				nt.RootTrackID, nt.Generation = "", 0
			}
			nt.Mutation = &library.NewMutation{Operator: c.op, Params: map[string]any{"mode": "render_plan", "plan_id": "p1"}, JobID: seed.JobID}
			err = e.svc.InsertTake(ctx, tx, nt)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("InsertTake: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("InsertTake err = %v, want %q", err, c.wantErr)
			}
		})
	}
}
