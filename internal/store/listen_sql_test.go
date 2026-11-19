package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
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
	if len(args) != 1 || args[0] != "human:alice" || !strings.Contains(cond, "lower(e.principal_id) = ?") || !strings.Contains(cond, "e.principal_kind = 'device'") {
		t.Errorf("alice owner: %q %v", cond, args)
	}
	if n := strings.Count(cond, "?"); n != len(args) {
		t.Errorf("%d placeholders for %d arguments", n, len(args))
	}
}

// TestOwnerEventsSQLKeyCaseInsensitive pins the case folding of the Owner key: SetOwner lowercases the configured name and
// the filter compares lower(principal_id), so events recorded with the name as the proxy sent it ("Alice") count as the
// Owner's when the config says "alice", and the reverse. Another user's event never counts; device events always do.
func TestOwnerEventsSQLKeyCaseInsensitive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	librarytest.Fixture(t, s, librarytest.FixedNow)
	for i, ev := range []struct{ kind, id string }{
		{"human", "human:Alice"}, {"human", "human:alice"}, {"human", "human:ALICE"}, {"human", "human:bob"}, {"device", "dev-1"}, {"agent", "agent:a"},
	} {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO listen_events(id,track_id,session_id,kind,source,principal_kind,principal_id,client_at,received_at)
			VALUES(?,?,'session-0001','start','web',?,?,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
			"01900000-0000-7000-8000-0000000000"+string(rune('a'+i))+"0", librarytest.TrackLuna, ev.kind, ev.id); err != nil {
			t.Fatal(err)
		}
	}
	count := func() string {
		cond, args := s.OwnerEventsSQL("e")
		return scalar(t, s.R, `SELECT group_concat(e.principal_id, ',') FROM (SELECT * FROM listen_events ORDER BY id) e WHERE `+cond, args...)
	}
	for _, owner := range []string{"alice", "Alice", "ALICE"} {
		s.SetOwner(owner)
		if got, want := count(), "human:Alice,human:alice,human:ALICE,dev-1"; got != want {
			t.Errorf("owner %q counts %q, want %q", owner, got, want)
		}
	}
}
