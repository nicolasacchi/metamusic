package tierb_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// planABC has three sections of eight eighth notes each, 120 quarters per minute: a bar of 4/4 lasts 2 s, a section of two
// bars lasts 4 s.
const planABC = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|GABc|\n% chorus\nCDEF|GABc|\n% outro\nCDEF|GABc|]\n"

func tune(t *testing.T, src string) *abc.Tune {
	t.Helper()
	tn, err := abc.Parse([]byte(src), abc.Options{Dialect: abc.DialectYuE2})
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// sing places the eight scale notes of the plan in each 4 s window of a section, shifted by semis, as sung notes.
func sing(sections int, semis int, offset float64) []tierb.Note {
	return singScale([]int{60, 62, 64, 65, 67, 69, 71, 72}, sections, semis, offset)
}

func singScale(scale []int, sections int, semis int, offset float64) []tierb.Note {
	var out []tierb.Note
	for s := range sections {
		for i, p := range scale {
			out = append(out, tierb.Note{StartS: offset + float64(s)*4 + float64(i)*0.4, DurS: 0.3, Pitch: p + semis})
		}
	}
	return out
}

func approx(a *float64, want float64) bool { return a != nil && math.Abs(*a-want) < 1e-9 }

func measuredWindows() []tierb.Window {
	return []tierb.Window{{Idx: 0, Label: "verse", StartS: 10, EndS: 14}, {Idx: 1, Label: "chorus", StartS: 14, EndS: 18}, {Idx: 2, Label: "outro", StartS: 18, EndS: 22}}
}

func TestAdherence_IdenticalMelody(t *testing.T) {
	t.Parallel()
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, planABC), Sung: sing(3, 0, 10), Measured: measuredWindows()})
	if !approx(r.MelodyChroma, 1) || r.TransposeMode == nil || *r.TransposeMode != 0 || r.Basis != "coarse" || r.TimingSource != "measured" || len(r.Sections) != 3 {
		t.Fatalf("result = %+v", r)
	}
	for _, s := range r.Sections {
		if !approx(s.Chroma, 1) || !approx(s.ChromaBest, 1) || *s.Transpose != 0 || s.NotesPlan != 8 || s.NotesSung != 8 {
			t.Errorf("section = %+v", s)
		}
	}
	if r.TempoRatio != nil || r.KeyRelation != "unknown" {
		t.Errorf("without Tier A: tempo %v key %q", r.TempoRatio, r.KeyRelation)
	}
}

func TestAdherence_TransposedMelody(t *testing.T) {
	t.Parallel()
	// an arpeggio: a fifth up shares only one pitch class with the plan
	const arpeggio = "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCEGc|EGCE|\n% chorus\nCEGc|EGCE|\n% outro\nCEGc|EGCE|]\n"
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, arpeggio), Sung: singScale([]int{60, 64, 67, 72, 64, 67, 60, 64}, 3, 7, 10), Measured: measuredWindows()})
	if !approx(r.MelodyChroma, 1) || r.TransposeMode == nil || *r.TransposeMode != -5 {
		t.Fatalf("result = %+v (mode %v)", r, r.TransposeMode)
	}
	for _, s := range r.Sections {
		if s.Chroma == nil || *s.Chroma >= 0.5 || !approx(s.ChromaBest, 1) || *s.Transpose != -5 {
			t.Errorf("section = %+v chroma %v", s, s.Chroma)
		}
	}
}

