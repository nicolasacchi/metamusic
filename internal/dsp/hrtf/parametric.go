package hrtf

import "math"

// The parametric model (C221.3): no dataset, no licence. A Woodworth interaural time difference on the far ear and a
// Brown-Duda head-shadow filter per ear. It lateralises; it does not externalise (no pinna cues, no elevation, no distance).

const (
	// ParametricTaps is the length of a parametric response. It holds the 16-tap delay kernel, the largest delay at 48 kHz
	// (about 32 samples) and the decay of the shadow filter.
	ParametricTaps = 256

	headRadiusM = 0.0875 // a
	speedOfSnd  = 343.0  // c
	// delayLead is the fixed latency of both ears: the delay kernel starts after it, so the near ear is not clipped.
	delayLead = 8
)

// foldAz folds an azimuth to -90..90 degrees (front and back mirror images share an interaural time difference).
func foldAz(az int) int {
	for az > 180 {
		az -= 360
	}
	for az < -180 {
		az += 360
	}
	switch {
	case az > 90:
		return 180 - az
	case az < -90:
		return -180 - az
	}
	return az
}

// ITD64 is the Woodworth interaural time difference tau = (a/c)(theta + sin theta) for az degrees, in 1/64 samples at rate.
func ITD64(az, rate int) int {
	th := float64(abs(foldAz(az))) * math.Pi / 180
	tau := headRadiusM / speedOfSnd * (th + math.Sin(th))
	return int(math.Floor(tau*float64(rate)*FracSteps + 0.5))
}

// earAngle is the angle in degrees between the source direction and the axis of the ear (right ear at +90, left at -90).
func earAngle(az int, ear Ear) int {
	axis := 90
	if ear == Left {
		axis = -90
	}
	d := abs(az-axis) % 360
	if d > 180 {
		d = 360 - d
	}
	return d
}

// ShadowCoefficients are b0, b1, a1 of the Brown-Duda head-shadow filter H(s) = (1 + alpha s / 2w0) / (1 + s / 2w0),
// w0 = c/a, alpha = 1.05 + 0.95 cos(theta pi / 150 deg), discretised by the bilinear transform:
// H(z) = (b0 + b1 z^-1) / (1 + a1 z^-1), DC gain 1.
func ShadowCoefficients(thetaEarDeg, rate int) (b0, b1, a1 float64) {
	alpha := 1.05 + float64(0.95*math.Cos(float64(thetaEarDeg)*math.Pi/150))
	k := float64(2*rate) / float64(2*speedOfSnd/headRadiusM)
	return (1 + float64(k*alpha)) / (1 + k), (1 - float64(k*alpha)) / (1 + k), (1 - k) / (1 + k)
}

// ParametricIR is the response of ear to a source at az degrees (positive to the right) at the given rate: the head-shadow
// filter, and on the far ear the interaural delay (quantised to 1/64 sample), after a fixed lead of delayLead samples.
func ParametricIR(az int, ear Ear, rate int) []float64 {
	b0, b1, a1 := ShadowCoefficients(earAngle(az, ear), rate)
	delay64 := 0
	if (ear == Left && az > 0) || (ear == Right && az < 0) { // the far ear
		delay64 = ITD64(az, rate)
	}
	whole, frac := delay64/FracSteps, delay64%FracSteps
	start := delayLead + whole - FracCentre // first tap of the kernel
	kern := FracKernel(frac)
	// impulse response of the shadow filter, long enough for every output tap that can see it
	s := make([]float64, ParametricTaps)
	var y1, x1 float64
	for n := range s {
		x := 0.0
		if n == 0 {
			x = 1
		}
		s[n] = float64(b0*x) + float64(b1*x1) - float64(a1*y1)
		x1, y1 = x, s[n]
	}
	h := make([]float64, ParametricTaps)
	for n := range h {
		var acc float64
		for j, kj := range kern {
			if m := n - start - j; m >= 0 {
				acc += float64(kj * s[m])
			}
		}
		h[n] = acc
	}
	return h
}
