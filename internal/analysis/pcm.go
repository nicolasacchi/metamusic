package analysis

import "math"

// PCM is the shared mono decode of one take (C136.0): the first up-to-240 s at Rate Hz, float32 in [-1, 1]. tempo, key
// and energy are all pure functions of it. The decoder zeroes NaN and Inf samples; the analyzers re-check.
type PCM struct {
	Rate      int
	Samples   []float32
	AnalyzedS float64 // len(Samples) / Rate, rounded to 0.01 s
}

// NewPCM wraps samples decoded at rate Hz and fills AnalyzedS.
func NewPCM(samples []float32, rate int) PCM {
	p := PCM{Rate: rate, Samples: samples}
	if rate > 0 {
		p.AnalyzedS = round(float64(len(samples))/float64(rate), 2)
	}
	return p
}

// sanitize returns x with every NaN and Inf replaced by zero, copying only when one is found.
func sanitize(x []float32) []float32 {
	bad := func(v float32) bool { f := float64(v); return math.IsNaN(f) || math.IsInf(f, 0) }
	for i, v := range x {
		if !bad(v) {
			continue
		}
		y := append([]float32(nil), x...)
		for j := i; j < len(y); j++ {
			if bad(y[j]) {
				y[j] = 0
			}
		}
		return y
	}
	return x
}
