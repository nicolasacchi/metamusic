package records

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

// STUB(L2-R-b): the frozen method set of C122. Lane R-b deletes this file and adds sync.go and verify.go; no signature
// changes. The reads that rigs and the CLI call in every process (Syncer.Settle, Pending, Run and Verify) are harmless
// no-ops here, so an idle stub never fails a rig; Changes answers errors.ErrUnsupported.

// Changes returns one page of a Record's changelog (C122.4).
func (s *Service) Changes(ctx context.Context, f ChangeFilter) (ChangePage, error) {
	return ChangePage{}, errors.ErrUnsupported
}

// Verify checks the invariants R1 to R9 (C122.5), read-only.
func (s *Service) Verify(ctx context.Context) ([]Anomaly, error) { return nil, nil }

// catchUpTx is the seam of EditSlots: it logs every pending Pick event of a Record's floating slots inside the caller's
// transaction, so history is never reordered around an edit (C121.3 step 2). Lane R-a calls it; lane R-b fills it.
func (s *Service) catchUpTx(ctx context.Context, tx *sql.Tx, recordID string) error { return nil }

// Syncer turns L1 pick_events into changelog rows (C122.1, D-L2-7). Any number of Syncers (server, CLI) are safe.
type Syncer struct {
	svc *Service
	log *slog.Logger
}

// NewSyncer builds a Syncer over s; a nil log means slog.Default().
func NewSyncer(s *Service, log *slog.Logger) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{svc: s, log: log}
}

// Settle drains until no slot has unconsumed events; safe next to any other Settle.
func (y *Syncer) Settle(ctx context.Context) (SyncReport, error) { return SyncReport{}, nil }

// Run loops, settling every SyncPoll while Pending is positive, until ctx ends.
func (y *Syncer) Run(ctx context.Context) { <-ctx.Done() }

// Pending is the number of slots with events beyond synced_seq (one indexed query).
func (y *Syncer) Pending(ctx context.Context) (int, error) { return 0, nil }
