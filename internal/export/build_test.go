package export_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/export"
)

// fakeSource is an in-memory export.Source.
type fakeSource struct {
	rows    map[string][]export.SourceRow
	recipes []export.RecipeRow
	drafts  []export.DraftRow
	styles  [][]byte
}

func (f *fakeSource) JSONLSourceRows(_ context.Context, p string) ([]export.SourceRow, error) {
	r, ok := f.rows[p]
	if !ok {
		return nil, export.ErrUnknownSource
	}
	return r, nil
}
func (f *fakeSource) SelectRecipes(context.Context, export.TrackFilter) ([]export.RecipeRow, error) {
	return f.recipes, nil
}
func (f *fakeSource) SelectDrafts(context.Context, export.DraftFilter) ([]export.DraftRow, error) {
	return f.drafts, nil
}
func (f *fakeSource) StyleSamples(context.Context) ([][]byte, error) { return f.styles, nil }

const run1, run2 = "01900000-0000-7000-8000-000000000001", "01900000-0000-7000-8000-000000000002"

// fileSource builds a source whose single path "music/songs.jsonl" holds the lines of file.
func fileSource(file string) *fakeSource {
	f := &fakeSource{rows: map[string][]export.SourceRow{}}
	for i, ln := range strings.Split(strings.TrimSuffix(file, "\n"), "\n") {
		f.rows["music/songs.jsonl"] = append(f.rows["music/songs.jsonl"], export.SourceRow{LineNo: i + 1, Raw: []byte(ln), ImportRunID: run1, RecipeID: fmt.Sprint("r", i)})
		f.styles = append(f.styles, []byte(ln))
	}
	return f
}

func req(id string, seed int64) yue2.Request {
	return yue2.Request{ID: id, Style: "pop, synth", Lyrics: "[verse]\nla la", Cot: "full", Seed: seed}
}

func rawLine(r yue2.Request) []byte { return export.EncodeRequest(r, export.DefaultStyle()) }

var source = export.Selection{Kind: "source", SourcePath: "music/songs.jsonl"}

func TestExport_SourceByteIdentical(t *testing.T) {
	lines := []string{
		string(rawLine(yue2.Request{ID: "syn-1", Style: "città, è", Lyrics: "[verse]\nsono là", Cot: "full", Seed: 1})),
		string(rawLine(yue2.Request{ID: "syn-2", Style: "pop", Lyrics: "[verse]\r\nx", Cot: "full", Seed: 2})) + "\r",
		string(rawLine(yue2.Request{ID: "syn-3", Style: "<b>&", Lyrics: "[verse]\n</b>&amp;", Cot: "full", Seed: 3})),
		`{"id":"syn-4","style":"pop","lyrics":"[verse]\nla 🎵","cot":"full","seed":4}`,
		`{ "seed": 5 , "id": "syn-5", "style": "x", "lyrics": "[verse]\ny", "cot": "off" }`,
	}
	file := strings.Join(lines, "\n") + "\n"
	res, err := export.Build(context.Background(), fileSource(file), source)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.JSONL) != file {
		t.Fatalf("bytes differ:\n%q\nwant\n%q", res.JSONL, file)
	}
	if len(res.Rows) != 5 || !res.Rows[0].Raw {
		t.Errorf("rows %+v", res.Rows)
	}
	for _, w := range res.Warnings {
		t.Errorf("unexpected warning %+v", w)
	}
}

func TestExport_SourceReportsOriginalIssues(t *testing.T) {
	a, b, c, d := rawLine(req("dup", 1)), rawLine(req("u1", 2)), rawLine(req("dup", 3)), rawLine(req("u2", 4))
	file := string(bytes.Join([][]byte{a, b, c, d}, []byte("\n"))) + "\n"
	src := fileSource(file)
	res, err := export.Build(context.Background(), src, source)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.JSONL) != file {
		t.Fatal("bytes differ")
	}
	if len(res.Validation.Issues) != 1 || res.Validation.Issues[0].Code != "duplicate_id" || res.Validation.Issues[0].Line != 3 || res.Validation.OK {
		t.Fatalf("validation %+v", res.Validation)
	}
	sel := source
	sel.ForBatch = true
	fb, err := export.Build(context.Background(), src, sel)
	if err != nil {
		t.Fatal(err)
	}
	if !fb.Validation.OK {
		t.Fatalf("for_batch validation %+v", fb.Validation)
	}
	lines := strings.Split(strings.TrimSuffix(string(fb.JSONL), "\n"), "\n")
	if lines[1] != string(b) || lines[3] != string(d) {
		t.Error("unique-id rows must stay raw")
	}
	if !strings.Contains(lines[0], `"dup-s1"`) || !strings.Contains(lines[2], `"dup-s3"`) {
		t.Errorf("renamed lines %q", lines)
	}
	if fb.Rows[0].RenamedFrom != "dup" || fb.Rows[0].Raw || !fb.Rows[1].Raw {
		t.Errorf("rows %+v", fb.Rows)
	}
}

