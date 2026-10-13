package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerBackups(a huma.API, d Deps) {
	op := huma.Operation{
		OperationID: "create_backup", Method: "POST", Path: "/api/backups",
		Summary:     "Start a backup",
		Description: "Starts a database and file snapshot in the background and returns the running run; poll list_backups. 409 while another backup runs.",
	}
	op.DefaultStatus = 202
	Register(a, op, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/settings/backups", Idempotency: "create", Rate: "heavy"},
		stub[createBackupIn, backupRunOut]("M2a-B7")) // STUB(M2a-B7)

	Register(a, huma.Operation{
		OperationID: "list_backups", Method: "GET", Path: "/api/backups",
		Summary:     "List backups",
		Description: "The newest backup runs, the snapshots on disk and the daily schedule.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings/backups"},
		stub[empty, backupListingOut]("M2a-B7")) // STUB(M2a-B7)
}
