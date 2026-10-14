package lineage

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func sp(s string) *string { return &s }

// TestModes_Table pins the C71.2 table row by row.
func TestModes_Table(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		op     string
		patch  bool
		rules  [5]Rule // style, lyrics, cot, abc, cfg_scale
		seeds  SeedRule
		parent string
	}{
		ModeNewSeed:      {OpRerollSeed, false, [5]Rule{}, SeedsNew, "required"},
		ModeSameSeedEdit: {OpSameSeedEdit, true, [5]Rule{Optional, Optional, Optional, Optional, Optional}, SeedsParentOnly, "required"},
		ModeLyricEdit:    {OpLyricEdit, true, [5]Rule{Forbidden, Required, Optional, Optional, Forbidden}, SeedsParentNew, "required"},
		ModeStyleEdit:    {OpStyleEdit, true, [5]Rule{Required, Forbidden, Optional, Optional, Optional}, SeedsParentNew, "required"},
		ModeFromDraft:    {OpFromDraft, false, [5]Rule{}, SeedsDraftNew, "optional"},
		ModeComposeNew:   {OpComposeNew, false, [5]Rule{}, SeedsRecipeNew, "none"},
		ModeFromTemplate: {"", false, [5]Rule{}, "", "many"},
	}
	if len(Modes) != len(want) {
		t.Fatalf("%d rows, want %d", len(Modes), len(want))
	}
	for _, m := range Modes {
		w, ok := want[m.Mode]
		if !ok {
			t.Errorf("unexpected mode %q", m.Mode)
			continue
		}
		got := [5]Rule{m.Style, m.Lyrics, m.Cot, m.ABC, m.CFGScale}
		if m.Operator != w.op || m.Patch != w.patch || got != w.rules || m.Seeds != w.seeds || m.Parent != w.parent {
			t.Errorf("%s = %+v, want %+v", m.Mode, m, w)
		}
		if m.Operator != "" && !ValidOperator(m.Operator) {
			t.Errorf("%s: operator %q is not an Operator Ledger operator", m.Mode, m.Operator)
		}
	}
	for _, m := range TemplateModes {
		if s, ok := Spec(m); !ok || s.Parent != "required" {
			t.Errorf("template mode %q is not a variant row", m)
		}
	}
	if _, ok := Spec("nope"); ok {
		t.Error("Spec accepted an unknown mode")
	}
}

func TestCheckPatch(t *testing.T) {
	t.Parallel()
	lyric, _ := Spec(ModeLyricEdit)
	style, _ := Spec(ModeStyleEdit)
	same, _ := Spec(ModeSameSeedEdit)
	newSeed, _ := Spec(ModeNewSeed)
	cases := []struct {
		name        string
		spec        ModeSpec
		patch       *Patch
		field, rule string
	}{
		{"new_seed with patch", newSeed, &Patch{Lyrics: sp("x")}, "patch", "patch_forbidden"},
		{"new_seed no patch", newSeed, nil, "", ""},
		{"lyric_edit missing patch", lyric, nil, "patch", "patch_required"},
		{"lyric_edit lyrics ok", lyric, &Patch{Lyrics: sp("x")}, "", ""},
		{"lyric_edit style forbidden", lyric, &Patch{Lyrics: sp("x"), Style: sp("y")}, "patch.style", "style_forbidden"},
		{"lyric_edit cfg forbidden", lyric, &Patch{Lyrics: sp("x"), CFGScale: OptNumber{Set: true, Value: 2}}, "patch.cfg_scale", "cfg_scale_forbidden"},
		{"lyric_edit lyrics required", lyric, &Patch{Cot: sp("off")}, "patch.lyrics", "lyrics_required"},
		{"style_edit lyrics forbidden", style, &Patch{Style: sp("y"), Lyrics: sp("z")}, "patch.lyrics", "lyrics_forbidden"},
		{"style_edit abc null ok", style, &Patch{Style: sp("y"), ABC: OptString{Set: true, Null: true}}, "", ""},
		{"same_seed_edit empty is a re-render", same, nil, "", ""},
		{"same_seed_edit empty patch", same, &Patch{}, "", ""},
	}
	for _, c := range cases {
		f, r := c.spec.CheckPatch(c.patch)
		if f != c.field || r != c.rule {
			t.Errorf("%s: (%q, %q), want (%q, %q)", c.name, f, r, c.field, c.rule)
		}
	}
}

