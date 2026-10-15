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

func TestMissingScopeError_As(t *testing.T) {
	t.Parallel()
	var ms *oidc.MissingScopeError
	if !errors.As(error(&oidc.MissingScopeError{Scope: "metamusic"}), &ms) || ms.Scope != "metamusic" {
		t.Fatal("MissingScopeError not usable with errors.As")
	}
}
