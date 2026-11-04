package align

// Fold is the octave fold of a measured tempo toward its partner's (C204.5): of the readings measured/2, measured and
// measured*2 it returns the exponent k in {-1, 0, +1} of the one nearest the partner in log distance. Ties go to 0, and a
// reading must be closer by more than a part in 10^12 to displace the one before it. A non-positive tempo is returned
// unfolded (0). The distance is compared as max(q, 1/q) of the ratio q, which orders readings like |ln q| without a log.
func Fold(partner, measured float64) int {
	if partner <= 0 || measured <= 0 {
		return 0
	}
	dist := func(bpm float64) float64 {
		q := bpm / partner
		if q < 1 {
			q = 1 / q
		}
		return q
	}
	best, bestD := 0, dist(measured)
	for _, k := range [...]int{-1, 1} {
		if d := dist(Folded(measured, k)); d < bestD*(1-1e-12) {
			best, bestD = k, d
		}
	}
	return best
}

// Folded is measured * 2^k for k in {-1, 0, +1}.
func Folded(measured float64, k int) float64 {
	switch {
	case k < 0:
		return measured / 2
	case k > 0:
		return measured * 2
	}
	return measured
}
