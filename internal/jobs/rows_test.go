package jobs

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestTransition_Row01(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm() // nothing is claimed in this test
	b := r.seedBatch(batchSpec{state: "awaiting_confirmation"})
	job := b.Jobs[0]
	r.step()
	r.wantState(job, StateAwaiting)
	r.exec(`UPDATE approvals SET state='approved', via='web', decided_by='human:h1', decided_at=? WHERE batch_id=?`, store.FormatTime(t0), b.Batch)
	r.exec(`UPDATE batches SET state='active' WHERE id=?`, b.Batch)
	r.step()
	r.wantState(job, StateQueued)
	if r.job(job).QueuedAt == nil {
		t.Error("queued_at not set")
	}
	if !r.hasEvent(job, EvStatus) {
		t.Errorf("events %v", r.events(job))
	}
}

func TestTransition_Row02(t *testing.T) {
	t.Parallel()
	t.Run("rejected batch", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{state: "rejected"})
		r.step()
		r.wantState(b.Jobs[0], StateCancelled)
		r.wantCode(b.Jobs[0], CodeRejected)
		if n := r.ledger(b.Batch, "release", ""); n != 0 {
			t.Errorf("a rejected batch is released by its operation, not the runner: %d", n)
		}
	})
	t.Run("cancel intent on an awaiting job releases its reserve", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 2})
		r.cancelIntent(b.Jobs[0])
		r.step()
		r.wantState(b.Jobs[0], StateCancelled)
		r.wantCode(b.Jobs[0], CodeCancelledByUser)
		r.wantState(b.Jobs[1], StateAwaiting)
		if got, want := r.ledger(b.Batch, "release", "never_ran"), r.ledger(b.Batch, "reserve", "take")/2; got != want {
			t.Errorf("released %d, want %d", got, want)
		}
	})
}

func TestTransition_Row03(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{state: "expired"})
	r.step()
	r.wantState(b.Jobs[0], StateExpired)
	r.wantCode(b.Jobs[0], CodeApprovalExpired)
}

