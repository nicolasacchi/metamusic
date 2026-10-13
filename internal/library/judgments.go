package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// tagSourceOrder is the order of TagRef.Sources.
var tagSourceOrder = []string{"human", "agent", "inherited"}

// judgmentExtras fills the M2a fields of a track detail: both rating rows,
// effective tags, comment counts, playlists and listening stats (C38).
func (s *Service) judgmentExtras(ctx context.Context, d *TrackDetail) error {
	q := s.st.R
	rt, err := s.Ratings(ctx, d.ID)
	if err != nil {
		return err
	}
	d.Rating = rt
	tags, err := s.Tags(ctx, d.ID)
	if err != nil {
		return err
	}
	d.Tags = tags.Tags
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(state = 'open'), 0) FROM comments WHERE track_id = ? AND hidden_at IS NULL`, d.ID).
		Scan(&d.Comments, &d.OpenComments); err != nil {
		return fmt.Errorf("library: comment counts: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.name FROM playlists p WHERE p.hidden_at IS NULL
		AND EXISTS (SELECT 1 FROM json_each(p.items_json) j WHERE j.value = ?) ORDER BY p.name, p.id`, d.ID)
	if err != nil {
		return fmt.Errorf("library: track playlists: %w", err)
	}
	for rows.Next() {
		var pr PlaylistRef
		if err := rows.Scan(&pr.ID, &pr.Name); err != nil {
			_ = rows.Close()
			return err
		}
		d.Playlists = append(d.Playlists, pr)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var starts, completes int
	var last sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = 'start'), 0), COALESCE(SUM(kind = 'complete'), 0), MAX(CASE WHEN kind = 'start' THEN client_at END)
		FROM listen_events WHERE track_id = ? AND principal_kind IN ('human', 'device')`, d.ID).Scan(&starts, &completes, &last); err != nil {
		return fmt.Errorf("library: listening stats: %w", err)
	}
	d.Listening = ListeningStats{Plays: starts, LastPlayedAt: ptrStr(last)}
	if starts > 0 {
		r := min(float64(completes)/float64(starts), 1)
		d.Listening.CompletionRate = &r
	}
	return nil
}

// Ratings returns both rating rows of a track as stored (principal references
// are unmasked: the API layer renders them per viewer, D-M2a-19).
func (s *Service) Ratings(ctx context.Context, trackID string) (TrackRating, error) {
	out := TrackRating{TrackID: trackID}
	rows, err := s.st.R.QueryContext(ctx, `SELECT source, stars, verdict, principal_kind, principal_id, principal_name, updated_at FROM ratings WHERE track_id = ?`, trackID)
	if err != nil {
		return out, fmt.Errorf("library: ratings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var stars sql.NullInt64
		var verdict sql.NullString
		row := RatingRow{}
		var kind string
		if err := rows.Scan(&source, &stars, &verdict, &kind, &row.By.ID, &row.By.Name, &row.UpdatedAt); err != nil {
			return out, err
		}
		row.By.Kind = kind
		if stars.Valid {
			v := int(stars.Int64)
			row.Stars = &v
		}
		row.Verdict = ptrStr(verdict)
		if source == "human" {
			out.Human = &row
		} else {
			out.Critic = &row
		}
	}
	return out, rows.Err()
}

// Tags returns a track's effective tags: every active row of any source,
// deduplicated by name, with the sources that carry it.
func (s *Service) Tags(ctx context.Context, trackID string) (TrackTags, error) {
	out := TrackTags{TrackID: trackID, Tags: []TagRef{}}
	rows, err := s.st.R.QueryContext(ctx, `SELECT g.name, tt.source FROM track_tags tt JOIN tags g ON g.id = tt.tag_id
		WHERE tt.track_id = ? AND tt.removed_at IS NULL ORDER BY g.name, tt.source`, trackID)
	if err != nil {
		return out, fmt.Errorf("library: tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, source string
		if err := rows.Scan(&name, &source); err != nil {
			return out, err
		}
		if n := len(out.Tags); n > 0 && out.Tags[n-1].Name == name {
			out.Tags[n-1].Sources = append(out.Tags[n-1].Sources, source)
			continue
		}
		out.Tags = append(out.Tags, TagRef{Name: name, Sources: []string{source}})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for i := range out.Tags {
		slices.SortFunc(out.Tags[i].Sources, func(a, b string) int { return slices.Index(tagSourceOrder, a) - slices.Index(tagSourceOrder, b) })
	}
	return out, nil
}

// TrackExists reports whether a track id exists.
func (s *Service) TrackExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.st.R.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
