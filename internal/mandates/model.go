package mandates

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
)

// Holders maps the wire name of a holder to its principal key (C366.1). The column is free text, so a later holder needs no migration.
var Holders = map[string]string{"night_shift": "agent:nightshift"}

// HolderNightShift is the wire name of the only v1 holder.
const HolderNightShift = "night_shift"

// Stored states and the effective states read from them (C366.3).
const (
	StateApproved = "approved"
	StateRevoked  = "revoked"

	EffScheduled = "scheduled"
	EffLive      = "live"
	EffEnded     = "ended"
	EffRevoked   = "revoked"
)

// Transition is one allowed move of the stored state.
type Transition struct{ From, To string }

// Transitions is the table of stored-state moves; the SQL trigger mandates_transition equals it (TestMandateTransitions_GoEqualsSQL).
var Transitions = []Transition{{StateApproved, StateRevoked}}

// Limits of C366.2.
const (
	MaxTitle           = 120
	MaxBudget          = money.MicroUSD(100_000_000)
	MaxValidity        = 31 * 24 * time.Hour
	FromSlack          = 5 * time.Minute // valid_from may be this far in the past
	MaxRoots           = 200
	MaxTakesPerLineage = 50
	MaxTakesPerBatch   = 12
	MaxBatchesPerDay   = 4
	MaxReason          = 500
)

// Scope is what a mandate allows (C366.2). It is stored as JSON; a stored scope is decoded strictly (unknown keys refused).
type Scope struct {
	Modes                   []string       `json:"modes"`
	Engines                 []string       `json:"engines"`
	MinStars                int            `json:"min_stars"`
	RootTrackIDs            []string       `json:"root_track_ids"` // nil: every lineage
	MaxTakesPerLineage      int            `json:"max_takes_per_lineage"`
	MaxTakesPerBatch        int            `json:"max_takes_per_batch"`
	MaxBatchCeilingMicroUSD money.MicroUSD `json:"max_batch_ceiling_micro_usd"`
	MaxBatchesPerDay        int            `json:"max_batches_per_day"`
	TierB                   bool           `json:"tier_b"`
}

// AllowedModes are the planner modes a v1 mandate may name (C366.2).
var AllowedModes = []string{lineage.ModeNewSeed, lineage.ModeStyleEdit}

// ParseScope decodes a stored scope strictly.
func ParseScope(raw string) (Scope, error) {
	var s Scope
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Scope{}, fmt.Errorf("mandates: scope: %w", err)
	}
	return s, nil
}

// JSON is the stored form of a scope: slices are never null except root_track_ids.
func (s Scope) JSON() (string, error) {
	if s.Modes == nil {
		s.Modes = []string{}
	}
	if s.Engines == nil {
		s.Engines = []string{}
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// Mandate is one stored row with its scope decoded.
type Mandate struct {
	ID, HolderKey, Title  string
	Budget                money.MicroUSD
	ValidFrom, ValidUntil time.Time
	Scope                 Scope
	State                 string // stored: approved | revoked
	CreatedBy             string
	CreatedAt             time.Time
	RevokedBy             *string
	RevokedAt             *time.Time
	RevokeReason          *string
}

// EffectiveState is the state a reader sees at now (C366.3): revoked, else scheduled before valid_from, live until valid_until, ended after.
func (m Mandate) EffectiveState(now time.Time) string {
	switch {
	case m.State == StateRevoked:
		return EffRevoked
	case now.Before(m.ValidFrom):
		return EffScheduled
	case now.Before(m.ValidUntil):
		return EffLive
	}
	return EffEnded
}

// HolderName is the wire name of the mandate's holder ("" for a key no wire name maps to).
func (m Mandate) HolderName() string {
	for name, key := range Holders {
		if key == m.HolderKey {
			return name
		}
	}
	return ""
}

func (s Scope) hasMode(m string) bool   { return slices.Contains(s.Modes, m) }
func (s Scope) hasEngine(e string) bool { return slices.Contains(s.Engines, e) }
