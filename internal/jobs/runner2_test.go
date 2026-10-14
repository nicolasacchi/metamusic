package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

func TestRunner_RanBetweenPolls(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	// never seen Running: the job ran entirely between two polls
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 71500))
	r.advance(6 * time.Second)
	r.step()
	r.wantState(job, StateCompleted)
	if r.be.Calls().Submit != 1 {
		t.Errorf("resubmitted: %d submits", r.be.Calls().Submit)
	}
	if got, want := r.ledger(b.Batch, "settle", "execution"), int64(money.ForMillis(71500, testPrice)); got != want {
		t.Errorf("settled %d, want %d", got, want)
	}
	if r.ledger(b.Batch, "release", "never_ran") != 0 {
		t.Error("never_ran after a run")
	}
	if a := r.att(job, 1); a.InProgressSeen || a.State != "done" {
		t.Errorf("attempt %+v", a)
	}
}

func TestRunner_404AfterDowntimeIsUnknown(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) {
		o.NeverRan = NeverRanShapes{TTLExpiry404: true, TTLExpiryStates: []provider.RemoteState{provider.TimedOut}}
	}))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitOne(job)
	ref := r.refOf(job, 1)
	r.advance(10 * time.Second)
	r.setStatus(ref, provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
	r.step() // a Queued sighting at t0+10s
	// the runner is down for 40 minutes; results of finished jobs are purged after 30 minutes
	r.advance(40 * time.Minute)
	r.setStatus(ref, provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"})
	r.step()
	a := r.att(job, 1)
	if a.State != "unknown" || a.Ref != ref {
		t.Fatalf("attempt %+v: a 404 after downtime is unknown, never a queue expiry", a)
	}
	r.wantState(job, StateQueued)
	if r.job(job).QueueExpiries != 0 {
		t.Error("counted as a queue expiry")
	}
	if got := r.ledger(b.Batch, "reserve", "topup_ambiguous"); got != r.ledger(b.Batch, "reserve", "take") {
		t.Errorf("ambiguous top-up %d before any resubmit", got)
	}
	if r.be.Calls().Submit != 1 {
		t.Error("resubmitted before the backoff")
	}
	if r.ledger(b.Batch, "release", "never_ran") != 0 {
		t.Error("never_ran after a 404")
	}
	// the resubmit comes after the backoff, with a top-up in place
	r.advance(2 * time.Minute)
	r.step()
	r.wantState(job, StateStarting)
	if r.be.Calls().Submit != 2 {
		t.Errorf("submits %d", r.be.Calls().Submit)
	}
	// the lost attempt is settled at its time bound when its window closes
	r.advance(4 * time.Hour)
	r.step()
	if r.att(job, 1).State != "done" || r.att(job, 1).TerminalBasis != "time_bound" {
		t.Errorf("attempt %+v", r.att(job, 1))
	}
	if r.ledger(b.Batch, "settle", "execution_unknown") == 0 {
		t.Error("time-bound settle")
	}
}

func TestRunner_TimedOutUnseenSettlesTimeout(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	r.deliver(spec, deliverOpts{noResult: true, skip: map[string]bool{"result.json": true}}) // an upload arrived
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT"})
	r.advance(6 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeDeadline)
	if got, want := r.ledger(b.Batch, "settle", "execution_unknown"), int64(money.ForSeconds(186+15, testPrice)); got != want {
		t.Errorf("settled %d, want %d", got, want)
	}
}

func TestRunner_ProviderRequeueCancelled(t *testing.T) {
	t.Parallel()
	r, b, job, _ := generating(t)
	ref := r.refOf(job, 1)
	r.setStatus(ref, provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateFailing)
	if r.cancelCount(ref) < 1 {
		t.Error("the requeued job must be cancelled at once")
	}
	// the cancelled requeue may have run twice: execution up to 2 x timeout fits pot + top-up
	r.setStatus(ref, provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(380000)})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerLost)
	if r.ledger(b.Batch, "settle", "excess") != 0 {
		t.Error("the topup_requeue reserve must cover the second execution")
	}
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Error("no trip: the top-up was granted")
	}
}

func TestRunner_ProviderRequeueRefusedTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// the ceiling holds the take and the overhead but no requeue top-up
	b := r.seedBatch(batchSpec{ceiling: 38525 + 38334 + 500})
	job := b.Jobs[0]
	r.submitOne(job)
	r.setStatus(r.refOf(job, 1), running())
	r.advance(6 * time.Second)
	r.step()
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateFailing)
	if r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'provisional_exceeds_reserved' AND reset_at IS NULL`) != 1 {
		t.Error("a refused topup_requeue must trip the provider")
	}
}

func TestRunner_PromotesAnnouncedGhost(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.makeGhost(job)
	if a := r.att(job, 1); a.State != "unknown" || a.Ref != "" {
		t.Fatalf("attempt %+v", a)
	}
	r.arm()
	r.setQuote(func(q *provider.Quote) { q.Endpoint.WorkersMax = 2 })
	if code := r.start(spec, "late-ref"); code != 200 {
		t.Fatalf("start %d", code)
	}
	r.step()
	r.wantState(job, StateStarting)
	if a := r.att(job, 1); a.State != "submitted" || a.Ref != "late-ref" {
		t.Errorf("attempt %+v", a)
	}
	// no second submit: the promoted ghost is the job's attempt
	r.step()
	if r.be.Calls().Submit != 1 {
		t.Errorf("submits %d", r.be.Calls().Submit)
	}
	// it is now polled and can be cancelled
	r.setStatus("late-ref", running())
	r.advance(11 * time.Second)
	r.step()
	if r.pollCount("late-ref") == 0 {
		t.Error("the promoted attempt is never polled")
	}
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	if r.cancelCount("late-ref") != 1 {
		t.Error("cancel")
	}
	// the unused topup_ambiguous stays outstanding
	if r.ledger(b.Batch, "reserve", "topup_ambiguous") == 0 {
		t.Error("topup")
	}
}

func TestRunner_Deadlines(t *testing.T) {
	t.Parallel()
	t.Run("submitting 2 minutes", func(t *testing.T) {
		t.Parallel()
		var crash = true
		r := newRig(t, withOpts(func(o *RunnerOptions) {
			o.Fault = func(p string) error {
				if crash && p == FaultAfterSubmitReturn {
					return context.Canceled
				}
				return nil
			}
		}))
		job := r.seedBatch(batchSpec{}).Jobs[0]
		if _, err := r.run.Step(t.Context()); err == nil {
			t.Fatal("fault")
		}
		crash = false
		r.wantState(job, StateSubmitting)
		r.advance(time.Minute)
		r.step()
		r.wantState(job, StateSubmitting)
		r.advance(90 * time.Second)
		r.step()
		r.wantState(job, StateQueued)
		if a := r.att(job, 1); a.State != "unknown" {
			t.Errorf("the pending attempt becomes a ghost: %+v", a)
		}
	})
	t.Run("starting at TTL + 5 minutes", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		r.submitOne(job)
		ref := r.refOf(job, 1)
		r.advance(2*time.Hour + 4*time.Minute)
		r.step()
		if r.cancelCount(ref) != 0 {
			t.Error("cancelled before the deadline")
		}
		r.advance(2 * time.Minute)
		r.step()
		if r.cancelCount(ref) != 1 || !r.hasEvent(job, EvDeadline) {
			t.Errorf("cancels %d events %v", r.cancelCount(ref), r.events(job))
		}
		r.step() // only once per attempt
		r.advance(6 * time.Second)
		r.step()
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'deadline'`, job); n != 1 {
			t.Errorf("deadline events %d", n)
		}
	})
	t.Run("transferring 15 minutes without a Completed", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		r.deliver(spec, deliverOpts{skip: map[string]bool{"config.json": true}})
		r.step()
		r.wantState(job, StateTransferring)
		r.advance(14 * time.Minute)
		r.step()
		r.wantState(job, StateTransferring)
		r.advance(2 * time.Minute)
		r.step()
		// the winner's provider status has not arrived: the job fails only once that attempt is over (row 19b)
		r.wantState(job, StateFailing)
		r.wantCode(job, CodeArtifactMissing)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(1000)})
		r.advance(3 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeArtifactMissing)
	})
}

