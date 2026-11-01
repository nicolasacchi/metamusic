package features

import (
	"context"
	"database/sql"
	"errors"
)

// Lane FC2 replaces these bodies and deletes this file. The signatures are frozen by L3-F2.

// ComputeTx is the Reconciler's phase B step and the entry of the sounds recompute hook (C135.5); digest-guarded and idempotent.
func (s *Service) ComputeTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs, songIDs []string) (Changes, error) {
	return Changes{}, errors.ErrUnsupported
}

// SweepStaleTx rewrites rows whose definition, digest or subject changed, in batches of BatchSongs.
func (s *Service) SweepStaleTx(ctx context.Context, tx *sql.Tx, env Env, limit int) (int, error) {
	return 0, errors.ErrUnsupported
}

// Verify runs the L3 invariants F1 to F6 (C135.6).
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error) { return nil, errors.ErrUnsupported }

// Take reads one take's row.
func (s *Service) Take(ctx context.Context, trackID string) (Row, bool, error) {
	return Row{}, false, errors.ErrUnsupported
}

// Songs reads the rows of the given Songs with one statement.
func (s *Service) Songs(ctx context.Context, songIDs []string) (map[string]SongRow, error) {
	return nil, errors.ErrUnsupported
}

// Takes reads the rows of the given takes with one statement.
func (s *Service) Takes(ctx context.Context, trackIDs []string) (map[string]Row, error) {
	return nil, errors.ErrUnsupported
}
