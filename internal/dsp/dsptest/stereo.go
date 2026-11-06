package dsptest

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// Stereo test signals of the hi-fi transforms (C225.5). All are generated, deterministic and committed as nothing.

// mixCorr returns a and r = rho*a + sqrt(1-rho^2)*b', where b' is b scaled to a's RMS, so that r has a's level and the
// Pearson coefficient of (a, r) is rho for independent a and b of long enough length.
func mixCorr(a, b []float64, rho float64) (l, r []float64) {
	if rho < -1 || rho > 1 {
		panic("dsptest: correlation outside [-1, 1]")
	}
	g := 1.0
	if rb := RMS(b); rb > 0 {
		g = RMS(a) / rb
	}
	k := math.Sqrt(1 - float64(rho*rho))
	r = make([]float64, len(a))
	for i := range a {
		r[i] = float64(rho*a[i]) + float64(k*float64(g*b[i]))
	}
	return append([]float64(nil), a...), r
}

// Correlated is a pair of Gaussian noises of standard deviation sigma and Pearson correlation rho.
func Correlated(seed uint64, n int, sigma, rho float64) (l, r []float64) {
	return mixCorr(Noise(seed, n, sigma), Noise(seed+1, n, sigma), rho)
}

// PinkStereo is a stereo pair of pink noises of the given duration with Pearson correlation rho (1 is dual mono, -1 is
// polarity inverted), at a level of the order of Pink.
func PinkStereo(rate int, seed uint64, seconds, rho float64) pcm.Buffer {
	n := Frames(rate, seconds)
	l, r := mixCorr(Pink(seed, n), Pink(seed+1, n), rho)
	return Stereo(rate, l, r)
}

// HardPanned is a sine on one channel only (left when left is true) and silence on the other.
func HardPanned(rate int, freq, amp, seconds float64, left bool) pcm.Buffer {
	x := Sine(rate, freq, amp, seconds)
	z := Silence(len(x))
	if left {
		return Stereo(rate, x, z)
	}
	return Stereo(rate, z, x)
}

// Pearson is the Pearson correlation coefficient of two equally long signals (0 when either is constant).
func Pearson(l, r []float64) float64 {
	n := float64(len(l))
	ml, mr := Mean(l), Mean(r)
	var sll, srr, slr float64
	for i := range l {
		a, b := l[i]-ml, r[i]-mr
		sll += float64(a * a)
		srr += float64(b * b)
		slr += float64(a * b)
	}
	if sll == 0 || srr == 0 || n == 0 {
		return 0
	}
	return slr / math.Sqrt(float64(sll*srr))
}

// RealWorldShape builds the audio of the FixtureT1 stereo rows by label: MONO is a near-mono pair (correlation about 0.98:
// the same pink noise plus a little independent noise), WIDE a wide one (about 0.1) and OOP an out-of-phase one (about -0.5).
// Any other label panics. The audio is generated at test time; nothing is committed.
func RealWorldShape(label string, rate int, seconds float64, seed uint64) pcm.Buffer {
	switch label {
	case "MONO":
		return PinkStereo(rate, seed, seconds, 0.98)
	case "WIDE":
		return PinkStereo(rate, seed, seconds, 0.1)
	case "OOP":
		return PinkStereo(rate, seed, seconds, -0.5)
	}
	panic("dsptest: unknown stereo shape " + label)
}
