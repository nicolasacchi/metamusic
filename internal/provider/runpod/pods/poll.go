package pods

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The poll protocol (C395). The handler writes observations only (last_poll_at, first_poll_at, last_reported, boot_json, and an
// assignment's done fields); pod and job states are moved by the lifecycle loop and the runner, which the handler nudges.
const (
	// MaxPollBody bounds a poll request body (C395.1).
	MaxPollBody = 16 << 10
	// HoldS is how long an idle poll is held for a hand-out before the answer "wait" (C395.4).
	HoldS = 20
	// handoutMarginS is the fixed term of the hand-out rule: now + timeout_s + K + 30 s <= deadline_at.
	handoutMarginS = 30
)

// Poll actions.
const (
	ActionWait = "wait"
	ActionTake = "take"
	ActionStop = "stop"
)

// PollRequest is mm-pod-poll-1.request (worker/src/mmworker/schema/mm-pod-poll-1.request.schema.json). Every key is required; the
// nullable ones are present with null.
type PollRequest struct {
	State     string    `json:"state"` // booting | idle | busy
	Agent     PollAgent `json:"agent"`
	Boot      *PollBoot `json:"boot"`
	AttemptID *string   `json:"attempt_id"`
	Done      *PollDone `json:"done"`
}

// PollAgent identifies the agent process.
type PollAgent struct {
	Version string `json:"version"`
	Build   string `json:"build"`
}

// PollBoot is what the pod reports about itself once it has booted.
type PollBoot struct {
	GPUName      string  `json:"gpu_name"`
	CUDA         string  `json:"cuda"`
	WeightsReady bool    `json:"weights_ready"`
	LoadS        float64 `json:"load_s"`
}

// PollDone reports a finished take.
type PollDone struct {
	AttemptID   string  `json:"attempt_id"`
	Status      string  `json:"status"` // ok | error
	ExecutionMS int64   `json:"execution_ms"`
	ErrorCode   *string `json:"error_code"`
}

// PollResponse is mm-pod-poll-1.response. Take is an mm.worker/1 or /2 input document or null; Cancel is never null.
type PollResponse struct {
	Action       string          `json:"action"` // wait | take | stop
	WaitS        int             `json:"wait_s"`
	Take         json.RawMessage `json:"take"`
	Cancel       []string        `json:"cancel"`
	DeadlineUnix int64           `json:"deadline_unix"`
}

var (
	pollPathRE = regexp.MustCompile(`^/worker/v1/pods/([0-9a-f-]{36})/poll$`)
	sigRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-f-]{36}$`)
	codeRE     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	pollKeys   = []string{"state", "agent", "boot", "attempt_id", "done"}
)

// PollOptions configures the poll handler.
type PollOptions struct {
	Store      *store.Store
	Signer     *Signer
	Queue      *Queue
	Clock      clock.Clock // nil = real
	Log        *slog.Logger
	KillSlackS int    // K: the worker's kill slack, in seconds
	Nudge      func() // wakes the lifecycle loop; nil = none
	// Hold is how long an idle poll waits for a take: 0 = HoldS seconds (C395.4), negative = do not wait (tests that need no hold).
	Hold time.Duration
}

// Poller serves POST /worker/v1/pods/{pod_id}/poll. It is not an operation: no registry entry, no OpenAPI path, no MCP tool.
type Poller struct{ o PollOptions }

// NewPoller returns the handler; it starts nothing and makes no network call.
func NewPoller(o PollOptions) *Poller {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Queue == nil {
		o.Queue = NewQueue()
	}
	return &Poller{o: o}
}

// Handler is mounted at /worker/v1/pods/ by internal/httpserver.
func (p *Poller) Handler() http.Handler { return http.HandlerFunc(p.serve) }

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func fail(w http.ResponseWriter, status int, code, msg string) {
	problem.Write(w, status, code, msg, nil)
}

func (p *Poller) serve(w http.ResponseWriter, r *http.Request) {
	rec := &recorder{ResponseWriter: w}
	w.Header().Set("Cache-Control", "no-store")
	p.handle(rec, r)
	// method, the path with the pod id masked, status, bytes: never the query (exp, sig), a take or a URL
	p.o.Log.Info("worker request", "method", r.Method, "path", redact.String(r.URL.Path), "status", rec.status, "bytes", rec.bytes)
}

func (p *Poller) handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	m := pollPathRE.FindStringSubmatch(r.URL.Path)
	if m == nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	podID := m[1]
	exp, sig, ok := linkParams(r)
	if !ok || !p.o.Signer.Verify(podID, exp, sig) {
		fail(w, http.StatusForbidden, "POD_LINK_INVALID", "pod link invalid")
		return
	}
	now := p.o.Clock.Now()
	if exp < now.Unix() {
		fail(w, http.StatusForbidden, "POD_LINK_EXPIRED", "pod link expired")
		return
	}
	req, status, code := readPoll(w, r)
	if req == nil {
		fail(w, status, code, "request body refused")
		return
	}
	row, err := GetRow(ctx, p.o.Store.R, podID)
	if errors.Is(err, ErrNotFound) || (err == nil && row.PollExp != exp) {
		fail(w, http.StatusForbidden, "POD_LINK_INVALID", "pod link invalid")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	obs, err := p.observe(ctx, row, req, now)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if obs.changed && p.o.Nudge != nil {
		p.o.Nudge()
	}
	cancelled, err := p.batchCancelled(ctx, row.BatchID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	resp := PollResponse{Cancel: []string{}, DeadlineUnix: row.DeadlineAt.Unix()}
	if stopped(row.State) || cancelled {
		resp.Action = ActionStop
		write(w, resp)
		return
	}
	if resp.Cancel, err = p.cancelList(ctx, row.ID); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	resp.Action, resp.WaitS = ActionWait, HoldS
	canHand := row.Kind == KindBatch && req.State == "idle" && !obs.restarted &&
		(row.State == StateReady || (row.State == StateBusy && obs.doneRecorded))
	if !canHand {
		write(w, resp)
		return
	}
	take, err := p.handOut(ctx, row)
	switch {
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	case take != nil:
		resp.Action, resp.WaitS, resp.Take = ActionTake, 0, take
	}
	write(w, resp)
}

func write(w http.ResponseWriter, resp PollResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// linkParams parses exp and sig of the capability URL; ok is false for a missing or malformed pair.
func linkParams(r *http.Request) (exp int64, sig string, ok bool) {
	q := r.URL.Query()
	s := q.Get("sig")
	if !sigRE.MatchString(s) {
		return 0, "", false
	}
	n, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, s, true
}

// readPoll reads and strictly decodes the body: at most MaxPollBody bytes (413), a JSON object with exactly the five keys and no unknown
// field anywhere (400). It returns nil and the problem to send on refusal.
func readPoll(w http.ResponseWriter, r *http.Request) (*PollRequest, int, string) {
	if r.ContentLength > MaxPollBody {
		return nil, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE"
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPollBody))
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return nil, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE"
	}
	if err != nil {
		return nil, http.StatusBadRequest, "BAD_REQUEST"
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return nil, http.StatusBadRequest, "BAD_REQUEST"
	}
	for _, k := range pollKeys {
		if _, ok := keys[k]; !ok {
			return nil, http.StatusBadRequest, "BAD_REQUEST"
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req PollRequest
	if err := dec.Decode(&req); err != nil || dec.More() {
		return nil, http.StatusBadRequest, "BAD_REQUEST"
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, http.StatusBadRequest, "BAD_REQUEST"
	}
	if !req.valid() {
		return nil, http.StatusBadRequest, "BAD_REQUEST"
	}
	return &req, 0, ""
}

func (q *PollRequest) valid() bool {
	if q.State != "booting" && q.State != "idle" && q.State != "busy" {
		return false
	}
	if n := len(q.Agent.Version); n < 1 || n > 64 {
		return false
	}
	if n := len(q.Agent.Build); n < 1 || n > 64 {
		return false
	}
	if b := q.Boot; b != nil && (len(b.GPUName) > 128 || len(b.CUDA) > 32 || b.LoadS < 0 || b.LoadS > 86400) {
		return false
	}
	if q.AttemptID != nil && !uuidRE.MatchString(*q.AttemptID) {
		return false
	}
	if d := q.Done; d != nil {
		if !uuidRE.MatchString(d.AttemptID) || (d.Status != "ok" && d.Status != "error") || d.ExecutionMS < 0 || d.ExecutionMS > 1<<40 {
			return false
		}
		if d.ErrorCode != nil && !codeRE.MatchString(*d.ErrorCode) {
			return false
		}
	}
	return true
}

type observation struct {
	changed      bool // something the loop should see: a first poll, a done, a restart
	doneRecorded bool // this request recorded the done of the pod's handed take (or repeated it)
	restarted    bool
}

// observe records what the request reports (C395.3, C395.5) in one transaction.
func (p *Poller) observe(ctx context.Context, row Row, req *PollRequest, now time.Time) (observation, error) {
	var obs observation
	at := store.FormatTime(now)
	tx, err := p.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return obs, err
	}
	defer tx.Rollback()
	var boot any
	if req.Boot != nil {
		b, err := json.Marshal(req.Boot)
		if err != nil {
			return obs, err
		}
		boot = string(b)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pods SET last_poll_at = ?, first_poll_at = COALESCE(first_poll_at, ?), last_reported = ?,
		boot_json = COALESCE(?, boot_json), updated_at = ? WHERE id = ?`, at, at, req.State, boot, at, row.ID); err != nil {
		return obs, err
	}
	obs.changed = row.FirstPollAt == nil
	var closed []string // attempts closed by this request: their cancel requests are forgotten after the commit
	if d := req.Done; d != nil {
		res, err := recordDone(ctx, tx, row.ID, d, now)
		if err != nil {
			return obs, err
		}
		obs.doneRecorded, obs.changed = res.known, obs.changed || res.fresh
		if res.known {
			closed = append(closed, d.AttemptID)
		}
	}
	if req.State == "booting" && (row.State == StateReady || row.State == StateBusy) {
		obs.restarted = true
		lost, err := MarkLost(ctx, tx, row.ID, codeWorkerReboot, now)
		if err != nil {
			return obs, err
		}
		// the loop moves the pod; until then every booting poll repeats the report, which is logged and nudged once
		if len(lost) > 0 || row.LastReported != "booting" {
			obs.changed = true
			if err := AppendEvent(ctx, tx, row.ID, Event{At: now, Event: "restart_reported", Code: codeWorkerReboot, Detail: map[string]any{"lost": len(lost)}}); err != nil {
				return obs, err
			}
		}
		closed = append(closed, lost...)
	}
	if err := tx.Commit(); err != nil {
		return obs, err
	}
	for _, id := range closed {
		p.o.Queue.ClearCancel(id)
	}
	return obs, nil
}

