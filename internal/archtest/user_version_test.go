package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoUserVersionLiteralOutsideStore: a test outside internal/store must not fake an older database by opening the latest schema,
// dropping objects and lowering the version stamp: every migration added later re-runs on objects that still exist and fails
// ("table ... already exists"). Build the older database with store.CreateBefore(ctx, path, "00NN_name.sql") (or, inside the store
// package, openAt/MigrateTo), seed it, close it, and let store.Open migrate it forward. The pattern is assembled so this file passes itself.
func TestNoUserVersionLiteralOutsideStore(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?i)pragma\s+user_` + `version\s*=\s*\d`)
	storeDir := filepath.Join(root, "internal", "store") + string(filepath.Separator)
	n := 0
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, "_test.go") || strings.HasPrefix(p, storeDir) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if loc := re.FindIndex(b); loc != nil {
			t.Errorf("%s: sets the schema version to a literal; build the older database with store.CreateBefore(ctx, path, \"00NN_name.sql\") instead (it migrates a fresh database only up to the migration before that file)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 20 {
		t.Fatalf("scanned only %d test files", n)
	}
}
