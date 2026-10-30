package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// The L2 glue of serve and `library` (C118, C130.9). Final since L2-INT-B.

// startL2 starts the Syncer and the tape runner on runCtx and the daily tape sweep on pruneCtx. serve calls it once, after
// the first Settle; mcp-stdio, the rigs and the CLI never do.
func startL2(runCtx, pruneCtx context.Context, svc *app.Services, running, pruned *sync.WaitGroup) {
	if svc.Syncer != nil {
		running.Add(1)
		go func() { defer running.Done(); svc.Syncer.Run(runCtx) }()
	}
	if svc.Tapes != nil {
		if err := svc.Tapes.Recover(runCtx); err != nil && runCtx.Err() == nil {
			slog.Error("tape recovery failed", "err", redact.Error(err))
		}
		running.Add(1)
		go func() { defer running.Done(); svc.Tapes.Run(runCtx) }()
		pruned.Add(1)
		go func() { defer pruned.Done(); sweepTapes(pruneCtx, svc.Tapes, 24*time.Hour) }()
	}
}

// sweepTapes expires tapes older than tape.RetentionDays now and then every interval until ctx ends.
func sweepTapes(ctx context.Context, s sweeper, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.Sweep(ctx, tape.RetentionDays*24*time.Hour); err != nil && ctx.Err() == nil {
			slog.Error("tape sweep failed", "err", redact.Error(err))
		} else if n > 0 {
			slog.Info("tape sweep", "expired", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// newRecords builds the Records service and Syncer a CLI process uses (own clock and ids; no server needed).
func newRecords(st *store.Store) (*records.Service, *records.Syncer) {
	// Verify R7 reads the Crate filter vocabulary through this hook (records imports no other L2 package); serve installs the
	// same one, so `library verify` raises crate_filter_invalid too.
	records.SetCrateFilterCheck(func(subject string, filter json.RawMessage) error {
		_, err := crates.ParseFilter(crates.Subject(subject), filter)
		return err
	})
	svc := records.New(st, time.Now, func() string { return uuid.NewString() })
	return svc, records.NewSyncer(svc, nil)
}

// l2Verify runs records.Verify after the L1 invariants (C130.9). The lines use the L1 form "<code> <subject> <detail>";
// errors counts the findings of severity "error", which alone make `library verify` exit 1 (warnings are printed only).
func l2Verify(ctx context.Context, st *store.Store, out io.Writer) (errors int, err error) {
	svc, _ := newRecords(st)
	an, err := svc.Verify(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range an {
		fmt.Fprintf(out, "%s %s %s\n", a.Code, a.Subject, a.Detail)
		if a.Severity == "error" {
			errors++
		}
	}
	return errors, nil
}

// l2Reconcile runs the Syncer after the provenance Settle (C130.9); with dry it only reports the slots with pending events.
func l2Reconcile(ctx context.Context, st *store.Store, dry bool, out io.Writer) error {
	_, syncer := newRecords(st)
	if dry {
		n, err := syncer.Pending(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "records: %d slots with pending Pick events (dry-run: nothing written)\n", n)
		return nil
	}
	rep, err := syncer.Settle(ctx)
	if err != nil {
		return err
	}
	if rep.Slots > 0 || rep.Rows > 0 {
		fmt.Fprintf(out, "records synced: %d slots, %d rows, %d followed, %d removed\n", rep.Slots, rep.Rows, rep.Followed, rep.Removed)
	}
	return nil
}
