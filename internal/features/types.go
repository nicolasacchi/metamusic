// Package features derives the per-Take and per-Song feature rows (tempo, energy, key, each with a basis) that sequencing,
// next actions and the Atlas read (L3 C135). The pure functions are derive.go, camelot.go, keyparse.go and prior.go; the service
// (compute, sweep, verify, read, load) is the only writer of take_features and song_features.
package features

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Basis says where a feature value came from.
type Basis string

// The four bases.
const (
	BasisMeasured  Basis = "measured"
	BasisDeclared  Basis = "declared"
	BasisPlan      Basis = "plan"
	BasisPredicted Basis = "predicted"
)

// Camelot is a key on the Camelot wheel: Num 1..12, Letter "A" (minor) or "B" (major).
type Camelot struct {
	Num    int
	Letter string
}

// Facets are the Sound facet values that feed the energy prior.
type Facets struct{ Mood, GenreFamily, TempoBand []string }

// Row is one take_features or song_features payload (fixed-point integers).
type Row struct {
	BPMx100                           *int
	BPMBasis                          Basis
	BPMRequestedx100, BPMMeasuredx100 *int
	BPMOctave                         bool
	Energyx10000                      *int
	EnergyBasis                       Basis
	Key                               *Camelot
	KeyBasis                          Basis
	DurationS, DurationExpectedS      *float64
	Detail                            json.RawMessage
}

// TempoMeasure is the stored tempo analysis (mm-tempo/1).
type TempoMeasure struct {
	BPM, Confidence float64
	OctaveAmbiguous bool
}

// KeyMeasure is the stored key analysis (mm-key/1); Key is nil when the analyzer answered "unknown".
type KeyMeasure struct {
	Key               *Camelot
	R, Margin         float64
	AmbiguousRelative bool
}

// EnergyMeasure is the stored energy analysis (mm-energy/1); Energy is nil when unknown.
type EnergyMeasure struct{ Energy *float64 }

// KeyGuess is a key read from the request text (style or ABC header) or the plan.
type KeyGuess struct {
	TonicPC int
	Mode    string // "major" | "minor"
}

// TakeInput is everything DeriveTake reads (C135.2).
type TakeInput struct {
	DeclaredBPM       *float64
	StyleBPM          *int
	PlanBPM           *float64
	MeasuredTempo     *TempoMeasure
	MeasuredKey       *KeyMeasure
	MeasuredEnergy    *EnergyMeasure
	StyleKey, PlanKey *KeyGuess
	Facets            Facets
	DurationS         float64
	ExpectedDurationS *float64
	RecipeKnown       bool
}

// SongRow is a song_features row: the subject's feature columns plus the five primary Sound values.
type SongRow struct {
	Row
	SongID                             string
	SubjectKind                        string // take | draft
	SubjectID                          string
	SubjectNote                        *string // "all_takes_discarded"
	Family, Vocal, Era, Mood, Language *string
}

// Env is the clock, id source and actor of a write (L1).
type Env = store.Env

// Changes is what a compute step did.
type Changes struct{ Takes, Songs int }

// Anomaly is a Verify finding (F1..F6, C135.6).
type Anomaly struct{ Code, Subject, Detail string }
