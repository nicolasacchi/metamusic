package pv

import "math"

// princarg wraps a phase to [-pi, pi).
func princarg(x float64) float64 {
	return x - float64(2*math.Pi*math.Floor(x/(2*math.Pi)+0.5))
}

// trueFreq is the instantaneous frequency in rad/sample of bin k of an n-point frame, from the phases ph1 (previous frame)
// and ph2 (this frame) taken h samples apart: the bin centre plus the wrapped deviation of the phase advance from it.
func trueFreq(k, n int, ph1, ph2 float64, h int) float64 {
	wk := float64(2 * math.Pi * float64(k) / float64(n))
	if h <= 0 {
		return wk
	}
	dev := princarg(ph2 - ph1 - float64(wk*float64(h)))
	return wk + float64(dev/float64(h))
}

// regions returns the units that carry one phase each: the peaks with their regions of influence when locking, otherwise
// every bin alone (so unlocked vocoding is the same code with regions of one bin).
func regions(lock bool, mag []float64, buf []Peak) []Peak {
	if lock {
		return FindPeaks(mag, PeakFloor, buf)
	}
	buf = buf[:0]
	for k := range mag {
		buf = append(buf, Peak{Bin: k, Lo: k, Hi: k})
	}
	return buf
}
