package fake

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The fake's Tier B stage of a render (C329.2, C339.7): after the render it writes the analysis.json that a faithful recogniser
// would, built by tierbtest.FromLyrics from the request's lyrics (the harness tracks the synthetic placement, so tests know the
// truth). Fault tokens in the recipe's style, like the other fake: markers, select the failure modes:
//
//	fake:ears_error    the document says status error (stage ran, recogniser failed); the take still completes
//	fake:ears_mishear  deterministic substitutions: the recogniser mishears the words listed in earsMishear
//	fake:ears_skip     the stage reports skipped (DEADLINE) and uploads nothing
//	fake:ears_garbage  the document is not an mm.ears-b/1 document (a paid, valueless artifact: the ingest records it and moves on)
// It is registered by internal/app (C329.2), not at init, so the fake package's own tests still see an image without the stage.

// earsMishear is what fake:ears_mishear makes the recogniser hear: folds of reference words to a similar-sounding word.
var earsMishear = map[string]string{"sera": "sela", "cielo": "celo", "mare": "male", "luna": "lana", "canto": "santo", "sogno": "sono"}

func earsTokens(style string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.FieldsFunc(style, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if rest, ok := strings.CutPrefix(tok, "fake:ears_"); ok {
			out[rest] = true
		}
	}
	return out
}

// EarsRenderHook is the SimKindHook of the "ears" stage.
func EarsRenderHook(_ context.Context, _ *SimWorker, in provider.WorkerInput) (SimKindResult, error) {
	var req struct {
		Style  string `json:"style"`
		Lyrics string `json:"lyrics"`
	}
	_ = json.Unmarshal(in.Request, &req)
	tok := earsTokens(req.Style)
	if tok["skip"] {
		return SimKindResult{Ears: &provider.EarsOut{Status: "skipped", Code: "DEADLINE"}}, nil
	}
	o := tierbtest.Opts{Transcribe: in.Ears != nil && slices.Contains(in.Ears.Stages, "transcribe")}
	if in.Ears != nil && in.Ears.Lang != "" {
		o.Lang = in.Ears.Lang
	}
	switch {
	case tok["error"]:
		o.Status = "error"
	case tok["mishear"]:
		o.Subs = earsMishear
	}
	body := tierbtest.FromLyrics(req.Lyrics, o)
	if tok["garbage"] {
		body = []byte(`{"contract":"mm.ears-b/0","garbage":true}`)
	}
	return SimKindResult{Artifacts: []SimArtifact{{Name: provider.AnalysisArtifact, Body: body}}}, nil
}
