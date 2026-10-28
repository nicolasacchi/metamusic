package records

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// STUB(L2-R-a): the frozen method set of C120 and C121. Lane R-a deletes this file and adds create.go, update.go, list.go,
// slots.go, edit.go and publish_rules.go; no signature changes. Every stub answers errors.ErrUnsupported, which the API
// maps to 501 NOT_IMPLEMENTED.

// Create makes a draft Record, with floating slots for SongIDs in order (C120.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewRecord) (Record, error) {
	return Record{}, errors.ErrUnsupported
}

// Get returns the Record row; views are recordview's.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	return Record{}, errors.ErrUnsupported
}

// List returns Records ordered by (updated_at DESC, id DESC) with a keyset cursor.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	return Page{}, errors.ErrUnsupported
}

// Update changes the text fields, re-rolls the cover or hides the Record under the revision lock (C120.1).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Record, error) {
	return Record{}, errors.ErrUnsupported
}

// EditSlots applies ops in one write transaction (C121.3).
func (s *Service) EditSlots(ctx context.Context, p auth.Principal, id string, revision int, ops []SlotOp) (EditResult, error) {
	return EditResult{}, errors.ErrUnsupported
}

// Propose moves a draft Record to proposed with a rationale (C120.1).
func (s *Service) Propose(ctx context.Context, p auth.Principal, id string, revision int, rationale string) (Record, error) {
	return Record{}, errors.ErrUnsupported
}

// Publish publishes or unpublishes a Record; the principal must be KindHuman (C120.1).
func (s *Service) Publish(ctx context.Context, p auth.Principal, id string, revision int, published bool) (Record, error) {
	return Record{}, errors.ErrUnsupported
}

// Slots returns the live slots in order.
func (s *Service) Slots(ctx context.Context, id string) ([]Slot, error) {
	return nil, errors.ErrUnsupported
}

// Resolved returns the live slots in order with their resolved Takes (C121.2): three statements, no N+1.
func (s *Service) Resolved(ctx context.Context, id string) ([]ResolvedSlot, error) {
	return nil, errors.ErrUnsupported
}
