package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/filt"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

const testRate = 16000

func mid(b pcm.Buffer) []float64 {
	m := make([]float64, b.Frames())
	for i := range m {
		m[i] = (b.Ch[0][i] + b.Ch[1][i]) / 2
	}
	return m
}

func sideOf(b pcm.Buffer) []float64 {
	m := make([]float64, b.Frames())
	for i := range m {
		m[i] = (b.Ch[0][i] - b.Ch[1][i]) / 2
	}
	return m
}

func equalBuf(a, b pcm.Buffer) bool {
	return slices.Equal(a.Ch[0], b.Ch[0]) && slices.Equal(a.Ch[1], b.Ch[1])
}

// measure reports the guard's own statistics for a buffer.
func measure(b pcm.Buffer) corrStats {
	seg, _ := segmentSums5(b.Ch[0], b.Ch[1], segLen(b.Rate), nil)
	var all sums5
	for _, s := range seg {
		all.add(s)
	}
	bl := blocks(seg)
	return statsOf(all, bl, gate(bl))
}

func TestWiden_MonoFoldIdentity(t *testing.T) {
	t.Parallel()
	for _, rho := range []float64{0.95, 0.8} {
		b := dsptest.PinkStereo(testRate, 3, 3, rho)
		for _, p := range []WidenParams{{1.3, 0.1, 120}, {1.6, 0.2, 0}, {2, 0.5, 250}} {
			out, info, err := Widen(b, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			in, o := mid(b), mid(out)
			var worst float64
			for i := range in {
				worst = math.Max(worst, math.Abs(o[i]-in[i]))
			}
			if worst >= 1e-12 {
				t.Errorf("rho %.2f %+v: fold-down moved by %g (strength %.2f)", rho, p, worst, info.StrengthEffective)
			}
		}
	}
}

// TestWiden_MonoFoldAfterFinish: after the safety stage the fold-down differs from the parent's by the one linked Finish gain.
func TestWiden_MonoFoldAfterFinish(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(testRate, 4, 4, 0.9)
	out, err := runWiden(context.Background(), RunIn{Rate: testRate, Inputs: []pcm.Buffer{b}, Params: json.RawMessage(`{"width":1.5,"decorrelate":0.1,"bass_mono_hz":120}`), TargetLUFS: -14})
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Safety struct {
			GainDB float64 `json:"gain_db"`
		} `json:"safety"`
		Hifi struct {
			Widen struct {
				Delta float64 `json:"monofold_delta_lu"`
			} `json:"widen"`
		} `json:"hifi"`
	}
	if err := json.Unmarshal(out.Report, &rep); err != nil {
		t.Fatal(err)
	}
	if d := math.Abs(rep.Hifi.Widen.Delta - rep.Safety.GainDB); d > 0.05 {
		t.Errorf("fold-down delta %.3f LU vs Finish gain %.3f dB", rep.Hifi.Widen.Delta, rep.Safety.GainDB)
	}
}

func TestWiden_NeutralIsIdentity(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(testRate, 5, 1, 0.7)
	out, info, err := Widen(b, WidenParams{Width: 1, Decorrelate: 0, BassMonoHz: 0}, nil)
	if err != nil || !equalBuf(out, b) || !info.Neutral {
		t.Fatalf("neutral parameters changed the signal: %v %+v", err, info)
	}
	out.Ch[0][0] = 99 // a copy, never the parent's slice
	if b.Ch[0][0] == 99 {
		t.Error("the neutral result aliases the input")
	}
}

func TestWiden_BassMono(t *testing.T) {
	t.Parallel()
	one := 1.0
	// The side of a hard-left sine is the sine itself; an LR4 high-pass at 250 Hz takes 80 Hz down by 39.7 dB (the C220.4 figure
	// of 40 dB is not reachable by a 4th-order filter at the 250 Hz maximum) and 40 Hz at the 120 Hz default by 38.3 dB.
	for _, tc := range []struct {
		freq   float64
		bassHz int
		minDB  float64
	}{{80, 250, 39}, {40, 120, 38}, {30, 120, 45}} {
		b := dsptest.HardPanned(testRate, tc.freq, 0.5, 2, true)
		out, _, err := widen(b, WidenParams{Width: 1, BassMonoHz: tc.bassHz}, &one, nil)
		if err != nil {
			t.Fatal(err)
		}
		tail := func(x []float64) []float64 { return x[testRate/2:] } // skip the filter's settling
		got := dsptest.DBFS(dsptest.RMS(tail(sideOf(out)))) - dsptest.DBFS(dsptest.RMS(tail(sideOf(b))))
		if got > -tc.minDB {
			t.Errorf("%g Hz at %d Hz: side %.1f dB re input, want <= -%.0f", tc.freq, tc.bassHz, got, tc.minDB)
		}
		if rho := dsptest.Pearson(tail(out.Ch[0]), tail(out.Ch[1])); rho < 0.98 {
			t.Errorf("%g Hz at %d Hz: bass correlation %.4f, want >= 0.98", tc.freq, tc.bassHz, rho)
		}
	}
	// a 2 kHz hard-left sine keeps its side
	b := dsptest.HardPanned(testRate, 2000, 0.5, 2, true)
	out, _, _ := widen(b, WidenParams{Width: 1.5, BassMonoHz: 120}, &one, nil)
	ratio := dsptest.RMS(sideOf(out)[testRate/2:]) / dsptest.RMS(sideOf(b)[testRate/2:])
	if want := 1.5; math.Abs(ratio-want) > 0.01 {
		t.Errorf("2 kHz side gain %.3f, want %.1f", ratio, want)
	}
}

