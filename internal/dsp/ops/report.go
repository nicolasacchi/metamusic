package ops

import (
	"encoding/json"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// Warning is one coded warning of a render report (transform.json "warnings").
type Warning = hifiWarning

// Report is report.json of the render child (C210.5, the keys of the bridge's childReport). Run fills everything but
// PCMSHA256, which the child adds once it has written out.pcm24. Members of the operator's own report that the bridge has no
// field for (glacier, hifi, morph, ...) are carried through unchanged in Extra.
type Report struct {
	PCMSHA256  string
	Frames     int64
	Units      int64
	Safety     safetyDoc
	Warnings   []Warning
	BeatPhase  []json.RawMessage // one entry per input, null when the kind measures none
	Voices     json.RawMessage   // null when the kind has none
	BlindSpots []string
	Extra      map[string]json.RawMessage
}

// MarshalJSON writes the report with sorted keys; the fixed members win over a same-named member of Extra.
func (r Report) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(r.Extra)+8)
	for k, v := range r.Extra {
		m[k] = v
	}
	voices := r.Voices
	if len(voices) == 0 {
		voices = json.RawMessage("null")
	}
	warns, blind, phase := r.Warnings, r.BlindSpots, r.BeatPhase
	if warns == nil {
		warns = []Warning{}
	}
	if blind == nil {
		blind = []string{}
	}
	if phase == nil {
		phase = []json.RawMessage{}
	}
	m["pcm_sha256"], m["frames"], m["units"] = r.PCMSHA256, r.Frames, r.Units
	m["safety"], m["warnings"], m["beat_phase"] = r.Safety, warns, phase
	m["voices"], m["blind_spots"] = voices, blind
	return json.Marshal(m)
}

// voicesOf are the kinds whose report lists voices (C210.5): the ensemble's fixed choir preset.
var voicesOf = map[string]func() any{
	KindEnsemble: func() any {
		type voice struct {
			Semitones float64 `json:"semitones"`
			Formant   float64 `json:"formant"`
			DelayMS   float64 `json:"delay_ms"`
			GainDB    float64 `json:"gain_db"`
			Pan       float64 `json:"pan"`
		}
		out := make([]voice, 0, len(EnsembleVoices))
		for _, v := range EnsembleVoices {
			out = append(out, voice(v))
		}
		return out
	},
}

var errSafetyMismatch = errors.New("ops: a kind's report disagrees with its self-finishing entry")

// buildReport merges the operator's own report with what the dispatcher measured. fin is nil when the operator ran
// safety.Finish itself: its report must then carry the "safety" object, and it is used as it is (H1/H2 contract).
func buildReport(kind string, own json.RawMessage, nInputs int, fin *safety.FinishReport, tpIn float64) (Report, error) {
	var members map[string]json.RawMessage
	if len(own) > 0 && string(own) != "null" {
		if err := json.Unmarshal(own, &members); err != nil {
			return Report{}, err
		}
	}
	var r Report
	take := func(key string, into any) (bool, error) {
		raw, ok := members[key]
		if !ok {
			return false, nil
		}
		delete(members, key)
		return true, json.Unmarshal(raw, into)
	}
	hasSafety, err := take("safety", &r.Safety)
	if err != nil {
		return Report{}, err
	}
	if hasSafety == (fin != nil) {
		return Report{}, errSafetyMismatch
	}
	if fin != nil {
		r.Safety = newSafetyDoc(*fin, tpIn)
	}
	if _, err = take("warnings", &r.Warnings); err != nil {
		return Report{}, err
	}
	if _, err = take("blind_spots", &r.BlindSpots); err != nil {
		return Report{}, err
	}
	if ok, err := take("beat_phase", &r.BeatPhase); err != nil {
		return Report{}, err
	} else if !ok {
		r.BeatPhase = make([]json.RawMessage, nInputs)
		for i := range r.BeatPhase {
			r.BeatPhase[i] = json.RawMessage("null")
		}
	}
	if ok, err := take("voices", &r.Voices); err != nil {
		return Report{}, err
	} else if !ok {
		if f := voicesOf[kind]; f != nil {
			if r.Voices, err = json.Marshal(f()); err != nil {
				return Report{}, err
			}
		}
	}
	r.Extra = members
	return r, nil
}
