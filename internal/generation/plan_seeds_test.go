package generation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// planRig is a rig whose fake backend has the plan capability open (production today has every capability closed).
func planRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.setCaps("plan", "v2")
	return r
}

func planSeedsItem(parent string, n int) generation.Item {
	return generation.Item{Mode: lineage.ModePlanSeeds, ParentTrackID: &parent, Desk: &generation.DeskItem{NSeeds: n}}
}

// composeLines is a compose recipe with the given number of lyric lines (a longer song is a longer expected length).
func composeLines(id string, lines int, extra string) json.RawMessage {
	var b strings.Builder
	b.WriteString("[Verse 1]\\n")
	for k := 0; k < lines; k++ {
		fmt.Fprintf(&b, "line number %d of the song\\n", k)
	}
	return json.RawMessage(`{"id":"` + id + `","style":"indie pop, 100 bpm, female vocal","lyrics":"` + b.String() + `[Chorus]\nsing it loud","cot":"full","seed":4000` + extra + `}`)
}

func composePlanItem(rec json.RawMessage, n int) generation.Item {
	return generation.Item{Mode: lineage.ModePlanSeeds, Recipe: rec, Desk: &generation.DeskItem{NSeeds: n}}
}

func (r *rig) planSeedsOf(e generation.Estimate) [][]int64 {
	r.t.Helper()
	var out [][]int64
	for _, it := range r.itemsJSON(e) {
		var spec struct {
			Plan struct {
				Seeds []int64 `json:"seeds"`
			} `json:"plan"`
		}
		b, _ := json.Marshal(it["spec"])
		if err := json.Unmarshal(b, &spec); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, spec.Plan.Seeds)
	}
	return out
}

// TestEstimate_PlanSeeds_Golden pins the planned jobs and the quote of a plan_seeds estimate (testdata/estimate-plan-seeds.json).
func TestEstimate_PlanSeeds_Golden(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	e := r.est(human, planSeedsItem(lt.TrackLuna, 4))
	type job struct {
		Kind, Operator, Title, Parent, RequestID string
		Needs                                    []string
		Spec                                     json.RawMessage
		Seed                                     int64
		ExpectedAudioS, ExpectedS, TimeoutS      int
		ExpectedMicro, MaxMicro                  int64
		Reuse                                    string
	}
	var got struct {
		Jobs                   []job
		Expected, Max, Ceiling int64
		Count                  int
		RequiresApproval       bool
	}
	items := r.itemsJSON(e)
	for k, tp := range e.Items[0].Takes {
		var needs []string
		nb, _ := json.Marshal(items[k]["needs"])
		_ = json.Unmarshal(nb, &needs)
		spec, _ := json.Marshal(items[k]["spec"])
		reuse, _ := items[k]["reuse_recipe_id"].(string)
		parent := ""
		if tp.ParentTrackID != nil {
			parent = *tp.ParentTrackID
		}
		got.Jobs = append(got.Jobs, job{Kind: items[k]["kind"].(string), Operator: tp.Operator, Title: tp.Title, Parent: parent, RequestID: tp.RequestID,
			Needs: needs, Spec: spec, Seed: tp.Seed, ExpectedAudioS: tp.ExpectedAudioS, ExpectedS: int(items[k]["expected_s"].(float64)), TimeoutS: tp.TimeoutS,
			ExpectedMicro: int64(items[k]["expected_micro_usd"].(float64)), MaxMicro: int64(items[k]["max_micro_usd"].(float64)), Reuse: reuse})
	}
	got.Expected, got.Max, got.Ceiling, got.Count, got.RequiresApproval = int64(e.Cost.Expected), int64(e.Cost.Max), int64(e.Cost.Ceiling), e.Takes, e.RequiresApproval
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/estimate-plan-seeds.json"
	if *updateDesk {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // our own testdata
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(b)); diff != "" {
		t.Errorf("plan_seeds estimate differs from the golden (-want +got):\n%s", diff)
	}
	if got.Jobs[0].Kind != "plan" || got.Jobs[0].Operator != lineage.OpPlanSeeds || !slices.Equal(got.Jobs[0].Needs, []string{"plan", "v2"}) {
		t.Errorf("job = %+v", got.Jobs[0])
	}
}

