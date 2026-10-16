package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// L1Options tunes the library-model services of Build (C87); every field is optional.
type L1Options struct {
	// Log is the Reconciler's logger; nil => slog.Default().
	Log *slog.Logger
	// Owner identifies this process in provenance_lease; "" => hostname-pid-random per process, so two server
	// processes on one database never share the settle lease (D-L1-4, D-L1-35).
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
func buildL1(st *store.Store, now func() time.Time, newID func() string, ds *duels.Service, ea *ears.Service, o L1Options) *l1Services {
	if o.Owner == "" {
		o.Owner = processOwner()
	}
	ms := muses.New(st, now, newID)
	ss := songs.New(st, now, newID)
	se := sessions.New(st, now)
	pi := picks.New(st, ds, now, newID)
	sd := sounds.New(st, now, newID)
	// Take flag codes come from Ears (read-only, starts no analysis); a nil Ears yields empty flags.
	var fl songview.Flags
	if ea != nil {
		fl = ea
	}
	sv := songview.New(st, ms, ss, se, pi, sd, ds, fl, now)
	// The Reconciler runs the real picks and sounds steps (D-L1-4).
	prov := provenance.New(provenance.Options{
		Store: st, Muses: ms, Songs: ss, Sessions: se, Picks: pi, Sounds: sd, Ladder: ds, Now: now, NewID: newID, Log: o.Log, Owner: o.Owner,
		FaultAfterPhaseA: o.FaultAfterPhaseA, BeforeSettle: o.BeforeSettle,
	})
	return &l1Services{Muses: ms, Songs: ss, SongView: sv, Sessions: se, Picks: pi, Sounds: sd, Board: board.New(st, now), Provenance: prov}
}

// libraryReader adapts songview to trackctx.LibraryReader so trackctx imports none of the L1 packages (C98, C99.1).
type libraryReader struct{ sv *songview.Service }

func (r libraryReader) ForTrack(ctx context.Context, trackID string) (trackctx.LibrarySections, error) {
	s, err := r.sv.ForTrack(ctx, trackID)
	return trackctx.LibrarySections{Song: s.Song, Muse: s.Muse, Sounds: s.Sounds}, err
}

// processOwner is a lease owner unique to this Build: hostname, pid and a random suffix.
func processOwner() string {
	host, _ := os.Hostname()
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}
