package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerAttentionM5 registers get_listen_heat (C256, C260). Frozen by M5-F2; lane M5-A1 fills the handler (and adds the id
// to implementedAttentionM5 in ops_attention_m5_test.go).
func registerAttentionM5(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_listen_heat", Method: "GET", Path: "/api/tracks/{id}/heat",
		Summary:     "Get the listening heat of a take",
		Description: "A 120-bin heat strip of how the operator and their devices listened to a take (heard, replays, rewinds, skips), up to three hotspots where people went back, and the bins they left most. Computed on read from listen events.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *getListenHeatIn) (*listenHeatOut, error) {
			if d.Attention == nil {
				return nil, unavailable("attention")
			}
			return nil, notImplemented(taskA1)
		})
}
