package sequence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// CrateSongs resolves a Song Crate to the ids of its Songs (manual membership, or the matches of a smart Crate's stored
// filter). sequence cannot import crates or songs (C133), so the composition root installs the resolver with SetCrates.
// It returns the full count of matches and at most limit ids (the caller refuses a count above MaxSongs).
type CrateSongs func(ctx context.Context, crateID string, limit int) (ids []string, total int, err error)

// SetCrates installs the Crate resolver; without one, crate_id answers 503 UNAVAILABLE. Call it before serving.
func (s *Service) SetCrates(f CrateSongs) { s.crates = f }

// Suggest answers suggest_sequence: a read-only plan that never writes (C138.1). Exactly one of RecordID, SongIDs or
// CrateID selects the Songs; the whole call runs in one read transaction.
func (s *Service) Suggest(ctx context.Context, in SuggestInput) (Suggestion, error) {
	n := 0
	for _, set := range []bool{in.RecordID != "", len(in.SongIDs) > 0, in.CrateID != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return Suggestion{}, problem.E(422, CodeBadSource, "give exactly one of record_id, song_ids or crate_id", map[string]any{})
	}
	songIDs := in.SongIDs
	if in.CrateID != "" {
		if s.crates == nil {
			return Suggestion{}, problem.E(503, "UNAVAILABLE", "crates are not available", map[string]any{"component": "crates"})
		}
		ids, total, err := s.crates(ctx, in.CrateID, batchLimit+1)
		if err != nil {
			return Suggestion{}, err
		}
		if total > batchLimit {
			return Suggestion{}, tooMany(total)
		}
		songIDs = ids
	}
	if len(songIDs) > batchLimit {
		return Suggestion{}, tooMany(len(songIDs))
	}
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Suggestion{}, fmt.Errorf("sequence: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var refs []Ref
	if in.RecordID != "" {
		refs, err = recordRefs(ctx, tx, in.RecordID)
	} else {
		refs, err = songRefs(ctx, tx, songIDs)
	}
	if err != nil {
		return Suggestion{}, err
	}
	p, err := Prepare(ctx, tx, refs, string(in.Strategy), in.First, in.Last)
	if err != nil {
		return Suggestion{}, err
	}
	return s.suggestion(ctx, tx, p)
}

// recordRefs lists the live slots of a Record in order, each with its resolved Take: the pin, else the Song's Pick take,
// else nil (the Song's subject features count).
func recordRefs(ctx context.Context, q Querier, recordID string) ([]Ref, error) {
	var order sql.NullString
	err := q.QueryRowContext(ctx, `SELECT order_json FROM records WHERE id = ?`, recordID).Scan(&order)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, problem.E(404, "NOT_FOUND", "record not found", map[string]any{"record_id": recordID})
	}
	if err != nil {
		return nil, fmt.Errorf("sequence: read record: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT s.song_id, COALESCE(s.pinned_take_id, p.take_id)
		FROM records r, json_each(r.order_json) j
		JOIN record_slots s ON s.id = j.value AND s.record_id = r.id AND s.removed_at IS NULL
		LEFT JOIN song_picks p ON p.song_id = s.song_id
		WHERE r.id = ? ORDER BY j.key`, recordID)
	if err != nil {
		return nil, fmt.Errorf("sequence: read slots: %w", err)
	}
	defer rows.Close()
	var refs []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.SongID, &r.TakeID); err != nil {
			return nil, fmt.Errorf("sequence: read slots: %w", err)
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// songRefs checks that every id is a Song (UNKNOWN_SONG) and not repeated, and returns Refs on the Songs' subjects.
func songRefs(ctx context.Context, q Querier, ids []string) ([]Ref, error) {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, problem.E(422, CodeUnknownSong, "a song is listed twice", map[string]any{"song_id": id})
		}
		seen[id] = true
	}
	known := map[string]bool{}
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT id FROM songs WHERE id IN (`+placeholders(len(part))+`)`, args...) //nolint:gosec // G202: only "?" placeholders
		if err != nil {
			return nil, fmt.Errorf("sequence: read songs: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("sequence: read songs: %w", err)
			}
			known[id] = true
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("sequence: read songs: %w", err)
		}
	}
	refs := make([]Ref, len(ids))
	for i, id := range ids {
		if !known[id] {
			return nil, problem.E(422, CodeUnknownSong, "no such song", map[string]any{"song_id": id})
		}
		refs[i] = Ref{SongID: id}
	}
	return refs, nil
}

