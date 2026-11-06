package generation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The E2 routing and spend hooks of the estimate and create paths (C151.3, D-E2-13, D-E2-15, D-E2-17b). Everything here is
// inert for yue2: a batch whose engine is yue2 takes the M3 gate and quote untouched.

const reasonNoRTF = "no_rtf"

// BatchHook is called inside the create transaction after a batch's rows are written (C158.2: the first create under an engine
// trial flips it to rendering). generation never imports enginetrial; the app injects the implementation.
type BatchHook interface {
	OnBatchCreated(ctx context.Context, tx *sql.Tx, trialID, batchID string) error
}

// UseBatchHook wires the create-transaction hook; nil (the default) turns it off.
func (s *Service) UseBatchHook(h BatchHook) { s.e2.batchHook = h }

func backendUnavailable(engineName, backend string) error {
	return perr(http.StatusUnprocessableEntity, "BACKEND_UNAVAILABLE", "no enabled backend serves this engine",
		map[string]any{"engine": engineName, "backend": backend})
}

// disabledBackendErr is GENERATION_DISABLED for the routed gate; a non-yue2 engine names its backend in the details.
func disabledBackendErr(engineName string, st provider.State) error {
	if engineName == DefaultEngine {
		return disabledErr(st.Reason)
	}
	return perr(http.StatusServiceUnavailable, "GENERATION_DISABLED", "generation is not enabled: "+st.Reason,
		map[string]any{"reason": st.Reason, "backend": st.Backend})
}

// ---- the engine of a batch (D-E2-13) ----

// parentEngine is the engine of a take's recipe, "" for an unknown track and for takes that are no engine's render (the
// reference-track stub and transforms; checkMothers refuses them with their own errors).
func (s *Service) parentEngine(ctx context.Context, id string) (string, error) {
	var e string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT r.engine FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("generation: parent engine: %w", err)
	}
	if _, ok := s.o.Engines[e]; !ok {
		return "", nil
	}
	return e, nil
}

func (s *Service) unknownEngine(engineName string) error {
	return perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "unknown engine", map[string]any{"field": "engine", "engine": engineName})
}

func oneEnginePerBatch(engines []string) error {
	return perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "an estimate renders with one engine", map[string]any{
		"field": "items", "rule": "one_engine_per_batch", "engines": engines})
}

// itemEngine derives the engine of one item: the mode's (port and the edit modes are acestep15, from_draft is yue2), else the
// parent's, else the item's own, else the body's engine, else yue2.
func (s *Service) itemEngine(ctx context.Context, i int, it Item, body string) (string, error) {
	explicit := it.Engine
	if explicit != "" {
		if _, ok := s.o.Engines[explicit]; !ok {
			return "", s.unknownEngine(explicit)
		}
	}
	switch {
	case it.Mode == lineage.ModeFromDraft:
		if explicit != "" && explicit != DefaultEngine {
			return "", engineCapabilityErr(explicit, it.Mode, "mode")
		}
		return DefaultEngine, nil
	case isE2Mode(it.Mode):
		if explicit != "" && explicit != acestep.Name {
			return "", engineCapabilityErr(explicit, it.Mode, "mode")
		}
		return acestep.Name, nil
	}
	var parents []string
	for _, id := range mothers(it) {
		e, err := s.parentEngine(ctx, id)
		if err != nil {
			return "", err
		}
		if e != "" && !slices.Contains(parents, e) {
			parents = append(parents, e)
		}
	}
	switch {
	case len(parents) > 1:
		return "", oneEnginePerBatch(parents)
	case len(parents) == 1:
		if explicit != "" && explicit != parents[0] {
			return "", invalidItem(i, "engine", "parent_engine_mismatch")
		}
		return parents[0], nil
	case explicit != "":
		return explicit, nil
	case body != "":
		return body, nil
	}
	return DefaultEngine, nil
}

// batchEngine is the one engine of an estimate. Registries with yue2 only skip the derivation, so a yue2-only deployment
// reads nothing extra.
func (s *Service) batchEngine(ctx context.Context, in EstimateIn, name string) (string, error) {
	if len(s.o.Engines) <= 1 && in.Engine == "" {
		return name, nil
	}
	var engines []string
	for i, it := range in.Items {
		e, err := s.itemEngine(ctx, i, it, in.Engine)
		if err != nil {
			return "", err
		}
		if !slices.Contains(engines, e) {
			engines = append(engines, e)
		}
	}
	if in.Engine != "" && !slices.Contains(engines, in.Engine) {
		engines = append(engines, in.Engine)
	}
	if len(engines) > 1 {
		return "", oneEnginePerBatch(engines)
	}
	return engines[0], nil
}

// ---- adoption and routing ----

