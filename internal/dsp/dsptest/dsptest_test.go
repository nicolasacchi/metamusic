package dsptest_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

func TestDsptest_Peak(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		rate int
		f    float64
		secs float64
	}{{16000, 1000, 1}, {16000, 440, 1}, {16000, 1234.5, 0.5}, {48000, 997, 0.3}} {
		x := dsptest.Sine(c.rate, c.f, 0.7, c.secs)
		if got := dsptest.PeakFreq(x, c.rate); math.Abs(got-c.f) > 0.05 {
			t.Errorf("sine at %v Hz (%d Hz rate): peak %v", c.f, c.rate, got)
		}
	}
}

func TestDsptest_Clicks(t *testing.T) {
	t.Parallel()
	for _, bpm := range []float64{120, 100, 133.3, 90} {
		const rate = 16000
		x := dsptest.Clicks(rate, bpm, 0.8, 6)
		pos := dsptest.ClickPositions(x, 0.5, 100)
		if len(pos) < 8 {
			t.Fatalf("bpm %v: only %d clicks", bpm, len(pos))
		}
		want := 60 / bpm * rate
		for i := 1; i < len(pos); i++ {
			if d := float64(pos[i]-pos[i-1]) - want; math.Abs(d) > 1 {
				t.Errorf("bpm %v: spacing %d, want %.2f +- 1", bpm, pos[i]-pos[i-1], want)
			}
		}
		if float64(pos[len(pos)-1]-pos[0])-want*float64(len(pos)-1) > 1 {
			t.Errorf("bpm %v: the train drifts", bpm)
		}
	}
}

func TestDsptest_Helpers(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(8000, 100, 0.5, 1)
	if p := dsptest.PeakAbs(x); math.Abs(p-0.5) > 1e-3 {
		t.Errorf("PeakAbs %v", p)
	}
	if r := dsptest.RMS(x); math.Abs(r-0.5/math.Sqrt2) > 1e-4 {
		t.Errorf("RMS %v", r)
	}
	if dsptest.RMS(nil) != 0 {
		t.Error("RMS of nothing")
	}
	if db := dsptest.DBFS(0.1); math.Abs(db+20) > 1e-9 {
		t.Errorf("DBFS %v", db)
	}
	if g := dsptest.DBToLin(-20); math.Abs(g-0.1) > 1e-12 {
		t.Errorf("DBToLin %v", g)
	}
	if m := dsptest.Mean(dsptest.Offset(x, 0.25)); math.Abs(m-0.25) > 1e-9 {
		t.Errorf("Mean %v", m)
	}
	if got := dsptest.Concat(dsptest.Silence(3), []float64{1}, dsptest.Scale([]float64{2}, 3)); len(got) != 5 || got[4] != 6 {
		t.Errorf("Concat/Scale %v", got)
	}
	b := dsptest.Dual(8000, x)
	b.Ch[0][0] = 9
	if b.Ch[1][0] == 9 || b.Rate != 8000 {
		t.Error("Dual must copy")
	}
	if dsptest.Frames(16000, 0.25) != 4000 {
		t.Error("Frames")
	}
	if dsptest.UlpDiff(1, math.Nextafter(1, 2)) != 1 || dsptest.UlpDiff(-1, -1) != 0 || dsptest.UlpDiff(math.Copysign(0, -1), 0) != 0 {
		t.Error("UlpDiff")
	}
	if dsptest.UlpDiff(math.Nextafter(0, 1), math.Nextafter(0, -1)) != 2 {
		t.Error("UlpDiff across zero")
	}
	// band energy: a 1 kHz tone has its energy in 900-1100 Hz and almost none at 3-4 kHz
	tone := dsptest.Sine(16000, 1000, 1, 0.5)
	n := 1 << 13
	mag := dsptest.Spectrum(tone, n)
	var ref float64
	for _, m := range mag {
		ref = math.Max(ref, m)
	}
	if in, out := dsptest.BandDB(mag, 16000, n, 900, 1100, ref), dsptest.BandDB(mag, 16000, n, 3000, 4000, ref); in < -3 || out > -60 {
		t.Errorf("band levels %v dB / %v dB", in, out)
	}
	// noise and pink noise are deterministic and sensible
	if a, b := dsptest.Noise(1, 100, 1), dsptest.Noise(1, 100, 1); a[99] != b[99] {
		t.Error("Noise is not deterministic")
	}
	if r := dsptest.RMS(dsptest.Noise(2, 20000, 0.5)); math.Abs(r-0.5) > 0.02 {
		t.Errorf("Noise rms %v", r)
	}
	p := dsptest.Pink(3, 20000)
	if dsptest.RMS(p) == 0 || dsptest.PeakAbs(p) > 10 {
		t.Error("Pink noise level")
	}
	if dsptest.Stereo(1, []float64{1}, []float64{2}).Ch[1][0] != 2 {
		t.Error("Stereo")
	}
	defer func() {
		if recover() == nil {
			t.Error("Stereo with unequal channels must panic")
		}
	}()
	dsptest.Stereo(1, []float64{1}, nil)
}
