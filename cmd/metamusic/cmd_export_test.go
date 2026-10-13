package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSONL(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "songs.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodLine = `{"id":"syn-a","style":"pop","lyrics":"[verse]\nla la","cot":"full","seed":1}`

func TestValidateBatch_ExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		args    func(p string) []string
		code    int
		stdout  string
	}{
		{"valid", goodLine + "\n" + strings.Replace(goodLine, "syn-a", "syn-b", 1) + "\n", nil, 0, "2 requests, 0 issues"},
		{"duplicate id", goodLine + "\n" + goodLine + "\n", nil, 1, "duplicate_id"},
		{"unknown key is fatal", `{"id":"x","style":"s","lyrics":"l","title":"t"}` + "\n", nil, 1, "[fatal]"},
		{"bad json", "{not json\n", nil, 1, "bad_json"},
		{"missing file", "", func(string) []string { return []string{"validate-batch", "/nonexistent/x.jsonl"} }, 2, ""},
		{"no args", "", func(string) []string { return []string{"validate-batch"} }, 2, ""},
		{"flag looking arg", "", func(string) []string { return []string{"validate-batch", "--help"} }, 2, ""},
	}
	for _, tc := range tests {
		args := []string{"validate-batch"}
		var p string
		if tc.args == nil {
			p = writeJSONL(t, tc.content)
			args = append(args, p)
		} else {
			args = tc.args(p)
		}
		code, out, errs := do(t, nil, "", args...)
		if code != tc.code || !strings.Contains(out, tc.stdout) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit %d containing %q", tc.name, code, out, errs, tc.code, tc.stdout)
		}
	}
}

func TestValidateBatch_AbcPathResolvedBesideFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.abc"), []byte("X:1\nK:C\nCDEF|\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "songs.jsonl")
	if err := os.WriteFile(p, []byte(`{"id":"x","style":"s","lyrics":"l","abc_path":"a.abc"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, nil, "", "validate-batch", p); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.Remove(filepath.Join(dir, "a.abc")); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, nil, "", "validate-batch", p); code != 1 || !strings.Contains(out, "abc_path_unresolved") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// TestCLI_M2aStubCommands: until M2a-B6/B7 land, export/backup/restore fail loudly (exit 2).
func TestCLI_M2aStubCommands(t *testing.T) {
	for _, cmd := range []string{"export", "backup", "restore"} {
		code, _, errs := do(t, nil, "", cmd)
		if code != 2 || !strings.Contains(errs, "not implemented") {
			t.Errorf("%s: exit %d, stderr %q", cmd, code, errs)
		}
	}
}
