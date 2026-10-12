package importer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

// shortSource returns files that deliver fewer bytes than their size, as if
// they were truncated (or still growing) while being read.
type shortSource struct{ source }

func (s shortSource) OpenRegular(rel string, want inbox.FileID) (fs.File, error) {
	f, err := s.source.OpenRegular(rel, want)
	if err != nil {
		return nil, err
	}
	return shortFile{f}, nil
}

type shortFile struct{ fs.File }

func (f shortFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	if n > 0 && err == nil {
		return n - 1, nil
	}
	return n, err
}
func (f shortFile) ReadAt(p []byte, off int64) (int, error) {
	return f.File.(io.ReaderAt).ReadAt(p, off)
}
func (f shortFile) Seek(o int64, w int) (int64, error) { return f.File.(io.Seeker).Seek(o, w) }

// changedSource fails every open the way a swapped file would.
type changedSource struct{ source }

func (changedSource) OpenRegular(string, inbox.FileID) (fs.File, error) { return nil, inbox.ErrChanged }

func TestScan_ChangedWhileReading(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("sh-1", 1)))
	e.put("rd/request.json", yue2Request("sh-2", 2))
	e.flac("rd/audio.flac", "rd")
	e.put("rd/config.json", "{}")
	e.flac("t.flac", "t")
	e.put("c.csv", "variant_id\nx\n")
	e.put("o.json", `{"a":1}`)
	im := e.importer(func(o *Options) { o.src = shortSource{o.Inbox}; o.Inbox = nil })
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inv.Report.Summary.RecipesUnique != 0 || inv.Report.Summary.AudioIdentities != 0 {
		t.Errorf("short reads were accepted: %+v", inv.Report.Summary)
	}
	if inv.Report.Summary.Unsettled == 0 {
		t.Errorf("nothing marked unsettled: %+v", inv.Report.Summary.ByKind)
	}
	if a := audioByPath(&inv.Report, "t.flac"); a == nil || a.Status != "unsettled" {
		t.Errorf("t.flac = %+v", a)
	}

	im2 := e.importer(func(o *Options) { o.src = changedSource{o.Inbox}; o.Inbox = nil })
	inv2, err := im2.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(inv2.Report.Errors, "changed") {
		t.Errorf("errors = %v", issueCodes(inv2.Report.Errors))
	}
	if a := audioByPath(&inv2.Report, "t.flac"); a == nil || a.Status != "invalid" {
		t.Errorf("t.flac = %+v", a)
	}
}

func TestScan_InvalidFLAC(t *testing.T) {
	e := newEnv(t)
	e.put("garbage.flac", "fLaC\x00\x00\x00\x22tooshort")
	flactest.WriteFile(t, filepath.Join(e.inboxDir, "empty.flac"), flactest.Spec{TotalSamples: 0, Frames: []byte("x")})
	e.age("empty.flac", e.now.Add(-3600e9))
	e.flac("fine.flac", "fine")
	_, inv := e.inventory()
	s := inv.Report.Summary
	if s.AudioInvalid != 2 || s.AudioIdentities != 1 || s.AudioFiles != 3 {
		t.Errorf("summary = %+v", s)
	}
	n := 0
	for _, is := range inv.Report.Errors {
		if is.Code == "invalid_flac" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("errors = %+v", inv.Report.Errors)
	}
}