func TestRunner_CancelPaths(t *testing.T) {
	t.Parallel()
	t.Run("awaiting", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{state: "awaiting_confirmation"})
		r.cancelIntent(b.Jobs[0])
		r.step()
		r.wantState(b.Jobs[0], StateCancelled)
	})
	t.Run("queued", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		b := r.seedBatch(batchSpec{})
		r.cancelIntent(b.Jobs[0])
		r.step()
		r.wantState(b.Jobs[0], StateCancelled)
	})
	t.Run("queued with a ghost", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		spec := r.makeGhost(job)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		// the ghost announces itself: it is told to stop (410) and, once it has a ref, cancelled
		if code := r.start(spec, "late-ref"); code != 410 {
			t.Errorf("start of a cancelled job's ghost: %d", code)
		}
		r.advance(3 * time.Hour)
		r.step()
		r.wantState(job, StateCancelled)
	})
	t.Run("generating and cancel_ignored", func(t *testing.T) {
		t.Parallel()
		r, _, job, _ := generating(t)
		ref := r.refOf(job, 1)
		r.cancelIntent(job)
		r.step()
		for range 5 {
			r.advance(20 * time.Second)
			r.step()
		}
		r.wantState(job, StateCancelling) // the provider never confirms: never cancelled early
		if r.cancelCount(ref) < 2 {
			t.Errorf("cancel POSTs %d", r.cancelCount(ref))
		}
		r.setStatus(ref, provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(1000)})
		r.advance(3 * time.Second)
		r.step()
		r.wantState(job, StateCancelled)
	})
}

func TestRunner_CancelReissuedEvery30s(t *testing.T) {
	t.Parallel()
	r, _, job, _ := generating(t)
	ref := r.refOf(job, 1)
	r.cancelIntent(job)
	r.step()
	if r.cancelCount(ref) != 1 {
		t.Fatalf("POSTs %d", r.cancelCount(ref))
	}
	r.advance(10 * time.Second)
	r.step()
	r.advance(10 * time.Second)
	r.step()
	if r.cancelCount(ref) != 1 {
		t.Errorf("POSTs %d inside 30 s", r.cancelCount(ref))
	}
	r.advance(11 * time.Second)
	r.step()
	if r.cancelCount(ref) != 2 {
		t.Errorf("POSTs %d after 31 s", r.cancelCount(ref))
	}
	for i := 0; i < 9; i++ {
		r.advance(30 * time.Second)
		r.step()
	}
	if r.cancelCount(ref) != 11 {
		t.Errorf("POSTs %d after 5 minutes", r.cancelCount(ref))
	}
	// the 5-minute ntfy fires once
	n := 0
	for _, m := range r.notes.all() {
		if m.Title == "Cancel not confirmed yet" {
			n++
			if m.Priority != 4 || !strings.HasSuffix(m.Click, "/jobs/"+job) {
				t.Errorf("message %+v", m)
			}
		}
	}
	if n != 1 {
		t.Errorf("unconfirmed-cancel notifications %d", n)
	}
}

func TestRunner_FirstVerifiedResultWins(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	ghost := r.ghost(job, "unknown", true)
	r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
	r.setStatus("ghost-ref", running())
	// the ghost's render fails inside the worker: an error mm-result.json is only an event
	r.exec(`UPDATE jobs SET attempts = 2 WHERE id = ?`, job)
	gu := r.ghostUploads(job, ghost)
	ghostSpec := provider.TakeSpec{JobID: job, AttemptID: ghost, Expect: spec.Expect, Uploads: gu}
	r.deliver(ghostSpec, deliverOpts{errorOut: true})
	r.step()
	r.wantState(job, StateGenerating)
	if !r.hasEvent(job, EvStatus) {
		t.Errorf("events %v", r.events(job))
	}
	// the first attempt delivers a verified result: it wins, the ghost is cancelled
	r.deliver(spec, deliverOpts{skip: map[string]bool{"config.json": true}})
	r.step()
	r.wantState(job, StateTransferring)
	if r.cancelCount("ghost-ref") != 1 {
		t.Errorf("the other live attempt must be cancelled: %d", r.cancelCount("ghost-ref"))
	}
	if r.winnerOf(job) != r.att(job, 1).ID {
		t.Error("winner")
	}
	// later uploads of the loser answer 410
	for _, u := range gu {
		if code := r.put(u.URL, []byte("late")); code != 410 && code != 409 {
			t.Errorf("late upload of the losing attempt: %d", code)
		}
	}
}

// ghostUploads mints the capability URLs of a seeded attempt the way the runner would have.
func (r *rig) ghostUploads(job, attempt string) map[string]provider.UploadTarget {
	r.t.Helper()
	exp := r.clk.Now().Add(3 * time.Hour).Unix()
	ups, _, err := r.up.Mint(upload.JobRef{JobID: job}, attempt, exp)
	if err != nil {
		// the seeded attempt has no upload rows yet: open them
		r.openUploads(job, attempt, exp)
		ups, _, err = r.up.Mint(upload.JobRef{JobID: job}, attempt, exp)
		if err != nil {
			r.t.Fatal(err)
		}
	}
	return ups
}

