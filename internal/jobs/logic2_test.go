package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestRunner_UnknownStatusIsRecordedOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	ref := r.refOf(job, 1)
	r.setStatus(ref, provider.RemoteStatus{State: provider.Unknown, Raw: "SOMETHING_NEW"})
	for range 3 {
		r.advance(6 * time.Second)
		r.step()
	}
	r.wantState(job, StateStarting) // non-terminal: polled again
	if n := r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'unknown_status' AND json_extract(detail_json, '$.raw') = 'SOMETHING_NEW'`, job); n != 1 {
		t.Errorf("unknown_status events %d", n)
	}
	if r.pollCount(ref) < 3 {
		t.Error("an unknown status must be polled again")
	}
}

func TestRunner_GhostOutcomesWhileQueued(t *testing.T) {
	t.Parallel()
	t.Run("a ghost that completes moves the queued job to transferring", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		job := r.seedBatch(batchSpec{}).Jobs[0]
		ghost := r.ghost(job, "unknown", true)
		r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
		out := provider.Output{Contract: provider.Contract, TakeID: job, AttemptID: ghost, Status: "ok"}
		r.setStatus("ghost-ref", completed(out, 50000))
		r.step()
		r.wantState(job, StateTransferring)
		// nothing was uploaded: the transfer fails after 60 s
		r.advance(61 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeArtifactMissing)
	})
	t.Run("a ghost that fails leaves the queued job to resubmit", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		job := r.seedBatch(batchSpec{}).Jobs[0]
		ghost := r.ghost(job, "unknown", true)
		r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
		r.setStatus("ghost-ref", provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(4000)})
		r.step()
		r.wantState(job, StateQueued)
		if a := r.att(job, 1); a.State != "done" || !a.Settled {
			t.Errorf("ghost %+v", a)
		}
		r.arm()
		r.step()
		r.wantState(job, StateStarting)
	})
	t.Run("a ghost lost while queued changes nothing", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.disarm()
		job := r.seedBatch(batchSpec{}).Jobs[0]
		ghost := r.ghost(job, "unknown", true)
		r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
		r.setStatus("ghost-ref", provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"})
		r.step()
		r.wantState(job, StateQueued)
		if r.att(job, 1).State != "unknown" {
			t.Error("still unknown")
		}
	})
}

func TestRunner_CancellingConcludesOnTerminalWithoutOutput(t *testing.T) {
	t.Parallel()
	r, _, job, _ := generating(t)
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	// Completed without a valid output: the cancel won
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Completed, Raw: "COMPLETED", ExecutionMS: ms(30000)})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateCancelled)
}

func TestRunner_CancelledWhileQueuedOnCancellingJob(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.NeverRan = NeverRanShapes{CancelledWhileQueued: true} }))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.submitOne(job)
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED"})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateCancelled)
	// it never ran: nothing is charged and the take is released
	if r.ledger(b.Batch, "settle", "") != 0 || r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Errorf("settled %d released %d", r.ledger(b.Batch, "settle", ""), r.ledger(b.Batch, "release", "never_ran"))
	}
}

func TestRunner_AnnouncedFromAnotherProviderJobIsARequeue(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	ref := r.refOf(job, 1)
	// the handler runs under another provider job id than /run returned
	if code := r.start(spec, "moved-ref"); code != 409 {
		t.Fatalf("start: %d", code)
	}
	r.step()
	r.wantState(job, StateFailing)
	r.wantCode(job, CodeWorkerLost)
	if !r.att(job, 1).RequeueSeen || !r.hasEvent(job, EvProviderRequeue) {
		t.Errorf("events %v", r.events(job))
	}
	if r.cancelCount(ref) < 1 || r.cancelCount("moved-ref") < 1 {
		t.Errorf("both provider jobs must be cancelled: %d %d", r.cancelCount(ref), r.cancelCount("moved-ref"))
	}
	if r.ledger(b.Batch, "reserve", "topup_requeue") == 0 {
		t.Error("requeue top-up")
	}
}

func TestRunner_GhostAnnouncesWhileSubmitting(t *testing.T) {
	t.Parallel()
	c := newCrash(FaultAfterSubmitReturn)
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
	job := r.seedBatch(batchSpec{}).Jobs[0]
	if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	spec := r.lastSpec()
	r.wantState(job, StateSubmitting)
	if code := r.start(spec, "ref-from-start"); code != 200 {
		t.Fatalf("start: %d", code)
	}
	r.step()
	r.wantState(job, StateStarting)
	if a := r.att(job, 1); a.State != "submitted" || a.Ref != "ref-from-start" {
		t.Errorf("attempt %+v", a)
	}
}

func TestRunner_CancelIntentOnSubmittingWithoutRefWaits(t *testing.T) {
	t.Parallel()
	c := newCrash(FaultAfterClaimCommit)
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
	job := r.seedBatch(batchSpec{}).Jobs[0]
	if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateSubmitting) // no ref yet: the runner cannot cancel a /run that has not gone out
	r.advance(3 * time.Minute)
	r.steps(2) // the 2-minute rule turns the pending attempt into a ghost and requeues the job; then the cancel applies
	r.wantState(job, StateCancelling)
}

func TestRunner_SendCancelsFailures(t *testing.T) {
	t.Parallel()
	var cancelErr error
	r := newRig(t)
	r.run.o.Backend = &cancelFailBackend{Backend: r.be, err: func() error { return cancelErr }}
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	atts := r.atts(job)
	// a generic failure is recorded and retried
	cancelErr = errors.New("connection reset")
	if err := r.run.sendCancels(t.Context(), job, atts); err != nil {
		t.Fatal(err)
	}
	if r.str(`SELECT json_extract(detail_json, '$.error') FROM job_events WHERE job_id = ? AND event = 'cancel_sent'`, job) != "error" {
		t.Error("cancel_sent error detail")
	}
	// access loss trips the provider
	cancelErr = provider.ErrAccess
	r.advance(31 * time.Second)
	if err := r.run.sendCancels(t.Context(), job, atts); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT COUNT(*) FROM provider_trips WHERE reason = 'provider_access'`) != 1 {
		t.Error("trip")
	}
	// with the lease nearly gone nothing is sent; with the lease lost it is an error
	cancelErr = nil
	r.advance(31 * time.Second)
	r.exec(`UPDATE runner_lease SET expires_at = ? WHERE id = 1`, store.FormatTime(r.clk.Now().Add(10*time.Second)))
	before := r.be.Calls().Cancel
	if err := r.run.sendCancels(t.Context(), job, atts); err != nil || r.be.Calls().Cancel != before {
		t.Errorf("short lease: %v, cancels %d -> %d", err, before, r.be.Calls().Cancel)
	}
	r.exec(`UPDATE runner_lease SET owner = 'someone-else' WHERE id = 1`)
	if err := r.run.sendCancels(t.Context(), job, atts); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("lost lease: %v", err)
	}
}

