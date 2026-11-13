package ops

import (
	"context"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// StreamFunc is a kind that can render block by block: it hands the output to sink in order and never holds it. The result is the
// rate and length of the signal and the operator's own report. RunTo uses it, with a Scratch, to finish an output that does not
// fit in memory (a glacier of 600 s is 28.8 million frames, about 3.4 GB through the in-memory path).
type StreamFunc func(ctx context.Context, in RunIn, sc safety.Scratch, sink func(l, r []float64) error) (rate, frames int, report json.RawMessage, err error)

// streamers are the kinds with a streaming render. It is a table, not a switch: the dispatcher asks it, like the registry, by name.
var streamers = map[string]StreamFunc{KindGlacier: streamGlacier, KindEnsemble: streamEnsemble, KindMorph: streamMorph}

// streamGlacier is runGlacier over the streaming entry Glacier: the same render and the same report, without the output buffer.
func streamGlacier(ctx context.Context, in RunIn, _ safety.Scratch, sink func(l, r []float64) error) (int, int, json.RawMessage, error) {
	p, err := ParseGlacier(in.Params)
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
	_, _, outLen, err := p.plan(x)
	if err != nil {
		return 0, 0, nil, err
	}
	if err := Glacier(ctx, x, p, in.Seed, in.Meter, sink); err != nil {
		return 0, 0, nil, err
	}
	rep, _ := json.Marshal(map[string]any{"glacier": map[string]any{
		"window_samples": GlacierWindow(p.WindowS, x.Rate), "frames": GlacierFrames(outLen, GlacierWindow(p.WindowS, x.Rate)),
		"out_frames": outLen, "factor": p.Factor,
	}})
	return x.Rate, outLen, rep, nil
}

// frameBuffer wraps two channel slices as a buffer without copying.
func frameBuffer(rate int, l, r []float64) pcm.Buffer {
	return pcm.Buffer{Rate: rate, Ch: [2][]float64{l, r}}
}
