package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The pod chaos tests (BULK-S1, C399.13): one scripted misfortune each, on the world of the pod property suite, with its invariants
// (podWorld.check) run after every round.

// podChaos builds a world with one pod batch of n takes (style carries the pod markers) and a runner holding the lease.
func podChaos(t *testing.T, n int, marker string, simOpts ...func(*fake.PodSimOptions)) (*propEnv, *podWorld, seeded) {
	t.Helper()
	e, w := newPodEnv(t, 7, "within", simOpts...)
	e.setCaps(spend.Caps{MonthlyMicro: 200_000_000, BatchMicro: 200_000_000, HumanDailyMicro: 200_000_000, AgentDailyMicro: 200_000_000, AutoApproveTakes: 100, AutoApproveMicro: 200_000_000})
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.pass()
	b := e.r.seedBatch(batchSpec{jobs: n, timeoutS: 120, ceiling: 20_000_000, recipe: e.recipeFor(marker), backend: provider.BackendFakePod, workersMax: 1})
	e.created++
	return e, w, b
}

// podRounds runs rounds of step seconds until done or the limit; it checks the invariants after each and returns the rounds run.
func (w *podWorld) podRounds(limit int, step time.Duration, done func() bool) int {
	w.e.t.Helper()
	for i := range limit {
		if done() {
			return i
		}
		w.advance(step, false)
		if w.round(i%3 == 0) {
			w.e.setCrash("")
			w.e.takeOver()
		}
		w.check()
	}
	if !done() {
		w.e.t.Fatalf("the condition never held after %d rounds\n%s\nPODS %s\nASSIGN %s\nATT %s\nTRIPS %s", limit, w.e.dumpStuck(),
			w.e.r.str(`SELECT group_concat(id||':'||state||':'||COALESCE(error_code,'')||':'||settled, ' ; ') FROM pods`),
			w.e.r.str(`SELECT group_concat(status||':'||COALESCE(error_code,'')||':'||COALESCE(pod_id,'-'), ' ; ') FROM pod_assignments`),
			w.e.r.str(`SELECT group_concat(state||':'||COALESCE(ref,''), ' ; ') FROM job_attempts`), w.e.r.str(`SELECT COALESCE(group_concat(reason||':'||COALESCE(reset_at,'open')), '') FROM provider_trips`))
	}
	return limit
}

func (w *podWorld) settledAndGone() bool {
	return w.e.allTerminal() && w.sim.Live() == 0 && w.e.r.count(`SELECT COUNT(*) FROM pods WHERE state <> 'terminated' OR settled = 0`) == 0
}

