package filt

import (
	"context"
	"errors"
	"flag"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

const rate = 16000

var update = flag.Bool("update", false, "rewrite the coefficient golden")

// impulseMag measures |H(f)| of c by an FFT of its impulse response (not by the analytic formula under test).
func impulseMag(t *testing.T, c Cascade, f float64) float64 {
	t.Helper()
	const n = 1 << 15
	x := make([]float64, n)
	x[0] = 1
	y, err := c.Process(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	re, im := dsp.RFFT(y, n)
	k := int(math.Round(f * n / rate))
	return math.Hypot(re[k], im[k])
}

func db(x float64) float64 { return 20 * dsp.Log(x) / math.Ln10 }

func TestFilt_BiquadResponse(t *testing.T) {
	t.Parallel()
	hp := Cascade{HighPass(rate, 30, Butterworth)}
	ls := Cascade{LowShelf(rate, 250, 1.5, 0.7)}
	hs := Cascade{HighShelf(rate, 2500, -2, 0.7)}
	lr := LR4HighPass(rate, 120)
	for _, tc := range []struct {
		name string
		c    Cascade
		want func(f float64) float64 // analytic dB
	}{
		{"highpass", hp, func(f float64) float64 { return -10 * math.Log10(1+math.Pow(30/f, 4)) }},
		{"lr4", lr, func(f float64) float64 { return -20 * math.Log10(1+math.Pow(120/f, 4)) }},
		{"lowshelf", ls, nil},
		{"highshelf", hs, nil},
	} {
		for _, f := range []float64{20, 60, 250, 1000, 6000} {
			got := db(impulseMag(t, tc.c, f))
			if tc.want != nil {
				if w := tc.want(f); math.Abs(got-w) > 0.1 {
					t.Errorf("%s at %g Hz: %.3f dB, analytic %.3f", tc.name, f, got, w)
				}
			}
			if a := db(tc.c.Mag(rate, f)); math.Abs(a-got) > 0.1 {
				t.Errorf("%s at %g Hz: Mag %.3f dB, impulse %.3f", tc.name, f, a, got)
			}
		}
	}
	// shelf plateaus and the midpoint: gain at the plateau, half the gain (in dB) at f0
	if g := db(ls[0].Mag(rate, 20)); math.Abs(g-1.5) > 0.1 {
		t.Errorf("low shelf plateau %.3f dB, want 1.5", g)
	}
	if g := db(hs[0].Mag(rate, 7000)); math.Abs(g+2) > 0.15 {
		t.Errorf("high shelf plateau %.3f dB, want -2", g)
	}
	if g := db(ls[0].Mag(rate, 250)); math.Abs(g-0.75) > 0.05 {
		t.Errorf("low shelf at f0 %.3f dB, want 0.75", g)
	}
}

func TestFilt_LR4Crossover(t *testing.T) {
	t.Parallel()
	lo, hi := LR4LowPass(rate, 200), LR4HighPass(rate, 200)
	for name, c := range map[string]Cascade{"low": lo, "high": hi} {
		if g := db(c.Mag(rate, 200)); math.Abs(g+6.02) > 0.05 {
			t.Errorf("LR4 %s at the crossover %.3f dB, want -6.02", name, g)
		}
	}
}

func TestFilt_Metered(t *testing.T) {
	t.Parallel()
	x := dsptest.Noise(1, 20000, 1)
	m := &dsp.Meter{Max: 1000}
	if _, err := LR4HighPass(rate, 100).Process(x, m); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	m = &dsp.Meter{}
	if _, err := (Cascade{HighPass(rate, 100, Butterworth)}).Process(x, m); err != nil || m.Used() != 5*20000 {
		t.Errorf("used %d, err %v", m.Used(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HighPass(rate, 100, Butterworth).Process(x, &dsp.Meter{Ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled run: %v", err)
	}
}

// presets are the four master presets' filter parameters (tilt, low cut) at 48 kHz, the coefficients committed below.
func presetCoefs() []string {
	var out []string
	for _, p := range []struct {
		name string
		tilt float64
		cut  float64
	}{{"neutral", 0, 25}, {"warm", -1.5, 25}, {"bright", 1.5, 25}, {"punch", 0, 30}} {
		rows := []Biquad{HighPass(48000, p.cut, Butterworth)}
		if p.tilt != 0 {
			rows = append(rows, LowShelf(48000, 250, -p.tilt/2, 0.7), HighShelf(48000, 2500, p.tilt/2, 0.7))
		}
		for i, q := range rows {
			var s []string
			for _, v := range []float64{q.B0, q.B1, q.B2, q.A1, q.A2} {
				s = append(s, strconv.FormatUint(math.Float64bits(v), 16))
			}
			out = append(out, p.name+"."+strconv.Itoa(i)+" "+strings.Join(s, " "))
		}
	}
	return out
}

// TestFilt_Deterministic pins the coefficient bit patterns of the four presets (linux/amd64, GOAMD64=v1).
func TestFilt_Deterministic(t *testing.T) {
	dsptest.RequireGolden(t)
	got := strings.Join(presetCoefs(), "\n") + "\n"
	const path = "testdata/hifi_filters.txt"
	if *update {
		if err := os.WriteFile(path, []byte("# float64 bit patterns (hex) of the master preset sections at 48 kHz: B0 B1 B2 A1 A2. linux/amd64, GOAMD64=v1.\n"+got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), got) {
		t.Errorf("coefficients differ from %s:\n%s", path, got)
	}
	if again := strings.Join(presetCoefs(), "\n") + "\n"; again != got {
		t.Error("two derivations differ")
	}
}

func TestFilt_DeterministicFMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestFilt_Deterministic$`)
}

// TestStream_BlocksMatchCascade: a signal filtered in blocks of any size through a Stream is, bit for bit, the signal filtered whole by
// Cascade.Process, and the meter is charged the same.
func TestStream_BlocksMatchCascade(t *testing.T) {
	t.Parallel()
	c := Cascade{HighPass(rate, 30, Butterworth), LowShelf(rate, 250, -1, 0.7), HighShelf(rate, 2500, 1, 0.7)}
	x := dsptest.Pink(3, 5000)
	var mw dsp.Meter
	want, err := c.Process(x, &mw)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []int{1, 7, 997, 4096, 5000, 9999} {
		var m dsp.Meter
		s := NewStream(c)
		got := append([]float64(nil), x...)
		for lo := 0; lo < len(got); lo += block {
			if err := s.Process(got[lo:min(lo+block, len(got))], &m); err != nil {
				t.Fatal(err)
			}
		}
		for i := range want {
			if math.Float64bits(got[i]) != math.Float64bits(want[i]) {
				t.Fatalf("block %d: sample %d is %v, Cascade gives %v", block, i, got[i], want[i])
			}
		}
		if m.Used() != mw.Used() {
			t.Errorf("block %d: %d units, Cascade spends %d", block, m.Used(), mw.Used())
		}
	}
	if err := NewStream(c).Process(make([]float64, 10), &dsp.Meter{Max: 1}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("the meter did not stop the stream: %v", err)
	}
}
