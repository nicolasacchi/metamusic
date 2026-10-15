package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestMigration0008_SeedsQueue upgrades a schema-7 library (the M3 fixture) and checks that the migration seeded one
// queue row per source row: runs, distinct collection names, batches, drafts, tracks (D-L1-4).
func TestMigration0008_SeedsQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 7); err != nil {
		t.Fatal(err)
	}
	v7 := &store.Store{W: raw, R: raw}
	librarytest.FixtureM3(t, v7, librarytest.FixedNow)
	src := map[string]string{
		"run": `SELECT COUNT(*) FROM import_runs`, "collection": `SELECT COUNT(DISTINCT name) FROM draft_collections`,
		"batch": `SELECT COUNT(*) FROM batches`, "draft": `SELECT COUNT(*) FROM recipe_drafts`, "track": `SELECT COUNT(*) FROM tracks`,
	}
	want := map[string]int{}
	for k, q := range src {
		if err := raw.QueryRowContext(ctx, q).Scan(new(int)); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = raw.QueryRowContext(ctx, q).Scan(&n)
		want[k] = n
		if n == 0 {
			t.Fatalf("fixture has no %s rows; the test would be vacuous", k)
		}
	}
	raw.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v < 8 {
		t.Fatalf("version %d", v)
	}
	for kind, n := range want {
		var got int
		if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provenance_dirty WHERE kind = ?`, kind).Scan(&got); err != nil || got != n {
			t.Errorf("queue rows of kind %s = %d (%v), want %d", kind, got, err, n)
		}
	}
	var songs int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM songs`).Scan(&songs); err != nil || songs != 0 {
		t.Errorf("the migration must create no Song: %d", songs)
	}
}
