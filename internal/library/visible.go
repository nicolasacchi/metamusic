package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/view"
)

// Visible answers ErrNotFound when the viewer may not see the track: it does not exist, or it is discarded and the
// viewer hides discarded takes (C308.2). The two cases are indistinguishable (404 NOT_FOUND, never 403). A viewer that
// sees discarded takes costs no query and a missing id is not an error here: the caller's own lookup reports it.
func Visible(ctx context.Context, st interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, v view.Viewer, trackID string) error {
	if !v.HideDiscarded {
		return nil
	}
	var status string
	err := st.QueryRowContext(ctx, `SELECT status FROM tracks WHERE id = ?`, trackID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("library: visible: %w", err)
	}
	if status == "discarded" {
		return ErrNotFound
	}
	return nil
}

// Visible is the Service form of Visible, using the viewer of the context's principal. Every id must be visible.
func (s *Service) Visible(ctx context.Context, ids ...string) error {
	v := view.FromContext(ctx)
	for _, id := range ids {
		if err := Visible(ctx, s.st.R, v, id); err != nil {
			return err
		}
	}
	return nil
}
