package importer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestRecipes_RawBytesKept(t *testing.T) {
	e := newEnv(t)
	utf := `{"cot": "full", "id": "raw-1", "lyrics": "[Verse]\nè à ì <b>&", "seed": 1, "style": "pop"}`
	crlf := req("raw-2", 2) + "\r"
	e.put("a.jsonl", "\xef\xbb\xbf"+utf+"\n"+crlf+"\n")
	inv, _ := e.applyAll()
	if !hasCode(inv.Report.Warnings, "crlf") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	want := map[string]string{"raw-1": utf, "raw-2": crlf}
	rows := e.query(`SELECT r.request_id, s.raw, s.raw_sha256, s.raw_kind, s.source_path, s.line_no FROM recipe_sources s JOIN recipes r ON r.id = s.recipe_id`)
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, kind, path string
		var raw []byte
		var sha string
		var line int
		if err := rows.Scan(&id, &raw, &sha, &kind, &path, &line); err != nil {
			t.Fatal(err)
		}
		n++
		if string(raw) != want[id] {
			t.Errorf("%s raw = %q, want %q", id, raw, want[id])
		}
		if sha != shaOf(want[id]) || kind != "jsonl_line" || path != "a.jsonl" || line < 1 {
			t.Errorf("%s: sha/kind/path/line = %s %s %s %d", id, sha, kind, path, line)
		}
	}
	if n != 2 {
		t.Errorf("sources = %d", n)
	}
}

func TestRecipes_InvalidLineReported(t *testing.T) {
	e := newEnv(t)
	var ls []string
	for i := 1; i <= 12; i++ {
		if i == 10 {
			ls = append(ls, "[1, 2]")
			continue
		}
		ls = append(ls, req(fmt.Sprintf("inv-%02d", i), i))
	}
	e.put("a.jsonl", lines(ls...))
	_, inv := e.inventory()
	sr := inv.Report.RecipeSources[0]
	if sr.Lines != 12 || sr.Valid != 11 || len(sr.Invalid) != 1 || sr.Invalid[0] != (InvalidLine{Line: 10, Code: "not_object"}) {
		t.Errorf("source = %+v", sr)
	}
	if inv.Report.Summary.RecipesUnique != 11 || inv.Report.Summary.RecipeLinesInvalid != 1 || inv.Report.Summary.Errors != 1 {
		t.Errorf("summary = %+v", inv.Report.Summary)
	}
	er := inv.Report.Errors[0]
	if er.Path != "a.jsonl" || er.Line != 10 || er.Code != "not_object" {
		t.Errorf("error = %+v", er)
	}
}

