package provenance

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options configure the Reconciler (C95).
type Options struct {
	Store    *store.Store
	Muses    *muses.Service
	Songs    *songs.Service
	Sessions *sessions.Service
	Picks    PickStep  // nil => nopPicks
	Sounds   SoundStep // nil => nopSounds
	Ladder   picks.Ladder
	Now      func() time.Time
	NewID    func() string
	Log      *slog.Logger
	// Poll overrides picks.PollInterval and SweepEvery picks.SweepInterval in Run (tests; 0 => the defaults).
	Poll, SweepEvery time.Duration
	// Owner identifies this settler in provenance_lease; two services on one database need different owners.
	Owner string
	// FaultAfterPhaseA is a test seam: an error returned right after phase A commits aborts the pass.
	FaultAfterPhaseA func() error
	// BeforeSettle is a test seam called after the lease is taken, before the first pass.
	BeforeSettle func(context.Context)
}

// Service is the Reconciler.
type Service struct {
	o  Options
	mu sync.Mutex // serialises Settle within a process
}

// New builds the Service.
func New(o Options) *Service {
	if o.Picks == nil {
		o.Picks = nopPicks{}
	}
	if o.Sounds == nil {
		o.Sounds = nopSounds{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Poll <= 0 {
		o.Poll = picks.PollInterval
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = picks.SweepInterval
	}
	if o.Owner == "" {
		o.Owner = "settler"
	}
	return &Service{o: o}
}
