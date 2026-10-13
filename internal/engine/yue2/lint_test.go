package yue2

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func mkReq(id, style, lyrics string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"id": id, "style": style, "lyrics": lyrics, "cot": "full", "seed": 7})
	return b
}

const goodVerse = "oro kala\nvenu tilo\nmira dosa\nlano pera\n"

func warnCodes(r LintResult) []string {
	var out []string
	for _, w := range r.Warnings {
		out = append(out, w.Code)
	}
	sort.Strings(out)
	return out
}

func lint1(raw json.RawMessage) LintResult {
	return Lint([]LintItem{{Request: raw}}, true, LintContext{}).Items[0]
}

func TestLint_ProtocolRules(t *testing.T) {
	// a title key is a dropped-key warning, not an error
	raw, _ := json.Marshal(map[string]any{"id": "a", "style": "english", "lyrics": "[Verse]\n" + goodVerse, "cot": "full", "seed": 7, "title": "x"})
	rep := Lint([]LintItem{{Request: raw}}, true, LintContext{})
	if !rep.OK || len(rep.Items[0].Errors) != 0 {
		t.Fatalf("title: %+v", rep)
	}
	found := false
	for _, w := range rep.Items[0].Warnings {
		if w.Code == "dropped_key:title" && w.Severity == "warning" && w.Field == "title" {
			found = true
		}
	}
	if !found {
		t.Errorf("no dropped_key:title in %+v", rep.Items[0].Warnings)
	}
	// duplicate ids in a batch
	a := mkReq("same", "english", "[Verse]\n"+goodVerse)
	b := mkReq("same", "english, pop", "[Verse]\n"+goodVerse)
	rep = Lint([]LintItem{{Request: a}, {Request: b}}, true, LintContext{})
	if rep.OK || len(rep.BatchErrors) != 1 || rep.BatchErrors[0].Code != "duplicate_id" || rep.BatchErrors[0].Line != 2 {
		t.Errorf("batch dup: %+v", rep.BatchErrors)
	}
	if rep := Lint([]LintItem{{Request: a}, {Request: b}}, false, LintContext{}); !rep.OK || len(rep.BatchErrors) != 0 {
		t.Errorf("no batch flag: %+v", rep)
	}
	// ok is false iff any error
	bad := json.RawMessage(`{"id":"b","style":"s","lyrics":"l","seed":9007199254740992}`)
	rep = Lint([]LintItem{{Request: a}, {Request: bad}}, true, LintContext{})
	if rep.OK || rep.Items[1].Errors[0].Code != "seed_out_of_range" || len(rep.Items[0].Errors) != 0 {
		t.Errorf("seed: %+v", rep)
	}
	// an invalid item gets no songwriting warnings and no duration
	if len(rep.Items[1].Warnings) != 0 || rep.Items[1].EstimatedDurationS != 0 || rep.Items[1].Hash != "" {
		t.Errorf("invalid item decorated: %+v", rep.Items[1])
	}
	if rep.Items[1].RequestID != "b" || rep.Items[1].Index != 1 {
		t.Errorf("index/id: %+v", rep.Items[1])
	}
	// non-slices are never nil (JSON arrays)
	if rep.Items[0].Errors == nil || rep.Items[0].Warnings == nil || rep.BatchErrors == nil {
		t.Error("nil slices")
	}
}

