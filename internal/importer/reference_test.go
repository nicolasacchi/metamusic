package importer

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
)

func TestReference_PlainImportIgnores(t *testing.T) {
	e := newEnv(t)
	e.flac("music/a.flac", "fa")
	e.flac("music/b.flac", "fb")
	e.mp3("lossy/x.mp3", 1)
	e.mp3("lossy/y.mp3", 2)
	before := e.counts()
	_, inv := e.inventory()
	r := inv.Report
	if r.Summary.TracksNew != 0 || r.Summary.TracksReference != 0 || r.Summary.AudioReference != 0 {
		t.Errorf("summary = %+v", r.Summary)
	}
	for _, p := range []string{"music/a.flac", "music/b.flac"} {
		if a := audioByPath(&r, p); a == nil || a.Status != "unmatched" || a.Format != "flac" {
			t.Errorf("%s = %+v", p, a)
		}
	}
	for _, p := range []string{"lossy/x.mp3", "lossy/y.mp3"} {
		if a := audioByPath(&r, p); a == nil || a.Status != "unmatched" || a.Format != "mp3" {
			t.Errorf("%s = %+v", p, a)
		}
	}
	orphans := 0
	for _, ig := range r.Ignored {
		if ig.Reason == "orphan_mp3" {
			orphans++
		}
	}
	if orphans != 2 || !hasCode(r.Warnings, "orphan_mp3") {
		t.Errorf("orphans = %d, warnings = %v", orphans, issueCodes(r.Warnings))
	}
	tpl := mappingTemplate(t, inv)
	for _, p := range []string{"music/a.flac", "music/b.flac", "lossy/x.mp3", "lossy/y.mp3"} {
		if !strings.Contains(tpl, p) {
			t.Errorf("template lacks %s:\n%s", p, tpl)
		}
	}
	e.applyAll()
	after := e.counts()
	if after["tracks"] != before["tracks"] || after["recipes"] != before["recipes"] {
		t.Errorf("rows changed: %v -> %v", before, after)
	}
}

func TestDryRun_ListsRecipeLess(t *testing.T) {
	e := newEnv(t)
	e.flac("music/a.flac", "fa")
	e.flac("music/b.flac", "fb")
	e.mp3("lossy/x.mp3", 1)
	e.mp3("lossy/y.mp3", 2)
	_, inv := e.inventory()
	if inv.Report.Summary.AudioUnmatched != 2 {
		t.Errorf("AudioUnmatched = %d, want 2 (FLAC only)", inv.Report.Summary.AudioUnmatched)
	}
	var sb strings.Builder
	inv.Report.FormatText(&sb)
	out := sb.String()
	if !strings.Contains(out, "Without a recipe") || !strings.Contains(out, "--reference-unmatched") || !strings.Contains(out, "--reference-lossy") {
		t.Errorf("text lacks the block:\n%s", out)
	}
	if inv.PlanDigest() != digestOf(nil) || len(inv.PlanEntries()) != 0 {
		t.Errorf("the listing changed the plan: %v", inv.PlanEntries())
	}
}

func TestReference_FlagUnmatched(t *testing.T) {
	t.Run("two unmatched", func(t *testing.T) {
		e := newEnv(t)
		e.flac("music/a.flac", "fa")
		e.flac("music/b.flac", "fb")
		e.mp3("lossy/x.mp3", 1)
		_, inv := e.inventory(withRefFlags(true, false))
		s := inv.Report.Summary
		if s.TracksNew != 2 || s.TracksReference != 2 || s.AudioReference != 2 || s.TracksLossy != 0 {
			t.Errorf("summary = %+v", s)
		}
		for _, p := range []string{"music/a.flac", "music/b.flac"} {
			if a := audioByPath(&inv.Report, p); a == nil || a.Status != "reference" {
				t.Errorf("%s = %+v", p, a)
			}
		}
		if a := audioByPath(&inv.Report, "lossy/x.mp3"); a == nil || a.Status != "unmatched" {
			t.Errorf("the MP3 is unaffected by --reference-unmatched: %+v", a)
		}
		if !inv.Report.Flags.ReferenceUnmatched || inv.Report.Flags.ReferenceLossy {
			t.Errorf("flags = %+v", inv.Report.Flags)
		}
		if !planHas(inv, `"rule":"reference"`) || !planHas(inv, `"reference":true`) {
			t.Errorf("plan = %v", inv.PlanEntries())
		}
	})
	t.Run("matched stays normal, ambiguous stays ambiguous", func(t *testing.T) {
		e := newEnv(t)
		e.put("r.jsonl", lines(req("real-song", 1), req("amb", 1), req("amb", 2)))
		e.flac("real-song.flac", "fr")
		e.flac("amb.flac", "fm")
		e.flac("music/a.flac", "fa")
		e.flac("music/b.flac", "fb")
		_, inv := e.inventory(withRefFlags(true, false))
		s := inv.Report.Summary
		if s.TracksNew != 3 || s.TracksReference != 2 {
			t.Errorf("summary = %+v", s)
		}
		if a := audioByPath(&inv.Report, "real-song.flac"); a == nil || a.Status != "new" {
			t.Errorf("matched = %+v", a)
		}
		if a := audioByPath(&inv.Report, "amb.flac"); a == nil || a.Status != "ambiguous" {
			t.Errorf("ambiguous = %+v", a)
		}
	})
}

