package picks

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Ladder is the ladder source (same method set as ears.Judgments.Strengths); app.Build wires duels.Service to it, so
// picks never imports duels (C87).
type Ladder interface {
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// Service records and serves Picks (C91). The method set is frozen by L1-B2; the bodies are L1-B4's (STUB(L1-B4)).
type Service struct {
	st    *store.Store
	l     Ladder
	now   func() time.Time
	newID func() string
}

// New builds the Service. A nil Ladder makes the ladder rule never apply.
func New(st *store.Store, l Ladder, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, l: l, now: now, newID: newID}
}

// RecomputeTx recomputes the Picks of the Songs inside the Reconciler's transaction (C91.5).
func (s *Service) RecomputeTx(ctx context.Context, tx *sql.Tx, env Env, songIDs []string, st map[string]ladder.Strength) (Changes, error) {
	return Changes{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Sweep recomputes every live Song with >= 2 eligible takes and >= 1 counted human verdict (C91.5).
func (s *Service) Sweep(ctx context.Context) (Changes, error) {
	return Changes{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Get reads the Pick of a Song (C91.4).
func (s *Service) Get(ctx context.Context, songID string) (PickView, error) {
	return PickView{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Pin pins or unpins (nil takeID) a take; humans and paired devices only (C91.6).
func (s *Service) Pin(ctx context.Context, p auth.Principal, songID string, takeID, proposalID *string) (PinResult, error) {
	return PinResult{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Propose records an agent's proposal (C91.7).
func (s *Service) Propose(ctx context.Context, p auth.Principal, songID, takeID, rationale string) (Proposal, error) {
	return Proposal{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Dismiss closes an open proposal as dismissed; humans only (C91.7).
func (s *Service) Dismiss(ctx context.Context, p auth.Principal, proposalID string) (Proposal, error) {
	return Proposal{}, errors.ErrUnsupported // STUB(L1-B4)
}

// Events pages the pick-event feed by the integer cursor (C91.8).
func (s *Service) Events(ctx context.Context, f EventFilter) (EventPage, error) {
	return EventPage{}, errors.ErrUnsupported // STUB(L1-B4)
}
