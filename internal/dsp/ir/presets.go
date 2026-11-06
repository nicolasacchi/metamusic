// Package ir builds the generated impulse responses of the T1 ensemble (C206): a late tail of band-split noise with
// frequency-dependent decay, image-source early reflections of a shoebox room, both channels decorrelated, unit energy. The
// build is deterministic for a seed on every host (own FFT, dsp.Exp/Pow, PCG noise) and metered.
package ir

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// Params describes a room (C206.1). RT60 is the mid-band reverberation time in seconds, PredelayMS the gap before the tail,
// Damping how much faster the highs decay than the lows (0..1), SizeM the longest room dimension in metres and Width the
// stereo width of the receivers and of the tail (0..1).
type Params struct {
	RT60       float64
	PredelayMS float64
	Damping    float64
	SizeM      float64
	Width      float64
}

// The four room presets of C206.1, in canonical order.
var presetTable = []struct {
	name string
	p    Params
}{
	{"cathedral", Params{RT60: 9, PredelayMS: 40, Damping: 0.45, SizeM: 60, Width: 0.9}},
	{"cistern", Params{RT60: 18, PredelayMS: 15, Damping: 0.8, SizeM: 30, Width: 0.6}},
	{"hangar", Params{RT60: 5, PredelayMS: 25, Damping: 0.15, SizeM: 120, Width: 0.7}},
	{"canyon", Params{RT60: 12, PredelayMS: 120, Damping: 0.3, SizeM: 250, Width: 1.0}},
}

// Preset returns the named room preset.
func Preset(name string) (Params, bool) {
	for _, e := range presetTable {
		if e.name == name {
			return e.p, true
		}
	}
	return Params{}, false
}

// PresetNames lists the presets in canonical order.
func PresetNames() []string {
	out := make([]string, len(presetTable))
	for i, e := range presetTable {
		out[i] = e.name
	}
	return out
}

// Pan is the constant-power pan law: p in [-1, 1] (left to right, clamped) gives gains with l*l + r*r = 1.
func Pan(p float64) (l, r float64) {
	p = math.Max(-1, math.Min(1, p))
	th := (p + 1) * math.Pi / 4
	return math.Cos(th), math.Sin(th)
}

// Mid-band reference of the damping law (C206.2) and the decay constant of -60 dB in amplitude.
const (
	dampRefHz = 500.0
	ln1000    = 6.9078
)

// BandEdgesHz are the octave band edges of the tail (C206.2): seven bands, the first below the first edge, the last above
// the last.
var BandEdgesHz = [6]float64{125, 250, 500, 1000, 2000, 4000}

// bandCentreHz is the geometric centre of band b (the outer bands use one octave beyond their edge).
func bandCentreHz(b int) float64 {
	switch {
	case b == 0:
		return BandEdgesHz[0] / math.Sqrt2
	case b == len(BandEdgesHz):
		return BandEdgesHz[len(BandEdgesHz)-1] * math.Sqrt2
	}
	return math.Sqrt(BandEdgesHz[b-1] * BandEdgesHz[b])
}

// bandRT60 is the decay time of band b: RT60 * (f/500)^(-0.4*damping), clamped to [0.1, 1.5] RT60.
func (p Params) bandRT60(b int) float64 {
	rt := p.RT60 * dsp.Pow(bandCentreHz(b)/dampRefHz, -0.4*p.Damping)
	return math.Max(0.1*p.RT60, math.Min(1.5*p.RT60, rt))
}
