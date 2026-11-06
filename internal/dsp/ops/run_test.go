package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// d5Fixture is the fixed job of one kind for the dispatcher suites. A kind that registers later (binaural) adds its own row from
// its own test file's init, so TestRun_Dispatch covers it by registering.
type d5Fixture struct {
	params, align string
	pair          bool
}

var d5Jobs = map[string]d5Fixture{
	KindGlacier:   {params: `{"factor":10,"start_s":0.2,"length_s":1.2,"window_s":0.1,"fade_in_s":0.5,"fade_out_s":0.5}`},
	KindShepard:   {params: `{"direction":"up","loop_s":1,"start_s":0.1,"layers":3,"period_s":8,"duration_s":4,"bell_sigma_octaves":0.6}`},
	KindEnsemble:  {params: `{"space":"hangar","wet_db":-6,"dry_db":-12,"tail_s":3}`},
	KindMaster:    {params: `{}`},
	KindWiden:     {params: `{}`},
	KindMorph:     {params: `{"bars":2,"beats_per_bar":4,"a_at":{"bar":1},"b_at":{"bar":0},"semitones_b":2}`, align: string(d5Align(120, 120, 0.1, 0.1)), pair: true},
	KindPolytempo: {params: `{"converge_bar":4,"unison_bars":2,"pan":"split"}`, align: string(d5Align(120, 100, 0.1, 0.1)), pair: true},
}

func d5Inputs(pair bool) []Input {
	if !pair {
		return []Input{{Role: "source", Buf: d4GoldenInput()}}
	}
	mix := func(first, bpm float64, seed uint64) pcm.Buffer {
		n := dsptest.Frames(d5Rate, 16)
		x := d5Clicks(d5Rate, bpm, first, 16, 0.6)
		pad := dsptest.Noise(seed, n, 0.01)
		for i := range x {
			x[i] += pad[i]
		}
		return dsptest.Dual(d5Rate, x)
	}
	return []Input{{Role: "a", Buf: mix(0.1, 120, 1)}, {Role: "b", Buf: mix(0.1, 100, 2)}}
}

func d5Job(t testing.TB, kind string) Job {
	t.Helper()
	f, ok := d5Jobs[kind]
	if !ok {
		t.Fatalf("kind %q is registered but has no dispatcher fixture: add a row to d5Jobs from a test file's init", kind)
	}
	return Job{Kind: kind, Rate: d5Rate, Seed: 7, Params: json.RawMessage(f.params), Alignment: json.RawMessage(f.align),
		Inputs: d5Inputs(f.pair), TargetLUFS: -16, RefLUFS: -15.2}
}

