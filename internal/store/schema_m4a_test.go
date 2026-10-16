package store

import (
	"context"
	"strings"
	"testing"
)

// remoteMigration is the 1-based position of 0009_remote.sql (renumbered from 0008 when L1's 0008_provenance.sql merged).
// It is the single schema-number constant of the M4a tests.
const remoteMigration = 9

func TestMigration0009_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < remoteMigration || !strings.HasSuffix(ms[remoteMigration-1].name, "_remote.sql") {
		t.Fatalf("migration %d = %v", remoteMigration, ms[len(ms)-1].name)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < remoteMigration || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	counts := map[string]int{}
	for _, m := range createRE.FindAllStringSubmatch(ms[remoteMigration-1].sql, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	for typ, want := range map[string]int{"table": 5, "index": 3, "trigger": 8} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C103 inventory)", typ, counts[typ], want)
		}
	}
	for _, idx := range []string{"subsonic_clients_username_live", "playlists_role"} {
		var uniq int
		if err := s.R.QueryRowContext(ctx, `SELECT "unique" FROM pragma_index_list(?) WHERE name=?`,
			map[string]string{"subsonic_clients_username_live": "subsonic_clients", "playlists_role": "playlists"}[idx], idx).Scan(&uniq); err != nil || uniq != 1 {
			t.Errorf("index %s: unique=%d err=%v", idx, uniq, err)
		}
	}
	for _, col := range [][2]string{{"tracks", "status_set_at"}, {"playlists", "role"}} {
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, col[0], col[1]).Scan(&c); err != nil || c != 1 {
			t.Errorf("column %s.%s missing", col[0], col[1])
		}
	}
	var name, role, by string
	var rev, items int
	if err := s.R.QueryRowContext(ctx, `SELECT name, role, revision, items_count, created_by FROM playlists WHERE id='00000000-0000-7000-8000-0000000000f1'`).
		Scan(&name, &role, &rev, &items, &by); err != nil {
		t.Fatal(err)
	}
	if name != "Offline set" || role != "offline" || rev != 1 || items != 0 || by != "system:migration" {
		t.Errorf("offline row: %q %q %d %d %q", name, role, rev, items, by)
	}
	var fk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d, %v", fk, err)
	}
	// earlier triggers still fire on top of the new migration
	insertStub(t, s, "m4")
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
}
