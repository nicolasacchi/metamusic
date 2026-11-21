package ears

import (
	"context"
	"errors"
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

// sweepCandidates lists, after the cursor, the tracks that hold an analysis*.json artifact and lack a current result: no current
// tier_b_raw row, or, with a current raw row, diction rows that exist only under a superseded definition. Each comes with a
// token naming its newest document. Adherence rows are not considered: AnalyzeTierB does not write them (EARS-F2 adds the writer
// and the clause together). It reads only artifacts and analyses: a track that never received a document (every import and every
// plain take of the existing library) is never a candidate, so the sweep can never turn into a backfill (D-EARS-OP-5).
func (s *Service) sweepCandidates(ctx context.Context, after string, limit int) (ids, tokens []string, err error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT a.track_id, MAX(a.created_at || '/' || a.name) FROM artifacts a
		WHERE a.name LIKE 'analysis%.json' AND a.track_id > ?1 AND (
			NOT EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?2 AND substr(x.version, 1, length(?3)) = ?3)
			OR (EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?4)
				AND NOT EXISTS (SELECT 1 FROM analyses x WHERE x.track_id = a.track_id AND x.analyzer = ?4 AND substr(x.version, 1, length(?5)) = ?5)))
		GROUP BY a.track_id ORDER BY a.track_id LIMIT ?6`,
		after, anTierBRaw, tierBRawDef+" ", anTierBDiction, tierBDictionDef+" ", limit)
	if err != nil {
		return nil, nil, fmt.Errorf("ears: tier B sweep: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, tok string
		if err := rows.Scan(&id, &tok); err != nil {
			return nil, nil, fmt.Errorf("ears: tier B sweep: %w", err)
		}
		ids, tokens = append(ids, id), append(tokens, tok)
	}
	return ids, tokens, rows.Err()
}

// knownBad reports whether the sweep already failed to parse this very document of the track.
func (s *Service) knownBad(id, tok string) bool {
	s.badMu.Lock()
	defer s.badMu.Unlock()
	return s.badDocs[id] == tok
}

func (s *Service) markBad(id, tok string) {
	s.badMu.Lock()
	defer s.badMu.Unlock()
	if s.badDocs == nil {
		s.badDocs = map[string]string{}
	}
	s.badDocs[id] = tok
}

// TierBSweep runs one CPU-only pass: every track with a stored analysis document and no current result is recomputed from that
// document through AnalyzeTierB (C337.3). It creates no job and no spend. A track that fails is reported and skipped; a
// cancelled context ends the pass.
func (s *Service) TierBSweep(ctx context.Context) (SweepReport, error) {
	var rep SweepReport
	if err := s.wired(); err != nil {
		return rep, err
	}
	// A document that never parses is remembered (until a newer one is stored) and skipped, and the pass pages past it, so a
	// pile of bad documents cannot starve the good tracks behind them.
	after := ""
	for rep.Candidates < maxSweepTracks {
		ids, toks, err := s.sweepCandidates(ctx, after, maxSweepTracks)
		if err != nil {
			return rep, err
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		for i, id := range ids {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			if s.knownBad(id, toks[i]) {
				continue
			}
			if rep.Candidates >= maxSweepTracks {
				return rep, nil
			}
			rep.Candidates++
			if _, err := s.AnalyzeTierB(ctx, id); err != nil {
				rep.Failed = append(rep.Failed, id)
				if errors.Is(err, ErrBadTierBDocument) {
					s.markBad(id, toks[i])
				}
				slog.Warn("tier B sweep: analysis failed", "track", id, "error", redact.String(err.Error()))
				continue
			}
			rep.Analysed++
		}
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
