package records

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Records service (C120, C121): the Record rows, the slot edits and the changelog writes of the human and
// agent operations. The method set is frozen by F2; the bodies live in the lane files (stub_a.go: Create to Resolved,
// stub_b.go: Changes, Verify, the Syncer and catchUpTx) and are replaced without changing a signature.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// New builds the service. A nil now means time.Now; a nil newID is the caller's mistake (the composition root always
// passes one).
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now, newID: newID}
}