func TestTransition_Row04(t *testing.T) {
	t.Parallel()
	crash := errors.New("crash")
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.Fault = func(p string) error {
			if p == FaultAfterClaimCommit {
				return crash
			}
			return nil
		}
	}))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	if _, err := r.run.Step(t.Context()); !errors.Is(err, crash) {
		t.Fatalf("Step: %v", err)
	}
	r.wantState(job, StateSubmitting)
	a := r.att(job, 1)
	if a.State != "pending" || a.Ref != "" || a.Price != testPrice {
		t.Errorf("attempt %+v", a)
	}
	if r.job(job).LeaseOwner != "runner-1" || r.job(job).FirstSubmit == nil || r.job(job).Attempts != 1 {
		t.Errorf("job %+v", r.job(job))
	}
	if n := r.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'open'`, a.ID); n != 6+5 {
		t.Errorf("open uploads %d", n)
	}
	if got := r.ledger(b.Batch, "reserve", "attempt_overhead"); got != 38334 {
		t.Errorf("overhead reserve %d", got)
	}
	if r.be.Calls().Submit != 0 {
		t.Error("submit before the claim committed")
	}
	if !a.WindowEndsAt.Equal(t0.Add(2*time.Hour + 201*time.Second + 15*time.Minute)) {
		t.Errorf("window ends %v", a.WindowEndsAt)
	}
}

func TestTransition_Row05(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{jobs: 2})
	r.cancelIntent(b.Jobs[0])
	r.step()
	r.wantState(b.Jobs[0], StateCancelled)
	r.wantCode(b.Jobs[0], CodeCancelledByUser)
	r.wantState(b.Jobs[1], StateQueued)
	if got := r.ledger(b.Batch, "release", "never_ran"); got != r.ledger(b.Batch, "reserve", "take")/2 {
		t.Errorf("never_ran release %d", got)
	}
}

func TestTransition_Row05b(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	ghost := r.ghost(job, "unknown", true)
	r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost) // a ghost whose status was lost keeps its ref
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	if r.cancelCount("ghost-ref") != 1 {
		t.Errorf("cancel POSTs %d", r.cancelCount("ghost-ref"))
	}
	r.wantCode(job, CodeCancelledByUser)
}

func TestTransition_Row06(t *testing.T) {
	t.Parallel()
	t.Run("NOT_SUBMITTED", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		b := r.seedBatch(batchSpec{})
		r.step()
		r.wantState(b.Jobs[0], StateQueued)
		r.advance(24*time.Hour + time.Minute)
		r.step()
		r.wantState(b.Jobs[0], StateFailed)
		r.wantCode(b.Jobs[0], CodeNotSubmitted)
		if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
			t.Error("reserve not released")
		}
	})
	t.Run("BUDGET_EXCEEDED from a refused claim reserve", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		// the ceiling fits the take reserve but not the attempt overhead
		b := r.seedBatch(batchSpec{ceiling: 40000})
		r.step()
		r.wantState(b.Jobs[0], StateFailed)
		r.wantCode(b.Jobs[0], CodeBudgetExceeded)
		if r.be.Calls().Submit != 0 || r.count(`SELECT COUNT(*) FROM job_attempts`) != 0 {
			t.Error("a refused claim left an attempt or reached the backend")
		}
		if r.ledger(b.Batch, "reserve", "attempt_overhead") != 0 {
			t.Error("overhead reserved despite the refusal")
		}
	})
	t.Run("PRICE_CHANGED", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{ceiling: 40000}) // room for the take (38525), not for the price top-up
		r.exec(`UPDATE provider_state SET quote_json = json_set(quote_json, '$.ListPricePPH', 900000) WHERE backend = 'fake'`)
		r.step()
		r.wantState(b.Jobs[0], StateFailed)
		r.wantCode(b.Jobs[0], CodePriceChanged)
		if r.be.Calls().Submit != 0 {
			t.Error("submit after a price refusal")
		}
	})
	t.Run("NO_CAPACITY after 24 h", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.exec(`UPDATE jobs SET first_submitted_at = ?, attempts = 1 WHERE id = ?`, store.FormatTime(t0.Add(-25*time.Hour)), job)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeNoCapacity)
	})
	t.Run("BUDGET_EXCEEDED once the ghost window closed without a top-up", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.ghost(job, "unknown", false)
		r.step()
		r.wantState(job, StateQueued) // the ghost is live: wait
		r.advance(4 * time.Hour)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeBudgetExceeded)
	})
}

func TestTransition_Row06b(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{jobs: 4, workersMax: 1})
	r.exec(`UPDATE jobs SET first_submitted_at = ?, attempts = 1, queue_expiries = 7 WHERE id = ?`, store.FormatTime(t0), b.Jobs[0])
	// job 0 already used its 6 expiries, so it fails NO_CAPACITY now and drags the never-submitted rest along
	r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE id = ?`, store.FormatTime(t0.Add(48*time.Hour)), b.Jobs[0])
	r.step()
	for _, j := range b.Jobs {
		r.wantState(j, StateFailed)
		r.wantCode(j, CodeNoCapacity)
	}
	if !r.hasEvent(b.Jobs[1], EvBatchNoCapacity) {
		t.Errorf("events %v", r.events(b.Jobs[1]))
	}
	if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Error("the takes of all four jobs must be released")
	}
}

func TestTransition_Row07(t *testing.T) {
	t.Parallel()
	for _, from := range []string{StateStarting, StateGenerating, StateQueued} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			spec := r.submitOne(job)
			ref := r.refOf(job, 1)
			if from == StateGenerating {
				r.setStatus(ref, provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
				r.advance(10 * time.Second)
				r.step()
				r.wantState(job, StateGenerating)
			}
			if from == StateQueued {
				// a ghost that finished and uploaded while the job is queued
				r.exec(`UPDATE job_attempts SET state = 'unknown' WHERE id = ?`, r.att(job, 1).ID)
				r.exec(`UPDATE jobs SET state = 'queued', lease_owner = NULL, lease_expires_at = NULL WHERE id = ?`, job)
				r.exec(`UPDATE jobs SET next_attempt_at = ? WHERE id = ?`, store.FormatTime(t0.Add(72*time.Hour)), job)
			}
			// config.json is never uploaded: the result is verified but the transfer cannot finish yet
			r.deliver(spec, deliverOpts{skip: map[string]bool{"config.json": true}})
			r.setStatus(ref, provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
			r.advance(time.Second)
			r.step()
			r.wantState(job, StateTransferring)
			if !r.hasEvent(job, EvResultReceived) {
				t.Errorf("no result_received event: %v", r.events(job))
			}
			// the winner is the attempt whose result arrived
			if r.winnerOf(job) != r.att(job, 1).ID {
				t.Error("winner")
			}
		})
	}
}

