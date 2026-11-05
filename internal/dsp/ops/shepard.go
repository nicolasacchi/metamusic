package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

// KindShepard is the registry name of the shepard (C205.4).
const KindShepard = "shepard"

func init() { Register(KindShepard, runShepard, shepardUnits) }

// pvWindow is the phase-vocoder frame of the operators (C203.4); the tests of this package use a smaller one at 16 kHz.
const pvWindow = 4096

// bankBudgetBytes bounds the voice signals one phase-vocoder bank call keeps alive: the bank returns one signal per voice, so
// a long output is rendered by groups of voices (each group re-analyses the input) rather than holding every voice at once.
// The sum over voices is taken in voice order whatever the grouping, so the result does not depend on it.
const bankBudgetBytes = 384 << 20

// voiceGroup is the number of voices rendered per bank call for signals of n samples.
func voiceGroup(n, voices int) int {
	return max(1, min(voices, bankBudgetBytes/(8*max(n, 1))))
}

// pvFrames is the number of frames of a bank call over n samples at hop hs.
func pvFrames(n, hs int) int64 { return int64(ceilDiv(n, hs) + 1) }

// ShepardParams are the resolved parameters of the operator. LoopS is the loop length in seconds; the parser derives it from
// loop_bars and the alignment tempo when loop_s is absent.
type ShepardParams struct {
	Direction string
	LoopS     float64
	StartS    float64
	Layers    int
	PeriodS   float64
	DurationS float64
	SigmaOct  float64 // bell width in octaves; 0 means layers/5
	N         int     // phase-vocoder frame; 0 means 4096 (tests use a smaller one at 16 kHz)
}

// ParseShepard reads the resolved params and the alignment object of job.json and checks the ranges of C202.3.
func ParseShepard(raw, alignment json.RawMessage) (ShepardParams, error) {
	in := struct {
		Direction *string  `json:"direction"`
		LoopBars  *int     `json:"loop_bars"`
		LoopS     *float64 `json:"loop_s"`
		StartS    *float64 `json:"start_s"`
		Layers    *int     `json:"layers"`
		PeriodS   *float64 `json:"period_s"`
		DurationS *float64 `json:"duration_s"`
		Sigma     *float64 `json:"bell_sigma_octaves"`
	}{}
	if err := parseParams(raw, &in); err != nil {
		return ShepardParams{}, err
	}
	p := ShepardParams{Direction: "up", Layers: 5, PeriodS: 24, DurationS: 120}
	if in.Direction != nil {
		p.Direction = *in.Direction
	}
	if in.StartS != nil {
		p.StartS = *in.StartS
	}
	if in.Layers != nil {
		p.Layers = *in.Layers
	}
	if in.PeriodS != nil {
		p.PeriodS = *in.PeriodS
	}
	if in.DurationS != nil {
		p.DurationS = *in.DurationS
	}
	if in.Sigma != nil {
		p.SigmaOct = *in.Sigma
	}
	switch {
	case in.LoopS != nil:
		p.LoopS = *in.LoopS
	case in.LoopBars != nil:
		var al struct {
			Inputs []struct {
				BPM         float64 `json:"bpm"`
				BeatsPerBar int     `json:"beats_per_bar"`
			} `json:"inputs"`
		}
		if err := parseParams(alignment, &al); err != nil {
			return ShepardParams{}, err
		}
		if len(al.Inputs) == 0 || !(al.Inputs[0].BPM > 0) {
			return ShepardParams{}, &ParamError{Field: "loop_s", Rule: RuleLoopSource}
		}
		bpb := al.Inputs[0].BeatsPerBar
		if bpb == 0 {
			bpb = 4
		}
		p.LoopS = float64(*in.LoopBars) * float64(bpb) * 60 / al.Inputs[0].BPM
	default:
		return ShepardParams{}, &ParamError{Field: "loop_s", Rule: RuleLoopSource}
	}
	return p, p.check()
}

// check is the operator's own sanity check: the service enforces the spec ranges of C202.3 (period 8-120 s and so on), the
// operator only refuses what it cannot render.
func (p ShepardParams) check() error {
	switch {
	case p.Direction != "up" && p.Direction != "down":
		return &ParamError{Field: "direction", Rule: RuleRange}
	case !(p.LoopS > 0 && p.LoopS <= 60):
		return &ParamError{Field: "loop_s", Rule: RuleRange}
	case !(p.StartS >= 0):
		return &ParamError{Field: "start_s", Rule: RuleRange}
	case p.Layers < 3 || p.Layers > 7:
		return &ParamError{Field: "layers", Rule: RuleRange}
	case !(p.PeriodS > 0):
		return &ParamError{Field: "period_s", Rule: RuleRange}
	case !(p.DurationS > 0 && p.DurationS <= 900):
		return &ParamError{Field: "duration_s", Rule: RuleRange}
	case p.SigmaOct != 0 && !(p.SigmaOct >= 0.1 && p.SigmaOct <= 4):
		return &ParamError{Field: "bell_sigma_octaves", Rule: RuleRange}
	}
	return nil
}

func (p ShepardParams) sigma() float64 {
	if p.SigmaOct > 0 {
		return p.SigmaOct
	}
	return float64(p.Layers) / 5
}

func (p ShepardParams) frame() int {
	if p.N > 0 {
		return p.N
	}
	return pvWindow
}

// offset is the pitch offset in octaves of layer j at output time tau: the layer climbs (or falls) one octave per period and
// wraps from the top of the window of `layers` octaves to its bottom.
func (p ShepardParams) offset(j int, tau float64) float64 {
	l := float64(p.Layers)
	v := tau/p.PeriodS + float64(j)
	v -= l * math.Floor(v/l)
	if p.Direction == "down" {
		return l/2 - v
	}
	return v - l/2
}

