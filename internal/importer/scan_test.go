package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func spied(e *env) (*Importer, *spySource) {
	var spy *spySource
	im := e.importer(func(o *Options) {
		spy = newSpy(o.Inbox)
		o.src = spy
		o.Inbox = nil
	})
	return im, spy
}

func TestScan_SymlinksIgnored(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte(req("out-1", 1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.put("real.jsonl", req("in-1", 1)+"\n")
	e.put("dir/inner.jsonl", req("in-2", 2)+"\n")
	for name, target := range map[string]string{
		"inside-link.jsonl": "real.jsonl", "escape-link.jsonl": outside, "dirlink": "dir", "escape-dir": filepath.Dir(outside),
	} {
		if err := os.Symlink(target, e.abs(name)); err != nil {
			t.Fatal(err)
		}
	}
	im, spy := spied(e)
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.Report.Summary.RecipesUnique; got != 2 {
		t.Errorf("recipes = %d, want 2 (links must not be followed)", got)
	}
	reasons := map[string]string{}
	for _, ig := range inv.Report.Ignored {
		reasons[ig.Path] = ig.Reason
	}
	for _, l := range []string{"inside-link.jsonl", "escape-link.jsonl", "dirlink", "escape-dir"} {
		if reasons[l] != "symlink" {
			t.Errorf("%s: reason %q, want symlink", l, reasons[l])
		}
		if spy.wasOpened(l) {
			t.Errorf("%s was opened", l)
		}
	}
	for _, p := range spy.opened() {
		if strings.Contains(p, "outside") || strings.HasPrefix(p, "dirlink/") || strings.HasPrefix(p, "escape-dir/") {
			t.Errorf("opened %q through a link", p)
		}
	}
}

func TestScan_FIFONeverOpened(t *testing.T) {
	e := newEnv(t)
	if err := syscall.Mkfifo(e.abs("pipe.flac"), 0o644); err != nil {
		t.Skip(err)
	}
	if err := syscall.Mkfifo(e.abs("pipe.jsonl"), 0o644); err != nil {
		t.Skip(err)
	}
	im, spy := spied(e)
	done := make(chan *Inventory, 1)
	go func() {
		inv, err := im.Inventory(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- inv
	}()
	select {
	case inv := <-done:
		if inv == nil {
			return
		}
		for _, ig := range inv.Report.Ignored {
			if ig.Reason != "not_regular" {
				t.Errorf("%s: reason %q", ig.Path, ig.Reason)
			}
		}
		if len(inv.Report.Ignored) != 2 {
			t.Errorf("ignored = %+v", inv.Report.Ignored)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scan blocked on a FIFO")
	}
	if len(spy.opened()) != 0 {
		t.Errorf("opened %v", spy.opened())
	}
}

func TestScan_UnreadableReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	e := newEnv(t)
	e.put("ok.jsonl", req("ok-1", 1)+"\n")
	e.put("locked.jsonl", req("locked-1", 2)+"\n")
	e.put("locked-dir/inner.jsonl", req("locked-2", 3)+"\n")
	e.flac("locked.flac", "locked")
	for _, p := range []string{"locked.jsonl", "locked.flac"} {
		if err := os.Chmod(e.abs(p), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(e.abs("locked-dir"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.abs("locked-dir"), 0o755) })
	_, inv := e.inventory()
	un := map[string]bool{}
	for _, is := range inv.Report.Errors {
		if is.Code == "unreadable" {
			un[is.Path] = true
		}
	}
	for _, p := range []string{"locked.jsonl", "locked.flac", "locked-dir"} {
		if !un[p] {
			t.Errorf("no unreadable error for %s: %+v", p, inv.Report.Errors)
		}
	}
	if inv.Report.Summary.RecipesUnique != 1 {
		t.Errorf("walk did not continue: recipes = %d", inv.Report.Summary.RecipesUnique)
	}
	if a := audioByPath(&inv.Report, "locked.flac"); a == nil || a.Status != "invalid" {
		t.Errorf("locked.flac = %+v", a)
	}
}

func TestScan_ArchivesNotOpened(t *testing.T) {
	e := newEnv(t)
	e.put("evil.zip", "PK\x03\x04../x")
	e.put("pack.tar.gz", "x")
	e.put("one.jsonl", req("a-1", 1)+"\n")
	im, spy := spied(e)
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spy.wasOpened("evil.zip") || spy.wasOpened("pack.tar.gz") {
		t.Errorf("archive opened: %v", spy.opened())
	}
	n := 0
	for _, ig := range inv.Report.Ignored {
		if ig.Reason == "archive" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("archives ignored = %d", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.inboxDir), "x")); err == nil {
		t.Error("something was extracted outside the inbox")
	}
}

func TestScan_ResultDirTopLevelOnly(t *testing.T) {
	e := newEnv(t)
	e.flac("rd/audio.flac", "rd")
	e.put("rd/request.json", yue2ReqJSON)
	e.put("rd/config.json", "{}")
	e.put("rd/bad name.json", "{}")
	e.put("rd/sub/extra.json", "{}")
	e.put("rd/sub/deep/semantic.npy", "x")
	e.flac("rd/sub/other.flac", "other")
	_, inv := e.inventory()
	if !hasCode(inv.Report.Warnings, "result_subdir") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	got := map[string]string{}
	for _, en := range inv.b.entries {
		got[en.path] = en.kind
		if strings.HasPrefix(en.path, "rd/sub/") && en.artifact != "" {
			t.Errorf("%s became an artifact", en.path)
		}
	}
	if got["rd/bad name.json"] != kIgnUnsafeName || got["rd/config.json"] != kArtifact || got["rd/sub/extra.json"] != kIgnJSON ||
		got["rd/sub/deep/semantic.npy"] != kIgnUnknown {
		t.Errorf("kinds = %v", got)
	}
	if len(inv.b.rdirs) != 1 || inv.b.rdirs[0].dir != "rd" {
		t.Errorf("result dirs = %+v", inv.b.rdirs)
	}
}

func TestScan_ReadCaps(t *testing.T) {
	e := newEnv(t)
	big := e.abs("huge.jsonl")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(65 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	e.age("huge.jsonl", e.now.Add(-time.Hour))
	long := `{"id": "long-1", "lyrics": "` + strings.Repeat("x", 2<<20) + `", "seed": 1, "style": "s"}`
	e.put("longline.jsonl", lines(req("ll-1", 1), long, req("ll-2", 2)))
	e.put("bigjson.json", `{"a":"`+strings.Repeat("y", 5<<20)+`"}`)
	e.put("bigcsv.csv", "variant_id\n"+strings.Repeat("z\n", 9<<20))
	e.put("bigabc.abc", strings.Repeat("A", 2<<20))
	im, spy := spied(e)
	inv, err := im.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := map[string]bool{}
	for _, is := range inv.Report.Errors {
		if is.Code == "too_large" {
			tooLarge[is.Path] = true
		}
	}
	for _, p := range []string{"huge.jsonl", "bigjson.json", "bigcsv.csv", "bigabc.abc"} {
		if !tooLarge[p] {
			t.Errorf("%s: no too_large error (%v)", p, inv.Report.Errors)
		}
		if spy.wasOpened(p) {
			t.Errorf("%s was opened despite exceeding the cap", p)
		}
	}
	var ll *SourceReport
	for i := range inv.Report.RecipeSources {
		if inv.Report.RecipeSources[i].Path == "longline.jsonl" {
			ll = &inv.Report.RecipeSources[i]
		}
	}
	if ll == nil || ll.Valid != 2 || len(ll.Invalid) != 1 || ll.Invalid[0].Code != "line_too_long" || ll.Invalid[0].Line != 2 {
		t.Errorf("longline report = %+v", ll)
	}
	if inv.Report.Summary.RecipesUnique != 2 {
		t.Errorf("recipes = %d", inv.Report.Summary.RecipesUnique)
	}
}

func TestScan_CaseInsensitiveAndBOM(t *testing.T) {
	e := newEnv(t)
	e.put("UP.JSONL", "\xef\xbb\xbf"+lines(req("bom-1", 1)))
	e.put("Mixed.Json", "\xef\xbb\xbf"+smallReq)
	e.put("INDEX.CSV", "\xef\xbb\xbfvariant_id,seed\r\nbom-1,1\r\n")
	e.put("SCORE.ABC", "\xef\xbb\xbfX:1\nM:4/4\nQ:1/4=120\nK:C\nCDEF|CDEF|]\n")
	e.flac("TAKE.FLAC", "take")
	_, inv := e.inventory()
	if inv.Report.Summary.RecipesUnique != 2 {
		t.Fatalf("recipes = %d; errors %v", inv.Report.Summary.RecipesUnique, inv.Report.Errors)
	}
	if !hasCode(inv.Report.Warnings, "bom") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	byKind := inv.Report.Summary.ByKind
	if byKind[kRecipesJSONL] != 1 || byKind[kRequestJSON] != 1 || byKind[kReferenceIndex] != 1 || byKind[kScoreABC] != 1 || byKind[kAudioFLAC] != 1 {
		t.Errorf("by_kind = %v", byKind)
	}
	if inv.Report.Summary.RefsAttached != 1 {
		t.Errorf("refs = %d (BOM+CRLF CSV)", inv.Report.Summary.RefsAttached)
	}
	// BOM bytes are not part of the stored raw line
	for _, r := range inv.b.recipeList {
		for _, oc := range r.occ {
			if bytes.HasPrefix(oc.raw, []byte("\xef\xbb\xbf")) {
				t.Errorf("raw of %s keeps the BOM", oc.key())
			}
		}
	}
}

func TestScan_JSONArrayOfRequests(t *testing.T) {
	e := newEnv(t)
	el := func(id string, seed int) string {
		return fmt.Sprintf(`{"id":%q,"style":"s","lyrics":"[Verse]\nl","seed":%d}`, id, seed)
	}
	e.put("arr.json", "[\n "+el("a-1", 1)+",\n "+el("a-2", 2)+",\n "+el("a-3", 3)+"\n]")
	e.put("mixed.json", "["+el("m-1", 1)+", 7]")
	_, inv := e.inventory()
	if inv.Report.Summary.RecipesUnique != 3 {
		t.Fatalf("recipes = %d", inv.Report.Summary.RecipesUnique)
	}
	if !hasCode(inv.Report.Warnings, "json_array_not_requests") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	for i, id := range []string{"a-1", "a-2", "a-3"} {
		r := recipeByID(&inv.Report, id, int64(i+1))
		if r == nil || len(r.Sources) != 1 || r.Sources[0] != fmt.Sprintf("arr.json:%d", i+1) {
			t.Errorf("recipe %s = %+v", id, r)
		}
	}
	for _, r := range inv.b.recipeList {
		if r.occ[0].kind != "json_array_item" || strings.HasPrefix(string(r.occ[0].raw), " ") {
			t.Errorf("occurrence = %q %q", r.occ[0].kind, r.occ[0].raw)
		}
	}
}

func TestScan_NonUTF8Names(t *testing.T) {
	e := newEnv(t)
	e.flac("\xff.flac", "weird")
	e.put("\xfe.jsonl", lines(req("nu-1", 1)))
	e.put("\xfd.zip", "x")
	_, inv := e.inventory()
	rep, _ := json.Marshal(inv.Report)
	if !utf8.Valid(rep) || bytes.ContainsAny(rep, "\xff\xfe\xfd") {
		t.Fatalf("report carries raw bytes: %q", rep)
	}
	if !hasCode(inv.Report.Warnings, "non_utf8_name") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	var a *AudioReport
	for i := range inv.Report.Audio {
		a = &inv.Report.Audio[i]
	}
	if a == nil || !strings.HasPrefix(a.Path, "sha256:") {
		t.Errorf("audio = %+v", a)
	}
	for _, ig := range inv.Report.Ignored {
		if !strings.HasPrefix(ig.Path, "name:") {
			t.Errorf("ignored %q is not a name: key", ig.Path)
		}
	}
	if len(inv.Report.RecipeSources) != 1 || !strings.HasPrefix(inv.Report.RecipeSources[0].Path, "sha256:") {
		t.Errorf("sources = %+v", inv.Report.RecipeSources)
	}
	for _, oc := range inv.b.recipeList[0].occ {
		if !strings.HasPrefix(oc.file.sourcePath(), "name:") {
			t.Errorf("source_path = %q", oc.file.sourcePath())
		}
	}
	var out bytes.Buffer
	WriteMappingTemplate(&out, inv)
	inv.Report.FormatText(&out)
	if !utf8.Valid(out.Bytes()) || bytes.ContainsAny(out.Bytes(), "\xff\xfe\xfd") {
		t.Errorf("template/stdout carry raw bytes: %q", out.String())
	}
	if !strings.Contains(out.String(), "sha256:") {
		t.Errorf("unmatched non-UTF-8 audio not listed by key:\n%s", out.String())
	}
}

func TestScan_IgnoredNotHashed(t *testing.T) {
	e := newEnv(t)
	e.put(".hidden", "x")
	e.put("a.flac.part", "x")
	e.put("pack.zip", "x")
	e.put("notes.txt", "x")
	e.put("orphan.mp3", "x")
	e.put("data.npy", "x")
	e.put("other.json", `{"a":1}`)
	e.put("rd/latent.npy", "x")
	e.flac("rd/audio.flac", "rd")
	e.put("rd/request.json", yue2ReqJSON)
	e.put("keep.jsonl", lines(req("keep-1", 1)))
	e.flac("take.flac", "take")
	if err := os.Symlink("keep.jsonl", e.abs("link.jsonl")); err != nil {
		t.Fatal(err)
	}
	im, spy := spied(e)
	if _, err := im.Inventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".hidden", "a.flac.part", "pack.zip", "notes.txt", "orphan.mp3", "data.npy", "rd/latent.npy", "link.jsonl"} {
		if spy.wasOpened(p) {
			t.Errorf("ignored file %s was opened", p)
		}
	}
	for _, p := range []string{"keep.jsonl", "take.flac", "rd/audio.flac", "rd/request.json", "other.json"} {
		if !spy.wasOpened(p) {
			t.Errorf("candidate %s was not read", p)
		}
	}
}

func TestScan_Unsettled(t *testing.T) {
	e := newEnv(t)
	e.put("old.jsonl", lines(req("old-1", 1)))
	e.put("young.jsonl", lines(req("young-1", 2)))
	e.age("young.jsonl", e.now.Add(-30*time.Second))
	e.flac("young.flac", "young")
	e.age("young.flac", e.now.Add(-30*time.Second))
	e.flac("old.flac", "old")
	// result dir with one young member holds back its audio
	e.flac("rd/audio.flac", "rd")
	e.put("rd/request.json", yue2ReqJSON)
	e.put("rd/result.json", `{"truncated":{"abc":false,"semantic":false}}`)
	e.age("rd/result.json", e.now.Add(-10*time.Second))
	_, inv := e.inventory(func(o *Options) { o.MinAge = 2 * time.Minute })
	s := inv.Report.Summary
	if s.RecipesUnique != 2 { // old-1 and the result-dir request
		t.Errorf("recipes = %d", s.RecipesUnique)
	}
	if s.Unsettled != 4 { // young.jsonl, young.flac, rd/result.json and the audio it holds back
		t.Errorf("unsettled = %d (%+v)", s.Unsettled, inv.Report.Ignored)
	}
	for _, p := range []string{"young.flac", "rd/audio.flac"} {
		if a := audioByPath(&inv.Report, p); a == nil || a.Status != "unsettled" {
			t.Errorf("%s = %+v", p, a)
		}
	}
	if a := audioByPath(&inv.Report, "old.flac"); a == nil || a.Status == "unsettled" {
		t.Errorf("old.flac = %+v", a)
	}
	if s.TracksNew != 0 {
		t.Errorf("tracks_new = %d", s.TracksNew)
	}
	// with the guard off everything is read
	_, inv2 := e.inventory(func(o *Options) { o.MinAge = 0 })
	if inv2.Report.Summary.Unsettled != 0 || inv2.Report.Summary.RecipesUnique != 3 {
		t.Errorf("MinAge 0: %+v", inv2.Report.Summary)
	}
}

func TestApply_NonUTF8NamesNotStored(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("nu-1", 1)))
	e.put("pilot_secret\xfe.jsonl", lines(req("nu-2", 2)))
	e.flac("secret_name\xff.flac", "weird")
	_, inv := e.inventory()
	a := inv.Report.Audio[0]
	e.mapping("audio,request_id,seed\n" + a.Path + ",nu-1,1\n")
	e.applyAll()
	for _, q := range []string{`SELECT title FROM tracks`, `SELECT slug FROM tracks`, `SELECT name FROM draft_collections`, `SELECT source FROM draft_collections`} {
		rows := e.query(q)
		n := 0
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			n++
			if !utf8.ValidString(v) || strings.Contains(strings.ToLower(v), "secret") || strings.ContainsRune(v, 0xFFFD) {
				t.Errorf("%s stored %q", q, v)
			}
		}
		rows.Close()
		if n == 0 && !strings.Contains(q, "draft_collections") {
			t.Errorf("%s: no rows", q)
		}
	}
}

