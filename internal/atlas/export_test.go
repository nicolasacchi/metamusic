package atlas

// Test seams for the unexported fixed-point helpers.
var (
	Ilog2Q16 = ilog2q16
	Isqrt    = isqrt
)

// SpiralEntry returns cos and sin of a whole degree in Q16.
func SpiralEntry(d int) (int, int) { return int(spiralCos[d]), int(spiralSin[d]) }
