package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// fakeQuote is the price and endpoint of the fake backend before the reconciler has cached its Quote: the
// fake is always enabled (C68), so an estimate must work from the first request.
func fakeQuote() provider.Quote {
	idle := 20
	return provider.Quote{Backend: provider.BackendFake, ListPricePPH: 690_000, Pools: []string{"fake"},
		GPUClasses: []string{"A5000", "RTX 3090", "RTX 4090"}, Endpoint: provider.EndpointConfig{WorkersMax: 2, IdleTimeoutS: &idle}}
}

// quoteInputs resolves the provider quote of an estimate from local facts only.
func (s *Service) quoteInputs(ctx context.Context, q lineage.Querier, st provider.State) (spend.QuoteParams, provider.Quote, error) {
	pq := fakeQuote()
	switch {
	case st.Quote != nil:
		pq = *st.Quote
	case st.Mode != provider.ModeFake:
		return spend.QuoteParams{}, pq, disabledErr(provider.ReasonQuoteStale)
	}
	price := spend.EffectivePrice(pq, s.o.PriceMarginPPM)
	if price <= 0 {
		return spend.QuoteParams{}, pq, disabledErr(provider.ReasonCatalogUnreadable)
	}
	if pq.Endpoint.WorkersMax < 1 {
		return spend.QuoteParams{}, pq, disabledErr(provider.ReasonEndpointInactive)
	}
	if pq.Endpoint.IdleTimeoutS == nil {
		return spend.QuoteParams{}, pq, disabledErr(provider.ReasonEndpointConfig)
	}
	rtf, ok, err := s.o.Spend.RTFMilli(ctx, q, pq.GPUClasses)
	if err != nil {
		return spend.QuoteParams{}, pq, fmt.Errorf("generation: rtf: %w", err)
	}
	if !ok {
		rtf = spend.DefaultRTFMilli
	}
	return spend.QuoteParams{PricePPH: price, RTFMilli: rtf, ColdStartS: s.o.ColdStartS, IdleS: *pq.Endpoint.IdleTimeoutS,
		WorkersMax: pq.Endpoint.WorkersMax, MaxRuntimeS: int(s.o.MaxRuntime / time.Second), KillSlackS: s.o.KillSlackS}, pq, nil
}

func principalKind(p auth.Principal) string {
	if p.Kind == auth.KindAgent {
		return "agent"
	}
	return "human"
}

