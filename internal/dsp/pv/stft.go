// Package pv is the phase vocoder of the T1 transforms (C203.4): time stretch with a per-frame ratio, pitch shift by peak
// remapping (a bank of voices shares one analysis), and a cepstral formant warp. Everything is single-goroutine float64 code
// on the deterministic primitives of internal/dsp (own FFT, no banned math, products written float64(a*b)), metered in
// dsp.Meter units.
//
// Frames are centred: frame m of the output is centred on sample m*Hs, windows are periodic Hann on both sides, the
// overlap-add is divided by the exact window-square sum, and the FFT is taken with the frame rotated by N/2 so that bin
// phases refer to the frame centre (this is what makes shifting a spectral region by whole bins phase-consistent).
//
// The plain-signature entry points (Stretch, Shift) return nil when the parameters are invalid or the meter stops the run;
// the *Err variants return the reason (dsp.ErrBudget, the context error, ErrParam) and are what operators should call.
package pv

import (
	"errors"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// ErrParam is returned (wrapped) for an invalid parameter: frame size, ratio out of range, missing sample rate.
var ErrParam = errors.New("pv: invalid parameter")

// DefaultN is the frame size used when Params.N is zero.
const DefaultN = 4096

// Ratio limits. Outside them the per-bin phase estimate is no longer unambiguous (stretch) or the result is not music.
const (
	MinAlpha = 0.25
	MaxAlpha = 8
	MaxRho   = 64
)

// Params configures a run. The zero value means N = 4096, Hs = N/4, no phase locking, unmetered.
type Params struct {
	N, Hs int  // frame size (a power of two, at least 64) and synthesis hop (default N/4, at most N/2)
	Lock  bool // identity phase locking (Laroche-Dolson); without it every bin keeps its own phase
	// Rate is the sample rate in Hz. Only the formant warp needs it (the lifter is 1 ms of quefrency); it is an error to
	// enable a formant ratio without it.
	Rate int
	// FormantRatio, for Shift, gives the formant ratio per output frame: formants move by this factor of their original
	// frequency whatever the pitch ratio does. A value <= 0, or a nil function, means "follow the pitch" (no correction).
	FormantRatio func(outFrame int) float64
	Meter        *dsp.Meter
}

func (p Params) norm() (Params, error) {
	if p.N == 0 {
		p.N = DefaultN
	}
	if p.N < 64 || p.N&(p.N-1) != 0 {
		return p, fmt.Errorf("%w: N=%d is not a power of two >= 64", ErrParam, p.N)
	}
	if p.Hs == 0 {
		p.Hs = p.N / 4
	}
	if p.Hs < 1 || p.Hs > p.N/2 {
		return p, fmt.Errorf("%w: Hs=%d outside 1..N/2", ErrParam, p.Hs)
	}
	return p, nil
}

// hann is the periodic Hann window of n points.
func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = float64(0.5 - float64(0.5*math.Cos(2*math.Pi*float64(i)/float64(n))))
	}
	return w
}

// spectrum is one analysed frame: bins 0..n/2 of the centre-referenced FFT, and their magnitudes.
type spectrum struct {
	re, im, mag []float64
}

func newSpectrum(n int) *spectrum {
	return &spectrum{re: make([]float64, n), im: make([]float64, n), mag: make([]float64, n/2+1)}
}

// engine holds the window and scratch buffers of one run.
type engine struct {
	n, half, hs int
	win         []float64
	sre, sim    []float64 // synthesis scratch
}

func newEngine(n, hs int) *engine {
	return &engine{n: n, half: n / 2, hs: hs, win: hann(n), sre: make([]float64, n), sim: make([]float64, n)}
}

// analyse windows x around centre c (zeros outside the signal), rotates the frame by N/2, transforms it and fills s.
func (e *engine) analyse(x []float64, c int, s *spectrum) {
	n := e.n
	for i := 0; i < n; i++ {
		j := c - e.half + i
		v := 0.0
		if j >= 0 && j < len(x) {
			v = float64(x[j] * e.win[i])
		}
		s.re[(i+e.half)&(n-1)] = v
		s.im[i] = 0
	}
	dsp.FFT(s.re, s.im)
	for k := 0; k <= e.half; k++ {
		s.mag[k] = math.Sqrt(float64(s.re[k]*s.re[k]) + float64(s.im[k]*s.im[k]))
	}
}

// synthesise inverse-transforms the half spectrum (re, im), windows it and overlap-adds it into acc around centre c.
func (e *engine) synthesise(re, im []float64, acc []float64, c int) {
	n, half := e.n, e.half
	for k := 0; k <= half; k++ {
		e.sre[k], e.sim[k] = re[k], im[k]
	}
	e.sim[0], e.sim[half] = 0, 0
	for k := 1; k < half; k++ {
		e.sre[n-k], e.sim[n-k] = re[k], -im[k]
	}
	dsp.IFFT(e.sre, e.sim)
	for i := 0; i < n; i++ {
		j := c - half + i
		if j < 0 || j >= len(acc) {
			continue
		}
		acc[j] += float64(e.sre[(i+half)&(n-1)] * e.win[i])
	}
}

// addWindowSum adds the window-square sum of a frame centred on c into wsum.
func (e *engine) addWindowSum(wsum []float64, c int) {
	for i := 0; i < e.n; i++ {
		if j := c - e.half + i; j >= 0 && j < len(wsum) {
			wsum[j] += float64(e.win[i] * e.win[i])
		}
	}
}

// normalise divides acc by the window-square sum.
func normalise(acc, wsum []float64) {
	for i := range acc {
		if wsum[i] > 1e-9 {
			acc[i] = float64(acc[i] / wsum[i])
		} else {
			acc[i] = 0
		}
	}
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }
