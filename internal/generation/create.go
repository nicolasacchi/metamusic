package generation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// estimateRow is what create needs of a stored estimate.
type estimateRow struct {
	ID, Backend, Engine, ItemsJSON, QuoteJSON, ApprovalReasons string
	Takes                                                      int
	Expected, Max, Retry, Ceiling, PricePPH                    int64
	WorkersMax, IdleS                                          int
	RequiresApproval                                           bool
	Quote                                                      quoteDoc
	Items                                                      []planTake
}

func loadEstimate(ctx context.Context, q lineage.Querier, id string) (estimateRow, error) {
	e := estimateRow{ID: id}
	var need int
	err := q.QueryRowContext(ctx, `SELECT backend, engine, items_json, quote_json, n_takes, expected_micro_usd, max_micro_usd, retry_allowance_micro_usd,
		ceiling_micro_usd, price_pph_micro_usd, workers_max, idle_s, requires_approval FROM estimates WHERE id = ?`, id).
		Scan(&e.Backend, &e.Engine, &e.ItemsJSON, &e.QuoteJSON, &e.Takes, &e.Expected, &e.Max, &e.Retry, &e.Ceiling, &e.PricePPH, &e.WorkersMax, &e.IdleS, &need)
	if err != nil {
		return e, fmt.Errorf("generation: load estimate: %w", err)
	}
	e.RequiresApproval = need == 1
	if err := json.Unmarshal([]byte(e.ItemsJSON), &e.Items); err != nil {
		return e, fmt.Errorf("generation: estimate items: %w", err)
	}
	if err := json.Unmarshal([]byte(e.QuoteJSON), &e.Quote); err != nil {
		return e, fmt.Errorf("generation: estimate quote: %w", err)
	}
	return e, nil
}

func tokenErr(err error) error {
	var te *spend.TokenError
	if !errors.As(err, &te) {
		return err
	}
	switch te.Reason {
	case spend.TokenExpired:
		return perr(http.StatusGone, "CONFIRMATION_EXPIRED", "the confirmation token has expired; estimate again", nil)
	case spend.TokenConsumed:
		d := map[string]any{"reason": "consumed"}
		if te.BatchID != "" {
			d["batch_id"] = te.BatchID
		}
		return perr(http.StatusConflict, "CONFIRMATION_INVALID", "the confirmation token was already used", d)
	}
	return perr(http.StatusConflict, "CONFIRMATION_INVALID", "the confirmation token is not valid", map[string]any{"reason": "unknown"})
}

func capErr(ce *spend.CapError) error {
	return perr(http.StatusPaymentRequired, "BUDGET_EXCEEDED", "this would exceed the "+ce.Cap+" spend cap", ce.Details())
}

// gateErr turns the local provider state into the refusal create answers (C72.4 step 1).
func gateErr(st provider.State) error {
	switch {
	case !st.Enabled:
		return disabledErr(st.Reason)
	case !st.Armed:
		return perr(http.StatusServiceUnavailable, "PROVIDER_DISARMED", "the provider is disarmed; the operator must arm it", nil)
	case st.Tripped:
		return perr(http.StatusServiceUnavailable, "PROVIDER_TRIPPED", "the provider is tripped; the operator must reset it", map[string]any{"trip_id": st.TripID, "reason": st.TripReason})
	}
	return nil
}

