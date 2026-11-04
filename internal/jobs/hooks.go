package jobs

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TakeInfo describes a take the ingest bridge just wrote (C255). PatchID is the Song PR the job rendered, if any. Fence is the
// lease predicate every statement of the hook transaction must repeat; it is zero in AfterTakeCommit.
type TakeInfo struct {
	TrackID, JobID, BatchID string
	PatchID                 *string
	Fence                   store.LeaseFence
}

// TakeHook is the seam through which a domain package reacts to a new take without jobs importing it (the callbacks
// package implements it; M5-F2 froze the interface, lane M5-C1 the implementation). AfterTakeTx runs inside the fenced take
// transaction right after the take row is inserted: an error rolls the whole take back, so a Song PR child never exists
// without its callbacks. AfterTakeCommit runs once after the commit, next to the Ears analysis; it may only warn.
type TakeHook interface {
	AfterTakeTx(ctx context.Context, tx *sql.Tx, t TakeInfo) error
	AfterTakeCommit(ctx context.Context, t TakeInfo)
}

// takeInfo builds the TakeInfo of a take of job; q reads jobs.patch_id (frozen by its own trigger since migration 0013).
func takeInfo(ctx context.Context, x q, job JobRow, trackID string, f store.LeaseFence) (TakeInfo, error) {
	var patch sql.NullString
	if err := x.QueryRowContext(ctx, `SELECT patch_id FROM jobs WHERE id = ?`, job.ID).Scan(&patch); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TakeInfo{}, err
	}
	t := TakeInfo{TrackID: trackID, JobID: job.ID, BatchID: job.BatchID, Fence: f}
	if patch.Valid {
		t.PatchID = &patch.String
	}
	return t, nil
}
