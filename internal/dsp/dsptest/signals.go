// Package dsptest holds synthetic test signals and measurement helpers for the dsp packages and the transform tests. Nothing
// here is committed audio: every signal is generated, deterministic and public-safe. It is a test-support package and lives
// under internal/dsp so the DSP determinism scans cover it too.
package dsptest

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// DBToLin converts decibels (amplitude) to a linear factor.
func DBToLin(db float64) float64 { return dsp.Pow(10, db/20) }

// Frames is the number of samples of a signal of the given duration (rounded).
func Frames(rate int, seconds float64) int { return int(math.Floor(float64(rate)*seconds + 0.5)) }

// Sine is amp * sin(2*pi*freq*t) for the duration.
func Sine(rate int, freq, amp, seconds float64) []float64 {
	x := make([]float64, Frames(rate, seconds))
	for i := range x {
		x[i] = float64(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return x
}

// Noise is Gaussian white noise of standard deviation sigma from seed.
func Noise(seed uint64, n int, sigma float64) []float64 {
	r := dsp.NewRand(seed)
	x := make([]float64, n)
	for i := range x {
		x[i] = float64(sigma * r.Norm())
	}
	return x
}

// Pink is approximately pink noise (Kellet's filter) of unit-order level from seed.
func Pink(seed uint64, n int) []float64 {
	r := dsp.NewRand(seed)
	x := make([]float64, n)
	var b0, b1, b2 float64
	for i := range x {
		w := r.Norm()
		b0 = float64(0.99765*b0) + float64(w*0.0990460)
		b1 = float64(0.96300*b1) + float64(w*0.2965164)
		b2 = float64(0.57000*b2) + float64(w*1.0526913)
		x[i] = float64(0.2 * (b0 + b1 + b2 + float64(w*0.1848)))
	}
	return x
}

// Clicks is a train of single-sample impulses of height amp at the given tempo: click k sits at sample round(k*60/bpm*rate).
func Clicks(rate int, bpm, amp, seconds float64) []float64 {
	x := make([]float64, Frames(rate, seconds))
	for k := 0; ; k++ {
		i := int(math.Floor(float64(k)*60/bpm*float64(rate) + 0.5))
		if i >= len(x) {
			return x
		}
		x[i] = amp
	}
}

// Silence is n zeros.
func Silence(n int) []float64 { return make([]float64, n) }

// Concat joins signals end to end.
func Concat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Scale returns g * x.
func Scale(x []float64, g float64) []float64 {
	y := make([]float64, len(x))
	for i, v := range x {
		y[i] = float64(g * v)
	}
	return y
}

// Offset returns x + d.
func Offset(x []float64, d float64) []float64 {
	y := make([]float64, len(x))
	for i, v := range x {
		y[i] = v + d
	}
	return y
}

// Stereo builds a buffer from two channels of equal length.
func Stereo(rate int, l, r []float64) pcm.Buffer {
	if len(l) != len(r) {
		panic("dsptest: channels differ in length")
	}
	return pcm.Buffer{Rate: rate, Ch: [2][]float64{append([]float64(nil), l...), append([]float64(nil), r...)}}
}

// Dual is x on both channels.
func Dual(rate int, x []float64) pcm.Buffer { return Stereo(rate, x, x) }
