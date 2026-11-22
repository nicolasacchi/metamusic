package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/score/synth"
)

// abcTune is a two-bar tune at Q:1/4=120: 2 s a bar, 4 s in all.
const abcTune = "X:1\nT:Synthetic tune\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|GABc|\n"

// nullSynth is a Synth that is never asked to render (estimates and creates do not synthesise).
type nullSynth struct{ calls atomic.Int32 }

func (n *nullSynth) Render(context.Context, synth.Request) (synth.Info, error) {
	n.calls.Add(1)
	return synth.Info{}, nil
}

// scoreRig wires a service with a soundfont set of the given names and a fake synth.
func scoreRig(t *testing.T, fluid bool, fonts ...string) (*rig, *int) {
	t.Helper()
	dir := t.TempDir()
	var fs []synth.Font
	for _, n := range fonts {
		p := filepath.Join(dir, n+".sf2")
		if err := os.WriteFile(p, []byte("synthetic soundfont "+n), 0o600); err != nil {
			t.Fatal(err)
		}
		fs = append(fs, synth.Font{Name: n, Path: p})
	}
	reads := new(int)
	r := newRig(t, func(_ *configT, o *Options) {
		o.ToolStatus = func() ToolStatus {
			return ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: fluid, FluidsynthVersion: "FluidSynth runtime version 2.3.4"}
		}
		o.Score = &ScoreSupport{Synth: &nullSynth{}, Info: synth.Info{Name: "fluidsynth", Version: "FluidSynth runtime version 2.3.4"},
			Fonts: synth.NewSet(fs, nil),
			ScoreABC: func(context.Context, string) ([]byte, string, bool, error) {
				*reads++
				return nil, "", false, nil
			}}
	})
	return r, reads
}

func scoreReq(params string) Request { return req(KindScoreRender, params) }

func abcParams(extra string) string {
	b, _ := json.Marshal(abcTune)
	return fmt.Sprintf(`{"score":{"abc":%s}%s}`, b, extra)
}

func TestScoreRender_UnavailableWithoutSoundfont(t *testing.T) {
	t.Parallel()
	for name, mod := range map[string]func(*configT, *Options){
		"no score support": func(_ *configT, o *Options) {
			o.ToolStatus = func() ToolStatus { return ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: true} }
		},
		"no readable font": func(_ *configT, o *Options) {
			o.ToolStatus = func() ToolStatus { return ToolStatus{FFmpeg: true, FLAC: true, Fluidsynth: true} }
			o.Score = &ScoreSupport{Synth: &nullSynth{}, Fonts: synth.NewSet([]synth.Font{{Name: "gone", Path: "/nonexistent/x.sf2"}}, nil)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, mod)
			_, err := r.svc.Estimate(t.Context(), human, scoreReq(abcParams("")))
			if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "soundfont" {
				t.Errorf("estimate details %v", pe.Details)
			}
			_, err = r.svc.Create(t.Context(), human, scoreReq(abcParams("")), nil)
			if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "soundfont" {
				t.Errorf("create details %v", pe.Details)
			}
		})
	}
}

func TestScoreRender_UnavailableWithoutFluidsynth(t *testing.T) {
	t.Parallel()
	r, _ := scoreRig(t, false, "orchestra")
	_, err := r.svc.Estimate(t.Context(), human, scoreReq(abcParams("")))
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "fluidsynth" {
		t.Errorf("estimate details %v", pe.Details)
	}
	_, err = r.svc.Create(t.Context(), human, scoreReq(abcParams("")), nil)
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "fluidsynth" {
		t.Errorf("create details %v", pe.Details)
	}
	if r.count("SELECT COUNT(*) FROM transforms") != 0 {
		t.Error("a refused create queued a row")
	}
}

