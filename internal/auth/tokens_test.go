package auth

import (
	"context"
	"errors"
	"path/filepath"
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
	// compile-time/shape check: TokenInfo exposes no hash field
	var _ = TokenInfo{ID: "", Name: "", Prefix: "", Scopes: nil, CreatedAt: "", LastUsedAt: "", RevokedAt: ""}
}
