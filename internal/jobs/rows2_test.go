package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func running() provider.RemoteStatus {
	return provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS", WorkerID: "w1"}
}

// generating drives a one-job batch to `generating` and returns the rig, batch, job and spec.
func generating(t *testing.T, mods ...rigOpt) (*rig, seeded, string, provider.TakeSpec) {
	t.Helper()
	r := newRig(t, mods...)
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	spec := r.submitOne(job)
	r.setStatus(r.refOf(job, 1), running())
	r.advance(6 * time.Second)
	r.step()
	r.wantState(job, StateGenerating)
	return r, b, job, spec
}

func TestTransition_Row14(t *testing.T) {
	t.Parallel()
	t.Run("starting", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{}).Jobs[0]
		r.submitOne(job)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		r.wantCode(job, CodeCancelledByUser)
		if r.cancelCount(r.refOf(job, 1)) != 1 {
			t.Error("Cancel POST")
		}
	})
	t.Run("generating", func(t *testing.T) {
		t.Parallel()
		r, _, job, _ := generating(t)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		if r.cancelCount(r.refOf(job, 1)) != 1 {
			t.Error("Cancel POST")
		}
	})
	t.Run("transferring before any result", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		out := provider.Output{Contract: provider.Contract, TakeID: spec.JobID, AttemptID: spec.AttemptID, Status: "ok"}
		r.setStatus(r.refOf(job, 1), completed(out, 70000))
		r.advance(10 * time.Second)
		r.step()
		r.wantState(job, StateTransferring)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
	})
	t.Run("transferring after a verified result keeps the take", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		r.deliver(spec, deliverOpts{skip: map[string]bool{"config.json": true}})
		r.step()
		r.wantState(job, StateTransferring)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateTransferring)
	})
}

func TestTransition_Row15(t *testing.T) {
	t.Parallel()
	t.Run("deadline", func(t *testing.T) {
		t.Parallel()
		r, b, job, _ := generating(t)
		ref := r.refOf(job, 1)
		r.advance(186*time.Second + 15*time.Second + 2*time.Minute + time.Second)
		r.step()
		r.wantState(job, StateFailing)
		r.wantCode(job, CodeDeadline)
		if r.cancelCount(ref) < 1 {
			t.Error("Cancel POST")
		}
		// the provider kills it at the timeout: execution up to timeout + slack, settled measured
		r.setStatus(ref, provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(200000)})
		r.advance(3 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeDeadline)
		if got, want := r.ledger(b.Batch, "settle", "execution"), int64(money.ForMillis(200000, testPrice)); got != want {
			t.Errorf("settled %d, want %d", got, want)
		}
	})
	t.Run("lost with evidence of a run", func(t *testing.T) {
		t.Parallel()
		r, b, job, _ := generating(t)
		ref := r.refOf(job, 1)
		r.setStatus(ref, provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"})
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateFailing)
		r.wantCode(job, CodeWorkerLost)
		if a := r.att(job, 1); a.State != "unknown" || a.Ref != ref {
			t.Errorf("attempt %+v", a)
		}
		// nothing confirms: the window closes and the attempt settles at its time bound
		r.advance(3 * time.Hour)
		r.step()
		r.wantState(job, StateFailed)
		if got, want := r.ledger(b.Batch, "settle", "execution_unknown"), int64(money.ForSeconds(201, testPrice)); got != want {
			t.Errorf("execution_unknown %d, want %d", got, want)
		}
		if r.ledger(b.Batch, "release", "never_ran") != 0 {
			t.Error("never_ran after a time-bound settle")
		}
	})
	t.Run("provider requeue", func(t *testing.T) {
		t.Parallel()
		r, b, job, _ := generating(t)
		ref := r.refOf(job, 1)
		r.setStatus(ref, provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateFailing)
		r.wantCode(job, CodeWorkerLost)
		if !r.att(job, 1).RequeueSeen || !r.hasEvent(job, EvProviderRequeue) {
			t.Errorf("requeue not recorded: %v", r.events(job))
		}
		if r.ledger(b.Batch, "reserve", "topup_requeue") != r.ledger(b.Batch, "reserve", "take") {
			t.Error("requeue top-up")
		}
		if r.cancelCount(ref) < 1 {
			t.Error("Cancel POST")
		}
	})
}

