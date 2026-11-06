package songpr_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// genRig wires the real generation.Service to the real songpr.Service as its PatchSource (what app/m5_songpr.go does).
func (r *rig) genRig() *generation.Service {
	r.t.Helper()
	now := func() time.Time { return lt.FixedNow }
	lib := library.New(r.st, nil, now)
	lib.UseIDFunc(r.id)
	sp := spend.New(spend.Options{Store: r.st, Now: now, NewID: r.id, ConfirmKey: []byte("test-key"), Location: time.UTC, Rand: rand.Reader})
	return generation.New(generation.Options{
		Store: r.st, Library: lib, Lineage: lineage.New(lineage.Options{Store: r.st, Now: now}), Spend: sp, Engines: registry.Default(registry.Options{}),
		Gate:   provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now),
		Notify: notify.Nop{}, PublicURL: "http://mm.example.com", Now: now, NewID: r.id, ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute,
		PatchSource: r.svc,
	})
}

func encodeStub(c generation.Created) (int, []byte, error) {
	b, err := json.Marshal(map[string]any{"id": c.BatchID})
	return 201, b, err
}

func songPRItem(patch string, n int) generation.Item {
	return generation.Item{Mode: lineage.ModeSongPR, PatchID: &patch, NSeeds: n}
}

