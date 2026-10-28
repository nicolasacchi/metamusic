// Package tape plans and renders the lossless tape export of a published Record (C128). This file holds the constants of
// C118; plan.go and gain.go hold the pure loudness plan and the gain stage.
package tape

import "time"

// Limits and timings of the tape export (C118, D-L2-17 to D-L2-19, D-L2-33). They change only by a decision.
const (
	// MaxItems and MaxDurationS bound one tape: 60 items and two hours.
	MaxItems     = 60
	MaxDurationS = 7200
	// Rate and Bits are the fixed format of a tape: 48 kHz, 24-bit (stereo).
	Rate = 48000
	Bits = 24
	// TargetMinLUFS and TargetMaxLUFS clamp the automatic target.
	TargetMinLUFS = -23.0
	TargetMaxLUFS = -14.0
	// SilentLUFS is the integrated loudness at or below which an item counts as silent: gain 0, excluded from the median.
	SilentLUFS = -60.0
	// MaxCutCDB and MaxBoostCDB clamp a per-item gain, in centi-dB.
	MaxCutCDB   = -1200
	MaxBoostCDB = 600
	// PeakCeilingDBTP is the true-peak ceiling the gain must leave: gain <= PeakCeilingDBTP - truePeak.
	PeakCeilingDBTP = -1.0
	// ItemTimeout bounds the decode of one item; LeaseTTL is the runner lease of a running tape.
	ItemTimeout = 120 * time.Second
	LeaseTTL    = 60 * time.Second
	// RetentionDays is how long a ready tape file is kept.
	RetentionDays = 14
	// MaxBytes is the largest tape file; Chunk is the size of one write of the gain stage to the encoder pipe.
	MaxBytes = 2 << 30
	Chunk    = 1 << 20
	// MaxLivePerRecord and MaxReadyBytes are the disk quotas (D-L2-33).
	MaxLivePerRecord = 3
	MaxReadyBytes    = 8 << 30
)

// Fixed-point scale of the plan: loudness and gain are integers in centi-units (hundredths of an LU or dB).
const (
	centi     = 100
	tenthStep = 10 // one decimal of LU in centi-units
	// maxSane bounds a believable loudness or true-peak reading in centi-units; anything beyond (or not finite) is unmeasured.
	maxSane = 100_000
)

// 24-bit full scale used by the gain stage.
const fullScale = 8_388_607
