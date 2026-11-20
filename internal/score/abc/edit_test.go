package abc

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func edit(t *testing.T, src string, d Dialect, ops ...Op) (string, *Tune, []Violation, error) {
	t.Helper()
	o := Options{Dialect: d}
	tu := mustParse(t, src, d)
	out, ed, v, err := Edit([]byte(src), tu, ops, o)
	return string(out), ed, v, err
}

func mustEdit(t *testing.T, src string, d Dialect, ops ...Op) string {
	t.Helper()
	out, _, v, err := edit(t, src, d, ops...)
	if err != nil || len(v) > 0 {
		t.Fatalf("edit: err %v violations %+v", err, v)
	}
	return out
}

func TestEdit_ReplaceBars(t *testing.T) {
	t.Parallel()
	got := mustEdit(t, invBase, DialectYuE2, Op{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 2, ABC: "AAAA | BBBB"})
	want := strings.Replace(invBase, "GABc | cBAG", "AAAA | BBBB", 1)
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// one bar, surrounding space and the other voice untouched, whitespace around the new text trimmed
	got = mustEdit(t, invBase, DialectYuE2, Op{Op: OpReplaceBars, Voice: "2", BarFrom: 0, BarTo: 0, ABC: "  E,4 "})
	if want := strings.Replace(invBase, "C,4 |", "E,4 |", 1); got != want {
		t.Fatalf("got\n%s", got)
	}
	// a chord symbol may be replaced with the bar
	got = mustEdit(t, invBase, DialectYuE2, Op{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: `"Am"ABcd`})
	if want := strings.Replace(invBase, `"C"CDEF`, `"Am"ABcd`, 1); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestEdit_ReplaceBars_KeepsInterleavedAndComments(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\n[V:1] CDEF | GABc |\n[V:2] C,4 | G,4 |\n% chorus\n[V:1] cBAG | FEDC |\n[V:2] A,4 | F,4 |\n"
	_ = src
	src = "X:1\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\nV:1\nCDEF | GABc |\nw: la la la la\n% chorus\ncBAG | FEDC |\nV:2\nC,4 | G,4 | A,4 | F,4 |\n"
	got := mustEdit(t, src, DialectYuE2, Op{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 2, ABC: "AAAA | BBBB"})
	want := strings.Replace(src, "GABc |\nw: la la la la\n% chorus\ncBAG", "AAAA |\nw: la la la la\n% chorus\nBBBB", 1)
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestEdit_ReplaceBars_Violations(t *testing.T) {
	t.Parallel()
	out, ed, v, err := edit(t, invBase, DialectYuE2, Op{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "AAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].Code != "bar_length" || out == "" || ed == nil {
		t.Fatalf("violations %+v out %q", v, out)
	}
	_, _, v, err = edit(t, invBase, DialectYuE2, Op{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "[M:3/4]GAB"})
	if err != nil || len(v) == 0 {
		t.Fatalf("inline field must be a violation: %v %+v", err, v)
	}
}

func TestEdit_Errors(t *testing.T) {
	t.Parallel()
	many := make([]string, 65)
	for i := range many {
		many[i] = "C"
	}
	long := mustParse(t, "X:1\nM:1/4\nL:1/4\nK:C\n"+strings.Repeat("C|", 70)+"\n", "")
	twenty := make([]Op, 21)
	for i := range twenty {
		twenty[i] = Op{Op: OpSetTempo, BPM: 100}
	}
	rows := []struct {
		name string
		ops  []Op
		rule string
		tune *Tune
	}{
		{"no ops", nil, "no_ops", nil},
		{"too many ops", twenty, "too_many_ops", nil},
		{"unknown op", []Op{{Op: "delete"}}, "bad_op", nil},
		{"unknown voice", []Op{{Op: OpReplaceBars, Voice: "9", BarFrom: 0, BarTo: 0, ABC: "C"}}, "bad_voice", nil},
		{"empty voice for replace", []Op{{Op: OpReplaceBars, BarFrom: 0, BarTo: 0, ABC: "C4"}}, "bad_voice", nil},
		{"negative bar", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: -1, BarTo: 0, ABC: "C4 | C4"}}, "bad_range", nil},
		{"reversed range", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 2, BarTo: 1, ABC: "C4 | C4"}}, "bad_range", nil},
		{"past the end", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 7, BarTo: 8, ABC: "C4 | C4"}}, "bad_range", nil},
		{"bar count mismatch", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 2, ABC: "C4"}}, "bad_abc", nil},
		{"too many bars in one op", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 64, ABC: strings.Join(many, "|")}}, "bad_range", long},
		{"empty bar text", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 1, ABC: "C4 |  "}}, "bad_abc", nil},
		{"newline in abc", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "C4\nD4"}}, "bad_abc", nil},
		{"comment in abc", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "C4 % x"}}, "bad_abc", nil},
		{"unparsable result", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "[CE"}}, "result_unparsable", nil},
		{"zero semitones", []Op{{Op: OpTransposeBars, Voice: "1", BarFrom: 0, BarTo: 0, Semitones: 0}}, "bad_semitones", nil},
		{"13 semitones", []Op{{Op: OpTransposeBars, Voice: "1", BarFrom: 0, BarTo: 0, Semitones: 13}}, "bad_semitones", nil},
		{"-13 semitones", []Op{{Op: OpTransposeBars, Voice: "1", BarFrom: 0, BarTo: 0, Semitones: -13}}, "bad_semitones", nil},
		{"transpose unknown voice", []Op{{Op: OpTransposeBars, Voice: "x", BarFrom: 0, BarTo: 0, Semitones: 2}}, "bad_voice", nil},
		{"transpose past the end", []Op{{Op: OpTransposeBars, Voice: "", BarFrom: 0, BarTo: 8, Semitones: 2}}, "bad_range", nil},
		{"transpose out of pitch range", []Op{{Op: OpTransposeBars, Voice: "1", BarFrom: 0, BarTo: 0, Semitones: 12}}, "pitch_range", highTune(t)},
		{"field line in abc", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "w: a b c d"}}, "bad_abc", nil},
		{"trailing backslash", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "C4\\"}}, "bad_abc", nil},
		{"inline key change", []Op{{Op: OpTransposeBars, Voice: "1", BarFrom: 0, BarTo: 1, Semitones: 3}}, "unsupported_feature", mustParse(t, "X:1\nM:4/4\nL:1/4\nK:C\n[K:D] dddd|dddd|\n", DialectYuE2)},
		{"tempo 39", []Op{{Op: OpSetTempo, BPM: 39}}, "bad_tempo", nil},
		{"tempo 241", []Op{{Op: OpSetTempo, BPM: 241}}, "bad_tempo", nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			tu := r.tune
			if tu == nil {
				tu = mustParse(t, invBase, DialectYuE2)
			}
			out, _, _, err := Edit(tu.Src, tu, r.ops, Options{Dialect: DialectYuE2})
			var ee *EditError
			if !errors.As(err, &ee) {
				t.Fatalf("err %v, want EditError %s", err, r.rule)
			}
			if ee.Rule != r.rule {
				t.Fatalf("rule %q, want %q (%v)", ee.Rule, r.rule, ee)
			}
			if out != nil {
				t.Fatalf("output %q with an error", out)
			}
		})
	}
}

