package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestMigration0006_UpgradeFromM2c opens a schema-5 library with the M2c fixture rows, lets store.Open apply
// 0006, and checks that every earlier row is unchanged, tokens still verify and the new columns are NULL.
func TestMigration0006_UpgradeFromM2c(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 5); err != nil {
		t.Fatal(err)
	}
	v5 := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, _, err := auth.NewTokenStore(v5, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureM2c(t, v5, librarytest.FixedNow)
	if v, _ := v5.UserVersion(ctx); v != 5 {
		t.Fatalf("setup version=%d", v)
	}
	snap := func(s *store.Store) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256||lossy) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||engine||request_id||request_json||created_by) FROM (SELECT * FROM recipes ORDER BY id)`,
			`SELECT group_concat(id||request_id||request_json||status||created_at||updated_at) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(id||hmac_hash||scopes) FROM api_tokens`,
			`SELECT group_concat(id||hmac_hash||scopes||name) FROM devices`,
			`SELECT group_concat(track_id||source||stars||principal_id) FROM ratings`,
			`SELECT group_concat(id||track_id||body) FROM comments`,
			`SELECT group_concat(id||child_track_id||operator||inferred) FROM mutations`,
		} {
			var v *string
			if err := s.R.QueryRowContext(ctx, q).Scan(&v); err != nil {
				t.Fatal(err)
			}
			if v == nil {
				out = append(out, "")
			} else {
				out = append(out, *v)
			}
		}
		return out
	}
	before := snap(v5)
	for i, b := range before {
		if b == "" && i != 7 {
			t.Fatalf("snapshot %d empty: fixture too thin", i)
		}
	}
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v < 6 {
		t.Fatalf("version=%d", v)
	}
	after := snap(s)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by upgrade: %q -> %q", i, before[i], after[i])
		}
	}
	if _, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain); err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
	var nulls, toks int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(daily_budget_micro_usd IS NULL AND auto_approve_takes IS NULL AND auto_approve_micro_usd IS NULL),0) FROM api_tokens`).Scan(&toks, &nulls); err != nil || toks == 0 || nulls != toks {
		t.Fatalf("api_tokens new columns: %d tokens, %d all-NULL, err %v", toks, nulls, err)
	}
	var withJob int
	if err := s.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tracks WHERE job_id IS NOT NULL OR env_id IS NOT NULL)+(SELECT count(*) FROM mutations WHERE job_id IS NOT NULL OR recipe_patch_json IS NOT NULL)`).Scan(&withJob); err != nil || withJob != 0 {
		t.Fatalf("generation columns set on old rows: %d %v", withJob, err)
	}
	var caps int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM spend_caps`).Scan(&caps); err != nil || caps != 1 {
		t.Fatalf("spend_caps rows=%d %v", caps, err)
	}
}