func TestInventory_MappingOptionAndRowErrors(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("mo-1", 1), req("mo-1", 2)))
	e.flac("x.flac", "x")
	p := filepath.Join(t.TempDir(), "elsewhere.csv")
	if err := os.WriteFile(p, []byte("x.flac,mo-1,2,A Title,\n../etc/passwd,x,,,\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, inv := e.inventory(func(o *Options) { o.MappingPath = p })
	a := audioByPath(&inv.Report, "x.flac")
	if a == nil || a.Status != "new" || a.Match.Rule != "mapping" || a.Title != "A Title" {
		t.Errorf("x.flac = %+v", a)
	}
	if !hasCode(inv.Report.Errors, "bad_mapping_row") || inv.Report.Errors[0].Line != 2 {
		t.Errorf("errors = %+v", inv.Report.Errors)
	}
	if strings.Contains(inv.Report.Errors[0].Message, "passwd") {
		t.Error("row value leaked")
	}
	// a missing explicit mapping file is fatal
	im := e.importer(func(o *Options) { o.MappingPath = filepath.Join(t.TempDir(), "nope.csv") })
	if _, err := im.Inventory(context.Background()); err == nil {
		t.Error("missing mapping accepted")
	}
	// rows naming no recipe warn and fall through to the other rules
	e.mapping("x.flac,does-not-exist,,,\n")
	_, inv2 := e.inventory()
	if !hasCode(inv2.Report.Warnings, "mapping_no_recipe") {
		t.Errorf("warnings = %v", issueCodes(inv2.Report.Warnings))
	}
	// sha256 keys name the content of any member of a duplicate group
	e.flac("copy.flac", "x")
	sum := audioByPath(&inv.Report, "x.flac").SHA256
	e.mapping("sha256:" + sum + ",mo-1,1,,\n")
	_, inv3 := e.inventory()
	if a := audioByPath(&inv3.Report, "copy.flac"); a.Status != "new" || a.Match.Rule != "mapping" || a.Match.RequestID != "mo-1" || a.Match.RecipeHash != recipeByID(&inv3.Report, "mo-1", 1).Hash {
		t.Errorf("sha mapping = %+v", a)
	}
}

func TestInventory_Cancelled(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.importer().Inventory(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestInventory_UnreadableRoot(t *testing.T) {
	e := newEnv(t)
	im := e.importer()
	if err := os.RemoveAll(e.inboxDir); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Inventory(context.Background()); err == nil {
		t.Error("missing inbox accepted")
	}
}

func TestReport_LibraryOrphans(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	_, res := e.applyAll()
	tracks := e.tracks()
	var referenced string
	for _, tr := range tracks {
		referenced = e.scalar(`SELECT master_path FROM tracks WHERE id=?`, tr.ID)
		break
	}
	mk := func(rel string) {
		t.Helper()
		if err := e.data.MkdirAll(filepath.ToSlash(filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := e.data.WriteFile(rel, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	mk(referenced)
	mk("audio/masters/stray.flac")
	mk("derived/ab/stray.mp3")
	mk("derived/probe/probe-v1.flac")
	mk("takes/whatever/score.abc")
	_ = res
	_, inv := e.inventory()
	got := strings.Join(inv.Report.LibraryOrphans, ",")
	if got != "audio/masters/stray.flac,derived/ab/stray.mp3" || inv.Report.Summary.LibraryOrphans != 2 {
		t.Errorf("orphans = %s", got)
	}
}

func TestPlace_TitlePrecedence(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("é", 250)
	e.put("r.jsonl", lines(req("t-map", 1), req("t-json", 2), req("t-tag", 3), req("t-stem", 4), req("t-audio", 5), req("t-ctrl", 6), req("t-long", 7)))
	e.put("requests/title.json", strings.Replace(req("t-json", 2), "}", `, "title": "From JSON"}`, 1))
	e.flac("a.flac", "a")
	e.flac("t_json.flac", "b") // slug => t-json; per-song title wins over the stem
	e.flac("tagged.flac", "c", "MM_REQUEST_ID=t-tag", "TITLE=From Tag")
	e.flac("t_stem.flac", "d")
	e.put("res/t-audio/request.json", yue2Request("t-audio", 5))
	e.flac("res/t-audio/audio.flac", "e")
	e.flac("ctrl.flac", "f", "MM_REQUEST_ID=t-ctrl", "TITLE=Line\x01Break  ")
	e.flac("long.flac", "g", "MM_REQUEST_ID=t-long", "TITLE="+long)
	e.mapping("a.flac,t-map,1,Mapped Title,\n")
	e.applyAll()
	tr := e.tracks()
	for k, want := range map[string]string{
		"t-map#1": "Mapped Title", "t-json#2": "From JSON", "t-tag#3": "From Tag", "t-stem#4": "T stem",
		"t-audio#5": "T audio", "t-ctrl#6": "Line Break",
	} {
		if tr[k].Title != want {
			t.Errorf("%s title = %q, want %q", k, tr[k].Title, want)
		}
	}
	if got := []rune(tr["t-long#7"].Title); len(got) != 200 {
		t.Errorf("long title = %d runes", len(got))
	}
	if tr["t-map#1"].Slug != "mapped_title" || tr["t-long#7"].Slug == "" {
		t.Errorf("slugs = %q %q", tr["t-map#1"].Slug, tr["t-long#7"].Slug)
	}
}

func TestSiblings_MappingParentByRequestID(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("px-1", 1), req("px-2", 1)))
	e.flac("a.flac", "a")
	e.flac("b.flac", "b")
	e.mapping("audio,request_id,seed,title,parent\na.flac,px-1,1,,\nb.flac,px-2,1,,px-1\n")
	e.applyAll()
	tr := e.tracks()
	if tr["px-2#1"].Parent != tr["px-1#1"].ID || tr["px-2#1"].Rule != "mapping" {
		t.Errorf("px-2 = %+v", tr["px-2#1"])
	}
	// the parent may already be in the library: generation continues from it
	e.put("r2.jsonl", lines(req("px-3", 1)))
	e.flac("c.flac", "c")
	e.mapping("audio,request_id,seed,title,parent\na.flac,px-1,1,,\nb.flac,px-2,1,,px-1\nc.flac,px-3,1,,b.flac\n")
	e.applyAll()
	tr = e.tracks()
	if c := tr["px-3#1"]; c.Parent != tr["px-2#1"].ID || c.Root != tr["px-1#1"].ID || c.Gen != 2 {
		t.Errorf("px-3 = %+v (root %s)", c, tr["px-1#1"].ID)
	}
}

func TestPlanChangedError(t *testing.T) {
	err := error(&PlanChangedError{Expected: "a", Actual: "b"})
	if !errors.Is(err, ErrPlanChanged) || !strings.Contains(err.Error(), "PLAN_CHANGED") {
		t.Errorf("err = %v", err)
	}
}

func TestDifferingKeys(t *testing.T) {
	mk := func(s string) *recipe {
		p, err := parseOne(s)
		if err != nil {
			t.Fatal(err)
		}
		return &recipe{parsed: p}
	}
	a := mk(`{"id":"x","style":"s","lyrics":"[Verse]\nl","seed":1}`)
	b := mk(`{"id":"x","style":"s","lyrics":"[Verse]\nl","seed":1,"cfg_scale":3}`)
	c := mk(`{"id":"x","style":"s","lyrics":"[Verse]\nl","seed":1,"abc":"X:1\nK:C\nC|]\n"}`)
	if got := strings.Join(differingKeys(a, b), ","); got != "cfg_scale" {
		t.Errorf("a/b = %s", got)
	}
	if got := strings.Join(differingKeys(a, c), ","); got != "abc" {
		t.Errorf("a/c = %s", got)
	}
	if got := strings.Join(differingKeys(a, a), ","); got != "none" {
		t.Errorf("a/a = %s", got)
	}
}

type failWriter struct{ n int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.n--; f.n < 0 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

func TestWriteMappingTemplate_WriteErrors(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("we-1", 1)))
	e.flac("zzz.flac", "z")
	_, inv := e.inventory()
	var ok bytes.Buffer
	if err := WriteMappingTemplate(&ok, inv); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := WriteMappingTemplate(&failWriter{n: n}, inv); err == nil {
			t.Errorf("write failure %d ignored", n)
		}
	}
}

func TestScan_ResultDirAudioWithUnsafeName(t *testing.T) {
	e := newEnv(t)
	e.put("rd/request.json", yue2Request("ua-1", 1))
	e.flac("rd/my audio (final).flac", "ua")
	_, inv := e.inventory()
	a := audioByPath(&inv.Report, "rd/my audio (final).flac")
	if a == nil || a.Status != "new" || a.Match.Rule != "result_dir" {
		t.Errorf("audio = %+v", a)
	}
}

func TestPlace_DraftMappingExistingMother(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("old-mum", 1)))
	e.flac("mum.flac", "m")
	e.mapping("mum.flac,old-mum,1,,\n")
	e.applyAll()
	// a varianti draft whose group key matches nobody; the mapping names the mother
	e.put("varianti/lost-v2.json", req("lost-v2", 2))
	e.mapping("mum.flac,old-mum,1,,\ndraft:lost-v2,,,,old-mum\n")
	inv, _ := e.applyAll()
	if got := e.drafts()["lost-v2#2"].Parent; got != e.tracks()["old-mum#1"].ID {
		t.Errorf("mother = %q", got)
	}
	if hasCode(inv.Report.Warnings, "mother_not_found") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	// a mapping row that changes an existing draft's NULL mother is applied on the next run
	e.put("varianti/late-v2.json", req("late-v2", 3))
	e.applyAll()
	if e.drafts()["late-v2#3"].Parent != "" {
		t.Fatal("setup: draft should have no mother yet")
	}
	e.mapping("mum.flac,old-mum,1,,\ndraft:late-v2,,,,old-mum\n")
	e.applyAll()
	if got := e.drafts()["late-v2#3"].Parent; got != e.tracks()["old-mum#1"].ID {
		t.Errorf("late mother = %q", got)
	}
}
