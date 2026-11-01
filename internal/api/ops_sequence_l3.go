package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerSequenceL3 registers suggest_sequence (C138.1). Lane SR replaces the handler body only.
func registerSequenceL3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "suggest_sequence", Method: "GET", Path: "/api/sequence",
		Summary:     "Suggest a sequence",
		Description: "Previews the best order of a Record's slots, of 2 to 60 Songs, or of a Crate by tempo, key and energy (strategy arc, ramp_up or steady) with a per-step explanation. Read-only; nothing is stored.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *suggestSequenceIn) (*suggestionOut, error) {
			if d.Sequence == nil {
				return nil, unavailable("sequence")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskSR)
		})
}
