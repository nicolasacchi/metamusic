package features_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

func tempo(bpm float64) *features.TempoMeasure {
	return &features.TempoMeasure{BPM: bpm, Confidence: 0.8}
}

func mkey(c *features.Camelot) *features.KeyMeasure { return &features.KeyMeasure{Key: c} }

func cam(n int, l string) *features.Camelot { return &features.Camelot{Num: n, Letter: l} }

func eq[T comparable](p *T, want *T) bool {
	if p == nil || want == nil {
		return p == want
	}
	return *p == *want
}

type want struct {
	bpm       *int
	bpmBasis  features.Basis
	req, meas *int
	octave    bool
	energy    *int
	enBasis   features.Basis
	key       *features.Camelot
	keyBasis  features.Basis
	dur, exp  *float64
	detail    string // JSON subset checked as a substring-free map compare, "" to skip
}

func TestDeriveTake_Table(t *testing.T) {
	t.Parallel()
	chill := features.Facets{Mood: []string{"chill"}, GenreFamily: []string{"reggae"}, TempoBand: []string{"slow"}}
	energetic := features.Facets{Mood: []string{"energetic"}, GenreFamily: []string{"dance"}, TempoBand: []string{"fast"}}
	rows := []struct {
		id string
		in features.TakeInput
		w  want
	}{
		{"D01 declared only", features.TakeInput{DeclaredBPM: f64(76), RecipeKnown: true},
			want{bpm: ip(7600), bpmBasis: "declared", req: ip(7600)}},
		{"D02 declared + measured", features.TakeInput{DeclaredBPM: f64(76), MeasuredTempo: tempo(75.4)},
			want{bpm: ip(7540), bpmBasis: "measured", req: ip(7600), meas: ip(7540)}},
		{"D03 octave double", features.TakeInput{DeclaredBPM: f64(76), MeasuredTempo: tempo(152.2)},
			want{bpm: ip(7600), bpmBasis: "declared", req: ip(7600), meas: ip(15220), octave: true, detail: `{"octave":true,"tempo":{"confidence":0.8,"octave_ambiguous":false}}`}},
		{"D04 octave half", features.TakeInput{DeclaredBPM: f64(76), MeasuredTempo: tempo(38.0)},
			want{bpm: ip(7600), bpmBasis: "declared", req: ip(7600), meas: ip(3800), octave: true}},
		{"D05 style beats plan", features.TakeInput{StyleBPM: ip(120), PlanBPM: f64(118)},
			want{bpm: ip(12000), bpmBasis: "declared", req: ip(12000)}},
		{"D06 plan only", features.TakeInput{PlanBPM: f64(92.5)},
			want{bpm: ip(9250), bpmBasis: "plan", req: ip(9250)}},
		{"D07 reference take measured", features.TakeInput{MeasuredTempo: tempo(95.0)},
			want{bpm: ip(9500), bpmBasis: "measured", meas: ip(9500)}},
		{"D08 measured 15 discarded", features.TakeInput{DeclaredBPM: f64(90), MeasuredTempo: tempo(15.0)},
			want{bpm: ip(9000), bpmBasis: "declared", req: ip(9000), detail: `{"octave":false,"discarded":{"bpm":["measured"]},"tempo":{"confidence":0.8,"octave_ambiguous":false}}`}},
		{"D09 key same", features.TakeInput{MeasuredKey: mkey(cam(8, "A")), StyleKey: &features.KeyGuess{TonicPC: 9, Mode: "minor"}},
			want{key: cam(8, "A"), keyBasis: "measured", detail: `{"octave":false,"key":{"measured":"8A","declared":"8A","relation":"same"}}`}},
		{"D10 key relative to plan", features.TakeInput{MeasuredKey: mkey(cam(8, "A")), PlanKey: &features.KeyGuess{TonicPC: 0, Mode: "major"}},
			want{key: cam(8, "A"), keyBasis: "measured", detail: `{"octave":false,"key":{"measured":"8A","plan":"8B","relation":"relative"}}`}},
		{"D11 null measured key, style key", features.TakeInput{MeasuredKey: mkey(nil), StyleKey: &features.KeyGuess{TonicPC: 6, Mode: "major"}},
			want{key: cam(2, "B"), keyBasis: "declared"}},
		{"D12 plan key only", features.TakeInput{PlanKey: &features.KeyGuess{TonicPC: 2, Mode: "minor"}},
			want{key: cam(7, "A"), keyBasis: "plan"}},
		{"D13 measured energy", features.TakeInput{MeasuredEnergy: &features.EnergyMeasure{Energy: f64(0.5321)}, Facets: chill},
			want{energy: ip(5321), enBasis: "measured"}},
		{"D14 predicted energy", features.TakeInput{Facets: chill, RecipeKnown: true},
			want{energy: ip(3333), enBasis: "predicted"}},
		{"D15 reference take no energy", features.TakeInput{}, want{}},
		{"D16 silent energy falls back to prior", features.TakeInput{MeasuredEnergy: &features.EnergyMeasure{}, Facets: energetic},
			want{energy: ip(8500), enBasis: "predicted"}},
		{"D17 duration passthrough", features.TakeInput{DurationS: 215.4, ExpectedDurationS: f64(210)},
			want{dur: f64(215.4), exp: f64(210)}},
		{"D18 expected 0", features.TakeInput{DurationS: 100, ExpectedDurationS: f64(0)}, want{dur: f64(100)}},
		{"D19 declared column wins over style", features.TakeInput{DeclaredBPM: f64(80), StyleBPM: ip(90)},
			want{bpm: ip(8000), bpmBasis: "declared", req: ip(8000)}},
		{"D20 requested 300.5 discarded", features.TakeInput{DeclaredBPM: f64(300.5), MeasuredTempo: tempo(100)},
			want{bpm: ip(10000), bpmBasis: "measured", meas: ip(10000), detail: `{"octave":false,"discarded":{"bpm":["requested"]},"tempo":{"confidence":0.8,"octave_ambiguous":false}}`}},
		{"D21 measured 124 vs 120", features.TakeInput{DeclaredBPM: f64(120), MeasuredTempo: tempo(124.0)},
			want{bpm: ip(12400), bpmBasis: "measured", req: ip(12000), meas: ip(12400)}},
		{"D22 measured 60 vs 120 is exact half", features.TakeInput{DeclaredBPM: f64(120), MeasuredTempo: tempo(60.0)},
			want{bpm: ip(12000), bpmBasis: "declared", req: ip(12000), meas: ip(6000), octave: true}},
	}
	if len(rows) != 22 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, r := range rows {
		got := features.DeriveTake(r.in)
		w := r.w
		check := func(name string, ok bool) {
			if !ok {
				t.Errorf("%s: %s mismatch in %+v", r.id, name, got)
			}
		}
		check("bpm", eq(got.BPMx100, w.bpm) && got.BPMBasis == w.bpmBasis)
		check("requested", eq(got.BPMRequestedx100, w.req))
		check("measured", eq(got.BPMMeasuredx100, w.meas))
		check("octave", got.BPMOctave == w.octave)
		check("energy", eq(got.Energyx10000, w.energy) && got.EnergyBasis == w.enBasis)
		check("key", eq(got.Key, w.key) && got.KeyBasis == w.keyBasis)
		check("duration", eq(got.DurationS, w.dur) && eq(got.DurationExpectedS, w.exp))
		if w.detail != "" && string(got.Detail) != w.detail {
			t.Errorf("%s: detail = %s, want %s", r.id, got.Detail, w.detail)
		}
		if !json.Valid(got.Detail) {
			t.Errorf("%s: invalid detail %s", r.id, got.Detail)
		}
	}
}