func TestExport_SourceSuperseded(t *testing.T) {
	l1, l2old, l2new, l3 := rawLine(req("a", 1)), rawLine(req("b", 2)), rawLine(req("b", 22)), rawLine(req("c", 3))
	src := &fakeSource{rows: map[string][]export.SourceRow{"music/songs.jsonl": {
		{LineNo: 1, Raw: l1, ImportRunID: run1}, {LineNo: 2, Raw: l2old, ImportRunID: run1}, {LineNo: 2, Raw: l2new, ImportRunID: run2},
		{LineNo: 3, Raw: l3, ImportRunID: run1},
	}}}
	res, err := export.Build(context.Background(), src, source)
	if err != nil {
		t.Fatal(err)
	}
	want := string(l1) + "\n" + string(l2new) + "\n" + string(l3) + "\n"
	if string(res.JSONL) != want {
		t.Fatalf("got %q want %q", res.JSONL, want)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "source_line_superseded" ||
		!slices.Equal(res.Warnings[0].Details["lines"].([]int), []int{2}) {
		t.Fatalf("warnings %+v", res.Warnings)
	}
	if _, err := export.Build(context.Background(), src, export.Selection{Kind: "source", SourcePath: "nope"}); !errors.Is(err, export.ErrUnknownSource) {
		t.Errorf("unknown path: %v", err)
	}
}

func recipe(r yue2.Request, raws ...export.RawSource) export.RecipeRow {
	return export.RecipeRow{RecipeID: "rec-" + r.ID + fmt.Sprint(r.Seed), Hash: fmt.Sprintf("h-%s-%d", r.ID, r.Seed), RequestID: r.ID,
		Request: r, FirstRecordedAt: "2026-10-01T10:00:00Z", JSONLSources: raws}
}

func TestExport_TracksRawRows(t *testing.T) {
	a, b, c := req("t-a", 1), req("t-b", 2), req("t-c", 3)
	rawA := rawLine(a)
	withTitle := append(bytes.TrimSuffix(rawLine(b), []byte("}")), []byte(`, "title": "x"}`)...)
	src := &fakeSource{
		styles: [][]byte{[]byte(`{"cot": "full", "id": "q", "lyrics": "x", "seed": 1, "style": "é"}`)},
		recipes: []export.RecipeRow{
			recipe(c), // json_file only: regenerated
			recipe(b, export.RawSource{Path: "music/songs.jsonl", LineNo: 2, Raw: withTitle, ImportRunID: run1}),
			recipe(a, export.RawSource{Path: "music/songs.jsonl", LineNo: 1, Raw: rawA, ImportRunID: run1}),
		},
	}
	res, err := export.Build(context.Background(), src, export.Selection{Kind: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	// order: sourced recipes by (path, line), then the others
	var ids []string
	for _, r := range res.Rows {
		ids = append(ids, r.RequestID)
	}
	if !slices.Equal(ids, []string{"t-a", "t-b", "t-c"}) {
		t.Fatalf("order %v", ids)
	}
	if !res.Rows[0].Raw || !bytes.Equal(res.Rows[0].Bytes, rawA) {
		t.Errorf("row 0 must be the raw bytes: %q", res.Rows[0].Bytes)
	}
	if res.Rows[1].Raw || bytes.Contains(res.Rows[1].Bytes, []byte("title")) {
		t.Errorf("row with an extra key must be regenerated: %q", res.Rows[1].Bytes)
	}
	if res.Rows[2].Raw || !bytes.Contains(res.Rows[2].Bytes, []byte(`"id": "t-c"`)) {
		t.Errorf("json_file recipe must be regenerated in the detected style: %q", res.Rows[2].Bytes)
	}
	if !res.Validation.OK {
		t.Errorf("validation %+v", res.Validation)
	}
	var warn *export.Note
	for i, w := range res.Warnings {
		if w.Code == "raw_not_batch_valid" {
			warn = &res.Warnings[i]
		}
	}
	if warn == nil || warn.Details["count"] != 1 {
		t.Errorf("warnings %+v", res.Warnings)
	}
}

func TestExport_ABCPathBundle(t *testing.T) {
	abc := "X:1\nT:t\nK:C\nCDEF|]\n"
	r := req("abc-1", 5)
	r.ABC = &abc
	raw := []byte(`{"id": "abc-1", "style": "pop, synth", "lyrics": "[verse]\nla la", "cot": "melody", "seed": 5, "abc_path": "abc/one.abc"}`)
	r.Cot = "melody"
	src := &fakeSource{recipes: []export.RecipeRow{recipe(r, export.RawSource{Path: "music/songs.jsonl", LineNo: 1, Raw: raw, ImportRunID: run1})}}
	res, err := export.Build(context.Background(), src, export.Selection{Kind: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rows[0].Raw || res.Rows[0].ABCPath != "abc/one.abc" || string(res.ABCFiles["abc/one.abc"]) != abc {
		t.Fatalf("rows %+v files %v", res.Rows, res.ABCFiles)
	}
	if !res.Validation.OK {
		t.Errorf("validation %+v", res.Validation)
	}
	// without the inlined abc the raw cannot resolve: regenerated
	r2 := r
	r2.ABC = nil
	r2.Cot = "full"
	res, err = export.Build(context.Background(), &fakeSource{recipes: []export.RecipeRow{recipe(r2, export.RawSource{Path: "music/songs.jsonl", LineNo: 1, Raw: raw, ImportRunID: run1})}}, export.Selection{Kind: "tracks"})
	if err != nil || res.Rows[0].Raw || len(res.ABCFiles) != 0 {
		t.Fatalf("unresolvable abc_path: %+v %v", res.Rows, err)
	}
}

func TestExport_ForBatchSiblingsUniqueIDs(t *testing.T) {
	long := strings.Repeat("x", 180)
	src := &fakeSource{recipes: []export.RecipeRow{
		recipe(req("syn-a", 1)), recipe(req("syn-a", 2)), recipe(req("syn-a", 3)),
	}}
	extra := req("syn-a", 1) // a fourth recipe, same id and seed, other style
	extra.Style = "other style"
	src.recipes = append(src.recipes, recipe(extra))
	src.recipes[3].Hash = "h-extra"
	src.recipes = append(src.recipes, recipe(req(long, 7)), recipe(req(long, 8)), recipe(req("solo", 9)))
	res, err := export.Build(context.Background(), src, export.Selection{Kind: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res.Rows {
		ids = append(ids, r.RequestID)
	}
	want := []string{"syn-a-s1", "syn-a-s2", "syn-a-s3", "syn-a-s1-2", long[:180-len("-s7")] + "-s7", long[:180-len("-s8")] + "-s8", "solo"}
	slices.Sort(want)
	slices.Sort(ids)
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Fatalf("ids (-want +got):\n%s", diff)
	}
	if !res.Validation.OK {
		t.Errorf("validation %+v", res.Validation)
	}
	for _, r := range res.Rows {
		if len(r.RequestID) > 180 {
			t.Errorf("id of %d chars", len(r.RequestID))
		}
		if (r.RequestID == "solo") == (r.RenamedFrom != "") {
			t.Errorf("row %s renamed from %q", r.RequestID, r.RenamedFrom)
		}
	}
}

func TestExport_TracksAlwaysUnique(t *testing.T) {
	src := &fakeSource{recipes: []export.RecipeRow{recipe(req("same", 11)), recipe(req("same", 12))}}
	res, err := export.Build(context.Background(), src, export.Selection{Kind: "tracks"}) // ForBatch is false
	if err != nil || !res.Validation.OK {
		t.Fatalf("%+v %v", res.Validation, err)
	}
	if res.Rows[0].RequestID != "same-s11" || res.Rows[1].RenamedFrom != "same" {
		t.Errorf("rows %+v", res.Rows)
	}
}

func TestExport_DraftsSelection(t *testing.T) {
	src := &fakeSource{drafts: []export.DraftRow{
		{DraftID: "d2", GroupKey: "g2", RequestID: "b_v1", Request: req("b_v1", 2)},
		{DraftID: "d1", GroupKey: "g1", RequestID: "a_v2", Request: req("a_v2", 1)},
		{DraftID: "d3", GroupKey: "g1", RequestID: "a_v1", Request: req("a_v1", 1)},
	}}
	res, err := export.Build(context.Background(), src, export.Selection{Kind: "drafts"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range res.Rows {
		ids = append(ids, r.RequestID)
		if r.Raw {
			t.Error("drafts are never raw")
		}
	}
	if !slices.Equal(ids, []string{"a_v1", "a_v2", "b_v1"}) || !res.Validation.OK {
		t.Errorf("ids %v validation %+v", ids, res.Validation)
	}
}

func TestExport_Empty(t *testing.T) {
	for _, k := range []string{"tracks", "drafts"} {
		if _, err := export.Build(context.Background(), &fakeSource{}, export.Selection{Kind: k}); !errors.Is(err, export.ErrEmpty) {
			t.Errorf("%s: %v", k, err)
		}
	}
	if _, err := export.Build(context.Background(), &fakeSource{}, export.Selection{Kind: "other"}); !errors.Is(err, export.ErrBadSelection) {
		t.Errorf("bad kind: %v", err)
	}
}
