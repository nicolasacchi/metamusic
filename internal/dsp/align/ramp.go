package align

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// Ramp is the tempo ramp of a morph transition (C204.6): the local tempo is T(b) = T0 * r^(b/B) with r = T1/T0 over B
// beats, a geometric ramp in beats. T0, T1 and Beats must be positive.
type Ramp struct{ T0, T1, Beats float64 }

// seriesEps is the |x| below which (1 - e^-x)/x and -ln(1 - x)/x use their Taylor series: the closed forms lose digits to
// cancellation there, and the series truncation error (x^4/120 relative) is below 1e-14.
const seriesEps = 1e-3

// lnR is ln(T1/T0).
func (r Ramp) lnR() float64 { return dsp.Log(r.T1 / r.T0) }

// Time is the output time in seconds at which beat beta of the ramp is reached: 60 B (1 - r^(-b/B)) / (T0 ln r), or 60 b /
// T0 when r = 1 (no division by zero: the expression is written as 60 b / T0 * (1 - e^-x)/x with x = b ln r / B).
func (r Ramp) Time(beta float64) float64 {
	x := beta * r.lnR() / r.Beats
	var g float64 // (1 - e^-x) / x
	if math.Abs(x) < seriesEps {
		g = 1 - x/2 + x*x/6 - x*x*x/24 + x*x*x*x/120
	} else {
		g = (1 - dsp.Exp(-x)) / x
	}
	return 60 * beta / r.T0 * g
}

// BeatAt is the inverse of Time: the beat reached at output time tau. For a ramp that slows down (T1 < T0) every tau has a
// beat; for one that speeds up, tau beyond 60 B / (T0 ln r) would need a negative argument of the logarithm and the answer
// is +Inf (no time of a real transition gets there: Total is always below it).
func (r Ramp) BeatAt(tau float64) float64 {
	z := tau * r.T0 * r.lnR() / (60 * r.Beats) // 1 - e^-s with s = beta ln r / B
	var h float64                              // -ln(1 - z) / z
	switch {
	case z >= 1:
		return math.Inf(1)
	case math.Abs(z) < seriesEps:
		h = 1 + z/2 + z*z/3 + z*z*z/4 + z*z*z*z/5
	default:
		h = -dsp.Log(1-z) / z
	}
	return tau * r.T0 / 60 * h
}

// Alpha is the stretch factor of the track that starts at T0 at output time tau: r^(-BeatAt(tau)/B), 1 at the start and
// T0/T1 at Total.
func (r Ramp) Alpha(tau float64) float64 {
	return dsp.Exp(-r.lnR() * r.BeatAt(tau) / r.Beats)
}

// Total is the duration of the whole ramp in seconds, Time(Beats).
func (r Ramp) Total() float64 { return r.Time(r.Beats) }

// TempoAt is the local tempo in BPM at beat beta.
func (r Ramp) TempoAt(beta float64) float64 {
	return r.T0 * dsp.Exp(r.lnR()*beta/r.Beats)
}
