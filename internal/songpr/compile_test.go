package songpr

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

const baseLyrics = `[Verse 1]
v1 line one
v1 line two
v1 line three
v1 line four
[Chorus]
c1 line one
c1 line two
c1 line three
c1 line four
[Verse 2]
v2 line one
v2 line two
v2 line three
v2 line four
[Bridge]
b line one
b line two
[Outro]
o line one`

// baseRequest is a synthetic recipe: verse 1 (section 0), chorus (1), verse 2 (2), bridge (3), outro (4).
func baseRequest() yue2.Request {
	return yue2.Request{ID: "base_1", Style: "pop, warm, female vocal, italian, 96bpm", Lyrics: baseLyrics, Cot: "full", Seed: 7}
}

func fixComment(id string, kind string, ordinal int) Addressed {
	return Addressed{CommentID: id, Kind: "fix", State: "open", OnLineage: true, Spot: &Spot{Kind: kind, Ordinal: ordinal}}
}

func input(ops []Op, addr ...Addressed) CompileInput {
	return CompileInput{Base: baseRequest(), RecipeKnown: true, Ops: ops, Addressed: addr}
}

func line(sec, ln int, text string) Op {
	return Op{Op: OpReplaceLine, SectionIdx: sec, LineIdx: ln, Text: text}
}
func style(s string) Op { return Op{Op: OpSetStyle, Style: s} }

// codes returns the distinct violation codes in order of appearance, rules the "code:rule" pairs.
func codes(err error) (cs, rules []string) {
	var ce *CompileError
	if !errors.As(err, &ce) {
		return nil, nil
	}
	for _, v := range ce.Violations {
		if !slices.Contains(cs, v.Code) {
			cs = append(cs, v.Code)
		}
		rules = append(rules, v.Code+":"+v.Rule)
	}
	return cs, rules
}

func lines(n int, sec int, prefix string) []Op {
	var ops []Op
	for i := 0; i < n; i++ {
		ops = append(ops, line(sec, i, fmt.Sprintf("%s %d", prefix, i)))
	}
	return ops
}

