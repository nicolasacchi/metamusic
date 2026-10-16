// Package jobs is the job state machine, the runner, the ingest bridge, the reconciler and the job reads
// (M3 C75, C76). The job and batch reads, the cancel intents and Wait live in this file and wait.go; the
// runner and the reconciler write job state, the operations only record intents (D-M3-3).
package jobs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Errors of the reads and cancels.
var (
	// ErrNotImplemented marks a body that is not part of the build (kept for the stubs of other lanes).
	ErrNotImplemented = errors.New("jobs: not implemented")
	// ErrNotFound: the job or batch does not exist.
	ErrNotFound = library.ErrNotFound
	// ErrNotBatchOwner: an agent cancelling another principal's batch (403 NOT_BATCH_OWNER).
	ErrNotBatchOwner = spend.ErrNotBatchOwner
	// ErrBadCursor: the cursor is not one this service issued.
	ErrBadCursor = errors.New("jobs: bad cursor")
)

// Options builds the read service.
type Options struct {
	Store *store.Store
	Clock clock.Clock // nil => clock.Real
	Hub   *Hub        // nil => a private hub
	Gate  *provider.Gate
	// Spend closes the approval and releases the ledger when a batch awaiting confirmation is cancelled.
	Spend *spend.Service
	// Wake nudges the runner after a cancel intent (an injected func; nil => none).
	Wake func()
}

// Service answers the job and batch reads, records cancel intents and implements Wait (C75.8).
type Service struct {
	o Options
}

// New builds the service.
func New(o Options) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Hub == nil {
		o.Hub = NewHub()
	}
	return &Service{o: o}
}

// Hub returns the in-process change notifier shared with the runner.
func (s *Service) Hub() *Hub { return s.o.Hub }

// wired reports whether the service has a store; an unwired Service (Options{}, as handler tests build it)
// answers errors.ErrUnsupported, which the API maps to 501 NOT_IMPLEMENTED.
func (s *Service) wired() error {
	if s.o.Store == nil {
		return errors.ErrUnsupported
	}
	return nil
}

// UseWake wires the runner's Wake after both are built.
func (s *Service) UseWake(f func()) { s.o.Wake = f }

// JobQuery filters ListJobs.
type JobQuery struct {
	States           []string
	BatchID, TrackID string
	Since            time.Time
	Limit            int
	Cursor           string
}

// BatchQuery filters ListBatches.
type BatchQuery struct {
	States, PrincipalKinds []string
	Since, Until           time.Time
	Limit                  int
	Cursor                 string
}

// JobCostInfo is the cost block of a job (D-M3-17).
type JobCostInfo struct {
	Expected money.MicroUSD
	Display  *money.MicroUSD // nil while only the estimate exists
	Basis    string          // estimate | provisional | reconciled
}

// Job is the read model of one job (C81 Job and JobSummary).
type Job struct {
	ID, BatchID, State, Title string
	TakeIndex                 int
	Outcome, ErrorCode        *string
	TrackID                   *string
	CreatedAt, UpdatedAt      time.Time

	RequestID                string
	Seed                     int64
	Operator                 string
	ParentTrackID, DraftID   *string
	Cost                     JobCostInfo
	Engine, Backend, Mode    string
	Attempts, Resubmits      int
	StartedAt, FinishedAt    *time.Time
	DeadlineAt, CancelReqAt  *time.Time
	TimeoutS, ExpectedAudioS int
	AudioS                   *float64
	TruncatedABC, TruncSem   *bool
	Env                      *library.Env
	Artifacts                []JobArtifact
	Timings                  JobTimings
	ErrorMessage             *string
	Events                   []JobEventRow
}

// JobArtifact is one received file of the job.
type JobArtifact struct {
	Name     string
	Bytes    int64
	SHA256   string
	Verified bool
}

// JobTimings are the measured durations.
type JobTimings struct {
	QueueDelayMS, ExecutionMS, ColdStartS *int64
}

// JobEventRow is one entry of the job's event log.
type JobEventRow struct {
	At       time.Time
	From, To *string
	Event    string
	Code     *string
}

