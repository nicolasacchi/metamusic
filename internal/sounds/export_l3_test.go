package sounds

import (
	"context"
	"database/sql"
)

// LegacySongDigest is the Song digest of L1 and L2 (before the compute definition entered it), so a test can plant
// a row computed "under mm-sound-compute/1".
func LegacySongDigest(ctx context.Context, q *sql.Tx, version int, songID string) (string, error) {
	sub, err := loadSubject(ctx, q, version, songID)
	if err != nil {
		return "", err
	}
	return sum256(jsonString(struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		Digest  string `json:"digest"`
		Version int    `json:"taxonomy_version"`
	}{sub.Kind, sub.ID, sub.Digest, version})), nil
}

// CurrentSongDigest is songDigest for a Song, for the tests that compare it with the legacy form.
func CurrentSongDigest(ctx context.Context, q *sql.Tx, version int, songID string) (string, error) {
	sub, err := loadSubject(ctx, q, version, songID)
	if err != nil {
		return "", err
	}
	return songDigest(version, sub), nil
}

// TakeDigestOf is the take digest of a stored take.
func TakeDigestOf(ctx context.Context, q *sql.Tx, version int, trackID string) (string, error) {
	in, _, err := loadTake(ctx, q, trackID)
	if err != nil {
		return "", err
	}
	return takeDigest(version, in), nil
}

// SoundComputeDef is the compute definition constant.
const SoundComputeDef = soundComputeDef
