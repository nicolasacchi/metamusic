package jobs

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The chaos suite (C86.4): named misbehaviours of the provider and of the runner itself, each on the real fake
// backend with its simulated worker uploading through the real handlers (header-only FLAC, stub ingester).
// The ledger invariants of the property suite are checked after every commit here too.

// chaosEnv is a quiet timeline: generous caps, no random events; the test scripts what happens.
func chaosEnv(t *testing.T, opts ...any) *propEnv {
	t.Helper()
	e := newPropEnv(t, 1, "within", opts...)
	e.setCaps(spend.Caps{MonthlyMicro: 50_000_000, BatchMicro: 50_000_000, HumanDailyMicro: 50_000_000, AgentDailyMicro: 50_000_000, AutoApproveTakes: 100, AutoApproveMicro: 50_000_000})
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil { // takes the lease so the reconciler may run
		t.Fatal(err)
	}
	e.pass()
	return e
}

// seed inserts one batch of n jobs sharing a marker string.
func (e *propEnv) seedBatch(marker string, n, timeoutS int) seeded {
	e.t.Helper()
	return e.r.seedBatch(batchSpec{jobs: n, timeoutS: timeoutS, recipe: e.recipeFor(marker), ceiling: 20_000_000, backend: e.bname})
}

// drive steps the runner (and the reconciler every third tick) in dt increments until done reports true.
func (e *propEnv) drive(maxTicks int, dt time.Duration, done func() bool) {
	e.t.Helper()
	for i := 0; i < maxTicks; i++ {
		if done() {
			return
		}
		e.r.clk.Advance(dt)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver()
		}
		if i%3 == 0 {
			e.pass()
		}
	}
	if !done() {
		e.t.Fatalf("drive: the condition never held after %d ticks of %v\n%s", maxTicks, dt, e.dumpStuck())
	}
}

func (e *propEnv) jobTerminal(id string) func() bool {
	return func() bool {
		switch e.r.state(id) {
		case StateCompleted, StateFailed, StateCancelled, StateExpired:
			return true
		}
		return false
	}
}

func (e *propEnv) tracks() int { return e.r.count(`SELECT COUNT(*) FROM tracks WHERE origin = 'job'`) }

func TestChaos_CrashBeforeRefPersisted(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t)
	s := e.seedBatch("", 1, 60)
	job := s.Jobs[0]
	e.setCrash(FaultAfterSubmitReturn)
	e.r.clk.Advance(5 * time.Second)
	if !e.step() {
		t.Fatal("the step did not crash at after_submit_return")
	}
	if e.fb.Counters().Submit != 1 {
		t.Fatalf("submits %d, want the one that returned before the crash", e.fb.Counters().Submit)
	}
	if ref := e.r.str(`SELECT COALESCE(ref,'') FROM job_attempts WHERE job_id = ? AND n = 1`, job); ref != "" {
		t.Fatalf("the ref was persisted (%q) in a crash that precedes it", ref)
	}
	e.r.clk.Advance(100 * time.Second)
	e.takeOver()
	e.drive(120, 20*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	if n := e.tracks(); n != 1 {
		t.Errorf("%d tracks, want exactly one", n)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE job_id = ? AND reason = 'topup_ambiguous'`, job); n != 1 {
		t.Errorf("%d topup_ambiguous reserves, want 1", n)
	}
	if !e.r.hasEvent(job, EvGhost) {
		t.Errorf("the adopted attempt never became a ghost: %v", e.r.events(job))
	}
}

func TestChaos_GhostAdoptedViaStart(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t)
	s := e.seedBatch("fake:submit=accepted_5xx", 1, 60)
	job := s.Jobs[0]
	e.drive(160, 20*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	if n := e.tracks(); n != 1 {
		t.Errorf("%d tracks, want exactly one", n)
	}
	if !e.r.hasEvent(job, EvGhost) || !e.r.hasEvent(job, EvGhostStarted) {
		t.Errorf("the ghost did not announce itself and get promoted: %v", e.r.events(job))
	}
	if n := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ? AND announced_ref IS NOT NULL`, job); n == 0 {
		t.Error("no attempt carries the ghost's announced ref")
	}
}

