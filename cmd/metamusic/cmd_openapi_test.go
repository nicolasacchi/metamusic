package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// openapi needs no config, database or DataDir: with an empty environment in
// an empty directory it prints the committed document and creates nothing.
func TestCLI_OpenAPIWithoutDatabase(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	code, out, errs := do(t, map[string]string{}, "", "openapi")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if d := cmp.Diff(string(want), out); d != "" {
		t.Fatalf("openapi output differs from the committed openapi.json:\n%s", d)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("openapi created files: %v %v", entries, err)
	}
}
