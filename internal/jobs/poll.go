package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Poll intervals (C75.3): Queued 5 s, Running 10 s, transferring/cancelling/failing 2 s.
const (
	pollQueued   = 5 * time.Second
	pollRunning  = 10 * time.Second
	pollDying    = 2 * time.Second
	generatingSl = 2 * time.Minute // C75.4: generating deadline = timeout_s + KillSlackS + 2 min since started_at
)

func (r *Runner) pollInterval(state string, a attRec) time.Duration {
	switch state {
	case StateTransferring, StateCancelling, StateFailing:
		return pollDying
	}
	if a.RemoteStatus == string(provider.Running) {
		return pollRunning
	}
	return pollQueued
}

// phasePoll polls the due attempts of every non-terminal job and classifies what the provider says.
func (r *Runner) phasePoll(ctx context.Context, rep *StepReport) error {
	// Attempts of terminal jobs keep being polled until the provider says they are over (C76.2): a result
	// can complete a job before its Completed status is seen, and a losing attempt may still be running.
	js, err := queryJobs(ctx, r.o.Store.R, `(state IN ('queued','starting','generating','transferring','ingesting','cancelling','failing')
		OR (state = 'completed' AND EXISTS (SELECT 1 FROM job_attempts a WHERE a.job_id = jobs.id AND a.state IN ('submitted','unknown') AND a.ref IS NOT NULL)))
		ORDER BY created_at, take_index`)
	if err != nil {
		return err
	}
	if len(js) == 0 {
		return nil
	}
	if err := r.fence().Confirm(ctx, r.o.Store.R, store.LeaseMargin); err != nil {
		if errors.Is(err, store.ErrLeaseShort) {
			return nil
		}
		return err
	}
	for _, j := range js {
		atts, err := queryAtts(ctx, r.o.Store.R, `job_id = ? AND state IN ('submitted','unknown') AND ref IS NOT NULL ORDER BY n`, j.ID)
		if err != nil {
			return err
		}
		for _, a := range atts {
			now := r.o.Clock.Now()
			r.mu.Lock()
			last, hold := r.lastPoll[a.ID], r.holdPoll[a.ID]
			r.mu.Unlock()
			if now.Before(hold) || (!last.IsZero() && now.Before(last.Add(r.pollInterval(j.State, a))) && !now.Before(last)) {
				continue
			}
			st, perr := r.o.Backend.Poll(ctx, provider.Ref{Backend: r.o.Backend.Name(), ID: a.Ref})
			rep.Polled++
			if perr != nil {
				if err := r.pollFailed(ctx, j, a, perr); err != nil {
					return err
				}
				continue
			}
			r.mu.Lock()
			r.lastPoll[a.ID] = now
			r.mu.Unlock()
			post, err := r.observe(ctx, j.ID, a.ID, st)
			if err := skipStale(err); err != nil {
				return err
			}
			if len(post) > 0 {
				if err := r.sendCancels(ctx, j.ID, post); err != nil {
					return err
				}
			}
		}
		cur, err := loadJob(ctx, r.o.Store.R, j.ID)
		if err != nil {
			return err
		}
		switch cur.State {
		case StateCancelling, StateFailing:
			live, err := queryAtts(ctx, r.o.Store.R, `job_id = ? AND state IN ('submitted','unknown') AND ref IS NOT NULL`, j.ID)
			if err != nil {
				return err
			}
			if err := r.sendCancels(ctx, j.ID, live); err != nil {
				return err
			}
		case StateTransferring, StateIngesting, StateCompleted:
			// the losers of a verified result keep being cancelled until they are over
			win, err := r.winner(ctx, r.o.Store.R, j.ID)
			if err != nil {
				return err
			}
			losers, err := queryAtts(ctx, r.o.Store.R, `job_id = ? AND state IN ('submitted','unknown') AND ref IS NOT NULL AND id <> ?`, j.ID, win)
			if err != nil {
				return err
			}
			if err := r.sendCancels(ctx, j.ID, losers); err != nil {
				return err
			}
		}
	}
	return nil
}

