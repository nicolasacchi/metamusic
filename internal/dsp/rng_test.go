package dsp_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

func TestRand_Deterministic(t *testing.T) {
	t.Parallel()
	a, b, c := dsp.NewRand(7), dsp.NewRand(7), dsp.NewRand(8)
	same, diff := true, false
	for range 100 {
		x, y, z := a.Uint64(), b.Uint64(), c.Uint64()
		same = same && x == y
		diff = diff || x != z
	}
	if !same || !diff {
		t.Errorf("same seed identical = %v, other seed differs = %v", same, diff)
	}
}

func TestRand_Float64(t *testing.T) {
	t.Parallel()
	r := dsp.NewRand(1)
	ref := dsp.NewRand(1)
	var sum float64
	const n = 100000
	for range n {
		f := r.Float64()
		if want := float64(ref.Uint64()>>11) * 0x1p-53; f != want || f < 0 || f >= 1 {
			t.Fatalf("Float64 = %v, want %v in [0,1)", f, want)
		}
		sum += f
	}
	if m := sum / n; math.Abs(m-0.5) > 0.01 {
		t.Errorf("mean %v, want 0.5", m)
	}
}

func TestRand_Norm(t *testing.T) {
	t.Parallel()
	r := dsp.NewRand(2)
	var s, s2, s4 float64
	const n = 200000
	for range n {
		v := r.Norm()
		s += v
		s2 += v * v
		s4 += v * v * v * v
	}
	mean, vr, kurt := s/n, s2/n, s4/n/((s2/n)*(s2/n))
	if math.Abs(mean) > 0.01 || math.Abs(vr-1) > 0.02 || math.Abs(kurt-3) > 0.1 {
		t.Errorf("mean %.4f variance %.4f kurtosis %.3f", mean, vr, kurt)
	}
}
