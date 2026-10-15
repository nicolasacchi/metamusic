package importer

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestOrphans_IgnoreCoverAndDownloadCaches: derived/covers and derived/dl are caches, never orphans (C16.7).
func TestOrphans_IgnoreCoverAndDownloadCaches(t *testing.T) {
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
	key := strings.Repeat("cd", 32)
	mk("derived/covers/cd/" + key + ".png")  // cover cache: never reported
	mk("derived/dl/cd/" + key + ".flac")     // tagged download cache: never reported
	mk("derived/ab/" + key + ".spectro.jpg") // unreferenced spectrogram: reported
	mk("audio/masters/stray.flac")
	_, inv := e.inventory()
	got := strings.Join(inv.Report.LibraryOrphans, ",")
	want := "audio/masters/stray.flac,derived/ab/" + key + ".spectro.jpg"
	if got != want || inv.Report.Summary.LibraryOrphans != 2 {
		t.Errorf("orphans = %s", got)
	}
}
