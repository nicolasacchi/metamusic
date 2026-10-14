package idem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/idem"
)

func TestIdem_CompleteTx(t *testing.T) {
	t.Parallel()
	s, c, st := newIdem(t)
	ctx := context.Background()
	rowState := func(pk, key string) (state string, status *int, body []byte) {
		t.Helper()
		var stt *int
		if err := st.R.QueryRowContext(ctx, `SELECT state, status, response FROM idempotency WHERE principal_key=? AND key=?`, pk, key).Scan(&state, &stt, &body); err != nil {
			t.Fatal(err)
		}
		return state, stt, body
	}

	b, err := s.Begin(ctx, "agent:a", "k1", "op", "sha", false)
	if err != nil || b.Outcome != idem.Proceed || b.Gen == "" {
		t.Fatalf("begin: %+v %v", b, err)
	}

	// rollback ⇒ still pending, marks untouched by the failed statement path
	ctxM, marks := idem.WithMarks(ctx)
	tx, _ := st.W.BeginTx(ctx, nil)
	if err := idem.CompleteTx(ctxM, tx, "agent:a", "k1", b.Gen, 201, "application/json", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if !marks.Completed() || marks.Audited() {
		t.Fatalf("marks after CompleteTx: completed=%v audited=%v", marks.Completed(), marks.Audited())
	}
	marks.Reset() // what the handler's deferred rollback does
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if marks.Completed() {
		t.Fatal("Reset did not clear")
	}
	if stt, _, _ := rowState("agent:a", "k1"); stt != "pending" {
		t.Fatalf("after rollback state = %s", stt)
	}

	// commit ⇒ done, replayable
	tx, _ = st.W.BeginTx(ctx, nil)
	if err := idem.CompleteTx(ctxM, tx, "agent:a", "k1", b.Gen, 201, "application/json", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stt, status, body := rowState("agent:a", "k1")
	if stt != "done" || status == nil || *status != 201 || string(body) != `{"ok":true}` {
		t.Fatalf("after commit: %s %v %s", stt, status, body)
	}
	if r, _ := s.Begin(ctx, "agent:a", "k1", "op", "sha", false); r.Outcome != idem.Replay || r.Status != 201 || string(r.Body) != `{"ok":true}` {
		t.Fatalf("replay: %+v", r)
	}

	// completing twice: the row is no longer pending
	tx, _ = st.W.BeginTx(ctx, nil)
	if err := idem.CompleteTx(ctx, tx, "agent:a", "k1", b.Gen, 201, "application/json", nil); !errors.Is(err, idem.ErrLeaseLost) {
		t.Fatalf("second complete: %v", err)
	}
	_ = tx.Rollback()

	// reclaimed by another request ⇒ new created_at ⇒ the old generation cannot complete
	b2, _ := s.Begin(ctx, "agent:a", "k2", "op", "sha2", false)
	c.t = c.t.Add(idem.Lease + time.Second) // the lease lapses; the same request may reclaim
	b3, err := s.Begin(ctx, "agent:a", "k2", "op", "sha2", false)
	if err != nil || b3.Outcome != idem.Proceed {
		t.Fatalf("reclaim: %+v %v", b3, err)
	}
	if b3.Gen == b2.Gen {
		t.Fatalf("generations equal: %s", b3.Gen)
	}
	tx, _ = st.W.BeginTx(ctx, nil)
	if err := idem.CompleteTx(ctx, tx, "agent:a", "k2", b2.Gen, 201, "application/json", nil); !errors.Is(err, idem.ErrLeaseLost) {
		t.Fatalf("stale generation: %v", err)
	}
	if err := idem.CompleteTx(ctx, tx, "agent:a", "k2", b3.Gen, 201, "application/json", nil); err != nil {
		t.Fatalf("current generation: %v", err)
	}
	_ = tx.Rollback()
}

func TestIdem_KeyAndMarksContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, _, _, ok := idem.KeyFrom(ctx); ok {
		t.Fatal("key present on empty context")
	}
	if idem.MarksFrom(ctx) != nil {
		t.Fatal("marks present on empty context")
	}
	var nilMarks *idem.Marks
	if nilMarks.Completed() || nilMarks.Audited() {
		t.Fatal("nil marks must read false")
	}
	nilMarks.MarkCompleted() // must not panic
	nilMarks.MarkAudited()
	nilMarks.Reset()

	ctx = idem.WithKey(ctx, "agent:a", "k", "2026-10-14T12:00:00Z")
	if pk, k, g, ok := idem.KeyFrom(ctx); !ok || pk != "agent:a" || k != "k" || g != "2026-10-14T12:00:00Z" {
		t.Fatalf("KeyFrom = %q %q %q %v", pk, k, g, ok)
	}
	ctx, m := idem.WithMarks(ctx)
	if idem.MarksFrom(ctx) != m {
		t.Fatal("MarksFrom did not return the installed marks")
	}
	m.MarkAudited()
	if !m.Audited() || m.Completed() {
		t.Fatal("independent flags expected")
	}
}

func TestIdem_CompleteTx_ClosedTransactionErrors(t *testing.T) {
	t.Parallel()
	_, _, st := newIdem(t)
	ctx := context.Background()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := idem.CompleteTx(ctx, tx, "agent:a", "k", "gen", 200, "application/json", nil); err == nil {
		t.Fatal("CompleteTx on a finished transaction must fail, never report success")
	}
}
