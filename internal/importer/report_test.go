package importer

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestMappingTemplate_Written(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("tpl-1", 1), req("tpl-1", 2), req("near-one", 3), req("kid-v2", 4)))
	e.put("varianti/kid-v2.json", req("kid-v2", 4))
	e.flac("tpl_1.flac", "t1")        // ambiguous between two seeds
	e.flac("near_on.flac", "n1")      // unmatched, close to near-one
	e.flac("zzz-unrelated.flac", "z") // unmatched, no candidates
	_, inv := e.inventory()
	var buf bytes.Buffer
	if err := WriteMappingTemplate(&buf, inv); err != nil {
		t.Fatal(err)
	}
	golden(t, "mapping.todo.golden.csv", buf.Bytes())

	// the template parses back as a mapping file (candidates column ignored)
	m, err := ParseMapping(bytes.NewReader(buf.Bytes()), e.importer().src.FS(), "t.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Errors) != 0 {
		t.Errorf("template does not parse: %+v", m.Errors)
	}
	// WriteDryRun puts the same content under import/
	im := e.importer()
	files, err := im.WriteDryRun(inv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(e.dataDir, files.MappingTodo))
	if err != nil || !bytes.Equal(got, buf.Bytes()) {
		t.Errorf("mapping.todo.csv = %q %v", got, err)
	}
	if fi, _ := os.Stat(filepath.Join(e.dataDir, files.MappingTodo)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
	// unsafe path characters are replaced by the content key
	e2 := newEnv(t)
	e2.flac("new\nline.flac", "nl")
	_, inv2 := e2.inventory()
	var b2 bytes.Buffer
	WriteMappingTemplate(&b2, inv2)
	if strings.Contains(b2.String(), "new\nline") || !strings.Contains(b2.String(), "sha256:") {
		t.Errorf("template = %q", b2.String())
	}
}

func TestReport_NoAbsolutePaths(t *testing.T) {
	e := newEnv(t)
	acceptanceLayout(t, e)
	e.put("outside-note.txt", "x")
	inv, res := e.applyAll()
	var out bytes.Buffer
	inv.Report.FormatText(&out)
	rep, _ := json.Marshal(res.Report)
	dry, _ := json.Marshal(inv.Report)
	var tpl bytes.Buffer
	WriteMappingTemplate(&tpl, inv)
	stored, err := os.ReadFile(filepath.Join(e.dataDir, res.ReportPath))
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string][]byte{"applied": rep, "dry": dry, "text": out.Bytes(), "template": tpl.Bytes(), "file": stored,
		"run": []byte(e.scalar(`SELECT report_json FROM import_runs`) + e.scalar(`SELECT summary_json FROM import_runs`))} {
		for _, bad := range []string{e.inboxDir, e.dataDir, filepath.Dir(e.inboxDir), os.TempDir()} {
			if bytes.Contains(blob, []byte(bad)) {
				t.Errorf("%s report contains %q", name, bad)
			}
		}
	}
	if inv.Report.Inbox != "inbox" {
		t.Errorf("inbox label = %q", inv.Report.Inbox)
	}
	// error and warning messages as well
	for _, is := range append(res.Report.Errors, res.Report.Warnings...) {
		if strings.HasPrefix(is.Path, "/") || strings.Contains(is.Message, e.inboxDir) {
			t.Errorf("issue %+v", is)
		}
	}
}

func TestReport_FileNamesUnique(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	im, inv := e.inventory()
	a, err := im.WriteDryRun(inv)
	if err != nil {
		t.Fatal(err)
	}
	_, inv2 := e.inventory()
	b, err := im.WriteDryRun(inv2) // same second
	if err != nil {
		t.Fatal(err)
	}
	if a.Report == b.Report {
		t.Fatalf("both reports are %s", a.Report)
	}
	ents, _ := os.ReadDir(filepath.Join(e.dataDir, "import/reports"))
	if len(ents) != 2 {
		t.Errorf("reports = %d", len(ents))
	}
	// O_EXCL: an existing file is never overwritten
	if _, err := im.writeReportFile(&inv.Report, im.now()); err == nil {
		t.Error("report overwritten")
	}
	for _, en := range ents {
		fi, _ := en.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", en.Name(), fi.Mode())
		}
	}
	// the saved report is the inventory
	var back Report
	raw, _ := os.ReadFile(filepath.Join(e.dataDir, a.Report))
	if err := json.Unmarshal(raw, &back); err != nil || back.Format != ReportFormat || !back.DryRun || back.PlanDigest != inv.PlanDigest() || back.ReportID != inv.Report.ReportID {
		t.Errorf("saved report: %v %+v", err, back.Format)
	}
	if !strings.Contains(a.Report, inv.Report.ReportID) || !strings.HasSuffix(a.Report, "-dry-run.json") {
		t.Errorf("name = %s", a.Report)
	}
}

func TestReport_DigestStable(t *testing.T) {
	build := func() (*env, *Inventory) {
		e := newEnv(t)
		acceptanceLayout(t, e)
		_, inv := e.inventory(func(o *Options) { o.MinAge = 2 * time.Minute })
		return e, inv
	}
	_, a := build()
	e2, b := build()
	if a.PlanDigest() != b.PlanDigest() {
		t.Error("two environments with the same archive give different digests")
	}
	if a.Report.ReportID == b.Report.ReportID {
		t.Error("report ids must be fresh")
	}
	// ids and timestamps are not part of the plan
	e2.now = e2.now.Add(72 * time.Hour)
	e2.age("music/songs.jsonl", e2.now.Add(-time.Hour))
	_, c := e2.inventory(func(o *Options) { o.MinAge = 2 * time.Minute })
	if c.PlanDigest() != a.PlanDigest() {
		t.Error("digest depends on the clock")
	}
	// but any plan change moves it
	e2.put("music/extra.jsonl", lines(req("extra-1", 1)))
	_, d := e2.inventory(func(o *Options) { o.MinAge = 2 * time.Minute })
	if d.PlanDigest() == a.PlanDigest() {
		t.Error("digest ignores a new recipe")
	}
	// JSON round trip of the entries
	for _, en := range a.PlanEntries() {
		var m map[string]any
		if err := json.Unmarshal([]byte(en), &m); err != nil || m["k"] == nil {
			t.Fatalf("entry %q", en)
		}
	}
	if digestOf(a.PlanEntries()) != a.PlanDigest() {
		t.Error("digest is not derived from the entries")
	}
}

func TestReport_FormatText(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("ft-1", 1), req("ft-1", 2), "{bad"))
	e.flac("ft_1.flac", "x")
	e.flac("ctrl\x1b[31m.flac", "y")
	_, inv := e.inventory()
	var out bytes.Buffer
	inv.Report.FormatText(&out)
	s := out.String()
	for _, want := range []string{"recipes (unique)", "ambiguous audio:", "unmatched audio:", "ft_1.flac", `ctrl\x1b[31m.flac`, "error: r.jsonl:3 not_object"} {
		if !strings.Contains(s, want) {
			t.Errorf("text misses %q:\n%s", want, s)
		}
	}
	if strings.ContainsRune(s, 0x1b) {
		t.Error("raw escape character printed")
	}
}