func TestLint_SongwritingRules(t *testing.T) {
	long := strings.Repeat("[Verse]\n"+goodVerse, 60)
	rows := []struct {
		name    string
		style   string
		lyrics  string
		want    []string
		code    string // the code whose position is checked
		line    int
		section string
	}{
		{"clean", "english, 100 bpm", "[Verse]\n" + goodVerse + "[Chorus]\n" + goodVerse, nil, "", 0, ""},
		{"no_sections", "english", goodVerse, []string{"no_sections"}, "no_sections", 0, ""},
		{"unknown_section_tag", "english", "[Verse]\n" + goodVerse + "[Refrain]\n" + goodVerse, []string{"unknown_section_tag"}, "unknown_section_tag", 6, "other"},
		{"non_sung_text paren", "english", "[Verse]\noro kala\nvenu (repeat 2x)\nmira dosa\nlano pera\n", []string{"non_sung_text"}, "non_sung_text", 3, "verse"},
		{"non_sung_text x2", "english", "[Verse]\noro kala\nvenu tilo\nmira dosa\nlano pera x2\n", []string{"non_sung_text"}, "non_sung_text", 5, "verse"},
		{"non_sung_text bis", "english", "[Verse]\noro kala\nbis\nmira dosa\nlano pera\n", []string{"non_sung_text"}, "non_sung_text", 3, "verse"},
		{"repeat_marker header", "english", "[Verse]\n" + goodVerse + "[Chorus x2]\n" + goodVerse, []string{"repeat_marker"}, "repeat_marker", 6, "chorus"},
		{"repeat_marker trailing", "english", "[Verse]\n" + goodVerse + "[Chorus] (repeat)\n" + goodVerse, []string{"repeat_marker"}, "repeat_marker", 6, "chorus"},
		{"repeat_not_written_out", "english", "[Chorus]\n" + goodVerse + "[Verse]\n" + goodVerse + "[Chorus]\n", []string{"repeat_not_written_out"}, "repeat_not_written_out", 11, "chorus 2"},
		{"empty_section", "english", "[Verse]\n" + goodVerse + "[Bridge]\n", []string{"empty_section"}, "empty_section", 6, "bridge"},
		{"empty_chorus_first", "english", "[Chorus]\n[Verse]\n" + goodVerse, []string{"empty_section"}, "empty_section", 1, "chorus"},
		{"exempt empties", "english", "[Intro]\n[Verse]\n" + goodVerse + "[Interlude]\n[Solo]\n[Instrumental]\n[Outro]\n", nil, "", 0, ""},
		{"too few lines", "english", "[Verse]\noro\nkala\nvenu\n", []string{"section_line_count"}, "section_line_count", 1, "verse"},
		{"too many lines", "english", "[Chorus]\n" + goodVerse + goodVerse, []string{"section_line_count"}, "section_line_count", 1, "chorus"},
		{"intro lines not counted", "english", "[Intro]\noro\n[Verse]\n" + goodVerse, nil, "", 0, ""},
		{"duration_over_cap", "english", long, []string{"duration_over_cap"}, "duration_over_cap", 0, ""},
		{"bpm high", "english, 200 bpm", "[Verse]\n" + goodVerse, []string{"bpm_out_of_range"}, "bpm_out_of_range", 0, ""},
		{"bpm low", "english, 50 bpm", "[Verse]\n" + goodVerse, []string{"bpm_out_of_range"}, "bpm_out_of_range", 0, ""},
		{"missing_language_tag", "pop, 100 bpm", "[Verse]\n" + goodVerse, []string{"missing_language_tag"}, "missing_language_tag", 0, ""},
		{"language_mismatch", "english", "[Verse]\n" + strings.Repeat("il lo gli una che non per con sono\n", 4), []string{"italian_unofficial", "language_mismatch"}, "language_mismatch", 0, ""},
		{"italian tagged", "italiano, pop", "[Verse]\n" + goodVerse, []string{"italian_unofficial"}, "italian_unofficial", 0, ""},
		{"tag synonym", "Español", "[Verse]\n" + goodVerse, nil, "", 0, ""},
	}
	for _, tc := range rows {
		res := lint1(mkReq("t1", tc.style, tc.lyrics))
		if len(res.Errors) != 0 {
			t.Errorf("%s: errors %+v", tc.name, res.Errors)
			continue
		}
		got := warnCodes(res)
		if !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("%s: warnings %v, want %v", tc.name, got, tc.want)
			continue
		}
		if tc.code == "" {
			continue
		}
		for _, w := range res.Warnings {
			if w.Code == tc.code {
				if w.Severity != "warning" || w.Line != tc.line || w.Section != tc.section || w.Message == "" {
					t.Errorf("%s: %+v, want line %d section %q", tc.name, w, tc.line, tc.section)
				}
			}
		}
	}
	// languages reported
	res := lint1(mkReq("t1", "english", "[Verse]\n"+strings.Repeat("il lo gli una che non per con sono\n", 4)))
	if res.Language.Detected != "it" || res.Language.Tagged != "en" {
		t.Errorf("language %+v", res.Language)
	}
	if res.EstimatedDurationS <= 0 || res.DurationBasis != "lyrics_default_bpm" || len(res.Hash) != 64 {
		t.Errorf("result %+v", res)
	}
}