func TestReference_AmbiguousNeverAuto(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("amb", 1), req("amb", 2)))
	e.flac("amb.flac", "fm")
	e.applyAll(withRefFlags(true, true))
	if n := e.count("tracks"); n != 0 {
		t.Errorf("an ambiguous FLAC became a track (%d tracks)", n)
	}
}

func TestReference_MappingTarget(t *testing.T) {
	t.Run("title and no flag", func(t *testing.T) {
		e := newEnv(t)
		e.flac("music/x.flac", "fx")
		e.mapping("music/x.flac,reference,,My Title,\n")
		inv, _ := e.applyAll()
		a := audioByPath(&inv.Report, "music/x.flac")
		if a == nil || a.Status != "reference" || a.Title != "My Title" {
			t.Fatalf("audio = %+v", a)
		}
		tr := e.trackBySlug("my_title")
		if tr.ImportRule != library.RuleReference || tr.Title != "My Title" {
			t.Errorf("track = %+v", tr)
		}
	})
	t.Run("seed is a bad row", func(t *testing.T) {
		e := newEnv(t)
		e.flac("music/x.flac", "fx")
		e.mapping("music/x.flac,reference,7,My Title,\n")
		_, inv := e.inventory()
		if !hasCode(inv.Report.Errors, "bad_mapping_row") {
			t.Errorf("errors = %v", issueCodes(inv.Report.Errors))
		}
	})
	t.Run("overrides a rule match", func(t *testing.T) {
		e := newEnv(t)
		e.put("r.jsonl", lines(req("real-song", 1)))
		e.flac("real-song.flac", "fr")
		e.mapping("real-song.flac,reference,,,\n")
		_, inv := e.inventory()
		a := audioByPath(&inv.Report, "real-song.flac")
		if a == nil || a.Status != "reference" || !hasCode(inv.Report.Warnings, "reference_overrides_match") {
			t.Errorf("audio = %+v, warnings = %v", a, issueCodes(inv.Report.Warnings))
		}
	})
}

func TestReference_SkipWins(t *testing.T) {
	e := newEnv(t)
	e.flac("music/x.flac", "fx")
	e.mapping("music/x.flac,-,,,\n")
	_, inv := e.inventory(withRefFlags(true, true))
	if a := audioByPath(&inv.Report, "music/x.flac"); a == nil || a.Status != "skipped" {
		t.Errorf("audio = %+v", a)
	}
}

func TestReference_StubIdNeverTagMatches(t *testing.T) {
	e := newEnv(t)
	e.flac("music/a.flac", "fa")
	e.applyAll(withRefFlags(true, false))
	stub := e.scalar(`SELECT request_id FROM recipes WHERE engine = 'unknown'`)
	if !strings.HasPrefix(stub, "ref_") {
		t.Fatalf("stub id = %q", stub)
	}
	e.flac("music/other.flac", "fo", "MM_REQUEST_ID="+stub)
	_, inv := e.inventory()
	if a := audioByPath(&inv.Report, "music/other.flac"); a == nil || a.Status != "unmatched" {
		t.Errorf("tagged with a stub id = %+v", a)
	}
}

