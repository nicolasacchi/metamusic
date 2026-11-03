// Package safety is the loudness safety stage every transform ends in (C207, D-T1-14): BS.1770 loudness, a true-peak limiter
// and Finish. T1-F1 froze FinishIn (including the true-peak ceiling of the hi-fi kinds, C225.5); T1-D1 implements the stage.
package safety

import "errors"

// The only two failures of a render (D-T1-14): everything else is derived, never failed on.
var (
	ErrNonFinite = errors.New("safety: non-finite sample")
	ErrSilent    = errors.New("safety: silent output")
	// ErrCeiling is the bug alarm of the last step: the true peak still exceeds the ceiling after one re-limit.
	ErrCeiling = errors.New("safety: true peak above the ceiling after limiting")
)

// The ceiling bounds of FinishIn.CeilingDBTP: the default is -1.0 dBTP, master may ask for as low as -3.0, and no kind can
// raise it above -1.0.
const (
	DefaultCeilingDBTP = -1.0
	MinCeilingDBTP     = -3.0
	MaxLimitingLU      = 6.0
)

// FinishIn is the input of Finish.
type FinishIn struct {
	TargetLUFS float64 // the requested integrated loudness
	RefLUFS    float64 // the parent's measured loudness, echoed in the report only
	OwnFades   bool    // the operator brings its own fades (glacier): skip the 10 ms fades
	// CeilingDBTP is the true-peak ceiling: 0 means DefaultCeilingDBTP; a value is clamped to [MinCeilingDBTP,
	// DefaultCeilingDBTP], so no kind can raise it above -1.0 dBTP (C225.5).
	CeilingDBTP float64
}

// Ceiling returns the effective ceiling of in: the default for 0, otherwise the value clamped to [-3.0, -1.0].
func (in FinishIn) Ceiling() float64 {
	switch c := in.CeilingDBTP; {
	case c == 0:
		return DefaultCeilingDBTP
	case c > DefaultCeilingDBTP:
		return DefaultCeilingDBTP
	case c < MinCeilingDBTP:
		return MinCeilingDBTP
	default:
		return c
	}
}

// FinishReport is what Finish records (C207.4).
type FinishReport struct {
	TargetLUFSRequested float64
	TargetLUFSEffective float64
	RefLUFS             float64 // echoed from FinishIn
	CeilingDBTP         float64
	GainDB              float64 // the gain to the effective target, before the limiter
	InputLUFS           float64 // loudness after DC removal and fades, before the gain
	OutputLUFS          float64
	OutputTruePeakDBTP  float64
	ShortfallLU         float64 // effective target minus output loudness
	Limited             bool    // the limiter engaged and the target was lowered to keep ShortfallLU within MaxLimitingLU
	MaxReductionDB      float64
	EngagedFraction     float64
	DCRemoved           bool
}
