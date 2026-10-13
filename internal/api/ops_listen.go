package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerListen(a huma.API, d Deps) {
	Register(a, withBody(huma.Operation{
		OperationID: "log_listen_events", Method: "POST", Path: "/api/listen-events",
		Summary:     "Log listen events",
		Description: "Records player events (start, segment, seek, complete, skip) with client-generated ids; re-sending an id is harmless.",
	}, bodyListen), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		stub[logListenIn, logListenOut]("M2a-B8")) // STUB(M2a-B8)
}
