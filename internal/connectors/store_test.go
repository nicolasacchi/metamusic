package connectors_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var fixedNow = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*connectors.Store, *store.Store, *atomic.Int64) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var calls atomic.Int64
	cs := connectors.NewStore(st, func() time.Time { return fixedNow }, func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", calls.Add(1))
	})
	return cs, st, &calls
}

// TestConnectorsStore_Ensure: first call creates (read,write, updated_by system:oauth); the second returns the
// same id with created=false and never calls newID; concurrent first calls yield one row.
func TestConnectorsStore_Ensure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, st, newIDCalls := newStore(t)

	g, created, err := cs.Ensure(ctx, "test-claude", "subject-1", "e2e", "Claude Test")
	if err != nil || !created {
		t.Fatalf("first Ensure: created=%v err=%v", created, err)
	}
	if g.ClientID != "test-claude" || g.Subject != "subject-1" || g.Username != "e2e" || g.Label != "Claude Test" {
		t.Errorf("grant fields: %+v", g)
	}
	if len(g.Scopes) != 2 || g.Scopes[0] != auth.ScopeRead || g.Scopes[1] != auth.ScopeWrite {
		t.Errorf("scopes = %v, want read,write", g.Scopes)
	}
	if g.UpdatedBy != "system:oauth" || g.Limits != (connectors.Limits{}) || g.RevokedAt != nil || g.LastUsedAt != nil {
		t.Errorf("defaults: %+v", g)
	}
	if !g.CreatedAt.Equal(fixedNow) || !g.UpdatedAt.Equal(fixedNow) {
		t.Errorf("timestamps: %v %v", g.CreatedAt, g.UpdatedAt)
	}
	var stored string
	if err := st.R.QueryRow(`SELECT created_at FROM connector_grants WHERE id = ?`, g.ID).Scan(&stored); err != nil || stored != store.FormatTime(fixedNow) {
		t.Errorf("created_at stored %q (%v), want the store.FormatTime form", stored, err)
	}
	if newIDCalls.Load() != 1 {
		t.Fatalf("newID called %d times after the first Ensure", newIDCalls.Load())
	}

	g2, created2, err := cs.Ensure(ctx, "test-claude", "subject-1", "ignored", "Ignored")
	if err != nil || created2 || g2.ID != g.ID || g2.Label != "Claude Test" {
		t.Fatalf("second Ensure: %+v created=%v err=%v", g2, created2, err)
	}
	if newIDCalls.Load() != 1 {
		t.Errorf("newID called on a hit: %d calls", newIDCalls.Load())
	}

	// A different subject or client is a different grant.
	for _, c := range [][2]string{{"test-claude", "subject-2"}, {"test-chatgpt", "subject-1"}} {
		o, created, err := cs.Ensure(ctx, c[0], c[1], "e2e", "L")
		if err != nil || !created || o.ID == g.ID {
			t.Errorf("%v: %+v created=%v err=%v", c, o, created, err)
		}
	}
}

func TestConnectorsStore_EnsureConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, st, _ := newStore(t)
	const n = 16
	var wg sync.WaitGroup
	ids := make([]string, n)
	createdN := atomic.Int64{}
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, created, err := cs.Ensure(ctx, "test-claude", "same-subject", "e2e", "Claude Test")
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = g.ID
			if created {
				createdN.Add(1)
			}
		}()
	}
	wg.Wait()
	if createdN.Load() != 1 {
		t.Errorf("created %d times, want exactly 1", createdN.Load())
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids differ: %v", ids)
		}
	}
	var rows int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM connector_grants`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("rows = %d (%v)", rows, err)
	}
}

func TestConnectorsStore_Validation(t *testing.T) {
	t.Parallel()
	cs, _, _ := newStore(t)
	for _, c := range []struct{ client, subject, user, label string }{
		{"", "s", "u", "l"}, {"c", "", "u", "l"}, {"c", "s", "", "l"}, {"c", "s", "u", ""},
	} {
		if _, _, err := cs.Ensure(context.Background(), c.client, c.subject, c.user, c.label); err == nil {
			t.Errorf("Ensure(%+v) accepted an empty field", c)
		}
	}
}

func TestConnectorsStore_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, _, _ := newStore(t)
	g, _, err := cs.Ensure(ctx, "test-claude", "subject-1", "e2e", "Claude Test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := cs.Get(ctx, g.ID)
	if err != nil || got.ID != g.ID || got.Label != "Claude Test" {
		t.Fatalf("Get: %+v %v", got, err)
	}
	if _, err := cs.Get(ctx, "00000000-0000-7000-8000-00000000dead"); !errors.Is(err, connectors.ErrNotFound) {
		t.Fatalf("Get(unknown) = %v, want ErrNotFound", err)
	}
}

func TestConnectorsStore_GetReadsLimitsAndRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cs, st, _ := newStore(t)
	g, _, _ := cs.Ensure(ctx, "test-claude", "subject-1", "e2e", "Claude Test")
	_, err := st.W.Exec(`UPDATE connector_grants SET scopes='read,write,generate', daily_budget_micro_usd=200000, auto_approve_takes=0,
		auto_approve_micro_usd=50000, revoked_at=?, revoked_by='human:op', last_used_at=? WHERE id=?`,
		store.FormatTime(fixedNow), store.FormatTime(fixedNow.Add(time.Minute)), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cs.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scopes) != 3 || got.Scopes[2] != auth.ScopeGenerate {
		t.Errorf("scopes %v", got.Scopes)
	}
	if got.Limits.DailyBudgetMicro == nil || int64(*got.Limits.DailyBudgetMicro) != 200000 ||
		got.Limits.AutoApproveTakes == nil || *got.Limits.AutoApproveTakes != 0 ||
		got.Limits.AutoApproveMicro == nil || int64(*got.Limits.AutoApproveMicro) != 50000 {
		t.Errorf("limits %+v", got.Limits)
	}
	if got.RevokedAt == nil || got.RevokedBy != "human:op" || got.LastUsedAt == nil || !got.LastUsedAt.Equal(fixedNow.Add(time.Minute)) {
		t.Errorf("revoked/last used: %+v", got)
	}
}
