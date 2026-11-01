package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerAdherenceL3 registers get_adherence (C141.4): requested versus got, the absorbed M6 layer. Lane AD replaces the
// handler body only.
func registerAdherenceL3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_adherence", Method: "GET", Path: "/api/atlas/adherence",
		Summary:     "Get requested versus got",
		Description: "How closely rendered Songs matched the tempo (and duration) that was asked for: overall summary, aggregates per Sound facet value, and the Songs ordered by error. Uses each Song's Pick take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/atlas"},
		func(ctx context.Context, in *adherenceIn) (*adherenceOut, error) {
			if d.Atlas == nil {
				return nil, unavailable("atlas")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskAD)
		})
}
