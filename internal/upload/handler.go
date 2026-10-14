package upload

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	uploadPathRE = regexp.MustCompile(`^/worker/v1/uploads/([0-9a-f-]{36})$`)
	digestRE     = regexp.MustCompile(`^sha-256=:([A-Za-z0-9+/]{43}=):$`)
	sigRE        = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// readDeadline bounds the body of one PUT (C74.2 step 7).
const readDeadline = 10 * time.Minute

// recorder captures the status and the byte count of a response for the one log line per request.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logged wraps a worker route: no-store, and one slog line with the method, the path without its query,
// the status and the response bytes (never sig, exp or any URL).
func (s *Service) logged(h func(w http.ResponseWriter, r *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w}
		w.Header().Set("Cache-Control", "no-store")
		h(rec, r)
		s.o.Log.Info("worker request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "bytes", rec.bytes)
	})
}

func fail(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	problem.Write(w, status, code, msg, details)
}

// linkParams parses exp and sig of a capability URL; ok is false for a missing or malformed pair.
func linkParams(r *http.Request) (exp int64, sig string, ok bool) {
	q := r.URL.Query()
	e, s := q.Get("exp"), q.Get("sig")
	if !sigRE.MatchString(s) {
		return 0, "", false
	}
	n, err := strconv.ParseInt(e, 10, 64)
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, s, true
}

type uploadRow struct {
	ID, JobID, AttemptID, Artifact, State string
	MaxBytes, Exp                         int64
	SHA                                   sql.NullString
	Bytes                                 sql.NullInt64
	JobState, AttemptState                string
}

// closedReason says why an attempt may not deliver any more (C74.2 step 3 / C74.3 step 2): the job is
// terminal, the attempt is rejected (or done, for the start handshake: doneCloses), or another attempt already
// delivered the verified result. A done attempt may still finish its uploads: its provider status can arrive
// before the last file is committed.
func closedReason(jobState, attemptState string, otherWon, doneCloses bool) bool {
	switch jobState {
	case "completed", "failed", "cancelled", "expired":
		return true
	}
	return attemptState == "rejected" || (doneCloses && attemptState == "done") || otherWon
}

