package generation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// wantProblem asserts err is a *problem.Error with the status and code.
func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want problem %d %s", err, status, code)
	}
	if pe.Status != status || pe.Code != code {
		t.Fatalf("problem = %d %s %v, want %d %s", pe.Status, pe.Code, pe.Details, status, code)
	}
	return pe
}

// takeDoc is the part of items_json the tests read.
type takeDoc struct {
	TakeIndex     int             `json:"take_index"`
	ItemIndex     int             `json:"item_index"`
	Mode          string          `json:"mode"`
	Operator      string          `json:"operator"`
	ParentTrackID *string         `json:"parent_track_id"`
	DraftID       *string         `json:"draft_id"`
	DraftRevision *int            `json:"draft_revision"`
	RequestJSON   string          `json:"request_json"`
	RenderHash    string          `json:"render_hash"`
	RequestID     string          `json:"request_id"`
	Seed          int64           `json:"seed"`
	Title         string          `json:"title"`
	ReuseRecipeID *string         `json:"reuse_recipe_id"`
	Params        map[string]any  `json:"params"`
	Patch         json.RawMessage `json:"patch"`
	InputBytes    int             `json:"input_bytes"`
	Warnings      []struct{ Code, Field, Severity, Message string }
}

func (r *rig) items(estimateID string) []takeDoc {
	r.t.Helper()
	var raw string
	if err := r.st.R.QueryRow(`SELECT items_json FROM estimates WHERE id = ?`, estimateID).Scan(&raw); err != nil {
		r.t.Fatal(err)
	}
	var out []takeDoc
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func seedsOf(e generation.Estimate) []int64 {
	var out []int64
	for _, it := range e.Items {
		for _, tk := range it.Takes {
			out = append(out, tk.Seed)
		}
	}
	return out
}

func idsOf(e generation.Estimate) []string {
	var out []string
	for _, it := range e.Items {
		for _, tk := range it.Takes {
			out = append(out, tk.RequestID)
		}
	}
	return out
}

func (r *rig) estErr(p auth.Principal, items ...generation.Item) error {
	r.t.Helper()
	before := r.count(`SELECT COUNT(*) FROM estimates`)
	_, err := r.svc.Estimate(context.Background(), p, generation.EstimateIn{Items: items, Via: viaFor(p)})
	if err == nil {
		r.t.Fatal("estimate succeeded, want an error")
	}
	if after := r.count(`SELECT COUNT(*) FROM estimates`); after != before {
		r.t.Errorf("a refused estimate wrote %d estimate rows", after-before)
	}
	return err
}

func TestEstimate_NewSeedsDeterministicIdsAndTitles(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(agent, newSeedItem(lt.TrackLuna, 3))
	if diff := cmp.Diff([]int64{102, 103, 104}, seedsOf(e)); diff != "" {
		t.Errorf("seeds (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"fx-luna-s102", "fx-luna-s103", "fx-luna-s104"}, idsOf(e)); diff != "" {
		t.Errorf("ids (-want +got):\n%s", diff)
	}
	tk := e.Items[0].Takes[0]
	if tk.Title != "Luna Park · s102" || tk.Operator != "reroll_seed" || tk.ParentTrackID == nil || *tk.ParentTrackID != lt.TrackLuna || tk.DraftID != nil {
		t.Errorf("take = %+v", tk)
	}
	docs := r.items(e.ID)
	if docs[0].Params["mode"] != "new_seed" || docs[0].Params["seed_policy"] != "auto" || docs[0].Params["n_seeds"] != float64(3) || docs[0].Params["via"] != nil {
		t.Errorf("params = %v", docs[0].Params)
	}
	if string(docs[0].Patch) != "null" || docs[0].ReuseRecipeID != nil || docs[0].InputBytes <= 0 {
		t.Errorf("patch %s reuse %v input %d", docs[0].Patch, docs[0].ReuseRecipeID, docs[0].InputBytes)
	}

	// created recipes count as used: a second estimate after a create continues above them
	c := r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 3)), spend.ViaHumanCreate)
	if c.Takes != 3 {
		t.Fatalf("created %d takes", c.Takes)
	}
	e2 := r.est(agent, newSeedItem(lt.TrackLuna, 2))
	if diff := cmp.Diff([]int64{105, 106}, seedsOf(e2)); diff != "" {
		t.Errorf("seeds after a create (-want +got):\n%s", diff)
	}
}