func TestScoreRender_ValidateTable(t *testing.T) {
	t.Parallel()
	big, _ := json.Marshal(strings.Repeat("a", MaxScoreBytes+1))
	cases := []struct {
		name, params, field, rule string
	}{
		{"both sources", `{"score":{"abc":"X:1\nK:C\nC|","from_track_id":"t"}}`, "score", RuleRequired},
		{"no source", `{"score":{}}`, "score", RuleRequired},
		{"no score", `{}`, "score", RuleRequired},
		{"unknown member", `{"score":{"abc":"X:1\nK:C\nC|","midi":"x"}}`, "score.midi", RuleUnknownField},
		{"empty abc", `{"score":{"abc":"  "}}`, "score.abc", RuleRequired},
		{"abc too large", `{"score":{"abc":` + string(big) + `}}`, "score.abc", RuleScoreTooLarge},
		{"abc type", `{"score":{"abc":3}}`, "score.abc", RuleType},
		{"track type", `{"score":{"from_track_id":3}}`, "score.from_track_id", RuleType},
		{"unknown field", `{"score":{"abc":"X:1\nK:C\nC|"},"midi_base64":"x"}`, "midi_base64", RuleUnknownField},
		{"bad dialect", `{"score":{"abc":"X:1\nK:C\nC|"},"dialect":"abc2"}`, "dialect", RuleRange},
		{"path as soundfont", `{"score":{"abc":"X:1\nK:C\nC|"},"soundfont":"/etc/x.sf2"}`, "soundfont", RuleRange},
		{"upper soundfont", `{"score":{"abc":"X:1\nK:C\nC|"},"soundfont":"Orchestra"}`, "soundfont", RuleRange},
		{"bpm low", `{"score":{"abc":"X:1\nK:C\nC|"},"bpm":19}`, "bpm", RuleRange},
		{"bpm high", `{"score":{"abc":"X:1\nK:C\nC|"},"bpm":301}`, "bpm", RuleRange},
		{"tail high", `{"score":{"abc":"X:1\nK:C\nC|"},"tail_s":10.5}`, "tail_s", RuleRange},
		{"gain low", `{"score":{"abc":"X:1\nK:C\nC|"},"gain":0.05}`, "gain", RuleRange},
		{"gain high", `{"score":{"abc":"X:1\nK:C\nC|"},"gain":1.6}`, "gain", RuleRange},
		{"chords", `{"score":{"abc":"X:1\nK:C\nC|"},"chords":"arpeggio"}`, "chords", RuleRange},
	}
	for _, c := range cases {
		_, err := validateScore(json.RawMessage(c.params))
		var re *RuleError
		if !asRule(err, &re) || re.Field != c.field || re.Rule != c.rule {
			t.Errorf("%s: got %v, want %s %s", c.name, err, c.field, c.rule)
		}
	}
	// defaults are written out and a spelled-out default hashes like an absent one
	a, err := validateScore(json.RawMessage(`{"score":{"abc":"X:1\nK:C\nC|"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := validateScore(json.RawMessage(`{"score":{"abc":"X:1\nK:C\nC|"},"tail_s":3,"gain":0.6,"chords":"none","bpm":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("defaults differ:\n%s\n%s", a, b)
	}
	var m map[string]any
	_ = json.Unmarshal(a, &m)
	if m["tail_s"] != 3.0 || m["gain"] != 0.6 || m["chords"] != "none" {
		t.Errorf("defaults not written out: %v", m)
	}
}

func asRule(err error, re **RuleError) bool { return errors.As(err, re) }

func TestScoreRender_EstimateResolves(t *testing.T) {
	t.Parallel()
	r, _ := scoreRig(t, true, "orchestra", "piano")
	e, err := r.svc.Estimate(t.Context(), human, scoreReq(abcParams("")))
	if err != nil {
		t.Fatal(err)
	}
	// 4 s of score at Q:1/4=120 plus the default 3 s tail
	if e.Output.Seconds != 7 || e.Output.SampleRate != 48000 || e.Title != "Synthetic tune" || len(e.Inputs) != 0 {
		t.Errorf("estimate %+v", e)
	}
	if e.Tools.Soundfont != "orchestra" || !e.Tools.Fluidsynth {
		t.Errorf("tools %+v", e.Tools)
	}
	var m map[string]any
	_ = json.Unmarshal(e.Resolved, &m)
	if m["soundfont"] != "orchestra" || m["dialect"] != "standard" {
		t.Errorf("resolved %v", m)
	}
	hasDefault := false
	for _, w := range e.Warnings {
		hasDefault = hasDefault || w.Code == WarnSoundfontDefaultUsed
	}
	if !hasDefault {
		t.Errorf("no soundfont_default_used warning: %+v", e.Warnings)
	}
	if e.CPU.ExpectedMS <= 0 || e.MemoryMB <= 0 {
		t.Errorf("cost %+v mem %d", e.CPU, e.MemoryMB)
	}
	// a tempo override rescales the length: 240 bpm halves 4 s
	e2, err := r.svc.Estimate(t.Context(), human, scoreReq(abcParams(`,"bpm":240,"tail_s":0,"soundfont":"piano"`)))
	if err != nil {
		t.Fatal(err)
	}
	if e2.Output.Seconds != 2 || e2.Tools.Soundfont != "piano" {
		t.Errorf("override estimate %+v", e2)
	}
	for _, w := range e2.Warnings {
		if w.Code == WarnSoundfontDefaultUsed {
			t.Error("a named soundfont warned about the default")
		}
	}
	// a different soundfont is a different spec (its sha is in the spec)
	if e.SpecHash == e2.SpecHash {
		t.Error("different specs hash equally")
	}
	// the estimate wrote nothing
	if r.count("SELECT COUNT(*) FROM transforms") != 0 || r.count("SELECT COUNT(*) FROM recipes WHERE engine = 'dsp'") != 0 {
		t.Error("an estimate wrote rows")
	}
}

func TestScoreRender_RefusalsAtEstimate(t *testing.T) {
	t.Parallel()
	r, _ := scoreRig(t, true, "orchestra")
	_, err := r.svc.Estimate(t.Context(), human, scoreReq(abcParams(`,"soundfont":"nope"`)))
	if pe := wantProblem(t, err, 422, ErrSoundfontUnknown); pe.Details["soundfont"] != "nope" {
		t.Errorf("details %v", pe.Details)
	}
	_, err = r.svc.Estimate(t.Context(), human, scoreReq(`{"score":{"abc":"X:1\nM:abc\nK:C\nC|"}}`))
	wantProblem(t, err, 422, ErrScoreInvalid)
	_, err = r.svc.Estimate(t.Context(), human, scoreReq(`{"score":{"abc":"X:1\nK:C\n"}}`))
	wantProblem(t, err, 422, ErrScoreInvalid) // no notes
	_, err = r.svc.Estimate(t.Context(), human, scoreReq(`{"score":{"from_track_id":"no-such-take"}}`))
	wantProblem(t, err, 404, "NOT_FOUND")
	_, err = r.svc.Estimate(t.Context(), human, scoreReq(fmt.Sprintf(`{"score":{"from_track_id":%q}}`, r.tx("I"))))
	if pe := wantProblem(t, err, 422, ErrScoreInvalid); pe.Details["rule"] != "no_score" {
		t.Errorf("details %v", pe.Details)
	}
	// longer than the output limit (900 s): 80 whole-note bars at 20 quarter notes a minute, 960 s
	long := "X:1\nM:4/4\nL:1/1\nQ:1/4=20\nK:C\n" + strings.Repeat("C|", 80)
	b, _ := json.Marshal(long)
	_, err = r.svc.Estimate(t.Context(), human, scoreReq(fmt.Sprintf(`{"score":{"abc":%s}}`, b)))
	if pe := wantProblem(t, err, 422, ErrTransformTooLarge); pe.Details["limit"] != "output_seconds" {
		t.Errorf("details %v", pe.Details)
	}
}

// TestScoreRender_FromTrackIsAnnotation: a from_track_id source is the score_source input (never a ledger parent), its engine
// picks the dialect, its length comes from its scores row, and the estimate opens no file.
func TestScoreRender_FromTrackIsAnnotation(t *testing.T) {
	t.Parallel()
	r, reads := scoreRig(t, true, "orchestra")
	q := scoreReq(fmt.Sprintf(`{"score":{"from_track_id":%q},"tail_s":2}`, r.tx("S")))
	e, err := r.svc.Estimate(t.Context(), human, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Inputs) != 1 || e.Inputs[0].Role != "score_source" || e.Inputs[0].TrackID != r.tx("S") {
		t.Fatalf("inputs %+v", e.Inputs)
	}
	var m map[string]any
	_ = json.Unmarshal(e.Resolved, &m)
	if m["dialect"] != "yue2" {
		t.Errorf("dialect %v, want yue2 for a YuE2 take", m["dialect"])
	}
	var spb float64
	var bars int
	if err := r.st.R.QueryRow(`SELECT bars, seconds_per_bar FROM scores WHERE track_id = ?`, r.tx("S")).Scan(&bars, &spb); err != nil {
		t.Fatal(err)
	}
	if want := float64(bars)*spb + 2; e.Output.Seconds < want-0.1 || e.Output.Seconds > want+0.1 {
		t.Errorf("output %v s, want %v from the scores row", e.Output.Seconds, want)
	}
	if *reads != 0 {
		t.Errorf("the estimate read %d score files", *reads)
	}
	res := r.create(human, q)
	if res.Status != 202 || len(res.View.Inputs) != 1 || res.View.Inputs[0].Role != "score_source" {
		t.Fatalf("create %+v", res)
	}
	var recipe string
	if err := r.st.R.QueryRow(`SELECT request_json FROM recipes WHERE id = (SELECT recipe_id FROM transforms WHERE id = ?)`, res.View.ID).Scan(&recipe); err != nil {
		t.Fatal(err)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(recipe), &spec); err != nil {
		t.Fatal(err)
	}
	var sha string
	_ = r.st.R.QueryRow(`SELECT abc_sha256 FROM scores WHERE track_id = ?`, r.tx("S")).Scan(&sha)
	if spec.Score == nil || spec.Score.SHA256 != sha || spec.Score.SourceTrackID == nil || *spec.Score.SourceTrackID != r.tx("S") || spec.Score.Dialect != "yue2" {
		t.Errorf("score ref %+v", spec.Score)
	}
	if spec.Synth == nil || spec.Synth.Name != "orchestra" || spec.Synth.Engine != "fluidsynth" || len(spec.Synth.SoundfontSHA) != 64 {
		t.Errorf("synth ref %+v", spec.Synth)
	}
	if *reads != 0 {
		t.Error("create read a score file")
	}
}

func TestScoreRender_EstimateReadsNoFile(t *testing.T) {
	t.Parallel()
	r, reads := scoreRig(t, true, "orchestra")
	for _, q := range []Request{
		scoreReq(abcParams("")),
		scoreReq(fmt.Sprintf(`{"score":{"from_track_id":%q}}`, r.tx("S"))),
	} {
		if _, err := r.svc.Estimate(t.Context(), human, q); err != nil {
			t.Fatal(err)
		}
	}
	if *reads != 0 {
		t.Errorf("the estimate read %d score files", *reads)
	}
	if got := r.svc.o.Score.Synth.(*nullSynth).calls.Load(); got != 0 {
		t.Errorf("the estimate ran the synth %d times", got)
	}
}

func TestScoreRender_SameSpecReuses(t *testing.T) {
	t.Parallel()
	r, _ := scoreRig(t, true, "orchestra")
	a := r.create(human, scoreReq(abcParams("")))
	b := r.create(agentA, scoreReq(abcParams(`,"tail_s":3,"gain":0.6`)))
	if !b.View.Reused || b.View.ID != a.View.ID {
		t.Errorf("same spec did not reuse: %+v %+v", a.View.ID, b.View.ID)
	}
}

func TestScoreRender_DialectAndBPM(t *testing.T) {
	t.Parallel()
	// a bpm override scales every tempo entry so the first reads the new tempo
	sc, err := loadScore([]byte("X:1\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|[Q:1/4=60]GABc|\n"), "standard", "none", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := scoreSeconds(sc) // 2 s + 4 s
	if base < 5.9 || base > 6.1 {
		t.Fatalf("base %v", base)
	}
	f := 240.0
	sc, _ = loadScore([]byte("X:1\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|[Q:1/4=60]GABc|\n"), "standard", "none", &f)
	if got := scoreSeconds(sc); got < 2.9 || got > 3.1 {
		t.Errorf("override length %v, want half of %v", got, base)
	}
	// the dialects differ in the accidental rule only
	a, _ := loadScore([]byte("X:1\nM:4/4\nL:1/4\nK:C\n^F f2 z|\n"), "standard", "none", nil)
	b, _ := loadScore([]byte("X:1\nM:4/4\nL:1/4\nK:C\n^F f2 z|\n"), "yue2", "none", nil)
	if a.Voices[0].Notes[1].Pitch != 77 || b.Voices[0].Notes[1].Pitch != 78 {
		t.Errorf("dialect pitches %d %d", a.Voices[0].Notes[1].Pitch, b.Voices[0].Notes[1].Pitch)
	}
}

func TestScoreRender_TitleFromTLine(t *testing.T) {
	t.Parallel()
	if got := titleScore(TitleIn{Resolved: json.RawMessage(`{"score":{"abc":"X:1\nT: Night Walk \nK:C\nC|"}}`)}); got != "Night Walk" {
		t.Errorf("title %q", got)
	}
	if got := titleScore(TitleIn{Resolved: json.RawMessage(`{"score":{"from_track_id":"x"}}`)}); got != "Score render" {
		t.Errorf("title %q", got)
	}
}

func TestExclusive_BusyAfterWait(t *testing.T) {
	t.Parallel()
	s := New(Options{})
	hold, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Exclusive(t.Context(), time.Second, func(context.Context) error { close(hold); <-release; return nil })
	}()
	<-hold
	start := time.Now()
	ran := false
	err := s.Exclusive(t.Context(), 60*time.Millisecond, func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, ErrBusy) || ran {
		t.Errorf("err %v ran %v, want ErrBusy and no run", err, ran)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("gave up after %v, before the wait", d)
	}
	close(release)
	wg.Wait()
	// the slot is free again, and fn's error passes through
	want := fmt.Errorf("boom")
	if err := s.Exclusive(t.Context(), time.Second, func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Errorf("err %v", err)
	}
}

func TestScoreRow_SecondsNullBPMOverride(t *testing.T) {
	r := scoreRow{bars: 4, meter: "4/4"}
	r.secPerBar.Valid, r.secPerBar.Float64 = true, 2 // timed, tempo unknown: the source ran at the default 120
	bpm := 240.0
	if got := r.seconds(&bpm); got < 3.99 || got > 4.01 {
		t.Errorf("override with a null bpm: %v s, want 4 (8 s halved)", got)
	}
	if got := r.seconds(nil); got != 8 {
		t.Errorf("no override: %v s, want 8", got)
	}
}

// TestScoreSupport_Accessor: the Desk reads the configured synth and soundfonts through the service, never a copy.
func TestScoreSupport_Accessor(t *testing.T) {
	t.Parallel()
	r, _ := scoreRig(t, true, "gm")
	sup := r.svc.ScoreSupport()
	if sup == nil || sup.Fonts == nil || len(sup.Fonts.Names()) != 1 || sup.Info.Name != "fluidsynth" {
		t.Errorf("ScoreSupport = %+v", sup)
	}
	bare := newRig(t)
	if bare.svc.ScoreSupport() != nil {
		t.Error("a service without T1b answers a ScoreSupport")
	}
}