// TestChaos_PodCrashBetweenPostAndPersist: the create request reaches RunPod and the pod exists, but the app never learns the outcome (a
// timeout or a crash between the POST and persisting the reply). The create is never replayed: the pod is found by its name and deleted,
// the one allowed replacement carries the batch, and each of the two pods reserved its overhead once.
func TestChaos_PodCrashBetweenPostAndPersist(t *testing.T) {
	t.Parallel()
	e, w, b := podChaos(t, 4, "")
	w.sim.FailNextCreate(errors.New("context deadline exceeded"), true)
	w.podRounds(400, 30*time.Second, w.settledAndGone)
	ps := w.sim.Pods()
	if len(ps) != 2 {
		t.Fatalf("pods = %+v: want the unconfirmed pod and one replacement", ps)
	}
	if ps[0].DeletedBy != "api" || ps[0].Takes != 0 {
		t.Errorf("the unconfirmed pod %+v must be deleted by name without ever serving a take", ps[0])
	}
	if got := e.r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'reserve' AND reason = 'pod_overhead'`, b.Batch); got != 2 {
		t.Errorf("%d overhead reserves for two pods", got)
	}
	for _, j := range b.Jobs {
		if s := e.r.state(j); s != StateCompleted {
			t.Errorf("job %s ended %s", j, s)
		}
	}
	if w.sim.Calls("create") != 2 {
		t.Errorf("%d creates: a create is never replayed", w.sim.Calls("create"))
	}
}

// TestChaos_PodSilentOrphaned: the agent dies when it is handed a take (its watchdog lives on). The app stops hearing from the pod, gives it
// up after the orphan window and deletes it, loses the take, and the replacement pod takes the work. The dead pod's bill is bounded by the
// orphan window, not by the dead-man.
func TestChaos_PodSilentOrphaned(t *testing.T) {
	t.Parallel()
	e, w, b := podChaos(t, 3, fake.MarkPodCrashMidTake)
	crashed := time.Time{}
	w.podRounds(300, 10*time.Second, func() bool {
		if crashed.IsZero() {
			for _, p := range w.sim.Pods() {
				if p.AgentDead {
					crashed = e.r.clk.Now()
				}
			}
		}
		return !crashed.IsZero()
	})
	w.podRounds(300, 10*time.Second, func() bool { return w.sim.Pods()[0].Deleted != nil })
	first := w.sim.Pods()[0]
	if first.DeletedBy != "api" {
		t.Errorf("the silent pod ended by %q, want deleted by the app", first.DeletedBy)
	}
	if d := first.Deleted.Sub(crashed); d > 4*time.Minute {
		t.Errorf("the app deleted the silent pod %s after its agent died, the orphan window is 3 minutes", d)
	}
	if got := e.r.count(`SELECT COUNT(*) FROM pods WHERE error_code = 'WORKER_LOST'`); got == 0 {
		t.Error("no pod row says WORKER_LOST")
	}
	w.podRounds(900, 30*time.Second, w.settledAndGone)
	if n := len(w.sim.Pods()); n > 2 {
		t.Errorf("%d pods: one replacement at most", n)
	}
	// every take of the batch ended: the marker kills the replacement's agent too, so the jobs fail rather than hang
	for _, j := range b.Jobs {
		if s := e.r.state(j); s != StateCompleted && s != StateFailed {
			t.Errorf("job %s ended %s", j, s)
		}
	}
}

// TestChaos_PodKeyRemovedMidRun: the pod key file disappears while a pod serves a batch. The app can neither stop nor delete anything (no
// create, no delete is attempted: the key is the only credential), yet the pod keeps its own deadline: it ends itself at the batch's absolute
// deadline, and when the key returns the app finds it gone and settles once, the lifetime beyond the reserved pot as an excess.
func TestChaos_PodKeyRemovedMidRun(t *testing.T) {
	t.Parallel()
	e, w, b := podChaos(t, 4, "")
	w.podRounds(300, 10*time.Second, func() bool { ps := w.sim.Pods(); return len(ps) == 1 && ps[0].Takes >= 1 })
	w.keyOn.Store(false)
	w.faulty = true
	creates, deletes := w.sim.Calls("create"), w.sim.Calls("delete")
	var dl time.Time
	var raw string
	if err := e.r.st.R.QueryRow(`SELECT deadline_at FROM pods`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	dl = podParseTime(t, raw)
	for e.r.clk.Now().Before(dl.Add(10 * time.Minute)) {
		w.advance(time.Minute, false)
		w.round(false)
		w.check()
	}
	if w.sim.Calls("create") != creates || w.sim.Calls("delete") != deletes {
		t.Errorf("the app called RunPod without its key: %d creates, %d deletes", w.sim.Calls("create")-creates, w.sim.Calls("delete")-deletes)
	}
	ps := w.sim.Pods()
	if len(ps) != 1 || ps[0].Deleted == nil || ps[0].DeletedBy != "deadline" {
		t.Fatalf("pods = %+v: the pod must end itself at the batch deadline", ps)
	}
	if ps[0].Deleted.After(dl.Add(time.Minute)) {
		t.Errorf("the pod lived until %s, past the deadline %s", ps[0].Deleted, dl)
	}
	if got := e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'pod_overrun'`); got != 0 {
		t.Errorf("%d overrun trips for a pod that ended at its deadline", got)
	}
	w.heal()
	// the app now learns the pod is gone; it was not confirmed within the overrun grace, so it opens pod_overrun and holds the queue
	// until the operator resets the trip (podRounds with the operator's reset in between)
	w.podRounds(300, 30*time.Second, func() bool {
		if e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reset_at IS NULL`) > 0 {
			w.resetTrips()
			w.arm()
		}
		return w.settledAndGone()
	})
	if got := e.r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'excess'`, b.Batch); got > 1 {
		t.Errorf("%d excess rows: a batch settles once", got)
	}
	billed, settled := int64(w.sim.Billed()), int64(0)
	settled, _ = settledAll(e.r.st.R)
	if billed > settled+int64(len(ps))*2*int64(podSimCost)/60 {
		t.Errorf("the pod billed %d but %d was settled", billed, settled)
	}
}

func podParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02T15:04:05.000Z", s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestReconciler_For: the pod lifecycle reaches its backend's trip through the reconciler that serves that backend, whether it is the
// first of the process or a peer; an unknown name or no reconciler at all is nil.
func TestReconciler_For(t *testing.T) {
	t.Parallel()
	be := func(name string) *Reconciler { return &Reconciler{o: ReconcilerOptions{Backend: providerNamed(name)}} }
	first, peer := be("fake"), be("fake-pod")
	first.peers = []*Reconciler{peer}
	if first.For("fake") != first || first.For("fake-pod") != peer || first.For("runpod-pod") != nil {
		t.Error("For does not find the reconciler of each backend")
	}
	if var0 := (*Reconciler)(nil); var0.For("fake") != nil {
		t.Error("a nil reconciler serves no backend")
	}
	if (&Reconciler{}).For("fake") != nil {
		t.Error("a reconciler with no backend serves none")
	}
}

type namedBackend struct {
	provider.Backend
	name string
}

func (n namedBackend) Name() string { return n.name }

func providerNamed(name string) provider.Backend { return namedBackend{name: name} }
