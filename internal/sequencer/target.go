package sequencer

// targetFor is the target relative energy (0..10000) of position p of n under the strategy; -1 when the strategy has
// no shape. n = 1 targets the middle.
func targetFor(s Strategy, p, n int) int {
	spec, ok := SpecOf(s)
	if !ok {
		return -1
	}
	if n <= 1 && spec.Curve != CurveNone {
		return 5000
	}
	switch spec.Curve {
	case CurveArc:
		f := fraction(p, n)
		if f <= 7000 {
			return 3000 + f
		}
		return 10000 - (f-7000)*5000/3000
	case CurveLinear:
		return fraction(p, n)
	}
	return -1
}

// fraction is the position as 0..10000; a single song sits at 5000.
func fraction(p, n int) int {
	if n <= 1 {
		return 5000
	}
	return p * 10000 / (n - 1)
}
