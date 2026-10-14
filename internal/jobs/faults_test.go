package jobs

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

var errCrash = errors.New("injected crash")

// crashAt returns a Fault func that fails once at point; fired reports whether it did.
type crashAt struct {
	mu    sync.Mutex
	point string
	armed bool
	fired bool
}

func newCrash(point string) *crashAt { return &crashAt{point: point, armed: true} }

func (c *crashAt) fault(p string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.armed && p == c.point {
		c.armed, c.fired = false, true
		return errCrash
	}
	return nil
}

func (c *crashAt) didFire() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.fired }

// settleWorld drives runner B until the batch is finished, scripting the provider as a healthy one:
// every submitted attempt that is not a ghost completes with a delivered result.
func (r *rig) finishWith(b *Runner, batch string, maxTicks int, onTick func(i int)) {
	r.t.Helper()
	for i := 0; i < maxTicks; i++ {
		if onTick != nil {
			onTick(i)
		}
		if _, err := b.Step(r.t.Context()); err != nil {
			r.t.Fatalf("B step %d: %v", i, err)
		}
		if err := b.Drain(r.t.Context()); err != nil {
			r.t.Fatal(err)
		}
		if r.str(`SELECT state FROM batches WHERE id = ?`, batch) == "finished" {
			return
		}
		if r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state NOT IN ('completed','failed','cancelled','expired')`, batch) == 0 {
			r.advance(30 * time.Minute) // only ghosts are left: let their windows close
		} else {
			r.advance(20 * time.Second)
		}
	}
	r.t.Fatalf("batch %s did not finish; jobs %v", batch, r.str(`SELECT group_concat(state) FROM jobs WHERE batch_id = ?`, batch))
}

// completeSubmitted makes every submitted attempt that has no result yet complete with a delivered result.
func (r *rig) completeSubmitted() {
	for _, spec := range r.be.Specs() {
		ref := "ref-" + spec.AttemptID
		var state string
		if err := r.st.R.QueryRow(`SELECT state FROM job_attempts WHERE id = ?`, spec.AttemptID).Scan(&state); err != nil || state != "submitted" {
			continue
		}
		if r.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state = 'open'`, spec.AttemptID) == 0 {
			continue // closed: the job moved on without this attempt
		}
		r.mu.Lock()
		_, scripted := r.status[ref]
		r.mu.Unlock()
		if scripted {
			continue
		}
		out := r.deliver(spec, deliverOpts{})
		r.setStatus(ref, completed(out, 60000))
	}
}

func (r *rig) assertConsistent(batch string) {
	r.t.Helper()
	if live := r.count(`SELECT COUNT(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = ? AND a.state IN ('pending','submitted','unknown')`, batch); live != 0 {
		r.t.Errorf("%d attempts still live", live)
	}
	if open := r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state NOT IN ('completed','failed','cancelled','expired')`, batch); open != 0 {
		r.t.Errorf("%d jobs not terminal", open)
	}
	reserved := r.ledger(batch, "reserve", "")
	used := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND ((kind = 'settle' AND reason <> 'excess') OR kind = 'release')`, batch)
	if used > reserved {
		r.t.Errorf("settled+released %d exceeds reserved %d", used, reserved)
	}
	if r.ledger(batch, "settle", "excess") != 0 {
		r.t.Error("excess settle")
	}
}

