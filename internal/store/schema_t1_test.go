package store

import (
	"context"
	"strings"
	"testing"
)

// transformsMigration is the file name of the T1 migration; its number is whatever was next free when it was written, so no
// test hard-codes the position: schema assertions compare with len(migrations).
const transformsMigration = "_transforms.sql"

func transformsMigrationSQL(t *testing.T) string {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if strings.HasSuffix(m.name, transformsMigration) {
			return m.sql
		}
	}
	t.Fatalf("no migration ends in %s", transformsMigration)
	return ""
}

// TestSchema_T1: every object the T1 migration creates exists, the inventory is the one C201 lists, the kind CHECK holds the
// nine kinds (C225.1), and the migration leaves no foreign-key violation and no row behind.
func TestSchema_T1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.UserVersion(ctx); err != nil || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	sqlText := transformsMigrationSQL(t)
	counts := map[string]int{}
	for _, m := range createL1RE.FindAllStringSubmatch(sqlText, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	// the C201 inventory: 2 tables, 6 indexes, 13 triggers
	for typ, want := range map[string]int{"table": 2, "index": 6, "trigger": 13} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C201 inventory)", typ, counts[typ], want)
		}
	}
	for _, col := range [][2]string{{"tracks", "transform_id"}, {"mutations", "transform_id"}} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s.%s missing", col[0], col[1])
		}
	}
	for _, kind := range []string{"morph", "shepard", "glacier", "polytempo", "ensemble", "score_render", "master", "widen", "binaural"} {
		if !strings.Contains(sqlText, "'"+kind+"'") {
			t.Errorf("kind %s is not in the CHECK list", kind)
		}
	}
	for _, tb := range []string{"transforms", "transform_inputs"} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows %v, want empty", tb, n, err)
		}
	}
	var fk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d, %v", fk, err)
	}
	// earlier triggers still fire on top of the new migration
	insertStub(t, s, "t1")
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
}
