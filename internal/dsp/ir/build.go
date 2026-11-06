package ir

import (
	"errors"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// ErrParam is returned (wrapped) by BuildErr for parameters outside their domain.
var ErrParam = errors.New("ir: invalid parameter")

// Info describes a built response: EffectiveLen is the first sample after which the backward-integrated energy stays below
// -70 dB (the returned slices are exactly that long), RT60Measured the T20 of the Schroeder curve extrapolated to 60 dB.
type Info struct {
	EffectiveLen int
	RT60Measured float64
}

const (
	tailOnsetS  = 0.030 // raised-cosine rise of the tail after the pre-delay
	erEnergy    = 0.15  // early reflections carry this fraction of the tail's energy before normalisation
	cutoffDB    = -70.0
	tailMargin  = 1.1667 // -70 dB is reached after 70/60 of a decay time (energy of an exponential decay)
	edgeOctaves = 0.25   // half-width of the band-edge crossfade, in octaves
	reanchor    = 1024   // the per-sample decay is a recurrence, re-anchored on dsp.Exp this often
)

// Build is BuildErr for callers that treat a failure (invalid parameters, the meter's budget or context) as no result: it
// returns nil slices.
func Build(p Params, rate int, seed int64, m *dsp.Meter) (l, r []float64, info Info) {
	l, r, info, err := BuildErr(p, rate, seed, m)
	if err != nil {
		return nil, nil, Info{}
	}
	return l, r, info
}

func (p Params) check(rate int) error {
	switch {
	case rate < 8000 || rate > 192000:
		return fmt.Errorf("%w: rate %d", ErrParam, rate)
	case !(p.RT60 >= 0.1 && p.RT60 <= 60):
		return fmt.Errorf("%w: rt60 %v", ErrParam, p.RT60)
	case !(p.PredelayMS >= 0 && p.PredelayMS <= 500):
		return fmt.Errorf("%w: predelay %v", ErrParam, p.PredelayMS)
	case !(p.Damping >= 0 && p.Damping <= 1):
		return fmt.Errorf("%w: damping %v", ErrParam, p.Damping)
	case !(p.SizeM >= 1 && p.SizeM <= 1000):
		return fmt.Errorf("%w: size %v", ErrParam, p.SizeM)
	case !(p.Width >= 0 && p.Width <= 1):
		return fmt.Errorf("%w: width %v", ErrParam, p.Width)
	}
	return nil
}

// tailFrames is the length of the generated tail: the pre-delay plus 70/60 of the slowest band's decay.
func (p Params) tailFrames(rate int) int {
	slow := 0.0
	for b := 0; b <= len(BandEdgesHz); b++ {
		slow = math.Max(slow, p.bandRT60(b))
	}
	return int(math.Ceil((p.PredelayMS/1000 + tailMargin*slow + tailOnsetS) * float64(rate)))
}

// TailBound is the number of frames the generated tail has before it is trimmed to Info.EffectiveLen: an upper bound of the
// length of the returned responses, for cost models that cannot build one.
func TailBound(p Params, rate int) int { return p.tailFrames(rate) }

// meterBlock is the granularity of the work meter inside the long passes: at most this many elements between two checks.
const meterBlock = 8192

// blocks runs fn over [0, n) in blocks of meterBlock elements and charges perElem units for each element before its block.
func blocks(m *dsp.Meter, n int, perElem int64, fn func(lo, hi int)) error {
	for lo := 0; lo < n; lo += meterBlock {
		hi := min(lo+meterBlock, n)
		if err := m.Add(perElem * int64(hi-lo)); err != nil {
			return err
		}
		fn(lo, hi)
	}
	return nil
}

// Units charged per element by the passes of BuildErr.
const (
	unitsNoise   = 40 // two noise streams and the width mix, per tail sample
	unitsPack    = 8  // masking one bin of the band spectrum
	unitsClear   = 1  // clearing one point of the transform buffers
	unitsDecay   = 12 // envelope and accumulation of both channels, per tail sample and band
	unitsAnalyse = 12 // Schroeder curves, early reflections, measurement and normalisation, per tail sample
)

// Units is exactly what BuildErr adds to the meter for p at rate: the noise, one forward and one inverse transform per band
// of the whole noise (both channels share each transform), the band passes and the measurement.
func Units(p Params, rate int) int64 {
	nt := p.tailFrames(rate)
	n := dsp.NextPow2(nt)
	masks := newBandMasks(n, rate)
	total := unitsNoise*int64(nt) + dsp.Units.FFT(n)
	for b := 0; b <= len(BandEdgesHz); b++ {
		lo, hi := masks.span(b)
		total += dsp.Units.FFT(n) + unitsClear*int64(n) + unitsPack*int64(hi-lo+1) + unitsDecay*int64(nt)
	}
	return total + unitsAnalyse*int64(nt)
}

// BuildErr builds the left and right responses for p at rate from seed (C206.2-C206.4) and returns the meter's error
// (dsp.ErrBudget or the context's) when it stops the work. Both channels have unit energy.
func BuildErr(p Params, rate int, seed int64, m *dsp.Meter) (l, r []float64, info Info, err error) {
	if err = p.check(rate); err != nil {
		return nil, nil, Info{}, err
	}
	nt := p.tailFrames(rate)
	n := dsp.NextPow2(nt)
	// Two independent noise streams, packed as one complex signal z = nL + i nR. Width: the right tail takes a share
	// c = (1-width)^2 of the left noise (c = 0 at full width: independent tails).
	zr, zi := make([]float64, n), make([]float64, n)
	s2 := uint64(seed) * 2 // #nosec G115 -- the bits of the seed are the stream id; wrap-around is intended
	rl, rr := dsp.NewRand(s2), dsp.NewRand(s2+1)
	c := (1 - p.Width) * (1 - p.Width)
	sc := math.Sqrt(1 - c*c)
	err = blocks(m, nt, unitsNoise, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			zr[i] = rl.Float64() - 0.5
			zi[i] = rr.Float64() - 0.5
			if c > 0 {
				zi[i] = float64(c*zr[i]) + float64(sc*zi[i])
			}
		}
	})
	if err != nil {
		return nil, nil, Info{}, err
	}
	if err = m.Add(dsp.Units.FFT(n)); err != nil {
		return nil, nil, Info{}, err
	}
	dsp.FFT(zr, zi)
	half := n/2 + 1
	// Separate the two real signals: ZL(k) = (Z(k) + conj Z(n-k)) / 2, ZR(k) = (Z(k) - conj Z(n-k)) / 2i.
	lr, li, rrr, rri := make([]float64, half), make([]float64, half), make([]float64, half), make([]float64, half)
	for k := 0; k < half; k++ {
		j := (n - k) & (n - 1)
		lr[k] = 0.5 * (zr[k] + zr[j])
		li[k] = 0.5 * (zi[k] - zi[j])
		rrr[k] = 0.5 * (zi[k] + zi[j])
		rri[k] = -0.5 * (zr[k] - zr[j])
	}
	outL, outR := make([]float64, nt), make([]float64, nt)
	mask := newBandMasks(n, rate)
	pre := int(math.Round(p.PredelayMS / 1000 * float64(rate)))
	for b := 0; b <= len(BandEdgesHz); b++ {
		if err = m.Add(dsp.Units.FFT(n)); err != nil {
			return nil, nil, Info{}, err
		}
		if err = blocks(m, n, unitsClear, func(lo, hi int) {
			clear(zr[lo:hi])
			clear(zi[lo:hi])
		}); err != nil {
			return nil, nil, Info{}, err
		}
		lo0, hi0 := mask.span(b)
		err = blocks(m, hi0-lo0+1, unitsPack, func(lo, hi int) {
			for k := lo0 + lo; k < lo0+hi; k++ {
				g := mask.at(b, k)
				if g == 0 {
					continue
				}
				ylr, yli, yrr, yri := float64(g*lr[k]), float64(g*li[k]), float64(g*rrr[k]), float64(g*rri[k])
				zr[k], zi[k] = ylr-yri, yli+yrr
				if k > 0 && k < n-k {
					zr[n-k], zi[n-k] = ylr+yri, yrr-yli
				}
			}
		})
		if err != nil {
			return nil, nil, Info{}, err
		}
		dsp.IFFT(zr, zi)
		rt, w := p.bandRT60(b), bandWeight(b, rate)
		err = blocks(m, nt, unitsDecay, func(lo, hi int) {
			decay(zr, outL, lo, hi, pre, rt, rate, w)
			decay(zi, outR, lo, hi, pre, rt, rate, w)
		})
		if err != nil {
			return nil, nil, Info{}, err
		}
	}
	if err = m.Add(unitsAnalyse * int64(nt)); err != nil {
		return nil, nil, Info{}, err
	}
	// Early reflections, scaled so their energy is erEnergy of the tail's.
	erL, erR := earlyReflections(p, rate)
	te, ee := energy(outL)+energy(outR), energy(erL)+energy(erR)
	if ee > 0 {
		g := math.Sqrt(erEnergy * te / ee)
		for i := range erL {
			if i < nt {
				outL[i] += float64(g * erL[i])
				outR[i] += float64(g * erR[i])
			}
		}
	}
	info.RT60Measured = rt60Measured(outL, outR, rate)
	info.EffectiveLen = effectiveLen(outL, outR)
	l, r = outL[:info.EffectiveLen], outR[:info.EffectiveLen]
	normalise(l)
	normalise(r)
	return l, r, info, nil
}

