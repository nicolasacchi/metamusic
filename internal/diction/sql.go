package diction

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
)

// The Tier B rows this package reads are written by internal/ears (analyses, tier B). The current diction row of a track is its
// newest one of the current definition (a version starting with "mm-diction/1 ", D-M2b-12); the language is the one the
// recogniser reported in the track's newest tier_b_raw row.
const dictionVersionLike = "mm-diction/1 %"

// storedDiction is the part of a tier_b_diction row the lab reads.
type storedDiction struct {
	Sections []struct {
		Lines []tierb.LineResult `json:"lines"`
	} `json:"sections"`
}

func (s storedDiction) lines() []tierb.LineResult {
	out := []tierb.LineResult{}
	for _, sec := range s.Sections {
		out = append(out, sec.Lines...)
	}
	return out
}

// normLang keeps a recogniser language when it is an ISO 639 code of two or three lower-case letters, else "und".
func normLang(l string) string {
	if n := len(l); n < 2 || n > 3 {
		return "und"
	}
	for _, r := range l {
		if r < 'a' || r > 'z' {
			return "und"
		}
	}
	return l
}

// trackInfo is what one track contributes to the view: its lyrics and, when it has one, the current diction row.
type trackInfo struct {
	lyrics  string
	lang    string
	results []tierb.LineResult // nil without a tier_b_diction row
}

var errNoTrack = errors.New("diction: unknown track")

func (s *Service) trackInfo(ctx context.Context, trackID string) (trackInfo, error) {
	var ti trackInfo
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, trackID).Scan(&ti.lyrics)
	if errors.Is(err, sql.ErrNoRows) {
		return ti, errNoTrack
	}
	if err != nil {
		return ti, fmt.Errorf("diction: track: %w", err)
	}
	var metrics string
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_diction' AND version LIKE ?
		ORDER BY created_at DESC, version DESC LIMIT 1`, trackID, dictionVersionLike).Scan(&metrics); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return ti, fmt.Errorf("diction: tier B row: %w", err)
	default:
		var sd storedDiction
		if err := json.Unmarshal([]byte(metrics), &sd); err != nil {
			return ti, fmt.Errorf("diction: tier B row of %s does not parse: %w", trackID, err)
		}
		ti.results = sd.lines()
	}
	var lang sql.NullString
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT json_extract(metrics_json, '$.language') FROM analyses WHERE track_id = ? AND analyzer = 'tier_b_raw'
		ORDER BY created_at DESC, version DESC LIMIT 1`, trackID).Scan(&lang); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ti, fmt.Errorf("diction: tier B language: %w", err)
	}
	ti.lang = normLang(lang.String)
	return ti, nil
}

// trackMarks reads the marks of one track, both sources.
func (s *Service) trackMarks(ctx context.Context, trackID string) ([]MarkRow, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT section_idx, line_idx, source, mark, updated_at FROM diction_marks WHERE track_id = ?
		ORDER BY section_idx, line_idx, source`, trackID)
	if err != nil {
		return nil, fmt.Errorf("diction: marks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []MarkRow{}
	for rows.Next() {
		var m MarkRow
		if err := rows.Scan(&m.SectionIdx, &m.LineIdx, &m.Source, &m.Mark, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("diction: marks: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func markMap(rows []MarkRow) map[LineKey]DictionMark {
	out := map[LineKey]DictionMark{}
	for _, m := range rows {
		k := LineKey{m.SectionIdx, m.LineIdx}
		dm := out[k]
		mk := m.Mark
		if m.Source == SourceHuman {
			dm.Human = &mk
		} else {
			dm.Critic = &mk
		}
		out[k] = dm
	}
	return out
}

// cacheKey is the cheap fingerprint of everything the lexicon depends on: the count and newest time of the diction rows, the
// marks and the overrides.
func (s *Service) cacheKey(ctx context.Context) (string, error) {
	var key string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT
		(SELECT count(*) || '/' || coalesce(max(created_at), '') FROM analyses WHERE analyzer = 'tier_b_diction') || ' ' ||
		(SELECT count(*) || '/' || coalesce(max(updated_at), '') FROM diction_marks) || ' ' ||
		(SELECT count(*) || '/' || coalesce(max(updated_at), '') FROM lexicon_overrides) || ' ' ||
		(SELECT count(*) FROM tracks WHERE status = 'discarded')`).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("diction: cache key: %w", err)
	}
	return key, nil
}

// allDiction reads the current diction row of every kept track, ordered by track id. Discarded takes are left out: the lexicon is one
// aggregate shared by every viewer, and a discarded take does not exist for an Editor or a Listener (C308.2).
func (s *Service) allDiction(ctx context.Context) ([]TrackDiction, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT a.track_id, a.created_at, a.metrics_json,
		(SELECT json_extract(r.metrics_json, '$.language') FROM analyses r WHERE r.track_id = a.track_id AND r.analyzer = 'tier_b_raw'
		   ORDER BY r.created_at DESC, r.version DESC LIMIT 1)
		FROM analyses a WHERE a.analyzer = 'tier_b_diction' AND a.version LIKE ?
		  AND NOT EXISTS (SELECT 1 FROM tracks t WHERE t.id = a.track_id AND t.status = 'discarded')
		ORDER BY a.track_id, a.created_at DESC, a.version DESC`, dictionVersionLike)
	if err != nil {
		return nil, fmt.Errorf("diction: rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TrackDiction
	for rows.Next() {
		var td TrackDiction
		var metrics string
		var lang sql.NullString
		if err := rows.Scan(&td.TrackID, &td.CreatedAt, &metrics, &lang); err != nil {
			return nil, fmt.Errorf("diction: rows: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].TrackID == td.TrackID {
			continue // an older row of the same track
		}
		var sd storedDiction
		if err := json.Unmarshal([]byte(metrics), &sd); err != nil {
			continue // a row that does not parse is skipped, not fatal for the whole lexicon
		}
		td.Lang, td.Lines = normLang(lang.String), sd.lines()
		out = append(out, td)
	}
	return out, rows.Err()
}

// humanMarks reads every current human mark as track -> line -> mark.
func (s *Service) humanMarks(ctx context.Context) (map[string]map[LineKey]string, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT track_id, section_idx, line_idx, mark FROM diction_marks WHERE source = 'human'`)
	if err != nil {
		return nil, fmt.Errorf("diction: human marks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[LineKey]string{}
	for rows.Next() {
		var tr, mk string
		var k LineKey
		if err := rows.Scan(&tr, &k.Section, &k.Line, &mk); err != nil {
			return nil, fmt.Errorf("diction: human marks: %w", err)
		}
		if out[tr] == nil {
			out[tr] = map[LineKey]string{}
		}
		out[tr][k] = mk
	}
	return out, rows.Err()
}

func (s *Service) overrides(ctx context.Context) (map[[2]string]Override, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT lang, word_fold, status, note FROM lexicon_overrides`)
	if err != nil {
		return nil, fmt.Errorf("diction: overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[[2]string]Override{}
	for rows.Next() {
		var lang, word string
		var o Override
		var note sql.NullString
		if err := rows.Scan(&lang, &word, &o.Status, &note); err != nil {
			return nil, fmt.Errorf("diction: overrides: %w", err)
		}
		if note.Valid {
			o.Note = &note.String
		}
		out[[2]string{lang, word}] = o
	}
	return out, rows.Err()
}