// Estimate expands items into takes, costs them and stores the frozen estimate (C71, C72.1, C72.2). It
// reads only local state: zero backend calls.
func (s *Service) Estimate(ctx context.Context, p auth.Principal, in EstimateIn) (Estimate, error) {
	if err := s.ready(); err != nil {
		return Estimate{}, err
	}
	name := in.Engine
	if name == "" {
		name = DefaultEngine
	}
	eng, ok := s.o.Engines[name]
	if !ok {
		return Estimate{}, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "unknown engine", map[string]any{"field": "engine"})
	}
	if len(in.Items) == 0 {
		return Estimate{}, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "items is required", map[string]any{"field": "items"})
	}
	if len(in.Items) > MaxItems {
		return Estimate{}, tooLarge(0, len(in.Items))
	}
	via := in.Via
	if via == "" {
		via = spend.ViaAPI
	}
	st, err := s.o.Gate.State(ctx)
	if err != nil {
		return Estimate{}, err
	}
	if !st.Enabled {
		return Estimate{}, disabledErr(st.Reason)
	}
	for i, it := range in.Items {
		if err := validateItem(i, it); err != nil {
			return Estimate{}, err
		}
	}
	total := 0
	for _, it := range in.Items {
		total += itemTakeCount(it)
	}
	if total > MaxTakes {
		return Estimate{}, tooLarge(total, 0)
	}
	if err := s.checkMothers(ctx, in.Items); err != nil {
		return Estimate{}, err
	}
	pl := &planner{s: s, ctx: ctx, q: s.o.Store.R, eng: eng, backend: st.Backend, ids: map[string]bool{}, planned: map[string][]int64{},
		renders: map[string]int{}, tracks: map[string]trackInfo{}, drafts: map[string]draftInfo{}, reused: map[string]bool{}}
	if err := pl.loadDrafts(in.Items); err != nil {
		return Estimate{}, err
	}
	if err := pl.expandAll(in.Items); err != nil {
		return Estimate{}, err
	}
	qp, pq, err := s.quoteInputs(ctx, s.o.Store.R, st)
	if err != nil {
		return Estimate{}, err
	}
	takes := make([]spend.TakeIn, len(pl.takes))
	for i, t := range pl.takes {
		takes[i] = spend.TakeIn{AudioS: t.ExpectedAudioS, Full: cotIsFull(t.RequestJSON)}
	}
	bq := spend.QuoteTakes(qp, takes)
	for i, t := range pl.takes {
		c := bq.Takes[i]
		t.ExpectedS, t.TimeoutS = c.ExpectedS, c.TimeoutS
		t.ExpectedMicroUSD, t.TakeMicroUSD, t.MaxMicroUSD = int64(c.ExpectedMicro), int64(c.TakeMicro), int64(c.MaxMicro)
		t.OverheadMicroUSD = int64(bq.AttemptOverheadMicro)
	}

	caps, err := s.o.Spend.LoadCaps(ctx, s.o.Store.R)
	if err != nil {
		return Estimate{}, err
	}
	tok, err := s.o.Spend.TokenLimitsFor(ctx, s.o.Store.R, p.Key())
	if err != nil {
		return Estimate{}, err
	}
	now := s.o.Now()
	u, err := s.o.Spend.Usage(ctx, s.o.Store.R, p.Key(), now)
	if err != nil {
		return Estimate{}, err
	}
	daily := spend.PrincipalDaily(p, tok, caps)
	fit := budgetFit(u, caps, daily, bq.CeilingMicro, bq.MaxMicro)
	need := spend.NeedsApproval(p, via, len(pl.takes), bq.CeilingMicro, tok, caps)

	token, err := s.o.Spend.NewToken()
	if err != nil {
		return Estimate{}, err
	}
	est := Estimate{
		ID: s.o.NewID(), Token: token, ExpiresAt: now.Add(TokenTTL), Engine: name, Backend: st.Backend, Takes: len(pl.takes),
		Cost: CostBlock{Expected: bq.ExpectedMicro, Max: bq.MaxMicro, RetryAllowance: bq.RetryAllowanceMicro, Ceiling: bq.CeilingMicro, AttemptOverhead: bq.AttemptOverheadMicro},
		ETAS: bq.ETAS, PricePerHour: qp.PricePPH, GPUPools: append([]string{}, pq.Pools...),
		RequiresApproval: need.Needed, ApprovalReasons: need.Reasons, Budget: fit, BatchCap: caps.BatchMicro, Provider: st,
		Month: pot(caps.MonthlyMicro, u.MonthSettled+u.Outstanding), Day: pot(daily, u.PrincipalDaySettled+u.PrincipalOutstanding),
	}
	if !pq.ObservedAt.IsZero() && pq.Backend != provider.BackendFake {
		t := pq.ObservedAt
		est.PriceObservedAt = &t
	}
	for i, t := range pl.takes {
		_ = i
		for _, w := range t.Warnings {
			est.Warnings = append(est.Warnings, Warning{TakeIndex: t.TakeIndex, Code: w.Code, Field: w.Field, Severity: w.Severity, Text: w.Message})
		}
	}
	for i, it := range in.Items {
		pi := PlannedItem{Index: i, Mode: it.Mode}
		for _, k := range pl.items[i] {
			t := pl.takes[k]
			pi.Takes = append(pi.Takes, TakePlan{TakeIndex: t.TakeIndex, RequestID: t.RequestID, Seed: t.Seed, Title: t.Title, Operator: t.Operator,
				ParentTrackID: t.ParentTrackID, DraftID: t.DraftID, ExpectedAudioS: t.ExpectedAudioS, TimeoutS: t.TimeoutS, Max: money.MicroUSD(t.MaxMicroUSD)})
		}
		est.Items = append(est.Items, pi)
	}
	if err := s.storeEstimate(ctx, p, est, pl.takes, qp, pq, bq, caps); err != nil {
		return Estimate{}, err
	}
	return est, nil
}

