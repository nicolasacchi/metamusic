package generation_test

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

var updateDesk = flag.Bool("update", false, "rewrite the render_plan golden")

// A synthetic, timed score (4 bars at 2 s each = 8 s) and its untimed twin.
const (
	deskScore    = "X:1\nT:desk\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nCDEF|GABc|cBAG|FEDC|]\n"
	deskUntimed  = "X:1\nT:desk\nM:4/4\nL:1/8\nK:C\nCDEF|GABc|]\n"
	deskPlanSeed = 7001
)

// fakePlans is a generation.PlanSource over fixed rows; calls counts the reads.
type fakePlans struct {
	rows  map[string]generation.PlanRow
	err   error
	calls atomic.Int32
}

func (f *fakePlans) PlansForEstimate(_ context.Context, _ lineage.Querier, ids []string) ([]generation.PlanRow, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	var out []generation.PlanRow
	for _, id := range ids {
		r, ok := f.rows[id]
		if !ok {
			return nil, problem.E(http.StatusNotFound, "NOT_FOUND", "plan not found", nil)
		}
		out = append(out, r)
	}
	return out, nil
}

// deskRig is a rig whose service reads plans from a fakePlans.
type deskRig struct {
	*rig
	plans *fakePlans
}

// planOf builds the plan row of a take: its canonical request without abc, the take as base.
func (r *rig) planOf(id, base string, mod func(*generation.PlanRow)) generation.PlanRow {
	r.t.Helper()
	req := r.str(`SELECT r.request_json FROM recipes r JOIN tracks t ON t.recipe_id = r.id WHERE t.id = ?`, base)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req), &m); err != nil {
		r.t.Fatal(err)
	}
	delete(m, "abc")
	raw, _ := json.Marshal(m)
	b := base
	row := generation.PlanRow{ID: id, Engine: "yue2", Cot: "full", Source: "take", Title: "Luna Park", Seed: deskPlanSeed, Request: raw,
		Score: []byte(deskScore), ScoreSHA256: strings.Repeat("a", 64), BaseTrackID: &b, Timed: true, ExpectedAudioS: 8}
	if mod != nil {
		mod(&row)
	}
	return row
}

// lunaRig is a rig with one plan, plan-a, made for the Luna take; mod edits the row.
func lunaRig(t *testing.T, mod func(*generation.PlanRow)) *deskRig {
	t.Helper()
	r := newRig(t)
	d := &deskRig{rig: r, plans: &fakePlans{rows: map[string]generation.PlanRow{}}}
	row := r.planOf("plan-a", lt.TrackLuna, mod)
	d.plans.rows[row.ID] = row
	d.opts.PlanSource = d.plans
	d.svc = generation.New(d.opts)
	return d
}

func renderPlanItem(n int, ids ...string) generation.Item {
	return generation.Item{Mode: lineage.ModeRenderPlan, NSeeds: n, Desk: &generation.DeskItem{PlanIDs: ids}}
}

