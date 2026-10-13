// Package upload is the worker-facing capability channel: HMAC upload URLs, the streaming PUT handler,
// the attempt start handshake and the uploads table (M3 C74). M3-B2 freezes the constructor, the method
// set and the signer; M3-B5 replaces the STUB bodies (the handlers answer 501 until then).
package upload

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a STUB(M3-B5) body.
var ErrNotImplemented = errors.New("upload: not implemented (STUB(M3-B5))")

// Querier is the read side shared by *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ServiceOptions builds a Service.
type ServiceOptions struct {
	Data      *os.Root // the data directory root; uploads live under incoming/uploads
	Signer    *Signer
	Store     *store.Store
	Clock     clock.Clock
	NewID     func() string // nil => UUIDv7
	PublicURL string
	// Wake nudges the runner (an arriving mm-result.json); nil-safe.
	Wake func()
}

// Service owns the uploads table and the two worker routes.
type Service struct{ o ServiceOptions }

// NewService builds the service.
func NewService(o ServiceOptions) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &Service{o: o}
}

// JobRef names the job an attempt belongs to.
type JobRef struct{ JobID, BatchID string }

// Received is one verified upload of an attempt; Path is relative to the data root.
type Received struct {
	UploadID, Artifact, SHA256 string
	Bytes                      int64
	Path                       string
}

// OpenAttempt inserts the attempt's upload rows in state 'open', inside the claim transaction. STUB(M3-B5).
func (s *Service) OpenAttempt(ctx context.Context, tx *sql.Tx, job JobRef, attemptID string, artifacts map[string]int64, exp int64, lease store.LeaseFence) error {
	return ErrNotImplemented
}

// Mint builds the capability URLs of an attempt in memory only; they are never persisted. STUB(M3-B5).
func (s *Service) Mint(job JobRef, attemptID string, exp int64) (uploads map[string]provider.UploadTarget, startURL string, err error) {
	return nil, "", ErrNotImplemented
}

// Received lists the attempt's received uploads. STUB(M3-B5).
func (s *Service) Received(ctx context.Context, q Querier, attemptID string) ([]Received, error) {
	return nil, ErrNotImplemented
}

// Consume marks received uploads consumed. STUB(M3-B5).
func (s *Service) Consume(ctx context.Context, tx *sql.Tx, uploadIDs []string) error {
	return ErrNotImplemented
}

// CloseAttempt closes the open uploads of an attempt. STUB(M3-B5).
func (s *Service) CloseAttempt(ctx context.Context, tx *sql.Tx, attemptID string) error {
	return ErrNotImplemented
}

// RemoveConsumedFiles deletes consumed upload files after the take commits. STUB(M3-B5).
func (s *Service) RemoveConsumedFiles(ctx context.Context, uploadIDs []string) error {
	return ErrNotImplemented
}

// SweepFiles removes received files of jobs that ended without a take, 24 h after their terminal state
// (C68). STUB(M3-B5).
func (s *Service) SweepFiles(ctx context.Context, now time.Time) (removed int, err error) {
	return 0, ErrNotImplemented
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	problem.Write(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "not implemented", map[string]any{"stub": "M3-B5"})
}

// Handler serves PUT /worker/v1/uploads/{id}. STUB(M3-B5): answers 501.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(notImplemented) }

// StartHandler serves POST /worker/v1/attempts/{id}/start. STUB(M3-B5): answers 501.
func (s *Service) StartHandler() http.Handler { return http.HandlerFunc(notImplemented) }
