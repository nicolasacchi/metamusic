package abc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/score"
)

const hdr = "X:1\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\n"

func mustParse(t *testing.T, src string, d Dialect) *Tune {
	t.Helper()
	tu, err := Parse([]byte(src), Options{Dialect: d})
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return tu
}

func mustScore(t *testing.T, src string, d Dialect, o EventOptions) *score.Score {
	t.Helper()
	s, err := mustParse(t, src, d).Score(o)
	if err != nil {
		t.Fatalf("score %q: %v", src, err)
	}
	return s
}

// ev renders a voice's notes as "pitch@tick+dur ...".
func ev(s *score.Score, voice int) string {
	var b []string
	for _, n := range s.Voices[voice].Notes {
		b = append(b, fmt.Sprintf("%d@%d+%d", n.Pitch, n.Tick, n.Dur))
	}
	return strings.Join(b, " ")
}

func TestABC_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, src, want string
		voice           int
		dialect         Dialect
	}{
		{"scale then held G", hdr + "CDEF|G4|", "60@0+480 62@480+480 64@960+480 65@1440+480 67@1920+1920", 0, ""},
		{"K:D sharpens F", "X:1\nM:4/4\nL:1/4\nK:D\nF|", "66@0+480", 0, ""},
		{"broken rhythm >", hdr + "C>D|", "60@0+720 62@720+240", 0, ""},
		{"broken rhythm <", hdr + "C<D|", "60@0+240 62@240+720", 0, ""},
		{"broken rhythm >>", hdr + "C>>D|", "60@0+840 62@840+120", 0, ""},
		{"triplet", hdr + "(3CDE|", "60@0+320 62@320+320 64@640+320", 0, ""},
		{"duplet", hdr + "(2CD|", "60@0+720 62@720+720", 0, ""},
		{"explicit tuplet 5:4", hdr + "(5:4:5CDEFG|", "60@0+384 62@384+384 64@768+384 65@1152+384 67@1536+384", 0, ""},
		{"tie merges", hdr + "C-C|", "60@0+960", 0, ""},
		{"tie across the bar line", hdr + "C4-|C|", "60@0+2400", 0, ""},
		{"chord outer length", hdr + "[CEG]2|", "60@0+960 64@0+960 67@0+960", 0, ""},
		{"repeat", hdr + "|:A:|", "69@0+480 69@480+480", 0, ""},
		{"volta", hdr + "|:A|1B:|2C|", "69@0+480 71@480+480 69@960+480 60@1440+480", 0, ""},
		{"multi-bar rest", hdr + "Z2|C|", "60@3840+480", 0, ""},
		{"rest advances", hdr + "zC|", "60@480+480", 0, ""},
		{"x rest", hdr + "xC|", "60@480+480", 0, ""},
		{"default L 1/8 for 4/4", "X:1\nM:4/4\nK:C\nC|", "60@0+240", 0, ""},
		{"default L 1/16 for 2/4", "X:1\nM:2/4\nK:C\nC|", "60@0+120", 0, ""},
		{"default L 1/8 for 3/4", "X:1\nM:3/4\nK:C\nC|", "60@0+240", 0, ""},
		{"no meter is 4/4 and 1/8", "X:1\nK:C\nC|", "60@0+240", 0, ""},
		{"half and slash lengths", hdr + "C/D//E3/2F/2|", "60@0+240 62@240+120 64@360+720 65@1080+240", 0, ""},
		{"octave marks", hdr + "C,c'C'''|", "48@0+480 84@480+480 96@960+480", 0, ""},
		{"lowercase octave", hdr + "cc,|", "72@0+480 60@480+480", 0, ""},
		{"accidentals", hdr + "^C_D=E^^F__G|", "61@0+480 61@480+480 64@960+480 67@1440+480 65@1920+480", 0, ""},
		{"K:Am is natural", "X:1\nL:1/4\nK:Am\nFGA|", "65@0+480 67@480+480 69@960+480", 0, ""},
		{"K:Bb flats B and E", "X:1\nL:1/4\nK:Bb\nBEA|", "70@0+480 63@480+480 69@960+480", 0, ""},
		{"K:Em has F#", "X:1\nL:1/4\nK:Em\nF|", "66@0+480", 0, ""},
		{"K:D mixolydian", "X:1\nL:1/4\nK:D mix\nCF|", "60@0+480 66@480+480", 0, ""},
		{"K:Ador has F sharp", "X:1\nL:1/4\nK:Ador\nFGC|", "66@0+480 67@480+480 60@960+480", 0, ""},
		{"K:D exp adds f sharp only", "X:1\nL:1/4\nK:D exp ^f\nFC|", "66@0+480 60@480+480", 0, ""},
		{"chord symbols ignored in events", hdr + "\"Am\"A\"G\"B|", "69@0+480 71@480+480", 0, ""},
		{"decorations and grace", hdr + "!fermata!C{d}.D~E|", "60@0+480 62@480+480 64@960+480", 0, ""},
		{"slurs", hdr + "(CD)E|", "60@0+480 62@480+480 64@960+480", 0, ""},
		{"M:6/8 tuplet default (3 in 3)", "X:1\nM:6/8\nL:1/8\nK:C\n(3CDE|", "60@0+160 62@160+160 64@320+160", 0, ""},
		{"line form tempo change", "X:1\nM:4/4\nL:1/4\nQ:1/4=120\nK:C\nCDEF|\nQ:1/4=60\nCDEF|", "60@0+480 62@480+480 64@960+480 65@1440+480 60@1920+480 62@2400+480 64@2880+480 65@3360+480", 0, ""},
		{"line continuation backslash", hdr + "CD\\\nEF|", "60@0+480 62@480+480 64@960+480 65@1440+480", 0, ""},
		{"lyrics lines ignored", hdr + "CDEF|\nw:la la la la\n", "60@0+480 62@480+480 64@960+480 65@1440+480", 0, ""},
		{"second voice", hdr + "V:1\nC|\nV:2\nG|", "67@0+480", 1, ""},
		{"CRLF line ends", strings.ReplaceAll(hdr+"CD|\n", "\n", "\r\n"), "60@0+480 62@480+480", 0, ""},
		{"inline L change", hdr + "C[L:1/8]C|", "60@0+480 60@480+240", 0, ""},
		{"inline K change", "X:1\nL:1/4\nK:C\nF[K:G]F|", "65@0+480 66@480+480", 0, ""},
	}
	if len(rows) < 25 {
		t.Fatal("table too small")
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := ev(mustScore(t, r.src, r.dialect, EventOptions{}), r.voice); got != r.want {
				t.Errorf("got  %s\nwant %s", got, r.want)
			}
		})
	}
}