func TestEstimate_ExplicitSeedsAndModes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna := lt.TrackLuna
	newLyrics := "[Verse 1]\nnew words here\n[Chorus]\nsing it louder"

	e := r.est(agent,
		generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Seeds: []int64{5, 0, 9007199254740991}},
		generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna, Patch: &lineage.Patch{Cot: sp("melody")}},
		generation.Item{Mode: lineage.ModeLyricEdit, ParentTrackID: &luna, NSeeds: 3, Patch: &lineage.Patch{Lyrics: &newLyrics}},
		generation.Item{Mode: lineage.ModeStyleEdit, ParentTrackID: &luna, Patch: &lineage.Patch{Style: sp("rock, 140 bpm"), ABC: lineage.OptString{Set: true, Null: true}}},
	)
	if diff := cmp.Diff([]int64{5, 0, 9007199254740991, 101, 101, 102, 103, 101}, seedsOf(e)); diff != "" {
		t.Errorf("seeds (-want +got):\n%s", diff)
	}
	ops := []string{}
	for _, it := range e.Items {
		for _, tk := range it.Takes {
			ops = append(ops, tk.Operator)
		}
	}
	want := []string{"reroll_seed", "reroll_seed", "reroll_seed", "same_seed_edit", "lyric_edit", "lyric_edit", "lyric_edit", "style_edit"}
	if diff := cmp.Diff(want, ops); diff != "" {
		t.Errorf("operators (-want +got):\n%s", diff)
	}
	docs := r.items(e.ID)
	if docs[3].Params["seed_policy"] != "parent" || docs[2].Params["seed_policy"] != "explicit" || docs[5].Params["seed_policy"] != "auto" {
		t.Errorf("policies: %v %v %v", docs[3].Params, docs[2].Params, docs[5].Params)
	}
	if !strings.Contains(string(docs[4].Patch), `"lyrics"`) || !strings.Contains(string(docs[7].Patch), `"abc":null`) {
		t.Errorf("patches: %s %s", docs[4].Patch, docs[7].Patch)
	}
	// the edited request really carries the edit, the seed and the allocated id
	if !strings.Contains(docs[4].RequestJSON, "new words here") || !strings.Contains(docs[7].RequestJSON, `"style":"rock, 140 bpm"`) {
		t.Errorf("requests: %s / %s", docs[4].RequestJSON, docs[7].RequestJSON)
	}
	// the same-seed edit with no seed collision keeps the parent's seed, so its id is fx-luna-s101
	if docs[3].RequestID != "fx-luna-s101" {
		t.Errorf("same_seed_edit id = %s", docs[3].RequestID)
	}
}

func TestEstimate_FromDraftReuseAndTemplates(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}})
	docs := r.items(e.ID)
	if len(docs) != 1 || docs[0].ReuseRecipeID == nil || docs[0].RequestID != "fxb_001_v1" || docs[0].Seed != 201 || docs[0].DraftRevision == nil {
		t.Fatalf("reuse: %+v", docs)
	}
	if docs[0].Title != "Fxb 001 V1" || docs[0].Operator != "from_draft" || docs[0].ParentTrackID != nil {
		t.Errorf("take = %+v", docs[0])
	}
	if want := r.str(`SELECT source_recipe_id FROM recipe_drafts WHERE id = ?`, lt.Draft1); *docs[0].ReuseRecipeID != want {
		t.Errorf("reuse_recipe_id = %s, want %s", *docs[0].ReuseRecipeID, want)
	}
	for _, w := range e.Warnings {
		if w.Code == "duplicate_render" {
			t.Errorf("a reused draft recipe must not be its own duplicate: %+v", w)
		}
	}

	// more seeds: the six backlog drafts share one content, so seeds 201..206 are used
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, NSeeds: 3})
	if diff := cmp.Diff([]int64{201, 207, 208}, seedsOf(e)); diff != "" {
		t.Errorf("draft seeds (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"fxb_001_v1", "fxb_001_v1-s207", "fxb_001_v1-s208"}, idsOf(e)); diff != "" {
		t.Errorf("draft ids (-want +got):\n%s", diff)
	}

	// a mother (the draft's own) shapes the title; an override replaces it
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.DraftVarianti}})
	if tk := e.Items[0].Takes[0]; tk.Title != "Luna Park · v1" || tk.ParentTrackID == nil || *tk.ParentTrackID != lt.TrackLuna {
		t.Errorf("mother take = %+v", tk)
	}
	citta := lt.TrackCitta
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.DraftVarianti}, ParentTrackID: &citta})
	if tk := e.Items[0].Takes[0]; *tk.ParentTrackID != lt.TrackCitta || !strings.HasPrefix(tk.Title, "Città lenta") {
		t.Errorf("override take = %+v", tk)
	}

	// explicit seed other than the draft's: a new row, no reuse
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, Seeds: []int64{300}})
	if d := r.items(e.ID)[0]; d.ReuseRecipeID != nil || d.RequestID != "fxb_001_v1-s300" {
		t.Errorf("other seed = %+v", d)
	}

	// the draft's recipe already has a live job: no reuse, the id is allocated like any other
	src := r.str(`SELECT source_recipe_id FROM recipe_drafts WHERE id = ?`, lt.Draft1)
	lt.SeedJob(t, r.st, lt.FixedNow, lt.JobSeed{Name: "blocker", RecipeID: src})
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}})
	if d := r.items(e.ID)[0]; d.ReuseRecipeID != nil || d.RequestID != "fxb_001_v1-s201" {
		t.Errorf("rendered draft = %+v", d)
	}

	// templates: one inner mode over several mothers, the template recorded as `via`
	luna := lt.TrackLuna
	e = r.est(agent, generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna, citta}, NSeeds: 2})
	d := r.items(e.ID)
	if len(d) != 4 || d[0].Operator != "reroll_seed" || d[0].Params["via"] != "from_template" || d[0].Params["mode"] != "new_seed" || d[2].ParentTrackID == nil || *d[2].ParentTrackID != citta {
		t.Errorf("template = %+v", d)
	}
	if e.Items[0].Mode != lineage.ModeFromTemplate {
		t.Errorf("item mode = %s", e.Items[0].Mode)
	}
}

