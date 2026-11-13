package jobs

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// While ingest is unavailable (the media tool probe is pending after a slow boot) the runner must not hand a queued paid
// job to the provider: the job stays queued and nothing reaches the backend. When ingest recovers the same job is submitted.
func TestRunner_IngestUnavailableHoldsQueuedJobs(t *testing.T) {
	t.Parallel()
	var up atomic.Bool
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.IngestReady = up.Load }))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	for range 3 {
		r.step()
		r.advance(10 * time.Second)
	}
	if s := r.state(job); s != StateQueued {
		t.Fatalf("job is %s while ingest is down, want queued", s)
	}
	if n := r.str(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ?`, job); n != "0" {
		t.Fatalf("%s attempts exist while ingest is down", n)
	}
	up.Store(true)
	r.submitOne(job) // fails the test unless the job reached starting
}

// A take the provider finished before the restart waits in `ingesting` (its uploads stay) until ingest is back, instead of
// failing INGEST_FAILED for money already spent.
func TestRunner_IngestUnavailableDefersFinishedTakes(t *testing.T) {
	t.Parallel()
	var up atomic.Bool
	up.Store(true)
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.IngestReady = up.Load }))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	ref := r.refOf(job, 1)
	out := r.deliver(spec, deliverOpts{})
	up.Store(false)
	r.setStatus(ref, completed(out, 70000))
	r.advance(10 * time.Second)
	for range 3 {
		r.step()
		r.advance(10 * time.Second)
	}
	if s := r.state(job); s == StateCompleted || s == StateFailed {
		t.Fatalf("job is %s while ingest is down, want it held", s)
	}
	if r.ing.calls != 0 {
		t.Fatalf("ingest ran %d times while unavailable", r.ing.calls)
	}
	up.Store(true)
	for range 3 {
		r.step()
		r.advance(10 * time.Second)
	}
	if s := r.state(job); s != StateCompleted {
		t.Fatalf("job is %s after recovery, want completed (events %v)", s, r.events(job))
	}
	_ = provider.Running
}