// weight is the bell of the layer at offset o octaves, brought to zero at the edge of the window by a raised cosine so that a
// layer that wraps from the top to the bottom is silent when it does.
func (p ShepardParams) weight(o float64) float64 {
	half := float64(p.Layers) / 2
	if math.Abs(o) >= half {
		return 0
	}
	z := o / p.sigma()
	return float64(dsp.Exp(-0.5*float64(z*z)) * (0.5 * (1 + math.Cos(math.Pi*o/half))))
}

// gains returns the normalised linear gain of every layer at output time tau: the bell weights scaled to constant power
// (the layers are different pitches, so their powers add).
func (p ShepardParams) gains(tau float64, dst []float64) []float64 {
	dst = dst[:0]
	var s float64
	for j := 0; j < p.Layers; j++ {
		w := p.weight(p.offset(j, tau))
		dst = append(dst, w)
		s += float64(w * w)
	}
	if s > 0 {
		g := 1 / math.Sqrt(s)
		for j := range dst {
			dst[j] = float64(dst[j] * g)
		}
	}
	return dst
}

// ShepardUnits is the work Shepard adds to the meter for outLen output samples: per channel, one analysis per frame for every
// group of voices, one synthesis per frame and voice, and one unit per sample for the tiled loop and for each layer summed. A layer at zero gain is skipped, so the real use can be a little
// lower. Safety is not included.
func ShepardUnits(outLen int64, layers, n int) int64 {
	frames := pvFrames(int(outLen), n/4)
	groups := int64(ceilDiv(layers, voiceGroup(int(outLen), layers)))
	pv := 2 * frames * (groups*(dsp.Units.FFT(n)+int64(10*n)) + int64(layers)*(dsp.Units.FFT(n)+int64(20*n)))
	return pv + 2*outLen*(1+int64(layers)) // the tiled loop and the sum of the layers, one unit per sample
}

func shepardUnits(_, out int64, _ int, raw json.RawMessage) int64 {
	layers := 5
	var in struct {
		Layers *int `json:"layers"`
	}
	if parseParams(raw, &in) == nil && in.Layers != nil {
		layers = *in.Layers
	}
	return ShepardUnits(out, layers, pvWindow)
}

// Shepard renders the endless glissando of x: a seamless loop of x at start_s, tiled to duration_s and read by `layers`
// pitch-shifted voices one octave apart whose offsets glide by one octave per period under a bell that fades them in at the
// bottom and out at the top. Time is unchanged; the output has exactly round(duration_s * rate) samples.
func Shepard(ctx context.Context, x pcm.Buffer, p ShepardParams, m *dsp.Meter) (pcm.Buffer, error) {
	if err := p.check(); err != nil {
		return pcm.Buffer{}, err
	}
	rate := x.Rate
	loopLen := roundHalfUp(p.LoopS * float64(rate))
	start := roundHalfUp(p.StartS * float64(rate))
	if start >= x.Frames() || loopLen < 2 {
		return pcm.Buffer{}, &ParamError{Field: "start_s", Rule: RuleRange}
	}
	outLen := roundHalfUp(p.DurationS * float64(rate))
	n := p.frame()
	hs := n / 4
	xfade := roundHalfUp(LoopCrossfadeS * float64(rate))
	gain := make([]float64, 0, p.Layers)
	group := voiceGroup(outLen, p.Layers)
	out := pcm.Buffer{Rate: rate}
	for c := 0; c < 2; c++ {
		cycle, err := tile(MakeLoop(x.Ch[c], start, loopLen, xfade), outLen, m)
		if err != nil {
			return pcm.Buffer{}, err
		}
		acc := make([]float64, outLen)
		for j0 := 0; j0 < p.Layers; j0 += group {
			if err := ctx.Err(); err != nil {
				return pcm.Buffer{}, err
			}
			voices := make([]pv.Voice, 0, group)
			for j := j0; j < min(j0+group, p.Layers); j++ {
				tau := func(frame int) float64 { return float64(frame) * float64(hs) / float64(rate) }
				voices = append(voices, pv.Voice{
					Rho: func(frame int) float64 { return dsp.Exp(math.Ln2 * p.offset(j, tau(frame))) },
					Gain: func(frame int) float64 {
						gain = p.gains(tau(frame), gain)
						return gain[j]
					},
				})
			}
			ys, err := pv.ShiftBank(cycle, voices, pv.Params{N: n, Hs: hs, Lock: true, Rate: rate, Meter: m})
			if err != nil {
				return pcm.Buffer{}, err
			}
			for _, y := range ys {
				if err := blocks(m, len(y), 1, func(lo, hi int) {
					for i := lo; i < hi; i++ {
						acc[i] += y[i]
					}
				}); err != nil {
					return pcm.Buffer{}, err
				}
			}
		}
		out.Ch[c] = acc
	}
	return out, nil
}

func runShepard(ctx context.Context, in RunIn) (RunOut, error) {
	p, err := ParseShepard(in.Params, in.Alignment)
	if err != nil {
		return RunOut{}, err
	}
	if len(in.Inputs) != 1 {
		return RunOut{}, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	x := in.Inputs[0]
	if x.Rate == 0 {
		x.Rate = in.Rate
	}
	out, err := Shepard(ctx, x, p, in.Meter)
	if err != nil {
		return RunOut{}, err
	}
	rep, _ := json.Marshal(map[string]any{"shepard": map[string]any{
		"loop_s": p.LoopS, "layers": p.Layers, "direction": p.Direction, "sigma_octaves": p.sigma(), "out_frames": out.Frames(),
	}})
	return RunOut{Buffer: out, Report: rep}, nil
}
