package yue2

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"sort"
)

// BatchReport is the outcome of ValidateBatch.
type BatchReport struct {
	Lines, Requests int
	OK, Fatal       bool
	Issues          []Issue // Line set; sorted by line
}

// ValidateBatch mirrors `yue2 batch` (yue2-infer 0.1.6 src/yue2/cli.py): the file is split on "\n",
// a trailing "\r" is JSON whitespace, whitespace-only lines are skipped (the iteration skips lines
// whose strip() is empty), every other line must be a JSON object, ids are unique across the file.
// dir is the JSONL file's directory (abc_path targets); nil means abc_path is unresolved.
func ValidateBatch(jsonl []byte, dir fs.FS) BatchReport {
	var rep BatchReport
	var resolver ABCResolver
	if dir != nil {
		resolver = func(rel string) ([]byte, error) {
			if !fs.ValidPath(rel) {
				return nil, fs.ErrInvalid
			}
			return fs.ReadFile(dir, rel)
		}
	}
	lines := bytes.Split(jsonl, []byte("\n"))
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	rep.Lines = len(lines)
	seen := map[string]bool{}
	for i, ln := range lines {
		no := i + 1
		ln = bytes.TrimRight(ln, "\r")
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		rep.Requests++
		fail := func(code, msg string) {
			rep.Issues = append(rep.Issues, Issue{Code: code, Severity: sevError, Message: msg, Line: no})
		}
		if !json.Valid(ln) {
			fail("bad_json", "line is not valid JSON")
			continue
		}
		if t := bytes.TrimSpace(ln); t[0] != '{' {
			fail("not_object", "line is not a JSON object")
			continue
		}
		for _, is := range CheckRequest(ln, ModeYuE2, resolver) {
			is.Line = no
			rep.Issues = append(rep.Issues, is)
			if is.Fatal {
				rep.Fatal = true
			}
		}
		var h struct {
			ID *string `json:"id"`
		}
		if json.Unmarshal(ln, &h) == nil && h.ID != nil {
			if seen[*h.ID] {
				rep.Issues = append(rep.Issues, Issue{Code: "duplicate_id", Field: "id", Severity: sevError,
					Message: "id already used earlier in the file", Line: no})
			}
			seen[*h.ID] = true
		}
	}
	sort.SliceStable(rep.Issues, func(a, b int) bool { return rep.Issues[a].Line < rep.Issues[b].Line })
	rep.OK = len(rep.Issues) == 0
	return rep
}