func TestChaos_UploadShaMismatch(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t)
	job := e.seedBatch("fake:sha_mismatch", 1, 60).Jobs[0]
	e.drive(120, 20*time.Second, e.jobTerminal(job))
	if got := e.r.state(job); got != StateFailed {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	e.r.wantCode(job, CodeArtifactMismatch)
	if got := e.r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE job_id = ? AND kind = 'settle' AND reason = 'execution'`, job); got <= 0 {
		t.Errorf("execution settle %d: the paid render must be settled", got)
	}
	if n := e.tracks(); n != 0 {
		t.Errorf("%d tracks from a mismatching upload", n)
	}
}

func TestChaos_SubmitErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, marker string
		want         string // final state
		code         string
		events       []string
		more         int // attempts beyond the first the job needed (0 = none)
	}{
		{"1010 blocks without retries", "fake:submit=1010", StateFailed, CodeEdgeBlocked, nil, 0},
		{"429 resubmits after the retry-after", "fake:submit=429", StateCompleted, "", []string{EvSubmitRetry}, 0},
		{"5xx becomes a ghost, a top-up and a resubmit", "fake:submit=5xx", StateCompleted, "", []string{EvGhost, EvTopup}, 1},
		{"paused backs off", "fake:submit=paused", StateCompleted, "", []string{EvSubmitRetry}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := chaosEnv(t)
			job := e.seedBatch(c.marker, 1, 60).Jobs[0]
			e.drive(200, 15*time.Second, e.jobTerminal(job))
			if got := e.r.state(job); got != c.want {
				t.Fatalf("job %s, want %s (%v)", got, c.want, e.r.events(job))
			}
			if c.code != "" {
				e.r.wantCode(job, c.code)
				if n := e.fb.Counters().Submit; n != 1 {
					t.Errorf("%d submits after a 1010, want exactly the one that was refused", n)
				}
			}
			for _, ev := range c.events {
				if !e.r.hasEvent(job, ev) {
					t.Errorf("missing %s in %v", ev, e.r.events(job))
				}
			}
		})
	}
	t.Run("401 trips provider_access and nothing is submitted", func(t *testing.T) {
		t.Parallel()
		e := chaosEnv(t, func(o *fake.Options) { o.Permissions.AccessRevoked = true })
		job := e.seedBatch("", 1, 60).Jobs[0]
		e.drive(120, 15*time.Second, func() bool {
			return e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = ?`, TripProviderAccess) > 0
		})
		for range 20 {
			e.r.clk.Advance(15 * time.Second)
			e.step()
		}
		// a revoked key fails closed: the tripped provider claims nothing and the job waits, unspent
		if n := e.fb.Counters().Submit; n != 0 {
			t.Errorf("%d submits through a revoked key", n)
		}
		e.r.wantState(job, StateQueued)
		if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE kind = 'settle'`); n != 0 {
			t.Errorf("%d settle rows without any execution", n)
		}
	})
}

func TestChaos_QueueExpiryResubmitThenNoCapacity(t *testing.T) {
	t.Parallel()
	// a short TTL keeps the seven expiries (and the backoffs 1, 2, 4 ... minutes between them) inside about a hundred
	// one-minute ticks (a coarser tick is still finer than every timer involved: the TTL is 5 minutes)
	e := chaosEnv(t, func(o *RunnerOptions) { o.TTL = 5 * time.Minute })
	s := e.seedBatch("fake:queue_expire=99", 3, 60)
	first := s.Jobs[0]
	e.drive(700, 60*time.Second, func() bool {
		return e.jobTerminal(first)() && e.r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state NOT IN ('completed','failed','cancelled','expired')`, s.Batch) == 0
	})
	e.r.wantState(first, StateFailed)
	if got := e.r.job(first).ErrorCode; got != CodeNoCapacity {
		t.Fatalf("job %s failed %s, want %s (events %v)", first, got, CodeNoCapacity, e.r.events(first))
	}
	// a fresh attempt (fresh upload ids, a fresh attempt_overhead reserve) per expiry
	atts := e.r.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ?`, first)
	if atts != 7 {
		t.Errorf("%d attempts, want 7 (the 7th expiry fails the job)", atts)
	}
	if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE job_id IS NULL AND batch_id = ? AND reason = 'attempt_overhead'`, s.Batch) +
		e.r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND reason = 'attempt_overhead'`, s.Batch); n < atts {
		t.Errorf("%d overhead reserves for %d attempts", n, atts)
	}
	if got := e.r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE job_id = ? AND kind = 'release' AND reason = 'never_ran'`, first); got <= 0 {
		t.Errorf("never_ran release %d", got)
	}
	for _, j := range s.Jobs[1:] {
		st := e.r.state(j)
		if st != StateFailed && st != StateCompleted && st != StateCancelled {
			t.Errorf("job %s left in %s", j, st)
		}
	}
	var reserved, ceiling int64
	_ = e.r.st.R.QueryRow(`SELECT COALESCE(SUM(l.micro_usd),0), b.ceiling_micro_usd FROM ledger l JOIN batches b ON b.id = l.batch_id WHERE l.batch_id = ? AND l.kind = 'reserve'`, s.Batch).Scan(&reserved, &ceiling)
	if reserved > ceiling {
		t.Errorf("reserves %d exceed the batch ceiling %d", reserved, ceiling)
	}
}

