package ears

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// TierBSweepInterval is how often the background sweep runs (C337.3).
const TierBSweepInterval = 30 * time.Minute

// maxSweepTracks bounds one sweep pass; the rest waits for the next one.
const maxSweepTracks = 200

// SweepReport sums one TierBSweep pass.
type SweepReport struct {
	Candidates int      // tracks whose stored document had no current result
	Analysed   int      // tracks AnalyzeTierB completed for
	Failed     []string // track ids AnalyzeTierB failed for
}

// sweepCandidates lists the tracks that hold an analysis*.json artifact and lack a current result: no current tier_b_raw row,
// or, with a current raw row of status ok, diction rows that exist only under a superseded definition (and, in the same way,
// adherence rows). It reads only artifacts and analyses: a track that never received a document (every import and every plain
// take of the existing library) is never a candidate, so the sweep can never turn into a backfill (D-EARS-OP-5).
func (s *Service) sweepCandidates(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT DISTINCT a.track_id FROM artifacts a
		WHERE a.name LIKE 'analysis%.json' AND (
			NOT EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?1 AND substr(x.version, 1, length(?2)) = ?2)
			OR (EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?3)
				AND NOT EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?3 AND substr(x.version, 1, length(?4)) = ?4))
			OR (EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?5)
				AND NOT EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?5 AND substr(x.version, 1, length(?6)) = ?6)))
		ORDER BY a.track_id LIMIT ?7`,
		anTierBRaw, tierBRawDef+" ", anTierBDiction, tierBDictionDef+" ", anTierBAdherence, tierBAdherenceDef+" ", limit)
	if err != nil {
		return nil, fmt.Errorf("ears: tier B sweep: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ears: tier B sweep: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TierBSweep runs one CPU-only pass: every track with a stored analysis document and no current result is recomputed from that
// document through AnalyzeTierB (C337.3). It creates no job and no spend. A track that fails is reported and skipped; a
// cancelled context ends the pass.
func (s *Service) TierBSweep(ctx context.Context) (SweepReport, error) {
	var rep SweepReport
	if err := s.wired(); err != nil {
		return rep, err
	}
	ids, err := s.sweepCandidates(ctx, maxSweepTracks)
	if err != nil {
		return rep, err
	}
	rep.Candidates = len(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if _, err := s.AnalyzeTierB(ctx, id); err != nil {
			rep.Failed = append(rep.Failed, id)
			slog.Warn("tier B sweep: analysis failed", "track", id, "error", redact.String(err.Error()))
			continue
		}
		rep.Analysed++
	}
	return rep, nil
}

// RunTierBSweep is the body of the background loop: a pass at start and one every interval until ctx ends.
func (s *Service) RunTierBSweep(ctx context.Context, every time.Duration) {
	for {
		if _, err := s.TierBSweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("tier B sweep failed", "error", redact.String(err.Error()))
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
