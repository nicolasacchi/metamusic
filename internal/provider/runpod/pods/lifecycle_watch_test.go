package pods_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// later makes the unfinished job of the rig wait: queued, claimable only after d from now, so it is neither live nor soon claimable.
func (r *lrig) later(d time.Duration) {
	r.t.Helper()
	r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE batch_id = ?`, store.FormatTime(r.clk.Now().Add(d)), r.batchID)
}

// endJobs ends every job of the batch (cancelled: the state a queued job may legally reach).
func (r *lrig) endJobs() {
	r.t.Helper()
	r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = ? WHERE batch_id = ?`, store.FormatTime(r.clk.Now()), r.batchID)
}

// drop removes the queued takes from memory (a hand-out took them, or the runner cancelled them).
func (r *lrig) drop() {
	for _, id := range strings.Fields(r.str(`SELECT group_concat(attempt_id, ' ') FROM pod_assignments WHERE pod_id IS NULL`)) {
		r.q.Drop(id)
	}
}

func TestReconcilePods_NoOrphanBeforeFirstPoll(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	r.api.setStatus(1, pods.StatusRunning)
	for range 8 { // 8 minutes without a single poll: only the boot timeout applies, never the orphan rule
		r.step(time.Minute)
	}
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateBooting || row.RunningSeenAt == nil {
		t.Errorf("state %s", row.State)
	}
}

func TestReconcilePods_OrphanAfterLastPoll(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	row := r.walkToReady()
	if w := r.agent(1, idle()); w.Code != 200 || noTake(decodeResp(t, w)) {
		t.Fatalf("no take handed: %s", w.Body)
	}
	r.drop()
	r.step(170 * time.Second)
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateBusy {
		t.Fatalf("state %s inside the orphan window, want busy", got)
	}
	r.step(20 * time.Second)
	row = r.row(r.batchID, 1, 1)
	if row.State != pods.StateOrphaned || row.ErrorCode != "WORKER_LOST" {
		t.Fatalf("state %s code %s", row.State, row.ErrorCode)
	}
	if got := r.str(`SELECT status || '|' || error_code FROM pod_assignments WHERE attempt_id = ?`, att); got != "lost|WORKER_LOST" {
		t.Errorf("handed take %q", got)
	}
	if r.api.nDeletes() != 1 {
		t.Errorf("deletes %d", r.api.nDeletes())
	}
	r.step(10 * time.Second)
	if row = r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || row.LifetimeS == nil || *row.LifetimeS%60 != 0 {
		t.Errorf("row %s life %v", row.State, row.LifetimeS)
	}
}

func TestReconcilePods_IdleStops(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.walkToReady()
	r.drop()
	r.later(2 * time.Hour)
	ping := func() { r.agent(1, idle()) } // the pod stays alive
	r.step(100 * time.Second)
	ping()
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateReady {
		t.Fatalf("stopped after 100 s idle: %s", got)
	}
	r.step(60 * time.Second) // 160 s after ready: past IdleS (120 s)
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminating || !strings.Contains(r.events(row.ID), "stop") {
		t.Fatalf("state %s events %s", row.State, r.events(row.ID))
	}
	// the stop is answered on the next poll; the pod is deleted only after 60 s
	if w := r.agent(1, idle()); decodeResp(t, w).Action != "stop" {
		t.Errorf("poll answer %s", w.Body)
	}
	if r.api.nDeletes() != 0 {
		t.Errorf("deleted at once (%d)", r.api.nDeletes())
	}
	r.step(30 * time.Second)
	if r.api.nDeletes() != 0 {
		t.Errorf("deleted after 30 s (%d)", r.api.nDeletes())
	}
	r.step(31 * time.Second)
	if r.api.nDeletes() != 1 {
		t.Fatalf("deletes %d after 60 s", r.api.nDeletes())
	}
	r.step(time.Second)
	if row = r.row(r.batchID, 1, 1); row.State != pods.StateTerminated || row.GoneSeenAt == nil || row.LifetimeS == nil {
		t.Errorf("row %+v", row)
	}
}

