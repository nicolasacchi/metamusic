package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
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
	ownerEv, ownerArgs := s.st.OwnerEventsSQL("") // the Owner side's plays only (C306.7)
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = 'start'), 0), COALESCE(SUM(kind = 'complete'), 0), MAX(CASE WHEN kind = 'start' THEN client_at END)
		FROM listen_events WHERE track_id = ? AND `+ownerEv, append([]any{d.ID}, ownerArgs...)...).Scan(&starts, &completes, &last); err != nil {
		return fmt.Errorf("library: listening stats: %w", err)
	}
	d.Listening = ListeningStats{Plays: starts, LastPlayedAt: ptrStr(last)}
	if starts > 0 {
		r := min(float64(completes)/float64(starts), 1)
		d.Listening.CompletionRate = &r
	}
	return nil
}

// Ratings returns every rating row of a track as stored, unshaped: Human and Mine hold the Owner's row, Critic the agent's
// and Raters every human rater's row. Principal references are unmasked: the API layer renders them per viewer
// (D-M2a-19) and calls ShapeRating first, so no other user's row leaves in a response unshaped (C306.4).
func (s *Service) Ratings(ctx context.Context, trackID string) (TrackRating, error) {
	out := TrackRating{TrackID: trackID}
	rows, err := s.st.R.QueryContext(ctx, `SELECT source, rater, stars, verdict, principal_kind, principal_id, principal_name, updated_at FROM ratings WHERE track_id = ? ORDER BY rater`, trackID)
	if err != nil {
		return out, fmt.Errorf("library: ratings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source, rater string
		var stars sql.NullInt64
		var verdict sql.NullString
		row := RatingRow{}
		var kind string
		if err := rows.Scan(&source, &rater, &stars, &verdict, &kind, &row.By.ID, &row.By.Name, &row.UpdatedAt); err != nil {
			return out, err
		}
		row.By.Kind = kind
		if stars.Valid {
			v := int(stars.Int64)
			row.Stars = &v
		}
		row.Verdict = ptrStr(verdict)
		switch {
		case source != "human":
			out.Critic = &row
		default:
			out.Raters = append(out.Raters, RaterRow{Rater: rater, Row: row})
			if rater == ownerRater {
				own := row
				out.Human, out.Mine = &row, &own
			}
		}
	}
	return out, rows.Err()
}

// ownerRater is the rater key of the Owner's ratings (C301.7, migration 0017).
const ownerRater = "owner"

// ShapeRating renders a track's ratings for viewer (C306.4, D-U1-9, D-U1-16) and drops the raw rows:
//   - Mine is the viewer's own row (the critic's for an agent); Human is a copy of it, except for an agent, which keeps
//     seeing the Owner's row there as before.
//   - Others, every other user's row, only for the Owner side (the Owner and the Owner's devices).
//   - Average, over every user's stars, for the Owner side and Editors; never for a Listener or an agent.
//   - Critic is hidden from a Listener.
//
// It is pure and idempotent on an already shaped value.
func ShapeRating(viewer auth.Principal, tr TrackRating) TrackRating {
	if tr.Raters == nil && (tr.Mine != nil || tr.Human != nil || tr.Others != nil || tr.Average != nil) {
		return tr // already shaped
	}
	byKey := map[string]*RatingRow{}
	for i := range tr.Raters {
		byKey[tr.Raters[i].Rater] = &tr.Raters[i].Row
	}
	out := TrackRating{TrackID: tr.TrackID, Critic: tr.Critic}
	cp := func(r *RatingRow) *RatingRow {
		if r == nil {
			return nil
		}
		c := *r
		return &c
	}
	switch {
	case viewer.IsAgent():
		out.Mine, out.Human = cp(tr.Critic), cp(byKey[ownerRater])
		return out
	case viewer.Role == auth.RoleListener:
		out.Critic = nil
		out.Mine = cp(byKey[auth.RaterKey(viewer)])
		out.Human = cp(out.Mine)
		return out
	}
	out.Mine = cp(byKey[auth.RaterKey(viewer)])
	out.Human = cp(out.Mine)
	var n, sum int
	for _, r := range tr.Raters {
		if r.Row.Stars != nil {
			n++
			sum += *r.Row.Stars
		}
	}
	if n > 0 {
		out.Average = &RatingAverage{Mean: math.Round(float64(sum)/float64(n)*100) / 100, N: n}
	}
	if viewer.IsOwnerSide() {
		for _, r := range tr.Raters {
			if r.Rater == ownerRater || (r.Row.Stars == nil && r.Row.Verdict == nil) {
				continue
			}
			out.Others = append(out.Others, OtherRating{User: r.Rater, Stars: r.Row.Stars, Verdict: r.Row.Verdict, UpdatedAt: r.Row.UpdatedAt})
		}
	}
	return out
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

// viewerRater is the rater whose row the caller sees as "the stars" in lists, sort, filter and facets (C307.2): the
// caller's own, or the Owner's for a delegate (agent, system) and for a bare context.
func viewerRater(ctx context.Context) string { return auth.DisplayRater(auth.FromContext(ctx)) }