func TestRunner_PriceTopUp(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.setQuote(func(q *provider.Quote) { q.ListPricePPH = 800000 })
	r.step()
	r.wantState(job, StateStarting)
	want := int64(money.ForSeconds(201, 800000) - money.ForSeconds(201, testPrice))
	if got := r.ledger(b.Batch, "reserve", "topup_price"); got != want {
		t.Errorf("topup_price %d, want %d", got, want)
	}
	if a := r.att(job, 1); a.Price != 800000 {
		t.Errorf("attempt price %d", a.Price)
	}
	if got, w := r.ledger(b.Batch, "reserve", "attempt_overhead"), int64(money.ForSeconds(200, 800000)); got != w {
		t.Errorf("overhead %d, want %d at the new price", got, w)
	}
	// settlement uses the price in force
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(100000)})
	r.advance(6 * time.Second)
	r.step()
	if got, w := r.ledger(b.Batch, "settle", "execution"), int64(money.ForMillis(100000, 800000)); got != w {
		t.Errorf("settled %d, want %d", got, w)
	}
	if r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'runner:topup'`) != 0 && r.o().Audit == nil {
		t.Error("audit")
	}
}

func (r *rig) o() RunnerOptions { return r.opts }

func TestRunner_SettleOnTerminalAttempts(t *testing.T) {
	t.Parallel()
	for _, ex := range []int64{1, 999, 70001, 201000} {
		t.Run(fmt.Sprint(ex), func(t *testing.T) {
			t.Parallel()
			r, b, job, _ := generating(t)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(ex)})
			r.advance(11 * time.Second)
			r.step()
			if got, want := r.ledger(b.Batch, "settle", "execution"), int64(money.ForMillis(ex, testPrice)); got != want {
				t.Errorf("exec %d ms: settled %d, want %d", ex, got, want)
			}
			if a := r.att(job, 1); !a.Settled || a.ExecutionMS == nil || *a.ExecutionMS != ex {
				t.Errorf("attempt %+v", a)
			}
		})
	}
	// settled exactly once
	r, b, job, _ := generating(t)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(5000)})
	r.advance(11 * time.Second)
	r.steps(3)
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND reason = 'execution'`, b.Batch); n != 1 {
		t.Errorf("execution rows %d", n)
	}
}

type fixedMeter struct {
	ready   bool
	seconds int64
	calls   int
}

func (m *fixedMeter) OverheadSeconds(context.Context, Querier, string) (int64, bool, error) {
	m.calls++
	return m.seconds, m.ready, nil
}