func TestChaos_StuckWorkerTrips(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t)
	job := e.seedBatch("fake:stuck_worker", 1, 60).Jobs[0]
	e.drive(400, 30*time.Second, func() bool {
		return e.r.count(`SELECT COUNT(*) FROM provider_trips`) > 0 && e.jobTerminal(job)()
	})
	if n := e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason IN (?, ?)`, TripStuckRunning, TripMeterExceeds); n == 0 {
		t.Errorf("no stuck-worker trip (trips: %d)", e.r.count(`SELECT COUNT(*) FROM provider_trips`))
	}
	// (no attempt is live when a stuck worker outlives its batch, so there is nothing for "cancel all" to cancel)
	if c := e.fb.Counters(); c.ScaleToZero == 0 || c.PurgeQueue == 0 {
		t.Errorf("the kill switch did not run in full: %+v", c)
	}
	urgent := false
	for _, m := range e.r.notes.all() {
		if m.Priority >= 5 {
			urgent = true
		}
	}
	if !urgent {
		t.Error("no priority 5 notification")
	}
}

func TestChaos_CancelIgnored(t *testing.T) {
	t.Parallel()
	t.Run("fake: the first cancel is acknowledged and ignored, the re-issue is honoured", func(t *testing.T) {
		t.Parallel()
		// no simulated worker: its own start poll would stop the pipeline once the attempt is closed (C74.3);
		// here the provider is the only party that can end the job
		e := chaosEnv(t, func(o *fake.Options) { o.Worker = nil })
		job := e.seedBatch("fake:cancel_ignored=1000 fake:slow=3000", 1, 600).Jobs[0]
		e.drive(80, 5*time.Second, func() bool { return e.r.state(job) == StateGenerating })
		if _, err := e.r.service(e.ctx).CancelJob(e.ctx, humanP, job, "stop"); err != nil {
			t.Fatal(err)
		}
		asked := e.r.clk.Now()
		for e.r.clk.Now().Sub(asked) < 25*time.Second {
			e.r.clk.Advance(5 * time.Second)
			e.step()
			if s := e.r.state(job); s != StateCancelling {
				t.Fatalf("after %v the job is %s, want cancelling while the cancel is ignored", e.r.clk.Now().Sub(asked), s)
			}
		}
		if n := e.fb.Counters().Cancel; n != 1 {
			t.Errorf("%d cancel calls inside the first 25 s, want 1", n)
		}
		e.drive(20, 10*time.Second, e.jobTerminal(job))
		e.r.wantState(job, StateCancelled)
		if n := e.fb.Counters().Cancel; n != 2 {
			t.Errorf("%d cancel calls, want the original and one re-issue", n)
		}
	})
	t.Run("scripted: a provider that never confirms keeps the job cancelling, re-issues every 30 s and alerts at 5 minutes", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		spec := r.submitOne(job)
		ref := r.refOf(job, 1)
		r.setStatus(ref, provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
		r.advance(10 * time.Second)
		r.step()
		r.wantState(job, StateGenerating)
		_ = spec
		if _, err := r.service(t.Context()).CancelJob(t.Context(), humanP, job, ""); err != nil {
			t.Fatal(err)
		}
		asked := r.clk.Now()
		alerted := func() bool {
			for _, m := range r.notes.all() {
				if strings.Contains(strings.ToLower(m.Title), "cancel") {
					return true
				}
			}
			return false
		}
		for r.clk.Now().Sub(asked) < 6*time.Minute {
			r.advance(10 * time.Second)
			r.step()
			if s := r.state(job); s != StateCancelling {
				t.Fatalf("after %v the job is %s, want cancelling for as long as the provider shows it running", r.clk.Now().Sub(asked), s)
			}
			if r.clk.Now().Sub(asked) < 4*time.Minute+50*time.Second && alerted() {
				t.Fatalf("the alert fired after only %v", r.clk.Now().Sub(asked))
			}
		}
		if n := r.cancelCount(ref); n < 11 {
			t.Errorf("%d cancels in 6 minutes, want one about every 30 s", n)
		}
		if !alerted() {
			t.Error("the five-minute cancel-not-confirmed notification never fired")
		}
	})
}

func TestChaos_ResultsPurgedDuringDowntime(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t, func(o *fake.Options) { o.Adversarial.PurgeResultsAfter30m = true })
	job := e.seedBatch("fake:slow=20", 1, 120).Jobs[0]
	e.drive(60, 5*time.Second, func() bool { s := e.r.state(job); return s == StateGenerating || s == StateStarting })
	// the runner is down for 40 minutes; the job runs and completes, and the provider purges the result
	e.r.clk.Advance(40 * time.Minute)
	e.takeOver()
	e.drive(80, 30*time.Second, e.jobTerminal(job))
	got := e.r.state(job)
	if got != StateFailed && got != StateCompleted {
		t.Fatalf("job %s (%v)", got, e.r.events(job))
	}
	// a result that cannot be read back is unknown, settled by the time bound: the paid execution is never
	// released as if the job had not run
	if got == StateFailed {
		if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE job_id = ? AND kind = 'release' AND reason = 'never_ran'`, job); n != 0 {
			t.Error("never_ran released for a job that ran")
		}
		if s := e.r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE job_id = ? AND kind = 'settle'`, job); s <= 0 {
			t.Errorf("settled %d for an execution the provider billed", s)
		}
	}
}

// keep the compiler honest about helpers shared with the property suite
var _ = errors.Is
var _ = store.FormatTime

// TestProperty_ColdStartAboveAllowanceIsNotMetered: the meter bills RUNNING time (plus the idle tail), not the
// worker record's life. A cold start above the quote's allowance (cold = 400 s against ColdStartS 180) is an
// INITIALIZING stretch RunPod does not bill from the listing's point of view, so it never trips the meter;
// the ledger stays internally consistent and the billing reconciliation still checks the real invoice.
func TestProperty_ColdStartAboveAllowanceIsNotMetered(t *testing.T) {
	t.Parallel()
	e := chaosEnv(t)
	job := e.seedBatch("fake:cold=400", 2, 60).Jobs[0]
	e.drive(300, 15*time.Second, e.jobTerminal(job))
	if n := e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = ?`, TripMeterExceeds); n != 0 {
		t.Errorf("an INITIALIZING cold start tripped the meter (%d trips)", n)
	}
}