func TestTransition_Row16(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		st   func(spec provider.TakeSpec) provider.RemoteStatus
		code string
	}{
		{"worker error", func(spec provider.TakeSpec) provider.RemoteStatus {
			out := provider.Output{Contract: provider.Contract, TakeID: spec.JobID, AttemptID: spec.AttemptID, Status: "error", Error: &provider.OutputError{Code: "PIPELINE_ERROR", Message: "x"}}
			return provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(30000), Output: rawOutput(out)}
		}, CodeWorkerError},
		{"worker lost", func(provider.TakeSpec) provider.RemoteStatus {
			return provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(12000)}
		}, CodeWorkerLost},
		{"deadline", func(provider.TakeSpec) provider.RemoteStatus {
			return provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(186000)}
		}, CodeDeadline},
		{"cancelled outside the app", func(provider.TakeSpec) provider.RemoteStatus {
			return provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(8000)}
		}, CodeProviderCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, b, job, spec := generating(t)
			r.setStatus(r.refOf(job, 1), c.st(spec))
			r.advance(11 * time.Second)
			r.step()
			r.wantState(job, StateFailed)
			r.wantCode(job, c.code)
			if r.ledger(b.Batch, "settle", "execution") == 0 {
				t.Error("the run must be settled")
			}
			if r.ledger(b.Batch, "release", "never_ran") != 0 {
				t.Error("it ran: nothing to release")
			}
		})
	}
	t.Run("from starting without any run: console cancel", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, withOpts(func(o *RunnerOptions) { o.NeverRan = NeverRanShapes{CancelledWhileQueued: true} }))
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.submitOne(job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED"})
		r.advance(6 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeProviderCanceled)
		if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
			t.Error("a purged queue entry never ran: the take is released")
		}
	})
	t.Run("unpinned shape is time-bound", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.submitOne(job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED"})
		r.advance(6 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		if r.ledger(b.Batch, "settle", "execution_unknown") == 0 || r.ledger(b.Batch, "release", "never_ran") != 0 {
			t.Error("conservative settle expected")
		}
	})
}

func TestTransition_Row16b(t *testing.T) {
	t.Parallel()
	r, b, job, _ := generating(t)
	ghost := r.ghost(job, "unknown", true)
	r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(9000)})
	r.setStatus("ghost-ref", running())
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateFailing)
	if r.cancelCount("ghost-ref") < 1 {
		t.Error("the other live attempt must be cancelled")
	}
	// row 24: the ghost ends
	r.setStatus("ghost-ref", provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(1000)})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerLost)
	if r.ledger(b.Batch, "settle", "execution") != int64(money.ForMillis(9000, testPrice)+money.ForMillis(1000, testPrice)) {
		t.Errorf("settled %d", r.ledger(b.Batch, "settle", "execution"))
	}
}

func TestTransition_Row17(t *testing.T) {
	t.Parallel()
	r, b, job, spec := generating(t)
	out := r.deliver(spec, deliverOpts{noResult: true})
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(11 * time.Second)
	// Completed moves the job to transferring (row 17); the uploads are all there but mm-result.json is not,
	// so it waits
	r.run.Wake()
	rep, err := r.run.Step(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = rep
	r.wantState(job, StateTransferring)
	if !r.hasEvent(job, EvCompletedRemote) {
		t.Errorf("events %v", r.events(job))
	}
	if r.att(job, 1).State != "done" || r.ledger(b.Batch, "settle", "execution") != int64(money.ForMillis(70000, testPrice)) {
		t.Errorf("attempt %+v", r.att(job, 1))
	}
}

// Row 18: every reported artifact received with a matching sha, the required ones present, a valid
// truncation dict: the job moves to ingesting. The ingest is held so the state can be seen.
func TestTransition_Row18(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	r.ing.block = make(chan struct{})
	r.ing.entered = make(chan struct{}, 1)
	out := r.deliver(spec, deliverOpts{allOptional: true})
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(11 * time.Second)
	if _, err := r.run.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-r.ing.entered
	r.wantState(job, StateIngesting)
	if !r.hasEvent(job, EvVerified) {
		t.Errorf("events %v", r.events(job))
	}
	if j := r.job(job); j.StateDeadline == nil || !j.StateDeadline.Equal(r.clk.Now().Add(10*time.Minute)) {
		t.Errorf("ingest deadline %v", j.StateDeadline)
	}
	if got := len(r.ing.last); got != 11 {
		t.Errorf("the ingester gets every received artifact and mm-result.json: %d", got)
	}
	close(r.ing.block)
	if err := r.run.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Row 20: the take transaction committed; the job completes with the take, its audio length and truncation.
func TestTransition_Row20(t *testing.T) {
	t.Parallel()
	for _, trunc := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "truncated"}[trunc], func(t *testing.T) {
			t.Parallel()
			r, _, job, spec := generating(t)
			out := r.deliver(spec, deliverOpts{truncated: [2]bool{trunc, false}})
			r.setStatus(r.refOf(job, 1), completed(out, 70000))
			r.advance(11 * time.Second)
			r.step()
			r.wantState(job, StateCompleted)
			j := r.job(job)
			want := map[bool]string{false: "clean", true: "truncated"}[trunc]
			if j.Outcome != want || j.TrackID == "" || false {
				t.Errorf("job %+v", j)
			}
			if got := r.count(`SELECT COUNT(*) FROM jobs WHERE id = ? AND audio_s > 215 AND truncated_abc = ? AND truncated_semantic = 0 AND finished_at IS NOT NULL AND lease_owner IS NULL`, job, map[bool]int{false: 0, true: 1}[trunc]); got != 1 {
				t.Error("audio_s / truncation / lease columns")
			}
			if !r.hasEvent(job, EvIngested) || r.ing.callCount() != 1 || len(r.ing.last) != 5 {
				t.Errorf("events %v calls %d files %d", r.events(job), r.ing.callCount(), len(r.ing.last))
			}
		})
	}
}