func (r *rig) wantProblem(err error, status int, code string) *problem.Error {
	r.t.Helper()
	pe, ok := asProblem(err)
	if !ok || pe.Status != status || pe.Code != code {
		r.t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

// TestEstimate_RenderPlan_Golden pins the planned takes and the quote of a render_plan estimate (testdata/estimate-render-plan.json).
func TestEstimate_RenderPlan_Golden(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	e := d.est(human, renderPlanItem(2, "plan-a"))
	type take struct {
		Seed                     int64
		RequestID, Title         string
		Operator, Parent         string
		ExpectedAudioS, TimeoutS int
		ExpectedS                int
		ExpectedMicro, MaxMicro  int64
		Request                  map[string]any
	}
	var got struct {
		Takes                  []take
		Expected, Max, Ceiling int64
		Count                  int
		RequiresApproval       bool
	}
	items := d.itemsJSON(e)
	for k, tp := range e.Items[0].Takes {
		var req map[string]any
		if err := json.Unmarshal([]byte(items[k]["request_json"].(string)), &req); err != nil {
			t.Fatal(err)
		}
		parent := ""
		if tp.ParentTrackID != nil {
			parent = *tp.ParentTrackID
		}
		got.Takes = append(got.Takes, take{Seed: tp.Seed, RequestID: tp.RequestID, Title: tp.Title, Operator: tp.Operator, Parent: parent,
			ExpectedAudioS: tp.ExpectedAudioS, TimeoutS: tp.TimeoutS, Request: req,
			ExpectedMicro: int64(items[k]["expected_micro_usd"].(float64)), MaxMicro: int64(items[k]["max_micro_usd"].(float64)), ExpectedS: int(items[k]["expected_s"].(float64))})
	}
	got.Expected, got.Max, got.Ceiling, got.Count = int64(e.Cost.Expected), int64(e.Cost.Max), int64(e.Cost.Ceiling), e.Takes
	got.RequiresApproval = e.RequiresApproval
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/estimate-render-plan.json"
	if *updateDesk {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // our own testdata
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(b)); diff != "" {
		t.Errorf("render_plan estimate differs from the golden (-want +got):\n%s", diff)
	}
}

// TestEstimate_RenderPlan_SeedsAndParent: the plan's own seed first, then n-1 new ones; the base take is the parent; a plan with no
// base makes root takes; every take carries the operator and the plan id; the recipe has the score as abc and the plan's cot.
func TestEstimate_RenderPlan_SeedsAndParent(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	e := d.est(human, renderPlanItem(3, "plan-a"))
	takes := e.Items[0].Takes
	if len(takes) != 3 || takes[0].Seed != deskPlanSeed || takes[1].Seed == deskPlanSeed || takes[1].Seed == takes[2].Seed {
		t.Fatalf("seeds = %+v", takes)
	}
	items := d.itemsJSON(e)
	for k, tp := range takes {
		if tp.Operator != lineage.OpRenderPlan || tp.ParentTrackID == nil || *tp.ParentTrackID != lt.TrackLuna {
			t.Errorf("take %d: operator %q parent %v", k, tp.Operator, tp.ParentTrackID)
		}
		if !strings.HasSuffix(tp.Title, " · plan s"+strconv.FormatInt(tp.Seed, 10)) || !strings.HasPrefix(tp.Title, "Luna Park") {
			t.Errorf("title = %q", tp.Title)
		}
		params := items[k]["params"].(map[string]any)
		if params["plan_id"] != "plan-a" || params["mode"] != "render_plan" {
			t.Errorf("params = %v", params)
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(items[k]["request_json"].(string)), &req); err != nil {
			t.Fatal(err)
		}
		if req["abc"] != deskScore || req["cot"] != "full" {
			t.Errorf("request abc/cot = %q / %v", req["abc"], req["cot"])
		}
		if _, has := items[k]["kind"]; has {
			t.Errorf("a render_plan take carries a kind: it is a plain generate job")
		}
		if _, has := items[k]["needs"]; has {
			t.Errorf("a render_plan take carries needs: it is a plain /1 render")
		}
	}
	// explicit seeds: exactly those
	e2, err := d.svc.Estimate(context.Background(), human, generation.EstimateIn{Via: spend.ViaHumanCreate,
		Items: []generation.Item{{Mode: lineage.ModeRenderPlan, Seeds: []int64{11, 12}, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := []int64{e2.Items[0].Takes[0].Seed, e2.Items[0].Takes[1].Seed}; !slices.Equal(got, []int64{11, 12}) {
		t.Errorf("explicit seeds = %v", got)
	}
	// a plan without a base take: root takes, titled from the plan
	d.plans.rows["plan-root"] = d.planOf("plan-root", lt.TrackLuna, func(r *generation.PlanRow) { r.BaseTrackID, r.Title = nil, "Draft score" })
	e3 := d.est(human, renderPlanItem(1, "plan-root"))
	if tp := e3.Items[0].Takes[0]; tp.ParentTrackID != nil || !strings.HasPrefix(tp.Title, "Draft score · plan s") {
		t.Errorf("rootless take = %+v", tp)
	}
	// two plans in one item: one set of takes per plan
	d.plans.rows["plan-b"] = d.planOf("plan-b", lt.TrackLuna, func(r *generation.PlanRow) { r.Seed = 8002 })
	e4 := d.est(human, renderPlanItem(2, "plan-a", "plan-b"))
	if e4.Takes != 4 {
		t.Errorf("two plans x two seeds = %d takes", e4.Takes)
	}
}

// TestEstimate_RenderPlan_PlanNotReady: the source's 409 and 404 reach the caller unchanged and nothing is stored.
func TestEstimate_RenderPlan_PlanNotReady(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	before := d.count(`SELECT COUNT(*) FROM estimates`)
	d.plans.err = problem.E(http.StatusConflict, "PLAN_NOT_READY", "a plan file is missing", map[string]any{"plan_ids": []string{"plan-a"}})
	_, err := d.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{renderPlanItem(1, "plan-a")}, Via: spend.ViaHumanCreate})
	d.wantProblem(err, http.StatusConflict, "PLAN_NOT_READY")
	d.plans.err = nil
	_, err = d.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{renderPlanItem(1, "no-such-plan")}, Via: spend.ViaHumanCreate})
	d.wantProblem(err, http.StatusNotFound, "NOT_FOUND")
	if n := d.count(`SELECT COUNT(*) FROM estimates`); n != before {
		t.Errorf("a refused estimate left %d rows", n-before)
	}
}

// TestEstimate_RenderPlan_Warnings: truncated, untimed and edited plans warn; a clean plan does not.
func TestEstimate_RenderPlan_Warnings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		mod  func(*generation.PlanRow)
		want []string
	}{
		{"clean", nil, nil},
		{"truncated", func(r *generation.PlanRow) { r.Truncated = true }, []string{generation.WarnPlanTruncated}},
		{"untimed", func(r *generation.PlanRow) { r.Timed, r.Score = false, []byte(deskUntimed) }, []string{generation.WarnPlanUntimed}},
		{"edited", func(r *generation.PlanRow) { r.Source = "edit" }, []string{generation.WarnPlanEdited}},
		{"all", func(r *generation.PlanRow) { r.Truncated, r.Timed, r.Source = true, false, "edit" },
			[]string{generation.WarnPlanTruncated, generation.WarnPlanUntimed, generation.WarnPlanEdited}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := lunaRig(t, c.mod)
			e := d.est(human, renderPlanItem(1, "plan-a"))
			var got []string
			for _, w := range e.Warnings {
				if strings.HasPrefix(w.Code, "plan_") {
					got = append(got, w.Code)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("plan warnings = %v, want %v", got, c.want)
			}
		})
	}
}

