package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
)

// backupErr maps backup errors to the problem model.
func backupErr(err error) error {
	var be *backup.Error
	if errors.As(err, &be) {
		return Err(be.Status, be.Code, be.Msg, nil)
	}
	return err
}

func registerBackups(a huma.API, d Deps) {
	op := huma.Operation{
		OperationID: "create_backup", Method: "POST", Path: "/api/backups",
		Summary:     "Start a backup",
		Description: "Starts a database and file snapshot in the background and returns the running run; poll list_backups. 409 while another backup runs.",
	}
	op.DefaultStatus = 202
	Register(a, op, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/settings/backups", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, _ *createBackupIn) (*backupRunOut, error) {
			if d.Backups == nil {
				return nil, unavailable("backups")
			}
			run, err := d.Backups.Start(ctx, "manual", auth.FromContext(ctx))
			if err != nil {
				return nil, backupErr(err)
			}
			return &backupRunOut{Body: run}, nil
		})
	Register(a, huma.Operation{
		OperationID: "list_backups", Method: "GET", Path: "/api/backups",
		Summary:     "List backups",
		Description: "The newest backup runs, the snapshots on disk and the daily schedule.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings/backups"},
		func(ctx context.Context, _ *empty) (*backupListingOut, error) {
			if d.Backups == nil {
				return nil, unavailable("backups")
			}
			l, err := d.Backups.List(ctx)
			if err != nil {
				return nil, backupErr(err)
			}
			now := time.Now
			if d.Now != nil {
				now = d.Now
			}
			l.Schedule = backup.ScheduleFor(scheduleAt(d), now(), time.Local)
			return &backupListingOut{Body: l}, nil
		})
}

// scheduleAt is the configured daily backup time; "off" without a configuration.
func scheduleAt(d Deps) string {
	if d.Cfg.BackupAt == "" {
		return "off"
	}
	return d.Cfg.BackupAt
}
