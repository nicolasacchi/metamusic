package pods_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func (r *lrig) reserved(batch string) int64 {
	var v int64
	if err := r.st.R.QueryRow(`SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'reserve' AND reason = 'pod_overhead'`, batch).Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return v
}

func TestReconcilePods_CreatesOnlyWhenQueued(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.tick()
	r.step(time.Hour)
	if n := r.api.nMutations(); n != 0 || len(r.rows(r.batchID)) != 0 {
		t.Fatalf("nothing is queued, yet %d creates or deletes and %d pods", n, len(r.rows(r.batchID)))
	}
	r.queue()
	r.tick()
	if r.api.nCreates() != 1 {
		t.Fatalf("creates = %d, want 1", r.api.nCreates())
	}
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateBooting || row.ProviderRef == "" || row.CreateSentAt == nil || row.Kind != pods.KindBatch || row.N != 1 {
		t.Fatalf("row %+v", row)
	}
	if !strings.HasPrefix(row.Name, "mm-") || len(row.Name) != 15 {
		t.Errorf("name %q", row.Name)
	}
	// a pod already lives: a second queued take creates nothing
	r.queue()
	r.step(10 * time.Second)
	if r.api.nCreates() != 1 {
		t.Errorf("creates = %d after a second take, want 1", r.api.nCreates())
	}
	b := r.api.creates[0]
	if b.GPUTypeID != "NVIDIA GeForce RTX 4090" || b.DiskGB != 40 || b.Env["MODE"] != "agent" || b.Env["MM_DEADMAN_S"] != "600" ||
		b.Env["MM_YUE2_3B_REVISION"] != strings.Repeat("a", 40) || !strings.Contains(b.Env["MM_POLL_URL"], "/worker/v1/pods/"+row.ID+"/poll?exp=") {
		t.Errorf("create body %+v", b)
	}
	// deadline = first create + BootS + T + Tmax + IdleS + SlackS = 1200 + 186 + 186 + 120 + 300 (one take existed at create)
	if got, want := row.DeadlineAt.Sub(*row.CreateSentAt), (1200+186+186+120+300)*time.Second; got != want {
		t.Errorf("deadline %s after the create, want %s", got, want)
	}
}