// pollFailed handles a Poll error: access loss trips the provider (C76.3), a rate limit delays the next
// poll, anything else is retried at the normal interval.
func (r *Runner) pollFailed(ctx context.Context, j jobRec, a attRec, err error) error {
	now := r.o.Clock.Now()
	r.mu.Lock()
	r.lastPoll[a.ID] = now
	if code, _, ra, ok := errShape(err); ok && code == "RATE_LIMITED" {
		r.holdPoll[a.ID] = now.Add(max(ra, pollQueued))
	}
	r.mu.Unlock()
	if errors.Is(err, provider.ErrAccess) || errors.Is(err, provider.ErrEndpointNotFound) {
		if terr := r.tripProvider(ctx, "provider_access", map[string]any{"op": "poll"}); terr != nil {
			return terr
		}
	}
	r.o.Log.Warn("poll failed", "job", j.ID, "attempt", a.ID)
	return nil
}

// observe applies one provider observation of an attempt in one transaction. It returns the attempts that
// need a Cancel POST once the transaction has committed.
func (r *Runner) observe(ctx context.Context, jobID, attemptID string, st provider.RemoteStatus) ([]attRec, error) {
	var post []attRec
	var completed bool
	err := r.tx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		j, err := loadJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		as, err := queryAtts(ctx, tx, `id = ?`, attemptID)
		if err != nil || len(as) == 0 {
			return err
		}
		a := as[0]
		if !a.live() {
			return nil
		}
		b, err := loadBatch(ctx, tx, j.BatchID)
		if err != nil {
			return err
		}
		var hasUpload, verified int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state IN ('received','consumed')`, a.ID).Scan(&hasUpload); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND attempt_id = ? AND event = 'result_received'`, j.ID, a.ID).Scan(&verified); err != nil {
			return err
		}
		cls := Classify(ClassifyInput{
			Status: st, Now: f.Now, TTL: r.o.TTL, SubmittedAt: a.SubmittedAt, LastQueued: a.LastQueuedAt,
			InProgressSeen: a.InProgressSeen, Announced: a.AnnouncedAt != nil, HasUpload: hasUpload > 0,
			WasRunning: a.RemoteStatus == string(provider.Running), VerifiedOK: verified > 0, Shapes: r.o.NeverRan,
		})
		if err := r.recordObservation(ctx, tx, f, &j, &a, st, cls); err != nil {
			return err
		}
		o := &obs{j: &j, a: a, b: b, st: st, cls: cls}
		switch cls.Verdict {
		case VerdictPending:
			err = r.onPending(ctx, tx, f, o)
		case VerdictResultOK, VerdictRan, VerdictRanUnknown:
			err = r.onRan(ctx, tx, f, o)
			completed = st.State == provider.Completed
		case VerdictQueueExpiry, VerdictCancelledQueued:
			err = r.onNeverRan(ctx, tx, f, o)
		case VerdictLost:
			err = r.onLost(ctx, tx, f, o)
		case VerdictRequeue:
			err = r.onRequeue(ctx, tx, f, &j, a, "status")
			post = append(post, a)
		}
		post = append(post, o.cancel...)
		return err
	})
	if err != nil {
		return nil, err
	}
	if completed {
		if ferr := r.fault(FaultAfterCompletedSeen); ferr != nil {
			return nil, ferr
		}
	}
	return post, nil
}

// obs carries one observation through the handlers.
type obs struct {
	j      *jobRec
	a      attRec
	b      batchRec
	st     provider.RemoteStatus
	cls    Classification
	cancel []attRec // attempts to Cancel after commit
}