func TestDeriveTake_ReferenceTakeNoRequestedValues(t *testing.T) {
	t.Parallel()
	got := features.DeriveTake(features.TakeInput{MeasuredTempo: tempo(100), MeasuredKey: mkey(cam(8, "B")), RecipeKnown: false})
	if got.BPMRequestedx100 != nil || got.EnergyBasis != "" || got.Energyx10000 != nil {
		t.Errorf("reference take derived non-measured values: %+v", got)
	}
}

func TestDeriveTake_DetailCoversOtherRelationAndNaN(t *testing.T) {
	t.Parallel()
	got := features.DeriveTake(features.TakeInput{
		MeasuredKey:   mkey(cam(8, "A")),
		StyleKey:      &features.KeyGuess{TonicPC: 6, Mode: "major"},
		MeasuredTempo: &features.TempoMeasure{BPM: 100, Confidence: nan()},
	})
	want := `{"octave":false,"key":{"measured":"8A","declared":"2B","relation":"other"},"tempo":{"confidence":0,"octave_ambiguous":false}}`
	if string(got.Detail) != want {
		t.Errorf("detail = %s, want %s", got.Detail, want)
	}
}

func TestDeriveTake_Deterministic(t *testing.T) {
	t.Parallel()
	in := features.TakeInput{DeclaredBPM: f64(76), MeasuredTempo: tempo(152.2), MeasuredKey: mkey(cam(8, "A")), PlanKey: &features.KeyGuess{TonicPC: 0, Mode: "major"}}
	a, b := features.DeriveTake(in), features.DeriveTake(in)
	if !reflect.DeepEqual(a, b) {
		t.Error("DeriveTake is not deterministic")
	}
}