// Create runs the create transaction (C72.4). encode renders the 201 body from the batch read back inside
// the transaction; it is called before commit so the idempotency row stores the exact bytes.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateIn, encode func(Created) (status int, body []byte, err error)) (created Created, err error) {
	if err = s.ready(); err != nil {
		return Created{}, err
	}
	via := in.Via
	if via != spend.ViaHumanCreate || p.Kind != auth.KindHuman {
		via = spend.ViaAPI
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return Created{}, err
	}
	defer func() {
		if err != nil {
			_ = s.o.Spend.RollbackTx(tx)
			idem.MarksFrom(ctx).Reset()
		}
	}()

	// 1. provider gate: local facts only, read through the transaction
	st, err := s.o.Gate.StateIn(ctx, tx)
	if err != nil {
		return Created{}, err
	}
	if err = gateErr(st); err != nil {
		return Created{}, err
	}
	hash := s.o.Spend.HashToken(in.Token)
	var peekBackend string
	switch e := tx.QueryRowContext(ctx, `SELECT backend FROM estimates WHERE token_hash = ? AND principal_id = ?`, hash, p.Key()).Scan(&peekBackend); {
	case e == nil && peekBackend != st.Backend:
		err = staleErr(map[string]any{"reason": "backend_changed"})
		return Created{}, err
	case e != nil && !errors.Is(e, sql.ErrNoRows):
		err = e
		return Created{}, err
	}

	// 2-3. the token: validate and consume in this transaction
	batchID := s.o.NewID()
	estID, err := s.o.Spend.ConsumeToken(ctx, tx, in.Token, p.Key(), batchID)
	if err != nil {
		err = tokenErr(err)
		return Created{}, err
	}
	est, err := loadEstimate(ctx, tx, estID)
	if err != nil {
		return Created{}, err
	}
	if in.VariantTrackID != nil && !variantShape(est, *in.VariantTrackID) {
		err = perr(http.StatusConflict, "CONFIRMATION_INVALID", "the confirmation token is not for a variant estimate of this track", map[string]any{"reason": "wrong_shape"})
		return Created{}, err
	}

	// 4. caps (through the transaction); a refusal rolls the consumption back
	caps, err := s.o.Spend.LoadCaps(ctx, tx)
	if err != nil {
		return Created{}, err
	}
	tok, err := s.o.Spend.TokenLimitsFor(ctx, tx, p.Key())
	if err != nil {
		return Created{}, err
	}
	now := s.o.Now()
	u, err := s.o.Spend.Usage(ctx, tx, p.Key(), now)
	if err != nil {
		return Created{}, err
	}
	if ce := spend.CheckCaps(u, caps, spend.PrincipalDaily(p, tok, caps), money.MicroUSD(est.Ceiling), money.MicroUSD(est.Max)); ce != nil {
		err = capErr(ce)
		return Created{}, err
	}

	// 5. what can drift since the estimate
	if err = s.revalidate(ctx, tx, est); err != nil {
		return Created{}, err
	}

	// 6. insert
	need := spend.NeedsApproval(p, via, len(est.Items), money.MicroUSD(est.Ceiling), tok, caps)
	if err = s.insertBatch(ctx, tx, p, in, est, batchID, via, need, now); err != nil {
		return Created{}, err
	}
	created, err = loadCreated(ctx, tx, batchID)
	if err != nil {
		return Created{}, err
	}
	if e, ok := audit.PendingFrom(ctx); ok {
		e.Status, e.CostMicroUSD = http.StatusCreated, money.MicroUSD(est.Ceiling)
		e.CreatedIDs = append([]string{batchID}, created.JobIDs...)
		if e.Duration = now.Sub(e.At); e.Duration < 0 {
			e.Duration = 0
		}
		if _, _, _, ok := idem.KeyFrom(ctx); ok {
			e.Idempotency = audit.IdemStored
		}
		if err = audit.InsertTx(ctx, tx, e); err != nil {
			return Created{}, fmt.Errorf("generation: audit: %w", err)
		}
	}
	if pk, key, gen, ok := idem.KeyFrom(ctx); ok {
		if encode == nil {
			err = errors.New("generation: create needs an encoder under an idempotency key")
			return Created{}, err
		}
		status, body, e := encode(created)
		if e != nil {
			err = e
			return Created{}, err
		}
		if e := idem.CompleteTx(ctx, tx, pk, key, gen, status, "application/json", body); e != nil {
			if errors.Is(e, idem.ErrLeaseLost) {
				err = perr(http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "a request with this Idempotency-Key is still running", nil)
			} else {
				err = e
			}
			return Created{}, err
		}
	}

	// 7. commit, then wake the runner
	if err = s.o.Spend.CommitTx(ctx, tx); err != nil {
		return Created{}, err
	}
	if s.o.Wake != nil {
		s.o.Wake()
	}
	return created, nil
}

// variantShape: a one-item estimate of a variant mode whose every take has this mother.
func variantShape(e estimateRow, track string) bool {
	if len(e.Items) == 0 {
		return false
	}
	for _, t := range e.Items {
		if t.ItemIndex != 0 || !isVariant(t.Mode) || t.ParentTrackID == nil || *t.ParentTrackID != track {
			return false
		}
	}
	return true
}

