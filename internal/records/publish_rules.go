package records

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func errEmpty(msg string) error {
	return problem.E(422, "RECORD_EMPTY", msg, map[string]any{})
}

// playable counts the slots that resolve to a Take that exists and is not discarded (state ok).
func playable(rs []ResolvedSlot) int {
	n := 0
	for _, r := range rs {
		if r.State == StateOK {
			n++
		}
	}
	return n
}

// Propose moves a draft Record to proposed with a rationale (C120.1). Any principal may propose; only a draft with at least
// one live slot qualifies. The rationale is stored in proposal_note and never in the changelog.
func (s *Service) Propose(ctx context.Context, p auth.Principal, id string, revision int, rationale string) (Record, error) {
	note, err := cleanLine("rationale", rationale, 1, MaxProposalNote)
	if err != nil {
		return Record{}, err
	}
	var out Record
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		rec, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec.Revision != revision {
			return errStale(rec)
		}
		if rec.Status != StatusDraft {
			return errState(rec, "only a draft record can be proposed")
		}
		slots, err := loadSlots(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(slots) == 0 {
			return errEmpty("a record needs at least one slot to be proposed")
		}
		at := s.stamp()
		if _, err := tx.ExecContext(ctx, `UPDATE records SET status = 'proposed', proposal_note = ?, proposed_by = ?, proposed_at = ?,
			revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ?`, note, p.Key(), at, p.Key(), at, id); err != nil {
			return fmt.Errorf("records: propose: %w", err)
		}
		if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: KindProposed}); err != nil {
			return err
		}
		out, err = loadRecord(ctx, tx, id)
		return err
	})
	return out, err
}

// Publish publishes or unpublishes a Record; the principal must be KindHuman (C120.1). The checks run in this order:
// principal (before any read), existence, revision (even for an already published Record), the no-op and legality rules,
// then the content rule (a playable slot).
func (s *Service) Publish(ctx context.Context, p auth.Principal, id string, revision int, published bool) (Record, error) {
	if p.Kind != auth.KindHuman {
		return Record{}, errHumanOnly("records are published by the operator")
	}
	var out Record
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		rec, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if rec.Revision != revision {
			return errStale(rec)
		}
		out = rec
		at := s.stamp()
		if published {
			if rec.Status == StatusPublished {
				return nil
			}
			rs, err := resolveRecord(ctx, tx, id)
			if err != nil {
				return err
			}
			if playable(rs) == 0 {
				return errEmpty("a record needs at least one playable slot to be published")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE records SET status = 'published', published_by = ?, published_at = ?,
				first_published_at = COALESCE(first_published_at, ?), revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
				p.Key(), at, at, p.Key(), at, id); err != nil {
				return fmt.Errorf("records: publish: %w", err)
			}
			if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: KindPublished}); err != nil {
				return err
			}
		} else {
			var kind string
			var detail []byte
			switch rec.Status {
			case StatusDraft:
				return nil
			case StatusProposed:
				kind, detail = KindProposalDismiss, []byte(`{"reason":"declined"}`)
			default:
				kind = KindUnpublished
			}
			if _, err := tx.ExecContext(ctx, `UPDATE records SET status = 'draft', published_by = NULL, published_at = NULL, proposed_by = NULL,
				proposed_at = NULL, proposal_note = NULL, revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ?`, p.Key(), at, id); err != nil {
				return fmt.Errorf("records: unpublish: %w", err)
			}
			if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: kind, Detail: detail}); err != nil {
				return err
			}
		}
		out, err = loadRecord(ctx, tx, id)
		return err
	})
	return out, err
}
