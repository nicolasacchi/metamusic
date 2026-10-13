package idem_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/idem"
)

// A closed database must surface as errors, never as a silent replay or proceed.
func TestStore_ClosedDatabaseErrors(t *testing.T) {
	s, _, st := newIdem(t)
	ctx := context.Background()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, "human:a", "k", "op", "sha", false); err == nil {
		t.Error("Begin on a closed store must fail")
	}
	if err := s.Complete(ctx, "human:a", "k", 201, "application/json", []byte("{}")); err == nil {
		t.Error("Complete on a closed store must fail")
	}
	if err := s.Extend(ctx, "human:a", "k"); err == nil {
		t.Error("Extend on a closed store must fail")
	}
	if err := s.Release(ctx, "human:a", "k"); err == nil {
		t.Error("Release on a closed store must fail")
	}
	if _, err := s.Prune(ctx); err == nil {
		t.Error("Prune on a closed store must fail")
	}
}

func TestStore_BeginCancelledContext(t *testing.T) {
	s, _, _ := newIdem(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Begin(ctx, "human:a", "k", "op", "sha", false); err == nil {
		t.Fatal("a cancelled context must fail Begin")
	}
}

func TestStore_CompleteNilBodyStoresEmpty(t *testing.T) {
	s, _, _ := newIdem(t)
	ctx := context.Background()
	if _, err := s.Begin(ctx, "human:a", "k", "op", "sha", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, "human:a", "k", 204, "", nil); err != nil {
		t.Fatal(err)
	}
	b, err := s.Begin(ctx, "human:a", "k", "op", "sha", false)
	if err != nil || b.Outcome != idem.Replay || b.Status != 204 || len(b.Body) != 0 {
		t.Fatalf("replay = %+v, %v", b, err)
	}
}

func TestStore_BeginSurfacesSchemaErrors(t *testing.T) {
	s, _, st := newIdem(t)
	if _, err := st.W.Exec(`ALTER TABLE idempotency RENAME TO idempotency_gone`); err != nil {
		t.Skipf("cannot break the schema here: %v", err)
	}
	if _, err := s.Begin(context.Background(), "human:a", "k", "op", "sha", false); err == nil {
		t.Fatal("a failing insert must be an error, not a proceed")
	}
}
