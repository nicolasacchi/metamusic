// Package picks derives and records the Pick of a Song (C91): the take the library stands behind, chosen by a pure,
// deterministic, explainable rule (pinned, ladder, stars, newest) and logged as an append-only event stream.
package picks

import (
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Env is the Reconciler environment (C87).
type Env = store.Env

// Source is how a Pick was decided.
type Source string

// The five sources, in priority order after none.
const (
	SourceNone   Source = "none"
	SourcePinned Source = "pinned"
	SourceLadder Source = "ladder"
	SourceStars  Source = "stars"
	SourceNewest Source = "newest"
)

// EvidenceDef is the definition tag of the evidence JSON.
const EvidenceDef = "mm.pick/1"

// TakeInput is one take of a Song as Derive sees it. The two timestamps are unix milliseconds from
// store.ParseInstant: pre-M3 (20 character) and M3 (24 character) timestamps mix and are never string-compared (D-L1-34).
type TakeInput struct {
	ID, Status            string
	RecordedAt, CreatedAt int64
	HumanStars            *int
	U, SD                 *float64 // ladder strength; nil means "not in the fit"
}

// Input is everything Derive needs.
type Input struct {
	Takes           []TakeInput // every take of the Song (eligibility is decided inside)
	PinnedTakeID    *string     // kept even while ineligible (D-L1-38)
	IncumbentTakeID *string     // the current Pick take when its source is 'ladder' (stickiness), else nil
	IntraDuels      int         // counted human blind verdicts on duels whose two slots are both takes of the Song
}

// Result is the derived Pick.
type Result struct {
	TakeID        *string
	Source        Source
	Evidence      Evidence
	PinIneligible bool
}

// R4 is a float rounded to four decimals when marshalled, like ladder.Strength.
type R4 float64

// MarshalJSON renders the value rounded to 4 decimals.
func (r R4) MarshalJSON() ([]byte, error) {
	return json.Marshal(math.Round(float64(r)*1e4) / 1e4)
}

// StrengthEvidence is a take's ladder strength inside evidence.
type StrengthEvidence struct {
	ID string `json:"id"`
	U  R4     `json:"u"`
	SD R4     `json:"sd"`
}

// LadderSkipped records why the ladder was considered and did not decide.
type LadderSkipped struct {
	DuelsN int    `json:"duels_n"`
	Z      *R4    `json:"z"`
	Reason string `json:"reason"` // few_duels | margin | single_take
}

// IncumbentEvidence records the stickiness decision when an incumbent was involved.
type IncumbentEvidence struct {
	ID         string `json:"id"`
	Held       bool   `json:"held"`
	ZKeep      *R4    `json:"z_keep,omitempty"`
	ZChallenge *R4    `json:"z_challenge,omitempty"`
}

// PinIneligibleEvidence records a kept pin that was skipped for display.
type PinIneligibleEvidence struct {
	TakeID string `json:"take_id"`
	Status string `json:"status"`
}

// Evidence is the mm.pick/1 document stored in song_picks.evidence_json and pick_events.evidence_json (C91.3).
type Evidence struct {
	Def      string `json:"def"`
	Rule     string `json:"rule"`
	Eligible int    `json:"eligible"`

	PinnedBy   *string `json:"pinned_by,omitempty"`
	PinnedAt   *string `json:"pinned_at,omitempty"`
	ProposalID *string `json:"proposal_id,omitempty"`

	DuelsN     *int              `json:"duels_n,omitempty"`
	Leader     *StrengthEvidence `json:"leader,omitempty"`
	RunnerUp   *StrengthEvidence `json:"runner_up,omitempty"`
	Z          *R4               `json:"z,omitempty"`
	MinZ       *R4               `json:"min_z,omitempty"`
	MinDuels   *int              `json:"min_duels,omitempty"`
	Stars      *int              `json:"stars,omitempty"`
	Tied       *int              `json:"tied,omitempty"`
	RecordedAt string            `json:"recorded_at,omitempty"`

	LadderSkipped *LadderSkipped         `json:"ladder_skipped,omitempty"`
	Incumbent     *IncumbentEvidence     `json:"incumbent,omitempty"`
	PinIneligible *PinIneligibleEvidence `json:"pin_ineligible,omitempty"`
}

// Changes is what a recompute did, for the Reconciler report.
type Changes struct {
	Events    int      // pick_events rows written
	Refreshed int      // evidence refreshed without an event
	Expired   int      // proposals expired
	Songs     []string // Songs whose (take, source) changed
}
