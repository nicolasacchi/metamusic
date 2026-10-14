package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func (r *rig) liveAttempts() int {
	r.t.Helper()
	return r.count(`SELECT COUNT(*) FROM job_attempts WHERE state IN ('pending','submitted','unknown')`)
}

func (r *rig) setQuote(mod func(q *provider.Quote)) {
	r.t.Helper()
	q := providertest.GoodQuote(provider.BackendFake, r.clk.Now())
	mod(&q)
	raw, _ := json.Marshal(q)
	r.exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = 'fake'`, string(raw), store.FormatTime(q.ObservedAt))
}

func TestRunner_ClaimRespectsCapacity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{jobs: 5, workersMax: 2})
	rep := r.step()
	if rep.Claimed != 2 || r.liveAttempts() != 2 {
		t.Fatalf("claimed %d live %d, want 2", rep.Claimed, r.liveAttempts())
	}
	r.wantState(b.Jobs[0], StateStarting)
	r.wantState(b.Jobs[1], StateStarting)
	r.wantState(b.Jobs[2], StateQueued)
	// one finishes: the third is claimed in the next tick
	spec := r.be.Specs()[0]
	out := r.deliver(spec, deliverOpts{})
	r.setStatus("ref-"+spec.AttemptID, completed(out, 60000))
	r.advance(6 * time.Second)
	r.step()
	r.wantState(b.Jobs[0], StateCompleted)
	r.step() // the claim follows the poll in the tick order
	r.wantState(b.Jobs[2], StateStarting)
	r.wantState(b.Jobs[3], StateQueued)
	if r.liveAttempts() != 2 {
		t.Errorf("live %d", r.liveAttempts())
	}
}

func TestRunner_UnknownAttemptsCountInFlight(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{jobs: 3, workersMax: 2})
	r.ghost(b.Jobs[0], "unknown", true)
	r.arm()
	r.step()
	if r.liveAttempts() != 2 {
		t.Fatalf("live attempts %d: the ghost counts toward workers.max", r.liveAttempts())
	}
	r.wantState(b.Jobs[0], StateStarting) // its resubmission took the second slot
	r.wantState(b.Jobs[1], StateQueued)
	r.wantState(b.Jobs[2], StateQueued)
}

func TestRunner_InFlightUsesFrozenMax(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{jobs: 4, workersMax: 2})
	r.setQuote(func(q *provider.Quote) { q.Endpoint.WorkersMax = 3 })
	r.step()
	if r.liveAttempts() != 2 {
		t.Errorf("live %d, want the frozen 2 although the live max is 3", r.liveAttempts())
	}
	// and a lowered live max wins (the minimum)
	r2 := newRig(t)
	r2.seedBatch(batchSpec{jobs: 4, workersMax: 2})
	r2.setQuote(func(q *provider.Quote) { q.Endpoint.WorkersMax = 1 })
	r2.step()
	if r2.liveAttempts() != 1 {
		t.Errorf("live %d, want 1", r2.liveAttempts())
	}
	_ = b
}

func TestRunner_IdleRaisedTopsUpOverhead(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	idle := 60
	r.setQuote(func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = &idle })
	r.step()
	if got, want := r.ledger(b.Batch, "reserve", "attempt_overhead"), int64(money.ForSeconds(180+60, testPrice)); got != want {
		t.Errorf("overhead %d, want %d (the live idle timeout)", got, want)
	}
	waitFor(t, func() bool { return len(r.notes.all()) > 0 })
	if ms := r.notes.all(); !strings.Contains(ms[0].Title, "drift") || len(ms) != 1 {
		t.Errorf("notes %+v", ms)
	}
	// a lower live idle never lowers the reserve below the frozen one
	r2 := newRig(t)
	b2 := r2.seedBatch(batchSpec{})
	low := 5
	r2.setQuote(func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = &low })
	r2.step()
	if got, want := r2.ledger(b2.Batch, "reserve", "attempt_overhead"), int64(money.ForSeconds(180+20, testPrice)); got != want {
		t.Errorf("overhead %d, want %d", got, want)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 2000 {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestRunner_NeverClaimsOtherBackend(t *testing.T) {
	t.Parallel()
	rp := providertest.New(provider.BackendRunPod, providertest.Script{})
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Backend = rp }))
	q, _ := json.Marshal(providertest.GoodQuote(provider.BackendRunPod, t0))
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES('runpod-serverless', 1, 'system:default', ?, ?, ?)`,
		store.FormatTime(t0), string(q), store.FormatTime(t0))
	b := r.seedBatch(batchSpec{}) // created under `fake`
	r.steps(2)
	r.wantState(b.Jobs[0], StateQueued)
	if rp.Calls().Total() != 0 {
		t.Errorf("backend calls %+v", rp.Calls())
	}
	r.advance(25 * time.Hour)
	r.step()
	r.wantState(b.Jobs[0], StateFailed)
	r.wantCode(b.Jobs[0], CodeNotSubmitted)
}