func TestTransition_Row08(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	a := r.att(job, 1)
	if a.State != "submitted" || a.Ref != "ref-"+spec.AttemptID {
		t.Errorf("attempt %+v", a)
	}
	j := r.job(job)
	if j.StateDeadline == nil || !j.StateDeadline.Equal(t0.Add(2*time.Hour+5*time.Minute)) {
		t.Errorf("starting deadline %v", j.StateDeadline)
	}
	if got := r.be.Calls().Submit; got != 1 {
		t.Errorf("submits %d", got)
	}
	if !spec.Deadline.Equal(a.WindowEndsAt) || spec.TimeoutS != 186 || spec.Engine != "yue2" || spec.UploadBudgetS != 60 {
		t.Errorf("spec %+v", spec)
	}
	if len(spec.Uploads) != 11 || spec.StartURL == "" {
		t.Errorf("uploads %d start %q", len(spec.Uploads), spec.StartURL)
	}
}

func TestTransition_Row08b(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	// an ambiguous submit left an unknown attempt; its uploads exist and the job is queued again
	spec := r.makeGhost(job)
	if code := r.start(spec, "late-ref-1"); code != 200 {
		t.Fatalf("start: %d", code)
	}
	r.step()
	r.wantState(job, StateStarting)
	a := r.att(job, 1)
	if a.State != "submitted" || a.Ref != "late-ref-1" {
		t.Errorf("promoted attempt %+v", a)
	}
	if !r.hasEvent(job, EvGhostStarted) {
		t.Errorf("events %v", r.events(job))
	}
}

// makeGhost drives an ambiguous submit (5xx): the job returns to the queue with an unknown attempt, a
// topup_ambiguous reserve and live uploads. It returns the TakeSpec the backend saw.
func (r *rig) makeGhost(job string) provider.TakeSpec {
	r.t.Helper()
	r.arm()
	r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
	r.step()
	r.wantState(job, StateQueued)
	r.disarm()
	r.onSubmit(nil)
	return r.lastSpec()
}

func TestTransition_Row09(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		err  error
		min  time.Duration
	}{
		{"429 with Retry-After", &httpErr{Code: "RATE_LIMITED", Status: 429, RetryAfter: 5 * time.Minute}, 5 * time.Minute},
		{"paused", &httpErr{Code: "ENDPOINT_PAUSED", Status: 409}, time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			r.submitErr(c.err)
			r.step()
			r.wantState(job, StateQueued)
			a := r.att(job, 1)
			if a.State != "rejected" || a.TerminalBasis != "not_accepted" {
				t.Errorf("attempt %+v", a)
			}
			if got := r.job(job).NextAttemptAt; !got.Equal(t0.Add(c.min)) {
				t.Errorf("next attempt %v, want %v", got, t0.Add(c.min))
			}
			if r.ledger(b.Batch, "release", "not_accepted") != r.ledger(b.Batch, "reserve", "attempt_overhead") {
				t.Error("the attempt overhead must be released")
			}
			if r.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'open'`, a.ID) != 0 {
				t.Error("uploads of a rejected attempt stay open")
			}
			// not before the delay
			r.onSubmit(nil)
			r.advance(c.min - time.Second)
			r.step()
			r.wantState(job, StateQueued)
			r.advance(2 * time.Second)
			r.step()
			r.wantState(job, StateStarting)
			if r.job(job).Attempts != 2 {
				t.Error("second attempt")
			}
		})
	}
	t.Run("404 trips provider_access", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		r.submitErr(&httpErr{Code: "NOT_FOUND", Status: 404})
		r.step()
		r.wantState(b.Jobs[0], StateQueued)
		if r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'provider_access' AND reset_at IS NULL`) != 1 {
			t.Error("no provider_access trip")
		}
		// a tripped provider claims nothing
		r.onSubmit(nil)
		r.advance(time.Hour)
		r.step()
		r.wantState(b.Jobs[0], StateQueued)
		if r.be.Calls().Submit != 1 {
			t.Error("claim while tripped")
		}
	})
	t.Run("5xx leaves a ghost with a top-up", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
		r.step()
		r.wantState(job, StateQueued)
		if a := r.att(job, 1); a.State != "unknown" || a.Ref != "" {
			t.Errorf("attempt %+v", a)
		}
		if r.ledger(b.Batch, "reserve", "topup_ambiguous") != r.ledger(b.Batch, "reserve", "take") {
			t.Error("ambiguous top-up")
		}
		if r.ledger(b.Batch, "release", "not_accepted") != 0 {
			t.Error("a ghost's overhead is not released")
		}
	})
}