// TestQuote_RenderPlanUsesScoreLength: the take is quoted at the score's length (never below the plan's), with the planning phase
// charged while DeskRenderSkipsPlan is false, so it costs the same as a cot=full render of that length.
func TestQuote_RenderPlanUsesScoreLength(t *testing.T) {
	t.Parallel()
	short := lunaRig(t, nil)
	long := lunaRig(t, func(r *generation.PlanRow) { r.ExpectedAudioS = 200 })
	es := short.est(human, renderPlanItem(1, "plan-a"))
	el := long.est(human, renderPlanItem(1, "plan-a"))
	if a, b := es.Items[0].Takes[0].ExpectedAudioS, el.Items[0].Takes[0].ExpectedAudioS; a >= b || b != 200 {
		t.Errorf("audio seconds: short plan %d, long plan %d (want the long plan at 200)", a, b)
	}
	if es.Cost.Expected >= el.Cost.Expected {
		t.Errorf("a longer score must cost more: %d vs %d", es.Cost.Expected, el.Cost.Expected)
	}
	// the planning phase is charged to a cot=full plan (conservative) and not to a cot=melody one of the same length
	melody := lunaRig(t, func(r *generation.PlanRow) { r.Cot = "melody"; r.ExpectedAudioS = 200 })
	if em := melody.est(human, renderPlanItem(1, "plan-a")); em.Cost.Expected >= el.Cost.Expected {
		t.Errorf("cot=melody %d must cost less than cot=full %d at the same length", em.Cost.Expected, el.Cost.Expected)
	}
	if generation.DeskRenderSkipsPlan {
		t.Fatal("DeskRenderSkipsPlan flips only after the plan-render canary measured the phase")
	}
}

// TestEstimate_DeskUnavailableWithoutSource: no PlanSource is 503 UNAVAILABLE {component: desk}; a plain estimate never reads it.
func TestEstimate_DeskUnavailableWithoutSource(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{renderPlanItem(1, "plan-a")}, Via: spend.ViaHumanCreate})
	pe := r.wantProblem(err, http.StatusServiceUnavailable, "UNAVAILABLE")
	if pe.Details["component"] != "desk" {
		t.Errorf("details = %v", pe.Details)
	}
	// a source whose reads would fail loudly is never touched by a plain estimate
	d := lunaRig(t, nil)
	d.plans.err = problem.E(http.StatusInternalServerError, "BOOM", "must not be read", nil)
	d.est(human, newSeedItem(lt.TrackLuna, 2))
	if n := d.plans.calls.Load(); n != 0 {
		t.Errorf("a plain estimate read the plan source %d times", n)
	}
}

