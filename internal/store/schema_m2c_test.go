package store

import (
	"context"
	"testing"
)

func TestMigration0005_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < 5 || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	var notnull int
	var dv *string
	rows, err := s.R.QueryContext(ctx, `SELECT "notnull", dflt_value FROM pragma_table_info('tracks') WHERE name='lossy'`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&notnull, &dv); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if n != 1 || notnull != 1 || dv == nil || *dv != "0" {
		t.Fatalf("tracks.lossy: rows=%d notnull=%d default=%v", n, notnull, dv)
	}
	want := map[string][]string{
		"index": {"tracks_lossy"},
		"trigger": {"tracks_lossy_frozen", "recipes_unknown_shape", "tracks_lossy_shape", "tracks_reference_rule",
			"tracks_reference_lineage", "renditions_lossy_no_mp3", "renditions_lossy_no_mp3_upd",
			"recipe_drafts_ref_parent_ins", "recipe_drafts_ref_parent_upd"},
	}
	for typ, names := range want {
		for _, nm := range names {
			var c int
			if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, nm).Scan(&c); err != nil || c != 1 {
				t.Errorf("%s %s missing", typ, nm)
			}
		}
	}
	if len(want["trigger"]) != 9 {
		t.Fatalf("plan names nine triggers, test lists %d", len(want["trigger"]))
	}
}