func TestReconcilePods_WaitsForSoonClaimable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		prep func(r *lrig)
	}{
		{"queued job claimable inside the idle window", func(r *lrig) {
			r.drop()
			r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE batch_id = ?`, store.FormatTime(r.clk.Now().Add(90*time.Second)), r.batchID)
		}},
		{"a live job of the batch", func(r *lrig) {
			r.drop()
			r.exec(`UPDATE jobs SET state = 'transferring' WHERE batch_id = ?`, r.batchID)
		}},
		{"a take still queued", func(r *lrig) { r.later(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newLRig(t)
			r.queue()
			r.walkToReady()
			tc.prep(r)
			for range 6 {
				r.step(40 * time.Second)
				r.agent(1, state("busy"))
			}
			if got := r.row(r.batchID, 1, 1).State; got != pods.StateReady {
				t.Errorf("state %s, want ready", got)
			}
		})
	}
}

func TestReconcilePods_DeadlineStops(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	row := r.walkToReady()
	r.drop()
	r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE batch_id = ?`, store.FormatTime(row.DeadlineAt.Add(time.Hour)), r.batchID)
	r.q.Add(r.batchID, r.spec(r.str(`SELECT attempt_id FROM pod_assignments LIMIT 1`), 186)) // a take is waiting: only the deadline can stop the pod
	r.quiet(row.DeadlineAt.Sub(r.clk.Now()) - 100*time.Second)
	for range 20 {
		if r.row(r.batchID, 1, 1).State == pods.StateTerminating {
			break
		}
		r.agent(1, state("busy"))
		r.step(10 * time.Second)
	}
	got := r.row(r.batchID, 1, 1)
	if got.State != pods.StateTerminating || got.ErrorCode != "POD_DEADLINE" {
		t.Fatalf("state %s code %q %s before the deadline", got.State, got.ErrorCode, row.DeadlineAt.Sub(r.clk.Now()))
	}
	if left := row.DeadlineAt.Sub(r.clk.Now()); left > 30*time.Second || left < 15*time.Second {
		t.Errorf("stopped %s before the deadline, want about 30 s", left)
	}
}

func TestReconcilePods_BootTimeout(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.tick()
	r.api.setStatus(1, pods.StatusRunning)
	r.step(1200 * time.Second)
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateBooting {
		t.Fatalf("state %s at BootS", got)
	}
	r.step(65 * time.Second) // BootS + 60 s
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminating || row.ErrorCode != "BOOT_TIMEOUT" || r.api.nDeletes() != 1 {
		t.Errorf("state %s code %s deletes %d", row.State, row.ErrorCode, r.api.nDeletes())
	}
}

func TestReconcilePods_PriceMismatchTerminates(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.api.cost = 300001
	r.queue()
	r.tick()
	r.step(10 * time.Second)
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminating || row.ErrorCode != "PRICE_MISMATCH" || r.api.nDeletes() != 1 {
		t.Fatalf("state %s code %s deletes %d", row.State, row.ErrorCode, r.api.nDeletes())
	}
	if got := r.tripped(); len(got) != 1 || got[0] != "pod_price" {
		t.Errorf("trips %v", got)
	}
	if got := r.str(`SELECT reported_pph_micro_usd FROM pods WHERE id = ?`, row.ID); got != "300001" {
		t.Errorf("reported price %s", got)
	}
}

func TestReconcilePods_PriceAtOrBelowTheFrozenPriceIsFine(t *testing.T) {
	t.Parallel()
	for _, cost := range []money.MicroUSD{0, 100000, 300000} {
		r := newLRig(t)
		r.api.cost = cost
		r.queue()
		r.tick()
		r.step(10 * time.Second)
		if got := r.row(r.batchID, 1, 1).State; got != pods.StateBooting || len(r.tripped()) != 0 {
			t.Errorf("cost %d: state %s trips %v", cost, got, r.tripped())
		}
	}
}

func TestReconcilePods_RestartLoop(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.walkToReady()
	r.drop()
	r.later(time.Hour)
	// the first restart: back to booting
	r.agent(1, state("booting"))
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateBooting || row.Restarts != 1 {
		t.Fatalf("after one restart: %s restarts %d (%s)", row.State, row.Restarts, r.events(row.ID))
	}
	// the stale boot report of the old container does not make it ready again
	r.tick()
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateBooting {
		t.Fatalf("re-promoted on a stale boot report: %s", got)
	}
	r.agent(1, idle().with("boot", bootDoc))
	r.tick()
	if got := r.row(r.batchID, 1, 1).State; got != pods.StateReady {
		t.Fatalf("not ready after the new boot report: %s", got)
	}
	// the second restart ends the pod
	r.agent(1, state("booting"))
	r.tick()
	row = r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminating || row.ErrorCode != "RESTART_LOOP" || row.Restarts != 2 || r.api.nDeletes() != 1 {
		t.Errorf("after two restarts: %s code %s restarts %d deletes %d", row.State, row.ErrorCode, row.Restarts, r.api.nDeletes())
	}
}

// quiet keeps the pod alive with busy polls for d, then stops polling.
func (r *lrig) quiet(d time.Duration) {
	for end := r.clk.Now().Add(d); r.clk.Now().Before(end); {
		r.clk.Advance(60 * time.Second)
		r.agent(1, state("busy"))
		r.tick()
	}
}

func TestReconcilePods_RecreateOnce(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.walkToReady()
	r.step(200 * time.Second) // no poll for 200 s: orphaned
	r.step(10 * time.Second)  // gone
	first := r.row(r.batchID, 1, 1)
	if first.State != pods.StateTerminated {
		t.Fatalf("first pod %s", first.State)
	}
	r.tick()
	if r.api.nCreates() != 2 {
		t.Fatalf("creates %d, want the one re-creation", r.api.nCreates())
	}
	second := r.row(r.batchID, 2, 1)
	if second.State != pods.StateBooting || second.N != 2 || !second.DeadlineAt.Equal(first.DeadlineAt) {
		t.Errorf("second pod %s n=%d deadline %s (first %s)", second.State, second.N, second.DeadlineAt, first.DeadlineAt)
	}
	// each pod has its own overhead reserve
	if got := r.reserved(r.batchID); got != 2*163750 {
		t.Errorf("reserved %d, want two overheads", got)
	}
	// the second pod is lost too: no third
	r.api.setStatus(2, pods.StatusRunning)
	r.step(1400 * time.Second) // boot timeout
	for range 4 {
		r.step(70 * time.Second)
	}
	if r.api.nCreates() != 2 {
		t.Errorf("creates %d: a third pod was created", r.api.nCreates())
	}
	b := pods.NewBackend(pods.BackendOptions{Name: lBackend, Store: r.st, Queue: r.q, Clock: r.clk, KillSlackS: 15})
	if _, err := b.Submit(context.Background(), r.spec(r.take(r.batchID), 186)); err == nil {
		t.Error("a take submitted after the re-creation was used was accepted")
	}
}

func TestReconcilePods_RecreateRefusedWithoutTime(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	r.walkToReady()
	r.quiet(1000 * time.Second) // the pod lives on busy polls, then goes silent
	r.step(200 * time.Second)
	r.step(10 * time.Second)
	if got := r.row(r.batchID, 1, 1); got.State != pods.StateTerminated {
		t.Fatalf("first pod %s", got.State)
	}
	r.tick()
	if r.api.nCreates() != 1 {
		t.Errorf("creates %d: a pod was created without the time to boot and render", r.api.nCreates())
	}
	if got := r.row(r.batchID, 1, 1).ErrorCode; got != "POD_DEADLINE" {
		t.Errorf("error code %q, want POD_DEADLINE", got)
	}
	if got := r.str(`SELECT status FROM pod_assignments WHERE attempt_id = ?`, att); got != "cancelled" {
		t.Errorf("waiting take %q", got)
	}
}

func TestReconcilePods_CancelledBatchNeverRecreates(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	r.walkToReady()
	r.step(200 * time.Second) // orphaned
	// the batch is cancelled: its unhanded take is closed, its jobs end, the batch finishes
	r.q.Drop(att)
	if _, err := pods.CloseAssignment(context.Background(), r.st.W, att, pods.AssignCancelled, "", r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.endJobs()
	r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now()), r.batchID)
	r.step(10 * time.Second)
	r.step(time.Hour)
	if r.api.nCreates() != 1 {
		t.Errorf("creates %d after the batch was cancelled", r.api.nCreates())
	}
}

