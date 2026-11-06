package duels

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// sectionKey is a validated SectionSelector: a label, or a kind with an ordinal.
type sectionKey struct {
	label   string // trimmed; "" when kind/ordinal is used
	kind    string
	ordinal int
}

// parseSection validates the selector of create_duel / suggest_duel_pairs: exactly one of label, or kind together with ordinal.
func parseSection(sel *SectionSelector) (sectionKey, error) {
	var k sectionKey
	hasLabel := sel.Label != nil
	hasKind := sel.Kind != nil || sel.Ordinal != nil
	switch {
	case hasLabel && hasKind:
		return k, invalid("section", "section takes either label, or kind with ordinal, not both")
	case hasLabel:
		if k.label = strings.TrimSpace(*sel.Label); k.label == "" {
			return k, invalid("section.label", "section.label must not be blank")
		}
	case sel.Kind != nil && sel.Ordinal != nil:
		if k.kind = strings.TrimSpace(*sel.Kind); k.kind == "" || *sel.Ordinal < 1 {
			return k, invalid("section", "section.kind must not be blank and section.ordinal must be at least 1")
		}
		k.ordinal = *sel.Ordinal
	case hasKind:
		return k, invalid("section", "section.kind and section.ordinal go together")
	default:
		return k, invalid("section", "section needs a label, or a kind with an ordinal")
	}
	return k, nil
}

// canonical is the label stored on the duel and shown in its view: the same for both slots, so it cannot tell them apart.
func (k sectionKey) canonical() string {
	if k.label != "" {
		return k.label
	}
	return fmt.Sprintf("%s %d", k.kind, k.ordinal)
}

// find returns the section of t the key names. ok is false when there is none or, for a label, when it names several
// (the key is then ambiguous on this track and nothing is guessed).
func (k sectionKey) find(t anchor.Timing) (sec anchor.Section, ok bool) {
	n := 0
	for _, s := range t.Sections {
		if (k.label != "" && strings.EqualFold(s.Label, k.label)) || (k.label == "" && s.Ordinal == k.ordinal && strings.EqualFold(s.Kind, k.kind)) {
			sec, n = s, n+1
		}
	}
	return sec, n == 1
}

// timedSection is a section of one track and the source its timing came from.
type timedSection struct {
	sec    anchor.Section
	source string
}

// sectionOf finds the section of one track: SECTION_UNTIMED (422, {track_id}) when the track has no timed sections
// (lyrics-only imports, references, multi-part imports, anything without start/end), SECTION_NOT_FOUND when it has
// timed sections but not this one. Timing's own rule picks the source (manual > measured > plan).
func (s *Service) sectionOf(ctx context.Context, trackID string, k sectionKey) (timedSection, error) {
	t, err := library.Timing(ctx, s.o.Store.R, trackID)
	if errors.Is(err, library.ErrNotFound) {
		return timedSection{}, notFound("track", trackID)
	}
	if err != nil {
		return timedSection{}, err
	}
	if len(t.Sections) == 0 {
		return timedSection{}, problem.E(422, "SECTION_UNTIMED", "the track has no timed sections", map[string]any{"track_id": trackID})
	}
	sec, ok := k.find(t)
	if !ok {
		return timedSection{}, problem.E(422, "SECTION_NOT_FOUND", "the track has no such section", map[string]any{"track_id": trackID, "section": k.canonical()})
	}
	return timedSection{sec: sec, source: t.Source}, nil
}

// sectionSource is the recorded source of a pair: the least informative of the two (plan < measured < manual), so the
// stored value never claims more than the weaker side had.
func sectionSource(a, b string) string {
	rank := map[string]int{"plan": 0, "measured": 1, "manual": 2}
	if rank[b] < rank[a] {
		return b
	}
	return a
}