// recordObservation stores what a status shows about the attempt (observations only, never decisions).
func (r *Runner) recordObservation(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, a *attRec, st provider.RemoteStatus, cls Classification) error {
	if st.State == provider.Unknown && a.RemoteStatus != string(provider.Unknown)+":"+st.Raw {
		if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvUnknownStatus, Detail: map[string]any{"raw": st.Raw}}); err != nil {
			return err
		}
	}
	var sets []string
	var args []any
	remote := string(st.State)
	if st.State == provider.Unknown {
		remote += ":" + st.Raw
	}
	if st.State != provider.NotFound && remote != a.RemoteStatus {
		sets, args = append(sets, "remote_status = ?"), append(args, remote)
	}
	if st.State == provider.Running && !a.InProgressSeen {
		sets = append(sets, "in_progress_seen = 1")
		a.InProgressSeen = true
	}
	if st.State == provider.Queued {
		sets, args = append(sets, "last_queued_at = ?"), append(args, store.FormatTime(f.Now))
	}
	if st.WorkerID != "" && a.WorkerKey == "" && len(r.o.MeterKey) > 0 {
		sets, args = append(sets, "worker_key = ?"), append(args, workerKey(r.o.MeterKey, st.WorkerID))
	}
	if len(sets) == 0 {
		return nil
	}
	a.RemoteStatus = remote
	_ = cls
	return r.setAttempt(ctx, tx, f, a.ID, strings.Join(sets, ", "), args...)
}

