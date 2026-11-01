package atlas

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service answers the Atlas and adherence reads; both are read-only.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the service. A nil now means time.Now.
func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}
