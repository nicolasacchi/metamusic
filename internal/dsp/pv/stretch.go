package pv

import (
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// stretchPlan walks the analysis positions. a[m] is the (fractional) analysis position of output frame m, a[0] = 0 and
// a[m+1] = a[m] + Hs/alpha(m): the mapping is monotone and exact. length is the output length in samples: the synthesis time
// at which the analysis position reaches n, interpolated inside the last step and rounded, which for a constant alpha is
// exactly round(alpha*n). a has ceil(length/Hs)+1 entries.
func stretchPlan(n int, alpha func(int) float64, hs int) (a []float64, length int, err error) {
	if n <= 0 {
		return nil, 0, nil
	}
	if alpha == nil {
		return nil, 0, fmt.Errorf("%w: nil alpha", ErrParam)
	}
	pos := 0.0
	var tau float64
	m := 0
	for ; ; m++ {
		al := alpha(m)
		if !(al >= MinAlpha && al <= MaxAlpha) {
			return nil, 0, fmt.Errorf("%w: alpha(%d)=%v outside [%v, %v]", ErrParam, m, al, MinAlpha, float64(MaxAlpha))
		}
		step := float64(hs) / al
		if pos+step >= float64(n) {
			tau = float64(float64(m)*float64(hs)) + float64((float64(n)-pos)*al)
			break
		}
		a = append(a, pos)
		pos += step
	}
	length = int(math.Floor(tau + 0.5 + 1e-9)) // ties round up, whatever the rounding noise of the walk
	frames := ceilDiv(length, hs) + 1
	a = append(a, pos) // frame m of the break
	for ; len(a) < frames; m++ {
		al := alpha(m)
		if !(al >= MinAlpha && al <= MaxAlpha) {
			return nil, 0, fmt.Errorf("%w: alpha(%d)=%v outside [%v, %v]", ErrParam, m, al, MinAlpha, float64(MaxAlpha))
		}
		pos += float64(hs) / al
		a = append(a, pos)
	}
	return a, length, nil
}

// StretchLen is the exact output length of Stretch for n input samples: round(alpha*n) for a constant ratio, and for a
// varying one the closed-form walk of the analysis position (see stretchPlan). It is 0 when the parameters are invalid.
func StretchLen(n int, alpha func(outFrame int) float64, p Params) int {
	p, err := p.norm()
	if err != nil {
		return 0
	}
	_, l, err := stretchPlan(n, alpha, p.Hs)
	if err != nil {
		return 0
	}
	return l
}

// Stretch changes the duration of x without changing its pitch. alpha(m) is the stretch ratio of the m-th output frame
// (alpha > 1 slows down): the analysis hop of that step is Hs/alpha. It returns nil on invalid parameters or when the meter
// stops the run; use StretchErr to learn why.
func Stretch(x []float64, alpha func(outFrame int) float64, p Params) []float64 {
	y, err := StretchErr(x, alpha, p)
	if err != nil {
		return nil
	}
	return y
}

// StretchErr is Stretch with the error: ErrParam, dsp.ErrBudget or the meter's context error.
func StretchErr(x []float64, alpha func(outFrame int) float64, p Params) ([]float64, error) {
	p, err := p.norm()
	if err != nil {
		return nil, err
	}
	a, length, err := stretchPlan(len(x), alpha, p.Hs)
	if err != nil {
		return nil, err
	}
	acc := make([]float64, length)
	if length == 0 {
		return acc, nil
	}
	e := newEngine(p.N, p.Hs)
	wsum := make([]float64, length)
	cur, prev := newSpectrum(p.N), newSpectrum(p.N)
	theta := make([]float64, e.half+1)
	outRe, outIm := make([]float64, e.half+1), make([]float64, e.half+1)
	var rg []Peak
	prevPos := 0
	for m := range a {
		if err := p.Meter.Add(dsp.Units.PVFrame(p.N)); err != nil {
			return nil, err
		}
		pos := int(math.Floor(a[m] + 0.5))
		e.analyse(x, pos, cur)
		c := m * p.Hs
		e.addWindowSum(wsum, c)
		rg = regions(p.Lock, cur.mag, rg)
		for k := range outRe {
			outRe[k], outIm[k] = 0, 0
		}
		if len(rg) == 0 {
			for k := range theta {
				theta[k] = 0
			}
		}
		for _, r := range rg {
			phi := math.Atan2(cur.im[r.Bin], cur.re[r.Bin])
			psi := phi
			if m > 0 {
				phiPrev := math.Atan2(prev.im[r.Bin], prev.re[r.Bin])
				w := trueFreq(r.Bin, p.N, phiPrev, phi, pos-prevPos)
				psi = phiPrev + theta[r.Bin] + float64(float64(p.Hs)*w)
			}
			th := princarg(psi - phi)
			cs, sn := math.Cos(th), math.Sin(th)
			for k := r.Lo; k <= r.Hi; k++ {
				theta[k] = th
				outRe[k] = float64(cur.re[k]*cs) - float64(cur.im[k]*sn)
				outIm[k] = float64(cur.re[k]*sn) + float64(cur.im[k]*cs)
			}
		}
		e.synthesise(outRe, outIm, acc, c)
		cur, prev = prev, cur
		prevPos = pos
	}
	normalise(acc, wsum)
	return acc, nil
}
