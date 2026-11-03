package registry_test

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/lineage"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

const goldenPath = "testdata/recipe_yue2.golden.json"

// goldenRow is every byte a yue2 recipe contributes to the library: canonical request, both hashes and the
// two recipe keys, or the stable error code.
type goldenRow struct {
	Case      string `json:"case"`
	Canonical string `json:"canonical,omitempty"`
	Hash      string `json:"sha_canonical,omitempty"`
	CoreHash  string `json:"sha_core,omitempty"`
	Content   string `json:"sha_content,omitempty"`
	Render    string `json:"sha_render,omitempty"`
	Err       string `json:"err,omitempty"`
}

func sp(s string) *string { return &s }

type goldenCase struct {
	name  string
	raw   string
	op    string
	seed  int64
	id    string
	patch *lineage.Patch
}

const (
	lyr  = "[verse]\nfirst line of the verse\nsecond line of the verse\n\n[chorus]\nthe chorus line\nanother chorus line"
	base = `{"id":"syn-a","style":"warm pop female vocal","lyrics":"[verse]\nfirst line of the verse\nsecond line of the verse\n\n[chorus]\nthe chorus line\nanother chorus line","cot":"off","seed":7}`
)

func goldenCases() []goldenCase {
	abc := "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=100\nK:C\nCDEF|"
	return []goldenCase{
		{name: "parse_off", raw: base, op: ""},
		{name: "parse_full", raw: `{"id":"syn-b","style":"rock","lyrics":"` + `[verse]\nx y z\n\n[chorus]\na b c` + `","cot":"full","seed":9,"cfg_scale":1.5}`},
		{name: "parse_unicode", raw: `{"id":"syn-c","style":"cantautore italiano","lyrics":"[verse]\nciao città è bello\n\n[chorus]\nsì sì sì","cot":"off","seed":1}`},
		{name: "parse_bad", raw: `{"id":"syn-d"}`},
		{name: "parse_abc", raw: `{"id":"syn-e","style":"jazz","lyrics":"[verse]\nx y z\n\n[chorus]\na b c","cot":"melody","seed":3,"abc":` + string(mustJSON(abc)) + `}`},
		{name: "reroll", raw: base, op: lineage.OpRerollSeed, seed: 4242, id: "syn-a-s4242"},
		{name: "same_seed_style", raw: base, op: lineage.OpSameSeedEdit, seed: 7, id: "syn-a-s7", patch: &lineage.Patch{Style: sp("dark synth")}},
		{name: "lyric_edit", raw: base, op: lineage.OpLyricEdit, seed: 7, id: "syn-a-s7", patch: &lineage.Patch{Lyrics: sp("[verse]\nnew words here\n\n[chorus]\nnew chorus here")}},
		{name: "style_edit_cot", raw: base, op: lineage.OpStyleEdit, seed: 8, id: "syn-a-s8", patch: &lineage.Patch{Style: sp("ambient"), Cot: sp("full")}},
		{name: "cfg_set", raw: base, op: lineage.OpSameSeedEdit, seed: 7, id: "syn-a-s7", patch: &lineage.Patch{CFGScale: lineage.OptNumber{Set: true, Value: 2.25}}},
		{name: "abc_set", raw: base, op: lineage.OpSameSeedEdit, seed: 7, id: "syn-a-s7", patch: &lineage.Patch{Cot: sp("melody"), ABC: lineage.OptString{Set: true, Value: abc}}},
		{name: "abc_remove", raw: `{"id":"syn-e","style":"jazz","lyrics":"[verse]\nx y z\n\n[chorus]\na b c","cot":"melody","seed":3,"abc":` + string(mustJSON(abc)) + `}`, op: lineage.OpSameSeedEdit, seed: 3, id: "syn-e", patch: &lineage.Patch{Cot: sp("off"), ABC: lineage.OptString{Set: true, Null: true}}},
		{name: "from_draft", raw: base, op: lineage.OpFromDraft, seed: 11, id: "syn-a-s11"},
		{name: "compose_new", raw: base, op: lineage.OpComposeNew, seed: 7, id: "syn-a"},
		{name: "bad_operator", raw: base, op: lineage.OpInferredSibling, seed: 7, id: "x"},
		{name: "bad_seed", raw: base, op: lineage.OpRerollSeed, seed: -1, id: "x"},
		{name: "bad_cot", raw: base, op: lineage.OpStyleEdit, seed: 7, id: "x", patch: &lineage.Patch{Cot: sp("nope")}},
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func errCode(err error) string {
	if err == nil {
		return ""
	}
	if pe, ok := err.(interface{ Error() string }); ok {
		return pe.Error()
	}
	return "error"
}

// legacyRows computes the rows through the pre-seam entry points (lineage.ParseRequest, ApplyParsed, Keys).
func legacyRows(t *testing.T) []goldenRow {
	t.Helper()
	var rows []goldenRow
	for _, c := range goldenCases() {
		row := goldenRow{Case: c.name}
		var err error
		if c.op == "" {
			p, perr := lineage.ParseRequest([]byte(c.raw), false)
			err = perr
			if err == nil {
				row.Canonical, row.Hash, row.CoreHash = string(p.Canonical), p.Hash, p.CoreHash
				row.Content, row.Render = lineage.Keys(p.Canonical)
			}
		} else {
			base, perr := lineage.RequestOf([]byte(c.raw))
			if perr != nil {
				t.Fatalf("%s: base: %v", c.name, perr)
			}
			p, aerr := lineage.ApplyParsed(base, c.op, lineage.Params{Seed: c.seed, ID: c.id}, c.patch)
			err = aerr
			if err == nil {
				row.Canonical, row.Hash, row.CoreHash = string(p.Canonical), p.Hash, p.CoreHash
				row.Content, row.Render = lineage.Keys(p.Canonical)
			}
		}
		row.Err = errCode(err)
		rows = append(rows, row)
	}
	return rows
}

func TestRecipeGolden_LegacyPath(t *testing.T) {
	rows := legacyRows(t)
	got, _ := json.MarshalIndent(rows, "", " ")
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Fatalf("legacy recipe path drifted from %s (run -update only on the pre-seam commit)", goldenPath)
	}
}
