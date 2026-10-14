package auth

import (
	"context"
	"testing"
)

func TestPrincipalHasAndContext(t *testing.T) {
	t.Parallel()
	if got := FromContext(context.Background()); got.Kind != KindAnonymous || got.Has(ScopeRead) {
		t.Fatalf("absent principal = %+v, want anonymous without scopes", got)
	}
	p := Principal{Kind: KindAgent, ID: "x", Scopes: []Scope{ScopeRead}}
	got := FromContext(WithPrincipal(context.Background(), p))
	if got.ID != "x" || !got.Has(ScopeRead) || got.Has(ScopeWrite) {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestPrincipal_KeyAndActsAsHuman(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		p     Principal
		key   string
		human bool
	}{
		{"human", Principal{Kind: KindHuman, ID: "alice"}, "human:alice", true},
		{"device", Principal{Kind: KindDevice, ID: "d1"}, "device:d1", true},
		{"agent", Principal{Kind: KindAgent, ID: "t1"}, "agent:t1", false},
		{"anonymous", Principal{Kind: KindAnonymous}, "", false},
		{"zero", Principal{}, "", false},
		{"cli", CLI, "system:cli", false},
	} {
		if got := tc.p.Key(); got != tc.key {
			t.Errorf("%s: Key() = %q, want %q", tc.name, got, tc.key)
		}
		if got := tc.p.ActsAsHuman(); got != tc.human {
			t.Errorf("%s: ActsAsHuman() = %v, want %v", tc.name, got, tc.human)
		}
	}
	if CLI.Via != "cli" || CLI.Kind != KindSystem {
		t.Fatalf("CLI = %+v", CLI)
	}
}

func TestSystemPrincipal(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"runner", "reconciler", "job", "billing"} {
		p := System(name)
		if p.Kind != KindSystem || p.ID != name || p.Name != name || p.Via != "system" || len(p.Scopes) != 0 {
			t.Errorf("System(%q) = %+v", name, p)
		}
		if p.Key() != "system:"+name {
			t.Errorf("Key = %q", p.Key())
		}
		if p.ActsAsHuman() {
			t.Errorf("System(%q) acts as human", name)
		}
		for _, sc := range allScopes {
			if p.Has(sc) {
				t.Errorf("System(%q) has scope %s", name, sc)
			}
		}
	}
	if CLI.Key() != "system:cli" {
		t.Errorf("CLI key = %q", CLI.Key())
	}
}
