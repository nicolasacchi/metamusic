package picks

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// MaxOpenProposals is the cap of open proposals per Song (C91.7).
const MaxOpenProposals = 20

// Propose records an agent's proposal (C91.7).
func (s *Service) Propose(ctx context.Context, p auth.Principal, songID, takeID, rationale string) (Proposal, error) {
	if p.ActsAsHuman() || p.Kind != auth.KindAgent {
		return Proposal{}, humanOnly("pin_pick")
	}
	if s.st == nil {
		return Proposal{}, errUnconfigured
	}
	rationale = strings.TrimSpace(rationale)
	if n := utf8.RuneCountInString(rationale); n < 1 || n > 500 {
		return Proposal{}, problem.E(422, "VALIDATION_FAILED", "rationale must be 1..500 characters", map[string]any{"field": "rationale"})
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("picks: propose: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireLiveSongTx(ctx, tx, songID); err != nil {
		return Proposal{}, err
	}
	if err := requireEligibleTakeTx(ctx, tx, songID, takeID); err != nil {
		return Proposal{}, err
	}
	cur, err := loadPickTx(ctx, tx, songID)
	if err != nil {
		return Proposal{}, err
	}
	if cur.cur.Take != nil && *cur.cur.Take == takeID {
		return Proposal{}, problem.E(409, "ALREADY_PICK", "the take is already the Pick", map[string]any{"song_id": songID, "take_id": takeID})
	}
	env := s.env()
	now := store.FormatTime(env.Now)
	if _, err := tx.ExecContext(ctx, `UPDATE pick_proposals SET state = 'superseded', decided_at = ?, decided_by = ? WHERE song_id = ? AND principal_id = ? AND state = 'open'`,
		now, p.Key(), songID, p.Key()); err != nil {
		return Proposal{}, fmt.Errorf("picks: supersede: %w", err)
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pick_proposals WHERE song_id = ? AND state = 'open'`, songID).Scan(&open); err != nil {
		return Proposal{}, fmt.Errorf("picks: count proposals: %w", err)
	}
	if open >= MaxOpenProposals {
		return Proposal{}, problem.E(409, "TOO_MANY_PROPOSALS", "the song has too many open proposals", map[string]any{"song_id": songID, "limit": MaxOpenProposals})
	}
	pr := Proposal{ID: env.NewID(), SongID: songID, TakeID: takeID, Rationale: rationale, PrincipalID: p.Key(), PrincipalName: p.Name, State: "open", CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES (?,?,?,?,?,?,'open',?)`,
		pr.ID, songID, takeID, rationale, pr.PrincipalID, pr.PrincipalName, now); err != nil {
		return Proposal{}, fmt.Errorf("picks: insert proposal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("picks: propose commit: %w", err)
	}
	return pr, nil
}

// Dismiss closes an open proposal as dismissed; humans only (C91.7).
func (s *Service) Dismiss(ctx context.Context, p auth.Principal, proposalID string) (Proposal, error) {
	if !p.ActsAsHuman() {
		return Proposal{}, humanOnly("")
	}
	if s.st == nil {
		return Proposal{}, errUnconfigured
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("picks: dismiss: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	pr, err := loadProposalTx(ctx, tx, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if pr.State != "open" {
		return Proposal{}, problem.E(409, "PROPOSAL_CLOSED", "the proposal is no longer open", map[string]any{"proposal_id": proposalID, "state": pr.State})
	}
	now := store.FormatTime(s.now())
	if _, err := tx.ExecContext(ctx, `UPDATE pick_proposals SET state = 'dismissed', decided_at = ?, decided_by = ? WHERE id = ?`, now, p.Key(), proposalID); err != nil {
		return Proposal{}, fmt.Errorf("picks: dismiss: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("picks: dismiss commit: %w", err)
	}
	pr.State, pr.DecidedAt, pr.DecidedBy = "dismissed", &now, ptr(p.Key())
	return pr, nil
}
