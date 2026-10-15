package muses

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Muse service (C89). The method set is frozen by L1-B2; the bodies are L1-B5's (STUB(L1-B5)) and
// answer errors.ErrUnsupported until then. EnsureReferenceTx (ensure.go) is real since L1-B3.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// New builds the Service.
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, now: now, newID: newID}
}

// Create makes a Muse (C89.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewMuse) (Muse, error) {
	return Muse{}, errors.ErrUnsupported // STUB(L1-B5)
}

// Get reads one Muse.
func (s *Service) Get(ctx context.Context, id string) (Muse, error) {
	return Muse{}, errors.ErrUnsupported // STUB(L1-B5)
}

// Ref reads the text of a Muse as a MuseRef; the only way other packages read Muse text.
func (s *Service) Ref(ctx context.Context, id string) (MuseRef, bool, error) {
	return MuseRef{}, false, errors.ErrUnsupported // STUB(L1-B5)
}

// List pages Muses (C89.1).
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	return Page{}, errors.ErrUnsupported // STUB(L1-B5)
}

// Update patches a Muse under the revision lock (C89.1).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Muse, error) {
	return Muse{}, errors.ErrUnsupported // STUB(L1-B5)
}

// AssignSongs assigns the Muse to 1..200 Songs, all or nothing (C89.1).
func (s *Service) AssignSongs(ctx context.Context, p auth.Principal, museID string, songIDs []string) (AssignResult, error) {
	return AssignResult{}, errors.ErrUnsupported // STUB(L1-B5)
}
