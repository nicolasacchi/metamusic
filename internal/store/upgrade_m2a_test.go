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

func TestMigration0003_UpgradeFromM1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 2); err != nil {
		t.Fatal(err)
	}
	v2 := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, _, err := auth.NewTokenStore(v2, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureV2(t, v2, librarytest.FixedNow)
	if v, _ := v2.UserVersion(ctx); v != 2 {
		t.Fatalf("setup version=%d", v)
	}
	snap := func(s *store.Store) [4]string {
		var out [4]string
		for i, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||request_id||request_json||status||created_at||updated_at) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(id||hmac_hash||scopes) FROM api_tokens`,
			`SELECT group_concat(id||request_id||hash) FROM (SELECT * FROM recipes ORDER BY id)`,
		} {
			if err := s.R.QueryRowContext(ctx, q).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := snap(v2)
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v < 3 {
		t.Fatalf("version=%d", v)
	}
	if after := snap(s); after != before {
		t.Fatalf("M1 rows changed by upgrade: %v -> %v", before, after)
	}
	var n, withDefaults int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*), sum(revision=1 AND created_by='import' AND updated_by='import') FROM recipe_drafts`).Scan(&n, &withDefaults); err != nil {
		t.Fatal(err)
	}
	if n == 0 || n != withDefaults {
		t.Fatalf("drafts=%d with defaults=%d", n, withDefaults)
	}
	if _, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain); err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
}