// ApprovalInfo is the approval of a batch.
type ApprovalInfo struct {
	BatchID, State               string
	Reasons                      []string
	Via, DecidedByKey, Reason    *string
	DecidedAt, LastRequestedAt   *time.Time
	ExpiresAt                    time.Time
	NextRequestAllowedAt         *time.Time
	DecidedByKind, DecidedByName string
}

// BatchCounts counts a batch's jobs by state class. Completed includes the truncated ones.
type BatchCounts struct {
	Awaiting, Queued, Running, Completed, Truncated, Failed, Cancelled, Expired int
}

// BatchCost is the money of a batch.
type BatchCost struct {
	Expected, Max, Ceiling, Reserved, Settled, Outstanding money.MicroUSD
	Reconciled                                             bool
}

// Batch is the read model of one batch (C81 Batch and BatchSummary).
type Batch struct {
	ID, State, Via, EstimateID                 string
	CreatedByKey, CreatedByKind, CreatedByName string
	Takes                                      int
	Rationale                                  *string
	CreatedAt                                  time.Time
	FinishedAt                                 *time.Time
	Counts                                     BatchCounts
	Cost                                       BatchCost
	Approval                                   ApprovalInfo
	Jobs                                       []Job
}

// JobPage and BatchPage carry a next cursor ("" at the end).
type (
	JobPage struct {
		Items      []Job
		NextCursor string
	}
	BatchPage struct {
		Items      []Batch
		NextCursor string
	}
)

// WaitResult is the outcome of Wait.
type WaitResult struct {
	Job               Job
	Changed, TimedOut bool
}

// ErrorMessage is the fixed English text of a job error code; provider text never reaches it.
func ErrorMessage(code string) string {
	switch code {
	case CodeCancelledByUser:
		return "The job was cancelled."
	case CodeRejected:
		return "The batch was rejected."
	case CodeApprovalExpired:
		return "The batch was not approved in time."
	case CodeNoCapacity:
		return "The provider had no capacity for this take."
	case CodeBudgetExceeded:
		return "The spend limits did not allow another attempt."
	case CodePriceChanged:
		return "The provider price rose above the price quoted for the batch."
	case CodeProviderAuth:
		return "The provider refused the request."
	case CodeEdgeBlocked:
		return "The provider's edge blocked the request."
	case CodeSubmitRejected:
		return "The provider rejected the request."
	case CodeWorkerError:
		return "The worker reported an error."
	case CodeWorkerLost:
		return "The worker disappeared before it finished."
	case CodeDeadline:
		return "The take did not finish within its time limit."
	case CodeArtifactMissing:
		return "The worker did not deliver every file."
	case CodeArtifactMismatch:
		return "A delivered file did not match the worker's report."
	case CodeWorkerContract:
		return "The worker output broke the contract."
	case CodeIngestFailed:
		return "The audio could not be added to the library."
	case CodeDuplicateAudio:
		return "The render is identical to an existing track."
	case CodeNotSubmitted:
		return "The take was never sent to the provider."
	case CodeProviderTripped:
		return "Generation was stopped by the provider safety trip."
	case CodeProviderCanceled:
		return "The provider job was cancelled outside the app."
	}
	return ""
}

// ---- cursors ----

// CursorFor is the cursor that resumes a job or batch list after the item created at `at` with id `id`.
func CursorFor(at time.Time, id string) string { return encodeJobCursor(at, id) }

func encodeJobCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(store.FormatTime(at) + "|" + id))
}

