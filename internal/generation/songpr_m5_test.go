package generation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// fakePatches is a generation.PatchSource over one in-memory patch, so these tests need no songpr (generation never imports it).
type fakePatches struct {
	mu      sync.Mutex
	row     generation.PatchRow
	stale   bool
	renders int
	calls   int
	err     error // returned by every method when set
}

func (f *fakePatches) PatchForEstimate(_ context.Context, _ lineage.Querier, id string) (generation.PatchRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return generation.PatchRow{}, f.err
	}
	if id != f.row.ID {
		return generation.PatchRow{}, problem.E(404, "NOT_FOUND", "no such resource", nil)
	}
	return f.row, nil
}

func (f *fakePatches) RecheckComments(context.Context, lineage.Querier, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.stale {
		return problem.E(409, "ESTIMATE_STALE", "comments changed", map[string]any{"reason": "comments_changed"})
	}
	return f.err
}

func (f *fakePatches) RenderCount(context.Context, lineage.Querier, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renders, f.err
}

type fakeGate struct{ pending, limit int }

func (g *fakeGate) Backlog(context.Context, lineage.Querier, string) (int, error) {
	return g.pending, nil
}
func (g *fakeGate) Limit() int { return g.limit }

const spPatchID = "01900000-0000-7000-8000-00000000a001"

