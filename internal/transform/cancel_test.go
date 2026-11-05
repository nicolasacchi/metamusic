package transform_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("not a problem error: %v", err)
	}
	return pe
}

// TestCancel_Queued: the op cancels a queued row in its own transaction: nothing ran, cpu_ms is 0, the audit row exists and
// the reservation is gone; the runner never sees it.
func TestCancel_Queued(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{principal: agentA, maxCPU: 90_000})
	if err := r.svc.Cancel(context.Background(), agentA, id); err != nil {
		t.Fatal(err)
	}
	row := r.row(id)
	if row.State != "cancelled" || row.ErrCode != transform.ErrCancelledByUser || !row.CPU.Valid || row.CPU.Int64 != 0 || !row.Cancel {
		t.Fatalf("row = %+v", row)
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE op = 'transform_cancelled'`); n != 1 {
		t.Errorf("audit rows = %d", n)
	}
	if r.runOne() || r.child.Runs() != 0 {
		t.Error("the runner picked up a cancelled row")
	}
	if got := r.changedIDs(); len(got) == 0 || got[len(got)-1] != id {
		t.Errorf("no change notification for the cancel: %v", got)
	}
	if err := r.svc.Cancel(context.Background(), agentA, id); err != nil {
		t.Errorf("cancelling a terminal row must be a no-op, got %v", err)
	}
	if r.row(id).State != "cancelled" {
		t.Error("a second cancel changed the row")
	}
}

// TestCancel_AgentOnlyOwn: an agent cancels only its own transforms; a human cancels any; an unknown id is 404.
func TestCancel_AgentOnlyOwn(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	a := r.queue(qspec{principal: agentA, tag: "a"})
	err := r.svc.Cancel(ctx, agentB, a)
	pe := problemOf(t, err)
	if pe.Status != 403 || pe.Code != "FORBIDDEN" || pe.Details["owner"] != agentA.Name {
		t.Fatalf("agent B cancelling agent A's transform: %+v", pe)
	}
	if r.row(a).State != "queued" {
		t.Fatal("a refused cancel changed the row")
	}
	if err := r.svc.Cancel(ctx, human, a); err != nil {
		t.Fatalf("a human must cancel any transform: %v", err)
	}
	if r.row(a).State != "cancelled" {
		t.Error("the human's cancel did not apply")
	}
	if pe := problemOf(t, r.svc.Cancel(ctx, human, "no-such-id")); pe.Status != 404 {
		t.Errorf("unknown id: %+v", pe)
	}
}

// TestCancel_CrossPrincipalReuse (D-T1-2): agent B that was handed agent A's live transform through reuse is not its owner: the
// row says principal A, so B's cancel is 403 with the owner named, while A and any human may cancel.
func TestCancel_CrossPrincipalReuse(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	id := r.queue(qspec{principal: agentA}) // B's create returned this row (reused: true); the row still belongs to A
	pe := problemOf(t, r.svc.Cancel(ctx, agentB, id))
	if pe.Status != 403 || pe.Details["owner"] != agentA.Name {
		t.Fatalf("B's cancel: %+v", pe)
	}
	if err := r.svc.Cancel(ctx, agentA, id); err != nil {
		t.Fatal(err)
	}
	if got := r.row(id); got.State != "cancelled" || got.ErrCode != transform.ErrCancelledByUser {
		t.Errorf("after A's cancel: %+v (B's wait ends with cancelled / CANCELLED_BY_USER)", got)
	}
}

// TestCancel_Running_FakeChild: a cancel of a running row sets the intent; the runner kills the child within CancelGrace,
// removes the work dir, finishes the row cancelled with the CPU used so far, and writes no Take.
func TestCancel_Running_FakeChild(t *testing.T) {
	image(t)
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true)
		r.child.Hang = true
		r.child.CPU = 1500 * time.Millisecond
		id := r.queue(qspec{principal: agentA})
		done := make(chan struct{})
		go func() { _, _ = r.svc.RunOne(context.Background()); close(done) }()
		synctest.Wait() // the child is running
		if r.row(id).State != "running" {
			t.Fatal("not running")
		}
		start := time.Now()
		if err := r.svc.Cancel(context.Background(), agentA, id); err != nil {
			t.Fatal(err)
		}
		if got := r.row(id); got.State != "running" || !got.Cancel {
			t.Fatalf("a running row is only flagged by the op: %+v", got)
		}
		<-done
		if d := time.Since(start); d > transform.CancelGrace {
			t.Errorf("the child was killed after %v, grace is %v", d, transform.CancelGrace)
		}
		row := r.row(id)
		if row.State != "cancelled" || row.ErrCode != transform.ErrCancelledByUser || !row.CPU.Valid || row.CPU.Int64 != 1500 || row.Owner != "" {
			t.Fatalf("row = %+v", row)
		}
		if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 0 {
			t.Error("a cancelled transform produced a Take")
		}
		if r.exists("incoming/transform-"+id) || r.exists("takes/"+id) {
			t.Error("files survived the cancel")
		}
		if n := r.count(`SELECT count(*) FROM audit_log WHERE op = 'transform_cancelled'`); n != 1 {
			t.Errorf("audit rows = %d", n)
		}
	})
}

// TestCancel_RecoveredRowWithIntent: a cancel requested while the runner died finishes the row cancelled at recovery instead of
// running it again.
func TestCancel_RecoveredRowWithIntent(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{state: "running"})
	r.exec(`UPDATE transforms SET cancel_requested_at = ? WHERE id = ?`, "2026-11-03T12:00:00.000Z", id)
	r.clk.add(transform.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.row(id); got.State != "cancelled" || got.ErrCode != transform.ErrCancelledByUser {
		t.Errorf("row = %+v", got)
	}
}
