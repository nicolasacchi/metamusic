package features

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service owns take_features and song_features: the only writer of both tables (D-L3-3).
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
