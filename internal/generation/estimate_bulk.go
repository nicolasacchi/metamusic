package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// Pod estimates (BULK-P6, C398.1, C393.1). Everything here is inert unless the estimate is routed to a pod backend, which only
// options.backend = "runpod-pod" (or the canary option) does: every other estimate takes the code path it always took.

// PodCanarySelfTerminate is the one value of options.pod_canary (C399.2).
const PodCanarySelfTerminate = "self_terminate"

// fakePodQuote is the price and endpoint of the simulated pod backend before the reconciler has cached its quote.
func fakePodQuote(backend string) provider.Quote {
	return provider.Quote{Backend: backend, ListPricePPH: 690_000, Pools: []string{}, GPUClasses: []string{"fake-pod-gpu"},
		Endpoint: provider.EndpointConfig{WorkersMax: 1, GPUCount: 1}}
}

// podDoc is quoteDoc.Pod: the frozen pod terms of the quote (omitted, so every other estimate is byte-identical). Canary is what the
// lifecycle loop reads (quote_json.$.pod.canary): nothing else makes a batch the canary.
type podDoc struct {
	BootS       int      `json:"boot_s"`
	IdleS       int      `json:"idle_s"`
	SlackS      int      `json:"slack_s"`
	OverrunS    int      `json:"overrun_s"`
	DeadlineS   int      `json:"deadline_s"`
	MaxRecreate int      `json:"max_recreate"`
	GPUTypes    []string `json:"gpu_types"`
	Canary      bool     `json:"canary,omitempty"`
}

// podColumnIdleS is what a pod batch freezes in idle_s, the smallest value the schema allows.
const podColumnIdleS = 1

// PodBlock is the pod part of an estimate: the boot allowance and the absolute batch deadline, both in seconds.
type PodBlock struct {
	BootS, DeadlineS int
}

// podEstimate is the state of one pod estimate between the routing and the stored quote.
type podEstimate struct {
	cfg     PodConfig
	canary  bool
	backend string
	be      provider.Backend
	doc     podDoc
	quote   spend.PodQuote
}

func podHumanOnly() error {
	return perr(http.StatusForbidden, "HUMAN_ONLY", "a pod batch is made by the Owner in person", map[string]any{"reason": "bulk"})
}

func podOwnerOnly() error {
	e := problem.OwnerOnly("a pod batch")
	e.Details = map[string]any{"reason": "bulk"}
	return e
}

// podWhoMay is D-BULK-13: the Owner's own session, and nobody else (agents, tokens including a mandate or night-shift token, connectors and
// devices: HUMAN_ONLY; Editors and Listeners: OWNER_ONLY), except that a token may ask for the canary, which always waits for the Owner.
func podWhoMay(p auth.Principal, canary bool) error {
	switch {
	case p.IsOwner(), canary && p.IsAgent():
		return nil
	case !p.IsOwnerSide():
		return podOwnerOnly()
	}
	return podHumanOnly()
}

// podBackendFor is the backend name an estimate asks the set for. A plain estimate names its own (options.backend); the canary option
// names a pod backend itself: the one the set holds, runpod-pod first (the real one), else fake-pod. An unknown option value, or a canary
// pinned to a serverless backend, is 422 VALIDATION_FAILED.
func (s *Service) podBackendFor(engineName string, in EstimateIn) (string, error) {
	if in.PodCanary == "" {
		return in.Backend, nil
	}
	if in.PodCanary != PodCanarySelfTerminate {
		return "", perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "unknown pod canary", map[string]any{"field": "options.pod_canary"})
	}
	if in.Backend != "" {
		if !provider.IsPodBackend(in.Backend) {
			return "", perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "the pod canary runs on a pod backend", map[string]any{
				"field": "options.backend", "rule": "pod_canary_backend"})
		}
		return in.Backend, nil
	}
	if s.e2.backends != nil {
		for _, name := range []string{provider.BackendRunPodPod, provider.BackendFakePod} {
			if _, err := s.e2.backends.Route(engineName, name); err == nil {
				return name, nil
			}
		}
	}
	return provider.BackendRunPodPod, nil // not in the set: routeGate answers BACKEND_UNAVAILABLE
}

