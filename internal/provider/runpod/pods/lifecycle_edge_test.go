package pods_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestReconcilePods_IntentWithoutKeyEndsNoKey(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.podOf("01900000-0000-7000-8000-00000000d0a1", "mm-0a1a1a1a1a1a", r.batchID, "intent")
	r.key.Store(false)
	r.tick()
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || row.ErrorCode != "NO_KEY" || r.api.nCalls() != 0 {
		t.Errorf("row %s %s calls %d", row.State, row.ErrorCode, r.api.nCalls())
	}
}

// An intent left by a crash resumes: the create goes out, and it needs no second reserve (the intent's reserve is in its own row).
func TestReconcilePods_IntentIsResumed(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.podOf("01900000-0000-7000-8000-00000000d0a2", "mm-0a2a2a2a2a2a", r.batchID, "intent")
	r.tick()
	if r.api.nCreates() != 1 || r.api.creates[0].Name != "mm-0a2a2a2a2a2a" {
		t.Fatalf("creates %+v", r.api.creates)
	}
	if got := r.row(r.batchID, 1, 1); got.State != pods.StateBooting {
		t.Errorf("state %s", got.State)
	}
}

func TestReconcilePods_IntentNoLongerNeededIsNotSent(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	r.podOf("01900000-0000-7000-8000-00000000d0a3", "mm-0a3a3a3a3a3a", r.batchID, "intent")
	r.q.Drop(att)
	if _, err := pods.CloseAssignment(context.Background(), r.st.W, att, pods.AssignCancelled, "", r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || row.ErrorCode != "NOT_NEEDED" || r.api.nCreates() != 0 {
		t.Errorf("row %s %s creates %d", row.State, row.ErrorCode, r.api.nCreates())
	}
}

// With less than 30 s of lease left no create goes out: the intent waits for the heartbeat.
func TestReconcilePods_ShortLeaseWaitsBeforeTheCreate(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.exec(`UPDATE runner_lease SET expires_at = ?`, store.FormatTime(rigNow.Add(20*time.Second)))
	r.queue()
	r.podOf("01900000-0000-7000-8000-00000000d0a4", "mm-0a4a4a4a4a4a", r.batchID, "intent")
	r.tick()
	if r.api.nCreates() != 0 {
		t.Fatal("a create went out with 20 s of lease")
	}
	r.exec(`UPDATE runner_lease SET expires_at = ?`, store.FormatTime(rigNow.Add(time.Hour)))
	r.tick()
	if r.api.nCreates() != 1 {
		t.Errorf("creates %d after the heartbeat", r.api.nCreates())
	}
}

func TestReconcilePods_StaleCreatingIsOrphanedNeverReplayed(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.podOf("01900000-0000-7000-8000-00000000d0a5", "mm-0a5a5a5a5a5a", r.batchID, "creating")
	r.tick()
	if r.api.nCreates() != 0 {
		t.Error("a create was replayed")
	}
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateOrphaned || row.ErrorCode != "CREATE_AMBIGUOUS" {
		t.Errorf("row %s %s", row.State, row.ErrorCode)
	}
	r.tick()
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || *row.LifetimeS != 0 {
		t.Errorf("row %s life %v", row.State, row.LifetimeS)
	}
}

// An orphan with no known id that is not listed: the listing's lag is waited out for two minutes, then the pod is declared gone.
func TestReconcilePods_OrphanWithoutIdWaitsForTheListing(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.createQ = []createAnswer{{err: &pods.ErrAmbiguous{Op: "create", Status: 504}}} // the pod was not created
	r.queue()
	r.tick()
	r.drop()
	r.step(30 * time.Second)
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateOrphaned {
		t.Fatalf("state %s inside the lag", got)
	}
	r.step(100 * time.Second)
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated || row.LifetimeS == nil || *row.LifetimeS != 180 {
		t.Errorf("row %s life %v", row.State, row.LifetimeS)
	}
}

func TestReconcilePods_CreateAnswersTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
		wantTrip string
	}{
		{"queued by RunPod", pods.ErrQueued, "QUEUED_REFUSED", ""},
		{"no key file", pods.ErrNoKey, "NO_KEY", ""},
		{"access refused", accessDenied{}, "ACCESS_REFUSED", "pod_access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newLRig(t)
			r.api.createQ = []createAnswer{{err: tc.err}}
			r.queue()
			r.tick()
			if got := r.row(r.batchID, 1, 1); got.State != pods.StateTerminated || got.ErrorCode != tc.wantCode {
				t.Fatalf("row %s %s", got.State, got.ErrorCode)
			}
			if tr := r.tripped(); (tc.wantTrip == "") != (len(tr) == 0) || (len(tr) > 0 && tr[0] != tc.wantTrip) {
				t.Errorf("trips %v", tr)
			}
		})
	}
}

