package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// NewSection is one measured section to insert (C335.2). Raw is the lyric header as written ("[Verse 1]"); Lines is the
// section's lyric lines, the shape of lines_json on every other source.
type NewSection struct {
	Idx        int
	Kind       string
	Label      string
	Raw        string
	Lines      []string
	StartS     float64
	EndS       float64
	Confidence float64
}

// AddMeasuredSections inserts sections rows of source 'measured' for trackID inside tx, and only when the track has no
// measured row yet (rows are immutable, so the first measurement wins). It reports how many rows it inserted: 0 when the
// track already had a measured row or secs is empty. A section with a bad time span or confidence fails the whole call.
func (s *Service) AddMeasuredSections(ctx context.Context, tx *sql.Tx, trackID string, secs []NewSection) (int, error) {
	if len(secs) == 0 {
		return 0, nil
	}
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sections WHERE track_id = ? AND source = 'measured')`, trackID).Scan(&have); err != nil {
		return 0, fmt.Errorf("library: measured sections: %w", err)
	}
	if have == 1 {
		return 0, nil
	}
	for _, sec := range secs {
		lines := sec.Lines
		if lines == nil {
			lines = []string{}
		}
		lj, err := json.Marshal(lines)
		if err != nil {
			return 0, fmt.Errorf("library: measured sections: %w", err)
		}
		if err := execTx(ctx, tx, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json)
			VALUES (?,'measured',?,?,?,?,?,?,?,?)`, trackID, sec.Idx, sec.Kind, sec.Label, sec.Raw, sec.StartS, sec.EndS, sec.Confidence, string(lj)); err != nil {
			return 0, fmt.Errorf("library: insert measured section %d: %w", sec.Idx, err)
		}
	}
	return len(secs), nil
}
