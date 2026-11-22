package songpr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// barsScore is a synthetic two-voice yue2 score whose sections line up with baseLyrics: verse 1 (bars 0-3), chorus (4-21, 18
// bars so that a 17-bar range fits inside one section), verse 2 (22-25), bridge (26-27), outro (28).
func barsScore() []byte {
	var b strings.Builder
	b.WriteString("X:1\nT:synthetic\nM:4/4\nL:1/8\nQ:1/4=96\nK:C\n%%MIDI program 53\nV:1 name=\"vocal\"\n")
	secs := []struct {
		label string
		bars  int
	}{{"verse 1", 4}, {"chorus", 18}, {"verse 2", 4}, {"bridge", 2}, {"outro", 1}}
	for _, s := range secs {
		b.WriteString("% " + s.label + "\n")
		for range s.bars {
			b.WriteString("c2 d2 e2 f2 |")
		}
		b.WriteString("\n")
	}
	b.WriteString("V:2 name=\"bass\"\n")
	for _, s := range secs {
		b.WriteString("% " + s.label + "\n")
		for range s.bars {
			b.WriteString("C,8 |")
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func barsInput(ops []Op, addr ...Addressed) CompileInput {
	in := input(ops, addr...)
	in.BaseScore, in.BaseScoreAligned = barsScore(), true
	return in
}

func rep(voice string, from, to int, abcText string) Op {
	return Op{Op: OpReplaceBars, Voice: voice, BarFrom: from, BarTo: to, ABC: abcText}
}

func segs(n int, seg string) string { return strings.TrimSuffix(strings.Repeat(seg+" | ", n), " | ") }

func TestCompile_ReplaceBars_BoundsTable(t *testing.T) {
	t.Parallel()
	chorus := fixComment("c-chorus", "chorus", 1)
	verse1 := fixComment("c-verse1", "verse", 1)
	cases := []struct {
		name     string
		in       CompileInput
		wantRule string // "code:rule" among the violations; "" compiles
		operator string
	}{
		{name: "one bar", in: barsInput([]Op{rep("1", 4, 4, "g8")}, chorus), operator: OperatorABCEdit},
		{name: "two bars", in: barsInput([]Op{rep("1", 5, 6, "g2 a2 b2 c'2 | g8")}, chorus), operator: OperatorABCEdit},
		{name: "sixteen bars", in: barsInput([]Op{rep("1", 4, 19, segs(16, "g8"))}, chorus), operator: OperatorABCEdit},
		{name: "two voices, sixteen bars together", in: barsInput([]Op{rep("1", 4, 11, segs(8, "g8")), rep("2", 4, 11, segs(8, "G,8"))}, chorus), operator: OperatorABCEdit},
		{name: "seventeen bars", in: barsInput([]Op{rep("1", 4, 20, segs(17, "g8"))}, chorus), wantRule: CodePatchOutOfBounds + ":too_many_bars"},
		{name: "seventeen bars over two ops", in: barsInput([]Op{rep("1", 4, 12, segs(9, "g8")), rep("2", 4, 11, segs(8, "G,8"))}, chorus), wantRule: CodePatchOutOfBounds + ":too_many_bars"},
		{name: "two voices in one op", in: barsInput([]Op{rep("1,2", 4, 4, "g8")}, chorus), wantRule: CodePatchInvalid + ":bad_voice"},
		{name: "no voice", in: barsInput([]Op{rep("", 4, 4, "g8")}, chorus), wantRule: CodePatchInvalid + ":bad_voice"},
		{name: "unknown voice", in: barsInput([]Op{rep("9", 4, 4, "g8")}, chorus), wantRule: CodePatchInvalid + ":bad_voice"},
		{name: "outside the scoped section", in: barsInput([]Op{rep("1", 22, 22, "g8")}, chorus), wantRule: CodePatchOutOfBounds + ":bars_out_of_scope"},
		{name: "straddles the section end", in: barsInput([]Op{rep("1", 21, 22, "g8 | g8")}, chorus), wantRule: CodePatchOutOfBounds + ":bars_out_of_scope"},
		{name: "second scoped section", in: barsInput([]Op{rep("1", 0, 0, "g8"), rep("2", 4, 4, "G,8")}, chorus, verse1), operator: OperatorABCEdit},
		{name: "extra section brings bars in scope", in: func() CompileInput {
			in := barsInput([]Op{rep("1", 26, 26, "g8")}, chorus)
			in.ExtraSections = []int{3}
			return in
		}(), operator: OperatorABCEdit},
		{name: "unaligned score", in: func() CompileInput {
			in := barsInput([]Op{rep("1", 4, 4, "g8")}, chorus)
			in.BaseScoreAligned = false
			return in
		}(), wantRule: CodePatchOutOfBounds + ":score_unaligned"},
		{name: "section count differs", in: func() CompileInput {
			in := barsInput([]Op{rep("1", 4, 4, "g8")}, chorus)
			in.BaseScore = []byte(strings.Replace(string(in.BaseScore), "% outro\nc2 d2 e2 f2 |\n", "", 1))
			return in
		}(), wantRule: CodePatchOutOfBounds + ":score_unaligned"},
		{name: "no score", in: func() CompileInput {
			in := barsInput([]Op{rep("1", 4, 4, "g8")}, chorus)
			in.BaseScore = nil
			return in
		}(), wantRule: CodePatchInvalid + ":no_score"},
		{name: "unparsable score", in: func() CompileInput {
			in := barsInput([]Op{rep("1", 4, 4, "g8")}, chorus)
			in.BaseScore = []byte("X:1\nM:4/4\nL:1/8\nK:C\nV:1\n")
			return in
		}(), wantRule: CodePatchInvalid + ":score_unparsable"},
		{name: "range reversed", in: barsInput([]Op{rep("1", 6, 5, "g8")}, chorus), wantRule: CodePatchInvalid + ":bad_range"},
		{name: "segment count differs from the range", in: barsInput([]Op{rep("1", 4, 5, "g8")}, chorus), wantRule: CodePatchInvalid + ":abc_bad_abc"},
		{name: "invariant bar length", in: barsInput([]Op{rep("1", 5, 5, "g4")}, chorus), wantRule: CodePatchInvalid + ":abc_bar_length"},
		{name: "invariant inline field", in: barsInput([]Op{rep("1", 5, 5, "[K:G] g8")}, chorus), wantRule: CodePatchInvalid + ":abc_unsupported_feature"},
		{name: "with lyrics", in: barsInput([]Op{rep("1", 4, 4, "g8"), line(1, 0, "a new first chorus line")}, chorus), operator: OperatorMultiEdit},
		{name: "with style", in: barsInput([]Op{rep("1", 4, 4, "g8"), style("pop, warm, male vocal, italian, 96bpm")}, chorus), operator: OperatorMultiEdit},
		{name: "same bars again", in: barsInput([]Op{rep("1", 4, 4, "c2 d2 e2 f2")}, chorus), wantRule: CodePatchNoChange + ":unchanged"},
		{name: "overlapping ops", in: barsInput([]Op{rep("1", 4, 5, "g8 | g8"), rep("1", 5, 6, "a8 | a8")}, chorus), operator: OperatorABCEdit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := Compile(c.in)
			if c.wantRule == "" {
				if err != nil {
					t.Fatalf("Compile: %v", err)
				}
				if got.Operator != c.operator {
					t.Errorf("operator = %q, want %q", got.Operator, c.operator)
				}
				if got.Patch.ABC == nil || len(got.Diff.ABC) == 0 {
					t.Errorf("no abc in the patch or the diff: %+v", got.Diff)
				}
				if got.Request.ABC == nil || *got.Request.ABC != *got.Patch.ABC {
					t.Error("the compiled request does not carry the patched score")
				}
				return
			}
			if err == nil {
				t.Fatalf("compiled; want %s", c.wantRule)
			}
			if _, rules := codes(err); !contains(rules, c.wantRule) {
				t.Errorf("rules = %v, want %s", rules, c.wantRule)
			}
		})
	}
}

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}

// every violation is listed, never the first only.
func TestCompile_ReplaceBars_ListsEveryViolation(t *testing.T) {
	t.Parallel()
	in := barsInput([]Op{rep("1", 5, 5, "g4"), rep("2", 6, 6, "[K:G] G,8"), rep("1", 25, 25, "g8")}, fixComment("c-chorus", "chorus", 1))
	_, err := Compile(in)
	_, rules := codes(err)
	for _, want := range []string{CodePatchInvalid + ":abc_bar_length", CodePatchInvalid + ":abc_unsupported_feature", CodePatchOutOfBounds + ":bars_out_of_scope"} {
		if !contains(rules, want) {
			t.Errorf("rules %v lack %s", rules, want)
		}
	}
}

// every byte outside the edited bars' content spans is the base's, and the lyrics are untouched.
func TestCompile_ReplaceBars_OutsideUntouched(t *testing.T) {
	t.Parallel()
	in := barsInput([]Op{rep("1", 5, 6, "g2 a2 b2 c'2 | g8")}, fixComment("c-chorus", "chorus", 1))
	got, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Lyrics != in.Base.Lyrics || got.Patch.Lyrics != nil || got.Request.Style != in.Base.Style || got.Patch.Style != nil {
		t.Error("lyrics or style changed by a bars-only patch")
	}
	base, err := abc.Parse(in.BaseScore, abc.Options{Dialect: abc.DialectYuE2})
	if err != nil {
		t.Fatal(err)
	}
	patched, err := abc.Parse([]byte(*got.Patch.ABC), abc.Options{Dialect: abc.DialectYuE2})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Voices) != len(patched.Voices) {
		t.Fatalf("voices %d -> %d", len(base.Voices), len(patched.Voices))
	}
	for vi := range base.Voices {
		if len(base.Voices[vi].Bars) != len(patched.Voices[vi].Bars) {
			t.Fatalf("voice %d: bar count changed", vi)
		}
		for bi, ob := range base.Voices[vi].Bars {
			nb := patched.Voices[vi].Bars[bi]
			same := bytes.Equal(base.Src[ob.Start:ob.End], patched.Src[nb.Start:nb.End])
			edited := vi == 0 && (bi == 5 || bi == 6)
			if same == edited {
				t.Errorf("voice %d bar %d: same=%v, edited=%v", vi, bi, same, edited)
			}
		}
	}
	if len(got.Diff.ABC) != 1 {
		t.Fatalf("diff = %+v", got.Diff.ABC)
	}
	d := got.Diff.ABC[0]
	if d.Voice != "1" || d.BarFrom != 5 || d.BarTo != 6 || d.Before != "c2 d2 e2 f2 | c2 d2 e2 f2" || d.After != "g2 a2 b2 c'2 | g8" {
		t.Errorf("diff entry = %+v", d)
	}
	if got.Scope.Bounds.BarsChanged != 2 || got.Scope.Bounds.MaxBars != MaxBars {
		t.Errorf("bounds = %+v", got.Scope.Bounds)
	}
}
