package morning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// candCols is the column list every candidate query selects (the loudness is the latest 'loudness' analysis, zero when none, which
// Select reads as no_loudness).
const candCols = `t.id, t.root_track_id, t.master_sha256, t.master_path, t.duration_s, t.total_samples, t.sample_rate, t.channels, t.created_at,
  COALESCE((SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness'
            ORDER BY a.created_at DESC, a.version DESC LIMIT 1), 0),
  COALESCE((SELECT json_extract(a.metrics_json, '$.true_peak_dbtp') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness'
            ORDER BY a.created_at DESC, a.version DESC LIMIT 1), 0),
  COALESCE(t.truncated_abc, 0) OR COALESCE(t.truncated_semantic, 0),
  (SELECT j.batch_id FROM jobs j WHERE j.track_id = t.id ORDER BY j.id LIMIT 1)`

// eligibleSQL is the one query of C380.1: takes made by a job since the previous scheduled crate, never rated by the Owner, not
// discarded and in no earlier crate, plus the takes the caller asked for by id (which skip the first three tests but never the
// discarded one). julianday compares instants of any fractional width (D-L1-34).
const eligibleSQL = `SELECT ` + candCols + ` FROM tracks t
  WHERE t.status <> 'discarded' AND (
    t.id IN (SELECT value FROM json_each(?))
    OR (t.origin = 'job' AND julianday(t.created_at) > julianday(?)
        AND NOT EXISTS (SELECT 1 FROM owner_ratings r WHERE r.track_id = t.id)
        AND NOT EXISTS (SELECT 1 FROM morning_crate_items i WHERE i.track_id = t.id)))
  ORDER BY t.created_at DESC, t.id`

// anchor pools: the Owner's five-star takes, and takes with status final.
const (
	anchors5SQL = `SELECT ` + candCols + ` FROM tracks t WHERE t.status <> 'discarded'
	  AND EXISTS (SELECT 1 FROM owner_ratings r WHERE r.track_id = t.id AND r.stars = 5) ORDER BY t.id LIMIT 200`
	anchorsFinalSQL = `SELECT ` + candCols + ` FROM tracks t WHERE t.status = 'final' ORDER BY t.id LIMIT 200`
)

// since is the lower bound of "new": the previous scheduled crate that finished without failing, none meaning the last 24 hours. A
// failed crate is skipped so the takes it never carried are tried again.
func (s *Service) since(ctx context.Context, q Querier) (time.Time, error) {
	var at sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT created_at FROM morning_crates WHERE trigger = 'scheduled' AND state IN ('ready','empty')
		ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&at); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("morning: since: %w", err)
	}
	if at.Valid {
		if t, err := store.ParseInstant(at.String); err == nil {
			return t, nil
		}
	}
	return s.now().Add(-defaultSince), nil
}

// Eligible lists the candidates for a crate: the takes of the window plus include, each with its source. Takes of night batch
// night are night_shift, the included ones manual and the rest job.
func (s *Service) Eligible(ctx context.Context, include []string, night string) ([]Candidate, error) {
	since, err := s.since(ctx, s.o.Store.R)
	if err != nil {
		return nil, err
	}
	inc, _ := json.Marshal(append([]string{}, include...))
	want := map[string]bool{}
	for _, id := range include {
		want[id] = true
	}
	cs, batches, err := s.scanCandidates(ctx, eligibleSQL, string(inc), store.FormatTime(since))
	if err != nil {
		return nil, err
	}
	for i := range cs {
		switch {
		case want[cs[i].TrackID]:
			cs[i].Source = SourceManual
		case night != "" && batches[cs[i].TrackID] == night:
			cs[i].Source = SourceNightShift
		default:
			cs[i].Source = SourceJob
		}
	}
	return cs, nil
}

func (s *Service) scanCandidates(ctx context.Context, q string, args ...any) ([]Candidate, map[string]string, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("morning: candidates: %w", err)
	}
	defer rows.Close()
	var out []Candidate
	batches := map[string]string{}
	for rows.Next() {
		var (
			c     Candidate
			at    string
			trunc int
			batch sql.NullString
		)
		if err := rows.Scan(&c.TrackID, &c.RootID, &c.MasterSHA, &c.MasterPath, &c.DurationS, &c.TotalSamples, &c.SampleRate, &c.Channels, &at,
			&c.LUFS, &c.TruePeak, &trunc, &batch); err != nil {
			return nil, nil, fmt.Errorf("morning: candidates: %w", err)
		}
		if c.CreatedAt, err = store.ParseInstant(at); err != nil {
			return nil, nil, fmt.Errorf("morning: candidates: %w", err)
		}
		c.Truncated = trunc != 0
		if batch.Valid {
			batches[c.TrackID] = batch.String
		}
		out = append(out, c)
	}
	return out, batches, rows.Err()
}

// input gathers everything Select reads.
func (s *Service) input(ctx context.Context, spec buildSpec) (Input, error) {
	cands, err := s.Eligible(ctx, spec.Include, spec.NightBatch)
	if err != nil {
		return Input{}, err
	}
	a5, _, err := s.scanCandidates(ctx, anchors5SQL)
	if err != nil {
		return Input{}, err
	}
	af, _, err := s.scanCandidates(ctx, anchorsFinalSQL)
	if err != nil {
		return Input{}, err
	}
	for _, pool := range [][]Candidate{a5, af} {
		for i := range pool {
			pool[i].Source = SourceJob
		}
	}
	return Input{Candidates: cands, Anchors5: a5, AnchorsFinal: af}, nil
}