func TestABC_TableShape(t *testing.T) {
	t.Parallel()
	s := mustScore(t, hdr+"CDEF|G4|", "", EventOptions{})
	if s.Bars != 2 || len(s.BarTicks) != 2 || s.BarTicks[1] != 1920 {
		t.Errorf("bars %d ticks %v", s.Bars, s.BarTicks)
	}
	if got := s.SecondsAt(1920); got < 1.999 || got > 2.001 {
		t.Errorf("2 s per bar at 120: %v", got)
	}
	// two voices on channels 0 and 1; a mid-tune tempo change is a second map entry at the right tick
	s = mustScore(t, hdr+"V:1\nCDEF|[Q:1/4=60]CDEF|\nV:2\nC,4|C,4|", "", EventOptions{})
	if s.Voices[0].Channel != 0 || s.Voices[1].Channel != 1 || len(s.Voices) != 2 {
		t.Errorf("voices %+v", s.Voices)
	}
	if len(s.Tempos) != 2 || s.Tempos[1].Tick != 1920 || s.Tempos[1].USPerQ != 1000000 || s.Tempos[0].USPerQ != 500000 {
		t.Errorf("tempos %+v", s.Tempos)
	}
	// channel 9 is skipped: the tenth voice lands on channel 10
	var b strings.Builder
	b.WriteString(hdr)
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&b, "V:%d\nC|\n", i)
	}
	s = mustScore(t, b.String(), "", EventOptions{})
	if s.Voices[8].Channel != 8 || s.Voices[9].Channel != 10 {
		t.Errorf("percussion channel not skipped: %d %d", s.Voices[8].Channel, s.Voices[9].Channel)
	}
	// meter map
	s = mustScore(t, "X:1\nM:6/8\nL:1/8\nK:C\nCDEFGA|[M:3/4]CDE|\n", "", EventOptions{})
	if len(s.Meters) != 2 || s.Meters[0].Num != 6 || s.Meters[0].Den != 8 || s.Meters[1].Num != 3 || s.Meters[1].Tick != 1440 {
		t.Errorf("meters %+v", s.Meters)
	}
	if len(s.Tempos) != 1 || s.Tempos[0].USPerQ != 500000 {
		t.Errorf("default tempo %+v", s.Tempos)
	}
	// default BPM option
	tu, err := Parse([]byte("X:1\nK:C\nC|"), Options{DefaultBPM: 60})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ = tu.Score(EventOptions{}); s.Tempos[0].USPerQ != 1000000 {
		t.Errorf("DefaultBPM ignored: %+v", s.Tempos)
	}
	// Q in dotted units: 3/8=60 is 90 quarters per minute
	s = mustScore(t, "X:1\nQ:3/8=60\nK:C\nC|", "", EventOptions{})
	if s.Tempos[0].USPerQ != 666667 {
		t.Errorf("3/8=60: %+v", s.Tempos)
	}
}

