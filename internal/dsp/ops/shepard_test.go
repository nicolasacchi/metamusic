package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// d4ShepardParams is the test configuration of C205.4: layers 3, period 2 s, sigma 0.6, and the 1024-point vocoder at 16 kHz.
func d4ShepardParams(direction string, duration float64) ShepardParams {
	return ShepardParams{Direction: direction, LoopS: 1, StartS: 0, Layers: 3, PeriodS: 2, DurationS: duration, SigmaOct: 0.6, N: 1024}
}

func d4Shepard(t testing.TB, p ShepardParams, m *dsp.Meter) pcm.Buffer {
	t.Helper()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 220, 0.5, 1.5))
	out, err := Shepard(context.Background(), x, p, m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestShepard_PitchTrack(t *testing.T) {
	t.Parallel()
	out := d4Shepard(t, d4ShepardParams("up", 4.1), nil)
	if out.Frames() != int(math.Floor(4.1*d4Rate+0.5)) {
		t.Fatalf("frames %d", out.Frames())
	}
	peaks := func(centre float64) (lo, hi, loL, hiL float64) {
		w := d4Window(out.Ch[0], d4Rate, centre-0.1, centre+0.1)
		lo, loL = d4PeakIn(w, d4Rate, 100, 230)
		hi, hiL = d4PeakIn(w, d4Rate, 230, 700)
		return
	}
	// tau = 2 s is the same state as tau = 0: two equal peaks half an octave either side of 220 Hz
	lo, hi, loL, hiL := peaks(2)
	d4Near(t, "tau 2 s: lower peak", lo, 155.6, 3)
	d4Near(t, "tau 2 s: upper peak", hi, 311.1, 3)
	d4Near(t, "tau 2 s: level difference dB", d4DB(loL, hiL), 0, 1.5)
	// tau = 1 s: the dominant peak sits at 220 Hz
	lo, hi, _, _ = peaks(1)
	if f, _ := d4PeakIn(d4Window(out.Ch[0], d4Rate, 0.9, 1.1), d4Rate, 100, 700); math.Abs(f-220) > 3 {
		t.Errorf("tau 1 s: dominant peak %.1f Hz, want 220 +- 3 (%.1f, %.1f)", f, lo, hi)
	}
	// the start of the run, windowed around 0.1 s: the layers are where the formula puts them
	w := d4Window(out.Ch[0], d4Rate, 0.05, 0.25)
	p := d4ShepardParams("up", 4)
	f1, _ := d4PeakIn(w, d4Rate, 100, 230)
	f2, _ := d4PeakIn(w, d4Rate, 230, 700)
	d4Near(t, "tau 0.15 s: lower", f1, 220*math.Exp2(p.offset(1, 0.15)), 4)
	d4Near(t, "tau 0.15 s: upper", f2, 220*math.Exp2(p.offset(2, 0.15)), 6)
	// both channels carry the same dual-mono source
	if d4Pearson(out.Ch[0], out.Ch[1]) < 0.999999 {
		t.Error("the channels of a dual-mono source differ")
	}
}

func TestShepard_EnvelopeFlat(t *testing.T) {
	t.Parallel()
	out := d4Shepard(t, d4ShepardParams("up", 12), nil)
	lo, hi := math.Inf(1), math.Inf(-1)
	for s := 0.0; s+0.5 <= 12; s += 0.5 {
		r := dsptest.DBFS(dsptest.RMS(d4Window(out.Ch[0], d4Rate, s, s+0.5)))
		lo, hi = math.Min(lo, r), math.Max(hi, r)
	}
	if hi-lo > 3 { // within +-1.5 dB of a common level
		t.Errorf("RMS of 0.5 s windows spans %.2f dB (%.2f .. %.2f), want <= 3", hi-lo, lo, hi)
	}
}

func TestShepard_Down_Mirrors(t *testing.T) {
	t.Parallel()
	down := d4Shepard(t, d4ShepardParams("down", 1.5), nil)
	up := d4Shepard(t, d4ShepardParams("up", 6), nil)
	// down at tau equals up at the mirrored time: the set of pitches (and levels) is the same
	const tau = 0.5
	dw := d4Window(down.Ch[0], d4Rate, tau-0.1, tau+0.1)
	uw := d4Window(up.Ch[0], d4Rate, 6-tau-0.1, 6-tau+0.1)
	for _, band := range [][2]float64{{100, 200}, {200, 400}, {400, 800}} {
		fd, ld := d4PeakIn(dw, d4Rate, band[0], band[1])
		fu, lu := d4PeakIn(uw, d4Rate, band[0], band[1])
		d4Near(t, "peak in band", fd, fu, 4)
		d4Near(t, "peak level dB", d4DB(ld, lu), 0, 2)
	}
}

func TestShepard_Gains(t *testing.T) {
	t.Parallel()
	p := ShepardParams{Direction: "up", LoopS: 1, Layers: 5, PeriodS: 24, DurationS: 60}
	if p.sigma() != 1 {
		t.Errorf("default sigma %v, want layers/5", p.sigma())
	}
	for tau := 0.0; tau < 130; tau += 0.7 {
		g := p.gains(tau, nil)
		var s float64
		for j, v := range g {
			s += v * v
			if o := p.offset(j, tau); math.Abs(o) >= 2.5 && v != 0 {
				t.Fatalf("tau %v layer %d at offset %v has gain %v: the edge must be silent", tau, j, o, v)
			}
		}
		if math.Abs(s-1) > 1e-12 {
			t.Fatalf("tau %v: sum of squared gains %v", tau, s)
		}
	}
	// a layer wraps from the top to the bottom exactly where its weight is zero
	for tau := 0.0; tau < 100; tau += 0.05 {
		prev, cur := p.offset(0, tau), p.offset(0, tau+0.05)
		if cur < prev-1 && (p.weight(prev) > 0.02 || p.weight(cur) > 0.02) {
			t.Fatalf("tau %v: layer 0 wraps from %v to %v with weights %v %v", tau, prev, cur, p.weight(prev), p.weight(cur))
		}
	}
	d := ShepardParams{Direction: "down", Layers: 5, PeriodS: 24}
	if d.offset(0, 0) != 2.5 || d.offset(0, 12) != 2.0 {
		t.Errorf("down offsets: %v %v", d.offset(0, 0), d.offset(0, 12))
	}
}

func TestShepard_Parse(t *testing.T) {
	t.Parallel()
	al := json.RawMessage(`{"inputs":[{"bpm":120,"bpm_source":"measured","beats_per_bar":4}]}`)
	p, err := ParseShepard(json.RawMessage(`{"loop_bars":2,"start_s":3}`), al)
	if err != nil || math.Abs(p.LoopS-4) > 1e-12 || p.StartS != 3 || p.Layers != 5 || p.Direction != "up" || p.PeriodS != 24 || p.DurationS != 120 {
		t.Errorf("loop_bars: %+v, %v", p, err)
	}
	p, err = ParseShepard(json.RawMessage(`{"loop_s":3.5,"loop_bars":4,"layers":7,"direction":"down","bell_sigma_octaves":1.5,"period_s":10,"duration_s":50}`), nil)
	if err != nil || p.LoopS != 3.5 || p.Layers != 7 || p.Direction != "down" || p.SigmaOct != 1.5 || p.sigma() != 1.5 {
		t.Errorf("loop_s wins: %+v, %v", p, err)
	}
	if p, err = ParseShepard(json.RawMessage(`{"loop_bars":1}`), json.RawMessage(`{"inputs":[{"bpm":90}]}`)); err != nil || math.Abs(p.LoopS-240.0/90) > 1e-12 {
		t.Errorf("default beats per bar: %+v, %v", p, err)
	}
	for name, c := range map[string]struct {
		params, al, field, rule string
	}{
		"no source":   {`{}`, ``, "loop_s", RuleLoopSource},
		"no tempo":    {`{"loop_bars":2}`, `{"inputs":[{"bpm":0}]}`, "loop_s", RuleLoopSource},
		"no input":    {`{"loop_bars":2}`, `{"inputs":[]}`, "loop_s", RuleLoopSource},
		"bad align":   {`{"loop_bars":2}`, `{`, "params", "type"},
		"bad params":  {`{`, ``, "params", "type"},
		"direction":   {`{"loop_s":1,"direction":"left"}`, ``, "direction", RuleRange},
		"loop long":   {`{"loop_s":61}`, ``, "loop_s", RuleRange},
		"start":       {`{"loop_s":1,"start_s":-1}`, ``, "start_s", RuleRange},
		"layers":      {`{"loop_s":1,"layers":2}`, ``, "layers", RuleRange},
		"period":      {`{"loop_s":1,"period_s":0}`, ``, "period_s", RuleRange},
		"duration":    {`{"loop_s":1,"duration_s":901}`, ``, "duration_s", RuleRange},
		"sigma":       {`{"loop_s":1,"bell_sigma_octaves":9}`, ``, "bell_sigma_octaves", RuleRange},
		"negative lp": {`{"loop_s":-1}`, ``, "loop_s", RuleRange},
	} {
		_, err := ParseShepard(json.RawMessage(c.params), json.RawMessage(c.al))
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Field != c.field || pe.Rule != c.rule {
			t.Errorf("%s: err = %v, want %s/%s", name, err, c.field, c.rule)
		}
	}
}

func TestShepard_RunAndMeter(t *testing.T) {
	t.Parallel()
	x := dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 220, 0.5, 1.5))
	params := json.RawMessage(`{"loop_s":1,"layers":3,"period_s":8,"duration_s":9,"start_s":0.2}`)
	m := &dsp.Meter{}
	out, err := runShepard(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params, Meter: m})
	if err != nil {
		t.Fatal(err)
	}
	if out.Buffer.Frames() != 9*d4Rate {
		t.Errorf("frames %d", out.Buffer.Frames())
	}
	_, u, ok := Lookup(KindShepard)
	if !ok {
		t.Fatal("shepard is not registered")
	}
	want := u(0, int64(9*d4Rate), d4Rate, params)
	if want < m.Used() || float64(m.Used()) < 0.9*float64(want) {
		t.Errorf("meter used %d, units model %d: want used <= model and within 10%%", m.Used(), want)
	}
	if got := u(0, int64(9*d4Rate), d4Rate, json.RawMessage(`{`)); got != ShepardUnits(int64(9*d4Rate), 5, pvWindow) {
		t.Errorf("unparsable params: %d", got)
	}
	var rep struct {
		Shepard struct {
			Layers int `json:"layers"`
		} `json:"shepard"`
	}
	if err := json.Unmarshal(out.Report, &rep); err != nil || rep.Shepard.Layers != 3 {
		t.Errorf("report %s: %v", out.Report, err)
	}
	if _, err := runShepard(context.Background(), RunIn{Rate: d4Rate, Params: params}); err == nil {
		t.Error("no input accepted")
	}
	if _, err := runShepard(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(`{}`)}); err == nil {
		t.Error("missing loop source accepted")
	}
	if _, err := runShepard(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params, Meter: &dsp.Meter{Max: want / 4}}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runShepard(ctx, RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	y := x.Clone()
	y.Rate = 0
	if _, err := runShepard(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{y}, Params: params}); err != nil {
		t.Errorf("rate fallback: %v", err)
	}
	bad := d4ShepardParams("up", 4)
	bad.StartS = 100
	if _, err := Shepard(context.Background(), x, bad, nil); err == nil {
		t.Error("start past the end accepted")
	}
	bad = d4ShepardParams("up", 4)
	bad.Layers = 1
	if _, err := Shepard(context.Background(), x, bad, nil); err == nil {
		t.Error("one layer accepted")
	}
}

// TestShepard_GroupingIsInvisible: rendering the layers in groups (a long output) gives the same bits as one bank.
func TestShepard_GroupingIsInvisible(t *testing.T) {
	t.Parallel()
	if g := voiceGroup(1<<20, 5); g != 5 {
		t.Errorf("a short output renders all layers at once, got groups of %d", g)
	}
	if g := voiceGroup(28_800_000, 7); g != 1 {
		t.Errorf("600 s of 7 layers at 48 kHz: groups of %d, want 1", g)
	}
	if g := voiceGroup(0, 5); g != 5 {
		t.Errorf("empty: %d", g)
	}
}