func decodeCursor(c string) (at, id string, err error) {
	if c == "" {
		return "", "", nil
	}
	raw, derr := base64.RawURLEncoding.DecodeString(c)
	if derr != nil {
		return "", "", ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || len(at) != len(store.TimeLayout) || id == "" {
		return "", "", ErrBadCursor
	}
	if _, perr := store.ParseTime(at); perr != nil {
		return "", "", ErrBadCursor
	}
	return at, id, nil
}

func clampLimit(n, def, limit int) int {
	switch {
	case n <= 0:
		return def
	case n > limit:
		return limit
	}
	return n
}

func in(col string, vals []string, args *[]any) string {
	if len(vals) == 0 {
		return ""
	}
	ph := make([]string, len(vals))
	for i, v := range vals {
		ph[i] = "?"
		*args = append(*args, v)
	}
	return " AND " + col + " IN (" + strings.Join(ph, ",") + ")"
}

// ---- jobs ----

const jobSelect = `SELECT j.id, j.batch_id, j.take_index, j.state, j.title, j.outcome, j.error_code, j.track_id, j.created_at, j.updated_at,
	r.request_id, r.seed, j.operator, j.source_track_id, j.draft_id, j.expected_micro_usd, j.mutation_json, j.engine, j.backend, j.attempts, j.resubmits,
	j.started_at, j.finished_at, j.state_deadline_at, j.cancel_requested_at, j.timeout_s, j.expected_audio_s, j.audio_s, j.truncated_abc, j.truncated_semantic, j.env_id
	FROM jobs j JOIN recipes r ON r.id = j.recipe_id`

type jobScan struct {
	j       Job
	envID   sql.NullString
	mutJSON string
}

func scanJobRead(sc interface{ Scan(...any) error }) (jobScan, error) {
	var s jobScan
	var outcome, errc, track, src, draft, started, finished, deadline, cancelAt, env sql.NullString
	var created, updated string
	var expected int64
	var audio sql.NullFloat64
	var tabc, tsem sql.NullInt64
	j := &s.j
	if err := sc.Scan(&j.ID, &j.BatchID, &j.TakeIndex, &j.State, &j.Title, &outcome, &errc, &track, &created, &updated,
		&j.RequestID, &j.Seed, &j.Operator, &src, &draft, &expected, &s.mutJSON, &j.Engine, &j.Backend, &j.Attempts, &j.Resubmits,
		&started, &finished, &deadline, &cancelAt, &j.TimeoutS, &j.ExpectedAudioS, &audio, &tabc, &tsem, &env); err != nil {
		return s, err
	}
	str := func(n sql.NullString) *string {
		if !n.Valid {
			return nil
		}
		v := n.String
		return &v
	}
	j.Outcome, j.ErrorCode, j.TrackID, j.ParentTrackID, j.DraftID = str(outcome), str(errc), str(track), str(src), str(draft)
	j.CreatedAt, j.UpdatedAt = mustT(created), mustT(updated)
	j.StartedAt, j.FinishedAt, j.DeadlineAt, j.CancelReqAt = parseT(started), parseT(finished), parseT(deadline), parseT(cancelAt)
	j.Cost.Expected = money.MicroUSD(expected)
	if audio.Valid {
		j.AudioS = &audio.Float64
	}
	if tabc.Valid {
		v := tabc.Int64 == 1
		j.TruncatedABC = &v
	}
	if tsem.Valid {
		v := tsem.Int64 == 1
		j.TruncSem = &v
	}
	s.envID = env
	var m struct {
		Params struct {
			Mode string `json:"mode"`
		} `json:"params"`
	}
	_ = json.Unmarshal([]byte(s.mutJSON), &m)
	j.Mode = m.Params.Mode
	if j.Mode == "" {
		j.Mode = j.Operator
	}
	if j.ErrorCode != nil {
		if msg := ErrorMessage(*j.ErrorCode); msg != "" {
			j.ErrorMessage = &msg
		}
	}
	return s, nil
}

// fillCost sets the cost block of a job (D-M3-17).
func fillCost(ctx context.Context, x Querier, j *Job) error {
	c, err := library.JobCost(ctx, x, j.ID)
	if err != nil {
		return err
	}
	j.Cost.Basis = c.Basis
	if c.Basis != "estimate" || IsTerminal(j.State) {
		d := money.MicroUSD(c.Display)
		j.Cost.Display = &d
	}
	return nil
}

// ListJobs lists jobs newest first.
func (s *Service) ListJobs(ctx context.Context, _ auth.Principal, q JobQuery) (JobPage, error) {
	if err := s.wired(); err != nil {
		return JobPage{}, err
	}
	at, id, err := decodeCursor(q.Cursor)
	if err != nil {
		return JobPage{}, err
	}
	limit := clampLimit(q.Limit, 50, 100)
	var args []any
	where := ` WHERE 1=1` + in("j.state", q.States, &args)
	if q.BatchID != "" {
		where += ` AND j.batch_id = ?`
		args = append(args, q.BatchID)
	}
	if q.TrackID != "" {
		where += ` AND (j.source_track_id = ? OR j.track_id = ?)`
		args = append(args, q.TrackID, q.TrackID)
	}
	if !q.Since.IsZero() {
		where += ` AND j.created_at >= ?`
		args = append(args, store.FormatTime(q.Since))
	}
	if at != "" {
		where += ` AND (j.created_at < ? OR (j.created_at = ? AND j.id < ?))`
		args = append(args, at, at, id)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, jobSelect+where+` ORDER BY j.created_at DESC, j.id DESC LIMIT ?`, append(args, limit+1)...) //nolint:gosec // G202: fixed fragments, values are bound
	if err != nil {
		return JobPage{}, err
	}
	var scans []jobScan
	for rows.Next() {
		sc, err := scanJobRead(rows)
		if err != nil {
			rows.Close()
			return JobPage{}, err
		}
		scans = append(scans, sc)
	}
	if err := rows.Close(); err != nil {
		return JobPage{}, err
	}
	page := JobPage{}
	if len(scans) > limit {
		scans = scans[:limit]
		last := scans[len(scans)-1].j
		page.NextCursor = encodeJobCursor(last.CreatedAt, last.ID)
	}
	for _, sc := range scans {
		if err := fillCost(ctx, s.o.Store.R, &sc.j); err != nil {
			return JobPage{}, err
		}
		page.Items = append(page.Items, sc.j)
	}
	return page, nil
}

// Job reads one job with its artifacts, environment, timings and newest events.
func (s *Service) Job(ctx context.Context, _ auth.Principal, id string) (Job, error) {
	if err := s.wired(); err != nil {
		return Job{}, err
	}
	return s.fullJob(ctx, id)
}

func (s *Service) fullJob(ctx context.Context, id string) (Job, error) {
	sc, err := scanJobRead(s.o.Store.R.QueryRowContext(ctx, jobSelect+` WHERE j.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j := sc.j
	x := s.o.Store.R
	if err := fillCost(ctx, x, &j); err != nil {
		return j, err
	}
	if sc.envID.Valid {
		env, err := library.EnvByID(ctx, x, sc.envID.String)
		if err != nil {
			return j, err
		}
		j.Env = env
	}
	if err := s.fillArtifacts(ctx, &j); err != nil {
		return j, err
	}
	if err := s.fillTimings(ctx, &j); err != nil {
		return j, err
	}
	rows, err := x.QueryContext(ctx, `SELECT at, from_state, to_state, event, code FROM (SELECT id, at, from_state, to_state, event, code FROM job_events WHERE job_id = ? ORDER BY id DESC LIMIT 50) ORDER BY id`, id)
	if err != nil {
		return j, err
	}
	defer rows.Close()
	for rows.Next() {
		var e JobEventRow
		var at string
		var from, to, code sql.NullString
		if err := rows.Scan(&at, &from, &to, &e.Event, &code); err != nil {
			return j, err
		}
		e.At = mustT(at)
		if from.Valid {
			e.From = &from.String
		}
		if to.Valid {
			e.To = &to.String
		}
		if code.Valid {
			e.Code = &code.String
		}
		j.Events = append(j.Events, e)
	}
	return j, rows.Err()
}

// fillArtifacts lists the files of the attempt that delivered (else of the latest attempt).
func (s *Service) fillArtifacts(ctx context.Context, j *Job) error {
	var attempt sql.NullString
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE((SELECT attempt_id FROM job_events WHERE job_id = ?1 AND event IN ('result_received','completed_remote') AND attempt_id IS NOT NULL ORDER BY id LIMIT 1),
		(SELECT id FROM job_attempts WHERE job_id = ?1 ORDER BY n DESC LIMIT 1))`, j.ID).Scan(&attempt)
	if err != nil || !attempt.Valid {
		return err
	}
	var verified int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'verified'`, j.ID).Scan(&verified); err != nil {
		return err
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT artifact, bytes, sha256 FROM uploads WHERE attempt_id = ? AND state IN ('received','consumed') ORDER BY artifact`, attempt.String)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a JobArtifact
		if err := rows.Scan(&a.Name, &a.Bytes, &a.SHA256); err != nil {
			return err
		}
		a.Verified = verified > 0
		j.Artifacts = append(j.Artifacts, a)
	}
	return rows.Err()
}