// onPending handles rule 7: Running starts the generating phase (row 12); a Queued attempt past its
// queue deadline is cancelled (row 13 follows the terminal status).
func (r *Runner) onPending(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	j, a := o.j, o.a
	if j.State != StateStarting {
		return nil
	}
	announcedEarlier := a.AnnouncedAt != nil
	r.mu.Lock()
	justPromoted := r.promoted[a.ID]
	r.mu.Unlock()
	if o.st.State == provider.Running || (announcedEarlier && !justPromoted) {
		return r.startGenerating(ctx, tx, f, j, a, o.st.State == provider.Running)
	}
	if o.st.State == provider.Queued && !f.Now.Before(a.SubmittedAt.Add(r.o.TTL+startingQueueSlack)) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE attempt_id = ? AND event = 'deadline'`, a.ID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvDeadline, Code: "QUEUE_TTL", Detail: map[string]any{"deadline": "starting"}}); err != nil {
				return err
			}
		}
		o.cancel = append(o.cancel, a)
	}
	return nil
}

func (r *Runner) startGenerating(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, a attRec, running bool) error {
	started := f.Now // a job enters generating once, so this is its first sign of life
	dl := started.Add(time.Duration(j.TimeoutS+r.o.KillSlackS)*time.Second + generatingSl)
	return r.moveJob(ctx, tx, f, j, StateGenerating, Event{AttemptID: a.ID, Name: EvRunning, Detail: map[string]any{"announced": a.AnnouncedAt != nil, "status": string(provider.Running)}},
		set("started_at", store.FormatTime(started)), set("state_deadline_at", store.FormatTime(dl)))
}

// onRan handles a verified result and the rules that say the attempt ran (rules 1 to 3): the attempt is
// settled once, and the job follows the provider's terminal status.
func (r *Runner) onRan(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	if !o.cls.Terminal {
		return nil // a verified result with the provider still running: settle on the terminal status
	}
	basis := basisTimeBound
	var exec *int64
	if o.cls.ExecutionMS > 0 {
		basis, exec = basisMeasured, &o.cls.ExecutionMS
	}
	j := o.j
	if err := r.finishAttempt(ctx, tx, f, j, o.b, &o.a, basis, exec, "provider", o.st.State == provider.Completed); err != nil {
		return err
	}
	return r.followTerminal(ctx, tx, f, o)
}

// followTerminal moves the job after one of its attempts reached a provider-terminal status.
func (r *Runner) followTerminal(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	j, a := o.j, o.a
	switch j.State {
	case StateTransferring, StateIngesting, StateCompleted:
		if o.st.State == provider.Completed && j.State == StateTransferring {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'completed_remote'`, j.ID).Scan(&n); err != nil {
				return err
			}
			if n == 0 { // the 60 s artifact clock starts at the Completed status (row 19)
				return insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvCompletedRemote})
			}
		}
		return nil
	case StateCancelling, StateFailing:
		if o.st.State == provider.Completed && validOutput(o.st.Output) {
			if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvCompletedRemote}); err != nil {
				return err
			}
			return r.moveJob(ctx, tx, f, j, StateTransferring, Event{AttemptID: a.ID, Name: EvCancelLostRace, Detail: map[string]any{"via": "completed"}},
				set("state_deadline_at", store.FormatTime(f.Now.Add(transferNoCompleted))), set("error_code", nil))
		}
		return r.conclude(ctx, tx, f, j, o.b)
	case StateQueued, StateSubmitting:
		if o.st.State == provider.Completed {
			return r.moveJob(ctx, tx, f, j, StateTransferring, Event{AttemptID: a.ID, Name: EvCompletedRemote}, set("state_deadline_at", store.FormatTime(f.Now.Add(transferNoCompleted))))
		}
		return nil // a ghost ended; the job resubmits
	case StateStarting, StateGenerating:
	default:
		return nil
	}
	switch o.st.State {
	case provider.Completed:
		if j.State == StateStarting {
			if err := r.startGenerating(ctx, tx, f, j, a, false); err != nil {
				return err
			}
		}
		others, err := liveOthers(ctx, tx, j.ID, a.ID)
		if err != nil {
			return err
		}
		o.cancel = append(o.cancel, others...)
		return r.moveJob(ctx, tx, f, j, StateTransferring, Event{AttemptID: a.ID, Name: EvCompletedRemote}, set("state_deadline_at", store.FormatTime(f.Now.Add(transferNoCompleted))))
	case provider.Failed, provider.TimedOut, provider.Cancelled:
		if j.CancelRequestedAt != nil {
			// The cancel intent landed after the cancel phase looked at this job: the attempt was closed
			// (the worker's start poll got 410) or cancelled, and no result can be kept. The user asked for
			// the stop, so this is a cancel (row 14 then 22), not a worker failure.
			others, err := liveOthers(ctx, tx, j.ID, a.ID)
			if err != nil {
				return err
			}
			o.cancel = append(o.cancel, others...)
			if err := r.moveJob(ctx, tx, f, j, StateCancelling, Event{AttemptID: a.ID, Name: EvCancelRequested, Code: CodeCancelledByUser}, set("error_code", CodeCancelledByUser),
				set("state_deadline_at", store.FormatTime(f.Now.Add(cancelUnconfirm)))); err != nil {
				return err
			}
			return r.conclude(ctx, tx, f, j, o.b)
		}
		code, text := terminalCode(o.st)
		if code == CodeWorkerLost && o.st.State == provider.Failed && workerReportedDeadline(ctx, tx, a.ID) {
			code = CodeDeadline
		}
		ev := Event{AttemptID: a.ID, Name: EvFailed, Code: code}
		if o.st.State == provider.Cancelled {
			ev.Name = EvProviderCancelled
			ev.Detail = map[string]any{"status": string(o.st.State)}
		} else if text != "" {
			ev.Detail = map[string]any{"text": text}
		}
		return r.failFromProvider(ctx, tx, f, o, code, ev)
	}
	return nil
}

// transferNoCompleted is the transfer deadline when no Completed status was seen (C75.4).
const (
	transferNoCompleted = 15 * time.Minute
	transferAfterDone   = 60 * time.Second
)

// failFromProvider ends a starting or generating job (row 16), or turns it into failing while another
// attempt is live (row 16b).
func (r *Runner) failFromProvider(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs, code string, ev Event) error {
	j := o.j
	others, err := liveOthers(ctx, tx, j.ID, o.a.ID)
	if err != nil {
		return err
	}
	if len(others) > 0 {
		o.cancel = append(o.cancel, others...)
		return r.moveJob(ctx, tx, f, j, StateFailing, ev, set("error_code", code))
	}
	if err := r.moveJob(ctx, tx, f, j, StateFailed, ev, set("error_code", code)); err != nil {
		return err
	}
	return r.releaseNeverRan(ctx, tx, f, j, o.b)
}

