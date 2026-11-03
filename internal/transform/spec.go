package transform

import "encoding/json"

// Kind is a transform kind. Nine in all (C218-C225): the five DSP kinds, score_render (T1b) and the hi-fi/3D kinds.
// Every kind is also the Operator Ledger operator of the Take it makes, and the CHECK list of transforms.kind.
type Kind string

// The nine kinds, in the canonical order of the migration's CHECK list.
const (
	KindMorph       Kind = "morph"
	KindShepard     Kind = "shepard"
	KindGlacier     Kind = "glacier"
	KindPolytempo   Kind = "polytempo"
	KindEnsemble    Kind = "ensemble"
	KindScoreRender Kind = "score_render"
	KindMaster      Kind = "master"
	KindWiden       Kind = "widen"
	KindBinaural    Kind = "binaural"
)

// AllKinds lists the nine kinds in canonical order.
var AllKinds = []Kind{KindMorph, KindShepard, KindGlacier, KindPolytempo, KindEnsemble, KindScoreRender, KindMaster, KindWiden, KindBinaural}

// Valid reports whether k is one of the nine kinds.
func (k Kind) Valid() bool {
	for _, a := range AllKinds {
		if a == k {
			return true
		}
	}
	return false
}

// Spec is the canonical transform spec (C202): it is recipes.request_json after canonjson. Params holds the resolved
// per-kind parameters with every default written out.
type Spec struct {
	ID         string          `json:"id"`
	Seed       int64           `json:"seed"`
	V          int             `json:"v"`
	Kind       Kind            `json:"kind"`
	DSPVersion string          `json:"dsp_version"`
	SampleRate int             `json:"sample_rate"`
	Inputs     []InputRef      `json:"inputs"`
	Params     json.RawMessage `json:"params"`
	Alignment  *Alignment      `json:"alignment,omitempty"`
	TargetLUFS float64         `json:"target_lufs"`
	Synth      *SynthRef       `json:"synth,omitempty"` // T1b
	Score      *ScoreRef       `json:"score,omitempty"` // T1b
}

// InputRef names one input Take and the master it was resolved against.
type InputRef struct {
	Role         string `json:"role"` // source | a | b | score_source
	TrackID      string `json:"track_id"`
	MasterSHA256 string `json:"master_sha256"`
}

// Alignment is the frozen alignment resolution of morph, polytempo and shepard: one entry per input, in input order.
type Alignment struct {
	Inputs []InputAlign `json:"inputs"`
}

// InputAlign is the tempo, key and downbeat resolution of one input (D-T1-9).
type InputAlign struct {
	BPM          float64  `json:"bpm"`
	BPMSource    string   `json:"bpm_source"`  // explicit | plan | declared | measured
	OctaveFold   int      `json:"octave_fold"` // -1, 0, +1
	KeyPC        int      `json:"key_pc"`      // pitch class 0..11 (C = 0)
	KeyMinor     bool     `json:"key_minor"`
	KeySource    string   `json:"key_source"` // "" when unknown or not needed
	DownbeatS    *float64 `json:"downbeat_s"`
	DownbeatAuto bool     `json:"downbeat_auto"`
	PlanBarS     *float64 `json:"plan_bar_s"`
	BeatsPerBar  int      `json:"beats_per_bar"`
}

// SynthRef records the synth and soundfont of a score render (T1b).
type SynthRef struct {
	Name         string `json:"name"`
	SoundfontSHA string `json:"soundfont_sha256"`
	Engine       string `json:"engine"`
}

// ScoreRef records the exact score of a score render (T1b).
type ScoreRef struct {
	SHA256        string  `json:"sha256"`
	Bytes         int     `json:"bytes"`
	SourceTrackID *string `json:"source_track_id"`
	Dialect       string  `json:"dialect"`
}
