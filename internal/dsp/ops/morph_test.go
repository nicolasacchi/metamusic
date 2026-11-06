package ops

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// morphClicks renders the standard click morph: A at 92 BPM with its downbeat at 0.4 s, B at 128 BPM with its downbeat at 0.2 s,
// 8 bars of 4 beats, the transition at bar 4 of A and bar 0 of B.
func morphClicks(t *testing.T, semis string) (pcm.Buffer, MorphInfo, MorphParams, [2]inputAlign) {
	t.Helper()
	a := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 92, 0.4, 36, 0.8))
	b := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 128, 0.2, 40, 0.8))
	params := `{"bars":8,"beats_per_bar":4,"a_at":{"bar":4},"b_at":{"bar":0}` + semis + `}`
	p, al, err := ParseMorph([]byte(params), d5Align(92, 128, 0.4, 0.2))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	out, info, err := Morph(context.Background(), a, b, p, al, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out, info, p, al
}

func TestMorph_TempoRamp(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	out, info, _, _ := morphClicks(t, "")
	d5Near(t, "transition length", float64(info.TransFrame)/d5Rate, 17.77, 0.05)
	ramp := align.Ramp{T0: info.T0, T1: info.T1, Beats: 32}
	d5Near(t, "ramp total", info.TotalS, ramp.Total(), 1e-9)
	// one click per beat through the transition, at the ramp's beat times, within 2 % of the local beat period
	x := out.Ch[0]
	var prev float64
	for k := 1; k < 32; k++ {
		want := info.TA + ramp.Time(float64(k))
		at, v := d5PeakNear(x, d5Rate, want, 0.12)
		if v < 0.05 {
			t.Fatalf("beat %d: no click near %.3f s", k, want)
		}
		period := 60 / ramp.TempoAt(float64(k))
		if math.Abs(at-want) > 0.02*period+0.004 {
			t.Errorf("beat %d at %.3f s, want %.3f s (period %.3f s)", k, at, want, period)
		}
		if k > 1 {
			got := at - prev
			if want := 60 / ramp.TempoAt(float64(k)-0.5); math.Abs(got-want) > 0.02*want+0.008 {
				t.Errorf("beat %d spacing %.3f s, want %.3f s +- 2 %%", k, got, want)
			}
		}
		prev = at
	}
}

func TestMorph_Length(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	out, info, _, _ := morphClicks(t, "")
	tAs := int(math.Floor(info.TA*d5Rate + 0.5))
	tBs := int(math.Floor(info.TB*d5Rate + 0.5))
	trans := int(math.Floor(info.TotalS*d5Rate + 0.5))
	tail := 40*d5Rate - (tBs + int(math.Floor(60*32/info.T1*d5Rate+0.5)))
	if out.Frames() != tAs+trans+tail || info.OutFrames != out.Frames() {
		t.Errorf("frames %d, want tA %d + ramp %d + tail %d = %d", out.Frames(), tAs, trans, tail, tAs+trans+tail)
	}
	if len(out.Ch[0]) != len(out.Ch[1]) {
		t.Error("channels differ in length")
	}
}

func TestMorph_DownbeatsLand(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	out, info, _, al := morphClicks(t, "")
	// B's clicks after the transition: the bar grid of the output continues B's own, shifted by the ramp's total
	end := info.TA + info.TotalS
	barB := 4 * 60 / info.T1
	for j := 0; j < 6; j++ {
		want := end + float64(j)*barB // B's downbeat tB + 32 beats + j bars
		at, v := d5PeakNear(out.Ch[0], d5Rate, want, 0.1)
		if v < 0.5 {
			t.Fatalf("bar %d: no click near %.3f s", j, want)
		}
		if math.Abs(at-want) > 0.015 {
			t.Errorf("B downbeat %d at %.3f s, want %.3f s +- 15 ms", j, at, want)
		}
	}
	_ = al
}

