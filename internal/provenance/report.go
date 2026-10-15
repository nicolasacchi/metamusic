// Package provenance is the Reconciler (C95): the only writer of derived library rows. SQL triggers on the source
// tables enqueue dirty keys; Settle drains the queue under a cross-process lease, one transaction per phase, so a
// database built by any writer (importer, drafts, M3 ingest, ratings, duels, Ears) ends consistent (D-L1-4).
package provenance

import (
	"errors"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

// Anomaly is a structural fault found by a reconcile function or by Verify (C95.5).
type Anomaly = songs.Anomaly

// Errors of the settle protocol (D-L1-35). provenance owns its own: store.ErrLeaseLost belongs to the runner lease.
var (
	// ErrSettleBusy: another live process holds the settle lease.
	ErrSettleBusy = errors.New("provenance: another settler holds the lease")
	// ErrLeaseLost: this settler's epoch went stale; nothing was written.
	ErrLeaseLost = errors.New("provenance: settle lease lost")
)

// Report is what Settle did.
type Report struct {
	Passes, KeysProcessed, KeysRequeued, KeysDropped int
	SongsCreated, SongsAttached, SongsRetired        int
	MusesCreated                                     int
	SessionsCreated, MembersAdded                    int
	PickEvents, PickRefreshed, SoundsComputed        int
	Anomalies                                        []Anomaly
}

// Add accumulates o into r.
func (r *Report) Add(o Report) {
	r.Passes += o.Passes
	r.KeysProcessed += o.KeysProcessed
	r.KeysRequeued += o.KeysRequeued
	r.KeysDropped += o.KeysDropped
	r.SongsCreated += o.SongsCreated
	r.SongsAttached += o.SongsAttached
	r.SongsRetired += o.SongsRetired
	r.MusesCreated += o.MusesCreated
	r.SessionsCreated += o.SessionsCreated
	r.MembersAdded += o.MembersAdded
	r.PickEvents += o.PickEvents
	r.PickRefreshed += o.PickRefreshed
	r.SoundsComputed += o.SoundsComputed
	r.Anomalies = append(r.Anomalies, o.Anomalies...)
}