// admit refuses an engine that has not been adopted with 409 ENGINE_NOT_ADOPTED (D-E2-15); yue2 is always adopted. Without
// an AdoptionGate every other engine is refused: money never flows to an engine nobody could vouch for.
func (s *Service) admit(ctx context.Context, q QueryReader, engineName, trialID string) error {
	if engineName == DefaultEngine {
		if trialID != "" {
			return perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "an engine trial names a candidate engine", map[string]any{
				"field": "options.trial_id", "rule": "trial_engine_mismatch"})
		}
		return nil
	}
	if s.e2.adoption == nil {
		return perr(http.StatusConflict, "ENGINE_NOT_ADOPTED", "the engine has not been adopted", map[string]any{"engine": engineName, "hint": "propose_engine_trial"})
	}
	return s.e2.adoption.Admitted(ctx, q, engineName, trialID)
}

// routeGate resolves the backend that serves engineName and its gate state from local facts. yue2 uses the M3 gate; every
// other engine needs the backend set and the gate set (C151.3).
func (s *Service) routeGate(ctx context.Context, engineName, requested string) (provider.State, provider.Backend, error) {
	if engineName == DefaultEngine {
		st, err := s.o.Gate.State(ctx)
		if err != nil {
			return st, nil, err
		}
		if requested != "" {
			if s.e2.backends != nil {
				if _, err := s.e2.backends.Route(DefaultEngine, requested); err != nil {
					return st, nil, backendUnavailable(engineName, requested)
				}
			} else if requested != st.Backend {
				return st, nil, backendUnavailable(engineName, requested)
			}
		}
		return st, nil, nil
	}
	if s.e2.backends == nil || s.e2.gates == nil {
		return provider.State{}, nil, backendUnavailable(engineName, requested)
	}
	name, err := s.e2.backends.Route(engineName, requested)
	if err != nil {
		return provider.State{}, nil, backendUnavailable(engineName, requested)
	}
	be, ok := s.e2.backends.Get(name)
	if !ok {
		return provider.State{}, nil, backendUnavailable(engineName, requested)
	}
	st, err := s.e2.gates.StateFor(ctx, name)
	if errors.Is(err, provider.ErrNoBackend) {
		return st, nil, backendUnavailable(engineName, requested)
	}
	return st, be, err
}

// ---- the quote of a non-yue2 batch (C152) ----

// quoteACE builds the quote inputs of an ACE-Step batch: the routed backend's cold start and maximum runtime, and one TakeIn
// per take with the RTF and load seconds of its task class (engine_rtf row, else the committed seed; neither is 503
// GENERATION_DISABLED reason no_rtf). It also adds the base-class cost warning.
func (s *Service) quoteACE(ctx context.Context, pl *planner, st provider.State, be provider.Backend) (spend.QuoteParams, provider.Quote, []spend.TakeIn, error) {
	cold, maxRun := s.o.ColdStartS, int(s.o.MaxRuntime.Seconds())
	if be != nil {
		cold, maxRun = be.ColdStartS(), int(be.MaxRuntime().Seconds())
	}
	qp, pq, err := s.quoteBase(st, fakeACEQuote(), cold, maxRun)
	if err != nil {
		return qp, pq, nil, err
	}
	type cell struct{ rtf, load int }
	cells := map[string]cell{}
	lookup := func(class string) (cell, error) {
		if c, ok := cells[class]; ok {
			return c, nil
		}
		rtf, load, ok, err := s.o.Spend.EngineRTF(ctx, s.o.Store.R, pl.engName, class, pq.GPUClasses)
		if err != nil {
			return cell{}, fmt.Errorf("generation: engine rtf: %w", err)
		}
		if !ok {
			return cell{}, perr(http.StatusServiceUnavailable, "GENERATION_DISABLED", "generation is not enabled: "+reasonNoRTF,
				map[string]any{"reason": reasonNoRTF, "backend": st.Backend, "task_class": class})
		}
		cells[class] = cell{rtf, load}
		return cells[class], nil
	}
	feat, _ := aceFeatures(pl.eng)
	takes := make([]spend.TakeIn, len(pl.takes))
	for i, t := range pl.takes {
		e := pl.e2[i]
		c, err := lookup(e.Class)
		if err != nil {
			return qp, pq, nil, err
		}
		load := c.load
		if e.LRC {
			load += feat.LRCFixedS
		}
		takes[i] = spend.TakeIn{AudioS: t.ExpectedAudioS, RTFMilli: c.rtf, LoadS: load}
		if qp.RTFMilli == 0 {
			qp.RTFMilli = c.rtf
		}
		if e.Class == acestep.ClassBase {
			if turbo, err := lookup(acestep.ClassTurbo); err == nil {
				t.Warnings = append(t.Warnings, warnIssue(WarnBaseClassCost, "",
					fmt.Sprintf("a base-class take: about %s times the GPU time of a turbo take of the same length", ratio(
						acestep.GenSeconds(t.ExpectedAudioS, c.rtf, load), acestep.GenSeconds(t.ExpectedAudioS, turbo.rtf, turbo.load)))))
			}
		}
	}
	return qp, pq, takes, nil
}

// ratio prints num/den with one decimal, integers only.
func ratio(num, den int) string {
	if den <= 0 {
		return "several"
	}
	t := (num*10 + den/2) / den
	return fmt.Sprintf("%d.%d", t/10, t%10)
}

