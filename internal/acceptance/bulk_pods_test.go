package acceptance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// podMoney checks the money invariants every finished pod batch keeps (C393, C399.13): the overhead of each pod was reserved exactly once
// and the pod settled once, no reserve is left unaccounted for, nothing was settled that was not billed, and the sum of what was settled
// stays inside what was reserved plus the explicit excess.
func podMoney(t *testing.T, r *servertest.Rig, batch string) {
	t.Helper()
	nPods := scalar(t, r, `SELECT COUNT(*) FROM pods WHERE batch_id = ? AND overhead_reserved_micro_usd > 0`, batch)
	if got := scalar(t, r, `SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'reserve' AND reason = 'pod_overhead'`, batch); got != nPods {
		t.Errorf("%s pod overhead reserves for %s pods: a pod's overhead is reserved exactly once", got, nPods)
	}
	if got, want := ledgerSum(t, r, batch, "reserve", "pod_overhead"), scalar(t, r, `SELECT COALESCE(SUM(overhead_reserved_micro_usd), 0) FROM pods WHERE batch_id = ?`, batch); scalar(t, r, `SELECT ?`, got) != want {
		t.Errorf("overhead reserved %d, the pod rows say %s", got, want)
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM pods WHERE batch_id = ? AND settled = 0`, batch); got != "0" {
		t.Errorf("%s pods of the batch are unsettled", got)
	}
	reserved := ledgerSum(t, r, batch, "reserve", "")
	settled := ledgerSum(t, r, batch, "settle", "")
	released := ledgerSum(t, r, batch, "release", "") + ledgerSum(t, r, batch, "residual", "")
	excess := ledgerSum(t, r, batch, "settle", "excess")
	if reserved+excess != settled+released {
		t.Errorf("reserved %d + excess %d does not balance settled %d + released/held %d", reserved, excess, settled, released)
	}
	if billed := int64(r.Pods.Billed()); settled < billed-int64(len(r.Pods.Pods())) {
		t.Errorf("settled %d is below what the pods billed (%d)", settled, billed)
	}
}

// TestAcceptance_Bulk_PodBatch: 30 takes end to end on fake-pod. One pod is made, handed every take over the signed poll route, renders
// and uploads through the real upload handlers, and ends itself on the stop answer; every job completes with its track, the pod overhead is
// reserved once and settled once, nothing is left reserved, and no trip opened.
func TestAcceptance_Bulk_PodBatch(t *testing.T) {
	t.Parallel()
	r := podRig(t, servertest.PodsOptions{})
	batch, jobs := podBatch(t, r, 30, "", "pod-batch")
	podPump(t, r, 5*time.Second, 800, podSettled(t, r, batch))
	for _, j := range jobs {
		if s, code := jobState(t, r, j); s != "completed" {
			t.Errorf("job %s %s %s", j, s, code)
		}
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND track_id IS NOT NULL`, batch); got != "30" {
		t.Errorf("%s tracks for 30 takes", got)
	}
	ps := r.Pods.Pods()
	if len(ps) != 1 || ps[0].Takes != 30 || ps[0].Deleted == nil || ps[0].DeletedBy != "stop" {
		t.Fatalf("pods = %+v: want one pod that took 30 takes and ended on the stop answer", ps)
	}
	if n := r.Pods.Calls("create"); n != 1 {
		t.Errorf("%d creates", n)
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM pod_assignments WHERE batch_id = ? AND status = 'ok' AND pod_id IS NOT NULL`, batch); got != "30" {
		t.Errorf("%s done assignments", got)
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM provider_trips`); got != "0" {
		t.Errorf("%s trips opened in a healthy batch", got)
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM pod_assignments WHERE batch_id = ? AND done_at IS NULL`, batch); got != "0" {
		t.Errorf("%s assignments left open", got)
	}
	podMoney(t, r, batch)
	if got := ledgerSum(t, r, batch, "settle", "excess"); got != 0 {
		t.Errorf("a healthy batch settled an excess of %d", got)
	}
}

// pumpPartitioned runs rounds that may fail (the app and RunPod cannot reach each other) until the instant is reached.
func pumpUntil(t *testing.T, r *servertest.Rig, step time.Duration, until time.Time, stop func() bool) {
	t.Helper()
	for r.Clock.Now().Before(until) && !stop() {
		r.Clock.Advance(step)
		_ = r.PodRoundErr(context.Background())
	}
}

func tripReasons(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	return scalar(t, r, `SELECT COALESCE(group_concat(reason), '') FROM provider_trips WHERE backend = 'fake-pod'`)
}

// TestAcceptance_Bulk_Deadman: the app goes away for 25 minutes (no lifecycle, no runner, no poll route: only the pod keeps running). The
// pod's own dead-man ends it after MM_DEADMAN_S without a successful poll, so its bill is bounded by that though nothing from outside
// could delete it. When the app returns it finds the pod gone, loses that pod's take and, with the batch still open, makes the one allowed
// replacement; each pod reserved its overhead once and settled once.
func TestAcceptance_Bulk_Deadman(t *testing.T) {
	t.Parallel()
	r := podRig(t, servertest.PodsOptions{})
	batch, jobs := podBatch(t, r, 30, "", "pod-deadman")
	podPump(t, r, 5*time.Second, 400, func() bool { ps := r.Pods.Pods(); return len(ps) == 1 && ps[0].Takes >= 3 })
	cut := r.Clock.Now()
	r.Pods.Partition(true) // the pod cannot reach the app: its polls fail
	for end := cut.Add(25 * time.Minute); r.Clock.Now().Before(end); {
		r.Clock.Advance(30 * time.Second)
		if err := r.Pods.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	first := r.Pods.Pods()[0]
	if first.Deleted == nil || first.DeletedBy != "deadman" {
		t.Fatalf("pod = %+v: it must end itself by its dead-man", first)
	}
	if d := first.Deleted.Sub(cut); d < 9*time.Minute || d > 11*time.Minute {
		t.Errorf("the dead-man ended the pod %s after the cut, want about 10 minutes", d)
	}
	r.Pods.Partition(false)
	podPump(t, r, 5*time.Second, 1500, podSettled(t, r, batch))
	ps := r.Pods.Pods()
	if len(ps) != 2 || ps[1].DeletedBy != "stop" {
		t.Fatalf("pods = %+v: want the dead one and one replacement that ended on stop", ps)
	}
	for _, j := range jobs {
		if s, _ := jobState(t, r, j); !terminalJob(s) {
			t.Errorf("job %s still %s", j, s)
		}
	}
	if got := scalar(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state = 'completed'`, batch); got == "0" {
		t.Error("nothing completed")
	}
	podMoney(t, r, batch)
}

