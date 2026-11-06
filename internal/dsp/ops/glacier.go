package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// KindGlacier is the registry name of the glacier (C205.3).
const KindGlacier = "glacier"

func init() { Register(KindGlacier, runGlacier, glacierUnits) }

// The glacier parameter ranges (C202.3).
const (
	GlacierFactorMin, GlacierFactorMax = 10.0, 50.0
	GlacierWindowMin, GlacierWindowMax = 0.1, 2.0
	GlacierFadeMax                     = 30.0
	// glacierGain makes the output as loud as the input: a random-phase frame spreads its energy evenly over the window, and
	// the sine windows of analysis and synthesis then keep half of it.
	glacierGain = math.Sqrt2
)

// GlacierParams are the resolved parameters of the operator.
type GlacierParams struct {
	Factor            float64
	StartS, LengthS   float64
	WindowS           float64
	FadeInS, FadeOutS float64
	hasLength         bool
}

// ParseGlacier reads the resolved params object of job.json (absent members take their C202.3 defaults; an absent length_s
// is left at 0 and resolved against the input by the operator) and checks the ranges.
func ParseGlacier(raw json.RawMessage) (GlacierParams, error) {
	in := struct {
		Factor   *float64 `json:"factor"`
		StartS   *float64 `json:"start_s"`
		LengthS  *float64 `json:"length_s"`
		WindowS  *float64 `json:"window_s"`
		FadeInS  *float64 `json:"fade_in_s"`
		FadeOutS *float64 `json:"fade_out_s"`
	}{}
	if err := parseParams(raw, &in); err != nil {
		return GlacierParams{}, err
	}
	p := GlacierParams{Factor: 20, WindowS: 0.5, FadeInS: 4, FadeOutS: 4}
	set := func(dst *float64, src *float64) {
		if src != nil {
			*dst = *src
		}
	}
	set(&p.Factor, in.Factor)
	set(&p.StartS, in.StartS)
	set(&p.WindowS, in.WindowS)
	set(&p.FadeInS, in.FadeInS)
	set(&p.FadeOutS, in.FadeOutS)
	if in.LengthS != nil {
		p.LengthS, p.hasLength = *in.LengthS, true
	}
	return p, p.check()
}

func (p GlacierParams) check() error {
	switch {
	case !(p.Factor >= GlacierFactorMin && p.Factor <= GlacierFactorMax):
		return &ParamError{Field: "factor", Rule: RuleRange}
	case !(p.StartS >= 0):
		return &ParamError{Field: "start_s", Rule: RuleRange}
	case p.hasLength && !(p.LengthS > 0):
		return &ParamError{Field: "length_s", Rule: RuleRange}
	case !(p.WindowS >= GlacierWindowMin && p.WindowS <= GlacierWindowMax):
		return &ParamError{Field: "window_s", Rule: RuleRange}
	case !(p.FadeInS >= 0 && p.FadeInS <= GlacierFadeMax):
		return &ParamError{Field: "fade_in_s", Rule: RuleRange}
	case !(p.FadeOutS >= 0 && p.FadeOutS <= GlacierFadeMax):
		return &ParamError{Field: "fade_out_s", Rule: RuleRange}
	}
	return nil
}

// GlacierWindow is the analysis window N in samples: window_s rounded up to a power of two.
func GlacierWindow(windowS float64, rate int) int {
	return dsp.NextPow2(max(roundHalfUp(windowS*float64(rate)), 2))
}

// GlacierLen is the exact output length, round(length_s * rate * factor).
func GlacierLen(lengthS, factor float64, rate int) int {
	return roundHalfUp(lengthS * float64(rate) * factor)
}

// GlacierFrames is the number of transform frames of an output of outLen samples at hop N/2.
func GlacierFrames(outLen, n int) int { return ceilDiv(outLen, n/2) + 1 }

// GlacierUnits is the work the operator adds to the meter for outLen output samples (C209.1): per frame and channel two
// transforms and 20 N of window, phase and overlap-add work. Safety is not included.
func GlacierUnits(outLen int64, rate int, windowS float64) int64 {
	n := GlacierWindow(windowS, rate)
	return int64(GlacierFrames(int(outLen), n)) * 2 * (2*dsp.Units.FFT(n) + 20*int64(n))
}

func glacierUnits(_, out int64, rate int, raw json.RawMessage) int64 {
	w := 0.5
	if p, err := ParseGlacier(raw); err == nil {
		w = p.WindowS
	}
	return GlacierUnits(out, rate, w)
}

// plan resolves the scanned excerpt of x: its first sample, its length and the output length. length_s is exact when given
// (outLen = round(length_s * rate * factor)); by default it is min(30 s, what remains after start). An excerpt that runs past
// the end of x is cut there.
func (p GlacierParams) plan(x pcm.Buffer) (start, length, outLen int, err error) {
	rate := x.Rate
	start = roundHalfUp(p.StartS * float64(rate))
	if start >= x.Frames() {
		return 0, 0, 0, &ParamError{Field: "start_s", Rule: RuleRange}
	}
	lengthS := p.LengthS
	if !p.hasLength {
		lengthS = float64(min(30*rate, x.Frames()-start)) / float64(rate)
	}
	length = roundHalfUp(lengthS * float64(rate))
	if start+length > x.Frames() {
		length = x.Frames() - start
		lengthS = float64(length) / float64(rate)
	}
	if length <= 0 {
		return 0, 0, 0, &ParamError{Field: "length_s", Rule: RuleRange}
	}
	return start, length, GlacierLen(lengthS, p.Factor, rate), nil
}