// refuseOverRuntime refuses, rather than clamps, a take whose unclamped timeout exceeds the backend's maximum runtime
// (D-E2-17b): 422 INVALID_ITEM rule exceeds_max_runtime {expected_s, max_runtime_s, fix}. yue2 keeps M3's silent clamp.
func (p *planner) refuseOverRuntime(takes []spend.TakeIn, bq spend.BatchQuote, maxRuntimeS int) error {
	for i, t := range p.takes {
		in := takes[i]
		if issue := acestep.CheckRuntime(in.AudioS, in.RTFMilli, in.LoadS, maxRuntimeS); issue != nil {
			gen := acestep.GenSeconds(in.AudioS, in.RTFMilli, in.LoadS)
			return perr(http.StatusUnprocessableEntity, "INVALID_ITEM", "the item is not valid: exceeds_max_runtime", map[string]any{
				"item": t.ItemIndex, "field": issue.Field, "rule": "exceeds_max_runtime", "expected_s": bq.Takes[i].ExpectedS,
				"timeout_s": acestep.UnclampedTimeoutS(gen), "max_runtime_s": maxRuntimeS, "fix": "MM_ACESTEP_MAX_RUNTIME"})
		}
	}
	return nil
}

// ---- create ----

// trialOf is the trial id of a stored estimate: every take carries it in its params (C158.2).
func trialOf(items []planTake) string {
	for _, t := range items {
		if t.Params.TrialID != "" {
			return t.Params.TrialID
		}
	}
	return ""
}

// preflightE2 re-renders the reference clips an estimate pinned and compares their digests (C149.3) before the create
// transaction opens: a render can take seconds and must not hold the single writer. A different digest is 409 ESTIMATE_STALE
// reference_changed. It reads the estimate by token without consuming it; yue2 estimates (and services without E2 seams) return
// at once; a service with E2 seams but no renderer refuses an estimate that pinned a reference (fail closed).
func (s *Service) preflightE2(ctx context.Context, tokenHash, principalKey string) error {
	if s.e2.refClips == nil && s.e2.backends == nil && s.e2.gates == nil && s.e2.adoption == nil {
		return nil // an M3 service: no E2 seam is wired
	}
	var itemsJSON, engineName string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT engine, items_json FROM estimates WHERE token_hash = ? AND principal_id = ?`, tokenHash, principalKey).Scan(&engineName, &itemsJSON)
	if errors.Is(err, sql.ErrNoRows) || engineName == DefaultEngine {
		return nil // an unknown token is the transaction's to refuse
	}
	if err != nil {
		return err
	}
	var e []planTake
	if err := json.Unmarshal([]byte(itemsJSON), &e); err != nil {
		return fmt.Errorf("generation: estimate items: %w", err)
	}
	seen := map[string]bool{}
	for _, t := range e {
		if t.Ref == nil || seen[t.Ref.ClipSHA256+t.Ref.TrackID] {
			continue
		}
		seen[t.Ref.ClipSHA256+t.Ref.TrackID] = true
		if err := s.ensureRef(ctx, *t.Ref); err != nil {
			return err
		}
	}
	return nil
}

// admitCreate re-checks adoption inside the create transaction: the engine may have been rejected since the estimate.
func (s *Service) admitCreate(ctx context.Context, tx *sql.Tx, e estimateRow) error {
	if e.Engine == DefaultEngine {
		return nil
	}
	return s.admit(ctx, tx, e.Engine, trialOf(e.Items))
}

// batchCreated runs the injected hook for a batch created under an engine trial.
func (s *Service) batchCreated(ctx context.Context, tx *sql.Tx, e estimateRow, batchID string) error {
	if s.e2.batchHook == nil {
		return nil
	}
	trial := trialOf(e.Items)
	if trial == "" {
		return nil
	}
	return s.e2.batchHook.OnBatchCreated(ctx, tx, trial, batchID)
}

// createGate is the gate state create reads inside its transaction: the M3 gate for yue2 (and for a token no estimate owns,
// which ConsumeToken refuses), the estimate's own backend's gate for any other engine.
func (s *Service) createGate(ctx context.Context, tx *sql.Tx, engineName, backend string) (provider.State, error) {
	if engineName == "" || engineName == DefaultEngine {
		return s.o.Gate.StateIn(ctx, tx)
	}
	if s.e2.gates == nil {
		return provider.State{}, staleErr(map[string]any{"reason": "backend_changed"})
	}
	st, err := s.e2.gates.StateIn(ctx, tx, backend)
	if errors.Is(err, provider.ErrNoBackend) {
		return st, staleErr(map[string]any{"reason": "backend_changed"}) // the backend left the set
	}
	return st, err
}

// gateErrFor is gateErr; a refusal of a non-yue2 backend names it in the details.
func gateErrFor(engineName string, st provider.State) error {
	err := gateErr(st)
	var pe *problem.Error
	if engineName != "" && engineName != DefaultEngine && errors.As(err, &pe) {
		if pe.Details == nil {
			pe.Details = map[string]any{}
		}
		pe.Details["backend"] = st.Backend
	}
	return err
}
