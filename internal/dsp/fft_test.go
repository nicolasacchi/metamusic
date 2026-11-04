package dsp_test

import (
	"math"
	"math/cmplx"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

func TestFFT_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 8, 4096} {
		x := dsptest.Noise(uint64(n), n, 1)
		re, im := append([]float64(nil), x...), make([]float64, n)
		dsp.FFT(re, im)
		dsp.IFFT(re, im)
		var worst float64
		for i := range x {
			worst = math.Max(worst, math.Max(math.Abs(re[i]-x[i]), math.Abs(im[i])))
		}
		if worst >= 1e-12 {
			t.Errorf("n=%d: round-trip error %g, want < 1e-12", n, worst)
		}
	}
}

func TestFFT_RoundTrip2p21(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("2^21 point transform: skipped under -short")
	}
	const n = 1 << 21
	x := dsptest.Noise(21, n, 1)
	re, im := append([]float64(nil), x...), make([]float64, n)
	dsp.FFT(re, im)
	dsp.IFFT(re, im)
	var num, den float64
	for i := range x {
		d := re[i] - x[i]
		num += float64(d*d) + float64(im[i]*im[i])
		den += float64(x[i] * x[i])
	}
	if rel := math.Sqrt(num / den); rel >= 1e-11 {
		t.Errorf("relative round-trip error %g at 2^21, want < 1e-11", rel)
	}
}

func TestFFT_MatchesDFT(t *testing.T) {
	t.Parallel()
	for _, n := range []int{2, 4, 8, 16, 64} {
		x := dsptest.Noise(uint64(n), n, 1)
		re, im := append([]float64(nil), x...), make([]float64, n)
		dsp.FFT(re, im)
		for k := range n {
			var want complex128
			for j, v := range x {
				want += complex(v, 0) * cmplx.Exp(complex(0, -2*math.Pi*float64(j*k)/float64(n)))
			}
			if d := cmplx.Abs(complex(re[k], im[k]) - want); d > 1e-12 {
				t.Fatalf("n=%d bin %d: off by %g", n, k, d)
			}
		}
	}
}

func TestFFT_Parseval(t *testing.T) {
	t.Parallel()
	const n = 8192
	x := dsptest.Noise(5, n, 1)
	re, im := append([]float64(nil), x...), make([]float64, n)
	dsp.FFT(re, im)
	var et, ef float64
	for i := range x {
		et += float64(x[i] * x[i])
		ef += float64(re[i]*re[i]) + float64(im[i]*im[i])
	}
	if rel := math.Abs(ef/n-et) / et; rel > 1e-12 {
		t.Errorf("Parseval relative error %g", rel)
	}
}

func TestFFT_MatchesAnalysisFFT(t *testing.T) {
	t.Parallel()
	const n = 8192
	x := dsptest.Noise(8, n, 1)
	ar, ai := append([]float64(nil), x...), make([]float64, n)
	analysis.FFT(ar, ai)
	br, bi := append([]float64(nil), x...), make([]float64, n)
	dsp.FFT(br, bi)
	for i := range n {
		if math.Abs(ar[i]-br[i]) > 1e-9 || math.Abs(ai[i]-bi[i]) > 1e-9 {
			t.Fatalf("bin %d: analysis (%g,%g) vs dsp (%g,%g)", i, ar[i], ai[i], br[i], bi[i])
		}
	}
}

func TestRFFT_RoundTrip(t *testing.T) {
	t.Parallel()
	x := dsptest.Noise(9, 1000, 1)
	re, im := dsp.RFFT(x, 1024)
	if len(re) != 513 || len(im) != 513 {
		t.Fatalf("RFFT returned %d bins, want 513", len(re))
	}
	if im[0] != 0 || math.Abs(im[512]) > 1e-12 {
		t.Errorf("DC/Nyquist bins are not real: %g, %g", im[0], im[512])
	}
	y := dsp.IRFFT(re, im, 1024)
	for i := range x {
		if math.Abs(y[i]-x[i]) > 1e-12 {
			t.Fatalf("sample %d: %g vs %g", i, y[i], x[i])
		}
	}
	for i := len(x); i < 1024; i++ {
		if math.Abs(y[i]) > 1e-12 {
			t.Fatalf("padding sample %d is %g, want 0", i, y[i])
		}
	}
}

func TestFFT_Panics(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ re, im int }{{3, 3}, {0, 0}, {4, 8}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("FFT(%d,%d) did not panic", c.re, c.im)
				}
			}()
			dsp.FFT(make([]float64, c.re), make([]float64, c.im))
		}()
	}
}

func TestTwiddles_Symmetry(t *testing.T) {
	t.Parallel()
	// the transform of an impulse at 1 is the exact twiddle row: bin k = e^{-2 pi i k / n}; the symmetry fill must be exact
	for _, n := range []int{8, 16, 1024} {
		re, im := make([]float64, n), make([]float64, n)
		re[1] = 1
		dsp.FFT(re, im)
		for k := range n {
			a := 2 * math.Pi * float64(k) / float64(n)
			if math.Abs(re[k]-math.Cos(a)) > 1e-15 || math.Abs(im[k]+math.Sin(a)) > 1e-15 {
				t.Fatalf("n=%d k=%d: (%g,%g) vs (%g,%g)", n, k, re[k], im[k], math.Cos(a), -math.Sin(a))
			}
		}
		if re[n/4] != 0 || im[n/4] != -1 {
			t.Errorf("n=%d: quarter-turn twiddle is (%g,%g), want exactly (0,-1)", n, re[n/4], im[n/4])
		}
	}
}