func TestRunner_ClaimRequiresArmedNotTripped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{jobs: 3, workersMax: 1})
	r.disarm()
	r.step()
	if r.be.Calls().Submit != 0 {
		t.Fatal("claimed while disarmed")
	}
	r.arm()
	r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('trip-1', 'fake', ?, 'stuck_running', '{}', '[]')`, store.FormatTime(t0))
	r.step()
	if r.be.Calls().Submit != 0 {
		t.Fatal("claimed while tripped")
	}
	r.exec(`UPDATE provider_trips SET reset_at = ?, reset_by = 'human:h1', reset_reason = 'looked' WHERE id = 'trip-1'`, store.FormatTime(t0))
	r.step()
	if r.be.Calls().Submit != 1 {
		t.Fatalf("submits %d after the reset", r.be.Calls().Submit)
	}
	r.wantState(b.Jobs[0], StateStarting)
}

func TestRunner_ClaimRefusedWhenQuoteStale(t *testing.T) {
	t.Parallel()
	rp := providertest.New(provider.BackendRunPod, providertest.Script{})
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Backend = rp }))
	insert := func(at time.Time, mod func(*provider.Quote)) {
		q := providertest.GoodQuote(provider.BackendRunPod, at)
		if mod != nil {
			mod(&q)
		}
		raw, _ := json.Marshal(q)
		r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES('runpod-serverless', 1, 'system:default', ?, ?, ?)
			ON CONFLICT(backend) DO UPDATE SET quote_json = excluded.quote_json, quote_at = excluded.quote_at`,
			store.FormatTime(t0), string(raw), store.FormatTime(at))
	}
	r.seedBatch(batchSpec{backend: "runpod-serverless"})
	insert(t0.Add(-2*time.Hour), nil)
	r.step()
	if rp.Calls().Submit != 0 {
		t.Fatal("claimed with a stale quote")
	}
	idle0 := (*int)(nil)
	insert(t0, func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = idle0 })
	r.step()
	if rp.Calls().Submit != 0 {
		t.Fatal("claimed with a quote that fails the config check")
	}
	insert(t0, nil)
	r.step()
	if rp.Calls().Submit != 1 {
		t.Fatalf("submits %d with a fresh quote", rp.Calls().Submit)
	}
}

func TestRunner_NoClaimWhenDiskLow(t *testing.T) {
	t.Parallel()
	testPreflightGate(t, &PreflightError{Check: "disk", Err: errors.New("low")}, "disk low")
}

func TestRunner_NoClaimWhenUploadRouteDown(t *testing.T) {
	t.Parallel()
	testPreflightGate(t, &PreflightError{Check: "route", Err: errors.New("down")}, "upload route down")
}

func testPreflightGate(t *testing.T, failure error, wantInTitle string) {
	t.Helper()
	var calls, bytes atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.Preflight = func(_ context.Context, inFlight int64) error {
			calls.Add(1)
			bytes.Store(inFlight)
			if fail.Load() {
				return failure
			}
			return nil
		}
	}))
	b := r.seedBatch(batchSpec{})
	r.step()
	r.wantState(b.Jobs[0], StateQueued)
	if r.be.Calls().Submit != 0 {
		t.Fatal("claimed while the preflight fails")
	}
	if !r.hasEvent(b.Jobs[0], EvPreflightFailed) {
		t.Errorf("events %v", r.events(b.Jobs[0]))
	}
	if ms := r.notes.all(); len(ms) != 1 || !strings.Contains(ms[0].Title, wantInTitle) || ms[0].Priority != 4 {
		t.Fatalf("notes %+v", ms)
	}
	if bytes.Load() < 160<<20 {
		t.Errorf("the preflight must see at least the new attempt's open uploads: %d", bytes.Load())
	}
	// cached for 60 s: no second call and no second ntfy
	r.advance(30 * time.Second)
	r.step()
	if calls.Load() != 1 || len(r.notes.all()) != 1 {
		t.Errorf("calls %d notes %d", calls.Load(), len(r.notes.all()))
	}
	// fixed: the next check after 60 s passes and the job is claimed
	fail.Store(false)
	r.advance(31 * time.Second)
	r.step()
	r.wantState(b.Jobs[0], StateStarting)
	if calls.Load() != 2 {
		t.Errorf("calls %d", calls.Load())
	}
}

