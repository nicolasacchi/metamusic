package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// cmdBackup takes a snapshot now (`backup [--to DIR]`) or lists snapshots
// (`backup list`). Exit 0 on success, 1 when the backup failed, 2 on usage
// errors (including a backup already running).
func cmdBackup(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	list := len(args) > 0 && args[0] == "list"
	if list {
		args = slices.Delete(args, 0, 1)
	}
	fs := newFlags("backup", s)
	to := fs.String("to", "", "write snapshots into this `DIR` instead of MM_BACKUP_DIR")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("backup: unexpected argument " + fs.Arg(0))
	}
	if list && *to != "" {
		return usageError("backup list: --to does not apply")
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	if *to != "" {
		abs, err := filepath.Abs(*to)
		if err != nil {
			return usageError("backup: " + err.Error())
		}
		data, _ := filepath.Abs(cfg.DataDir)
		if rel, err := filepath.Rel(data, abs); err == nil && filepath.IsLocal(rel) { // "." included
			return usageError("backup: --to must be outside the data directory")
		}
		cfg.BackupDir = abs
	}
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fatal(errors.New("backup: no database in the data directory"))
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	lib := library.New(st, nil, time.Now)
	svc := backup.New(backup.Options{
		Store: st, DataDir: cfg.DataDir, BackupDir: cfg.BackupDir,
		Exports: exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil)),
		Version: version, Notify: cliNotifier(cfg),
	})
	defer func() { _ = svc.Close(context.WithoutCancel(ctx)) }()

	if list {
		return printBackups(ctx, svc, s)
	}
	run, err := svc.RunSync(ctx, "cli", auth.CLI)
	var be *backup.Error
	switch {
	case errors.As(err, &be) && be.Status == http.StatusConflict:
		return usageError("backup: " + be.Msg)
	case err != nil:
		return exitError{code: 1, err: fmt.Errorf("backup failed: %w", err)}
	}
	fmt.Fprintf(s.out, "snapshot %s: %d files, %d bytes (%d linked, %d copied)\n",
		*run.Snapshot, run.Stats.Files, run.Stats.Bytes, run.Stats.Linked, run.Stats.Copied)
	return nil
}

// cliNotifier publishes failures through ntfy when it is configured and the
// optional token file is readable; otherwise notifications are dropped.
func cliNotifier(cfg config.Config) notify.Notifier {
	if cfg.NtfyURL == "" {
		return notify.Nop{}
	}
	var token string
	if sec, err := config.LoadSecrets(cfg.SecretsDir, "local"); err == nil {
		token = sec.NtfyToken
	}
	return notify.NewNtfy(cfg.NtfyURL, token, &http.Client{Timeout: 10 * time.Second})
}

func printBackups(ctx context.Context, svc *backup.Service, s streams) error {
	l, err := svc.List(ctx)
	if err != nil {
		return fatal(err)
	}
	fmt.Fprintf(s.out, "%d snapshot(s)\n", len(l.Snapshots))
	for _, sn := range l.Snapshots {
		fmt.Fprintf(s.out, "  %s  %s  %d files  %d bytes  (%d linked, %d copied)\n", sn.Name, sn.CreatedAt, sn.Files, sn.Bytes, sn.Linked, sn.Copied)
	}
	fmt.Fprintf(s.out, "%d recent run(s)\n", len(l.Runs))
	for _, r := range l.Runs {
		snap := "-"
		if r.Snapshot != nil {
			snap = *r.Snapshot
		}
		fmt.Fprintf(s.out, "  %s  %-9s  %-9s  %s  %s", r.StartedAt, r.State, r.Cause, snap, r.ID)
		if r.Error != nil {
			fmt.Fprintf(s.out, "  error: %s", *r.Error)
		}
		fmt.Fprintln(s.out)
	}
	return nil
}

// cmdRestore rebuilds a data directory from a snapshot (`restore --from SNAPSHOT
// --into DIR`): exit 0 ok, 1 when verification fails, 2 on usage errors, a
// non-empty target or a snapshot from a newer schema. It needs no MM_AUTH.
func cmdRestore(ctx context.Context, args []string, _ func(string) string, s streams) error {
	fs := newFlags("restore", s)
	from := fs.String("from", "", "snapshot `DIR` (inside the backup directory)")
	into := fs.String("into", "", "new or empty data `DIR` to rebuild")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 || *from == "" || *into == "" {
		return usageError("restore: expected --from SNAPSHOT --into DIR")
	}
	rep, err := backup.Restore(ctx, *from, *into, backup.RestoreOptions{Version: version})
	var ve *backup.VerifyError
	switch {
	case errors.As(err, &ve):
		fmt.Fprintln(s.err, "metamusic: snapshot verification failed:")
		for _, p := range ve.Paths {
			fmt.Fprintf(s.err, "  %s\n", p)
		}
		return exitError{code: 1}
	case errors.Is(err, backup.ErrTargetNotEmpty), errors.Is(err, backup.ErrNewerSchema), errors.Is(err, backup.ErrBadSnapshot):
		return usageError("restore: " + err.Error())
	case err != nil:
		return exitError{code: 1, err: fmt.Errorf("restore failed: %w", err)}
	}
	fmt.Fprintf(s.out, "restored %d files (%d linked, %d copied), schema %d -> %d\n", rep.Files, rep.Linked, rep.Copied, rep.SchemaFrom, rep.SchemaTo)
	for _, k := range []string{"tracks", "masters", "artifacts", "renditions", "drafts", "comments"} {
		if n, ok := rep.Counts[k]; ok {
			fmt.Fprintf(s.out, "  %-10s %d\n", k, n)
		}
	}
	abs, err := filepath.Abs(*into)
	if err != nil {
		abs = *into
	}
	fmt.Fprintf(s.out, "next: start the server with MM_DATA_DIR=%s\n", abs)
	fmt.Fprintln(s.out, "secrets are not part of backups: keep the same secret_key to keep tokens, sessions and devices valid; a new one invalidates them")
	return nil
}
