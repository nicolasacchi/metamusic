package dsp_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

func direct(x, h []float64) []float64 {
	y := make([]float64, len(x)+len(h)-1)
	for i, a := range x {
		for j, b := range h {
			y[i+j] += a * b
		}
	}
	return y
}

func TestConvolveUP_MatchesDirect(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ nx, nh, block int }{{1000, 300, 64}, {1000, 5000, 64}, {777, 64, 64}, {1, 1, 64}, {100, 1, 64}, {1, 200, 64}, {3000, 1500, 512}, {900, 131, 100}} {
		x, h := dsptest.Noise(uint64(c.nx), c.nx, 1), dsptest.Noise(uint64(c.nh)+7, c.nh, 1)
		got := dsp.ConvolveUP(x, h, c.block, nil)
		want := direct(x, h)
		if len(got) != len(want) {
			t.Fatalf("%+v: length %d, want %d", c, len(got), len(want))
		}
		for i := range want {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Fatalf("%+v: sample %d differs by %g", c, i, got[i]-want[i])
			}
		}
	}
}

func TestConvolveUP_DefaultBlockAndEmpty(t *testing.T) {
	t.Parallel()
	x := dsptest.Noise(3, 9000, 1)
	h := dsptest.Noise(4, 600, 1)
	got := dsp.ConvolveUP(x, h, 0, nil)
	want := direct(x, h)
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("sample %d differs by %g", i, got[i]-want[i])
		}
	}
	if dsp.ConvolveUP(nil, h, 64, nil) != nil || dsp.ConvolveUP(x, nil, 64, nil) != nil {
		t.Error("an empty operand must give nil")
	}
}

func TestConvolveUP_Meter(t *testing.T) {
	t.Parallel()
	x, h := make([]float64, 1000), make([]float64, 200)
	m := &dsp.Meter{Max: 10}
	if y, err := dsp.ConvolveUPErr(x, h, 64, m); err == nil || y != nil {
		t.Error("the budget was not enforced")
	}
	m = &dsp.Meter{}
	if _, err := dsp.ConvolveUPErr(x, h, 64, m); err != nil {
		t.Fatal(err)
	}
	blocks := (len(x) + len(h) - 1 + 63) / 64
	if want := int64(blocks) * dsp.Units.ConvBlock(4, 128); m.Used() != want {
		t.Errorf("units %d, want %d", m.Used(), want)
	}
}