func TestRunner_AttemptWrittenBeforeSubmit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	var seen struct{ attempt, uploads, overhead, state, events string }
	r.onSubmit(func(spec provider.TakeSpec) (provider.Ref, error) {
		seen.attempt = r.str(`SELECT state FROM job_attempts WHERE id = ? AND job_id = ?`, spec.AttemptID, job)
		seen.uploads = r.str(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'open'`, spec.AttemptID)
		seen.overhead = r.str(`SELECT SUM(micro_usd) FROM ledger WHERE attempt_id = ? AND reason = 'attempt_overhead'`, spec.AttemptID)
		seen.state = r.str(`SELECT state FROM jobs WHERE id = ?`, job)
		return provider.Ref{Backend: provider.BackendFake, ID: "ref-x"}, nil
	})
	r.step()
	if seen.attempt != "pending" || seen.uploads != "11" || seen.overhead != "38334" || seen.state != "submitting" {
		t.Errorf("at Submit: %+v", seen)
	}
}

func TestRunner_NoNetworkWithShortLease(t *testing.T) {
	t.Parallel()
	var eat atomic.Bool
	eat.Store(true)
	var r *rig
	r = newRig(t, withOpts(func(o *RunnerOptions) {
		// the preflight (a network call itself) eats the lease: 70 of its 90 seconds
		o.Preflight = func(context.Context, int64) error {
			if eat.Load() {
				r.advance(70 * time.Second)
			}
			return nil
		}
	}))
	b := r.seedBatch(batchSpec{})
	r.step()
	r.wantState(b.Jobs[0], StateQueued)
	if r.be.Calls().Total() != 0 {
		t.Errorf("a network call with 20 s of lease left: %+v", r.be.Calls())
	}
	if r.count(`SELECT COUNT(*) FROM job_attempts`) != 0 {
		t.Error("the claim must roll back")
	}
	eat.Store(false)
	r.advance(2 * time.Minute) // the next tick renews the lease first
	r.step()
	r.wantState(b.Jobs[0], StateStarting)
}

func TestRunner_UploadsFreshPerAttempt(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.NeverRan = NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}}
	}))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitOne(job)
	r.advance(2*time.Hour + time.Second)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
	r.step()
	r.wantState(job, StateQueued)
	r.advance(time.Minute)
	r.step()
	r.wantState(job, StateStarting)
	specs := r.be.Specs()
	if len(specs) != 2 {
		t.Fatalf("submits %d", len(specs))
	}
	for name, u := range specs[0].Uploads {
		if specs[1].Uploads[name].URL == u.URL {
			t.Errorf("%s: the second attempt reuses the first attempt's capability URL", name)
		}
	}
	if specs[0].StartURL == specs[1].StartURL {
		t.Error("start URL reused")
	}
	if n := r.count(`SELECT COUNT(DISTINCT id) FROM uploads WHERE job_id = ?`, job); n != 22 {
		t.Errorf("distinct upload ids %d", n)
	}
	// the first attempt's uploads are closed: a late PUT from it is refused
	if r.put(specs[0].Uploads["audio.flac"].URL, []byte("late")) != 410 {
		t.Error("late upload of the expired attempt must answer 410")
	}
}

func TestRunner_QueueExpiryResubmit(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.NeverRan = NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}}
	}))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	wantBackoff := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute}
	r.step()
	for i, want := range wantBackoff {
		r.wantState(job, StateStarting)
		if got := r.job(job).Attempts; got != i+1 {
			t.Fatalf("attempts %d, want %d", got, i+1)
		}
		r.advance(2*time.Hour + time.Second)
		r.setStatus(r.refOf(job, i+1), provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
		r.step()
		r.wantState(job, StateQueued)
		j := r.job(job)
		if j.QueueExpiries != i+1 || !j.NextAttemptAt.Equal(r.clk.Now().Add(want)) {
			t.Fatalf("expiry %d: expiries %d next %v (now %v), want backoff %v", i+1, j.QueueExpiries, j.NextAttemptAt, r.clk.Now(), want)
		}
		r.advance(want - time.Second)
		r.step()
		r.wantState(job, StateQueued)
		r.advance(time.Second)
		r.step()
	}
	// every attempt reserved its own overhead
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE job_id = ? AND reason = 'attempt_overhead'`, job); n != 7 {
		t.Errorf("overhead reserves %d, want one per attempt", n)
	}
	r.wantState(job, StateStarting) // the 7th attempt
	r.advance(2*time.Hour + time.Second)
	r.setStatus(r.refOf(job, 7), provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeNoCapacity)
	if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Error("never_ran release of the take")
	}
	if r.ledger(b.Batch, "settle", "execution") != 0 || r.ledger(b.Batch, "settle", "execution_unknown") != 0 {
		t.Error("expired queue entries cost nothing")
	}
	if got, ceil := r.ledger(b.Batch, "reserve", ""), int64(10_000_000); got > ceil {
		t.Errorf("reserves %d exceed the ceiling", got)
	}
}