func TestEstimate_ComposeNew(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	recipe := func(id string) []byte {
		return []byte(`{"id":"` + id + `","style":"indie pop, 100 bpm, female vocal","lyrics":"[Verse 1]\nline one\nline two\n[Chorus]\nline three\nline four","cot":"full","seed":4000}`)
	}
	title := "My New Song"
	e := r.est(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("syn-a"), Title: &title})
	if tk := e.Items[0].Takes[0]; tk.RequestID != "syn-a" || tk.Seed != 4000 || tk.Title != title || tk.Operator != "compose_new" || tk.ParentTrackID != nil {
		t.Errorf("compose = %+v", tk)
	}
	if d := r.items(e.ID)[0]; d.Params["seed_policy"] != "recipe" {
		t.Errorf("policy = %v", d.Params)
	}
	// several seeds: no id is kept, every take is <id>-s<seed>
	e = r.est(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("syn-a"), NSeeds: 3})
	if diff := cmp.Diff([]string{"syn-a-s4000", "syn-a-s4001", "syn-a-s4002"}, idsOf(e)); diff != "" {
		t.Errorf("ids (-want +got):\n%s", diff)
	}
	if e.Items[0].Takes[1].Title != "Syn A S4001" {
		t.Errorf("title = %q", e.Items[0].Takes[1].Title)
	}
	// an id that exists is not kept
	e = r.est(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("fx-luna")})
	if id := e.Items[0].Takes[0].RequestID; id != "fx-luna-s4000" {
		t.Errorf("taken id was kept: %s", id)
	}
	// two identical compose items: the second's render equals the first take's
	e = r.est(agent,
		generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("syn-b")},
		generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("syn-c")})
	if diff := cmp.Diff([]string{"syn-b", "syn-c"}, idsOf(e)); diff != "" {
		t.Errorf("ids (-want +got):\n%s", diff)
	}
}

func TestEstimate_RefusesUnknownRecipeAndLossy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna, citta, ref, lossy := lt.TrackLuna, lt.TrackCitta, lt.TrackRefFlac, lt.TrackLossy
	for _, c := range []struct {
		name  string
		items []generation.Item
		track string
	}{
		{"new_seed on a reference FLAC", []generation.Item{{Mode: lineage.ModeNewSeed, ParentTrackID: &ref}}, ref},
		{"new_seed on a lossy reference", []generation.Item{{Mode: lineage.ModeNewSeed, ParentTrackID: &lossy}}, lossy},
		{"one bad mother among five", []generation.Item{{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed,
			ParentTrackIDs: []string{luna, citta, lt.TrackTwinA, ref, lt.TrackShort}}}, ref},
		{"from_draft override", []generation.Item{{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, ParentTrackID: &ref}}, ref},
		{"the bad item comes second", []generation.Item{newSeedItem(luna, 1), {Mode: lineage.ModeStyleEdit, ParentTrackID: &lossy, Patch: &lineage.Patch{Style: sp("x")}}}, lossy},
	} {
		err := r.estErr(agent, c.items...)
		pe := wantProblem(t, err, 422, "RECIPE_UNKNOWN")
		if pe.Details["track_id"] != c.track {
			t.Errorf("%s: track_id = %v, want %s", c.name, pe.Details["track_id"], c.track)
		}
	}
	// a lossy master of a track that has a recipe: LOSSY_MASTER (the lossy-frozen trigger is dropped to build
	// the impossible row; the check must hold even if one slipped through)
	r.exec(`DROP TRIGGER tracks_lossy_frozen`)
	r.exec(`UPDATE tracks SET lossy = 1 WHERE id = ?`, lt.TrackTwinA)
	twin := lt.TrackTwinA
	if pe := wantProblem(t, r.estErr(agent, newSeedItem(twin, 1)), 422, "LOSSY_MASTER"); pe.Details["track_id"] != twin {
		t.Errorf("lossy: %v", pe.Details)
	}
	// unknown track
	missing := "no-such-track"
	pe := wantProblem(t, r.estErr(agent, newSeedItem(missing, 1)), 422, "UNKNOWN_TRACK")
	if pe.Details["track_id"] != missing {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestEstimate_ComposeIdOfStubRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	stub := r.str(`SELECT request_id FROM recipes WHERE engine = 'unknown' ORDER BY id LIMIT 1`)
	rec := `{"id":"` + stub + `","style":"pop","lyrics":"[Verse 1]\nline one\nline two","cot":"full","seed":4000}`
	err := r.estErr(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: []byte(rec)})
	pe := wantProblem(t, err, 422, "LINT_FAILED")
	b, _ := json.Marshal(pe.Details)
	if !strings.Contains(string(b), `"recipe_unknown"`) {
		t.Errorf("details = %s", b)
	}
}

