// Package upload is the worker-facing capability channel: HMAC upload URLs, the streaming PUT handler,
// the attempt start handshake and the uploads table (M3 C74).
package upload

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"sync"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/store"
)

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
	// Log receives one line per worker request (method, path without query, status, bytes); nil => slog.Default().
	Log *slog.Logger
}

// Service owns the uploads table and the two worker routes.
type Service struct {
	o ServiceOptions

	mu     sync.Mutex
	flight map[string]struct{} // upload ids with a PUT in flight (C74.2 step 6)

	// beforeLock runs between the first read of the upload row and the in-flight lock; tests use it to
	// let another request finish in that window.
	beforeLock func()
}

// NewService builds the service.
func NewService(o ServiceOptions) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{o: o, flight: map[string]struct{}{}}
}

func (s *Service) newID() string {
	if s.o.NewID != nil {
		return s.o.NewID()
	}
	return uuid.NewV7().String()
}

func (s *Service) wake() {
	if s.o.Wake != nil {
		s.o.Wake()
	}
}

// lock claims the in-flight slot of an upload id; false means another PUT holds it.
func (s *Service) lock(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.flight[id]; busy {
		return false
	}
	s.flight[id] = struct{}{}
	return true
}

func (s *Service) unlock(id string) {
	s.mu.Lock()
	delete(s.flight, id)
	s.mu.Unlock()
}

func (s *Service) inFlight(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, busy := s.flight[id]
	return busy
}

// JobRef names the job an attempt belongs to.
type JobRef struct{ JobID, BatchID string }

// Received is one verified upload of an attempt; Path is relative to the data root.
type Received struct {
	UploadID, Artifact, SHA256 string
	Bytes                      int64
	Path                       string
}
