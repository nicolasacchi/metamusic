package playlists

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// CreateTx is Create inside tx (M6N C381.4: the Morning Crate makes its playlist in the reveal transaction). It returns the new
// playlist's id; read the playlist after the commit.
func (s *Service) CreateTx(ctx context.Context, tx *sql.Tx, p auth.Principal, in NewPlaylist) (string, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return "", err
	}
	desc, err := cleanDesc(in.Description)
	if err != nil {
		return "", err
	}
	if len(in.TrackIDs) > MaxItems {
		return "", invalid("track_ids", fmt.Sprintf("at most %d tracks", MaxItems))
	}
	if err := s.checkTracks(ctx, tx, in.TrackIDs); err != nil {
		return "", err
	}
	items, _ := json.Marshal(nonNil(in.TrackIDs))
	id, now := s.newID(), s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO playlists(id, name, description, items_json, items_count, revision, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,1,?,?,?,?)`, id, name, desc, string(items), len(in.TrackIDs), p.Key(), p.Key(), now, now); err != nil {
		return "", fmt.Errorf("playlists: create: %w", err)
	}
	return id, nil
}