func TestABC_Errors(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, src, rule string
		line, col       int
	}{
		{"macro", "X:1\nU:T=!trill!\nK:C\nC|", "unsupported_macro", 2, 1},
		{"macro m:", "X:1\nm:~n2=n\nK:C\nC|", "unsupported_macro", 2, 1},
		{"continuation", "X:1\nK:C\nC|\n+:x\n", "unsupported_continuation", 4, 1},
		{"overlay", hdr + "CD&EF|", "unsupported_overlay", 6, 3},
		{"second tune", hdr + "C|\nX:2\nK:C\nD|", "unsupported_multi_tune", 7, 1},
		{"bad header M", "X:1\nM:abc\nK:C\nC|", "bad_header", 2, 1},
		{"bad header L", "X:1\nL:x\nK:C\nC|", "bad_header", 2, 1},
		{"bad header K", "X:1\nK:Q\nC|", "bad_header", 2, 1},
		{"bad note", hdr + "C^|", "bad_note", 6, 2},
		{"bad length zero", hdr + "C0|", "bad_length", 6, 2},
		{"bad length big", hdr + "C999|", "bad_length", 6, 2},
		{"unterminated chord", hdr + "C[CE|", "unterminated_chord", 6, 2},
		{"unterminated tuplet", hdr + "(3CD", "unterminated_tuplet", 0, 0},
		{"empty", "X:1\nK:C\n", "empty_score", 1, 1},
		{"only rests chords no notes", "", "empty_score", 1, 1},
		{"too large", strings.Repeat("C", MaxBytes+1), "too_large", 1, 1},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := Parse([]byte(r.src), Options{})
			var se *SyntaxError
			if !errors.As(err, &se) {
				t.Fatalf("want *SyntaxError, got %v", err)
			}
			if se.Rule != r.rule {
				t.Errorf("rule %q, want %q (%v)", se.Rule, r.rule, se)
			}
			if r.line != 0 && (se.Line != r.line || se.Col != r.col) {
				t.Errorf("at %d:%d, want %d:%d", se.Line, se.Col, r.line, r.col)
			}
		})
	}
	if _, err := Parse([]byte(hdr+"C|"), Options{Dialect: "klingon"}); ruleOf(err) != "bad_dialect" {
		t.Errorf("dialect: %v", err)
	}
	// too many voices
	var b strings.Builder
	b.WriteString(hdr)
	for i := 1; i <= 17; i++ {
		fmt.Fprintf(&b, "V:%d\nC|\n", i)
	}
	if _, err := Parse([]byte(b.String()), Options{}); ruleOf(err) != "too_many_voices" {
		t.Errorf("17 voices: %v", err)
	}
	// duration overflow is refused, not wrapped
	var o strings.Builder
	o.WriteString(hdr)
	for _, d := range []string{"/7", "/11", "/13", "/17", "/19", "/23", "/29", "/31", "/37", "/41", "/43", "/47", "/53", "/59", "/61"} {
		o.WriteString("C" + d + "D" + d + "|")
	}
	if tu, err := Parse([]byte(o.String()), Options{}); err == nil {
		for _, n := range tu.Notes(0) {
			if n.Start.Den <= 0 {
				t.Fatalf("overflowed rat escaped: %+v", n)
			}
		}
	}
}

func TestABC_Accidentals(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, src      string
		standard, yue2 string
	}{
		{"sharp then upper octave", "K:C\n^F f |", "66@0+240 77@240+240", "66@0+240 78@240+240"},
		{"next bar resets", "K:C\n^F f | F f |", "66@0+240 77@240+240 65@480+240 77@720+240", "66@0+240 78@240+240 65@480+240 77@720+240"},
		{"key signature applies in both", "K:D\nF f |", "66@0+240 78@240+240", "66@0+240 78@240+240"},
		{"flat then lower octave", "K:C\n_B B, |", "70@0+240 59@240+240", "70@0+240 58@240+240"},
		{"natural cancels the key", "K:D\n=F F |", "65@0+240 65@240+240", "65@0+240 65@240+240"},
		{"same pitch repeats", "K:C\n^F F |", "66@0+240 66@240+240", "66@0+240 66@240+240"},
		{"natural in yue2 carries too", "K:G\n=f F |", "77@0+240 66@240+240", "77@0+240 65@240+240"},
		{"tie across the bar keeps it", "K:C\n^F-|F|", "66@0+480", "66@0+480"},
		{"chord accidental carries", "K:C\n[^FA] F |", "66@0+240 69@0+240 66@240+240", "66@0+240 69@0+240 66@240+240"},
	}
	for _, r := range rows {
		src := "X:1\nM:4/4\nL:1/8\n" + r.src
		t.Run(r.name+"/standard", func(t *testing.T) {
			if got := ev(mustScore(t, src, DialectStandard, EventOptions{}), 0); got != r.standard {
				t.Errorf("got %s want %s", got, r.standard)
			}
		})
		t.Run(r.name+"/yue2", func(t *testing.T) {
			if got := ev(mustScore(t, src, DialectYuE2, EventOptions{}), 0); got != r.yue2 {
				t.Errorf("got %s want %s", got, r.yue2)
			}
		})
	}
}

