package sequencer_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// The signatures of C137 that lanes SQ1 and SQ2 must keep; SQ2 deletes this file with stub_sq.go.
var (
	_ func([]sequencer.Song, sequencer.Options) (sequencer.Result, error) = sequencer.Plan
	_ func(sequencer.Song, sequencer.Song) sequencer.Parts                = sequencer.TransitionMicro
	_ func(sequencer.Song, sequencer.Song) int64                          = sequencer.Distance
	_ func(sequencer.Step, sequencer.Strategy) string                     = sequencer.ExplainText
	_ func([]sequencer.Song, sequencer.Options) string                    = sequencer.Digest
)

func TestStub_PlanUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := sequencer.Plan(nil, sequencer.Options{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Plan = %v, want ErrUnsupported until lane SQ2", err)
	}
}

func TestStrategies_Registry(t *testing.T) {
	t.Parallel()
	want := []sequencer.Strategy{"arc", "ramp_up", "steady"}
	if !slices.Equal(sequencer.Strategies, want) {
		t.Errorf("Strategies = %v, want %v", sequencer.Strategies, want)
	}
	if sequencer.MaxSongs != 60 || sequencer.ExactMax != 12 || sequencer.BeamWidth != 8 || sequencer.MaxLocalPasses != 50 {
		t.Error("a C133 constant changed")
	}
}
