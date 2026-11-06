package ops

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

func polyRun(t *testing.T, a, b []float64, params, algn string) (pcm.Buffer, PolytempoInfo, PolytempoParams) {
	t.Helper()
	p, al, err := ParsePolytempo([]byte(params), []byte(algn))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	out, info, err := Polytempo(context.Background(), dsptest.Dual(d5Rate, a), dsptest.Dual(d5Rate, b), p, al, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out, info, p
}

func TestPolytempo_Converge(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	const bpmA, bpmB, dA, dB = 90.0, 120.0, 0.3, 0.1
	clicksA := d5Clicks(d5Rate, bpmA, dA, 50, 0.8)
	clicksB := d5Clicks(d5Rate, bpmB, dB, 60, 0.8)
	silentA := dsptest.Silence(len(clicksA))
	silentB := dsptest.Silence(len(clicksB))
	params := `{"converge_bar":12,"unison_bars":4,"pan":"centre"}`
	algn := string(d5Align(bpmA, bpmB, dA, dB))
	// the two sides are rendered one at a time (the other silent) so that each one's clicks can be read on their own
	outA, info, p := polyRun(t, clicksA, silentB, params, algn)
	outB, infoB, _ := polyRun(t, silentA, clicksB, params, algn)
	// the measured grid moves the plan by milliseconds at most (the silent side keeps the spec's numbers)
	d5Near(t, "entry, A side vs B side", info.Plan.EntryS, infoB.Plan.EntryS, 0.01)
	if info.Plan.M != infoB.Plan.M {
		t.Errorf("m differs: %d vs %d", info.Plan.M, infoB.Plan.M)
	}
	plan := infoB.Plan
	barA := 4 * 60 / info.TA
	d5Near(t, "meeting", info.TcS, dA+12*4*60/bpmA, 0.01)
	if plan.EntryS < 0 || plan.EntryS >= 60*4/plan.LBPM {
		t.Errorf("entry offset %.3f s outside [0, %.3f)", plan.EntryS, 60*4/plan.LBPM)
	}
	d5Near(t, "output end", float64(outA.Frames())/d5Rate, info.TcS+4*barA, 1.0/d5Rate)
	d5Near(t, "channels", float64(len(outA.Ch[1])), float64(len(outA.Ch[0])), 0)
	_ = p
	// the B beat that meets A: its click lands on A's downbeat at Tc within 10 ms
	atB, vB := d5PeakNear(outB.Ch[0], d5Rate, info.TcS, 0.1)
	if vB < 0.05 {
		t.Fatal("no B click near the meeting")
	}
	if math.Abs(atB-info.TcS) > 0.010 {
		t.Errorf("B click at %.4f s, A downbeat at %.4f s", atB, info.TcS)
	}
	if atA, _ := d5PeakNear(outA.Ch[0], d5Rate, info.TcS, 0.1); math.Abs(atA-info.TcS) > 0.010 {
		t.Errorf("A click at %.4f s, expected downbeat %.4f s", atA, info.TcS)
	}
	// unison: every beat after the meeting, the offsets between the two sides' clicks stay within 10 ms
	for k := 1; k <= 15; k++ {
		want := info.TcS + float64(k)*60/info.TA
		a1, va := d5PeakNear(outA.Ch[0], d5Rate, want, 0.08)
		b1, vb := d5PeakNear(outB.Ch[0], d5Rate, want, 0.08)
		if va < 0.2 || vb < 0.05 {
			t.Fatalf("beat %d: no click (A %.2f, B %.2f)", k, va, vb)
		}
		if math.Abs((a1-want)-(b1-want)) > 0.010 {
			t.Errorf("unison beat %d: A at %+.1f ms, B at %+.1f ms", k, 1000*(a1-want), 1000*(b1-want))
		}
	}
	// before B enters, B's side is silent
	if r := dsptest.RMS(outB.Ch[0][:int(plan.EntryS*d5Rate)-d5N]); r > 1e-6 {
		t.Errorf("B side not silent before its entry: rms %g", r)
	}
}

func TestPolytempo_PlanTable(t *testing.T) {
	t.Parallel()
	// o = Tc - 60 (first + m bpb) / L, m the largest integer >= 0 with o >= 0, so 0 <= o < 60 bpb / L
	for _, c := range []struct {
		tc, ta, tb, first float64
		bpb, unison       int
	}{{32.3, 90, 120, 0.1 * 2, 4, 4}, {20, 120, 90, 0, 4, 8}, {10.97, 90, 120, 3.5, 3, 0}, {50, 100, 100, 1, 7, 2}} {
		p, ok := PlanPolytempo(c.tc, c.ta, c.tb, c.first, c.bpb, c.unison)
		if !ok {
			t.Fatalf("%+v: no plan", c)
		}
		if p.EntryS < 0 || p.EntryS >= 60*float64(c.bpb)/p.LBPM {
			t.Errorf("%+v: o = %.4f outside [0, %.4f)", c, p.EntryS, 60*float64(c.bpb)/p.LBPM)
		}
		beats := (c.tc - p.EntryS) * p.LBPM / 60
		if math.Abs(beats-(c.first+float64(p.M*c.bpb))) > 1e-9 {
			t.Errorf("%+v: %.6f beats before the meeting, want first + m bpb = %.6f", c, beats, c.first+float64(p.M*c.bpb))
		}
	}
	if _, ok := PlanPolytempo(10.97, 90, 120, 40, 4, 4); ok {
		t.Error("a first downbeat 40 beats in cannot meet at 10.97 s")
	}
	// equal tempos: L = T
	if p, _ := PlanPolytempo(10, 120, 120, 0, 4, 1); p.LBPM != 120 {
		t.Errorf("L = %g for equal tempos", p.LBPM)
	}
}

func TestPolytempo_TooEarly(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	a := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 90, 0.3, 40, 0.8))
	b := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 120, 30, 60, 0.8))
	p, al, err := ParsePolytempo([]byte(`{"converge_bar":4,"unison_bars":2}`), d5Align(90, 120, 0.3, 30))
	if err != nil {
		t.Fatal(err)
	}
	p.N = d5N
	_, _, err = Polytempo(context.Background(), a, b, p, al, nil)
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Rule != RuleConvergeTooEarly {
		t.Fatalf("error %v, want converge_too_early", err)
	}
}

