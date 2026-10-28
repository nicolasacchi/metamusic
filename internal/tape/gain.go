package tape

import "math"

// GainFactor is the linear factor of a gain in centi-dB: 10^(cdB/2000). It is computed as a float64 once per call, with an
// explicit conversion so that no compiler may fuse it into a later operation.
func GainFactor(gainCDB int) float64 {
	return float64(math.Pow(10, float64(gainCDB)/2000))
}

// ApplyGain multiplies 24-bit samples (held in int32) by the static gain of gainCDB centi-dB into dst, rounding each product
// to the nearest integer, halves away from zero, and clamping to +-8 388 607. It returns the number of samples that had to
// be clamped: the export fails with TAPE_CLIPPED when that is not 0 (the peak guard of PlanGains makes 0 the only reachable
// value for measured items). Gain 0 copies the samples unchanged, so a full-scale negative sample survives it.
// dst must be at least as long as src and may be src itself.
//
// One float64 product per sample with explicit float64 conversions, so FMA contraction cannot make amd64 and arm64 differ.
func ApplyGain(dst []int32, src []int32, gainCDB int) (clipped int) {
	dst = dst[:len(src)]
	if gainCDB == 0 {
		copy(dst, src)
		return 0
	}
	f := GainFactor(gainCDB)
	for i, v := range src {
		x := math.Round(float64(float64(v) * f))
		switch {
		case x > fullScale:
			dst[i], clipped = fullScale, clipped+1
		case x < -fullScale:
			dst[i], clipped = -fullScale, clipped+1
		default:
			dst[i] = int32(x)
		}
	}
	return clipped
}
