package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMigration0002_UpgradeFromM0(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 1); err != nil {
		t.Fatal(err)
	}
	v1 := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, _, err := auth.NewTokenStore(v1, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewLocalAuth(v1, key, "https://mm.example.com").SetPassword(ctx, "operator", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('k','v')`); err != nil {
		t.Fatal(err)
	}
	snap := func(s *store.Store) [3]string {
		var out [3]string
		for i, q := range []string{
			`SELECT group_concat(key||'='||value) FROM settings`,
			`SELECT group_concat(username||password_hash||updated_at) FROM human_account`,
			`SELECT group_concat(id||hmac_hash||scopes||created_at) FROM api_tokens`,
		} {
			if err := s.R.QueryRowContext(ctx, q).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := snap(v1)
	if v, _ := v1.UserVersion(ctx); v != 1 {
		t.Fatalf("setup version=%d", v)
	}
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v < 2 {
		t.Fatalf("version=%d", v)
	}
	if after := snap(s); after != before {
		t.Fatalf("rows changed by upgrade: %v -> %v", before, after)
	}
	pr, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain)
	if err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
	_ = pr
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tracks table: n=%d err=%v", n, err)
	}
}
