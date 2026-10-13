package librarytest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestFixture_RowCounts(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fx := FixtureV2(t, st, FixedNow)
	want := map[string]int{"tracks": 5, "recipe_drafts": 6, "recipes": 11, "import_runs": 1, "device_profiles": 1, "draft_collections": 3,
		"scores": 2, "mutations": 1, "renditions": 10, "analyses": 10, "recipe_sources": 11}
	for table, n := range want {
		var got int
		if err := st.R.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil || got != n {
			t.Errorf("%s: %d rows, want %d (%v)", table, got, n, err)
		}
	}
	if want := []string{Draft1, Draft2, Draft3, Draft4, Draft5, Draft6}; !slices.Equal(fx.Drafts, want) {
		t.Errorf("TestFixture_FixedIds: drafts %v, want %v", fx.Drafts, want)
	}
	if len(fx.Tracks) != 5 || fx.Tracks[0] != TrackLuna {
		t.Errorf("fixtures %+v", fx)
	}
}

// TestWriteFixtureDB is the entry point of `make fixture-db`: with
// MM_FIXTURE_DB=<path> it writes the fixture database (and its media files)
// there; otherwise it only checks that the writer works in a temp dir.
func TestWriteFixtureDB(t *testing.T) {
	path := os.Getenv("MM_FIXTURE_DB")
	if path == "" {
		path = filepath.Join(t.TempDir(), "fixture", "metamusic.db")
	}
	WriteFixtureDB(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// TestLibrarytest_FixtureExtendsV2: Fixture is FixtureV2 plus the C42 M2a rows.
func TestLibrarytest_FixtureExtendsV2(t *testing.T) {
	count := func(st *store.Store, table string) int {
		var n int
		if err := st.R.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	open := func() *store.Store {
		st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	v2, full := open(), open()
	fx2 := FixtureV2(t, v2, FixedNow)
	fx := Fixture(t, full, FixedNow)
	if !slices.Equal(fx.Tracks, fx2.Tracks) || !slices.Equal(fx.Drafts[:len(fx2.Drafts)], fx2.Drafts) {
		t.Fatalf("Fixture must start with the V2 ids: %+v vs %+v", fx, fx2)
	}
	// every V2 table keeps its rows except the ones the M2a drafts add
	for _, table := range []string{"tracks", "device_profiles", "import_runs", "scores", "mutations", "renditions", "analyses", "artifacts", "draft_collections"} {
		if a, b := count(v2, table), count(full, table); a != b {
			t.Errorf("%s: V2 %d rows, Fixture %d", table, a, b)
		}
	}
	for table, want := range map[string]int{
		"ratings": 4, "tags": 3, "track_tags": 4, "comments": 5, "playlists": 1, "listen_events": 3, "devices": 1, "backup_runs": 1,
		"recipe_drafts": 9, "recipes": 12,
	} {
		if got := count(full, table); got != want {
			t.Errorf("Fixture %s: %d rows, want %d", table, got, want)
		}
	}
	for _, id := range []string{DraftVarianti, DraftUpload1, DraftUpload2} {
		if !slices.Contains(fx.Drafts, id) {
			t.Errorf("draft %s missing from the fixture ids", id)
		}
	}
	var origins []string
	rows, err := full.R.Query(`SELECT origin FROM recipe_drafts WHERE id IN (?,?,?) ORDER BY id`, DraftVarianti, DraftUpload1, DraftUpload2)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var o string
		_ = rows.Scan(&o)
		origins = append(origins, o)
	}
	_ = rows.Close()
	if !slices.Equal(origins, []string{"varianti_import", "jsonl_upload", "jsonl_upload"}) {
		t.Errorf("origins %v", origins)
	}
}

// TestWriteFiles_ArtifactFile: every artifacts row has its file, with a matching sha256.
func TestWriteFiles_ArtifactFile(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	Fixture(t, st, FixedNow)
	WriteFiles(t, dir)
	rows, err := st.R.Query(`SELECT path, sha256 FROM artifacts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var p, sum string
		_ = rows.Scan(&p, &sum)
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if Sha(string(b)) != sum {
			t.Errorf("%s: sha mismatch", p)
		}
		n++
	}
	if n == 0 || !strings.HasPrefix(func() string {
		var p string
		_ = st.R.QueryRow(`SELECT path FROM artifacts WHERE track_id = ?`, TrackLuna).Scan(&p)
		return p
	}(), "takes/"+TrackLuna+"/") {
		t.Errorf("artifact rows: %d", n)
	}
}