func TestReconcilePods_OverrunTrip(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	row := r.walkToReady()
	r.api.keepAlive = true // the DELETE does not work
	r.quiet(row.DeadlineAt.Sub(r.clk.Now()) - 100*time.Second)
	for r.clk.Now().Before(row.DeadlineAt.Add(290 * time.Second)) {
		r.step(10 * time.Second)
	}
	if got := r.tripped(); len(got) != 0 && got[0] == "pod_overrun" {
		t.Fatalf("tripped before deadline + 300 s: %v", got)
	}
	r.step(20 * time.Second)
	found := false
	for _, x := range r.tripped() {
		found = found || x == "pod_overrun"
	}
	if !found {
		t.Errorf("trips %v, want pod_overrun", r.tripped())
	}
}

func TestReconcilePods_DeadmanTrip(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.walkToReady()
	r.api.keepAlive = true
	r.step(200 * time.Second) // orphaned; the DELETE is sent and does not work
	last := r.row(r.batchID, 1, 1).LastPollAt
	for r.clk.Now().Before(last.Add(10*time.Minute + 50*time.Second)) {
		r.step(10 * time.Second)
	}
	for _, x := range r.tripped() {
		if x == "pod_deadman" {
			t.Fatalf("tripped before deadman + 60 s: %v", r.tripped())
		}
	}
	r.step(15 * time.Second)
	found := false
	for _, x := range r.tripped() {
		found = found || x == "pod_deadman"
	}
	if !found {
		t.Errorf("trips %v, want pod_deadman", r.tripped())
	}
}

