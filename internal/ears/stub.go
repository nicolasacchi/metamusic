package ears

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// STUB(M2b-E3): every method below answers errors.ErrUnsupported (501 NOT_IMPLEMENTED at the API) until M2b-E3.

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }

// UseJudgments wires the duels service after both are built (app.Build creates ears first).
func (s *Service) UseJudgments(j Judgments) { s.o.Judgments = j }

// AnalyzeTrack runs the missing Tier A analyzers of one track (C51.1).
func (s *Service) AnalyzeTrack(ctx context.Context, trackID string, o AnalyzeOptions) (Outcome, error) { // STUB(M2b-E3)
	return Outcome{}, errors.ErrUnsupported
}

// Backfill analyses every track (or o.TrackIDs) that lacks analyzers.
func (s *Service) Backfill(ctx context.Context, o BackfillOptions) (BackfillReport, error) { // STUB(M2b-E3)
	return BackfillReport{}, errors.ErrUnsupported
}

// Dossier builds the get_track_analysis payload (C51.4).
func (s *Service) Dossier(ctx context.Context, trackID string) (Dossier, error) { // STUB(M2b-E3)
	return Dossier{}, errors.ErrUnsupported
}

// Spectrogram builds the get_track_spectrogram payload (C51.5).
func (s *Service) Spectrogram(ctx context.Context, p auth.Principal, trackID string, inline bool) (SpectrogramView, error) { // STUB(M2b-E3)
	return SpectrogramView{}, errors.ErrUnsupported
}

// Compare builds the compare_tracks payload (C51.6).
func (s *Service) Compare(ctx context.Context, a, b string) (Comparison, error) { // STUB(M2b-E3)
	return Comparison{}, errors.ErrUnsupported
}

// Perception builds the /context perception block (C51.8).
func (s *Service) Perception(ctx context.Context, trackID string) (Perception, error) { // STUB(M2b-E3)
	return Perception{}, errors.ErrUnsupported
}