type cancelFailBackend struct {
	provider.Backend
	err func() error
}

func (c *cancelFailBackend) Cancel(ctx context.Context, ref provider.Ref) error {
	if e := c.err(); e != nil {
		_ = c.Backend.Cancel(ctx, ref)
		return e
	}
	return c.Backend.Cancel(ctx, ref)
}

func TestRunner_HeartbeatLost(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.step() // acquires the lease
	if err := r.run.heartbeat(t.Context()); err != nil {
		t.Fatal(err)
	}
	exp := r.str(`SELECT expires_at FROM runner_lease WHERE id = 1`)
	if exp != store.FormatTime(r.clk.Now().Add(LeaseLength)) {
		t.Errorf("expires %s", exp)
	}
	r.exec(`UPDATE runner_lease SET owner = 'someone-else' WHERE id = 1`)
	if err := r.run.heartbeat(t.Context()); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("heartbeat after losing the lease: %v", err)
	}
	if r.run.Holding() {
		t.Error("still holding")
	}
}

func TestRunner_TransferContractViolations(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]struct {
		opts deliverOpts
		code string
		wait bool // fails only after the 60 s window
	}{
		"output names another take":           {deliverOpts{forgeTake: true}, CodeWorkerContract, false},
		"reported but never uploaded":         {deliverOpts{claim: map[string]bool{"plan.json": true}}, CodeArtifactMissing, true},
		"a required artifact is not reported": {deliverOpts{unreported: map[string]bool{"result.json": true}}, CodeArtifactMissing, true},
		"a non-allowlisted artifact":          {deliverOpts{claim: map[string]bool{"latent.npy": true}}, CodeWorkerContract, false},
		"mm-result lists itself":              {deliverOpts{claim: map[string]bool{"mm-result.json": true}}, CodeWorkerContract, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, _, job, spec := generating(t)
			out := r.deliver(spec, o.opts)
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			r.step()
			if o.wait {
				r.wantState(job, StateTransferring)
				r.advance(61 * time.Second)
				r.step()
			}
			r.wantState(job, StateFailed)
			r.wantCode(job, o.code)
		})
	}
	t.Run("a mismatched result is ignored as an event while generating", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		r.deliver(spec, deliverOpts{forgeTake: true})
		r.step()
		r.wantState(job, StateGenerating)
		if !r.hasEvent(job, EvStatus) {
			t.Errorf("events %v", r.events(job))
		}
	})
	t.Run("a result from another attempt is ignored once the job transfers from a Completed status", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		ghost := r.ghost(job, "unknown", true)
		r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
		r.setStatus("ghost-ref", running())
		out := r.deliver(spec, deliverOpts{noResult: true, skip: map[string]bool{"config.json": true}})
		r.setStatus(r.refOf(job, 1), completed(out, 60000))
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateTransferring)
		gu := r.ghostUploads(job, ghost)
		r.deliver(provider.TakeSpec{JobID: job, AttemptID: ghost, Expect: spec.Expect, Uploads: gu}, deliverOpts{})
		r.step()
		if r.winnerOf(job) != r.att(job, 1).ID {
			t.Error("the first Completed attempt must stay the winner")
		}
		if n := r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'result_received'`, job); n != 0 {
			t.Errorf("result_received events %d", n)
		}
	})
}

func TestRunner_CompletedJobCarriesEnvironment(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.exec(`INSERT INTO env_fingerprints(env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json, first_seen_at)
		VALUES('env-test', 'yue2', '0.1', 'c', '{}', 'd', 'fake', 'A5000', 'sha256:0', '{}', ?)`, store.FormatTime(t0))
	r.ing.env = "env-test"
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 60000))
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateCompleted)
	if got := r.job(job); got.TrackID == "" {
		t.Error("track")
	}
	if r.str(`SELECT env_id FROM jobs WHERE id = ?`, job) != "env-test" {
		t.Error("env_id of the job")
	}
}

func TestRunner_MissingIngesterIsAnError(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t, withOpts(func(o *RunnerOptions) { o.Ingest = nil }))
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 60000))
	r.advance(11 * time.Second)
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("a verified job with nobody to ingest it must be an error")
	}
	r.wantState(job, StateIngesting)
}

func TestRunner_RunPodNeedsAUsableQuote(t *testing.T) {
	t.Parallel()
	rp := providertest.New(provider.BackendRunPod, providertest.Script{})
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Backend = rp }))
	b := r.seedBatch(batchSpec{backend: "runpod-serverless"})
	ts := store.FormatTime(t0)
	set := func(quote any, at any) {
		r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES('runpod-serverless', 1, 'system:default', ?, ?, ?)
			ON CONFLICT(backend) DO UPDATE SET quote_json = excluded.quote_json, quote_at = excluded.quote_at`, ts, quote, at)
	}
	// no row at all, then a row without a quote, then garbage
	r.step()
	set(nil, nil)
	r.step()
	set(`[]`, ts)
	r.step()
	set(`{"Backend":"runpod-serverless"}`, "not a time")
	r.step()
	if rp.Calls().Total() != 0 {
		t.Fatal("claimed without a usable quote")
	}
	r.wantState(b.Jobs[0], StateQueued)
}

