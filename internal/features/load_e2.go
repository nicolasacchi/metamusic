package features

import "encoding/json"

// aceStepEngine is recipes.engine of the ACE-Step 1.5 engine (internal/engine/acestep.Name). It is repeated here so that
// features does not import the engine package: the bridge reads three request fields and nothing else.
const aceStepEngine = "acestep15"

// aceRequestFacts is the part of an ACE-Step request (E2 C148.3) that L3 reads.
type aceRequestFacts struct {
	Task       string `json:"task"`
	BPM        *int   `json:"bpm"`
	KeyScale   string `json:"keyscale"`
	DurationDS int    `json:"duration_ds"`
}

// applyEngineInputs is the engine branch of loadTake (C164, D-E2-40). For every engine but acestep15 it does nothing, so
// yue2 inputs and digests are untouched. An ACE-Step take has no score and no style term; the caption already reached
// textInputs as the style, and this adds what the request declares:
//   - tempo: tracks.bpm_declared (written at ingest) wins, request.bpm fills in when the column is empty;
//   - key: request.keyscale ("C Major", "F# Minor") overrides a key parsed from the caption, basis declared;
//   - duration: duration_ds/10 for text2music (edits are sized by their source, not by the request).
func applyEngineInputs(engine, requestJSON string, in *TakeInput) {
	if engine != aceStepEngine {
		return
	}
	var f aceRequestFacts
	if json.Unmarshal([]byte(requestJSON), &f) != nil {
		return // an unreadable request is "nothing declared", never an error
	}
	if in.DeclaredBPM == nil && f.BPM != nil && *f.BPM > 0 {
		in.DeclaredBPM = ptr(float64(*f.BPM))
	}
	if k := keyGuess(ParseStyleKey(f.KeyScale)); k != nil {
		in.StyleKey = k
	}
	if in.ExpectedDurationS == nil && (f.Task == "" || f.Task == "text2music") && f.DurationDS > 0 {
		in.ExpectedDurationS = ptr(float64(f.DurationDS) / 10)
	}
}
