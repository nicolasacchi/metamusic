package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// newProvenance builds the Reconciler a CLI process settles with. It owns its own lease owner: a server running on the
// same database holds the lease under another one (D-L1-35). The Picks and Sounds steps join with L1-B7; until then the
// CLI derives the structural rows (Songs, Muses from refs, Sessions) like the server does.
func newProvenance(st *store.Store) *provenance.Service {
	return provenance.New(provenance.Options{Store: st, Now: time.Now, Owner: fmt.Sprintf("cli-%d", os.Getpid())})
}

// settleCLI drains the queue when this process can take the settle lease and otherwise only reports the queue: with a
// server up the server drains it within a poll interval (D-L1-27, D-L1-35). It returns the one-line summary and never
// fails the calling command (import, seed-synthetic): a database that could not be settled is reported, not hidden.
func settleCLI(ctx context.Context, st *store.Store) string {
	prov := newProvenance(st)
	rep, err := prov.Settle(ctx)
	switch {
	case errors.Is(err, provenance.ErrSettleBusy):
		q, _ := prov.PendingByKind(ctx)
		return "Library model: another process holds the settle lease; queue " + provenance.FormatQueue(q) + " (the server drains it)"
	case err != nil:
		return "Library model: failed (" + err.Error() + "); run `metamusic library reconcile`"
	}
	return fmt.Sprintf("Library model: %d keys settled, %d Songs created, %d Muses, %d sessions", rep.KeysProcessed, rep.SongsCreated, rep.MusesCreated, rep.SessionsCreated)
}

// cmdLibrary is `metamusic library reconcile [--dry-run] [--wait] | verify` (D-L1-27).
func cmdLibrary(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("library: expected reconcile|verify")
	}
	switch args[0] {
	case "reconcile":
		return cmdLibraryReconcile(ctx, args[1:], getenv, s)
	case "verify":
		return cmdLibraryVerify(ctx, args[1:], getenv, s)
	}
	return usageError("library: unknown subcommand " + args[0])
}

func openLibraryStore(ctx context.Context, getenv func(string) string, readOnly bool) (*store.Store, error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return nil, fatal(err)
	}
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fatal(errors.New("library: no database in the data directory"))
	}
	open := store.Open
	if readOnly {
		open = store.OpenReadOnly
	}
	st, err := open(ctx, dbPath)
	if err != nil {
		return nil, fatal(err)
	}
	return st, nil
}

func cmdLibraryReconcile(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("library reconcile", s)
	dry := fs.Bool("dry-run", false, "report the queue by kind; write nothing")
	wait := fs.Bool("wait", false, "when a server holds the settle lease, poll until it has drained the queue")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("library reconcile: unexpected argument " + fs.Arg(0))
	}
	st, err := openLibraryStore(ctx, getenv, *dry)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	prov := newProvenance(st)
	q, err := prov.PendingByKind(ctx)
	if err != nil {
		return fatal(err)
	}
	if *dry {
		fmt.Fprintf(s.out, "queue: %s (dry-run: nothing written)\n", provenance.FormatQueue(q))
		return nil
	}
	rep, err := prov.Settle(ctx)
	switch {
	case errors.Is(err, provenance.ErrSettleBusy):
		fmt.Fprintf(s.out, "another process holds the settle lease; queue: %s\n", provenance.FormatQueue(q))
		if !*wait {
			return nil
		}
		for {
			n, err := prov.Pending(ctx)
			if err != nil {
				return fatal(err)
			}
			if n == 0 {
				fmt.Fprintln(s.out, "queue: empty")
				return nil
			}
			select {
			case <-ctx.Done():
				return fatal(ctx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
	case err != nil:
		return fatal(fmt.Errorf("library reconcile: %w", err))
	}
	fmt.Fprintf(s.out, "reconciled: %d passes, %d keys, Songs +%d (attached %d, retired %d), Muses +%d, sessions +%d, members +%d\n",
		rep.Passes, rep.KeysProcessed, rep.SongsCreated, rep.SongsAttached, rep.SongsRetired, rep.MusesCreated, rep.SessionsCreated, rep.MembersAdded)
	for _, a := range rep.Anomalies {
		fmt.Fprintf(s.out, "anomaly: %s %s\n", a.Code, a.Subject)
	}
	return nil
}

func cmdLibraryVerify(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("library verify", s)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("library verify: unexpected argument " + fs.Arg(0))
	}
	st, err := openLibraryStore(ctx, getenv, true)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	an, err := newProvenance(st).Verify(ctx)
	if err != nil {
		return fatal(err)
	}
	if len(an) == 0 {
		fmt.Fprintln(s.out, "library model: consistent")
		return nil
	}
	for _, a := range an {
		fmt.Fprintf(s.out, "%s %s %s\n", a.Code, a.Subject, a.Detail)
	}
	return exitError{code: 1, err: fmt.Errorf("%d anomalies", len(an))}
}
