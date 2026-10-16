package sounds_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func f(v float64) *float64 { return &v }

func TestTempoBand_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		bpm  float64
		want string
	}{
		{30, "slow"}, {79.9, "slow"}, {80, "mid"}, {109.9, "mid"}, {110, "upbeat"}, {129.9, "upbeat"},
		{130, "fast"}, {159.9, "fast"}, {160, "rapid"}, {240, "rapid"},
	} {
		if got := sounds.TempoBand(tc.bpm); got != tc.want {
			t.Errorf("TempoBand(%v) = %s, want %s", tc.bpm, got, tc.want)
		}
	}
}

func TestCompareTempo_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		req, meas    *float64
		mismatch, oc bool
	}{
		{"76/75 ok", f(76), f(75), false, false},
		{"76/152 octave", f(76), f(152), false, true},
		{"152/76 octave", f(152), f(76), false, true},
		{"76/95 mismatch", f(76), f(95), true, false},
		{"120/125 ok", f(120), f(125), false, false},
		{"120/126 ok", f(120), f(126), false, false},
		{"120/127 mismatch", f(120), f(127), true, false},
		{"octave within 6 pct", f(100), f(206), false, true},
		{"nil requested", nil, f(100), false, false},
		{"nil measured", f(100), nil, false, false},
	} {
		m, o := sounds.CompareTempo(tc.req, tc.meas)
		if m != tc.mismatch || o != tc.oc {
			t.Errorf("%s: got %v,%v want %v,%v", tc.name, m, o, tc.mismatch, tc.oc)
		}
	}
}
