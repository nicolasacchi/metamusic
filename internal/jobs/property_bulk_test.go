package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The pod half of the harness (BULK-S1, C399.13). A timeline is a seeded life of the pod backend: the real pods.Backend, poll handler and
// lifecycle loop over a fake.PodSim whose simulated agents poll the real handler and upload through the real upload handlers; the runner,
// the reconciler, the spend service and the invariant model of the property suite are the production ones. Every instant is the rig's
// manual clock and every agent acts in a call of PodSim.Tick, so a failing seed replays exactly.

const (
	podWorldImage  = "ghcr.io/example/mm-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	podWorldGPU    = "NVIDIA GeForce RTX 4090"
	podWorldWindow = 3 * time.Hour // MM_POD_WINDOW_MAX of the property timelines (production: 8 h)
	// podWorldKill is the longest a pod may live once the lifecycle can act on it: the stop answer, then (for a pod that cannot end itself)
	// the delete 60 s later, with up to one two-minute step of the timeline in between.
	podWorldKill = 4 * time.Minute
	podSimCost   = money.MicroUSD(340_000)
)

// podMeterAdapter lets the pod meter (which names its own Querier) serve the runner's BatchMeter.
type podMeterAdapter struct{ m pods.Meter }

func (a podMeterAdapter) OverheadSeconds(ctx context.Context, x Querier, batchID string) (int64, bool, error) {
	return a.m.OverheadSeconds(ctx, x, batchID)
}

// podWorld is the pod backend of one timeline.
type podWorld struct {
	e     *propEnv
	sim   *fake.PodSim
	q     *pods.Queue
	be    *pods.Backend
	lc    *pods.Lifecycle
	gate  *provider.Gate
	keyOn atomic.Bool

	cut       bool // the network is cut by a fault of the timeline (the pods cannot reach the app)
	tripSeen  bool
	createsAt int
	faulty    bool                 // the key is removed or the network is cut
	healedAt  time.Time            // when the last fault ended
	service   map[string]time.Time // pod or window -> when the lifecycle could first act on its end
}