func TestTransition_Row19(t *testing.T) {
	t.Parallel()
	t.Run("missing mm-result after 60 s", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		out := r.deliver(spec, deliverOpts{noResult: true})
		r.setStatus(r.refOf(job, 1), completed(out, 70000))
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateTransferring)
		r.advance(59 * time.Second)
		r.step()
		r.wantState(job, StateTransferring)
		r.advance(2 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeArtifactMissing)
	})
	t.Run("missing artifact after 60 s", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		out := r.deliver(spec, deliverOpts{skip: map[string]bool{"config.json": true}})
		// the output claims config.json although it never arrived
		out.Artifacts["config.json"] = provider.ArtifactDigest{SHA256: "00", Bytes: 1}
		r.setStatus(r.refOf(job, 1), completed(out, 70000))
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateTransferring)
		r.advance(61 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeArtifactMissing)
	})
	t.Run("sha mismatch fails at once", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		out := r.deliver(spec, deliverOpts{lieSHA: map[string]bool{"audio.flac": true}})
		r.setStatus(r.refOf(job, 1), completed(out, 70000))
		r.advance(11 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeArtifactMismatch)
	})
	t.Run("contract violations", func(t *testing.T) {
		t.Parallel()
		for name, o := range map[string]deliverOpts{"truncated dict": {badTrunc: true}, "no audio seconds": {noAudioSec: true}} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r, _, job, spec := generating(t)
				out := r.deliver(spec, o)
				r.setStatus(r.refOf(job, 1), completed(out, 70000))
				r.advance(11 * time.Second)
				r.step()
				r.wantState(job, StateFailed)
				r.wantCode(job, CodeWorkerContract)
			})
		}
	})
}

func TestTransition_Row19b(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	ghost := r.ghost(job, "unknown", true)
	r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
	r.setStatus("ghost-ref", running())
	out := r.deliver(spec, deliverOpts{lieSHA: map[string]bool{"request.json": true}})
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(11 * time.Second)
	r.step()
	r.wantState(job, StateFailing)
	r.wantCode(job, CodeArtifactMismatch)
	if r.cancelCount("ghost-ref") < 1 {
		t.Error("the other attempt must be cancelled first")
	}
	r.setStatus("ghost-ref", provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(500)})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
}

