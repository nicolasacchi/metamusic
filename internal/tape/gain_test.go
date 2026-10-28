package tape_test

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TestGainFactor_Pinned: the seven gain factors as hex float literals. Equal on every architecture because ApplyGain
// computes float64(math.Pow(10, cdB/2000)) once and multiplies in float64 with explicit casts (no FMA contraction).
func TestGainFactor_Pinned(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		cdb  int
		want float64
	}{
		{-1200, 0x1.0137987dd704dp-02},
		{-600, 0x1.009b9cf334252p-01},
		{-100, 0x1.c8520affa0a4bp-01},
		{0, 0x1p+00},
		{90, 0x1.1bf2e175194e8p+00},
		{400, 0x1.95bb8f6d46053p+00},
		{600, 0x1.fec982d5bb8bp+00},
	} {
		if got := tape.GainFactor(c.cdb); got != c.want {
			t.Errorf("GainFactor(%d) = %x, want %x", c.cdb, got, c.want)
		}
	}
}

func TestApplyGain_Table(t *testing.T) {
	t.Parallel()
	one := func(v int32, cdb int) (int32, int) {
		dst := make([]int32, 1)
		c := tape.ApplyGain(dst, []int32{v}, cdb)
		return dst[0], c
	}
	for _, c := range []struct {
		name        string
		in          int32
		cdb         int
		want        int32
		wantClipped int
	}{
		{"-600 cdB on 1 000 000", 1_000_000, -600, 501_187, 0},
		{"-600 cdB on a negative sample", -1_000_000, -600, -501_187, 0},
		{"+600 cdB clamps and counts", 7_000_000, 600, 8_388_607, 1},
		{"+600 cdB clamps the negative side", -7_000_000, 600, -8_388_607, 1},
		{"0 cdB is the identity", 1_234_567, 0, 1_234_567, 0},
		{"0 cdB keeps full-scale negative", -8_388_608, 0, -8_388_608, 0},
		{"-1200 cdB on +1 is 0", 1, -1200, 0, 0},
		{"-1200 cdB on -1 is 0", -1, -1200, 0, 0},
		{"a result at full scale is not a clip", 8_388_607, 0, 8_388_607, 0},
		{"-100 cdB rounds to the nearest", 1000, -100, 891, 0},
		{"-100 cdB, negative, symmetric", -1000, -100, -891, 0},
		{"+90 cdB", 1000, 90, 1109, 0},
		{"zero stays zero", 0, 600, 0, 0},
	} {
		got, clipped := one(c.in, c.cdb)
		if got != c.want || clipped != c.wantClipped {
			t.Errorf("%s: %d clipped %d, want %d clipped %d", c.name, got, clipped, c.want, c.wantClipped)
		}
	}
}

// TestApplyGain_Ramp compares a 1 000-sample ramp with an independent float reference, and checks the in-place form, the
// symmetry of the rounding and the count of clamped samples.
func TestApplyGain_Ramp(t *testing.T) {
	t.Parallel()
	src := make([]int32, 1000)
	for i := range src {
		src[i] = int32(i-500) * 16000
	}
	for _, cdb := range []int{-1200, -777, -100, -1, 0, 1, 90, 333, 600} {
		dst := make([]int32, len(src))
		clipped := tape.ApplyGain(dst, src, cdb)
		f := math.Pow(10, float64(cdb)/2000)
		wantClipped := 0
		for i, v := range src {
			w := math.Round(float64(v) * f)
			if cdb != 0 {
				if w > 8_388_607 {
					w, wantClipped = 8_388_607, wantClipped+1
				} else if w < -8_388_607 {
					w, wantClipped = -8_388_607, wantClipped+1
				}
			}
			if dst[i] != int32(w) {
				t.Fatalf("cdb %d sample %d: %d, want %d", cdb, i, dst[i], int32(w))
			}
		}
		if clipped != wantClipped {
			t.Errorf("cdb %d: clipped %d, want %d", cdb, clipped, wantClipped)
		}
		// in place gives the same result
		inplace := slices.Clone(src)
		if c2 := tape.ApplyGain(inplace, inplace, cdb); c2 != clipped || !slices.Equal(inplace, dst) {
			t.Errorf("cdb %d: in-place result differs", cdb)
		}
	}
	// the transform is odd: gain(-x) = -gain(x)
	pos, neg := make([]int32, 1), make([]int32, 1)
	for v := int32(1); v < 5000; v += 7 {
		tape.ApplyGain(pos, []int32{v}, -333)
		tape.ApplyGain(neg, []int32{-v}, -333)
		if pos[0] != -neg[0] {
			t.Fatalf("not symmetric at %d: %d vs %d", v, pos[0], neg[0])
		}
	}
	// an empty source is fine
	if tape.ApplyGain(nil, nil, -100) != 0 {
		t.Error("empty")
	}
}

// TestPerf_GainApply: ApplyGain runs at least 100 MSamples/s (one hour of stereo 48 kHz is 346 MSamples, so under 3.5 s).
func TestPerf_GainApply(t *testing.T) {
	const n = 20_000_000 // 0.2 s at the target rate
	budget := perftest.Budget(t, 200*time.Millisecond)
	src := make([]int32, 1<<20)
	for i := range src {
		src[i] = int32((i*7919)%1_000_000) - 500_000
	}
	dst := make([]int32, len(src))
	sw := perftest.Start()
	for done := 0; done < n; done += len(src) {
		tape.ApplyGain(dst, src, -230)
	}
	if el := sw.Elapsed(); el > budget {
		t.Errorf("%d samples took %v (budget %v): below 100 MSamples/s", n, el, budget)
	}
}
