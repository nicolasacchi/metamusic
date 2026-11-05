package duels_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
)

func pairSet(ps duels.PairSuggestions) map[[2]string]duels.PairSuggestion {
	out := map[[2]string]duels.PairSuggestion{}
	for _, p := range ps.Pairs {
		a, b := p.ATrackID, p.BTrackID
		if a > b {
			a, b = b, a
		}
		out[[2]string{a, b}] = p
	}
	return out
}

func key(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

func TestSuggest_Lineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newM5Env(t, false)
	g0, g1, g2 := e.info.Lineage[0], e.info.Lineage[1], e.info.Lineage[2]
	res, err := e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, K: 6})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scope != "lineage" || res.RootTrackID == nil || *res.RootTrackID != g0 || res.Section != nil || len(res.Pairs) != 3 {
		t.Fatalf("result = %+v", res)
	}
	for _, p := range res.Pairs {
		if p.ATitle == "" || p.BTitle == "" || p.Score <= 0 || p.PAWinsB <= 0 || p.PAWinsB >= 1 || p.HumanVerdict != 0 {
			t.Errorf("pair = %+v", p)
		}
	}
	// an open duel removes its pair
	e.section(t, human, g0, g1, lbl("chorus 1"))
	res, _ = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, K: 6})
	ps := pairSet(res)
	if _, ok := ps[key(g0, g1)]; ok || len(ps) != 2 {
		t.Errorf("pairs with an open duel = %v", ps)
	}
	// three human verdicts settle a pair; the verdicts shape the model
	for i := 0; i < 3; i++ {
		d := e.create(t, human, g1, g2, true)
		e.win(t, human, d.ID, g1)
	}
	res, _ = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, K: 6})
	ps = pairSet(res)
	if _, ok := ps[key(g1, g2)]; ok || len(ps) != 1 || res.Model.VerdictsCounted != 3 {
		t.Errorf("after 3 verdicts: pairs %v, model %+v", ps, res.Model)
	}
	// the one left is G0 vs G2; k caps, scores never rise down the list
	res, _ = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, K: 1})
	if len(res.Pairs) != 1 {
		t.Errorf("k=1 returned %d", len(res.Pairs))
	}
	// critic verdicts never count
	d := e.create(t, agent, g0, g2, true)
	e.judge(t, agent, d.ID, "a")
	res2, _ := e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0})
	if res2.Model.VerdictsCounted != 3 {
		t.Errorf("critic verdict counted: %+v", res2.Model)
	}
}

func TestSuggest_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newM5Env(t, false)
	_, err := e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage"})
	asProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: "nope"})
	asProblem(t, err, 404, "NOT_FOUND")
	_, err = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "weird"})
	asProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: e.info.Lineage[0], Section: &duels.SectionSelector{Kind: sp("chorus")}})
	asProblem(t, err, 422, "VALIDATION_FAILED")
}

func TestSuggest_Section(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newM5Env(t, false)
	g0, g1 := e.info.Lineage[0], e.info.Lineage[1]
	// G2 is lyrics-only: only the pair of the two timed takes remains, with the section echoed
	res, err := e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, Section: &duels.SectionSelector{Label: sp("Chorus 1")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Section == nil || *res.Section != "Chorus 1" || len(res.Pairs) != 1 || key(res.Pairs[0].ATrackID, res.Pairs[0].BTrackID) != key(g0, g1) {
		t.Fatalf("section pairs = %+v", res)
	}
	res, _ = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, Section: &duels.SectionSelector{Kind: sp("chorus"), Ordinal: sp(2)}})
	if res.Section == nil || *res.Section != "chorus 2" || len(res.Pairs) != 1 {
		t.Errorf("kind/ordinal = %+v", res)
	}
	// no track has a bridge: empty, not an error
	res, err = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, Section: &duels.SectionSelector{Label: sp("bridge")}})
	if err != nil || len(res.Pairs) != 0 || res.Pairs == nil {
		t.Errorf("bridge = %+v, %v", res, err)
	}
	// every suggested pair can be created as a section duel
	res, _ = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "lineage", RootTrackID: g0, Section: &duels.SectionSelector{Label: sp("outro")}})
	for _, p := range res.Pairs {
		e.section(t, human, p.ATrackID, p.BTrackID, lbl("outro"))
	}
}

func TestSuggest_Global(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newM5Env(t, false)
	res, err := e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "global", K: 15})
	if err != nil {
		t.Fatal(err)
	}
	if res.RootTrackID != nil || res.Section != nil || len(res.Pairs) == 0 || len(res.Pairs) > 15 {
		t.Fatalf("global = %+v", res)
	}
	// Alpha and Beta have an open duel: that pair is absent; every track named has a rating or a duel
	if _, ok := pairSet(res)[key(duelstest.TrackAlpha, duelstest.TrackBeta)]; ok {
		t.Error("pair with an open duel suggested")
	}
	for _, p := range res.Pairs {
		var n int
		if err := e.st.R.QueryRow(`SELECT (SELECT count(*) FROM ratings WHERE source = 'human' AND stars IS NOT NULL AND track_id IN (?1, ?2))
			+ (SELECT count(*) FROM duels WHERE slot_a_track_id IN (?1, ?2) OR slot_b_track_id IN (?1, ?2))`, p.ATrackID, p.BTrackID).Scan(&n); err != nil || n == 0 {
			t.Errorf("pair %s/%s has neither rating nor duel", p.ATrackID, p.BTrackID)
		}
	}
	// a reference original and a multi-part import can be in a global suggestion; a section request skips them
	res, err = e.svc.Suggest(ctx, duels.SuggestQuery{Scope: "global", Section: &duels.SectionSelector{Label: sp("chorus 1")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Pairs {
		for _, id := range []string{p.ATrackID, p.BTrackID} {
			if id == e.info.Originals[0] || id == e.info.Groups[0][0] || id == e.info.Lineage[2] {
				t.Errorf("untimed track %s suggested for a section", id)
			}
		}
	}
}
