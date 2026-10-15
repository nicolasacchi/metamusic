package store

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var createL1RE = regexp.MustCompile(`(?im)^CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX|TRIGGER|VIEW|VIRTUAL TABLE)\s+(\w+)`)

func TestMigration0008_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 8 || ms[7].name != "0008_provenance.sql" {
		t.Fatalf("migration 7 = %v", ms[len(ms)-1].name)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < 8 || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	counts := map[string]int{}
	for _, m := range createL1RE.FindAllStringSubmatch(ms[7].sql, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		if typ == "virtual table" {
			typ = "table"
		}
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	for typ, want := range map[string]int{"table": 17, "view": 1, "index": 16, "trigger": 59} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C88 inventory)", typ, counts[typ], want)
		}
	}
	for _, tb := range []string{"songs", "pick_events", "song_picks", "sound_taxonomy_versions"} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows %v, want empty (the seed is applied by the app, not the migration)", tb, n, err)
		}
	}
	var lease int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM provenance_lease WHERE id = 1 AND owner = '' AND epoch = 1`).Scan(&lease); err != nil || lease != 1 {
		t.Errorf("provenance_lease must hold one free row: %d %v", lease, err)
	}
	rows, err := s.R.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Errorf("foreign_key_check is not empty")
	}
}
