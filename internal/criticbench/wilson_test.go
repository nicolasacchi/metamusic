package criticbench

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-4 }

func TestWilson_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		k, n   int
		lo, hi float64
	}{
		{24, 30, 0.6269, 0.9050},
		{18, 30, 0.4232, 0.7541},
		{6, 30, 0.0950, 0.3731},
		{0, 0, 0, 1},
		{0, 10, 0, 0.2775},
		{10, 10, 0.7225, 1},
		{-3, 10, 0, 0.2775}, // out of range clamps
		{12, 10, 0.7225, 1},
	} {
		lo, hi := Wilson(tc.k, tc.n)
		if !near(lo, tc.lo) || !near(hi, tc.hi) {
			t.Errorf("Wilson(%d, %d) = [%.4f, %.4f], want [%.4f, %.4f]", tc.k, tc.n, lo, hi, tc.lo, tc.hi)
		}
	}
}

func TestScore_States(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		agree, nEff  int
		want         string
		wantLo, wHi  float64
		skipInterval bool
	}{
		{"trusted", 24, 30, StateTrusted, 0.6269, 0.9050, false},
		{"unknown", 18, 30, StateUnknown, 0.4232, 0.7541, false},
		{"contrary", 6, 30, StateContrary, 0.0950, 0.3731, false},
		{"insufficient", 25, 25, StateInsufficient, 0, 0, true},
		{"nothing predicted", 0, 0, StateInsufficient, 0, 1, false},
	} {
		s := newScore(tc.nEff, tc.nEff, tc.agree, tc.nEff, 1)
		if s.State != tc.want {
			t.Errorf("%s: state %q, want %q", tc.name, s.State, tc.want)
		}
		if !tc.skipInterval && (!near(s.Lo, tc.wantLo) || !near(s.Hi, tc.wHi)) {
			t.Errorf("%s: [%.4f, %.4f], want [%.4f, %.4f]", tc.name, s.Lo, s.Hi, tc.wantLo, tc.wHi)
		}
	}
}

// The rate is Agree / Predicted while the interval is taken at NEff: shared takes never inflate confidence.
func TestScore_RateAtEffectiveN(t *testing.T) {
	t.Parallel()
	s := newScore(60, 20, 45, 60, 1) // 45 of 60 agree, but only 20 distinct takes back them
	if s.Rate != 0.75 || s.NEff != 20 || s.State != StateInsufficient {
		t.Fatalf("got %+v", s)
	}
	lo, hi := Wilson(15, 20) // round(0.75 * 20)
	if !near(s.Lo, lo) || !near(s.Hi, hi) {
		t.Errorf("[%v, %v], want [%v, %v]", s.Lo, s.Hi, lo, hi)
	}
}
