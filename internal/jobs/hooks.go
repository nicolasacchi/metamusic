package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TakeInfo describes a take the ingest bridge just wrote (C255). PatchID is the Song PR the job rendered, if any. Fence is the
// lease predicate every statement of the hook transaction must repeat; it is zero in AfterTakeCommit.
type TakeInfo struct {
	TrackID, JobID, BatchID string
	PatchID                 *string
	// Kind and Needs are the job's kind and the capabilities it needed (C323.7); a take is rendered by a generate job today.
	Kind  string
	Needs []string
	Fence store.LeaseFence
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
	var kind, needs string
	if err := x.QueryRowContext(ctx, `SELECT patch_id, kind, needs_json FROM jobs WHERE id = ?`, job.ID).Scan(&patch, &kind, &needs); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TakeInfo{}, err
	}
	ns, err := jobspec.ParseNeeds(needs)
	if err != nil {
		return TakeInfo{}, fmt.Errorf("jobs: job %s needs_json: %w", job.ID, err) // fail closed: the take rolls back
	}
	t := TakeInfo{TrackID: trackID, JobID: job.ID, BatchID: job.BatchID, Kind: kind, Needs: ns, Fence: f}
	if patch.Valid {
		t.PatchID = &patch.String
	}
	return t, nil
}
