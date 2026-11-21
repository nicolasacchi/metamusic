package nightshift

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/mandates"
)

func ip(i int) *int         { return &i }
func fp(f float64) *float64 { return &f }
func scope12() mandates.Scope { // a scope that filters nothing by itself
	return mandates.Scope{Modes: []string{"new_seed", "style_edit"}, Engines: []string{"yue2"}, MinStars: 3,
		MaxTakesPerLineage: 3, MaxTakesPerBatch: 12, MaxBatchCeilingMicroUSD: 943_587, MaxBatchesPerDay: 1}
}

func lin(root string, stars int, style string) Lineage {
	return Lineage{RootID: root, BestTrackID: root + "-best", Engine: "yue2", Style: style, BestStars: ip(stars)}
}

func baseInput(ls ...Lineage) Input {
	return Input{Lineages: ls, Scope: scope12(), MaxTakes: 6, MinStars: 4, ExplorePPM: 300_000, Seed: NightSeed("2026-11-21")}
}

// seedWith finds a seed whose BPM coin for root is plus (or minus).
func seedWith(t *testing.T, root string, plus bool) uint64 {
	t.Helper()
	for s := uint64(1); s < 1000; s++ {
		if coinPlus(s, root) == plus {
			return s
		}
	}
	t.Fatal("no seed")
	return 0
}

func roots(p []Planned) []string {
	var out []string
	for _, x := range p {
		out = append(out, x.Kind+":"+x.RootID)
	}
	return out
}

func newSeedItem(best string) generation.Item {
	return generation.Item{Mode: "new_seed", ParentTrackID: &best, NSeeds: 1}
}