type knownFake struct {
	hash, where, selfDraft, selfRecipe string
}

func (k knownFake) fn(hash, selfDraftID, selfRecipeID string) (string, bool) {
	if hash != k.hash {
		return "", false
	}
	if (k.selfDraft != "" && selfDraftID == k.selfDraft) || (k.selfRecipe != "" && selfRecipeID == k.selfRecipe) {
		return "", false
	}
	return k.where, true
}

func TestLint_DuplicateRecipe(t *testing.T) {
	raw := mkReq("dup", "english", "[Verse]\n"+goodVerse)
	p, err := ParseRequest(raw, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	k := knownFake{hash: p.Hash, where: "recipe", selfRecipe: "r1"}
	ctx := LintContext{KnownHash: k.fn}
	res := Lint([]LintItem{{Request: raw}}, true, ctx).Items[0]
	if got := warnCodes(res); !reflect.DeepEqual(got, []string{"duplicate_recipe"}) || !strings.Contains(res.Warnings[0].Message, "recipe") {
		t.Fatalf("known hash: %+v", res.Warnings)
	}
	// an unedited imported draft is not a duplicate of its own source recipe, nor of itself
	if res := Lint([]LintItem{{Request: raw, DraftID: "d1", SourceRecipeID: "r1"}}, true, ctx).Items[0]; len(res.Warnings) != 0 {
		t.Errorf("own recipe flagged: %+v", res.Warnings)
	}
	k2 := knownFake{hash: p.Hash, where: "draft:d1", selfDraft: "d1"}
	if res := Lint([]LintItem{{Request: raw, DraftID: "d1"}}, true, LintContext{KnownHash: k2.fn}).Items[0]; len(res.Warnings) != 0 {
		t.Errorf("own draft flagged: %+v", res.Warnings)
	}
	if res := Lint([]LintItem{{Request: raw, DraftID: "d2"}}, true, LintContext{KnownHash: k2.fn}).Items[0]; len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "draft:d1") {
		t.Errorf("other draft: %+v", res.Warnings)
	}
	// an earlier item of the same call
	rep := Lint([]LintItem{{Request: raw}, {Request: raw}}, false, LintContext{})
	if len(rep.Items[0].Warnings) != 0 || warnCodes(rep.Items[1])[0] != "duplicate_recipe" {
		t.Errorf("earlier item: %+v", rep.Items)
	}
}

func TestLint_RunsOnCanonical(t *testing.T) {
	line := []byte(`{"id":"u1","style":"english","lyrics":"[Verse]\n` + strings.ReplaceAll(goodVerse, "\n", `\n`) + `","cot":"melody","seed":3,"abc_path":"x.abc","extra":1}`)
	abc := string(abcDoc("M:4/4\nQ:1/4=120\nK:C", bars(4)))
	p, err := ParseRequest(line, ParseOptions{ResolveABCPath: func(string) ([]byte, error) { return []byte(abc), nil }})
	if err != nil {
		t.Fatal(err)
	}
	var carry []Issue
	for _, is := range CheckRequest(line, ModeApp, func(string) ([]byte, error) { return []byte(abc), nil }) {
		if is.Severity == "warning" {
			carry = append(carry, is)
		}
	}
	rep := Lint([]LintItem{{Request: p.Canonical, Carry: carry}}, true, LintContext{})
	res := rep.Items[0]
	if len(res.Errors) != 0 || !rep.OK {
		t.Fatalf("canonical lint: %+v", res)
	}
	n := 0
	for _, w := range res.Warnings {
		if w.Code == "dropped_key:extra" {
			n++
		}
	}
	if n != 1 || res.DurationBasis != "abc" {
		t.Errorf("carry warnings %d basis %q: %+v", n, res.DurationBasis, res.Warnings)
	}
	// the same line sent to lint_recipe cannot resolve its abc_path
	raw := Lint([]LintItem{{Request: line}}, true, LintContext{}).Items[0]
	if len(raw.Errors) != 1 || raw.Errors[0].Code != "abc_path_unresolved" {
		t.Errorf("raw line: %+v", raw.Errors)
	}
}
