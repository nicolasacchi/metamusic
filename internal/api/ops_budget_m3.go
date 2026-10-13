package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerBudgetM3 registers the spend and cap operations (C81). Every handler is a STUB(M3-B4).
func registerBudgetM3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_budget", Method: "GET", Path: "/api/budget",
		Summary:     "Get what you may still spend",
		Description: "The caps, this month's and today's use, your auto-approve limits, the provider balance when readable, and whether generation is enabled.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *struct{}) (*budgetOut, error) {
			if d.Spend == nil {
				return nil, unavailable("spend")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, huma.Operation{
		OperationID: "get_spend", Method: "GET", Path: "/api/spend",
		Summary:     "Get a month's spend report",
		Description: "Settled spend per day and per batch for a month (default the current one), with bill adjustments and what is still outstanding.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *getSpendIn) (*spendOut, error) {
			if d.Spend == nil {
				return nil, unavailable("spend")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, huma.Operation{
		OperationID: "list_ledger", Method: "GET", Path: "/api/ledger",
		Summary:     "List ledger entries",
		Description: "The append-only money ledger (reserve, settle, residual, release, bill), newest first, filtered by batch, kind or time.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *listLedgerIn) (*ledgerPageOut, error) {
			if d.Spend == nil {
				return nil, unavailable("spend")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_spend_caps", Method: "PUT", Path: "/human/api/spend/caps",
		Summary:     "Set the spend caps",
		Description: "The operator changes the monthly, batch and daily caps and the auto-approve limits, globally or per agent token. At least one field; every change is recorded.",
	}, bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/spend"},
		func(ctx context.Context, in *setSpendCapsIn) (*capsOut, error) {
			if d.Spend == nil {
				return nil, unavailable("spend")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})
}