func TestRunner_BatchFinish(t *testing.T) {
	t.Parallel()
	meter := &fixedMeter{}
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Meter = meter }))
	b := r.seedBatch(batchSpec{jobs: 2, workersMax: 2})
	r.step()
	for i := range 2 {
		spec := r.be.Specs()[i]
		if i == 0 {
			out := r.deliver(spec, deliverOpts{})
			r.setStatus("ref-"+spec.AttemptID, completed(out, 70000))
		} else {
			r.setStatus("ref-"+spec.AttemptID, provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(20000)})
		}
	}
	r.advance(6 * time.Second)
	r.step()
	r.wantState(b.Jobs[0], StateCompleted)
	r.wantState(b.Jobs[1], StateFailed)
	// the meter still sees a running worker: the batch waits
	r.step()
	if r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) != "active" || meter.calls == 0 {
		t.Fatal("the batch finished before the meter was ready")
	}
	// overhead above what was reserved is capped at the reserves
	meter.ready, meter.seconds = true, 100000
	rep := r.step()
	if rep.Finished != 1 {
		t.Fatalf("finished %d", rep.Finished)
	}
	if r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) != "finished" {
		t.Fatal("batch not finished")
	}
	reserved := r.ledger(b.Batch, "reserve", "attempt_overhead")
	if got := r.ledger(b.Batch, "settle", "overhead"); got != reserved {
		t.Errorf("overhead settled %d, capped at the %d reserved", got, reserved)
	}
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'residual'`, b.Batch); n != 1 {
		t.Errorf("residual rows %d", n)
	}
	// one summary ntfy
	var batchNotes []string
	for _, m := range r.notes.all() {
		if m.Title == "Batch done" {
			batchNotes = append(batchNotes, m.Body)
			if m.Click != testPublicURL+"/batches/"+b.Batch || m.Priority != 3 {
				t.Errorf("message %+v", m)
			}
		}
	}
	if len(batchNotes) != 1 || !strings.HasPrefix(batchNotes[0], "1 completed, 1 failed - $") || !strings.HasSuffix(batchNotes[0], "provisional") {
		t.Errorf("summary %q", batchNotes)
	}
	r.steps(2)
	if len(r.notes.all()) != 1 {
		t.Errorf("notes %d after more ticks", len(r.notes.all()))
	}
}

func TestRunner_BatchFinishNothingRan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	b := r.seedBatch(batchSpec{})
	r.cancelIntent(b.Jobs[0])
	r.steps(2)
	if r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) != "finished" {
		t.Fatal("not finished")
	}
	if r.ledger(b.Batch, "settle", "overhead") != 0 || r.outstanding(b.Batch) != 0 {
		t.Errorf("overhead %d outstanding %d", r.ledger(b.Batch, "settle", "overhead"), r.outstanding(b.Batch))
	}
}

func TestRunner_NeverRanRelease(t *testing.T) {
	t.Parallel()
	t.Run("rejected attempts release", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		r.submitErr(&httpErr{Code: "UNAUTHORIZED", Status: 401})
		r.step()
		if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
			t.Error("rejected only")
		}
	})
	t.Run("a pinned queue expiry releases, a ran attempt does not", func(t *testing.T) {
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
		// second attempt ran for a bit and failed: the job cannot release
		r.advance(time.Minute + time.Second)
		r.step()
		r.setStatus(r.refOf(job, 2), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(9000)})
		r.advance(6 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		if r.ledger(b.Batch, "release", "never_ran") != 0 {
			t.Error("released although an attempt ran")
		}
	})
	t.Run("a time-bound close never releases", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
		r.step()
		r.disarm()
		r.advance(4 * time.Hour)
		r.step()
		r.wantState(job, StateQueued) // the ghost closed at its time bound; its top-up covers a resubmit
		r.advance(25 * time.Hour)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeNoCapacity)
		if r.ledger(b.Batch, "release", "never_ran") != 0 {
			t.Error("a ghost closed by its window may have run")
		}
	})
}

func TestRunner_EventsNoSecrets(t *testing.T) {
	t.Parallel()
	r, b, job, spec := generating(t)
	ref := r.refOf(job, 1)
	r.start(spec, ref)
	r.setStatus(ref, provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1000), ErrorText: "boom https://x.example/v2/ep/status/" + ref + "?sig=abc&exp=1 Bearer abcdefghijkl"})
	r.advance(11 * time.Second)
	r.step()
	rows, err := r.st.R.Query(`SELECT COALESCE(detail_json, '') || ' ' || COALESCE(code, '') FROM job_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		n++
		for _, bad := range []string{"sig=abc", "exp=1", ref, "abcdefghijkl", "/worker/v1", "\"url\"", "\"ref\"", "announced_ref", "token", "authorization"} {
			if strings.Contains(s, bad) {
				t.Errorf("event detail %q contains %q", s, bad)
			}
		}
	}
	if n < 5 {
		t.Errorf("only %d events", n)
	}
	_ = b
	// every allowlist is free of secret-looking keys
	for _, name := range EventNames() {
		keys, _ := EventDetailKeys(name)
		for _, k := range keys {
			switch k {
			case "url", "ref", "announced_ref", "sig", "exp", "token", "authorization":
				t.Errorf("event %s allows %q", name, k)
			}
		}
	}
	// an unknown key or event is refused
	err = r.run.tx(t.Context(), func(tx *sql.Tx) error {
		return insertEvent(t.Context(), tx, r.run.fence(), Event{JobID: job, Name: EvStatus, Detail: map[string]any{"url": "x"}})
	})
	if err == nil {
		t.Error("a url key was accepted")
	}
	err = r.run.tx(t.Context(), func(tx *sql.Tx) error {
		return insertEvent(t.Context(), tx, r.run.fence(), Event{JobID: job, Name: "made_up"})
	})
	if err == nil {
		t.Error("an unknown event was accepted")
	}
	// long provider text is cut, URLs inside it are redacted
	err = r.run.tx(t.Context(), func(tx *sql.Tx) error {
		return insertEvent(t.Context(), tx, r.run.fence(), Event{JobID: job, Name: EvStatus, Detail: map[string]any{"text": strings.Repeat("é", 400) + " https://api.runpod.ai/v2/endpoint123/status/abc"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var d string
	if err := r.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? ORDER BY id DESC LIMIT 1`, job).Scan(&d); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(d), &m); err != nil || len(m["text"]) > 500 {
		t.Errorf("text %d bytes: %v", len(m["text"]), err)
	}
	_ = store.FormatTime
}