// TestEstimate_DeskFieldsForbiddenElsewhere: a desk object on any other mode, and every field a render_plan item may not carry.
func TestEstimate_DeskFieldsForbiddenElsewhere(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	luna := lt.TrackLuna
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "plan-" + strconv.Itoa(i)
		}
		return out
	}
	for _, c := range []struct {
		name string
		it   generation.Item
		rule string
	}{
		{"desk on new_seed", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "field_forbidden"},
		{"desk on analyze", generation.Item{Mode: lineage.ModeAnalyze, TrackIDs: []string{luna}, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "field_forbidden"},
		{"no desk", generation.Item{Mode: lineage.ModeRenderPlan}, "required"},
		{"no plans", generation.Item{Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{}}, "plan_ids_required"},
		{"eleven plans", generation.Item{Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{PlanIDs: ids(11)}}, "plan_ids_at_most_10"},
		{"duplicate plan", generation.Item{Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a", "plan-a"}}}, "duplicate"},
		{"five seeds", generation.Item{Mode: lineage.ModeRenderPlan, NSeeds: 5, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "render_plan_at_most_4"},
		{"five explicit seeds", generation.Item{Mode: lineage.ModeRenderPlan, Seeds: []int64{1, 2, 3, 4, 5}, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "render_plan_at_most_4"},
		{"desk n_seeds is plan_seeds'", generation.Item{Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}, NSeeds: 2}}, "field_forbidden"},
		{"desk cot is plan_seeds'", generation.Item{Mode: lineage.ModeRenderPlan, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}, Cot: "full"}}, "field_forbidden"},
		{"a mother", generation.Item{Mode: lineage.ModeRenderPlan, ParentTrackID: &luna, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "render_plan_fields"},
		{"a recipe", generation.Item{Mode: lineage.ModeRenderPlan, Recipe: []byte(`{}`), Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "render_plan_fields"},
		{"a patch", generation.Item{Mode: lineage.ModeRenderPlan, Patch: &lineage.Patch{Style: sp("x")}, Desk: &generation.DeskItem{PlanIDs: []string{"plan-a"}}}, "patch_forbidden"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := d.estimateErr(generation.EstimateIn{Items: []generation.Item{c.it}, Via: spend.ViaHumanCreate})
			pe := d.wantProblem(err, http.StatusUnprocessableEntity, "INVALID_ITEM")
			if pe.Details["rule"] != c.rule {
				t.Errorf("rule = %v, want %s", pe.Details["rule"], c.rule)
			}
		})
	}
	// acestep15 has no render-from-score mode
	_, err := d.svc.Estimate(context.Background(), human, generation.EstimateIn{Engine: "acestep15", Items: []generation.Item{renderPlanItem(1, "plan-a")}, Via: spend.ViaHumanCreate})
	if pe, ok := asProblem(err); !ok || pe.Status != http.StatusUnprocessableEntity {
		t.Errorf("acestep15 render_plan: %v", err)
	}
}

// TestCreate_RenderPlanLedger: create writes a generate job with operator render_plan, params.plan_id and a recipe whose request
// carries the score; the take is a child of the plan's base take (a root when the plan has none); a plain /1 job (no needs).
func TestCreate_RenderPlanLedger(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	d.plans.rows["plan-root"] = d.planOf("plan-root", lt.TrackLuna, func(r *generation.PlanRow) { r.BaseTrackID, r.Seed = nil, 9100 })
	e := d.est(human, renderPlanItem(1, "plan-a"), renderPlanItem(1, "plan-root"))
	c := d.mustCreate(human, e, spend.ViaHumanCreate)
	if c.ApprovalState != "approved" || c.Takes != 2 {
		t.Fatalf("created = %+v", c)
	}
	if n := d.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND operator = 'render_plan' AND kind = 'generate' AND needs_json = '[]' AND spec_json = '{}'`, c.BatchID); n != 2 {
		t.Fatalf("%d of 2 jobs are plain render_plan jobs", n)
	}
	if got := d.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND json_extract(mutation_json, '$.params.plan_id') = 'plan-a' AND source_track_id = ?`, c.BatchID, lt.TrackLuna); got != 1 {
		t.Errorf("plan-a job with the base take as parent: %d", got)
	}
	if got := d.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND json_extract(mutation_json, '$.params.plan_id') = 'plan-root' AND source_track_id IS NULL`, c.BatchID); got != 1 {
		t.Errorf("a plan without a base must make a root job: %d", got)
	}
	if n := d.count(`SELECT COUNT(*) FROM jobs j JOIN recipes r ON r.id = j.recipe_id WHERE j.batch_id = ? AND json_extract(r.request_json, '$.abc') = ?`, c.BatchID, deskScore); n != 2 {
		t.Errorf("%d of 2 recipes carry the score as abc", n)
	}
	// recipes are new rows: the base take's own recipe is untouched
	if n := d.count(`SELECT COUNT(*) FROM recipes WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?) AND json_extract(request_json, '$.abc') IS NULL`, lt.TrackLuna); n != 1 {
		t.Error("the base take's recipe changed")
	}
}

// TestCreate_RenderPlanAwaitsOwner: a Desk render is never auto-approved. An Editor waits (editor_batch), an agent inside its
// auto-approve limits waits too (desk_render), the Owner's own click is the approval. A plain batch of the agent is still auto.
func TestCreate_RenderPlanAwaitsOwner(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.AutoApproveTakes, caps.AutoApproveMicro = 50, 1_000_000_000
	d := lunaRig(t, nil)
	r2 := newRig(t, withCaps(caps))
	d.rig = r2
	d.opts = r2.opts
	d.opts.PlanSource = d.plans
	d.svc = generation.New(d.opts)
	row := r2.planOf("plan-a", lt.TrackLuna, nil)
	d.plans.rows[row.ID] = row

	ea := d.est(agent, renderPlanItem(1, "plan-a"))
	if !ea.RequiresApproval || !slices.Contains(ea.ApprovalReasons, generation.ReasonDeskRender) {
		t.Fatalf("agent estimate: requires %v reasons %v", ea.RequiresApproval, ea.ApprovalReasons)
	}
	ca := d.mustCreate(agent, ea, spend.ViaAPI)
	if ca.ApprovalState != "pending" {
		t.Errorf("agent Desk render approval = %s, want pending", ca.ApprovalState)
	}
	eb := d.est(bobEditor, renderPlanItem(1, "plan-a"))
	if cb := d.mustCreate(bobEditor, eb, spend.ViaAPI); cb.ApprovalState != "pending" || !slices.Contains(eb.ApprovalReasons, spend.ReasonEditorBatch) {
		t.Errorf("editor: %s %v", cb.ApprovalState, eb.ApprovalReasons)
	}
	eo := d.est(human, renderPlanItem(1, "plan-a"))
	if eo.RequiresApproval {
		t.Errorf("the Owner's own estimate on the human path needs no second approval: %v", eo.ApprovalReasons)
	}
	if co := d.mustCreate(human, eo, spend.ViaHumanCreate); co.ApprovalState != "approved" {
		t.Errorf("owner approval = %s", co.ApprovalState)
	}
	// the same agent's plain batch still rides its auto-approve limits
	ep := d.est(agent, newSeedItem(lt.TrackLuna, 1))
	if ep.RequiresApproval {
		t.Errorf("a plain agent batch within limits needs approval: %v", ep.ApprovalReasons)
	}
	// the Owner on a non-human path (an API-key style create) waits like everyone else
	eh := d.est(human, renderPlanItem(1, "plan-a"))
	if ch, err := d.create(human, eh, spend.ViaAPI); err != nil || ch.ApprovalState != "pending" {
		t.Errorf("owner via api: %+v %v", ch, err)
	}
}

// TestEstimate_RenderPlanBaseGates: the base take of a plan is a mother like any other: an unknown one is refused.
func TestEstimate_RenderPlanBaseGates(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, func(r *generation.PlanRow) { b := "01900000-0000-7000-8000-00000000ffff"; r.BaseTrackID = &b })
	err := d.estimateErr(generation.EstimateIn{Items: []generation.Item{renderPlanItem(1, "plan-a")}, Via: spend.ViaHumanCreate})
	d.wantProblem(err, http.StatusUnprocessableEntity, "UNKNOWN_TRACK")
}

// TestEstimate_RenderPlan_NeedsNoCapability: a render_plan batch is a plain /1 render (D-DESK-2), so the capability gate is never
// read: with every worker capability closed (production today) it estimates and creates, and its jobs carry no needs.
func TestEstimate_RenderPlan_NeedsNoCapability(t *testing.T) {
	t.Parallel()
	d := lunaRig(t, nil)
	d.setCaps() // closed
	e := d.est(human, renderPlanItem(1, "plan-a"))
	c := d.mustCreate(human, e, spend.ViaHumanCreate)
	if n := d.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND needs_json = '[]'`, c.BatchID); n != 1 {
		t.Errorf("%d jobs without needs", n)
	}
}