// fillTimings: the winning attempt's execution time and the cold start (the announcement minus the
// worker's start, when the meter saw that worker).
func (s *Service) fillTimings(ctx context.Context, j *Job) error {
	var exec sql.NullInt64
	var announced, started sql.NullString
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT a.execution_ms, a.announced_at, (SELECT MAX(w.started_at) FROM worker_meter w WHERE w.worker_key = a.worker_key AND w.started_at <= COALESCE(a.announced_at, a.terminal_at, a.submitted_at))
		FROM job_attempts a WHERE a.job_id = ? AND (a.execution_ms IS NOT NULL OR a.announced_at IS NOT NULL) ORDER BY (a.execution_ms IS NULL), a.n DESC LIMIT 1`, j.ID).Scan(&exec, &announced, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if exec.Valid {
		j.Timings.ExecutionMS = &exec.Int64
	}
	if announced.Valid && started.Valid {
		if d := mustT(announced.String).Sub(mustT(started.String)); d >= 0 {
			v := int64(d / time.Second)
			j.Timings.ColdStartS = &v
		}
	}
	return nil
}

// ---- batches ----

const batchSelect = `SELECT b.id, b.state, b.via, b.estimate_id, b.principal_id, b.principal_kind, b.principal_name, b.n_takes, b.rationale, b.created_at, b.finished_at,
	b.expected_micro_usd, b.max_micro_usd, b.ceiling_micro_usd, b.reconciled_at IS NOT NULL FROM batches b`

func scanBatch(sc interface{ Scan(...any) error }) (Batch, error) {
	var b Batch
	var rat, fin sql.NullString
	var created string
	var exp, maxCost, ceil int64
	if err := sc.Scan(&b.ID, &b.State, &b.Via, &b.EstimateID, &b.CreatedByKey, &b.CreatedByKind, &b.CreatedByName, &b.Takes, &rat, &created, &fin,
		&exp, &maxCost, &ceil, &b.Cost.Reconciled); err != nil {
		return b, err
	}
	if rat.Valid {
		b.Rationale = &rat.String
	}
	b.CreatedAt, b.FinishedAt = mustT(created), parseT(fin)
	b.Cost.Expected, b.Cost.Max, b.Cost.Ceiling = money.MicroUSD(exp), money.MicroUSD(maxCost), money.MicroUSD(ceil)
	return b, nil
}

func (s *Service) fillBatch(ctx context.Context, b *Batch, withJobs bool) error {
	x := s.o.Store.R
	rows, err := x.QueryContext(ctx, `SELECT state, outcome, COUNT(*) FROM jobs WHERE batch_id = ? GROUP BY state, outcome`, b.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var st string
		var outcome sql.NullString
		var n int
		if err := rows.Scan(&st, &outcome, &n); err != nil {
			rows.Close()
			return err
		}
		c := &b.Counts
		switch st {
		case StateAwaiting:
			c.Awaiting += n
		case StateQueued:
			c.Queued += n
		case StateCompleted:
			c.Completed += n
			if outcome.String == "truncated" {
				c.Truncated += n
			}
		case StateFailed:
			c.Failed += n
		case StateCancelled:
			c.Cancelled += n
		case StateExpired:
			c.Expired += n
		default:
			c.Running += n
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var reserved, settled int64
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN kind = 'reserve' THEN micro_usd END), 0), COALESCE(SUM(CASE WHEN kind = 'settle' THEN micro_usd END), 0) FROM ledger WHERE batch_id = ?`, b.ID).Scan(&reserved, &settled); err != nil {
		return err
	}
	b.Cost.Reserved, b.Cost.Settled = money.MicroUSD(reserved), money.MicroUSD(settled)
	if b.Cost.Outstanding, err = spend.BatchOutstanding(ctx, x, b.ID); err != nil {
		return err
	}
	if err := s.fillApproval(ctx, b); err != nil {
		return err
	}
	if !withJobs {
		return nil
	}
	jr, err := x.QueryContext(ctx, jobSelect+` WHERE j.batch_id = ? ORDER BY j.take_index LIMIT 50`, b.ID)
	if err != nil {
		return err
	}
	var scans []jobScan
	for jr.Next() {
		sc, err := scanJobRead(jr)
		if err != nil {
			jr.Close()
			return err
		}
		scans = append(scans, sc)
	}
	if err := jr.Close(); err != nil {
		return err
	}
	for _, sc := range scans {
		if err := fillCost(ctx, x, &sc.j); err != nil {
			return err
		}
		b.Jobs = append(b.Jobs, sc.j)
	}
	return nil
}