// Pods are created only inside an operator-approved window: armed, enabled, untripped, key present, window not over.
func TestReconcilePods_NoCreateOutsideTheWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		prep func(r *lrig)
	}{
		{"disarmed", func(r *lrig) { r.exec(`UPDATE provider_state SET armed = 0 WHERE backend = ?`, lBackend) }},
		{"tripped", func(r *lrig) {
			r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('t1', ?, ?, 'pod_overrun', '{}', '[]')`, lBackend, store.FormatTime(rigNow))
		}},
		{"key absent", func(r *lrig) { r.key.Store(false) }},
		{"window over", func(r *lrig) { r.clk.Advance(9 * time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newLRig(t)
			r.queue()
			tc.prep(r)
			r.tick()
			if r.api.nMutations() != 0 || len(r.rows(r.batchID)) != 0 || r.reserved(r.batchID) != 0 {
				t.Errorf("mutations=%d pods=%d reserved=%d", r.api.nMutations(), len(r.rows(r.batchID)), r.reserved(r.batchID))
			}
		})
	}
}

// The lease gate: a process that does not hold the runner lease does nothing at all.
func TestLifecycle_NoLeaseNoWork(t *testing.T) {
	t.Parallel()
	r := newLRig(t, func(o *pods.LifecycleOptions) { o.Holding = func() bool { return false } })
	r.queue()
	r.tick()
	if r.api.nCalls() != 0 || len(r.rows(r.batchID)) != 0 {
		t.Error("a non-holder acted")
	}
}

// The overhead is reserved exactly once per pod (P2's duty): the reserve and the pods row commit together, and a crash after the intent
// does not reserve again when the loop resumes it.
func TestReconcilePods_OverheadReservedOncePerPod(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	// F(1200 + 120 + 300 + 15 + 330 = 1965 s, 300000) = 163750 (the golden of TestPodQuote_Example)
	if got := r.reserved(r.batchID); got != 163750 {
		t.Fatalf("reserved %d, want 163750", got)
	}
	row := r.row(r.batchID, 1, 1)
	if row.OverheadReservedAt == nil || int64(row.OverheadReserved) != 163750 {
		t.Errorf("row reserved %v at %v", row.OverheadReserved, row.OverheadReservedAt)
	}
	// more ticks, a vanished pod, more ticks: still one reserve (the same pod identity never reserves twice)
	for range 5 {
		r.step(10 * time.Second)
	}
	if got := r.reserved(r.batchID); got != 163750 {
		t.Errorf("reserved %d after more ticks, want 163750", got)
	}
	if !strings.Contains(strings.Join(r.audits, "\n"), pods.OpPodCreate) {
		t.Errorf("no create audit row: %v", r.audits)
	}
}

// A crash between the intent and the POST (the transaction committed, the process died): the next tick resumes the intent, creates the pod
// and does not reserve again.
func TestReconcilePods_IntentResumesWithoutASecondReserve(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	// stop the first loop right after the reserve: the key vanishes for the POST, but the intent already exists
	r.key.Store(true)
	first := r.api
	first.createQ = []createAnswer{{err: errors.New("process died")}} // an unknown failure: the pod is orphaned, never replayed
	r.tick()
	if r.api.nCreates() != 1 {
		t.Fatalf("creates %d", r.api.nCreates())
	}
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateOrphaned {
		t.Fatalf("state %s", row.State)
	}
	for range 3 {
		r.step(10 * time.Second)
	}
	if r.api.nCreates() != 1 {
		t.Errorf("the create was replayed (%d)", r.api.nCreates())
	}
	if got := r.reserved(r.batchID); got != 163750 {
		t.Errorf("reserved %d, want one reserve", got)
	}
}

func TestReconcilePods_OverheadRefusedFailsClaims(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	// a batch whose ceiling cannot hold the pod overhead
	const small = "01900000-0000-7000-8000-00000000b003"
	r.lbatch(small, "est-small", 30, 100_000)
	r.batchID = small
	att := r.queue()
	r.tick()
	if r.api.nMutations() != 0 {
		t.Errorf("a refused reserve still called RunPod (%d)", r.api.nCalls())
	}
	rows := r.rows(small)
	if len(rows) != 1 || rows[0].State != pods.StateTerminated || rows[0].ErrorCode != "BUDGET_EXCEEDED" || rows[0].LifetimeS == nil || *rows[0].LifetimeS != 0 {
		t.Fatalf("rows %+v", rows)
	}
	if r.reserved(small) != 0 {
		t.Error("a refused reserve wrote a ledger row")
	}
	// the waiting take is closed (the runner requeues it into the refusal) and later claims are refused
	if got := r.str(`SELECT status || '|' || error_code FROM pod_assignments WHERE attempt_id = ?`, att); got != "cancelled|BUDGET_EXCEEDED" {
		t.Errorf("assignment %q", got)
	}
	if r.q.Has(att) {
		t.Error("the take is still queued")
	}
	b := pods.NewBackend(pods.BackendOptions{Name: lBackend, Store: r.st, Queue: r.q, Clock: r.clk, KillSlackS: 15})
	att2 := r.take(small)
	_, err := b.Submit(context.Background(), r.spec(att2, 186))
	var se *pods.SubmitError
	if !errors.As(err, &se) || se.Code != "BAD_REQUEST" {
		t.Errorf("Submit after the refusal: %v", err)
	}
	// and the loop never retries it
	r.step(time.Hour)
	if r.api.nMutations() != 0 || len(r.rows(small)) != 1 {
		t.Error("the loop retried a refused batch")
	}
}

func TestReconcilePods_OneTypePerCreate(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.createQ = []createAnswer{{err: &pods.ErrRefused{Status: 400, Capacity: true}}}
	r.queue()
	r.tick()
	// the first type was refused: a new row of the same pod (n = 1) with the next type, one type per create
	r.tick()
	if r.api.nCreates() != 2 {
		t.Fatalf("creates %d", r.api.nCreates())
	}
	if a, b := r.api.creates[0].GPUTypeID, r.api.creates[1].GPUTypeID; a != "NVIDIA GeForce RTX 4090" || b != "NVIDIA RTX A5000" {
		t.Errorf("types %q then %q", a, b)
	}
	first, second := r.row(r.batchID, 1, 1), r.row(r.batchID, 1, 2)
	if first.State != pods.StateTerminated || first.ErrorCode != "CREATE_REFUSED" || second.State != pods.StateBooting || second.N != 1 {
		t.Errorf("rows %+v %+v", first, second)
	}
	// one overhead for the pod, not one per try
	if got := r.reserved(r.batchID); got != 163750 {
		t.Errorf("reserved %d, want 163750", got)
	}
}

func TestReconcilePods_NoCapacityRetriesAfter5m(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	refused := createAnswer{err: &pods.ErrRefused{Status: 400, Capacity: true}}
	r.api.createQ = []createAnswer{refused, refused}
	r.queue()
	r.tick()
	r.tick() // both types refused
	if r.api.nCreates() != 2 {
		t.Fatalf("creates %d", r.api.nCreates())
	}
	if !strings.Contains(r.events(r.row(r.batchID, 1, 2).ID), "no_capacity") {
		t.Errorf("no no_capacity event: %s", r.events(r.row(r.batchID, 1, 2).ID))
	}
	r.step(4 * time.Minute)
	if r.api.nCreates() != 2 {
		t.Errorf("retried after 4 minutes (%d creates)", r.api.nCreates())
	}
	r.step(61 * time.Second)
	if r.api.nCreates() != 3 {
		t.Fatalf("creates %d after 5 minutes, want 3", r.api.nCreates())
	}
	if got := r.api.creates[2].GPUTypeID; got != "NVIDIA GeForce RTX 4090" {
		t.Errorf("the new round starts with the first type, got %q", got)
	}
	// the takes stayed queued throughout
	if !r.q.Has(r.str(`SELECT attempt_id FROM pod_assignments LIMIT 1`)) {
		t.Error("the take left the queue")
	}
}

func TestReconcilePods_RateLimitRetriesTheSameType(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.createQ = []createAnswer{{err: &pods.ErrRateLimited{RetryAfter: 45 * time.Second}}}
	r.queue()
	r.tick()
	r.step(10 * time.Second)
	if r.api.nCreates() != 1 {
		t.Fatalf("retried inside Retry-After (%d)", r.api.nCreates())
	}
	r.step(40 * time.Second)
	if r.api.nCreates() != 2 || r.api.creates[1].GPUTypeID != r.api.creates[0].GPUTypeID {
		t.Errorf("creates %d, types %q", r.api.nCreates(), r.api.creates[len(r.api.creates)-1].GPUTypeID)
	}
}

func TestReconcilePods_BalanceStopsBatch(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.createQ = []createAnswer{{err: pods.ErrBalance}}
	att := r.queue()
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated || row.ErrorCode != "BALANCE" {
		t.Fatalf("row %+v", row)
	}
	r.step(time.Hour)
	if r.api.nCreates() != 1 {
		t.Errorf("no retry for the batch, got %d creates", r.api.nCreates())
	}
	if got := r.str(`SELECT status FROM pod_assignments WHERE attempt_id = ?`, att); got != "cancelled" {
		t.Errorf("assignment %q", got)
	}
}

func TestReconcilePods_AmbiguousCreateOrphanedNeverReplayed(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	// the create reached RunPod (the pod exists) but the answer was lost
	r.api.createQ = []createAnswer{{err: &pods.ErrAmbiguous{Op: "create", Status: 502}, effect: true}}
	r.queue()
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateOrphaned {
		t.Fatalf("state %s", row.State)
	}
	// the name finds it; it is deleted; it is never created again
	for range 4 {
		r.step(15 * time.Second)
	}
	row = r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated || row.ProviderRef == "" || row.LifetimeS == nil || *row.LifetimeS != 60 {
		t.Errorf("row %+v (life %v)", row, row.LifetimeS)
	}
	if got := r.api.nDeletes(); got < 1 {
		t.Errorf("deletes %d", got)
	}
	// the lost pod's take may get the one re-creation, but a create is never replayed: every create has its own name
	seen := map[string]bool{}
	for _, c := range r.api.creates {
		if seen[c.Name] {
			t.Errorf("create %q was replayed", c.Name)
		}
		seen[c.Name] = true
	}
	if n := r.api.nCreates(); n > 2 {
		t.Errorf("creates %d", n)
	}
}

func TestReconcilePods_NameSweepTrips(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.mu.Lock()
	r.api.pods["stray"] = &fakePod{Pod: pods.Pod{ID: "stray", Name: "mm-abcdefabcdef", Status: pods.StatusRunning}}
	r.api.pods["other"] = &fakePod{Pod: pods.Pod{ID: "other", Name: "someone-elses-pod", Status: pods.StatusRunning}}
	r.api.mu.Unlock()
	r.tick()
	if got := r.tripped(); len(got) != 1 || got[0] != "pod_unknown" {
		t.Errorf("trips %v", got)
	}
	if r.api.nDeletes() != 1 || r.api.deletes[0] != "stray" {
		t.Errorf("deletes %v", r.api.deletes)
	}
	// a pod that is not ours by name is left alone, and the sweep runs once a minute
	lists := r.api.lists
	r.step(10 * time.Second)
	if r.api.lists != lists {
		t.Error("the sweep ran again inside a minute")
	}
}

func TestReconcilePods_KeyRemovedAlertsOnly(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	calls := r.api.nCalls()
	r.key.Store(false)
	for range 3 {
		r.step(10 * time.Minute)
	}
	if r.api.nCalls() != calls {
		t.Errorf("calls after the key was removed: %d -> %d", calls, r.api.nCalls())
	}
	if len(r.tripped()) != 0 {
		t.Errorf("trips %v with the key removed", r.tripped())
	}
	row := r.row(r.batchID, 1, 1)
	if got := strings.Count(r.events(row.ID), "key_absent"); got != 1 || row.State != pods.StateBooting {
		t.Errorf("events %q state %s", r.events(row.ID), row.State)
	}
}

func TestReconcilePods_AccessTrip(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	r.api.getErr = &pods.APIError{Op: "get pod", Status: 403}
	r.api.getErr = accessDenied{}
	r.step(10 * time.Second)
	if got := r.tripped(); len(got) != 1 || got[0] != "pod_access" {
		t.Errorf("trips %v", got)
	}
}

type accessDenied struct{}

func (accessDenied) Error() string        { return "access refused" }
func (accessDenied) Is(target error) bool { return target == provider.ErrAccess }

func TestReconcilePods_CreateInvalidTrips(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.createQ = []createAnswer{{err: pods.ErrInvalid}}
	r.queue()
	r.tick()
	if got := r.tripped(); len(got) != 1 || got[0] != "pod_create_invalid" {
		t.Errorf("trips %v", got)
	}
	r.step(time.Hour)
	if r.api.nCreates() != 1 {
		t.Errorf("creates %d", r.api.nCreates())
	}
}
