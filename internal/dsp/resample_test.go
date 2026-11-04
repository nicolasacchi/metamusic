package dsp_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

func TestResample_Sine(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(16000, 440, 1, 1)
	y := dsp.Resample(x, 22050.0/16000, 0)
	if got := dsptest.PeakFreq(y[1000:len(y)-1000], 22050); math.Abs(got-440) > 1 {
		t.Errorf("peak at %.2f Hz, want 440 +- 1", got)
	}
	// alias and image products: everything outside +-4 bins of the sine under -80 dB re the sine
	body := y[2000 : len(y)-2000]
	n := 1 << 15
	mag := dsptest.Spectrum(body, n)
	var peak float64
	for _, m := range mag {
		peak = math.Max(peak, m)
	}
	binHz := 22050.0 / float64(n)
	for k, m := range mag {
		if math.Abs(float64(k)*binHz-440) > 50 && dsptest.DBFS(m/peak) > -80 {
			t.Fatalf("spurious component %.1f dB at %.0f Hz", dsptest.DBFS(m/peak), float64(k)*binHz)
		}
	}
	if a := dsptest.RMS(body) * math.Sqrt2; math.Abs(a-1) > 0.01 {
		t.Errorf("amplitude %.4f, want 1 +- 0.01", a)
	}
}

func TestResample_Downsample(t *testing.T) {
	t.Parallel()
	// 48 k -> 16 k: a 1 kHz tone survives, a 12 kHz tone (above the new Nyquist) is removed
	const r = 48000
	x := dsptest.Sine(r, 1000, 0.5, 0.5)
	hi := dsptest.Sine(r, 12000, 0.5, 0.5)
	sum := make([]float64, len(x))
	for i := range sum {
		sum[i] = x[i] + hi[i]
	}
	y := dsp.Resample(sum, 1.0/3, 0)
	body := y[500 : len(y)-500]
	mag := dsptest.Spectrum(body, 1<<13)
	var peak, other float64
	for k, m := range mag {
		f := float64(k) * 16000 / float64(1<<13)
		if math.Abs(f-1000) < 60 {
			peak = math.Max(peak, m)
		} else {
			other = math.Max(other, m)
		}
	}
	if db := dsptest.DBFS(other / peak); db > -75 {
		t.Errorf("the 12 kHz tone leaks at %.1f dB re the 1 kHz tone", db)
	}
}

func TestResample_Length(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		n     int
		ratio float64
		want  int
	}{{1000, 1, 1000}, {1000, 48000.0 / 44100, 1088}, {1001, 0.5, 501}, {1000, 3, 3000}, {7, 0.25, 2}, {0, 2, 0}, {3, 1e-9, 0}} {
		if got := len(dsp.Resample(make([]float64, c.n), c.ratio, 8)); got != c.want {
			t.Errorf("len(Resample(%d samples, %v)) = %d, want %d", c.n, c.ratio, got, c.want)
		}
	}
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if dsp.Resample([]float64{1, 2}, bad, 8) != nil {
			t.Errorf("ratio %v accepted", bad)
		}
	}
}

func TestResample_Identity(t *testing.T) {
	t.Parallel()
	// ratio 1 on a band-limited signal: the interpolator is evaluated at the sample points, where the kernel is a near delta
	x := dsptest.Sine(16000, 300, 0.4, 0.05)
	hi := dsptest.Sine(16000, 3000, 0.3, 0.05)
	for i := range x {
		x[i] += hi[i]
	}
	y := dsp.Resample(x, 1, 0)
	for i := 40; i < len(x)-40; i++ {
		if math.Abs(y[i]-x[i]) > 2e-3 {
			t.Fatalf("sample %d: %g vs %g", i, y[i], x[i])
		}
	}
}

// TestResample_MixedRateInput: a 44.1 kHz second input of a morph lands on the first input's 48 kHz grid (D-T1-19): a tone
// keeps its frequency and level, and the length is the exact ratio of the rates.
func TestResample_MixedRateInput(t *testing.T) {
	t.Parallel()
	second := dsptest.Sine(44100, 1000, 0.5, 0.5)
	on48 := dsp.Resample(second, 48000.0/44100, 0)
	if want := int(math.Floor(float64(len(second))*48000/44100 + 0.5)); len(on48) != want {
		t.Fatalf("length %d, want %d", len(on48), want)
	}
	if f := dsptest.PeakFreq(on48[500:len(on48)-500], 48000); math.Abs(f-1000) > 1 {
		t.Errorf("tone moved to %.2f Hz", f)
	}
	if a := dsptest.RMS(on48[500:len(on48)-500]) * math.Sqrt2; math.Abs(a-0.5) > 0.01 {
		t.Errorf("amplitude %.4f, want 0.5", a)
	}
}

func TestResample_Metered(t *testing.T) {
	t.Parallel()
	x := make([]float64, 5000)
	m := &dsp.Meter{Max: 100}
	if y, err := dsp.ResampleMetered(x, 2, 32, m); err == nil || y != nil {
		t.Errorf("a tiny budget was not enforced: %v", err)
	}
	m = &dsp.Meter{}
	if _, err := dsp.ResampleMetered(x, 2, 32, m); err != nil || m.Used() != dsp.Units.ResampleSample(32)*10000 {
		t.Errorf("units %d, err %v", m.Used(), err)
	}
}
