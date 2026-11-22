package criticbench

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestMetric_Prediction(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		dir      int
		min      float64
		a, b     float64
		predict  string // "A", "B" or ""
		hasValue bool
	}{
		{"lower is better, clear", -1, 0.5, 2.0, 1.2, "B", true},
		{"lower is better, too close", -1, 0.5, 2.0, 1.8, "", true},
		{"higher is better", 1, 0.5, 2.0, 1.2, "A", true},
		{"exactly the delta", 1, 0.5, 1.5, 1.0, "A", true},
		{"equal", 1, 0.5, 1, 1, "", true},
	} {
		pairs := []Pair{{A: "a", B: "b", Winner: "a", At: t0}}
		s := Metric(tc.dir, tc.min, map[string]float64{"a": tc.a, "b": tc.b}, pairs)
		if tc.predict == "" {
			if s.Predicted != 0 || s.Pairs != 1 {
				t.Errorf("%s: predicted %d pairs %d", tc.name, s.Predicted, s.Pairs)
			}
			continue
		}
		wantAgree := 0
		if tc.predict == "A" { // the owner preferred a
			wantAgree = 1
		}
		if s.Predicted != 1 || s.Agree != wantAgree {
			t.Errorf("%s: predicted %d agree %d, want 1 and %d", tc.name, s.Predicted, s.Agree, wantAgree)
		}
	}
}

func TestMetric_CoverageAndDisagreements(t *testing.T) {
	t.Parallel()
	var pairs []Pair
	vals := map[string]float64{}
	for i := 0; i < 14; i++ {
		a, b := fmt.Sprintf("a%02d", i), fmt.Sprintf("b%02d", i)
		pairs = append(pairs, Pair{A: a, B: b, Winner: a, At: t0.Add(time.Duration(i) * time.Hour)})
		if i < 12 {
			vals[a], vals[b] = 0, 1 // direction +1 predicts b: always wrong
		}
	}
	s := Metric(1, 0.5, vals, pairs)
	if s.Pairs != 12 || s.Predicted != 12 || s.Agree != 0 {
		t.Fatalf("got %+v", s)
	}
	if s.Coverage < 0.857 || s.Coverage > 0.858 {
		t.Errorf("coverage %v", s.Coverage)
	}
	if len(s.Disagreements) != 10 || s.Disagreements[0] != "a11|b11" || s.Disagreements[9] != "a02|b02" {
		t.Errorf("disagreements newest first, at most 10: %v", s.Disagreements)
	}
	if empty := Metric(1, 0.5, nil, nil); empty.Coverage != 0 || empty.State != StateInsufficient {
		t.Errorf("empty: %+v", empty)
	}
}

func TestRatingPairs_WithinLineageOnly(t *testing.T) {
	t.Parallel()
	owner := map[string]int{"r1a": 5, "r1b": 2, "r1c": 2, "r2a": 4, "r2b": 1, "lone": 3}
	roots := map[string]string{"r1a": "r1", "r1b": "r1", "r1c": "r1", "r2a": "r2", "r2b": "r2", "lone": "r3"}
	at := map[string]time.Time{}
	for k := range owner {
		at[k] = t0
	}
	got := RatingPairs(owner, roots, at, 400)
	// r1: (a,b) (a,c) differ; (b,c) tie; r2: (a,b). No cross-lineage pair.
	if len(got) != 3 {
		t.Fatalf("pairs = %+v", got)
	}
	for _, p := range got {
		if roots[p.A] != roots[p.B] || p.Root != roots[p.A] {
			t.Errorf("cross-lineage pair %+v", p)
		}
		if owner[p.Winner] <= owner[otherOf(p, p.Winner)] {
			t.Errorf("winner is not the higher rated: %+v", p)
		}
	}
}

func otherOf(p Pair, w string) string {
	if p.A == w {
		return p.B
	}
	return p.A
}

func TestRatingPairs_EffectiveN(t *testing.T) {
	t.Parallel()
	owner := map[string]int{"hub": 5}
	roots := map[string]string{"hub": "r"}
	at := map[string]time.Time{"hub": t0}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("t%02d", i)
		owner[id], roots[id], at[id] = 1, "r", t0
	}
	pairs := RatingPairs(owner, roots, at, 400)
	if len(pairs) != 20 {
		t.Fatalf("pairs %d", len(pairs))
	}
	// a critic that always sides with the hub: 20 pairs, 21 distinct takes => NEff 20; with only 5 spokes, NEff is capped by takes
	vals := map[string]float64{"hub": 10}
	for _, p := range pairs {
		vals[otherOf(p, "hub")] = 0
	}
	s := Metric(1, 1, vals, pairs)
	if s.Pairs != 20 || s.Agree != 20 || s.NEff != 20 {
		t.Fatalf("got %+v", s)
	}
	few := pairs[:3]
	s = Metric(1, 1, vals, few)
	if s.NEff != 3 || s.NEff > distinctTakes(few) {
		t.Errorf("NEff %d with %d distinct takes", s.NEff, distinctTakes(few))
	}
	// duplicated pairs about the same two takes do not add information
	dup := []Pair{{A: "a", B: "b", Winner: "a", At: t0}, {A: "a", B: "b", Winner: "a", At: t0}, {A: "a", B: "b", Winner: "a", At: t0}}
	if s := Metric(1, 1, map[string]float64{"a": 2, "b": 0}, dup); s.NEff != 2 {
		t.Errorf("NEff %d, want 2 distinct takes", s.NEff)
	}
}

