package yue2

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidateBatch_Table(t *testing.T) {
	l := func(id string) string { return `{"id":"` + id + `","style":"s","lyrics":"l"}` }
	fsys := fstest.MapFS{"a.abc": {Data: []byte("X:1")}}
	rows := []struct {
		name        string
		in          string
		dir         bool
		lines, reqs int
		ok, fatal   bool
		wantCode    string
		wantLine    int
	}{
		{name: "three valid", in: l("a") + "\n" + l("b") + "\n" + l("c") + "\n", lines: 3, reqs: 3, ok: true},
		{name: "no trailing newline", in: l("a") + "\n" + l("b"), lines: 2, reqs: 2, ok: true},
		{name: "duplicate id", in: l("a") + "\n" + l("b") + "\n" + l("a") + "\n", lines: 3, reqs: 3, wantCode: "duplicate_id", wantLine: 3},
		{name: "unknown key fatal", in: `{"id":"a","style":"s","lyrics":"l","title":"t"}` + "\n", lines: 1, reqs: 1, fatal: true, wantCode: "unknown_key", wantLine: 1},
		{name: "blank lines skipped", in: l("a") + "\n\n  \t\n" + l("b") + "\n", lines: 4, reqs: 2, ok: true},
		{name: "CRLF", in: l("a") + "\r\n" + l("b") + "\r\n", lines: 2, reqs: 2, ok: true},
		{name: "abc_path resolved", in: `{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}` + "\n", dir: true, lines: 1, reqs: 1, ok: true},
		{name: "abc_path missing", in: `{"id":"a","style":"s","lyrics":"l","abc_path":"z.abc"}` + "\n", dir: true, lines: 1, reqs: 1, wantCode: "abc_path_unresolved", wantLine: 1},
		{name: "abc_path escaping", in: `{"id":"a","style":"s","lyrics":"l","abc_path":"../a.abc"}` + "\n", dir: true, lines: 1, reqs: 1, wantCode: "abc_path_unresolved", wantLine: 1},
		{name: "abc_path nil dir", in: `{"id":"a","style":"s","lyrics":"l","abc_path":"a.abc"}` + "\n", lines: 1, reqs: 1, wantCode: "abc_path_unresolved", wantLine: 1},
		{name: "not object", in: l("a") + "\n[1]\n", lines: 2, reqs: 2, wantCode: "not_object", wantLine: 2},
		{name: "bad json", in: "{not json\n", lines: 1, reqs: 1, wantCode: "bad_json", wantLine: 1},
		{name: "duplicate keys last wins", in: `{"id":"a","id":"b","style":"s","lyrics":"l"}` + "\n" + l("a") + "\n", lines: 2, reqs: 2, ok: true},
		{name: "empty", in: "", lines: 0, reqs: 0, ok: true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			var rep BatchReport
			if r.dir {
				rep = ValidateBatch([]byte(r.in), fsys)
			} else {
				rep = ValidateBatch([]byte(r.in), nil)
			}
			if rep.Lines != r.lines || rep.Requests != r.reqs || rep.OK != r.ok || rep.Fatal != r.fatal {
				t.Fatalf("report = %+v", rep)
			}
			if r.wantCode != "" {
				var f bool
				for _, i := range rep.Issues {
					if i.Code == r.wantCode && i.Line == r.wantLine {
						f = true
					}
				}
				if !f {
					t.Fatalf("issues = %+v, want %s on line %d", rep.Issues, r.wantCode, r.wantLine)
				}
			}
			if r.ok && len(rep.Issues) != 0 {
				t.Fatalf("issues = %+v", rep.Issues)
			}
		})
	}
}

func TestValidateBatch_IssuesSortedByLine(t *testing.T) {
	in := strings.Join([]string{`{"id":"a","style":"s","lyrics":"l"}`, `[1]`, `{"id":"a","style":"s","lyrics":"l"}`, `{x`}, "\n")
	rep := ValidateBatch([]byte(in), nil)
	last := 0
	for _, i := range rep.Issues {
		if i.Line < last {
			t.Fatalf("unsorted: %+v", rep.Issues)
		}
		last = i.Line
	}
	if len(rep.Issues) != 3 {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}
