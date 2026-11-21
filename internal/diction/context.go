package diction

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// ContextSummary is what /context shows of a take's diction beside the overall CER (C345.5): how many lines fall in each class
// (a human mark replaces the recogniser's class) and the words of the take that the lexicon calls risky.
type ContextSummary struct {
	HasLines              bool // the take has a listening check
	Clear, Mixed, Unclear int
	RiskyWords            []string
}

// ContextSummary reads the lines and risky words of one take. A take without a Tier B diction row has no lines (HasLines false)
// and no risky words; an unknown track is ErrNotFound.
func (s *Service) ContextSummary(ctx context.Context, trackID string) (ContextSummary, error) {
	if err := s.wired(); err != nil {
		return ContextSummary{}, err
	}
	ti, err := s.trackInfo(ctx, trackID)
	if errors.Is(err, errNoTrack) {
		return ContextSummary{}, ErrNotFound
	}
	if err != nil || ti.results == nil {
		return ContextSummary{}, err
	}
	rows, err := s.trackMarks(ctx, trackID)
	if err != nil {
		return ContextSummary{}, err
	}
	ref := tierb.RefWords(ti.lyrics)
	_, lines := BuildLines(LinesInput{Sections: yue2.LyricsSections(ti.lyrics), Ref: ref, Results: ti.results, Marks: markMap(rows), Thresholds: DefaultThresholds})
	out := ContextSummary{HasLines: true}
	for _, ln := range lines {
		switch ln.Class {
		case ClassClear:
			out.Clear++
		case ClassMixed:
			out.Mixed++
		case ClassUnclear:
			out.Unclear++
		}
	}
	sn, err := s.lexicon(ctx)
	if err != nil {
		return ContextSummary{}, err
	}
	for _, e := range riskyOf(sn, ti.lang, ref) {
		out.RiskyWords = append(out.RiskyWords, e.Word)
	}
	return out, nil
}