func TestPlan_ExactPlan(t *testing.T) {
	t.Parallel()
	a := lin("a", 5, "pop")
	a.U, a.SD, a.NDuels = fp(2.0), fp(0.4), 5
	b := lin("b", 5, "rock")
	b.U, b.SD, b.NDuels = fp(1.0), fp(0.5), 4
	c := lin("c", 4, "pop, 100 BPM")
	d := lin("d", 3, "folk")
	d.SD = fp(0.2)
	e := lin("e", 2, "jazz") // below the mandate's min stars
	in := baseInput(e, d, c, b, a)
	in.MaxTakes, in.Seed = 3, seedWith(t, "c", true)

	planned, dropped := Plan(in)
	style := "pop, 108 BPM"
	cbest := "c-best"
	want := []Planned{
		{Kind: "exploit", RootID: "a", Item: newSeedItem("a-best"), Reason: "exploit: new seed, 5 stars, u 2.00 over 5 duels"},
		{Kind: "exploit", RootID: "b", Item: newSeedItem("b-best"), Reason: "exploit: new seed, 5 stars, u 1.00 over 4 duels"},
		{Kind: "explore", RootID: "c", Item: generation.Item{Mode: "style_edit", ParentTrackID: &cbest, NSeeds: 1, Patch: &generation.Patch{Style: &style}},
			Reason: "explore: style edit, bpm 100 to 108"},
	}
	if diff := cmp.Diff(want, planned); diff != "" {
		t.Errorf("plan (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]Dropped{{RootID: "e", Reason: "min_stars"}}, dropped); diff != "" {
		t.Errorf("dropped (-want +got):\n%s", diff)
	}
}

func TestPlan_ExploreExploitSplit(t *testing.T) {
	t.Parallel()
	var ls []Lineage
	for i := 0; i < 12; i++ {
		ls = append(ls, lin(fmt.Sprintf("r%02d", i), 5, "pop"))
	}
	for _, c := range []struct {
		n       int
		ppm     int64
		explore int
		exploit int
	}{{8, 300_000, 2, 6}, {5, 300_000, 2, 3}, {1, 300_000, 0, 1}, {8, 0, 0, 8}, {12, 500_000, 6, 6}, {2, 300_000, 1, 1}} {
		in := baseInput(ls...)
		in.MaxTakes, in.ExplorePPM = c.n, c.ppm
		planned, _ := Plan(in)
		var xr, xp int
		for _, p := range planned {
			if p.Kind == "explore" {
				xr++
			} else {
				xp++
			}
		}
		if xr != c.explore || xp != c.exploit {
			t.Errorf("N=%d ppm=%d: explore %d exploit %d, want %d and %d", c.n, c.ppm, xr, xp, c.explore, c.exploit)
		}
		if got := min(c.n, 12); len(planned) != got {
			t.Errorf("N=%d: %d takes", c.n, len(planned))
		}
	}
}

func TestPlan_MaxTakesBoundedByScope(t *testing.T) {
	t.Parallel()
	var ls []Lineage
	for i := 0; i < 10; i++ {
		ls = append(ls, lin(fmt.Sprintf("r%02d", i), 5, "pop"))
	}
	in := baseInput(ls...)
	in.MaxTakes = 8
	in.Scope.MaxTakesPerBatch = 4
	if p, _ := Plan(in); len(p) != 4 {
		t.Errorf("got %d takes, want 4", len(p))
	}
	in.MaxTakes = 0
	if p, d := Plan(in); len(p) != 0 || len(d) != 0 {
		t.Errorf("zero takes: %v %v", p, d)
	}
}

func TestPlan_OnePerLineage(t *testing.T) {
	t.Parallel()
	var ls []Lineage
	for i := 0; i < 5; i++ {
		l := lin(fmt.Sprintf("r%d", i), 5, "pop, 100 BPM")
		ls = append(ls, l)
	}
	in := baseInput(ls...)
	in.MaxTakes, in.ExplorePPM = 12, 500_000
	planned, _ := Plan(in)
	if len(planned) != 5 {
		t.Fatalf("5 lineages must give 5 takes, got %v", roots(planned))
	}
	seen := map[string]bool{}
	for _, p := range planned {
		if seen[p.RootID] {
			t.Errorf("root %s planned twice", p.RootID)
		}
		seen[p.RootID] = true
		if p.Item.NSeeds != 1 || p.Item.ParentTrackID == nil || *p.Item.ParentTrackID != p.RootID+"-best" {
			t.Errorf("item %+v", p.Item)
		}
	}
}

func TestPlan_FiltersToMandateScope(t *testing.T) {
	t.Parallel()
	mk := func(root string, mod func(*Lineage)) Lineage {
		l := lin(root, 5, "pop")
		if mod != nil {
			mod(&l)
		}
		return l
	}
	for _, c := range []struct {
		name   string
		l      Lineage
		scope  func(*mandates.Scope)
		byRoot map[string]int
		reason string
	}{
		{"engine", mk("x", func(l *Lineage) { l.Engine = "acestep15" }), nil, nil, "engine"},
		{"unrated", mk("x", func(l *Lineage) { l.BestStars = nil }), nil, nil, "min_stars"},
		{"stars below", mk("x", func(l *Lineage) { l.BestStars = ip(2) }), nil, nil, "min_stars"},
		{"root filter", mk("x", nil), func(s *mandates.Scope) { s.RootTrackIDs = []string{"ok"} }, nil, "root_filter"},
		{"per lineage", mk("x", nil), nil, map[string]int{"x": 3}, "per_lineage"},
		{"backlogged", mk("x", func(l *Lineage) { l.Backlogged = true }), nil, nil, "backlogged"},
		{"recent unjudged", mk("x", func(l *Lineage) { l.RecentUnjudged = true }), nil, nil, "recent_unjudged"},
		{"no mode", mk("x", nil), func(s *mandates.Scope) { s.Modes = nil }, nil, "mode"},
		{"engine before stars", mk("x", func(l *Lineage) { l.Engine = "z"; l.BestStars = nil }), nil, nil, "engine"},
		{"stars before backlog", mk("x", func(l *Lineage) { l.BestStars = ip(1); l.Backlogged = true }), nil, nil, "min_stars"},
	} {
		in := baseInput(c.l, lin("ok", 5, "pop"))
		if c.scope != nil {
			c.scope(&in.Scope)
		}
		in.ByRoot = c.byRoot
		planned, dropped := Plan(in)
		if diff := cmp.Diff([]Dropped{{RootID: "x", Reason: c.reason}}, dropped); diff != "" && c.reason != "mode" {
			t.Errorf("%s: dropped (-want +got):\n%s", c.name, diff)
		}
		for _, p := range planned {
			if p.RootID == "x" {
				t.Errorf("%s: filtered lineage planned", c.name)
			}
		}
	}
	// boundary: ByRoot 2 of 3 still fits, a root in the filter list passes
	in := baseInput(lin("x", 5, "pop"))
	in.ByRoot = map[string]int{"x": 2}
	in.Scope.RootTrackIDs = []string{"x"}
	if p, d := Plan(in); len(p) != 1 || len(d) != 0 {
		t.Errorf("boundary: %v %v", p, d)
	}
}

func TestPlan_SkipsRecentUnjudged(t *testing.T) {
	t.Parallel()
	a, b := lin("a", 5, "pop"), lin("b", 5, "pop")
	a.RecentUnjudged = true
	planned, dropped := Plan(baseInput(a, b))
	if diff := cmp.Diff([]string{"exploit:b"}, roots(planned)); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff([]Dropped{{"a", "recent_unjudged"}}, dropped); diff != "" {
		t.Error(diff)
	}
}

func TestPlan_SkipsBacklogged(t *testing.T) {
	t.Parallel()
	a, b := lin("a", 5, "pop"), lin("b", 5, "pop")
	b.Backlogged = true
	planned, dropped := Plan(baseInput(a, b))
	if diff := cmp.Diff([]string{"exploit:a"}, roots(planned)); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff([]Dropped{{"b", "backlogged"}}, dropped); diff != "" {
		t.Error(diff)
	}
}

func TestPlan_LeftoverSlots(t *testing.T) {
	t.Parallel()
	// exploit pool short: nothing reaches MinStars 4, six slots, so exploit leftovers go to explore (stars 3 with MinStars-1)
	var low []Lineage
	for i := 0; i < 4; i++ {
		low = append(low, lin(fmt.Sprintf("l%d", i), 3, "pop"))
	}
	in := baseInput(low...)
	planned, _ := Plan(in)
	if len(planned) != 4 {
		t.Fatalf("exploit empty: %v", roots(planned))
	}
	for _, p := range planned {
		if p.Kind != "explore" {
			t.Errorf("%s should be explore", p.RootID)
		}
	}
	// explore pool short: all candidates are exploit-grade, N 6 and 6 lineages; explore takes 2 of them, no slot is lost
	var high []Lineage
	for i := 0; i < 6; i++ {
		high = append(high, lin(fmt.Sprintf("h%d", i), 5, "pop"))
	}
	in = baseInput(high...)
	in.MaxTakes = 6
	planned, _ = Plan(in)
	if len(planned) != 6 {
		t.Errorf("full night: %v", roots(planned))
	}
	// an explore pool that cannot fill its slots leaves them to exploit: exploit-grade lineages with narrow ladders (SD 0.3, 5 stars
	// qualify by stars), a MinStars 5 and explore floor 4 would admit them too, so use stars 5 against a scope MinStars of 5
	in = baseInput(high[:3]...)
	in.MaxTakes, in.ExplorePPM = 6, 500_000 // 3 explore, 3 exploit wanted, only 3 lineages exist
	planned, _ = Plan(in)
	if len(planned) != 3 {
		t.Errorf("three lineages: %v", roots(planned))
	}
}

func TestPlan_ExploreRulesAndExploitLeftover(t *testing.T) {
	t.Parallel()
	// stars 3 with MinStars 5 is below the explore floor (4) and has a narrow ladder: not an explore candidate
	narrow := lin("narrow", 3, "pop")
	narrow.U, narrow.SD, narrow.NDuels = fp(0.1), fp(0.3), 6
	wide := lin("wide", 3, "pop")
	wide.U, wide.SD, wide.NDuels = fp(0.1), fp(1.2), 6
	in := baseInput(narrow, wide)
	in.MinStars = 5
	planned, _ := Plan(in)
	if diff := cmp.Diff([]string{"explore:wide"}, roots(planned)); diff != "" {
		t.Errorf("explore rule:\n%s", diff)
	}
	// exploit-grade candidates fill unfilled explore slots: 1 lineage exploit-grade, N 2, explore wants 1, exploit wants 1
	only := baseInput(lin("only", 5, "pop"))
	only.MaxTakes, only.ExplorePPM = 2, 500_000
	planned, _ = Plan(only)
	if len(planned) != 1 {
		t.Errorf("one lineage fills one slot: %v", roots(planned))
	}
}

func TestPlan_ExploitRanking(t *testing.T) {
	t.Parallel()
	u := func(root string, stars int, uu float64, n int) Lineage {
		l := lin(root, stars, "pop")
		l.U, l.SD, l.NDuels = fp(uu), fp(0.3), n
		return l
	}
	ls := []Lineage{
		u("fewduels", 5, 9.0, 2), // U ignored below 3 duels: ranks by stars among the unmeasured
		u("low-u", 5, 0.5, 3),    // measured, low U
		u("high-u", 4, 1.5, 10),  // measured, high U: first despite fewer stars
		lin("stars4", 4, "pop"),  // unmeasured
		lin("stars5", 5, "pop"),  // unmeasured
	}
	in := baseInput(ls...)
	in.MaxTakes, in.ExplorePPM = 5, 0
	planned, _ := Plan(in)
	want := []string{"exploit:high-u", "exploit:low-u"}
	got := roots(planned)
	if diff := cmp.Diff(want, got[:2]); diff != "" {
		t.Errorf("measured first by U:\n%s", diff)
	}
	// then the unmeasured by stars: the two 5-star ones (any order between them is the seeded hash), the 4-star last
	if got[4] != "exploit:stars4" {
		t.Errorf("order %v", got)
	}
}

func TestPlan_BPMShift(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		style string
		plus  bool
		want  string
	}{
		{"pop, 100 BPM", true, "pop, 108 BPM"},
		{"pop, 100 BPM", false, "pop, 92 BPM"},
		{"pop, 176 BPM", true, "pop, 168 BPM"}, // 184 leaves 60..180: flipped
		{"pop, 64 BPM", false, "pop, 72 BPM"},  // 56 leaves 60..180: flipped
		{"BPM 90; rock", true, "BPM 98; rock"},
	} {
		l := lin("r", 5, c.style)
		in := baseInput(l)
		in.MaxTakes, in.ExplorePPM = 1, 1_000_000 // N 1 and 100 %: (1*1e6+5e5)/1e6 = 1 explore
		in.Seed = seedWith(t, "r", c.plus)
		planned, _ := Plan(in)
		if len(planned) != 1 || planned[0].Kind != "explore" || planned[0].Item.Mode != "style_edit" {
			t.Fatalf("%s: %+v", c.style, planned)
		}
		got := *planned[0].Item.Patch.Style
		if got != c.want {
			t.Errorf("%s plus=%v: %q, want %q", c.style, c.plus, got, c.want)
		}
		lintClean(t, got)
	}
	// both directions out of range, or no BPM term: a new seed instead
	for _, style := range []string{"pop, 40 BPM", "pop, 200 BPM", "pop, rock"} {
		in := baseInput(lin("r", 5, style))
		in.MaxTakes, in.ExplorePPM = 1, 1_000_000
		planned, _ := Plan(in)
		if len(planned) != 1 || planned[0].Item.Mode != "new_seed" || planned[0].Item.Patch != nil {
			t.Errorf("%s: %+v", style, planned)
		}
	}
}