// revalidate re-checks, through tx, only what can change between estimate and create (C72.4 step 5).
func (s *Service) revalidate(ctx context.Context, tx *sql.Tx, e estimateRow) error {
	var reqIDs, draftIDs []string
	drafts := map[string]bool{}
	for _, t := range e.Items {
		if t.ParentTrackID != nil {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id = ?)`, *t.ParentTrackID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return staleErr(map[string]any{"reason": "parent_missing", "track_id": *t.ParentTrackID})
			}
		}
		if t.DraftID != nil && !drafts[*t.DraftID] {
			drafts[*t.DraftID] = true
			d, ok, err := loadDraft(ctx, tx, *t.DraftID)
			if err != nil {
				return err
			}
			if !ok || d.Status != "backlog" || d.InFlight || (t.DraftRevision != nil && d.Revision != *t.DraftRevision) {
				draftIDs = append(draftIDs, *t.DraftID)
			}
		}
		if t.ReuseRecipeID != nil {
			d, ok, err := loadDraft(ctx, tx, *t.DraftID)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			rid, ok, err := lineage.ReusableDraftRecipe(ctx, tx, lineage.DraftRow{ID: d.ID, Revision: d.Revision, SourceRecipeID: d.SourceRecipeID, RequestJSON: d.RequestJSON})
			if err != nil {
				return err
			}
			if !ok || rid != *t.ReuseRecipeID {
				if !slices.Contains(draftIDs, d.ID) {
					draftIDs = append(draftIDs, d.ID)
				}
			}
			continue
		}
		taken, err := recipeIDTaken(ctx, tx, t.RequestID)
		if err != nil {
			return err
		}
		if taken {
			reqIDs = append(reqIDs, t.RequestID)
		}
	}
	if len(reqIDs) > 0 || len(draftIDs) > 0 {
		d := map[string]any{"reason": "library_changed"}
		if len(reqIDs) > 0 {
			d["request_ids"] = reqIDs
		}
		if len(draftIDs) > 0 {
			d["draft_ids"] = draftIDs
		}
		return staleErr(d)
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertBatch writes recipes, batch, approval, jobs and the take reserves (C72.4 step 6).
func (s *Service) insertBatch(ctx context.Context, tx *sql.Tx, p auth.Principal, in CreateIn, e estimateRow, batchID, via string, need spend.ApprovalNeed, now time.Time) error {
	ts := store.FormatTime(now)
	state, jobState := "active", "queued"
	if need.Needed {
		state, jobState = "awaiting_confirmation", "awaiting_confirmation"
	}
	recipeIDs := make([]string, len(e.Items))
	for i, t := range e.Items {
		if t.ReuseRecipeID != nil {
			recipeIDs[i] = *t.ReuseRecipeID
			continue
		}
		parsed, err := lineage.ParseRequest([]byte(t.RequestJSON), false)
		if err != nil || parsed.Hash != t.RecipeHash {
			return fmt.Errorf("generation: estimate take %d does not re-parse to its recipe hash", i)
		}
		rid, _, err := s.o.Library.InsertRecipe(ctx, tx, parsed, p.Key())
		if errors.Is(err, library.ErrRecipeExists) {
			return staleErr(map[string]any{"reason": "library_changed", "request_ids": []string{t.RequestID}})
		}
		if err != nil {
			return err
		}
		recipeIDs[i] = rid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO batches(id, estimate_id, principal_id, principal_kind, principal_name, via, engine, backend, n_takes, rationale,
		expected_micro_usd, max_micro_usd, retry_allowance_micro_usd, ceiling_micro_usd, price_pph_micro_usd, attempt_overhead_s, kill_slack_s, workers_max, idle_s, state, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		batchID, e.ID, p.Key(), principalKind(p), p.Name, via, e.Engine, e.Backend, len(e.Items), nullStr(in.Rationale),
		e.Expected, e.Max, e.Retry, e.Ceiling, e.PricePPH, e.Quote.AttemptOverheadS, e.Quote.KillSlackS, e.WorkersMax, e.IdleS, state, ts); err != nil {
		return fmt.Errorf("generation: insert batch: %w", err)
	}
	if _, err := s.o.Spend.InsertApproval(ctx, tx, batchID, p, via, need); err != nil {
		return err
	}
	var rationale any
	if in.Rationale != "" {
		rationale = in.Rationale
	}
	for i, t := range e.Items {
		jobID := s.o.NewID()
		mut, _ := json.Marshal(map[string]any{"operator": t.Operator, "params": t.Params, "recipe_patch": t.Patch, "rationale": rationale, "principal_id": p.Key()})
		var queuedAt any
		if jobState == "queued" {
			queuedAt = ts
		}
		var parent, draft any
		if t.ParentTrackID != nil {
			parent = *t.ParentTrackID
		}
		if t.DraftID != nil {
			draft = *t.DraftID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id, batch_id, take_index, kind, engine, backend, recipe_id, source_track_id, draft_id, operator, mutation_json, title,
			expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd, state, next_attempt_at, queued_at, created_at, updated_at)
			VALUES (?,?,?,'generate',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			jobID, batchID, t.TakeIndex, e.Engine, e.Backend, recipeIDs[i], parent, draft, t.Operator, string(mut), t.Title,
			t.ExpectedAudioS, t.ExpectedS, t.TimeoutS, t.ExpectedMicroUSD, t.TakeMicroUSD, jobState, ts, queuedAt, ts, ts); err != nil {
			return fmt.Errorf("generation: insert job: %w", err)
		}
		bid, jid := batchID, jobID
		if err := s.o.Spend.Ledger().Append(ctx, tx, spend.Entry{Kind: spend.KindReserve, MicroUSD: money.MicroUSD(t.TakeMicroUSD), BatchID: &bid, JobID: &jid,
			PrincipalID: p.Key(), Reason: "take"}); err != nil {
			return err
		}
	}
	return nil
}