func TestTransition_Row10(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		err  error
		code string
		trip bool
	}{
		{"401", &httpErr{Code: "UNAUTHORIZED", Status: 401}, CodeProviderAuth, true},
		{"1010", &httpErr{Code: "EDGE_BLOCKED", Status: 403}, CodeEdgeBlocked, true},
		{"400", &httpErr{Code: "BAD_REQUEST", Status: 400}, CodeSubmitRejected, false},
		{"access sentinel", provider.ErrAccess, CodeProviderAuth, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			r.submitErr(c.err)
			r.step()
			r.wantState(job, StateFailed)
			r.wantCode(job, c.code)
			if a := r.att(job, 1); a.State != "rejected" {
				t.Errorf("attempt %+v", a)
			}
			if got := r.ledger(b.Batch, "release", "never_ran"); got != r.ledger(b.Batch, "reserve", "take") {
				t.Errorf("never_ran %d", got)
			}
			if trips := r.count(`SELECT COUNT(*) FROM provider_trips WHERE reset_at IS NULL`); (trips == 1) != c.trip {
				t.Errorf("trips %d", trips)
			}
		})
	}
}

func TestTransition_Row11(t *testing.T) {
	t.Parallel()
	var crash atomic.Bool
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.Fault = func(p string) error {
			if crash.Load() && p == FaultAfterClaimCommit {
				return errors.New("crash")
			}
			return nil
		}
	}))
	job := r.seedBatch(batchSpec{}).Jobs[0]
	crash.Store(true)
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("the fault did not fire")
	}
	crash.Store(false)
	// the state adoption may meet: submitting with a ref (the /run returned, the outcome was not written)
	r.exec(`UPDATE job_attempts SET state = 'submitted', ref = 'ref-late' WHERE job_id = ?`, job)
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	if r.cancelCount("ref-late") != 1 {
		t.Error("cancel POST")
	}
}

func TestTransition_Row12(t *testing.T) {
	t.Parallel()
	t.Run("running status", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		r.submitOne(job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
		r.advance(6 * time.Second)
		r.step()
		r.wantState(job, StateGenerating)
		j := r.job(job)
		if j.StartedAt == nil || !r.att(job, 1).InProgressSeen {
			t.Errorf("started_at %v seen %v", j.StartedAt, r.att(job, 1).InProgressSeen)
		}
		if j.StateDeadline == nil || !j.StateDeadline.Equal(j.StartedAt.Add(186*time.Second+15*time.Second+2*time.Minute)) {
			t.Errorf("generating deadline %v", j.StateDeadline)
		}
	})
	t.Run("start announcement", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		spec := r.submitOne(job)
		if code := r.start(spec, r.refOf(job, 1)); code != 200 {
			t.Fatalf("start %d", code)
		}
		r.advance(6 * time.Second)
		r.step() // promotes (nothing to promote) and polls Queued: announced, so generating
		r.wantState(job, StateGenerating)
	})
}

func TestTransition_Row13(t *testing.T) {
	t.Parallel()
	shapes := NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}}
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.NeverRan = shapes }))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitOne(job)
	ref := r.refOf(job, 1)
	r.advance(2*time.Hour + time.Second)
	r.setStatus(ref, provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
	r.step()
	r.wantState(job, StateQueued)
	j := r.job(job)
	if j.QueueExpiries != 1 || !j.NextAttemptAt.Equal(r.clk.Now().Add(time.Minute)) {
		t.Errorf("expiries %d next %v", j.QueueExpiries, j.NextAttemptAt)
	}
	if a := r.att(job, 1); a.State != "done" || a.TerminalBasis != "provider" || a.ExecutionMS == nil || *a.ExecutionMS != 0 || !a.Settled {
		t.Errorf("attempt %+v", a)
	}
	if r.ledger(b.Batch, "settle", "") != 0 {
		t.Error("an expired queue entry costs nothing")
	}
	if !r.hasEvent(job, EvQueueExpired) {
		t.Errorf("events %v", r.events(job))
	}
}
