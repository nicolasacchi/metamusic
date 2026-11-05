package pv

import (
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// Voice is one layer of a shift bank.
type Voice struct {
	Rho     func(outFrame int) float64 // pitch ratio per frame, in (0, MaxRho]; required
	Formant func(outFrame int) float64 // formant ratio per frame; nil or <= 0 follows the pitch
	Gain    func(outFrame int) float64 // linear gain per frame; nil is 1, and a frame at 0 is skipped (its state restarts)
}

// voiceState is the output-side memory of a voice: for every output bin of the previous frame, the analysis bin it came
// from (-1: none) and the phase rotation applied to it.
type voiceState struct {
	src, srcNext     []int32
	theta, thetaNext []float64
	best             []float64
	outRe, outIm     []float64
	acc              []float64
	started          bool
}

func newVoiceState(half, n int) *voiceState {
	v := &voiceState{
		src: make([]int32, half+1), srcNext: make([]int32, half+1),
		theta: make([]float64, half+1), thetaNext: make([]float64, half+1),
		best: make([]float64, half+1), outRe: make([]float64, half+1), outIm: make([]float64, half+1),
		acc: make([]float64, n),
	}
	v.reset()
	return v
}

func (st *voiceState) reset() {
	for i := range st.src {
		st.src[i] = -1
	}
	st.started = false
}

// Shift changes the pitch of x by the ratio rho(m) of each output frame without changing its length or timing: the peaks of
// every frame move to bin round(k*rho) with their regions, and their phase advances at the shifted frequency. With
// p.FormantRatio the spectral envelope is held (or moved) independently of the pitch. It returns nil on invalid parameters or
// when the meter stops the run; use ShiftErr to learn why.
func Shift(x []float64, rho func(outFrame int) float64, p Params) []float64 {
	y, err := ShiftErr(x, rho, p)
	if err != nil {
		return nil
	}
	return y
}

// ShiftErr is Shift with the error.
func ShiftErr(x []float64, rho func(outFrame int) float64, p Params) ([]float64, error) {
	out, err := ShiftBank(x, []Voice{{Rho: rho, Formant: p.FormantRatio}}, p)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// ShiftBank renders several pitch-shifted voices of x from one analysis per frame (one synthesis per voice) and returns one
// signal of len(x) per voice; the caller sums or pans them. A bank of one voice is exactly Shift. p.FormantRatio is not used:
// each voice has its own Formant.
func ShiftBank(x []float64, voices []Voice, p Params) ([][]float64, error) {
	p, err := p.norm()
	if err != nil {
		return nil, err
	}
	if len(voices) == 0 {
		return nil, fmt.Errorf("%w: no voices", ErrParam)
	}
	formant := false
	for i, v := range voices {
		if v.Rho == nil {
			return nil, fmt.Errorf("%w: voice %d has no Rho", ErrParam, i)
		}
		formant = formant || v.Formant != nil
	}
	if formant && p.Rate <= 0 {
		return nil, fmt.Errorf("%w: a formant ratio needs Params.Rate", ErrParam)
	}
	n, hs := p.N, p.Hs
	e := newEngine(n, hs)
	half := e.half
	states := make([]*voiceState, len(voices))
	for i := range states {
		states[i] = newVoiceState(half, len(x))
	}
	if len(x) == 0 {
		return accs(states), nil
	}
	wsum := make([]float64, len(x))
	cur, prev := newSpectrum(n), newSpectrum(n)
	var env *envelope
	if formant {
		env = newEnvelope(n, p.Rate)
	}
	var pk []Peak
	var phi, omega []float64
	frames := ceilDiv(len(x), hs) + 1
	for m := 0; m < frames; m++ {
		c := m * hs
		if err := p.Meter.Add(dsp.Units.FFT(n) + int64(10*n)); err != nil {
			return nil, err
		}
		e.analyse(x, c, cur)
		e.addWindowSum(wsum, c)
		pk = regions(true, cur.mag, pk)
		// per peak: analysis phase and instantaneous frequency (shared by every voice)
		phi, omega = phi[:0], omega[:0]
		for _, r := range pk {
			ph := math.Atan2(cur.im[r.Bin], cur.re[r.Bin])
			phi = append(phi, ph)
			w := trueFreq(r.Bin, n, math.Atan2(prev.im[r.Bin], prev.re[r.Bin]), ph, hs)
			if m == 0 {
				w = float64(2 * math.Pi * float64(r.Bin) / float64(n))
			}
			omega = append(omega, w)
		}
		envReady := false
		for vi, v := range voices {
			st := states[vi]
			g := 1.0
			if v.Gain != nil {
				g = v.Gain(m)
			}
			if g == 0 {
				st.reset()
				continue
			}
			rho := v.Rho(m)
			if !(rho > 0 && rho <= MaxRho) {
				return nil, fmt.Errorf("%w: rho(%d)=%v outside (0, %v]", ErrParam, m, rho, float64(MaxRho))
			}
			ratio := 0.0
			if v.Formant != nil {
				ratio = v.Formant(m)
			}
			units := dsp.Units.FFT(n) + int64(20*n)
			if ratio > 0 && !envReady {
				units += 2*dsp.Units.FFT(n) + int64(10*n)
			}
			if err := p.Meter.Add(units); err != nil {
				return nil, err
			}
			if ratio > 0 && !envReady {
				env.compute(cur.mag)
				envReady = true
			}
			st.frame(e, cur, prev, pk, phi, omega, rho, ratio, g, env, c)
		}
		cur, prev = prev, cur
	}
	for _, st := range states {
		normalise(st.acc, wsum)
	}
	return accs(states), nil
}

func accs(states []*voiceState) [][]float64 {
	out := make([][]float64, len(states))
	for i, s := range states {
		out[i] = s.acc
	}
	return out
}

// frame renders one voice's synthesis frame from the analysed spectrum cur (prev is the previous analysis frame).
func (st *voiceState) frame(e *engine, cur, prev *spectrum, pk []Peak, phi, omega []float64, rho, ratio, gain float64, env *envelope, c int) {
	half := e.half
	for k := 0; k <= half; k++ {
		st.outRe[k], st.outIm[k], st.best[k] = 0, 0, -1
		st.srcNext[k], st.thetaNext[k] = -1, 0
	}
	for i, r := range pk {
		p := r.Bin
		pp := int(math.Floor(float64(float64(p)*rho) + 0.5))
		delta := pp - p
		psi := phi[i]
		if st.started && pp >= 0 && pp <= half && st.src[pp] >= 0 {
			s := int(st.src[pp])
			psiPrev := math.Atan2(prev.im[s], prev.re[s]) + st.theta[pp]
			psi = psiPrev + float64(float64(e.hs)*float64(rho*omega[i]))
		}
		th := princarg(psi - phi[i])
		cs, sn := math.Cos(th), math.Sin(th)
		// the formant correction is one factor per region, taken at the peak: the lobe keeps its shape
		f := gain
		if ratio > 0 {
			f = float64(f * env.gain(p, min(max(pp, 0), half), ratio))
		}
		for k := r.Lo; k <= r.Hi; k++ {
			kk := k + delta
			if kk < 0 || kk > half {
				continue
			}
			mag := float64(cur.mag[k] * f)
			if mag <= st.best[kk] {
				continue
			}
			st.best[kk] = mag
			st.outRe[kk] = float64(f * (float64(cur.re[k]*cs) - float64(cur.im[k]*sn)))
			st.outIm[kk] = float64(f * (float64(cur.re[k]*sn) + float64(cur.im[k]*cs)))
			st.srcNext[kk] = int32(k) // #nosec G115 -- k <= N/2
			st.thetaNext[kk] = th
		}
	}
	e.synthesise(st.outRe, st.outIm, st.acc, c)
	st.src, st.srcNext = st.srcNext, st.src
	st.theta, st.thetaNext = st.thetaNext, st.theta
	st.started = true
}
