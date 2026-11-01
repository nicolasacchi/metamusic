// Package sequencer is the pure sequencing planner of L3 (C137): integer cost model, exact and heuristic search and the
// explanation. It imports the standard library only. The method set and types are frozen by L3-F2; the bodies are lanes
// SQ1 (cost model, explanation) and SQ2 (planner).
package sequencer

import "errors"

// Camelot is a key on the Camelot wheel: Num 1..12, Letter 'A' (minor) or 'B' (major).
type Camelot struct {
	Num    int
	Letter byte
}

// Bases names how each feature of a Song was obtained (measured, declared, plan, predicted); carried into the explanation only.
type Bases struct{ BPM, Energy, Key string }

// Song is one sequencing input. Unknown tempo is 0 and unknown energy is NoEnergy.
type Song struct {
	ID           string
	BPMx100      int
	Energyx10000 int
	Key          *Camelot
	Bases        Bases
}

// Strategy is a target-curve and weight-row pair.
type Strategy string

const (
	StrategyArc    Strategy = "arc"
	StrategyRampUp Strategy = "ramp_up"
	StrategySteady Strategy = "steady"
)

// Options select the strategy and the optional fixed endpoints (Song ids; either may be empty).
type Options struct {
	Strategy    Strategy
	First, Last string
}

// Parts are the micro-costs of one transition.
type Parts struct{ Tempo, Key, Energy int64 }

// Transition is the cost and the facts of moving from one Song to the next.
type Transition struct {
	Cost              int64
	Parts             Parts
	TempoDeltaPctX100 *int
	KeyRelation       string
	EnergyDelta       *int
}

// Reason is one coded reason of a step (C137.5); the text is rendered by ExplainText.
type Reason struct {
	Code   string
	Params map[string]int
}

// Step is one position of the planned order.
type Step struct {
	Position  int
	SongID    string
	EnergyRel int // 0..10000, relative to the set
	Target    int // 0..10000; -1 when the strategy has no shape
	From      *Transition
	Reasons   []Reason
	Missing   []string // bpm, key, energy
}

// Coverage is the percentage of Songs (floor) that carry each feature.
type Coverage struct{ Tempo, Key, Energy int }

// Warning is a coded caveat of a plan (low_coverage, predicted_energy, heuristic, no_features, single_song,
// endpoint_cost, pending_features). Only the fields a code uses are set.
type Warning struct {
	Code    string
	Feature string
	Song    string
	Known   int
	Total   int
	N       int
}

// Result is a plan. Costs are in units of 1e-10 (C137.3). ImprovementPctX10 is signed tenths of a percent, nil when CostInput is 0.
type Result struct {
	Order             []string
	Exact             bool
	CostInput         int64
	CostOutput        int64
	ImprovementPctX10 *int
	Steps             []Step
	Coverage          Coverage
	Warnings          []Warning
	Digest            string
}

// Errors of Plan.
var (
	ErrTooMany           = errors.New("sequencer: too many songs")
	ErrUnknownStrategy   = errors.New("sequencer: unknown strategy")
	ErrEndpointMissing   = errors.New("sequencer: endpoint is not in the set")
	ErrEndpointsConflict = errors.New("sequencer: first and last are the same song")
)