// ---- settlement -------------------------------------------------------------------------------------------------------------

// finish is what the runner does once the meter is ready: FinishBatch with the metered overhead.
func (r *lrig) finish() {
	r.t.Helper()
	secs, ready, err := pods.Meter{}.OverheadSeconds(context.Background(), r.st.R, r.batchID)
	if err != nil || !ready {
		r.t.Fatalf("meter %d %v %v", secs, ready, err)
	}
	err = r.sp.RunTx(context.Background(), func(tx *sql.Tx) error {
		_, err := r.sp.FinishBatch(context.Background(), tx, spend.FinishInput{BatchID: r.batchID, OverheadMicro: money.ForSeconds(secs, 300000),
			Lease: store.LeaseFence{Owner: "rig", Now: r.clk.Now()}})
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *lrig) settled(kind, reason string) int64 {
	var v int64
	if err := r.st.R.QueryRow(`SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = ? AND reason = ?`, r.batchID, kind, reason).Scan(&v); err != nil {
		r.t.Fatal(err)
	}
	return v
}

func TestReconcilePods_SettlesOverheadAndExcess(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	att := r.queue()
	r.walkToReady()
	w := r.agent(1, idle())
	if noTake(decodeResp(t, w)) {
		t.Fatal("no take")
	}
	r.drop()
	// the pod runs one take for 100 s and the pod lives 1 h: 3600 s of lifetime
	r.step(100 * time.Second)
	r.agent(1, idle().with("done", done(att, "ok", 99000)))
	r.endJobs()
	r.tick() // all jobs terminal: the pod is stopped
	r.step(61 * time.Second)
	r.step(10 * time.Second)
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated {
		t.Fatalf("pod %s", row.State)
	}
	life := int64(*row.LifetimeS)
	if life%60 != 0 || life < 60 {
		t.Fatalf("lifetime %d", life)
	}
	// not finished yet: nothing settles
	r.tick()
	if got := r.settled("settle", "excess"); got != 0 || len(r.tripped()) != 0 {
		t.Fatalf("settled %d before the batch finished (trips %v)", got, r.tripped())
	}
	r.finish()
	pot := int64(163750)
	overhead := r.settled("settle", "overhead")
	secs, _, _ := pods.Meter{}.OverheadSeconds(context.Background(), r.st.R, r.batchID)
	if want := min(int64(money.ForSeconds(secs, 300000)), pot); overhead != want {
		t.Fatalf("overhead settle %d, want min(F(%d s), pot) = %d", overhead, secs, want)
	}
	r.tick()
	wantExcess := max(int64(money.ForSeconds(life, 300000))-overhead, 0)
	if got := r.settled("settle", "excess"); got != wantExcess {
		t.Errorf("excess %d, want F(%d s) - %d = %d", got, life, overhead, wantExcess)
	}
	tr := r.tripped()
	if (wantExcess > 0) != (len(tr) == 1 && tr[0] == "pod_excess") {
		t.Errorf("trips %v for an excess of %d", tr, wantExcess)
	}
	if r.count(`SELECT COUNT(*) FROM pods WHERE batch_id = ? AND settled = 1`, r.batchID) != 1 {
		t.Error("the pod is not marked settled")
	}
	// settled once
	for range 3 {
		r.step(time.Minute)
	}
	if got := r.settled("settle", "excess"); got != wantExcess || len(r.tripped()) != len(tr) {
		t.Errorf("settled again: %d, trips %v", got, r.tripped())
	}
}

// settleExec settles the attempt the way the runner does: F(execution_ms) as reason "execution".
func (r *lrig) settleExec(att string, ms int64) {
	r.t.Helper()
	job := r.str(`SELECT job_id FROM job_attempts WHERE id = ?`, att)
	err := r.sp.RunTx(context.Background(), func(tx *sql.Tx) error {
		_, err := r.sp.SettleAttempt(context.Background(), tx, spend.AttemptSettle{BatchID: r.batchID, JobID: job, AttemptID: att, PrincipalID: "human:alice",
			Basis: spend.BasisMeasured, ExecutionMS: &ms, PricePPH: 300000, Lease: store.LeaseFence{Owner: "rig", Now: r.clk.Now()}})
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

// A healthy batch whose executions are not whole seconds settles F(ms) per take and F(life - sum of ceil seconds) as overhead; that is
// short of F(life) by rounding only, which must not be an excess nor a trip.
func TestReconcilePods_FractionalExecutionsAreNoExcess(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	a1 := r.queue()
	a2 := r.queue()
	r.walkToReady()
	if noTake(decodeResp(t, r.agent(1, idle()))) {
		t.Fatal("no take")
	}
	r.step(1500 * time.Millisecond)
	if noTake(decodeResp(t, r.agent(1, idle().with("done", done(a1, "ok", 1500))))) { // the done poll hands the next take
		t.Fatal("no second take")
	}
	r.settleExec(a1, 1500)
	r.step(99700 * time.Millisecond)
	r.agent(1, idle().with("done", done(a2, "ok", 99700)))
	r.settleExec(a2, 99700)
	r.endJobs()
	r.tick()
	r.step(61 * time.Second)
	r.step(10 * time.Second)
	if row := r.row(r.batchID, 1, 1); row.State != pods.StateTerminated {
		t.Fatalf("pod %s", row.State)
	}
	r.finish()
	r.tick()
	// (the rig's tiny execution pot makes the takes' own excess rows; the pod excess is the batch-level row, which has no job)
	if got := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'excess' AND job_id IS NULL`, r.batchID); got != 0 {
		t.Errorf("%d batch-level excess rows for a healthy batch", got)
	}
	if tr := r.tripped(); len(tr) != 0 {
		t.Errorf("trips %v", tr)
	}
	if r.count(`SELECT COUNT(*) FROM pods WHERE batch_id = ? AND settled = 1`, r.batchID) != 1 {
		t.Error("the pod is not marked settled")
	}
}

func TestReconcilePods_SettlesExcessWhenPotIsShort(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.queue()
	r.walkToReady()
	r.drop()
	r.endJobs()
	// the pod sits for a long time (the stop is answered but nothing deletes it for a while): a lifetime far beyond the pot
	r.api.keepAlive = true
	r.step(10 * time.Second)
	r.step(70 * time.Second)
	r.clk.Advance(2 * time.Hour)
	r.api.keepAlive = false
	r.tick()
	r.tick()
	row := r.row(r.batchID, 1, 1)
	if row.State != pods.StateTerminated || *row.LifetimeS < 7200 {
		t.Fatalf("pod %s life %v", row.State, row.LifetimeS)
	}
	r.finish()
	r.tick()
	life := int64(*row.LifetimeS)
	want := int64(money.ForSeconds(life, 300000)) - r.settled("settle", "overhead")
	if want <= 0 {
		t.Fatalf("scenario has no excess (%d)", want)
	}
	if got := r.settled("settle", "excess"); got != want {
		t.Errorf("excess %d, want %d", got, want)
	}
	if tr := r.tripped(); len(tr) == 0 || tr[len(tr)-1] != "pod_excess" {
		t.Errorf("trips %v", tr)
	}
	if got := r.reserved(r.batchID); got != 163750 {
		t.Errorf("excess is never reserved: reserved %d", got)
	}
}

// ---- windows ----------------------------------------------------------------------------------------------------------------

func TestReconcilePods_AutoDisarmWindow(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	// the batch is active, so the window is not idle; the pod is created late in the window and its own deadline is past the window's end
	r.step(7*time.Hour + 20*time.Minute)
	if len(r.disarms) != 0 {
		t.Fatalf("disarmed early: %v", r.disarms)
	}
	for range 10 { // ten takes: the deadline is 1200 + 1860 + 186 + 120 + 300 s after the create, past the end of the window
		r.queue()
	}
	r.walkToReady() // takes stay queued, so the pod is never idle
	r.quiet(35 * time.Minute)
	if len(r.disarms) != 0 || r.row(r.batchID, 1, 1).State != pods.StateReady {
		t.Fatalf("before the end of the window: disarms %v state %s", r.disarms, r.row(r.batchID, 1, 1).State)
	}
	r.quiet(10 * time.Minute)
	if len(r.disarms) != 1 || r.disarms[0] != "window_ended" {
		t.Fatalf("disarms %v", r.disarms)
	}
	row := r.row(r.batchID, 1, 1)
	if (row.State != pods.StateTerminating && row.State != pods.StateTerminated) || row.ErrorCode != "WINDOW_ENDED" {
		t.Errorf("the pod outlived the window: %s %q", row.State, row.ErrorCode)
	}
	if row.TerminatedAt != nil && row.TerminatedAt.Sub(rigNow) > 8*time.Hour+3*time.Minute {
		t.Errorf("the pod ended at %s, long after the window", row.TerminatedAt.Sub(rigNow))
	}
	// armed is false now: no more disarms, and no pod is created
	r.step(time.Minute)
	if len(r.disarms) != 1 {
		t.Errorf("disarms %v", r.disarms)
	}
}

func TestReconcilePods_WindowEndRetriesAFailedStop(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.step(7*time.Hour + 20*time.Minute)
	for range 10 {
		r.queue()
	}
	r.walkToReady()
	r.api.mu.Lock()
	r.api.deleteErr = errors.New("api down")
	r.api.mu.Unlock()
	for end := r.clk.Now().Add(50 * time.Minute); r.clk.Now().Before(end); { // past the end of the window, and every stop fails
		r.clk.Advance(60 * time.Second)
		r.agent(1, state("busy"))
		_ = r.l.Tick(context.Background()) // the failed stop is an error of the pass
	}
	if len(r.disarms) != 0 {
		t.Fatalf("disarmed with a live pod: %v", r.disarms)
	}
	r.api.mu.Lock()
	r.api.deleteErr = nil
	r.api.mu.Unlock()
	r.quiet(10 * time.Minute)
	row := r.row(r.batchID, 1, 1)
	if row.ErrorCode != "WINDOW_ENDED" || (row.State != pods.StateTerminating && row.State != pods.StateTerminated) {
		t.Fatalf("the pod outlived the window: %s %q", row.State, row.ErrorCode)
	}
	r.step(time.Minute)
	r.step(time.Minute)
	if len(r.disarms) != 1 || r.disarms[0] != "window_ended" {
		t.Errorf("disarms %v", r.disarms)
	}
}

func TestReconcilePods_AutoDisarmIdle(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	// a pod batch is active: an hour of nothing is not idle
	r.step(time.Hour)
	if len(r.disarms) != 0 {
		t.Fatalf("disarmed with an active batch: %v", r.disarms)
	}
	r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now()), r.batchID)
	r.step(29 * time.Minute)
	if len(r.disarms) != 0 {
		t.Fatalf("disarmed after 29 minutes: %v", r.disarms)
	}
	r.step(2 * time.Minute)
	if len(r.disarms) != 1 || r.disarms[0] != "window_idle" {
		t.Fatalf("disarms %v", r.disarms)
	}
	if got := r.str(`SELECT changed_by || '|' || reason FROM provider_state WHERE backend = ?`, lBackend); got != "system:bulk|window_idle" {
		t.Errorf("state row %q", got)
	}
}

func TestReconcilePods_IdleClockStartsAtArming(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(rigNow.Add(-5*time.Hour)), r.batchID)
	r.step(20 * time.Minute) // the batch ended long ago, but the window was armed 20 minutes ago
	if len(r.disarms) != 0 {
		t.Errorf("disarmed %v 20 minutes after arming", r.disarms)
	}
	r.step(11 * time.Minute)
	if len(r.disarms) != 1 {
		t.Errorf("disarms %v", r.disarms)
	}
}

func TestPodMeter_ReadyOnlyWhenTerminated(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	m := pods.Meter{}
	secs, ready, err := m.OverheadSeconds(context.Background(), r.st.R, r.batchID)
	if err != nil || !ready || secs != 0 {
		t.Fatalf("no pod: %d %v %v", secs, ready, err)
	}
	r.queue()
	r.tick()
	if _, ready, _ = m.OverheadSeconds(context.Background(), r.st.R, r.batchID); ready {
		t.Fatal("ready with a live pod")
	}
	r.drop() // no take is waiting, so no replacement pod is created
	r.api.vanish(1)
	r.step(10 * time.Second)
	secs, ready, err = m.OverheadSeconds(context.Background(), r.st.R, r.batchID)
	if err != nil || !ready || secs != 60 {
		t.Errorf("terminated pod: %d %v %v", secs, ready, err)
	}
	// executions handed to the pod are subtracted, rounded up per take, floored at zero
	pod := r.row(r.batchID, 1, 1)
	att := r.take(r.batchID)
	r.exec(`UPDATE pod_assignments SET pod_id = ?, handed_at = ? WHERE attempt_id = ?`, pod.ID, store.FormatTime(rigNow), att)
	r.exec(`UPDATE pod_assignments SET done_at = ?, status = 'ok', execution_ms = 20001 WHERE attempt_id = ?`, store.FormatTime(rigNow.Add(21*time.Second)), att)
	if secs, _, _ = m.OverheadSeconds(context.Background(), r.st.R, r.batchID); secs != 60-21 {
		t.Errorf("overhead %d, want 39", secs)
	}
	att2 := r.take(r.batchID)
	r.exec(`UPDATE pod_assignments SET pod_id = ?, handed_at = ? WHERE attempt_id = ?`, pod.ID, store.FormatTime(rigNow), att2)
	r.exec(`UPDATE pod_assignments SET done_at = ?, status = 'ok', execution_ms = 500000 WHERE attempt_id = ?`, store.FormatTime(rigNow.Add(time.Hour)), att2)
	if secs, _, _ = m.OverheadSeconds(context.Background(), r.st.R, r.batchID); secs != 0 {
		t.Errorf("overhead %d, want the floor 0", secs)
	}
}
