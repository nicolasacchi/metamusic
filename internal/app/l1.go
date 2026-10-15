package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// L1Options tunes the library-model services of Build (C87); every field is optional.
type L1Options struct {
	// Log is the Reconciler's logger; nil => slog.Default().
	Log *slog.Logger
	// Owner identifies this process in provenance_lease; "" => "settler". Two processes on one database need
	// different owners (tests that model two settlers set it).
	Owner string
	// BeforeSettle is called after the settle lease is taken, before the first pass (test seam: holds the first
	// Settle of TestServe_SettlesBeforeListening).
	BeforeSettle func(context.Context)
	// FaultAfterPhaseA returns an error right after phase A commits (test seam: TestReconciler_CrashBetweenPhases).
	FaultAfterPhaseA func() error
}

// l1Services are the L1 services Build constructs.
type l1Services struct {
	Muses      *muses.Service
	Songs      *songs.Service
	SongView   *songview.Service
	Sessions   *sessions.Service
	Picks      *picks.Service
	Sounds     *sounds.Service
	Board      *board.Service
	Provenance *provenance.Service
}

// buildL1 constructs the L1 services and wires the cross-package seams: the ladder arrives at picks (and songview)
// from duels so those packages import neither duels nor ears (C87).
func buildL1(st *store.Store, now func() time.Time, newID func() string, ds *duels.Service, o L1Options) *l1Services {
	ms := muses.New(st, now, newID)
	ss := songs.New(st, now, newID)
	se := sessions.New(st, now)
	pi := picks.New(st, ds, now, newID)
	sd := sounds.New(st, now, newID)
	// songs.Flags is wired to ears.Service.FlagCodes by L1-E1; until then flags are empty (nil).
	sv := songview.New(st, ms, ss, se, pi, sd, ds, nil, now)
	// The real picks and sounds steps join the Reconciler in L1-B7; until then it runs with the no-op steps.
	prov := provenance.New(provenance.Options{
		Store: st, Muses: ms, Songs: ss, Sessions: se, Ladder: ds, Now: now, NewID: newID, Log: o.Log, Owner: o.Owner,
		FaultAfterPhaseA: o.FaultAfterPhaseA, BeforeSettle: o.BeforeSettle,
	})
	return &l1Services{Muses: ms, Songs: ss, SongView: sv, Sessions: se, Picks: pi, Sounds: sd, Board: board.New(st, now), Provenance: prov}
}
