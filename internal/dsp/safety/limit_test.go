package safety

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

func TestTruePeak(t *testing.T) {
	t.Parallel()
	if got := TruePeakDBTP(dsptest.Dual(48000, dsptest.Sine(48000, 997, 1, 1))); math.Abs(got) > 0.05 {
		t.Errorf("0 dBFS sine: %.3f dBTP", got)
	}
	if got := TruePeakDBTP(dsptest.Dual(48000, dsptest.Silence(100))); got != -200 {
		t.Errorf("silence: %v", got)
	}
	if got := TruePeakDBTP(pcm.Buffer{Rate: 48000}); got != -200 {
		t.Errorf("empty: %v", got)
	}
	// linked channels: the hotter one decides
	b := dsptest.Stereo(48000, dsptest.Sine(48000, 997, 0.1, 0.2), dsptest.Sine(48000, 997, 0.5, 0.2))
	if got := TruePeakDBTP(b); math.Abs(got-dsptest.DBFS(0.5)) > 0.05 {
		t.Errorf("linked: %.3f", got)
	}
}

// interSampleHot is +-0.9 alternating in pairs, i.e. a fs/4 sine at 45 degrees: every sample is 0.9 but the waveform peaks at 1.27.
func interSampleHot(n int) pcm.Buffer {
	x := make([]float64, n)
	for i := range x {
		x[i] = 0.9
		if (i/2)%2 == 1 {
			x[i] = -0.9
		}
	}
	return dsptest.Dual(48000, x)
}

func TestLimit_Ceiling(t *testing.T) {
	t.Parallel()
	const rate = 16000
	noise := dsptest.Dual(rate, dsptest.Noise(1, rate*2, 4)) // peaks at about +12 dBFS (sigma 4, 4 sigma)
	b := dsptest.Stereo(rate, noise.Ch[0], dsptest.Noise(2, rate*2, 4))
	if p := dsptest.PeakAbs(b.Ch[0]); p < 3.5 {
		t.Fatalf("test noise peak %v is not hot enough", p)
	}
	info := Limit(&b, -1.0, nil)
	if tp := TruePeakDBTP(b); tp > -0.95 {
		t.Errorf("noise at +12 dBFS: true peak %.3f dBTP after limiting, want <= -0.95", tp)
	}
	if info.MaxReductionDB < 12 || info.EngagedFraction < 0.9 {
		t.Errorf("info %+v", info)
	}

	hot := interSampleHot(4800)
	before := TruePeakDBTP(hot)
	if sp := dsptest.PeakAbs(hot.Ch[0]); sp > 0.9001 || before < -0.9 {
		t.Fatalf("the pattern must be sample-safe (%.3f) and inter-sample hot (%.3f dBTP)", sp, before)
	}
	Limit(&hot, -1.0, nil)
	if after := TruePeakDBTP(hot); after > -0.95 {
		t.Errorf("inter-sample pattern: %.3f dBTP after (was %.3f), want <= -0.95", after, before)
	}
}

func TestLimit_Transparent(t *testing.T) {
	t.Parallel()
	b := dsptest.Dual(48000, dsptest.Sine(48000, 997, dsptest.DBToLin(-20), 1))
	orig := b.Clone()
	info := Limit(&b, -1.0, nil)
	if info.EngagedFraction != 0 || info.MaxReductionDB != 0 {
		t.Errorf("a -20 dBFS sine engaged the limiter: %+v", info)
	}
	for c := range 2 {
		for i := range orig.Ch[c] {
			if math.Abs(b.Ch[c][i]-orig.Ch[c][i]) >= 1e-6 {
				t.Fatalf("sample %d changed by %g", i, b.Ch[c][i]-orig.Ch[c][i])
			}
		}
	}
}

func TestLimit_SingleClickAttackAndRelease(t *testing.T) {
	t.Parallel()
	const rate = 48000
	x := dsptest.Scale(dsptest.Sine(rate, 200, 1, 1), 0.05)
	x[24000] = 4 // one hot sample
	b := dsptest.Dual(rate, x)
	Limit(&b, -1.0, &dsp.Meter{})
	c := dsp.Pow(10, -1.0/20)
	if p := math.Abs(b.Ch[0][24000]); p > c*1.0001 {
		t.Errorf("the hot sample is %v, over the ceiling %v", p, c)
	}
	// the gain recovers: 400 ms later the sine is back to within 0.2 dB
	i := 24000 + 19200
	if g := math.Abs(b.Ch[0][i]) / math.Abs(x[i]); x[i] != 0 && g < 0.97 {
		t.Errorf("gain %.3f 400 ms after the click: release too slow", g)
	}
	// and the reduction began before the click (lookahead): the samples just before it are already lowered
	if g := math.Abs(b.Ch[0][24000-100]) / math.Abs(x[24000-100]); g > 0.999 {
		t.Errorf("no look-ahead: gain %.4f 100 samples before the click", g)
	}
}

func TestLimit_Meter(t *testing.T) {
	t.Parallel()
	m := &dsp.Meter{}
	b := dsptest.Dual(8000, dsptest.Sine(8000, 440, 0.1, 1))
	Limit(&b, -1, m)
	if m.Used() == 0 {
		t.Error("Limit charged no units")
	}
}

func TestSlidingMinAndBoxcar(t *testing.T) {
	t.Parallel()
	g := []float64{1, 1, 0.5, 1, 1, 1, 0.8, 1, 1}
	want := []float64{0.5, 0.5, 0.5, 1, 0.8, 0.8, 0.8, 1, 1}
	got := slidingMinAhead(g, 2)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slidingMinAhead %v, want %v", got, want)
		}
	}
	box := boxcarBack([]float64{1, 1, 0, 1, 1}, 2)
	for i, w := range []float64{1, 1, 0.5, 0.5, 1} {
		if math.Abs(box[i]-w) > 1e-12 {
			t.Fatalf("boxcarBack %v", box)
		}
	}
	// a long input exercises the deque compaction
	long := make([]float64, 300000)
	for i := range long {
		long[i] = 1 - float64(i%7)/10
	}
	m := slidingMinAhead(long, 3)
	for _, i := range []int{0, 1000, 299990} {
		w := 1.0
		for k := i; k <= i+3 && k < len(long); k++ {
			w = math.Min(w, long[k])
		}
		if m[i] != w {
			t.Fatalf("long slidingMinAhead[%d] = %v, want %v", i, m[i], w)
		}
	}
}
