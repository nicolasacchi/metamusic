package jobs

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestCancelIntentsTx_MatchesCancelBatch: the factored body leaves exactly the rows CancelBatch leaves, for a batch awaiting
// confirmation (withdrawn) and for a running one (only the jobs carry an intent), and a terminal job is never marked.
func TestCancelIntentsTx_MatchesCancelBatch(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"awaiting_confirmation", "active"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			svc := r.service(t.Context())
			a := r.seedBatch(batchSpec{state: state, jobs: 2, principal: "agent:a1"})
			b := r.seedBatch(batchSpec{state: state, jobs: 2, principal: "agent:a1"})
			owner := auth.Principal{Kind: auth.KindAgent, ID: "a1", Name: "a1"}
			if _, err := svc.CancelBatch(t.Context(), owner, a.Batch, ""); err != nil {
				t.Fatal(err)
			}
			tx, err := r.st.W.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.CancelIntentsTx(t.Context(), tx, []string{b.Batch}, owner.Key()); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			shape := func(batch string) string {
				return r.str(`SELECT b.state || '|' || COALESCE(ap.state,'-') || '|' || COALESCE(ap.via,'-') || '|' || COALESCE(ap.decided_by,'-') || '|' ||
					(SELECT group_concat(COALESCE(j.cancel_requested_by,'-'), ',') FROM jobs j WHERE j.batch_id = b.id) || '|' ||
					(SELECT COALESCE(SUM(CASE kind WHEN 'reserve' THEN micro_usd WHEN 'release' THEN -micro_usd ELSE 0 END),0) FROM ledger WHERE batch_id = b.id)
					FROM batches b LEFT JOIN approvals ap ON ap.batch_id = b.id WHERE b.id = ?`, batch)
			}
			if x, y := shape(a.Batch), shape(b.Batch); x != y {
				t.Fatalf("CancelBatch left %q, CancelIntentsTx left %q", x, y)
			}
		})
	}
}

func TestCancelIntentsTx_SkipsTerminalJobsAndRollsBack(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{state: "active", jobs: 2})
	r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = ? WHERE id = ?`, "2026-10-14T12:00:00.000Z", s.Jobs[0])
	tx, err := r.st.W.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelIntentsTx(t.Context(), tx, []string{s.Batch}, "system:mandate-revoke"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM jobs WHERE cancel_requested_at IS NOT NULL`); n != 0 {
		t.Fatalf("a rolled-back cancel left %d intents", n)
	}
	tx, _ = r.st.W.BeginTx(t.Context(), nil)
	if err := svc.CancelIntentsTx(t.Context(), tx, []string{s.Batch}, "system:mandate-revoke"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := r.str(`SELECT COALESCE(cancel_requested_by,'-') FROM jobs WHERE id = ?`, s.Jobs[0]); got != "-" {
		t.Fatalf("a terminal job was marked: %s", got)
	}
	if got := r.str(`SELECT cancel_requested_by FROM jobs WHERE id = ?`, s.Jobs[1]); got != "system:mandate-revoke" {
		t.Fatalf("live job by = %s", got)
	}
}

func TestCancelIntentsTx_Unwired(t *testing.T) {
	t.Parallel()
	svc := New(Options{})
	if err := svc.CancelIntentsTx(t.Context(), (*sql.Tx)(nil), []string{"x"}, "y"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelIntentsTx_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	waiting := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 1, principal: "agent:a1"})
	begin := func() *sql.Tx {
		tx, err := r.st.W.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	// a finished transaction fails on the first statement
	done := begin()
	_ = done.Rollback()
	if err := svc.CancelIntentsTx(t.Context(), done, []string{waiting.Batch}, "system:x"); err == nil {
		t.Error("a finished transaction was accepted")
	}
	// an unknown batch id is an error from the state read, never a silent success
	tx := begin()
	if err := svc.CancelIntentsTx(t.Context(), tx, []string{"no-such-batch"}, "system:x"); err == nil {
		t.Error("an unknown batch was accepted")
	}
	_ = tx.Rollback()
	// withdrawing a waiting batch needs the spend service
	svc.o.Spend = nil
	tx = begin()
	if err := svc.CancelIntentsTx(t.Context(), tx, []string{waiting.Batch}, "system:x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("no spend service: %v", err)
	}
	_ = tx.Rollback()
}
