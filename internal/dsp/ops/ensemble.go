package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

// KindEnsemble is the registry name of the ensemble (C205.5).
const KindEnsemble = "ensemble"

func init() { Register(KindEnsemble, runEnsemble, ensembleUnits) }

// EnsembleVoice is one voice of the fixed choir preset (C202.3).
type EnsembleVoice struct {
	Semitones float64
	Formant   float64
	DelayMS   float64
	GainDB    float64
	Pan       float64
}

// EnsembleVoices is the fixed `choir_octaves` preset: it is not a parameter.
var EnsembleVoices = [...]EnsembleVoice{
	{-12, 0.85, 0, -3, -0.6},
	{-7, 0.92, 12, -4, -0.3},
	{0, 1.0, 23, 0, 0},
	{5, 1.08, 31, -5, 0.3},
	{12, 1.18, 42, -6, 0.6},
}

// The ensemble parameter ranges (C202.3).
const (
	EnsembleDBMin, EnsembleDBMax     = -40.0, 0.0
	EnsembleTailMin, EnsembleTailMax = 0.0, 40.0
	ensembleFadeS                    = 0.050 // fade-out of an output whose room tail was cut by tail_s
)

// Units charged per sample by the linear passes of Ensemble.
const (
	unitsStackSample = 2 // adding one voice into the stack
	unitsDrySample   = 2 // the dry copy of the stack
	unitsWetSample   = 3 // adding the convolved room
)

// EnsembleParams are the resolved parameters of the operator.
type EnsembleParams struct {
	Space string
	WetDB float64
	DryDB float64
	NoDry bool // dry_db was null: -infinity
	TailS float64
	N     int // phase-vocoder frame; 0 means 4096 (tests use a smaller one at 16 kHz)
}

// ParseEnsemble reads the resolved params object of job.json (absent members take their C202.3 defaults; a present null
// dry_db means no dry signal) and checks the ranges.
func ParseEnsemble(raw json.RawMessage) (EnsembleParams, error) {
	var members map[string]json.RawMessage
	if err := parseParams(raw, &members); err != nil {
		return EnsembleParams{}, err
	}
	p := EnsembleParams{Space: "cathedral", WetDB: -6, DryDB: -12, TailS: 20}
	get := func(name string, dst any) error {
		if v, ok := members[name]; ok && string(v) != "null" {
			if err := json.Unmarshal(v, dst); err != nil {
				return &ParamError{Field: name, Rule: "type"}
			}
		}
		return nil
	}
	for name, dst := range map[string]any{"space": &p.Space, "wet_db": &p.WetDB, "dry_db": &p.DryDB, "tail_s": &p.TailS} {
		if err := get(name, dst); err != nil {
			return EnsembleParams{}, err
		}
	}
	if v, ok := members["dry_db"]; ok && string(v) == "null" {
		p.NoDry = true
	}
	return p, p.check()
}

func (p EnsembleParams) check() error {
	if _, ok := ir.Preset(p.Space); !ok {
		return &ParamError{Field: "space", Rule: RulePreset}
	}
	switch {
	case !(p.WetDB >= EnsembleDBMin && p.WetDB <= EnsembleDBMax):
		return &ParamError{Field: "wet_db", Rule: RuleRange}
	case !p.NoDry && !(p.DryDB >= EnsembleDBMin && p.DryDB <= EnsembleDBMax):
		return &ParamError{Field: "dry_db", Rule: RuleRange}
	case !(p.TailS >= EnsembleTailMin && p.TailS <= EnsembleTailMax):
		return &ParamError{Field: "tail_s", Rule: RuleRange}
	}
	return nil
}

func (p EnsembleParams) frame() int {
	if p.N > 0 {
		return p.N
	}
	return pvWindow
}

func dbToLin(db float64) float64 { return dsp.Pow(10, db/20) }

// stackGains are the left and right gains with which channel c of a voice enters the stack: the constant-power pan law
// scaled by sqrt 2, so that a voice panned to the centre of a stereo input passes unchanged and a dual-mono voice keeps its
// power wherever it is panned.
func stackGains(pan float64) (l, r float64) {
	gl, gr := ir.Pan(pan)
	return float64(math.Sqrt2 * gl), float64(math.Sqrt2 * gr)
}