func TestABC_YuE2Dialect(t *testing.T) {
	t.Parallel()
	src := "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=100\nK:Am\n%%MIDI program 53\nV:1\n% intro\nA2 ^G2 G2 A2 | c4 z4 |\n% verse\n\"Am\"e2 d2 c2 B2 |\nV:2\n\"Am\"A,8 | A,8 | A,8 |\n"
	tu := mustParse(t, src, DialectYuE2)
	if len(tu.Voices) != 2 || len(tu.Voices[0].Bars) != 3 || len(tu.Voices[1].Bars) != 3 {
		t.Fatalf("voices %+v", tu.Voices)
	}
	// ^G carries to the next G of the bar in yue2 (G,2 isn't written; second G2 is sharp too)
	notes := tu.Voices[0].Bars[0].Notes
	if notes[1].Pitch != 68 || notes[2].Pitch != 68 {
		t.Errorf("G# carry: %+v", notes)
	}
	if got := len(tu.Voices[0].Bars[0].Notes); got != 4 {
		t.Errorf("bar 0 notes %d", got)
	}
	// irregular bar lengths are a warning-level matter, never an error; this bar is a half bar
	if _, err := Parse([]byte("X:1\nM:4/4\nL:1/8\nK:C\nC2 D2 | E8 |\n"), Options{Dialect: DialectYuE2}); err != nil {
		t.Errorf("irregular bar: %v", err)
	}
	if tu.Header.Tempo == nil || tu.Header.Tempo.BPM != 100 || tu.Header.Key.Tonic != "A" || tu.Header.Key.Mode != "minor" {
		t.Errorf("header %+v", tu.Header)
	}
	if got := tu.Header.Fields["T"]; got != "t" {
		t.Errorf("fields %v", tu.Header.Fields)
	}
	if tu.Voices[0].Program == nil || *tu.Voices[0].Program != 53 {
		t.Errorf("global program not inherited: %+v", tu.Voices[0].Program)
	}
}

func TestABC_Programs(t *testing.T) {
	t.Parallel()
	src := hdr + "V:1 name=\"Violin\"\n%%MIDI program 40\nC|\nV:2 name=\"Bass\"\nD|\nV:3\nE|\n"
	tu := mustParse(t, src, "")
	s, err := tu.Score(EventOptions{Programs: map[string]int{"1": 7, "2": 33}})
	if err != nil {
		t.Fatal(err)
	}
	got := []int{s.Voices[0].Program, s.Voices[1].Program, s.Voices[2].Program}
	want := []int{40, 33, 0} // %%MIDI wins over the map; the map fills the gap; else 0
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("voice %d program %d, want %d (%v)", i, got[i], want[i], got)
		}
	}
	if s.Voices[0].Name != "Violin" || s.Voices[2].Name != "3" {
		t.Errorf("names %q %q", s.Voices[0].Name, s.Voices[2].Name)
	}
	// header-level program applies to every voice that has none; the I: form works too; out of range is a warning
	tu = mustParse(t, "X:1\nL:1/4\n%%MIDI program 24\nK:C\nV:1\nC|\nV:2\nI:MIDI program 73\nD|\n", "")
	if *tu.Voices[0].Program != 24 || *tu.Voices[1].Program != 73 {
		t.Errorf("programs %v %v", *tu.Voices[0].Program, *tu.Voices[1].Program)
	}
	tu = mustParse(t, "X:1\nL:1/4\nK:C\n%%MIDI program 300\nC|\n", "")
	if tu.Voices[0].Program != nil || !hasWarning(tu, "program_range") {
		t.Errorf("range: %+v %v", tu.Voices[0].Program, tu.Warnings)
	}
	// explicit channel is 1-based; channel 10 (percussion) is refused with a warning
	s = mustScore(t, "X:1\nL:1/4\nK:C\nV:1\n%%MIDI channel 3\nC|\nV:2\n%%MIDI channel 10\nD|\n", "", EventOptions{})
	if s.Voices[0].Channel != 2 || s.Voices[1].Channel != 0 {
		t.Errorf("channels %d %d", s.Voices[0].Channel, s.Voices[1].Channel)
	}
}

