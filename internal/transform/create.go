package transform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// CreateResult is what Create returns: the view and the HTTP status the operation answers (202 for a queued or running
// transform, also a reused live one; 200 for a reused done one).
type CreateResult struct {
	View   View
	Status int
}

// Create runs the create order of C210.1: (1) enabled and tools, (2) body shape and ranges, (3) inputs exist and qualify,
// (4) alignment, (5) limits, (6) reuse by spec_hash, (7) queue, (8) the daily quota of the principal's kind, (9) one transaction
// holding the recipe, the transform, its inputs, the idempotency completion and the audit row; then Wake once.
//
// encode renders the response body from the result before commit, so the idempotency row stores the exact bytes (the pattern of
// generation.Create); it may be nil when no Idempotency-Key is in play.
func (s *Service) Create(ctx context.Context, p auth.Principal, req Request, encode func(CreateResult) (status int, body []byte, err error)) (res CreateResult, err error) {
	if err = s.ready(); err != nil {
		return CreateResult{}, err
	}
	pl, err := s.build(ctx, s.o.Store.R, p, req) // steps 2-5
	if err != nil {
		return CreateResult{}, err
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return CreateResult{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			idem.MarksFrom(ctx).Reset()
		}
	}()
	now := s.o.Now()

	// 6. reuse: a live or done transform of this spec answers for every principal; a discarded result is restored (D-T1-2)
	if ex, e := s.existing(ctx, tx, pl.Canon.SpecHash); e != nil {
		err = e
		return CreateResult{}, err
	} else if ex != nil {
		res, err = s.reuse(ctx, tx, p, ex, now)
		if err != nil {
			return CreateResult{}, err
		}
		if err = s.finishCreate(ctx, tx, p, res, encode, now, "create_transform"); err != nil {
			return CreateResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return CreateResult{}, err
		}
		s.hub.notify(res.View.ID)
		return res, nil
	}

	// 7. queue: one overall limit on live transforms
	running, queued, err := s.queueState(ctx, tx)
	if err != nil {
		return CreateResult{}, err
	}
	if limit := s.o.Config.TransformQueueMax; running+queued >= limit {
		err = problem.E(http.StatusTooManyRequests, ErrQueueFull, "the transform queue is full",
			map[string]any{"running": running, "queued": queued, "max": limit, "retry_after_s": 30})
		return CreateResult{}, err
	}

	// 8. quota of the principal kind: live rows reserve their budget, finished rows count their measured CPU
	quota, err := s.poolQuota(ctx, tx, pl.PrincipalKd)
	if err != nil {
		return CreateResult{}, err
	}
	used, err := DailyCPUUsedMS(ctx, tx, pl.PrincipalKd, now.Add(-QuotaWindow))
	if err != nil {
		return CreateResult{}, err
	}
	if used+pl.CPU.MaxMS > quota.CapMS {
		err = problem.E(http.StatusTooManyRequests, ErrQuotaExceeded, "the daily transform CPU pool of this principal kind is used up",
			map[string]any{"used_ms": quota.UsedMS, "reserved_ms": quota.ReservedMS, "cap_ms": quota.CapMS, "resets_at": quota.ResetsAt.UTC().Format(time.RFC3339)})
		return CreateResult{}, err
	}

	// 9. insert
	id := s.o.NewID()
	view, err := s.insert(ctx, tx, p, pl, id, now) // the writer is serialised and step 6 ran in this transaction, so the live-spec index cannot fire here
	if err != nil {
		return CreateResult{}, err
	}
	res = CreateResult{View: view, Status: http.StatusAccepted}
	if err = s.finishCreate(ctx, tx, p, res, encode, now, "create_transform"); err != nil {
		return CreateResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CreateResult{}, err
	}
	s.hub.notify(id)
	if s.o.Wake != nil {
		s.o.Wake()
	}
	return res, nil
}

// insert writes the recipe, the transform row and its inputs through the library write path (C201.3, C201.4).
func (s *Service) insert(ctx context.Context, tx *sql.Tx, p auth.Principal, pl *plan, id string, now time.Time) (View, error) {
	recipeID, _, err := s.o.Library.InsertTransformRecipe(ctx, tx, library.SpecRecipe{
		Kind: string(pl.Spec.Kind), Canonical: pl.Canon.Bytes, Hash: pl.Canon.RecipeHash, RequestID: pl.Canon.ID, Seed: pl.Spec.Seed,
	}, p.Key())
	if err != nil {
		return View{}, err
	}
	var rationale any
	if pl.Req.Rationale != nil {
		rationale = *pl.Req.Rationale
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO transforms(id, kind, recipe_id, spec_hash, title, rationale, principal_id, principal_kind,
		principal_name, state, expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?)`,
		id, string(pl.Spec.Kind), recipeID, pl.Canon.SpecHash, pl.Title, rationale, p.Key(), pl.PrincipalKd, principalName(p),
		pl.CPU.ExpectedMS, pl.CPU.MaxMS, pl.Res.OutS, pl.CPU.MemMB, store.FormatTime(now))
	if err != nil {
		return View{}, fmt.Errorf("transform: insert: %w", err)
	}
	for i, in := range pl.Spec.Inputs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO transform_inputs(transform_id, ord, role, track_id, master_sha256) VALUES (?,?,?,?,?)`,
			id, i, in.Role, in.TrackID, in.MasterSHA256); err != nil {
			return View{}, fmt.Errorf("transform: insert input: %w", err)
		}
	}
	v, err := s.view(ctx, tx, id)
	return v, err
}

