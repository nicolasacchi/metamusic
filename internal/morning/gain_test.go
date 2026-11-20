package morning

import "testing"

func TestGain_Table(t *testing.T) {
	for _, tc := range []struct {
		lufs, tp float64
		cdb      int
		limited  bool
	}{
		{-11.2, -0.4, -480, false},
		{-20, -6, 400, false},
		{-20, -2, 100, true},
		{-16, -0.5, -50, true},
		{-16, -1, 0, false},      // tie: not limited
		{-16.005, -6, 1, false},  // 0.5 cdB rounds away from zero
		{-15.995, -6, -1, false}, // and so does the negative
	} {
		cdb, limited := Gain(tc.lufs, tc.tp)
		if cdb != tc.cdb || limited != tc.limited {
			t.Errorf("Gain(%v, %v) = %d, %v; want %d, %v", tc.lufs, tc.tp, cdb, limited, tc.cdb, tc.limited)
		}
	}
}