func TestTransition_Row21(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   error
		code  string
		event string
	}{
		{"ingest failed", &IngestFailedError{Code: "IO"}, CodeIngestFailed, EvFailed},
		{"duplicate audio", &DuplicateAudioError{TrackID: "t-existing"}, CodeDuplicateAudio, EvDuplicateAudio},
		{"unclassified error", errors.New("boom"), CodeIngestFailed, EvFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, b, job, spec := generating(t)
			r.ing.err = c.err
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 70000))
			r.advance(11 * time.Second)
			r.step()
			r.wantState(job, StateFailed)
			r.wantCode(job, c.code)
			if !r.hasEvent(job, c.event) {
				t.Errorf("events %v", r.events(job))
			}
			if c.event == EvDuplicateAudio && r.str(`SELECT json_extract(detail_json, '$.track_id') FROM job_events WHERE job_id = ? AND event = 'duplicate_audio'`, job) != "t-existing" {
				t.Error("the existing track must be in the event")
			}
			if r.ledger(b.Batch, "settle", "execution") == 0 {
				t.Error("the render was paid: settled normally")
			}
		})
	}
	t.Run("ingest timeout", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		r.ing.block = make(chan struct{})
		r.ing.entered = make(chan struct{}, 1)
		out := r.deliver(spec, deliverOpts{})
		r.setStatus(r.refOf(job, 1), completed(out, 70000))
		r.advance(11 * time.Second)
		if _, err := r.run.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-r.ing.entered
		r.wantState(job, StateIngesting)
		r.advance(10*time.Minute + time.Second)
		if _, err := r.run.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.wantState(job, StateFailed)
		r.wantCode(job, CodeIngestFailed)
		if r.str(`SELECT json_extract(detail_json, '$.ingest_code') FROM job_events WHERE job_id = ? AND event = 'failed'`, job) != "TIMEOUT" {
			t.Error("ingest_code TIMEOUT")
		}
		close(r.ing.block)
		if err := r.run.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.wantState(job, StateFailed) // the late ingest result does not resurrect the job
	})
}

func TestTransition_Row22(t *testing.T) {
	t.Parallel()
	t.Run("provider confirms", func(t *testing.T) {
		t.Parallel()
		r, b, job, _ := generating(t)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		ref := r.refOf(job, 1)
		r.setStatus(ref, provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(20000)})
		r.advance(3 * time.Second)
		r.step()
		r.wantState(job, StateCancelled)
		r.wantCode(job, CodeCancelledByUser)
		if r.ledger(b.Batch, "settle", "execution") != int64(money.ForMillis(20000, testPrice)) {
			t.Error("the cancelled run is settled")
		}
	})
	t.Run("window closes", func(t *testing.T) {
		t.Parallel()
		r, b, job, _ := generating(t)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		// the provider never confirms (cancel_ignored): the job stays cancelling until the window closes
		r.advance(time.Hour)
		r.step()
		r.wantState(job, StateCancelling)
		r.advance(2 * time.Hour)
		r.step()
		r.wantState(job, StateCancelled)
		if r.ledger(b.Batch, "settle", "execution_unknown") == 0 {
			t.Error("a submitted attempt closed by its window settles execution_unknown")
		}
	})
}

func TestTransition_Row23(t *testing.T) {
	t.Parallel()
	t.Run("verified result while cancelling", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		r.cancelIntent(job)
		r.step()
		r.wantState(job, StateCancelling)
		r.deliver(spec, deliverOpts{})
		r.advance(3 * time.Second)
		r.step()
		r.wantState(job, StateCompleted)
		if !r.hasEvent(job, EvCancelLostRace) {
			t.Errorf("events %v", r.events(job))
		}
	})
	t.Run("completed status with valid output while failing", func(t *testing.T) {
		t.Parallel()
		r, _, job, spec := generating(t)
		ref := r.refOf(job, 1)
		r.advance(186*time.Second + 15*time.Second + 2*time.Minute + time.Second)
		r.step()
		r.wantState(job, StateFailing)
		out := r.deliver(spec, deliverOpts{noResult: true})
		r.setStatus(ref, completed(out, 150000))
		r.advance(3 * time.Second)
		r.step()
		if s := r.state(job); s != StateTransferring {
			t.Fatalf("state %s events %v", s, r.events(job))
		}
	})
}

func TestTransition_Row24(t *testing.T) {
	t.Parallel()
	r, _, job, _ := generating(t)
	r.advance(186*time.Second + 15*time.Second + 2*time.Minute + time.Second)
	r.step()
	r.wantState(job, StateFailing)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(201000)})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeDeadline)
}

var _ = context.Background
var _ = store.FormatTime

// A verified result arrives while the cancel is in flight and no Completed status was ever seen: the take
// is kept (row 23).
func TestRunner_ResultWhileCancelling(t *testing.T) {
	t.Parallel()
	r, b, job, spec := generating(t)
	r.cancelIntent(job)
	r.step()
	r.wantState(job, StateCancelling)
	r.deliver(spec, deliverOpts{})
	r.advance(3 * time.Second)
	r.step()
	r.wantState(job, StateCompleted)
	if !r.hasEvent(job, EvCancelLostRace) || !r.hasEvent(job, EvResultReceived) {
		t.Errorf("events %v", r.events(job))
	}
	_ = b
}
