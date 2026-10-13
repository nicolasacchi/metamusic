package duels

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
)

// STUB(M2b-J2): every method below answers errors.ErrUnsupported (501 NOT_IMPLEMENTED at the API) until M2b-J2.

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }

// Create makes a duel (C48.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Duel, error) { // STUB(M2b-J2)
	return Duel{}, errors.ErrUnsupported
}

// Next returns the oldest duel waiting for p's source (C48.5).
func (s *Service) Next(ctx context.Context, p auth.Principal, exclude []string) (NextResult, error) { // STUB(M2b-J2)
	return NextResult{}, errors.ErrUnsupported
}

// Get returns p's view of a duel.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Duel, error) { // STUB(M2b-J2)
	return Duel{}, errors.ErrUnsupported
}

// List pages duels, newest first.
func (s *Service) List(ctx context.Context, p auth.Principal, f ListFilter) (Page, error) { // STUB(M2b-J2)
	return Page{}, errors.ErrUnsupported
}

// Submit records a verdict (C48.4).
func (s *Service) Submit(ctx context.Context, p auth.Principal, id string, in VerdictInput) (SubmitResult, error) { // STUB(M2b-J2)
	return SubmitResult{}, errors.ErrUnsupported
}

// Ladder fits and returns the ladder (C48.6).
func (s *Service) Ladder(ctx context.Context, q LadderQuery) (LadderPage, error) { // STUB(M2b-J2)
	return LadderPage{}, errors.ErrUnsupported
}

// PairStats counts verdicts on duels between exactly a and b (C48.7).
func (s *Service) PairStats(ctx context.Context, a, b string) (ladder.PairStats, error) { // STUB(M2b-J2)
	return ladder.PairStats{}, errors.ErrUnsupported
}

// Strengths returns the global fit restricted to ids, and nu.
func (s *Service) Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error) { // STUB(M2b-J2)
	return nil, 0, errors.ErrUnsupported
}

// OpenClipKeys returns the clip keys of both slots of every open duel (the clip cache pins).
func (s *Service) OpenClipKeys(ctx context.Context) (map[string]struct{}, error) { // STUB(M2b-J2)
	return nil, errors.ErrUnsupported
}