func (s *Service) fillApproval(ctx context.Context, b *Batch) error {
	var reasons string
	var via, by, reason, decided, lastReq sql.NullString
	var expires string
	a := &b.Approval
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT batch_id, state, reasons, via, decided_by, decided_at, reason, last_requested_at, expires_at FROM approvals WHERE batch_id = ?`, b.ID).
		Scan(&a.BatchID, &a.State, &reasons, &via, &by, &decided, &reason, &lastReq, &expires)
	if err != nil {
		return err
	}
	a.Reasons = []string{}
	_ = json.Unmarshal([]byte(reasons), &a.Reasons)
	str := func(n sql.NullString) *string {
		if !n.Valid {
			return nil
		}
		return &n.String
	}
	a.Via, a.DecidedByKey, a.Reason = str(via), str(by), str(reason)
	a.DecidedAt, a.LastRequestedAt, a.ExpiresAt = parseT(decided), parseT(lastReq), mustT(expires)
	if a.State == "pending" && a.LastRequestedAt != nil {
		t := a.LastRequestedAt.Add(spend.ApprovalRequestGap)
		a.NextRequestAllowedAt = &t
	}
	if a.DecidedByKey != nil {
		a.DecidedByKind, _, _ = strings.Cut(*a.DecidedByKey, ":")
		a.DecidedByName = strings.TrimPrefix(*a.DecidedByKey, a.DecidedByKind+":")
		if *a.DecidedByKey == b.CreatedByKey {
			a.DecidedByName = b.CreatedByName
		}
	}
	return nil
}

// ListBatches lists batches newest first.
func (s *Service) ListBatches(ctx context.Context, _ auth.Principal, q BatchQuery) (BatchPage, error) {
	if err := s.wired(); err != nil {
		return BatchPage{}, err
	}
	at, id, err := decodeCursor(q.Cursor)
	if err != nil {
		return BatchPage{}, err
	}
	limit := clampLimit(q.Limit, 50, 100)
	var args []any
	where := ` WHERE 1=1` + in("b.state", q.States, &args) + in("b.principal_kind", q.PrincipalKinds, &args)
	if !q.Since.IsZero() {
		where += ` AND b.created_at >= ?`
		args = append(args, store.FormatTime(q.Since))
	}
	if !q.Until.IsZero() {
		where += ` AND b.created_at <= ?`
		args = append(args, store.FormatTime(q.Until))
	}
	if at != "" {
		where += ` AND (b.created_at < ? OR (b.created_at = ? AND b.id < ?))`
		args = append(args, at, at, id)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, batchSelect+where+` ORDER BY b.created_at DESC, b.id DESC LIMIT ?`, append(args, limit+1)...) //nolint:gosec // G202: fixed fragments, values are bound
	if err != nil {
		return BatchPage{}, err
	}
	var bs []Batch
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			rows.Close()
			return BatchPage{}, err
		}
		bs = append(bs, b)
	}
	if err := rows.Close(); err != nil {
		return BatchPage{}, err
	}
	page := BatchPage{}
	if len(bs) > limit {
		bs = bs[:limit]
		page.NextCursor = encodeJobCursor(bs[len(bs)-1].CreatedAt, bs[len(bs)-1].ID)
	}
	for _, b := range bs {
		if err := s.fillBatch(ctx, &b, false); err != nil {
			return BatchPage{}, err
		}
		page.Items = append(page.Items, b)
	}
	return page, nil
}

// Batch reads one batch with its jobs in take order (at most 50).
func (s *Service) Batch(ctx context.Context, _ auth.Principal, id string) (Batch, error) {
	if err := s.wired(); err != nil {
		return Batch{}, err
	}
	return s.batch(ctx, s.o.Store.R, id)
}

func (s *Service) batch(ctx context.Context, x Querier, id string) (Batch, error) {
	b, err := scanBatch(x.QueryRowContext(ctx, batchSelect+` WHERE b.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	if err != nil {
		return Batch{}, err
	}
	return b, s.fillBatch(ctx, &b, true)
}

