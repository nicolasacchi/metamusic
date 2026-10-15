package songs

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Song list/update service (C90). The method set is frozen by L1-B2; the bodies are L1-B5's
// (STUB(L1-B5)) and answer errors.ErrUnsupported until then. The structural reconcile functions (reconcile.go) are
// real since L1-B3.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// New builds the Service.
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, now: now, newID: newID}
}

// List pages Songs (C90.7).
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	return Page{}, errors.ErrUnsupported // STUB(L1-B5)
}

// Update patches a Song under the revision lock (C90.5).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Song, error) {
	return Song{}, errors.ErrUnsupported // STUB(L1-B5)
}

// FilterSQL renders a ListFilter as a WHERE fragment over songs s with its arguments; sounds reuses it for facet
// counts (excludeFacet leaves one facet out). STUB(L1-B5): only the Sound clauses (filter.go, L1-B6) are real so far.
func FilterSQL(f ListFilter, excludeFacet string) (where string, args []any) {
	return SoundClauses(f, excludeFacet)
}