func TestRecipes_SourcesAllRecorded(t *testing.T) {
	e := newEnv(t)
	a, b, c := req("bk-1", 1), req("bk-2", 2), req("bk-3", 3)
	e.put("backlog.jsonl", lines(a, b, c))
	e.put("pilot.jsonl", lines(a, b))
	_, res := e.applyAll()
	if res.State != "completed" {
		t.Fatal(res.State)
	}
	if e.count("recipes") != 3 || e.count("recipe_sources") != 5 {
		t.Fatalf("recipes=%d sources=%d", e.count("recipes"), e.count("recipe_sources"))
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM recipe_sources WHERE source_path='pilot.jsonl' AND line_no IN (1,2) AND cast(raw as text) IN (?, ?)`, a, b).Scan(&n); err != nil || n != 2 {
		t.Fatalf("pilot sources = %d %v", n, err)
	}
	// a re-run adds no rows
	before := e.counts()
	e.applyAll()
	after := e.counts()
	for k := range before {
		want := before[k]
		if k == "import_runs" {
			want++
		}
		if after[k] != want {
			t.Errorf("%s: %d -> %d", k, before[k], after[k])
		}
	}
	// a new occurrence in run 2 adds exactly one row
	e.put("pilot2.jsonl", lines(c))
	e.applyAll()
	if e.count("recipe_sources") != 6 || e.count("recipes") != 3 {
		t.Errorf("after new occurrence: recipes=%d sources=%d", e.count("recipes"), e.count("recipe_sources"))
	}
}

func TestRecipes_IDConflict(t *testing.T) {
	e := newEnv(t)
	e.put("a.jsonl", lines(req("dup-1", 1), req("dup-1", 2)))
	inv, _ := e.applyAll()
	if !hasCode(inv.Report.Warnings, "id_conflict") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	if e.count("recipes") != 2 {
		t.Errorf("recipes = %d, want both imported", e.count("recipes"))
	}
}

func TestRecipes_UnterminatedLastLine(t *testing.T) {
	e := newEnv(t)
	e.put("a.jsonl", req("ut-1", 1)+"\n"+req("ut-2", 2))
	inv, _ := e.applyAll()
	if !hasCode(inv.Report.Warnings, "unterminated_last_line") || e.count("recipes") != 1 {
		t.Fatalf("warnings=%v recipes=%d", issueCodes(inv.Report.Warnings), e.count("recipes"))
	}
	if inv.Report.Summary.Errors != 0 {
		t.Errorf("errors = %v", inv.Report.Errors)
	}
	e.put("a.jsonl", req("ut-1", 1)+"\n"+req("ut-2", 2)+"\n")
	e.applyAll()
	if e.count("recipes") != 2 || e.count("recipe_sources") != 2 {
		t.Errorf("recipes=%d sources=%d", e.count("recipes"), e.count("recipe_sources"))
	}
}

func TestRecipes_ABCPathConfinement(t *testing.T) {
	e := newEnv(t)
	abcLine := func(id, p string) string {
		return fmt.Sprintf(`{"cot": "melody", "id": %q, "lyrics": "[Verse]\nla", "seed": 1, "style": "pop", "abc_path": %q}`, id, p)
	}
	e.put("outside.abc", "X:1\nK:C\nCDEF|]\n")
	e.put("sub/good.abc", "X:1\nK:C\nCDEF|]\n")
	e.put("sub/real.abc", "X:1\nK:C\nGGGG|]\n")
	e.put("sub/big.abc", strings.Repeat("A", 2<<20))
	if err := os.Symlink("real.abc", e.abs("sub/link.abc")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(e.abs("sub/pipe.abc"), 0o644); err != nil {
		t.Skip(err)
	}
	e.put("sub/bad.abc", "X:1\n\xff\xfe\n")
	cases := []struct{ id, path string }{
		{"abc-up", "../outside.abc"}, {"abc-abs", "/etc/hostname"}, {"abc-bs", `a\b.abc`}, {"abc-link", "link.abc"},
		{"abc-fifo", "pipe.abc"}, {"abc-big", "big.abc"}, {"abc-utf8", "bad.abc"}, {"abc-missing", "nope.abc"},
		{"abc-dot", "./good.abc"}, {"abc-ok", "good.abc"},
	}
	var ls []string
	for _, c := range cases {
		ls = append(ls, abcLine(c.id, c.path))
	}
	e.put("sub/recipes.jsonl", lines(ls...))
	inv, _ := e.applyAll()
	bad := map[string]bool{}
	for _, is := range inv.Report.Errors {
		if is.Code == "abc_path_unresolved" {
			bad[fmt.Sprint(is.Line)] = true
		}
	}
	for i, c := range cases {
		if c.id == "abc-ok" {
			continue
		}
		if !bad[fmt.Sprint(i+1)] {
			t.Errorf("%s (%q): not rejected; errors %v", c.id, c.path, inv.Report.Errors)
		}
	}
	if bad[fmt.Sprint(len(cases))] || e.count("recipes") != 1 {
		t.Fatalf("valid relative path rejected: recipes=%d", e.count("recipes"))
	}
	var abc, rawLine string
	if err := e.st.R.QueryRow(`SELECT json_extract(request_json,'$.abc') FROM recipes`).Scan(&abc); err != nil || abc != "X:1\nK:C\nCDEF|]\n" {
		t.Errorf("inlined abc = %q %v", abc, err)
	}
	if err := e.st.R.QueryRow(`SELECT cast(raw as text) FROM recipe_sources`).Scan(&rawLine); err != nil || !strings.Contains(rawLine, `"abc_path": "good.abc"`) {
		t.Errorf("raw = %q %v", rawLine, err)
	}
	if k := inv.b.byPath["sub/good.abc"].kind; k != kABCSource {
		t.Errorf("good.abc kind = %s", k)
	}
	if k := inv.b.byPath["sub/real.abc"].kind; k != kScoreABC {
		t.Errorf("real.abc kind = %s", k)
	}
}

func TestRecipes_ResultDirRequestJSONSameHash(t *testing.T) {
	e := newEnv(t)
	e.put("songs.jsonl", lines(req("rd-1", 7)))
	e.put("res/rd-1/request.json", yue2Request("rd-1", 7))
	e.flac("res/rd-1/audio.flac", "rd1")
	_, inv := e.inventory()
	if inv.Report.Summary.RecipesUnique != 1 || inv.Report.Summary.RecipeSources != 2 {
		t.Fatalf("summary = %+v", inv.Report.Summary)
	}
	r := inv.Report.Recipes[0]
	if len(r.Sources) != 2 {
		t.Errorf("sources = %v", r.Sources)
	}
	var kinds []string
	for _, oc := range inv.b.recipeList[0].occ {
		kinds = append(kinds, oc.kind)
	}
	if !bytes.Equal([]byte(strings.Join(kinds, ",")), []byte("json_file,jsonl_line")) && strings.Join(kinds, ",") != "jsonl_line,json_file" {
		// occurrence order is by path: res/... sorts after songs.jsonl? both orders are fine, kinds must be these two
		t.Errorf("kinds = %v", kinds)
	}
	_ = context.Background
}
