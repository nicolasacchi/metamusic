package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestTrip_CancelsEveryLiveAttemptOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey }))
	s := r.seedBatch(batchSpec{jobs: 4, workersMax: 2})
	a, b := s.Jobs[0], s.Jobs[1]
	r.step() // claims two
	r.wantState(a, StateStarting)
	r.wantState(b, StateStarting)
	// c is a queued job with a ghost attempt; d is queued without any
	c, d := s.Jobs[2], s.Jobs[3]
	r.ghost(c, "unknown", false)
	rec := r.recon()
	r.hold(r.run)
	if err := rec.Trip(t.Context(), TripCrashLoop, map[string]any{"threshold_s": 240}); err != nil {
		t.Fatal(err)
	}
	for _, j := range []string{a, b, c} {
		r.wantState(j, StateCancelling)
		r.wantCode(j, CodeProviderTripped)
	}
	r.wantState(d, StateQueued)
	if r.cancelCount(r.refOf(a, 1)) != 1 || r.cancelCount(r.refOf(b, 1)) != 1 {
		t.Error("each live attempt with a ref gets one Cancel")
	}
	if !r.hasEvent(a, EvTrip) {
		t.Errorf("events %v", r.events(a))
	}
	// a second trip call while one is open changes nothing
	r.mu.Lock()
	purges := r.purges
	r.mu.Unlock()
	if err := rec.Trip(t.Context(), TripStuckRunning, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	again := r.purges
	r.mu.Unlock()
	if again != purges || r.count(`SELECT COUNT(*) FROM provider_trips`) != 1 {
		t.Errorf("a second trip acted: purges %d -> %d, trips %d", purges, again, r.count(`SELECT COUNT(*) FROM provider_trips`))
	}
	// the queued job cannot be claimed while the trip is open
	r.advance(time.Second)
	r.step()
	r.wantState(d, StateQueued)
}

func TestTrip_KeepsAVerifiedResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.deliver(spec, deliverOpts{skip: map[string]bool{"audio.flac": true}}) // the audio is still missing
	r.step()                                                                // the verified mm-result moves the job to transferring and it waits for the rest
	r.wantState(job, StateTransferring)
	rec := r.recon()
	r.hold(r.run)
	if err := rec.Trip(t.Context(), TripStuckRunning, nil); err != nil {
		t.Fatal(err)
	}
	r.wantState(job, StateTransferring) // the take is kept
	// a submitting job whose /run has not returned a ref cannot be cancelled yet
	c := newCrash(FaultAfterClaimCommit)
	r2 := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
	j2 := r2.seedBatch(batchSpec{}).Jobs[0]
	if _, err := r2.run.Step(t.Context()); !errors.Is(err, errCrash) {
		t.Fatalf("setup: %v", err)
	}
	r2.wantState(j2, StateSubmitting)
	rec2 := r2.recon()
	r2.hold(r2.run)
	if err := rec2.Trip(t.Context(), TripStuckRunning, nil); err != nil {
		t.Fatal(err)
	}
	r2.wantState(j2, StateSubmitting)
}

func TestTrip_ActsOnlyWithLeaseLeft(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	rec := r.recon()
	r.hold(r.run)
	if err := r.run.tripProvider(t.Context(), TripCrashLoop, nil); err != nil {
		t.Fatal(err)
	}
	r.advance(LeaseLength - 10*time.Second) // 10 s of lease left: below the margin for network calls
	if err := rec.actOnTrips(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	purges := r.purges
	r.mu.Unlock()
	if purges != 0 {
		t.Fatal("a network action ran with less than 30 s of lease")
	}
	r.hold(r.run)
	if err := rec.actOnTrips(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	purges = r.purges
	r.mu.Unlock()
	if purges != 1 {
		t.Errorf("purges %d after the lease was renewed", purges)
	}
}

func TestTrip_PurgeFailureStillScalesToZero(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.mu.Lock()
	r.purgeFn = func() error { return provider.ErrAccess }
	r.mu.Unlock()
	rec := r.recon()
	r.hold(r.run)
	if err := rec.Trip(t.Context(), TripProviderAccess, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	scales := r.scales
	r.mu.Unlock()
	if scales != 1 {
		t.Errorf("scale-to-zero calls %d", scales)
	}
	m, ok := r.findNote("Generation stopped: provider_access")
	if !ok || !strings.Contains(m.Body, "scaled workers.max to 0") || strings.Contains(m.Body, "purged") {
		t.Errorf("trip ntfy %+v", m)
	}
	if r.auditOps("reconciler:purge_queue") != 0 {
		t.Error("a failed purge is not an audit row")
	}
}

func TestReconciler_RunLoop(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	rec := r.recon()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	r.hold(r.run) // the runner holds the lease; Run only works then
	go func() { done <- rec.Run(ctx) }()
	pumped := r.pump(ctx)
	deadline := time.After(20 * time.Second)
	for r.be.Calls().Sweep < 3 {
		select {
		case <-deadline:
			t.Fatalf("Run swept %d times", r.be.Calls().Sweep)
		case <-time.After(5 * time.Millisecond):
			r.hold(r.run) // the manual clock moves fast; keep the lease fresh like the heartbeat does
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-pumped
	// a Run whose runner does not hold the lease does nothing
	other := r.second("runner-5")
	idle := r.recon(func(o *ReconcilerOptions) { o.Runner = other })
	ctx2, cancel2 := context.WithCancel(t.Context())
	before := r.be.Calls().Total()
	finished := make(chan struct{})
	go func() { _ = idle.Run(ctx2); close(finished) }()
	time.Sleep(20 * time.Millisecond)
	cancel2()
	<-finished
	if r.be.Calls().Total() != before {
		t.Error("a reconciler without the lease called the provider")
	}
}

func TestReconciler_DefaultsAndQuietPaths(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := NewReconciler(ReconcilerOptions{Store: r.st, Backend: r.be, Spend: r.sp, Runner: r.run})
	if rec.o.Clock == nil || rec.o.Log == nil || rec.o.Location == nil {
		t.Fatal("defaults")
	}
	if rec.workerKeyOf("w") != "" {
		t.Error("no meter key, no worker key")
	}
	rec.notify(t.Context(), NoteWorkersNotZero("x")) // no notifier: silent
	if err := rec.Pass(t.Context()); err != nil {
		t.Errorf("a pass without the lease: %v", err)
	}
	noAudit := r.recon(func(o *ReconcilerOptions) { o.Audit = nil; o.Notify = failingNotifier{} })
	r.seedBatch(batchSpec{state: "awaiting_confirmation"})
	r.advance(25 * time.Hour)
	r.sweep(worker("w1", "RUNNING", upt(10), t0))
	r.mustPass(noAudit)
	r.advance(3 * time.Minute)
	r.mustPass(noAudit) // trips; the notifier fails and is only logged
	if r.openTripReason() != TripStuckRunning {
		t.Errorf("trip %q", r.openTripReason())
	}
	if r.auditOps("reconciler:trip") != 0 {
		t.Error("a reconciler without an audit store writes no rows")
	}
}

type failingNotifier struct{}

func (failingNotifier) Notify(context.Context, notify.Message) error { return errors.New("ntfy down") }

var _ = store.ErrLeaseLost
