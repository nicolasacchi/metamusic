package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerAudit(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_audit", Method: "GET", Path: "/api/audit",
		Summary:     "Read the audit log",
		Description: "Recorded agent calls and operator mutations, newest first, filtered by principal, operation, time and status. Never contains request bodies.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/settings/audit"},
		stub[getAuditIn, auditOut]("M2a-B8")) // STUB(M2a-B8)
}