func delaySamples(v EnsembleVoice, rate int) int {
	return roundHalfUp(v.DelayMS * float64(rate) / 1000)
}

func maxDelay(rate int) int {
	var d int
	for _, v := range EnsembleVoices {
		d = max(d, delaySamples(v, rate))
	}
	return d
}

// ensembleStack renders the voices of x into the stereo stack (length len(x) + the longest delay): every voice is a
// pitch- and formant-shifted copy of each channel, delayed, gained and panned.
func ensembleStack(ctx context.Context, x pcm.Buffer, p EnsembleParams, m *dsp.Meter) (pcm.Buffer, error) {
	rate, n := x.Rate, p.frame()
	size := x.Frames() + maxDelay(rate)
	stack := pcm.Buffer{Rate: rate, Ch: [2][]float64{make([]float64, size), make([]float64, size)}}
	group := voiceGroup(x.Frames(), len(EnsembleVoices))
	for c := 0; c < 2; c++ {
		for j0 := 0; j0 < len(EnsembleVoices); j0 += group {
			if err := ctx.Err(); err != nil {
				return pcm.Buffer{}, err
			}
			j1 := min(j0+group, len(EnsembleVoices))
			voices := make([]pv.Voice, 0, group)
			for _, v := range EnsembleVoices[j0:j1] {
				rho, phi := dsp.Exp(math.Ln2*v.Semitones/12), v.Formant
				voices = append(voices, pv.Voice{
					Rho:     func(int) float64 { return rho },
					Formant: func(int) float64 { return phi },
				})
			}
			ys, err := pv.ShiftBank(x.Ch[c], voices, pv.Params{N: n, Hs: n / 4, Lock: true, Rate: rate, Meter: m})
			if err != nil {
				return pcm.Buffer{}, err
			}
			for k, y := range ys {
				v := EnsembleVoices[j0+k]
				gl, gr := stackGains(v.Pan)
				g := dbToLin(v.GainDB)
				// channel c of the input enters the side of the stack it belongs to, scaled by that side's pan gain
				side := gl
				if c == 1 {
					side = gr
				}
				d := delaySamples(v, rate)
				dst := stack.Ch[c][d:]
				a := float64(g * side)
				if err := meterBlocks(m, len(y), unitsStackSample, func(lo, hi int) {
					for i := lo; i < hi; i++ {
						dst[i] += float64(a * y[i])
					}
				}); err != nil {
					return pcm.Buffer{}, err
				}
			}
		}
	}
	return stack, nil
}

// ensembleIR builds the room response and cuts it to tail_s; cut says whether it was cut (the output then fades out).
func ensembleIR(p EnsembleParams, rate int, seed int64, m *dsp.Meter) (l, r []float64, cut bool, err error) {
	room, _ := ir.Preset(p.Space)
	l, r, _, err = ir.BuildErr(room, rate, seed, m)
	if err != nil {
		return nil, nil, false, err
	}
	if limit := roundHalfUp(p.TailS * float64(rate)); limit < len(l) {
		return l[:limit], r[:limit], true, nil
	}
	return l, r, false, nil
}