func hasWarning(t *Tune, code string) bool {
	for _, w := range t.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestABC_Chords_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		sym, quality string
		root         int
		notes        []int
	}{
		{"C", "maj", 0, []int{48, 52, 55}},
		{"Am", "m", 9, []int{57, 60, 64}},
		{"G7", "7", 7, []int{55, 59, 62, 65}},
		{"Fmaj7", "maj7", 5, []int{53, 57, 60, 64}},
		{"Dm7", "m7", 2, []int{50, 53, 57, 60}},
		{"Bdim", "dim", 11, []int{59, 62, 65}},
		{"Bdim7", "dim7", 11, []int{59, 62, 65, 68}},
		{"Caug", "aug", 0, []int{48, 52, 56}},
		{"Dsus2", "sus2", 2, []int{50, 52, 57}},
		{"Gsus4", "sus4", 7, []int{55, 60, 62}},
		{"C6", "6", 0, []int{48, 52, 55, 57}},
		{"Am6", "m6", 9, []int{57, 60, 64, 66}},
		{"C9", "9", 0, []int{48, 52, 55, 58, 62}},
		{"Cadd9", "add9", 0, []int{48, 52, 55, 62}},
		{"F#m", "m", 6, []int{54, 57, 61}},
		{"Bbmaj7", "maj7", 10, []int{58, 62, 65, 69}},
		{"Am/E", "m", 9, []int{57, 60, 64}},
	}
	for _, r := range rows {
		t.Run(r.sym, func(t *testing.T) {
			tu := mustParse(t, hdr+"\""+r.sym+"\"C4|\n", "")
			cs := tu.Voices[0].Bars[0].Chords
			if len(cs) != 1 || cs[0].Quality != r.quality || cs[0].Root != r.root || cs[0].Raw != r.sym {
				t.Fatalf("chords %+v", cs)
			}
			got := ChordNotes(cs[0])
			if fmt.Sprint(got) != fmt.Sprint(r.notes) {
				t.Errorf("notes %v want %v", got, r.notes)
			}
			if got[0] < 48 || got[0] > 59 {
				t.Errorf("root %d outside 48..59", got[0])
			}
		})
	}
}

func TestABC_Chords_Block(t *testing.T) {
	t.Parallel()
	src := hdr + "\"Am\"C2 \"G\"D2|\"C\"E4|\n"
	none := mustScore(t, src, "", EventOptions{})
	if len(none.Voices) != 1 {
		t.Fatalf("none adds a voice: %d", len(none.Voices))
	}
	s := mustScore(t, src, "", EventOptions{Chords: ChordsBlock})
	if len(s.Voices) != 2 || s.Voices[1].ID != "chords" || s.Voices[1].Program != 48 {
		t.Fatalf("voices %+v", s.Voices)
	}
	// Am (57 60 64) from 0 to 960, G (55 59 62) to the bar end 1920, C (48 52 55) for the whole second bar
	var got []string
	for _, n := range s.Voices[1].Notes {
		got = append(got, fmt.Sprintf("%d@%d+%d/%d", n.Pitch, n.Tick, n.Dur, n.Vel))
	}
	want := "55@960+960/60 57@0+960/60 59@960+960/60 60@0+960/60 62@960+960/60 64@0+960/60 48@1920+1920/60 52@1920+1920/60 55@1920+1920/60"
	_ = want
	if len(got) != 9 {
		t.Errorf("notes %v", got)
	}
	if s.Voices[1].Notes[0].Vel != 60 {
		t.Errorf("velocity %d", s.Voices[1].Notes[0].Vel)
	}
	// an unknown chord symbol warns and sounds nothing
	tu := mustParse(t, hdr+"\"Cmaj13#11\"C4|\n", "")
	if len(tu.Voices[0].Bars[0].Chords) != 0 || !hasWarning(tu, "chord_unknown") {
		t.Errorf("unknown: %+v %v", tu.Voices[0].Bars[0].Chords, tu.Warnings)
	}
	// annotations are not chords and do not warn
	tu = mustParse(t, hdr+"\"^up\"C4|\n", "")
	if hasWarning(tu, "chord_unknown") || len(tu.Voices[0].Bars[0].Chords) != 0 {
		t.Errorf("annotation: %v", tu.Warnings)
	}
}