func TestMorph_EqualPower(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	const secs = 40
	a := dsptest.Dual(d5Rate, dsptest.Noise(1, secs*d5Rate, 0.1))
	b := dsptest.Dual(d5Rate, dsptest.Noise(2, secs*d5Rate, 0.1))
	// noise has no beat: the spec's numbers stand (beat_phase_weak) and the crossfade is the whole story
	p, al, err := ParseMorph([]byte(`{"bars":4,"a_at":{"bar":2},"b_at":{"bar":0}}`), d5Align(120, 120, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	out, info, err := Morph(context.Background(), a, b, p, al, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Warnings) == 0 {
		t.Error("noise should report beat_phase_weak")
	}
	tA := int(info.TA * d5Rate)
	win := d5Rate / 2
	ref := dsptest.RMS(a.Ch[0])
	for u := 0.1; u < 0.95; u += 0.1 {
		i := tA + int(u*float64(info.TransFrame)) - win/2
		got := dsptest.RMS(out.Ch[0][i : i+win])
		if db := 20 * math.Log10(got/ref); math.Abs(db) > 0.5 {
			t.Errorf("RMS at %.0f %% of the transition is %.2f dB from the source's", 100*u, db)
		}
	}
}

func TestMorph_KeyShift(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	// A is silent, so the transition is B alone under its gain: B (523.25 Hz) shifted by -3 semitones glides from 440 Hz up to
	// its own pitch over the ramp.
	secs := 40.0
	a := dsptest.Dual(d5Rate, dsptest.Silence(dsptest.Frames(d5Rate, secs)))
	b := dsptest.Dual(d5Rate, dsptest.Sine(d5Rate, 523.25, 0.5, secs))
	p, al, err := ParseMorph([]byte(`{"bars":8,"a_at":{"bar":2},"b_at":{"bar":0},"semitones_b":-3}`), d5Align(120, 120, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	out, info, err := Morph(context.Background(), a, b, p, al, nil)
	if err != nil {
		t.Fatal(err)
	}
	tA := int(info.TA * d5Rate)
	for _, u := range []float64{0.15, 0.5, 0.85} {
		w := d5Rate * 6 / 10
		i := tA + int(u*float64(info.TransFrame)) - w/2
		got := dsptest.PeakFreq(out.Ch[0][i:i+w], d5Rate)
		want := 523.25 * math.Pow(2, -3*(1-u)/12)
		if c := d5Cents(got, want); math.Abs(c) > 3 {
			t.Errorf("u=%.2f: B at %.2f Hz, want %.2f Hz (%.1f cents off)", u, got, want, c)
		}
	}
	// the tail is B at its own pitch
	tail := out.Ch[0][out.Frames()-2*d5Rate : out.Frames()-d5Rate]
	if c := d5Cents(dsptest.PeakFreq(tail, d5Rate), 523.25); math.Abs(c) > 3 {
		t.Errorf("tail pitch %.1f cents off", c)
	}
}

func TestMorph_Errors(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	a := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 0, 12, 0.5))
	b := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 0, 12, 0.5))
	short := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 0, 5, 0.5))
	cases := []struct {
		name         string
		a, b         pcm.Buffer
		params, algn string
		rule         string
	}{
		{"a too short", short, b, `{"bars":4,"a_at":{"bar":1}}`, string(d5Align(120, 120, 0, 0)), RuleATooShort},
		{"b too short", a, short, `{"bars":4,"a_at":{"bar":1}}`, string(d5Align(120, 120, 0, 0)), RuleBTooShort},
		{"tempo ratio", a, b, `{"bars":2,"a_at":{"bar":0}}`, string(d5Align(40, 240, 0, 0)), RuleTempoRatio},
		{"bars range", a, b, `{"bars":99,"a_at":{"bar":0}}`, string(d5Align(120, 120, 0, 0)), RuleRange},
		{"no a_at", a, b, `{"bars":2}`, string(d5Align(120, 120, 0, 0)), RuleInput},
		{"no downbeat", a, b, `{"bars":2,"a_at":{"bar":0}}`, `{"inputs":[{"bpm":120},{"bpm":120,"downbeat_s":0}]}`, RuleDownbeat},
		{"one input", a, b, `{"bars":2,"a_at":{"bar":0}}`, `{"inputs":[{"bpm":120,"downbeat_s":0}]}`, RuleInput},
		{"bpm range", a, b, `{"bars":2,"a_at":{"bar":0}}`, string(d5Align(300, 120, 0, 0)), RuleRange},
		{"semitones", a, b, `{"bars":2,"a_at":{"bar":0},"semitones_b":13}`, string(d5Align(120, 120, 0, 0)), RuleRange},
		{"bad json", a, b, `{"bars":"x"}`, string(d5Align(120, 120, 0, 0)), "params"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, al, err := ParseMorph([]byte(c.params), []byte(c.algn))
			if err == nil {
				p.N = d5N
				_, _, err = Morph(context.Background(), c.a, c.b, p, al, nil)
			}
			var pe *ParamError
			if !errors.As(err, &pe) {
				t.Fatalf("error %v, want a *ParamError", err)
			}
			if pe.Rule != c.rule && !(c.rule == "params" && pe.Field == "params") {
				t.Errorf("rule %q (field %q), want %q", pe.Rule, pe.Field, c.rule)
			}
		})
	}
}

