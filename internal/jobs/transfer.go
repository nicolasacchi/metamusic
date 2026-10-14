package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// DuplicateAudioError is what an Ingester returns when the rendered audio already exists as a track
// (D-M3-32): the job ends DUPLICATE_AUDIO with the existing track in its event.
type DuplicateAudioError struct{ TrackID string }

func (e *DuplicateAudioError) Error() string {
	return "jobs: the render is bit-identical to an existing track"
}

// IngestFailedError carries the M1 ingest code (C15) of a failed ingest.
type IngestFailedError struct {
	Code string
	Err  error
}

func (e *IngestFailedError) Error() string { return "jobs: ingest failed: " + e.Code }
func (e *IngestFailedError) Unwrap() error { return e.Err }

const (
	ingestTimeout      = 10 * time.Minute
	submittingDeadline = 2 * time.Minute
	maxResultSize      = 64 << 10
)

// phaseResults picks up newly received mm-result.json uploads (C74.2). A verified ok result of a live
// attempt moves its job to transferring (row 7) or back from cancelling/failing (row 23); the first
// verified result wins and the other live attempts get a Cancel. An error result is only an event.
func (r *Runner) phaseResults(ctx context.Context, _ *StepReport) error {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT u.id, u.attempt_id, u.job_id FROM uploads u JOIN jobs j ON j.id = u.job_id
		WHERE u.artifact = 'mm-result.json' AND u.state = 'received'
		  AND j.state IN ('queued','submitting','starting','generating','cancelling','failing','transferring') ORDER BY u.received_at, u.id`)
	if err != nil {
		return err
	}
	type res struct{ id, attempt, job string }
	var found []res
	for rows.Next() {
		var x res
		if err := rows.Scan(&x.id, &x.attempt, &x.job); err != nil {
			rows.Close()
			return err
		}
		found = append(found, x)
	}
	rows.Close()
	for _, x := range found {
		r.mu.Lock()
		seen := r.seenResult[x.id]
		r.mu.Unlock()
		if seen {
			continue
		}
		var cancels []attRec
		err := r.tx(ctx, func(tx *sql.Tx) error {
			var err error
			cancels, err = r.handleResult(ctx, tx, x.id, x.attempt, x.job)
			return err
		})
		if err := skipStale(err); err != nil {
			return err
		}
		r.mu.Lock()
		r.seenResult[x.id] = true
		r.mu.Unlock()
		if len(cancels) > 0 {
			if err := r.sendCancels(ctx, x.job, cancels); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) readOutput(uploadID string) (provider.Output, error) {
	raw, err := r.o.Uploads.ReadFile(uploadID, maxResultSize)
	if err != nil {
		return provider.Output{}, err
	}
	return provider.DecodeOutput(raw)
}

func (r *Runner) handleResult(ctx context.Context, tx *sql.Tx, uploadID, attemptID, jobID string) ([]attRec, error) {
	f := r.fence()
	j, err := loadJob(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	as, err := queryAtts(ctx, tx, `id = ?`, attemptID)
	if err != nil || len(as) == 0 {
		return nil, err
	}
	a := as[0]
	out, err := r.readOutput(uploadID)
	if err != nil || out.Contract != provider.Contract || out.TakeID != j.ID || out.AttemptID != a.ID {
		return nil, insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvStatus, Code: CodeWorkerContract, Detail: map[string]any{"reason": "mm-result.json does not match its attempt"}})
	}
	if out.Status != "ok" {
		d := map[string]any{"reason": "worker reported an error"}
		if out.Error != nil {
			d["worker_error"] = out.Error.Code
		}
		return nil, insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvStatus, Code: CodeWorkerError, Detail: d})
	}
	var kept int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'result_received'`, j.ID).Scan(&kept); err != nil {
		return nil, err
	}
	if kept > 0 {
		return nil, nil // first verified result wins
	}
	ev := Event{AttemptID: a.ID, Name: EvResultReceived, Detail: map[string]any{"artifact_count": len(out.Artifacts), "status": out.Status}}
	deadline := set("state_deadline_at", store.FormatTime(f.Now.Add(transferNoCompleted)))
	switch j.State {
	case StateQueued, StateSubmitting, StateStarting, StateGenerating:
		others, err := liveOthers(ctx, tx, j.ID, a.ID)
		if err != nil {
			return nil, err
		}
		if err := r.moveJob(ctx, tx, f, &j, StateTransferring, ev, deadline); err != nil {
			return nil, err
		}
		return others, nil
	case StateCancelling, StateFailing:
		others, err := liveOthers(ctx, tx, j.ID, a.ID)
		if err != nil {
			return nil, err
		}
		if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvResultReceived, Detail: ev.Detail}); err != nil {
			return nil, err
		}
		if err := r.moveJob(ctx, tx, f, &j, StateTransferring, Event{AttemptID: a.ID, Name: EvCancelLostRace, Detail: map[string]any{"via": "result"}}, deadline, set("error_code", nil)); err != nil {
			return nil, err
		}
		return others, nil
	case StateTransferring:
		// Completed arrived first; record that this attempt delivered, which also closes the others' uploads
		w, err := r.winner(ctx, tx, j.ID)
		if err != nil {
			return nil, err
		}
		if w == a.ID {
			return nil, insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvResultReceived, Detail: ev.Detail})
		}
	}
	return nil, nil
}

