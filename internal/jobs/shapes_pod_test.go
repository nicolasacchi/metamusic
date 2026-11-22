package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// pinnerBackend is a backend that pins its own cancelled-while-queued shape (provider.ShapePinner).
type pinnerBackend struct {
	provider.Backend
	neverRan bool
}

func (p pinnerBackend) CancelledWhileQueuedIsNeverRan() bool { return p.neverRan }

// TestRunner_ShapesPerBackend: the runner reads the cancelled-while-queued shape from the attempt's own backend when it pins one, and from
// the runner-wide pinned shapes (the serverless probe's) otherwise (C396.1).
func TestRunner_ShapesPerBackend(t *testing.T) {
	t.Parallel()
	cancelledQueued := func(r *rig) (settledUnknown, releasedNeverRan int64) {
		b := r.seedBatch(batchSpec{})
		job := b.Jobs[0]
		r.submitOne(job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED"})
		r.advance(6 * time.Second)
		r.step()
		r.wantState(job, StateFailed)
		return r.ledger(b.Batch, "settle", "execution_unknown"), r.ledger(b.Batch, "release", "never_ran")
	}
	wrap := func(neverRan bool) rigOpt {
		return withOpts(func(o *RunnerOptions) { o.Backend = pinnerBackend{o.Backend, neverRan} })
	}
	t.Run("pod backend pins it: never ran, though the runner-wide shapes pin nothing", func(t *testing.T) {
		t.Parallel()
		unknown, released := cancelledQueued(newRig(t, wrap(true)))
		if unknown != 0 || released == 0 {
			t.Errorf("settled unknown %d, released never_ran %d", unknown, released)
		}
	})
	t.Run("a backend that does not pin it overrides a runner-wide pin", func(t *testing.T) {
		t.Parallel()
		unknown, released := cancelledQueued(newRig(t, wrap(false), withOpts(func(o *RunnerOptions) { o.NeverRan = NeverRanShapes{CancelledWhileQueued: true} })))
		if unknown == 0 || released != 0 {
			t.Errorf("settled unknown %d, released never_ran %d", unknown, released)
		}
	})
	t.Run("no ShapePinner: the runner-wide shapes, as before", func(t *testing.T) {
		t.Parallel()
		unknown, released := cancelledQueued(newRig(t))
		if unknown == 0 || released != 0 {
			t.Errorf("unpinned: settled unknown %d, released never_ran %d", unknown, released)
		}
		unknown, released = cancelledQueued(newRig(t, withOpts(func(o *RunnerOptions) { o.NeverRan = NeverRanShapes{CancelledWhileQueued: true} })))
		if unknown != 0 || released == 0 {
			t.Errorf("pinned: settled unknown %d, released never_ran %d", unknown, released)
		}
	})
	t.Run("the other shapes are never touched", func(t *testing.T) {
		t.Parallel()
		shapes := NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}, TTLExpiry404: true}
		r := newRig(t, wrap(true), withOpts(func(o *RunnerOptions) { o.NeverRan = shapes }))
		got := r.run.neverRanFor(provider.BackendFake)
		if !got.CancelledWhileQueued || !got.TTLExpiry404 || !got.ttlState(provider.TimedOut) {
			t.Errorf("shapes = %+v", got)
		}
	})
}
