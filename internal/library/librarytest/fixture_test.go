package librarytest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestFixture_RowCounts(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fx := Fixture(t, st, FixedNow)
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