func pot(limit, used money.MicroUSD) Pot {
	return Pot{Cap: limit, Used: used, Left: max(limit-used, 0)}
}

// budgetFit says which caps a create would hit now, each checked on its own.
func budgetFit(u spend.Usage, c spend.Caps, daily, ceiling, reserve money.MicroUSD) CapFit {
	f := CapFit{Fits: true, WouldExceed: []string{}}
	if ceiling > c.BatchMicro {
		f.WouldExceed = append(f.WouldExceed, spend.CapBatch)
	}
	if u.MonthSettled+u.Outstanding+reserve > c.MonthlyMicro {
		f.WouldExceed = append(f.WouldExceed, spend.CapMonthly)
	}
	if u.PrincipalDaySettled+u.PrincipalOutstanding+reserve > daily {
		f.WouldExceed = append(f.WouldExceed, spend.CapDaily)
	}
	f.Fits = len(f.WouldExceed) == 0
	return f
}

func (s *Service) storeEstimate(ctx context.Context, p auth.Principal, est Estimate, takes []*planTake, qp spend.QuoteParams, pq provider.Quote, bq spend.BatchQuote, _ spend.Caps) error {
	items, err := json.Marshal(takes)
	if err != nil {
		return err
	}
	hashes := make([]string, len(takes))
	for i, t := range takes {
		hashes[i] = t.RecipeHash
	}
	rh, _ := json.Marshal(hashes)
	var observed *string
	if !pq.ObservedAt.IsZero() {
		v := store.FormatTime(pq.ObservedAt)
		observed = &v
	}
	qd := quoteDoc{PricePPH: int64(qp.PricePPH), ListPricePPH: int64(pq.ListPricePPH), MarginPPM: s.o.PriceMarginPPM, RTFMilli: qp.RTFMilli,
		ColdStartS: qp.ColdStartS, IdleS: qp.IdleS, WorkersMax: qp.WorkersMax, MaxRuntimeS: qp.MaxRuntimeS, KillSlackS: qp.KillSlackS,
		AttemptOverheadS: bq.AttemptOverheadS, ETAS: bq.ETAS, GPUPools: append([]string{}, pq.Pools...), PriceObservedAt: observed}
	qj, _ := json.Marshal(qd)
	reasons, _ := json.Marshal(est.ApprovalReasons)
	need := 0
	if est.RequiresApproval {
		need = 1
	}
	_, err = s.o.Store.W.ExecContext(ctx, `INSERT INTO estimates(id, principal_id, principal_kind, engine, backend, items_json, recipe_hashes, n_takes, quote_json,
		expected_micro_usd, max_micro_usd, retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, workers_max, idle_s,
		requires_approval, approval_reasons, token_hash, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		est.ID, p.Key(), principalKind(p), est.Engine, est.Backend, string(items), string(rh), len(takes), string(qj),
		int64(bq.ExpectedMicro), int64(bq.MaxMicro), int64(bq.RetryAllowanceMicro), int64(bq.CeilingMicro), int64(qp.PricePPH), qp.WorkersMax, qp.IdleS,
		need, string(reasons), s.o.Spend.HashToken(est.Token), store.FormatTime(s.o.Now()), store.FormatTime(est.ExpiresAt))
	if err != nil {
		return fmt.Errorf("generation: store estimate: %w", err)
	}
	return nil
}

// cotIsFull reports whether a canonical request asks the model to plan the ABC score itself (cot=full), the
// phase that adds planning time to the quote.
func cotIsFull(requestJSON string) bool {
	var probe struct {
		Cot string `json:"cot"`
	}
	return json.Unmarshal([]byte(requestJSON), &probe) == nil && probe.Cot == "full"
}
