package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestOwnerEventsSQL pins the one Owner-side listening filter (C306.7): with an Owner key set, a human event counts only for
// that principal and device events count; with none set (local mode, no user map) every human event is the Owner's.
func TestOwnerEventsSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'listen_events_principal'`); n != "1" {
		t.Errorf("index listen_events_principal missing (0018)")
	}
	cond, args := s.OwnerEventsSQL("")
	if len(args) != 0 || !strings.Contains(cond, "'human'") {
		t.Errorf("unset owner: %q %v", cond, args)
	}
	s.SetOwner("alice")
	cond, args = s.OwnerEventsSQL("e")
	if len(args) != 1 || args[0] != "human:alice" || !strings.Contains(cond, "e.principal_id = ?") || !strings.Contains(cond, "e.principal_kind = 'device'") {
		t.Errorf("alice owner: %q %v", cond, args)
	}
	if n := strings.Count(cond, "?"); n != len(args) {
		t.Errorf("%d placeholders for %d arguments", n, len(args))
	}
}
