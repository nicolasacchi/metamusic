package importer

import (
	"testing"
	"time"
)

func TestSiblings_Infer(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("sib-1", 1), req("sib-1", 2), req("fam", 1), req("fam-s1101", 1101), req("fam_seed7", 7), req("lone", 1)))
	base := e.now.Add(-10 * time.Hour)
	for i, f := range []struct{ name, frames string }{
		{"sib_a.flac", "sa"}, {"sib_b.flac", "sb"}, {"fam.flac", "f0"}, {"fam_s1101.flac", "f1"}, {"fam_seed7.flac", "f2"}, {"lone.flac", "l"},
	} {
		e.flac(f.name, f.frames)
		e.age(f.name, base.Add(time.Duration(i)*time.Minute))
	}
	e.mapping("audio,request_id,seed\nsib_a.flac,sib-1,1\nsib_b.flac,sib-1,2\n")
	inv, _ := e.applyAll()
	if inv.Report.Summary.SiblingsInferred != 3 {
		t.Errorf("siblings_inferred = %d", inv.Report.Summary.SiblingsInferred)
	}
	tr := e.tracks()
	root, child := tr["sib-1#1"], tr["sib-1#2"]
	if root.Parent != "" || root.Root != root.ID || root.Gen != 0 {
		t.Errorf("root = %+v", root)
	}
	if child.Parent != root.ID || child.Root != root.ID || child.Gen != 1 || child.Rule != "same_request_id" {
		t.Errorf("child = %+v", child)
	}
	fr := tr["fam#1"]
	for _, k := range []string{"fam-s1101#1101", "fam_seed7#7"} {
		c := tr[k]
		if c.Parent != fr.ID || c.Root != fr.ID || c.Gen != 1 || c.Rule != "seed_family" {
			t.Errorf("%s = %+v (root %s)", k, c, fr.ID)
		}
	}
	if l := tr["lone#1"]; l.Parent != "" || l.Gen != 0 {
		t.Errorf("lone = %+v", l)
	}
	var inferred int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM mutations WHERE operator='inferred_sibling' AND inferred=1`).Scan(&inferred); err != nil || inferred != 3 {
		t.Errorf("mutations = %d %v", inferred, err)
	}

	// re-run: a newer newcomer attaches to the existing root; an older one does too, with a warning
	e.put("r2.jsonl", lines(req("sib-1", 3), req("sib-1", 4)))
	e.flac("sib_c.flac", "sc")
	e.age("sib_c.flac", base.Add(time.Hour))
	e.flac("sib_d.flac", "sd")
	e.age("sib_d.flac", base.Add(-time.Hour)) // older than the root
	e.mapping("audio,request_id,seed\nsib_a.flac,sib-1,1\nsib_b.flac,sib-1,2\nsib_c.flac,sib-1,3\nsib_d.flac,sib-1,4\n")
	inv2, _ := e.applyAll()
	if !hasCode(inv2.Report.Warnings, "older_than_root") {
		t.Errorf("warnings = %v", issueCodes(inv2.Report.Warnings))
	}
	tr = e.tracks()
	for _, k := range []string{"sib-1#3", "sib-1#4"} {
		if c := tr[k]; c.Parent != root.ID || c.Root != root.ID || c.Gen != 1 || c.Rule != "same_request_id" {
			t.Errorf("%s = %+v", k, c)
		}
	}
	if tr["sib-1#1"].Root != root.ID || tr["sib-1#1"].Parent != "" {
		t.Error("existing lineage was rewritten")
	}
}

func TestSiblings_MappingParent(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("pm-1", 1), req("pm-1", 2), req("pm-2", 1), req("pm-3", 1), req("pm-3", 2)))
	for i, n := range []string{"a", "b", "c", "d", "e"} {
		e.flac(n+".flac", n)
		e.age(n+".flac", e.now.Add(-time.Duration(10-i)*time.Hour))
	}
	// b would be a sibling of a; the mapping makes it a root. c hangs under the take a; d under the take e.
	e.mapping("audio,request_id,seed,title,parent\n" +
		"a.flac,pm-1,1,,\nb.flac,pm-1,2,,-\nc.flac,pm-2,1,,a.flac\nd.flac,pm-3,1,,e.flac\ne.flac,pm-3,2,,\n")
	_, inv := e.inventory()
	if inv.Report.Summary.SiblingsInferred != 2 {
		t.Errorf("edges = %d", inv.Report.Summary.SiblingsInferred)
	}
	e.applyAll()
	tr := e.tracks()
	a, b, c, d, ee := tr["pm-1#1"], tr["pm-1#2"], tr["pm-2#1"], tr["pm-3#1"], tr["pm-3#2"]
	if b.Parent != "" || b.Gen != 0 {
		t.Errorf("'-' must keep b a root: %+v", b)
	}
	if c.Parent != a.ID || c.Root != a.ID || c.Gen != 1 || c.Rule != "mapping" {
		t.Errorf("c = %+v", c)
	}
	if d.Parent != ee.ID || d.Root != ee.ID || d.Gen != 1 || d.Rule != "mapping" {
		t.Errorf("d = %+v", d)
	}
	var inferred int
	if err := e.st.R.QueryRow(`SELECT inferred FROM mutations WHERE child_track_id=?`, c.ID).Scan(&inferred); err != nil || inferred != 0 {
		t.Errorf("mapping mutation inferred = %d %v", inferred, err)
	}
}

func TestSiblings_MappingParentProblems(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("pp-1", 1), req("pp-2", 1), req("pp-2", 2)))
	for _, n := range []string{"a", "b", "c"} {
		e.flac(n+".flac", n)
	}
	// unknown parent, ambiguous request id (two roots), self parent
	e.mapping("audio,request_id,seed,title,parent\n" +
		"a.flac,pp-1,1,,nobody\nb.flac,pp-2,1,,\nc.flac,pp-2,2,,c.flac\n")
	_, inv := e.inventory()
	n := 0
	for _, w := range inv.Report.Warnings {
		if w.Code == "parent_unresolved" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("parent_unresolved = %d (%v)", n, issueCodes(inv.Report.Warnings))
	}
	e.applyAll()
	for k, tr := range e.tracks() {
		if tr.Parent != "" {
			t.Errorf("%s got a parent", k)
		}
	}
}

func TestSiblings_Cycle(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("cy-1", 1), req("cy-2", 1)))
	e.flac("a.flac", "a")
	e.flac("b.flac", "b")
	e.mapping("audio,request_id,seed,title,parent\na.flac,cy-1,1,,b.flac\nb.flac,cy-2,1,,a.flac\n")
	_, inv := e.inventory()
	if !hasCode(inv.Report.Warnings, "parent_cycle") {
		t.Fatalf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	e.applyAll()
	if e.count("tracks") != 2 {
		t.Errorf("tracks = %d", e.count("tracks"))
	}
}

func TestSiblings_VariantsNoLineage(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("pfx_001_v1", 1), req("pfx_001_v2", 2), req("pfx_001_v3", 3)))
	for _, n := range []string{"pfx_001_v1", "pfx_001_v2", "pfx_001_v3"} {
		e.flac(n+".flac", n)
	}
	inv, _ := e.applyAll()
	if inv.Report.Summary.SiblingsInferred != 0 {
		t.Errorf("edges = %d", inv.Report.Summary.SiblingsInferred)
	}
	if e.count("tracks") != 3 || e.count("mutations") != 0 {
		t.Errorf("tracks=%d mutations=%d", e.count("tracks"), e.count("mutations"))
	}
	for k, tr := range e.tracks() {
		if tr.Parent != "" || tr.Root != tr.ID || tr.Gen != 0 {
			t.Errorf("%s = %+v", k, tr)
		}
	}
}
