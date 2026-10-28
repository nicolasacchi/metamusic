package records

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// requireEditable is the principal rule of D-L2-4: a published Record is edited by a human (or a paired device) only.
func requireEditable(p auth.Principal, rec Record) error {
	if rec.Status == StatusPublished && !p.ActsAsHuman() {
		return errHumanOnly("published records are edited by a human")
	}
	return nil
}

// dismissProposal sends a proposed Record back to draft because a non-human principal edited it (D-L2-4): the proposal
// fields are cleared and a proposal_dismissed row (detail {"reason": reason}) is appended. It returns nil rows for a
// Record that is not proposed or for an editor who acts as a human (a human's edit keeps the proposal).
func (s *Service) dismissProposal(ctx context.Context, tx *sql.Tx, rec Record, p auth.Principal, at, reason string) ([]Change, error) {
	if rec.Status != StatusProposed || p.ActsAsHuman() {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE records SET status = 'draft', proposed_by = NULL, proposed_at = NULL, proposal_note = NULL WHERE id = ?`, rec.ID); err != nil {
		return nil, fmt.Errorf("records: dismiss proposal: %w", err)
	}
	c, err := s.insertChangeRow(ctx, tx, rec.ID, p, at, Change{Kind: KindProposalDismiss, Detail: []byte(`{"reason":"` + reason + `"}`)})
	if err != nil {
		return nil, err
	}
	return []Change{c}, nil
}

// Update changes the text fields, re-rolls the cover or hides the Record under the revision lock (C120.1).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Record, error) {
	if patch.Title == nil && patch.Subtitle == nil && patch.LinerNote == nil && !patch.CoverReroll && patch.Hidden == nil {
		return Record{}, errInvalid("body", "nothing to change")
	}
	var title, sub, note *string
	var err error
	if patch.Title != nil {
		v, err := cleanLine("title", *patch.Title, 1, MaxTitle)
		if err != nil {
			return Record{}, err
		}
		title = &v
	}
	if patch.Subtitle != nil {
		v, err := cleanLine("subtitle", *patch.Subtitle, 0, MaxSubtitle)
		if err != nil {
			return Record{}, err
		}
		sub = &v
	}
	if patch.LinerNote != nil {
		v, err := cleanNote("liner_note", *patch.LinerNote, MaxLinerNote)
		if err != nil {
			return Record{}, err
		}
		note = &v
	}
	var out Record
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		rec, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requireEditable(p, rec); err != nil {
			return err
		}
		if rec.Revision != patch.Revision {
			return errStale(rec)
		}
		at := s.stamp()
		var kinds []string
		set := rec // the row as it will be
		if title != nil && *title != rec.Title {
			set.Title = *title
			kinds = append(kinds, KindRenamed)
		}
		if (sub != nil && *sub != rec.Subtitle) || (note != nil && *note != rec.LinerNote) {
			if sub != nil {
				set.Subtitle = *sub
			}
			if note != nil {
				set.LinerNote = *note
			}
			kinds = append(kinds, KindRetexted)
		}
		if patch.CoverReroll {
			set.CoverRerolls++
			set.CoverSeed = CoverSeed(rec.ID, set.CoverRerolls)
			kinds = append(kinds, KindCoverRerolled)
		}
		hide := ""
		if patch.Hidden != nil {
			switch {
			case *patch.Hidden && rec.HiddenAt == nil:
				hide = KindHidden
			case !*patch.Hidden && rec.HiddenAt != nil:
				hide = KindUnhidden
			}
			if hide != "" {
				kinds = append(kinds, hide)
			}
		}
		if len(kinds) == 0 { // nothing differs: no write, no revision
			out = rec
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE records SET title = ?, subtitle = ?, liner_note = ?, cover_rerolls = ?, cover_seed = ?,
			revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
			set.Title, set.Subtitle, set.LinerNote, set.CoverRerolls, set.CoverSeed, p.Key(), at, id); err != nil {
			return fmt.Errorf("records: update: %w", err)
		}
		switch hide {
		case KindHidden:
			_, err = tx.ExecContext(ctx, `UPDATE records SET hidden_at = ?, hidden_by = ? WHERE id = ?`, at, p.Key(), id)
		case KindUnhidden:
			_, err = tx.ExecContext(ctx, `UPDATE records SET hidden_at = NULL, hidden_by = NULL WHERE id = ?`, id)
		}
		if err != nil {
			return fmt.Errorf("records: update hidden: %w", err)
		}
		for _, k := range kinds {
			if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: k}); err != nil {
				return err
			}
		}
		if _, err := s.dismissProposal(ctx, tx, rec, p, at, "edited"); err != nil {
			return err
		}
		out, err = loadRecord(ctx, tx, id)
		return err
	})
	return out, err
}
