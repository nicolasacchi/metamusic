package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
)

// recHook records the calls of the TakeHook seam (C255).
type recHook struct {
	mu        sync.Mutex
	tx        []TakeInfo
	commit    []TakeInfo
	txErr     error
	seenInTx  int // tracks visible to the hook's own transaction at AfterTakeTx
	b         *bridgeRig
	seenAfter int // tracks visible to a reader at AfterTakeCommit
}

func (h *recHook) AfterTakeTx(ctx context.Context, tx *sql.Tx, t TakeInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tx = append(h.tx, t)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE id = ?`, t.TrackID).Scan(&h.seenInTx)
	return h.txErr
}

func (h *recHook) AfterTakeCommit(_ context.Context, t TakeInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commit = append(h.commit, t)
	h.seenAfter = h.b.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, t.TrackID)
}

// TestTakeHook_CalledInTransactionThenAfterCommit: the hook runs once inside the take transaction after the take row exists, with
// the job, batch and track ids and no patch, and once after the commit.
func TestTakeHook_CalledInTransactionThenAfterCommit(t *testing.T) {
	h := &recHook{}
	b := newBridgeRig(t, true, func(o *BridgeOptions) { o.TakeHook = h })
	h.b = b
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	b.run1(job, b.flac(1), [2]bool{}, nil)
	b.wantState(job, StateCompleted)
	track := b.job(job).TrackID
	if len(h.tx) != 1 || len(h.commit) != 1 {
		t.Fatalf("hook calls: tx %d, commit %d, want 1 and 1", len(h.tx), len(h.commit))
	}
	for _, info := range []TakeInfo{h.tx[0], h.commit[0]} {
		if info.TrackID != track || info.JobID != job || info.BatchID != b.job(job).BatchID || info.PatchID != nil {
			t.Errorf("take info %+v, want track %s job %s and no patch", info, track, job)
		}
	}
	if h.tx[0].Fence.Owner == "" || h.commit[0].Fence.Owner != "" {
		t.Errorf("fence: tx %+v, commit %+v: only the transaction carries the lease fence", h.tx[0].Fence, h.commit[0].Fence)
	}
	if h.seenInTx != 1 || h.seenAfter != 1 {
		t.Errorf("tracks visible: in the transaction %d, after the commit %d, want 1 and 1", h.seenInTx, h.seenAfter)
	}
}

// TestTakeHook_ErrorRollsTheTakeBack: a hook error fails the take transaction like any other write: no track, no env row, the
// uploads stay for a retry, the work directory is gone, and AfterTakeCommit never runs.
func TestTakeHook_ErrorRollsTheTakeBack(t *testing.T) {
	h := &recHook{txErr: errors.New("hook boom")}
	b := newBridgeRig(t, true, func(o *BridgeOptions) { o.TakeHook = h })
	h.b = b
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(3))
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	var ing *IngestFailedError
	if !errors.As(err, &ing) || ing.Code != "DB_INSERT" {
		t.Fatalf("err = %v, want DB_INSERT", err)
	}
	if n := b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job); n != 0 {
		t.Errorf("%d tracks after a failing hook", n)
	}
	if n := b.count(`SELECT COUNT(*) FROM env_fingerprints`); n != 0 {
		t.Errorf("env rows %d: the transaction must roll back as a whole", n)
	}
	if got := b.tracksOnDisk(); len(got) != 0 {
		t.Errorf("takes left behind: %v", got)
	}
	if len(h.commit) != 0 {
		t.Error("AfterTakeCommit ran for a rolled-back take")
	}
}