// podStart applies the rules that need no plan (C398.1): who may, no mandate, no analyze or plan items, the safety gate. It returns nil
// for an estimate that is not routed to a pod backend. Without a pod configuration a pod estimate is 503 UNAVAILABLE.
func (s *Service) podStart(ctx context.Context, p auth.Principal, in EstimateIn, st provider.State, be provider.Backend) (*podEstimate, error) {
	if !provider.IsPodBackend(st.Backend) {
		return nil, nil
	}
	canary := in.PodCanary != ""
	if err := podWhoMay(p, canary); err != nil {
		return nil, err
	}
	if s.pods == nil {
		return nil, perr(http.StatusServiceUnavailable, "UNAVAILABLE", "the pod backend is not available", map[string]any{"component": "pods"})
	}
	if in.MandateID != "" {
		return nil, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a spending mandate never covers a pod batch", map[string]any{
			"field": "mandate", "rule": "mandate_not_on_pods"})
	}
	for _, it := range in.Items {
		if it.Mode == lineage.ModeAnalyze || it.Mode == lineage.ModeRenderPlan {
			return nil, podKindUnavailable(st.Backend)
		}
	}
	if s.pods.RequireCanary && !canary {
		verdict, err := PodSafety(ctx, s.o.Store.R, provider.QuoteImageDigest(st.Quote, s.o.Now()))
		if err != nil {
			return nil, err
		}
		if verdict != PodSafetyVerified {
			return nil, perr(http.StatusServiceUnavailable, "GENERATION_DISABLED", "generation is not enabled: "+ReasonPodSafetyUnverified,
				map[string]any{"reason": ReasonPodSafetyUnverified, "backend": st.Backend})
		}
	}
	return &podEstimate{cfg: *s.pods, canary: canary, backend: st.Backend, be: be}, nil
}

func podKindUnavailable(backend string) error {
	return perr(http.StatusUnprocessableEntity, "BACKEND_UNAVAILABLE", "the pod backend renders only generate items", map[string]any{
		"engine": DefaultEngine, "backend": backend, "reason": "kind"})
}

// takeLimit refuses a take count the batch's backend cannot take: the 50-take limit of every other backend, or the pod window of an
// ordinary pod batch (MM_POD_MIN_TAKES..MM_POD_MAX_TAKES) or the one take of the canary.
func (pe *podEstimate) takeLimit(total int) error {
	switch {
	case pe == nil:
		if total > MaxTakes {
			return tooLarge(total, 0)
		}
	case pe.canary:
		if total != 1 {
			return perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "the pod canary is one take", map[string]any{
				"field": "items", "rule": "pod_canary_one_take", "takes": total})
		}
	case total < pe.cfg.MinTakes:
		return perr(http.StatusUnprocessableEntity, "BULK_TOO_SMALL", fmt.Sprintf("a pod batch holds at least %d takes", pe.cfg.MinTakes),
			map[string]any{"takes": total, "minimum": pe.cfg.MinTakes})
	case total > pe.cfg.MaxTakes:
		return perr(http.StatusUnprocessableEntity, "BATCH_TOO_LARGE", fmt.Sprintf("a pod batch holds at most %d takes", pe.cfg.MaxTakes),
			map[string]any{"takes": total, "limit": pe.cfg.MaxTakes})
	}
	return nil
}

// plan checks the expanded takes: only generate jobs run on a pod, and each take's needs gain the agent capability (amendment A2) so the
// capability gate and the claim filter apply to the pod backend's image.
func (pe *podEstimate) plan(takes []*planTake) error {
	for _, t := range takes {
		if t.Kind != "" && t.Kind != jobspec.KindGenerate {
			return podKindUnavailable(pe.backend)
		}
	}
	for _, t := range takes {
		t.Needs = jobspec.WithCaps(t.Needs, jobspec.CapAgent)
	}
	return pe.takeLimit(len(takes))
}