func d5Hash(t testing.TB, b pcm.Buffer) string {
	t.Helper()
	s, err := pcm.WritePCM24(io.Discard, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestOps_Signature calls every kind through the one signature of C205.
func TestOps_Signature(t *testing.T) {
	d4SkipHeavy(t)
	for _, kind := range Kinds() {
		t.Run(kind, func(t *testing.T) {
			j := d5Job(t, kind)
			out, rep, err := Run(context.Background(), j)
			if err != nil {
				t.Fatal(err)
			}
			if out.Rate != d5Rate || out.Frames() == 0 || len(out.Ch[0]) != len(out.Ch[1]) {
				t.Fatalf("buffer rate %d, %d/%d frames", out.Rate, len(out.Ch[0]), len(out.Ch[1]))
			}
			if rep.Frames != int64(out.Frames()) || rep.Units <= 0 {
				t.Errorf("report frames %d (buffer %d), units %d", rep.Frames, out.Frames(), rep.Units)
			}
			if rep.Safety.TargetLUFSRequested == 0 || rep.Safety.TargetLUFSEffective == 0 || rep.Safety.TruePeakDBTP > -0.5 {
				t.Errorf("safety %+v", rep.Safety)
			}
			if tp := safety.TruePeakDBTP(out); tp > -0.5 {
				t.Errorf("true peak %.2f dBTP after the render", tp)
			}
			if len(rep.BeatPhase) != len(j.Inputs) {
				t.Errorf("%d beat_phase entries for %d inputs", len(rep.BeatPhase), len(j.Inputs))
			}
			for _, ch := range out.Ch {
				for _, v := range ch {
					if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1 {
						t.Fatalf("sample %v", v)
					}
				}
			}
		})
	}
}

// TestRun_Dispatch: every registered kind runs through Lookup, an unknown kind is an error, and the self-finishing kinds are
// not finished a second time.
func TestRun_Dispatch(t *testing.T) {
	d4SkipHeavy(t)
	if got := Kinds(); len(got) < 7 {
		t.Fatalf("registered kinds %v", got)
	}
	_, _, err := Run(context.Background(), Job{Kind: "nope", Rate: d5Rate, Inputs: d5Inputs(false)})
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Rule != RuleUnknownKind {
		t.Errorf("unknown kind: %v", err)
	}
	for _, kind := range Kinds() {
		j := d5Job(t, kind)
		raw, _, _ := Lookup(kind)
		in := RunIn{Rate: j.Rate, Params: j.Params, Alignment: j.Alignment, Seed: j.Seed, TargetLUFS: j.TargetLUFS, RefLUFS: j.RefLUFS, Meter: &dsp.Meter{}}
		for _, x := range j.Inputs {
			in.Inputs = append(in.Inputs, x.Buf)
		}
		direct, err := raw(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		out, _, err := Run(context.Background(), j)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		same := d5Hash(t, direct.Buffer) == d5Hash(t, out)
		if SelfFinishing(kind) != same {
			t.Errorf("%s: self-finishing=%v but the dispatcher's output equals the operator's: %v", kind, SelfFinishing(kind), same)
		}
	}
}

func TestRun_Deterministic(t *testing.T) {
	d4SkipHeavy(t)
	for _, kind := range Kinds() {
		a, _, err := Run(context.Background(), d5Job(t, kind))
		if err != nil {
			t.Fatal(err)
		}
		b, _, _ := Run(context.Background(), d5Job(t, kind))
		if d5Hash(t, a) != d5Hash(t, b) {
			t.Errorf("%s: two runs differ", kind)
		}
	}
}

func TestRun_Report(t *testing.T) {
	d4SkipHeavy(t)
	keys := func(rep Report) []string {
		raw, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	fixed := []string{"beat_phase", "blind_spots", "frames", "pcm_sha256", "safety", "units", "voices", "warnings"}
	for kind, extra := range map[string]string{KindGlacier: "glacier", KindEnsemble: "ensemble", KindMaster: "hifi", KindMorph: "morph", KindPolytempo: "polytempo"} {
		_, rep, err := Run(context.Background(), d5Job(t, kind))
		if err != nil {
			t.Fatal(err)
		}
		if want := append(slices.Clone(fixed), extra); !slices.Equal(keys(rep), sortedCopy(want)) {
			t.Errorf("%s: keys %v, want %v", kind, keys(rep), sortedCopy(want))
		}
		var voices []map[string]float64
		_ = json.Unmarshal(rep.Voices, &voices)
		if (kind == KindEnsemble) != (len(voices) == len(EnsembleVoices)) {
			t.Errorf("%s: voices %s", kind, rep.Voices)
		}
		if kind == KindMorph || kind == KindPolytempo {
			if len(rep.BlindSpots) == 0 || len(rep.BeatPhase) != 2 || string(rep.BeatPhase[0]) == "null" {
				t.Errorf("%s: blind spots %v, beat_phase %s", kind, rep.BlindSpots, rep.BeatPhase)
			}
		} else if string(rep.BeatPhase[0]) != "null" {
			t.Errorf("%s: beat_phase %s", kind, rep.BeatPhase)
		}
		if kind == KindMaster && rep.Safety.TruePeakIn == 0 {
			t.Error("master: the operator's own safety object was not merged as it is")
		}
	}
}

func sortedCopy(s []string) []string { out := slices.Clone(s); sort.Strings(out); return out }

func TestRun_Progress(t *testing.T) {
	d4SkipHeavy(t)
	var got []int
	phases := map[string]bool{}
	j := d5Job(t, KindEnsemble)
	j.Progress = func(p int, phase string) { got = append(got, p); phases[phase] = true }
	if _, _, err := Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 || !slices.IsSorted(got) || got[len(got)-1] != renderShare || !phases["rendering"] || !phases["finishing"] {
		t.Errorf("progress %v phases %v", got, phases)
	}
	if slices.Max(got) >= 1000 {
		t.Errorf("progress reached %d", slices.Max(got))
	}
}

func TestRun_BudgetAndContext(t *testing.T) {
	d4SkipHeavy(t)
	j := d5Job(t, KindGlacier)
	j.Meter = &dsp.Meter{Max: 1000}
	if _, _, err := Run(context.Background(), j); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("tiny budget: %v", err)
	}
	// the budget also covers safety: enough for the operator, not for the finish
	full := d5Job(t, KindGlacier)
	_, rep, err := Run(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	opOnly := rep.Units - SafetyUnits(rep.Frames)
	j = d5Job(t, KindGlacier)
	j.Meter = &dsp.Meter{Max: opOnly + SafetyUnits(rep.Frames)/2}
	if _, _, err := Run(context.Background(), j); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("budget without room for safety: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Run(ctx, d5Job(t, KindEnsemble)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	// cancelled while running: the meter's poll sees the context
	ctx, cancel = context.WithCancel(context.Background())
	j = d5Job(t, KindEnsemble)
	j.Progress = func(int, string) { cancel() }
	if _, _, err := Run(ctx, j); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled by the progress hook: %v", err)
	}
}

func TestRun_BadJobs(t *testing.T) {
	var pe *ParamError
	j := d5Job(t, KindGlacier)
	j.Inputs = nil
	if _, _, err := Run(context.Background(), j); !errors.As(err, &pe) {
		t.Errorf("no inputs: %v", err)
	}
	j = d5Job(t, KindGlacier)
	j.Inputs[0].Buf.Rate = 8000
	if _, _, err := Run(context.Background(), j); !errors.As(err, &pe) || pe.Rule != RuleChannel {
		t.Errorf("rate mismatch: %v", err)
	}
	j = d5Job(t, KindGlacier)
	j.Rate = 0
	if _, _, err := Run(context.Background(), j); !errors.As(err, &pe) {
		t.Errorf("zero rate: %v", err)
	}
	j = d5Job(t, KindGlacier)
	j.Params = json.RawMessage(`{"factor":"x"}`)
	if _, _, err := Run(context.Background(), j); !errors.As(err, &pe) {
		t.Errorf("bad params: %v", err)
	}
}

// d5Temp registers a throw-away kind for one test and removes it again. It is not parallel-safe with Kinds() iteration, so the
// tests that use it are sequential (parallel tests of this package wait for them).
func d5Temp(t *testing.T, kind string, fn RunFunc) {
	t.Helper()
	Register(kind, fn, func(int64, int64, int, json.RawMessage) int64 { return 1 })
	t.Cleanup(func() { mu.Lock(); delete(reg, kind); mu.Unlock() })
}

func TestRun_SafetyFailures(t *testing.T) {
	good := dsptest.Dual(d5Rate, dsptest.Sine(d5Rate, 440, 0.3, 1))
	bad := func(f func(*pcm.Buffer)) RunFunc {
		return func(context.Context, RunIn) (RunOut, error) {
			b := good.Clone()
			f(&b)
			return RunOut{Buffer: b}, nil
		}
	}
	d5Temp(t, "zz_nan", bad(func(b *pcm.Buffer) { b.Ch[0][100] = math.NaN() }))
	d5Temp(t, "zz_silent", bad(func(b *pcm.Buffer) { clear(b.Ch[0]); clear(b.Ch[1]) }))
	d5Temp(t, "zz_ok", bad(func(*pcm.Buffer) {}))
	d5Temp(t, "zz_err", func(context.Context, RunIn) (RunOut, error) { return RunOut{}, errors.New("boom") })
	d5Temp(t, "zz_badreport", func(context.Context, RunIn) (RunOut, error) {
		return RunOut{Buffer: good.Clone(), Report: json.RawMessage(`{"warnings":3}`)}, nil
	})
	d5Temp(t, "zz_claims_safety", func(context.Context, RunIn) (RunOut, error) {
		return RunOut{Buffer: good.Clone(), Report: json.RawMessage(`{"safety":{}}`)}, nil
	})
	job := func(kind string) Job {
		return Job{Kind: kind, Rate: d5Rate, Inputs: []Input{{Buf: good}}, TargetLUFS: -16}
	}
	for kind, want := range map[string]error{"zz_nan": safety.ErrNonFinite, "zz_silent": safety.ErrSilent, "zz_err": nil} {
		_, _, err := Run(context.Background(), job(kind))
		if err == nil || (want != nil && !errors.Is(err, want)) {
			t.Errorf("%s: %v, want %v", kind, err, want)
		}
	}
	if _, _, err := Run(context.Background(), job("zz_ok")); err != nil {
		t.Errorf("a sine through the finish: %v", err)
	}
	if _, _, err := Run(context.Background(), job("zz_badreport")); err == nil {
		t.Error("a malformed operator report was accepted")
	}
	// an operator that carries a safety object without being listed as self-finishing is a bug, not a pass-through
	if _, _, err := Run(context.Background(), job("zz_claims_safety")); !errors.Is(err, errSafetyMismatch) {
		t.Errorf("safety claimed by an unlisted kind: %v", err)
	}
}

func TestReport_MarshalPrecedence(t *testing.T) {
	r := Report{PCMSHA256: "x", Frames: 3, Extra: map[string]json.RawMessage{"frames": json.RawMessage("99"), "glacier": json.RawMessage(`{"a":1}`)}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if string(m["frames"]) != "3" || string(m["glacier"]) != `{"a":1}` || string(m["voices"]) != "null" || string(m["warnings"]) != "[]" || string(m["blind_spots"]) != "[]" {
		t.Errorf("report %s", raw)
	}
}
