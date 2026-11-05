package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

func d4Choir(seconds float64) pcm.Buffer {
	return dsptest.Dual(d4Rate, dsptest.Sine(d4Rate, 220, 0.4, seconds))
}

func TestEnsemble_Stack(t *testing.T) {
	t.Parallel()
	x := d4Choir(1.5)
	stack, err := ensembleStack(context.Background(), x, EnsembleParams{N: 1024}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := x.Frames() + 42*d4Rate/1000; len(stack.Ch[0]) != want || len(stack.Ch[1]) != want {
		t.Fatalf("stack length %d/%d, want %d", len(stack.Ch[0]), len(stack.Ch[1]), want)
	}
	// every voice is at its semitone offset from 220 Hz and clearly present (a pure tone has no envelope for the formant
	// ratio to hold, so the levels of the voices differ from their gains; the pan law is checked on the ratio of the sides)
	body := d4Window(stack.Ch[0], d4Rate, 0.4, 1.2)
	_, top := d4PeakIn(body, d4Rate, 80, 600)
	for _, v := range EnsembleVoices {
		want := 220 * math.Exp2(v.Semitones/12)
		f, level := d4PeakIn(body, d4Rate, want*0.96, want*1.04)
		d4Near(t, "voice frequency", f, want, 2.5)
		if db := d4DB(level, top); db < -45 {
			t.Errorf("voice %+v at %.1f Hz is %.1f dB below the strongest", v, f, -db)
		}
	}
	// the delays: after the input ends only the delayed voices ring on, the last one until 42 ms
	end := x.Frames()
	if e := dsptest.RMS(stack.Ch[0][end+31*d4Rate/1000 : end+42*d4Rate/1000]); e <= 0 {
		t.Error("nothing of the 42 ms voice after the input ended")
	}
	if e := dsptest.RMS(stack.Ch[0][end-d4Rate/100 : end]); e <= 0 {
		t.Error("silent end of the input")
	}
}

func TestEnsemble_PanLaw(t *testing.T) {
	t.Parallel()
	for _, v := range EnsembleVoices {
		l, r := stackGains(v.Pan)
		if d := math.Abs(l*l + r*r - 2); d > 1e-9 {
			t.Errorf("pan %v: L^2+R^2 = %v, want 2 (constant power times the sqrt 2 scale)", v.Pan, l*l+r*r)
		}
	}
	if l, r := stackGains(0); math.Abs(l-1) > 1e-12 || math.Abs(r-1) > 1e-12 {
		t.Errorf("centre = (%v, %v), want unity", l, r)
	}
	// each voice sits left or right of the centre by its pan gains: the same voice in both sides differs by the gain ratio only
	stack, err := ensembleStack(context.Background(), d4Choir(1.5), EnsembleParams{N: 1024}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bl, br := d4Window(stack.Ch[0], d4Rate, 0.4, 1.2), d4Window(stack.Ch[1], d4Rate, 0.4, 1.2)
	for _, v := range EnsembleVoices {
		want := 220 * math.Exp2(v.Semitones/12)
		_, ll := d4PeakIn(bl, d4Rate, want*0.96, want*1.04)
		_, rl := d4PeakIn(br, d4Rate, want*0.96, want*1.04)
		gl, gr := stackGains(v.Pan)
		if d := d4DB(ll/rl, gl/gr); math.Abs(d) > 0.5 {
			t.Errorf("voice at pan %v: left/right %.2f dB off the pan law", v.Pan, d)
		}
	}
	// a stereo input keeps its image: only the left channel makes the left side louder than the right side's share
	st := dsptest.Stereo(d4Rate, dsptest.Sine(d4Rate, 220, 0.4, 1), make([]float64, d4Rate))
	s2, err := ensembleStack(context.Background(), st, EnsembleParams{N: 1024}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dsptest.RMS(s2.Ch[1]) != 0 || dsptest.RMS(s2.Ch[0]) == 0 {
		t.Error("a left-only input leaked into the right side of the stack")
	}
}

func TestEnsemble_TailLength(t *testing.T) {
	t.Parallel()
	x := d4Choir(0.5)
	room, _ := ir.Preset("hangar")
	_, _, info := ir.Build(room, d4Rate, 5, nil)
	p := EnsembleParams{Space: "hangar", WetDB: -6, DryDB: -12, TailS: 20, N: 1024}
	out, irLen, err := Ensemble(context.Background(), x, p, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if irLen != info.EffectiveLen || out.Frames() != x.Frames()+info.EffectiveLen || len(out.Ch[1]) != out.Frames() {
		t.Errorf("tail %d (effective %d), frames %d, want %d", irLen, info.EffectiveLen, out.Frames(), x.Frames()+info.EffectiveLen)
	}
	// tail_s cuts the response, with a fade, but never lengthens it
	p.TailS = 1.5
	out, irLen, err = Ensemble(context.Background(), x, p, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if irLen != int(1.5*d4Rate) || out.Frames() != x.Frames()+irLen {
		t.Errorf("tail_s 1.5: tail %d frames %d", irLen, out.Frames())
	}
	if end := dsptest.RMS(out.Ch[0][out.Frames()-20:]); end > 1e-3*dsptest.RMS(out.Ch[0]) {
		t.Errorf("the cut tail ends at %.5f, not faded out", end)
	}
	// no tail at all: the longest voice delay still fits
	p.TailS = 0
	out, irLen, err = Ensemble(context.Background(), x, p, 5, nil)
	if err != nil || irLen != 0 || out.Frames() != x.Frames()+maxDelay(d4Rate) {
		t.Errorf("tail_s 0: tail %d frames %d (%v), want %d", irLen, out.Frames(), err, x.Frames()+maxDelay(d4Rate))
	}
}

func TestEnsemble_DryWet(t *testing.T) {
	t.Parallel()
	x := d4Choir(0.5)
	base := EnsembleParams{Space: "hangar", WetDB: -40, DryDB: 0, TailS: 2, N: 1024}
	dry, _, err := Ensemble(context.Background(), x, base, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	noDry := base
	noDry.NoDry = true
	wetOnly, _, err := Ensemble(context.Background(), x, noDry, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := d4DB(dsptest.RMS(dry.Ch[0][2000:6000]), dsptest.RMS(wetOnly.Ch[0][2000:6000])); d < 20 {
		t.Errorf("dry 0 dB over wet -40 dB: only %.1f dB apart at the start", d)
	}
	// the tail beyond the input is the room alone: present with a wet send, absent without one and without delays' help
	if dsptest.RMS(wetOnly.Ch[0][x.Frames()+2000:]) <= 0 {
		t.Error("no reverb tail")
	}
	loud := base
	loud.WetDB = 0
	more, _, _ := Ensemble(context.Background(), x, loud, 1, nil)
	late := func(b pcm.Buffer) float64 { return dsptest.RMS(b.Ch[0][x.Frames()+3000 : x.Frames()+12000]) }
	if d := d4DB(late(more), late(dry)); d < 35 {
		t.Errorf("wet 0 dB vs -40 dB: %.1f dB louder tail, want ~40", d)
	}
}

func TestEnsemble_Parse(t *testing.T) {
	t.Parallel()
	p, err := ParseEnsemble(nil)
	if err != nil || p.Space != "cathedral" || p.WetDB != -6 || p.DryDB != -12 || p.NoDry || p.TailS != 20 || p.frame() != 4096 {
		t.Errorf("defaults: %+v, %v", p, err)
	}
	p, err = ParseEnsemble(json.RawMessage(`{"space":"canyon","wet_db":-3,"dry_db":null,"tail_s":5}`))
	if err != nil || p.Space != "canyon" || p.WetDB != -3 || !p.NoDry || p.TailS != 5 {
		t.Errorf("null dry: %+v, %v", p, err)
	}
	for name, c := range map[string]struct{ params, field, rule string }{
		"space":    {`{"space":"custom"}`, "space", RulePreset},
		"wet":      {`{"wet_db":1}`, "wet_db", RuleRange},
		"dry":      {`{"dry_db":-41}`, "dry_db", RuleRange},
		"tail":     {`{"tail_s":41}`, "tail_s", RuleRange},
		"tail neg": {`{"tail_s":-1}`, "tail_s", RuleRange},
		"type":     {`{"wet_db":"x"}`, "wet_db", "type"},
		"json":     {`[`, "params", "type"},
	} {
		_, err := ParseEnsemble(json.RawMessage(c.params))
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Field != c.field || pe.Rule != c.rule {
			t.Errorf("%s: err = %v, want %s/%s", name, err, c.field, c.rule)
		}
	}
	if _, _, err := Ensemble(context.Background(), d4Choir(0.2), EnsembleParams{Space: "x"}, 1, nil); err == nil {
		t.Error("Ensemble accepted an unknown space")
	}
}

func TestEnsemble_RunMeterAndErrors(t *testing.T) {
	t.Parallel()
	x := d4Choir(0.5)
	params := json.RawMessage(`{"space":"hangar","tail_s":2}`)
	m := &dsp.Meter{}
	out, err := runEnsemble(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params, Seed: 3, Meter: m})
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Ensemble struct {
			Space    string `json:"space"`
			IRFrames int    `json:"ir_frames"`
		} `json:"ensemble"`
	}
	if err := json.Unmarshal(out.Report, &rep); err != nil || rep.Ensemble.Space != "hangar" || rep.Ensemble.IRFrames != 2*d4Rate {
		t.Errorf("report %s: %v", out.Report, err)
	}
	_, u, ok := Lookup(KindEnsemble)
	if !ok {
		t.Fatal("ensemble is not registered")
	}
	// the registered model runs the 4096-point vocoder like the operator and bounds the real work from above
	want := u(int64(x.Frames()), 0, d4Rate, params)
	if m.Used() > want || float64(m.Used()) < 0.8*float64(want) {
		t.Errorf("meter used %d, units model %d: want used <= model and within 20%%", m.Used(), want)
	}
	if got := u(int64(x.Frames()), 0, d4Rate, json.RawMessage(`{`)); got <= 0 {
		t.Errorf("unparsable params: %d", got)
	}
	if _, err := runEnsemble(context.Background(), RunIn{Rate: d4Rate, Params: params}); err == nil {
		t.Error("no input accepted")
	}
	if _, err := runEnsemble(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: json.RawMessage(`{"space":"x"}`)}); err == nil {
		t.Error("bad params accepted")
	}
	for _, max := range []int64{want / 50, want / 2, want * 9 / 10} {
		if _, err := runEnsemble(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params, Seed: 3, Meter: &dsp.Meter{Max: max}}); !errors.Is(err, dsp.ErrBudget) {
			t.Errorf("budget %d: %v", max, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runEnsemble(ctx, RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{x}, Params: params}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	y := x.Clone()
	y.Rate = 0
	if _, err := runEnsemble(context.Background(), RunIn{Rate: d4Rate, Inputs: []pcm.Buffer{y}, Params: params}); err != nil {
		t.Errorf("rate fallback: %v", err)
	}
}