func TestCompile_BoundsTable(t *testing.T) {
	t.Parallel()
	chorus := fixComment("c-chorus", "chorus", 1)
	verse1 := fixComment("c-verse1", "verse", 1)
	cases := []struct {
		name     string
		in       CompileInput
		wantCode string // "" => compiles
		wantRule string // one "code:rule" that must be among the violations
		operator string
	}{
		{name: "one chorus line", in: input([]Op{line(1, 0, "a new first chorus line")}, chorus), operator: OperatorLyricEdit},
		{name: "style swap of one term", in: input([]Op{style("pop, warm, male vocal, italian, 96bpm")}, chorus), operator: OperatorStyleEdit},
		{name: "lyrics and style", in: input([]Op{line(1, 1, "another chorus line"), style("pop, warm, male vocal, italian, 96bpm")}, chorus), operator: OperatorMultiEdit},
		{name: "exactly eight lines over three sections",
			in: func() CompileInput {
				in := input(append(append(lines(4, 1, "x"), lines(3, 0, "y")...), line(2, 0, "z")), chorus)
				in.ExtraSections = []int{0, 2}
				return in
			}(), operator: OperatorLyricEdit},
		{name: "nine lines", in: func() CompileInput {
			in := input(append(append(lines(4, 1, "x"), lines(4, 0, "y")...), line(2, 0, "z")), chorus)
			in.ExtraSections = []int{0, 2}
			return in
		}(), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":too_many_lines"},
		{name: "three style terms swapped", in: input([]Op{style("rock, dark, male vocal, italian, 96bpm")}, chorus), operator: OperatorStyleEdit},
		{name: "a fourth style term", in: input([]Op{style("rock, dark, male vocal, loud, italian, 96bpm")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":style_terms"},
		{name: "bpm plus twenty", in: input([]Op{style("pop, warm, female vocal, italian, 116bpm")}, chorus), operator: OperatorStyleEdit},
		{name: "bpm plus twenty-one", in: input([]Op{style("pop, warm, female vocal, italian, 117bpm")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":bpm_delta"},
		{name: "bpm minus twenty-one", in: input([]Op{style("pop, warm, female vocal, italian, 75bpm")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":bpm_delta"},
		{name: "language tag changed", in: input([]Op{style("pop, warm, female vocal, english, 96bpm")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":language_tag"},
		{name: "language tag dropped", in: input([]Op{style("pop, warm, female vocal, 96bpm")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":language_tag"},
		{name: "renamed section tag", in: input([]Op{line(1, 0, "[Chorus 2]")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":section_tag"},
		{name: "tag inside a line", in: input([]Op{line(1, 0, "la la [Bridge] la")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":section_tag"},
		{name: "section out of scope", in: input([]Op{line(2, 0, "wrong section")}, chorus), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":section_out_of_scope"},
		{name: "untimed comment without extra sections", in: input([]Op{line(1, 0, "x line")}, Addressed{CommentID: "c-untimed", Kind: "note", State: "open", OnLineage: true}),
			wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":comment_unscoped"},
		{name: "untimed comment covered by an extra section", in: func() CompileInput {
			in := input([]Op{line(3, 1, "bridge fixed")}, Addressed{CommentID: "c-untimed", Kind: "note", State: "open", OnLineage: true})
			in.ExtraSections = []int{3}
			return in
		}(), operator: OperatorLyricEdit},
		{name: "spot by label on an untimed base", in: input([]Op{line(3, 0, "bridge line")}, Addressed{CommentID: "c-label", Kind: "fix", State: "regressed", OnLineage: true, Spot: &Spot{Label: "bridge"}}), operator: OperatorLyricEdit},
		{name: "spot that matches no section", in: input([]Op{line(1, 0, "x")}, Addressed{CommentID: "c-nowhere", Kind: "fix", State: "open", OnLineage: true, Spot: &Spot{Kind: "solo", Ordinal: 1}}),
			wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":comment_unscoped"},
		{name: "five sections", in: func() CompileInput {
			in := input([]Op{line(1, 0, "x")}, chorus, verse1, fixComment("c3", "verse", 2), fixComment("c4", "bridge", 1))
			in.ExtraSections = []int{4}
			return in
		}(), wantCode: CodePatchOutOfBounds, wantRule: CodePatchOutOfBounds + ":too_many_sections"},
		{name: "four sections", in: input([]Op{line(1, 0, "x")}, chorus, verse1, fixComment("c3", "verse", 2), fixComment("c4", "bridge", 1)), operator: OperatorLyricEdit},
		{name: "resolved comment", in: input([]Op{line(1, 0, "x")}, Addressed{CommentID: "c-res", Kind: "fix", State: "resolved", OnLineage: true, Spot: &Spot{Kind: "chorus", Ordinal: 1}}),
			wantCode: CodeCommentNotAddressable, wantRule: CodeCommentNotAddressable + ":state"},
		{name: "hidden comment", in: input([]Op{line(1, 0, "x")}, Addressed{CommentID: "c-hid", Kind: "fix", State: "open", Hidden: true, OnLineage: true, Spot: &Spot{Kind: "chorus", Ordinal: 1}}),
			wantCode: CodeCommentNotAddressable, wantRule: CodeCommentNotAddressable + ":hidden"},
		{name: "keep comment", in: input([]Op{line(1, 0, "x")}, Addressed{CommentID: "c-keep", Kind: "keep", State: "open", OnLineage: true, Spot: &Spot{Kind: "chorus", Ordinal: 1}}),
			wantCode: CodeCommentNotAddressable, wantRule: CodeCommentNotAddressable + ":kind"},
		{name: "foreign lineage comment", in: input([]Op{line(1, 0, "x")}, Addressed{CommentID: "c-foreign", Kind: "fix", State: "open", OnLineage: false, Spot: &Spot{Kind: "chorus", Ordinal: 1}}),
			wantCode: CodeCommentNotAddressable, wantRule: CodeCommentNotAddressable + ":lineage"},
		{name: "no-op", in: input([]Op{line(1, 0, "c1 line one"), style("pop, warm, female vocal, italian, 96bpm")}, chorus), wantCode: CodePatchNoChange, wantRule: CodePatchNoChange + ":unchanged"},
		{name: "style reordered only is a no-op", in: input([]Op{style("pop, warm, female vocal, italian, 96bpm")}, chorus), wantCode: CodePatchNoChange, wantRule: CodePatchNoChange + ":unchanged"},
		{name: "reference base", in: func() CompileInput { in := input([]Op{line(1, 0, "x")}, chorus); in.RecipeKnown = false; return in }(), wantCode: CodeRecipeUnknown, wantRule: CodeRecipeUnknown + ":unknown_engine"},
		{name: "no such section", in: input([]Op{line(9, 0, "x")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":no_such_section"},
		{name: "no such line", in: input([]Op{line(1, 4, "x")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":no_such_line"},
		{name: "same line twice", in: input([]Op{line(1, 0, "x"), line(1, 0, "y")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":duplicate_line"},
		{name: "newline in text", in: input([]Op{line(1, 0, "x\ny")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":newline"},
		{name: "empty text", in: input([]Op{line(1, 0, "   ")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":text_length"},
		{name: "long text", in: input([]Op{line(1, 0, strings.Repeat("é", MaxLineRunes+1))}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":text_length"},
		{name: "two style ops", in: input([]Op{style("rock, italian, 96bpm"), style("pop, italian, 96bpm")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":duplicate_style"},
		{name: "empty style", in: input([]Op{style(" ")}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":style_length"},
		{name: "unknown op", in: input([]Op{{Op: "delete_section", SectionIdx: 1}}, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":unknown_op"},
		{name: "no ops", in: input(nil, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":ops_count"},
		{name: "twenty-one ops", in: input(lines(MaxOps+1, 1, "x"), chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":ops_count"},
		{name: "no comments", in: input([]Op{line(1, 0, "x")}), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":addresses_count"},
		{name: "eleven comments", in: func() CompileInput {
			var a []Addressed
			for i := 0; i <= MaxAddressed; i++ {
				a = append(a, Addressed{CommentID: fmt.Sprintf("c%d", i), Kind: "fix", State: "open", OnLineage: true, Spot: &Spot{Kind: "chorus", Ordinal: 1}})
			}
			return input([]Op{line(1, 0, "x")}, a...)
		}(), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":addresses_count"},
		{name: "same comment twice", in: input([]Op{line(1, 0, "x")}, chorus, chorus), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":duplicate_comment"},
		{name: "extra section out of range", in: func() CompileInput {
			in := input([]Op{line(1, 0, "x")}, chorus)
			in.ExtraSections = []int{12}
			return in
		}(), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":no_such_section"},
		{name: "three extra sections", in: func() CompileInput {
			in := input([]Op{line(1, 0, "x")}, chorus)
			in.ExtraSections = []int{0, 2, 3}
			return in
		}(), wantCode: CodePatchInvalid, wantRule: CodePatchInvalid + ":extra_sections_count"},
	}
	if len(cases) < 16 {
		t.Fatalf("only %d rows", len(cases))
	}
	for _, tc := range cases {
		got, err := Compile(tc.in)
		if tc.wantCode == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
				continue
			}
			if got.Operator != tc.operator {
				t.Errorf("%s: operator %q, want %q", tc.name, got.Operator, tc.operator)
			}
			continue
		}
		cs, rules := codes(err)
		if err == nil || len(cs) == 0 || !slices.Contains(rules, tc.wantRule) {
			t.Errorf("%s: violations %v (err %v), want %s", tc.name, rules, err, tc.wantRule)
		}
	}
}

// TestCompile_ReportsEveryViolation: nothing is first-fail.
func TestCompile_ReportsEveryViolation(t *testing.T) {
	t.Parallel()
	in := input([]Op{line(2, 0, "[Chorus 2]"), line(1, 9, "x"), style("rock, dark, male vocal, loud, english, 130bpm")}, fixComment("c1", "chorus", 1),
		Addressed{CommentID: "c2", Kind: "fix", State: "resolved", OnLineage: true})
	_, err := Compile(in)
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	_, rules := codes(err)
	for _, want := range []string{"PATCH_OUT_OF_BOUNDS:section_tag", "PATCH_OUT_OF_BOUNDS:section_out_of_scope", "PATCH_INVALID:no_such_line",
		"PATCH_OUT_OF_BOUNDS:style_terms", "PATCH_OUT_OF_BOUNDS:bpm_delta", "PATCH_OUT_OF_BOUNDS:language_tag", "COMMENT_NOT_ADDRESSABLE:state"} {
		if !slices.Contains(rules, want) {
			t.Errorf("missing %s in %v", want, rules)
		}
	}
	if ce.Code() != CodeCommentNotAddressable {
		t.Errorf("primary code = %s", ce.Code())
	}
	if !strings.Contains(ce.Error(), CodeCommentNotAddressable) {
		t.Errorf("Error() = %q", ce.Error())
	}
}

// TestCompile_Output: the compiled request, the M3 patch, the diff, the scope and the bounds report of a mixed patch.
func TestCompile_Output(t *testing.T) {
	t.Parallel()
	in := input([]Op{line(1, 2, "  a brand new third line "), line(1, 3, "c1 line four"), style("rock, warm, female vocal, italian, 100bpm")}, fixComment("c-chorus", "chorus", 1))
	in.ExtraSections = []int{2}
	got, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operator != OperatorMultiEdit {
		t.Errorf("operator = %s", got.Operator)
	}
	wantLyrics := strings.Replace(baseLyrics, "c1 line three", "a brand new third line", 1)
	if got.Patch.Lyrics == nil || *got.Patch.Lyrics != wantLyrics {
		t.Errorf("lyrics patch:\n%v", got.Patch.Lyrics)
	}
	if got.Patch.Style == nil || *got.Patch.Style != "rock, warm, female vocal, italian, 100bpm" {
		t.Errorf("style patch: %v", got.Patch.Style)
	}
	if got.Request.Lyrics != wantLyrics || got.Request.ID != "base_1" || got.Request.Seed != 7 {
		t.Errorf("request: %+v", got.Request)
	}
	wantDiff := Diff{
		Lyrics: []LineChange{{SectionIdx: 1, LineIdx: 2, Before: "c1 line three", After: "a brand new third line"}}, // the identical line 3 is no change
		Style:  &StyleChange{Before: "pop, warm, female vocal, italian, 96bpm", After: "rock, warm, female vocal, italian, 100bpm", Added: []string{"rock"}, Removed: []string{"pop"}},
	}
	if d := cmp.Diff(wantDiff, got.Diff); d != "" {
		t.Errorf("diff: %s", d)
	}
	wantScope := []ScopedSection{{Idx: 1, Kind: "chorus", Label: "chorus", CommentIDs: []string{"c-chorus"}}, {Idx: 2, Kind: "verse", Label: "verse 2", Extra: true}}
	if d := cmp.Diff(wantScope, got.Scope.Sections); d != "" {
		t.Errorf("scope: %s", d)
	}
	if b := got.Scope.Bounds; b.LinesChanged != 1 || b.SectionsScoped != 2 || b.StyleTermsChanged != 1 || b.BPMDelta != 4 ||
		b.MaxLines != MaxLines || b.MaxSections != MaxScopedSections || b.MaxStyleTerms != MaxStyleTermDelta || b.MaxBPMDelta != MaxBPMDelta {
		t.Errorf("bounds: %+v", b)
	}
	// the base request is not modified
	if baseRequest().Lyrics != baseLyrics || in.Base.Lyrics != baseLyrics {
		t.Error("base modified")
	}
}

// TestCompile_PreservesLineEndings: CRLF lyrics keep their line endings and an implicit first section is addressable.
func TestCompile_PreservesLineEndings(t *testing.T) {
	t.Parallel()
	in := input([]Op{line(0, 1, "second line replaced")}, Addressed{CommentID: "c", Kind: "note", State: "open", OnLineage: true, Spot: &Spot{Label: "other"}})
	in.Base.Lyrics = "first line\r\nsecond line\r\n[Chorus]\r\nhook line"
	got, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first line\r\nsecond line replaced\r\n[Chorus]\r\nhook line"; got.Request.Lyrics != want {
		t.Errorf("lyrics = %q, want %q", got.Request.Lyrics, want)
	}
}

// TestCompile_UnparseableBaseIsRefused: a base the engine protocol rejects cannot be patched into a valid request.
func TestCompile_UnparseableBaseIsRefused(t *testing.T) {
	t.Parallel()
	in := input([]Op{line(1, 0, "ok line")}, fixComment("c", "chorus", 1))
	in.Base.Cot = "bogus"
	_, err := Compile(in)
	if cs, _ := codes(err); !slices.Contains(cs, CodeLintFailed) {
		t.Errorf("err = %v", err)
	}
}