// winner is the attempt whose result the job transfers: the first result_received, else the attempt whose
// Completed status was seen.
func (r *Runner) winner(ctx context.Context, x q, jobID string) (string, error) {
	for _, ev := range []string{"result_received", "completed_remote"} {
		var id string
		err := x.QueryRowContext(ctx, `SELECT attempt_id FROM job_events WHERE job_id = ? AND event = ? AND attempt_id IS NOT NULL ORDER BY id LIMIT 1`, jobID, ev).Scan(&id)
		switch {
		case err == nil:
			return id, nil
		case !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
	}
	return "", nil
}

// phaseTransfer verifies the uploads of transferring jobs (rows 18, 19, 19b) and hands verified jobs to
// the ingest pool.
func (r *Runner) phaseTransfer(ctx context.Context, _ *StepReport) error {
	js, err := queryJobs(ctx, r.o.Store.R, `state = 'transferring' ORDER BY created_at, take_index`)
	if err != nil {
		return err
	}
	for _, c := range js {
		var cancels []attRec
		err := r.tx(ctx, func(tx *sql.Tx) error {
			var err error
			cancels, err = r.verifyTransfer(ctx, tx, c.ID)
			return err
		})
		if err := skipStale(err); err != nil {
			return err
		}
		if len(cancels) > 0 {
			if err := r.sendCancels(ctx, c.ID, cancels); err != nil {
				return err
			}
		}
	}
	if err := r.concludeIngests(ctx); err != nil {
		return err
	}
	return r.dispatchIngest(ctx)
}

// verifyTransfer checks one transferring job against the worker's output.
func (r *Runner) verifyTransfer(ctx context.Context, tx *sql.Tx, jobID string) ([]attRec, error) {
	f := r.fence()
	j, err := loadJob(ctx, tx, jobID)
	if err != nil || j.State != StateTransferring {
		return nil, err
	}
	b, err := loadBatch(ctx, tx, j.BatchID)
	if err != nil {
		return nil, err
	}
	win, err := r.winner(ctx, tx, j.ID)
	if err != nil {
		return nil, err
	}
	var since time.Time
	var winSince sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT at FROM job_events WHERE job_id = ? AND event = 'completed_remote' ORDER BY id LIMIT 1`, j.ID).Scan(&winSince); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	completedSeen := winSince.Valid
	if completedSeen {
		since = mustT(winSince.String)
	}
	failJob := func(code string, ev Event) ([]attRec, error) {
		// failed is reachable only when no attempt can still spend, the winner (whose provider status may
		// not have arrived yet) included: otherwise row 19b, and the job fails when they are all over
		live, err := liveOthers(ctx, tx, j.ID, "")
		if err != nil {
			return nil, err
		}
		ev.Code = code
		if len(live) > 0 {
			return live, r.moveJob(ctx, tx, f, &j, StateFailing, ev, set("error_code", code))
		}
		return nil, r.moveJob(ctx, tx, f, &j, StateFailed, ev, set("error_code", code))
	}
	timedOut := func() bool {
		if completedSeen {
			return !f.Now.Before(since.Add(transferAfterDone))
		}
		return j.StateDeadline != nil && !f.Now.Before(*j.StateDeadline)
	}
	if win == "" {
		if timedOut() {
			return failJob(CodeArtifactMissing, Event{Name: EvFailed, Detail: map[string]any{"missing": []string{"mm-result.json"}}})
		}
		return nil, nil
	}
	recv, err := r.o.Uploads.Received(ctx, tx, win)
	if err != nil {
		return nil, err
	}
	byName := map[string]upload.Received{}
	for _, u := range recv {
		byName[u.Artifact] = u
	}
	res, ok := byName[provider.ResultArtifact]
	if !ok {
		if timedOut() {
			return failJob(CodeArtifactMissing, Event{AttemptID: win, Name: EvFailed, Detail: map[string]any{"missing": []string{provider.ResultArtifact}}})
		}
		return nil, nil
	}
	out, err := r.readOutput(res.UploadID)
	if err != nil || out.Contract != provider.Contract || out.Status != "ok" || out.TakeID != j.ID || out.AttemptID != win ||
		out.AudioSeconds == nil || !validTruncated(out.Truncated) {
		return failJob(CodeWorkerContract, Event{AttemptID: win, Name: EvFailed})
	}
	required, err := r.requiredArtifacts(ctx, tx, &j)
	if err != nil {
		return nil, err
	}
	var missing, mismatched []string
	for name, dg := range out.Artifacts {
		if _, allowed := provider.ArtifactMaxBytes(name); !allowed || name == provider.ResultArtifact {
			return failJob(CodeWorkerContract, Event{AttemptID: win, Name: EvFailed})
		}
		u, have := byName[name]
		switch {
		case !have:
			missing = append(missing, name)
		case u.SHA256 != dg.SHA256 || u.Bytes != dg.Bytes:
			mismatched = append(mismatched, name)
		}
	}
	for _, name := range required {
		if name == provider.ResultArtifact {
			continue
		}
		if _, reported := out.Artifacts[name]; !reported {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	if len(mismatched) > 0 {
		slices.Sort(mismatched)
		return failJob(CodeArtifactMismatch, Event{AttemptID: win, Name: EvFailed, Detail: map[string]any{"mismatched": mismatched}})
	}
	if len(missing) > 0 {
		if timedOut() {
			return failJob(CodeArtifactMissing, Event{AttemptID: win, Name: EvFailed, Detail: map[string]any{"missing": missing}})
		}
		return nil, nil
	}
	d := map[string]any{"artifacts": len(out.Artifacts), "truncated_abc": out.Truncated["abc"], "truncated_semantic": out.Truncated["semantic"]}
	_ = b
	return nil, r.moveJob(ctx, tx, f, &j, StateIngesting, Event{AttemptID: win, Name: EvVerified, Detail: d},
		set("state_deadline_at", store.FormatTime(f.Now.Add(ingestTimeout))))
}

func validTruncated(t map[string]bool) bool {
	if len(t) != 2 {
		return false
	}
	_, a := t["abc"]
	_, s := t["semantic"]
	return a && s
}

func (r *Runner) requiredArtifacts(ctx context.Context, x q, j *jobRec) ([]string, error) {
	eng, ok := r.o.Engines[j.Engine]
	if !ok {
		return nil, fmt.Errorf("jobs: no engine %q registered", j.Engine)
	}
	var request string
	if err := x.QueryRowContext(ctx, `SELECT request_json FROM recipes WHERE id = ?`, j.RecipeID).Scan(&request); err != nil {
		return nil, err
	}
	req, _ := eng.Artifacts([]byte(request))
	return req, nil
}

// dispatchIngest hands every `ingesting` job that is not in the pool to it. Step returns at once; the pool
// runs IngestTake (up to 5 minutes for an MP3 encode) while the heartbeat keeps the lease.
func (r *Runner) dispatchIngest(ctx context.Context) error {
	// a failed ingest waits for its attempts to end (error_code is set); it is not run again
	js, err := queryJobs(ctx, r.o.Store.R, `state = 'ingesting' AND lease_owner = ? AND error_code IS NULL ORDER BY created_at, take_index`, r.o.Owner)
	if err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx)
	for _, j := range js {
		if r.o.Ingest == nil {
			return errors.New("jobs: no Ingester configured")
		}
		id := j.ID
		r.pool.start(id, func() {
			if err := r.ingestJob(bg, id); err != nil && !errors.Is(err, store.ErrLeaseLost) {
				r.o.Log.Warn("ingest", "job", id, "err", err.Error())
			}
		})
	}
	return nil
}

// ingestJob runs the Ingester for a verified job and records row 20 or row 21.
func (r *Runner) ingestJob(ctx context.Context, jobID string) error {
	var j jobRec
	var att attRec
	var files []upload.Received
	var out provider.Output
	if err := func() error {
		var err error
		j, err = loadJob(ctx, r.o.Store.R, jobID)
		if err != nil || j.State != StateIngesting {
			return errStale
		}
		win, err := r.winner(ctx, r.o.Store.R, jobID)
		if err != nil {
			return err
		}
		as, err := queryAtts(ctx, r.o.Store.R, `id = ?`, win)
		if err != nil || len(as) == 0 {
			return fmt.Errorf("jobs: no winning attempt for %s", jobID)
		}
		att = as[0]
		files, err = r.o.Uploads.Received(ctx, r.o.Store.R, win)
		if err != nil {
			return err
		}
		for _, u := range files {
			if u.Artifact == provider.ResultArtifact {
				out, err = r.readOutput(u.UploadID)
				return err
			}
		}
		return fmt.Errorf("jobs: %s has no mm-result.json", jobID)
	}(); err != nil {
		if errors.Is(err, errStale) {
			return nil
		}
		return err
	}
	if err := r.fault(FaultMidIngest); err != nil {
		return err
	}
	track, ierr := r.o.Ingest.IngestTake(ctx, j.JobRow, AttemptRow{ID: att.ID, JobID: att.JobID, State: att.State, N: att.N, WindowEndsAt: att.WindowEndsAt}, files, out)
	err := r.tx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		cur, err := loadJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if cur.State != StateIngesting {
			return nil // timed out, or the bridge completed it itself
		}
		if ierr == nil {
			return r.completeJob(ctx, tx, f, &cur, track, out)
		}
		var dup *DuplicateAudioError
		var ing *IngestFailedError
		switch {
		case errors.As(ierr, &dup):
			return r.failIngest(ctx, tx, f, &cur, Event{AttemptID: att.ID, Name: EvDuplicateAudio, Code: CodeDuplicateAudio, Detail: map[string]any{"track_id": dup.TrackID}})
		case errors.As(ierr, &ing):
			return r.failIngest(ctx, tx, f, &cur, Event{AttemptID: att.ID, Name: EvFailed, Code: CodeIngestFailed, Detail: map[string]any{"ingest_code": ing.Code}})
		default:
			return r.failIngest(ctx, tx, f, &cur, Event{AttemptID: att.ID, Name: EvFailed, Code: CodeIngestFailed, Detail: map[string]any{"ingest_code": "INTERNAL"}})
		}
	})
	return skipStale(err)
}

// failIngest records a failed ingest (row 21): the event now, the failure at once when no attempt can still
// spend, else as soon as the last one is over (concludeIngests).
func (r *Runner) failIngest(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, ev Event) error {
	atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
	if err != nil {
		return err
	}
	if len(liveAttempts(atts)) == 0 {
		return r.moveJob(ctx, tx, f, j, StateFailed, ev, set("error_code", ev.Code))
	}
	ev.JobID, ev.From, ev.To = j.ID, j.State, j.State
	if err := insertEvent(ctx, tx, f, ev); err != nil {
		return err
	}
	return r.setJob(ctx, tx, f, j, set("error_code", ev.Code))
}

// concludeIngests fails the ingesting jobs whose ingest failed once their attempts are over (row 21).
func (r *Runner) concludeIngests(ctx context.Context) error {
	js, err := queryJobs(ctx, r.o.Store.R, `state = 'ingesting' AND error_code IS NOT NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	for _, c := range js {
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil || j.State != StateIngesting || j.ErrorCode == "" {
				return err
			}
			atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
			if err != nil || len(liveAttempts(atts)) > 0 {
				return err
			}
			return r.moveJob(ctx, tx, f, &j, StateFailed, Event{})
		})
		if err := skipStale(err); err != nil {
			return err
		}
	}
	return nil
}

