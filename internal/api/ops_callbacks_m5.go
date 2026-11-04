package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerCallbacksM5 registers the three Callback Listening operations (C255, C260). Frozen by M5-F2; lane M5-C1 fills the
// handlers (and adds the ids to implementedCallbacksM5 in ops_callbacks_m5_test.go).
func registerCallbacksM5(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_callbacks", Method: "GET", Path: "/api/callbacks",
		Summary:     "List callbacks",
		Description: "Callbacks of Song PR children: pending ones first (oldest first), then answered ones. A callback asks the human whether a child fixed a comment, or kept what a keep comment praised, at the same musical spot.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/judge/callbacks"},
		func(ctx context.Context, in *listCallbacksIn) (*callbackPageOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			return nil, notImplemented(taskC1)
		})

	Register(a, huma.Operation{
		OperationID: "get_callback", Method: "GET", Path: "/api/callbacks/{id}",
		Summary:     "Get a callback",
		Description: "One callback with the comment, the child, the spot on the child and on the source take, and the verdicts the caller may see.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/judge/callbacks/:id"},
		func(ctx context.Context, in *getCallbackIn) (*callbackOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			return nil, notImplemented(taskC1)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "submit_callback_verdict", Method: "POST", Path: "/api/callbacks/{id}/verdicts",
		Summary:     "Answer a callback",
		Description: "fixed, not_yet or worse for a fix callback; kept or lost for a keep callback. A human answer moves the comment; an agent answer is a critic verdict that never does. Repeating an identical answer returns it with replayed true.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/judge/callbacks/:id"},
		func(ctx context.Context, in *submitCallbackVerdictIn) (*callbackSubmitOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			return nil, notImplemented(taskC1)
		})
}
