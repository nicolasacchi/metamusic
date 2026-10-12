package auth

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

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
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key)
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
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key)
	for _, bad := range []string{"", "mmp_", "mmp_abc", "xxx_" + strings.Repeat("a", 52), "mmp_" + strings.Repeat("a", 52)} {
		if _, err := ts.Verify(ctx, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify(%q) err = %v", bad, err)
		}
	}
	// a token minted with a different key must not verify
	other := NewTokenStore(ts.st, []byte("another-key-another-key-another!"))
	plain, _, _ := other.Create(ctx, "o", []Scope{ScopeRead})
	if _, err := ts.Verify(ctx, plain); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("cross-key verify err = %v", err)
	}
}

func TestTokenInfoHasNoHash(t *testing.T) {
	typ := reflect.TypeOf(TokenInfo{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "hash") {
			t.Fatalf("TokenInfo has hash-like field %q", typ.Field(i).Name)
		}
	}
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key)
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
	ts := NewTokenStore(newStore(t), key)
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
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key)
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
	ctx := context.Background()
	ts := NewTokenStore(newStore(t), key)
	plain, id, _ := ts.Create(ctx, "n", []Scope{ScopeRead})
	if _, err := ts.Verify(ctx, plain); err != nil {
		t.Fatal(err)
	}
	// pretend it was used 10s ago; a second verify must not rewrite it
	const stamp = "2999-01-01T00:00:00Z"
	_, _ = ts.st.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, stamp, id)
	_, _ = ts.Verify(ctx, plain)
	var lu string
	_ = ts.st.R.QueryRowContext(ctx, `SELECT last_used_at FROM api_tokens WHERE id=?`, id).Scan(&lu)
	if lu != stamp {
		t.Fatalf("last_used_at rewritten within throttle window: %s", lu)
	}
}
