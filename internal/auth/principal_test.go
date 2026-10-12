package auth

import (
	"context"
	"testing"
)

func TestPrincipalHasAndContext(t *testing.T) {
	if got := FromContext(context.Background()); got.Kind != KindAnonymous || got.Has(ScopeRead) {
		t.Fatalf("absent principal = %+v, want anonymous without scopes", got)
	}
	p := Principal{Kind: KindAgent, ID: "x", Scopes: []Scope{ScopeRead}}
	got := FromContext(WithPrincipal(context.Background(), p))
	if got.ID != "x" || !got.Has(ScopeRead) || got.Has(ScopeWrite) {
		t.Fatalf("round trip = %+v", got)
	}
}