func TestApply_Patch(t *testing.T) {
	t.Parallel()
	abc := "X:1\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|]\n"
	cfg := 3.0
	base := Request{ID: "syn-a", Style: "pop, 100 bpm", Lyrics: "[Verse 1]\nla la", Cot: "full", Seed: 1100, ABC: &abc, CFGScale: &cfg}
	p := Params{Mode: ModeStyleEdit, SeedPolicy: "parent", NSeeds: 1, Seed: 1100, ID: "syn-a-s1100"}

	t.Run("abc null removes, cfg_scale null removes", func(t *testing.T) {
		got, err := Apply(base, OpStyleEdit, p, &Patch{Style: sp("rock"), ABC: OptString{Set: true, Null: true}, CFGScale: OptNumber{Set: true, Null: true}})
		if err != nil {
			t.Fatal(err)
		}
		want := Request{ID: "syn-a-s1100", Style: "rock", Lyrics: base.Lyrics, Cot: "full", Seed: 1100}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("-want +got:\n%s", diff)
		}
	})
	t.Run("lyrics replaced, base untouched", func(t *testing.T) {
		got, err := Apply(base, OpLyricEdit, Params{Seed: 1101, ID: "syn-a-s1101"}, &Patch{Lyrics: sp("[Chorus]\nnew words")})
		if err != nil {
			t.Fatal(err)
		}
		if got.Lyrics != "[Chorus]\nnew words" || got.Seed != 1101 || got.ID != "syn-a-s1101" || got.ABC == nil || *got.ABC != abc {
			t.Errorf("got %+v", got)
		}
		if base.Lyrics != "[Verse 1]\nla la" || base.Seed != 1100 {
			t.Error("the base request was mutated")
		}
	})
	t.Run("cot change is re-validated", func(t *testing.T) {
		_, err := Apply(base, OpSameSeedEdit, p, &Patch{Cot: sp("off")}) // abc needs cot melody|full
		var pe *ParseError
		if !asParseError(err, &pe) || pe.Code != "abc_requires_cot" {
			t.Fatalf("err = %v, want abc_requires_cot", err)
		}
		got, err := Apply(base, OpSameSeedEdit, p, &Patch{Cot: sp("off"), ABC: OptString{Set: true, Null: true}})
		if err != nil || got.Cot != "off" || got.ABC != nil {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("empty patch is a re-render", func(t *testing.T) {
		a, err := ApplyParsed(base, OpSameSeedEdit, Params{Seed: base.Seed}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, render := Keys(a.Canonical)
		b, _ := ApplyParsed(base, OpSameSeedEdit, Params{Seed: base.Seed}, &Patch{})
		_, render2 := Keys(b.Canonical)
		if render != render2 || a.Request.ID != "syn-a" {
			t.Error("an empty patch changed the render")
		}
	})
	t.Run("operators", func(t *testing.T) {
		for _, op := range []string{"inferred_sibling", "nope", ""} {
			if _, err := Apply(base, op, p, nil); err == nil {
				t.Errorf("operator %q accepted", op)
			}
		}
	})
}

func TestIsEmptyAndLint(t *testing.T) {
	t.Parallel()
	var nilPatch *Patch
	if !nilPatch.IsEmpty() || !(&Patch{}).IsEmpty() || (&Patch{Style: sp("x")}).IsEmpty() || (&Patch{ABC: OptString{Set: true}}).IsEmpty() {
		t.Error("IsEmpty wrong")
	}
	if _, err := ParseRequest([]byte(`{"id":"a","style":"pop","lyrics":"[Verse]\nx","cot":"none"}`), false); err == nil {
		t.Error("cot none accepted")
	}
	p, err := ParseRequest([]byte(`{"id":"a","style":"pop","lyrics":"[Verse]\nx","cot":"full","seed":5,"title":"T"}`), true)
	if err != nil || p.Title != "T" {
		t.Errorf("title: %+v %v", p, err)
	}
	errs, _ := Lint(p.Canonical)
	_ = errs
}
