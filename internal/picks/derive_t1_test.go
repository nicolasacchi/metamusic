package picks_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/picks"
)

func transform() func(*picks.TakeInput) { return func(t *picks.TakeInput) { t.Transform = true } }

// TestPick_TransformNeverNewest: a transform Take never wins by being the newest; a pin, the stars and the ladder can still
// select it (C211.6).
func TestPick_TransformNeverNewest(t *testing.T) {
	t.Parallel()
	gen := []picks.TakeInput{tk("a", 0), tk("b", 1)}
	withGlacier := func(opts ...func(*picks.TakeInput)) []picks.TakeInput {
		return append(append([]picks.TakeInput{}, gen...), tk("g", 2, append([]func(*picks.TakeInput){transform()}, opts...)...))
	}
	for _, c := range []struct {
		name   string
		in     picks.Input
		take   string
		source picks.Source
	}{
		{"newer glacier leaves the Pick", picks.Input{Takes: withGlacier()}, "b", picks.SourceNewest},
		{"only a transform: no Pick", picks.Input{Takes: []picks.TakeInput{tk("g", 0, transform())}}, "", picks.SourceNone},
		{"pinned glacier is the Pick", picks.Input{Takes: withGlacier(), PinnedTakeID: sp("g")}, "g", picks.SourcePinned},
		{"human 5 stars select it", picks.Input{Takes: withGlacier(stars(5))}, "g", picks.SourceStars},
		{"a duel win selects it", picks.Input{Takes: []picks.TakeInput{tk("a", 0, us(0, 1)), tk("b", 1, us(0, 1)), tk("g", 2, transform(), us(3, 0.5))}, IntraDuels: picks.LadderMinDuels}, "g", picks.SourceLadder},
	} {
		got := picks.Derive(c.in)
		id := ""
		if got.TakeID != nil {
			id = *got.TakeID
		}
		if id != c.take || got.Source != c.source {
			t.Errorf("%s: take=%q source=%s, want %q %s", c.name, id, got.Source, c.take, c.source)
		}
	}
}
