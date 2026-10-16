package sounds

import "math"

// TempoBand maps a BPM to its band (D-L1-17): slow (< 80), mid (80-109), upbeat (110-129), fast (130-159), rapid (>= 160).
func TempoBand(bpm float64) string {
	switch {
	case bpm < BandMid:
		return "slow"
	case bpm < BandUpbeat:
		return "mid"
	case bpm < BandFast:
		return "upbeat"
	case bpm < BandRapid:
		return "fast"
	}
	return "rapid"
}

// CompareTempo judges a measured tempo against the requested one (D-L1-17). octave: the measurement is within 6 % of
// 2x or 1/2 the request (an octave error of the estimator, not a mismatch); otherwise mismatch when the distance
// exceeds max(4, 5 % of the request). Either input nil gives (false, false).
func CompareTempo(requested, measured *float64) (mismatch, octave bool) {
	if requested == nil || measured == nil || *requested <= 0 {
		return false, false
	}
	r, m := *requested, *measured
	if math.Abs(m-2*r) <= TempoOctaveTol*2*r || math.Abs(m-r/2) <= TempoOctaveTol*r/2 {
		return false, true
	}
	return math.Abs(m-r) > math.Max(TempoAbsTol, TempoRelTol*r), false
}
