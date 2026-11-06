package pv

import (
	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// envelope holds the cepstrally smoothed log-magnitude spectrum of one frame.
type envelope struct {
	logE    []float64 // bins 0..n/2
	re, im  []float64 // scratch of length n
	lifter  int       // quefrency cut-off in samples (1 ms)
	n, half int
}

func newEnvelope(n, rate int) *envelope {
	q := rate / 1000
	if q < 1 {
		q = 1
	}
	if q > n/2-1 {
		q = n/2 - 1
	}
	return &envelope{logE: make([]float64, n/2+1), re: make([]float64, n), im: make([]float64, n), lifter: q, n: n, half: n / 2}
}

// compute fills logE from the magnitude spectrum: the log spectrum is made symmetric, transformed to the cepstrum, cut at
// the lifter quefrency (rectangular, both sides) and transformed back.
func (v *envelope) compute(mag []float64) {
	var top float64
	for _, m := range mag {
		if m > top {
			top = m
		}
	}
	eps := float64(1e-10*top) + 1e-300
	n, half := v.n, v.half
	for k := 0; k <= half; k++ {
		l := dsp.Log(mag[k] + eps)
		v.re[k] = l
		if k > 0 && k < half {
			v.re[n-k] = l
		}
	}
	for i := range v.im {
		v.im[i] = 0
	}
	dsp.FFT(v.re, v.im) // real and symmetric: the cepstrum times n
	inv := 1 / float64(n)
	for q := 0; q < n; q++ {
		if q > v.lifter && q < n-v.lifter {
			v.re[q], v.im[q] = 0, 0
			continue
		}
		v.re[q] = float64(v.re[q] * inv)
		v.im[q] = float64(v.im[q] * inv)
	}
	dsp.FFT(v.re, v.im)
	copy(v.logE, v.re[:half+1])
}

// logAt is the log envelope at fractional bin x, linear between bins and held at the edges.
func (v *envelope) logAt(x float64) float64 {
	if x <= 0 {
		return v.logE[0]
	}
	if x >= float64(v.half) {
		return v.logE[v.half]
	}
	i := int(x)
	f := x - float64(i)
	return float64(v.logE[i]*(1-f)) + float64(v.logE[i+1]*f)
}

// Gain limits of the formant correction, in dB: a frame whose envelope is nearly empty must not blow up.
const maxFormantGain = 100.0

// gain is the magnitude factor that turns bin k of the analysed frame, once its content has been moved to output bin kk, into
// the spectrum whose envelope is the original envelope moved by phi: E(kk/phi)/E(k) (the fine structure divided by its own
// envelope and multiplied by the envelope at the new place, FormantWarp of C203.4).
func (v *envelope) gain(k, kk int, phi float64) float64 {
	lg := v.logAt(float64(kk)/phi) - v.logE[k]
	g := dsp.Exp(lg)
	if g > maxFormantGain {
		return maxFormantGain
	}
	if g < 1/maxFormantGain {
		return 1 / maxFormantGain
	}
	return g
}

// FormantWarp returns the magnitude spectrum of a frame with its spectral envelope moved by phi (formants at f * phi) and
// its fine structure (harmonics) left where it is. mag holds bins 0..n/2 of a frame of n = 2*(len(mag)-1) points at the given
// rate. Shift applies the same correction to the bins it has moved; this stand-alone form is for tests and tools.
func FormantWarp(mag []float64, rate int, phi float64) []float64 {
	n := 2 * (len(mag) - 1)
	out := make([]float64, len(mag))
	if n < 4 || phi <= 0 {
		copy(out, mag)
		return out
	}
	env := newEnvelope(n, rate)
	env.compute(mag)
	for k := range mag {
		out[k] = float64(mag[k] * env.gain(k, k, phi))
	}
	return out
}
