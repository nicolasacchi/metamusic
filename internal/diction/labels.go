package diction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// The Critic Bench interface (C346.1): the human marks that label lines, with the recogniser's CER for the same line, so a label
// critic can learn which CER a listener calls clear. The consumer declares its own interface (criticbench.LineSource) and
// *Service satisfies it; this package never imports it.

// LabelKey names one lyric line of one take. (LineKey, without a track, is the key inside a take.)
type LabelKey struct {
	TrackID       string
	Section, Line int
}

// LabelledLine is a line a human marked, with its recogniser score.
type LabelledLine struct {
	Key      LabelKey
	Lang     string
	CER      float64   // the line's ASR CER from the current tier_b_diction row
	Human    string    // "clear" | "unclear": the current human mark
	MarkedAt time.Time // when the mark was written
}

// LabelSource is what the label critic reads.
type LabelSource interface {
	// HumanLabelledLines lists the lines with a current human mark of clear or unclear that also have a CER in the take's
	// current tier_b_diction row, newest mark first; lang "" means every language and limit <= 0 means no limit. Critic marks
	// and withdrawn marks never appear.
	HumanLabelledLines(ctx context.Context, lang string, limit int) ([]LabelledLine, error)
	// Thresholds are the class thresholds in force.
	Thresholds() Thresholds
	// Version changes with any mark or tier_b_diction row.
	Version(ctx context.Context) (string, error)
}

var _ LabelSource = (*Service)(nil)

// Thresholds returns the class thresholds in force (the calibration PR changes DefaultThresholds).
func (s *Service) Thresholds() Thresholds { return DefaultThresholds }

// Version is a short fingerprint of the diction rows and every mark; any new row or any written mark changes it.
func (s *Service) Version(ctx context.Context) (string, error) {
	if err := s.wired(); err != nil {
		return "", err
	}
	var key string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT
		(SELECT count(*) || '/' || coalesce(max(created_at), '') || '/' || coalesce(max(version), '') FROM analyses WHERE analyzer = 'tier_b_diction') || ' ' ||
		(SELECT count(*) || '/' || coalesce(group_concat(track_id || ':' || section_idx || ':' || line_idx || ':' || source || ':' || mark || ':' || updated_at, ','), '')
			FROM (SELECT * FROM diction_marks ORDER BY track_id, section_idx, line_idx, source))`).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("diction: version: %w", err)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6]), nil
}

// HumanLabelledLines implements LabelSource.
func (s *Service) HumanLabelledLines(ctx context.Context, lang string, limit int) ([]LabelledLine, error) {
	if err := s.wired(); err != nil {
		return nil, err
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT track_id, section_idx, line_idx, mark, updated_at FROM diction_marks
		WHERE source = 'human' AND mark IN ('clear', 'unclear') ORDER BY updated_at DESC, track_id, section_idx, line_idx`)
	if err != nil {
		return nil, fmt.Errorf("diction: labels: %w", err)
	}
	type mk struct {
		key      LabelKey
		mark, at string
	}
	var marks []mk
	for rows.Next() {
		var m mk
		if err := rows.Scan(&m.key.TrackID, &m.key.Section, &m.key.Line, &m.mark, &m.at); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("diction: labels: %w", err)
		}
		marks = append(marks, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	tracks, err := s.allDiction(ctx)
	if err != nil {
		return nil, err
	}
	type cerKey struct {
		track string
		line  LineKey
	}
	cers := map[cerKey]float64{}
	langs := map[string]string{}
	for _, td := range tracks {
		langs[td.TrackID] = td.Lang
		for _, ln := range td.Lines {
			cers[cerKey{td.TrackID, LineKey{ln.Section, ln.Line}}] = ln.CER
		}
	}
	out := []LabelledLine{}
	for _, m := range marks {
		cer, ok := cers[cerKey{m.key.TrackID, LineKey{m.key.Section, m.key.Line}}]
		if !ok || (lang != "" && langs[m.key.TrackID] != lang) {
			continue
		}
		at, err := store.ParseTime(m.at)
		if err != nil {
			return nil, fmt.Errorf("diction: labels: %w", err)
		}
		out = append(out, LabelledLine{Key: m.key, Lang: langs[m.key.TrackID], CER: cer, Human: m.mark, MarkedAt: at})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}
