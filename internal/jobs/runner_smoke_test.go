package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestRunner_HappyPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	ref := r.refOf(job, 1)
	if got := r.att(job, 1).State; got != "submitted" {
		t.Fatalf("attempt %s", got)
	}
	r.setStatus(ref, provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
	r.advance(10 * time.Second)
	r.step()
	if s := r.state(job); s != StateGenerating {
		t.Fatalf("state %s", s)
	}
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(ref, completed(out, 70000))
	r.advance(10 * time.Second)
	rep := r.step()
	if rep.Polled != 1 || rep.Settled != 1 {
		t.Errorf("report %+v", rep)
	}
	if s := r.state(job); s != StateCompleted {
		t.Fatalf("state %s events %v", s, r.events(job))
	}
	// The ingest pool completes the job on its own goroutine while the step above is still running its later phases, so
	// the batch is finished either by that step (the pool won) or by the next one (the step won). Which one is scheduler
	// luck, so count Finished across both: exactly one batch is finished exactly once.
	finished := rep.Finished
	next := r.step()
	finished += next.Finished
	if finished != 1 {
		t.Errorf("batch finished %d times (%+v then %+v), want once", finished, rep, next)
	}
	if s := r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch); s != "finished" {
		t.Fatalf("batch %s", s)
	}
}
