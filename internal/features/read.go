package features

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// scanner is the part of *sql.Row and *sql.Rows Scan needs.
type scanner interface{ Scan(dest ...any) error }

// rowDest scans the twelve shared columns into a Row.
type rowDest struct {
	bpm, req, meas, energy, keyNum sql.NullInt64
	bpmBasis, energyBasis          sql.NullString
	keyLetter, keyBasis            sql.NullString
	oct                            int
	dur, exp                       sql.NullFloat64
}

func (d *rowDest) ptrs() []any {
	return []any{&d.bpm, &d.bpmBasis, &d.req, &d.meas, &d.oct, &d.energy, &d.energyBasis, &d.keyNum, &d.keyLetter, &d.keyBasis, &d.dur, &d.exp}
}

func optInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	return ptr(int(n.Int64))
}

func optFloat(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	return ptr(n.Float64)
}

func (d *rowDest) row() Row {
	r := Row{BPMx100: optInt(d.bpm), BPMBasis: Basis(d.bpmBasis.String), BPMRequestedx100: optInt(d.req), BPMMeasuredx100: optInt(d.meas),
		BPMOctave: d.oct == 1, Energyx10000: optInt(d.energy), EnergyBasis: Basis(d.energyBasis.String), KeyBasis: Basis(d.keyBasis.String),
		DurationS: optFloat(d.dur), DurationExpectedS: optFloat(d.exp)}
	if d.keyNum.Valid {
		r.Key = &Camelot{Num: int(d.keyNum.Int64), Letter: d.keyLetter.String}
	}
	return r
}

const takeSelect = `SELECT track_id, ` + rowColumns + `, detail_json FROM take_features`

func scanTake(sc scanner) (string, Row, error) {
	var id string
	var d rowDest
	var detail sql.NullString
	if err := sc.Scan(append(append([]any{&id}, d.ptrs()...), &detail)...); err != nil {
		return "", Row{}, err
	}
	r := d.row()
	r.Detail = decodeDetail(detail)
	return id, r, nil
}

// Take reads one take's row; ok is false when the take has none.
func (s *Service) Take(ctx context.Context, trackID string) (Row, bool, error) {
	_, r, err := scanTake(s.st.R.QueryRowContext(ctx, takeSelect+` WHERE track_id = ?`, trackID))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, fmt.Errorf("features: take: %w", err)
	}
	return r, true, nil
}

func jsonList(ids []string) string {
	b, _ := json.Marshal(ids) // a slice of strings always marshals
	return string(b)
}

// Takes reads the rows of the given takes with one statement; takes without a row are absent from the map.
func (s *Service) Takes(ctx context.Context, trackIDs []string) (map[string]Row, error) {
	out := make(map[string]Row, len(trackIDs))
	if len(trackIDs) == 0 {
		return out, nil
	}
	rows, err := s.st.R.QueryContext(ctx, takeSelect+` WHERE track_id IN (SELECT value FROM json_each(?))`, jsonList(trackIDs))
	if err != nil {
		return nil, fmt.Errorf("features: takes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		id, r, err := scanTake(rows)
		if err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// Songs reads the rows of the given Songs with one statement; Songs without a row are absent from the map.
func (s *Service) Songs(ctx context.Context, songIDs []string) (map[string]SongRow, error) {
	out := make(map[string]SongRow, len(songIDs))
	if len(songIDs) == 0 {
		return out, nil
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT song_id, subject_kind, subject_id, subject_note, `+rowColumns+`, family, vocal, era, mood, language
		FROM song_features WHERE song_id IN (SELECT value FROM json_each(?))`, jsonList(songIDs))
	if err != nil {
		return nil, fmt.Errorf("features: songs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sr SongRow
		var d rowDest
		var note, fam, voc, era, mood, lang sql.NullString
		dest := append([]any{&sr.SongID, &sr.SubjectKind, &sr.SubjectID, &note}, d.ptrs()...)
		dest = append(dest, &fam, &voc, &era, &mood, &lang)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		sr.Row = d.row()
		if note.Valid {
			sr.SubjectNote = ptr(note.String)
		}
		for p, n := range map[**string]sql.NullString{&sr.Family: fam, &sr.Vocal: voc, &sr.Era: era, &sr.Mood: mood, &sr.Language: lang} {
			if n.Valid {
				*p = ptr(n.String)
			}
		}
		out[sr.SongID] = sr
	}
	return out, rows.Err()
}