func highTune(t *testing.T) *Tune {
	return mustParse(t, "X:1\nM:4/4\nL:1/4\nK:C\nV:1\nc''''4 | C4 |\n", DialectYuE2)
}

func TestEdit_NilTuneIsParsed(t *testing.T) {
	t.Parallel()
	out, _, v, err := Edit([]byte(invBase), nil, []Op{{Op: OpSetTempo, BPM: 90}}, Options{Dialect: DialectYuE2})
	if err != nil || len(v) != 0 || !bytes.Contains(out, []byte("Q:1/4=90\n")) {
		t.Fatalf("%v %v %q", err, v, out)
	}
}

func TestEdit_SetTempo(t *testing.T) {
	t.Parallel()
	got := mustEdit(t, invBase, DialectYuE2, Op{Op: OpSetTempo, BPM: 132})
	if want := strings.Replace(invBase, "Q:1/4=100", "Q:1/4=132", 1); got != want {
		t.Fatalf("got\n%s", got)
	}
	// no Q: line: inserted before K:
	noQ := strings.Replace(invBase, "Q:1/4=100\n", "", 1)
	got = mustEdit(t, noQ, DialectYuE2, Op{Op: OpSetTempo, BPM: 80})
	if want := strings.Replace(noQ, "K:C\n", "Q:1/4=80\nK:C\n", 1); got != want {
		t.Fatalf("got\n%s", got)
	}
	// other forms of Q: are rewritten, CRLF kept
	crlf := strings.ReplaceAll(strings.Replace(invBase, "Q:1/4=100", "Q:100 % slow", 1), "\n", "\r\n")
	got = mustEdit(t, crlf, DialectYuE2, Op{Op: OpSetTempo, BPM: 60})
	if want := strings.Replace(crlf, "Q:100 % slow\r\n", "Q:1/4=60\r\n", 1); got != want {
		t.Fatalf("got %q", got)
	}
	// the parsed result carries the tempo and is allowed to differ
	_, ed, v, err := edit(t, invBase, DialectYuE2, Op{Op: OpSetTempo, BPM: 240})
	if err != nil || len(v) != 0 || ed.Header.Tempo == nil || ed.Header.Tempo.BPM != 240 {
		t.Fatalf("%v %v %+v", err, v, ed.Header.Tempo)
	}
	// a later header Q: wins, so it is the one rewritten
	two := strings.Replace(invBase, "Q:1/4=100\n", "Q:1/4=100\nQ:1/4=110\n", 1)
	_, ed, _, err = edit(t, two, DialectYuE2, Op{Op: OpSetTempo, BPM: 70})
	if err != nil || ed.Header.Tempo.BPM != 70 {
		t.Fatalf("%v %+v", err, ed.Header.Tempo)
	}
}

