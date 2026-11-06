package hrtf

import (
	"math"
	"math/cmplx"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/filt"
)

// peakPos is the position of the largest tap: where a click arrives in a response that is a spike (the parametric model).
func peakPos(h []float64) int {
	best := 0
	for i, v := range h {
		if math.Abs(v) > math.Abs(h[best]) {
			best = i
		}
	}
	return best
}

// iacc is the interaural delay, in samples, of b relative to a: the lag of the maximum of their cross-correlation after both
// are low-passed at 1.5 kHz (the usual definition of the ITD of a measured head, insensitive to the noisy onset of a
// shadowed ear), refined by a parabola through the peak.
func iacc(a, b []float64, rate int) float64 {
	lp := filt.LowPass(rate, 1500, filt.Butterworth)
	pad := func(x []float64) []float64 {
		y, _ := lp.Process(append(append([]float64(nil), x...), make([]float64, 256)...), nil)
		return y
	}
	x, y := pad(a), pad(b)
	corr := func(lag int) float64 {
		var s float64
		for i := range x {
			if j := i + lag; j >= 0 && j < len(y) {
				s += x[i] * y[j]
			}
		}
		return s
	}
	best, bv := -100, math.Inf(-1)
	for lag := -100; lag <= 100; lag++ {
		if v := corr(lag); v > bv {
			best, bv = lag, v
		}
	}
	l, r := corr(best-1), corr(best+1)
	return float64(best) + 0.5*(l-r)/(l-2*bv+r)
}

func TestFracKernel(t *testing.T) {
	t.Parallel()
	if k := FracKernel(0); k[FracCentre] != 1 {
		t.Errorf("frac 0 is not a unit impulse: %v", k)
	}
	for frac := 0; frac < FracSteps; frac += 7 {
		k := FracKernel(frac)
		var sum, phase float64
		for _, v := range k {
			sum += v
		}
		if math.Abs(sum-1) > 1e-12 {
			t.Errorf("frac %d: DC gain %v", frac, sum)
		}
		// the delay at 1 kHz (48 kHz rate) from the phase of the kernel's frequency response
		var z complex128
		for j, v := range k {
			z += complex(v, 0) * cmplx.Exp(complex(0, -2*math.Pi*1000/48000*float64(j)))
		}
		phase = -cmplx.Phase(z) / (2 * math.Pi * 1000 / 48000)
		want := float64(FracCentre) + float64(frac)/FracSteps
		if math.Abs(phase-want) > 0.02 {
			t.Errorf("frac %d: delay %.4f samples, want %.4f", frac, phase, want)
		}
		if mag := cmplx.Abs(z); math.Abs(mag-1) > 0.01 {
			t.Errorf("frac %d: gain at 1 kHz %.4f", frac, mag)
		}
	}
}

// TestParametric_WoodworthITD: a click on one side arrives at the far ear a Woodworth delay after the near ear (within one
// sample), and the delay the filter chain applies is the quantised Woodworth value to 1/64 sample (checked through the phase of
// the two ears at 500 Hz, once the analytic phase of the head-shadow filters is removed).
func TestParametric_WoodworthITD(t *testing.T) {
	t.Parallel()
	for _, rate := range []int{16000, 44100, 48000} {
		for _, az := range []int{10, 30, 45, 60, 90, -30, -90} {
			near, far := Right, Left
			if az < 0 {
				near, far = Left, Right
			}
			th := math.Abs(float64(az)) * math.Pi / 180
			want := headRadiusM / speedOfSnd * (th + math.Sin(th)) * float64(rate)
			got := float64(peakPos(ParametricIR(az, far, rate)) - peakPos(ParametricIR(az, near, rate)))
			if math.Abs(got-want) > 1 {
				t.Errorf("rate %d az %d: click lag %.2f samples, Woodworth %.2f", rate, az, got, want)
			}
			q := float64(ITD64(az, rate)) / FracSteps
			if math.Abs(q-want) > 1.0/FracSteps {
				t.Errorf("rate %d az %d: quantised ITD %.4f, want %.4f", rate, az, q, want)
			}
			// phase at 500 Hz: z = sum h[n] e^{-jwn}; the delay beyond the shadow filter is the phase difference / w
			const f = 500.0
			w := 2 * math.Pi * f / float64(rate)
			resp := func(ear Ear) complex128 {
				var z complex128
				for n, v := range ParametricIR(az, ear, rate) {
					z += complex(v, 0) * cmplx.Exp(complex(0, -w*float64(n)))
				}
				b0, b1, a1 := ShadowCoefficients(earAngle(az, ear), rate)
				e := cmplx.Exp(complex(0, -w))
				z /= (complex(b0, 0) + complex(b1, 0)*e) / (1 + complex(a1, 0)*e)
				return z
			}
			if d := -cmplx.Phase(resp(far)/resp(near)) / w; math.Abs(d-q) > 0.05 {
				t.Errorf("rate %d az %d: applied delay %.3f samples, want %.3f", rate, az, d, q)
			}
		}
	}
}