func TestSongPR_GenerationRoundTrip(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	gen := r.genRig()
	ctx := context.Background()
	g2, cm := r.fx.Lineage[2], r.fx.Comments
	p := r.propose(agent, g2, []string{cm["fix_chorus"]}, replace(1, 0, "a brand new hook line"))
	est := func(who auth.Principal, items ...generation.Item) (generation.Estimate, error) {
		via := spend.ViaAPI
		if who.Kind == auth.KindHuman {
			via = spend.ViaHumanCreate
		}
		return gen.Estimate(ctx, who, generation.EstimateIn{Items: items, Via: via})
	}
	create := func(who auth.Principal, e generation.Estimate) (generation.Created, error) {
		via := spend.ViaAPI
		if who.Kind == auth.KindHuman {
			via = spend.ViaHumanCreate
		}
		return gen.Create(ctx, who, generation.CreateIn{Token: e.Token, Via: via, Rationale: "test"}, encodeStub)
	}

	// estimate: the base seed plus one new seed, the patch's operator, the patch applied, the parent filled in
	e, err := est(agent, songPRItem(p.ID, 2))
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if e.Takes != 2 || len(e.Items) != 1 || len(e.Items[0].Takes) != 2 {
		t.Fatalf("estimate = %+v", e.Items)
	}
	for i, tk := range e.Items[0].Takes {
		if tk.Operator != songpr.OperatorLyricEdit || tk.ParentTrackID == nil || *tk.ParentTrackID != g2 {
			t.Errorf("take %d = %+v", i, tk)
		}
	}
	if e.Items[0].Takes[0].Seed == e.Items[0].Takes[1].Seed {
		t.Error("the two takes share a seed")
	}
	for _, w := range e.Warnings {
		if w.Code == "comment_in_flight" {
			t.Errorf("unexpected warning %+v", w)
		}
	}
	c, err := create(agent, e)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := r.count(`SELECT COUNT(*) FROM jobs WHERE patch_id = ? AND batch_id = ?`, p.ID, c.BatchID); got != 2 {
		t.Fatalf("%d jobs carry the patch id", got)
	}
	var op, mut string
	if err := r.st.R.QueryRow(`SELECT operator, mutation_json FROM jobs WHERE patch_id = ? LIMIT 1`, p.ID).Scan(&op, &mut); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Operator string `json:"operator"`
		Params   struct {
			Mode    string `json:"mode"`
			Via     string `json:"via"`
			PatchID string `json:"patch_id"`
		} `json:"params"`
		Patch struct{ Lyrics string } `json:"recipe_patch"`
	}
	if err := json.Unmarshal([]byte(mut), &m); err != nil {
		t.Fatal(err)
	}
	if op != "lyric_edit" || m.Operator != "lyric_edit" || m.Params.Mode != "song_pr" || m.Params.Via != "song_pr" || m.Params.PatchID != p.ID || !strings.Contains(m.Patch.Lyrics, "a brand new hook line") {
		t.Errorf("job = %s %+v", op, m)
	}
	if n, err := r.svc.RenderCount(ctx, r.st.R, p.ID); err != nil || n != 1 {
		t.Errorf("RenderCount = %d %v", n, err)
	}
	got, err := r.svc.Get(ctx, human, p.ID)
	if err != nil || len(got.Renders) != 2 || got.Renders[0].BatchID != c.BatchID || got.Renders[0].ChildTrackID != nil {
		t.Errorf("renders = %+v %v", got.Renders, err)
	}

	// another patch on the same comment, while the first still renders: a warning, not a refusal
	q := r.propose(agent, g2, []string{cm["fix_chorus"]}, replace(1, 1, "a second idea"))
	e2, err := est(agent, songPRItem(q.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range e2.Warnings {
		warned = warned || w.Code == "comment_in_flight"
	}
	if !warned {
		t.Errorf("no comment_in_flight warning in %+v", e2.Warnings)
	}

	// the comments changed after the estimate: create refuses, the refusal rolls back, the token still works once the comment is back
	eh, err := est(human, songPRItem(p.ID, 1))
	if err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE comments SET hidden_at = '2026-10-06T00:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, cm["fix_chorus"])
	_, err = create(human, eh)
	pe := wantProblem(t, err, 409, "ESTIMATE_STALE")
	if pe.Details["reason"] != "comments_changed" {
		t.Errorf("details = %v", pe.Details)
	}
	if _, err := est(human, songPRItem(p.ID, 1)); err == nil {
		t.Error("an estimate for a patch with a hidden comment succeeded")
	}
	r.exec(`UPDATE comments SET hidden_at = NULL, hidden_by = NULL WHERE id = ?`, cm["fix_chorus"])
	// addressed (what the first child does to the comment) stays valid: the patch may render again
	r.exec(`UPDATE comments SET state = 'addressed', state_changed_at = '2026-10-06T00:00:00Z', state_changed_by = 'system:job' WHERE id = ?`, cm["fix_chorus"])
	if _, err := create(human, eh); err != nil {
		t.Fatalf("create after the comment came back: %v", err)
	}
	// resolved is final: the patch is stale
	r.exec(`UPDATE comments SET state = 'resolved', state_changed_by = 'human:fixture' WHERE id = ?`, cm["fix_chorus"])
	_, err = est(human, songPRItem(p.ID, 1))
	wantProblem(t, err, 409, "ESTIMATE_STALE")
	r.exec(`UPDATE comments SET state = 'open' WHERE id = ?`, cm["fix_chorus"])

	// the third render is allowed, the fourth is not (PatchMaxRenders), at estimate and at create
	e3, err := est(human, songPRItem(p.ID, 1))
	if err != nil {
		t.Fatalf("third estimate: %v", err)
	}
	e4, err := est(human, songPRItem(p.ID, 1)) // estimated while only two renders exist
	if err != nil {
		t.Fatalf("fourth estimate, early: %v", err)
	}
	if _, err := create(human, e3); err != nil {
		t.Fatalf("third render: %v", err)
	}
	_, err = create(human, e4)
	pe = wantProblem(t, err, 422, "PATCH_RENDER_LIMIT")
	if pe.Details["limit"] != float64(songpr.PatchMaxRenders) && pe.Details["limit"] != songpr.PatchMaxRenders {
		t.Errorf("details = %v", pe.Details)
	}
	_, err = est(human, songPRItem(p.ID, 1))
	wantProblem(t, err, 422, "PATCH_RENDER_LIMIT")
	if n, _ := r.svc.RenderCount(ctx, r.st.R, p.ID); n != songpr.PatchMaxRenders {
		t.Errorf("RenderCount = %d", n)
	}
	// a rejected or cancelled render does not use up the budget
	r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = '2026-10-06T00:00:00Z' WHERE batch_id = ?`, c.BatchID)
	if n, _ := r.svc.RenderCount(ctx, r.st.R, p.ID); n != songpr.PatchMaxRenders-1 {
		t.Errorf("RenderCount after a cancelled batch = %d", n)
	}
}

func TestSongPR_GenerationRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	gen := r.genRig()
	ctx := context.Background()
	g1, g2, cm := r.fx.Lineage[1], r.fx.Lineage[2], r.fx.Comments
	p := r.propose(human, g2, []string{cm["fix_chorus"]}, replace(1, 0, "a brand new hook line"))
	item := songPRItem(p.ID, 1)
	wrongParent := item
	wrongParent.ParentTrackID = &g1
	tooMany := songPRItem(p.ID, 5)
	unknown := songPRItem("no-such-patch", 1)
	noID := generation.Item{Mode: lineage.ModeSongPR}
	withPatch := item
	withPatch.Patch = &lineage.Patch{Style: sp("x")}
	idOnVariant := generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &g2, PatchID: &p.ID}
	for _, c := range []struct {
		name   string
		it     generation.Item
		status int
		code   string
	}{
		{"patch of another take", wrongParent, 422, "INVALID_ITEM"},
		{"five seeds", tooMany, 422, "INVALID_ITEM"},
		{"unknown patch", unknown, 404, "NOT_FOUND"},
		{"no patch id", noID, 422, "INVALID_ITEM"},
		{"item patch beside the stored one", withPatch, 422, "INVALID_ITEM"},
		{"patch_id on another mode", idOnVariant, 422, "INVALID_ITEM"},
	} {
		_, err := gen.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{c.it}, Via: spend.ViaHumanCreate})
		wantProblem(t, err, c.status, c.code)
	}
	// the same parent as the patch's base is fine
	same := item
	same.ParentTrackID = &g2
	if _, err := gen.Estimate(ctx, human, generation.EstimateIn{Items: []generation.Item{same}, Via: spend.ViaHumanCreate}); err != nil {
		t.Errorf("explicit base parent: %v", err)
	}
}

func TestSongPR_LimitsMirrorConstants(t *testing.T) {
	t.Parallel()
	if generation.PatchMaxRenders != songpr.PatchMaxRenders || generation.MaxSongPRSeeds != songpr.MaxSeeds {
		t.Errorf("generation %d/%d, songpr %d/%d", generation.PatchMaxRenders, generation.MaxSongPRSeeds, songpr.PatchMaxRenders, songpr.MaxSeeds)
	}
}

func TestSongPR_RecheckAndSourceErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	_, err := r.svc.PatchForEstimate(ctx, r.st.R, "no-such-patch")
	wantProblem(t, err, 404, "NOT_FOUND")
	p := r.propose(human, r.fx.Lineage[0], []string{r.fx.Comments["fix_chorus"], r.fx.Comments["fix_verse"]}, replace(1, 0, "x1"), replace(0, 0, "x2"))
	row, err := r.svc.PatchForEstimate(ctx, r.st.R, p.ID)
	if err != nil || row.BaseTrackID != r.fx.Lineage[0] || row.Operator != "lyric_edit" || row.Lyrics == nil || row.Style != nil || len(row.CommentIDs) != 2 {
		t.Errorf("row = %+v %v", row, err)
	}
	if err := r.svc.RecheckComments(ctx, r.st.R, p.ID); err != nil {
		t.Errorf("RecheckComments on fresh comments: %v", err)
	}
	// a note that became a keep is no longer addressable: kind is frozen in practice, so exercise hidden on one of two and name it
	r.exec(`UPDATE comments SET hidden_at = '2026-10-06T00:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, r.fx.Comments["fix_verse"])
	pe := wantProblem(t, r.svc.RecheckComments(ctx, r.st.R, p.ID), 409, "ESTIMATE_STALE")
	if ids, _ := pe.Details["comment_ids"].([]string); len(ids) != 1 || ids[0] != r.fx.Comments["fix_verse"] {
		t.Errorf("details = %v", pe.Details)
	}
}
