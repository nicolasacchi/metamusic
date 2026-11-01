package sounds

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// List bounds of ListProposals and the number of open proposals whose impact IncludeImpact recomputes per call.
const (
	DefaultProposalLimit = 50
	MaxProposalLimit     = 100
	ImpactRecomputeMax   = 10
)

// ProposalFilter is the input of ListProposals. States empty means every state; IncludeImpact recomputes the impact of
// the first ImpactRecomputeMax open proposals of the page against the current taxonomy instead of the snapshot.
type ProposalFilter struct {
	States        []string
	IncludeImpact bool
	Limit         int // 0 = DefaultProposalLimit
	Cursor        string
}

// ProposalPage is one page of proposals, newest first; Total counts every match, not just the page.
type ProposalPage struct {
	Items      []Proposal
	NextCursor *string
	Total      int
}

// ListProposals pages the proposals, newest first (created_at, then id).
func (s *Service) ListProposals(ctx context.Context, f ProposalFilter) (ProposalPage, error) {
	limit := f.Limit
	switch {
	case limit == 0:
		limit = DefaultProposalLimit
	case limit < 1 || limit > MaxProposalLimit:
		return ProposalPage{}, problem.E(422, "INVALID_LIMIT", fmt.Sprintf("limit must be 1..%d", MaxProposalLimit), nil)
	}
	where, args := "1 = 1", []any{}
	if len(f.States) > 0 {
		for _, st := range f.States {
			switch st {
			case ProposalOpen, ProposalAccepted, ProposalRejected, ProposalExpired:
			default:
				return ProposalPage{}, problem.E(422, "INVALID_STATE", "state must be open, accepted, rejected or expired", nil)
			}
			args = append(args, st)
		}
		where = "state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",") + ")"
	}
	pageWhere, pageArgs := where, append([]any{}, args...)
	if f.Cursor != "" {
		parts, ok := decodeCursor(f.Cursor, 2)
		if !ok {
			return ProposalPage{}, badCursor()
		}
		pageWhere += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		pageArgs = append(pageArgs, parts[0], parts[0], parts[1])
	}
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProposalPage{}, fmt.Errorf("sounds: list proposals: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var page ProposalPage
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM taxonomy_proposals WHERE `+where, args...).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("sounds: count proposals: %w", err)
	}
	//nolint:gosec // G202: pageWhere is built from constant fragments and "?" placeholders only
	rows, err := tx.QueryContext(ctx, `SELECT `+proposalCols+` FROM taxonomy_proposals WHERE `+pageWhere+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(pageArgs, limit+1)...)
	if err != nil {
		return page, fmt.Errorf("sounds: list proposals: %w", err)
	}
	defer rows.Close()
	page.Items = []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, p)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		c := encodeCursor(last.CreatedAt, last.ID)
		page.NextCursor = &c
	}
	if f.IncludeImpact {
		done := 0
		for i := range page.Items {
			if page.Items[i].State != ProposalOpen {
				continue // a decided proposal keeps the snapshot it was decided on
			}
			if done == ImpactRecomputeMax {
				break
			}
			done++
			imp, err := s.impactIn(ctx, tx, page.Items[i].Mappings)
			if err != nil {
				return page, err
			}
			page.Items[i].Impact = imp
		}
	}
	return page, nil
}
