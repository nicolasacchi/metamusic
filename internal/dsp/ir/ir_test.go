package ir_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
)

var update = flag.Bool("update", false, "rewrite testdata/ir-golden.txt (after a deliberate dsp_version bump only)")

const rate16 = 16000

func mustPreset(t testing.TB, name string) ir.Params {
	t.Helper()
	p, ok := ir.Preset(name)
	if !ok {
		t.Fatalf("no preset %q", name)
	}
	return p
}

func energy(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return s
}

// t20 is the Schroeder T20 of one channel, extrapolated to 60 dB, independent of the package's own measure.
func t20(x []float64, rate int) float64 {
	s := make([]float64, len(x))
	var acc float64
	for i := len(x) - 1; i >= 0; i-- {
		acc += x[i] * x[i]
		s[i] = acc
	}
	at := func(db float64) float64 {
		thr := s[0] * math.Pow(10, db/10)
		for i, v := range s {
			if v < thr {
				return float64(i)
			}
		}
		return float64(len(s))
	}
	return 3 * (at(-25) - at(-5)) / float64(rate)
}

func TestIR_Presets(t *testing.T) {
	t.Parallel()
	want := map[string]ir.Params{
		"cathedral": {RT60: 9, PredelayMS: 40, Damping: 0.45, SizeM: 60, Width: 0.9},
		"cistern":   {RT60: 18, PredelayMS: 15, Damping: 0.8, SizeM: 30, Width: 0.6},
		"hangar":    {RT60: 5, PredelayMS: 25, Damping: 0.15, SizeM: 120, Width: 0.7},
		"canyon":    {RT60: 12, PredelayMS: 120, Damping: 0.3, SizeM: 250, Width: 1.0},
	}
	if got := ir.PresetNames(); strings.Join(got, ",") != "cathedral,cistern,hangar,canyon" {
		t.Errorf("PresetNames = %v", got)
	}
	for name, w := range want {
		if got, ok := ir.Preset(name); !ok || got != w {
			t.Errorf("Preset(%s) = %+v, %v; want %+v", name, got, ok, w)
		}
	}
	if _, ok := ir.Preset("custom"); ok {
		t.Error("Preset(custom) exists: custom spaces are cut")
	}
}

func TestIR_RT60(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		want float64
	}{{"cathedral", 9}, {"hangar", 5}} {
		l, r, info := ir.Build(mustPreset(t, c.name), rate16, 7, nil)
		for ch, x := range [][]float64{l, r} {
			if got := t20(x, rate16); math.Abs(got-c.want)/c.want > 0.10 {
				t.Errorf("%s ch%d: T20 extrapolated to 60 dB = %.2f s, want %.1f +-10%%", c.name, ch, got, c.want)
			}
		}
		if math.Abs(info.RT60Measured-c.want)/c.want > 0.10 {
			t.Errorf("%s: Info.RT60Measured = %.2f, want %.1f +-10%%", c.name, info.RT60Measured, c.want)
		}
	}
}

