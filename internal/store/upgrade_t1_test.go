package store_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestUpgrade_T1: a database at an earlier schema (the L3 upgrade fixture at schema 10, the empty schemas 5 and 12) migrates
// to head in one boot: no earlier row changes, the transform tables are empty, no foreign key breaks, the new tables work.
func TestUpgrade_T1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, from := range []int{5, 10, 12} {
		prev, p := openAt(t, from)
		var before []string
		if from == 10 {
			librarytest.FixtureL3Upgrade(t, prev, librarytest.FixedNow)
		}
		if from >= 10 {
			for _, q := range []string{
				`SELECT COALESCE(group_concat(id||recipe_id||title||status||master_sha256||created_at),'') FROM (SELECT * FROM tracks ORDER BY id)`,
				`SELECT COALESCE(group_concat(id||engine||request_id||request_json||created_by),'') FROM (SELECT * FROM recipes ORDER BY id)`,
				`SELECT COALESCE(group_concat(id||parent_track_id||child_track_id||operator),'') FROM (SELECT * FROM mutations ORDER BY id)`,
			} {
				before = append(before, scalar(t, prev.R, q))
			}
		}
		prev.W.Close()
		s, err := store.Open(ctx, p)
		if err != nil {
			t.Fatalf("from %d: %v", from, err)
		}
		if v, _ := s.UserVersion(ctx); v != store.MigrationCount() {
			t.Fatalf("from %d: version=%d, want %d", from, v, store.MigrationCount())
		}
		if from >= 10 {
			for i, q := range []string{
				`SELECT COALESCE(group_concat(id||recipe_id||title||status||master_sha256||created_at),'') FROM (SELECT * FROM tracks ORDER BY id)`,
				`SELECT COALESCE(group_concat(id||engine||request_id||request_json||created_by),'') FROM (SELECT * FROM recipes ORDER BY id)`,
				`SELECT COALESCE(group_concat(id||parent_track_id||child_track_id||operator),'') FROM (SELECT * FROM mutations ORDER BY id)`,
			} {
				if got := scalar(t, s.R, q); got != before[i] {
					t.Errorf("from %d: row set %d changed by the upgrade", from, i)
				}
			}
		}
		for _, tb := range []string{"transforms", "transform_inputs"} {
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM `+tb); n != "0" {
				t.Errorf("from %d: %s has %s rows", from, tb, n)
			}
		}
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM tracks WHERE transform_id IS NOT NULL`); n != "0" {
			t.Errorf("from %d: %s tracks carry a transform id", from, n)
		}
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
			t.Errorf("from %d: foreign_key_check rows = %s", from, n)
		}
		s.Close()
	}
}
