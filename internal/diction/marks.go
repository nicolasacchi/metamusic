package diction

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// SetMarks stores marks on the lines of a take (C345.2). The source is human for a person (the Owner or the Owner's device) and
// critic for an agent; critic marks never affect the lexicon or the calibration (D-EARS-14). Unknown lines are 422.
func (s *Service) SetMarks(ctx context.Context, p auth.Principal, trackID string, marks []MarkIn) (TrackMarks, error) {
	if err := s.wired(); err != nil {
		return TrackMarks{}, err
	}
	var source string
	switch {
	case p.IsPerson():
		source = SourceHuman
	case p.IsAgent():
		source = SourceCritic
	default:
		return TrackMarks{}, problem.E(http.StatusForbidden, "FORBIDDEN", "this principal cannot mark lines", nil)
	}
	ti, err := s.trackInfo(ctx, trackID)
	if errors.Is(err, errNoTrack) {
		return TrackMarks{}, ErrNotFound
	}
	if err != nil {
		return TrackMarks{}, err
	}
	have := map[LineKey]bool{}
	for _, sec := range yue2.LyricsSections(ti.lyrics) {
		for li := range sec.Lines {
			have[LineKey{sec.Idx, li}] = true
		}
	}
	for i, m := range marks {
		if !have[LineKey{m.SectionIdx, m.LineIdx}] {
			return TrackMarks{}, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", fmt.Sprintf("marks[%d]: the take has no such lyric line", i),
				map[string]any{"field": fmt.Sprintf("marks[%d]", i)})
		}
		if m.Mark != MarkClear && m.Mark != MarkUnclear && m.Mark != MarkNone {
			return TrackMarks{}, invalid(fmt.Sprintf("marks[%d].mark", i), "mark must be clear, unclear or none")
		}
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return TrackMarks{}, fmt.Errorf("diction: marks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range marks {
		var prev string
		_ = tx.QueryRowContext(ctx, `SELECT updated_at FROM diction_marks WHERE track_id = ? AND section_idx = ? AND line_idx = ? AND source = ?`,
			trackID, m.SectionIdx, m.LineIdx, source).Scan(&prev)
		if _, err := tx.ExecContext(ctx, `INSERT INTO diction_marks(track_id, section_idx, line_idx, source, mark, marked_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(track_id, section_idx, line_idx, source) DO UPDATE SET mark = excluded.mark, marked_by = excluded.marked_by, updated_at = excluded.updated_at`,
			trackID, m.SectionIdx, m.LineIdx, source, m.Mark, p.Key(), s.nextStamp(prev)); err != nil {
			return TrackMarks{}, fmt.Errorf("diction: mark: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return TrackMarks{}, fmt.Errorf("diction: marks commit: %w", err)
	}
	s.invalidate()
	rows, err := s.trackMarks(ctx, trackID)
	if err != nil {
		return TrackMarks{}, err
	}
	return TrackMarks{TrackID: trackID, Marks: rows}, nil
}

// Marks reads the marks of a take.
func (s *Service) Marks(ctx context.Context, trackID string) (TrackMarks, error) {
	if err := s.wired(); err != nil {
		return TrackMarks{}, err
	}
	if _, err := s.trackInfo(ctx, trackID); errors.Is(err, errNoTrack) {
		return TrackMarks{}, ErrNotFound
	} else if err != nil {
		return TrackMarks{}, err
	}
	rows, err := s.trackMarks(ctx, trackID)
	return TrackMarks{TrackID: trackID, Marks: rows}, err
}