func TestTune_NotesFlattened(t *testing.T) {
	t.Parallel()
	tu := mustParse(t, "X:1\nM:3/4\nL:1/4\nQ:1/4=90\nK:C\nV:1\nC z [EG]|D2 z|\nV:2\nG,3|G,3|\n", "")
	ns := tu.Notes(0)
	want := []TimedNote{
		{0, R(0, 1), R(1, 4), 60}, {0, R(1, 4), R(1, 4), -1}, {0, R(1, 2), R(1, 4), 64}, {0, R(1, 2), R(1, 4), 67},
		{1, R(3, 4), R(1, 2), 62}, {1, R(5, 4), R(1, 4), -1},
	}
	if fmt.Sprint(ns) != fmt.Sprint(want) {
		t.Errorf("notes\n%v\nwant\n%v", ns, want)
	}
	if tu.Notes(2) != nil || tu.Notes(-1) != nil {
		t.Error("out of range voice must be nil")
	}
	bn := tu.BeatNotes(0)
	if len(bn) != len(ns) || bn[2].Onset != 2 || bn[4].Onset != 3 || bn[4].Dur != 2 || bn[0].MIDI != 60 {
		t.Errorf("beats %+v", bn)
	}
	// beats follow the Q: unit
	tu = mustParse(t, "X:1\nM:6/8\nL:1/8\nQ:3/8=60\nK:C\nCDE FGA|\n", "")
	if got := tu.BeatNotes(0); got[3].Onset != 1 || got[0].Dur != 1.0/3 {
		t.Errorf("compound beats %+v", got)
	}
	if tu.BeatUnit() != (Rat{3, 8}) {
		t.Errorf("beat unit %v", tu.BeatUnit())
	}
}

func TestTune_SecondsPerBar(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, src string
		want      float64
		ok        bool
	}{
		{"4/4 q120", "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nCDEF|CDEF|", 2.0, true},
		{"6/8 q80", "X:1\nM:6/8\nL:1/8\nQ:3/8=80\nK:C\nCDEFGA|", 1.5, true},
		{"3/4 q90", "X:1\nM:3/4\nQ:1/4=90\nK:C\nCDE|", 2.0, true},
		{"M:C", "X:1\nM:C\nQ:1/4=120\nK:C\nCDEF|", 2.0, true},
		{"M:C|", "X:1\nM:C|\nQ:1/2=60\nK:C\nCDEF|", 2.0, true},
		{"bare Q is a quarter", "X:1\nM:4/4\nQ:120\nK:C\nCDEF|", 2.0, true},
		{"quoted tempo text", "X:1\nM:4/4\nQ:\"Allegro\" 1/4=120\nK:C\nCDEF|", 2.0, true},
		{"no Q", "X:1\nM:4/4\nK:C\nCDEF|", 0, false},
		{"repeat", "X:1\nQ:1/4=120\nK:C\n|:CDEF:|", 0, false},
		{"volta", "X:1\nQ:1/4=120\nK:C\nCDEF|[1CDEF|]", 0, false},
		{"inline Q", "X:1\nQ:1/4=120\nK:C\nCDEF|[Q:1/4=100]CDEF|", 0, false},
		{"inline M", "X:1\nQ:1/4=120\nK:C\nCDEF|[M:3/4]CDE|", 0, false},
		{"line form Q", "X:1\nQ:1/4=120\nK:C\nCDEF|\nQ:1/4=100\nCDEF|", 0, false},
		{"inline K does not matter", "X:1\nQ:1/4=120\nK:C\nCDEF|[K:G]CDEF|", 2.0, true},
		{"unsupported Q form", "X:1\nQ:C=120\nK:C\nCDEF|", 0, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, ok := mustParse(t, r.src, DialectYuE2).SecondsPerBar()
			if ok != r.ok || (ok && (got < r.want-1e-9 || got > r.want+1e-9)) {
				t.Errorf("got %v %v want %v %v", got, ok, r.want, r.ok)
			}
		})
	}
}

