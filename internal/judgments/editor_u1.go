package judgments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func ownerOnlyErr(what string) error {
	return &Error{Status: 403, Code: "OWNER_ONLY", Msg: what + " is restricted to the Owner"}
}

// editorGuard is the field rule for a principal that is not on the Owner side (C305, Q4): a Listener changes nothing; an Editor
// may change titles and set or clear to_relisten (draft), nothing else. It returns the SQL fragment appended to the UPDATE, so
// the check and the write are one statement: a track that is already final or discarded keeps its status even if the Owner
// moves it between a read and the write. Discarding changes what Listeners see, so it stays the Owner's. An unknown status is
// a validation error, as for the Owner side.
func (s *Service) editorGuard(p auth.Principal, status *string) (string, error) {
	if p.IsOwnerSide() {
		return "", nil
	}
	if p.Role != auth.RoleEditor {
		return "", ownerOnlyErr("updating a track")
	}
	if status == nil {
		return "", nil
	}
	switch *status {
	case "draft", "to_relisten":
		return " AND status NOT IN ('final','discarded')", nil
	case "final", "discarded":
		return "", ownerOnlyErr("setting a track " + *status)
	}
	return "", inv("status", "must be draft, to_relisten, final or discarded")
}

// updateMissed explains an UPDATE that touched no row: no such track, or (guarded) a final or discarded one.
func (s *Service) updateMissed(ctx context.Context, trackID string, guarded bool) error {
	var one int
	switch err := s.st.W.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, trackID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return library.ErrNotFound
	case err != nil:
		return fmt.Errorf("judgments: track lookup: %w", err)
	}
	if guarded {
		return ownerOnlyErr("changing the status of a final or discarded track")
	}
	return library.ErrNotFound
}