// bandEnergy is the share of each octave band in the tail's energy, whatever the sample rate (white noise would put most of
// it in the top band, which decays fastest, and the response would not decay with the mid-band RT60). The mid bands carry
// the most, the outer ones the least.
var bandEnergy = [len(BandEdgesHz) + 1]float64{0.1, 0.4, 0.9, 1.3, 1.4, 1.1, 0.7}

// bandWeight scales a band's noise to bandEnergy: the noise is white, so the band holds its bandwidth's share of the
// spectrum up to the Nyquist frequency before the weight.
func bandWeight(b, rate int) float64 {
	nyq := float64(rate) / 2
	lo, hi := 0.0, nyq
	if b > 0 {
		lo = BandEdgesHz[b-1]
	}
	if b < len(BandEdgesHz) {
		hi = BandEdgesHz[b]
	}
	hi = math.Min(hi, nyq)
	if hi <= lo {
		return 0
	}
	return math.Sqrt(bandEnergy[b] / ((hi - lo) / nyq))
}

// decay adds w * x(t) * exp(-ln1000 t / rt) * onset(t) to dst[i] for i in [lo, hi) with t = i - pre >= 0. The envelope is a
// recurrence re-anchored on dsp.Exp every reanchor samples, so a block starting anywhere gives the same bits as a whole pass.
func decay(x, dst []float64, lo, hi, pre int, rt float64, rate int, w float64) {
	if w == 0 {
		return
	}
	k := ln1000 / (rt * float64(rate))
	g := dsp.Exp(-k)
	onset := int(tailOnsetS * float64(rate))
	var e float64
	for i := max(lo, pre); i < hi; i++ {
		t := i - pre
		if t%reanchor == 0 || i == max(lo, pre) {
			e = dsp.Exp(-k * float64(t&^(reanchor-1)))
			for r := t &^ (reanchor - 1); r < t; r++ {
				e = float64(e * g)
			}
		}
		a := float64(w * e)
		if t < onset {
			a = float64(a * (0.5 * (1 - math.Cos(math.Pi*float64(t)/float64(onset)))))
		}
		dst[i] += float64(a * x[i])
		e = float64(e * g)
	}
}

