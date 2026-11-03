package transform

import "time"

// The constants of C200.4. Tests reference the names; the operator-tunable values live in config (C200.1).
const (
	MaxInputs         = 2      // inputs per transform
	MaxInputS         = 600    // longest input in seconds
	MaxScoreBytes     = 262144 // (T1b) largest score.abc
	MaxScoreEvents    = 200000 // (T1b) largest note-event count of a score
	MaxTitleRunes     = 200    // title length (transforms.title CHECK)
	MaxRationaleRunes = 1000   // rationale length (transforms.rationale CHECK)
	ConcurrentRenders = 1      // renders at a time; not a knob (D-T1-10)
	QuotaWindow       = 24 * time.Hour
	ProgressThrottle  = time.Second
	CancelGrace       = 2 * time.Second
	RSSPoll           = 500 * time.Millisecond
	WallFactor        = 3 // wall deadline = WallFactor x budget, at least 60 s
	MaxAttempts       = 2 // transforms.attempts CHECK
	WorkSweepAge      = time.Hour
	LeaseTTL          = 60 * time.Second

	DSPVersion        = "mm-dsp/1" // part of every spec: any algorithm change bumps it and therefore the hash (D-T1-3)
	TargetLUFSDefault = -16.0      // the target when the primary parent has no measured loudness (D-T1-14)
	CeilingDBTP       = -1.0       // the true-peak ceiling of every transform (D-T1-14); no kind may raise it (C225.5)
	MaxLimitingLU     = 6.0        // the limiter may cost at most this much loudness before the target is lowered
)