func TestScan_BOMWarningPerFileType(t *testing.T) {
	e := newEnv(t)
	bom := "\xef\xbb\xbf"
	el := func(id string, seed int) string {
		return fmt.Sprintf(`{"id":%q,"style":"s","lyrics":"[Verse]\nl","seed":%d}`, id, seed)
	}
	e.put("a.jsonl", bom+lines(req("bom-1", 1)))
	e.put("b.json", bom+smallReq)
	e.put("arr.json", bom+"["+el("bom-a", 1)+","+el("bom-b", 2)+"]")
	e.put("coll.json", bom+`{"name":"c","songs":[{"variant_ids":["bom-1"]}]}`)
	e.put("idx.csv", bom+"variant_id,seed\r\nbom-1,1\r\n")
	e.put("take.abc", bom+"X:1\nM:4/4\nQ:1/4=120\nK:C\nCDEF|CDEF|]\n")
	e.flac("take.flac", "take")
	e.mapping("take.flac,bom-1,1,,\n")
	_, inv := e.inventory()
	got := map[string]bool{}
	for _, w := range inv.Report.Warnings {
		if w.Code == "bom" {
			got[w.Path] = true
		}
	}
	for _, p := range []string{"a.jsonl", "b.json", "arr.json", "coll.json", "idx.csv", "take.abc"} {
		if !got[p] {
			t.Errorf("no bom warning for %s (%v)", p, got)
		}
	}
}

func TestApply_BOMScoreOneByteForm(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req3("sc-1", 1)))
	e.put("sc-1.abc", "\xef\xbb\xbf"+abc3)
	e.flac("sc-1.flac", "x")
	e.applyAll()
	sha := e.scalar(`SELECT abc_sha256 FROM scores`)
	r, ok := e.ing.requestFor("sc-1", 1)
	if sha == "" || !ok || len(r.Artifacts) != 1 || r.Artifacts[0].SHA256 != sha || sha != shaOf(abc3) {
		t.Errorf("score sha %q, artifacts %+v", sha, r.Artifacts)
	}
}
