// Package upload is the worker-facing capability channel: HMAC upload URLs, the streaming PUT handler,
// the attempt start handshake and the uploads table (M3 C74).
package upload

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"sync"
	"time"
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
	flight map[string]chan struct{} // upload ids with a PUT in flight (C74.2 step 6); closed when the PUT leaves

	// beforeLock runs between the first read of the upload row and the in-flight lock; tests use it to
	// let another request finish in that window.
	beforeLock func()

	// beforeWrite runs before each write of body bytes to the part file (tests simulate a stalled disk).
	beforeWrite func()

	// lockWait overrides lockWaitDefault (tests); zero means the default.
	lockWait time.Duration

	// idle overrides idleTimeout (tests); zero means the default.
	idle time.Duration
}

// NewService builds the service.
func NewService(o ServiceOptions) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{o: o, flight: map[string]chan struct{}{}}
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

// lockWaitDefault is how long a second PUT of an id waits for the first one to leave. It is deliberately
// longer than the worker's 60 s per-attempt socket timeout: a worker that retries while a stalled predecessor
// (blocked on a slow disk) still holds the id then sees a timeout, which it retries, instead of a fatal 409.
const lockWaitDefault = 90 * time.Second

// tryLock claims the in-flight slot of an upload id; when another PUT holds it, busy is closed as that PUT leaves.
func (s *Service) tryLock(id string) (ok bool, busy <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, held := s.flight[id]; held {
		return false, ch
	}
	s.flight[id] = make(chan struct{})
	return true, nil
}

// lock claims the slot, waiting up to lockWait for the holder to leave; false means the wait ran out or ctx ended.
func (s *Service) lock(ctx context.Context, id string) bool {
	wait := s.lockWait
	if wait <= 0 {
		wait = lockWaitDefault
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ok, busy := s.tryLock(id)
		if ok {
			return true
		}
		select {
		case <-busy:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (s *Service) unlock(id string) {
	s.mu.Lock()
	if ch, ok := s.flight[id]; ok {
		close(ch)
		delete(s.flight, id)
	}
	s.mu.Unlock()
}

func (s *Service) inFlight(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, busy := s.flight[id]
	return busy
}

// JobRef names the job an attempt belongs to. Engine selects the artifact allowlist its uploads are held to
// (provider.ArtifactsFor); empty means the first engine, yue2, so every pre-E2 caller is unchanged.
type JobRef struct{ JobID, BatchID, Engine string }

// Received is one verified upload of an attempt; Path is relative to the data root.
type Received struct {
	UploadID, Artifact, SHA256 string
	Bytes                      int64
	Path                       string
}
