package ops

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

// TestLoop_Seamless: the wrap from the end of the cycle to its beginning has no step beyond the signal's own curvature, below
// -40 dB re RMS, wherever the cycle starts and whatever its length (not a multiple of any period).
func TestLoop_Seamless(t *testing.T) {
	t.Parallel()
	x := make([]float64, 4*d4Rate)
	a, b := dsptest.Sine(d4Rate, 110.3, 0.5, 4), dsptest.Sine(d4Rate, 167.9, 0.3, 4)
	for i := range x {
		x[i] = a[i] + b[i]
	}
	xf := roundHalfUp(LoopCrossfadeS * d4Rate)
	for _, c := range []struct{ start, length int }{{8000, 12345}, {1000, 20000}, {17, 9001}, {30000, 777}, {d4Rate / 2, 3 * d4Rate / 2}} {
		l := MakeLoop(x, c.start, c.length, xf)
		if len(l) != c.length {
			t.Fatalf("length %d", len(l))
		}
		n := len(l)
		// the second difference across the wrap: predicted next sample vs the real first one
		jump := math.Abs(l[0] - (2*l[n-1] - l[n-2]))
		if db := 20 * math.Log10(jump/dsptest.RMS(l)); db > -40 {
			t.Errorf("start %d length %d: wrap discontinuity %.1f dB re RMS, want < -40", c.start, c.length, db)
		}
		// without the crossfade the same cycle has a visible step
		raw := MakeLoop(x, c.start, c.length, 0)
		if rj := math.Abs(raw[0] - (2*raw[n-1] - raw[n-2])); rj < 3*jump {
			t.Errorf("start %d length %d: the crossfade does not help (%.4f vs %.4f)", c.start, c.length, jump, rj)
		}
	}
}

func TestLoop_EdgesAndTile(t *testing.T) {
	t.Parallel()
	if MakeLoop([]float64{1, 2}, 0, 0, 3) != nil {
		t.Error("empty loop")
	}
	// the start before the signal reads zeros; a crossfade longer than half the cycle is clamped
	l := MakeLoop([]float64{1, 1, 1, 1}, -2, 6, 100)
	if len(l) != 6 || l[0] != 0 || l[1] != 0 || l[2] != 1 {
		t.Errorf("loop %v", l)
	}
	tl := Tile([]float64{1, 2, 3}, 8)
	want := []float64{1, 2, 3, 1, 2, 3, 1, 2}
	for i, v := range want {
		if tl[i] != v {
			t.Fatalf("tile %v", tl)
		}
	}
	if len(Tile(nil, 4)) != 4 || len(Tile([]float64{1}, 0)) != 0 {
		t.Error("tile edge cases")
	}
	// an equal-power fade: power preserved for identical signals
	c := make([]float64, 1000)
	for i := range c {
		c[i] = 1
	}
	l = MakeLoop(c, 500, 400, 100)
	for i := 300; i < 400; i++ {
		if l[i] < 0.999 || l[i] > 1.42 {
			t.Fatalf("crossfade of equal signals out of range at %d: %v", i, l[i])
		}
	}
}