// TestParametric_Shadow compares the discretised head-shadow filter with the analogue Brown-Duda curve at 500 Hz, 2 kHz and
// 4 kHz (48 kHz rate): within 1.5 dB.
func TestParametric_Shadow(t *testing.T) {
	t.Parallel()
	const rate = 48000
	w0 := speedOfSnd / headRadiusM
	for _, theta := range []int{0, 60, 90, 120, 150, 180} {
		alpha := 1.05 + 0.95*math.Cos(float64(theta)*math.Pi/150)
		for _, f := range []float64{500, 2000, 4000} {
			s := complex(0, 2*math.Pi*f)
			analog := cmplx.Abs((1 + complex(alpha, 0)*s/complex(2*w0, 0)) / (1 + s/complex(2*w0, 0)))
			b0, b1, a1 := ShadowCoefficients(theta, rate)
			z := cmplx.Exp(complex(0, -2*math.Pi*f/rate))
			digital := cmplx.Abs((complex(b0, 0) + complex(b1, 0)*z) / (1 + complex(a1, 0)*z))
			if d := 20 * math.Log10(digital/analog); math.Abs(d) > 1.5 {
				t.Errorf("theta %d, %.0f Hz: digital %.2f dB vs analogue %.2f dB", theta, f, 20*math.Log10(digital), 20*math.Log10(analog))
			}
		}
		if b0, b1, a1 := ShadowCoefficients(theta, rate); math.Abs((b0+b1)/(1+a1)-1) > 1e-12 {
			t.Errorf("theta %d: DC gain is not 1", theta)
		}
	}
}

func TestParametric_Symmetry(t *testing.T) {
	t.Parallel()
	for _, az := range []int{-90, -45, -10, 0, 10, 30, 90, 135, 180} {
		l, r := ParametricIR(az, Left, 48000), ParametricIR(-az, Right, 48000)
		for n := range l {
			if l[n] != r[n] {
				t.Fatalf("az %d tap %d: left %v, mirrored right %v", az, n, l[n], r[n])
			}
		}
	}
	// front and back mirror images share the interaural delay
	if ITD64(30, 48000) != ITD64(150, 48000) || ITD64(-30, 48000) != ITD64(-150, 48000) {
		t.Error("ITD is not folded to +-90 degrees")
	}
}

func TestParametric_Deterministic(t *testing.T) {
	t.Parallel()
	a, b := ParametricIR(45, Left, 44100), ParametricIR(45, Left, 44100)
	for n := range a {
		if a[n] != b[n] {
			t.Fatal("two calls differ")
		}
	}
	if len(a) != ParametricTaps {
		t.Errorf("length %d", len(a))
	}
}

func TestIRAt_Resampled(t *testing.T) {
	t.Parallel()
	tb := Default()
	itd48 := iacc(tb.IR(90, 0, Right), tb.IR(90, 0, Left), Rate)
	for _, rate := range []int{16000, 44100, 96000} {
		l, err := tb.IRAt(90, 0, Left, rate, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := tb.IRAt(90, 0, Right, rate, nil)
		itd := iacc(r, l, rate) * Rate / float64(rate) // in 48 kHz samples
		if math.Abs(itd-itd48) > 1 {
			t.Errorf("rate %d: ITD %.2f samples (in 48 kHz units), %.2f at 48 kHz", rate, itd, itd48)
		}
		if want := int(math.Floor(Taps*float64(rate)/Rate + 0.5)); len(l) != want {
			t.Errorf("rate %d: %d taps, want %d", rate, len(l), want)
		}
	}
	if _, err := tb.IRAt(0, 0, Left, 48000, &dsp.Meter{Max: 1}); err != nil {
		t.Errorf("48 kHz needs no resampling and so no budget: %v", err)
	}
	if _, err := tb.IRAt(0, 0, Left, 44100, &dsp.Meter{Max: 1}); err == nil {
		t.Error("resampling ignored the meter")
	}
}

func TestHRTF_MeasuredITD(t *testing.T) {
	t.Parallel()
	tb := Default()
	// a source at +90 degrees: the left ear hears it about 0.8 ms after the right ear (the plan's 0.55-0.75 ms does not fit
	// this dummy head: its interaural cross-correlation peaks at 38-39 samples whatever the low-pass)
	got := iacc(tb.IR(90, 0, Right), tb.IR(90, 0, Left), Rate)
	if got < 34 || got > 42 {
		t.Errorf("ITD at +90 = %.1f samples, want 34-42", got)
	}
	if front := iacc(tb.IR(0, 0, Right), tb.IR(0, 0, Left), Rate); math.Abs(front) > 0.01 {
		t.Errorf("ITD at 0 = %v, want 0 (the table is symmetric)", front)
	}
	// 30 degrees is between the two extremes
	if mid := iacc(tb.IR(30, 0, Right), tb.IR(30, 0, Left), Rate); mid < 10 || mid > 20 {
		t.Errorf("ITD at +30 = %.1f samples, want 10-20", mid)
	}
}