// suggestion renders a planned run with titles injected and the pending flag.
func (s *Service) suggestion(ctx context.Context, q Querier, p *Planned) (Suggestion, error) {
	var songIDs, takeIDs []string
	for _, r := range p.Refs {
		songIDs = append(songIDs, r.SongID)
		if r.TakeID != nil {
			takeIDs = append(takeIDs, *r.TakeID)
		}
	}
	songTitles, err := titles(ctx, q, "songs", songIDs)
	if err != nil {
		return Suggestion{}, err
	}
	takeTitles, err := titles(ctx, q, "tracks", takeIDs)
	if err != nil {
		return Suggestion{}, err
	}
	pending, err := pendingFeatures(ctx, q, songIDs)
	if err != nil {
		return Suggestion{}, err
	}
	take := map[string]*string{}
	for _, r := range p.Refs {
		take[r.SongID] = r.TakeID
	}
	res, d := p.Result, p.Doc
	out := Suggestion{Def: Def, Strategy: p.Strategy, InputOrder: d.InputOrder, Order: d.Order, Coverage: res.Coverage, Exact: res.Exact,
		Digest: res.Digest, Pending: pending, Cost: Cost{Input: d.Cost.Input, Output: d.Cost.Output, ImprovementPct: d.Cost.ImprovementPct},
		Warnings: slices.Clone(res.Warnings), Steps: make([]StepView, len(res.Steps))}
	for i, st := range res.Steps {
		v := StepView{Step: st, Text: d.Steps[i].Text, Title: songTitles[st.SongID]}
		if t := take[st.SongID]; t != nil {
			v.TakeTitle = takeTitles[*t]
		}
		out.Steps[i] = v
	}
	if pending {
		out.Warnings = append(out.Warnings, sequencer.Warning{Code: "pending_features"})
	}
	return out, nil
}

// titles reads the title column of table (songs or tracks) for ids.
func titles(ctx context.Context, q Querier, table string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += chunk {
		part := ids[start:min(start+chunk, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT id, title FROM `+table+` WHERE id IN (`+placeholders(len(part))+`)`, args...) //nolint:gosec // G202: fixed table names, "?" placeholders
		if err != nil {
			return nil, fmt.Errorf("sequence: read titles: %w", err)
		}
		for rows.Next() {
			var id, title string
			if err := rows.Scan(&id, &title); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("sequence: read titles: %w", err)
			}
			out[id] = title
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("sequence: read titles: %w", err)
		}
	}
	return out, nil
}

// pendingFeatures reports whether the provenance queue holds a track or draft key of any involved Song: its features may
// not be derived yet (the Reconciler drains the queue).
func pendingFeatures(ctx context.Context, q Querier, songIDs []string) (bool, error) {
	for start := 0; start < len(songIDs); start += chunk {
		part := songIDs[start:min(start+chunk, len(songIDs))]
		args := make([]any, 0, 2*len(part))
		for _, id := range part {
			args = append(args, id)
		}
		args = append(args, args...)
		var p bool
		in := placeholders(len(part))
		err := q.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM provenance_dirty d WHERE d.kind = 'track' AND d.id IN (SELECT ts.track_id FROM take_songs ts WHERE ts.song_id IN (`+in+`)))
			OR EXISTS (SELECT 1 FROM provenance_dirty d WHERE d.kind = 'draft' AND d.id IN (SELECT s.draft_id FROM songs s WHERE s.id IN (`+in+`) AND s.draft_id IS NOT NULL))`, args...).Scan(&p) //nolint:gosec // G202: only "?" placeholders
		if err != nil {
			return false, fmt.Errorf("sequence: read queue: %w", err)
		}
		if p {
			return true, nil
		}
	}
	return false, nil
}