func lintClean(t *testing.T, style string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": "n1", "style": style, "lyrics": "[Verse]\nlano pera\nmira dosa\nvenu tilo\noro kala\n", "cot": "full", "seed": 7})
	rep := yue2.Lint([]yue2.LintItem{{Request: raw}}, true, yue2.LintContext{})
	if len(rep.Items[0].Errors) != 0 {
		t.Errorf("style %q: lint errors %+v", style, rep.Items[0].Errors)
	}
	if _, bpm := yue2.StyleTerms(style); bpm < 60 || bpm > 180 {
		t.Errorf("style %q: bpm %d outside 60..180", style, bpm)
	}
}

func TestPlan_ModesOfTheMandate(t *testing.T) {
	t.Parallel()
	explore := func(scopeModes []string, style string) ([]Planned, []Dropped) {
		in := baseInput(lin("r", 5, style))
		in.MaxTakes, in.ExplorePPM = 1, 1_000_000
		in.Scope.Modes = scopeModes
		return Plan(in)
	}
	if p, _ := explore([]string{"new_seed"}, "pop, 100 BPM"); len(p) != 1 || p[0].Item.Mode != "new_seed" {
		t.Errorf("new_seed only: %+v", p)
	}
	if p, _ := explore([]string{"style_edit"}, "pop, 100 BPM"); len(p) != 1 || p[0].Item.Mode != "style_edit" {
		t.Errorf("style_edit only: %+v", p)
	}
	p, d := explore([]string{"style_edit"}, "pop")
	if len(p) != 0 || !reflect.DeepEqual(d, []Dropped{{"r", "mode"}}) {
		t.Errorf("style_edit only, no BPM: %+v %+v", p, d)
	}
	// style_edit alone: exploit needs new_seed, so the exploit lineage is dropped and the explore pool serves the leftover
	in := baseInput(lin("r", 5, "pop, 100 BPM"))
	in.MaxTakes, in.ExplorePPM = 1, 0
	in.Scope.Modes = []string{"style_edit"}
	p, d = Plan(in)
	if len(p) != 1 || p[0].Kind != "explore" || p[0].Item.Mode != "style_edit" || len(d) != 0 {
		t.Errorf("exploit falls to explore: %+v %+v", p, d)
	}
}