// ---- cancel ----

// completeAudit finishes the gate's precomputed audit entry and writes it inside tx, so the row exists
// exactly when the effect committed (D-M3-21). Without a pending entry (a service call outside the gate)
// nothing is written.
func completeAudit(ctx context.Context, tx *sql.Tx, clk clock.Clock, start time.Time, created []string) error {
	e, ok := audit.PendingFrom(ctx)
	if !ok {
		return nil
	}
	e.Status = 200
	e.Duration = clk.Now().Sub(start)
	e.CreatedIDs = created
	return audit.InsertTx(ctx, tx, e)
}

const cancelSQL = `UPDATE jobs SET cancel_requested_at = ?, cancel_requested_by = ? WHERE id = ? AND cancel_requested_at IS NULL AND state NOT IN ('completed','failed','cancelled','expired')`

func (s *Service) mayCancel(p auth.Principal, owner string) error {
	if p.Kind == auth.KindAgent && owner != p.Key() {
		return ErrNotBatchOwner
	}
	return nil
}

// CancelJob records a cancel intent (terminal jobs are unchanged; an agent on another principal's batch
// gets ErrNotBatchOwner). The runner applies it (rows 2, 5, 5b, 11, 14).
func (s *Service) CancelJob(ctx context.Context, p auth.Principal, id, reason string) (Job, error) {
	if err := s.wired(); err != nil {
		return Job{}, err
	}
	_ = reason // the reason is part of the audited request, not of the job row
	start := s.o.Clock.Now()
	var owner string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT b.principal_id FROM jobs j JOIN batches b ON b.id = j.batch_id WHERE j.id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if err := s.mayCancel(p, owner); err != nil {
		return Job{}, err
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, cancelSQL, store.FormatTime(start), p.Key(), id); err != nil {
			return fmt.Errorf("jobs: cancel intent: %w", err)
		}
		return completeAudit(ctx, tx, s.o.Clock, start, nil)
	})
	if err != nil {
		return Job{}, err
	}
	s.changed()
	return s.fullJob(ctx, id)
}

