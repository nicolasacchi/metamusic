package store

import (
	"context"
	"strings"
	"testing"
)

// curationMigration is the 1-based position of 0010_curation.sql: the single schema-number constant of the L2 tests.
const curationMigration = 10

var l2Tables = []string{"records", "record_slots", "record_changes", "crates", "crate_items", "tapes"}

func TestMigration0010_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < curationMigration || ms[curationMigration-1].name != "0010_curation.sql" {
		t.Fatalf("migration %d = %v", curationMigration, ms[len(ms)-1].name)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < curationMigration || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	counts := map[string]int{}
	for _, m := range createL1RE.FindAllStringSubmatch(ms[curationMigration-1].sql, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	// the C119 inventory: 6 tables, 17 indexes (1 on muses), 30 triggers (2 on muses)
	for typ, want := range map[string]int{"table": 6, "index": 17, "trigger": 30} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C119 inventory)", typ, counts[typ], want)
		}
	}
	for _, tb := range l2Tables {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows %v, want empty (0010 creates no L2 row)", tb, n, err)
		}
	}
	// the migration writes no settings row
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key LIKE 'curation.%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("curation settings rows = %d %v", n, err)
	}
	for _, c := range []struct{ tbl, col string }{{"muses", "ordinal"}, {"records", "cover_seed"}, {"tapes", "attempts"}} {
		var c1 int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, c.tbl, c.col).Scan(&c1); err != nil || c1 != 1 {
			t.Errorf("column %s.%s missing", c.tbl, c.col)
		}
	}
	for idx, tbl := range map[string]string{"muses_ordinal": "muses", "record_slots_live_song": "record_slots", "record_changes_event": "record_changes",
		"crate_items_live_song": "crate_items", "crate_items_live_track": "crate_items", "tapes_live_key": "tapes"} {
		var uniq int
		if err := s.R.QueryRowContext(ctx, `SELECT "unique" FROM pragma_index_list(?) WHERE name=?`, tbl, idx).Scan(&uniq); err != nil || uniq != 1 {
			t.Errorf("index %s: unique=%d err=%v", idx, uniq, err)
		}
	}
	var fk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d, %v", fk, err)
	}
	// earlier triggers still fire on top of the new migration
	insertStub(t, s, "l2")
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
}
