package albums

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenOps are the operations whose success can change what the catalog shows without touching a table Token probes directly:
// a Take's title or status, a Song's title, a Muse. The audit_log rows of these operations are the fifth probe (C126.5).
var TokenOps = []string{"update_track", "update_song", "update_muse", "assign_muse_songs"}

// exportFlag is implemented by curation.Service; the export opt-in does not change an album, but the token is documented to
// move on every flag change, so it is read when the Settings value offers it.
type exportFlag interface {
	ExportMuseNames(ctx context.Context) (bool, error)
}

// Token is a cheap change token (C126.5): the first 16 hex characters of sha256 over a handful of indexed probes and the
// opt-in flags. It never scans Takes and never calls MuseDirectory.Ordinals or Labels, only Version. It changes on every
// publish, unpublish, hide and edit of a listed Record, every Pick event, every Song change, every Take title or status
// edit made through TokenOps and every flag change, and does not change on unrelated writes such as a rating.
func (s *Service) Token(ctx context.Context) (string, error) {
	marks := make([]string, 0, 12)
	args := make([]any, len(TokenOps))
	ph := make([]string, len(TokenOps))
	for i, op := range TokenOps {
		args[i], ph[i] = op, "?"
	}
	var listed, maxRec, maxChange, maxPick, maxSong, maxAudit string
	err := s.st.R.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM records WHERE status = 'published' AND hidden_at IS NULL),
		COALESCE((SELECT MAX(updated_at) FROM records WHERE status = 'published' AND hidden_at IS NULL), ''),
		COALESCE((SELECT MAX(rowid) FROM record_changes), 0),
		COALESCE((SELECT MAX(rowid) FROM pick_events), 0),
		COALESCE((SELECT MAX(updated_at) FROM songs), ''),
		COALESCE((SELECT MAX(id) FROM audit_log WHERE op IN (`+strings.Join(ph, ",")+`) AND status < 400), '')`, args...).
		Scan(&listed, &maxRec, &maxChange, &maxPick, &maxSong, &maxAudit)
	if err != nil {
		return "", fmt.Errorf("albums: token: %w", err)
	}
	marks = append(marks, listed, maxRec, maxChange, maxPick, maxSong, maxAudit)
	dir := ""
	if s.muses != nil {
		if dir, err = s.muses.Version(ctx); err != nil {
			return "", fmt.Errorf("albums: token: %w", err)
		}
	}
	marks = append(marks, dir)
	if s.set != nil {
		sub, err := s.set.SubsonicMuseNames(ctx)
		if err != nil {
			return "", fmt.Errorf("albums: token: %w", err)
		}
		marks = append(marks, fmt.Sprint(sub))
		if e, ok := s.set.(exportFlag); ok {
			exp, err := e.ExportMuseNames(ctx)
			if err != nil {
				return "", fmt.Errorf("albums: token: %w", err)
			}
			marks = append(marks, fmt.Sprint(exp))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(marks, "|")))
	return hex.EncodeToString(sum[:8]), nil
}
