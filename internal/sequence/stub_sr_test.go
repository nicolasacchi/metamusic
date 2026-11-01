package sequence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// The signatures of C138 that lane SR must keep; this file goes with stub_sr.go.
var (
	_ func(context.Context, sequence.Querier, []sequence.Ref) ([]sequencer.Song, map[string]sequence.FeatureBasis, error) = sequence.LoadFeatures
	_ func(*sequence.Service, context.Context, sequence.SuggestInput) (sequence.Suggestion, error)                        = (*sequence.Service).Suggest
)

func TestStub_SuggestUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := sequence.New(nil).Suggest(context.Background(), sequence.SuggestInput{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Suggest = %v, want ErrUnsupported until lane SR", err)
	}
}
