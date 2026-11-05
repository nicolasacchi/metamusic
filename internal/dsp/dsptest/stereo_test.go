package dsptest

import (
	"math"
	"testing"
)

func TestStereo_Correlated(t *testing.T) {
	t.Parallel()
	for _, rho := range []float64{-1, -0.5, 0, 0.3, 0.9, 1} {
		l, r := Correlated(5, 200000, 0.2, rho)
		if got := Pearson(l, r); math.Abs(got-rho) > 0.01 {
			t.Errorf("Correlated(%.1f): measured %.4f", rho, got)
		}
		b := PinkStereo(16000, 3, 20, rho)
		if got := Pearson(b.Ch[0], b.Ch[1]); math.Abs(got-rho) > 0.05 {
			t.Errorf("PinkStereo(%.1f): measured %.4f", rho, got)
		}
	}
}

func TestStereo_HardPannedAndShapes(t *testing.T) {
	t.Parallel()
	for _, left := range []bool{true, false} {
		b := HardPanned(16000, 440, 0.5, 0.5, left)
		silent := b.Ch[1]
		if !left {
			silent = b.Ch[0]
		}
		if PeakAbs(silent) != 0 || math.Abs(PeakAbs(b.Ch[0])+PeakAbs(b.Ch[1])-0.5) > 1e-3 {
			t.Errorf("left=%v: not hard panned", left)
		}
	}
	if Pearson([]float64{1, 1, 1}, []float64{1, 2, 3}) != 0 || Pearson(nil, nil) != 0 {
		t.Error("a constant or empty channel must give 0")
	}
	for label, want := range map[string]float64{"MONO": 0.98, "WIDE": 0.1, "OOP": -0.5} {
		b := RealWorldShape(label, 16000, 20, 9)
		if got := Pearson(b.Ch[0], b.Ch[1]); math.Abs(got-want) > 0.05 {
			t.Errorf("%s: measured %.3f, want about %.2f", label, got, want)
		}
	}
	for name, f := range map[string]func(){
		"unknown shape": func() { RealWorldShape("NOPE", 16000, 1, 1) },
		"bad rho":       func() { Correlated(1, 10, 1, 1.5) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			f()
		}()
	}
}
