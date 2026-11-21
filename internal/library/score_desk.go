package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// maxScoreFileBytes bounds the read of a stored score.abc (the transform limit MaxScoreBytes is 256 KiB; the cap is a guard
// against a corrupted or swapped file).
const maxScoreFileBytes = 1 << 20

// ScoreABC returns the score.abc text of a take that has a scores row (D-DESK-16, C355.7): the bytes of the file at
// scores.abc_path read through the data root and checked against the recorded abc_sha256. ok is false when the take has no
// score; an unreadable file or a hash mismatch is an error (the file is frozen, so a mismatch is corruption, never stale data).
func (s *Service) ScoreABC(ctx context.Context, trackID string) (text []byte, sha256hex string, ok bool, err error) {
	var rel, want string
	err = s.st.R.QueryRowContext(ctx, `SELECT abc_path, abc_sha256 FROM scores WHERE track_id = ?`, trackID).Scan(&rel, &want)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("library: score row: %w", err)
	}
	if s.data == nil || rel == "" || strings.HasPrefix(rel, "/") {
		return nil, "", false, fmt.Errorf("library: score file of %s is not readable", trackID)
	}
	b, err := s.data.ReadFile(rel)
	if err != nil {
		return nil, "", false, fmt.Errorf("library: score file of %s: %w", trackID, err)
	}
	if len(b) > maxScoreFileBytes {
		return nil, "", false, fmt.Errorf("library: score file of %s is too large", trackID)
	}
	if sha256Hex(b) != want {
		return nil, "", false, fmt.Errorf("library: score file of %s does not match its recorded hash", trackID)
	}
	return b, want, true, nil
}
