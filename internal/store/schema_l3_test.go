package store

import (
	"context"
	"strings"
	"testing"
)

// agentsAtlasMigration is the 1-based position of 0011_agents_atlas.sql: the single schema-number constant of the L3 tests.
const agentsAtlasMigration = 11

var l3Tables = []string{"take_features", "song_features", "record_sequences", "taxonomy_proposals"}

func TestMigration0011_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < agentsAtlasMigration || ms[agentsAtlasMigration-1].name != "0011_agents_atlas.sql" {
		t.Fatalf("migration %d = %v", agentsAtlasMigration, ms[len(ms)-1].name)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < agentsAtlasMigration || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	counts := map[string]int{}
	for _, m := range createL1RE.FindAllStringSubmatch(ms[agentsAtlasMigration-1].sql, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	// the C134 inventory: 4 tables, 6 indexes, 6 triggers
	for typ, want := range map[string]int{"table": 4, "index": 6, "trigger": 6} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C134 inventory)", typ, counts[typ], want)
		}
	}
	for _, tb := range l3Tables {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows %v, want empty (0011 creates no feature, sequence or proposal row)", tb, n, err)
		}
	}
	// song_sounds gained ord on a WITHOUT ROWID table: NOT NULL DEFAULT 0, CHECK >= 0
	var notnull int
	var dflt string
	if err := s.R.QueryRowContext(ctx, `SELECT "notnull", COALESCE(dflt_value,'') FROM pragma_table_info('song_sounds') WHERE name='ord'`).Scan(&notnull, &dflt); err != nil || notnull != 1 || dflt != "0" {
		t.Errorf("song_sounds.ord: notnull=%d default=%q err=%v", notnull, dflt, err)
	}
	var pk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('song_sounds') WHERE pk > 0`).Scan(&pk); err != nil || pk != 3 {
		t.Errorf("song_sounds primary key columns = %d, %v", pk, err)
	}
	// the queue holds nothing on an empty library (the re-seed is data-driven)
	var q int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM provenance_dirty`).Scan(&q); err != nil || q != 0 {
		t.Errorf("queue rows on an empty library = %d, %v", q, err)
	}
	var fk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d, %v", fk, err)
	}
	// earlier triggers still fire on top of the new migration
	insertStub(t, s, "l3")
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
}

// TestMigration0011_ChecksOnFeatureRows pins the CHECKs of the two feature caches (C134).
func TestMigration0011_ChecksOnFeatureRows(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)
	const ins = `INSERT INTO take_features(track_id,bpm_x100,bpm_basis,energy_x10000,energy_basis,key_num,key_letter,key_basis,digest,feature_def,computed_at) VALUES(?,?,?,?,?,?,?,?,?,'mm-features/1',?)`
	d := strings.Repeat("a", 64)
	mustExec(t, s, ins, "t1", 9600, "measured", 3000, "measured", 8, "A", "measured", d, l1ts)
	for _, c := range []struct {
		name string
		args []any
	}{
		{"bpm below 20", []any{"t2", 1999, "measured", nil, nil, nil, nil, nil, d, l1ts}},
		{"bpm above 300", []any{"t2", 30001, "measured", nil, nil, nil, nil, nil, d, l1ts}},
		{"bpm without basis", []any{"t2", 9600, nil, nil, nil, nil, nil, nil, d, l1ts}},
		{"basis without bpm", []any{"t2", nil, "plan", nil, nil, nil, nil, nil, d, l1ts}},
		{"energy above 1", []any{"t2", nil, nil, 10001, "measured", nil, nil, nil, d, l1ts}},
		{"energy basis invalid", []any{"t2", nil, nil, 5000, "declared", nil, nil, nil, d, l1ts}},
		{"key letter without num", []any{"t2", nil, nil, nil, nil, nil, "A", "measured", d, l1ts}},
		{"key num 13", []any{"t2", nil, nil, nil, nil, 13, "A", "measured", d, l1ts}},
		{"short digest", []any{"t2", nil, nil, nil, nil, nil, nil, nil, "abc", l1ts}},
	} {
		expectErr(t, exec(t, s, ins, c.args...), "CHECK")
		_ = c.name
	}
	expectErr(t, exec(t, s, ins, "nope", nil, nil, nil, nil, nil, nil, nil, d, l1ts), "FOREIGN KEY")
	mustExec(t, s, ins, "t2", nil, nil, nil, nil, nil, nil, nil, d, l1ts) // a row with no feature at all is legal
}