// TestEstimate_PlanSeeds_Seeds: default 8 new seeds above the base's, explicit seeds as given, and a repeat plans new seeds.
func TestEstimate_PlanSeeds_Seeds(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	def := generation.Item{Mode: lineage.ModePlanSeeds, ParentTrackID: sp(lt.TrackLuna)}
	e := r.est(human, def)
	seeds := r.planSeedsOf(e)
	if len(seeds) != 1 || len(seeds[0]) != 8 || seeds[0][0] <= 101 {
		t.Fatalf("default seeds = %v", seeds)
	}
	explicit := generation.Item{Mode: lineage.ModePlanSeeds, ParentTrackID: sp(lt.TrackLuna), Desk: &generation.DeskItem{Seeds: []int64{5, 6, 7}}}
	if got := r.planSeedsOf(r.est(human, explicit)); !slices.Equal(got[0], []int64{5, 6, 7}) {
		t.Errorf("explicit seeds = %v", got)
	}
	// after a create, the same estimate plans the next seeds
	r.mustCreate(human, e, spend.ViaHumanCreate)
	again := r.planSeedsOf(r.est(human, def))
	for _, s := range again[0] {
		if slices.Contains(seeds[0], s) {
			t.Fatalf("seed %d planned twice: %v then %v", s, seeds[0], again[0])
		}
	}
}