// TestAcceptance_Bulk_AbsoluteDeadline: a pod that cannot end itself (no_self_terminate) and cannot be reached (RunPod is cut) outlives the
// batch's absolute deadline. The app trips pod_overrun at deadline + the grace, the overhead it reserved for the pod is the most it can
// have reserved, and when the operator removes the pod the lifetime beyond it is settled once as an excess with pod_excess.
func TestAcceptance_Bulk_AbsoluteDeadline(t *testing.T) {
	t.Parallel()
	r := podRig(t, servertest.PodsOptions{})
	batch, jobs := podBatch(t, r, 30, "fake:slow=300 fake:pod_no_self_terminate", "pod-deadline")
	podPump(t, r, 5*time.Second, 400, func() bool { ps := r.Pods.Pods(); return len(ps) == 1 && ps[0].Takes >= 1 })
	deadline, err := time.Parse("2006-01-02T15:04:05.000Z", scalar(t, r, `SELECT deadline_at FROM pods WHERE batch_id = ?`, batch))
	if err != nil {
		t.Fatal(err)
	}
	r.Pods.Partition(true)
	pumpUntil(t, r, 60*time.Second, deadline.Add(2*time.Hour), func() bool { return strings.Contains(tripReasons(t, r), "pod_overrun") })
	if !strings.Contains(tripReasons(t, r), "pod_overrun") {
		t.Fatalf("trips = %q, want pod_overrun", tripReasons(t, r))
	}
	if now := r.Clock.Now(); now.Before(deadline) || now.After(deadline.Add(15*time.Minute)) {
		t.Errorf("pod_overrun opened at %s, the deadline is %s", now.Sub(deadline), deadline)
	}
	if ps := r.Pods.Pods(); len(ps) != 1 || ps[0].Deleted != nil {
		t.Fatalf("pods = %+v: the pod that cannot end itself is still billing", ps)
	}
	// up to the overrun trip the pod billed no more than the batch had reserved (the takes' reserves and the pod's overhead)
	if billed, reserved := int64(r.Pods.Billed()), ledgerSum(t, r, batch, "reserve", ""); billed > reserved {
		t.Errorf("at the overrun trip the pod billed %d, more than the %d reserved", billed, reserved)
	}
	// the operator deletes the pod from the console and the network returns
	for _, p := range r.Pods.Pods() {
		r.Pods.Kill(p.ID)
	}
	r.Pods.Partition(false)
	podPump(t, r, 30*time.Second, 100, func() bool { return scalar(t, r, `SELECT COUNT(*) FROM pods WHERE state <> 'terminated'`) == "0" })
	// the open trip holds the batch's queued takes until the operator acts: the owner cancels the batch
	r.Human(t).OK(t, "POST", "/api/batches/"+batch+"/cancel", map[string]any{"reason": "pod overrun"})
	podPump(t, r, 30*time.Second, 600, podSettled(t, r, batch))
	if got := scalar(t, r, `SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'excess'`, batch); got != "1" {
		t.Errorf("%s excess settle rows: the lifetime beyond the pot is settled once", got)
	}
	// a trip is one open row per backend: pod_excess has nothing to add to the open pod_overrun, and the backend stays closed
	if got := scalar(t, r, `SELECT COUNT(*) FROM provider_trips WHERE backend = 'fake-pod' AND reset_at IS NULL`); got != "1" {
		t.Errorf("%s open trips, want the one pod_overrun", got)
	}
	if n := r.Pods.Calls("create"); n != 1 {
		t.Errorf("%d creates: a tripped backend makes no pod", n)
	}
	for _, j := range jobs {
		if s, _ := jobState(t, r, j); !terminalJob(s) {
			t.Errorf("job %s still %s", j, s)
		}
	}
	podMoney(t, r, batch)
}
