package ladder

import "errors"

// STUB(M2b-J1): the BTD fit lands in M2b-J1; until then every entry point reports ErrUnsupported.

// FitBTD fits the model by MM iterations (C47).
func FitBTD(n int, cmps []Comparison, o Options) (Fit, error) {
	return Fit{}, errors.ErrUnsupported
}

// LogPosterior evaluates the log posterior up to constants.
func LogPosterior(n int, cmps []Comparison, theta []float64, nu float64, o Options) float64 {
	return 0
}

// PBeats is the Davidson outcome distribution for theta_i against theta_j; it sums to 1.
func PBeats(thI, thJ, nu float64) (win, tie, loss float64) { return 0, 0, 0 }
