package importer

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

func (e *env) mapping(csv string) {
	e.t.Helper()
	if err := e.data.MkdirAll("import", 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := e.data.WriteFile("import/mapping.csv", []byte(csv), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func TestMatch_Rules(t *testing.T) {
	e := newEnv(t)
	e.put("recipes.jsonl", lines(
		req("m-1", 1), req("m-1", 2),
		req("rd-2", 3),
		req("tag-3", 4), req("tag-seed", 1), req("tag-seed", 2), req("cmt-3", 5),
		req("fn-4", 6),
		req("sidejson-5", 7),
		req("slug-six", 8),
		req("seven", 77), req("seven", 88),
		req("eight-8", 9),
		req("grp-9-v1", 10), req("grp-9-v2", 11),
		req("amb-10", 1), req("amb-10", 2),
		req("lev-11", 12),
		req("prec-12", 13),
		req("skip-13", 14),
	))
	e.put("requests/e8.json", strings.Replace(req("eight-8", 9), "}", `, "title": "The Eighth"}`, 1))
	e.put("requests/side-5.json", req("sidejson-5", 7))
	e.put("rdir/request.json", yue2Request("rd-2", 3))
	e.flac("rdir/audio.flac", "rd2")
	e.flac("anything.flac", "m1")
	e.flac("tagged.flac", "t3", "MM_REQUEST_ID=tag-3")
	e.flac("tagged-seed.flac", "ts", "MM_REQUEST_ID=tag-seed", "MM_SEED=2")
	e.flac("commented.flac", "cm", "COMMENT=cmt-3")
	e.flac("fn-4.flac", "f4")
	e.flac("music/side-5.flac", "s5")
	e.flac("Slug Six.flac", "s6")
	e.flac("seven_s77.flac", "s7")
	e.flac("The Eighth.flac", "s8")
	e.flac("grp_9.flac", "g9")
	e.flac("amb-10.flac", "a10")
	e.flac("lev-11x.flac", "l11")
	e.flac("prec_12.flac", "p12", "MM_REQUEST_ID=prec-12")
	e.flac("skipme.flac", "sk")
	e.flac("nothing-like-it.flac", "nn")
	e.mapping("audio,request_id,seed,title,parent\nanything.flac,m-1,2,,\nskipme.flac,-,,,\n")
	_, inv := e.inventory()
	r := &inv.Report
	cases := []struct {
		path, rule, id string
		seed           int64
	}{
		{"anything.flac", "mapping", "m-1", 2},
		{"rdir/audio.flac", "result_dir", "rd-2", 3},
		{"tagged.flac", "tag", "tag-3", 4},
		{"tagged-seed.flac", "tag", "tag-seed", 2},
		{"commented.flac", "tag", "cmt-3", 5},
		{"fn-4.flac", "filename_id", "fn-4", 6},
		{"music/side-5.flac", "sidecar_json", "sidejson-5", 7},
		{"Slug Six.flac", "slug", "slug-six", 8},
		{"seven_s77.flac", "slug_seed", "seven", 77},
		{"The Eighth.flac", "title_slug", "eight-8", 9},
		{"prec_12.flac", "tag", "prec-12", 13},
	}
	for _, c := range cases {
		a := audioByPath(r, c.path)
		if a == nil || a.Status != "new" || a.Match == nil || a.Match.Rule != c.rule || a.Match.RequestID != c.id {
			t.Errorf("%s: %+v, want rule %s id %s", c.path, a, c.rule, c.id)
			continue
		}
		if rec := recipeByID(r, c.id, c.seed); rec == nil || rec.Hash != a.Match.RecipeHash {
			t.Errorf("%s: matched hash %s is not %s/%d", c.path, a.Match.RecipeHash, c.id, c.seed)
		}
	}
	if a := audioByPath(r, "skipme.flac"); a == nil || a.Status != "skipped" {
		t.Errorf("skipme = %+v", a)
	}
	a := audioByPath(r, "grp_9.flac")
	if a == nil || a.Status != "unmatched" || a.Match != nil || len(a.Candidates) < 2 || a.Candidates[0] != "grp-9-v1#10" || a.Candidates[1] != "grp-9-v2#11" {
		t.Errorf("group_slug must give candidates only: %+v", a)
	}
	a = audioByPath(r, "amb-10.flac")
	if a == nil || a.Status != "ambiguous" || len(a.Candidates) != 2 || a.Candidates[0] != "amb-10#1" {
		t.Errorf("ambiguous = %+v", a)
	}
	a = audioByPath(r, "lev-11x.flac")
	if a == nil || a.Status != "unmatched" || len(a.Candidates) == 0 || a.Candidates[0] != "lev-11#12" {
		t.Errorf("unmatched-with-candidates = %+v", a)
	}
	a = audioByPath(r, "nothing-like-it.flac")
	if a == nil || a.Status != "unmatched" || len(a.Candidates) != 0 {
		t.Errorf("unmatched = %+v", a)
	}
	if r.Summary.AudioAmbiguous != 1 || r.Summary.AudioUnmatched != 3 || r.Summary.AudioSkipped != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
	// every undecided audio is pre-filled in the mapping template
	var sb strings.Builder
	if err := WriteMappingTemplate(&sb, inv); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"grp_9.flac", "amb-10.flac", "lev-11x.flac", "nothing-like-it.flac"} {
		if !strings.Contains(sb.String(), p+",") {
			t.Errorf("template misses %s:\n%s", p, sb.String())
		}
	}
}

func TestMatch_DuplicateGroupStrongestRule(t *testing.T) {
	e := newEnv(t)
	e.put("recipes.jsonl", lines(req("dup-1", 5)))
	e.flac("aaa/dup_1.flac", "same")
	e.put("zzz/rd/request.json", yue2Request("dup-1", 5))
	e.flac("zzz/rd/audio.flac", "same")
	_, inv := e.inventory()
	r := &inv.Report
	if len(r.Duplicates) != 1 || r.Duplicates[0].Rule != "result_dir" || r.Duplicates[0].Representative != "zzz/rd/audio.flac" || len(r.Duplicates[0].Paths) != 2 {
		t.Fatalf("duplicates = %+v", r.Duplicates)
	}
	if a := audioByPath(r, "zzz/rd/audio.flac"); a.Status != "new" || a.Match.Rule != "result_dir" {
		t.Errorf("rep = %+v", a)
	}
	if a := audioByPath(r, "aaa/dup_1.flac"); a.Status != "duplicate" {
		t.Errorf("copy = %+v", a)
	}
	if r.Summary.TracksNew != 1 || r.Summary.DuplicateGroups != 1 || r.Summary.AudioIdentities != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
	// members that name different recipes at the strongest rule are ambiguous
	e2 := newEnv(t)
	e2.put("recipes.jsonl", lines(req("one-1", 1), req("two-2", 2)))
	e2.flac("one_1.flac", "x")
	e2.flac("two_2.flac", "x")
	_, inv2 := e2.inventory()
	if a := audioByPath(&inv2.Report, "one_1.flac"); a.Status != "ambiguous" {
		t.Errorf("conflicting members: %+v", a)
	}
}

func TestMatch_RetaggedCopyAlreadyImported(t *testing.T) {
	e := newEnv(t)
	e.put("recipes.jsonl", lines(req("rt-1", 1)))
	e.flac("a/rt_1.flac", "audio")
	e.applyAll()
	if e.count("tracks") != 1 {
		t.Fatal("setup")
	}
	// the original disappears; a player re-tagged copy shows up elsewhere
	e.rm("a/rt_1.flac")
	e.flac("b/Renamed.flac", "audio", "TITLE=Added by a player", "ARTIST=someone")
	_, inv := e.inventory()
	a := audioByPath(&inv.Report, "b/Renamed.flac")
	if a == nil || a.Status != "already_imported" {
		t.Fatalf("retagged copy = %+v", a)
	}
	if inv.Report.Summary.TracksNew != 0 || inv.Report.Summary.AudioAlreadyImported != 1 {
		t.Errorf("summary = %+v", inv.Report.Summary)
	}
	before := e.counts()
	e.applyAll()
	if e.count("tracks") != before["tracks"] {
		t.Error("retagged copy created a second track")
	}
}

func TestMatch_PossibleReencodeWarning(t *testing.T) {
	e := newEnv(t)
	md5 := [16]byte{1, 2, 3, 4, 5, 6, 7, 8}
	for i, name := range []string{"one.flac", "two.flac"} {
		flactest.WriteFile(t, filepath.Join(e.inboxDir, name), flactest.Spec{TotalSamples: 1000, MD5: md5, Frames: []byte{byte(i), 9}})
		e.age(name, e.now.Add(-time.Hour))
	}
	flactest.WriteFile(t, filepath.Join(e.inboxDir, "zero1.flac"), flactest.Spec{TotalSamples: 1000, Frames: []byte{1}})
	flactest.WriteFile(t, filepath.Join(e.inboxDir, "zero2.flac"), flactest.Spec{TotalSamples: 1000, Frames: []byte{2}})
	_, inv := e.inventory()
	n := 0
	for _, w := range inv.Report.Warnings {
		if w.Code == "possible_reencode" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("possible_reencode warnings = %d (%v); all-zero MD5s must not count", n, issueCodes(inv.Report.Warnings))
	}
	if inv.Report.Summary.AudioIdentities != 4 {
		t.Errorf("both files must still be processed: identities = %d", inv.Report.Summary.AudioIdentities)
	}
}
