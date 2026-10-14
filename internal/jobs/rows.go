package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// jobRec is a full jobs row as the runner uses it.
type jobRec struct {
	JobRow
	Engine, Backend          string
	Outcome, ErrorCode       string
	CancelRequestedAt        *time.Time
	CancelRequestedBy        string
	LeaseOwner               string
	NextAttemptAt            time.Time
	QueuedAt, FirstSubmit    *time.Time
	StateDeadline, StartedAt *time.Time
	Attempts, QueueExpiries  int
	TrackID                  string
}

// attRec is a job_attempts row.
type attRec struct {
	ID, JobID, State, Backend   string
	N                           int
	Ref, AnnouncedRef           string
	SubmittedAt, WindowEndsAt   time.Time
	Price                       money.MicroUSD
	RemoteStatus                string
	InProgressSeen, RequeueSeen bool
	LastQueuedAt, AnnouncedAt   *time.Time
	ExecutionMS                 *int64
	TerminalBasis               string
	TerminalAt                  *time.Time
	Settled                     bool
	WorkerKey                   string
}

func (a attRec) live() bool {
	return a.State == "pending" || a.State == "submitted" || a.State == "unknown"
}

// batchRec is the frozen part of a batch plus its state.
type batchRec struct {
	ID, State, PrincipalID, PrincipalName, Engine, Backend string
	NTakes                                                 int
	Price                                                  money.MicroUSD
	AttemptOverheadS, KillSlackS, WorkersMax, IdleS        int
	CreatedAt                                              time.Time
	Ceiling                                                money.MicroUSD
}

// q is the read side of *sql.DB and *sql.Tx.
type q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func parseT(ns sql.NullString) *time.Time {
	if !ns.Valid {
		return nil
	}
	t, err := store.ParseTime(ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func mustT(s string) time.Time {
	t, _ := store.ParseTime(s)
	return t
}

const jobCols = `id, batch_id, take_index, engine, backend, recipe_id, source_track_id, draft_id, operator, mutation_json, title,
	expected_audio_s, timeout_s, state, outcome, error_code, cancel_requested_at, cancel_requested_by, lease_owner,
	next_attempt_at, queued_at, first_submitted_at, attempts, queue_expiries, state_deadline_at, started_at, track_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanJob(sc rowScanner) (jobRec, error) {
	var j jobRec
	var src, draft, outcome, errc, cancelAt, cancelBy, lease, queued, first, dl, started, track sql.NullString
	var next string
	err := sc.Scan(&j.ID, &j.BatchID, &j.TakeIndex, &j.Engine, &j.Backend, &j.RecipeID, &src, &draft, &j.Operator, &j.MutationJSON, &j.Title,
		&j.ExpectedAudioS, &j.TimeoutS, &j.State, &outcome, &errc, &cancelAt, &cancelBy, &lease, &next, &queued, &first, &j.Attempts, &j.QueueExpiries, &dl, &started, &track)
	if err != nil {
		return j, err
	}
	if src.Valid {
		j.SourceTrackID = &src.String
	}
	if draft.Valid {
		j.DraftID = &draft.String
	}
	j.Outcome, j.ErrorCode, j.CancelRequestedBy, j.LeaseOwner, j.TrackID = outcome.String, errc.String, cancelBy.String, lease.String, track.String
	j.CancelRequestedAt, j.QueuedAt, j.FirstSubmit, j.StateDeadline, j.StartedAt = parseT(cancelAt), parseT(queued), parseT(first), parseT(dl), parseT(started)
	j.NextAttemptAt = mustT(next)
	return j, nil
}

func loadJob(ctx context.Context, x q, id string) (jobRec, error) {
	j, err := scanJob(x.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)) //nolint:gosec // G202: column list constant
	if errors.Is(err, sql.ErrNoRows) {
		return j, fmt.Errorf("jobs: job %s: %w", id, sql.ErrNoRows)
	}
	return j, err
}

func queryJobs(ctx context.Context, x q, where string, args ...any) ([]jobRec, error) {
	rows, err := x.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE `+where, args...) //nolint:gosec // G202: callers pass constants
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobRec
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

const attCols = `id, job_id, n, backend, state, ref, announced_ref, submitted_at, window_ends_at, price_pph_micro_usd, remote_status,
	in_progress_seen, requeue_seen, last_queued_at, announced_at, execution_ms, terminal_basis, terminal_at, settled, worker_key`

func scanAtt(sc rowScanner) (attRec, error) {
	var a attRec
	var ref, annRef, remote, lastQ, annAt, basis, termAt, wk sql.NullString
	var sub, win string
	var price int64
	var prog, req, settled int
	var exec sql.NullInt64
	if err := sc.Scan(&a.ID, &a.JobID, &a.N, &a.Backend, &a.State, &ref, &annRef, &sub, &win, &price, &remote, &prog, &req, &lastQ, &annAt, &exec, &basis, &termAt, &settled, &wk); err != nil {
		return a, err
	}
	a.Ref, a.AnnouncedRef, a.RemoteStatus, a.TerminalBasis, a.WorkerKey = ref.String, annRef.String, remote.String, basis.String, wk.String
	a.SubmittedAt, a.WindowEndsAt, a.Price = mustT(sub), mustT(win), money.MicroUSD(price)
	a.InProgressSeen, a.RequeueSeen, a.Settled = prog == 1, req == 1, settled == 1
	a.LastQueuedAt, a.AnnouncedAt, a.TerminalAt = parseT(lastQ), parseT(annAt), parseT(termAt)
	if exec.Valid {
		a.ExecutionMS = &exec.Int64
	}
	return a, nil
}

func queryAtts(ctx context.Context, x q, where string, args ...any) ([]attRec, error) {
	rows, err := x.QueryContext(ctx, `SELECT `+attCols+` FROM job_attempts WHERE `+where, args...) //nolint:gosec // G202: callers pass constants
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []attRec
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadBatch(ctx context.Context, x q, id string) (batchRec, error) {
	var b batchRec
	var price, ceil int64
	var created string
	err := x.QueryRowContext(ctx, `SELECT id, state, principal_id, principal_name, engine, backend, n_takes, price_pph_micro_usd,
		attempt_overhead_s, kill_slack_s, workers_max, idle_s, created_at, ceiling_micro_usd FROM batches WHERE id = ?`, id).
		Scan(&b.ID, &b.State, &b.PrincipalID, &b.PrincipalName, &b.Engine, &b.Backend, &b.NTakes, &price, &b.AttemptOverheadS, &b.KillSlackS, &b.WorkersMax, &b.IdleS, &created, &ceil)
	b.Price, b.Ceiling, b.CreatedAt = money.MicroUSD(price), money.MicroUSD(ceil), mustT(created)
	return b, err
}

// liveAttempts filters the live attempts of a list.
func liveAttempts(as []attRec) []attRec {
	var out []attRec
	for _, a := range as {
		if a.live() {
			out = append(out, a)
		}
	}
	return out
}
