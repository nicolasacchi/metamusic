package importer

import (
	"strings"
	"testing"
)

func TestLossy_ClassifyMP3(t *testing.T) {
	type tc struct {
		name   string
		setup  func(e *env)
		flags  func(*Options)
		path   string
		kind   string // entry reason in Ignored, "" when not ignored
		status string // audio status, "" when no audio entry
		code   string // warning or error code expected
	}
	cases := []tc{
		{name: "sidecar of one FLAC", setup: func(e *env) { e.flac("m/a.flac", "fa"); e.mp3("m/a.mp3", 1) },
			flags: withRefFlags(true, true), path: "m/a.mp3", kind: "mp3_sidecar"},
		{name: "mismatched stems", setup: func(e *env) { e.flac("m/a.flac", "fa"); e.flac("n/a.flac", "fb"); e.mp3("o/a.mp3", 1) },
			flags: withRefFlags(true, true), path: "o/a.mp3", kind: "orphan_mp3", code: "orphan_mp3"},
		{name: "fake ID3 junk", setup: func(e *env) { e.put("m/fake.mp3", "ID3\x03\x00\x00\x00\x00\x00\x00synthetic") },
			flags: withRefFlags(true, true), path: "m/fake.mp3", kind: "orphan_mp3", code: "orphan_mp3"},
		{name: "valid standalone without flag", setup: func(e *env) { e.mp3("m/s.mp3", 1) },
			path: "m/s.mp3", kind: "orphan_mp3", status: "unmatched", code: "orphan_mp3"},
		{name: "valid standalone with flag", setup: func(e *env) { e.mp3("m/s.mp3", 1) },
			flags: withRefFlags(false, true), path: "m/s.mp3", status: "reference"},
		{name: "valid standalone mapped reference", setup: func(e *env) { e.mp3("m/s.mp3", 1); e.mapping("m/s.mp3,reference,,,\n") },
			path: "m/s.mp3", status: "reference"},
		{name: "mapping skip", setup: func(e *env) { e.mp3("m/s.mp3", 1); e.mapping("m/s.mp3,-,,,\n") },
			flags: withRefFlags(true, true), path: "m/s.mp3", kind: "orphan_mp3", status: "skipped"},
		{name: "mapping request id", setup: func(e *env) { e.mp3("m/s.mp3", 1); e.mapping("m/s.mp3,some-id,,,\n") },
			flags: withRefFlags(true, true), path: "m/s.mp3", kind: "orphan_mp3", code: "lossy_needs_reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			if c.flags == nil {
				c.flags = withRefFlags(false, false)
			}
			_, inv := e.inventory(c.flags)
			r := inv.Report
			ign := ""
			for _, ig := range r.Ignored {
				if ig.Path == c.path {
					ign = ig.Reason
				}
			}
			if ign != c.kind {
				t.Errorf("ignored reason = %q, want %q", ign, c.kind)
			}
			st := ""
			if a := audioByPath(&r, c.path); a != nil {
				st = a.Status
				if a.Format != "mp3" {
					t.Errorf("format = %q", a.Format)
				}
			}
			if st != c.status {
				t.Errorf("audio status = %q, want %q", st, c.status)
			}
			if c.code != "" && !hasCode(r.Warnings, c.code) && !hasCode(r.Errors, c.code) {
				t.Errorf("no %s in warnings %v / errors %v", c.code, issueCodes(r.Warnings), issueCodes(r.Errors))
			}
			if c.code == "lossy_needs_reference" {
				e.applyAllForce(inv)
				if n := e.count("tracks"); n != 0 {
					t.Errorf("a refused MP3 was imported")
				}
			}
		})
	}
}

// applyAllForce applies inv even though it carries errors.
func (e *env) applyAllForce(inv *Inventory) {
	e.t.Helper()
	im := e.importer(withRefFlags(true, true))
	if _, err := im.Apply(e.t.Context(), inv, ApplyOptions{Force: true}); err != nil {
		e.t.Fatal(err)
	}
}

func TestLossy_MappingNeedsReference(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("some-id", 1)))
	e.mp3("m/s.mp3", 1)
	e.mapping("m/s.mp3,some-id,1,,\n")
	_, inv := e.inventory(withRefFlags(true, true))
	if !hasCode(inv.Report.Errors, "lossy_needs_reference") {
		t.Fatalf("errors = %v", issueCodes(inv.Report.Errors))
	}
	if inv.Report.Summary.TracksNew != 0 {
		t.Errorf("TracksNew = %d", inv.Report.Summary.TracksNew)
	}
}

func TestLossy_DuplicateCopies(t *testing.T) {
	e := newEnv(t)
	e.mp3("m/one.mp3", 5)
	e.mp3("m/two.mp3", 5)
	_, inv := e.inventory(withRefFlags(false, true))
	r := inv.Report
	if len(r.Duplicates) != 1 || len(r.Duplicates[0].Paths) != 2 {
		t.Fatalf("duplicates = %+v", r.Duplicates)
	}
	if r.Summary.TracksNew != 1 || r.Summary.TracksLossy != 1 {
		t.Errorf("summary = %+v", r.Summary)
	}
	if !strings.HasPrefix(r.Duplicates[0].AudioID, "mp3:") {
		t.Errorf("audio id = %q", r.Duplicates[0].AudioID)
	}
	res := 0
	for _, a := range r.Audio {
		if a.Status == "reference" {
			res++
		}
	}
	if res != 1 {
		t.Errorf("%d reference entries, want one (the other is a duplicate)", res)
	}
}