func TestApply_ReferenceTakes(t *testing.T) {
	e := newEnv(t)
	e.flac("music/a.flac", "fa")
	e.mp3("lossy/x.mp3", 1)
	e.applyAll(withRefFlags(true, true))

	if n := e.count("tracks"); n != 2 {
		t.Fatalf("tracks = %d", n)
	}
	var sawFlac, sawMP3 bool
	e.ing.mu.Lock()
	for _, r := range e.ing.requests {
		if len(r.Artifacts) != 0 || r.Seed != 0 {
			t.Errorf("request = %+v", r)
		}
		switch {
		case r.Reference:
			sawFlac = true
			if !strings.HasPrefix(r.RecipeID, "ref_") || r.RecipeID != r.RequestID {
				t.Errorf("FLAC request ids = %q / %q", r.RecipeID, r.RequestID)
			}
		default:
			sawMP3 = true
		}
	}
	e.ing.mu.Unlock()
	if !sawFlac || !sawMP3 {
		t.Errorf("requests: flac %v, mp3 %v", sawFlac, sawMP3)
	}
	if got := e.scalar(`SELECT count(*) FROM tracks WHERE lossy = 1 AND import_rule = 'reference' AND bits_per_sample = 16 AND master_path LIKE '%.mp3'`); got != "1" {
		t.Errorf("lossy tracks = %s", got)
	}
	if got := e.scalar(`SELECT count(*) FROM tracks WHERE lossy = 0 AND import_rule = 'reference'`); got != "1" {
		t.Errorf("flac reference tracks = %s", got)
	}
	if got := e.scalar(`SELECT count(*) FROM recipes WHERE engine = 'unknown'`); got != "2" {
		t.Errorf("stubs = %s", got)
	}
	if got := e.scalar(`SELECT count(*) FROM renditions r JOIN tracks t ON t.id = r.track_id WHERE t.lossy = 1 AND r.kind <> 'peaks'`); got != "0" {
		t.Errorf("lossy renditions other than peaks = %s", got)
	}

	before := e.counts()
	_, inv := e.inventory(withRefFlags(true, true))
	for _, a := range inv.Report.Audio {
		if a.Status != "already_imported" {
			t.Errorf("%s = %s after the import", a.Path, a.Status)
		}
	}
	_, res := e.applyAll(withRefFlags(true, true))
	if res.TracksImported != 0 {
		t.Errorf("second run imported %d", res.TracksImported)
	}
	after := e.counts()
	for _, k := range []string{"tracks", "recipes", "renditions"} {
		if before[k] != after[k] {
			t.Errorf("%s: %d -> %d", k, before[k], after[k])
		}
	}
}

func TestApply_ReferenceFailedIngestLeavesNoStub(t *testing.T) {
	e := newEnv(t)
	e.flac("music/a.flac", "fa")
	e.mp3("lossy/x.mp3", 1)
	e.ing.fail = func(ingest.Request) error { return &ingest.Error{Code: ingest.CodeToolFailed, Detail: "tool exited 1"} }
	before := e.count("recipes")
	im, inv := e.inventory(withRefFlags(true, true))
	res, err := im.Apply(t.Context(), inv, ApplyOptions{ExpectDigest: inv.PlanDigest()})
	if err != nil {
		t.Fatal(err)
	}
	if res.TracksFailed != 2 || e.count("recipes") != before || e.count("tracks") != 0 {
		t.Errorf("failed = %d, recipes %d -> %d, tracks %d", res.TracksFailed, before, e.count("recipes"), e.count("tracks"))
	}
}

func TestPlanDigest_ReferenceCoverage(t *testing.T) {
	digest := func(setup func(e *env), flags func(*Options)) string {
		e := newEnv(t)
		e.flac("music/a.flac", "fa")
		e.mp3("lossy/x.mp3", 1)
		if setup != nil {
			setup(e)
		}
		_, inv := e.inventory(flags)
		return inv.PlanDigest()
	}
	both := withRefFlags(true, true)
	base := digest(nil, both)
	if base == digest(nil, withRefFlags(true, false)) || base == digest(nil, withRefFlags(false, true)) || base == digest(nil, withRefFlags(false, false)) {
		t.Error("the flags do not change the digest")
	}
	if base != digest(nil, both) {
		t.Error("the digest is not stable")
	}
	if base == digest(func(e *env) { e.flac("music/a.flac", "changed") }, both) {
		t.Error("changed FLAC bytes keep the digest")
	}
	if base == digest(func(e *env) { e.mp3("lossy/x.mp3", 9) }, both) {
		t.Error("changed MP3 bytes keep the digest")
	}
	if base == digest(func(e *env) { e.mapping("lossy/x.mp3,reference,,Other Title,\n") }, both) {
		t.Error("a mapping title keeps the digest")
	}
}

func TestPlanChanged_ReferenceFile(t *testing.T) {
	e := newEnv(t)
	e.mp3("lossy/x.mp3", 1)
	_, inv := e.inventory(withRefFlags(false, true))
	e.mp3("lossy/x.mp3", 2)
	im2, inv2 := e.inventory(withRefFlags(false, true))
	if _, err := im2.Apply(t.Context(), inv2, ApplyOptions{ExpectDigest: inv.PlanDigest()}); err == nil {
		t.Fatal("apply accepted a changed plan")
	}
}
