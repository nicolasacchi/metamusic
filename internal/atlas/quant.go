package atlas

// q16One is 1.0 in the layout's Q16 fixed point.
const q16One = 65536

// XY is a laid-out point in [0, 1].
type XY struct{ X, Y float64 }

// coord converts a Q16 coordinate to the wire value: clamped to [0, 1] and rounded to 4 decimals. This is the one place
// where a coordinate becomes a float; the layout itself is integer-only (D-L3-29).
func coord(q int) float64 {
	if q < 0 {
		q = 0
	}
	if q > q16One {
		q = q16One
	}
	return float64((q*10000+q16One/2)/q16One) / 10000
}