func TestEdit_TransposeBars_Table(t *testing.T) {
	t.Parallel()
	const h = "X:1\nM:4/4\nL:1/4\n"
	rows := []struct {
		name, src string
		d         Dialect
		op        Op
		want      string // the whole tune body line(s) after the header
	}{
		{"C up two, flats", h + "K:C\nCDEF|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "D E _G =G"},
		{"down across the octave", h + "K:C\nC4|\n", DialectStandard, Op{Voice: "1", Semitones: -1}, "B,4"},
		{"sharp key", h + "K:G\nGABc|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "^G ^A c ^c"},
		{"signature sharp needs no mark", h + "K:G\nG4|\n", DialectStandard, Op{Voice: "1", Semitones: -1}, "F4"},
		{"signature natural cancelled", h + "K:G\nF4|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "G4"},
		{"memory tracked", h + "K:C\nF^FF|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "_G=GG"},
		{"yue2 carries across octaves", h + "K:C\n^Ff|\n", DialectYuE2, Op{Voice: "1", Semitones: 12}, "_gg'"},
		{"standard keeps octaves apart", h + "K:C\n^Ff|\n", DialectStandard, Op{Voice: "1", Semitones: 12}, "_gf'"},
		{"tie into the next bar forces the accidental", h + "K:C\n^F4-|F4|\n", DialectStandard, Op{Voice: "1", Semitones: 1, BarFrom: 0, BarTo: 1}, "G4-|=G4"},
		{"rests and lengths kept", h + "K:C\nC3/2 z/2 D2|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "D3/2 z/2 E2"},
		{"chord notes", h + "K:C\n[CEG]4|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "[D_GA]4"},
		{"tuplet", h + "K:C\n(3CDE F|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "(3D E _G =G"},
		{"chord symbol flat key", h + "K:C\n\"G\"G4|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "\"Ab\"_A4"},
		{"chord symbol sharp key", h + "K:G\n\"G\"G4|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "\"G#\"^G4"},
		{"slash bass moves too", h + "K:C\n\"Am/E\"A4|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "\"Bm/Gb\"B4"},
		{"quality kept", h + "K:C\n\"Cmaj7\"C4|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "\"Dmaj7\"D4"},
		{"annotation untouched", h + "K:C\n\"^verse\"C4|\n", DialectStandard, Op{Voice: "1", Semitones: 2}, "\"^verse\"D4"},
		{"lower octave marks", h + "K:C\nc'c,|\n", DialectStandard, Op{Voice: "1", Semitones: 1}, "_d'_D"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			r.op.Op = OpTransposeBars
			got := mustEdit(t, r.src, r.d, r.op)
			// the body is the second line after K:
			body := got[strings.Index(got, "K:")+len("K:"):]
			body = body[strings.Index(body, "\n")+1:]
			body = strings.TrimSpace(body)
			want := strings.TrimSuffix(r.want, "|")
			// compare without the bar line and spaces, which the edit leaves as they were
			norm := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", ""), " ", "") }
			if norm(body) != norm(want) {
				t.Fatalf("got %q, want %q", body, want)
			}
		})
	}
}

func TestEdit_TransposeBars_AllVoicesAndPitches(t *testing.T) {
	t.Parallel()
	before := mustParse(t, invBase, DialectYuE2)
	out := mustEdit(t, invBase, DialectYuE2, Op{Op: OpTransposeBars, BarFrom: 1, BarTo: 2, Semitones: 5})
	after := mustParse(t, out, DialectYuE2)
	for vi := range before.Voices {
		for bi := range before.Voices[vi].Bars {
			for ni, n := range before.Voices[vi].Bars[bi].Notes {
				want := n.Pitch
				if bi == 1 || bi == 2 {
					want += 5
				}
				if got := after.Voices[vi].Bars[bi].Notes[ni].Pitch; got != want {
					t.Fatalf("voice %d bar %d note %d: %d, want %d", vi, bi, ni, got, want)
				}
			}
		}
	}
	if !bytes.Contains([]byte(out), []byte("K:C\n")) {
		t.Fatal("K: must be unchanged")
	}
}

func TestEdit_Sequential(t *testing.T) {
	t.Parallel()
	got := mustEdit(t, invBase, DialectYuE2,
		Op{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "cdef"},
		Op{Op: OpTransposeBars, Voice: "1", BarFrom: 1, BarTo: 1, Semitones: 2},
		Op{Op: OpSetTempo, BPM: 90})
	if !strings.Contains(got, "| de_g=g |") || !strings.Contains(got, "Q:1/4=90") {
		t.Fatalf("got\n%s", got)
	}
}

func TestEdit_SharedSpanRefused(t *testing.T) {
	t.Parallel()
	src := "X:1\nM:4/4\nL:1/4\nK:C\nV:1\nCDEF | Z3 | GABc |\n"
	tu := mustParse(t, src, DialectYuE2)
	_, _, _, err := Edit([]byte(src), tu, []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 1, BarTo: 1, ABC: "C4"}}, Options{Dialect: DialectYuE2})
	var ee *EditError
	if !errors.As(err, &ee) || ee.Rule != "shared_span" {
		t.Fatalf("err %v", err)
	}
}

// TestEdit_OutsideUntouched: every byte outside the edited bar spans (and the Q: line for set_tempo) is unchanged.
func TestEdit_OutsideUntouched(t *testing.T) {
	t.Parallel()
	src := invBase + "w: lyrics stay\n% trailing comment\n"
	src = strings.Replace(src, "V:2", "%%MIDI program 33\nV:2", 1)
	tu := mustParse(t, src, DialectYuE2)
	type tc struct {
		name string
		ops  []Op
	}
	cases := []tc{
		{"replace", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 2, BarTo: 5, ABC: "AAAA | BBBB | cccc | GGGG"}}},
		{"transpose one voice", []Op{{Op: OpTransposeBars, Voice: "2", BarFrom: 0, BarTo: 3, Semitones: -3}}},
		{"transpose all", []Op{{Op: OpTransposeBars, BarFrom: 4, BarTo: 7, Semitones: 7}}},
		{"tempo", []Op{{Op: OpSetTempo, BPM: 150}}},
		{"mixed", []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: `"Am"ABcd`}, {Op: OpTransposeBars, Voice: "2", BarFrom: 7, BarTo: 7, Semitones: 1}, {Op: OpSetTempo, BPM: 61}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ed, v, err := Edit([]byte(src), tu, c.ops, Options{Dialect: DialectYuE2})
			if err != nil || len(v) > 0 {
				t.Fatalf("%v %+v", err, v)
			}
			if got, want := skeleton(tu, tu.Src, ed.Src, true), skeleton(ed, ed.Src, ed.Src, false); got != want {
				t.Fatalf("outside bytes differ:\n%q\n%q", got, want)
			}
			_ = out
		})
	}
}

// skeleton is the source with every bar span of every voice cut out and the Q: header line dropped; bars of the
// original are cut from the original text, bars of the edited tune from the edited text.
func skeleton(tu *Tune, _ []byte, edited []byte, orig bool) string {
	src := tu.Src
	if !orig {
		src = edited
	}
	type sp struct{ s, e int }
	var cuts []sp
	for _, v := range tu.Voices {
		for _, b := range v.Bars {
			cuts = append(cuts, sp{b.Start, b.End})
		}
	}
	var sb strings.Builder
	pos := 0
	for pos < len(src) {
		cut := -1
		for _, c := range cuts {
			if c.s == pos && c.e > pos {
				cut = c.e
			}
		}
		if cut >= 0 {
			pos = cut
			continue
		}
		sb.WriteByte(src[pos])
		pos++
	}
	var lines []string
	for _, l := range strings.Split(sb.String(), "\n") {
		if !strings.HasPrefix(l, "Q:") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func FuzzEdit(f *testing.F) {
	f.Add([]byte(invBase), byte(0), uint8(1), uint8(2), int8(3), []byte("AAAA | BBBB"))
	f.Add([]byte(invPickup), byte(1), uint8(0), uint8(1), int8(-5), []byte("D"))
	f.Add([]byte("X:1\nM:4/4\nL:1/4\nK:G\n\"G\"^F4|f4-|F4|\n"), byte(2), uint8(0), uint8(2), int8(12), []byte("C4"))
	f.Add([]byte("X:1\nK:C\nV:1\nCDEF|Z3|G4|\nV:2\nC4|C4|C4|C4|\n"), byte(1), uint8(0), uint8(3), int8(1), []byte("C"))
	f.Fuzz(func(t *testing.T, src []byte, kind byte, from, to uint8, semis int8, text []byte) {
		if len(src) > 8192 {
			return
		}
		start := time.Now()
		o := Options{Dialect: DialectYuE2}
		tu, err := Parse(src, o)
		if err != nil {
			return
		}
		voice := ""
		if len(tu.Voices) > 0 {
			voice = tu.Voices[int(from)%len(tu.Voices)].ID
		}
		var op Op
		switch kind % 3 {
		case 0:
			op = Op{Op: OpReplaceBars, Voice: voice, BarFrom: int(from % 8), BarTo: int(to % 8), ABC: string(text)}
		case 1:
			op = Op{Op: OpTransposeBars, Voice: voice, BarFrom: int(from % 8), BarTo: int(to % 8), Semitones: int(semis)}
		default:
			op = Op{Op: OpSetTempo, BPM: int(semis) + 100}
		}
		out, ed, v, err := Edit(src, tu, []Op{op}, o)
		if err == nil && (out == nil || ed == nil) {
			t.Fatalf("nil result without an error")
		}
		if err != nil && (out != nil || ed != nil || v != nil) {
			t.Fatalf("result with an error")
		}
		if err == nil && len(v) == 0 {
			if _, perr := Parse(out, o); perr != nil {
				t.Fatalf("result does not parse: %v", perr)
			}
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("slow input: %v", time.Since(start))
		}
	})
}

func TestEdit_ReplaceBars_PipeInQuotes(t *testing.T) {
	t.Parallel()
	tu := mustParse(t, invBase, DialectYuE2)
	out, _, _, err := Edit(tu.Src, tu, []Op{{Op: OpReplaceBars, Voice: "1", BarFrom: 0, BarTo: 0, ABC: "\"A|B\"C4"}}, Options{Dialect: DialectYuE2})
	if err != nil {
		var ee *EditError
		if errors.As(err, &ee) && ee.Rule == "bad_abc" {
			t.Fatalf("a pipe inside quotes split the bar: %v", err)
		}
	}
	_ = out
}
