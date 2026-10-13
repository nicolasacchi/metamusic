package ladder

import "math"

// PBeats is the Davidson outcome distribution for theta_i against theta_j; it sums to 1.
// win is P(i beats j), loss is P(j beats i), tie is P(tie).
func PBeats(thI, thJ, nu float64) (win, tie, loss float64) {
	d := (thI - thJ) / 2
	// Scale by sqrt(pi_i*pi_j) and then by e^{|d|} so large |theta| cannot overflow.
	e := math.Exp(-math.Abs(d))
	hi, lo, t := 1.0, e*e, nu*e
	den := hi + lo + t
	if d >= 0 {
		return hi / den, t / den, lo / den
	}
	return lo / den, t / den, hi / den
}
