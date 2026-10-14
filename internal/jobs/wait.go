package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// MaxWait is the longest long-poll (design §4.1).
const MaxWait = 55 * time.Second

// recheckEvery is how often Wait re-reads the row when nothing nudged the hub: another process may run the
// runner.
const recheckEvery = time.Second

// readWaitJob reads the fields of the Job read model that Wait returns.
func (s *Service) readWaitJob(ctx context.Context, id string) (Job, error) {
	var j Job
	var outcome, errc, track sql.NullString
	var created, updated string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT id, batch_id, take_index, state, title, outcome, error_code, track_id, created_at, updated_at FROM jobs WHERE id = ?`, id).
		Scan(&j.ID, &j.BatchID, &j.TakeIndex, &j.State, &j.Title, &outcome, &errc, &track, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return j, fmt.Errorf("jobs: job %s: %w", id, sql.ErrNoRows)
	}
	if err != nil {
		return j, err
	}
	if outcome.Valid {
		j.Outcome = &outcome.String
	}
	if errc.Valid {
		j.ErrorCode = &errc.String
	}
	if track.Valid {
		j.TrackID = &track.String
	}
	j.CreatedAt, j.UpdatedAt = mustT(created), mustT(updated)
	return j, nil
}

// Wait returns at once when the job's state differs from sinceState or is terminal, else blocks on the
// hub or a 1 s database re-check until timeout (0 to 55 s), request cancellation or server shutdown (C75.8).
func (s *Service) Wait(ctx context.Context, _ auth.Principal, id, sinceState string, timeout time.Duration) (WaitResult, error) {
	if err := s.wired(); err != nil {
		return WaitResult{}, err
	}
	timeout = min(max(timeout, 0), MaxWait)
	ch, cancel := s.o.Hub.Subscribe()
	defer cancel()
	stop := reqinfo.ShutdownDone(ctx) // the server's BaseContext, cancelled when shutdown starts
	deadline := s.o.Clock.NewTimer(timeout)
	defer deadline.Stop()
	for {
		j, err := s.readWaitJob(ctx, id)
		if err != nil {
			return WaitResult{}, err
		}
		if IsTerminal(j.State) || j.State != sinceState {
			return s.result(ctx, id, true, false)
		}
		if timeout == 0 {
			return s.result(ctx, id, false, true)
		}
		recheck := s.o.Clock.NewTimer(recheckEvery)
		select {
		case <-ch:
		case <-recheck.C():
		case <-deadline.C():
			recheck.Stop()
			return s.last(ctx, id, sinceState)
		case <-stop:
			recheck.Stop()
			return s.last(context.WithoutCancel(ctx), id, sinceState)
		case <-ctx.Done():
			recheck.Stop()
			return WaitResult{}, ctx.Err()
		}
		recheck.Stop()
	}
}

// last reads the job one more time and reports it as the result of a wait that ended without a change.
func (s *Service) last(ctx context.Context, id, since string) (WaitResult, error) {
	j, err := s.readWaitJob(ctx, id)
	if err != nil {
		return WaitResult{}, err
	}
	changed := IsTerminal(j.State) || j.State != since
	return s.result(ctx, id, changed, !changed)
}

// result reads the full job for the answer of a wait.
func (s *Service) result(ctx context.Context, id string, changed, timedOut bool) (WaitResult, error) {
	j, err := s.fullJob(ctx, id)
	if errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("jobs: job %s: %w", id, sql.ErrNoRows)
	}
	return WaitResult{Job: j, Changed: changed, TimedOut: timedOut}, err
}