type doneResult struct{ known, fresh bool }

// recordDone sets the done fields of the pod's handed assignment once; execution_ms is measured by the server (done_at - handed_at).
// A repeat of the same report writes nothing; a report for an attempt this pod was not handed is ignored.
func recordDone(ctx context.Context, tx *sql.Tx, podID string, d *PollDone, now time.Time) (doneResult, error) {
	var handed string
	var done sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT handed_at, done_at FROM pod_assignments WHERE attempt_id = ? AND pod_id = ?`, d.AttemptID, podID).Scan(&handed, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return doneResult{}, nil
	}
	if err != nil {
		return doneResult{}, err
	}
	if done.Valid {
		return doneResult{known: true}, nil
	}
	ht, err := store.ParseTime(handed)
	if err != nil {
		return doneResult{}, err
	}
	ms := max(now.Sub(ht).Milliseconds(), 0)
	var code any
	if d.ErrorCode != nil {
		code = *d.ErrorCode
	}
	res, err := tx.ExecContext(ctx, `UPDATE pod_assignments SET done_at = ?, status = ?, execution_ms = ?, agent_execution_ms = ?, error_code = ?
		WHERE attempt_id = ? AND pod_id = ? AND done_at IS NULL`, store.FormatTime(now), d.Status, ms, d.ExecutionMS, code, d.AttemptID, podID)
	if err != nil {
		return doneResult{}, err
	}
	n, err := res.RowsAffected()
	return doneResult{known: true, fresh: n == 1}, err
}

// batchCancelled: the batch is no longer active, or every unfinished job of it has a cancel request.
func (p *Poller) batchCancelled(ctx context.Context, batchID string) (bool, error) {
	var stop int
	err := p.o.Store.R.QueryRowContext(ctx, `SELECT (b.state <> 'active') OR NOT EXISTS (SELECT 1 FROM jobs j WHERE j.batch_id = b.id
		AND j.state NOT IN ('completed','failed','cancelled','expired') AND j.cancel_requested_at IS NULL) FROM batches b WHERE b.id = ?`, batchID).Scan(&stop)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return stop == 1, err
}

// cancelList lists the pod's handed, not-done attempts the runner asked to cancel.
func (p *Poller) cancelList(ctx context.Context, podID string) ([]string, error) {
	rows, err := p.o.Store.R.QueryContext(ctx, `SELECT attempt_id FROM pod_assignments WHERE pod_id = ? AND handed_at IS NOT NULL AND done_at IS NULL ORDER BY handed_at, attempt_id`, podID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if p.o.Queue.CancelRequested(id) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// handOut gives the pod the oldest queued take of its batch, holding the request up to HoldS seconds (by the injected clock) when
// nothing is queued. nil means "answer wait": nothing arrived, the oldest take does not fit before the deadline, or the pod stopped.
func (p *Poller) handOut(ctx context.Context, row Row) (json.RawMessage, error) {
	hold := p.o.Hold
	if hold == 0 {
		hold = HoldS * time.Second
	}
	shutdown := reqinfo.ShutdownDone(ctx)
	timer := p.o.Clock.NewTimer(max(hold, 0))
	defer timer.Stop()
	for {
		changed := p.o.Queue.Changed() // taken before the check, so a take added in between is not missed
		raw, res, err := p.tryHand(ctx, row)
		if err != nil || raw != nil || res == TakeUnfit || p.o.Hold < 0 {
			return raw, err
		}
		select {
		case <-changed:
		case <-timer.C():
			return nil, nil
		case <-ctx.Done():
			return nil, nil
		case <-shutdown: // a nil channel (no server) blocks forever
			return nil, nil
		}
	}
}

// tryHand takes one queued take and records the hand-out; a take that cannot be built or whose assignment is no longer queued is
// dropped (and recorded) and the next one is tried.
func (p *Poller) tryHand(ctx context.Context, row Row) (json.RawMessage, TakeResult, error) {
	for {
		now := p.o.Clock.Now()
		spec, res := p.o.Queue.Take(row.BatchID, func(t provider.TakeSpec) bool {
			return !now.Add(time.Duration(t.TimeoutS+p.o.KillSlackS+handoutMarginS) * time.Second).After(row.DeadlineAt)
		})
		if res != TakeOK {
			return nil, res, nil
		}
		raw, err := provider.BuildWorkerInput(spec)
		if err != nil {
			if _, cerr := CloseAssignment(ctx, p.o.Store.W, spec.AttemptID, AssignNotHanded, "BAD_REQUEST", now); cerr != nil {
				p.o.Queue.Requeue(row.BatchID, spec)
				return nil, res, cerr
			}
			continue
		}
		outcome, err := p.markHanded(ctx, spec.AttemptID, row.ID, now)
		if err != nil {
			p.o.Queue.Requeue(row.BatchID, spec)
			return nil, res, err
		}
		switch outcome {
		case handGone:
			continue // cancelled or closed while queued
		case handBlocked:
			// the pod stopped, the batch ended or the pod still holds an unfinished take since the row was read: keep the take queued
			p.o.Queue.Requeue(row.BatchID, spec)
			return nil, TakeUnfit, nil
		}
		return raw, TakeOK, nil
	}
}

type handOutcome int

const (
	handOK      handOutcome = iota
	handGone                // the assignment is no longer an open, unhanded one
	handBlocked             // the pod or its batch no longer qualifies, or the pod has an unfinished take
)

// markHanded sets pod_id and handed_at once, in one statement that re-checks what the request read earlier: the pod is a ready or busy
// batch pod of an active batch and has no handed, not-done assignment.
func (p *Poller) markHanded(ctx context.Context, attemptID, podID string, now time.Time) (handOutcome, error) {
	res, err := p.o.Store.W.ExecContext(ctx, `UPDATE pod_assignments SET pod_id = ?1, handed_at = ?2 WHERE attempt_id = ?3 AND pod_id IS NULL AND done_at IS NULL
		AND EXISTS (SELECT 1 FROM pods p JOIN batches b ON b.id = p.batch_id WHERE p.id = ?1 AND p.kind = 'batch' AND p.state IN ('ready','busy')
			AND b.state = 'active' AND b.id = pod_assignments.batch_id)
		AND NOT EXISTS (SELECT 1 FROM pod_assignments o WHERE o.pod_id = ?1 AND o.handed_at IS NOT NULL AND o.done_at IS NULL)`,
		podID, store.FormatTime(now), attemptID)
	if err != nil {
		return handGone, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 1 {
		return handOK, err
	}
	var open int
	err = p.o.Store.W.QueryRowContext(ctx, `SELECT COUNT(*) FROM pod_assignments WHERE attempt_id = ? AND pod_id IS NULL AND done_at IS NULL`, attemptID).Scan(&open)
	if err != nil {
		return handGone, err
	}
	if open == 0 {
		return handGone, nil
	}
	return handBlocked, nil
}
