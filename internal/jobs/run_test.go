package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// TestRunner_RunLoop drives the whole life of a job through Run (lease, heartbeat goroutine, ticks,
// Wake) on the manual clock, which a pump advances while the test waits.
func TestRunner_RunLoop(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.onSubmit(func(spec provider.TakeSpec) (provider.Ref, error) {
		ref := "ref-" + spec.AttemptID
		go func() { // a worker: it renders, uploads and the provider reports the result
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(ref, completed(out, 61000))
		}()
		return provider.Ref{Backend: provider.BackendFake, ID: ref}, nil
	})
	ctx, cancel := contextWithCancel(t)
	runDone := make(chan error, 1)
	go func() { runDone <- r.run.Run(ctx) }()
	pumpDone := r.pump(ctx)
	r.run.Wake()
	waitFor(t, func() bool { return r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) == "finished" })
	r.wantState(job, StateCompleted)
	if !r.run.Holding() {
		t.Error("the runner should hold the lease while running")
	}
	cancel()
	<-pumpDone
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if r.run.Holding() {
		t.Error("lease still held after Run returned")
	}
	if exp := r.str(`SELECT expires_at FROM runner_lease WHERE id = 1`); mustT(exp).After(r.clk.Now()) {
		t.Errorf("lease not released: %s", exp)
	}
}

// A runner that cannot get the lease keeps retrying and runs nothing.
func TestRunner_RunWithoutLease(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	r.disarm() // nothing is claimed by anyone
	other := r.second("runner-2")
	if _, err := other.Step(t.Context()); err != nil { // runner-2 takes the lease first
		t.Fatal(err)
	}
	ctx, cancel := contextWithCancel(t)
	runDone := make(chan error, 1)
	go func() { runDone <- r.run.Run(ctx) }()
	waitFor(t, func() bool { return r.clk.Pending() > 0 }) // Run is waiting to retry the lease
	if r.run.Holding() || r.be.Calls().Total() != 0 {
		t.Error("a runner without the lease acted")
	}
	r.advance(10 * time.Second) // still inside runner-2's lease: it retries and fails again
	waitFor(t, func() bool { return r.clk.Pending() > 0 })
	if r.run.Holding() {
		t.Error("acquired a held lease")
	}
	cancel()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

// A lost lease stops the tick loop and sends the runner back to acquiring.
func TestRunner_RunLosesLease(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	r.seedBatch(batchSpec{})
	ctx, cancel := contextWithCancel(t)
	runDone := make(chan error, 1)
	go func() { runDone <- r.run.Run(ctx) }()
	waitFor(t, func() bool { return r.run.Holding() })
	// another instance steals the lease row (as after a long pause)
	r.exec(`UPDATE runner_lease SET owner = 'runner-2', expires_at = ? WHERE id = 1`, "2099-01-01T00:00:00.000Z")
	pumpDone := r.pump(ctx)
	waitFor(t, func() bool { return !r.run.Holding() })
	cancel()
	<-pumpDone
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}