func TestPlan_TieBreakOnlyLast(t *testing.T) {
	t.Parallel()
	a, b := lin("a", 5, "pop"), lin("b", 5, "pop")
	in := baseInput(a, b)
	in.MaxTakes, in.ExplorePPM = 1, 0
	// equal stars, no ladder: the tie-break decides, in both directions
	in.TieBreak = map[string]float64{"a": 1.0, "b": 2.0}
	if p, _ := Plan(in); roots(p)[0] != "exploit:b" {
		t.Errorf("tie-break b: %v", roots(p))
	}
	in.TieBreak = map[string]float64{"a": 2.0, "b": 1.0}
	if p, _ := Plan(in); roots(p)[0] != "exploit:a" {
		t.Errorf("tie-break a: %v", roots(p))
	}
	// it never overrides stars or U
	c := lin("c", 4, "pop")
	in = baseInput(a, c)
	in.MaxTakes, in.ExplorePPM = 1, 0
	in.TieBreak = map[string]float64{"c": 100}
	if p, _ := Plan(in); roots(p)[0] != "exploit:a" {
		t.Errorf("stars outrank tie-break: %v", roots(p))
	}
	a.U, a.SD, a.NDuels = fp(1.0), fp(0.5), 5
	b.U, b.SD, b.NDuels = fp(2.0), fp(0.5), 5
	in = baseInput(a, b)
	in.MaxTakes, in.ExplorePPM = 1, 0
	in.TieBreak = map[string]float64{"a": 50}
	if p, _ := Plan(in); roots(p)[0] != "exploit:b" {
		t.Errorf("U outranks tie-break: %v", roots(p))
	}
	// absent: the seeded hash decides, and the seed changes it for some seed
	in.TieBreak = nil
	x, y := lin("x", 5, "pop"), lin("y", 5, "pop")
	first := map[string]bool{}
	for s := uint64(0); s < 40; s++ {
		in = baseInput(x, y)
		in.MaxTakes, in.ExplorePPM, in.Seed = 1, 0, s
		p, _ := Plan(in)
		first[p[0].RootID] = true
	}
	if len(first) != 2 {
		t.Errorf("the seed never changes the order: %v", first)
	}
}

