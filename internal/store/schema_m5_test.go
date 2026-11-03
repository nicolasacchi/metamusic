package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// closeLoopMigration is the position of 0013_close_loop.sql (the single schema-number constant of the M5 tests; renumber
// here if another milestone's migration merged first).
const closeLoopMigration = 13

var m5NewTables = []string{"patches", "patch_comments", "voice_notes", "callbacks", "callback_verdicts"}

// TestMigration_M5 upgrades a populated schema-12 library: every row survives the ADD COLUMNs on jobs, comments and duels,
// the foreign keys stay clean, the new tables start empty and carry their immutable and no-delete triggers.
func TestMigration_M5(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, closeLoopMigration-1)
	librarytest.FixtureL3Upgrade(t, prev, librarytest.FixedNow)
	counts := func(db interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) map[string]string {
		rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			names = append(names, n)
		}
		rows.Close()
		out := map[string]string{}
		for _, n := range names {
			out[n] = scalar(t, db, `SELECT COUNT(*) FROM "`+n+`"`)
		}
		return out
	}
	before := counts(prev.R)
	if before["tracks"] == "0" || before["comments"] == "" {
		t.Fatalf("thin fixture: %v", before)
	}
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < closeLoopMigration {
		t.Fatalf("version=%d migrations=%d", v, store.MigrationCount())
	}
	after := counts(s.R)
	for tb, n := range before {
		if after[tb] != n {
			t.Errorf("%s: %s rows before, %s after", tb, n, after[tb])
		}
	}
	for _, tb := range m5NewTables {
		if after[tb] != "0" {
			t.Errorf("%s has %s rows", tb, after[tb])
		}
		for _, tr := range []string{tb + "_immutable", tb + "_no_delete"} {
			if scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name = ?`, tr) != "1" {
				t.Errorf("trigger %s missing", tr)
			}
		}
	}
	for _, col := range [][2]string{{"jobs", "patch_id"}, {"comments", "voice_note_id"}, {"comments", "origin_callback_id"},
		{"duels", "section_label"}, {"duels", "section_source"}, {"duels", "b_from_ms"}, {"duels", "b_to_ms"}} {
		if scalar(t, s.R, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, col[0], col[1]) != "1" {
			t.Errorf("column %s.%s missing", col[0], col[1])
		}
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
	for _, tr := range []string{"jobs_patch_frozen", "comments_m5_frozen", "duels_section_window"} {
		if scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name = ?`, tr) != "1" {
			t.Errorf("trigger %s missing", tr)
		}
	}

	// behaviour: patches are immutable and undeletable, a patch row needs valid JSON, the duel window triggers bite
	track := librarytest.TrackLuna
	if _, err := s.W.ExecContext(ctx, `INSERT INTO patches(id,base_track_id,base_recipe_hash,operator,ops_json,compiled_json,scope_json,diff_json,rationale,principal_kind,principal_id,principal_name,created_at)
		VALUES('p1',?,'h','lyric_edit','[]','{}','{}','{}','why','agent','a','a','2026-01-01T00:00:00.000Z')`, track); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE patches SET title = 'x'`, `DELETE FROM patches`} {
		if _, err := s.W.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "patches:") {
			t.Errorf("%s: %v", q, err)
		}
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO patches(id,base_track_id,base_recipe_hash,operator,ops_json,compiled_json,scope_json,diff_json,rationale,principal_kind,principal_id,principal_name,created_at)
		VALUES('p2',?,'h','lyric_edit','{}','{}','{}','{}','why','agent','a','a','2026-01-01T00:00:00.000Z')`, track); err == nil {
		t.Error("ops_json that is not an array was accepted")
	}
	other := librarytest.TrackCitta
	ins := func(id, cols, vals string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at`+cols+
			`) VALUES(?,?,?,0,0,0,30000,'mm-clip/1','t','off',0,0,'human:x','human','2026-01-01T00:00:00.000Z','2026-02-01T00:00:00.000Z'`+vals+`)`, id, track, other)
		return err
	}
	if err := ins("dA", ",section_label,section_source,b_from_ms,b_to_ms", ",'chorus','plan',1000,31000"); err != nil {
		t.Errorf("a consistent section duel: %v", err)
	}
	for name, a := range map[string][2]string{
		"only b_from":     {",b_from_ms", ",1000"},
		"window no label": {",b_from_ms,b_to_ms", ",1000,31000"},
		"label no window": {",section_label,section_source", ",'chorus','plan'"},
		"unequal lengths": {",section_label,section_source,b_from_ms,b_to_ms", ",'chorus','plan',1000,20000"},
		"label no source": {",section_label,b_from_ms,b_to_ms", ",'chorus',1000,31000"},
	} {
		if err := ins("dB"+name, a[0], a[1]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := ins("dC", "", ""); err != nil {
		t.Errorf("a plain duel: %v", err)
	}
}
