package atlas

import "math/bits"

// ilog2q16 returns log2(v) in Q16 fixed point (round to nearest) for v >= 1, computed bit by bit: the integer part is the
// position of the top bit, the fraction comes from repeatedly squaring the normalised mantissa. v <= 0 gives 0.
func ilog2q16(v int) int {
	if v <= 0 {
		return 0
	}
	n := bits.Len64(uint64(v)) - 1
	const one = 1 << 30 // mantissa in Q30, kept in [one, 2*one)
	var x uint64
	if n >= 30 {
		x = uint64(v) >> (n - 30)
	} else {
		x = uint64(v) << (30 - n)
	}
	r := n << 16
	// 17 fractional bits: the 17th decides the rounding of the 16th.
	var frac int
	for i := 0; i < 17; i++ {
		x = x * x >> 30
		frac <<= 1
		if x >= 2*one {
			x >>= 1
			frac |= 1
		}
	}
	return r + (frac+1)>>1
}

// isqrt is the integer square root: the largest r with r*r <= v.
func isqrt(v int64) int64 {
	if v <= 0 {
		return 0
	}
	r := int64(1) << ((bits.Len64(uint64(v)) + 1) / 2)
	for {
		n := (r + v/r) >> 1
		if n >= r {
			return r
		}
		r = n
	}
}
