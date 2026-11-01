package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deleteAllowlist names the only tables that may be physically deleted from:
// the idempotency cache, M0's session cache, M4a's Subsonic salt cache and (L1, C88.3, D-L1-24) the four derived Sound caches and the reconcile
// queue, which the Reconciler rebuilds by delete-and-insert, and (L3, D-L3-3) the two feature caches take_features and
// song_features, rebuilt the same way. Nothing else, ever.
var deleteAllowlist = map[string]bool{
	"idempotency": true, "human_sessions": true,
	"subsonic_salts": true, "provenance_dirty": true, "take_sounds": true, "song_sounds": true, "sound_state": true, "song_unmapped_terms": true,
	"take_features": true, "song_features": true,
}

var deleteRE = regexp.MustCompile(`(?i)\bdelete\s+from\s+([A-Za-z_][A-Za-z0-9_]*)`)

// scanDeletes returns "path: table" for every DELETE FROM outside the allowlist
// in non-test .go files under root.
func scanDeletes(t *testing.T, root string) (offenders []string, scanned int) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		scanned++
		for _, m := range deleteRE.FindAllStringSubmatch(string(b), -1) {
			if !deleteAllowlist[strings.ToLower(m[1])] {
				offenders = append(offenders, p+": "+m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return offenders, scanned
}

func TestNoDeleteSQL(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	off, n := scanDeletes(t, filepath.Join(root, "internal"))
	if n < 20 {
		t.Fatalf("scanned only %d files", n)
	}
	if len(off) > 0 {
		t.Fatalf("physical DELETE outside the cache-table allowlist: %v", off)
	}
	// The checker itself: a planted DELETE in a temp copy must be found.
	dir := t.TempDir()
	plant := "package x\n\nvar q = `DELETE FROM " + "comments WHERE id=?`\nvar ok = `DELETE FROM human_sessions`\nvar ok2 = `delete from idempotency`\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(plant), 0o600); err != nil {
		t.Fatal(err)
	}
	off, _ = scanDeletes(t, dir)
	if len(off) != 1 || !strings.Contains(off[0], "comments") {
		t.Fatalf("planted delete not detected exactly once: %v", off)
	}
}

// TestNoDeleteSQL_AllowlistL3: L3 adds exactly the two derived feature caches to the allowlist and nothing else.
func TestNoDeleteSQL_AllowlistL3(t *testing.T) {
	t.Parallel()
	want := []string{"idempotency", "human_sessions", "subsonic_salts", "provenance_dirty", "take_sounds", "song_sounds", "sound_state",
		"song_unmapped_terms", "take_features", "song_features"}
	if len(deleteAllowlist) != len(want) {
		t.Errorf("allowlist has %d tables, want %d", len(deleteAllowlist), len(want))
	}
	for _, tb := range want {
		if !deleteAllowlist[tb] {
			t.Errorf("%s missing from the allowlist", tb)
		}
	}
	for _, tb := range []string{"record_sequences", "taxonomy_proposals", "records", "record_changes"} {
		if deleteAllowlist[tb] {
			t.Errorf("%s must never be deletable", tb)
		}
	}
}
