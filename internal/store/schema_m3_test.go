package store

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var createRE = regexp.MustCompile(`(?im)^CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX|TRIGGER)\s+(\w+)`)

func TestMigration0006_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 6 || ms[5].name != "0006_generate.sql" {
		t.Fatalf("migration 6 = %v", ms[len(ms)-1].name)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < 6 || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	// every table, index and trigger of the migration exists (the list is read from the DDL itself, so a
	// drive-by edit of one side cannot pass); the counts pin the contract's inventory.
	counts := map[string]int{}
	for _, m := range createRE.FindAllStringSubmatch(ms[5].sql, -1) {
		typ, name := strings.ToLower(m[1]), m[2]
		counts[typ]++
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&c); err != nil || c != 1 {
			t.Errorf("%s %s missing", typ, name)
		}
	}
	for typ, want := range map[string]int{"table": 20, "index": 23, "trigger": 52} {
		if counts[typ] != want {
			t.Errorf("%s: %d created, want %d (C69 inventory)", typ, counts[typ], want)
		}
	}
	for _, col := range [][2]string{
		{"api_tokens", "daily_budget_micro_usd"}, {"api_tokens", "auto_approve_takes"}, {"api_tokens", "auto_approve_micro_usd"},
		{"tracks", "job_id"}, {"tracks", "env_id"}, {"mutations", "job_id"}, {"mutations", "recipe_patch_json"},
	} {
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, col[0], col[1]).Scan(&c); err != nil || c != 1 {
			t.Errorf("column %s.%s missing", col[0], col[1])
		}
	}
	var monthly, batch, human, agent, takes, auto int64
	var by string
	if err := s.R.QueryRowContext(ctx, `SELECT monthly_micro_usd,batch_micro_usd,human_daily_micro_usd,agent_daily_micro_usd,auto_approve_takes,auto_approve_micro_usd,updated_by FROM spend_caps WHERE id=1`).
		Scan(&monthly, &batch, &human, &agent, &takes, &auto, &by); err != nil {
		t.Fatal(err)
	}
	if monthly != 10_000_000 || batch != 5_000_000 || human != 5_000_000 || agent != 1_000_000 || takes != 5 || auto != 500_000 || by != "system:migration" {
		t.Errorf("spend_caps defaults: %d %d %d %d %d %d %s", monthly, batch, human, agent, takes, auto, by)
	}
	for tbl, want := range map[string]int{"rtf_stats": 3, "job_transitions": 34, "spend_caps": 1} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil || n != want {
			t.Errorf("%s rows = %d, want %d", tbl, n, want)
		}
	}
	// the M2c triggers still fire on top of 0006
	insertStub(t, s, "st")
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
	expectErr(t, exec(t, s, stubInsert, "r-bad", "bad", `{"extra":1,"id":"bad","seed":0,"title":"t"}`, "h-bad", "h-bad"), "recipes: unknown-engine shape")
	var fk int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fk); err != nil || fk != 0 {
		t.Errorf("foreign_key_check rows = %d, %v", fk, err)
	}
}

// TestSchemaVersionAgnostic: no test outside the migration tests hard-codes a schema version; the two
// literals M2c left (httpserver healthz, schema_m2c_test) compare with len(migrations) or a minimum.
func TestSchemaVersionAgnostic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.UserVersion(ctx)
	if err != nil || v != len(ms) {
		t.Fatalf("user_version=%d migrations=%d err=%v", v, len(ms), err)
	}
}