func TestPolytempo_Errors(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	long := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 100, 0, 60, 0.5))
	short := dsptest.Dual(d5Rate, d5Clicks(d5Rate, 100, 0, 10, 0.5))
	al := string(d5Align(100, 100, 0, 0))
	cases := []struct {
		name         string
		a, b         pcm.Buffer
		params, algn string
		rule         string
	}{
		{"a too short", short, long, `{"converge_bar":6}`, al, RuleATooShort},
		{"b too short", long, short, `{"converge_bar":6}`, al, RuleBTooShort},
		{"converge range", long, long, `{"converge_bar":2}`, al, RuleRange},
		{"unison range", long, long, `{"unison_bars":65}`, al, RuleRange},
		{"pan", long, long, `{"pan":"hard"}`, al, RuleRange},
		{"bpb", long, long, `{"beats_per_bar":9}`, al, RuleRange},
		{"ratio", long, long, `{"converge_bar":6}`, string(d5Align(40, 240, 0, 0)), RuleTempoRatio},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, a, err := ParsePolytempo([]byte(c.params), []byte(c.algn))
			if err == nil {
				p.N = d5N
				_, _, err = Polytempo(context.Background(), c.a, c.b, p, a, nil)
			}
			var pe *ParamError
			if !errors.As(err, &pe) || pe.Rule != c.rule {
				t.Fatalf("error %v, want rule %s", err, c.rule)
			}
		})
	}
}

func TestPolytempo_PanAndMeter(t *testing.T) {
	d4SkipHeavy(t)
	t.Parallel()
	a := dsptest.Dual(d5Rate, dsptest.Noise(1, 50*d5Rate, 0.1))
	b := dsptest.Silence(60 * d5Rate)
	b2 := dsptest.Dual(d5Rate, b)
	algn := string(d5Align(100, 100, 0, 0))
	p, al, _ := ParsePolytempo([]byte(`{"converge_bar":6,"unison_bars":2,"pan":"split"}`), []byte(algn))
	p.N = d5N
	m := &dsp.Meter{}
	out, _, err := Polytempo(context.Background(), a, b2, p, al, m)
	if err != nil {
		t.Fatal(err)
	}
	// A is left-weighted by the constant-power split, -3 dB on each side
	l, r := dsptest.RMS(out.Ch[0]), dsptest.RMS(out.Ch[1])
	wantL, wantR := stackGainsRMS(-polySplit)
	d5Near(t, "L/R ratio dB", 20*math.Log10(l/r), 20*math.Log10(wantL/wantR), 0.1)
	d5Near(t, "left level dB re source", 20*math.Log10(l/dsptest.RMS(a.Ch[0])), 20*math.Log10(wantL), 0.1)
	model := PolytempoUnits(int64(a.Frames()+b2.Frames()), int64(out.Frames()), d5Rate, p, 100)
	if rt := float64(m.Used()) / float64(model); rt < 0.8 || rt > 1.05 {
		t.Errorf("meter used %d units, model %d (ratio %.2f)", m.Used(), model, rt)
	}
	if _, _, err := Polytempo(context.Background(), a, b2, p, al, &dsp.Meter{Max: m.Used() / 2}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("half the budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Polytempo(ctx, a, b2, p, al, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func stackGainsRMS(pan float64) (l, r float64) {
	gl, gr := stackGains(pan)
	s := dbToLin(polySideDB)
	return gl * s, gr * s
}
