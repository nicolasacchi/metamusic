package picks

import "time"

// Thresholds and timings of the Pick derivation and the Reconciler (C87). They are changed only by a decision
// (D-L1-9, D-L1-38).
const (
	// LadderMinDuels is the number of counted human blind verdicts inside a Song before the ladder may decide.
	LadderMinDuels = 3
	// LadderChallengeZ is the margin (in combined standard deviations) a fresh leader, or a challenger over the
	// incumbent, needs.
	LadderChallengeZ = 0.5
	// LadderKeepZ is the smaller margin below which an incumbent ladder Pick is finally displaced.
	LadderKeepZ = 0.25
	// LadderPriorSD is the standard deviation of a take absent from the fit; it must equal the ladder default
	// (TestPicks_PriorSDMatchesLadder).
	LadderPriorSD = 1.5

	// SweepInterval is how often Run recomputes the cross-Song ladder drift.
	SweepInterval = time.Hour
	// PollInterval is how often Run looks at the queue while it is non-empty.
	PollInterval = time.Second
	// BatchKeys is the number of queue keys (and Sweep Songs) per pass.
	BatchKeys = 100
	// MaxPendingReport caps the queue listing of reports.
	MaxPendingReport = 1000
	// SettleLeaseTTL is the lifetime of the cross-process settle lease (D-L1-35).
	SettleLeaseTTL = 30 * time.Second
	// MaxQueueTries is the number of re-marks of a transient anomaly before its key is dropped.
	MaxQueueTries = 5
	// MaxSongIDsFilter caps song_ids filters.
	MaxSongIDsFilter = 200

	// ladderEpsilon is the tolerance of the margin comparisons (a z of exactly 0.5 passes).
	ladderEpsilon = 1e-9
)