// run builds the crate row id to its end state. It never returns an error: the outcome is the row.
func (s *Service) run(ctx context.Context, id string, spec buildSpec) {
	state, code := s.build(ctx, id, spec)
	if state != "" {
		return
	}
	// a failure: the row says which, written even when ctx was cancelled
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if code == "" {
		code = CodeToolFailed
	}
	if _, err := s.o.Store.W.ExecContext(wctx, `UPDATE morning_crates SET state = 'failed', error_code = ?, built_at = ? WHERE id = ? AND state = 'building'`,
		code, store.FormatTime(s.now()), id); err != nil {
		slog.Error("morning: could not record a failed build", "err", redact.Error(err))
	}
}

// build returns a non-empty state when it finished the row itself (ready or empty); otherwise the failure code.
func (s *Service) build(ctx context.Context, id string, spec buildSpec) (state, code string) {
	in, err := s.input(ctx, spec)
	if err != nil {
		return "", failCode(ctx, err)
	}
	plan := Select(in, spec.Opts, spec.Seed)
	if len(plan.Sides) == 0 {
		if err := s.finish(ctx, id, plan, nil, nil, StateEmpty); err != nil {
			return "", failCode(ctx, err)
		}
		return StateEmpty, ""
	}
	srcs := map[string]Source{}
	for _, pool := range [][]Candidate{in.Candidates, in.Anchors5, in.AnchorsFinal} {
		for _, c := range pool {
			srcs[c.TrackID] = Source{TrackID: c.TrackID, MasterPath: c.MasterPath, SampleRate: c.SampleRate, Channels: c.Channels}
		}
	}
	if s.o.Builder == nil {
		slog.Error("morning: no side builder (media tools missing)")
		return "", CodeToolFailed
	}
	results := make([]SideResult, len(plan.Sides))
	for i, side := range plan.Sides {
		sources := map[string]Source{}
		for _, ch := range side.Chapters {
			sources[ch.TrackID] = srcs[ch.TrackID]
		}
		res, err := s.o.Builder.BuildSide(ctx, SideInput{Side: side, Sources: sources})
		if err != nil {
			slog.Error("morning: side build failed", "side", i, "err", redact.Error(err))
			return "", failCode(ctx, err)
		}
		results[i] = res
	}
	if err := s.finish(ctx, id, plan, results, itemSources(in), StateReady); err != nil {
		return "", failCode(ctx, err)
	}
	return StateReady, ""
}

// failCode maps an error to the stored code: a build error keeps its own, a cancelled context is INTERRUPTED.
func failCode(ctx context.Context, err error) string {
	var be *BuildError
	switch {
	case errors.As(err, &be):
		return be.Code
	case ctx.Err() != nil:
		return CodeInterrupted
	}
	slog.Error("morning: build failed", "err", redact.Error(err))
	return CodeToolFailed
}

// finish writes the plan's rows and the end state in one transaction, so a crate is never ready without its chapters and sides.
func (s *Service) finish(ctx context.Context, id string, plan Plan, results []SideResult, sources map[string]string, state string) error {
	wctx := context.WithoutCancel(ctx) // a finished build is recorded even when the shutdown began meanwhile
	var (
		takes, anchors int
		frames         int64
	)
	for _, side := range plan.Sides {
		takes += len(side.Chapters)
		frames += side.Samples
		for _, ch := range side.Chapters {
			if ch.Anchor {
				anchors++
			}
		}
	}
	excl, _ := json.Marshal(append([]Excluded{}, plan.Excluded...))
	return s.writeTx(wctx, func(tx *sql.Tx) error {
		for i, side := range plan.Sides {
			for _, ch := range side.Chapters {
				var lufs any
				for _, cl := range results[i].Chapters {
					if cl.N == ch.N {
						lufs = cl.LUFS
					}
				}
				src := SourceJob
				if v, ok := sources[ch.TrackID]; ok {
					src = v
				}
				if _, err := tx.ExecContext(wctx, `INSERT INTO morning_crate_items(crate_id, n, side_index, track_id, anchor, source, start_frame, audio_start_frame, end_frame,
					gain_cdb, limited, lufs_after) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, ch.N, side.Index, ch.TrackID, b2i(ch.Anchor), src, ch.Start, ch.AudioStart, ch.End,
					ch.GainCDB, b2i(ch.Limited), lufs); err != nil {
					return fmt.Errorf("morning: insert item: %w", err)
				}
			}
			r := results[i]
			if _, err := tx.ExecContext(wctx, `INSERT INTO morning_crate_sides(crate_id, side_index, sha256, path, bytes, samples) VALUES(?,?,?,?,?,?)`,
				id, side.Index, r.SHA256, r.RelPath, r.Bytes, r.Samples); err != nil {
				return fmt.Errorf("morning: insert side: %w", err)
			}
		}
		res, err := tx.ExecContext(wctx, `UPDATE morning_crates SET state = ?, built_at = ?, adjacency_relaxed = ?, takes = ?, anchors = ?, duration_ms = ?, excluded_json = ?
			WHERE id = ? AND state = 'building'`, state, store.FormatTime(s.now()), b2i(plan.AdjacencyRelaxed), takes, anchors, frames*1000/SampleRate, string(excl), id)
		if err != nil {
			return fmt.Errorf("morning: finish crate: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("morning: the crate is no longer building")
		}
		return nil
	})
}

// itemSources maps each candidate to the source it entered under (an anchor, which is not a candidate, is stored as a job take).
func itemSources(in Input) map[string]string {
	m := map[string]string{}
	for _, c := range in.Candidates {
		m[c.TrackID] = c.Source
	}
	return m
}
