package records_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// update rewrites the golden files of this package (`go test ./internal/records/ -update`); every test file of the
// package shares this one flag.
var update = flag.Bool("update", false, "rewrite golden files")

// golden compares got (indented JSON) with testdata/<name>, rewriting it under -update.
// webMirrors lists the goldens the SPA's bun tests read as a byte copy; -update writes both files.
var webMirrors = map[string]string{
	"change_text.golden.json": filepath.Join("..", "..", "web", "src", "lib", "fixtures", "change_text.golden.json"),
}

func golden(t *testing.T, name string, got any) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		// The web test reads a byte copy of this golden (web/src/lib/recordView.test.ts asserts equality).
		if mirror, ok := webMirrors[name]; ok {
			if err := os.WriteFile(mirror, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Errorf("%s differs (run with -update after a deliberate change):\n%s", name, b)
	}
}

type whyRow struct {
	Name     string          `json:"name"`
	Source   string          `json:"source"`
	Evidence json.RawMessage `json:"evidence"`
	Text     string          `json:"text"`
}

// TestWhyText_Golden is C122.3's eight-row table: the single why-vocabulary.
func TestWhyText_Golden(t *testing.T) {
	t.Parallel()
	rows := []whyRow{
		{Name: "ladder, 1 duel", Source: "ladder", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"ladder","eligible":2,"duels_n":1}`)},
		{Name: "ladder, 4 duels", Source: "ladder", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"ladder","eligible":3,"duels_n":4}`)},
		{Name: "pinned", Source: "pinned", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"pinned","eligible":2}`)},
		{Name: "stars, untied", Source: "stars", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"stars","eligible":2,"stars":4,"tied":1}`)},
		{Name: "stars, tied 3", Source: "stars", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"stars","eligible":4,"stars":5,"tied":3}`)},
		{Name: "newest", Source: "newest", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"newest","eligible":2}`)},
		{Name: "none", Source: "none", Evidence: json.RawMessage(`{"def":"mm.pick/1","rule":"none","eligible":0}`)},
		{Name: "ladder, evidence missing", Source: "ladder", Evidence: nil},
	}
	for i := range rows {
		rows[i].Text = records.WhyText(rows[i].Source, rows[i].Evidence)
	}
	golden(t, "why_text.golden.json", rows)
}

func TestWhyText_Vocabulary(t *testing.T) {
	t.Parallel()
	ev := func(s string) json.RawMessage { return json.RawMessage(s) }
	for _, c := range []struct {
		source   string
		evidence json.RawMessage
		want     string
	}{
		{"ladder", ev(`{"duels_n":1}`), "won 1 blind duel"},
		{"ladder", ev(`{"duels_n":3}`), "won 3 blind duels"},
		{"ladder", ev(`{}`), "won blind duels"},
		{"ladder", ev(`not json`), "won blind duels"},
		{"ladder", ev(`null`), "won blind duels"},
		{"stars", ev(`{"stars":4}`), "4★ · undecided"},
		{"stars", ev(`{"stars":4,"tied":1}`), "4★ · undecided"},
		{"stars", ev(`{"stars":4,"tied":2}`), "4★ (newest of 2 tied) · undecided"},
		{"stars", ev(`{}`), "stars · undecided"},
		{"newest", nil, "newest take · undecided"},
		{"pin", nil, "pinned as the song's Pick"},
		{"pinned", nil, "pinned as the song's Pick"},
		{"unpin", nil, "pin removed"},
		{"pin_ineligible", nil, "pinned take discarded, fell back"},
		{"pin_restored", nil, "pinned take restored"},
		{"initial", nil, "first take"},
		{"emptied", nil, "no playable take"},
		{"retired", nil, "follows the merged song"},
		{"duplicate", nil, "duplicate"},
		{"none", nil, ""},
		{"", nil, ""},
		{"surprise", nil, ""},
	} {
		if got := records.WhyText(c.source, c.evidence); got != c.want {
			t.Errorf("WhyText(%q, %s) = %q, want %q", c.source, c.evidence, got, c.want)
		}
	}
}

// TestWhyText_Undecided: exactly the sources records.Undecided names end in " · undecided".
func TestWhyText_Undecided(t *testing.T) {
	t.Parallel()
	const suffix = " · undecided"
	for _, src := range []string{"newest", "stars", "ladder", "pinned", "pin", "none", "unpin", "initial"} {
		got := records.WhyText(src, nil)
		has := len(got) >= len(suffix) && got[len(got)-len(suffix):] == suffix
		if has != records.Undecided(src) {
			t.Errorf("%s: suffix %v, Undecided %v", src, has, records.Undecided(src))
		}
	}
}