// CancelBatch records a cancel intent on every non-terminal job of a batch.
func (s *Service) CancelBatch(ctx context.Context, p auth.Principal, id, reason string) (Batch, error) {
	if err := s.wired(); err != nil {
		return Batch{}, err
	}
	start := s.o.Clock.Now()
	var owner string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT principal_id FROM batches WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	if err != nil {
		return Batch{}, err
	}
	if err := s.mayCancel(p, owner); err != nil {
		return Batch{}, err
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested_at = ?, cancel_requested_by = ? WHERE batch_id = ? AND cancel_requested_at IS NULL AND state NOT IN ('completed','failed','cancelled','expired')`,
			store.FormatTime(start), p.Key(), id); err != nil {
			return fmt.Errorf("jobs: cancel intents: %w", err)
		}
		// a batch still awaiting confirmation never started: withdraw its approval, end the batch and release
		// the reservation in this transaction, so it leaves the approvals hub and list_batches
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM batches WHERE id = ?`, id).Scan(&state); err != nil {
			return err
		}
		if state == StateAwaiting {
			if s.o.Spend == nil {
				return errors.ErrUnsupported
			}
			if _, err := s.o.Spend.WithdrawBatch(ctx, tx, id, p.Key(), reason, nil); err != nil {
				return fmt.Errorf("jobs: withdraw batch: %w", err)
			}
		}
		return completeAudit(ctx, tx, s.o.Clock, start, nil)
	})
	if err != nil {
		return Batch{}, err
	}
	s.changed()
	return s.batch(ctx, s.o.Store.R, id)
}

func (s *Service) changed() {
	s.o.Hub.Notify()
	if s.o.Wake != nil {
		s.o.Wake()
	}
}

func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		idem.MarksFrom(ctx).Reset()
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
