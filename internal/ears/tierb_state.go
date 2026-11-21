package ears

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// stateOfRaw maps a stored raw document to a state: ok, skipped (the recogniser never ran) or error.
func stateOfRaw(r *tierBRawDoc) string {
	switch {
	case r.Status == tierb.StatusOK:
		return TierBOK
	case r.Stages.ASR == tierb.StageSkipped || r.Stages.ASR == tierb.StageNotRequested:
		return TierBSkipped
	}
	return TierBError
}

// tierBProgress reads, in one query, what the library holds about a track that has no tier_b_raw row yet: a stored analysis
// document, a live analyze job, a completed render that needed the ears capability.
func (s *Service) tierBProgress(ctx context.Context, trackID string) (doc, analyzing, rendered bool, err error) {
	err = s.o.Store.R.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM artifacts WHERE track_id = ?1 AND name LIKE 'analysis%.json'),
		EXISTS (SELECT 1 FROM jobs WHERE kind = 'analyze' AND source_track_id = ?1 AND state NOT IN ('completed','failed','cancelled','expired')),
		EXISTS (SELECT 1 FROM jobs WHERE kind = 'generate' AND track_id = ?1 AND EXISTS (SELECT 1 FROM json_each(jobs.needs_json) WHERE value = 'ears'))`,
		trackID).Scan(&doc, &analyzing, &rendered)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, false, false, fmt.Errorf("ears: tier B state: %w", err)
	}
	return doc, analyzing, rendered, nil
}

// tierBStateOf is the state of a track given its current tier_b_raw document (nil when there is none).
func (s *Service) tierBStateOf(ctx context.Context, trackID string, raw *tierBRawDoc) (string, error) {
	if raw != nil {
		return stateOfRaw(raw), nil
	}
	doc, analyzing, rendered, err := s.tierBProgress(ctx, trackID)
	switch {
	case err != nil:
		return "", err
	case analyzing, doc:
		return TierBPending, nil // a live analyze job, or a document the take hook or the sweep has not analysed yet
	case rendered:
		return TierBMissing, nil // the render needed the ears capability and no document arrived
	}
	return TierBNone, nil
}

// TierBState is the state of a track's listening check (C337.4): none (the take's job had no ears need, or an import with no
// analysis), pending (an analyze job for it is live, or a document is stored and not analysed yet), ok, error, skipped (the
// recogniser never ran) or missing (ears was needed and no document arrived). library.ErrNotFound for an unknown track.
func (s *Service) TierBState(ctx context.Context, trackID string) (string, error) {
	if err := s.wired(); err != nil {
		return "", err
	}
	var one int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, trackID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return "", library.ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("ears: tier B state: %w", err)
	}
	rows, _, err := s.loadRows(ctx, trackID)
	if err != nil {
		return "", err
	}
	var raw *tierBRawDoc
	if r, ok := rows[anTierBRaw]; ok {
		raw = decode[tierBRawDoc](r, true)
	}
	return s.tierBStateOf(ctx, trackID, raw)
}
