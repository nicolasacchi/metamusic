package muses

import (
	"context"
	"fmt"
)

// MuseMeta is the neutral face of a Muse for the album catalog: its frozen ordinal and whether it is hidden. No text. The
// composition root adapts it to albums.MuseMeta (muses cannot import the L2 packages, C118).
type MuseMeta struct {
	Ordinal int
	Hidden  bool
}

// Ordinals returns every Muse's frozen ordinal (muses.ordinal, D-L2-37) and hidden flag in ONE statement, hidden Muses
// included: they keep their number.
func (s *Service) Ordinals(ctx context.Context) (map[string]MuseMeta, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id, COALESCE(ordinal, 0), hidden_at IS NOT NULL FROM muses`)
	if err != nil {
		return nil, fmt.Errorf("muses: ordinals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]MuseMeta{}
	for rows.Next() {
		var id string
		var m MuseMeta
		if err := rows.Scan(&id, &m.Ordinal, &m.Hidden); err != nil {
			return nil, fmt.Errorf("muses: ordinals: %w", err)
		}
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("muses: ordinals: %w", err)
	}
	return out, nil
}

// Version is a cheap change marker of the Muse table: COUNT(*), MAX(updated_at) and SUM(revision), one statement (the sum
// catches two edits inside one clock tick). Create, update and hide
// each move it.
func (s *Service) Version(ctx context.Context) (string, error) {
	var v string
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) || '|' || COALESCE(MAX(updated_at), '') || '|' || COALESCE(SUM(revision), 0) FROM muses`).Scan(&v); err != nil {
		return "", fmt.Errorf("muses: version: %w", err)
	}
	return v, nil
}

// Labels returns the real name of each Muse: its title, or "title · artist" for a reference Muse that has an artist. Ids with
// no Muse are absent. It is the text path of the subsonic_muse_names opt-in and is wired to the catalog only for it (D-L2-16).
func (s *Service) Labels(ctx context.Context, ids []string) (map[string]string, error) {
	refs, err := s.Refs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("muses: labels: %w", err)
	}
	out := make(map[string]string, len(refs))
	for id, r := range refs {
		out[id] = r.Title
		if Kind(r.Kind) == KindReference && r.Artist != nil {
			out[id] = r.Title + " · " + *r.Artist
		}
	}
	return out, nil
}
