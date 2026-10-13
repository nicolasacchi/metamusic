package importer

import "testing"

func lossyTrio(e *env) {
	for i, n := range []string{"trio_p1", "trio_p2", "trio_p3", "lone", "pair_p2", "pair_p5"} {
		e.mp3("lossy/"+n+".mp3", i+1)
	}
}

func TestLossy_FlagAndGroups(t *testing.T) {
	e := newEnv(t)
	lossyTrio(e)
	_, inv := e.inventory(withRefFlags(false, true))
	s := inv.Report.Summary
	if s.TracksNew != 6 || s.TracksReference != 6 || s.TracksLossy != 6 || s.ReferenceGroups != 2 || s.SiblingsInferred != 3 {
		t.Fatalf("summary = %+v", s)
	}
	parent := func(p string) *ParentReport { return audioByPath(&inv.Report, "lossy/"+p+".mp3").Parent }
	for _, n := range []string{"trio_p1", "lone", "pair_p2"} {
		if parent(n) != nil {
			t.Errorf("%s has a parent %+v", n, parent(n))
		}
	}
	for _, c := range [][2]string{{"trio_p2", "trio_p1"}, {"trio_p3", "trio_p1"}, {"pair_p5", "pair_p2"}} {
		p := parent(c[0])
		if p == nil || p.Rule != "reference_group" || p.Of != "lossy/"+c[1]+".mp3" {
			t.Errorf("%s parent = %+v", c[0], p)
		}
	}
	e.applyAll(withRefFlags(false, true))
	tr := e.tracks()
	byslug := map[string]trackRow{}
	for _, x := range tr {
		byslug[x.Slug] = x
	}
	p1 := byslug["trio_p1"]
	for _, n := range []string{"trio_p2", "trio_p3"} {
		x := byslug[n]
		if x.Parent != p1.ID || x.Root != p1.ID || x.Gen != 1 || x.Rule != "reference_group" {
			t.Errorf("%s = %+v (root %s)", n, x, p1.ID)
		}
	}
	if got := e.scalar(`SELECT count(*) FROM mutations WHERE inferred = 1`); got != "3" {
		t.Errorf("inferred mutations = %s", got)
	}
}

func TestReferenceGroups_ExistingRoot(t *testing.T) {
	e := newEnv(t)
	lossyTrio(e)
	e.applyAll(withRefFlags(false, true))
	p1 := e.trackBySlug("trio_p1")
	before := e.tracks()

	e.mp3("lossy/trio_p4.mp3", 40)
	_, inv := e.inventory(withRefFlags(false, true))
	a := audioByPath(&inv.Report, "lossy/trio_p4.mp3")
	if a == nil || a.Parent == nil || a.Parent.Of != p1.ID || a.Parent.Rule != "reference_group" {
		t.Fatalf("p4 = %+v", a)
	}
	e.applyAll(withRefFlags(false, true))
	p4 := e.trackBySlug("trio_p4")
	if p4.Parent != p1.ID || p4.Gen != 1 {
		t.Errorf("p4 = %+v", p4)
	}
	after := e.tracks()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("existing row %s changed: %+v -> %+v", k, v, after[k])
		}
	}

	e.mp3("lossy/trio_p0.mp3", 41)
	_, inv = e.inventory(withRefFlags(false, true))
	a = audioByPath(&inv.Report, "lossy/trio_p0.mp3")
	if a == nil || a.Parent == nil || a.Parent.Of != p1.ID {
		t.Fatalf("p0 = %+v", a)
	}
	if !hasCode(inv.Report.Warnings, "older_than_root") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
}

func TestReferenceGroups_MappingParent(t *testing.T) {
	e := newEnv(t)
	lossyTrio(e)
	e.mapping("lossy/trio_p3.mp3,reference,,,lossy/lone.mp3\nlossy/trio_p2.mp3,reference,,,-\n")
	_, inv := e.inventory(withRefFlags(false, true))
	p3 := audioByPath(&inv.Report, "lossy/trio_p3.mp3")
	if p3.Parent == nil || p3.Parent.Rule != "mapping" || p3.Parent.Of != "lossy/lone.mp3" {
		t.Errorf("p3 = %+v", p3.Parent)
	}
	if p2 := audioByPath(&inv.Report, "lossy/trio_p2.mp3"); p2.Parent != nil {
		t.Errorf("p2 opted out but has %+v", p2.Parent)
	}
	e.applyAll(withRefFlags(false, true))
	if got := e.scalar(`SELECT inferred FROM mutations m JOIN tracks t ON t.id = m.child_track_id WHERE t.slug = 'trio_p3'`); got != "0" {
		t.Errorf("mapping mutation inferred = %q", got)
	}
}

func TestReferenceGroups_NoSeedFamily(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("fam", 1), req("fam-s1101", 1101)))
	e.flac("fam.flac", "k1")
	e.flac("fam_s1101.flac", "k2")
	e.flac("music/ref_x_s9.flac", "k3")
	e.mp3("lossy/ref_x_p1.mp3", 1)
	_, inv := e.inventory(withRefFlags(true, true))
	for _, p := range []string{"music/ref_x_s9.flac", "lossy/ref_x_p1.mp3"} {
		a := audioByPath(&inv.Report, p)
		if a == nil || a.Status != "reference" || a.Parent != nil {
			t.Errorf("%s = %+v", p, a)
		}
	}
	if inv.Report.Summary.SiblingsInferred != 1 {
		t.Errorf("the real seed family lost its edge")
	}
}
