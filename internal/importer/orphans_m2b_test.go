package importer

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOrphans_IgnoreClipCache(t *testing.T) {
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
	sha := strings.Repeat("ab", 32)
	mk("derived/clips/aa/" + sha + ".flac")  // clip cache: never reported
	mk("derived/exports/aa/" + sha + ".zip") // export cache: never reported
	mk("derived/ab/" + sha + ".spectro.jpg") // unreferenced spectrogram: reported
	mk("audio/masters/stray.flac")
	_, inv := e.inventory()
	got := strings.Join(inv.Report.LibraryOrphans, ",")
	want := "audio/masters/stray.flac,derived/ab/" + sha + ".spectro.jpg"
	if got != want || inv.Report.Summary.LibraryOrphans != 2 {
		t.Errorf("orphans = %s", got)
	}
}