func TestEstimate_LintBlocksErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, c := range []struct{ name, rec, code string }{
		{"cot none", `{"id":"a-1","style":"pop","lyrics":"[Verse 1]\nline","cot":"none","seed":5}`, "bad_cot"},
		{"abc with cot off", `{"id":"a-2","style":"pop","lyrics":"[Verse 1]\nline","cot":"off","seed":5,"abc":"X:1\nK:C\nCDEF|"}`, "abc_requires_cot"},
		{"missing lyrics", `{"id":"a-3","style":"pop","cot":"full","seed":5}`, "missing_lyrics"},
	} {
		err := r.estErr(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: []byte(c.rec)})
		pe := wantProblem(t, err, 422, "LINT_FAILED")
		b, _ := json.Marshal(pe.Details)
		if !strings.Contains(string(b), c.code) {
			t.Errorf("%s: details = %s, want %s", c.name, b, c.code)
		}
	}
}

func TestEstimate_PatchThatBreaksProtocolIsLintFailed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// FixtureM3's job recipe has no abc; add one via the patch, then switch cot off in the same patch
	luna := lt.TrackLuna
	abc := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nCDEF|CDEF|]\n"
	err := r.estErr(agent, generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna,
		Patch: &lineage.Patch{Cot: sp("off"), ABC: lineage.OptString{Set: true, Value: abc}}})
	pe := wantProblem(t, err, 422, "LINT_FAILED")
	b, _ := json.Marshal(pe.Details)
	if !strings.Contains(string(b), "abc_requires_cot") {
		t.Errorf("details = %s", b)
	}
}

func TestEstimate_DuplicateRenderWarning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna := lt.TrackLuna
	// an explicit re-render of an imported recipe: a warning, not an error (D-M3-11)
	e := r.est(agent, generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna})
	var dup []generation.Warning
	for _, w := range e.Warnings {
		if w.Code == "duplicate_render" {
			dup = append(dup, w)
		}
	}
	if len(dup) != 1 || dup[0].TakeIndex != 0 || dup[0].Severity != "warning" || !strings.Contains(dup[0].Text, "recipe") {
		t.Errorf("warnings = %+v", e.Warnings)
	}
	// two takes with the same render in one estimate
	both := r.est(agent,
		generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna},
		generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna})
	var second []generation.Warning
	for _, w := range both.Warnings {
		if w.Code == "duplicate_render" && w.TakeIndex == 1 {
			second = append(second, w)
		}
	}
	if len(second) != 1 {
		t.Fatalf("second take warnings = %+v", both.Warnings)
	}
	// parent_discarded
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, lt.TrackCitta)
	e = r.est(agent, newSeedItem(lt.TrackCitta, 1))
	found := false
	for _, w := range e.Warnings {
		found = found || w.Code == "parent_discarded"
	}
	if !found {
		t.Errorf("no parent_discarded: %+v", e.Warnings)
	}
}

func TestEstimate_BatchTooLarge(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	items := []generation.Item{}
	for range 6 {
		items = append(items, newSeedItem(lt.TrackLuna, 10))
	}
	pe := wantProblem(t, r.estErr(agent, items...), 422, "BATCH_TOO_LARGE")
	if pe.Details["takes"] != 60 || pe.Details["limit"] != 50 {
		t.Errorf("details = %v", pe.Details)
	}
	items = items[:0]
	for range 21 {
		items = append(items, newSeedItem(lt.TrackLuna, 1))
	}
	pe = wantProblem(t, r.estErr(agent, items...), 422, "BATCH_TOO_LARGE")
	if pe.Details["items"] != 21 || pe.Details["limit"] != 20 {
		t.Errorf("details = %v", pe.Details)
	}
	// exactly 50 takes is fine
	items = items[:0]
	for range 5 {
		items = append(items, newSeedItem(lt.TrackLuna, 10))
	}
	if e := r.est(agent, items...); e.Takes != 50 {
		t.Errorf("takes = %d", e.Takes)
	}
}

