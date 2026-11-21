package mandates

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// Service implements generation.MandateCover (C368.2; the compile-time check is in the app wiring and in generation's tests, because
// mandates never imports generation): it resolves the lineage roots and the Owner's stars of the parents itself, so generation never
// reads a mandate table.

// Cover reads the mandate, its usage and the parents through q and applies the pure Cover. An unknown mandate is a 404.
func (s *Service) Cover(ctx context.Context, q lineage.Querier, p auth.Principal, mandateID, engine string, takes []spend.MandateTake, ceiling money.MicroUSD, tierB bool) (bool, []string, error) {
	if s.o.Spend == nil {
		return false, nil, errors.ErrUnsupported
	}
	m, err := s.get(ctx, q, mandateID)
	if err != nil {
		return false, nil, err
	}
	u, err := s.o.Spend.MandateUsage(ctx, q, mandateID)
	if err != nil {
		return false, nil, err
	}
	ct := make([]CoverTake, len(takes))
	for i, t := range takes {
		ct[i] = CoverTake{Mode: t.Mode}
		if t.ParentTrackID == nil {
			continue
		}
		ct[i].ParentTrackID = *t.ParentTrackID
		if ct[i].RootTrackID, ct[i].ParentStars, err = parentFacts(ctx, q, *t.ParentTrackID); err != nil {
			return false, nil, err
		}
	}
	ok, reasons := Cover(m, u, CoverInput{PrincipalKey: p.Key(), Engine: engine, Takes: ct, Ceiling: ceiling, TierB: tierB, Now: s.o.Now(), Loc: s.Location()})
	if reasons == nil {
		reasons = []string{}
	}
	return ok, reasons, nil
}

// Recheck is Cover through the create transaction; a mandate that no longer covers the batch is 409 MANDATE_UNAVAILABLE.
func (s *Service) Recheck(ctx context.Context, tx *sql.Tx, p auth.Principal, mandateID, engine string, takes []spend.MandateTake, ceiling money.MicroUSD, tierB bool) error {
	ok, reasons, err := s.Cover(ctx, tx, p, mandateID, engine, takes, ceiling, tierB)
	if err != nil {
		return err
	}
	if !ok {
		return problem.E(409, "MANDATE_UNAVAILABLE", "the mandate no longer covers this batch", map[string]any{"mandate_id": mandateID, "reasons": reasons})
	}
	return nil
}

// parentFacts is the lineage root of a parent track and the Owner's stars on it (nil: unrated, or the track is unknown).
func parentFacts(ctx context.Context, q lineage.Querier, trackID string) (root string, stars *int, err error) {
	var r sql.NullString
	var st sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT t.root_track_id, (SELECT stars FROM owner_ratings WHERE track_id = t.id)
		FROM tracks t WHERE t.id = ?`, trackID).Scan(&r, &st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if st.Valid {
		v := int(st.Int64)
		stars = &v
	}
	return r.String, stars, nil
}
