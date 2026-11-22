package judgments

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Transactional variants for the Morning Crate's reveal (M6N C381.4). They are the ordinary writers run inside the caller's
// transaction: same validation, same rows, same errors; nothing here is visible until the caller commits, and nothing is audited
// (the caller's operation owns the audit row).

// AddCommentTx is AddComment inside tx. It returns the new comment's id; read the comment after the commit.
func (s *Service) AddCommentTx(ctx context.Context, tx *sql.Tx, p auth.Principal, src string, trackID string, in NewComment) (string, error) {
	return s.addComment(ctx, tx, tx, p, src, trackID, in)
}

// RateTx is Rate inside tx: it sets or clears the caller's own rating.
func (s *Service) RateTx(ctx context.Context, tx *sql.Tx, p auth.Principal, trackID string, stars *int, verdict *string) error {
	return s.rate(ctx, tx, tx, p, trackID, stars, verdict)
}
