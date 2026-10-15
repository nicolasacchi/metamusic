package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Session list/rename service (C92.2). The method set is frozen by L1-B2; the bodies are L1-B5's
// (STUB(L1-B5)). The sync functions (sync.go, ensure.go) are real since L1-B3.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the Service.
func New(st *store.Store, now func() time.Time) *Service { return &Service{st: st, now: now} }

// List pages Sessions (C92.2).
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	return Page{}, errors.ErrUnsupported // STUB(L1-B5)
}

// Update renames or hides a Session under the revision lock (C92.2).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Session, error) {
	return Session{}, errors.ErrUnsupported // STUB(L1-B5)
}