func TestPlan_ExploreRanksUncertaintyFirst(t *testing.T) {
	t.Parallel()
	mk := func(root string, sd *float64) Lineage {
		l := lin(root, 4, "pop")
		l.SD = sd
		if sd != nil {
			l.U, l.NDuels = fp(0), 4
		}
		return l
	}
	in := baseInput(mk("narrow", fp(0.3)), mk("wide", fp(1.4)), mk("unknown", nil), mk("mid", fp(1.0)))
	in.MaxTakes, in.ExplorePPM, in.MinStars = 4, 1_000_000, 5 // nothing exploit-grade
	planned, _ := Plan(in)
	if diff := cmp.Diff([]string{"explore:unknown", "explore:wide", "explore:mid", "explore:narrow"}, roots(planned)); diff != "" {
		t.Errorf("explore order:\n%s", diff)
	}
}

func TestPlan_Deterministic(t *testing.T) {
	t.Parallel()
	var ls []Lineage
	for i := 0; i < 15; i++ {
		l := lin(fmt.Sprintf("r%02d", i), 3+i%3, fmt.Sprintf("pop, %d BPM", 90+i))
		if i%2 == 0 {
			l.U, l.SD, l.NDuels = fp(float64(i%4)), fp(0.2+float64(i%5)/4), 3+i%4
		}
		ls = append(ls, l)
	}
	in := baseInput(ls...)
	in.TieBreak = map[string]float64{"r01": 1, "r03": 2}
	want, wd := Plan(in)
	for i := 0; i < 20; i++ {
		got, gd := Plan(in)
		if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(wd, gd) {
			t.Fatal("plan differs between runs")
		}
	}
	rev := in
	rev.Lineages = nil
	for i := len(ls) - 1; i >= 0; i-- {
		rev.Lineages = append(rev.Lineages, ls[i])
	}
	got, _ := Plan(rev)
	if diff := cmp.Diff(roots(want), roots(got)); diff != "" {
		t.Errorf("input order changed the plan:\n%s", diff)
	}
	// a different night gives a different (but valid) plan for at least one seed
	changed := false
	for s := uint64(1); s < 30 && !changed; s++ {
		o := in
		o.Seed = s
		p, _ := Plan(o)
		changed = !reflect.DeepEqual(roots(p), roots(want))
	}
	if !changed {
		t.Error("the seed never changes the plan")
	}
	if NightSeed("2026-11-21") == NightSeed("2026-11-22") {
		t.Error("NightSeed")
	}
}
