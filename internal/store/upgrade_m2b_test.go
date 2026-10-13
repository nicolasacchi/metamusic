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

func TestMigration0004_UpgradeFromM2a(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 3); err != nil {
		t.Fatal(err)
	}
	v3 := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, _, err := auth.NewTokenStore(v3, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	librarytest.Fixture(t, v3, librarytest.FixedNow)
	if v, _ := v3.UserVersion(ctx); v != 3 {
		t.Fatalf("setup version=%d", v)
	}
	snap := func(s *store.Store) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||request_id||request_json||status||created_at||updated_at) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(id||hmac_hash||scopes) FROM api_tokens`,
			`SELECT group_concat(id||hmac_hash||scopes||name) FROM devices`,
			`SELECT group_concat(track_id||source||stars||principal_id) FROM ratings`,
			`SELECT group_concat(id||track_id||body) FROM comments`,
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
	before := snap(v3)
	var devices int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM devices`).Scan(&devices); err != nil || devices == 0 {
		t.Fatalf("fixture device missing: %d %v", devices, err)
	}
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != 4 {
		t.Fatalf("version=%d", v)
	}
	after := snap(s)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by upgrade: %q -> %q", i, before[i], after[i])
		}
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM duels`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("duels=%d err=%v", n, err)
	}
	if _, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain); err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
}