// newPodEnv builds a timeline's world on the pod backend (profile "within" or "adversarial").
func newPodEnv(t *testing.T, seed uint64, profile string, simOpts ...func(*fake.PodSimOptions)) (*propEnv, *podWorld) {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy: runs a full timeline on the fake pod backend")
	}
	e := &propEnv{t: t, seed: seed, profile: profile, rng: rand.New(rand.NewPCG(seed, 0)), ctx: t.Context(), recipes: map[string]string{}}
	w := &podWorld{e: e, service: map[string]time.Time{}}
	w.keyOn.Store(true)
	w.q = pods.NewQueue()
	obs := func(u spend.Usage) { e.onCommit(u) }
	e.r = newRig(t, func(r *rig, o *RunnerOptions) {
		if r.clk == nil {
			return // newRigAt first runs the options on a probe rig
		}
		so := fake.PodSimOptions{Clock: r.clk, DefaultPrice: podSimCost,
			Worker: fake.NewSimWorker(fake.SimWorkerOptions{Uploader: fake.HandlerUploader{UploadHandler: r.up.Handler(), StartHandler: r.up.StartHandler()},
				Clock: r.clk, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()}),
			StyleOf: func(ctx context.Context, name string) string {
				var s string
				_ = r.st.R.QueryRowContext(ctx, `SELECT COALESCE(group_concat(rc.style, ' '), '') FROM pods p JOIN jobs j ON j.batch_id = p.batch_id JOIN recipes rc ON rc.id = j.recipe_id WHERE p.name = ?`, name).Scan(&s)
				return s
			}}
		for _, f := range simOpts {
			f(&so)
		}
		w.sim = fake.NewPodSim(so)
		w.be = pods.NewBackend(pods.BackendOptions{Name: provider.BackendFakePod, API: w.sim, Store: r.st, Queue: w.q, Clock: r.clk, Image: podWorldImage,
			GPUTypes: []string{podWorldGPU}, MaxRuntime: 15 * time.Minute, KillSlackS: 15, KeyPresent: w.keyOn.Load, BillingReadable: func(context.Context) bool { return false }})
		r.sp = spend.New(spend.Options{Store: r.st, Now: r.clk.Now, NewID: r.newID, ConfirmKey: []byte("test-key"), Location: time.UTC, Observer: obs})
		o.Spend, o.Backend, o.Meter = r.sp, w.be, podMeterAdapter{}
		o.Fault = func(point string) error { return e.fault(point) }
		e.be, e.bname = w.be, provider.BackendFakePod
	})
	q, err := w.be.Quote(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(q)
	e.r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?, 1, 'system:test', ?, ?, ?)`,
		provider.BackendFakePod, store.FormatTime(t0), string(raw), store.FormatTime(t0))
	e.cur = e.r.run
	e.runs = []*Runner{e.cur}
	e.owners = 1
	e.rec = e.newReconciler()
	w.gate = provider.NewGate(e.r.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: provider.BackendFakePod, KeysLoaded: true, MaxRuntime: 6 * time.Hour}, e.r.clk.Now)

	signer := pods.NewSigner([]byte("synthetic-pod-poll-key-0123456789ab"))
	w.lc = pods.NewLifecycle(pods.LifecycleOptions{
		Store: e.r.st, API: w.sim, Queue: w.q, Signer: signer, PublicURL: testPublicURL, Spend: e.r.sp, Clock: e.r.clk, Log: slog.New(slog.DiscardHandler), NewID: e.r.newID,
		Backend: provider.BackendFakePod, Image: podWorldImage, GPUTypes: []string{podWorldGPU}, DiskGB: 40, Weights3B: "8f24312f187763b9854adeec87e437e2b03bbff1",
		BootS: 1200, IdleS: 120, SlackS: 300, KillSlackS: 15, MaxRecreate: 1, MaxRuntime: 6 * time.Hour, Deadman: 10 * time.Minute, OrphanAfter: 180 * time.Second,
		WindowMax:  podWorldWindow,
		KeyPresent: w.keyOn.Load,
		Holding:    func() bool { return e.cur.Holding() },
		Fence:      func() store.LeaseFence { return e.cur.Fence() },
		State:      w.gate.State,
		Trip: func(ctx context.Context, reason string, details map[string]any) error {
			return e.rec.Trip(ctx, reason, details)
		},
		Disarm: func(ctx context.Context, _ string) error {
			_, err := e.r.st.W.ExecContext(ctx, `UPDATE provider_state SET armed = 0, changed_by = 'system:bulk', changed_at = ? WHERE backend = ?`, store.FormatTime(e.r.clk.Now()), provider.BackendFakePod)
			return err
		},
	})
	w.sim.Attach(pods.NewPoller(pods.PollOptions{Store: e.r.st, Signer: signer, Queue: w.q, Clock: e.r.clk, Log: slog.New(slog.DiscardHandler), KillSlackS: 15,
		Nudge: w.lc.Nudge, Hold: -1}).Handler())
	return e, w
}

// advance moves the clock by d. The agents are autonomous processes: they poll every few seconds whatever the app's loops do, so the
// clock moves in steps of five seconds with an agent tick in each, and the app's own loops (round) run once at the end. With away the
// app is gone for the whole span (a restart, a deploy): no poll reaches it.
func (w *podWorld) advance(d time.Duration, away bool) {
	if away {
		w.sim.Partition(true)
	}
	for left := d; left > 0; {
		step := min(left, 5*time.Second)
		w.e.r.clk.Advance(step)
		left -= step
		if err := w.sim.Tick(w.e.ctx); err != nil {
			w.e.failf("pod simulation: %v", err)
		}
	}
	if away {
		w.sim.Partition(w.cut)
	}
}

// round is one pass of the whole system at the current instant: the runner claims and submits, the reconciler refreshes, the lifecycle
// creates and watches pods, every agent polls once, and the lifecycle and the runner see what they reported. Errors the timeline may
// cause (a cut network, a removed key) are the lifecycle's to survive and are not failures.
func (w *podWorld) round(reconcile bool) (crashed bool) {
	e := w.e
	if e.step() {
		return true
	}
	if reconcile {
		e.pass()
	}
	_ = w.lc.Tick(e.ctx)
	if err := w.sim.Tick(e.ctx); err != nil {
		e.failf("pod simulation: %v", err)
	}
	_ = w.lc.Tick(e.ctx)
	return e.step()
}

// ---- batches -----------------------------------------------------------------------------------------------------------------------

// pickPodMarker is the style marker of a batch's takes. The within profile stays inside what the lifecycle recovers from without a trip.
func (w *podWorld) pickPodMarker(timeoutS int) string {
	e := w.e
	n := e.rng.IntN(100)
	switch {
	case n < 55:
		return ""
	case n < 70:
		return fmt.Sprintf("fake:slow=%d", 10+e.rng.IntN(max(timeoutS-30, 10)))
	case n < 78:
		return fmt.Sprintf("%s=%d", fake.MarkPodBootSlow, 100+e.rng.IntN(800))
	case n < 86:
		return fake.MarkPodRestart
	case n < 91:
		return fake.MarkPodCrashMidTake
	case n < 95:
		return fake.MarkPodSilent
	}
	if e.profile == "adversarial" {
		return fake.MarkPodNoSelfTerminate
	}
	return ""
}

func (w *podWorld) createBatch() {
	e := w.e
	n := 2 + e.rng.IntN(12)
	timeoutS := 60 + e.rng.IntN(120)
	principal := propPrincipals[e.rng.IntN(len(propPrincipals))]
	marker := w.pickPodMarker(timeoutS)
	e.r.seedBatch(batchSpec{jobs: n, principal: principal, timeoutS: timeoutS, ceiling: 20_000_000, recipe: e.recipeFor(marker), backend: provider.BackendFakePod, workersMax: 1})
	e.created++
}

// ---- the invariants ----------------------------------------------------------------------------------------------------------------

// check runs after every round: the pod invariants of C393 and C396 over the raw rows and the ground truth of the simulation.
func (w *podWorld) check() {
	e := w.e
	now := e.r.clk.Now()
	// the overhead of a pod is reserved exactly once, in the ledger and on the row
	if n := e.r.count(`SELECT COUNT(*) FROM (SELECT p.batch_id b, SUM(p.overhead_reserved_at IS NOT NULL) n, SUM(p.overhead_reserved_micro_usd) amt FROM pods p GROUP BY p.batch_id) x
		WHERE x.n <> (SELECT COUNT(*) FROM ledger l WHERE l.batch_id = x.b AND l.kind = 'reserve' AND l.reason = 'pod_overhead')
		OR x.amt <> (SELECT COALESCE(SUM(l.micro_usd), 0) FROM ledger l WHERE l.batch_id = x.b AND l.kind = 'reserve' AND l.reason = 'pod_overhead')`); n != 0 {
		e.failf("%d batches whose pod overhead reserves differ from their pods (reserved once per pod)", n)
	}
	// a batch's pods settle once: at most one excess row (a take's own provisional excess is another row of the same reason, which the
	// within profile forbids)
	if n := e.r.count(`SELECT COUNT(*) FROM (SELECT batch_id FROM ledger WHERE kind = 'settle' AND reason = 'excess' GROUP BY batch_id HAVING COUNT(*) > 1)`); n != 0 {
		e.failf("%d batches settled an excess twice", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM pods WHERE settled = 1 AND state <> 'terminated'`); n != 0 {
		e.failf("%d pods settled before they were terminated", n)
	}
	w.checkLifetimes(now)
	w.checkReality()
	w.checkTrip()
}