func energy(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += float64(v * v)
	}
	return s
}

func normalise(x []float64) {
	e := energy(x)
	if e <= 0 {
		return
	}
	g := 1 / math.Sqrt(e)
	for i := range x {
		x[i] = float64(x[i] * g)
	}
}

// schroeder returns the backward-integrated energy of the pair (sum of both channels), non-increasing, ending at 0.
func schroeder(l, r []float64) []float64 {
	s := make([]float64, len(l))
	var acc float64
	for i := len(l) - 1; i >= 0; i-- {
		acc += float64(l[i]*l[i]) + float64(r[i]*r[i])
		s[i] = acc
	}
	return s
}

// effectiveLen is the first sample from which the backward-integrated energy stays below -70 dB of the total.
func effectiveLen(l, r []float64) int {
	s := schroeder(l, r)
	if len(s) == 0 || s[0] == 0 {
		return len(l)
	}
	thr := s[0] * dsp.Pow(10, cutoffDB/10)
	for i, v := range s {
		if v < thr {
			return max(i, 1)
		}
	}
	return len(l)
}

// rt60Measured is T20 of the Schroeder curve, extrapolated to 60 dB: three times the time between the -5 and -25 dB points.
func rt60Measured(l, r []float64, rate int) float64 {
	s := schroeder(l, r)
	if len(s) == 0 || s[0] == 0 {
		return 0
	}
	cross := func(db float64) float64 {
		thr := s[0] * dsp.Pow(10, db/10)
		for i, v := range s {
			if v < thr {
				return float64(i)
			}
		}
		return float64(len(s))
	}
	return 3 * (cross(-25) - cross(-5)) / float64(rate)
}