func TestAdherence_TempoKeyRelation(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/8\nQ:1/4=100\nK:Am\n% verse\nABcd|efga|\n"
	bpm := 104.0
	cMajor := &tierb.KeyFact{Tonic: 0, Mode: "major"}
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, src), TempoBPM: &bpm, Key: cMajor})
	if !approx(r.TempoRatio, 1.04) || r.KeyRelation != "relative" {
		t.Errorf("tempo %v key %q, want 1.04 relative", r.TempoRatio, r.KeyRelation)
	}
	for _, c := range []struct {
		key  tierb.KeyFact
		want string
	}{{tierb.KeyFact{Tonic: 9, Mode: "minor"}, "exact"}, {tierb.KeyFact{Tonic: 2, Mode: "minor"}, "different"}, {tierb.KeyFact{Tonic: 9, Mode: "major"}, "different"}, {tierb.KeyFact{Tonic: 0, Mode: ""}, "unknown"}} {
		k := c.key
		if got := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, src), Key: &k}).KeyRelation; got != c.want {
			t.Errorf("A minor vs %+v = %q, want %q", c.key, got, c.want)
		}
	}
	// a dotted quarter unit is converted to quarter notes per minute: 3/8=60 is 90 quarters
	dot := "X:1\nM:6/8\nL:1/8\nQ:3/8=60\nK:C\n% a\nCDE|FGA|\n"
	bpm = 90
	if r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, dot), TempoBPM: &bpm}); !approx(r.TempoRatio, 1) {
		t.Errorf("tempo ratio = %v, want 1", r.TempoRatio)
	}
	// a major score against a minor Tier A key: relative the other way round
	maj := "X:1\nM:4/4\nL:1/8\nK:Eb\n% a\nEFGA|Bcde|\n"
	if got := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, maj), Key: &tierb.KeyFact{Tonic: 0, Mode: "minor"}}).KeyRelation; got != "relative" {
		t.Errorf("Eb major vs C minor = %q", got)
	}
	if got := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, "X:1\nM:4/4\nL:1/8\nK:G mixolydian\n% a\nGABc|dedc|\n"), Key: &tierb.KeyFact{Tonic: 7, Mode: "major"}}).KeyRelation; got != "different" {
		t.Errorf("G mixolydian vs G major = %q, want different", got)
	}
}

func TestAdherence_PlanTimingFallback(t *testing.T) {
	t.Parallel()
	// no measured sections: windows come from the plan (2 s per bar), the take sings from 0
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, planABC), Sung: sing(3, 0, 0)})
	if r.TimingSource != "plan_timing" || !approx(r.MelodyChroma, 1) || len(r.Sections) != 3 {
		t.Fatalf("result = %+v", r)
	}
	// an untimed score without measured sections cannot be placed
	untimed := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, "X:1\nM:4/4\nL:1/8\nK:C\n% a\nCDEF|GABc|\n"), Sung: sing(1, 0, 0)})
	if untimed.TimingSource != "none" || untimed.MelodyChroma != nil || len(untimed.Sections) != 1 || untimed.Sections[0].NotesSung != 0 {
		t.Errorf("untimed = %+v", untimed)
	}
}

func TestAdherence_TooFewNotesNull(t *testing.T) {
	t.Parallel()
	few := sing(1, 0, 10)[:3] // three notes in the verse, nothing elsewhere
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, planABC), Sung: few, Measured: measuredWindows()})
	if r.MelodyChroma != nil || r.TransposeMode != nil || len(r.Sections) != 3 {
		t.Fatalf("result = %+v", r)
	}
	if s := r.Sections[0]; s.Chroma != nil || s.ChromaBest != nil || s.Transpose != nil || s.NotesSung != 3 || s.NotesPlan != 8 {
		t.Errorf("verse = %+v", s)
	}
}

func TestAdherence_PartialAndModeTies(t *testing.T) {
	t.Parallel()
	// the chorus is sung a tone up, verse and outro are on plan: the mode is 0, two of three sections agree
	var sung []tierb.Note
	for i, semis := range []int{0, 2, 0} {
		sung = append(sung, sing(1, semis, 10+float64(i)*4)...)
	}
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, planABC), Sung: sung, Measured: measuredWindows()})
	if r.TransposeMode == nil || *r.TransposeMode != 0 || *r.Sections[1].Transpose != 2 {
		t.Errorf("mode %v, chorus transpose %v", r.TransposeMode, r.Sections[1].Transpose)
	}
	// fewer measured windows than score sections: the unmatched section has no sung notes
	r = tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, planABC), Sung: sung, Measured: measuredWindows()[:2]})
	if r.Sections[2].NotesSung != 0 || r.Sections[2].Chroma != nil || len(r.Sections) != 3 {
		t.Errorf("third section = %+v", r.Sections[2])
	}
}

func TestAdherence_NoSectionsOneWholeSection(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nCDEF|GABc|\n"
	r := tierb.Adherence(tierb.AdherenceInput{Tune: tune(t, src), Sung: sing(1, 0, 10), Measured: measuredWindows()})
	if len(r.Sections) != 1 || r.Sections[0].NotesPlan != 8 || !approx(r.MelodyChroma, 1) {
		t.Errorf("result = %+v", r)
	}
	if r := tierb.Adherence(tierb.AdherenceInput{}); r.Basis != "coarse" || len(r.Sections) != 0 || r.MelodyChroma != nil {
		t.Errorf("nil tune = %+v", r)
	}
}