// checkLifetimes: no pod outlives its absolute deadline or the armed window, whatever happened to its agent, once the lifecycle can act (the
// key is in place, the network works, the app is running): a pod that cannot end itself is deleted from outside after its stop.
func (w *podWorld) checkLifetimes(now time.Time) {
	e := w.e
	if w.faulty {
		return
	}
	over := func(key string, due time.Time, what string) {
		if now.Before(due) {
			return
		}
		from, ok := w.service[key]
		if !ok {
			from = now
			w.service[key] = now
		}
		if from.Before(w.healedAt) {
			from = w.healedAt
		}
		if now.Sub(from) > podWorldKill {
			e.failf("%s: alive %s after the lifecycle could first stop it", what, now.Sub(from))
		}
	}
	for _, p := range w.sim.Pods() {
		if p.Deleted != nil {
			continue
		}
		var dl string
		if err := e.r.st.R.QueryRow(`SELECT deadline_at FROM pods WHERE name = ?`, p.Name).Scan(&dl); err != nil {
			continue // the intent is not written yet
		}
		d, err := store.ParseTime(dl)
		if err != nil {
			e.failf("deadline %q: %v", dl, err)
			continue
		}
		over("pod:"+p.Name, d.Add(-30*time.Second), "pod "+p.Name+" past its absolute deadline")
	}
	if st, err := w.gate.State(e.ctx); err == nil && st.Armed && !st.ArmedChangedAt.IsZero() {
		if w.sim.Live() > 0 {
			over("window", st.ArmedChangedAt.Add(podWorldWindow), "pods past the armed window")
		} else {
			delete(w.service, "window")
		}
	}
}