func TestRunner_NoCapacityAfter24h(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitErr(&httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: 11 * time.Hour})
	for range 3 {
		r.step()
		r.wantState(job, StateQueued)
		r.advance(11*time.Hour + time.Minute)
	}
	r.step() // 33 h after the first submit: past the window
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeNoCapacity)
	if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Error("never_ran release")
	}
	if r.ledger(b.Batch, "release", "not_accepted") != r.ledger(b.Batch, "reserve", "attempt_overhead") {
		t.Error("each rejected attempt releases its overhead")
	}
	if got := r.job(job).Attempts; got != 3 {
		t.Errorf("attempts %d", got)
	}
}

func TestRunner_NoCapacityFailsBatchRemainder(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: about fifteen simulated hours of ticks")
	}
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.NeverRan = NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}}
	}))
	b := r.seedBatch(batchSpec{jobs: 7, workersMax: 2})
	// no capacity for the later takes: they never get a slot (their turn is far away) while the first two
	// cycle through queue expiries
	r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE batch_id = ? AND take_index >= 2`, store.FormatTime(t0.Add(100*time.Hour)), b.Batch)
	// every attempt expires in the queue
	r.onSubmit(func(spec provider.TakeSpec) (provider.Ref, error) {
		ref := "ref-" + spec.AttemptID
		r.setStatus(ref, provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
		return provider.Ref{Backend: provider.BackendFake, ID: ref}, nil
	})
	var failedAt time.Time
	for i := 0; i < 200 && failedAt.IsZero(); i++ {
		// quarter-hour ticks: the scenario spans about fifteen hours
		r.step()
		for _, a := range r.liveSubmitted() {
			if r.clk.Now().Sub(a.SubmittedAt) > 2*time.Hour {
				r.setStatus(a.Ref, provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
			}
		}
		if r.state(b.Jobs[2]) == StateFailed {
			failedAt = r.clk.Now()
		}
		r.advance(15 * time.Minute)
	}
	if failedAt.IsZero() {
		t.Fatal("the never-submitted jobs never failed")
	}
	if d := failedAt.Sub(t0); d > 24*time.Hour {
		t.Errorf("remainder failed after %v, not with the first NO_CAPACITY", d)
	}
	for _, j := range b.Jobs[2:] {
		r.wantState(j, StateFailed)
		r.wantCode(j, CodeNoCapacity)
		if !r.hasEvent(j, EvBatchNoCapacity) {
			t.Errorf("events of %s: %v", j, r.events(j))
		}
	}
	if r.state(b.Jobs[0]) != StateFailed {
		t.Errorf("first job is %s", r.state(b.Jobs[0]))
	}
	if r.ledger(b.Batch, "release", "never_ran") < r.ledger(b.Batch, "reserve", "take")*5/7 {
		t.Error("the five never-submitted takes must be released")
	}
}

func (r *rig) liveSubmitted() []attRec {
	as, err := queryAtts(r.t.Context(), r.st.R, `state = 'submitted'`)
	if err != nil {
		r.t.Fatal(err)
	}
	return as
}

func TestRunner_NotSubmittedAfter24h(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{jobs: 2})
	r.advance(23*time.Hour + 59*time.Minute)
	r.step()
	r.wantState(b.Jobs[0], StateQueued)
	r.advance(2 * time.Minute)
	r.step()
	for _, j := range b.Jobs {
		r.wantState(j, StateFailed)
		r.wantCode(j, CodeNotSubmitted)
	}
	if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Error("released")
	}
	r.step() // the batch finishes: nothing ran
	if r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) != "finished" {
		t.Error("batch not finished")
	}
}

func TestRunner_BackoffGapReservesNewColdStart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitErr(&httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: 120 * time.Second}) // longer than IdleS
	r.step()
	r.wantState(job, StateQueued)
	r.onSubmit(nil)
	r.advance(2*time.Minute + time.Second)
	r.step()
	r.wantState(job, StateStarting)
	rows := r.count(`SELECT COUNT(DISTINCT attempt_id) FROM ledger WHERE job_id = ? AND reason = 'attempt_overhead'`, job)
	if rows != 2 {
		t.Errorf("attempt_overhead reserves for %d attempts, want 2: every cold start is reserved", rows)
	}
	// the rejected attempt's reserve is released, the live one stays
	if got, want := r.outstanding(b.Batch), r.ledger(b.Batch, "reserve", "take")+38334; got != want {
		t.Errorf("outstanding %d, want %d", got, want)
	}
}

func TestRunner_SubmitOutcomes(t *testing.T) {
	t.Parallel()
	t.Run("429 honours Retry-After", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		r.submitErr(&httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: 7 * time.Minute})
		r.step()
		if got := r.job(job).NextAttemptAt; !got.Equal(t0.Add(7 * time.Minute)) {
			t.Errorf("next %v", got)
		}
	})
	t.Run("network error and client timeout leave a ghost", func(t *testing.T) {
		t.Parallel()
		for name, err := range map[string]error{"network": errors.New("connection reset by peer"), "timeout": context.DeadlineExceeded, "wrapped timeout": errors.Join(errors.New("run"), context.DeadlineExceeded)} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				b := r.seedBatch(batchSpec{})
				r.submitErr(err)
				r.step()
				r.wantState(b.Jobs[0], StateQueued)
				if a := r.att(b.Jobs[0], 1); a.State != "unknown" {
					t.Errorf("attempt %+v", a)
				}
				if r.ledger(b.Batch, "reserve", "topup_ambiguous") == 0 {
					t.Error("no ambiguous top-up")
				}
			})
		}
	})
	t.Run("top-up refused by the ceiling: BUDGET_EXCEEDED after the ghost window", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		// room for the take and the first overhead, not for the ghost's top-up
		b := r.seedBatch(batchSpec{ceiling: 38525 + 38334 + 1000})
		job := b.Jobs[0]
		r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
		r.step()
		r.wantState(job, StateQueued)
		if r.ledger(b.Batch, "reserve", "topup_ambiguous") != 0 {
			t.Fatal("the top-up must have been refused")
		}
		r.onSubmit(nil)
		r.advance(2 * time.Minute)
		r.step()
		r.wantState(job, StateQueued) // the ghost is live and has no top-up: no resubmit
		if r.be.Calls().Submit != 1 {
			t.Errorf("resubmitted without a top-up: %d submits", r.be.Calls().Submit)
		}
		r.advance(4 * time.Hour)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeBudgetExceeded)
	})
	t.Run("1010 stops further submits", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{jobs: 3, workersMax: 2})
		r.submitErr(&httpErr{Code: "EDGE_BLOCKED", Status: 403})
		r.steps(3)
		if got := r.be.Calls().Submit; got != 1 {
			t.Errorf("submits %d, want exactly 1", got)
		}
		r.wantState(b.Jobs[0], StateFailed)
		r.wantCode(b.Jobs[0], CodeEdgeBlocked)
		r.wantState(b.Jobs[1], StateQueued)
	})
	t.Run("401 trips and fails", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		r.submitErr(&httpErr{Code: "UNAUTHORIZED", Status: 401})
		r.step()
		r.wantCode(b.Jobs[0], CodeProviderAuth)
		if r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'provider_access'`) != 1 {
			t.Error("trip")
		}
	})
	t.Run("a backend that returns no reference leaves a ghost", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		r.onSubmit(func(provider.TakeSpec) (provider.Ref, error) { return provider.Ref{}, nil })
		r.step()
		r.wantState(b.Jobs[0], StateQueued)
		if r.att(b.Jobs[0], 1).State != "unknown" {
			t.Error("attempt")
		}
	})
}
