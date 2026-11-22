package ops

import (
	"context"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// KindScoreRender is the registry name of the score render (T1b, C355.1). The operator is the identity: the audio already exists
// when the child runs (the parent synthesised the score and decoded it to in-0.f32, D-DESK-7), so the render is the parent's
// signal plus T1's common safety finish (ownFades false: a synth render ends in its own release tail). The child keeps importing
// only the standard library and internal/dsp/..., and pcm_sha256 stays the oracle.
const KindScoreRender = "score_render"

func init() { Register(KindScoreRender, runScoreRender, scoreRenderUnits) }

// runScoreRender returns a copy of the first input (the safety stage works in place; the operator must not alias its input).
func runScoreRender(ctx context.Context, in RunIn) (RunOut, error) {
	if err := ctx.Err(); err != nil {
		return RunOut{}, err
	}
	src := in.Inputs[0]
	out := pcm.Buffer{Rate: src.Rate}
	for c := range src.Ch {
		out.Ch[c] = append([]float64(nil), src.Ch[c]...)
	}
	return RunOut{Buffer: out}, nil
}

// scoreRenderUnits is the model of the identity operator: no work of its own (the safety stage is added by Units). The synth
// is a tool whose time the cost model charges separately.
func scoreRenderUnits(int64, int64, int, json.RawMessage) int64 { return 0 }