func TestRunner_FaultPoints(t *testing.T) {
	t.Parallel()
	points := []struct {
		name string
		run  func(t *testing.T)
	}{
		{FaultAfterClaimCommit, func(t *testing.T) {
			c := newCrash(FaultAfterClaimCommit)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			// only committed state: the attempt is pending, the uploads open, the overhead reserved, no /run
			if a := r.att(job, 1); a.State != "pending" || r.be.Calls().Submit != 0 || r.ledger(b.Batch, "reserve", "attempt_overhead") == 0 {
				t.Errorf("attempt %+v", a)
			}
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			r.finishWith(B, b.Batch, 80, func(int) { r.completeSubmitted() })
			r.wantState(job, StateCompleted)
			if a := r.att(job, 1); a.State != "done" || a.TerminalBasis != "time_bound" {
				t.Errorf("the pre-crash attempt is a ghost closed by its window: %+v", a)
			}
			r.assertConsistent(b.Batch)
		}},
		{FaultAfterSubmitReturn, func(t *testing.T) {
			c := newCrash(FaultAfterSubmitReturn)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			spec := r.lastSpec() // the provider got the job; the ref was never persisted
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			if _, err := B.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			r.wantState(job, StateQueued)
			if a := r.att(job, 1); a.State != "unknown" {
				t.Fatalf("attempt %+v", a)
			}
			// the ghost renders, announces itself, and its result completes the take: one render, one track
			if code := r.start(spec, "ref-"+spec.AttemptID); code != 200 {
				t.Fatalf("start %d", code)
			}
			out := r.deliver(spec, deliverOpts{})
			r.setStatus("ref-"+spec.AttemptID, completed(out, 60000))
			r.finishWith(B, b.Batch, 80, nil)
			r.wantState(job, StateCompleted)
			if r.be.Calls().Submit != 1 {
				t.Errorf("submits %d: the ghost completed the take", r.be.Calls().Submit)
			}
			if r.ledger(b.Batch, "reserve", "topup_ambiguous") == 0 {
				t.Error("the unused ambiguous top-up stays outstanding")
			}
			r.assertConsistent(b.Batch)
		}},
		{FaultAfterCompletedSeen, func(t *testing.T) {
			c := newCrash(FaultAfterCompletedSeen)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			spec := r.submitOne(job)
			out := r.deliver(spec, deliverOpts{noResult: true})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(6 * time.Second)
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			r.wantState(job, StateTransferring) // the Completed observation was committed
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			// the missing mm-result arrives late; B finishes the transfer
			r.deliverResultOnly(spec, out)
			r.finishWith(B, b.Batch, 40, nil)
			r.wantState(job, StateCompleted)
			r.assertConsistent(b.Batch)
		}},
		{FaultMidSettle, func(t *testing.T) {
			c := newCrash(FaultMidSettle)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			spec := r.submitOne(job)
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(6 * time.Second)
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			// the crash inside the settle transaction leaves nothing of it
			if a := r.att(job, 1); a.State != "submitted" || a.Settled || r.ledger(b.Batch, "settle", "") != 0 {
				t.Errorf("attempt %+v settles %d", a, r.ledger(b.Batch, "settle", ""))
			}
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			r.finishWith(B, b.Batch, 40, nil)
			r.wantState(job, StateCompleted)
			if n := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND reason = 'execution'`, b.Batch); n != 1 {
				t.Errorf("execution settled %d times", n)
			}
			r.assertConsistent(b.Batch)
		}},
		{FaultMidIngest, func(t *testing.T) {
			c := newCrash(FaultMidIngest)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			spec := r.submitOne(job)
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(6 * time.Second)
			r.step()
			if !c.didFire() {
				t.Fatal("the ingest fault did not fire")
			}
			r.wantState(job, StateIngesting)
			if r.ing.callCount() != 0 {
				t.Error("the crash is before the ingest")
			}
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			r.finishWith(B, b.Batch, 40, nil)
			r.wantState(job, StateCompleted)
			if r.ing.callCount() != 1 {
				t.Errorf("ingest calls %d", r.ing.callCount())
			}
			r.assertConsistent(b.Batch)
		}},
		{FaultAfterCancelPost, func(t *testing.T) {
			c := newCrash(FaultAfterCancelPost)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			r.submitOne(job)
			ref := r.refOf(job, 1)
			r.cancelIntent(job)
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			r.wantState(job, StateCancelling)
			if r.cancelCount(ref) != 1 {
				t.Fatalf("cancel POSTs %d", r.cancelCount(ref))
			}
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			r.setStatus(ref, running())
			if _, err := B.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if r.cancelCount(ref) != 2 {
				t.Errorf("the new runner re-issues the Cancel: %d POSTs", r.cancelCount(ref))
			}
			r.setStatus(ref, provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(1000)})
			r.advance(3 * time.Second)
			r.finishWith(B, b.Batch, 20, nil)
			r.wantState(job, StateCancelled)
			r.assertConsistent(b.Batch)
		}},
		{FaultAfterReserveCommit, func(t *testing.T) {
			c := newCrash(FaultAfterReserveCommit)
			r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
			if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
				t.Fatalf("A: %v", err)
			}
			// the ambiguous top-up committed together with the requeue
			r.wantState(job, StateQueued)
			if r.ledger(b.Batch, "reserve", "topup_ambiguous") == 0 {
				t.Fatal("top-up not committed")
			}
			r.onSubmit(nil)
			r.advance(91 * time.Second)
			B := r.second("runner-2")
			r.finishWith(B, b.Batch, 80, func(int) { r.completeSubmitted() })
			r.wantState(job, StateCompleted)
			r.assertConsistent(b.Batch)
		}},
	}
	for _, p := range points {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			p.run(t)
		})
	}
}

// deliverResultOnly uploads just mm-result.json of out for spec.
func (r *rig) deliverResultOnly(spec provider.TakeSpec, out provider.Output) {
	r.t.Helper()
	if code := r.put(spec.Uploads[provider.ResultArtifact].URL, rawOutput(out)); code != 201 {
		r.t.Fatalf("mm-result upload: %d", code)
	}
}