func principalName(p auth.Principal) string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// reuse answers a create whose spec already has a live or done transform. A done transform whose Take was discarded is
// restored to draft in this transaction (deterministic audio: a re-render would be identical, and tracks_audio_identity would
// refuse it) and answers restored: true.
func (s *Service) reuse(ctx context.Context, tx *sql.Tx, p auth.Principal, ex *Existing, now time.Time) (CreateResult, error) {
	v, err := s.view(ctx, tx, ex.TransformID)
	if err != nil {
		return CreateResult{}, err
	}
	v.Reused = true
	status := http.StatusAccepted
	if ex.State == StateDone {
		status = http.StatusOK
		res, err := tx.ExecContext(ctx, `UPDATE tracks SET status = 'draft', status_set_by = ?, status_set_at = ? WHERE id = ? AND status = 'discarded'`,
			p.Key(), now.UTC().Format(time.RFC3339), ex.TransformID)
		if err != nil {
			return CreateResult{}, fmt.Errorf("transform: restore: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			v.Restored = true
			if e, ok := audit.PendingFrom(ctx); ok {
				e.Op, e.Status, e.CreatedIDs, e.ID = "transform_restored", http.StatusOK, []string{ex.TransformID}, ""
				e.Idempotency = audit.IdemNone
				if err := audit.InsertTx(ctx, tx, e); err != nil {
					return CreateResult{}, fmt.Errorf("transform: audit: %w", err)
				}
			}
		}
	}
	return CreateResult{View: v, Status: status}, nil
}

// finishCreate writes the audit row and completes the idempotency key inside the transaction, so both exist exactly when the
// effect committed (M3 C80).
func (s *Service) finishCreate(ctx context.Context, tx *sql.Tx, p auth.Principal, res CreateResult, encode func(CreateResult) (int, []byte, error), now time.Time, op string) error {
	if e, ok := audit.PendingFrom(ctx); ok {
		e.Status = res.Status
		e.CostMicroUSD = 0
		if !res.View.Reused {
			e.CreatedIDs = []string{res.View.ID}
		}
		if e.Duration = now.Sub(e.At); e.Duration < 0 {
			e.Duration = 0
		}
		if _, _, _, ok := idem.KeyFrom(ctx); ok {
			e.Idempotency = audit.IdemStored
		}
		if err := audit.InsertTx(ctx, tx, e); err != nil {
			return fmt.Errorf("transform: audit: %w", err)
		}
	}
	if pk, key, gen, ok := idem.KeyFrom(ctx); ok {
		if encode == nil {
			return errors.New("transform: create needs an encoder under an idempotency key")
		}
		status, body, err := encode(res)
		if err != nil {
			return err
		}
		if err := idem.CompleteTx(ctx, tx, pk, key, gen, status, "application/json", body); err != nil {
			if errors.Is(err, idem.ErrLeaseLost) {
				return problem.E(http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "a request with this Idempotency-Key is still running", nil)
			}
			return err
		}
	}
	return nil
}

// Cancel answers cancel_transform (C210.2, C210.8). An agent cancels only its own transforms and a human any; a principal that
// merely received another's transform through reuse is not its owner. A queued row is cancelled here, in one transaction (state
// cancelled, CANCELLED_BY_USER, cpu_ms 0: nothing ran, so its quota reservation is released); a running row only gets the
// intent (cancel_requested_at) and the runner kills the child. A terminal row is returned unchanged.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, id string) (v View, err error) {
	if !s.Enabled() {
		return View{}, unavailable("transform")
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return View{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			idem.MarksFrom(ctx).Reset()
		}
	}()
	cur, err := s.view(ctx, tx, id)
	if err != nil {
		return View{}, err
	}
	if !p.ActsAsHuman() && cur.PrincipalID != p.Key() {
		err = forbidden("only the principal that created a transform, or a human, can cancel it", map[string]any{"owner": cur.PrincipalID})
		return View{}, err
	}
	now := s.o.Now()
	stamp := store.FormatTime(now)
	changed := false
	switch cur.State {
	case StateQueued:
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE transforms SET state = 'cancelled', error_code = ?, cpu_ms = 0, finished_at = ?, cancel_requested_at = ?
			WHERE id = ? AND state = 'queued'`, ErrCancelledByUser, stamp, stamp, id)
		if err != nil {
			return View{}, fmt.Errorf("transform: cancel: %w", err)
		}
		n, _ := res.RowsAffected()
		changed = n == 1
	case StateRunning:
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE transforms SET cancel_requested_at = ? WHERE id = ? AND state = 'running' AND cancel_requested_at IS NULL`, stamp, id)
		if err != nil {
			return View{}, fmt.Errorf("transform: cancel: %w", err)
		}
		n, _ := res.RowsAffected()
		changed = n == 1
	}
	if v, err = s.view(ctx, tx, id); err != nil {
		return View{}, err
	}
	if e, ok := audit.PendingFrom(ctx); ok {
		e.Status, e.CostMicroUSD = http.StatusOK, 0
		if changed {
			e.CreatedIDs = []string{id}
		}
		if e.Duration = now.Sub(e.At); e.Duration < 0 {
			e.Duration = 0
		}
		if err = audit.InsertTx(ctx, tx, e); err != nil {
			return View{}, fmt.Errorf("transform: audit: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return View{}, err
	}
	if changed {
		s.hub.notify(id)
	}
	return v, nil
}