func TestEstimate_InvalidItemTable(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna, citta := lt.TrackLuna, lt.TrackCitta
	rec := []byte(`{"id":"inv-1","style":"pop","lyrics":"[Verse 1]\nline","cot":"full","seed":5}`)
	title := "T"
	cases := []struct {
		name        string
		item        generation.Item
		field, rule string
	}{
		{"unknown mode", generation.Item{Mode: "teleport", ParentTrackID: &luna}, "mode", "unknown_mode"},
		{"new_seed without parent", generation.Item{Mode: lineage.ModeNewSeed}, "parent_track_id", "required"},
		{"new_seed with patch", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Patch: &lineage.Patch{Style: sp("x")}}, "patch", "patch_forbidden"},
		{"new_seed with drafts", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, DraftIDs: []string{lt.Draft1}}, "draft_ids", "not_allowed_for_new_seed"},
		{"new_seed with the parent's seed", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Seeds: []int64{101}}, "seeds", "contains_parent_seed"},
		{"same_seed_edit n_seeds 2", generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna, NSeeds: 2}, "n_seeds", "same_seed_edit_renders_one_take"},
		{"same_seed_edit other seed", generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna, Seeds: []int64{7}}, "seeds", "same_seed_edit_uses_the_parent_seed"},
		{"lyric_edit without patch", generation.Item{Mode: lineage.ModeLyricEdit, ParentTrackID: &luna}, "patch", "patch_required"},
		{"lyric_edit with style", generation.Item{Mode: lineage.ModeLyricEdit, ParentTrackID: &luna, Patch: &lineage.Patch{Lyrics: sp("x"), Style: sp("y")}}, "patch.style", "style_forbidden"},
		{"style_edit without style", generation.Item{Mode: lineage.ModeStyleEdit, ParentTrackID: &luna, Patch: &lineage.Patch{Cot: sp("off")}}, "patch.style", "style_required"},
		{"from_draft without drafts", generation.Item{Mode: lineage.ModeFromDraft}, "draft_ids", "required"},
		{"from_draft with a patch", generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1}, Patch: &lineage.Patch{Style: sp("x")}}, "patch", "not_allowed_for_from_draft"},
		{"compose without recipe", generation.Item{Mode: lineage.ModeComposeNew}, "recipe", "required"},
		{"compose with a parent", generation.Item{Mode: lineage.ModeComposeNew, Recipe: rec, ParentTrackID: &luna}, "parent_track_id", "not_allowed_for_compose_new"},
		{"template with one mother", generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna}}, "parent_track_ids", "needs_2_to_50"},
		{"template without template_mode", generation.Item{Mode: lineage.ModeFromTemplate, ParentTrackIDs: []string{luna, citta}}, "template_mode", "required"},
		{"template new_seed with patch", generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna, citta}, Patch: &lineage.Patch{Style: sp("x")}}, "patch", "patch_forbidden"},
		{"duplicate mothers", generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna, luna}}, "parent_track_ids", "duplicate"},
		{"duplicate seeds", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Seeds: []int64{5, 5}}, "seeds", "duplicate"},
		{"seed out of range", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, Seeds: []int64{-1}}, "seeds", "out_of_range"},
		{"title on several takes", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, NSeeds: 2, Title: &title}, "title", "single_take_only"},
		{"duplicate drafts", generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1, lt.Draft1}}, "draft_ids", "duplicate"},
		{"n_seeds 11", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, NSeeds: 11}, "n_seeds", "range_1_to_10"},
	}
	if len(cases) < 15 {
		t.Fatalf("only %d rows", len(cases))
	}
	for _, c := range cases {
		err := r.estErr(agent, c.item)
		pe := wantProblem(t, err, 422, "INVALID_ITEM")
		if pe.Details["field"] != c.field || pe.Details["rule"] != c.rule || pe.Details["item"] != 0 {
			t.Errorf("%s: details = %v, want field %s rule %s", c.name, pe.Details, c.field, c.rule)
		}
	}
	// the item index names the offender
	pe := wantProblem(t, r.estErr(agent, newSeedItem(luna, 1), generation.Item{Mode: lineage.ModeNewSeed}), 422, "INVALID_ITEM")
	if pe.Details["item"] != 1 {
		t.Errorf("item = %v", pe.Details["item"])
	}
}

func TestEstimate_InputTooLarge(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	bigABC := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n" + strings.Repeat("<", 400_000) // HTML-escaped to < in the job input
	rec, _ := json.Marshal(map[string]any{"id": "big-1", "style": "pop", "lyrics": "[Verse 1]\nline", "cot": "full", "seed": 5, "abc": bigABC})
	pe := wantProblem(t, r.estErr(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: rec}), 422, "INVALID_ITEM")
	if pe.Details["rule"] != "input_too_large" {
		t.Errorf("details = %v", pe.Details)
	}
	smaller, _ := json.Marshal(map[string]any{"id": "big-2", "style": "pop", "lyrics": "[Verse 1]\nline", "cot": "full", "seed": 5, "abc": bigABC[:10_000]})
	if e := r.est(agent, generation.Item{Mode: lineage.ModeComposeNew, Recipe: smaller}); e.Takes != 1 {
		t.Errorf("takes = %d", e.Takes)
	}
}

func TestEstimate_DraftNotGeneratable(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`UPDATE recipe_drafts SET status = 'archived' WHERE id = ?`, lt.Draft2)
	pe := wantProblem(t, r.estErr(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1, lt.Draft2}}), 409, "DRAFT_NOT_GENERATABLE")
	if diff := cmp.Diff([]string{lt.Draft2}, pe.Details["draft_ids"]); diff != "" || pe.Details["why"] != "archived" {
		t.Errorf("details = %v", pe.Details)
	}
	// a fulfilled draft
	r.exec(`UPDATE recipe_drafts SET status = 'fulfilled', fulfilled_by_track_id = ? WHERE id = ?`, lt.TrackCitta, lt.Draft3)
	pe = wantProblem(t, r.estErr(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft3}}), 409, "DRAFT_NOT_GENERATABLE")
	if pe.Details["why"] != "fulfilled" {
		t.Errorf("details = %v", pe.Details)
	}
}

