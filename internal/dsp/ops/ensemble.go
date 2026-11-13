package ops

import (
	"context"
	"encoding/json"
	"io"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
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
	var stack pcm.Buffer
	stack.Rate = x.Rate
	for c := range 2 {
		ch, err := ensembleStackChannel(ctx, x, p, m, c)
		if err != nil {
			return pcm.Buffer{}, err
		}
		stack.Ch[c] = ch
	}
	return stack, nil
}

// ensembleStackChannel renders channel c of the stack: the channels are independent, so the streamed render holds one at a time.
func ensembleStackChannel(ctx context.Context, x pcm.Buffer, p EnsembleParams, m *dsp.Meter, c int) ([]float64, error) {
	rate, n := x.Rate, p.frame()
	size := x.Frames() + maxDelay(rate)
	stack := make([]float64, size)
	group := voiceGroup(x.Frames(), len(EnsembleVoices))
	for j0 := 0; j0 < len(EnsembleVoices); j0 += group {
		if err := ctx.Err(); err != nil {
			return nil, err
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
			return nil, err
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
			dst := stack[d:]
			a := float64(g * side)
			if err := meterBlocks(m, len(y), unitsStackSample, func(lo, hi int) {
				for i := lo; i < hi; i++ {
					dst[i] += float64(a * y[i])
				}
			}); err != nil {
				return nil, err
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
	out, sink := collect(x.Rate, x.Frames())
	irLen, _, err := ensembleTo(ctx, x, p, seed, m, heapScratch{}, sink)
	if err != nil {
		return pcm.Buffer{}, 0, err
	}
	return *out, irLen, nil
}

// ensembleTo is Ensemble streamed: the channels are rendered one after the other (a stack channel, then its convolution with the
// room block by block into a spool of that channel's output), and the two spools are merged into sink in order. The memory is the
// input, one stack channel, the voice bank and the room's spectra, not the stack, the convolution result and the output of both
// channels. It returns the length of the room's response and of the output.
func ensembleTo(ctx context.Context, x pcm.Buffer, p EnsembleParams, seed int64, m *dsp.Meter, sc safety.Scratch, sink func(l, r []float64) error) (irLen, outLen int, err error) {
	if err := p.check(); err != nil {
		return 0, 0, err
	}
	rate := x.Rate
	hl, hr, cut, err := ensembleIR(p, rate, seed, m)
	if err != nil {
		return 0, 0, err
	}
	outLen = x.Frames() + max(len(hl), maxDelay(rate))
	dry, wet := 0.0, dbToLin(p.WetDB)
	if !p.NoDry {
		dry = dbToLin(p.DryDB)
	}
	var chans [2]safety.Spool
	for c, h := range [2][]float64{hl, hr} {
		stack, err := ensembleStackChannel(ctx, x, p, m, c)
		if err != nil {
			return 0, 0, err
		}
		if chans[c], err = sc.NewSpool(); err != nil {
			return 0, 0, err
		}
		defer func() { _ = chans[c].Close() }()
		if err := ensembleChannelOut(ctx, stack, h, dry, wet, cut, rate, outLen, m, func(o []float64) error { return putF64(chans[c], o) }); err != nil {
			return 0, 0, err
		}
	}
	var rd [2]io.ReadCloser
	for c := range rd {
		if rd[c], err = chans[c].Open(); err != nil {
			return 0, 0, err
		}
		defer func() { _ = rd[c].Close() }()
	}
	block := max(hifiBlock, 1)
	l, r := make([]float64, min(block, outLen)), make([]float64, min(block, outLen))
	for lo := 0; lo < outLen; lo += block {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		k := min(block, outLen-lo)
		for c, dst := range [2][]float64{l[:k], r[:k]} {
			if err := getF64(rd[c], dst); err != nil {
				return 0, 0, err
			}
		}
		if err := sink(l[:k], r[:k]); err != nil {
			return 0, 0, err
		}
	}
	return len(hl), outLen, nil
}

// ensembleChannelOut turns one channel of the stack into its output, block by block: dry * stack plus wet * (stack convolved with h),
// the fade at the end when the response was cut. Every sample is computed as Ensemble always did; the convolution blocks the output
// does not reach are charged without being computed.
func ensembleChannelOut(ctx context.Context, stack, h []float64, dry, wet float64, cut bool, rate, outLen int, m *dsp.Meter, emit func(o []float64) error) error {
	var cs *dsp.ConvStream
	bs := dsp.DefaultConvBlock
	dryLen := min(len(stack), outLen) // the stack that reaches the output
	wetLen := 0                       // the samples of the wet signal that reach it: the convolution is len(stack)+len(h)-1 long
	if len(h) > 0 {
		cs = dsp.NewConvStream(h, 0)
		bs = cs.Block()
		wetLen = min(len(stack)+len(h)-1, outLen)
	}
	fade := min(roundHalfUp(ensembleFadeS*float64(rate)), outLen)
	o := make([]float64, min(bs, outLen))
	if err := m.Add(unitsDrySample * int64(dryLen)); err != nil {
		return err
	}
	if err := m.Add(unitsWetSample * int64(wetLen)); err != nil {
		return err
	}
	convBlocks := 0
	if cs != nil {
		convBlocks = ceilDiv(len(stack)+len(h)-1, bs)
	}
	for j := 0; j*bs < outLen; j++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lo := j * bs
		k := min(bs, outLen-lo)
		var y []float64
		if cs != nil {
			var in []float64
			if lo < len(stack) {
				in = stack[lo:min(lo+bs, len(stack))]
			}
			var err error
			if y, err = cs.Push(in, m); err != nil {
				return err
			}
		}
		for i := range k {
			gi := lo + i
			v := 0.0
			if gi < dryLen {
				v = float64(dry * stack[gi])
			}
			if gi < wetLen {
				v += float64(wet * y[i])
			}
			if cut && gi >= outLen-fade {
				f := gi - (outLen - fade)
				v = float64(v * (0.5 * (1 + math.Cos(math.Pi*(float64(f)+0.5)/float64(fade)))))
			}
			o[i] = v
		}
		if err := emit(o[:k]); err != nil {
			return err
		}
	}
	if cs != nil {
		return m.Add(int64(max(convBlocks-ceilDiv(outLen, bs), 0)) * cs.BlockUnits())
	}
	return nil
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
	return RunOut{Buffer: out, Report: ensembleReport(p, irLen, out.Frames())}, nil
}

func ensembleReport(p EnsembleParams, irLen, outFrames int) json.RawMessage {
	rep, _ := json.Marshal(map[string]any{"ensemble": map[string]any{
		"space": p.Space, "voices": len(EnsembleVoices), "ir_frames": irLen, "out_frames": outFrames,
	}})
	return rep
}

// streamEnsemble is runEnsemble with the render streamed (ensembleTo).
func streamEnsemble(ctx context.Context, in RunIn, sc safety.Scratch, sink func(l, r []float64) error) (int, int, json.RawMessage, error) {
	p, err := ParseEnsemble(in.Params)
	if err != nil {
		return 0, 0, nil, err
	}
	if len(in.Inputs) != 1 {
		return 0, 0, nil, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	x := in.Inputs[0]
	if x.Rate == 0 {
		x.Rate = in.Rate
	}
	irLen, outLen, err := ensembleTo(ctx, x, p, in.Seed, in.Meter, sc, sink)
	if err != nil {
		return 0, 0, nil, err
	}
	return x.Rate, outLen, ensembleReport(p, irLen, outLen), nil
}