// TestEstimate_PlanSeeds_SplitsByRuntime: sixteen seeds of a long song do not fit one job's runtime, so they are planned in two
// near-equal jobs under one recipe, each within the runtime cap.
func TestEstimate_PlanSeeds_SplitsByRuntime(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	e := r.est(human, composePlanItem(composeLines("long-song", 30, ""), 16))
	takes := e.Items[0].Takes
	if len(takes) != 2 || takes[0].ExpectedAudioS < 150 {
		t.Fatalf("takes = %+v", takes)
	}
	seeds := r.planSeedsOf(e)
	if len(seeds[0]) != 8 || len(seeds[1]) != 8 {
		t.Errorf("job sizes = %d, %d", len(seeds[0]), len(seeds[1]))
	}
	for _, tp := range takes {
		if tp.TimeoutS > 900 {
			t.Errorf("timeout %d above the runtime cap", tp.TimeoutS)
		}
		if !strings.HasSuffix(tp.Title, " · plans ×8") {
			t.Errorf("title = %q", tp.Title)
		}
	}
	var splits int
	for _, w := range e.Warnings {
		if w.Code == generation.WarnPlanSplit {
			splits++
		}
	}
	if splits != 2 {
		t.Errorf("plan_split warnings = %d", splits)
	}
	// create shares the one new recipe between the two jobs
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if n := r.count(`SELECT COUNT(DISTINCT recipe_id) FROM jobs WHERE batch_id = ?`, c.BatchID); n != 1 {
		t.Errorf("split jobs use %d recipes", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ?`, c.BatchID); n != 2 {
		t.Errorf("jobs = %d", n)
	}
}

// TestEstimate_PlanSeeds_RefusesCotOffAndABC: a plan needs cot full or melody and no abc.
func TestEstimate_PlanSeeds_RefusesCotOffAndABC(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	ctx := context.Background()
	est := func(it generation.Item) error {
		_, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{it}, Via: spend.ViaHumanCreate})
		return err
	}
	wantRule := func(err error, field, rule string) {
		t.Helper()
		pe := r.wantProblem(err, http.StatusUnprocessableEntity, "INVALID_ITEM")
		if pe.Details["rule"] != rule || pe.Details["field"] != field {
			t.Errorf("details = %v, want %s %s", pe.Details, field, rule)
		}
	}
	off := "off"
	patchOff := planSeedsItem(lt.TrackLuna, 2)
	patchOff.Patch = &lineage.Patch{Cot: &off}
	wantRule(est(patchOff), "desk.cot", "plan_requires_cot")
	cotOff := composePlanItem(json.RawMessage(strings.Replace(string(composeLines("c-off", 2, "")), `"cot":"full"`, `"cot":"off"`, 1)), 2)
	wantRule(est(cotOff), "desk.cot", "plan_requires_cot")
	// desk.cot rescues a base whose cot is off
	cotOff.Desk.Cot = "melody"
	if err := est(cotOff); err != nil {
		t.Errorf("desk.cot melody on a cot=off base: %v", err)
	}
	// abc: a base that carries one is refused unless the patch removes it
	withABC := composeLines("with-abc", 2, `,"abc":"X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nCDEF|GABc|]\n"`)
	wantRule(est(composePlanItem(withABC, 2)), "patch.abc", "plan_requires_no_abc")
	cleared := composePlanItem(withABC, 2)
	cleared.Patch = &lineage.Patch{ABC: lineage.OptString{Set: true, Null: true}}
	if err := est(cleared); err != nil {
		t.Errorf("abc cleared by the patch: %v", err)
	}
	setABC := planSeedsItem(lt.TrackLuna, 2)
	setABC.Patch = &lineage.Patch{ABC: lineage.OptString{Set: true, Value: "X:1\nK:C\nC|]\n"}}
	wantRule(est(setABC), "patch.abc", "plan_requires_no_abc")
}

// TestEstimate_PlanSeeds_Shape: the static shape rules of the mode.
func TestEstimate_PlanSeeds_Shape(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	ctx := context.Background()
	luna := lt.TrackLuna
	title := "x"
	base := func() generation.Item { return planSeedsItem(luna, 2) }
	cases := []struct {
		name        string
		mod         func(*generation.Item)
		field, rule string
	}{
		{"no base", func(it *generation.Item) { it.ParentTrackID = nil }, "mode", "plan_seeds_one_base"},
		{"two bases", func(it *generation.Item) { it.DraftIDs = []string{lt.Draft1} }, "mode", "plan_seeds_one_base"},
		{"two drafts", func(it *generation.Item) { it.ParentTrackID, it.DraftIDs = nil, []string{lt.Draft1, lt.Draft2} }, "draft_ids", "plan_seeds_one_draft"},
		{"plan ids", func(it *generation.Item) { it.Desk.PlanIDs = []string{"p"} }, "desk.plan_ids", "field_forbidden"},
		{"item seeds", func(it *generation.Item) { it.NSeeds = 2 }, "mode", "plan_seeds_fields"},
		{"title", func(it *generation.Item) { it.Title = &title }, "mode", "plan_seeds_fields"},
		{"template", func(it *generation.Item) { it.TemplateMode = lineage.ModeNewSeed }, "mode", "plan_seeds_fields"},
		{"n too big", func(it *generation.Item) { it.Desk.NSeeds = 17 }, "desk.n_seeds", "range_1_to_16"},
		{"both", func(it *generation.Item) { it.Desk.Seeds = []int64{1} }, "desk.seeds", "n_seeds_or_seeds"},
		{"seed dup", func(it *generation.Item) { it.Desk.NSeeds, it.Desk.Seeds = 0, []int64{1, 1} }, "desk.seeds", "duplicate"},
		{"seed range", func(it *generation.Item) { it.Desk.NSeeds, it.Desk.Seeds = 0, []int64{-1} }, "desk.seeds", "out_of_range"},
		{"too many seeds", func(it *generation.Item) {
			it.Desk.NSeeds = 0
			for k := int64(0); k < 17; k++ {
				it.Desk.Seeds = append(it.Desk.Seeds, k)
			}
		}, "desk.seeds", "at_most_16"},
		{"cot", func(it *generation.Item) { it.Desk.Cot = "think" }, "desk.cot", "cot_invalid"},
	}
	for _, c := range cases {
		it := base()
		c.mod(&it)
		_, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{it}, Via: spend.ViaHumanCreate})
		pe := r.wantProblem(err, http.StatusUnprocessableEntity, "INVALID_ITEM")
		if pe.Details["rule"] != c.rule || pe.Details["field"] != c.field {
			t.Errorf("%s: details = %v, want %s %s", c.name, pe.Details, c.field, c.rule)
		}
	}
	// a Desk object on render_plan may not carry plan_seeds' fields, and the other way round
	it := base()
	it.Desk = nil
	if err := func() error {
		_, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{it}, Via: spend.ViaHumanCreate})
		return err
	}(); err != nil {
		t.Errorf("a plan_seeds item without a desk object: %v", err)
	}
	// a patch field the row forbids stays refused
	cfg := base()
	cfg.Patch = &lineage.Patch{CFGScale: lineage.OptNumber{Set: true, Value: 3}}
	_, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{cfg}, Via: spend.ViaHumanCreate})
	r.wantProblem(err, http.StatusUnprocessableEntity, "INVALID_ITEM")
}

// TestEstimate_PlanSeeds_ClosedCapRefuses: with the plan capability closed (production today) estimate and create refuse before
// anything is stored or reserved; a plain estimate and a render_plan are unaffected.
func TestEstimate_PlanSeeds_ClosedCapRefuses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps() // every capability closed
	ctx := context.Background()
	estimates, ledger, jobs := r.count(`SELECT COUNT(*) FROM estimates`), r.count(`SELECT COUNT(*) FROM ledger`), r.count(`SELECT COUNT(*) FROM jobs`)
	_, err := r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{planSeedsItem(lt.TrackLuna, 4)}, Via: spend.ViaHumanCreate})
	wantWorkerOutdated(t, err, "plan", "v2")
	if r.count(`SELECT COUNT(*) FROM estimates`) != estimates || r.count(`SELECT COUNT(*) FROM ledger`) != ledger {
		t.Error("a refused estimate wrote rows")
	}
	r.est(human, newSeedItem(lt.TrackLuna, 1)) // a plain render is untouched
	// v2 alone is not enough
	r.setCaps("v2")
	_, err = r.svc.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{planSeedsItem(lt.TrackLuna, 4)}, Via: spend.ViaHumanCreate})
	wantWorkerOutdated(t, err, "plan")
	// opened: estimable; closed again before create: create refuses, the token stays unused, nothing is reserved
	r.setCaps("plan", "v2")
	e := r.est(human, planSeedsItem(lt.TrackLuna, 4))
	r.setCaps()
	_, err = r.create(human, e, spend.ViaHumanCreate)
	wantWorkerOutdated(t, err, "plan", "v2")
	if r.count(`SELECT COUNT(*) FROM jobs`) != jobs || r.count(`SELECT COUNT(*) FROM ledger`) != ledger {
		t.Error("a refused create wrote rows")
	}
	r.setCaps("plan", "v2")
	r.mustCreate(human, e, spend.ViaHumanCreate) // the same token still works
}

// TestCreate_PlanJobRows: the job rows of a plan_seeds create: kind, needs, spec, operator, recipe reuse rule, source and draft ids,
// no take, one reservation per job.
func TestCreate_PlanJobRows(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	recipes := r.count(`SELECT COUNT(*) FROM recipes`)
	lunaRecipe := r.str(`SELECT recipe_id FROM tracks WHERE id = ?`, lt.TrackLuna)

	// a take with no patch: the job runs on the take's own recipe, no recipe is written
	c := r.mustCreate(human, r.est(human, planSeedsItem(lt.TrackLuna, 3)), spend.ViaHumanCreate)
	if n := r.count(`SELECT COUNT(*) FROM recipes`); n != recipes {
		t.Errorf("recipes %d -> %d: a take's own recipe is reused", recipes, n)
	}
	row := func(batch, col string) string {
		return r.str(`SELECT CAST(`+col+` AS TEXT) FROM jobs WHERE batch_id = ?`, batch)
	}
	for col, want := range map[string]string{"kind": "plan", "needs_json": `["plan","v2"]`, "operator": "plan_seeds", "recipe_id": lunaRecipe,
		"source_track_id": lt.TrackLuna, "state": "queued", "engine": "yue2"} {
		if got := row(c.BatchID, col); got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}
	if got := row(c.BatchID, "COALESCE(track_id, 'none')"); got != "none" {
		t.Errorf("a plan job has a track: %s", got)
	}
	if got := row(c.BatchID, "COALESCE(draft_id, 'none')"); got != "none" {
		t.Errorf("draft_id = %s", got)
	}
	var spec struct {
		Plan struct {
			Seeds []int64 `json:"seeds"`
			S     int     `json:"seconds_per_seed"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(row(c.BatchID, "spec_json")), &spec); err != nil || len(spec.Plan.Seeds) != 3 || spec.Plan.S < 1 {
		t.Errorf("spec = %v %v", spec, err)
	}
	var mut struct {
		Operator string `json:"operator"`
		Params   struct {
			Mode string `json:"mode"`
			N    int    `json:"n_seeds"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(row(c.BatchID, "mutation_json")), &mut); err != nil || mut.Operator != "plan_seeds" || mut.Params.Mode != "plan_seeds" || mut.Params.N != 3 {
		t.Errorf("mutation = %+v %v", mut, err)
	}
	if n := r.count(`SELECT COUNT(*) FROM tracks WHERE id IN (SELECT track_id FROM jobs WHERE batch_id = ?)`, c.BatchID); n != 0 {
		t.Errorf("plan job made %d tracks", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, c.BatchID); n != 1 {
		t.Errorf("reservations = %d", n)
	}

	// a patch: a new recipe of the planned content, the take's recipe untouched
	style := "dream pop, 90 bpm, female vocal"
	patched := planSeedsItem(lt.TrackLuna, 2)
	patched.Patch = &lineage.Patch{Style: &style}
	c2 := r.mustCreate(human, r.est(human, patched), spend.ViaHumanCreate)
	rid := row(c2.BatchID, "recipe_id")
	if rid == lunaRecipe || r.count(`SELECT COUNT(*) FROM recipes`) != recipes+1 {
		t.Errorf("patched plan: recipe %s, recipes %d", rid, r.count(`SELECT COUNT(*) FROM recipes`))
	}
	if got := r.str(`SELECT style FROM recipes WHERE id = ?`, rid); got != style {
		t.Errorf("recipe style = %q", got)
	}
	if got := r.str(`SELECT style FROM recipes WHERE id = ?`, lunaRecipe); got == style {
		t.Error("the take's own recipe was edited")
	}

	// a draft base: the job carries the draft and its recipe is new
	draft := generation.Item{Mode: lineage.ModePlanSeeds, DraftIDs: []string{lt.Draft1}, Desk: &generation.DeskItem{NSeeds: 2}}
	c3 := r.mustCreate(human, r.est(human, draft), spend.ViaHumanCreate)
	if got := row(c3.BatchID, "draft_id"); got != lt.Draft1 {
		t.Errorf("draft_id = %q", got)
	}
	if got := row(c3.BatchID, "kind"); got != "plan" {
		t.Errorf("kind = %s", got)
	}
	// the draft is in flight while its plan job lives
	_, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{draft}, Via: spend.ViaHumanCreate})
	r.wantProblem(err, http.StatusConflict, "DRAFT_NOT_GENERATABLE")

	// a composed recipe: a new recipe, no source track
	c4 := r.mustCreate(human, r.est(human, composePlanItem(composeLines("fresh-song", 3, ""), 2)), spend.ViaHumanCreate)
	if got := row(c4.BatchID, "COALESCE(source_track_id, 'none')"); got != "none" {
		t.Errorf("source_track_id = %s", got)
	}
	if got := r.str(`SELECT COUNT(*) FROM recipes WHERE request_id LIKE 'fresh-song%'`); got != "1" {
		t.Errorf("composed recipes = %s", got)
	}
}

// TestCreate_PlanJob_StaleRecipeID: two estimates plan the same new recipe id; the second create finds it taken and is refused as stale.
func TestCreate_PlanJob_StaleRecipeID(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	style := "dream pop, 90 bpm, female vocal"
	it := planSeedsItem(lt.TrackLuna, 2)
	it.Patch = &lineage.Patch{Style: &style}
	e := r.est(human, it)
	r.mustCreate(human, r.est(human, it), spend.ViaHumanCreate) // the same id is free in both estimates
	_, err := r.create(human, e, spend.ViaHumanCreate)
	if err == nil {
		t.Fatal("a second create of the same planned recipe id must be stale")
	}
	r.wantProblem(err, http.StatusConflict, "ESTIMATE_STALE")
}

// TestQuote_PlanJobExamples: a plan job of n seeds of A audio seconds costs LoadS + ceil(PlanMilli x n x A / 1000) seconds, its
// timeout the usual 1.6 x that + 80 (A = 200, n = 8: expected 372 s, timeout 660 s).
func TestQuote_PlanJobExamples(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	e := r.est(human, composePlanItem(composeLines("quote-song", 12, ""), 5))
	tp := e.Items[0].Takes[0]
	items := r.itemsJSON(e)
	gen := 10 + (220*5*tp.ExpectedAudioS+999)/1000
	if got := int(items[0]["expected_s"].(float64)); got != gen+10 {
		t.Errorf("expected_s = %d, want %d (A = %d)", got, gen+10, tp.ExpectedAudioS)
	}
	if want := (8*gen+4)/5 + 80; tp.TimeoutS != want {
		t.Errorf("timeout_s = %d, want %d", tp.TimeoutS, want)
	}
	if tp.MinAudioS != 0 {
		t.Errorf("min_audio_s = %d", tp.MinAudioS)
	}
}

// TestEstimate_EditorPlanSeedsAwaitsOwner: an Editor may estimate and create a plan_seeds batch, only the Owner approves it, and the
// jobs wait until then.
func TestEstimate_EditorPlanSeedsAwaitsOwner(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.AutoApproveTakes, caps.AutoApproveMicro = 50, 1_000_000_000
	r := newRig(t, withCaps(caps))
	r.setCaps("plan", "v2")
	e := r.est(bobEditor, planSeedsItem(lt.TrackLuna, 4))
	if !e.RequiresApproval || !contains(e.ApprovalReasons, "editor_batch") {
		t.Fatalf("estimate: requires %v reasons %v", e.RequiresApproval, e.ApprovalReasons)
	}
	c := r.mustCreate(bobEditor, e, spend.ViaAPI)
	if c.State != "awaiting_confirmation" || c.ApprovalState != "pending" {
		t.Errorf("state %s approval %s", c.State, c.ApprovalState)
	}
	if got := r.str(`SELECT DISTINCT state FROM jobs WHERE batch_id = ?`, c.BatchID); got != "awaiting_confirmation" {
		t.Errorf("job state %s", got)
	}
	if _, err := r.svc.Approve(context.Background(), bobEditor, c.BatchID, "self"); problemCode(err) != "OWNER_ONLY" {
		t.Errorf("editor approve: %v", err)
	}
	if got, err := r.svc.Approve(context.Background(), human, c.BatchID, "ok"); err != nil || got.State != "active" {
		t.Errorf("owner approve: %v %v", got.State, err)
	}
}

// TestEstimate_PlanSeeds_MixedWithRenders: plan jobs and renders share one batch, each as its own kind.
func TestEstimate_PlanSeeds_MixedWithRenders(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	e := r.est(human, planSeedsItem(lt.TrackLuna, 2), newSeedItem(lt.TrackLuna, 1))
	if e.Takes != 2 || e.Items[0].Mode != lineage.ModePlanSeeds || e.Items[1].Mode != lineage.ModeNewSeed {
		t.Fatalf("estimate = %+v", e.Items)
	}
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if got := r.str(`SELECT group_concat(kind, ',') FROM (SELECT kind FROM jobs WHERE batch_id = ? ORDER BY take_index)`, c.BatchID); got != "plan,generate" {
		t.Errorf("kinds = %s", got)
	}
}

// TestEstimate_PlanSeeds_RealJobsCountAgainstMaxTakes: an item counts as one take before expansion, but the jobs it splits into
// count against the batch cap afterwards.
func TestEstimate_PlanSeeds_RealJobsCountAgainstMaxTakes(t *testing.T) {
	t.Parallel()
	r := planRig(t)
	var items []generation.Item
	for range 20 {
		items = append(items, composePlanItem(composeLines("long-song", 60, ""), 16))
	}
	wantProblem(t, r.estErr(human, items...), 422, "BATCH_TOO_LARGE")
}