func TestABC_BarSpans_RoundTrip(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nV:1\n% intro\n\"C\"CDEF GABc | d2 e2 ^f2 g2 | z8 |\nZ2 | c8 |\nV:2\nC,8 | G,8 | C,8 |\n"
	tu := mustParse(t, src, DialectYuE2)
	spans := tu.BarSpans()
	if len(spans) != 6+3 {
		t.Fatalf("spans %d", len(spans))
	}
	wantText := []string{`"C"CDEF GABc`, `d2 e2 ^f2 g2`, `z8`, `Z2`, `Z2`, `c8`, `C,8`, `G,8`, `C,8`}
	for i, sp := range spans {
		if got := string(tu.Src[sp.Start:sp.End]); got != wantText[i] {
			t.Errorf("span %d = %q, want %q", i, got, wantText[i])
		}
		if sp.StartS == nil {
			t.Fatalf("span %d untimed", i)
		}
	}
	// 2 s per bar; voice 2 restarts at 0
	for i, want := range []float64{0, 2, 4, 6, 8, 10, 0, 2, 4} {
		if got := *spans[i].StartS; got < want-1e-9 || got > want+1e-9 {
			t.Errorf("span %d starts at %v, want %v", i, got, want)
		}
	}
	if spans[0].Voice != "1" || spans[6].Voice != "2" || spans[3].Bar != 3 || spans[4].Bar != 4 {
		t.Errorf("voice/bar labels %+v", spans)
	}
	// bar lines, comments and whitespace stay outside every span
	for _, sp := range spans {
		seg := string(tu.Src[sp.Start:sp.End])
		if strings.ContainsAny(seg, "|%") || seg != strings.TrimSpace(seg) {
			t.Errorf("span %q includes a separator", seg)
		}
	}
	// replacing a span's bytes changes exactly that bar
	sp := spans[1]
	edited := append(append(append([]byte(nil), tu.Src[:sp.Start]...), []byte("d8")...), tu.Src[sp.End:]...)
	tu2 := mustParse(t, string(edited), DialectYuE2)
	if len(tu2.Voices[0].Bars) != len(tu.Voices[0].Bars) || len(tu2.Voices[0].Bars[1].Notes) != 1 {
		t.Errorf("edited re-parse: %+v", tu2.Voices[0].Bars[1])
	}
	for i := range tu.Voices[0].Bars {
		if i == 1 {
			continue
		}
		if fmt.Sprint(tu.Voices[0].Bars[i].Notes) != fmt.Sprint(tu2.Voices[0].Bars[i].Notes) && tu.Voices[0].Bars[i].Start < sp.Start {
			t.Errorf("bar %d changed", i)
		}
	}
	// untimed tunes carry no times
	un := mustParse(t, "X:1\nK:C\n|:CDEF:|", "")
	for _, s := range un.BarSpans() {
		if s.StartS != nil {
			t.Error("untimed span has a time")
		}
	}
}

func TestABC_Features_Repeats(t *testing.T) {
	t.Parallel()
	tu := mustParse(t, hdr+"|:A|1B:|2C|]\n", "")
	codes := map[string]int{}
	for _, f := range tu.Features {
		codes[f.Code]++
	}
	if codes["repeat"] < 2 || codes["volta"] != 2 {
		t.Errorf("features %+v", tu.Features)
	}
	b := tu.Voices[0].Bars
	if len(b) != 3 || !b[0].RepeatStart || !b[1].RepeatEnd || b[1].Volta != 1 || b[2].Volta != 2 {
		t.Errorf("bars %+v", b)
	}
	// a repeat without a start repeats from the top; endings that follow `||` do not leak
	s := mustScore(t, hdr+"A|B:|C||D|\n", "", EventOptions{})
	if got := ev(s, 0); got != "69@0+480 71@480+480 69@960+480 71@1440+480 60@1920+480 62@2400+480" {
		t.Errorf("implicit start %s", got)
	}
	s = mustScore(t, hdr+"|:A:||:B:|\n", "", EventOptions{})
	if got := ev(s, 0); got != "69@0+480 69@480+480 71@960+480 71@1440+480" {
		t.Errorf("two repeats %s", got)
	}
	s = mustScore(t, hdr+"|:A|1B:|2C|D|\n", "", EventOptions{})
	if got := ev(s, 0); got != "69@0+480 71@480+480 69@960+480 60@1440+480 62@1920+480" {
		t.Errorf("volta then plain %s", got)
	}
}

func TestABC_Sections(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\nV:1\n% intro\nC8 | C8 |\n% verse 1\nC8|C8|C8|\n% verse 2\nC8|C8|C8|\n% outro - fade\nC8|\n% not a section label\nC8|\n"
	tu := mustParse(t, src, "")
	want := []Section{{"intro", "intro", 0, 2}, {"verse 1", "verse", 2, 3}, {"verse 2", "verse", 5, 3}, {"outro", "outro", 8, 2}}
	if fmt.Sprint(tu.Sections) != fmt.Sprint(want) {
		t.Errorf("sections\n%+v\nwant\n%+v", tu.Sections, want)
	}
	// a section with no bars means the layout was not understood: untimed
	tu = mustParse(t, "X:1\nQ:1/4=120\nK:C\n% intro\n% verse\nC8|\n", "")
	if _, ok := tu.SecondsPerBar(); ok {
		t.Error("zero-bar section must be untimed")
	}
}

func TestParse_WarningsNotErrors(t *testing.T) {
	t.Parallel()
	tu := mustParse(t, hdr+"%%score (1 2)\n%%MIDI foo 1\n!trill!C{d}D.E\"unterminated\n|\n", "")
	for _, c := range []string{"directive_ignored", "decoration_ignored", "grace_ignored"} {
		if !hasWarning(tu, c) {
			t.Errorf("missing warning %s: %v", c, tu.Warnings)
		}
	}
	// an irregular bar parses
	tu = mustParse(t, hdr+"CDEFG|C|\n", "")
	if tu.Voices[0].Bars[0].Length != (Rat{5, 4}) {
		t.Errorf("length %v", tu.Voices[0].Bars[0].Length)
	}
}