func hashIR(l, r []float64) string {
	h := sha256.New()
	var b [8]byte
	for _, x := range [][]float64{l, r} {
		for _, v := range x {
			binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
			h.Write(b[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestIR_Deterministic(t *testing.T) {
	t.Parallel()
	p := mustPreset(t, "hangar")
	l1, r1, i1 := ir.Build(p, rate16, 1, nil)
	l2, r2, i2 := ir.Build(p, rate16, 1, nil)
	if hashIR(l1, r1) != hashIR(l2, r2) || i1 != i2 {
		t.Error("same seed gave different bytes")
	}
	l3, r3, i3 := ir.Build(p, rate16, 2, nil)
	if hashIR(l1, r1) == hashIR(l3, r3) {
		t.Error("another seed gave identical bytes")
	}
	if d := math.Abs(i1.RT60Measured-i3.RT60Measured) / i1.RT60Measured; d > 0.05 {
		t.Errorf("RT60 differs by %.1f%% between seeds (%.2f vs %.2f), want <= 5%%", 100*d, i1.RT60Measured, i3.RT60Measured)
	}
}

const goldenPath = "testdata/ir-golden.txt"

// TestIR_Golden pins the cathedral at 16 kHz bit for bit (linux/amd64, GOAMD64=v1), and checks the pan law that the ensemble
// places its voices with is constant-power for every pan.
func TestIR_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	l, r, _ := ir.Build(mustPreset(t, "cathedral"), rate16, 42, nil)
	got := hashIR(l, r)
	if *update {
		if err := os.WriteFile(goldenPath, []byte("# sha256 of the little-endian bit patterns of L then R of the cathedral at 16 kHz, seed 42. linux/amd64, GOAMD64=v1.\ncathedral16k "+got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "cathedral16k" {
			want = f[1]
		}
	}
	if got != want {
		t.Errorf("cathedral16k hash %s, golden %s", got, want)
	}
}

func TestIR_GoldenFMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestIR_Golden$`)
}

func TestIR_PanConstantPower(t *testing.T) {
	t.Parallel()
	for p := -1.5; p <= 1.5; p += 0.01 {
		l, r := ir.Pan(p)
		if d := math.Abs(l*l + r*r - 1); d > 1e-9 {
			t.Fatalf("pan %.2f: L^2+R^2 = %v", p, l*l+r*r)
		}
	}
	if l, r := ir.Pan(-1); math.Abs(l-1) > 1e-12 || math.Abs(r) > 1e-12 {
		t.Errorf("hard left = (%v, %v)", l, r)
	}
	if l, r := ir.Pan(0); math.Abs(l-r) > 1e-12 {
		t.Errorf("centre = (%v, %v)", l, r)
	}
}

// bandpass keeps [lo, hi) Hz of x by a rectangular FFT mask.
func bandpass(x []float64, rate int, lo, hi float64) []float64 {
	n := dsp.NextPow2(len(x))
	re, im := dsp.RFFT(x, n)
	for k := range re {
		if f := float64(k) * float64(rate) / float64(n); f < lo || f >= hi {
			re[k], im[k] = 0, 0
		}
	}
	return dsp.IRFFT(re, im, n)[:len(x)]
}

func TestIR_Damping(t *testing.T) {
	t.Parallel()
	p := ir.Params{RT60: 3, PredelayMS: 10, Damping: 0.8, SizeM: 30, Width: 1}
	l, _, _ := ir.Build(p, rate16, 3, nil)
	low := t20(bandpass(l, rate16, 177, 354), rate16)
	high := t20(bandpass(l, rate16, 2828, 5657), rate16)
	if high > 0.55*low {
		t.Errorf("T20 at 4 kHz = %.2f s, at 250 Hz = %.2f s: ratio %.2f, want <= 0.55", high, low, high/low)
	}
	// With no damping the bands decay alike.
	p.Damping = 0
	l, _, _ = ir.Build(p, rate16, 3, nil)
	low, high = t20(bandpass(l, rate16, 177, 354), rate16), t20(bandpass(l, rate16, 2828, 5657), rate16)
	if r := high / low; r < 0.8 || r > 1.25 {
		t.Errorf("damping 0: T20 ratio %.2f, want about 1", r)
	}
}

func TestIR_UnitEnergy(t *testing.T) {
	t.Parallel()
	for _, name := range ir.PresetNames() {
		l, r, _ := ir.Build(mustPreset(t, name), 8000, 5, nil)
		for ch, x := range [][]float64{l, r} {
			if d := math.Abs(energy(x) - 1); d > 1e-9 {
				t.Errorf("%s ch%d: sum of squares %v", name, ch, energy(x))
			}
		}
	}
}

func TestIR_EffectiveLen(t *testing.T) {
	t.Parallel()
	p := mustPreset(t, "cathedral")
	const rate = 48000
	l, r, info := ir.Build(p, rate, 9, nil)
	if info.EffectiveLen != len(l) || len(l) != len(r) {
		t.Fatalf("EffectiveLen %d, len(L) %d, len(R) %d", info.EffectiveLen, len(l), len(r))
	}
	if limit := int(9 * 1.3 * rate); info.EffectiveLen > limit {
		t.Errorf("cathedral at 48 kHz: effective length %d > %d", info.EffectiveLen, limit)
	}
	if info.EffectiveLen < 5*rate {
		t.Errorf("effective length %d is shorter than 5 s", info.EffectiveLen)
	}
	// The energy beyond the effective length of the returned response is nil by construction; inside, the backward-integrated
	// energy at 90 % of it is still well above -70 dB.
	var tail float64
	for i := info.EffectiveLen * 9 / 10; i < len(l); i++ {
		tail += l[i]*l[i] + r[i]*r[i]
	}
	if tail < 1e-7 {
		t.Errorf("energy in the last tenth is %v, below -70 dB: the response is longer than needed", tail)
	}
}

func TestIR_StereoDecorrelated(t *testing.T) {
	t.Parallel()
	p := ir.Params{RT60: 2, PredelayMS: 10, Damping: 0.3, SizeM: 40, Width: 1}
	l, r, _ := ir.Build(p, rate16, 11, nil)
	start := len(l) / 4 // the tail, past the early reflections
	var sl, sr, slr float64
	for i := start; i < len(l); i++ {
		sl += l[i] * l[i]
		sr += r[i] * r[i]
		slr += l[i] * r[i]
	}
	if rho := slr / math.Sqrt(sl*sr); math.Abs(rho) >= 0.1 {
		t.Errorf("tail correlation %.3f, want |rho| < 0.1 at width 1", rho)
	}
	// A narrower room correlates the tails.
	p.Width = 0.2
	l, r, _ = ir.Build(p, rate16, 11, nil)
	sl, sr, slr = 0, 0, 0
	for i := len(l) / 4; i < len(l); i++ {
		sl += l[i] * l[i]
		sr += r[i] * r[i]
		slr += l[i] * r[i]
	}
	if rho := slr / math.Sqrt(sl*sr); rho < 0.3 {
		t.Errorf("width 0.2: correlation %.3f, want it clearly positive", rho)
	}
}

func TestIR_EarlyReflections(t *testing.T) {
	t.Parallel()
	p := ir.Params{RT60: 1, PredelayMS: 20, Damping: 0.3, SizeM: 20, Width: 1}
	l, r, _ := ir.Build(p, rate16, 4, nil)
	// Nothing before the pre-delay (less the sinc support of 8 samples).
	pre := int(0.020*rate16) - 9
	for i := 0; i < pre; i++ {
		if l[i] != 0 || r[i] != 0 {
			t.Fatalf("sample %d before the pre-delay is non-zero", i)
		}
	}
	// The receivers differ, so the two early parts differ.
	if hashIR(l[pre:pre+400], nil) == hashIR(r[pre:pre+400], nil) {
		t.Error("left and right early parts are identical")
	}
}

func TestIR_Metered(t *testing.T) {
	t.Parallel()
	p := mustPreset(t, "hangar")
	m := &dsp.Meter{}
	if _, _, _, err := ir.BuildErr(p, rate16, 1, m); err != nil {
		t.Fatal(err)
	}
	if want := ir.Units(p, rate16); m.Used() != want {
		t.Errorf("meter used %d units, Units says %d", m.Used(), want)
	}
	if _, _, info := ir.Build(p, rate16, 1, &dsp.Meter{Max: m.Used() / 2}); info.EffectiveLen != 0 {
		t.Error("Build under a small budget returned a response")
	}
	if _, _, _, err := ir.BuildErr(p, rate16, 1, &dsp.Meter{Max: m.Used() / 2}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := ir.BuildErr(p, rate16, 1, &dsp.Meter{Ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestIR_ParamErrors(t *testing.T) {
	t.Parallel()
	ok := mustPreset(t, "hangar")
	for name, mut := range map[string]func(*ir.Params){
		"rt60": func(p *ir.Params) { p.RT60 = 0 }, "rt60 big": func(p *ir.Params) { p.RT60 = 100 },
		"predelay": func(p *ir.Params) { p.PredelayMS = -1 }, "damping": func(p *ir.Params) { p.Damping = 2 },
		"size": func(p *ir.Params) { p.SizeM = 0 }, "width": func(p *ir.Params) { p.Width = -0.1 },
	} {
		p := ok
		mut(&p)
		if _, _, _, err := ir.BuildErr(p, rate16, 1, nil); !errors.Is(err, ir.ErrParam) {
			t.Errorf("%s: err = %v, want ErrParam", name, err)
		}
	}
	if _, _, _, err := ir.BuildErr(ok, 100, 1, nil); !errors.Is(err, ir.ErrParam) {
		t.Errorf("rate 100: err = %v", err)
	}
	if l, r, info := ir.Build(ir.Params{}, rate16, 1, nil); l != nil || r != nil || info != (ir.Info{}) {
		t.Error("Build of invalid params returned a response")
	}
}

func TestIR_CanyonAndCisternBuild(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"cistern", "canyon"} {
		l, _, info := ir.Build(mustPreset(t, name), 8000, 6, nil)
		if len(l) == 0 || info.RT60Measured <= 0 {
			t.Errorf("%s: empty response %+v", name, info)
		}
	}
}
