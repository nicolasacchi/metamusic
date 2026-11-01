package features

import (
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

type detailDoc struct {
	Octave    bool            `json:"octave"`
	Discarded *discardedDoc   `json:"discarded,omitempty"`
	Key       *keyDetailDoc   `json:"key,omitempty"`
	Tempo     *tempoDetailDoc `json:"tempo,omitempty"`
}

type discardedDoc struct {
	BPM []string `json:"bpm"`
}

type keyDetailDoc struct {
	Measured string `json:"measured"`
	Declared string `json:"declared,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Relation string `json:"relation"`
}

type tempoDetailDoc struct {
	Confidence      float64 `json:"confidence"`
	OctaveAmbiguous bool    `json:"octave_ambiguous"`
}

func fixed(x float64, scale float64) int { return int(math.Round(x * scale)) }

func inRange(bpm float64) bool { return bpm >= MinBPM && bpm <= MaxBPM }

func ptr[T any](v T) *T { return &v }

// DeriveTake is the pure, deterministic derivation of one take's row (C135.2). Fixed point everywhere: BPM x 100,
// energy x 10 000.
func DeriveTake(in TakeInput) Row {
	var r Row
	var d detailDoc

	// Tempo: requested = declared, else style, else plan.
	var requested *float64
	var reqBasis Basis
	switch {
	case in.DeclaredBPM != nil:
		requested, reqBasis = in.DeclaredBPM, BasisDeclared
	case in.StyleBPM != nil:
		requested, reqBasis = ptr(float64(*in.StyleBPM)), BasisDeclared
	case in.PlanBPM != nil:
		requested, reqBasis = in.PlanBPM, BasisPlan
	}
	var measured *float64
	if in.MeasuredTempo != nil {
		measured = ptr(in.MeasuredTempo.BPM)
		conf := in.MeasuredTempo.Confidence
		if math.IsNaN(conf) || math.IsInf(conf, 0) {
			conf = 0
		}
		d.Tempo = &tempoDetailDoc{Confidence: conf, OctaveAmbiguous: in.MeasuredTempo.OctaveAmbiguous}
	}
	var discarded []string
	if requested != nil && !inRange(*requested) {
		discarded = append(discarded, "requested")
		requested = nil
	}
	if measured != nil && !inRange(*measured) {
		discarded = append(discarded, "measured")
		measured = nil
	}
	if discarded != nil {
		d.Discarded = &discardedDoc{BPM: discarded}
	}
	if requested != nil {
		r.BPMRequestedx100 = ptr(fixed(*requested, 100))
	}
	if measured != nil {
		r.BPMMeasuredx100 = ptr(fixed(*measured, 100))
	}
	if _, octave := sounds.CompareTempo(requested, measured); octave {
		r.BPMOctave, d.Octave = true, true
	}
	switch {
	case r.BPMOctave || (measured == nil && requested != nil):
		r.BPMx100, r.BPMBasis = r.BPMRequestedx100, reqBasis
	case measured != nil:
		r.BPMx100, r.BPMBasis = r.BPMMeasuredx100, BasisMeasured
	}

	// Key: measured, else the style key, else the plan key.
	var guess *KeyGuess
	var guessBasis Basis
	switch {
	case in.StyleKey != nil:
		guess, guessBasis = in.StyleKey, BasisDeclared
	case in.PlanKey != nil:
		guess, guessBasis = in.PlanKey, BasisPlan
	}
	switch {
	case in.MeasuredKey != nil && in.MeasuredKey.Key != nil:
		k := *in.MeasuredKey.Key
		r.Key, r.KeyBasis = &k, BasisMeasured
		if guess != nil {
			g := CamelotOf(guess.TonicPC, guess.Mode)
			kd := &keyDetailDoc{Measured: k.String(), Relation: relation(k, g)}
			if guessBasis == BasisDeclared {
				kd.Declared = g.String()
			} else {
				kd.Plan = g.String()
			}
			d.Key = kd
		}
	case guess != nil:
		g := CamelotOf(guess.TonicPC, guess.Mode)
		r.Key, r.KeyBasis = &g, guessBasis
	}

	// Energy: measured, else the facet prior (empty facets for a reference take).
	switch {
	case in.MeasuredEnergy != nil && in.MeasuredEnergy.Energy != nil:
		r.Energyx10000, r.EnergyBasis = ptr(fixed(*in.MeasuredEnergy.Energy, 10000)), BasisMeasured
	default:
		if e, ok := PredictEnergy(in.Facets); ok {
			r.Energyx10000, r.EnergyBasis = &e, BasisPredicted
		}
	}

	if in.DurationS > 0 {
		r.DurationS = ptr(in.DurationS)
	}
	if in.ExpectedDurationS != nil && *in.ExpectedDurationS > 0 {
		r.DurationExpectedS = ptr(*in.ExpectedDurationS)
	}
	// A struct of bools, strings and finite floats always marshals.
	r.Detail, _ = json.Marshal(d)
	return r
}

func relation(measured, other Camelot) string {
	switch {
	case measured == other:
		return "same"
	case measured.Num == other.Num:
		return "relative"
	}
	return "other"
}