func TestEstimate_DraftInFlight(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	seeded := lt.SeedJob(t, r.st, lt.FixedNow, lt.JobSeed{Name: "inflight"})
	// jobs_frozen forbids changing draft_id of an existing job: insert one that names the draft instead
	r.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES ('job-draft',?,1,'generate','yue2','fake',?,?,'from_draft','{}','T',216,96,186,1,1,'queued','2026-10-06T12:00:00.000Z','2026-10-06T12:00:00.000Z','2026-10-06T12:00:00.000Z')`,
		seeded.BatchID, seeded.RecipeID, lt.Draft4)
	pe := wantProblem(t, r.estErr(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft4}}), 409, "DRAFT_NOT_GENERATABLE")
	if pe.Details["why"] != "in_flight" {
		t.Errorf("details = %v", pe.Details)
	}
	pe = wantProblem(t, r.estErr(agent, generation.Item{Mode: lineage.ModeFromDraft, DraftIDs: []string{"nope"}}), 422, "UNKNOWN_DRAFT")
	if diff := cmp.Diff([]string{"nope"}, pe.Details["draft_ids"]); diff != "" {
		t.Error(diff)
	}
}

func TestEstimate_ProviderDisabledReasons(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	items := generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}}
	reason := func(svc *generation.Service) string {
		t.Helper()
		before := r.count(`SELECT COUNT(*) FROM estimates`)
		_, err := svc.Estimate(context.Background(), agent, items)
		pe := wantProblem(t, err, 503, "GENERATION_DISABLED")
		if r.count(`SELECT COUNT(*) FROM estimates`) != before {
			t.Error("a disabled estimate wrote a row")
		}
		return fmt.Sprint(pe.Details["reason"])
	}
	idle := 20
	mod := func(f func(q *provider.Quote)) provider.Quote {
		q := r.goodQuote()
		f(&q)
		return q
	}
	none := r.withGate(r.gate(provider.ModeNone))
	if got := reason(none); got != provider.ReasonBackendNone {
		t.Errorf("none: %s", got)
	}
	if got := reason(r.runpodService(r.goodQuote(), true, false)); got != provider.ReasonKeysAbsent {
		t.Errorf("keys: %s", got)
	}
	for _, c := range []struct {
		want string
		q    provider.Quote
	}{
		{provider.ReasonEndpointInactive, mod(func(q *provider.Quote) { q.Endpoint.WorkersMax = 0 })},
		{provider.ReasonEndpointConfig, mod(func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = nil })},
		{provider.ReasonEndpointConfig, mod(func(q *provider.Quote) { q.Endpoint.WorkersMax = 3; q.Endpoint.IdleTimeoutS = &idle })},
		{provider.ReasonQuoteStale, mod(func(q *provider.Quote) { q.ObservedAt = r.clk.Now().Add(-2 * 60 * 60e9) })},
		{provider.ReasonCatalogUnreadable, mod(func(q *provider.Quote) { q.ListPricePPH = 0 })},
		{provider.ReasonSweepUnreadable, mod(func(q *provider.Quote) { q.SweepUnreadable = true })},
	} {
		if got := reason(r.runpodService(c.q, true, true)); got != c.want {
			t.Errorf("want %s, got %s", c.want, got)
		}
	}
	// estimates work while disarmed (only create refuses): the brief says so
	e, err := r.runpodService(r.goodQuote(), false, true).Estimate(context.Background(), agent, items)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Provider.Enabled || e.Provider.Armed || e.Backend != provider.BackendRunPod {
		t.Errorf("provider = %+v backend %s", e.Provider, e.Backend)
	}
	// and the serverless quote drives the price: 690000 list, no margin in this rig
	if e.PricePerHour != 690_000 || len(e.GPUPools) != 1 || e.PriceObservedAt == nil {
		t.Errorf("price %d pools %v observed %v", e.PricePerHour, e.GPUPools, e.PriceObservedAt)
	}
}

func TestEstimate_ApprovalPreview(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(agent, newSeedItem(lt.TrackLuna, 6))
	if !e.RequiresApproval || !contains(e.ApprovalReasons, spend.ReasonTakesOverLim) {
		t.Errorf("6 takes: %v %v", e.RequiresApproval, e.ApprovalReasons)
	}
	e = r.est(agent, newSeedItem(lt.TrackLuna, 1))
	if e.RequiresApproval && !contains(e.ApprovalReasons, spend.ReasonCostOverLimit) {
		t.Errorf("1 take: %v %v", e.RequiresApproval, e.ApprovalReasons)
	}
	// a human on the SPA path (human_create) needs none; on the API path always
	e, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 6)}, Via: spend.ViaHumanCreate})
	if err != nil || e.RequiresApproval || len(e.ApprovalReasons) != 0 {
		t.Errorf("human_create: %+v %v", e.ApprovalReasons, err)
	}
	e, err = r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}, Via: spend.ViaAPI})
	if err != nil || !e.RequiresApproval || !contains(e.ApprovalReasons, spend.ReasonHumanAPIPath) {
		t.Errorf("api: %+v %v", e.ApprovalReasons, err)
	}
	// a token's own auto-approve limit applies
	r.exec(`INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at,auto_approve_takes,auto_approve_micro_usd) VALUES ('fixture-token','fixture-agent','p','h','read,generate','2026-10-06T00:00:00Z',0,0)`)
	e = r.est(agent, newSeedItem(lt.TrackLuna, 1))
	if !e.RequiresApproval || !contains(e.ApprovalReasons, spend.ReasonTakesOverLim) || !contains(e.ApprovalReasons, spend.ReasonCostOverLimit) {
		t.Errorf("token limits: %v", e.ApprovalReasons)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestEstimate_BudgetFit(t *testing.T) {
	t.Parallel()
	d := spend.DefaultCaps
	for _, c := range []struct {
		name string
		caps spend.Caps
		p    auth.Principal
		want []string
	}{
		{"fits", d, agent, []string{}},
		{"batch", func() spend.Caps { x := d; x.BatchMicro = 1000; return x }(), agent, []string{"batch"}},
		{"monthly", func() spend.Caps { x := d; x.MonthlyMicro = 1000; return x }(), agent, []string{"monthly"}},
		{"daily", func() spend.Caps { x := d; x.AgentDailyMicro = 1000; return x }(), agent, []string{"daily"}},
		{"human daily", func() spend.Caps { x := d; x.HumanDailyMicro = 1000; return x }(), human, []string{"daily"}},
		{"all three", func() spend.Caps { x := d; x.BatchMicro, x.MonthlyMicro, x.AgentDailyMicro = 1, 1, 1; return x }(), agent, []string{"batch", "monthly", "daily"}},
	} {
		r := newRig(t, withCaps(c.caps))
		e := r.est(c.p, newSeedItem(lt.TrackLuna, 2))
		if diff := cmp.Diff(c.want, e.Budget.WouldExceed); diff != "" || e.Budget.Fits != (len(c.want) == 0) {
			t.Errorf("%s: %v fits=%v", c.name, e.Budget.WouldExceed, e.Budget.Fits)
		}
		if e.BatchCap != c.caps.BatchMicro || e.Month.Cap != c.caps.MonthlyMicro {
			t.Errorf("%s: pots %+v %+v batch %d", c.name, e.Month, e.Day, e.BatchCap)
		}
	}
	// the pots show the fixture's settled and outstanding amounts
	r := newRig(t)
	e := r.est(human, newSeedItem(lt.TrackLuna, 1))
	if e.Month.Used <= 0 || e.Month.Left != e.Month.Cap-e.Month.Used || e.Day.Cap != spend.DefaultCaps.HumanDailyMicro {
		t.Errorf("pots month %+v day %+v", e.Month, e.Day)
	}
}

func TestEstimate_CeilingShown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := r.est(agent, newSeedItem(lt.TrackLuna, 7))
	var sumMax, maxOne money.MicroUSD
	for _, tk := range e.Items[0].Takes {
		sumMax += tk.Max
		maxOne = max(maxOne, tk.Max)
	}
	overhead := money.ForSeconds(180+20, 690_000) // cold start + idle, price without margin
	if e.Cost.AttemptOverhead != overhead || overhead != 38334 {
		t.Errorf("overhead = %d", e.Cost.AttemptOverhead)
	}
	if e.Cost.Max != sumMax {
		t.Errorf("max %d != Σ take.max %d", e.Cost.Max, sumMax)
	}
	if e.Cost.RetryAllowance != 2*maxOne || e.Cost.Ceiling != e.Cost.Max+e.Cost.RetryAllowance {
		t.Errorf("retry %d ceiling %d (max %d, one %d)", e.Cost.RetryAllowance, e.Cost.Ceiling, e.Cost.Max, maxOne)
	}
	if e.Cost.Expected <= 0 || e.Cost.Expected > e.Cost.Max || e.ETAS <= 180 {
		t.Errorf("expected %d eta %d", e.Cost.Expected, e.ETAS)
	}
	if got := e.Cost.Ceiling.DisplayCeil(); !strings.HasPrefix(got, "$") {
		t.Errorf("display = %q", got)
	}
	// the stored estimate agrees with what is returned
	if got := r.val(`SELECT ceiling_micro_usd FROM estimates WHERE id = ?`, e.ID); got != int64(e.Cost.Ceiling) {
		t.Errorf("stored ceiling %d", got)
	}
	if got := r.val(`SELECT max_micro_usd + retry_allowance_micro_usd - ceiling_micro_usd FROM estimates WHERE id = ?`, e.ID); got != 0 {
		t.Errorf("ceiling != max + retry: %d", got)
	}
}

func TestEstimate_Deterministic(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	items := []generation.Item{newSeedItem(lt.TrackLuna, 3), {Mode: lineage.ModeFromDraft, DraftIDs: []string{lt.Draft1, lt.Draft2}, NSeeds: 2}}
	a := r.est(agent, items...)
	b := r.est(agent, items...)
	if a.ID == b.ID || a.Token == b.Token {
		t.Error("ids and tokens must differ between estimates")
	}
	if a.Cost != b.Cost || a.ETAS != b.ETAS || a.Takes != b.Takes {
		t.Errorf("costs differ: %+v / %+v", a.Cost, b.Cost)
	}
	var ia, ib string
	r.exec(`SELECT 1`)
	if err := r.st.R.QueryRow(`SELECT items_json FROM estimates WHERE id = ?`, a.ID).Scan(&ia); err != nil {
		t.Fatal(err)
	}
	if err := r.st.R.QueryRow(`SELECT items_json FROM estimates WHERE id = ?`, b.ID).Scan(&ib); err != nil {
		t.Fatal(err)
	}
	if ia != ib {
		t.Errorf("items_json differs:\n%s\n%s", ia, ib)
	}
	// the token is stored only as a hash, bound to the principal
	if n := r.count(`SELECT COUNT(*) FROM estimates WHERE token_hash = ?`, a.Token); n != 0 {
		t.Error("the token is stored in clear")
	}
	if got := r.str(`SELECT principal_id FROM estimates WHERE id = ?`, a.ID); got != agent.Key() {
		t.Errorf("principal = %s", got)
	}
}

// TestMutation_ReplayReproducesRender: Apply(parent, operator, params, patch) gives the child's render hash for every mode.
func TestMutation_ReplayReproducesRender(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	luna, citta := lt.TrackLuna, lt.TrackCitta
	lyrics := "[Verse 1]\nbrand new words\n[Chorus]\nbrand new hook"
	e := r.est(agent,
		newSeedItem(luna, 2),
		generation.Item{Mode: lineage.ModeSameSeedEdit, ParentTrackID: &luna},
		generation.Item{Mode: lineage.ModeLyricEdit, ParentTrackID: &luna, NSeeds: 2, Patch: &lineage.Patch{Lyrics: &lyrics}},
		generation.Item{Mode: lineage.ModeStyleEdit, ParentTrackID: &luna, Patch: &lineage.Patch{Style: sp("rock, 140 bpm"), CFGScale: lineage.OptNumber{Set: true, Value: 3}}},
		generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeLyricEdit, ParentTrackIDs: []string{luna, citta}, Patch: &lineage.Patch{Lyrics: &lyrics}},
	)
	itemPatch := []*lineage.Patch{nil, nil, {Lyrics: &lyrics}, {Style: sp("rock, 140 bpm"), CFGScale: lineage.OptNumber{Set: true, Value: 3}}, {Lyrics: &lyrics}}
	docs := r.items(e.ID)
	if len(docs) != 8 {
		t.Fatalf("%d takes, want 8", len(docs))
	}
	for _, d := range docs {
		parentRecipe := r.str(`SELECT r.request_json FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, *d.ParentTrackID)
		base, err := lineage.RequestOf([]byte(parentRecipe))
		if err != nil {
			t.Fatal(err)
		}
		params := lineage.Params{Mode: fmt.Sprint(d.Params["mode"]), Seed: d.Seed, ID: d.RequestID}
		got, err := lineage.ApplyParsed(base, d.Operator, params, itemPatch[d.ItemIndex])
		if err != nil {
			t.Fatalf("take %d: %v", d.TakeIndex, err)
		}
		if _, render := lineage.Keys(got.Canonical); render != d.RenderHash || string(got.Canonical) != d.RequestJSON {
			t.Errorf("take %d (%s): replay render %s, planned %s", d.TakeIndex, d.Operator, render, d.RenderHash)
		}
	}
}

