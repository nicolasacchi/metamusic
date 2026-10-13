// Package ladder is the pure math behind the personal preference ladder (C47):
// a Bradley-Terry-Davidson fit with a Gaussian prior on log-strengths, a
// Gamma prior on the tie parameter and a fixed reject anchor. It imports only
// the standard library so duels, ears and trackctx can share its result types.
package ladder

// Anchor is the virtual reject item; theta_R is fixed at 0.
const Anchor = -1

// Comparison is one judged pair. Winner may be Anchor (both_bad); Loser never is.
type Comparison struct {
	Winner, Loser int  // item indices 0..n-1
	Tie           bool // Davidson tie between Winner and Loser (never with the Anchor)
}

// Options tunes FitBTD; zero values select the defaults noted per field.
type Options struct {
	PriorSD float64  // default 1.5 (prior theta_i ~ N(0, PriorSD^2))
	NuShape float64  // default 2 (prior nu ~ Gamma(shape, rate))
	NuRate  float64  // default 2
	FixedNu *float64 // tests: hold nu fixed (0 => plain Bradley-Terry; 0 with any Tie comparison is an error)
	Tol     float64  // default 1e-9 (max |d theta_i| and |d nu| per iteration)
	MaxIter int      // default 50000
}

// Fit is the MAP estimate with Laplace standard deviations.
type Fit struct {
	Theta, SD    []float64 // per item; items without comparisons: theta = 0, SD = PriorSD
	Nu           float64
	Iterations   int
	Converged    bool
	LogPosterior float64
}

// Strength is a track's position on the reject-anchored scale.
type Strength struct {
	U      float64 `json:"u"`
	SD     float64 `json:"sd"`
	NDuels int     `json:"n_duels"`
}

// Counts tallies verdicts of one source on one pair.
type Counts struct {
	AWins   int `json:"a_wins"`
	BWins   int `json:"b_wins"`
	Ties    int `json:"ties"`
	BothBad int `json:"both_bad"`
	N       int `json:"n"`
}

// PairStats is the head-to-head record of two tracks (compare_tracks).
type PairStats struct {
	Human       Counts `json:"human"`         // blind + sighted human-source verdicts on duels whose slots are exactly {a, b}
	HumanBlindN int    `json:"human_blind_n"` // how many of Human.N were blind
	Critic      Counts `json:"critic"`
}