func TestFeatures_OctaveEqualsSounds(t *testing.T) {
	t.Parallel()
	for _, req := range []float64{60, 76, 90, 100, 120, 128, 140, 180} {
		for _, f := range []float64{0.25, 0.4, 0.5, 0.53, 0.55, 0.9, 0.95, 1, 1.03, 1.05, 1.5, 1.9, 1.94, 2, 2.06, 2.13, 2.2, 3} {
			m := req * f
			if m < features.MinBPM || m > features.MaxBPM {
				continue
			}
			_, wantOct := sounds.CompareTempo(&req, &m)
			got := features.DeriveTake(features.TakeInput{DeclaredBPM: &req, MeasuredTempo: tempo(m)})
			if got.BPMOctave != wantOct {
				t.Errorf("requested %v measured %v: features octave %v, sounds %v", req, m, got.BPMOctave, wantOct)
			}
		}
	}
}

func TestDeriveTake_AnalysisRowSelection(t *testing.T) {
	t.Parallel()
	rows := []features.AnalysisRow{
		{Version: "mm-key/2 tool", MetricsJSON: "future"},
		{Version: "mm-key/10 tool", MetricsJSON: "prefix trap"},
		{Version: "mm-key/1 aaa", MetricsJSON: "old"},
		{Version: "mm-key/1 zzz", MetricsJSON: "new"},
		{Version: "mm-key/1", MetricsJSON: "bare"},
	}
	got, ok := features.SelectAnalysis(rows, "mm-key/1")
	if !ok || got.MetricsJSON != "new" {
		t.Errorf("selected %+v ok=%v, want the greatest mm-key/1 version", got, ok)
	}
	if _, ok := features.SelectAnalysis(rows[:2], "mm-key/1"); ok {
		t.Error("mm-key/2 and mm-key/10 must not satisfy mm-key/1")
	}
	if _, ok := features.SelectAnalysis(nil, "mm-key/1"); ok {
		t.Error("no rows, no selection")
	}
	// A loudness row is never offered to the energy selector: it is another analyzer's row, and its definition
	// text differs, so even if passed it is ignored.
	if _, ok := features.SelectAnalysis([]features.AnalysisRow{{Version: "mm-loudness/1 ffmpeg"}}, "mm-energy/1"); ok {
		t.Error("loudness must not re-derive energy")
	}
}
