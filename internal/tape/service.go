package tape

import (
	"log/slog"
	"time"
)

// Service is the tape export service (C128.1, C128.5): snapshot, plan, key, quotas, the single runner, the resolver and the
// sweep. The method set is frozen by F2; lane T2 replaces stub_t2.go and lane T1 replaces stub_t1.go.
type Service struct {
	o   Options
	now func() time.Time
	log *slog.Logger
}

// New builds the service. A nil Now means time.Now and a nil Log slog.Default(); nil collaborators make the matching
// operations fail at use, never here, so the composition root can build the service on a store without audio tools.
func New(o Options) *Service {
	s := &Service{o: o, now: o.Now, log: o.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}