func TestWiden_AllpassFlat(t *testing.T) {
	t.Parallel()
	const n = 1 << 15
	x := make([]float64, n)
	x[0] = 1
	y, err := allpassChain(x, testRate, nil)
	if err != nil {
		t.Fatal(err)
	}
	re, im := dsp.RFFT(y, n)
	for k := 1; k <= 64; k++ {
		bin := k * 127 // 64 bins spread over the band
		if mag := math.Hypot(re[bin], im[bin]); math.Abs(mag-1) > 1e-9 {
			t.Errorf("|AP| at bin %d = %.12f", bin, mag)
		}
	}
	for i, want := range [4]int{149, 226, 350, 533} {
		if got := AllpassDelaySamples(48000, allpassDelays[i]); got != want {
			t.Errorf("48 kHz delay %d = %d samples, want %d", i, got, want)
		}
	}
}

func TestWiden_CorrelationBounds(t *testing.T) {
	t.Parallel()
	type row struct {
		name    string
		b       pcm.Buffer
		p       WidenParams
		reduced bool
		pass    bool
		warn    string
	}
	rows := []row{
		// rho 0.9 at full width 2 stays at 0.65: the bound does not bite, so the strength must NOT be reduced needlessly
		{name: "rho 0.9 width 2", b: dsptest.PinkStereo(testRate, 11, 8, 0.9), p: WidenParams{2, 0, 0}},
		{name: "rho 0.6 width 2", b: dsptest.PinkStereo(testRate, 12, 8, 0.6), p: WidenParams{2, 0, 0}, reduced: true},
		{name: "rho 0.6 default", b: dsptest.PinkStereo(testRate, 12, 8, 0.6), p: WidenParams{1.3, 0.1, 120}},
		{name: "rho 0.2", b: dsptest.PinkStereo(testRate, 13, 8, 0.2), p: WidenParams{1.5, 0.1, 120}, pass: true, warn: WarnWidenAlreadyWide},
		{name: "rho -0.5", b: dsptest.PinkStereo(testRate, 14, 8, -0.5), p: WidenParams{1.5, 0.1, 120}, pass: true, warn: WarnWidenOutOfPhase},
		// the real-world shapes of FixtureT1: MONO, WIDE and OOP rows
		{name: "MONO", b: dsptest.RealWorldShape("MONO", testRate, 8, 21), p: WidenParams{1.3, 0.1, 120}},
		{name: "MONO width 2", b: dsptest.RealWorldShape("MONO", testRate, 8, 21), p: WidenParams{2, 0.5, 0}},
		{name: "WIDE", b: dsptest.RealWorldShape("WIDE", testRate, 8, 22), p: WidenParams{1.3, 0.1, 120}, pass: true, warn: WarnWidenAlreadyWide},
		{name: "OOP", b: dsptest.RealWorldShape("OOP", testRate, 8, 23), p: WidenParams{1.3, 0.1, 120}, pass: true, warn: WarnWidenOutOfPhase},
	}
	for _, r := range rows {
		out, info, err := Widen(r.b, r.p, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := measure(r.b)
		got := measure(out)
		rhoOut := dsptest.Pearson(out.Ch[0], out.Ch[1])
		switch {
		case r.pass:
			if !equalBuf(out, r.b) || info.StrengthEffective != 0 || !slices.Contains(info.Warnings, r.warn) {
				t.Errorf("%s: want a pass-through with %s, got strength %.3f warnings %v", r.name, r.warn, info.StrengthEffective, info.Warnings)
			}
			continue
		case r.reduced:
			if info.StrengthEffective >= 1 || info.StrengthEffective <= 0 || !slices.Contains(info.Warnings, WarnWidenStrengthLow) {
				t.Errorf("%s: strength %.3f warnings %v, want 0 < s < 1 with %s", r.name, info.StrengthEffective, info.Warnings, WarnWidenStrengthLow)
			}
		}
		if rhoOut < math.Min(0.30, in.Rho)-0.005 {
			t.Errorf("%s: rho_out %.3f below the bound (rho_in %.3f)", r.name, rhoOut, in.Rho)
		}
		if got.P10 < math.Min(0.05, in.P10)-0.005 {
			t.Errorf("%s: rho_p10_out %.3f below the bound (p10_in %.3f)", r.name, got.P10, in.P10)
		}
		if math.Abs(info.RhoOut-got.Rho) > 1e-6 || math.Abs(info.RhoP10Out-got.P10) > 1e-6 {
			t.Errorf("%s: reported (%.5f, %.5f), measured (%.5f, %.5f)", r.name, info.RhoOut, info.RhoP10Out, got.Rho, got.P10)
		}
	}
}

func TestWiden_MonoSource(t *testing.T) {
	t.Parallel()
	x := dsptest.Pink(31, dsptest.Frames(testRate, 4))
	b := dsptest.Dual(testRate, x)
	out, info, err := Widen(b, WidenParams{1.5, 0, 120}, nil)
	if err != nil || !equalBuf(out, b) || !slices.Contains(info.Warnings, WarnWidenMonoSource) {
		t.Errorf("mono source, decorrelate 0: %v %v", err, info.Warnings)
	}
	out, info, err = Widen(b, WidenParams{1.5, 0.3, 120}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rho := dsptest.Pearson(out.Ch[0], out.Ch[1]); rho < 0.30 || rho > 0.999 {
		t.Errorf("decorrelated mono rho %.3f, want 0.30..1 exclusive", rho)
	}
	for i, v := range mid(out) {
		if math.Abs(v-x[i]) > 1e-12 {
			t.Fatalf("fold-down moved at %d", i)
		}
	}
	if slices.Contains(info.Warnings, WarnWidenMonoSource) {
		t.Error("a decorrelated mono source must not warn mono_source")
	}
}

func TestWiden_Bisection_Deterministic(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(testRate, 41, 6, 0.6)
	p := WidenParams{2, 0.3, 0}
	o1, i1, _ := Widen(b, p, nil)
	o2, i2, _ := Widen(b, p, nil)
	if !equalBuf(o1, o2) || i1.StrengthEffective != i2.StrengthEffective || i1.StrengthEffective >= 1 {
		t.Errorf("bisection differs between runs or did not engage: %v %v", i1.StrengthEffective, i2.StrengthEffective)
	}
}

func TestWiden_Length(t *testing.T) {
	t.Parallel()
	for _, secs := range []float64{0.05, 0.37, 2.3} {
		b := dsptest.PinkStereo(testRate, 51, secs, 0.8)
		out, _, err := Widen(b, WidenParams{1.4, 0.1, 120}, nil)
		if err != nil || out.Frames() != b.Frames() || len(out.Ch[1]) != b.Frames() {
			t.Errorf("%.2f s: %d frames, want %d (%v)", secs, out.Frames(), b.Frames(), err)
		}
	}
}

func TestWiden_Metered(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(testRate, 61, 4, 0.8)
	if _, _, err := Widen(b, WidenParams{1.4, 0.1, 120}, &dsp.Meter{Max: 1000}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	m := &dsp.Meter{}
	if _, _, err := Widen(b, WidenParams{1.4, 0.1, 120}, m); err != nil {
		t.Fatal(err)
	}
	if want := WidenUnits(int64(b.Frames())) - SafetyUnits(int64(b.Frames())); m.Used() > want {
		t.Errorf("used %d units, model %d", m.Used(), want)
	}
}

func TestWiden_Params(t *testing.T) {
	t.Parallel()
	p, err := ParseWiden(json.RawMessage(`{"source_id":"x","width":null}`))
	if err != nil || p != (WidenParams{1.3, 0.1, 120}) {
		t.Errorf("defaults: %+v %v", p, err)
	}
	for _, bad := range []string{`{"width":0.9}`, `{"width":2.1}`, `{"decorrelate":0.6}`, `{"bass_mono_hz":30}`, `{"bass_mono_hz":300}`, `{"width":"x"}`} {
		var pe *ParamError
		if _, err := ParseWiden(json.RawMessage(bad)); !errors.As(err, &pe) {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := ParseWiden(json.RawMessage(`{"bass_mono_hz":0}`)); err != nil {
		t.Errorf("bass_mono_hz 0 refused: %v", err)
	}
	if _, err := runWiden(context.Background(), RunIn{Rate: testRate}); err == nil {
		t.Error("a job without input accepted")
	}
	_ = filt.Butterworth
}
