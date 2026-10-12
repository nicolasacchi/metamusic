package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoReplaceSQL forbids the REPLACE forms (they bypass no-delete triggers
// and reset created_at). The patterns are assembled so this file passes itself.
func TestNoReplaceSQL(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?i)\b(insert\s+or\s+` + "repl" + `ace|` + "repl" + `ace\s+into)\b`)
	n := 0
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".sql")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if loc := re.FindIndex(b); loc != nil {
			t.Errorf("%s: forbidden REPLACE statement at byte %d", p, loc[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("scanned only %d files", n)
	}
}