// liveOthers lists the live attempts of a job other than skip.
func liveOthers(ctx context.Context, x q, jobID, skip string) ([]attRec, error) {
	return queryAtts(ctx, x, `job_id = ? AND state IN ('pending','submitted','unknown') AND id <> ? ORDER BY n`, jobID, skip)
}

// terminalCode maps a terminal status of a starting or generating job to its error code. A worker whose
// generation deadline passed ends with DEADLINE (C73, C75): the handler returns {"error": "DEADLINE: ...",
// "mm": <output>}, which RunPod reports as FAILED with that error text, and the output's error.code says the
// same; neither is a lost worker.
func terminalCode(st provider.RemoteStatus) (code, text string) {
	switch st.State {
	case provider.TimedOut:
		return CodeDeadline, st.ErrorText
	case provider.Cancelled:
		return CodeProviderCanceled, st.ErrorText
	}
	wc, isErr := workerErrorCode(st)
	switch {
	case wc == "DEADLINE":
		return CodeDeadline, st.ErrorText
	case isErr:
		return CodeWorkerError, st.ErrorText
	}
	return CodeWorkerLost, st.ErrorText
}

var workerErrPrefix = regexp.MustCompile(`^([A-Z][A-Z0-9_]*): `)

// workerErrorCode is the worker's own error code of a terminal status, from the C73.5 output (status
// "error"), from the handler's {"error": "CODE: msg", "mm": output} return, or from the error text's
// "CODE: " prefix. isErr is true only for a decodable error output (the pre-existing WORKER_ERROR path).
func workerErrorCode(st provider.RemoteStatus) (code string, isErr bool) {
	if out, err := provider.DecodeOutput(st.Output); err == nil && out.Status == "error" {
		if out.Error != nil {
			return out.Error.Code, true
		}
		return "", true
	}
	var wrapped struct {
		Error string          `json:"error"`
		MM    json.RawMessage `json:"mm"`
	}
	if len(st.Output) > 0 && json.Unmarshal(st.Output, &wrapped) == nil {
		if m := workerErrPrefix.FindStringSubmatch(wrapped.Error); m != nil {
			return m[1], false
		}
	}
	if m := workerErrPrefix.FindStringSubmatch(st.ErrorText); m != nil {
		return m[1], false
	}
	return "", false
}

// workerReportedDeadline reports whether the attempt's mm-result.json already said DEADLINE (the transfer
// handler records worker_error on a WORKER_ERROR event): the provider's own terminal status then carries no
// code of its own.
func workerReportedDeadline(ctx context.Context, x q, attemptID string) bool {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE attempt_id = ? AND event = 'status' AND code = ? AND json_extract(detail_json, '$.worker_error') = 'DEADLINE'`,
		attemptID, CodeWorkerError).Scan(&n)
	return err == nil && n > 0
}

// validOutput reports whether a status carries an ok worker output of the right contract.
func validOutput(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	out, err := provider.DecodeOutput(raw)
	return err == nil && out.Contract == provider.Contract && out.Status == "ok"
}

// onNeverRan handles rule 4/5's never-ran shapes: the attempt is done without a charge.
func (r *Runner) onNeverRan(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	j, a := o.j, o.a
	zero := int64(0)
	if err := r.finishAttempt(ctx, tx, f, j, o.b, &a, basisNeverRan, &zero, "provider", false); err != nil {
		return err
	}
	switch j.State {
	case StateStarting:
		ours := o.cls.Verdict == VerdictQueueExpiry
		if !ours {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE attempt_id = ? AND event = 'deadline'`, a.ID).Scan(&n); err != nil {
				return err
			}
			ours = n > 0 // we cancelled it ourselves at the queue deadline
		}
		if ours {
			return r.queueExpired(ctx, tx, f, o)
		}
		return r.failFromProvider(ctx, tx, f, o, CodeProviderCanceled, Event{AttemptID: a.ID, Name: EvProviderCancelled, Detail: map[string]any{"status": string(o.st.State)}})
	case StateCancelling, StateFailing:
		return r.conclude(ctx, tx, f, j, o.b)
	}
	return nil
}

