package sequence

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// Lane SR replaces these bodies and deletes this file. The signatures are frozen by L3-F2.

// LoadFeatures reads the three features of each ref with one statement per table.
func LoadFeatures(ctx context.Context, q Querier, refs []Ref) ([]sequencer.Song, map[string]FeatureBasis, error) {
	return nil, nil, errors.ErrUnsupported
}

// Suggest answers suggest_sequence: a read-only plan that never writes (C138.1).
func (s *Service) Suggest(ctx context.Context, in SuggestInput) (Suggestion, error) {
	return Suggestion{}, errors.ErrUnsupported
}
