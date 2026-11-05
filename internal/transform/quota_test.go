package transform_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

func quotaOf(t *testing.T, r *rig, kind string) transform.Quota {
	t.Helper()
	q, err := r.svc.QuotaOf(context.Background(), r.st.R, kind, r.clk.now())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// TestQuota_ReservationAndRelease: a live row reserves its max_cpu_ms; a terminal row holds only the cpu_ms it recorded, so the
// reservation is released the moment the row ends; the window is rolling.
func TestQuota_ReservationAndRelease(t *testing.T) {
	r := newRig(t, false)
	r.child.CPU = 2 * time.Second
	id := r.queue(qspec{maxCPU: 40_000})
	if q := quotaOf(t, r, "human"); q.ReservedMS != 40_000 || q.UsedMS != 0 || q.CapMS != 14_400_000 {
		t.Fatalf("queued: %+v", q)
	}
	if q := quotaOf(t, r, "agent"); q.ReservedMS != 0 || q.CapMS != 1_800_000 {
		t.Fatalf("agent pool: %+v", q)
	}
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	cpu := r.row(id).CPU
	q := quotaOf(t, r, "human")
	if !cpu.Valid || cpu.Int64 < 2000 || q.ReservedMS != 0 || q.UsedMS != cpu.Int64 {
		t.Fatalf("done: cpu %v, quota %+v", cpu, q)
	}
	r.clk.add(transform.QuotaWindow + time.Minute) // the row leaves the rolling window
	if q := quotaOf(t, r, "human"); q.UsedMS != 0 {
		t.Errorf("after the window: %+v", q)
	}
}

// TestQuota_TerminalRecordsCPU: every terminal row records cpu_ms (the schema enforces it), a failure that never reached the
// child records zero, and none of them keeps a reservation.
func TestQuota_TerminalRecordsCPU(t *testing.T) {
	r := newRig(t, false)
	r.media.DecodeErr = errors.New("decode broke")
	failed := r.queue(qspec{maxCPU: 50_000, tag: "decode-fails"})
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	row := r.row(failed)
	if row.State != "failed" || !row.CPU.Valid || row.CPU.Int64 != 0 {
		t.Fatalf("failed row: %+v", row)
	}
	if q := quotaOf(t, r, "human"); q.ReservedMS != 0 {
		t.Errorf("a failed row kept its reservation: %+v", q)
	}
}

// TestQuota_AgentPoolSharedHumanUnaffected (D-T1-10): all agents draw from one pool, so one can starve the others, and a human
// has a pool of its own.
func TestQuota_AgentPoolSharedHumanUnaffected(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	r.queue(qspec{principal: agentA, maxCPU: 1_700_000}) // agent A reserves most of the 1 800 s cap
	if err := r.svc.CheckQuota(ctx, r.st.R, "agent", 50_000); err != nil {
		t.Fatalf("a request that fits was refused: %v", err)
	}
	err := r.svc.CheckQuota(ctx, r.st.R, transform.QuotaKind(agentB), 200_000)
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 429 || pe.Code != transform.ErrQuotaExceeded {
		t.Fatalf("agent B beyond the shared pool: %v", err)
	}
	for _, k := range []string{"used_ms", "reserved_ms", "cap_ms", "resets_at"} {
		if _, ok := pe.Details[k]; !ok {
			t.Errorf("details lack %s: %v", k, pe.Details)
		}
	}
	if pe.Details["reserved_ms"] != int64(1_700_000) || pe.Details["cap_ms"] != int64(1_800_000) {
		t.Errorf("details = %v", pe.Details)
	}
	if err := r.svc.CheckQuota(ctx, r.st.R, transform.QuotaKind(human), 200_000); err != nil {
		t.Errorf("the human pool was charged for agent work: %v", err)
	}
}

// TestQuota_CancelledQueuedReleases: create-then-cancel loops release their reservation immediately, so they can never
// exhaust the pool.
func TestQuota_CancelledQueuedReleases(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		id := r.queue(qspec{principal: agentA, maxCPU: 600_000})
		if q := quotaOf(t, r, "agent"); q.ReservedMS != 600_000 {
			t.Fatalf("round %d: reserved %d", i, q.ReservedMS)
		}
		if err := r.svc.Cancel(ctx, agentA, id); err != nil {
			t.Fatal(err)
		}
		if q := quotaOf(t, r, "agent"); q.ReservedMS != 0 || q.UsedMS != 0 {
			t.Fatalf("round %d after cancel: %+v", i, q)
		}
	}
	if err := r.svc.CheckQuota(ctx, r.st.R, "agent", 1_800_000); err != nil {
		t.Errorf("the pool is not whole after 25 create-cancel rounds: %v", err)
	}
}

// TestQuota_ResetsAt: resets_at is when the oldest counted CPU leaves the window.
func TestQuota_ResetsAt(t *testing.T) {
	r := newRig(t, false)
	r.child.CPU = 3 * time.Second
	r.queue(qspec{})
	r.runOne()
	q := quotaOf(t, r, "human")
	want := r.clk.now().Add(transform.QuotaWindow)
	if d := q.ResetsAt.Sub(want); d < -time.Second || d > time.Second {
		t.Errorf("resets_at %v, want about %v", q.ResetsAt, want)
	}
	if q.RemainingMS() != q.CapMS-q.UsedMS {
		t.Errorf("remaining %d", q.RemainingMS())
	}
}
