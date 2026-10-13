// Package listening records listen events (contract C36). M2a-B4a freezes the
// signatures; M2a-B8 implements them.
package listening

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Listening is the summary shown on track detail and in the context bundle.
type Listening = library.ListeningStats

// Event is one listen event.
type Event struct {
	ID, TrackID, SessionID, Kind string
	FromMS, ToMS                 *int64
	Format, DeviceID             *string
	ClientAt                     time.Time
}

// Rejected names an event that failed validation.
type Rejected struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// Service implements C36.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the listening service; nil now means time.Now.
func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

// Log stores events idempotently; src is the transport label.
func (s *Service) Log(ctx context.Context, p auth.Principal, src string, evs []Event) (accepted, duplicates int, rejected []Rejected, err error) {
	// STUB(M2a-B8)
	return 0, 0, nil, errors.ErrUnsupported
}

// Stats summarises human listening of one track.
func (s *Service) Stats(ctx context.Context, trackID string) (Listening, error) {
	// STUB(M2a-B8)
	return Listening{}, errors.ErrUnsupported
}
