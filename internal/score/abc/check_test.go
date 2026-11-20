package abc

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestCheck_Report_Golden(t *testing.T) {
	song, err := os.ReadFile("testdata/song-sections.abc")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		Name string
		Src  string
		Base string
		Opts Options
	}{
		{"sections yue2", string(song), "", Options{Dialect: DialectYuE2}},
		{"divergent yue2", "X:1\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\nV:1\n^Ff F|c|\n", "", Options{Dialect: DialectYuE2}},
		{"standard untimed", "X:1\nM:3/4\nL:1/8\nK:G\n|: GAB cde :|\n", "", Options{Dialect: DialectStandard}},
		{"unparsable", "X:1\nM:4/4\nK:C\nV:1\n[CE\n", "", Options{Dialect: DialectYuE2}},
		{"violations", strings.Replace(invBase, "GABc", "GABcc", 1), invBase, Options{Dialect: DialectYuE2}},
		{"base ok", invBase, invBase, Options{Dialect: DialectYuE2}},
		{"warnings", "X:1\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\nV:1\n!f!\"Hx\"C D E F|\n", "", Options{Dialect: DialectYuE2}},
		{"bad base", invBase, "not abc at all [", Options{Dialect: DialectYuE2}},
	}
	type entry struct {
		Name   string `json:"name"`
		Report Report `json:"report"`
	}
	var got []entry
	for _, c := range cases {
		var base []byte
		if c.Base != "" {
			base = []byte(c.Base)
		}
		got = append(got, entry{c.Name, Check([]byte(c.Src), base, c.Opts)})
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/check_report.golden.json"
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("report differs from %s (run with -update)\n%s", path, b)
	}
}

func TestCheck_Report_Facts(t *testing.T) {
	t.Parallel()
	r := Check([]byte(invBase), nil, Options{Dialect: DialectYuE2})
	if !r.OK || len(r.Errors) != 0 || r.Bars != 8 || !r.Timed || r.Dialect != "yue2" || len(r.Voices) != 2 || len(r.Sections) != 2 {
		t.Fatalf("%+v", r)
	}
	if r.SecondsPerBar == nil || *r.SecondsPerBar != 2.4 || r.DurationS == nil || *r.DurationS != 8*2.4 {
		t.Fatalf("timing %v %v", r.SecondsPerBar, r.DurationS)
	}
	if r.Events != 32+8 || r.Chords != 2 || r.DivergentBars != 0 || r.DisplayABC != nil || r.Violations != nil {
		t.Fatalf("counts %+v", r)
	}
	bad := Check([]byte("X:1\nK:C\n[CE\n"), nil, Options{})
	if bad.OK || len(bad.Errors) != 1 || bad.Errors[0].Rule != "unterminated_chord" || bad.Errors[0].Line == 0 {
		t.Fatalf("%+v", bad)
	}
	d := Check([]byte("X:1\nM:4/4\nL:1/4\nK:C\n^Ff|\n"), nil, Options{Dialect: DialectYuE2})
	if d.DivergentBars != 1 || d.DisplayABC == nil || !strings.Contains(*d.DisplayABC, "^F^f") {
		t.Fatalf("%+v", d)
	}
}
