package oidc_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/oidc"
)

// TestConstants pins the frozen contract constants (C105); C1 adds the verification tables.
func TestConstants(t *testing.T) {
	t.Parallel()
	if oidc.Alg != "RS256" || oidc.RequiredScope != "metamusic" || oidc.UserClaim != "mm_user" || oidc.Leeway != 30*time.Second {
		t.Fatalf("constants changed: %q %q %q %v", oidc.Alg, oidc.RequiredScope, oidc.UserClaim, oidc.Leeway)
	}
}

// TestVerifier_StubNeverAccepts: until M4a-C1 the stub refuses every token (fail closed).
func TestVerifier_StubNeverAccepts(t *testing.T) {
	t.Parallel()
	v, err := oidc.NewVerifier(oidc.Config{Issuer: "https://auth.example.com", Resource: "https://mm.example.com/mcp"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), "a.b.c"); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatalf("Verify = %v, want ErrInvalid", err)
	}
	var ms *oidc.MissingScopeError
	if !errors.As(error(&oidc.MissingScopeError{Scope: "metamusic"}), &ms) || ms.Scope != "metamusic" {
		t.Fatal("MissingScopeError not usable with errors.As")
	}
	if s := v.Status(); s.Keys != 0 || s.FetchedAt != nil {
		t.Fatalf("status %+v", s)
	}
	v.Prefetch(t.Context())
}