// Glacier renders the excerpt of x into blocks of N/2 samples handed to sink (the slices are only valid during the call), so
// the memory of a 900 s output is that of one window. sink may stop the run by returning an error. The meter is charged per
// frame and channel.
func Glacier(ctx context.Context, x pcm.Buffer, p GlacierParams, seed int64, m *dsp.Meter, sink func(l, r []float64) error) error {
	if err := p.check(); err != nil {
		return err
	}
	rate := x.Rate
	start, length, outLen, err := p.plan(x)
	if err != nil {
		return err
	}
	n := GlacierWindow(p.WindowS, rate)
	hs := n / 2
	frames := GlacierFrames(outLen, n)
	win := make([]float64, n)
	for i := range win {
		win[i] = math.Sin(math.Pi * (float64(i) + 0.5) / float64(n))
	}
	nIn, nOut := min(roundHalfUp(p.FadeInS*float64(rate)), outLen), min(roundHalfUp(p.FadeOutS*float64(rate)), outLen)
	var rnd [2]*dsp.Rand
	for c := range rnd {
		rnd[c] = dsp.NewRand(uint64(seed)*2 + uint64(c)) // #nosec G115 -- the bits of the seed are the stream id; wrap-around is intended
	}
	re, im := make([]float64, n), make([]float64, n)
	carry := [2][]float64{make([]float64, hs), make([]float64, hs)}
	blk := [2][]float64{make([]float64, hs), make([]float64, hs)}
	emitted := 0
	for f := 0; f < frames; f++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// the analysis position runs evenly from start-N/2 to start+length-N/2 (integer arithmetic: exact on every host)
		pos := start - hs
		if frames > 1 {
			pos += (2*length*f + (frames - 1)) / (2 * (frames - 1))
		}
		for c := 0; c < 2; c++ {
			if err := m.Add(2*dsp.Units.FFT(n) + 20*int64(n)); err != nil {
				return err
			}
			for i := 0; i < n; i++ {
				v := 0.0
				if j := pos + i; j >= start && j < start+length {
					v = float64(x.Ch[c][j] * win[i])
				}
				re[i], im[i] = v, 0
			}
			dsp.FFT(re, im)
			randomPhases(re, im, rnd[c])
			dsp.IFFT(re, im)
			for i := 0; i < hs; i++ {
				blk[c][i] = carry[c][i] + float64(glacierGain*float64(re[i]*win[i]))
				carry[c][i] = float64(glacierGain * float64(re[hs+i]*win[hs+i]))
			}
		}
		if f == 0 {
			continue // the first half frame lies before the output
		}
		cnt := min(hs, outLen-emitted)
		for c := 0; c < 2; c++ {
			for i := 0; i < cnt; i++ {
				blk[c][i] = float64(blk[c][i] * glacierFade(emitted+i, outLen, nIn, nOut))
			}
		}
		if err := sink(blk[0][:cnt], blk[1][:cnt]); err != nil {
			return err
		}
		emitted += cnt
	}
	return nil
}

// randomPhases keeps the magnitude of every bin of the transform (re, im) of a real frame and replaces its phase by
// U[0, 2pi), keeping the spectrum Hermitian; DC and Nyquist stay real.
func randomPhases(re, im []float64, r *dsp.Rand) {
	n := len(re)
	half := n / 2
	re[0], im[0] = math.Abs(re[0]), 0
	re[half], im[half] = math.Abs(re[half]), 0
	for k := 1; k < half; k++ {
		mag := math.Sqrt(float64(re[k]*re[k]) + float64(im[k]*im[k]))
		ph := 2 * math.Pi * r.Float64()
		re[k], im[k] = float64(mag*math.Cos(ph)), float64(mag*math.Sin(ph))
		re[n-k], im[n-k] = re[k], -im[k]
	}
}

// glacierFade is the raised-cosine gain of output sample g under the fade lengths in and out.
func glacierFade(g, outLen, nIn, nOut int) float64 {
	v := 1.0
	if g < nIn {
		v = 0.5 * (1 - math.Cos(math.Pi*(float64(g)+0.5)/float64(nIn)))
	}
	if r := outLen - 1 - g; r < nOut {
		v *= 0.5 * (1 - math.Cos(math.Pi*(float64(r)+0.5)/float64(nOut)))
	}
	return v
}

func runGlacier(ctx context.Context, in RunIn) (RunOut, error) {
	p, err := ParseGlacier(in.Params)
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
	_, _, outLen, err := p.plan(x)
	if err != nil {
		return RunOut{}, err
	}
	out := pcm.Buffer{Rate: x.Rate, Ch: [2][]float64{make([]float64, 0, outLen), make([]float64, 0, outLen)}}
	err = Glacier(ctx, x, p, in.Seed, in.Meter, func(l, r []float64) error {
		out.Ch[0], out.Ch[1] = append(out.Ch[0], l...), append(out.Ch[1], r...)
		return nil
	})
	if err != nil {
		return RunOut{}, err
	}
	rep, _ := json.Marshal(map[string]any{"glacier": map[string]any{
		"window_samples": GlacierWindow(p.WindowS, x.Rate), "frames": GlacierFrames(outLen, GlacierWindow(p.WindowS, x.Rate)),
		"out_frames": outLen, "factor": p.Factor,
	}})
	return RunOut{Buffer: out, Report: rep}, nil
}