// A pod that disappears while it works: its takes are lost, it ends through terminating, and the lifetime is rounded up to the minute.
func TestReconcilePods_PodGoneWhileBusy(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	r.walkToReady()
	if noTake(decodeResp(t, r.agent(1, idle()))) {
		t.Fatal("no take")
	}
	r.drop()
	r.tick() // busy
	r.clk.Advance(61 * time.Second)
	r.api.vanish(1)
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated || row.ErrorCode != "POD_STATE" || row.LifetimeS == nil {
		t.Fatalf("row %s %s", row.State, row.ErrorCode)
	}
	if got := *row.LifetimeS; got%60 != 0 || got < 120 {
		t.Errorf("lifetime %d is not a whole number of minutes", got)
	}
	if got := r.str(`SELECT status || '|' || error_code FROM pod_assignments WHERE attempt_id = ?`, att); got != "lost|WORKER_LOST" {
		t.Errorf("handed take %q", got)
	}
}

func TestReconcilePods_ExitedPodIsDeleted(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	r.api.setStatus(1, pods.StatusExited)
	r.step(10 * time.Second)
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminating || row.ErrorCode != "POD_STATE" || r.api.nDeletes() != 1 {
		t.Errorf("row %s %s deletes %d", row.State, row.ErrorCode, r.api.nDeletes())
	}
}

func TestReconcilePods_LifetimeIsRoundedUpToTheMinute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		wait time.Duration
		want int
	}{{30 * time.Second, 60}, {60 * time.Second, 60}, {61 * time.Second, 120}, {150 * time.Second, 180}} {
		r := newLRig(t)
		r.queue()
		r.tick()
		r.drop()
		r.clk.Advance(tc.wait)
		r.api.vanish(1)
		r.tick()
		if row := r.row(r.batchID, 1, 1); row.LifetimeS == nil || *row.LifetimeS != tc.want {
			t.Errorf("after %s: lifetime %v, want %d", tc.wait, row.LifetimeS, tc.want)
		}
	}
}

func TestReconcilePods_SweepListFailureIsAnErrorNotATrip(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.listErr = accessDenied{}
	if err := r.l.Tick(context.Background()); err == nil || strings.Contains(err.Error(), "rp0") {
		t.Errorf("tick error %v", err)
	}
	if tr := r.tripped(); len(tr) != 1 || tr[0] != "pod_access" {
		t.Errorf("trips %v", tr)
	}
}

// The overrun trip is opened once per pod however many passes see it.
func TestReconcilePods_TripsOpenOncePerPod(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	row := r.walkToReady()
	r.api.keepAlive = true
	r.quiet(row.DeadlineAt.Sub(r.clk.Now()) - 100*time.Second)
	for r.clk.Now().Before(row.DeadlineAt.Add(10 * time.Minute)) {
		r.step(30 * time.Second)
	}
	n := 0
	for _, x := range r.tripped() {
		if x == "pod_overrun" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("pod_overrun opened %d times: %v", n, r.tripped())
	}
}

func TestReconcilePods_ExcessNeedsAFinishedBatch(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	r.drop()
	r.api.vanish(1)
	r.step(10 * time.Second)
	r.step(10 * time.Second)
	if got := r.settled("settle", "excess"); got != 0 || r.count(`SELECT COUNT(*) FROM pods WHERE settled = 1`) != 0 || len(r.tripped()) != 0 {
		t.Errorf("settled %d, trips %v with the batch still active", got, r.tripped())
	}
}

// An intent left behind a window that closed (disarmed, tripped, or past its maximum) is never sent.
func TestReconcilePods_IntentAfterTheWindowClosedIsNotSent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		prep func(r *lrig)
	}{
		{"disarmed", func(r *lrig) { r.exec(`UPDATE provider_state SET armed = 0 WHERE backend = ?`, lBackend) }},
		{"tripped", func(r *lrig) {
			r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('t9', ?, ?, 'pod_price', '{}', '[]')`, lBackend, store.FormatTime(rigNow))
		}},
		{"past the maximum", func(r *lrig) { r.clk.Advance(9 * time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newLRig(t)
			r.queue()
			r.podOf("01900000-0000-7000-8000-00000000d0b1", "mm-0b1b1b1b1b1b", r.batchID, "intent")
			tc.prep(r)
			r.tick()
			if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || row.ErrorCode != "WINDOW_CLOSED" || r.api.nCreates() != 0 {
				t.Errorf("row %s %s creates %d", row.State, row.ErrorCode, r.api.nCreates())
			}
		})
	}
}
