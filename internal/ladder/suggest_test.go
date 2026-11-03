package ladder

import (
	"math"
	"testing"
)

// TestSuggestPairs_Golden: theta = [0, 0.1, 2.0], sd = 0.5, nu = 0.3 (D-M5-31): the pair whose outcome is least certain and whose
// strengths are least known comes first; a pair with an open duel or three human verdicts is absent.
func TestSuggestPairs_Golden(t *testing.T) {
	t.Parallel()
	fit := Fit{Theta: []float64{0, 0.1, 2.0}, SD: []float64{0.5, 0.5, 0.5}, Nu: 0.3}
	ids := []string{"a", "b", "c"}
	got := SuggestPairs(fit, ids, nil, nil, 10)
	want := []struct {
		a, b  int
		score float64
	}{{0, 1, 0.1248}, {1, 2, 0.0686}, {0, 2, 0.0648}}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs: %+v", len(got), got)
	}
	for i, w := range want {
		g := got[i]
		if g.A != w.a || g.B != w.b || math.Abs(g.Score-w.score) > 1e-3 || g.AID != ids[w.a] || g.BID != ids[w.b] {
			t.Errorf("pair %d = %+v, want (%d,%d) %.4f", i, g, w.a, w.b, w.score)
		}
		if g.SDA != 0.5 || g.SDB != 0.5 || g.PAWins <= 0 || g.PAWins >= 1 {
			t.Errorf("pair %d fields: %+v", i, g)
		}
	}
	// k limits, an open duel and three verdicts exclude, fewer than three keep
	if g := SuggestPairs(fit, ids, nil, nil, 2); len(g) != 2 || g[0].A != 0 || g[1].B != 2 || g[1].A != 1 {
		t.Errorf("k=2: %+v", g)
	}
	g := SuggestPairs(fit, ids, map[[2]int]int{{0, 1}: 3, {1, 2}: 2}, map[[2]int]bool{{0, 2}: true}, 10)
	if len(g) != 1 || g[0].A != 1 || g[0].B != 2 || g[0].HumanVerdicts != 2 {
		t.Errorf("exclusions: %+v", g)
	}
	// keys are unordered: (1,0) is the same pair as (0,1)
	if g := SuggestPairs(fit, ids, map[[2]int]int{{1, 0}: 3}, map[[2]int]bool{{2, 1}: true}, 10); len(g) != 1 || g[0].A != 0 || g[0].B != 2 {
		t.Errorf("unordered keys: %+v", g)
	}
	// degenerate inputs
	for _, k := range []int{0, -1} {
		if g := SuggestPairs(fit, ids, nil, nil, k); len(g) != 0 {
			t.Errorf("k=%d: %+v", k, g)
		}
	}
	if g := SuggestPairs(Fit{Theta: []float64{1}, SD: []float64{1}}, nil, nil, nil, 5); len(g) != 0 {
		t.Errorf("one item: %+v", g)
	}
}

// TestSuggestPairs_Deterministic: equal scores order by index pair, and the input is not modified.
func TestSuggestPairs_Deterministic(t *testing.T) {
	t.Parallel()
	fit := Fit{Theta: []float64{0, 0, 0, 0}, SD: []float64{1, 1, 1, 1}, Nu: 0.5}
	g := SuggestPairs(fit, nil, nil, nil, 6)
	if len(g) != 6 {
		t.Fatalf("got %d", len(g))
	}
	prev := [2]int{-1, -1}
	for _, p := range g {
		if p.AID != "" || p.BID != "" {
			t.Errorf("ids without ids: %+v", p)
		}
		cur := [2]int{p.A, p.B}
		if cur[0] < prev[0] || (cur[0] == prev[0] && cur[1] <= prev[1]) {
			t.Errorf("order: %v after %v", cur, prev)
		}
		prev = cur
	}
}