// completeJob is row 20: the take transaction committed, the job completes with the take's facts.
func (r *Runner) completeJob(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, trackID string, out provider.Output) error {
	var env sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT env_id FROM tracks WHERE id = ?`, trackID).Scan(&env); err != nil {
		return fmt.Errorf("jobs: read ingested track: %w", err)
	}
	outcome := "clean"
	if out.Truncated["abc"] || out.Truncated["semantic"] {
		outcome = "truncated"
	}
	sets := []setCol{set("outcome", outcome), set("track_id", trackID), set("truncated_abc", boolInt(out.Truncated["abc"])), set("truncated_semantic", boolInt(out.Truncated["semantic"]))}
	if env.Valid {
		sets = append(sets, set("env_id", env.String))
	}
	d := map[string]any{"track_id": trackID, "outcome": outcome}
	if out.AudioSeconds != nil {
		if v, err := out.AudioSeconds.Float64(); err == nil && v > 0 {
			sets = append(sets, set("audio_s", v))
			d["audio_s"] = v
		}
	}
	return r.moveJob(ctx, tx, f, j, StateCompleted, Event{Name: EvIngested, Detail: d}, sets...)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// phaseDeadlines applies the runner deadlines of C75.4 that polling does not: generating (row 15), ingesting
// (row 21, TIMEOUT), and the unconfirmed-cancel warning.
func (r *Runner) phaseDeadlines(ctx context.Context, _ *StepReport) error {
	now := r.o.Clock.Now()
	// submitting: 2 minutes (C75.4). Within one tick the submit outcome follows the claim, so a job still
	// submitting after that is the leftover of a failed tick; it is repaired like an adopted one.
	stuck, err := queryJobs(ctx, r.o.Store.R, `state = 'submitting' AND lease_owner = ? AND updated_at <= ?`, r.o.Owner, store.FormatTime(now.Add(-submittingDeadline)))
	if err != nil {
		return err
	}
	for _, j := range stuck {
		if err := skipStale(r.adoptJob(ctx, j.ID)); err != nil {
			return err
		}
	}
	js, err := queryJobs(ctx, r.o.Store.R, `state IN ('generating','ingesting','cancelling','failing') AND state_deadline_at IS NOT NULL AND state_deadline_at <= ? ORDER BY created_at`, store.FormatTime(now))
	if err != nil {
		return err
	}
	for _, c := range js {
		var cancels []attRec
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			if j.StateDeadline == nil || j.StateDeadline.After(f.Now) {
				return nil
			}
			switch j.State {
			case StateGenerating:
				atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
				if err != nil {
					return err
				}
				cancels = liveAttempts(atts)
				return r.moveJob(ctx, tx, f, &j, StateFailing, Event{Name: EvDeadline, Code: CodeDeadline, Detail: map[string]any{"deadline": "generating"}}, set("error_code", CodeDeadline))
			case StateIngesting:
				if j.ErrorCode != "" {
					return nil // already failed, waiting for its attempts
				}
				return r.failIngest(ctx, tx, f, &j, Event{Name: EvFailed, Code: CodeIngestFailed, Detail: map[string]any{"ingest_code": "TIMEOUT"}})
			default:
				r.warnUnconfirmed(j)
			}
			return nil
		})
		if err := skipStale(err); err != nil {
			return err
		}
		if len(cancels) > 0 {
			if err := r.sendCancels(ctx, c.ID, cancels); err != nil {
				return err
			}
		}
	}
	return nil
}
