package ops

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

const d4Rate = 16000

// d4PeakIn is the frequency and level (linear) of the strongest spectral peak of x between lo and hi Hz: Hann window,
// zero-padding to 8x, parabolic interpolation on the log magnitude.
func d4PeakIn(x []float64, rate int, lo, hi float64) (freq, level float64) {
	n := 8 * dsp.NextPow2(len(x))
	mag := dsptest.Spectrum(x, n)
	bin := func(f float64) int { return int(f * float64(n) / float64(rate)) }
	best := max(bin(lo), 1)
	for k := best; k <= min(bin(hi), len(mag)-2); k++ {
		if mag[k] > mag[best] {
			best = k
		}
	}
	a, b, c := dsp.Log(mag[best-1]+1e-300), dsp.Log(mag[best]+1e-300), dsp.Log(mag[best+1]+1e-300)
	off := 0.0
	if d := a - 2*b + c; d != 0 {
		off = 0.5 * (a - c) / d
	}
	return (float64(best) + off) * float64(rate) / float64(n), mag[best]
}

func d4DB(a, b float64) float64 { return 20 * math.Log10(a/b) }

func d4Window(x []float64, rate int, fromS, toS float64) []float64 {
	return x[int(fromS*float64(rate)):int(toS*float64(rate))]
}

func d4Pearson(a, b []float64) float64 {
	var sa, sb, sab float64
	for i := range a {
		sa += a[i] * a[i]
		sb += b[i] * b[i]
		sab += a[i] * b[i]
	}
	return sab / math.Sqrt(sa*sb)
}

func d4Near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f +- %.3f", what, got, want, tol)
	}
}
