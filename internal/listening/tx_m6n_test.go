package listening_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/listening"
)

// TestListenTx_SameBehaviour: LogTx stores and refuses exactly what Log does, inside the caller's transaction (C381.4), and a
// rollback takes the events with it.
func TestListenTx_SameBehaviour(t *testing.T) {
	t.Parallel()
	s, st := newSvc(t)
	ctx := context.Background()
	bad := ev(2, "rewind")
	evs := []listening.Event{ev(1, "skip"), bad, ev(3, "complete")}
	run := func(commit bool) (acc, dup int, rej []listening.Rejected) {
		t.Helper()
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		acc, dup, rej, err = s.LogTx(ctx, tx, human, "web", evs)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		return acc, dup, rej
	}
	count := func() (n int) {
		if err := st.R.QueryRow(`SELECT COUNT(*) FROM listen_events WHERE id IN (?, ?, ?)`, id(1), id(2), id(3)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if acc, dup, rej := run(false); acc != 2 || dup != 0 || len(rej) != 1 || rej[0].Code != "bad_kind" || count() != 0 {
		t.Fatalf("rolled back: %d %d %v, %d rows", acc, dup, rej, count())
	}
	if acc, dup, rej := run(true); acc != 2 || dup != 0 || len(rej) != 1 || count() != 2 {
		t.Fatalf("committed: %d %d %v, %d rows", acc, dup, rej, count())
	}
	if acc, dup, _ := run(true); acc != 0 || dup != 2 {
		t.Fatalf("replay: %d %d", acc, dup)
	}
	// Log gives the same answer for the same events
	acc, dup, rej, err := s.Log(ctx, human, "web", evs)
	if err != nil || acc != 0 || dup != 2 || len(rej) != 1 {
		t.Fatalf("Log: %d %d %v %v", acc, dup, rej, err)
	}
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, _, err := s.LogTx(ctx, tx, human, "nope", evs); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}
