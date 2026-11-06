package hrtf

import "math"

// Fractional delay for the parametric model (C218.3): delays are fixed-point, in 1/64 sample, applied by a 16-tap windowed sinc
// whose tables are closed forms of Sin and Cos, so a delay means the same thing on every host.

// FracSteps is the number of fractions per sample.
const FracSteps = 64

// FracTaps is the length of the delay kernel; its centre tap is FracCentre.
const (
	FracTaps   = 16
	FracCentre = 7
)

// FracKernel is the kernel that delays by frac/64 samples around tap FracCentre (frac in 0..63): a sinc under a Blackman
// window centred on the delayed position, normalised to unit sum so the DC gain is exactly 1. frac = 0 is a unit impulse.
func FracKernel(frac int) [FracTaps]float64 {
	var k [FracTaps]float64
	if frac == 0 {
		k[FracCentre] = 1
		return k
	}
	var sum float64
	for j := range k {
		x := float64(j-FracCentre) - float64(frac)/FracSteps
		w := 0.42 + float64(0.5*math.Cos(math.Pi*x/8)) + float64(0.08*math.Cos(2*math.Pi*x/8))
		k[j] = float64(math.Sin(math.Pi*x) / (math.Pi * x) * w)
		sum += k[j]
	}
	for j := range k {
		k[j] /= sum
	}
	return k
}
