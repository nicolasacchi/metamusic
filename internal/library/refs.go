package library

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TrackRef is the read model clip, duels, ears and trackctx share (C45).
type TrackRef struct {
	ID, Title, RequestID, RootTrackID, ParentTrackID string // ParentTrackID "" for roots
	ParentOperator                                   string // operator of the track's mutations row; "" for roots
	Seed                                             int64
	DurationS                                        float64
	SampleRate, Channels                             int
	TotalSamples                                     int64
	MasterSHA256, MasterPath                         string // MasterPath is DataDir-relative
	Status                                           string
	RecordedAt                                       time.Time
}

const trackRefSelect = `SELECT t.id, t.title, r.request_id, t.root_track_id, COALESCE(t.parent_track_id, ''), COALESCE(m.operator, ''),
	r.seed, t.duration_s, t.sample_rate, t.channels, t.total_samples, t.master_sha256, t.master_path, t.status, t.recorded_at
	FROM tracks t JOIN recipes r ON r.id = t.recipe_id LEFT JOIN mutations m ON m.child_track_id = t.id`

func scanTrackRef(sc interface{ Scan(...any) error }) (TrackRef, error) {
	var r TrackRef
	var rec string
	if err := sc.Scan(&r.ID, &r.Title, &r.RequestID, &r.RootTrackID, &r.ParentTrackID, &r.ParentOperator,
		&r.Seed, &r.DurationS, &r.SampleRate, &r.Channels, &r.TotalSamples, &r.MasterSHA256, &r.MasterPath, &r.Status, &rec); err != nil {
		return r, err
	}
	r.RecordedAt, _ = time.Parse(time.RFC3339, rec)
	return r, nil
}

// refsChunk keeps the IN list well under SQLite's variable limit.
const refsChunk = 500

// TrackRefs returns the refs of the given ids; unknown ids are absent from the map.
func (s *Service) TrackRefs(ctx context.Context, ids []string) (map[string]TrackRef, error) {
	out := make(map[string]TrackRef, len(ids))
	for start := 0; start < len(ids); start += refsChunk {
		chunk := ids[start:min(start+refsChunk, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.st.R.QueryContext(ctx, trackRefSelect+` WHERE t.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("library: track refs: %w", err)
		}
		for rows.Next() {
			r, err := scanTrackRef(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("library: track refs: %w", err)
			}
			out[r.ID] = r
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LineageTracks returns every track of the lineage rooted at rootID: the root first, then by
// recorded_at, id. ErrNotFound when rootID is not the root of any track.
func (s *Service) LineageTracks(ctx context.Context, rootID string) ([]TrackRef, error) {
	rows, err := s.st.R.QueryContext(ctx, trackRefSelect+` WHERE t.root_track_id = ? ORDER BY (t.id = t.root_track_id) DESC, t.recorded_at, t.id`, rootID)
	if err != nil {
		return nil, fmt.Errorf("library: lineage: %w", err)
	}
	var out []TrackRef
	for rows.Next() {
		r, err := scanTrackRef(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("library: lineage: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}
