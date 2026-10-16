package picks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Pin pins or unpins (nil takeID) a take; humans and paired devices only (C91.6).
func (s *Service) Pin(ctx context.Context, p auth.Principal, songID string, takeID, proposalID *string) (PinResult, error) {
	if !p.ActsAsHuman() {
		return PinResult{}, humanOnly("")
	}
	if s.st == nil {
		return PinResult{}, errUnconfigured
	}
	st, err := s.strengthsFor(ctx, []string{songID}) // before the write transaction: the single writer is never held across the fit
	if err != nil {
		return PinResult{}, err
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return PinResult{}, fmt.Errorf("picks: pin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	env := s.env()
	env.Actor = p.Key()
	if err := requireLiveSongTx(ctx, tx, songID); err != nil {
		return PinResult{}, err
	}
	prev, err := loadPickTx(ctx, tx, songID)
	if err != nil {
		return PinResult{}, err
	}
	var prop *Proposal
	var op *pinOp
	var ch Changes
	changed := false
	if takeID == nil {
		if prev.pinnedTake != nil {
			op = &pinOp{Set: false, By: principalActor(p)}
		}
	} else {
		if err := requireEligibleTakeTx(ctx, tx, songID, *takeID); err != nil {
			return PinResult{}, err
		}
		if proposalID != nil {
			if prop, err = checkAcceptableTx(ctx, tx, songID, *takeID, *proposalID); err != nil {
				return PinResult{}, err
			}
		}
		if !(prev.pinnedTake != nil && *prev.pinnedTake == *takeID && prev.cur.Source == SourcePinned) {
			op = &pinOp{Set: true, Take: *takeID, By: principalActor(p), ProposalID: proposalID}
		}
	}
	// the proposal is closed before the recompute: the take becoming the Pick would expire it otherwise
	if prop != nil {
		now := store.FormatTime(env.Now)
		if _, err := tx.ExecContext(ctx, `UPDATE pick_proposals SET state = 'accepted', decided_at = ?, decided_by = ? WHERE id = ?`, now, p.Key(), prop.ID); err != nil {
			return PinResult{}, fmt.Errorf("picks: accept proposal: %w", err)
		}
		prop.State, prop.DecidedAt, prop.DecidedBy = "accepted", &now, ptr(p.Key())
	}
	if op != nil {
		if changed, err = s.recomputeSongTx(ctx, tx, env, songID, st, op, &ch); err != nil {
			return PinResult{}, err
		}
		if _, err := expireProposalsTx(ctx, tx, env, songID); err != nil {
			return PinResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PinResult{}, fmt.Errorf("picks: pin commit: %w", err)
	}
	view, err := s.Get(ctx, songID)
	if err != nil {
		return PinResult{}, err
	}
	return PinResult{Pick: view, Proposal: prop, Changed: changed}, nil
}

// checkAcceptableTx loads proposalID and requires it to be an open proposal of this Song for this take.
func checkAcceptableTx(ctx context.Context, tx *sql.Tx, songID, takeID, proposalID string) (*Proposal, error) {
	pr, err := loadProposalTx(ctx, tx, proposalID)
	if err != nil {
		return nil, err
	}
	switch {
	case pr.SongID != songID || pr.TakeID != takeID:
		return nil, problem.E(409, "PROPOSAL_MISMATCH", "the proposal is for another song or take", map[string]any{"proposal_id": proposalID})
	case pr.State != "open":
		return nil, problem.E(409, "PROPOSAL_CLOSED", "the proposal is no longer open", map[string]any{"proposal_id": proposalID, "state": pr.State})
	}
	return &pr, nil
}

func loadProposalTx(ctx context.Context, tx *sql.Tx, id string) (Proposal, error) {
	var p Proposal
	err := tx.QueryRowContext(ctx, `SELECT id, song_id, take_id, rationale, principal_id, principal_name, state, created_at, decided_at, decided_by
		FROM pick_proposals WHERE id = ?`, id).Scan(&p.ID, &p.SongID, &p.TakeID, &p.Rationale, &p.PrincipalID, &p.PrincipalName, &p.State, &p.CreatedAt, &p.DecidedAt, &p.DecidedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return p, problem.E(404, "NOT_FOUND", "proposal not found", map[string]any{"proposal_id": id})
	}
	if err != nil {
		return p, fmt.Errorf("picks: proposal: %w", err)
	}
	return p, nil
}