// checkReality: what the pods billed (the ground truth: the simulated RunPod's own meter) is covered by what was settled plus what is
// still reserved, with a minute of rounding per pod (the lifecycle bills lifetimes in whole minutes). In the within profile that holds at
// every tick; in the adversarial one a breach may exist only under a trip (the pod outlived its allowance and was reported), and it is
// settled as an excess.
func (w *podWorld) checkReality() {
	e := w.e
	billed := int64(w.sim.Billed())
	settled, err := settledAll(e.r.st.R)
	if err != nil {
		e.failf("model settled: %v", err)
		return
	}
	out, _, _, err := outstandingBy(e.r.st.R, "")
	if err != nil {
		e.failf("model outstanding: %v", err)
		return
	}
	slack := int64(len(w.sim.Pods())) * int64(money.ForSeconds(120, podSimCost))
	if billed > settled+out+slack && (e.profile == "within" || e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = 'fake-pod'`) == 0) {
		e.failf("reality check: the pods billed %d, covered by settled %d + outstanding %d + %d", billed, settled, out, slack)
	}
}

// checkTrip: an open trip stops creation: no pod is made while it stands.
func (w *podWorld) checkTrip() {
	e := w.e
	open := e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = 'fake-pod' AND reset_at IS NULL`)
	switch {
	case open > 0 && !w.tripSeen:
		w.tripSeen, w.createsAt = true, w.sim.Calls("create")
	case open > 0:
		if n := w.sim.Calls("create"); n != w.createsAt {
			e.failf("%d pods created while a trip stood open", n-w.createsAt)
		}
	default:
		w.tripSeen = false
	}
}

// ---- the timeline ------------------------------------------------------------------------------------------------------------------

// armed reports the backend's arming; the loop disarms it after the window or 30 idle minutes, so the operator arms it again for a batch.
func (w *podWorld) armed() bool {
	return w.e.r.count(`SELECT COUNT(*) FROM provider_state WHERE backend = ? AND armed = 1`, provider.BackendFakePod) == 1
}

func (w *podWorld) arm() {
	w.e.r.exec(`UPDATE provider_state SET armed = 1, changed_by = 'human:h1', changed_at = ? WHERE backend = ?`, store.FormatTime(w.e.r.clk.Now()), provider.BackendFakePod)
}

func (w *podWorld) resetTrips() {
	w.e.r.exec(`UPDATE provider_trips SET reset_at = ?, reset_by = 'human:h1', reset_reason = 'test' WHERE backend = ? AND reset_at IS NULL`, store.FormatTime(w.e.r.clk.Now()), provider.BackendFakePod)
}

// heal ends every fault of the timeline.
func (w *podWorld) heal() {
	w.keyOn.Store(true)
	w.cut = false
	w.sim.Partition(false)
	w.faulty = false
	w.healedAt = w.e.r.clk.Now()
	clear(w.service)
}

// run lives one timeline: batches arrive, the clock moves in steps from seconds to minutes, runners crash and are taken over, and (in the
// adversarial profile) the pod key disappears or the network is cut for a while.
func (w *podWorld) run() {
	e := w.e
	rng := e.rng
	e.setCaps(spend.Caps{MonthlyMicro: 200_000_000, BatchMicro: 200_000_000, HumanDailyMicro: 200_000_000, AgentDailyMicro: 200_000_000, AutoApproveTakes: 100, AutoApproveMicro: 200_000_000})
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil {
		e.failf("first step: %v", err)
	}
	e.pass() // the quote
	createAt := make([]int, 1+rng.IntN(3))
	for i := range createAt {
		createAt[i] = rng.IntN(60)
	}
	faultFrom, faultTo := -1, -1
	if e.profile == "adversarial" {
		faultFrom = 10 + rng.IntN(60)
		faultTo = faultFrom + 5 + rng.IntN(60)
	}
	const maxTicks = 250
	tick := 0
	for ; tick < maxTicks; tick++ {
		for _, at := range createAt {
			if at == tick {
				if !w.armed() {
					w.arm() // the operator arms the backend for a bulk run
				}
				w.createBatch()
			}
		}
		if !w.armed() && !e.allTerminal() && rng.IntN(4) == 0 {
			w.arm()
		}
		switch tick {
		case faultFrom:
			w.faulty = true
			if rng.IntN(2) == 0 {
				w.keyOn.Store(false)
			} else {
				w.cut = true
				w.sim.Partition(true)
			}
		case faultTo:
			w.heal()
		}
		d, away := []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}[rng.IntN(5)], false
		if rng.IntN(60) == 0 {
			d, away = time.Duration(3+rng.IntN(20))*time.Minute, true // the app was away
			clear(w.service)
		}
		w.advance(d, away)
		if rng.IntN(25) == 0 {
			e.setCrash(faultPoints[rng.IntN(len(faultPoints))])
		}
		if w.round(tick%3 == 0) {
			e.setCrash("")
			e.r.clk.Advance(time.Duration(95+rng.IntN(60)) * time.Second) // the dead runner's lease lapses
			clear(w.service)
			e.takeOver()
		}
		w.check()
		if tick > 70 && e.allTerminal() && w.sim.Live() == 0 {
			break
		}
	}
	e.t.Logf("seed %d (%s): %d ticks, %d batches, %d jobs, %d pods", e.seed, e.profile, tick, e.created, e.r.count(`SELECT COUNT(*) FROM jobs`), len(w.sim.Pods()))
	w.drain()
	w.finish()
}