func TestMorph_MeterAndContext(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	a := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 0, 30, 0.5))
	b := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 0, 30, 0.5))
	p, al, err := ParseMorph([]byte(`{"bars":2,"a_at":{"bar":1},"semitones_b":2}`), d5Align(120, 120, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	m := &dsp.Meter{}
	if _, _, err := Morph(context.Background(), a, b, p, al, m); err != nil {
		t.Fatal(err)
	}
	used := m.Used()
	model := MorphUnits(int64(a.Frames()+b.Frames()), 0, d5Rate, p, 120, 120)
	if r := float64(used) / float64(model); r < 0.8 || r > 1.05 {
		t.Errorf("meter used %d units, model says %d (ratio %.2f)", used, model, r)
	}
	if _, _, err := Morph(context.Background(), a, b, p, al, &dsp.Meter{Max: used / 2}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("half the budget: %v, want ErrBudget", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Morph(ctx, a, b, p, al, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestMorph_AutoAndPlanDownbeat(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	a := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 100, 0.3, 40, 0.8))
	b := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 100, 0.3, 40, 0.8))
	for name, algn := range map[string]string{
		"auto": `{"inputs":[{"bpm":100,"downbeat_auto":true},{"bpm":100,"downbeat_auto":true}]}`,
		"plan": `{"inputs":[{"bpm":100,"plan_bar_s":2.4},{"bpm":100,"plan_bar_s":2.4}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, al, err := ParseMorph([]byte(`{"bars":2,"a_at":{"bar":3},"b_at":{"bar":0}}`), []byte(algn))
			if err != nil {
				t.Fatal(err)
			}
			p.N = d5N
			_, info, err := Morph(context.Background(), a, b, p, al, nil)
			if err != nil {
				t.Fatal(err)
			}
			// the measured bar start lies on a beat of the click grid
			if d := math.Mod(info.TA-0.3, 0.6); math.Min(d, 0.6-d) > 0.02 {
				t.Errorf("A bar start %.3f s is %.0f ms off the beat grid", info.TA, 1000*math.Min(d, 0.6-d))
			}
			if got := info.Phase[0].DownbeatSource; got != name {
				t.Errorf("downbeat_source %q, want %q", got, name)
			}
			if (info.Phase[0].DownbeatConfidence != nil) != (name == "auto") {
				t.Error("downbeat_confidence is set only for the auto heuristic")
			}
		})
	}
}