// songPRRig is a rig whose service reads fake; the patches row exists so jobs.patch_id satisfies its foreign key.
func songPRRig(t *testing.T) (*rig, *generation.Service, *fakePatches) {
	t.Helper()
	r := newRig(t)
	f := &fakePatches{row: generation.PatchRow{ID: spPatchID, BaseTrackID: lt.TrackLuna, Operator: "style_edit", Style: sp("rock, 140 bpm"), CommentIDs: nil}}
	r.exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?, ?, 'h', 'style_edit', '[]', '{}', '{}', '{}', 'r', 'agent', 'agent:fixture-token', 'a', '2026-10-06T00:00:00Z')`, spPatchID, lt.TrackLuna)
	o := r.opts
	o.PatchSource = f
	return r, generation.New(o), f
}

// spTakeDoc is the part of items_json a song_pr take adds to takeDoc.
type spTakeDoc struct {
	takeDoc
	PatchID             *string  `json:"patch_id"`
	AddressesCommentIDs []string `json:"addresses_comment_ids"`
}

func spItem(n int) generation.Item {
	id := spPatchID
	return generation.Item{Mode: lineage.ModeSongPR, PatchID: &id, NSeeds: n}
}

func estimateWith(t *testing.T, svc *generation.Service, p auth.Principal, items ...generation.Item) (generation.Estimate, error) {
	t.Helper()
	return svc.Estimate(context.Background(), p, generation.EstimateIn{Items: items, Via: viaFor(p)})
}

// TestGeneration_SongPRMode_Table: the item rules of the song_pr mode (C253.4) and what a valid item expands to.
func TestGeneration_SongPRMode_Table(t *testing.T) {
	t.Parallel()
	r, svc, f := songPRRig(t)
	luna, citta := lt.TrackLuna, lt.TrackCitta
	id := spPatchID
	for _, c := range []struct {
		name   string
		it     generation.Item
		status int
		code   string
		rule   string
	}{
		{"no patch id", generation.Item{Mode: lineage.ModeSongPR}, 422, "INVALID_ITEM", "required"},
		{"empty patch id", generation.Item{Mode: lineage.ModeSongPR, PatchID: sp("")}, 422, "INVALID_ITEM", "required"},
		{"an item patch beside the stored one", func() generation.Item { i := spItem(1); i.Patch = &lineage.Patch{Style: sp("x")}; return i }(), 422, "INVALID_ITEM", "patch_forbidden"},
		{"five seeds", spItem(5), 422, "INVALID_ITEM", "song_pr_at_most_4"},
		{"five explicit seeds", func() generation.Item { i := spItem(1); i.NSeeds = 0; i.Seeds = []int64{1, 2, 3, 4, 5}; return i }(), 422, "INVALID_ITEM", "song_pr_at_most_4"},
		{"another take than the patch's base", func() generation.Item { i := spItem(1); i.ParentTrackID = &citta; return i }(), 422, "INVALID_ITEM", "not_the_patch_base"},
		{"patch_id on new_seed", generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &luna, PatchID: &id}, 422, "INVALID_ITEM", "not_allowed_for_new_seed"},
		{"patch_id on a template", generation.Item{Mode: lineage.ModeFromTemplate, TemplateMode: lineage.ModeNewSeed, ParentTrackIDs: []string{luna, citta}, PatchID: &id}, 422, "INVALID_ITEM", "not_allowed_for_from_template"},
	} {
		_, err := estimateWith(t, svc, human, c.it)
		pe := wantProblem(t, err, c.status, c.code)
		if got, _ := pe.Details["rule"].(string); got != c.rule {
			t.Errorf("%s: rule = %q, want %q", c.name, got, c.rule)
		}
	}
	// a valid item: the patch's base is the parent, the patch's operator the take's, the base seed plus new seeds, the patch applied
	e, err := estimateWith(t, svc, agent, spItem(3))
	if err != nil {
		t.Fatal(err)
	}
	var docs []spTakeDoc
	if err := json.Unmarshal([]byte(r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID)), &docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("%d takes", len(docs))
	}
	for i, d := range docs {
		if d.Operator != "style_edit" || d.Mode != "song_pr" || d.ParentTrackID == nil || *d.ParentTrackID != luna || d.Params["via"] != "song_pr" || d.Params["patch_id"] != spPatchID {
			t.Errorf("take %d = %+v", i, d)
		}
		if d.PatchID == nil || *d.PatchID != spPatchID || !strings.Contains(d.RequestJSON, `"style":"rock, 140 bpm"`) {
			t.Errorf("take %d patch/request: %v %s", i, d.PatchID, d.RequestJSON)
		}
	}
	if docs[0].Seed != 101 || docs[1].Seed == 101 || docs[2].Seed == 101 {
		t.Errorf("seeds = %d %d %d, want the base seed first", docs[0].Seed, docs[1].Seed, docs[2].Seed)
	}
	if f.calls == 0 {
		t.Error("the estimate never re-checked the comments")
	}
	// the same base given explicitly is fine
	it := spItem(1)
	it.ParentTrackID = &luna
	if _, err := estimateWith(t, svc, human, it); err != nil {
		t.Errorf("explicit base: %v", err)
	}
	// create sets jobs.patch_id on every job
	c, err := svc.Create(context.Background(), agent, generation.CreateIn{Token: e.Token, Via: spend.ViaAPI, Rationale: "test"}, encode)
	if err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND patch_id = ?`, c.BatchID, spPatchID); n != 3 {
		t.Errorf("%d jobs carry the patch id", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM jobs WHERE patch_id IS NULL AND batch_id = ?`, c.BatchID); n != 0 {
		t.Errorf("%d jobs lack it", n)
	}
	// a job's patch id is frozen
	if _, err := r.st.W.Exec(`UPDATE jobs SET patch_id = NULL WHERE batch_id = ?`, c.BatchID); err == nil {
		t.Error("jobs.patch_id was updated")
	}
}

// TestGeneration_SongPRNoSource: without a PatchSource the mode is unavailable (503 UNAVAILABLE songpr), other modes are not.
func TestGeneration_SongPRNoSource(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := estimateWith(t, r.svc, human, spItem(1))
	pe := wantProblem(t, err, 503, "UNAVAILABLE")
	if pe.Details["component"] != "songpr" {
		t.Errorf("details = %v", pe.Details)
	}
	if _, err := estimateWith(t, r.svc, human, newSeedItem(lt.TrackLuna, 1)); err != nil {
		t.Errorf("new_seed without a source: %v", err)
	}
	// a source that is not wired yet (the late delegate answers ErrUnsupported) is the same refusal
	o := r.opts
	o.PatchSource = &fakePatches{err: errors.ErrUnsupported}
	_, err = estimateWith(t, generation.New(o), human, spItem(1))
	wantProblem(t, err, 503, "UNAVAILABLE")
}

func TestEstimate_SongPR_StaleComments(t *testing.T) {
	t.Parallel()
	r, svc, f := songPRRig(t)
	batches0 := r.count(`SELECT COUNT(*) FROM batches`)
	e, err := estimateWith(t, svc, human, spItem(1))
	if err != nil {
		t.Fatal(err)
	}
	f.stale = true
	// create refuses and the refusal rolls the token back
	_, err = svc.Create(context.Background(), human, generation.CreateIn{Token: e.Token, Via: spend.ViaHumanCreate}, encode)
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "comments_changed" {
		t.Errorf("details = %v", pe.Details)
	}
	if n := r.count(`SELECT COUNT(*) FROM batches`) - batches0; n != 0 {
		t.Errorf("a stale create wrote %d batches", n)
	}
	// so does a fresh estimate
	_, err = estimateWith(t, svc, human, spItem(1))
	wantProblem(t, err, 409, "ESTIMATE_STALE")
	f.stale = false
	if _, err := svc.Create(context.Background(), human, generation.CreateIn{Token: e.Token, Via: spend.ViaHumanCreate}, encode); err != nil {
		t.Errorf("the token should still work once the comments are back: %v", err)
	}
}

func TestCreate_SongPR_RenderLimit(t *testing.T) {
	t.Parallel()
	r, svc, f := songPRRig(t)
	batches0 := r.count(`SELECT COUNT(*) FROM batches`)
	f.renders = generation.PatchMaxRenders - 1
	e, err := estimateWith(t, svc, human, spItem(1))
	if err != nil {
		t.Fatalf("the last allowed render: %v", err)
	}
	f.renders = generation.PatchMaxRenders
	_, err = svc.Create(context.Background(), human, generation.CreateIn{Token: e.Token, Via: spend.ViaHumanCreate}, encode)
	pe := wantProblem(t, err, 422, "PATCH_RENDER_LIMIT")
	if pe.Details["patch_id"] != spPatchID || pe.Details["limit"] != generation.PatchMaxRenders {
		t.Errorf("details = %v", pe.Details)
	}
	if n := r.count(`SELECT COUNT(*) FROM batches`) - batches0; n != 0 {
		t.Errorf("%d batches", n)
	}
	_, err = estimateWith(t, svc, human, spItem(1))
	wantProblem(t, err, 422, "PATCH_RENDER_LIMIT")
}

// TestEstimate_Backlog: the callback backlog paces agents only, per lineage root, at the limit; 0 switches it off (C255.5).
func TestEstimate_Backlog(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	batches0 := r.count(`SELECT COUNT(*) FROM batches`)
	gate := &fakeGate{pending: 6, limit: 6}
	o := r.opts
	o.BacklogGate = gate
	svc := generation.New(o)
	luna := lt.TrackLuna
	root := r.str(`SELECT root_track_id FROM tracks WHERE id = ?`, luna)
	_, err := estimateWith(t, svc, agent, newSeedItem(luna, 1))
	pe := wantProblem(t, err, 409, "CALLBACKS_BACKLOG")
	if pe.Details["root_track_id"] != root || pe.Details["pending"] != 6 || pe.Details["limit"] != 6 {
		t.Errorf("details = %v", pe.Details)
	}
	if _, err := estimateWith(t, svc, human, newSeedItem(luna, 1)); err != nil {
		t.Errorf("a human is exempt: %v", err)
	}
	gate.pending = 5
	e, err := estimateWith(t, svc, agent, newSeedItem(luna, 1))
	if err != nil {
		t.Fatalf("below the limit: %v", err)
	}
	// the backlog grew after the estimate: create refuses and rolls back
	gate.pending = 7
	_, err = svc.Create(context.Background(), agent, generation.CreateIn{Token: e.Token, Via: spend.ViaAPI}, encode)
	wantProblem(t, err, 409, "CALLBACKS_BACKLOG")
	if n := r.count(`SELECT COUNT(*) FROM batches`) - batches0; n != 0 {
		t.Errorf("%d batches", n)
	}
	gate.limit = 0 // off
	if _, err := estimateWith(t, svc, agent, newSeedItem(luna, 1)); err != nil {
		t.Errorf("gate off: %v", err)
	}
	// an unwired gate never refuses
	if _, err := estimateWith(t, r.svc, agent, newSeedItem(luna, 1)); err != nil {
		t.Errorf("no gate: %v", err)
	}
}
