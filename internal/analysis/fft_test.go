package analysis

import (
	"math"
	"testing"
)

func TestFFT_KnownBins(t *testing.T) {
	const n = 8192
	const sr = 48000.0
	re := make([]float64, n)
	im := make([]float64, n)
	w := Hann(n)
	for i := range re {
		re[i] = math.Sin(2*math.Pi*1000*float64(i)/sr) * w[i]
	}
	FFT(re, im)
	pow := make([]float64, n/2+1)
	var total, best float64
	peak := 0
	for k := range pow {
		pow[k] = re[k]*re[k] + im[k]*im[k]
		total += pow[k]
		if pow[k] > best {
			best, peak = pow[k], k
		}
	}
	if want := int(math.Round(1000 * n / sr)); peak != want || peak != 171 {
		t.Fatalf("peak bin = %d, want %d", peak, want)
	}
	var near float64
	for k := peak - 2; k <= peak+2; k++ {
		near += pow[k]
	}
	if r := near / total; r <= 0.99 {
		t.Fatalf("energy ratio within +-2 bins = %.4f", r)
	}
}

func TestFFT_Parseval(t *testing.T) {
	const n = 1024
	re := make([]float64, n)
	im := make([]float64, n)
	var et float64
	x := uint32(12345)
	for i := range re {
		x = x*1664525 + 1013904223
		re[i] = float64(int32(x))/float64(1<<31) + 0.1
		im[i] = 0
		et += re[i] * re[i]
	}
	FFT(re, im)
	var ef float64
	for i := range re {
		ef += re[i]*re[i] + im[i]*im[i]
	}
	ef /= n
	if math.Abs(ef-et)/et > 1e-9 {
		t.Fatalf("Parseval: time %g freq %g", et, ef)
	}
}

func TestFFT_PanicsOnNonPowerOfTwo(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	FFT(make([]float64, 6), make([]float64, 6))
}
