package morning

import "math"

// Loudness targets of a Morning Crate side.
const (
	// TargetLUFS is the integrated loudness every chapter is matched to.
	TargetLUFS = -16.0
	// CeilingTP is the true-peak ceiling (dBTP) a gain may not exceed.
	CeilingTP = -1.0
)

// Gain returns the per-take gain in centi-dB for a master measured at lufs
// (integrated) and truePeak (dBTP): g = min(-16 - lufs, -1 - truePeak),
// rounded half away from zero. limited is true iff the true-peak term binds
// strictly, meaning the take cannot reach the target without breaking the
// ceiling.
func Gain(lufs, truePeak float64) (cdb int, limited bool) {
	byLoudness := TargetLUFS - lufs
	byPeak := CeilingTP - truePeak
	g := byLoudness
	if byPeak < byLoudness {
		g, limited = byPeak, true
	}
	// Snap to micro-dB first so binary noise (0.4999999...) cannot flip a tie.
	return int(math.Round(math.Round(g*1e6) / 1e4)), limited
}