// otherDelivered reports whether a verified result of another attempt of the job exists: the runner
// records it as a result_received event carrying the winning attempt's id.
func (s *Service) otherDelivered(r *http.Request, jobID, attemptID string) (bool, error) {
	var n int
	err := s.o.Store.R.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'result_received' AND attempt_id IS NOT NULL AND attempt_id <> ?`,
		jobID, attemptID).Scan(&n)
	return n > 0, err
}

// Handler serves PUT /worker/v1/uploads/{id} (C74.2).
func (s *Service) Handler() http.Handler { return s.logged(s.serveUpload) }

func (s *Service) serveUpload(w http.ResponseWriter, r *http.Request) {
	m := uploadPathRE.FindStringSubmatch(r.URL.Path)
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		fail(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use PUT", nil)
		return
	}
	if m == nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "not found", nil)
		return
	}
	id := m[1]
	exp, sig, ok := linkParams(r)
	var row uploadRow
	if ok {
		err := s.o.Store.R.QueryRowContext(r.Context(), `SELECT u.id, u.job_id, u.attempt_id, u.artifact, u.state, u.max_bytes, u.exp, u.sha256, u.bytes, j.state, a.state
			FROM uploads u JOIN jobs j ON j.id = u.job_id JOIN job_attempts a ON a.id = u.attempt_id WHERE u.id = ?`, id).
			Scan(&row.ID, &row.JobID, &row.AttemptID, &row.Artifact, &row.State, &row.MaxBytes, &row.Exp, &row.SHA, &row.Bytes, &row.JobState, &row.AttemptState)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			ok = false
		case err != nil:
			fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
			return
		}
	}
	if !ok || !s.o.Signer.Verify(id, row.JobID, row.Artifact, row.MaxBytes, exp, sig) {
		fail(w, http.StatusForbidden, "UPLOAD_LINK_INVALID", "upload link invalid", nil)
		return
	}
	if exp < s.o.Clock.Now().Unix() {
		fail(w, http.StatusForbidden, "UPLOAD_LINK_EXPIRED", "upload link expired", nil)
		return
	}
	if exp > row.Exp {
		fail(w, http.StatusForbidden, "UPLOAD_LINK_INVALID", "upload link invalid", nil)
		return
	}
	other, err := s.otherDelivered(r, row.JobID, row.AttemptID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	if closedReason(row.JobState, row.AttemptState, other, false) || row.State == "closed" {
		fail(w, http.StatusGone, "UPLOAD_CLOSED", "uploads for this attempt are closed", nil)
		return
	}
	if r.ContentLength < 0 {
		fail(w, http.StatusLengthRequired, "LENGTH_REQUIRED", "Content-Length is required", nil)
		return
	}
	if r.ContentLength > row.MaxBytes {
		fail(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "upload exceeds max_bytes", map[string]any{"max_bytes": row.MaxBytes})
		return
	}
	want, ok := parseDigest(r.Header.Get("Content-Digest"))
	if !ok {
		fail(w, http.StatusBadRequest, "DIGEST_REQUIRED", "Content-Digest: sha-256=:<base64>: is required", nil)
		return
	}
	if row.State == "received" || row.State == "consumed" {
		if row.SHA.String == want {
			s.reply(w, http.StatusOK, row.ID, row.Artifact, want, row.Bytes.Int64)
			return
		}
		fail(w, http.StatusConflict, "UPLOAD_CONFLICT", "a different body was already received", nil)
		return
	}
	if s.beforeLock != nil {
		s.beforeLock()
	}
	if !s.lock(id) {
		w.Header().Set("Retry-After", "2")
		fail(w, http.StatusConflict, "UPLOAD_IN_PROGRESS", "another upload of this artifact is in progress", nil)
		return
	}
	defer s.unlock(id)
	// another PUT may have finished between the read above and the lock: look again (steps 3 and 5)
	var state string
	var sha sql.NullString
	var n sql.NullInt64
	if err := s.o.Store.R.QueryRowContext(r.Context(), `SELECT state, sha256, bytes FROM uploads WHERE id = ?`, id).Scan(&state, &sha, &n); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	row.State, row.SHA, row.Bytes = state, sha, n
	switch state {
	case "received", "consumed":
		if row.SHA.String == want {
			s.reply(w, http.StatusOK, row.ID, row.Artifact, want, row.Bytes.Int64)
			return
		}
		fail(w, http.StatusConflict, "UPLOAD_CONFLICT", "a different body was already received", nil)
		return
	case "closed":
		fail(w, http.StatusGone, "UPLOAD_CLOSED", "uploads for this attempt are closed", nil)
		return
	}
	s.receive(w, r, row, want)
}

// parseDigest decodes `sha-256=:<base64>:` into lower-case hex.
func parseDigest(h string) (string, bool) {
	m := digestRE.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil {
		return "", false
	}
	raw, _ := base64.StdEncoding.DecodeString(m[1]) // the pattern admits exactly 43 base64 characters and a pad: 32 bytes, always valid
	return hex.EncodeToString(raw), true
}

func (s *Service) reply(w http.ResponseWriter, status int, id, artifact, sha string, bytes int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": id, "artifact": artifact, "sha256": sha, "bytes": bytes})
}

// receive streams the body into the part file, verifies length and digest, and commits the row.
func (s *Service) receive(w http.ResponseWriter, r *http.Request, row uploadRow, want string) {
	root := s.o.Data
	if err := root.MkdirAll(uploadsDir, 0o700); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	part := uploadPath(row.ID) + ".part"
	_ = root.Remove(part) // a part file left by a crashed process; we hold the in-flight lock
	f, err := root.OpenFile(part, osCreateExcl, partMode)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = root.Remove(part)
		}
	}()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(readDeadline)) // real time: a socket deadline
	h := sha256.New()
	body := http.MaxBytesReader(w, r.Body, row.MaxBytes)
	n, err := io.Copy(io.MultiWriter(f, h), body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		fail(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "upload exceeds max_bytes", map[string]any{"max_bytes": row.MaxBytes})
		return
	case err != nil, n != r.ContentLength:
		fail(w, http.StatusBadRequest, "LENGTH_MISMATCH", "body length differs from Content-Length", nil)
		return
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		fail(w, http.StatusUnprocessableEntity, "UPLOAD_DIGEST_MISMATCH", "body does not match Content-Digest", nil)
		return
	}
	if err := f.Sync(); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	if err := f.Close(); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	done = true
	final := uploadPath(row.ID)
	if err := root.Chmod(part, finalMode); err != nil {
		_ = root.Remove(part)
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	if err := root.Rename(part, final); err != nil {
		_ = root.Remove(part)
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	switch err := s.commit(r, row, got, n); {
	case errors.Is(err, errClosed):
		_ = root.Remove(final)
		fail(w, http.StatusGone, "UPLOAD_CLOSED", "uploads for this attempt are closed", nil)
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	s.reply(w, http.StatusCreated, row.ID, row.Artifact, got, n)
	if row.Artifact == "mm-result.json" {
		s.wake()
	}
}

var errClosed = errors.New("upload: closed")

// commit records the received upload and its event in one transaction.
func (s *Service) commit(r *http.Request, row uploadRow, sha string, n int64) error {
	ctx := r.Context()
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.FormatTime(s.o.Clock.Now())
	res, err := tx.ExecContext(ctx, `UPDATE uploads SET state = 'received', sha256 = ?, bytes = ?, received_at = ? WHERE id = ? AND state = 'open'`, sha, n, now, row.ID)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return errClosed
	}
	detail, _ := json.Marshal(map[string]any{"upload_id": row.ID, "artifact": row.Artifact, "bytes": n})
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, event, detail_json) VALUES (?, ?, ?, 'upload', ?)`,
		row.JobID, row.AttemptID, now, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}