func TestRunner_UnknownEngineIsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Engines = nil }))
	b := r.seedBatch(batchSpec{})
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("a job whose engine is not registered must be an error")
	}
	r.wantState(b.Jobs[0], StateQueued)
}

func TestErrorMessages(t *testing.T) {
	t.Parallel()
	if (&DuplicateAudioError{TrackID: "t"}).Error() == "" || (&IngestFailedError{Code: "IO", Err: errors.New("x")}).Error() == "" ||
		!errors.Is(&IngestFailedError{Err: sql.ErrNoRows}, sql.ErrNoRows) {
		t.Error("ingest errors")
	}
	if (&claimRefusal{code: CodeBudgetExceeded}).Error() == "" {
		t.Error("claim refusal")
	}
	pe := &PreflightError{Check: "disk", Err: errors.New("low")}
	if pe.Error() == "" || !errors.Is(pe, pe.Err) {
		t.Error("preflight error")
	}
	if _, ok := EventDetailKeys("nope"); ok {
		t.Error("unknown event")
	}
	type shape struct{ Code string }
	if _, _, _, ok := errShape(&nilSafe{}); ok {
		t.Error("an error without the shape")
	}
	var nilErr *nilSafeShape
	if _, _, _, ok := errShape(nilErr); ok {
		t.Error("typed nil")
	}
	_ = shape{}
	_ = money.PerUSD
}

