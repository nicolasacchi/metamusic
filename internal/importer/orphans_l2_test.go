package importer

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestOrphans_SkipTapes: derived/tapes holds rebuildable tape files (C128.7): never reported, while the other caches stay
// skipped and a stray derived file is still an orphan.
func TestOrphans_SkipTapes(t *testing.T) {
	e := newEnv(t)
	simpleInbox(e)
	e.applyAll()
	mk := func(rel string) {
		t.Helper()
		if err := e.data.MkdirAll(filepath.ToSlash(filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := e.data.WriteFile(rel, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	key := strings.Repeat("ef", 32)
	mk("derived/tapes/ef/" + key + ".flac")  // tape cache: never reported
	mk("derived/covers/ef/" + key + ".png")  // still skipped
	mk("derived/dl/ef/" + key + ".flac")     // still skipped
	mk("derived/ab/" + key + ".spectro.jpg") // unreferenced spectrogram: reported
	_, inv := e.inventory()
	got := strings.Join(inv.Report.LibraryOrphans, ",")
	want := "derived/ab/" + key + ".spectro.jpg"
	if got != want || inv.Report.Summary.LibraryOrphans != 1 {
		t.Errorf("orphans = %s, want %s", got, want)
	}
}
