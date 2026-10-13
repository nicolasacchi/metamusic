package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type pingOut struct {
	Body struct {
		OK      bool   `json:"ok" doc:"Always true"`
		Version string `json:"version" doc:"Server version"`
	}
}

type whoamiOut struct {
	Body struct {
		Kind   string   `json:"kind" enum:"human,agent,device" doc:"Principal kind"`
		Name   string   `json:"name" doc:"Username or token name"`
		Scopes []string `json:"scopes" nullable:"false" doc:"Granted scopes"`
		Via    string   `json:"via" doc:"How the principal authenticated: proxy, local, token or device"`
	}
}

type systemOut struct {
	Body struct {
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		AuthMode      string `json:"auth_mode"`
		SchemaVersion int    `json:"schema_version" doc:"SQLite migration level"`
		StartedAt     string `json:"started_at" doc:"RFC 3339 UTC"`
		UptimeS       int64  `json:"uptime_s"`
		AuditFailures int64  `json:"audit_failures" doc:"Audit rows lost since start after retries; non-zero means a gap in the audit trail"`
	}
}

type empty struct{}

func registerSystem(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "ping", Method: "GET", Path: "/api/ping",
		Summary: "Ping the server", Description: "Liveness check that also reports the server version.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/"},
		func(ctx context.Context, _ *empty) (*pingOut, error) {
			out := &pingOut{}
			out.Body.OK, out.Body.Version = true, d.Version
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "whoami", Method: "GET", Path: "/api/me",
		Summary: "Show the calling principal", Description: "Returns the identity, scopes and authentication path of the caller.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/"},
		func(ctx context.Context, _ *empty) (*whoamiOut, error) {
			p := auth.FromContext(ctx)
			out := &whoamiOut{}
			out.Body.Kind, out.Body.Name, out.Body.Via = string(p.Kind), p.Name, p.Via
			out.Body.Scopes = make([]string, 0, len(p.Scopes))
			for _, s := range p.Scopes {
				out.Body.Scopes = append(out.Body.Scopes, string(s))
			}
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_system_info", Method: "GET", Path: "/api/system",
		Summary: "Get system information", Description: "Version, commit, auth mode, database schema version and uptime.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/settings"},
		func(ctx context.Context, _ *empty) (*systemOut, error) {
			out := &systemOut{}
			b := &out.Body
			b.Version, b.Commit, b.AuthMode = d.Version, d.Commit, d.Cfg.AuthMode
			if d.Store != nil {
				v, err := d.Store.UserVersion(ctx)
				if err != nil {
					return nil, err
				}
				b.SchemaVersion = v
			}
			now := d.clock()
			started := d.Started
			if started.IsZero() {
				started = now
			}
			b.StartedAt = started.UTC().Format(time.RFC3339)
			b.UptimeS = int64(now.Sub(started).Seconds())
			b.AuditFailures = audit.Failures()
			return out, nil
		})
}