// queueExpired is row 13: the job goes back to the queue behind a backoff, or fails NO_CAPACITY when this
// was the 7th expiry (row 6).
func (r *Runner) queueExpired(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	j := o.j
	n := j.QueueExpiries + 1
	next := f.Now.Add(backoff(n))
	if err := r.moveJob(ctx, tx, f, j, StateQueued, Event{AttemptID: o.a.ID, Name: EvQueueExpired, Detail: map[string]any{"expiries": n, "next_attempt_at": store.FormatTime(next)}},
		set("queue_expiries", n), set("next_attempt_at", store.FormatTime(next))); err != nil {
		return err
	}
	j.QueueExpiries = n
	if n <= MaxQueueExpiryResubmits {
		return nil
	}
	atts, err := queryAtts(ctx, tx, `job_id = ?`, j.ID)
	if err != nil {
		return err
	}
	if len(liveAttempts(atts)) == 0 {
		return r.failNoCapacity(ctx, tx, f, j, o.b)
	}
	return nil
}

// onLost handles rule 5: a status that no longer exists makes the attempt unknown.
func (r *Runner) onLost(ctx context.Context, tx *sql.Tx, f store.LeaseFence, o *obs) error {
	j, a := o.j, o.a
	if a.State == "unknown" {
		return nil // already unknown; the window decides
	}
	var uploads int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND state IN ('received','consumed')`, a.ID).Scan(&uploads); err != nil {
		return err
	}
	evidence := a.InProgressSeen || a.AnnouncedAt != nil || uploads > 0
	if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'unknown'"); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvLost, Detail: map[string]any{"status": "NOT_FOUND"}}); err != nil {
		return err
	}
	switch j.State {
	case StateStarting, StateGenerating:
		if evidence {
			others, err := liveOthers(ctx, tx, j.ID, a.ID)
			if err != nil {
				return err
			}
			o.cancel = append(o.cancel, others...)
			return r.moveJob(ctx, tx, f, j, StateFailing, Event{AttemptID: a.ID, Name: EvLost, Code: CodeWorkerLost}, set("error_code", CodeWorkerLost))
		}
		if j.State == StateStarting {
			// nothing shows it ran: a ghost with a full top-up before any resubmission (D-M3-4)
			return r.requeueAfterSubmit(ctx, tx, f, j, o.b, &a.ID, "lost")
		}
	}
	return nil
}

// onRequeue handles rule 6 (and an announcement from another provider job): the attempt is flagged, a
// topup_requeue reserve covers the second possible execution, and the job fails WORKER_LOST once every
// attempt is terminal.
func (r *Runner) onRequeue(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, a attRec, via string) error {
	if a.RequeueSeen {
		return nil
	}
	if err := r.setAttempt(ctx, tx, f, a.ID, "requeue_seen = 1"); err != nil {
		return err
	}
	b, err := loadBatch(ctx, tx, j.BatchID)
	if err != nil {
		return err
	}
	amount := moneyForTimeout(j.TimeoutS, b.KillSlackS, a.Price)
	id := a.ID
	d := map[string]any{"topup_micro_usd": int64(amount)}
	ce, err := r.reserve(ctx, tx, f, j, b, &id, "topup_requeue", amount)
	if err != nil {
		return err
	}
	if ce != nil {
		d["topup_refused"] = true
		if err := r.trip(ctx, tx, f, b.Backend, "provisional_exceeds_reserved", map[string]any{"cause": "topup_requeue"}); err != nil {
			return err
		}
	}
	if err := insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvProviderRequeue, Detail: d}); err != nil {
		return err
	}
	switch j.State {
	case StateStarting, StateGenerating:
		return r.moveJob(ctx, tx, f, j, StateFailing, Event{AttemptID: a.ID, Name: EvFailed, Code: CodeWorkerLost}, set("error_code", CodeWorkerLost))
	}
	return nil
}
