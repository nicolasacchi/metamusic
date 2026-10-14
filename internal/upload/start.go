package upload

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	startPathRE = regexp.MustCompile(`^/worker/v1/attempts/([0-9a-f-]{36})/start$`)
	refRE       = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
)

// maxStartBody bounds the start handshake body (C74.3).
const maxStartBody = 1 << 10

// StartHandler serves POST /worker/v1/attempts/{id}/start (C74.3). It records an observation only: the
// runner is the single writer of attempt and job state.
func (s *Service) StartHandler() http.Handler { return s.logged(s.serveStart) }

func (s *Service) serveStart(w http.ResponseWriter, r *http.Request) {
	m := startPathRE.FindStringSubmatch(r.URL.Path)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST", nil)
		return
	}
	if m == nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "not found", nil)
		return
	}
	id := m[1]
	exp, sig, ok := linkParams(r)
	var jobID, attState, jobState string
	var winExp int64
	var winEnds string
	var ref, announced sql.NullString
	var cancelAt sql.NullString
	if ok {
		err := s.o.Store.R.QueryRowContext(r.Context(), `SELECT a.job_id, a.state, a.ref, a.announced_ref, a.window_ends_at, j.state, j.cancel_requested_at
			FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE a.id = ?`, id).Scan(&jobID, &attState, &ref, &announced, &winEnds, &jobState, &cancelAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			ok = false
		case err != nil:
			fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
			return
		}
		if t, perr := store.ParseTime(winEnds); ok && perr == nil {
			winExp = t.Unix()
		}
	}
	if !ok || !s.o.Signer.VerifyStart(id, jobID, exp, sig) {
		fail(w, http.StatusForbidden, "ATTEMPT_LINK_INVALID", "attempt link invalid", nil)
		return
	}
	if exp < s.o.Clock.Now().Unix() {
		fail(w, http.StatusForbidden, "ATTEMPT_LINK_EXPIRED", "attempt link expired", nil)
		return
	}
	if exp > winExp {
		fail(w, http.StatusForbidden, "ATTEMPT_LINK_INVALID", "attempt link invalid", nil)
		return
	}
	other, err := s.otherDelivered(r, jobID, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	if closedReason(jobState, attState, other, true) || cancelAt.Valid {
		fail(w, http.StatusGone, "ATTEMPT_CLOSED", "the attempt is closed", nil)
		return
	}
	var body struct {
		RunPodJobID string `json:"runpod_job_id"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxStartBody+1))
	if err != nil || len(raw) > maxStartBody || json.Unmarshal(raw, &body) != nil || !refRE.MatchString(body.RunPodJobID) {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "body must be {\"runpod_job_id\": \"<id>\"}", nil)
		return
	}
	switch status, err := s.announce(r, id, jobID, body.RunPodJobID, ref, announced); {
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	case status == http.StatusConflict:
		fail(w, http.StatusConflict, "ATTEMPT_REF_CONFLICT", "a different provider job was announced for this attempt", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"continue": true})
	s.wake()
}

// announce records the observation (one statement) and its job event; a repeat of the same id writes
// nothing. A different id than the stored ref or announced_ref is a conflict, recorded as an event.
func (s *Service) announce(r *http.Request, attemptID, jobID, announcedRef string, ref, prior sql.NullString) (int, error) {
	ctx := r.Context()
	now := store.FormatTime(s.o.Clock.Now())
	conflict := (ref.Valid && ref.String != announcedRef) || (prior.Valid && prior.String != announcedRef)
	if !conflict && prior.Valid {
		return http.StatusOK, nil // already observed
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	event := "start"
	if conflict {
		event = "start_conflict"
	}
	// The observation is recorded even for a conflicting id when nothing was announced yet: the runner
	// compares announced_ref with ref and treats a difference as a provider requeue (C73.2 rule 6). A
	// second, different announcement is only an event (announced_ref is set once).
	if !prior.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE job_attempts SET announced_at = COALESCE(announced_at, ?), announced_ref = COALESCE(announced_ref, ?)
			WHERE id = ? AND state IN ('pending','submitted','unknown')`, now, announcedRef, attemptID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, event) VALUES (?, ?, ?, ?)`, jobID, attemptID, now, event); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if conflict {
		return http.StatusConflict, nil
	}
	return http.StatusOK, nil
}
