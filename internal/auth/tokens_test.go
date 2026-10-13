package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var key = []byte("0123456789abcdef0123456789abcdef")

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	plain, id, err := ts.Create(ctx, "agent1", []Scope{ScopeRead, ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^mmp_[a-z2-7]{52}$`).MatchString(plain) {
		t.Fatalf("bad token format %q", plain)
	}
	p, err := ts.Verify(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindAgent || p.ID != id || p.Name != "agent1" || p.Via != "token" || !p.Has(ScopeWrite) || p.Has(ScopeAdmin) {
		t.Fatalf("principal = %+v", p)
	}
	list, err := ts.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if list[0].Prefix != plain[4:12] || list[0].LastUsedAt == "" || list[0].RevokedAt != "" {
		t.Fatalf("info = %+v", list[0])
	}
	if err := ts.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Verify(ctx, plain); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked verify err = %v", err)
	}
	if err := ts.Revoke(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown err = %v", err)
	}
}

func TestTokenVerifyRejects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	for _, bad := range []string{"", "mmp_", "mmp_abc", "xxx_" + strings.Repeat("a", 52), "mmp_" + strings.Repeat("a", 52)} {
		if _, err := ts.Verify(ctx, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify(%q) err = %v", bad, err)
		}
	}
	// a token minted with a different key must not verify
	other := NewTokenStore(ts.st, []byte("another-key-another-key-another!"), time.Now)
	plain, _, _ := other.Create(ctx, "o", []Scope{ScopeRead})
	if _, err := ts.Verify(ctx, plain); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-key verify err = %v", err)
	}
}

func TestTokenInfoHasNoHash(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(TokenInfo{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "hash") {
			t.Fatalf("TokenInfo has hash-like field %q", typ.Field(i).Name)
		}
	}
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	plain, _, err := ts.Create(ctx, "a", []Scope{ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := ts.st.R.QueryRowContext(ctx, `SELECT hmac_hash FROM api_tokens`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	list, _ := ts.List(ctx)
	v := reflect.ValueOf(list[0])
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() != reflect.String {
			continue
		}
		s := v.Field(i).String()
		if s == hash || (s != "" && strings.Contains(plain, s) && len(s) > 8) {
			t.Fatalf("field %s leaks secret material: %q", typ.Field(i).Name, s)
		}
	}
}

func TestTokenCreateValidation(t *testing.T) {
	t.Parallel()
	ts := NewTokenStore(newStore(t), key, time.Now)
	tests := []struct {
		name   string
		tname  string
		scopes []Scope
	}{
		{"empty name", "", []Scope{ScopeRead}},
		{"blank name", "  ", []Scope{ScopeRead}},
		{"no scopes", "n", nil},
		{"unknown scope", "n", []Scope{"root"}},
		{"comma scope", "n", []Scope{"read,admin"}},
		{"duplicate", "n", []Scope{ScopeRead, ScopeRead}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ts.Create(context.Background(), tc.tname, tc.scopes); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestTokenIDIsUUIDv7AndOrdered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	var ids []string
	for i := 0; i < 5; i++ {
		_, id, err := ts.Create(ctx, "n", []Scope{ScopeRead})
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 36 || id[14] != '7' {
			t.Fatalf("id %q is not a uuid v7", id)
		}
		ids = append(ids, id)
	}
	list, _ := ts.List(ctx)
	for i := range ids {
		if list[i].ID != ids[i] {
			t.Fatalf("list not in creation order at %d", i)
		}
	}
}

func TestTokenLastUsedThrottled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	plain, id, _ := ts.Create(ctx, "n", []Scope{ScopeRead})
	get := func() string {
		var lu string
		_ = ts.st.R.QueryRowContext(ctx, `SELECT COALESCE(last_used_at,'') FROM api_tokens WHERE id=?`, id).Scan(&lu)
		return lu
	}
	set := func(d time.Duration) string {
		v := time.Now().UTC().Add(-d).Format(time.RFC3339)
		_, _ = ts.st.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, v, id)
		return v
	}
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if get() == "" {
		t.Fatal("first use not recorded")
	}
	recent := set(10 * time.Second)
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if lu := get(); lu != recent {
		t.Fatalf("last_used_at rewritten within throttle window: %s", lu)
	}
	stale := set(120 * time.Second)
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if lu := get(); lu == stale {
		t.Fatal("stale last_used_at not refreshed")
	}
}

func TestTokenVerifyDoesNotWaitForWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	plain, id, _ := ts.Create(ctx, "n", []Scope{ScopeRead})
	recent := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
	_, _ = ts.st.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, recent, id)
	tx, err := ts.st.W.BeginTx(ctx, nil) // holds the only writer connection
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := time.Now()
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Verify blocked %v on the writer inside the throttle window", d)
	}
}

func TestTokenStore_InjectedClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	ts := NewTokenStore(newStore(t), key, func() time.Time { return now })
	plain, id, err := ts.Create(ctx, "clocked", []Scope{ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	info, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if info.CreatedAt != "2026-03-04T05:06:07Z" || info.LastUsedAt != "2026-03-04T05:06:07Z" {
		t.Fatalf("times = %q / %q", info.CreatedAt, info.LastUsedAt)
	}
	now = now.Add(2 * time.Minute)
	if err := ts.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if info, _ = ts.Get(ctx, id); info.RevokedAt != "2026-03-04T05:08:07Z" {
		t.Fatalf("revoked_at = %q", info.RevokedAt)
	}
}

func TestTokenStore_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key, time.Now)
	plain, id, err := ts.Create(ctx, "getme", []Scope{ScopeRead, ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != id || info.Name != "getme" || info.Prefix != plain[4:12] || len(info.Scopes) != 2 || info.RevokedAt != "" {
		t.Fatalf("info = %+v", info)
	}
	if strings.Contains(fmt.Sprintf("%+v", info), plain) {
		t.Fatal("TokenInfo carries the plaintext")
	}
	if _, err := ts.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown err = %v", err)
	}
}
