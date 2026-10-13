package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ArtifactRef is one kept take artifact.
type ArtifactRef struct {
	Name, Path, SHA256 string
	Bytes              int64
}

// Artifact returns the artifact row of a track by name; ErrNotFound when absent.
func (s *Service) Artifact(ctx context.Context, trackID, name string) (ArtifactRef, error) {
	a := ArtifactRef{Name: name}
	err := s.st.R.QueryRowContext(ctx, `SELECT path, sha256, bytes FROM artifacts WHERE track_id = ? AND name = ?`, trackID, name).
		Scan(&a.Path, &a.SHA256, &a.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactRef{}, ErrNotFound
	}
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("library: artifact: %w", err)
	}
	return a, nil
}
