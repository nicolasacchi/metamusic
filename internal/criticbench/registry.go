package criticbench

import (
	"context"
	"database/sql"
	"sort"
)

// Critic kinds and tiers (C387.1).
const (
	KindMetric   = "metric"
	KindPairwise = "pairwise"
	KindLabels   = "labels"

	TierA       = "A"
	TierB       = "B"
	TierAgent   = "agent"
	TierDiction = "diction"
)

// Critic is one row of the bench: who or what is judging, and how well it agrees with the Owner.
type Critic struct {
	ID         string      `json:"id" doc:"Stable id, e.g. tier_a.true_peak or agent.stars:agent:<token>"`
	Label      string      `json:"label"`
	Kind       string      `json:"kind" enum:"metric,pairwise,labels"`
	Tier       string      `json:"tier" enum:"A,B,agent,diction"`
	Direction  *int        `json:"direction" doc:"+1 when a larger value is hypothesised to be better, -1 when smaller; null for a critic that names winners"`
	MinDelta   *float64    `json:"min_delta" doc:"The smallest difference of values that counts as a call; null for a critic that names winners"`
	Provenance string      `json:"provenance" doc:"Where the values come from, and that a direction is a hypothesis"`
	Scores     Scores      `json:"scores"`
	State      string      `json:"state" enum:"insufficient,unknown,trusted,contrary" doc:"The ratings state, or the duels state when ratings is insufficient (lines for a label critic)"`
	Thresholds []Threshold `json:"thresholds" doc:"Label critics: agreement with the Owner's marks at each CER threshold; else null"`
	SlotBias   *SlotBiasOf `json:"slot_bias" doc:"A duel critic's share of verdicts that named slot A; else null"`
}

// Scores is a critic's agreement with each reference; null when the critic is not compared on it.
type Scores struct {
	Ratings *Score `json:"ratings"`
	Duels   *Score `json:"duels"`
	Lines   *Score `json:"lines"`
}

// Threshold is one point of a label critic's threshold sweep (C386.4).
type Threshold struct {
	Mixed float64    `json:"mixed"`
	N     int        `json:"n"`
	Agree int        `json:"agree"`
	Rate  float64    `json:"rate"`
	CI95  [2]float64 `json:"ci95"`
}

// SlotBiasOf is SlotBias as a value.
type SlotBiasOf struct {
	N      int     `json:"n" doc:"Verdicts that named a slot"`
	Share  float64 `json:"share" doc:"Share that named slot A"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	Biased bool    `json:"biased" doc:"At least 20 verdicts and 0.5 is outside the interval"`
}

func (c *Critic) settle() {
	switch {
	case c.Kind == KindLabels && c.Scores.Lines != nil:
		c.State = c.Scores.Lines.State
	case c.Scores.Ratings != nil && (c.Scores.Ratings.State != StateInsufficient || c.Scores.Duels == nil):
		c.State = c.Scores.Ratings.State
	case c.Scores.Duels != nil:
		c.State = c.Scores.Duels.State
	default:
		c.State = StateInsufficient
	}
}

// Reader is the read side of the store (*sql.DB and *sql.Tx).
type Reader interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Reference is what every critic is compared against: the Owner's ratings and blind duel verdicts.
type Reference struct {
	Owner       map[string]int // track -> Owner stars
	RatingPairs []Pair         // newest WindowRatings
	DuelPairs   []Pair         // newest WindowDuels
}

// Source contributes critics. Tier A and the agent critics are built in; later lanes add Tier B and the label critic with
// Service.Use. A source returns no critic when it has nothing to say (absent, not failing).
type Source interface {
	Critics(ctx context.Context, q Reader, ref *Reference) ([]Critic, error)
}

// Versioned is the optional part of a Source whose data lives outside the three tables the cache key covers; its token joins
// the key.
type Versioned interface {
	Version(ctx context.Context) (string, error)
}

// LineCounter is the optional part of a Source that reports how many marked lines it scored (window.lines).
type LineCounter interface {
	Lines(ctx context.Context) (int, error)
}

// MetricValuer is the optional part of a Source of metric critics: the value of one of its critics for the given takes, which
// need not be in the reference windows. ok is false when the source does not own the critic. The night planner's tie-break reads
// it (C387.4).
type MetricValuer interface {
	MetricValues(ctx context.Context, q Reader, id string, tracks []string) (vals map[string]float64, ok bool, err error)
}

var tierRank = map[string]int{TierA: 0, TierAgent: 1, TierB: 2, TierDiction: 3}

func sortCritics(cs []Critic) {
	sort.SliceStable(cs, func(i, j int) bool {
		if ri, rj := tierRank[cs[i].Tier], tierRank[cs[j].Tier]; ri != rj {
			return ri < rj
		}
		return cs[i].ID < cs[j].ID
	})
}