type nilSafe struct{}

func (*nilSafe) Error() string { return "x" }

type nilSafeShape struct{ Code string }

func (*nilSafeShape) Error() string { return "y" }

func TestRunner_RefusalWhileAGhostIsLiveIsRememberedUntilItEnds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	r.makeGhost(job)
	r.arm()
	r.submitErr(&httpErr{Code: "UNAUTHORIZED", Status: 401})
	r.advance(2 * time.Minute)
	r.step() // the resubmission is refused while attempt 1 may still run
	r.wantState(job, StateQueued)
	if r.job(job).ErrorCode != CodeProviderAuth {
		t.Fatalf("remembered code %q", r.job(job).ErrorCode)
	}
	if a := r.att(job, 2); a.State != "rejected" {
		t.Errorf("attempt 2 %+v", a)
	}
	// nothing is claimed in the meantime
	r.onSubmit(nil)
	r.advance(time.Hour)
	r.step()
	if r.be.Calls().Submit != 2 {
		t.Errorf("submits %d", r.be.Calls().Submit)
	}
	r.wantState(job, StateQueued)
	// the ghost's window closes: the job fails, no longer open to spend
	r.advance(4 * time.Hour)
	r.steps(2)
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeProviderAuth)
	if r.ledger(b.Batch, "release", "never_ran") != 0 {
		t.Error("a ghost closed by its window may have run: no release")
	}
}

func TestRunner_FailedIngestWaitsForItsAttempts(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.ing.err = &IngestFailedError{Code: "IO"}
	r.deliver(spec, deliverOpts{}) // the result arrives; the provider still reports Running
	r.setStatus(r.refOf(job, 1), running())
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateIngesting) // failed, but the winner may still spend: not terminal yet
	if r.job(job).ErrorCode != CodeIngestFailed || !r.hasEvent(job, EvFailed) {
		t.Errorf("job %+v events %v", r.job(job), r.events(job))
	}
	r.advance(11 * time.Second)
	r.steps(2)
	if r.ing.callCount() != 1 {
		t.Errorf("the failed ingest ran %d times", r.ing.callCount())
	}
	r.wantState(job, StateIngesting)
	// the provider reports the end: now the job fails
	r.setStatus(r.refOf(job, 1), completed(provider.Output{}, 60000))
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeIngestFailed)
}

func TestRunner_IngestTimeoutWaitsForItsAttempts(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.ing.block = make(chan struct{})
	r.ing.entered = make(chan struct{}, 1)
	r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), running())
	r.advance(11 * time.Second)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-r.ing.entered
	r.advance(11 * time.Minute)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.wantState(job, StateIngesting)
	r.wantCode(job, CodeIngestFailed)
	// asking again does not repeat the event
	r.advance(time.Minute)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'failed'`, job); n != 1 {
		t.Errorf("failed events %d", n)
	}
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(20000)})
	r.advance(11 * time.Second)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.wantState(job, StateFailed)
	close(r.ing.block)
	if err := r.run.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.wantState(job, StateFailed)
}
