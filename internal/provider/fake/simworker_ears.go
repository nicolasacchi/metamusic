package fake

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
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

// The fake's analyze kind (C340.6): the worker fetches the pinned master (the session checks its digest before the hook runs) and
// answers the document a faithful recogniser would write for the lyric it "hears" in that file. An analyze document carries no request,
// so the lyric of a file is scripted by its sha256 (tierbtest.Script); a file nobody scripted is instrumental (no words). Tests add the
// known sung lyric of a synthetic master with ScriptAnalyzeLyrics.
var (
	analyzeMu     sync.RWMutex
	analyzeScript = tierbtest.Script{BySHA: map[string]string{}}
)

// ScriptAnalyzeLyrics makes the simulated analyze kind hear lyrics in the file whose sha256 is sha. It is meant for tests: the table is
// process-wide and only grows, and a sha256 names one synthetic master.
func ScriptAnalyzeLyrics(sha, lyrics string) {
	analyzeMu.Lock()
	defer analyzeMu.Unlock()
	analyzeScript.BySHA[sha] = lyrics
}

// AnalyzeHook is the SimKindHook of the "analyze" kind.
func AnalyzeHook(_ context.Context, _ *SimWorker, in provider.WorkerInput) (SimKindResult, error) {
	src := in.Inputs[provider.InputSrcAudio]
	analyzeMu.RLock()
	lyrics, _ := analyzeScript.Lyrics(src.SHA256)
	analyzeMu.RUnlock()
	o := tierbtest.Opts{Transcribe: in.Ears != nil && slices.Contains(in.Ears.Stages, "transcribe")}
	if in.Ears != nil && in.Ears.Lang != "" {
		o.Lang = in.Ears.Lang
	}
	return SimKindResult{Artifacts: []SimArtifact{{Name: provider.AnalysisArtifact, Body: tierbtest.FromLyrics(lyrics, o)}}}, nil
}
