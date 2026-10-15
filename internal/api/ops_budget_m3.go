package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func microPtr(v *int64) *money.MicroUSD {
	if v == nil {
		return nil
	}
	m := money.MicroUSD(*v)
	return &m
}

func int64Ptr(m *money.MicroUSD) (*int64, *string) {
	if m == nil {
		return nil, nil
	}
	v, s := int64(*m), m.USD()
	return &v, &s
}

func capsFrom(viewer auth.Principal, v generation.CapsView) CapsView {
	c := v.Caps
	out := CapsView{
		MonthlyMicroUSD: int64(c.MonthlyMicro), MonthlyUSD: usd(c.MonthlyMicro), BatchMicroUSD: int64(c.BatchMicro), BatchUSD: usd(c.BatchMicro),
		HumanDailyMicroUSD: int64(c.HumanDailyMicro), HumanDailyUSD: usd(c.HumanDailyMicro), AgentDailyMicroUSD: int64(c.AgentDailyMicro), AgentDailyUSD: usd(c.AgentDailyMicro),
		AutoApproveTakes: c.AutoApproveTakes, AutoApproveMicroUSD: int64(c.AutoApproveMicro), AutoApproveUSD: usd(c.AutoApproveMicro),
		UpdatedBy: refOf(viewer, v.UpdatedBy), UpdatedAt: store.FormatTime(v.UpdatedAt), Tokens: []CapToken{}, Connectors: []CapConnector{},
	}
	for _, t := range v.Tokens {
		ct := CapToken{TokenID: t.TokenID, Name: t.Name, AutoApproveTakes: t.Limits.AutoApproveTakes}
		ct.DailyBudgetMicroUSD, ct.DailyBudgetUSD = int64Ptr(t.Limits.DailyMicro)
		ct.AutoApproveMicroUSD, ct.AutoApproveUSD = int64Ptr(t.Limits.AutoApproveMicro)
		out.Tokens = append(out.Tokens, ct)
	}
	return out
}

func budgetFrom(viewer auth.Principal, v generation.BudgetView) Budget {
	m, d := v.Month, v.Day
	return Budget{
		Caps: capsFrom(viewer, v.Caps),
		Month: BudgetMonth{Start: store.FormatTime(m.Start), SettledMicroUSD: int64(m.Settled), SettledUSD: usd(m.Settled),
			BillAdjustmentsMicroUSD: int64(m.BillAdjustments), BillAdjustmentsUSD: usd(m.BillAdjustments), OutstandingMicroUSD: int64(m.Outstanding), OutstandingUSD: usd(m.Outstanding),
			CapMicroUSD: int64(m.Cap), CapUSD: usd(m.Cap), LeftMicroUSD: int64(m.Left), LeftUSD: usd(m.Left)},
		Day: BudgetDay{Principal: refOf(viewer, d.Principal), SettledMicroUSD: int64(d.Settled), SettledUSD: usd(d.Settled),
			OutstandingMicroUSD: int64(d.Outstanding), OutstandingUSD: usd(d.Outstanding), CapMicroUSD: int64(d.Cap), CapUSD: usd(d.Cap), LeftMicroUSD: int64(d.Left), LeftUSD: usd(d.Left)},
		AutoApprove: AutoApprove{Takes: v.AutoApproveTakes, MicroUSD: int64(v.AutoApprove), USD: usd(v.AutoApprove)},
		Balance:     Balance{Source: "unknown"},
		Provider:    providerBrief(v.Provider),
	}
}

func spendFrom(viewer auth.Principal, v generation.SpendView) Spend {
	out := Spend{Month: v.Month, Days: []SpendDay{}, Batches: []SpendBatch{}, Totals: SpendTotals{
		SettledMicroUSD: int64(v.Settled), SettledUSD: usd(v.Settled), BillAdjustmentsMicroUSD: int64(v.BillAdjustments), BillAdjustmentsUSD: usd(v.BillAdjustments),
		OutstandingMicroUSD: int64(v.Outstanding), OutstandingUSD: usd(v.Outstanding)}}
	for _, d := range v.Days {
		out.Days = append(out.Days, SpendDay{Date: d.Date, SettledMicroUSD: int64(d.Settled), SettledUSD: usd(d.Settled)})
	}
	for _, b := range v.Batches {
		out.Batches = append(out.Batches, SpendBatch{ID: b.ID, CreatedAt: store.FormatTime(b.CreatedAt), CreatedBy: refOf(viewer, b.CreatedBy), Takes: b.Takes, State: b.State,
			ExpectedMicroUSD: int64(b.Expected), ExpectedUSD: usd(b.Expected), MaxMicroUSD: int64(b.Max), MaxUSD: usd(b.Max),
			SettledMicroUSD: int64(b.Settled), SettledUSD: usd(b.Settled), Reconciled: b.Reconciled})
	}
	return out
}

func parseWhen(field, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", field+" must be RFC 3339", map[string]any{"field": field})
	}
	return &t, nil
}

// registerBudgetM3 registers the spend and cap operations (C81). Each handler checks its dependencies (503 UNAVAILABLE {component}).
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
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			p := auth.FromContext(ctx)
			v, err := d.Generation.Budget(ctx, p)
			if err != nil {
				return nil, genErr(err)
			}
			return &budgetOut{Body: budgetFrom(p, v)}, nil
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
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			v, err := d.Generation.SpendReport(ctx, in.Month)
			if err != nil {
				return nil, genErr(err)
			}
			return &spendOut{Body: spendFrom(auth.FromContext(ctx), v)}, nil
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
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			p := auth.FromContext(ctx)
			since, err := parseWhen("since", in.Since)
			if err != nil {
				return nil, err
			}
			until, err := parseWhen("until", in.Until)
			if err != nil {
				return nil, err
			}
			page, err := d.Generation.ListLedger(ctx, generation.LedgerQuery{BatchID: in.BatchID, Kinds: in.Kind, Since: since, Until: until, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, genErr(err)
			}
			out := LedgerPage{Items: []LedgerEntry{}}
			for _, r := range page.Items {
				out.Items = append(out.Items, LedgerEntry{ID: r.ID, At: store.FormatTime(r.At), Kind: r.Kind, MicroUSD: int64(r.Micro), USD: usd(r.Micro),
					BatchID: r.BatchID, JobID: r.JobID, Reason: r.Reason, Principal: refOf(p, r.Principal)})
			}
			if page.NextCursor != "" {
				out.NextCursor = &page.NextCursor
			}
			return &ledgerPageOut{Body: out}, nil
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
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			p := auth.FromContext(ctx)
			b := in.Body
			patch := spend.CapsPatch{MonthlyMicro: microPtr(b.MonthlyMicroUSD), BatchMicro: microPtr(b.BatchMicroUSD), HumanDailyMicro: microPtr(b.HumanDailyMicroUSD),
				AgentDailyMicro: microPtr(b.AgentDailyMicroUSD), AutoApproveTakes: b.AutoApproveTakes, AutoApproveMicro: microPtr(b.AutoApproveMicroUSD)}
			for _, t := range b.Tokens {
				patch.Tokens = append(patch.Tokens, spend.TokenPatch{TokenID: t.TokenID, DailyMicro: microPtr(t.DailyBudgetMicroUSD),
					AutoApproveTakes: t.AutoApproveTakes, AutoApproveMicro: microPtr(t.AutoApproveMicroUSD)})
			}
			v, err := d.Generation.SetCaps(ctx, p, patch)
			if err != nil {
				return nil, genErr(err)
			}
			return &capsOut{Body: capsFrom(p, v)}, nil
		})
}