// TestChaos_SubmitErrorsRunPod is the submit table against the serverless backend over runpodtest.
func TestChaos_SubmitErrorsRunPod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		fault  runpodtest.Fault
		want   string
		code   string
		trip   string
		events []string
	}{
		{"1010 blocks without retries", runpodtest.Fault{Status: 403, Body: "error code: 1010", Times: -1}, StateFailed, CodeEdgeBlocked, TripProviderAccess, nil},
		{"401 fails PROVIDER_AUTH and trips provider_access", runpodtest.Fault{Status: 401, Times: -1}, StateFailed, CodeProviderAuth, TripProviderAccess, nil},
		{"404 on /run trips provider_access", runpodtest.Fault{Status: 404, Times: -1}, "", "", TripProviderAccess, nil},
		{"429 resubmits after the retry-after", runpodtest.Fault{Status: 429, Header: http.Header{"Retry-After": {"30"}}, Times: 1}, StateCompleted, "", "", []string{EvSubmitRetry}},
		{"5xx before acceptance is a ghost, a top-up and a resubmit", runpodtest.Fault{Status: 503, AfterAccept: true, Times: 1}, StateCompleted, "", "", []string{EvGhost, EvTopup}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newRunPodEnv(t, 1)
			e.setCaps(spend.Caps{MonthlyMicro: 50_000_000, BatchMicro: 50_000_000, HumanDailyMicro: 50_000_000, AgentDailyMicro: 50_000_000, AutoApproveTakes: 100, AutoApproveMicro: 50_000_000})
			e.capsOn = true
			if _, err := e.cur.Step(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.pass()
			job := e.seedBatch("", 1, 60).Jobs[0]
			e.rp.Fault(runpodtest.OpRun, c.fault)
			if c.want == "" { // a trip is the outcome: the job waits behind the tripped provider
				e.drive(120, 15*time.Second, func() bool {
					return e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = ?`, c.trip) > 0
				})
				return
			}
			// (the endpoint has workers.max 1: the ghost counts as in flight until its window closes, 2 h on)
			e.drive(300, time.Minute, e.jobTerminal(job))
			if got := e.r.state(job); got != c.want {
				t.Fatalf("job %s, want %s (%v)", got, c.want, e.r.events(job))
			}
			if c.code != "" {
				e.r.wantCode(job, c.code)
			}
			if c.trip != "" && e.r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = ?`, c.trip) == 0 {
				t.Errorf("no %s trip", c.trip)
			}
			if c.code == CodeEdgeBlocked {
				if n := e.rp.Calls(runpodtest.OpRun); n != 1 {
					t.Errorf("%d /run calls after a 1010, want exactly the one that was refused (%v)", n, e.r.events(job))
				}
			}
			for _, ev := range c.events {
				if !e.r.hasEvent(job, ev) {
					t.Errorf("missing %s in %v", ev, e.r.events(job))
				}
			}
		})
	}
}

// TestChaos_StorageFaults: the database fails under the runner and the reconciler, one call per tick (a random
// K-th statement, begin or commit), while batches with every kind of misbehaviour run. Whatever a failed tick
// left half-done, later ticks finish the work: every job ends, the ledger invariants (checked after every
// commit by a model that reads through its own, healthy handle) never break and nothing is spent twice.
func TestChaos_StorageFaults(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{}
	e := chaosEnv(t, withFailing(ctl), func(o *RunnerOptions) { o.TTL = 20 * time.Minute })
	clean, err := store.Open(e.ctx, e.r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clean.Close() })
	e.mq = clean.R
	e.faulty = true
	var jobs []string
	for _, b := range []struct {
		marker string
		n      int
	}{{"", 4}, {"fake:queue_expire=1", 2}, {"fake:submit=5xx", 2}, {"fake:submit=429", 1}, {"fake:fail=worker_lost", 1}, {"fake:slow=100", 2}, {"fake:requeue", 1}} {
		jobs = append(jobs, e.seedBatch(b.marker, b.n, 60).Jobs...)
	}
	rng := e.rng
	for i := 0; i < 260; i++ {
		e.r.clk.Advance([]time.Duration{5 * time.Second, 15 * time.Second, 40 * time.Second}[rng.IntN(3)])
		ctl.arm(int64(1 + rng.IntN(70)))
		crashed := e.step()
		ctl.disarm()
		if crashed {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver()
		}
		if i%3 == 0 {
			ctl.arm(int64(1 + rng.IntN(60)))
			e.pass()
			ctl.disarm()
		}
		if rng.IntN(20) == 0 {
			e.cancelRandom()
		}
	}
	e.faulty = false
	ctl.disarm()
	e.r.clk.Advance(3 * time.Minute) // any lease a failed tick left behind lapses
	e.drive(600, 30*time.Second, e.allTerminal)
	for _, j := range jobs {
		if !e.jobTerminal(j)() {
			t.Errorf("job %s is %s", j, e.r.state(j))
		}
	}
	if n := e.r.count(`SELECT COUNT(*) FROM ledger WHERE kind = 'settle' AND reason = 'excess'`); n != 0 {
		t.Errorf("%d excess rows after storage faults", n)
	}
	e.checkAll()
}

// checkAll runs the per-batch and per-job conservation checks over everything in the ledger.
func (e *propEnv) checkAll() {
	for _, tbl := range []struct{ q string }{{`SELECT id FROM batches`}} {
		rows, err := e.q().Query(tbl.q)
		if err != nil {
			e.failf("model: %v", err)
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		_ = rows.Close()
		for _, id := range ids {
			e.checkBatch(id)
		}
	}
	rows, err := e.q().Query(`SELECT id FROM jobs`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		e.checkJob(id)
	}
}