// Ensemble renders the choir of x in the generated room: output = dry * stack + wet * (stack convolved with the room), of
// length len(x) + the room's tail (at least the longest voice delay). It returns the output and the tail length in frames.
func Ensemble(ctx context.Context, x pcm.Buffer, p EnsembleParams, seed int64, m *dsp.Meter) (pcm.Buffer, int, error) {
	if err := p.check(); err != nil {
		return pcm.Buffer{}, 0, err
	}
	rate := x.Rate
	hl, hr, cut, err := ensembleIR(p, rate, seed, m)
	if err != nil {
		return pcm.Buffer{}, 0, err
	}
	stack, err := ensembleStack(ctx, x, p, m)
	if err != nil {
		return pcm.Buffer{}, 0, err
	}
	outLen := x.Frames() + max(len(hl), maxDelay(rate))
	dry, wet := 0.0, dbToLin(p.WetDB)
	if !p.NoDry {
		dry = dbToLin(p.DryDB)
	}
	out := pcm.Buffer{Rate: rate}
	for c, h := range [2][]float64{hl, hr} {
		o := make([]float64, outLen)
		sc := stack.Ch[c][:min(len(stack.Ch[c]), outLen)]
		if err := meterBlocks(m, len(sc), unitsDrySample, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				o[i] = float64(dry * sc[i])
			}
		}); err != nil {
			return pcm.Buffer{}, 0, err
		}
		if len(h) > 0 {
			if err := ctx.Err(); err != nil {
				return pcm.Buffer{}, 0, err
			}
			y, err := dsp.ConvolveUPErr(stack.Ch[c], h, 0, m)
			if err != nil {
				return pcm.Buffer{}, 0, err
			}
			y = y[:min(len(y), outLen)]
			if err := meterBlocks(m, len(y), unitsWetSample, func(lo, hi int) {
				for i := lo; i < hi; i++ {
					o[i] += float64(wet * y[i])
				}
			}); err != nil {
				return pcm.Buffer{}, 0, err
			}
		}
		if cut {
			// the response was cut where it was still audible: the last 50 ms of the output fade out
			fade := min(roundHalfUp(ensembleFadeS*float64(rate)), outLen)
			for i := 0; i < fade; i++ {
				o[outLen-fade+i] = float64(o[outLen-fade+i] * (0.5 * (1 + math.Cos(math.Pi*(float64(i)+0.5)/float64(fade)))))
			}
		}
		out.Ch[c] = o
	}
	return out, len(hl), nil
}

// EnsembleUnits is the work Ensemble adds to the meter, as an upper bound (the response is cut at -70 dB, which is only known
// once it is built): the voices, the room and the convolution of both channels. Safety is not included.
func EnsembleUnits(inFrames int64, rate int, p EnsembleParams) int64 {
	n := p.frame()
	room, _ := ir.Preset(p.Space)
	irLen := min(ir.TailBound(room, rate), roundHalfUp(p.TailS*float64(rate)))
	frames := pvFrames(int(inFrames), n/4)
	v := int64(len(EnsembleVoices))
	groups := int64(ceilDiv(len(EnsembleVoices), voiceGroup(int(inFrames), len(EnsembleVoices))))
	units := 2 * frames * (groups*(dsp.Units.FFT(n)+int64(10*n)) + v*(dsp.Units.FFT(n)+int64(20*n)) + groups*(2*dsp.Units.FFT(n)+int64(10*n)))
	units += ir.Units(room, rate)
	stackLen := int(inFrames) + maxDelay(rate)
	outLen := int(inFrames) + max(irLen, maxDelay(rate))
	units += 2*int64(len(EnsembleVoices))*unitsStackSample*inFrames + 2*unitsDrySample*int64(min(stackLen, outLen))
	if irLen > 0 {
		const block = dsp.DefaultConvBlock
		nBlocks := int64(ceilDiv(stackLen+irLen-1, block))
		parts := ceilDiv(irLen, block)
		units += 2*nBlocks*dsp.Units.ConvBlock(parts, 2*block) + 2*unitsWetSample*int64(min(stackLen+irLen-1, outLen))
	}
	return units
}

func ensembleUnits(in, _ int64, rate int, raw json.RawMessage) int64 {
	p, err := ParseEnsemble(raw)
	if err != nil {
		p = EnsembleParams{Space: "cathedral", TailS: 20}
	}
	return EnsembleUnits(in, rate, p)
}

func runEnsemble(ctx context.Context, in RunIn) (RunOut, error) {
	p, err := ParseEnsemble(in.Params)
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
	out, irLen, err := Ensemble(ctx, x, p, in.Seed, in.Meter)
	if err != nil {
		return RunOut{}, err
	}
	rep, _ := json.Marshal(map[string]any{"ensemble": map[string]any{
		"space": p.Space, "voices": len(EnsembleVoices), "ir_frames": irLen, "out_frames": out.Frames(),
	}})
	return RunOut{Buffer: out, Report: rep}, nil
}
