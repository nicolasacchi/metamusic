package nightshift

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestNightPrincipal_Shape(t *testing.T) {
	t.Parallel()
	p := Principal()
	if p.Kind != auth.KindAgent || p.ID != "nightshift" || p.Via != "nightshift" || p.Role != "" || p.User != "" {
		t.Errorf("principal %+v", p)
	}
	if p.Key() != "agent:nightshift" || p.Key() != HolderKey {
		t.Errorf("key %q", p.Key())
	}
	if !p.Has(auth.ScopeRead) || !p.Has(auth.ScopeGenerate) || p.Has(auth.ScopeWrite) || p.Has(auth.ScopeAdmin) {
		t.Errorf("scopes %v", p.Scopes)
	}
	if !p.IsAgent() || p.IsPerson() || p.IsOwner() || !p.IsOwnerSide() {
		t.Error("kind predicates")
	}
	// the scope slice is fresh on every call
	p.Scopes[0] = auth.ScopeAdmin
	if Principal().Scopes[0] != auth.ScopeRead {
		t.Error("Principal shares its scope slice")
	}

	// no api_tokens row: the global limits apply (all-nil TokenLimits)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp := spend.New(spend.Options{Store: st, Now: time.Now, ConfirmKey: []byte("k"), Location: time.UTC})
	lim, err := sp.TokenLimitsFor(context.Background(), st.R, Principal().Key())
	if err != nil {
		t.Fatal(err)
	}
	if lim.DailyMicro != nil || lim.AutoApproveMicro != nil || lim.AutoApproveTakes != nil {
		t.Errorf("limits %+v, want the global defaults", lim)
	}
}
