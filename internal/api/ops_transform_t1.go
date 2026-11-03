package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerTransformT1 registers the seven transform operations (C212.1). All are generic MCP operations (D-T1-21, operator
// decision of 2026-11-03: no dedicated tool, tools/list unchanged) and none is human-only. T1-F1 froze the Meta and the
// types; lane B4 replaces the handler bodies (each answers 501 NOT_IMPLEMENTED naming taskT1B4 until then).
func registerTransformT1(a huma.API, d Deps) {
	stub := func() error {
		if d.Transform == nil {
			return unavailable("transform")
		}
		return m2bErr(errors.ErrUnsupported, taskT1B4)
	}

	Register(a, withBody(huma.Operation{
		OperationID: "estimate_transform", Method: "POST", Path: "/api/transforms/estimate",
		Summary: "Estimate a transform",
		Description: "Free and pure: CPU time, output length and size, memory, the resolved tempo and key of each input, warnings, quota and queue position. " +
			"Costs $0 and writes nothing; it needs write scope because it previews quota and queue state of your principal kind.",
	}, bodyTransform), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *estimateTransformIn) (*transformEstimateOut, error) { return nil, stub() })

	Register(a, withBody(huma.Operation{
		OperationID: "create_transform", Method: "POST", Path: "/api/transforms",
		Summary: "Create a transform",
		Description: "Queues a CPU transform of one or two Takes (morph, shepard, glacier, polytempo, ensemble, score_render, master, widen, binaural). " +
			"The result is an ordinary Take. The same spec returns the existing transform (reused). Spends no money; limited by a daily CPU pool per principal kind.",
	}, bodyTransform), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *createTransformIn) (*createTransformOut, error) { return nil, stub() })

	Register(a, huma.Operation{
		OperationID: "get_transform", Method: "GET", Path: "/api/transforms/{id}",
		Summary:     "Get a transform",
		Description: "State, phase, progress, inputs, CPU use, warnings and error of one transform; its id is the id of its Take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *getTransformIn) (*transformOut, error) { return nil, stub() })

	Register(a, huma.Operation{
		OperationID: "list_transforms", Method: "GET", Path: "/api/transforms",
		Summary:     "List transforms",
		Description: "Transforms newest first, filtered by state, kind or an input Take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *listTransformsIn) (*transformPageOut, error) { return nil, stub() })

	Register(a, huma.Operation{
		OperationID: "wait_for_transform", Method: "GET", Path: "/api/transforms/{id}/wait",
		Summary:     "Wait for a transform",
		Description: "Long-polls up to 55 s until the state differs from since_state or the transform is terminal.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *waitForTransformIn) (*transformWaitOut, error) { return nil, stub() })

	Register(a, huma.Operation{
		OperationID: "cancel_transform", Method: "POST", Path: "/api/transforms/{id}/cancel",
		Summary:     "Cancel a transform",
		Description: "Cancels a queued or running transform; an agent cancels only its own, a human any.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/transforms"},
		func(ctx context.Context, in *cancelTransformIn) (*transformOut, error) { return nil, stub() })

	Register(a, huma.Operation{
		OperationID: "get_transform_capabilities", Method: "GET", Path: "/api/transforms/capabilities",
		Summary: "Get transform capabilities",
		Description: "The kinds with their parameter tables, defaults, ranges and blind spots, the limits, your daily CPU quota, the queue and the tools of the host. " +
			"Generated from the tables that validate requests.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *struct{}) (*transformCapabilitiesOut, error) { return nil, stub() })
}
