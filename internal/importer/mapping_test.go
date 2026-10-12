package importer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

const sha64 = "3f00000000000000000000000000000000000000000000000000000000000a9a"

func mapFS() fstest.MapFS {
	return fstest.MapFS{
		"music/a.flac": {Data: []byte("x")},
		"b.flac":       {Data: []byte("y")},
	}
}

func TestMapping_Parse(t *testing.T) {
	csv := "\ufeff# operator mapping\n" +
		"audio,request_id,seed,title,parent\n" +
		"music/a.flac,syn-song-01,1001,\"A, \"\"quoted\"\" title\",\n" +
		"# another comment\n" +
		"sha256:" + sha64 + ",-,,,\n" +
		"draft:syn-song-01-v2,,,,syn-song-01\n" +
		"b.flac,syn-song-02,,,music/a.flac\n" +
		"b.flac,syn-song-03,,,\n" + // duplicate key
		"music/a.flac,x,notanumber,,\n" + // bad seed
		"draft:v3,rid,,,\n" + // draft row with request_id
		"draft:,,,,\n" + // empty draft id
		"sha256:zz,x,,,\n" + // bad sha
		"music/a.flac,x,1,y,z,cand,extra\n" // too many columns
	m, err := ParseMapping(strings.NewReader(csv), mapFS(), "import/mapping.csv")
	if err != nil {
		t.Fatal(err)
	}
	a := m.ByPath["music/a.flac"]
	if a == nil || a.RequestID != "syn-song-01" || a.Seed == nil || *a.Seed != 1001 || a.Title != `A, "quoted" title` {
		t.Fatalf("row a = %+v", a)
	}
	s := m.BySHA[sha64]
	if s == nil || !s.Skip {
		t.Fatalf("sha row = %+v", s)
	}
	d := m.Drafts["syn-song-01-v2"]
	if d == nil || d.Parent != "syn-song-01" {
		t.Fatalf("draft row = %+v", d)
	}
	b := m.ByPath["b.flac"]
	if b == nil || b.RequestID != "syn-song-02" || b.Parent != "music/a.flac" {
		t.Fatalf("row b = %+v (first row must win)", b)
	}
	wantLines := map[int]bool{8: true, 9: true, 10: true, 11: true, 12: true, 13: true}
	got := map[int]bool{}
	for _, e := range m.Errors {
		if e.Code != "bad_mapping_row" || e.Path != "import/mapping.csv" {
			t.Errorf("issue = %+v", e)
		}
		got[e.Line] = true
		for _, bad := range []string{"notanumber", "syn-song-03", "zz"} {
			if strings.Contains(e.Message, bad) {
				t.Errorf("message leaks a value: %q", e.Message)
			}
		}
	}
	for l := range wantLines {
		if !got[l] {
			t.Errorf("no error for line %d; errors: %+v", l, m.Errors)
		}
	}
}

func TestMapping_UnbalancedQuote(t *testing.T) {
	m, err := ParseMapping(strings.NewReader("audio,request_id\n\"b.flac,x\n"), mapFS(), "m.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Errors) == 0 {
		t.Fatal("expected a row error")
	}
}

func TestMapping_PathConfinement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok.flac", filepath.Join(dir, "link.flac")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.flac"), 0o644); err != nil {
		t.Skip(err)
	}
	fsys := os.DirFS(dir)
	bad := []string{
		"/etc/passwd", "../x.flac", "a/../ok.flac", "./ok.flac", "a//b.flac", `a\b.flac`, "ok.flac/",
		"\xff.flac", "link.flac", "missing.flac", "d", "pipe.flac", "", ".",
	}
	for _, p := range bad {
		m, err := ParseMapping(strings.NewReader("\""+strings.ReplaceAll(p, `"`, `""`)+"\",rid,,,\n"), fsys, "m.csv")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Errors) != 1 || m.Errors[0].Code != "bad_mapping_row" || len(m.ByPath) != 0 {
			t.Errorf("path %q accepted: errors=%v rows=%v", p, m.Errors, m.ByPath)
		}
		for _, e := range m.Errors {
			if strings.Contains(e.Message, "etc") || strings.Contains(e.Message, "passwd") {
				t.Errorf("message leaks the path: %q", e.Message)
			}
		}
	}
	m, err := ParseMapping(strings.NewReader("ok.flac,rid,7,,\n"), fsys, "m.csv")
	if err != nil || len(m.Errors) != 0 || m.ByPath["ok.flac"] == nil {
		t.Fatalf("clean path rejected: %v %v", err, m.Errors)
	}
}