func TestRatingPairs_WindowAndOrder(t *testing.T) {
	t.Parallel()
	owner, roots, at := map[string]int{}, map[string]string{}, map[string]time.Time{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("t%d", i)
		owner[id], roots[id], at[id] = i+1, "r", t0.Add(time.Duration(i)*time.Minute)
	}
	got := RatingPairs(owner, roots, at, 4) // 10 pairs, keep the 4 whose later rating is newest
	if len(got) != 4 {
		t.Fatalf("len %d", len(got))
	}
	for _, p := range got {
		if p.A != "t3" && p.B != "t3" && p.A != "t4" && p.B != "t4" {
			t.Errorf("an old pair survived: %+v", p)
		}
	}
	if !got[0].At.After(got[len(got)-1].At) && !got[0].At.Equal(got[len(got)-1].At) {
		t.Errorf("not newest first: %v", got)
	}
	if again := RatingPairs(owner, roots, at, 4); fmt.Sprint(again) != fmt.Sprint(got) {
		t.Error("not deterministic")
	}
	if RatingPairs(owner, roots, at, 0) != nil && len(RatingPairs(owner, roots, at, 0)) != 0 {
		t.Error("window 0 keeps nothing")
	}
}

func TestPairwise(t *testing.T) {
	t.Parallel()
	pairs := []Pair{
		{ID: "d1", A: "a", B: "b", Winner: "a", At: t0},
		{ID: "d2", A: "c", B: "d", Winner: "d", At: t0.Add(time.Hour)},
		{ID: "d3", A: "e", B: "f", Winner: "e", At: t0.Add(2 * time.Hour)},
		{ID: "d4", A: "g", B: "h", Winner: "g", At: t0.Add(3 * time.Hour)},
	}
	s := Pairwise(pairs, map[string]string{"d1": "a", "d2": "c", "d3": "", "zz": "a"})
	if s.Pairs != 3 || s.Predicted != 2 || s.Agree != 1 {
		t.Fatalf("got %+v", s)
	}
	if len(s.Disagreements) != 1 || s.Disagreements[0] != "d2" {
		t.Errorf("disagreements %v", s.Disagreements)
	}
	if s.Coverage != 0.75 {
		t.Errorf("coverage %v", s.Coverage)
	}
}

func TestSlotBias(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, ab  int
		share  float64
		biased bool
	}{
		{15, 20, 0.75, true},
		{10, 20, 0.5, false},
		{18, 19, 18.0 / 19, false}, // too few verdicts
		{0, 0, 0, false},
		{3, 40, 0.075, true},
	} {
		share, lo, hi, biased := SlotBias(tc.a, tc.ab)
		if !near(share, tc.share) || biased != tc.biased || lo > hi {
			t.Errorf("SlotBias(%d, %d) = %v [%v, %v] %v", tc.a, tc.ab, share, lo, hi, biased)
		}
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()
	human := map[string]bool{"l1": true, "l2": false, "l3": true, "l4": false}
	pred := map[string]bool{"l1": true, "l2": true, "l3": true, "extra": true}
	s := Labels(pred, human)
	if s.Pairs != 3 || s.NEff != 3 || s.Agree != 2 || s.Predicted != 3 {
		t.Fatalf("got %+v", s)
	}
	if s.Coverage != 0.75 {
		t.Errorf("coverage %v", s.Coverage)
	}
	if len(s.Disagreements) != 1 || s.Disagreements[0] != "l2" {
		t.Errorf("disagreements %v", s.Disagreements)
	}
}

// TestCalibrate_RecoversKnownAccuracy simulates an Owner rating 30 lineages (300 ratings) and a critic that picks the better take
// with a fixed probability: the Wilson state follows the true rate.
func TestCalibrate_RecoversKnownAccuracy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		acc  float64
		want string
	}{{0.8, StateTrusted}, {0.5, StateUnknown}, {0.2, StateContrary}} {
		rng := rand.New(rand.NewSource(7)) // #nosec G404 -- deterministic, seeded simulation
		owner, roots, at := map[string]int{}, map[string]string{}, map[string]time.Time{}
		for l := 0; l < 30; l++ {
			for k := 0; k < 10; k++ {
				id := fmt.Sprintf("l%02d-t%d", l, k)
				owner[id] = 1 + k%5
				roots[id], at[id] = fmt.Sprintf("l%02d", l), t0
			}
		}
		pairs := RatingPairs(owner, roots, at, 400)
		if len(pairs) != 400 {
			t.Fatalf("pairs %d", len(pairs))
		}
		verdicts := map[string]string{}
		for _, p := range pairs {
			if rng.Float64() < tc.acc {
				verdicts[p.Key()] = p.Winner
			} else {
				verdicts[p.Key()] = otherOf(p, p.Winner)
			}
		}
		s := Pairwise(pairs, verdicts)
		if s.State != tc.want {
			t.Errorf("acc %.1f: state %s (rate %.3f [%.3f, %.3f] n_eff %d), want %s", tc.acc, s.State, s.Rate, s.Lo, s.Hi, s.NEff, tc.want)
		}
		if tc.acc < s.Lo || tc.acc > s.Hi {
			t.Errorf("acc %.1f: true rate outside [%.3f, %.3f]", tc.acc, s.Lo, s.Hi)
		}
		if s.Rate < s.Lo-1e-9 || s.Rate > s.Hi+1e-9 {
			t.Errorf("acc %.1f: rate %.3f outside [%.3f, %.3f]", tc.acc, s.Rate, s.Lo, s.Hi)
		}
	}
}