// podQuoteInputs resolves the pod's quote inputs from local facts: the price is the dearest configured card (the backend's quote) with
// the margin, one worker, no cold start or idle (those are pod terms). The RTF is learned as for any yue2 estimate.
func (s *Service) podQuoteInputs(ctx context.Context, pl *planner, st provider.State, pe *podEstimate, ears earsPlan) (spend.QuoteParams, provider.Quote, []spend.TakeIn, error) {
	pq := fakePodQuote(st.Backend)
	switch {
	case st.Quote != nil:
		pq = *st.Quote
	case st.Mode != provider.ModeFake:
		return spend.QuoteParams{}, pq, nil, disabledErr(provider.ReasonQuoteStale)
	}
	price := spend.EffectivePrice(pq, s.o.PriceMarginPPM)
	if price <= 0 {
		return spend.QuoteParams{}, pq, nil, disabledErr(provider.ReasonCatalogUnreadable)
	}
	rtf, ok, err := s.o.Spend.RTFMilli(ctx, s.o.Store.R, pq.GPUClasses)
	if err != nil {
		return spend.QuoteParams{}, pq, nil, fmt.Errorf("generation: rtf: %w", err)
	}
	if !ok {
		rtf = spend.DefaultRTFMilli
	}
	maxRun := s.o.MaxRuntime
	if pe.be != nil {
		maxRun = pe.be.MaxRuntime()
	}
	// IdleS is the column floor: estimates.idle_s and batches.idle_s are CHECK >= 1, so a pod batch freezes 1 (the plan said 0; the pod's own
	// idle window is quote_json.pod.idle_s). QuotePod ignores it: boot and idle are pod terms.
	qp := spend.QuoteParams{PricePPH: price, RTFMilli: rtf, WorkersMax: 1, IdleS: podColumnIdleS, MaxRuntimeS: int(maxRun / time.Second), KillSlackS: s.o.KillSlackS}
	takes := make([]spend.TakeIn, len(pl.takes))
	for i, t := range pl.takes {
		takes[i] = pl.quoteTakeIn(i, t, ears)
	}
	return qp, pq, takes, nil
}

// price quotes the batch with QuotePod (the canary: one take, no re-creation) and freezes the pod terms. ErrPodRuntime is 422
// BATCH_TOO_LARGE reason pod_runtime.
func (pe *podEstimate) price(qp spend.QuoteParams, pq provider.Quote, takes []spend.TakeIn) (spend.BatchQuote, error) {
	recreate := pe.cfg.MaxRecreate
	if pe.canary {
		recreate = 0
	}
	q, err := spend.QuotePod(spend.PodQuoteParams{Base: qp, BootS: pe.cfg.BootS, IdleS: pe.cfg.IdleS, SlackS: pe.cfg.SlackS,
		OverrunS: spend.PodOverrunGraceS + spend.PodPassS, MaxRuntimeS: pe.cfg.MaxRuntimeS, MaxRecreate: recreate}, takes)
	if errors.Is(err, spend.ErrPodRuntime) {
		return spend.BatchQuote{}, perr(http.StatusUnprocessableEntity, "BATCH_TOO_LARGE", "the batch does not fit the pod runtime limit", map[string]any{
			"reason": "pod_runtime", "max_runtime_s": pe.cfg.MaxRuntimeS})
	}
	if err != nil {
		return spend.BatchQuote{}, err
	}
	pe.quote = q
	types := pe.cfg.GPUTypes
	if len(types) == 0 {
		types = append([]string{}, pq.GPUClasses...)
	}
	pe.doc = podDoc{BootS: pe.cfg.BootS, IdleS: pe.cfg.IdleS, SlackS: pe.cfg.SlackS, OverrunS: spend.PodOverrunGraceS + spend.PodPassS,
		DeadlineS: q.DeadlineS, MaxRecreate: recreate, GPUTypes: slices.Clone(types), Canary: pe.canary}
	return q.Batch, nil
}

// approval is the approval need of the batch: a canary always waits for the Owner, whatever the limits and whoever asks.
func (pe *podEstimate) approval(n spend.ApprovalNeed) spend.ApprovalNeed {
	if pe != nil && pe.canary {
		return spend.PodCanaryNeed(n)
	}
	return n
}

// block is the estimate's pod block (nil for every other estimate).
func (pe *podEstimate) block() *PodBlock {
	if pe == nil {
		return nil
	}
	return &PodBlock{BootS: pe.doc.BootS, DeadlineS: pe.doc.DeadlineS}
}

// docOrNil is quoteDoc.Pod.
func (pe *podEstimate) docOrNil() *podDoc {
	if pe == nil {
		return nil
	}
	d := pe.doc
	return &d
}

// storedPodNeed is the create-time twin of approval: a stored canary estimate always creates a batch that waits for the Owner, and
// is never covered by a mandate.
func storedPodNeed(q quoteDoc, n spend.ApprovalNeed) spend.ApprovalNeed {
	if q.Pod != nil && q.Pod.Canary {
		return spend.PodCanaryNeed(n)
	}
	return n
}