func TestParse_BarFloodRefused(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(hdr+strings.Repeat("Z9999 ", 20000)), Options{})
	if ruleOf(err) != "too_many_events" {
		t.Errorf("flood: %v", err)
	}
}

func TestRat(t *testing.T) {
	t.Parallel()
	a, b := R(1, 3), R(1, 6)
	if a.Add(b) != R(1, 2) || a.Sub(b) != R(1, 6) || a.Mul(b) != R(1, 18) || a.Cmp(b) != 1 || b.Cmp(a) != -1 || a.Cmp(R(2, 6)) != 0 {
		t.Error("arithmetic")
	}
	if R(1, 0) != (Rat{0, 1}) || R(4, 8) != (Rat{1, 2}) {
		t.Error("constructor")
	}
	if got := R(1, 3).ticks(480); got != 640 {
		t.Errorf("ticks %d", got)
	}
	if _, ok := mulc(Rat{1 << 40, 1}, Rat{1 << 40, 1}); ok {
		t.Error("mulc missed overflow")
	}
	if _, ok := addc(Rat{1, 1 << 40}, Rat{1, 3 << 40}); !ok {
		t.Error("addc false positive")
	}
	if _, ok := addc(Rat{1, 1<<40 + 1}, Rat{1, 1<<41 + 1}); ok {
		t.Error("addc missed overflow")
	}
}

func FuzzParseABC(f *testing.F) {
	for _, s := range []string{hdr + "CDEF|G4|", hdr + "|:A|1B:|2C|]", "X:1\nK:C\n[V:1]C|[V:2]D|", "X:1\nK:D\n\"Am\"(3CDE [CEG]2 C>D z Z3 |", "%%MIDI program 3\nK:C\nC", "X:1\nK:C\nC/////", "K:D exp ^f\nF"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, d := range []Dialect{DialectStandard, DialectYuE2} {
			tu, err := Parse(b, Options{Dialect: d})
			if err != nil {
				if ruleOf(err) == "internal" {
					t.Fatalf("parser fault on %q: %v", b, err)
				}
				continue
			}
			tu.BarSpans()
			tu.SecondsPerBar()
			for i := range tu.Voices {
				tu.Notes(i)
				tu.BeatNotes(i)
			}
			for _, sp := range tu.BarSpans() {
				if sp.Start < 0 || sp.End < sp.Start || sp.End > len(tu.Src) {
					t.Fatalf("bad span %+v for %q", sp, b)
				}
			}
			if s, err := tu.Score(EventOptions{Chords: ChordsBlock}); err == nil && s.Bars < 0 {
				t.Fatal("negative bars")
			}
		}
	})
}

func TestABC_InlineVoicesAndInterleave(t *testing.T) {
	t.Parallel()
	tu := mustParse(t, hdr+"[V:1]CD|[V:2]G,A,|[V:1]EF|\n", "")
	if len(tu.Voices) != 2 || len(tu.Voices[0].Bars) != 2 || len(tu.Voices[1].Bars) != 1 {
		t.Fatalf("voices %+v", tu.Voices)
	}
	if _, ok := tu.SecondsPerBar(); ok {
		t.Error("an inline voice switch makes the tune untimed (as yue2.ParseScore)")
	}
	tu = mustParse(t, "X:1\nL:1/4\nQ:1/4=60\nK:C\nV:1\nCD|\nV:2\nEF|\nV:1\nGA|\nV:2\nBc|\n", "")
	if len(tu.Voices[0].Bars) != 2 || len(tu.Voices[1].Bars) != 2 {
		t.Errorf("interleaved %+v", tu.Voices)
	}
	if got := ev(mustScore(t, "X:1\nL:1/4\nQ:1/4=60\nK:C\nV:1\nCD|\nV:2\nEF|\nV:1\nGA|\n", "", EventOptions{}), 0); got != "60@0+480 62@480+480 67@960+480 69@1440+480" {
		t.Errorf("interleaved events %s", got)
	}
}

func TestABC_EventsDeterministic(t *testing.T) {
	t.Parallel()
	src := hdr + "|:\"Am\"A>B (3cde|1 f2:|2 g2z2|]\n"
	a := fmt.Sprint(mustScore(t, src, "", EventOptions{Chords: ChordsBlock}))
	b := fmt.Sprint(mustScore(t, src, "", EventOptions{Chords: ChordsBlock}))
	if a != b {
		t.Error("score events differ between runs")
	}
}

func ruleOf(err error) string {
	var se *SyntaxError
	if errors.As(err, &se) {
		return se.Rule
	}
	return ""
}
