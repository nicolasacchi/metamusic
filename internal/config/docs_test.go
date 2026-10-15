package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestDocs_ConfigTableListsEveryEnv pins that README's configuration table has a row for every
// MM_* variable that config.Load reads.
func TestDocs_ConfigTableListsEveryEnv(t *testing.T) {
	t.Parallel()
	srcs, err := filepath.Glob("*.go")
	if err != nil || len(srcs) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(srcs))
	}
	getRE := regexp.MustCompile(`getenv\("(MM_[A-Z0-9_]+)"\)`)
	vars := map[string]bool{}
	for _, f := range srcs {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range getRE.FindAllStringSubmatch(string(b), -1) {
			vars[m[1]] = true
		}
	}
	if len(vars) < 20 {
		t.Fatalf("only %d variables found; the extraction is broken", len(vars))
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for v := range vars {
		if !strings.Contains(string(readme), "| `"+v+"` |") {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("README configuration table lacks rows for: %s", strings.Join(missing, ", "))
	}
}