// drain heals every fault, resets the trips the timeline opened (the operator's step) and runs the system until nothing is live: every job
// terminal, every pod gone at the simulated RunPod and settled.
func (w *podWorld) drain() {
	e := w.e
	e.setCrash("")
	w.heal()
	w.resetTrips()
	w.arm()
	for i := 0; i < 900; i++ {
		w.advance(30*time.Second, false)
		if w.round(i%4 == 0) {
			e.r.clk.Advance(100 * time.Second)
			clear(w.service)
			e.takeOver()
		}
		w.check()
		if i%40 == 39 {
			w.arm()
			w.resetTrips() // a trip reported again holds the queue until the operator acts
		}
		if e.allTerminal() && w.sim.Live() == 0 && e.r.count(`SELECT COUNT(*) FROM pods WHERE state <> 'terminated' OR settled = 0`) == 0 {
			return
		}
	}
}

// finish checks the end state of a timeline.
func (w *podWorld) finish() {
	e := w.e
	if !e.allTerminal() {
		e.failf("jobs still not terminal after the drain: %d\n%s", e.r.count(`SELECT COUNT(*) FROM jobs WHERE state NOT IN ('completed','failed','cancelled','expired')`), e.dumpStuck())
	}
	if n := w.sim.Live(); n != 0 {
		e.failf("%d pods still billing at the end", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM pods WHERE state <> 'terminated' OR settled = 0`); n != 0 {
		e.failf("%d pods not terminated and settled at the end", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE state NOT IN ('done','rejected')`); n != 0 {
		e.failf("%d attempts neither done nor rejected", n)
	}
	// ground truth: every pod's bill is inside what the ledger settled
	billed := int64(w.sim.Billed())
	settled, _ := settledAll(e.r.st.R)
	slack := int64(len(w.sim.Pods())) * int64(money.ForSeconds(120, podSimCost))
	if billed > settled+slack {
		e.failf("the pods billed %d but only %d was settled (+%d rounding)", billed, settled, slack)
	}
	if e.profile == "within" {
		if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE kind = 'settle' AND reason = 'excess'`); n != 0 {
			e.failf("%d excess rows in the within profile", n)
		}
		if n := e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = 'fake-pod'`); n != 0 {
			e.failf("%d trips in the within profile: %s", n, e.r.str(`SELECT group_concat(reason) FROM provider_trips WHERE backend = 'fake-pod'`))
		}
	}
}

// TestProperty_SpendNeverExceedsCap_Pods (C399.13): over seeded timelines on the fake pod backend, the pod overhead is reserved exactly once
// per pod and settled once, no commit breaks a cap or a batch's conservation (the property suite's model runs on every commit), no pod
// outlives its absolute deadline or the armed window while the key and the network work, a trip stops creation, and what the simulated
// RunPod billed (the sum of every pod's lifetime at its price) is covered by what was settled. The adversarial profile adds pods that
// cannot end themselves, a removed key and a cut network.
func TestProperty_SpendNeverExceedsCap_Pods(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy property suite")
	}
	n := max(3, propTimelines(t, propDefaultTimelines)/3) // a third of the main suite, at least 3 of each profile
	for pi, profile := range []string{"within", "adversarial"} {
		for i := range n {
			seed := uint64(50000 + 1000*pi + i + 1)
			if !propInShard(t, seed) {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", profile, seed), func(t *testing.T) {
				t.Parallel()
				e, w := newPodEnv(t, seed, profile)
				w.run()
				if e.created == 0 {
					t.Fatalf("seed %d created no batch", seed)
				}
				// a create the network cut off leaves a row and no pod at RunPod: the row is the evidence in the adversarial profile
				if n := e.r.count(`SELECT COUNT(*) FROM pods`); n == 0 || (profile == "within" && len(w.sim.Pods()) == 0) {
					t.Fatalf("seed %d made no pod: the timeline proves nothing", seed)
				}
			})
		}
	}
}
