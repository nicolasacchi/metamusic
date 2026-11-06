package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Stem artifacts (E2b, C157.1, C166). A stem is a file attached to its source take as the artifact stem-<name>.flac; a repeated
// stem name is stored as stem-<name>-<n>.flac (n from 2). No tracks row exists, so nothing in Picks, Songs, search, stats,
// Records or tapes can see a stem.

// StemArtifactName is the artifact name of the n-th stem called name (n 1 has no suffix).
func StemArtifactName(name string, n int) string {
	if n <= 1 {
		return "stem-" + name + ".flac"
	}
	return "stem-" + name + "-" + strconv.Itoa(n) + ".flac"
}

// NextStemArtifact returns the artifact name the next stem called name of the take gets, and its number: the first free of
// stem-<name>.flac, stem-<name>-2.flac, ... It reads inside tx, so the answer holds for the insert that follows it.
func NextStemArtifact(ctx context.Context, tx *sql.Tx, trackID, name string) (artifact string, n int, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM artifacts WHERE track_id = ? AND name GLOB ? ORDER BY name`, trackID, "stem-"+name+"*.flac")
	if err != nil {
		return "", 0, fmt.Errorf("library: stem artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	last := 0
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return "", 0, err
		}
		if base, ok := StemName(a); !ok || base != name {
			continue
		}
		k := 1
		if a != StemArtifactName(name, 1) {
			v, cerr := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(a, "stem-"+name+"-"), ".flac"))
			if cerr != nil {
				continue
			}
			k = v
		}
		last = max(last, k)
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	n = last + 1
	return StemArtifactName(name, n), n, nil
}

// ErrNoSuchTrack is returned by AttachArtifact for a take that does not exist.
var ErrNoSuchTrack = errors.New("library: no such track")

// AttachArtifact inserts one artifact row of an existing take inside tx (the file is already in place, frozen at 0444). It is
// the only write E2b's attach ingest makes to the library: artifacts rows are immutable by trigger and the primary key
// (track_id, name) refuses a name twice.
func (s *Service) AttachArtifact(ctx context.Context, tx *sql.Tx, trackID string, a NewArtifact) error {
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, trackID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoSuchTrack
	case err != nil:
		return fmt.Errorf("library: attach artifact: %w", err)
	}
	if err := execTx(ctx, tx, `INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?,?,?,?,?,?)`,
		trackID, a.Name, a.SHA256, a.Bytes, a.Path, s.nowStr()); err != nil {
		return fmt.Errorf("library: attach artifact: %w", err)
	}
	return nil
}
