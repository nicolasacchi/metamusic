package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// The T1 CLI (C213): `metamusic transform verify <take-id> | --all [--limit n]` and `metamusic transform sweep`. Both are
// host-operator maintenance and a documented parity exception (D-T1-18): a re-render with no ingest and no queue row, and an
// offline recovery under the lease. There is no REST or MCP face.

// t1Options is the test seam of the services the CLI builds (the fakes of internal/transform/transformtest). Production leaves
// it zero.
var t1Options app.T1Options

// t1Service is what the two subcommands use of the transform service.
type t1Service interface {
	Verify(ctx context.Context, takeID string) (transform.VerifyReport, error)
	Recover(ctx context.Context) error
	Sweep(ctx context.Context) error
}

const defaultVerifyLimit = 50

// cmdTransform dispatches `transform verify|sweep`.
func cmdTransform(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("transform: expected verify|sweep")
	}
	switch args[0] {
	case "verify":
		return cmdTransformVerify(ctx, args[1:], getenv, s)
	case "sweep":
		return cmdTransformSweep(ctx, args[1:], getenv, s)
	}
	return usageError("transform: unknown subcommand " + args[0])
}

// openTransform opens the data directory and builds the services as mcp-stdio does (no runner, no lease), returning the
// transform service, the store and a close function.
func openTransform(ctx context.Context, getenv func(string) string) (*app.Services, *store.Store, func(), error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return nil, nil, nil, fatal(err)
	}
	cfg.Runner = false
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, nil, fatal(fmt.Errorf("transform: no database in the data directory"))
	}
	sec, err := config.LoadSecrets(cfg.SecretsDir, "local")
	if err != nil {
		return nil, nil, nil, fatal(err)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, nil, fatal(err)
	}
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		_ = st.Close()
		return nil, nil, nil, fatal(fmt.Errorf("opening data dir: %w", err))
	}
	svc, err := app.Build(ctx, cfg, sec, st, root, app.Options{Version: version, Commit: commit, T1: t1Options})
	if err != nil {
		_ = root.Close()
		_ = st.Close()
		return nil, nil, nil, fatal(err)
	}
	closeAll := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if err := svc.Close(cctx); err != nil {
			slog.Error("closing services failed", "err", redact.Error(err))
		}
		_ = root.Close()
		_ = st.Close()
	}
	if svc.Transform == nil {
		closeAll()
		return nil, nil, nil, fatal(fmt.Errorf("transform: transforms are disabled (MM_TRANSFORM_ENABLED=false)"))
	}
	return svc, st, closeAll, nil
}

func cmdTransformVerify(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("transform verify", s)
	all := fs.Bool("all", false, "verify the most recent done transforms")
	limit := fs.Int("limit", defaultVerifyLimit, "with --all, how many transforms at most")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if (*all && fs.NArg() > 0) || (!*all && fs.NArg() != 1) || fs.NArg() > 1 {
		return usageError("transform verify: expected a take id, or --all")
	}
	if *limit < 1 {
		return usageError("transform verify: --limit must be at least 1")
	}
	svc, st, closeAll, err := openTransform(ctx, getenv)
	if err != nil {
		return err
	}
	defer closeAll()
	ids := fs.Args()
	if *all {
		if ids, err = doneTransformIDs(ctx, st, *limit); err != nil {
			return fatal(err)
		}
	}
	return verifyIDs(ctx, svc.Transform, ids, *all, s)
}

// doneTransformIDs lists the newest done transforms.
func doneTransformIDs(ctx context.Context, st *store.Store, limit int) ([]string, error) {
	rows, err := st.R.QueryContext(ctx, `SELECT id FROM transforms WHERE state = 'done' ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// verifyIDs re-renders each take and reports one line per take: match, MISMATCH or inconclusive (with the reason). Exit 1 on any
// mismatch (or, with --all, any take that could not be verified); an inconclusive answer is reported and exits 0 (C213).
func verifyIDs(ctx context.Context, svc t1Service, ids []string, many bool, s streams) error {
	bad, match, inconclusive := 0, 0, 0
	for _, id := range ids {
		rep, err := svc.Verify(ctx, id)
		switch {
		case err != nil && !many:
			return fatal(fmt.Errorf("transform verify: %s", redact.Error(err)))
		case err != nil:
			fmt.Fprintf(s.out, "%s error %s\n", id, redact.Error(err))
			bad++
		case rep.Inconclusive:
			fmt.Fprintf(s.out, "%s inconclusive %s\n", id, redact.String(rep.Reason))
			inconclusive++
		case rep.Match:
			fmt.Fprintf(s.out, "%s match %s\n", id, rep.Got)
			match++
		default:
			fmt.Fprintf(s.out, "%s MISMATCH expected %s got %s\n", id, rep.Expected, rep.Got)
			bad++
		}
	}
	if many {
		fmt.Fprintf(s.out, "%d verified: %d match, %d inconclusive, %d failed\n", len(ids), match, inconclusive, bad)
	}
	if bad > 0 {
		return exitError{code: 1}
	}
	return nil
}

func cmdTransformSweep(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("transform sweep", s)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("transform sweep: unexpected argument " + fs.Arg(0))
	}
	svc, st, closeAll, err := openTransform(ctx, getenv)
	if err != nil {
		return err
	}
	defer closeAll()
	return sweepOffline(ctx, st, svc.Transform, time.Now, s)
}

// sweepOffline is C210.3 run by hand: recover expired leases, then remove stale work dirs and orphan take directories. It
// refuses while a server holds a live lease on a running transform (an unexpired lease_expires_at), since that server's runner
// owns those directories.
func sweepOffline(ctx context.Context, st *store.Store, svc t1Service, now func() time.Time, s streams) error {
	var live int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM transforms WHERE state = 'running' AND lease_expires_at > ?`,
		store.FormatTime(now())).Scan(&live); err != nil {
		return fatal(err)
	}
	if live > 0 {
		return fatal(fmt.Errorf("transform sweep: a server holds a live lease on %d running transform(s); stop it first or let it sweep itself", live))
	}
	if err := svc.Recover(ctx); err != nil {
		return fatal(fmt.Errorf("transform sweep: %s", redact.Error(err)))
	}
	if err := svc.Sweep(ctx); err != nil {
		return fatal(fmt.Errorf("transform sweep: %s", redact.Error(err)))
	}
	fmt.Fprintln(s.out, "transform sweep: done")
	return nil
}
