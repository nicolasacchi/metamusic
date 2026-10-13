package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestBackup_ReferenceAndLossy: reference tracks (stub recipes, one with an MP3 master) survive a
// backup and a restore: files verify, masters keep mode 0444, the lossy flag is preserved, and the
// songs.jsonl export (which leaves the stubs out) does not break the run.
func TestBackup_ReferenceAndLossy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	lt.ApplyM2c(t, e.st, lt.FixedNow)
	masters := map[string]string{}
	for _, id := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		var p string
		if err := e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(e.data, p), 0o444); err != nil {
			t.Fatal(err)
		}
		masters[id] = p
	}
	_, m, dir := e.run(t)
	have := map[string]ManifestFile{}
	for _, f := range m.Files {
		have[f.Path] = f
	}
	for id, p := range masters {
		if f, ok := have[p]; !ok || f.Kind != "master" {
			t.Errorf("%s: master %s not in the manifest: %+v", id, p, f)
		}
	}
	if _, ok := have["export/songs.jsonl"]; !ok {
		t.Error("the known tracks' export is missing")
	}

	into := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(into, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for id, p := range masters {
		var path, sha string
		var lossy int
		if err := st.R.QueryRow(`SELECT master_path, master_sha256, lossy FROM tracks WHERE id = ?`, id).Scan(&path, &sha, &lossy); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if path != p || lossy != map[bool]int{false: 0, true: 1}[id == lt.TrackLossy] {
			t.Errorf("%s: path %q lossy %d", id, path, lossy)
		}
		full := filepath.Join(into, p)
		if sum, _, err := hashFile(full); err != nil || sum != sha {
			t.Errorf("%s: sha %v", id, err)
		}
		if fi, err := os.Stat(full); err != nil || fi.Mode().Perm() != 0o444 {
			t.Errorf("%s: mode %v %v", id, fi, err)
		}
	}
}
