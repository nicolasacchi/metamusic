package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// TimedSection is one entry of a timed section timeline.
type TimedSection struct {
	Idx          int
	Kind, Label  string
	StartS, EndS float64
	Lines        []string // lines_json of the selected source's row, else nil
}

// Sections is the perception view of a track's structure (C45).
type Sections struct {
	Timed             bool
	Source            string // winning source when timed; "" when untimed
	Confidence        *float64
	Timeline          []TimedSection // empty when untimed
	Structure         []string       // lyrics labels in order, always present
	ExpectedDurationS *float64       // score bars x seconds_per_bar when timed, else nil
}

// Sections applies the same selection rule as Track (selectSections), so perception and /context
// cannot diverge. ErrNotFound for an unknown track.
func (s *Service) Sections(ctx context.Context, trackID string) (Sections, error) {
	out := Sections{Structure: []string{}}
	var one int
	if err := s.st.R.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, trackID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	} else if err != nil {
		return out, fmt.Errorf("library: sections: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json FROM sections WHERE track_id = ? ORDER BY idx`, trackID)
	if err != nil {
		return out, fmt.Errorf("library: sections: %w", err)
	}
	bySource := map[string][]SectionOut{}
	for rows.Next() {
		var so SectionOut
		var st, en, conf sql.NullFloat64
		var lj sql.NullString
		if err := rows.Scan(&so.Source, &so.Idx, &so.Kind, &so.Label, &so.Raw, &st, &en, &conf, &lj); err != nil {
			_ = rows.Close()
			return out, fmt.Errorf("library: sections: %w", err)
		}
		if st.Valid && en.Valid {
			so.StartS, so.EndS = &st.Float64, &en.Float64
		}
		if conf.Valid {
			so.Confidence = &conf.Float64
		}
		if lj.Valid {
			_ = json.Unmarshal([]byte(lj.String), &so.Lines)
		}
		bySource[so.Source] = append(bySource[so.Source], so)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	for _, so := range bySource["lyrics"] {
		out.Structure = append(out.Structure, so.Label)
	}
	best, sel, timed := selectSections(bySource)
	if best == "" || !timed {
		return out, nil
	}
	out.Timed, out.Source = true, best
	out.Confidence = sel[0].Confidence
	for _, so := range sel {
		out.Timeline = append(out.Timeline, TimedSection{Idx: so.Idx, Kind: so.Kind, Label: so.Label, StartS: *so.StartS, EndS: *so.EndS, Lines: so.Lines})
	}
	var bars int
	var spb sql.NullFloat64
	switch err := s.st.R.QueryRowContext(ctx, `SELECT bars, seconds_per_bar FROM scores WHERE track_id = ?`, trackID).Scan(&bars, &spb); {
	case err == nil:
		if spb.Valid {
			v := float64(bars) * spb.Float64
			out.ExpectedDurationS = &v
		}
	case !errors.Is(err, sql.ErrNoRows):
		return out, fmt.Errorf("library: sections: %w", err)
	}
	return out, nil
}