// A learned factor stored under the worker's spelling of the GPU applies to the quote's class
// ("NVIDIA RTX A5000" vs the catalog's "RTX A5000"), and a take with cot=full is quoted with planning.
func TestEstimate_LearnedRTFApplies(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	base := r.est(agent, newSeedItem(lt.TrackLuna, 7)).Items[0].Takes[0]
	if base.ExpectedAudioS == 0 || base.TimeoutS <= spend.WorkerFinishMarginS+spend.UploadBudgetS {
		t.Fatalf("take %+v: no worker generation budget", base)
	}
	r.exec(`INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES ('NVIDIA GeForce RTX 3080', 3000, 5, '2026-10-15T20:00:00.000Z')`)
	if again := r.est(agent, newSeedItem(lt.TrackLuna, 7)).Items[0].Takes[0]; again.TimeoutS != base.TimeoutS {
		t.Errorf("a class outside the allowed set changed the estimate: %d -> %d", base.TimeoutS, again.TimeoutS)
	}
	r.exec(`INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES ('NVIDIA RTX A5000 ', 3000, 5, '2026-10-15T20:00:00.000Z')`)
	if slow := r.est(agent, newSeedItem(lt.TrackLuna, 7)).Items[0].Takes[0]; slow.TimeoutS <= base.TimeoutS {
		t.Errorf("learned 3000 did not raise the estimate: %d -> %d", base.TimeoutS, slow.TimeoutS)
	}
}
