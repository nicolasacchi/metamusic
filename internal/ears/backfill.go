package ears

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// capabilityOf names the missing capability inside an analysis.ErrToolMissing error
// ("analysis: required tool capability missing: filter showspectrumpic" => "filter showspectrumpic").
func capabilityOf(err error) string {
	msg := err.Error()
	if errors.Is(err, analysis.ErrToolMissing) {
		if rest, ok := strings.CutPrefix(msg, analysis.ErrToolMissing.Error()+": "); ok {
			return rest
		}
	}
	return msg
}

// Backfill analyses every track (or o.TrackIDs) that lacks analyzers.
func (s *Service) Backfill(ctx context.Context, o BackfillOptions) (BackfillReport, error) {
	rep := BackfillReport{Ran: map[string]int{}}
	if err := s.wired(); err != nil {
		return rep, err
	}
	if err := s.missingTierA(); err != nil {
		rep.ToolMissing = capabilityOf(err)
		return rep, nil
	}
	ids := o.TrackIDs
	if len(ids) == 0 {
		rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id FROM tracks ORDER BY recorded_at, id`)
		if err != nil {
			return rep, fmt.Errorf("ears: backfill: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return rep, err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return rep, err
		}
	}
	rep.Tracks = len(ids)
	outs := make([]Outcome, len(ids))
	errs := make([]error, len(ids))
	jobs := max(o.Jobs, 1)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, id := range ids {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := ctx.Err(); err != nil {
				errs[i] = err
				return
			}
			outs[i], errs[i] = s.one(ctx, id, o)
		}()
	}
	wg.Wait()
	for i, out := range outs {
		switch err := errs[i]; {
		case errors.Is(err, library.ErrNotFound):
			out = Outcome{TrackID: ids[i], Failed: map[string]string{"track": "NOT_FOUND"}}
		case err != nil:
			return rep, err
		}
		for _, n := range out.Ran {
			rep.Ran[n]++
		}
		switch {
		case len(out.Failed) > 0:
			rep.Failed++
			rep.Failures = append(rep.Failures, out)
		case len(out.Ran) > 0:
			rep.Analysed++
		}
	}
	return rep, nil
}

// one analyses (or, for a dry run, only plans) one track.
func (s *Service) one(ctx context.Context, id string, o BackfillOptions) (Outcome, error) {
	if !o.DryRun {
		return s.AnalyzeTrack(ctx, id, AnalyzeOptions{Force: o.Force})
	}
	refs, err := s.o.Library.TrackRefs(ctx, []string{id})
	if err != nil {
		return Outcome{}, err
	}
	ref, ok := refs[id]
	if !ok {
		return Outcome{}, library.ErrNotFound
	}
	p, err := s.planFor(ctx, ref, o.Force)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{TrackID: id, Ran: p.needed()}, nil
}
