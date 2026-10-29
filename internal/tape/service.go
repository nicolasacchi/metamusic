package tape

import (
	"log/slog"
	"time"
)

// Service is the tape export service (C128.1, C128.5): snapshot, plan, key, quotas, the single runner, the resolver and the
// sweep. The method set is frozen by F2; export.go, runner.go, resolve.go and sweep.go hold the behaviour (T2), pipeline.go
// and its files the production Pipeline (T1).
type Service struct {
	o   Options
	now func() time.Time
	log *slog.Logger
	// wake nudges the runner after an export queued a tape (capacity one: a pending nudge is enough).
	wake chan struct{}
	// renewEvery is how often a running tape renews its lease; progressEvery is the least gap between progress writes.
	renewEvery, progressEvery time.Duration
}

// New builds the service. A nil Now means time.Now and a nil Log slog.Default(); nil collaborators make the matching
// operations fail at use, never here, so the composition root can build the service on a store without audio tools.
func New(o Options) *Service {
	s := &Service{o: o, now: o.Now, log: o.Log, wake: make(chan struct{}, 1), renewEvery: LeaseTTL / 6, progressEvery: time.Second}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}
